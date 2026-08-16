package compare

import (
	"errors"
	"math"
	"reflect"
	"testing"
	"time"

	"github.com/RafaelPanisset/tracebudget/internal/analyze"
	"github.com/RafaelPanisset/tracebudget/internal/baseline"
	"github.com/RafaelPanisset/tracebudget/internal/model"
)

func TestCompareDefaultPolicies(t *testing.T) {
	baselineDocument := fixtureBaseline()
	tests := []struct {
		name     string
		mutate   func(*analyze.Observation)
		code     string
		severity model.Severity
	}{
		{"new error", addNewError, "new_error", model.SeverityFail},
		{"new cross service edge", addCrossServiceEdge, "new_service_edge", model.SeverityFail},
		{"new client dependency", addClientNode, "new_external_dependency", model.SeverityFail},
		{"new internal span", addInternalNode, "new_internal_node", model.SeverityWarn},
		{"count increase", increaseNodeMaximum, "count_increase", model.SeverityFail},
		{"latency increase", increaseP95, "latency_increase", model.SeverityWarn},
		{"removed node", removeObservedNode, "removed_node", model.SeverityWarn},
		{"removed edge", removeObservedEdge, "removed_edge", model.SeverityWarn},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := cloneObservation(baselineDocument.Observed)
			test.mutate(&candidate)
			result, err := Compare(baselineDocument, candidate)
			if err != nil {
				t.Fatal(err)
			}
			finding := requireFinding(t, result, test.code)
			if finding.Severity != test.severity {
				t.Fatalf("severity = %s, want %s", finding.Severity, test.severity)
			}
		})
	}
}

func TestCompareIgnoresConfiguredPolicy(t *testing.T) {
	document := fixtureBaseline()
	document.Policies.NewError = model.SeverityIgnore
	candidate := cloneObservation(document.Observed)
	addNewError(&candidate)

	result, err := Compare(document, candidate)
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != model.OutcomePass || len(result.Findings) != 0 {
		t.Fatalf("result = %#v, want pass without findings", result)
	}
}

func TestCompareRejectsNoComparableTraces(t *testing.T) {
	document := fixtureBaseline()
	candidate := cloneObservation(document.Observed)
	candidate.Evidence = analyze.Evidence{Incomplete: 20}
	addNewError(&candidate)

	result, err := Compare(document, candidate)
	if !errors.Is(err, ErrNoComparableTraces) {
		t.Fatalf("error = %v, want ErrNoComparableTraces", err)
	}
	if result.Outcome != "" || len(result.Findings) != 0 {
		t.Fatalf("result = %#v, want no policy findings", result)
	}
}

func TestCompareFailsExcessiveIncompleteTraceRate(t *testing.T) {
	document := fixtureBaseline()
	candidate := cloneObservation(document.Observed)
	candidate.Evidence.Incomplete = 1

	result, err := Compare(document, candidate)
	if err != nil {
		t.Fatal(err)
	}
	requireFinding(t, result, "incomplete_trace_rate")
	if result.Outcome != model.OutcomeFail {
		t.Fatalf("outcome = %s, want %s", result.Outcome, model.OutcomeFail)
	}
}

func TestCompareRequiresTwentySamplesForBlockingP95(t *testing.T) {
	document := fixtureBaselineWithP95Budget(600 * time.Millisecond)
	candidate := cloneObservation(document.Observed)
	candidate.Nodes[0].Durations.Samples = 19
	if _, err := Compare(document, candidate); !errors.Is(err, ErrInsufficientSamples) {
		t.Fatalf("error = %v, want ErrInsufficientSamples", err)
	}
}

func TestCompareTreatsMissingP95BudgetTargetAsInsufficientSamples(t *testing.T) {
	document := fixtureBaselineWithP95Budget(600 * time.Millisecond)
	document.Budgets.Spans[0].Name = "not-observed"

	if _, err := Compare(document, cloneObservation(document.Observed)); !errors.Is(err, ErrInsufficientSamples) {
		t.Fatalf("error = %v, want ErrInsufficientSamples", err)
	}
}

func TestCompareFailsExplicitP95Budget(t *testing.T) {
	document := fixtureBaselineWithP95Budget(600 * time.Millisecond)
	candidate := cloneObservation(document.Observed)
	candidate.Nodes[0].Durations.Samples = 20
	candidate.Nodes[0].Durations.P95 = model.Duration(601 * time.Millisecond)

	result, err := Compare(document, candidate)
	if err != nil {
		t.Fatal(err)
	}
	requireFinding(t, result, "p95_budget_exceeded")
	if result.Outcome != model.OutcomeFail {
		t.Fatalf("outcome = %s, want %s", result.Outcome, model.OutcomeFail)
	}
}

func TestCompareFailsExplicitCountAndErrorRateBudgets(t *testing.T) {
	document := fixtureBaseline()
	maxPerTrace := 1
	maxErrorRate := 0.01
	document.Budgets.Spans = []baseline.SpanBudget{{
		Service: "gateway", Name: "checkout", Kind: model.SpanKindServer, MaxPerTrace: &maxPerTrace,
	}}
	document.Budgets.MaxErrorRate = &maxErrorRate
	candidate := cloneObservation(document.Observed)
	candidate.Nodes[0].Counts.Max = 2
	candidate.Nodes[0].Errors = 1

	result, err := Compare(document, candidate)
	if err != nil {
		t.Fatal(err)
	}
	requireFinding(t, result, "max_per_trace_exceeded")
	requireFinding(t, result, "error_rate_exceeded")
	if result.Outcome != model.OutcomeFail {
		t.Fatalf("outcome = %s, want %s", result.Outcome, model.OutcomeFail)
	}
}

func TestCompareCalculatesErrorRateWithoutIntegerOverflow(t *testing.T) {
	document := fixtureBaseline()
	maxErrorRate := 0.75
	document.Budgets.MaxErrorRate = &maxErrorRate
	candidate := cloneObservation(document.Observed)
	for index := range candidate.Nodes {
		candidate.Nodes[index].Total = math.MaxInt
		candidate.Nodes[index].Errors = math.MaxInt
	}

	result, err := Compare(document, candidate)
	if err != nil {
		t.Fatal(err)
	}
	requireFinding(t, result, "error_rate_exceeded")
}

func TestCompareNoChangePassesWithoutFindings(t *testing.T) {
	document := fixtureBaseline()
	result, err := Compare(document, cloneObservation(document.Observed))
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != model.OutcomePass || len(result.Findings) != 0 {
		t.Fatalf("unexpected result: %#v", result)
	}
}

func TestCompareFindingOrderIsStable(t *testing.T) {
	document := fixtureBaseline()
	candidate := observationWithMultipleRegressions(document.Observed)
	first, err := Compare(document, candidate)
	if err != nil {
		t.Fatal(err)
	}
	wantCodes := []string{"new_error", "new_external_dependency", "removed_edge"}
	if got := findingCodes(first.Findings); !reflect.DeepEqual(got, wantCodes) {
		t.Fatalf("codes = %v, want %v", got, wantCodes)
	}
	for iteration := 0; iteration < 50; iteration++ {
		result, err := Compare(document, candidate)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(first, result) {
			t.Fatalf("iteration %d: results differ:\n%#v\n%#v", iteration, first, result)
		}
	}
}

func TestCompareDoesNotMutateInputs(t *testing.T) {
	document := fixtureBaseline()
	candidate := observationWithMultipleRegressions(document.Observed)
	documentBefore := document
	candidateBefore := candidate

	if _, err := Compare(document, candidate); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(document, documentBefore) {
		t.Fatalf("document mutated: got %#v, want %#v", document, documentBefore)
	}
	if !reflect.DeepEqual(candidate, candidateBefore) {
		t.Fatalf("candidate mutated: got %#v, want %#v", candidate, candidateBefore)
	}
}

func fixtureBaseline() baseline.Document {
	server := model.NodeKey{Service: "gateway", Name: "checkout", Kind: model.SpanKindServer}
	client := model.NodeKey{Service: "gateway", Name: "db.query", Kind: model.SpanKindClient}
	observed := analyze.Observation{
		Nodes: []analyze.NodeObservation{
			{Key: server, Counts: analyze.CountRange{Min: 1, Max: 1, Median: 1}, Total: 20, Durations: analyze.DurationSummary{Samples: 20, P95: model.Duration(100 * time.Millisecond)}},
			{Key: client, Counts: analyze.CountRange{Min: 1, Max: 1, Median: 1}, Total: 20, Durations: analyze.DurationSummary{Samples: 20, P95: model.Duration(50 * time.Millisecond)}},
		},
		Edges: []analyze.EdgeObservation{{
			Key:    model.EdgeKey{Parent: server, Child: client},
			Counts: analyze.CountRange{Min: 1, Max: 1, Median: 1},
		}},
		Evidence: analyze.Evidence{Complete: 20},
	}
	return baseline.DefaultDocument("checkout", 20, model.RootSelector{Service: "gateway", Span: "checkout", ExpectedPerRun: 1}, observed)
}

func fixtureBaselineWithP95Budget(limit time.Duration) baseline.Document {
	document := fixtureBaseline()
	value := model.Duration(limit)
	document.Budgets.Spans = []baseline.SpanBudget{{Service: "gateway", Name: "checkout", Kind: model.SpanKindServer, P95: &value}}
	return document
}

func cloneObservation(input analyze.Observation) analyze.Observation {
	output := input
	output.Nodes = append([]analyze.NodeObservation(nil), input.Nodes...)
	output.Edges = append([]analyze.EdgeObservation(nil), input.Edges...)
	return output
}

func addNewError(observation *analyze.Observation) { observation.Nodes[0].Errors = 1 }

func addCrossServiceEdge(observation *analyze.Observation) {
	observation.Edges = append(observation.Edges, analyze.EdgeObservation{
		Key: model.EdgeKey{
			Parent: observation.Nodes[0].Key,
			Child:  model.NodeKey{Service: "inventory", Name: "reserve", Kind: model.SpanKindServer},
		},
		Counts: analyze.CountRange{Min: 1, Max: 1, Median: 1},
	})
}

func addClientNode(observation *analyze.Observation) {
	observation.Nodes = append(observation.Nodes, analyze.NodeObservation{
		Key:    model.NodeKey{Service: "gateway", Name: "inventory.price", Kind: model.SpanKindClient},
		Counts: analyze.CountRange{Min: 1, Max: 1, Median: 1}, Total: 20,
	})
}

func addInternalNode(observation *analyze.Observation) {
	observation.Nodes = append(observation.Nodes, analyze.NodeObservation{
		Key:    model.NodeKey{Service: "gateway", Name: "validate", Kind: model.SpanKindInternal},
		Counts: analyze.CountRange{Min: 1, Max: 1, Median: 1}, Total: 20,
	})
}

func increaseNodeMaximum(observation *analyze.Observation) { observation.Nodes[0].Counts.Max = 2 }
func increaseP95(observation *analyze.Observation)         { observation.Nodes[0].Durations.P95++ }
func removeObservedNode(observation *analyze.Observation)  { observation.Nodes = observation.Nodes[1:] }
func removeObservedEdge(observation *analyze.Observation)  { observation.Edges = nil }

func requireFinding(t *testing.T, result Result, code string) model.Finding {
	t.Helper()
	for _, finding := range result.Findings {
		if finding.Code == code {
			return finding
		}
	}
	t.Fatalf("finding %q missing from %#v", code, result.Findings)
	return model.Finding{}
}

func observationWithMultipleRegressions(input analyze.Observation) analyze.Observation {
	output := cloneObservation(input)
	addNewError(&output)
	addClientNode(&output)
	removeObservedEdge(&output)
	return output
}

func findingCodes(findings []model.Finding) []string {
	codes := make([]string, len(findings))
	for index, finding := range findings {
		codes[index] = finding.Code
	}
	return codes
}
