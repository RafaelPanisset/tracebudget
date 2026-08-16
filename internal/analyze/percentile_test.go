package analyze

import (
	"math"
	"slices"
	"testing"

	"github.com/RafaelPanisset/tracebudget/internal/model"
)

func TestNearestRankUsesNearestRankDefinition(t *testing.T) {
	samples := []model.Duration{100, 10, 90, 20, 80, 30, 70, 40, 60, 50}
	if got := NearestRank(samples, 0.50); got != 50 {
		t.Fatalf("p50=%d, want 50", got)
	}
	if got := NearestRank(samples, 0.95); got != 100 {
		t.Fatalf("p95=%d, want 100", got)
	}
}

func TestNearestRankKeepsExactDecimalBoundary(t *testing.T) {
	samples := make([]model.Duration, 100)
	for i := range samples {
		samples[i] = model.Duration(i + 1)
	}

	if got := NearestRank(samples, 0.07); got != 7 {
		t.Fatalf("p07=%d, want 7", got)
	}
}

func TestNearestRankDoesNotMutateSamples(t *testing.T) {
	samples := []model.Duration{30, 10, 20}
	want := slices.Clone(samples)

	_ = NearestRank(samples, 0.50)

	if !slices.Equal(samples, want) {
		t.Fatalf("samples mutated: got %v, want %v", samples, want)
	}
}

func TestNearestRankIsTotalAtBoundaries(t *testing.T) {
	samples := []model.Duration{30, 10, 20}
	tests := []struct {
		name       string
		percentile float64
		want       model.Duration
	}{
		{name: "negative", percentile: -1, want: 10},
		{name: "zero", percentile: 0, want: 10},
		{name: "NaN", percentile: math.NaN(), want: 10},
		{name: "positive infinity", percentile: math.Inf(1), want: 30},
		{name: "above one", percentile: 2, want: 30},
	}

	if got := NearestRank(nil, 2); got != 0 {
		t.Fatalf("empty samples=%d, want 0", got)
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := NearestRank(samples, test.percentile); got != test.want {
				t.Fatalf("NearestRank(%v, %v)=%d, want %d", samples, test.percentile, got, test.want)
			}
		})
	}
}
