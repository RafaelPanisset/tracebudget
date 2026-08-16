package analyze

import (
	"math"
	"sort"

	"github.com/RafaelPanisset/tracebudget/internal/model"
)

// NearestRank returns a nearest-rank percentile without mutating samples.
// Empty input returns zero. Percentiles at or below zero, including NaN,
// select the minimum; percentiles at or above one select the maximum.
func NearestRank(samples []model.Duration, percentile float64) model.Duration {
	if len(samples) == 0 {
		return 0
	}

	sorted := append([]model.Duration(nil), samples...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	switch {
	case math.IsNaN(percentile), percentile <= 0:
		return sorted[0]
	case percentile >= 1:
		return sorted[len(sorted)-1]
	default:
		rank := sort.Search(len(sorted), func(i int) bool {
			return percentile <= float64(i+1)/float64(len(sorted))
		})
		return sorted[rank]
	}
}
