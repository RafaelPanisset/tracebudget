package analyze

import (
	"errors"
	"math"
	"strings"
	"testing"

	"github.com/RafaelPanisset/tracebudget/internal/assemble"
	"github.com/RafaelPanisset/tracebudget/internal/model"
)

func TestNormalizeBuildsNodesEdgesCountsDurationsAndErrors(t *testing.T) {
	trace := assemble.Trace{TraceID: "trace", RunID: "run", RootID: "root", Spans: []model.Span{
		{SpanID: "root", ServiceName: "gateway", Name: "checkout", Kind: model.SpanKindServer},
		{SpanID: "db-1", ParentSpanID: "root", ServiceName: "gateway", Name: "db.query", Kind: model.SpanKindClient, StartUnixNano: 10, EndUnixNano: 30},
		{SpanID: "db-2", ParentSpanID: "root", ServiceName: "gateway", Name: "db.query", Kind: model.SpanKindClient, Status: model.StatusError, StartUnixNano: 40, EndUnixNano: 70},
		{SpanID: "db-3", ParentSpanID: "root", ServiceName: "gateway", Name: "db.query", Kind: model.SpanKindClient, HasException: true, StartUnixNano: 80, EndUnixNano: 120},
	}}

	summary, err := Normalize(trace)
	if err != nil {
		t.Fatal(err)
	}
	db := model.NodeKey{Service: "gateway", Name: "db.query", Kind: model.SpanKindClient}
	if summary.TraceID != "trace" || summary.RunID != "run" {
		t.Fatalf("lost intermediate correlation identity: %#v", summary)
	}
	if summary.NodeCounts[db] != 3 || summary.ErrorCounts[db] != 2 {
		t.Fatalf("unexpected summary: %#v", summary)
	}
	edge := model.EdgeKey{
		Parent: model.NodeKey{Service: "gateway", Name: "checkout", Kind: model.SpanKindServer},
		Child:  db,
	}
	if summary.EdgeCounts[edge] != 3 {
		t.Fatalf("unexpected edge counts: %#v", summary.EdgeCounts)
	}
	wantDurations := []model.Duration{20, 30, 40}
	gotDurations := summary.Durations[db]
	if len(gotDurations) != len(wantDurations) {
		t.Fatalf("durations=%v, want %v", gotDurations, wantDurations)
	}
	for index, want := range wantDurations {
		if gotDurations[index] != want {
			t.Fatalf("durations=%v, want %v", gotDurations, wantDurations)
		}
	}
}

func TestNormalizeCanonicalizesCapturedAttributesUnambiguously(t *testing.T) {
	trace := assemble.Trace{TraceID: "trace", RootID: "root", Spans: []model.Span{{
		SpanID: "root", ServiceName: "inventory", Name: "db.query", Kind: model.SpanKindClient,
		AllowedAttributes: map[string]string{
			"db.system":         "postgresql",
			"db.operation.name": "INSERT",
			"a&b":               "x=y z+q",
		},
	}}}

	summary, err := Normalize(trace)
	if err != nil {
		t.Fatal(err)
	}
	key := model.NodeKey{
		Service: "inventory", Name: "db.query", Kind: model.SpanKindClient,
		Attributes: "a%26b=x%3Dy+z%2Bq&db.operation.name=INSERT&db.system=postgresql",
	}
	if summary.NodeCounts[key] != 1 {
		t.Fatalf("canonical allowlisted dimensions missing: %#v", summary.NodeCounts)
	}
}

func TestNormalizeRejectsInvalidTraceWithoutPartialSummary(t *testing.T) {
	tests := []struct {
		name        string
		trace       assemble.Trace
		messagePart string
	}{
		{
			name: "duplicate span identity",
			trace: assemble.Trace{Spans: []model.Span{
				{SpanID: "same", StartUnixNano: 1, EndUnixNano: 2},
				{SpanID: "same", StartUnixNano: 2, EndUnixNano: 3},
			}},
			messagePart: "duplicate span ID",
		},
		{
			name: "empty span identity",
			trace: assemble.Trace{Spans: []model.Span{
				{SpanID: "", StartUnixNano: 1, EndUnixNano: 2},
			}},
			messagePart: "empty span ID",
		},
		{
			name: "missing parent",
			trace: assemble.Trace{Spans: []model.Span{
				{SpanID: "child", ParentSpanID: "absent", StartUnixNano: 1, EndUnixNano: 2},
			}},
			messagePart: "missing parent",
		},
		{
			name: "negative duration",
			trace: assemble.Trace{Spans: []model.Span{
				{SpanID: "span", StartUnixNano: 10, EndUnixNano: 9},
			}},
			messagePart: "ends before it starts",
		},
		{
			name: "duration overflows model duration",
			trace: assemble.Trace{Spans: []model.Span{
				{SpanID: "span", EndUnixNano: math.MaxUint64},
			}},
			messagePart: "duration exceeds maximum",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			summary, err := Normalize(test.trace)
			if !errors.Is(err, ErrInvalidTrace) {
				t.Fatalf("err=%v, want ErrInvalidTrace", err)
			}
			if !strings.Contains(err.Error(), test.messagePart) {
				t.Fatalf("err=%q, want part %q", err, test.messagePart)
			}
			if summary.NodeCounts != nil || summary.EdgeCounts != nil || summary.ErrorCounts != nil || summary.Durations != nil {
				t.Fatalf("returned partial summary: %#v", summary)
			}
		})
	}
}

func TestNormalizeAcceptsMaximumModelDuration(t *testing.T) {
	key := model.NodeKey{Service: "service", Name: "operation", Kind: model.SpanKindInternal}
	trace := assemble.Trace{Spans: []model.Span{{
		SpanID: "span", ServiceName: key.Service, Name: key.Name, Kind: key.Kind,
		EndUnixNano: math.MaxInt64,
	}}}

	summary, err := Normalize(trace)
	if err != nil {
		t.Fatal(err)
	}
	if got := summary.Durations[key]; len(got) != 1 || got[0] != model.Duration(math.MaxInt64) {
		t.Fatalf("durations=%v, want [%d]", got, int64(math.MaxInt64))
	}
}
