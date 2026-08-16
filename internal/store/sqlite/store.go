package sqlite

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/RafaelPanisset/tracebudget/internal/model"
	_ "modernc.org/sqlite"
)

// ErrConflictingDuplicate indicates that an existing span has the same
// identity but different semantic content.
var ErrConflictingDuplicate = errors.New("same trace_id and span_id have different content")

// WriteResult describes the spans accepted by a batch write.
type WriteResult struct {
	Inserted   int
	Duplicates int
}

// Store persists captured spans in SQLite.
type Store struct {
	database *sql.DB
}

//go:embed schema.sql
var schema string

// Open opens a SQLite store and initializes its schema.
func Open(path string) (*Store, error) {
	database, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite store: %w", err)
	}
	database.SetMaxOpenConns(1)
	if _, err := database.Exec(schema); err != nil {
		initializeErr := fmt.Errorf("initialize sqlite store: %w", err)
		if closeErr := database.Close(); closeErr != nil {
			return nil, errors.Join(initializeErr, fmt.Errorf("close sqlite store after initialization failure: %w", closeErr))
		}
		return nil, initializeErr
	}
	return &Store{database: database}, nil
}

// Close closes the underlying database.
func (s *Store) Close() error {
	if err := s.database.Close(); err != nil {
		return fmt.Errorf("close sqlite store: %w", err)
	}
	return nil
}

// WriteBatch atomically persists spans and classifies exact duplicate identities.
func (s *Store) WriteBatch(ctx context.Context, spans []model.Span) (result WriteResult, returned error) {
	transaction, err := s.database.BeginTx(ctx, nil)
	if err != nil {
		return WriteResult{}, fmt.Errorf("begin span batch: %w", err)
	}
	defer func() {
		rollbackErr := transaction.Rollback()
		if returned == nil {
			return
		}
		result = WriteResult{}
		if rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
			returned = errors.Join(returned, fmt.Errorf("rollback span batch: %w", rollbackErr))
		}
	}()

	for _, span := range spans {
		payload, hash, err := encodeSpan(span)
		if err != nil {
			return result, err
		}
		inserted, err := transaction.ExecContext(ctx, `
			INSERT OR IGNORE INTO spans
				(trace_id, span_id, execution_id, content_hash, payload, arrived_at_unix_nano)
			VALUES (?, ?, ?, ?, ?, ?)`,
			span.TraceID, span.SpanID, span.ExecutionID, hash[:], payload, span.ArrivedAt.UnixNano(),
		)
		if err != nil {
			return result, fmt.Errorf("insert span %q/%q: %w", span.TraceID, span.SpanID, err)
		}
		affected, err := inserted.RowsAffected()
		if err != nil {
			return result, fmt.Errorf("count inserted span %q/%q: %w", span.TraceID, span.SpanID, err)
		}
		if affected == 1 {
			result.Inserted++
			continue
		}

		var storedHash []byte
		if err := transaction.QueryRowContext(
			ctx,
			`SELECT content_hash FROM spans WHERE trace_id = ? AND span_id = ?`,
			span.TraceID,
			span.SpanID,
		).Scan(&storedHash); err != nil {
			return result, fmt.Errorf("load duplicate span %q/%q: %w", span.TraceID, span.SpanID, err)
		}
		if !bytes.Equal(storedHash, hash[:]) {
			return result, fmt.Errorf("span %q/%q: %w", span.TraceID, span.SpanID, ErrConflictingDuplicate)
		}
		result.Duplicates++
	}
	if err := transaction.Commit(); err != nil {
		return result, fmt.Errorf("commit span batch: %w", err)
	}
	return result, nil
}

// SpansForExecution loads captured spans for an execution in arrival order.
func (s *Store) SpansForExecution(ctx context.Context, executionID string) (spans []model.Span, returned error) {
	rows, err := s.database.QueryContext(ctx, `
		SELECT candidate.payload
		FROM spans AS candidate
		WHERE candidate.trace_id IN (
			SELECT marker.trace_id
			FROM spans AS marker
			WHERE marker.execution_id = ?
		)
		ORDER BY candidate.arrived_at_unix_nano, candidate.trace_id, candidate.span_id`, executionID)
	if err != nil {
		return nil, fmt.Errorf("query spans for execution %q: %w", executionID, err)
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil {
			spans = nil
			returned = errors.Join(returned, fmt.Errorf("close spans for execution %q: %w", executionID, closeErr))
		}
	}()

	for rows.Next() {
		var payload []byte
		if err := rows.Scan(&payload); err != nil {
			return nil, fmt.Errorf("scan span for execution %q: %w", executionID, err)
		}
		var span model.Span
		if err := json.Unmarshal(payload, &span); err != nil {
			return nil, fmt.Errorf("decode stored span for execution %q: %w", executionID, err)
		}
		spans = append(spans, span)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate spans for execution %q: %w", executionID, err)
	}
	return spans, nil
}

func encodeSpan(span model.Span) ([]byte, [sha256.Size]byte, error) {
	payload, err := json.Marshal(span)
	if err != nil {
		return nil, [sha256.Size]byte{}, fmt.Errorf("encode span %q/%q: %w", span.TraceID, span.SpanID, err)
	}
	semantic := span
	semantic.ArrivedAt = time.Time{}
	hashPayload, err := json.Marshal(semantic)
	if err != nil {
		return nil, [sha256.Size]byte{}, fmt.Errorf("encode semantic span %q/%q: %w", span.TraceID, span.SpanID, err)
	}
	return payload, sha256.Sum256(hashPayload), nil
}
