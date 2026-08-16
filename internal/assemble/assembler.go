// Package assemble correlates accepted spans into complete scenario traces.
package assemble

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/RafaelPanisset/tracebudget/internal/model"
)

// Config describes one finished capture and its expected scenario runs.
type Config struct {
	ExecutionID    string
	Root           model.RootSelector
	CaptureEndedAt time.Time
	QuietPeriod    time.Duration
	ExpectedRuns   []string
	ExpectedPerRun int
}

// Trace is a complete trace rooted at one marked scenario span.
type Trace struct {
	TraceID string
	RunID   string
	RootID  string
	Spans   []model.Span
}

// Result contains assembled traces and evidence excluded from assembly.
type Result struct {
	Root       model.RootSelector
	Traces     []Trace
	Incomplete int
	Unmatched  int
}

var (
	// ErrInvalidConfig indicates that assembly configuration is malformed.
	ErrInvalidConfig = errors.New("invalid assembly config")
	// ErrInvalidSpan indicates that an input span cannot be assembled safely.
	ErrInvalidSpan = errors.New("invalid assembly span")
	// ErrRootNotFound indicates that no marked span matches root selection.
	ErrRootNotFound = errors.New("marked root not found")
	// ErrAmbiguousRoot indicates that discovery found multiple root selectors.
	ErrAmbiguousRoot = errors.New("multiple marked root selectors")
	// ErrUnexpectedRootCount indicates that a trace has multiple matching roots.
	ErrUnexpectedRootCount = errors.New("trace contains multiple matching roots")
	// ErrUnexpectedTraceCount indicates an unexpected run or complete trace count.
	ErrUnexpectedTraceCount = errors.New("unexpected complete trace count")
)

// Assemble discovers or applies a root selector, classifies every input trace,
// and returns complete traces in deterministic identity order. Returned spans
// and their attribute maps do not alias the input.
func Assemble(spans []model.Span, config Config) (Result, error) {
	if err := validateConfig(config); err != nil {
		return Result{}, err
	}
	if err := validateSpanIdentities(spans); err != nil {
		return Result{}, err
	}
	grouped := groupByTraceID(spans)
	roots := markedRoots(spans, config.ExecutionID, config.Root)
	if err := validateRootCandidates(roots); err != nil {
		return Result{}, err
	}
	selector, err := selectRoot(roots, config.ExpectedPerRun)
	if err != nil {
		return Result{}, err
	}

	result := Result{Root: selector}
	traceIDs := sortedTraceIDs(grouped)
	rootRuns := make([]string, 0, len(traceIDs))
	var conflictingRoots []model.Span
	for _, traceID := range traceIDs {
		traceSpans := grouped[traceID]
		roots := matchingRoots(traceSpans, config.ExecutionID, selector)
		switch {
		case len(roots) == 0:
			result.Unmatched++
			continue
		case len(roots) != 1:
			result.Incomplete++
			conflictingRoots = append(conflictingRoots, roots...)
			continue
		}

		root := roots[0]
		rootRuns = append(rootRuns, root.RunID)
		if !isComplete(traceSpans, root, config.CaptureEndedAt, config.QuietPeriod) {
			result.Incomplete++
			continue
		}
		result.Traces = append(result.Traces, Trace{
			TraceID: traceID,
			RunID:   root.RunID,
			RootID:  root.SpanID,
			Spans:   cloneAndSortSpans(traceSpans),
		})
	}

	if len(conflictingRoots) != 0 {
		return result, fmt.Errorf(
			"%w: candidates=[%s]", ErrUnexpectedRootCount, formatRootCandidates(conflictingRoots),
		)
	}
	if err := validateRunCounts(result.Traces, rootRuns, config.ExpectedRuns, config.ExpectedPerRun); err != nil {
		return result, err
	}
	return result, nil
}

func validateConfig(config Config) error {
	if config.ExecutionID == "" {
		return fmt.Errorf("%w: execution ID is empty", ErrInvalidConfig)
	}
	if (config.Root.Service == "") != (config.Root.Span == "") {
		return fmt.Errorf("%w: root selector requires both service and span", ErrInvalidConfig)
	}
	if config.ExpectedPerRun < 0 {
		return fmt.Errorf("%w: expected count per run is negative", ErrInvalidConfig)
	}
	if len(config.ExpectedRuns) == 0 {
		return fmt.Errorf("%w: expected runs are empty", ErrInvalidConfig)
	}
	seen := make(map[string]struct{}, len(config.ExpectedRuns))
	for _, runID := range config.ExpectedRuns {
		if runID == "" {
			return fmt.Errorf("%w: expected run ID is empty", ErrInvalidConfig)
		}
		if _, duplicate := seen[runID]; duplicate {
			return fmt.Errorf("%w: expected run %q is duplicated", ErrInvalidConfig, runID)
		}
		seen[runID] = struct{}{}
	}
	return nil
}

func validateSpanIdentities(spans []model.Span) error {
	for _, span := range spans {
		if span.TraceID == "" {
			return fmt.Errorf("%w: span %q has empty trace ID", ErrInvalidSpan, span.SpanID)
		}
		if span.SpanID == "" {
			return fmt.Errorf("%w: trace %q has empty span ID", ErrInvalidSpan, span.TraceID)
		}
	}
	return nil
}

func validateRootCandidates(roots []model.Span) error {
	for _, root := range roots {
		if root.ServiceName == "" || root.Name == "" {
			return fmt.Errorf(
				"%w: marked root %q/%q requires service and span name",
				ErrInvalidSpan, root.TraceID, root.SpanID,
			)
		}
	}
	return nil
}

func isMarkedRoot(span model.Span, executionID string) bool {
	return span.ExecutionID == executionID && span.RunID != "" && span.ParentSpanID == ""
}

func groupByTraceID(spans []model.Span) map[string][]model.Span {
	grouped := make(map[string][]model.Span)
	for _, span := range spans {
		grouped[span.TraceID] = append(grouped[span.TraceID], span)
	}
	return grouped
}

func sortedTraceIDs(grouped map[string][]model.Span) []string {
	traceIDs := make([]string, 0, len(grouped))
	for traceID := range grouped {
		traceIDs = append(traceIDs, traceID)
	}
	sort.Strings(traceIDs)
	return traceIDs
}

func markedRoots(spans []model.Span, executionID string, explicit model.RootSelector) []model.Span {
	roots := make([]model.Span, 0)
	hasExplicitSelector := explicit.Service != "" || explicit.Span != ""
	for _, span := range spans {
		if !isMarkedRoot(span, executionID) {
			continue
		}
		if hasExplicitSelector && (span.ServiceName != explicit.Service || span.Name != explicit.Span) {
			continue
		}
		roots = append(roots, span)
	}
	return roots
}

func selectRoot(roots []model.Span, expectedPerRun int) (model.RootSelector, error) {
	if len(roots) == 0 {
		return model.RootSelector{}, ErrRootNotFound
	}
	type selectorKey struct {
		service string
		span    string
	}
	selectors := make(map[selectorKey]struct{})
	for _, root := range roots {
		selectors[selectorKey{service: root.ServiceName, span: root.Name}] = struct{}{}
	}
	if len(selectors) != 1 {
		return model.RootSelector{}, fmt.Errorf(
			"%w: candidates=[%s]", ErrAmbiguousRoot, formatRootCandidates(roots),
		)
	}
	for selector := range selectors {
		return model.RootSelector{
			Service: selector.service, Span: selector.span, ExpectedPerRun: expectedPerRun,
		}, nil
	}
	return model.RootSelector{}, ErrRootNotFound
}

func formatRootCandidates(roots []model.Span) string {
	sorted := append([]model.Span(nil), roots...)
	sort.Slice(sorted, func(i, j int) bool {
		left, right := sorted[i], sorted[j]
		if left.TraceID != right.TraceID {
			return left.TraceID < right.TraceID
		}
		if left.SpanID != right.SpanID {
			return left.SpanID < right.SpanID
		}
		if left.RunID != right.RunID {
			return left.RunID < right.RunID
		}
		if left.ServiceName != right.ServiceName {
			return left.ServiceName < right.ServiceName
		}
		return left.Name < right.Name
	})
	candidates := make([]string, len(sorted))
	for index, root := range sorted {
		candidates[index] = fmt.Sprintf(
			`{trace_id:%q span_id:%q run_id:%q service:%q span:%q}`,
			root.TraceID, root.SpanID, root.RunID, root.ServiceName, root.Name,
		)
	}
	return strings.Join(candidates, ", ")
}

func matchingRoots(spans []model.Span, executionID string, selector model.RootSelector) []model.Span {
	var matched []model.Span
	for _, span := range spans {
		if span.ExecutionID == executionID &&
			span.RunID != "" &&
			span.ParentSpanID == "" &&
			span.ServiceName == selector.Service &&
			span.Name == selector.Span {
			matched = append(matched, span)
		}
	}
	return matched
}

func isComplete(spans []model.Span, root model.Span, captureEndedAt time.Time, quietPeriod time.Duration) bool {
	quietBoundary := captureEndedAt.Add(-quietPeriod)
	if root.EndUnixNano == 0 || root.ArrivedAt.After(quietBoundary) {
		return false
	}

	bySpanID := make(map[string]model.Span, len(spans))
	for _, span := range spans {
		if span.SpanID == "" || span.ArrivedAt.After(quietBoundary) {
			return false
		}
		if _, duplicate := bySpanID[span.SpanID]; duplicate {
			return false
		}
		bySpanID[span.SpanID] = span
	}
	for _, span := range spans {
		if !reachesRoot(span.SpanID, root.SpanID, bySpanID) {
			return false
		}
	}
	return true
}

func reachesRoot(spanID, rootID string, bySpanID map[string]model.Span) bool {
	seen := make(map[string]struct{})
	for spanID != rootID {
		if _, cycle := seen[spanID]; cycle {
			return false
		}
		seen[spanID] = struct{}{}
		span, exists := bySpanID[spanID]
		if !exists || span.ParentSpanID == "" {
			return false
		}
		spanID = span.ParentSpanID
	}
	_, rootExists := bySpanID[rootID]
	return rootExists
}

func cloneAndSortSpans(spans []model.Span) []model.Span {
	cloned := make([]model.Span, len(spans))
	for index, span := range spans {
		cloned[index] = span
		if span.AllowedAttributes != nil {
			cloned[index].AllowedAttributes = make(map[string]string, len(span.AllowedAttributes))
			for key, value := range span.AllowedAttributes {
				cloned[index].AllowedAttributes[key] = value
			}
		}
	}
	sort.Slice(cloned, func(i, j int) bool {
		return cloned[i].SpanID < cloned[j].SpanID
	})
	return cloned
}

func validateRunCounts(traces []Trace, rootRuns, expectedRuns []string, expectedPerRun int) error {
	expected := make(map[string]struct{}, len(expectedRuns))
	for _, runID := range expectedRuns {
		expected[runID] = struct{}{}
	}

	if expectedPerRun != 0 || len(expectedRuns) != 0 {
		for _, runID := range rootRuns {
			if _, exists := expected[runID]; !exists {
				return fmt.Errorf("%w: unexpected run %q", ErrUnexpectedTraceCount, runID)
			}
		}
	}
	if expectedPerRun == 0 {
		return nil
	}

	counts := make(map[string]int, len(expectedRuns))
	for _, trace := range traces {
		counts[trace.RunID]++
	}
	for _, runID := range expectedRuns {
		if counts[runID] != expectedPerRun {
			return fmt.Errorf(
				"%w: run %q has %d complete traces, want %d",
				ErrUnexpectedTraceCount, runID, counts[runID], expectedPerRun,
			)
		}
	}
	return nil
}
