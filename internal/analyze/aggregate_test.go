package analyze

import (
	"slices"
	"testing"

	"github.com/RafaelPanisset/tracebudget/internal/model"
)

func TestAggregateIncludesMissingNodesAndEdgesAsZeroPerTrace(t *testing.T) {
	client := model.NodeKey{Service: "gateway", Name: "inventory.call", Kind: model.SpanKindClient}
	server := model.NodeKey{Service: "gateway", Name: "checkout", Kind: model.SpanKindServer}
	edge := model.EdgeKey{Parent: server, Child: client}
	summaries := []TraceSummary{
		{
			NodeCounts:  map[model.NodeKey]int{client: 4, server: 1},
			EdgeCounts:  map[model.EdgeKey]int{edge: 4},
			Durations:   map[model.NodeKey][]model.Duration{client: {40, 10}},
			ErrorCounts: map[model.NodeKey]int{client: 1},
		},
		{
			NodeCounts:  map[model.NodeKey]int{server: 1},
			EdgeCounts:  map[model.EdgeKey]int{},
			Durations:   map[model.NodeKey][]model.Duration{},
			ErrorCounts: map[model.NodeKey]int{},
		},
		{
			NodeCounts:  map[model.NodeKey]int{client: 2, server: 1},
			EdgeCounts:  map[model.EdgeKey]int{edge: 2},
			Durations:   map[model.NodeKey][]model.Duration{client: {30}},
			ErrorCounts: map[model.NodeKey]int{client: 1},
		},
		{
			NodeCounts:  map[model.NodeKey]int{server: 1},
			EdgeCounts:  map[model.EdgeKey]int{},
			Durations:   map[model.NodeKey][]model.Duration{},
			ErrorCounts: map[model.NodeKey]int{},
		},
	}

	observation := Aggregate(summaries, Evidence{Complete: 4, Incomplete: 2, Unmatched: 1})
	clientObservation, ok := findNodeObservation(observation.Nodes, client)
	if !ok {
		t.Fatalf("client observation missing: %#v", observation.Nodes)
	}
	if clientObservation.Counts != (CountRange{Min: 0, Max: 4, Median: 1}) {
		t.Fatalf("counts=%#v, want min=0 max=4 median=1", clientObservation.Counts)
	}
	if clientObservation.Total != 6 || clientObservation.Errors != 2 || clientObservation.ErrorRate != 1.0/3.0 {
		t.Fatalf("unexpected totals/errors: %#v", clientObservation)
	}
	if clientObservation.Durations != (DurationSummary{Samples: 3, P50: 30, P95: 40}) {
		t.Fatalf("durations=%#v", clientObservation.Durations)
	}
	edgeObservation, ok := findEdgeObservation(observation.Edges, edge)
	if !ok {
		t.Fatalf("edge observation missing: %#v", observation.Edges)
	}
	if edgeObservation.Counts != (CountRange{Min: 0, Max: 4, Median: 1}) {
		t.Fatalf("edge counts=%#v, want min=0 max=4 median=1", edgeObservation.Counts)
	}
	if observation.Evidence != (Evidence{Complete: 4, Incomplete: 2, Unmatched: 1}) {
		t.Fatalf("evidence=%#v", observation.Evidence)
	}
}

func TestAggregateSortsDistinctKeysWithoutStringCollisions(t *testing.T) {
	first := model.NodeKey{Service: "a", Name: "b/c", Kind: model.SpanKindClient}
	second := model.NodeKey{Service: "a/b", Name: "c", Kind: model.SpanKindClient}
	if first.String() != second.String() {
		t.Fatalf("fixture must collide through String: %q != %q", first.String(), second.String())
	}
	child := model.NodeKey{Service: "z", Name: "child", Kind: model.SpanKindServer}
	firstEdge := model.EdgeKey{Parent: first, Child: child}
	secondEdge := model.EdgeKey{Parent: second, Child: child}
	summary := TraceSummary{
		NodeCounts:  map[model.NodeKey]int{second: 1, child: 1, first: 1},
		EdgeCounts:  map[model.EdgeKey]int{secondEdge: 1, firstEdge: 1},
		Durations:   map[model.NodeKey][]model.Duration{},
		ErrorCounts: map[model.NodeKey]int{},
	}

	for iteration := 0; iteration < 20; iteration++ {
		observation := Aggregate([]TraceSummary{summary}, Evidence{Complete: 1})
		wantNodes := []model.NodeKey{first, second, child}
		gotNodes := make([]model.NodeKey, len(observation.Nodes))
		for index, node := range observation.Nodes {
			gotNodes[index] = node.Key
		}
		if !slices.Equal(gotNodes, wantNodes) {
			t.Fatalf("iteration %d: nodes=%#v, want %#v", iteration, gotNodes, wantNodes)
		}
		wantEdges := []model.EdgeKey{firstEdge, secondEdge}
		gotEdges := make([]model.EdgeKey, len(observation.Edges))
		for index, observedEdge := range observation.Edges {
			gotEdges[index] = observedEdge.Key
		}
		if !slices.Equal(gotEdges, wantEdges) {
			t.Fatalf("iteration %d: edges=%#v, want %#v", iteration, gotEdges, wantEdges)
		}
	}
}

func TestAggregateDoesNotAliasOrMutateSummaries(t *testing.T) {
	key := model.NodeKey{Service: "service", Name: "operation", Kind: model.SpanKindInternal}
	durations := []model.Duration{30, 10, 20}
	wantDurations := slices.Clone(durations)
	summaries := []TraceSummary{{
		NodeCounts:  map[model.NodeKey]int{key: 3},
		EdgeCounts:  map[model.EdgeKey]int{},
		Durations:   map[model.NodeKey][]model.Duration{key: durations},
		ErrorCounts: map[model.NodeKey]int{},
	}}

	observation := Aggregate(summaries, Evidence{Complete: 1})
	if !slices.Equal(durations, wantDurations) {
		t.Fatalf("durations mutated: got %v, want %v", durations, wantDurations)
	}

	observation.Nodes[0].Key.Name = "changed"
	observation.Nodes[0].Total = 99
	if summaries[0].NodeCounts[key] != 3 || !slices.Equal(summaries[0].Durations[key], wantDurations) {
		t.Fatalf("observation aliases summaries: %#v", summaries)
	}
}

func TestAggregateEmptyInputPreservesEvidence(t *testing.T) {
	want := Evidence{Incomplete: 2, Unmatched: 3}
	observation := Aggregate(nil, want)
	if len(observation.Nodes) != 0 || len(observation.Edges) != 0 || observation.Evidence != want {
		t.Fatalf("unexpected observation: %#v", observation)
	}
}

func findNodeObservation(nodes []NodeObservation, key model.NodeKey) (NodeObservation, bool) {
	for _, node := range nodes {
		if node.Key == key {
			return node, true
		}
	}
	return NodeObservation{}, false
}

func findEdgeObservation(edges []EdgeObservation, key model.EdgeKey) (EdgeObservation, bool) {
	for _, edge := range edges {
		if edge.Key == key {
			return edge, true
		}
	}
	return EdgeObservation{}, false
}
