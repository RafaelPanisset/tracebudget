// Package analyze normalizes complete traces into deterministic structural observations.
package analyze

import "github.com/RafaelPanisset/tracebudget/internal/model"

// TraceSummary contains the normalized behavior of one complete trace.
// TraceID and RunID are correlation metadata and are explicitly excluded from
// serialized output; aggregate observations do not retain either identity.
type TraceSummary struct {
	TraceID     string `yaml:"-" json:"-"`
	RunID       string `yaml:"-" json:"-"`
	NodeCounts  map[model.NodeKey]int
	EdgeCounts  map[model.EdgeKey]int
	ErrorCounts map[model.NodeKey]int
	Durations   map[model.NodeKey][]model.Duration
}

// CountRange summarizes per-trace occurrence counts.
type CountRange struct {
	Min    int     `yaml:"min"`
	Max    int     `yaml:"max"`
	Median float64 `yaml:"median"`
}

// DurationSummary contains informational nearest-rank latency percentiles.
type DurationSummary struct {
	Samples int            `yaml:"samples"`
	P50     model.Duration `yaml:"p50"`
	P95     model.Duration `yaml:"p95"`
}

// NodeObservation contains aggregate behavior for one structural node.
type NodeObservation struct {
	Key       model.NodeKey   `yaml:"key"`
	Counts    CountRange      `yaml:"counts"`
	Total     int             `yaml:"total"`
	Errors    int             `yaml:"errors"`
	ErrorRate float64         `yaml:"error_rate"`
	Durations DurationSummary `yaml:"durations"`
}

// EdgeObservation contains aggregate behavior for one structural edge.
type EdgeObservation struct {
	Key    model.EdgeKey `yaml:"key"`
	Counts CountRange    `yaml:"counts"`
}

// Evidence records complete and excluded telemetry counts.
type Evidence struct {
	Complete   int `yaml:"complete"`
	Incomplete int `yaml:"incomplete"`
	Unmatched  int `yaml:"unmatched"`
}

// Observation is a deterministic aggregate suitable for serialized baselines.
type Observation struct {
	Nodes    []NodeObservation `yaml:"nodes"`
	Edges    []EdgeObservation `yaml:"edges"`
	Evidence Evidence          `yaml:"evidence"`
}
