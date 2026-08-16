// Package compare evaluates an observed candidate against a saved baseline.
package compare

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/RafaelPanisset/tracebudget/internal/analyze"
	"github.com/RafaelPanisset/tracebudget/internal/baseline"
	"github.com/RafaelPanisset/tracebudget/internal/model"
)

var (
	// ErrInsufficientSamples reports evidence that cannot support a blocking P95 budget.
	ErrInsufficientSamples = errors.New("insufficient complete samples for blocking p95 budget")
	// ErrNoComparableTraces reports a capture that cannot be compared with a baseline.
	ErrNoComparableTraces = errors.New("no comparable traces")
)

// Result is the comparison outcome and its policy findings.
type Result struct {
	Outcome  model.Outcome
	Findings []model.Finding
}

// Compare deterministically evaluates candidate behavior against document.
func Compare(document baseline.Document, candidate analyze.Observation) (Result, error) {
	if candidate.Evidence.Complete == 0 {
		return Result{}, fmt.Errorf("%w: capture produced no comparable traces", ErrNoComparableTraces)
	}

	baseNodes, err := indexNodes(document.Observed.Nodes, "baseline")
	if err != nil {
		return Result{}, err
	}
	candidateNodes, err := indexNodes(candidate.Nodes, "candidate")
	if err != nil {
		return Result{}, err
	}
	baseEdges, err := indexEdges(document.Observed.Edges, "baseline")
	if err != nil {
		return Result{}, err
	}
	candidateEdges, err := indexEdges(candidate.Edges, "candidate")
	if err != nil {
		return Result{}, err
	}

	findings := compareNodes(document, baseNodes, candidateNodes)
	findings = append(findings, compareEdges(document, baseEdges, candidateEdges)...)
	findings = append(findings, compareEvidence(document.Policies.IncompleteTraceRate, candidate.Evidence)...)
	budgetFindings, err := compareBudgets(document.Budgets, candidateNodes)
	if err != nil {
		return Result{}, err
	}
	findings = append(findings, budgetFindings...)
	sort.Slice(findings, func(i, j int) bool {
		return compareFindings(findings[i], findings[j]) < 0
	})

	return Result{Outcome: reduceOutcome(findings), Findings: findings}, nil
}

func indexNodes(nodes []analyze.NodeObservation, source string) (map[model.NodeKey]analyze.NodeObservation, error) {
	indexed := make(map[model.NodeKey]analyze.NodeObservation, len(nodes))
	for _, node := range nodes {
		if _, exists := indexed[node.Key]; exists {
			return nil, fmt.Errorf("duplicate node observation in %s: %s", source, node.Key.String())
		}
		indexed[node.Key] = node
	}
	return indexed, nil
}

func indexEdges(edges []analyze.EdgeObservation, source string) (map[model.EdgeKey]analyze.EdgeObservation, error) {
	indexed := make(map[model.EdgeKey]analyze.EdgeObservation, len(edges))
	for _, edge := range edges {
		if _, exists := indexed[edge.Key]; exists {
			return nil, fmt.Errorf("duplicate edge observation in %s: %s", source, edgeSubject(edge.Key))
		}
		indexed[edge.Key] = edge
	}
	return indexed, nil
}

func compareNodes(document baseline.Document, base, candidate map[model.NodeKey]analyze.NodeObservation) []model.Finding {
	findings := make([]model.Finding, 0)
	for key, current := range candidate {
		previous, existed := base[key]
		if !existed {
			severity := document.Policies.NewInternalNode
			code := "new_internal_node"
			if key.Kind == model.SpanKindClient || key.Kind == model.SpanKindProducer {
				severity = document.Policies.NewExternalDependency
				code = "new_external_dependency"
			}
			findings = appendFinding(findings, code, severity, key.String(), "missing", "present")
			continue
		}
		if previous.Errors == 0 && current.Errors > 0 {
			findings = appendFinding(findings, "new_error", document.Policies.NewError, key.String(), "0", strconv.Itoa(current.Errors))
		}
		if current.Counts.Max > previous.Counts.Max {
			findings = appendFinding(findings, "count_increase", document.Policies.CountIncrease, key.String(), strconv.Itoa(previous.Counts.Max), strconv.Itoa(current.Counts.Max))
		}
		if previous.Durations.P95 > 0 && current.Durations.P95 > previous.Durations.P95 {
			findings = appendFinding(findings, "latency_increase", document.Policies.LatencyIncrease, key.String(), durationString(previous.Durations.P95), durationString(current.Durations.P95))
		}
	}
	for key := range base {
		if _, exists := candidate[key]; !exists {
			findings = appendFinding(findings, "removed_node", document.Policies.RemovedNode, key.String(), "present", "missing")
		}
	}
	return findings
}

func compareEdges(document baseline.Document, base, candidate map[model.EdgeKey]analyze.EdgeObservation) []model.Finding {
	findings := make([]model.Finding, 0)
	for key, current := range candidate {
		previous, existed := base[key]
		subject := edgeSubject(key)
		if !existed {
			if key.Parent.Service != key.Child.Service {
				findings = appendFinding(findings, "new_service_edge", document.Policies.NewServiceEdge, subject, "missing", "present")
			}
			continue
		}
		if current.Counts.Max > previous.Counts.Max {
			findings = appendFinding(findings, "count_increase", document.Policies.CountIncrease, subject, strconv.Itoa(previous.Counts.Max), strconv.Itoa(current.Counts.Max))
		}
	}
	for key := range base {
		if _, exists := candidate[key]; !exists {
			findings = appendFinding(findings, "removed_edge", document.Policies.RemovedEdge, edgeSubject(key), "present", "missing")
		}
	}
	return findings
}

func compareEvidence(policy baseline.RatePolicy, evidence analyze.Evidence) []model.Finding {
	denominator := float64(evidence.Complete) + float64(evidence.Incomplete)
	if denominator == 0 {
		return nil
	}
	rate := float64(evidence.Incomplete) / denominator
	if rate <= policy.Max {
		return nil
	}
	return []model.Finding{{
		Code:      "incomplete_trace_rate",
		Severity:  model.SeverityFail,
		Subject:   "capture",
		Baseline:  formatRate(policy.Max),
		Candidate: formatRate(rate),
		Message:   "incomplete trace rate exceeds policy",
	}}
}

func compareBudgets(budgets baseline.Budgets, nodes map[model.NodeKey]analyze.NodeObservation) ([]model.Finding, error) {
	findings := make([]model.Finding, 0)
	for _, budget := range budgets.Spans {
		matched := false
		for key, node := range nodes {
			if !matchesBudget(key, budget) {
				continue
			}
			matched = true
			if budget.MaxPerTrace != nil && node.Counts.Max > *budget.MaxPerTrace {
				findings = appendFinding(findings, "max_per_trace_exceeded", model.SeverityFail, key.String(), strconv.Itoa(*budget.MaxPerTrace), strconv.Itoa(node.Counts.Max))
			}
			if budget.P95 == nil {
				continue
			}
			if node.Durations.Samples < 20 {
				return nil, insufficientSamples(key.String(), node.Durations.Samples)
			}
			if node.Durations.P95 > *budget.P95 {
				findings = appendFinding(findings, "p95_budget_exceeded", model.SeverityFail, key.String(), durationString(*budget.P95), durationString(node.Durations.P95))
			}
		}
		if budget.P95 != nil && !matched {
			return nil, insufficientSamples(budgetSubject(budget), 0)
		}
	}
	if budgets.MaxErrorRate != nil {
		total, errorsTotal := 0.0, 0.0
		for _, node := range nodes {
			total += float64(node.Total)
			errorsTotal += float64(node.Errors)
		}
		rate := 0.0
		if total > 0 {
			rate = errorsTotal / total
		}
		if rate > *budgets.MaxErrorRate {
			findings = appendFinding(findings, "error_rate_exceeded", model.SeverityFail, "all spans", formatRate(*budgets.MaxErrorRate), formatRate(rate))
		}
	}
	return findings, nil
}

func insufficientSamples(subject string, samples int) error {
	return fmt.Errorf("%w: %s has %d", ErrInsufficientSamples, subject, samples)
}

func matchesBudget(key model.NodeKey, budget baseline.SpanBudget) bool {
	return key.Service == budget.Service && key.Name == budget.Name && (budget.Kind == "" || key.Kind == budget.Kind)
}

func appendFinding(findings []model.Finding, code string, severity model.Severity, subject, baselineValue, candidateValue string) []model.Finding {
	if severity == model.SeverityIgnore {
		return findings
	}
	return append(findings, model.Finding{
		Code:      code,
		Severity:  severity,
		Subject:   subject,
		Baseline:  baselineValue,
		Candidate: candidateValue,
		Message:   strings.ReplaceAll(code, "_", " "),
	})
}

func compareFindings(left, right model.Finding) int {
	for _, comparison := range []int{
		severityOrder(left.Severity) - severityOrder(right.Severity),
		strings.Compare(left.Code, right.Code),
		strings.Compare(left.Subject, right.Subject),
		strings.Compare(left.Baseline, right.Baseline),
		strings.Compare(left.Candidate, right.Candidate),
		strings.Compare(left.Message, right.Message),
	} {
		if comparison != 0 {
			return comparison
		}
	}
	return 0
}

func severityOrder(severity model.Severity) int {
	switch severity {
	case model.SeverityFail:
		return 0
	case model.SeverityWarn:
		return 1
	default:
		return 2
	}
}

func reduceOutcome(findings []model.Finding) model.Outcome {
	for _, finding := range findings {
		if finding.Severity == model.SeverityFail {
			return model.OutcomeFail
		}
	}
	return model.OutcomePass
}

func edgeSubject(key model.EdgeKey) string {
	return key.Parent.String() + " -> " + key.Child.String()
}

func budgetSubject(budget baseline.SpanBudget) string {
	key := model.NodeKey{Service: budget.Service, Name: budget.Name, Kind: budget.Kind}
	return key.String()
}

func durationString(value model.Duration) string {
	return time.Duration(value).String()
}

func formatRate(value float64) string {
	return fmt.Sprintf("%.2f%%", value*100)
}
