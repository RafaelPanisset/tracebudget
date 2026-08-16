package sqlite

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/RafaelPanisset/tracebudget/internal/model"
)

func TestStoreWritesAndLoadsExecutionSpans(t *testing.T) {
	store := openTestStore(t)
	spans := []model.Span{
		{TraceID: "trace-a", SpanID: "span-a", ExecutionID: "exec-1", Name: "root"},
		{TraceID: "trace-b", SpanID: "span-b", ExecutionID: "exec-2", Name: "other"},
	}

	result, err := store.WriteBatch(context.Background(), spans)
	if err != nil {
		t.Fatal(err)
	}
	if result.Inserted != 2 {
		t.Fatalf("got %#v", result)
	}

	loaded, err := store.SpansForExecution(context.Background(), "exec-1")
	if err != nil || len(loaded) != 1 || loaded[0].Name != "root" {
		t.Fatalf("loaded=%#v err=%v", loaded, err)
	}
}

func TestSpansForExecutionIncludesUnmarkedSpansFromMatchingTraces(t *testing.T) {
	store := openTestStore(t)
	spans := []model.Span{
		{TraceID: "trace-other", SpanID: "alien", ExecutionID: "exec-other", Name: "alien", ArrivedAt: time.Unix(0, 0)},
		{TraceID: "trace-match", SpanID: "child", Name: "child", ArrivedAt: time.Unix(0, 1)},
		{TraceID: "trace-match", SpanID: "root", ExecutionID: "exec-1", Name: "root", ArrivedAt: time.Unix(0, 2)},
		{TraceID: "trace-match", SpanID: "marked-sibling", ExecutionID: "exec-1", Name: "marked-sibling", ArrivedAt: time.Unix(0, 3)},
	}
	if _, err := store.WriteBatch(context.Background(), spans); err != nil {
		t.Fatal(err)
	}

	loaded, err := store.SpansForExecution(context.Background(), "exec-1")
	if err != nil {
		t.Fatal(err)
	}
	gotNames := make([]string, len(loaded))
	for index, span := range loaded {
		gotNames[index] = span.Name
	}
	wantNames := []string{"child", "root", "marked-sibling"}
	if !slices.Equal(gotNames, wantNames) {
		t.Fatalf("got names %v, want %v", gotNames, wantNames)
	}
}

func TestStoreDistinguishesExactAndConflictingDuplicates(t *testing.T) {
	store := openTestStore(t)
	original := model.Span{TraceID: "trace", SpanID: "span", Name: "original"}
	if _, err := store.WriteBatch(context.Background(), []model.Span{original}); err != nil {
		t.Fatal(err)
	}

	duplicate, err := store.WriteBatch(context.Background(), []model.Span{original})
	if err != nil || duplicate.Duplicates != 1 {
		t.Fatalf("result=%#v err=%v", duplicate, err)
	}

	retriedLater := original
	retriedLater.ArrivedAt = time.Unix(20, 0)
	duplicate, err = store.WriteBatch(context.Background(), []model.Span{retriedLater})
	if err != nil || duplicate.Duplicates != 1 {
		t.Fatalf("later retry result=%#v err=%v", duplicate, err)
	}

	changed := original
	changed.Name = "changed"
	if _, err := store.WriteBatch(context.Background(), []model.Span{changed}); !errors.Is(err, ErrConflictingDuplicate) {
		t.Fatalf("got %v", err)
	}
}

func TestWriteBatchRollsBackOnConflict(t *testing.T) {
	store := openTestStore(t)
	original := model.Span{TraceID: "trace", SpanID: "span", ExecutionID: "exec", Name: "original"}
	if _, err := store.WriteBatch(context.Background(), []model.Span{original}); err != nil {
		t.Fatal(err)
	}

	changed := original
	changed.Name = "changed"
	newSpan := model.Span{TraceID: "new", SpanID: "new", ExecutionID: "exec", Name: "new"}
	result, err := store.WriteBatch(context.Background(), []model.Span{newSpan, changed})
	if !errors.Is(err, ErrConflictingDuplicate) {
		t.Fatalf("got %v", err)
	}
	if result != (WriteResult{}) {
		t.Fatalf("rolled-back batch reported writes: %#v", result)
	}

	loaded, err := store.SpansForExecution(context.Background(), "exec")
	if err != nil || len(loaded) != 1 || loaded[0].Name != "original" {
		t.Fatalf("loaded=%#v err=%v", loaded, err)
	}
}

func TestSpansForExecutionRejectsCorruptPayload(t *testing.T) {
	store := openTestStore(t)
	_, err := store.database.Exec(`INSERT INTO spans
		(trace_id, span_id, execution_id, content_hash, payload, arrived_at_unix_nano)
		VALUES ('trace', 'span', 'exec', X'00', X'7B', 1)`)
	if err != nil {
		t.Fatal(err)
	}

	_, err = store.SpansForExecution(context.Background(), "exec")
	if err == nil {
		t.Fatal("expected corrupt JSON to fail")
	}
	if !strings.Contains(err.Error(), `decode stored span for execution "exec"`) {
		t.Fatalf("error lacks operation context: %v", err)
	}
}

func TestWriteBatchHonorsCancelledContext(t *testing.T) {
	store := openTestStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	result, err := store.WriteBatch(ctx, []model.Span{{TraceID: "trace", SpanID: "span", ExecutionID: "exec"}})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("got result=%#v err=%v", result, err)
	}
	if result != (WriteResult{}) {
		t.Fatalf("cancelled batch reported writes: %#v", result)
	}
	loaded, loadErr := store.SpansForExecution(context.Background(), "exec")
	if loadErr != nil || len(loaded) != 0 {
		t.Fatalf("cancelled batch persisted spans: loaded=%#v err=%v", loaded, loadErr)
	}
}

func openTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := Open(filepath.Join(t.TempDir(), "spans.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})
	return store
}
