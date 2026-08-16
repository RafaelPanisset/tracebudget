PRAGMA journal_mode = WAL;
PRAGMA synchronous = NORMAL;

CREATE TABLE IF NOT EXISTS spans (
    trace_id TEXT NOT NULL,
    span_id TEXT NOT NULL,
    execution_id TEXT NOT NULL,
    content_hash BLOB NOT NULL,
    payload BLOB NOT NULL,
    arrived_at_unix_nano INTEGER NOT NULL,
    PRIMARY KEY (trace_id, span_id)
);

CREATE INDEX IF NOT EXISTS spans_execution_arrival
    ON spans (execution_id, arrived_at_unix_nano);
