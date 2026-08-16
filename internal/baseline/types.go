// Package baseline defines the versioned, portable behavioral baseline format.
package baseline

import (
	"github.com/RafaelPanisset/tracebudget/internal/analyze"
	"github.com/RafaelPanisset/tracebudget/internal/model"
)

const SchemaVersion = 1

type Policies struct {
	NewServiceEdge        model.Severity `yaml:"new_service_edge"`
	NewExternalDependency model.Severity `yaml:"new_external_dependency"`
	NewInternalNode       model.Severity `yaml:"new_internal_node"`
	NewError              model.Severity `yaml:"new_error"`
	RemovedNode           model.Severity `yaml:"removed_node"`
	RemovedEdge           model.Severity `yaml:"removed_edge"`
	CountIncrease         model.Severity `yaml:"count_increase"`
	LatencyIncrease       model.Severity `yaml:"latency_increase"`
	IncompleteTraceRate   RatePolicy     `yaml:"incomplete_trace_rate"`
}

type RatePolicy struct {
	Max float64 `yaml:"max"`
}

type SpanBudget struct {
	Service     string          `yaml:"service"`
	Name        string          `yaml:"name"`
	Kind        model.SpanKind  `yaml:"kind,omitempty"`
	P95         *model.Duration `yaml:"p95,omitempty"`
	MaxPerTrace *int            `yaml:"max_per_trace,omitempty"`
}

type Budgets struct {
	Spans        []SpanBudget `yaml:"spans,omitempty"`
	MaxErrorRate *float64     `yaml:"max_error_rate,omitempty"`
}

func (budgets Budgets) IsZero() bool {
	return len(budgets.Spans) == 0 && budgets.MaxErrorRate == nil
}

type Document struct {
	SchemaVersion int                 `yaml:"schema_version"`
	Scenario      string              `yaml:"scenario"`
	Runs          int                 `yaml:"runs"`
	Root          model.RootSelector  `yaml:"root"`
	Policies      Policies            `yaml:"policies"`
	Budgets       Budgets             `yaml:"budgets,omitempty"`
	Observed      analyze.Observation `yaml:"observed"`
	Limitations   []string            `yaml:"limitations"`
}
