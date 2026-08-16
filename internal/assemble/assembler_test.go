package assemble

import (
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/RafaelPanisset/tracebudget/internal/model"
)

func TestAssembleDiscoversMarkedRootAndIncludesUnmarkedChildren(t *testing.T) {
	ended := time.Unix(20, 0)
	spans := []model.Span{
		{
			TraceID: "t1", SpanID: "child", ParentSpanID: "root",
			ServiceName: "inventory", Name: "reserve", ArrivedAt: ended.Add(-time.Second),
		},
		{
			TraceID: "t1", SpanID: "root", ServiceName: "planner", Name: "trip.plan",
			ExecutionID: "exec", RunID: "run-1", EndUnixNano: 10,
			ArrivedAt: ended.Add(-time.Second),
		},
	}

	result, err := Assemble(spans, Config{
		ExecutionID: "exec", CaptureEndedAt: ended, QuietPeriod: 500 * time.Millisecond,
		ExpectedRuns: []string{"run-1"}, ExpectedPerRun: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Root != (model.RootSelector{Service: "planner", Span: "trip.plan", ExpectedPerRun: 1}) {
		t.Fatalf("unexpected root: %#v", result.Root)
	}
	if len(result.Traces) != 1 || result.Traces[0].TraceID != "t1" || result.Traces[0].RootID != "root" {
		t.Fatalf("unexpected traces: %#v", result.Traces)
	}
	gotSpanIDs := []string{result.Traces[0].Spans[0].SpanID, result.Traces[0].Spans[1].SpanID}
	if !slices.Equal(gotSpanIDs, []string{"child", "root"}) {
		t.Fatalf("got span IDs %v", gotSpanIDs)
	}
}

func TestAssembleRejectsZeroAndAmbiguousRoots(t *testing.T) {
	ended := time.Unix(20, 0)
	_, err := Assemble(nil, Config{
		ExecutionID: "exec", CaptureEndedAt: ended,
		ExpectedRuns: []string{"run-1"}, ExpectedPerRun: 1,
	})
	if !errors.Is(err, ErrRootNotFound) {
		t.Fatalf("zero roots: %v", err)
	}

	spans := []model.Span{
		markedAssembleRoot("t1", "a", "run-1", ended, "one", "root"),
		markedAssembleRoot("t2", "b", "run-1", ended, "two", "root"),
	}
	_, err = Assemble(spans, Config{
		ExecutionID: "exec", CaptureEndedAt: ended,
		ExpectedRuns: []string{"run-1"}, ExpectedPerRun: 1,
	})
	if !errors.Is(err, ErrAmbiguousRoot) {
		t.Fatalf("ambiguous roots: %v", err)
	}
}

func TestAssembleReportsAllAmbiguousRootCandidatesDeterministically(t *testing.T) {
	ended := time.Unix(20, 0)
	traceZ := markedAssembleRoot("trace-z", "span-b", "run-2", ended, "inventory", "checkout")
	traceASecond := markedAssembleRoot("trace-a", "span-z", "run-1", ended, "planner", "trip.plan")
	traceAFirst := markedAssembleRoot("trace-a", "span-a", "run-1", ended, "planner", "other")
	inputs := [][]model.Span{
		{traceZ, traceASecond, traceAFirst},
		{traceAFirst, traceZ, traceASecond},
	}
	want := `multiple marked root selectors: candidates=[{trace_id:"trace-a" span_id:"span-a" run_id:"run-1" service:"planner" span:"other"}, {trace_id:"trace-a" span_id:"span-z" run_id:"run-1" service:"planner" span:"trip.plan"}, {trace_id:"trace-z" span_id:"span-b" run_id:"run-2" service:"inventory" span:"checkout"}]`

	for index, spans := range inputs {
		_, err := Assemble(spans, Config{
			ExecutionID: "exec", CaptureEndedAt: ended,
			ExpectedRuns: []string{"run-1", "run-2"}, ExpectedPerRun: 0,
		})
		if !errors.Is(err, ErrAmbiguousRoot) {
			t.Fatalf("input %d: got %v", index, err)
		}
		if err.Error() != want {
			t.Fatalf("input %d:\n got %q\nwant %q", index, err, want)
		}
	}
}

func TestAssembleUsesExplicitSelectorAndCountsUnmatchedTraces(t *testing.T) {
	ended := time.Unix(20, 0)
	spans := []model.Span{
		markedAssembleRoot("t2", "b", "run-1", ended, "planner", "trip.plan"),
		{TraceID: "unrelated", SpanID: "x", ServiceName: "other", Name: "noise", EndUnixNano: 1, ArrivedAt: ended.Add(-time.Second)},
		markedAssembleRoot("t1", "a", "run-1", ended, "planner", "trip.plan"),
		markedAssembleRoot("other-root", "c", "run-1", ended, "worker", "job"),
	}

	result, err := Assemble(spans, Config{
		ExecutionID: "exec", Root: model.RootSelector{Service: "planner", Span: "trip.plan"},
		CaptureEndedAt: ended, QuietPeriod: 500 * time.Millisecond,
		ExpectedRuns: []string{"run-1"}, ExpectedPerRun: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Root != (model.RootSelector{Service: "planner", Span: "trip.plan", ExpectedPerRun: 2}) {
		t.Fatalf("unexpected root: %#v", result.Root)
	}
	if result.Unmatched != 2 {
		t.Fatalf("unmatched=%d, want 2", result.Unmatched)
	}
	gotTraceIDs := []string{result.Traces[0].TraceID, result.Traces[1].TraceID}
	if !slices.Equal(gotTraceIDs, []string{"t1", "t2"}) {
		t.Fatalf("got trace IDs %v", gotTraceIDs)
	}
}

func TestAssembleRejectsTwoMatchingRootsInOneTrace(t *testing.T) {
	ended := time.Unix(20, 0)
	spans := []model.Span{
		markedAssembleRoot("t1", "a", "run-1", ended, "planner", "trip.plan"),
		markedAssembleRoot("t1", "b", "run-1", ended, "planner", "trip.plan"),
	}

	_, err := Assemble(spans, Config{
		ExecutionID: "exec", Root: model.RootSelector{Service: "planner", Span: "trip.plan"},
		CaptureEndedAt: ended, QuietPeriod: 500 * time.Millisecond,
		ExpectedRuns: []string{"run-1"}, ExpectedPerRun: 1,
	})
	if !errors.Is(err, ErrUnexpectedRootCount) {
		t.Fatalf("got %v", err)
	}
}

func TestAssembleReportsAllUnexpectedRootCountsAndPreservesEvidence(t *testing.T) {
	ended := time.Unix(20, 0)
	traceZSecond := markedAssembleRoot("trace-z", "root-z2", "run-1", ended, "planner", "trip.plan")
	traceASecond := markedAssembleRoot("trace-a", "root-a2", "run-1", ended, "planner", "trip.plan")
	traceZFirst := markedAssembleRoot("trace-z", "root-z1", "run-1", ended, "planner", "trip.plan")
	traceAFirst := markedAssembleRoot("trace-a", "root-a1", "run-1", ended, "planner", "trip.plan")
	valid := markedAssembleRoot("trace-ok", "root-ok", "run-1", ended, "planner", "trip.plan")
	unmatched := model.Span{TraceID: "trace-noise", SpanID: "noise", ArrivedAt: ended.Add(-time.Second)}
	inputs := [][]model.Span{
		{traceZSecond, unmatched, traceASecond, valid, traceZFirst, traceAFirst},
		{traceAFirst, traceZFirst, valid, traceASecond, unmatched, traceZSecond},
	}
	wantError := `trace contains multiple matching roots: candidates=[{trace_id:"trace-a" span_id:"root-a1" run_id:"run-1" service:"planner" span:"trip.plan"}, {trace_id:"trace-a" span_id:"root-a2" run_id:"run-1" service:"planner" span:"trip.plan"}, {trace_id:"trace-z" span_id:"root-z1" run_id:"run-1" service:"planner" span:"trip.plan"}, {trace_id:"trace-z" span_id:"root-z2" run_id:"run-1" service:"planner" span:"trip.plan"}]`

	for index, spans := range inputs {
		result, err := Assemble(spans, Config{
			ExecutionID: "exec", Root: model.RootSelector{Service: "planner", Span: "trip.plan"},
			CaptureEndedAt: ended, QuietPeriod: 500 * time.Millisecond,
			ExpectedRuns: []string{"run-1"}, ExpectedPerRun: 1,
		})
		if !errors.Is(err, ErrUnexpectedRootCount) {
			t.Fatalf("input %d: got %v", index, err)
		}
		if err.Error() != wantError {
			t.Fatalf("input %d:\n got %q\nwant %q", index, err, wantError)
		}
		if len(result.Traces) != 1 || result.Traces[0].TraceID != "trace-ok" || result.Incomplete != 2 || result.Unmatched != 1 {
			t.Fatalf("input %d lost evidence: %#v", index, result)
		}
	}
}

func TestAssembleClassifiesIncompleteTraces(t *testing.T) {
	ended := time.Unix(20, 0)
	tests := []struct {
		name  string
		spans []model.Span
	}{
		{
			name: "root has no end",
			spans: []model.Span{{
				TraceID: "t", SpanID: "root", ServiceName: "planner", Name: "trip.plan",
				ExecutionID: "exec", RunID: "run-1", ArrivedAt: ended.Add(-time.Second),
			}},
		},
		{
			name: "late arrival",
			spans: []model.Span{{
				TraceID: "t", SpanID: "root", ServiceName: "planner", Name: "trip.plan",
				ExecutionID: "exec", RunID: "run-1", EndUnixNano: 1,
				ArrivedAt: ended.Add(-100 * time.Millisecond),
			}},
		},
		{
			name: "missing parent",
			spans: []model.Span{
				markedAssembleRoot("t", "root", "run-1", ended, "planner", "trip.plan"),
				{TraceID: "t", SpanID: "child", ParentSpanID: "absent", ServiceName: "inventory", Name: "reserve", EndUnixNano: 1, ArrivedAt: ended.Add(-time.Second)},
			},
		},
		{
			name: "second unmarked root",
			spans: []model.Span{
				markedAssembleRoot("t", "root", "run-1", ended, "planner", "trip.plan"),
				{TraceID: "t", SpanID: "detached", ServiceName: "inventory", Name: "reserve", EndUnixNano: 1, ArrivedAt: ended.Add(-time.Second)},
			},
		},
		{
			name: "disconnected parent cycle",
			spans: []model.Span{
				markedAssembleRoot("t", "root", "run-1", ended, "planner", "trip.plan"),
				{TraceID: "t", SpanID: "a", ParentSpanID: "b", ServiceName: "inventory", Name: "reserve", EndUnixNano: 1, ArrivedAt: ended.Add(-time.Second)},
				{TraceID: "t", SpanID: "b", ParentSpanID: "a", ServiceName: "inventory", Name: "reserve", EndUnixNano: 1, ArrivedAt: ended.Add(-time.Second)},
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result, err := Assemble(test.spans, Config{
				ExecutionID: "exec", CaptureEndedAt: ended, QuietPeriod: 500 * time.Millisecond,
				ExpectedRuns: []string{"run-1"}, ExpectedPerRun: 0,
			})
			if err != nil {
				t.Fatal(err)
			}
			if result.Incomplete != 1 || len(result.Traces) != 0 || result.Unmatched != 0 {
				t.Fatalf("unexpected result: %#v", result)
			}
		})
	}
}

func TestAssembleAcceptsArrivalAtQuietPeriodBoundary(t *testing.T) {
	ended := time.Unix(20, 0)
	root := markedAssembleRoot("t", "root", "run-1", ended, "planner", "trip.plan")
	root.ArrivedAt = ended.Add(-500 * time.Millisecond)

	result, err := Assemble([]model.Span{root}, Config{
		ExecutionID: "exec", CaptureEndedAt: ended, QuietPeriod: 500 * time.Millisecond,
		ExpectedRuns: []string{"run-1"}, ExpectedPerRun: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Traces) != 1 || result.Incomplete != 0 {
		t.Fatalf("unexpected result: %#v", result)
	}
}

func TestAssembleValidatesExactCountsPerRunAndSortsOutput(t *testing.T) {
	ended := time.Unix(20, 0)
	spans := []model.Span{
		markedAssembleRoot("trace-d", "root-d", "run-2", ended, "planner", "trip.plan"),
		markedAssembleRoot("trace-b", "root-b", "run-2", ended, "planner", "trip.plan"),
		{TraceID: "trace-b", SpanID: "a-child", ParentSpanID: "root-b", ArrivedAt: ended.Add(-time.Second)},
		markedAssembleRoot("trace-c", "root-c", "run-1", ended, "planner", "trip.plan"),
		markedAssembleRoot("trace-a", "root-a", "run-1", ended, "planner", "trip.plan"),
	}

	result, err := Assemble(spans, Config{
		ExecutionID: "exec", CaptureEndedAt: ended, QuietPeriod: 500 * time.Millisecond,
		ExpectedRuns: []string{"run-1", "run-2"}, ExpectedPerRun: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	gotTraceIDs := make([]string, len(result.Traces))
	for index, trace := range result.Traces {
		gotTraceIDs[index] = trace.TraceID
	}
	if !slices.Equal(gotTraceIDs, []string{"trace-a", "trace-b", "trace-c", "trace-d"}) {
		t.Fatalf("got trace IDs %v", gotTraceIDs)
	}
	gotSpanIDs := []string{result.Traces[1].Spans[0].SpanID, result.Traces[1].Spans[1].SpanID}
	if !slices.Equal(gotSpanIDs, []string{"a-child", "root-b"}) {
		t.Fatalf("got span IDs %v", gotSpanIDs)
	}
}

func TestAssembleReturnsDiagnosticsWithExpectedCountError(t *testing.T) {
	ended := time.Unix(20, 0)
	incomplete := markedAssembleRoot("trace-incomplete", "root-incomplete", "run-1", ended, "planner", "trip.plan")
	incomplete.EndUnixNano = 0
	spans := []model.Span{
		markedAssembleRoot("trace-complete", "root-complete", "run-1", ended, "planner", "trip.plan"),
		incomplete,
		{TraceID: "trace-unmatched", SpanID: "noise", ArrivedAt: ended.Add(-time.Second)},
	}

	result, err := Assemble(spans, Config{
		ExecutionID: "exec", CaptureEndedAt: ended, QuietPeriod: 500 * time.Millisecond,
		ExpectedRuns: []string{"run-1"}, ExpectedPerRun: 2,
	})
	if !errors.Is(err, ErrUnexpectedTraceCount) {
		t.Fatalf("got %v", err)
	}
	if len(result.Traces) != 1 || result.Incomplete != 1 || result.Unmatched != 1 {
		t.Fatalf("diagnostics were lost: %#v", result)
	}
}

func TestAssembleRejectsUnexpectedRunWithoutHidingClassification(t *testing.T) {
	ended := time.Unix(20, 0)
	incomplete := markedAssembleRoot("trace", "root", "run-2", ended, "planner", "trip.plan")
	incomplete.EndUnixNano = 0

	result, err := Assemble([]model.Span{incomplete}, Config{
		ExecutionID: "exec", CaptureEndedAt: ended, QuietPeriod: 500 * time.Millisecond,
		ExpectedRuns: []string{"run-1"}, ExpectedPerRun: 0,
	})
	if !errors.Is(err, ErrUnexpectedTraceCount) {
		t.Fatalf("got %v", err)
	}
	if result.Incomplete != 1 {
		t.Fatalf("classification was lost: %#v", result)
	}
}

func TestAssembleCopiesInputSpansAndAttributeMaps(t *testing.T) {
	ended := time.Unix(20, 0)
	spans := []model.Span{markedAssembleRoot("trace", "root", "run-1", ended, "planner", "trip.plan")}
	spans[0].AllowedAttributes = map[string]string{"db.system": "postgresql"}

	result, err := Assemble(spans, Config{
		ExecutionID: "exec", CaptureEndedAt: ended, QuietPeriod: 500 * time.Millisecond,
		ExpectedRuns: []string{"run-1"}, ExpectedPerRun: 1,
	})
	if err != nil {
		t.Fatal(err)
	}

	spans[0].Name = "changed"
	spans[0].AllowedAttributes["db.system"] = "mysql"
	if got := result.Traces[0].Spans[0]; got.Name != "trip.plan" || got.AllowedAttributes["db.system"] != "postgresql" {
		t.Fatalf("result aliases input: %#v", got)
	}

	result.Traces[0].Spans[0].AllowedAttributes["db.system"] = "oracle"
	if spans[0].AllowedAttributes["db.system"] != "mysql" {
		t.Fatalf("input aliases result: %#v", spans[0])
	}
}

func TestAssembleRejectsInvalidConfig(t *testing.T) {
	tests := []struct {
		name   string
		config Config
	}{
		{
			name: "empty execution ID",
			config: Config{
				ExpectedRuns: []string{"run-1"}, ExpectedPerRun: 1,
			},
		},
		{
			name: "selector has only service",
			config: Config{
				ExecutionID: "exec", Root: model.RootSelector{Service: "planner"},
				ExpectedRuns: []string{"run-1"}, ExpectedPerRun: 1,
			},
		},
		{
			name: "selector has only span",
			config: Config{
				ExecutionID: "exec", Root: model.RootSelector{Span: "trip.plan"},
				ExpectedRuns: []string{"run-1"}, ExpectedPerRun: 1,
			},
		},
		{
			name: "no expected runs",
			config: Config{
				ExecutionID: "exec", ExpectedPerRun: 1,
			},
		},
		{
			name: "empty expected run ID",
			config: Config{
				ExecutionID: "exec", ExpectedRuns: []string{""}, ExpectedPerRun: 1,
			},
		},
		{
			name: "duplicate expected run",
			config: Config{
				ExecutionID: "exec", ExpectedRuns: []string{"run-1", "run-1"}, ExpectedPerRun: 1,
			},
		},
		{
			name: "negative expected count",
			config: Config{
				ExecutionID: "exec", ExpectedRuns: []string{"run-1"}, ExpectedPerRun: -1,
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := Assemble(nil, test.config)
			if !errors.Is(err, ErrInvalidConfig) {
				t.Fatalf("got %v", err)
			}
		})
	}
}

func TestAssembleRejectsInvalidSpanIdentitiesAndMarkedRoots(t *testing.T) {
	ended := time.Unix(20, 0)
	root := markedAssembleRoot("trace", "root", "run-1", ended, "planner", "trip.plan")
	tests := []struct {
		name  string
		spans []model.Span
	}{
		{
			name: "empty trace ID",
			spans: []model.Span{
				root,
				{SpanID: "child", ParentSpanID: "root", ArrivedAt: ended.Add(-time.Second)},
			},
		},
		{
			name: "empty span ID",
			spans: []model.Span{
				root,
				{TraceID: "trace", ParentSpanID: "root", ArrivedAt: ended.Add(-time.Second)},
			},
		},
		{
			name: "marked root has empty service",
			spans: []model.Span{
				markedAssembleRoot("trace", "root", "run-1", ended, "", "trip.plan"),
			},
		},
		{
			name: "marked root has empty span name",
			spans: []model.Span{
				markedAssembleRoot("trace", "root", "run-1", ended, "planner", ""),
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := Assemble(test.spans, Config{
				ExecutionID: "exec", CaptureEndedAt: ended,
				ExpectedRuns: []string{"run-1"}, ExpectedPerRun: 1,
			})
			if !errors.Is(err, ErrInvalidSpan) {
				t.Fatalf("got %v", err)
			}
		})
	}
}

func TestAssembleScopesMalformedRootValidationToSelectedCandidates(t *testing.T) {
	ended := time.Unix(20, 0)
	valid := markedAssembleRoot("trace-valid", "root-valid", "run-1", ended, "planner", "trip.plan")
	tests := []struct {
		name      string
		malformed model.Span
	}{
		{
			name:      "empty service",
			malformed: markedAssembleRoot("trace-other", "root-other", "run-1", ended, "", "other"),
		},
		{
			name:      "empty span name",
			malformed: markedAssembleRoot("trace-other", "root-other", "run-1", ended, "worker", ""),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			spans := []model.Span{test.malformed, valid}
			t.Run("explicit selector", func(t *testing.T) {
				result, err := Assemble(spans, Config{
					ExecutionID: "exec", Root: model.RootSelector{Service: "planner", Span: "trip.plan"},
					CaptureEndedAt: ended, QuietPeriod: 500 * time.Millisecond,
					ExpectedRuns: []string{"run-1"}, ExpectedPerRun: 1,
				})
				if err != nil {
					t.Fatal(err)
				}
				if len(result.Traces) != 1 || result.Traces[0].TraceID != "trace-valid" || result.Unmatched != 1 {
					t.Fatalf("unexpected result: %#v", result)
				}
			})

			t.Run("auto discovery", func(t *testing.T) {
				_, err := Assemble(spans, Config{
					ExecutionID: "exec", CaptureEndedAt: ended,
					ExpectedRuns: []string{"run-1"}, ExpectedPerRun: 1,
				})
				if !errors.Is(err, ErrInvalidSpan) {
					t.Fatalf("got %v", err)
				}
			})
		})
	}
}

func markedAssembleRoot(traceID, spanID, runID string, ended time.Time, service, name string) model.Span {
	return model.Span{
		TraceID: traceID, SpanID: spanID, ServiceName: service, Name: name,
		ExecutionID: "exec", RunID: runID, EndUnixNano: 1,
		ArrivedAt: ended.Add(-time.Second),
	}
}
