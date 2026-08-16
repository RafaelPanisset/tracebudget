package baseline

import (
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/RafaelPanisset/tracebudget/internal/analyze"
	"github.com/RafaelPanisset/tracebudget/internal/model"
)

func TestRepositoryRoundTripMatchesGoldenFile(t *testing.T) {
	document := fixtureDocument()
	path := filepath.Join(t.TempDir(), "checkout.yaml")
	if err := WriteAtomic(path, document, false); err != nil {
		t.Fatal(err)
	}
	actual, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	expected, err := os.ReadFile("testdata/baseline.golden.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmpText(string(expected), string(actual)); diff != "" {
		t.Fatal(diff)
	}
	loaded, err := Load(path)
	if err != nil || !reflect.DeepEqual(loaded, document) {
		t.Fatalf("loaded=%#v err=%v", loaded, err)
	}
}

func TestLoadRejectsUnknownNewerAndMultipleDocuments(t *testing.T) {
	path := writeTemp(t, "schema_version: 2\nscenario: checkout\n")
	if _, err := Load(path); !errors.Is(err, ErrUnsupportedSchema) {
		t.Fatalf("got %v", err)
	}
	path = writeTemp(t, "schema_version: 1\nscenario: checkout\nunknown: true\n")
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "field unknown not found") {
		t.Fatalf("got %v", err)
	}
	path = writeTemp(t, "schema_version: 1\nscenario: checkout\nroot:\n  unknown: true\n")
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "field unknown not found") {
		t.Fatalf("got %v", err)
	}
	path = writeTemp(t, goldenFixture(t)+"---\nschema_version: 1\n")
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "multiple YAML documents") {
		t.Fatalf("got %v", err)
	}
}

func TestLoadRejectsFractionalIntegerFields(t *testing.T) {
	for _, test := range []struct {
		name string
		yaml string
	}{
		{
			name: "schema version",
			yaml: strings.Replace(goldenFixture(t), "schema_version: 1\n", "schema_version: 1.9\n", 1),
		},
		{
			name: "runs",
			yaml: strings.Replace(goldenFixture(t), "runs: 20\n", "runs: 20.5\n", 1),
		},
		{
			name: "expected per run",
			yaml: strings.Replace(goldenFixture(t), "expected_per_run: 1\n", "expected_per_run: 1.5\n", 1),
		},
		{
			name: "max per trace",
			yaml: strings.Replace(
				goldenFixture(t),
				"limitations:\n",
				"budgets:\n  spans:\n    - service: scenario\n      name: checkout\n      max_per_trace: -0.5\nlimitations:\n",
				1,
			),
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := Load(writeTemp(t, test.yaml)); err == nil {
				t.Fatal("Load succeeded")
			}
		})
	}
}

func TestLoadAndWriteRejectInvalidSpanBudgetKind(t *testing.T) {
	invalidKindYAML := strings.Replace(
		goldenFixture(t),
		"limitations:\n",
		"budgets:\n  spans:\n    - service: scenario\n      name: checkout\n      kind: DATABASE\nlimitations:\n",
		1,
	)
	if _, err := Load(writeTemp(t, invalidKindYAML)); err == nil || !strings.Contains(err.Error(), "invalid span budget kind") {
		t.Fatalf("Load error = %v", err)
	}

	document := fixtureDocument()
	document.Budgets.Spans = []SpanBudget{{Service: "scenario", Name: "checkout", Kind: "DATABASE"}}
	err := WriteAtomic(filepath.Join(t.TempDir(), "checkout.yaml"), document, false)
	if err == nil || !strings.Contains(err.Error(), "invalid span budget kind") {
		t.Fatalf("WriteAtomic error = %v", err)
	}
}

func TestWriteAtomicRequiresForceAndLeavesNoTemporaryFile(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "checkout.yaml")
	first := fixtureDocument()
	if err := WriteAtomic(path, first, false); err != nil {
		t.Fatal(err)
	}
	changed := first
	changed.Runs = 30
	if err := WriteAtomic(path, changed, false); !errors.Is(err, ErrBaselineExists) {
		t.Fatalf("got %v", err)
	}
	if err := WriteAtomic(path, changed, true); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Runs != 30 {
		t.Fatalf("runs = %d, want 30", loaded.Runs)
	}
	matches, _ := filepath.Glob(filepath.Join(directory, ".tracebudget-*.tmp"))
	if len(matches) != 0 {
		t.Fatalf("temporary files leaked: %v", matches)
	}
}

func TestWriteAtomicWithoutForceAllowsExactlyOneConcurrentWriter(t *testing.T) {
	path := filepath.Join(t.TempDir(), "checkout.yaml")
	document := fixtureDocument()
	const writers = 24

	results := make(chan error, writers)
	var group sync.WaitGroup
	for range writers {
		group.Add(1)
		go func() {
			defer group.Done()
			results <- WriteAtomic(path, document, false)
		}()
	}
	group.Wait()
	close(results)

	successes := 0
	for err := range results {
		if err == nil {
			successes++
			continue
		}
		if !errors.Is(err, ErrBaselineExists) {
			t.Fatalf("got %v", err)
		}
	}
	if successes != 1 {
		t.Fatalf("successful writes = %d, want 1", successes)
	}
}

func TestWriteAtomicRejectsNaNRates(t *testing.T) {
	t.Run("incomplete trace rate", func(t *testing.T) {
		document := fixtureDocument()
		document.Policies.IncompleteTraceRate.Max = math.NaN()
		err := WriteAtomic(filepath.Join(t.TempDir(), "checkout.yaml"), document, false)
		if err == nil || !strings.Contains(err.Error(), "incomplete trace rate") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("max error rate", func(t *testing.T) {
		document := fixtureDocument()
		rate := math.NaN()
		document.Budgets.MaxErrorRate = &rate
		err := WriteAtomic(filepath.Join(t.TempDir(), "checkout.yaml"), document, false)
		if err == nil || !strings.Contains(err.Error(), "max_error_rate") {
			t.Fatalf("got %v", err)
		}
	})
}

func TestWriteAtomicReportsInvalidSeverityInDocumentedOrder(t *testing.T) {
	document := fixtureDocument()
	document.Policies.NewServiceEdge = "invalid-service-edge"
	document.Policies.NewExternalDependency = "invalid-external-dependency"

	err := WriteAtomic(filepath.Join(t.TempDir(), "checkout.yaml"), document, false)
	if err == nil || !strings.Contains(err.Error(), `invalid severity for new_service_edge: "invalid-service-edge"`) {
		t.Fatalf("got %v", err)
	}
}

func TestDefaultDocumentOwnsObservedSlices(t *testing.T) {
	observed := analyze.Observation{
		Nodes: []analyze.NodeObservation{{Total: 1}},
		Edges: []analyze.EdgeObservation{{Counts: analyze.CountRange{Max: 1}}},
	}
	document := DefaultDocument("checkout", 1, model.RootSelector{Service: "scenario", Span: "checkout", ExpectedPerRun: 1}, observed)

	observed.Nodes[0].Total = 2
	observed.Nodes = append(observed.Nodes, analyze.NodeObservation{Total: 3})
	observed.Edges[0].Counts.Max = 2
	observed.Edges = append(observed.Edges, analyze.EdgeObservation{Counts: analyze.CountRange{Max: 3}})

	if got := document.Observed.Nodes; len(got) != 1 || got[0].Total != 1 {
		t.Fatalf("nodes = %#v", got)
	}
	if got := document.Observed.Edges; len(got) != 1 || got[0].Counts.Max != 1 {
		t.Fatalf("edges = %#v", got)
	}
}

func fixtureDocument() Document {
	root := model.RootSelector{Service: "scenario", Span: "checkout", ExpectedPerRun: 1}
	observed := analyze.Observation{Evidence: analyze.Evidence{Complete: 20}}
	return DefaultDocument("checkout", 20, root, observed)
}

func writeTemp(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "baseline.yaml")
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func goldenFixture(t *testing.T) string {
	t.Helper()
	contents, err := os.ReadFile("testdata/baseline.golden.yaml")
	if err != nil {
		t.Fatal(err)
	}
	return string(contents)
}

func cmpText(expected, actual string) string {
	if expected == actual {
		return ""
	}
	return fmt.Sprintf("expected:\n%s\nactual:\n%s", expected, actual)
}
