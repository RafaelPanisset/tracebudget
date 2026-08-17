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
		{"new producer dependency", addProducerNode, "new_external_dependency", model.SeverityFail},
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

func TestCompareNewFailingInternalNodeFailsUnderDefaultPolicies(t *testing.T) {
	document := fixtureBaseline()
	candidate := cloneObservation(document.Observed)
	addInternalNode(&candidate)
	candidate.Nodes[len(candidate.Nodes)-1].Errors = 1

	result, err := Compare(document, candidate)
	if err != nil {
		t.Fatal(err)
	}
	requireFinding(t, result, "new_internal_node")
	requireFinding(t, result, "new_error")
	if result.Outcome != model.OutcomeFail {
		t.Fatalf("outcome = %s, want %s", result.Outcome, model.OutcomeFail)
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

func TestCompareCalculatesIncompleteTraceRateWithoutIntegerOverflow(t *testing.T) {
	document := fixtureBaseline()
	candidate := cloneObservation(document.Observed)
	candidate.Evidence = analyze.Evidence{Complete: math.MaxInt, Incomplete: math.MaxInt}

	result, err := Compare(document, candidate)
	if err != nil {
		t.Fatal(err)
	}
	requireFinding(t, result, "incomplete_trace_rate")
}

func TestCompareCalculatesExactErrorRateIndependentlyOfNodeOrder(t *testing.T) {
	document := fixtureBaseline()
	document.Policies.NewInternalNode = model.SeverityIgnore
	limit := math.Nextafter(0.5, 0)
	document.Budgets.MaxErrorRate = &limit
	large := 1 << 53
	nodes := []analyze.NodeObservation{
		{Key: document.Observed.Nodes[0].Key, Total: large, Errors: large / 2},
		{Key: document.Observed.Nodes[1].Key, Total: 1, Errors: 1},
		{Key: model.NodeKey{Service: "gateway", Name: "validate", Kind: model.SpanKindInternal}, Total: 2},
	}
	orders := [][]int{{0, 1, 2}, {1, 2, 0}, {2, 0, 1}}
	var first Result
	hasFirst := false
	for iteration := 0; iteration < 30; iteration++ {
		for _, order := range orders {
			candidate := cloneObservation(document.Observed)
			candidate.Nodes = []analyze.NodeObservation{nodes[order[0]], nodes[order[1]], nodes[order[2]]}
			result, err := Compare(document, candidate)
			if err != nil {
				t.Fatal(err)
			}
			requireFinding(t, result, "error_rate_exceeded")
			if !hasFirst {
				first = result
				hasFirst = true
				continue
			}
			if !reflect.DeepEqual(first, result) {
				t.Fatalf("results differ for equivalent order %v:\n%#v\n%#v", order, first, result)
			}
		}
	}
}

func TestCompareRejectsDuplicateObservations(t *testing.T) {
	tests := []struct {
		name       string
		mutate     func(*baseline.Document, *analyze.Observation)
		wantString string
	}{
		{
			name: "baseline node",
			mutate: func(document *baseline.Document, _ *analyze.Observation) {
				document.Observed.Nodes = append(document.Observed.Nodes, document.Observed.Nodes[0])
			},
			wantString: "duplicate node observation in baseline: gateway/checkout/SERVER",
		},
		{
			name: "candidate node",
			mutate: func(_ *baseline.Document, candidate *analyze.Observation) {
				candidate.Nodes = append(candidate.Nodes, candidate.Nodes[0])
			},
			wantString: "duplicate node observation in candidate: gateway/checkout/SERVER",
		},
		{
			name: "baseline edge",
			mutate: func(document *baseline.Document, _ *analyze.Observation) {
				document.Observed.Edges = append(document.Observed.Edges, document.Observed.Edges[0])
			},
			wantString: "duplicate edge observation in baseline: gateway/checkout/SERVER -> gateway/db.query/CLIENT",
		},
		{
			name: "candidate edge",
			mutate: func(_ *baseline.Document, candidate *analyze.Observation) {
				candidate.Edges = append(candidate.Edges, candidate.Edges[0])
			},
			wantString: "duplicate edge observation in candidate: gateway/checkout/SERVER -> gateway/db.query/CLIENT",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			document := fixtureBaseline()
			candidate := cloneObservation(document.Observed)
			test.mutate(&document, &candidate)

			_, err := Compare(document, candidate)
			if err == nil {
				t.Fatal("Compare succeeded")
			}
			if err.Error() != test.wantString {
				t.Fatalf("error = %q, want %q", err, test.wantString)
			}
		})
	}
}

func TestCompareRejectsMalformedObservations(t *testing.T) {
	tests := []struct {
		name       string
		mutate     func(*baseline.Document, *analyze.Observation)
		wantString string
	}{
		{
			name: "baseline negative complete evidence",
			mutate: func(document *baseline.Document, _ *analyze.Observation) {
				document.Observed.Evidence.Complete = -1
			},
			wantString: "invalid baseline evidence complete: must be nonnegative",
		},
		{
			name: "candidate negative incomplete evidence",
			mutate: func(_ *baseline.Document, candidate *analyze.Observation) {
				candidate.Evidence.Incomplete = -1
			},
			wantString: "invalid candidate evidence incomplete: must be nonnegative",
		},
		{
			name: "candidate negative unmatched evidence",
			mutate: func(_ *baseline.Document, candidate *analyze.Observation) {
				candidate.Evidence.Unmatched = -1
			},
			wantString: "invalid candidate evidence unmatched: must be nonnegative",
		},
		{
			name: "node count min exceeds max",
			mutate: func(_ *baseline.Document, candidate *analyze.Observation) {
				candidate.Nodes[0].Counts = analyze.CountRange{Min: 2, Max: 1, Median: 1}
			},
			wantString: "invalid candidate node gateway/checkout/SERVER counts: min exceeds max",
		},
		{
			name: "node count negative min",
			mutate: func(_ *baseline.Document, candidate *analyze.Observation) {
				candidate.Nodes[0].Counts.Min = -1
			},
			wantString: "invalid candidate node gateway/checkout/SERVER counts: min must be nonnegative",
		},
		{
			name: "node count median is not finite",
			mutate: func(_ *baseline.Document, candidate *analyze.Observation) {
				candidate.Nodes[0].Counts.Median = math.NaN()
			},
			wantString: "invalid candidate node gateway/checkout/SERVER counts: median must be finite",
		},
		{
			name: "node count median outside range",
			mutate: func(_ *baseline.Document, candidate *analyze.Observation) {
				candidate.Nodes[0].Counts.Median = 2
			},
			wantString: "invalid candidate node gateway/checkout/SERVER counts: median must be within min and max",
		},
		{
			name: "edge count min exceeds max",
			mutate: func(_ *baseline.Document, candidate *analyze.Observation) {
				candidate.Edges[0].Counts = analyze.CountRange{Min: 2, Max: 1, Median: 1}
			},
			wantString: "invalid candidate edge gateway/checkout/SERVER -> gateway/db.query/CLIENT counts: min exceeds max",
		},
		{
			name: "edge count negative max",
			mutate: func(_ *baseline.Document, candidate *analyze.Observation) {
				candidate.Edges[0].Counts.Max = -1
			},
			wantString: "invalid candidate edge gateway/checkout/SERVER -> gateway/db.query/CLIENT counts: max must be nonnegative",
		},
		{
			name: "edge count median is not finite",
			mutate: func(_ *baseline.Document, candidate *analyze.Observation) {
				candidate.Edges[0].Counts.Median = math.Inf(1)
			},
			wantString: "invalid candidate edge gateway/checkout/SERVER -> gateway/db.query/CLIENT counts: median must be finite",
		},
		{
			name: "edge count median outside range",
			mutate: func(_ *baseline.Document, candidate *analyze.Observation) {
				candidate.Edges[0].Counts.Median = 2
			},
			wantString: "invalid candidate edge gateway/checkout/SERVER -> gateway/db.query/CLIENT counts: median must be within min and max",
		},
		{
			name: "node negative total",
			mutate: func(_ *baseline.Document, candidate *analyze.Observation) {
				candidate.Nodes[0].Total = -1
			},
			wantString: "invalid candidate node gateway/checkout/SERVER total: must be nonnegative",
		},
		{
			name: "node negative errors",
			mutate: func(_ *baseline.Document, candidate *analyze.Observation) {
				candidate.Nodes[0].Errors = -1
			},
			wantString: "invalid candidate node gateway/checkout/SERVER errors: must be nonnegative",
		},
		{
			name: "node negative samples",
			mutate: func(_ *baseline.Document, candidate *analyze.Observation) {
				candidate.Nodes[0].Durations.Samples = -1
			},
			wantString: "invalid candidate node gateway/checkout/SERVER duration samples: must be nonnegative",
		},
		{
			name: "node negative p50",
			mutate: func(_ *baseline.Document, candidate *analyze.Observation) {
				candidate.Nodes[0].Durations.P50 = -1
			},
			wantString: "invalid candidate node gateway/checkout/SERVER duration p50: must be nonnegative",
		},
		{
			name: "node negative p95",
			mutate: func(_ *baseline.Document, candidate *analyze.Observation) {
				candidate.Nodes[0].Durations.P95 = -1
			},
			wantString: "invalid candidate node gateway/checkout/SERVER duration p95: must be nonnegative",
		},
		{
			name: "node errors exceed total",
			mutate: func(_ *baseline.Document, candidate *analyze.Observation) {
				candidate.Nodes[0].Errors = candidate.Nodes[0].Total + 1
			},
			wantString: "invalid candidate node gateway/checkout/SERVER errors: must not exceed total",
		},
		{
			name: "node samples exceed total",
			mutate: func(_ *baseline.Document, candidate *analyze.Observation) {
				candidate.Nodes[0].Durations.Samples = candidate.Nodes[0].Total + 1
			},
			wantString: "invalid candidate node gateway/checkout/SERVER duration samples: must not exceed total",
		},
		{
			name: "node p50 exceeds p95",
			mutate: func(_ *baseline.Document, candidate *analyze.Observation) {
				candidate.Nodes[0].Durations.P50 = candidate.Nodes[0].Durations.P95 + 1
			},
			wantString: "invalid candidate node gateway/checkout/SERVER durations: p50 must not exceed p95",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			document := fixtureBaseline()
			candidate := cloneObservation(document.Observed)
			test.mutate(&document, &candidate)

			_, err := Compare(document, candidate)
			if err == nil {
				t.Fatal("Compare succeeded")
			}
			if err.Error() != test.wantString {
				t.Fatalf("error = %q, want %q", err, test.wantString)
			}
		})
	}
}

func TestCompareSelectsKindlessP95BudgetTargetDeterministically(t *testing.T) {
	internal := model.NodeKey{Service: "gateway", Name: "shared", Kind: model.SpanKindInternal}
	server := model.NodeKey{Service: "gateway", Name: "shared", Kind: model.SpanKindServer}
	limit := model.Duration(100 * time.Millisecond)
	document := fixtureBaseline()
	document.Observed.Nodes = []analyze.NodeObservation{
		{Key: server, Total: 20, Durations: analyze.DurationSummary{Samples: 19, P95: limit}},
		{Key: internal, Total: 20, Durations: analyze.DurationSummary{Samples: 18, P95: limit}},
	}
	document.Observed.Edges = nil
	document.Budgets.Spans = []baseline.SpanBudget{{Service: "gateway", Name: "shared", P95: &limit}}
	orders := [][]int{{0, 1}, {1, 0}}
	const want = "insufficient complete samples for blocking p95 budget: gateway/shared/INTERNAL has 18"
	for iteration := 0; iteration < 30; iteration++ {
		for _, order := range orders {
			candidate := cloneObservation(document.Observed)
			candidate.Nodes = []analyze.NodeObservation{document.Observed.Nodes[order[0]], document.Observed.Nodes[order[1]]}
			_, err := Compare(document, candidate)
			if err == nil || err.Error() != want {
				t.Fatalf("order %v: error = %v, want %q", order, err, want)
			}
		}
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
	documentBefore.Observed.Nodes = append([]analyze.NodeObservation(nil), document.Observed.Nodes...)
	documentBefore.Observed.Edges = append([]analyze.EdgeObservation(nil), document.Observed.Edges...)
	candidateBefore := cloneObservation(candidate)

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

func addProducerNode(observation *analyze.Observation) {
	observation.Nodes = append(observation.Nodes, analyze.NodeObservation{
		Key:    model.NodeKey{Service: "gateway", Name: "orders.publish", Kind: model.SpanKindProducer},
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
