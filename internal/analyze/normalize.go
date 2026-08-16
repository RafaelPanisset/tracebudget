package analyze

import (
	"errors"
	"fmt"
	"math"
	"net/url"
	"sort"
	"strings"

	"github.com/RafaelPanisset/tracebudget/internal/assemble"
	"github.com/RafaelPanisset/tracebudget/internal/model"
)

// ErrInvalidTrace indicates that a trace cannot be normalized safely.
var ErrInvalidTrace = errors.New("invalid trace")

// Normalize converts one complete trace into structural counts and relative
// durations. It validates the entire trace before returning any summary.
func Normalize(trace assemble.Trace) (TraceSummary, error) {
	if err := validateTrace(trace); err != nil {
		return TraceSummary{}, err
	}

	summary := TraceSummary{
		TraceID:     trace.TraceID,
		RunID:       trace.RunID,
		NodeCounts:  make(map[model.NodeKey]int),
		EdgeCounts:  make(map[model.EdgeKey]int),
		ErrorCounts: make(map[model.NodeKey]int),
		Durations:   make(map[model.NodeKey][]model.Duration),
	}
	nodesBySpanID := make(map[string]model.NodeKey, len(trace.Spans))
	for _, span := range trace.Spans {
		key := model.NodeKey{
			Service:    span.ServiceName,
			Name:       span.Name,
			Kind:       span.Kind,
			Attributes: canonicalAttributes(span.AllowedAttributes),
		}
		nodesBySpanID[span.SpanID] = key
		summary.NodeCounts[key]++
		if span.Status == model.StatusError || span.HasException {
			summary.ErrorCounts[key]++
		}
		summary.Durations[key] = append(
			summary.Durations[key],
			model.Duration(span.EndUnixNano-span.StartUnixNano),
		)
	}
	for _, span := range trace.Spans {
		if span.ParentSpanID == "" {
			continue
		}
		summary.EdgeCounts[model.EdgeKey{
			Parent: nodesBySpanID[span.ParentSpanID],
			Child:  nodesBySpanID[span.SpanID],
		}]++
	}
	return summary, nil
}

func validateTrace(trace assemble.Trace) error {
	seen := make(map[string]struct{}, len(trace.Spans))
	for _, span := range trace.Spans {
		if span.SpanID == "" {
			return fmt.Errorf("%w: empty span ID", ErrInvalidTrace)
		}
		if _, duplicate := seen[span.SpanID]; duplicate {
			return fmt.Errorf("%w: duplicate span ID %q", ErrInvalidTrace, span.SpanID)
		}
		seen[span.SpanID] = struct{}{}
		if span.EndUnixNano < span.StartUnixNano {
			return fmt.Errorf("%w: span %q ends before it starts", ErrInvalidTrace, span.SpanID)
		}
		delta := span.EndUnixNano - span.StartUnixNano
		if delta > uint64(math.MaxInt64) {
			return fmt.Errorf("%w: span %q duration exceeds maximum", ErrInvalidTrace, span.SpanID)
		}
	}
	for _, span := range trace.Spans {
		if span.ParentSpanID == "" {
			continue
		}
		if _, exists := seen[span.ParentSpanID]; !exists {
			return fmt.Errorf(
				"%w: span %q has missing parent %q",
				ErrInvalidTrace,
				span.SpanID,
				span.ParentSpanID,
			)
		}
	}
	return nil
}

func canonicalAttributes(attributes map[string]string) string {
	keys := make([]string, 0, len(attributes))
	for key := range attributes {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	values := make([]string, 0, len(keys))
	for _, key := range keys {
		values = append(values, url.QueryEscape(key)+"="+url.QueryEscape(attributes[key]))
	}
	return strings.Join(values, "&")
}
