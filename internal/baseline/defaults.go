package baseline

import (
	"github.com/RafaelPanisset/tracebudget/internal/analyze"
	"github.com/RafaelPanisset/tracebudget/internal/model"
)

func DefaultPolicies() Policies {
	return Policies{
		NewServiceEdge:        model.SeverityFail,
		NewExternalDependency: model.SeverityFail,
		NewInternalNode:       model.SeverityWarn,
		NewError:              model.SeverityFail,
		RemovedNode:           model.SeverityWarn,
		RemovedEdge:           model.SeverityWarn,
		CountIncrease:         model.SeverityFail,
		LatencyIncrease:       model.SeverityWarn,
		IncompleteTraceRate:   RatePolicy{Max: 0},
	}
}

func DefaultDocument(scenario string, runs int, root model.RootSelector, observed analyze.Observation) Document {
	copied := observed
	copied.Nodes = append([]analyze.NodeObservation(nil), observed.Nodes...)
	copied.Edges = append([]analyze.EdgeObservation(nil), observed.Edges...)
	return Document{
		SchemaVersion: SchemaVersion,
		Scenario:      scenario,
		Runs:          runs,
		Root:          root,
		Policies:      DefaultPolicies(),
		Observed:      copied,
		Limitations: []string{
			"Upstream SDK sampling or drops before the TraceBudget receiver cannot be detected.",
		},
	}
}
