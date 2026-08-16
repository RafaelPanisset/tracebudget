package analyze

import (
	"cmp"
	"sort"

	"github.com/RafaelPanisset/tracebudget/internal/model"
)

// Aggregate combines per-trace summaries into a deterministic observation.
// A node or edge absent from a trace contributes a zero to its count range.
func Aggregate(summaries []TraceSummary, evidence Evidence) Observation {
	nodeKeys := make(map[model.NodeKey]struct{})
	edgeKeys := make(map[model.EdgeKey]struct{})
	for _, summary := range summaries {
		for key := range summary.NodeCounts {
			nodeKeys[key] = struct{}{}
		}
		for key := range summary.ErrorCounts {
			nodeKeys[key] = struct{}{}
		}
		for key := range summary.Durations {
			nodeKeys[key] = struct{}{}
		}
		for key := range summary.EdgeCounts {
			edgeKeys[key] = struct{}{}
		}
	}

	observation := Observation{Evidence: evidence}
	for key := range nodeKeys {
		counts := make([]int, len(summaries))
		errorsTotal := 0
		var durations []model.Duration
		for index, summary := range summaries {
			counts[index] = summary.NodeCounts[key]
			errorsTotal += summary.ErrorCounts[key]
			durations = append(durations, summary.Durations[key]...)
		}
		total := sumInts(counts)
		errorRate := 0.0
		if total > 0 {
			errorRate = float64(errorsTotal) / float64(total)
		}
		observation.Nodes = append(observation.Nodes, NodeObservation{
			Key:       key,
			Counts:    summarizeCounts(counts),
			Total:     total,
			Errors:    errorsTotal,
			ErrorRate: errorRate,
			Durations: DurationSummary{
				Samples: len(durations),
				P50:     NearestRank(durations, 0.50),
				P95:     NearestRank(durations, 0.95),
			},
		})
	}
	for key := range edgeKeys {
		counts := make([]int, len(summaries))
		for index, summary := range summaries {
			counts[index] = summary.EdgeCounts[key]
		}
		observation.Edges = append(observation.Edges, EdgeObservation{
			Key:    key,
			Counts: summarizeCounts(counts),
		})
	}

	sort.Slice(observation.Nodes, func(i, j int) bool {
		return compareNodeKeys(observation.Nodes[i].Key, observation.Nodes[j].Key) < 0
	})
	sort.Slice(observation.Edges, func(i, j int) bool {
		return compareEdgeKeys(observation.Edges[i].Key, observation.Edges[j].Key) < 0
	})
	return observation
}

func summarizeCounts(values []int) CountRange {
	if len(values) == 0 {
		return CountRange{}
	}
	sorted := append([]int(nil), values...)
	sort.Ints(sorted)
	middle := len(sorted) / 2
	median := float64(sorted[middle])
	if len(sorted)%2 == 0 {
		median = (float64(sorted[middle-1]) + float64(sorted[middle])) / 2
	}
	return CountRange{Min: sorted[0], Max: sorted[len(sorted)-1], Median: median}
}

func sumInts(values []int) int {
	total := 0
	for _, value := range values {
		total += value
	}
	return total
}

func compareNodeKeys(left, right model.NodeKey) int {
	return cmp.Or(
		cmp.Compare(left.Service, right.Service),
		cmp.Compare(left.Name, right.Name),
		cmp.Compare(left.Kind, right.Kind),
		cmp.Compare(left.Attributes, right.Attributes),
	)
}

func compareEdgeKeys(left, right model.EdgeKey) int {
	return cmp.Or(
		compareNodeKeys(left.Parent, right.Parent),
		compareNodeKeys(left.Child, right.Child),
	)
}
