# TraceBudget MVP Implementation Plan

Implement this plan task by task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build a Linux-first CLI that records OTLP/HTTP traces for a deterministic scenario, commits a human-readable baseline, and blocks CI when a candidate introduces configured structural, reliability, or latency regressions.

**Architecture:** One Go binary supervises the scenario command, receives OTLP/HTTP telemetry into a bounded buffer, persists accepted spans temporarily in SQLite, assembles complete traces after the flush window, and normalizes them into a versioned YAML baseline. `record` writes that baseline; `compare` applies deterministic policies and renders terminal or Markdown output with exit codes `0`, `1`, or `2`.

**Tech Stack:** Go 1.26.x; Cobra v1.10.2; OTLP protobuf v1.11.0; protobuf-go v1.36.12; modernc SQLite v1.56.0; go-yaml v3.0.5; OpenTelemetry Go v1.45.0; pgx v5.10.0; PostgreSQL 18; Docker Compose; GitHub Actions.

## Global Constraints

- The module path is `github.com/RafaelPanisset/tracebudget`.
- The first release officially supports Linux amd64/arm64 and Ubuntu GitHub Actions runners.
- Accept traces only through `POST /v1/traces` using `application/x-protobuf` or `application/json`; OTLP/gRPC is out of scope.
- Bind the receiver to `127.0.0.1:0` by default. Non-loopback binding must be explicit.
- Inject `OTEL_TRACES_SAMPLER=always_on` and `OTEL_EXPORTER_OTLP_TRACES_PROTOCOL=http/protobuf` into child processes.
- Treat a marker-bearing span as an automatic root candidate only when it has no captured parent; explicit service/span selectors narrow that set.
- Default values are: `runs=1`, `expected_traces_per_run=1`, `flush_timeout=5s`, `quiet_period=500ms`, `buffer_spans=10000`, and `command_timeout=5m`.
- Store baselines at `.tracebudget/<scenario>.yaml`; validate scenario names with `^[a-z0-9][a-z0-9-]{0,62}$`.
- Exit code `0` means pass, `1` means a policy regression, and `2` means the comparison evidence is untrustworthy or the tool/child command failed.
- Never silently discard an accepted span. Overload, malformed data, conflicting duplicates, persistence failures, incomplete required traces, and insufficient latency samples invalidate the execution.
- Do not persist arbitrary attribute values. Only `service.name`, TraceBudget markers, and explicitly allowlisted attributes may enter a baseline.
- Keep raw SQLite artifacts only when `--keep-artifacts` is explicit; otherwise remove them after the command.
- `--verbose` emits newline-delimited JSON lifecycle diagnostics; normal output remains stable and human-readable.
- Use the Go standard library for assertions and logging; do not add a test assertion framework.
- This plan implements the first release only. `trip-reality-check` dogfooding and distributed ingestion are separate follow-up work after the benchmark report.
- Follow TDD for every behavior: observe the focused test fail, implement the minimum behavior, observe it pass, then run `go test ./...` before committing.

---

## File and Package Map

| Path | Responsibility |
| --- | --- |
| `cmd/tracebudget/main.go` | Translate the root command result into `os.Exit`; no domain logic. |
| `internal/model/` | Shared immutable domain types, duration encoding, scenario validation, outcomes, and findings. |
| `internal/diagnostics/` | Thread-safe ingestion counters and irreversible integrity-failure state. |
| `internal/ingest/` | Bounded span buffer, OTLP decoding, and the HTTP receiver. |
| `internal/store/sqlite/` | Temporary span persistence, exact-duplicate detection, and conflicting-duplicate rejection. |
| `internal/assemble/` | Root discovery, run correlation, trace completeness, and missing-parent checks. |
| `internal/analyze/` | Deterministic node/edge normalization, counts, errors, and nearest-rank percentiles. |
| `internal/baseline/` | Schema-v1 YAML types, strict parsing, default policies, and atomic writes. |
| `internal/compare/` | Baseline-versus-candidate policy evaluation. |
| `internal/report/` | Terminal and Markdown renderers plus outcome-to-exit-code mapping. |
| `internal/supervisor/` | Linux process groups, environment merging, timeout, cancellation, and child status. |
| `internal/capture/` | End-to-end orchestration of receiver, drain worker, child runs, flush, and cleanup. |
| `internal/app/` | High-level `Record` and `Compare` use cases consumed by the CLI. |
| `internal/cli/` | Cobra command construction, flags, argument validation, and dependency injection. |
| `demo/` | Two instrumented services, PostgreSQL, scenario driver, and controlled regressions. |
| `test/e2e/` | Black-box CLI and Docker Compose acceptance tests. |
| `.github/workflows/` | Unit/race/E2E CI and Linux release builds. |
| `action.yml` | Composite action that installs a verified release and invokes `compare`. |
| `docs/quickstart.md` | Public setup and first baseline/compare workflow. |
| `docs/benchmarks/` | Reproducible benchmark method, environment, and raw output. |

## Task 1: Establish the Domain Contract and Go Module

**Files:**
- Create: `go.mod`
- Create: `internal/model/duration.go`
- Create: `internal/model/duration_test.go`
- Create: `internal/model/types.go`
- Create: `internal/model/types_test.go`

**Interfaces:**
- Produces: `model.Span`, `model.NodeKey`, `model.EdgeKey`, `model.Duration`, `model.Severity`, `model.Finding`, `model.Outcome`, `model.RootSelector`, `model.CaptureConfig`, and `model.ValidateScenarioName(string) error`.
- Consumes: only Go standard-library types.

- [ ] **Step 1: Create the module and write failing domain tests**

```go
// internal/model/duration_test.go
package model

import (
    "testing"
    "time"
)

func TestDurationTextRoundTrip(t *testing.T) {
    original := Duration(600 * time.Millisecond)
    text, err := original.MarshalText()
    if err != nil {
        t.Fatal(err)
    }
    if string(text) != "600ms" {
        t.Fatalf("got %q", text)
    }

    var decoded Duration
    if err := decoded.UnmarshalText(text); err != nil {
        t.Fatal(err)
    }
    if decoded != original {
        t.Fatalf("got %s, want %s", decoded, original)
    }
}
```

```go
// internal/model/types_test.go
package model

import "testing"

func TestValidateScenarioName(t *testing.T) {
    for _, valid := range []string{"checkout", "trip-plan", "a1"} {
        if err := ValidateScenarioName(valid); err != nil {
            t.Fatalf("%q: %v", valid, err)
        }
    }
    for _, invalid := range []string{"Trip Plan", "-checkout", "checkout_1", ""} {
        if err := ValidateScenarioName(invalid); err == nil {
            t.Fatalf("expected %q to fail", invalid)
        }
    }
}

func TestNodeKeyStringIsStable(t *testing.T) {
    key := NodeKey{Service: "planner", Name: "provider.search", Kind: SpanKindClient}
    if got := key.String(); got != "planner/provider.search/CLIENT" {
        t.Fatalf("got %q", got)
    }
}
```

```go
// go.mod
module github.com/RafaelPanisset/tracebudget

go 1.26
```

- [ ] **Step 2: Run the focused tests and verify they fail**

Run: `go test ./internal/model -run 'Test(Duration|Validate|Node)' -v`  
Expected: FAIL because `Duration`, `NodeKey`, and `ValidateScenarioName` do not exist.

- [ ] **Step 3: Implement the shared domain types**

```go
// internal/model/duration.go
package model

import "time"

type Duration time.Duration

func (d Duration) MarshalText() ([]byte, error) {
    return []byte(time.Duration(d).String()), nil
}

func (d *Duration) UnmarshalText(text []byte) error {
    parsed, err := time.ParseDuration(string(text))
    if err != nil {
        return err
    }
    *d = Duration(parsed)
    return nil
}
```

```go
// internal/model/types.go
package model

import (
    "fmt"
    "regexp"
    "time"
)

type SpanKind string

const (
    SpanKindUnspecified SpanKind = "UNSPECIFIED"
    SpanKindInternal    SpanKind = "INTERNAL"
    SpanKindServer      SpanKind = "SERVER"
    SpanKindClient      SpanKind = "CLIENT"
    SpanKindProducer    SpanKind = "PRODUCER"
    SpanKindConsumer    SpanKind = "CONSUMER"
)

type StatusCode string

const (
    StatusUnset StatusCode = "UNSET"
    StatusOK    StatusCode = "OK"
    StatusError StatusCode = "ERROR"
)

type Span struct {
    TraceID           string
    SpanID            string
    ParentSpanID      string
    ServiceName       string
    Name              string
    Kind              SpanKind
    Status            StatusCode
    HasException      bool
    StartUnixNano     uint64
    EndUnixNano       uint64
    ArrivedAt         time.Time
    ExecutionID       string
    RunID             string
    AllowedAttributes map[string]string
}

type NodeKey struct {
    Service    string   `yaml:"service"`
    Name       string   `yaml:"name"`
    Kind       SpanKind `yaml:"kind"`
    Attributes string   `yaml:"attributes,omitempty"`
}

func (k NodeKey) String() string {
    value := fmt.Sprintf("%s/%s/%s", k.Service, k.Name, k.Kind)
    if k.Attributes != "" {
        value += "[" + k.Attributes + "]"
    }
    return value
}

type EdgeKey struct {
    Parent NodeKey `yaml:"parent"`
    Child  NodeKey `yaml:"child"`
}

type Severity string

const (
    SeverityIgnore Severity = "ignore"
    SeverityWarn   Severity = "warn"
    SeverityFail   Severity = "fail"
)

type Outcome string

const (
    OutcomePass  Outcome = "pass"
    OutcomeFail  Outcome = "fail"
    OutcomeError Outcome = "error"
)

type Finding struct {
    Code      string
    Severity  Severity
    Subject   string
    Baseline  string
    Candidate string
    Message   string
}

type RootSelector struct {
    Service        string `yaml:"service"`
    Span           string `yaml:"span"`
    ExpectedPerRun int    `yaml:"expected_per_run"`
}

type CaptureConfig struct {
    FlushTimeout      time.Duration
    QuietPeriod       time.Duration
    BufferSpans       int
    CommandTimeout    time.Duration
    KeepArtifacts     bool
    ListenAddress     string
    ExportEndpoint    string
    AllowedAttributes []string
    Verbose           bool
}

var scenarioName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

func ValidateScenarioName(value string) error {
    if !scenarioName.MatchString(value) {
        return fmt.Errorf("scenario must match %s", scenarioName.String())
    }
    return nil
}
```

- [ ] **Step 4: Run the model tests and the full suite**

Run: `gofmt -w internal/model && go test ./internal/model -v && go test ./...`  
Expected: PASS.

- [ ] **Step 5: Commit the domain contract**

```bash
git add go.mod internal/model
git commit -m "feat: define TraceBudget domain model"
```

## Task 2: Add Integrity Diagnostics and an Atomic Bounded Buffer

**Files:**
- Create: `internal/diagnostics/tracker.go`
- Create: `internal/diagnostics/tracker_test.go`
- Create: `internal/ingest/buffer.go`
- Create: `internal/ingest/buffer_test.go`

**Interfaces:**
- Consumes: `model.Span` from Task 1.
- Produces: `diagnostics.Tracker.MarkIntegrityFailure(string)`, `diagnostics.Tracker.Snapshot()`, `ingest.Buffer.Offer([]model.Span) error`, `ingest.Buffer.Take(context.Context, int) ([]model.Span, error)`, and `ingest.Buffer.Close()`.

- [ ] **Step 1: Write failing tests for atomic overload and irreversible integrity failure**

```go
// internal/ingest/buffer_test.go
package ingest

import (
    "context"
    "errors"
    "testing"

    "github.com/RafaelPanisset/tracebudget/internal/diagnostics"
    "github.com/RafaelPanisset/tracebudget/internal/model"
)

func TestBufferRejectsWholeBatchWhenCapacityWouldBeExceeded(t *testing.T) {
    tracker := diagnostics.NewTracker()
    buffer := NewBuffer(2, tracker)

    if err := buffer.Offer([]model.Span{{SpanID: "1"}}); err != nil {
        t.Fatal(err)
    }
    err := buffer.Offer([]model.Span{{SpanID: "2"}, {SpanID: "3"}})
    if !errors.Is(err, ErrOverloaded) {
        t.Fatalf("got %v", err)
    }

    batch, err := buffer.Take(context.Background(), 10)
    if err != nil {
        t.Fatal(err)
    }
    if len(batch) != 1 || batch[0].SpanID != "1" {
        t.Fatalf("partial batch was accepted: %#v", batch)
    }
    if !tracker.Snapshot().IntegrityFailed {
        t.Fatal("overload must invalidate the execution")
    }
}
```

```go
// internal/diagnostics/tracker_test.go
package diagnostics

import "testing"

func TestIntegrityFailureCannotBeCleared(t *testing.T) {
    tracker := NewTracker()
    tracker.MarkIntegrityFailure("overload")
    tracker.AddReceived(4)
    tracker.AddSQLiteWriteDuration(25 * time.Millisecond)
    snapshot := tracker.Snapshot()
    if !snapshot.IntegrityFailed || snapshot.Received != 4 {
        t.Fatalf("unexpected snapshot: %#v", snapshot)
    }
    if len(snapshot.IntegrityReasons) != 1 || snapshot.IntegrityReasons[0] != "overload" {
        t.Fatalf("unexpected reasons: %#v", snapshot.IntegrityReasons)
    }
    if snapshot.SQLiteWriteDuration != 25*time.Millisecond {
        t.Fatalf("unexpected write duration: %s", snapshot.SQLiteWriteDuration)
    }
}
```

- [ ] **Step 2: Run the focused tests and verify they fail**

Run: `go test ./internal/diagnostics ./internal/ingest -run 'Test(Buffer|Integrity)' -v`  
Expected: FAIL because the tracker and buffer are undefined.

- [ ] **Step 3: Implement the tracker and mutex-protected queue**

```go
// internal/diagnostics/tracker.go
package diagnostics

import (
    "sync"
    "time"
)

type Snapshot struct {
    Received         int64
    Duplicates       int64
    Invalid          int64
    Incomplete       int64
    Unmatched        int64
    CurrentBuffer    int
    PeakBuffer       int
    SQLiteWriteDuration time.Duration
    AssemblyDuration    time.Duration
    NormalizationDuration time.Duration
    ComparisonDuration  time.Duration
    IntegrityFailed  bool
    IntegrityReasons []string
}

type Tracker struct {
    mu       sync.Mutex
    snapshot Snapshot
}

func NewTracker() *Tracker { return &Tracker{} }

func (t *Tracker) AddReceived(value int) {
    t.mu.Lock()
    defer t.mu.Unlock()
    t.snapshot.Received += int64(value)
}

func (t *Tracker) SetBufferDepth(value int) {
    t.mu.Lock()
    defer t.mu.Unlock()
    t.snapshot.CurrentBuffer = value
    if value > t.snapshot.PeakBuffer {
        t.snapshot.PeakBuffer = value
    }
}

func (t *Tracker) AddDuplicates(value int) { t.add(&t.snapshot.Duplicates, value) }
func (t *Tracker) AddInvalid(value int)    { t.add(&t.snapshot.Invalid, value) }
func (t *Tracker) AddIncomplete(value int) { t.add(&t.snapshot.Incomplete, value) }
func (t *Tracker) AddUnmatched(value int)  { t.add(&t.snapshot.Unmatched, value) }

func (t *Tracker) AddSQLiteWriteDuration(value time.Duration) {
    t.mu.Lock()
    defer t.mu.Unlock()
    t.snapshot.SQLiteWriteDuration += value
}

func (t *Tracker) add(target *int64, value int) {
    t.mu.Lock()
    defer t.mu.Unlock()
    *target += int64(value)
}

func (t *Tracker) MarkIntegrityFailure(reason string) {
    t.mu.Lock()
    defer t.mu.Unlock()
    t.snapshot.IntegrityFailed = true
    t.snapshot.IntegrityReasons = append(t.snapshot.IntegrityReasons, reason)
}

func (t *Tracker) Snapshot() Snapshot {
    t.mu.Lock()
    defer t.mu.Unlock()
    copy := t.snapshot
    copy.IntegrityReasons = append([]string(nil), t.snapshot.IntegrityReasons...)
    return copy
}
```

```go
// internal/ingest/buffer.go
package ingest

import (
    "context"
    "errors"
    "sync"

    "github.com/RafaelPanisset/tracebudget/internal/diagnostics"
    "github.com/RafaelPanisset/tracebudget/internal/model"
)

var (
    ErrClosed     = errors.New("span buffer closed")
    ErrOverloaded = errors.New("span buffer capacity exceeded")
)

type Buffer struct {
    mu      sync.Mutex
    queue   []model.Span
    limit   int
    closed  bool
    wake    chan struct{}
    tracker *diagnostics.Tracker
}

func NewBuffer(limit int, tracker *diagnostics.Tracker) *Buffer {
    return &Buffer{limit: limit, wake: make(chan struct{}, 1), tracker: tracker}
}

func (b *Buffer) Offer(batch []model.Span) error {
    b.mu.Lock()
    defer b.mu.Unlock()
    if b.closed {
        return ErrClosed
    }
    if len(b.queue)+len(batch) > b.limit {
        b.tracker.MarkIntegrityFailure("ingestion buffer overloaded")
        return ErrOverloaded
    }
    b.queue = append(b.queue, append([]model.Span(nil), batch...)...)
    b.tracker.AddReceived(len(batch))
    b.tracker.SetBufferDepth(len(b.queue))
    select {
    case b.wake <- struct{}{}:
    default:
    }
    return nil
}

func (b *Buffer) Take(ctx context.Context, maximum int) ([]model.Span, error) {
    for {
        b.mu.Lock()
        if len(b.queue) > 0 {
            count := maximum
            if count > len(b.queue) {
                count = len(b.queue)
            }
            result := append([]model.Span(nil), b.queue[:count]...)
            b.queue = append(b.queue[:0], b.queue[count:]...)
            b.tracker.SetBufferDepth(len(b.queue))
            b.mu.Unlock()
            return result, nil
        }
        if b.closed {
            b.mu.Unlock()
            return nil, ErrClosed
        }
        b.mu.Unlock()

        select {
        case <-ctx.Done():
            return nil, ctx.Err()
        case <-b.wake:
        }
    }
}

func (b *Buffer) Close() {
    b.mu.Lock()
    b.closed = true
    b.mu.Unlock()
    select {
    case b.wake <- struct{}{}:
    default:
    }
}
```

- [ ] **Step 4: Add close, cancellation, and race tests**

```go
func TestBufferDrainsBeforeClosed(t *testing.T) {
    buffer := NewBuffer(2, diagnostics.NewTracker())
    if err := buffer.Offer([]model.Span{{SpanID: "1"}}); err != nil {
        t.Fatal(err)
    }
    buffer.Close()
    batch, err := buffer.Take(context.Background(), 2)
    if err != nil || len(batch) != 1 {
        t.Fatalf("batch=%#v err=%v", batch, err)
    }
    if _, err := buffer.Take(context.Background(), 2); !errors.Is(err, ErrClosed) {
        t.Fatalf("got %v", err)
    }
}

func TestBufferTakeHonorsContext(t *testing.T) {
    buffer := NewBuffer(1, diagnostics.NewTracker())
    ctx, cancel := context.WithCancel(context.Background())
    cancel()
    if _, err := buffer.Take(ctx, 1); !errors.Is(err, context.Canceled) {
        t.Fatalf("got %v", err)
    }
}

func TestBufferConcurrentOfferAndTake(t *testing.T) {
    tracker := diagnostics.NewTracker()
    buffer := NewBuffer(1000, tracker)
    const total = 500
    done := make(chan error, 1)
    go func() {
        received := 0
        for received < total {
            batch, err := buffer.Take(context.Background(), 17)
            if err != nil {
                done <- err
                return
            }
            received += len(batch)
        }
        done <- nil
    }()
    for index := 0; index < total; index++ {
        if err := buffer.Offer([]model.Span{{SpanID: strconv.Itoa(index)}}); err != nil {
            t.Fatal(err)
        }
    }
    if err := <-done; err != nil {
        t.Fatal(err)
    }
    buffer.Close()
    if tracker.Snapshot().Received != total {
        t.Fatalf("got %d", tracker.Snapshot().Received)
    }
}
```

- [ ] **Step 5: Run the buffer suite with the race detector**

Run: `gofmt -w internal/diagnostics internal/ingest && go test -race ./internal/diagnostics ./internal/ingest -v`  
Expected: PASS with no race report.

- [ ] **Step 6: Commit the bounded-ingestion primitive**

```bash
git add internal/diagnostics internal/ingest
git commit -m "feat: add bounded span ingestion buffer"
```

## Task 3: Receive and Decode OTLP/HTTP Traces

**Files:**
- Create: `internal/ingest/decoder.go`
- Create: `internal/ingest/decoder_test.go`
- Create: `internal/ingest/receiver.go`
- Create: `internal/ingest/receiver_test.go`
- Modify: `go.mod`
- Create: `go.sum`

**Interfaces:**
- Consumes: `ingest.Buffer.Offer`, `diagnostics.Tracker`, and `model.Span`.
- Produces: `ingest.Decoder.Decode(contentType string, body []byte, arrivedAt time.Time) ([]model.Span, error)`, `ingest.Receiver.Start() (string, error)`, and `ingest.Receiver.Shutdown(context.Context) error`.

- [ ] **Step 1: Pin protobuf dependencies and write failing decoder tests**

Run:

```bash
go get go.opentelemetry.io/proto/otlp@v1.11.0
go get google.golang.org/protobuf@v1.36.12
```

Construct an `ExportTraceServiceRequest` with service `planner`, one root span, one client child, an `ERROR` status, and an `exception` event:

```go
func fixtureRequest(t *testing.T) *collectortracepb.ExportTraceServiceRequest {
    t.Helper()
    traceID, _ := hex.DecodeString("00112233445566778899aabbccddeeff")
    rootID, _ := hex.DecodeString("0011223344556677")
    childID, _ := hex.DecodeString("8899aabbccddeeff")
    stringValue := func(value string) *commonpb.AnyValue {
        return &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: value}}
    }
    return &collectortracepb.ExportTraceServiceRequest{ResourceSpans: []*tracepb.ResourceSpans{{
        Resource: &resourcepb.Resource{Attributes: []*commonpb.KeyValue{
            {Key: "service.name", Value: stringValue("planner")},
            {Key: "tracebudget.execution_id", Value: stringValue("exec-1")},
            {Key: "tracebudget.run_id", Value: stringValue("run-1")},
        }},
        ScopeSpans: []*tracepb.ScopeSpans{{Spans: []*tracepb.Span{
            {TraceId: traceID, SpanId: rootID, Name: "trip.plan", Kind: tracepb.Span_SPAN_KIND_SERVER, StartTimeUnixNano: 1, EndTimeUnixNano: 2},
            {
                TraceId: traceID, SpanId: childID, ParentSpanId: rootID,
                Name: "provider.search", Kind: tracepb.Span_SPAN_KIND_CLIENT,
                StartTimeUnixNano: 2, EndTimeUnixNano: 3,
                Status: &tracepb.Status{Code: tracepb.Status_STATUS_CODE_ERROR},
                Events: []*tracepb.Span_Event{{Name: "exception"}},
                Attributes: []*commonpb.KeyValue{
                    {Key: "safe.number", Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_IntValue{IntValue: 7}}},
                    {Key: "secret", Value: stringValue("do-not-store")},
                },
            },
        }}},
    }}}
}
```

Test both encodings:

```go
func TestDecoderAcceptsProtobufAndJSON(t *testing.T) {
    request := fixtureRequest(t)
    binaryBody, _ := proto.Marshal(request)
    jsonBody, _ := protojson.Marshal(request)

    tests := []struct {
        contentType string
        body        []byte
    }{
        {"application/x-protobuf", binaryBody},
        {"application/json", jsonBody},
    }

    for _, test := range tests {
        spans, err := NewDecoder(nil).Decode(test.contentType, test.body, time.Unix(10, 0))
        if err != nil {
            t.Fatal(err)
        }
        if len(spans) != 2 || spans[1].Status != model.StatusError || !spans[1].HasException {
            t.Fatalf("unexpected spans: %#v", spans)
        }
    }
}

func TestDecoderKeepsOnlyAllowlistedScalarAttributes(t *testing.T) {
    body, _ := proto.Marshal(fixtureRequest(t))
    spans, err := NewDecoder([]string{"safe.number"}).Decode("application/x-protobuf", body, time.Unix(10, 0))
    if err != nil {
        t.Fatal(err)
    }
    if !reflect.DeepEqual(spans[1].AllowedAttributes, map[string]string{"safe.number": "7"}) {
        t.Fatalf("got %#v", spans[1].AllowedAttributes)
    }
}
```

- [ ] **Step 2: Run the decoder test and verify it fails**

Run: `go test ./internal/ingest -run TestDecoderAcceptsProtobufAndJSON -v`  
Expected: FAIL because `Decoder` is undefined.

- [ ] **Step 3: Implement strict decoding and model conversion**

```go
// internal/ingest/decoder.go
package ingest

import (
    "encoding/hex"
    "fmt"
    "time"

    collectortracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
    commonpb "go.opentelemetry.io/proto/otlp/common/v1"
    tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
    "google.golang.org/protobuf/encoding/protojson"
    "google.golang.org/protobuf/proto"

    "github.com/RafaelPanisset/tracebudget/internal/model"
)

type Decoder struct {
    allowed map[string]struct{}
}

func NewDecoder(allowed []string) *Decoder {
    values := make(map[string]struct{}, len(allowed))
    for _, value := range allowed {
        values[value] = struct{}{}
    }
    return &Decoder{allowed: values}
}

func (d *Decoder) Decode(contentType string, body []byte, arrivedAt time.Time) ([]model.Span, error) {
    request := new(collectortracepb.ExportTraceServiceRequest)
    switch contentType {
    case "application/x-protobuf":
        if err := proto.Unmarshal(body, request); err != nil {
            return nil, fmt.Errorf("decode OTLP protobuf: %w", err)
        }
    case "application/json":
        if err := protojson.Unmarshal(body, request); err != nil {
            return nil, fmt.Errorf("decode OTLP JSON: %w", err)
        }
    default:
        return nil, fmt.Errorf("unsupported content type %q", contentType)
    }

    var result []model.Span
    for _, resourceSpans := range request.ResourceSpans {
        resource := attributes(resourceSpans.GetResource().GetAttributes())
        service := resource["service.name"]
        for _, scopeSpans := range resourceSpans.ScopeSpans {
            for _, span := range scopeSpans.Spans {
                converted, err := d.convert(service, resource, span, arrivedAt)
                if err != nil {
                    return nil, err
                }
                result = append(result, converted)
            }
        }
    }
    return result, nil
}

func (d *Decoder) convert(service string, resource map[string]string, span *tracepb.Span, arrivedAt time.Time) (model.Span, error) {
    if len(span.TraceId) != 16 || len(span.SpanId) != 8 {
        return model.Span{}, fmt.Errorf("invalid trace or span identity")
    }
    if span.EndTimeUnixNano < span.StartTimeUnixNano {
        return model.Span{}, fmt.Errorf("negative span duration")
    }
    allowed := map[string]string{}
    for _, attribute := range span.Attributes {
        if _, ok := d.allowed[attribute.Key]; !ok {
            continue
        }
        value, ok := scalarText(attribute.Value)
        if !ok {
            return model.Span{}, fmt.Errorf("allowlisted attribute %q is not a scalar", attribute.Key)
        }
        allowed[attribute.Key] = value
    }
    return model.Span{
        TraceID: hex.EncodeToString(span.TraceId), SpanID: hex.EncodeToString(span.SpanId),
        ParentSpanID: hex.EncodeToString(span.ParentSpanId), ServiceName: service,
        Name: span.Name, Kind: spanKind(span.Kind), Status: statusCode(span.GetStatus().GetCode()),
        HasException: hasException(span.Events), StartUnixNano: span.StartTimeUnixNano,
        EndUnixNano: span.EndTimeUnixNano, ArrivedAt: arrivedAt,
        ExecutionID: resource["tracebudget.execution_id"], RunID: resource["tracebudget.run_id"],
        AllowedAttributes: allowed,
    }, nil
}

func attributes(values []*commonpb.KeyValue) map[string]string {
    result := map[string]string{}
    for _, value := range values {
        result[value.Key] = value.GetValue().GetStringValue()
    }
    return result
}

func scalarText(value *commonpb.AnyValue) (string, bool) {
    switch typed := value.GetValue().(type) {
    case *commonpb.AnyValue_StringValue:
        return typed.StringValue, true
    case *commonpb.AnyValue_BoolValue:
        return strconv.FormatBool(typed.BoolValue), true
    case *commonpb.AnyValue_IntValue:
        return strconv.FormatInt(typed.IntValue, 10), true
    case *commonpb.AnyValue_DoubleValue:
        return strconv.FormatFloat(typed.DoubleValue, 'g', -1, 64), true
    case *commonpb.AnyValue_BytesValue:
        return hex.EncodeToString(typed.BytesValue), true
    default:
        return "", false
    }
}
```

```go
func spanKind(value tracepb.Span_SpanKind) model.SpanKind {
    switch value {
    case tracepb.Span_SPAN_KIND_INTERNAL:
        return model.SpanKindInternal
    case tracepb.Span_SPAN_KIND_SERVER:
        return model.SpanKindServer
    case tracepb.Span_SPAN_KIND_CLIENT:
        return model.SpanKindClient
    case tracepb.Span_SPAN_KIND_PRODUCER:
        return model.SpanKindProducer
    case tracepb.Span_SPAN_KIND_CONSUMER:
        return model.SpanKindConsumer
    default:
        return model.SpanKindUnspecified
    }
}

func statusCode(value tracepb.Status_StatusCode) model.StatusCode {
    switch value {
    case tracepb.Status_STATUS_CODE_OK:
        return model.StatusOK
    case tracepb.Status_STATUS_CODE_ERROR:
        return model.StatusError
    default:
        return model.StatusUnset
    }
}

func hasException(events []*tracepb.Span_Event) bool {
    for _, event := range events {
        if event.GetName() == "exception" {
            return true
        }
    }
    return false
}
```

- [ ] **Step 4: Write receiver tests before implementing the HTTP server**

```go
type fakeSink struct{ err error }

func (sink fakeSink) Offer([]model.Span) error { return sink.err }

func TestReceiverStatusContract(t *testing.T) {
    protobufBody, _ := proto.Marshal(fixtureRequest(t))
    jsonBody, _ := protojson.Marshal(fixtureRequest(t))
    oversized := bytes.Repeat([]byte("x"), (16<<20)+1)
    tests := []struct {
        name, method, path, contentType string
        body                            []byte
        sinkError                       error
        status                          int
    }{
        {"protobuf", http.MethodPost, "/v1/traces", "application/x-protobuf", protobufBody, nil, 200},
        {"json", http.MethodPost, "/v1/traces", "application/json; charset=utf-8", jsonBody, nil, 200},
        {"wrong path", http.MethodPost, "/wrong", "application/x-protobuf", protobufBody, nil, 404},
        {"unsupported", http.MethodPost, "/v1/traces", "text/plain", protobufBody, nil, 415},
        {"malformed", http.MethodPost, "/v1/traces", "application/x-protobuf", []byte("bad"), nil, 400},
        {"oversized", http.MethodPost, "/v1/traces", "application/x-protobuf", oversized, nil, 413},
        {"overloaded", http.MethodPost, "/v1/traces", "application/x-protobuf", protobufBody, ErrOverloaded, 503},
    }
    for _, test := range tests {
        t.Run(test.name, func(t *testing.T) {
            tracker := diagnostics.NewTracker()
            receiver := NewReceiver("127.0.0.1:0", NewDecoder(nil), fakeSink{err: test.sinkError}, tracker)
            request := httptest.NewRequest(test.method, test.path, bytes.NewReader(test.body))
            request.Header.Set("Content-Type", test.contentType)
            response := httptest.NewRecorder()
            receiver.server.Handler.ServeHTTP(response, request)
            if response.Code != test.status {
                t.Fatalf("got %d body=%q", response.Code, response.Body.String())
            }
            if test.sinkError != nil && !tracker.Snapshot().IntegrityFailed {
                t.Fatal("sink rejection must invalidate capture integrity")
            }
        })
    }
}
```

Run: `go test ./internal/ingest -run TestReceiver -v`  
Expected: FAIL because `Receiver` is undefined.

- [ ] **Step 5: Implement the receiver and OTLP response**

```go
type SpanSink interface {
    Offer([]model.Span) error
}

type Receiver struct {
    bind      string
    decoder   *Decoder
    sink      SpanSink
    tracker   *diagnostics.Tracker
    listener  net.Listener
    server    *http.Server
    maxBody   int64
}

func NewReceiver(bind string, decoder *Decoder, sink SpanSink, tracker *diagnostics.Tracker) *Receiver {
    receiver := &Receiver{bind: bind, decoder: decoder, sink: sink, tracker: tracker, maxBody: 16 << 20}
    mux := http.NewServeMux()
    mux.HandleFunc("POST /v1/traces", receiver.handleTraces)
    receiver.server = &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
    return receiver
}

func (r *Receiver) Start() (string, error) {
    listener, err := net.Listen("tcp", r.bind)
    if err != nil {
        return "", err
    }
    r.listener = listener
    go func() { _ = r.server.Serve(listener) }()
    return "http://" + listener.Addr().String() + "/v1/traces", nil
}

func (r *Receiver) Shutdown(ctx context.Context) error {
    return r.server.Shutdown(ctx)
}

func (r *Receiver) handleTraces(writer http.ResponseWriter, request *http.Request) {
    contentType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
    if err != nil || (contentType != "application/x-protobuf" && contentType != "application/json") {
        r.tracker.AddInvalid(1)
        r.tracker.MarkIntegrityFailure("unsupported OTLP content type")
        http.Error(writer, "unsupported OTLP content type", http.StatusUnsupportedMediaType)
        return
    }
    body, err := io.ReadAll(http.MaxBytesReader(writer, request.Body, r.maxBody))
    if err != nil {
        var tooLarge *http.MaxBytesError
        if errors.As(err, &tooLarge) {
            http.Error(writer, "OTLP request exceeds 16 MiB", http.StatusRequestEntityTooLarge)
        } else {
            http.Error(writer, "read OTLP request", http.StatusBadRequest)
        }
        r.tracker.AddInvalid(1)
        r.tracker.MarkIntegrityFailure("invalid OTLP request body")
        return
    }
    spans, err := r.decoder.Decode(contentType, body, time.Now())
    if err != nil {
        r.tracker.AddInvalid(1)
        r.tracker.MarkIntegrityFailure("malformed OTLP payload")
        http.Error(writer, err.Error(), http.StatusBadRequest)
        return
    }
    if err := r.sink.Offer(spans); err != nil {
        r.tracker.MarkIntegrityFailure(err.Error())
        http.Error(writer, "capture unavailable", http.StatusServiceUnavailable)
        return
    }
    response := new(collectortracepb.ExportTraceServiceResponse)
    var encoded []byte
    if contentType == "application/json" {
        encoded, err = protojson.Marshal(response)
    } else {
        encoded, err = proto.Marshal(response)
    }
    if err != nil {
        r.tracker.MarkIntegrityFailure(err.Error())
        http.Error(writer, "encode OTLP response", http.StatusInternalServerError)
        return
    }
    writer.Header().Set("Content-Type", contentType)
    writer.WriteHeader(http.StatusOK)
    _, _ = writer.Write(encoded)
}
```

- [ ] **Step 6: Run protocol tests, race tests, and the full suite**

Run: `gofmt -w internal/ingest && go mod tidy && go test -race ./internal/ingest -v && go test ./...`  
Expected: PASS.

- [ ] **Step 7: Commit OTLP/HTTP ingestion**

```bash
git add go.mod go.sum internal/ingest
git commit -m "feat: receive OTLP HTTP traces"
```

## Task 4: Persist Accepted Spans in SQLite

**Files:**
- Create: `internal/store/sqlite/schema.sql`
- Create: `internal/store/sqlite/store.go`
- Create: `internal/store/sqlite/store_test.go`
- Modify: `go.mod`
- Modify: `go.sum`

**Interfaces:**
- Consumes: batches of `model.Span` drained from `ingest.Buffer`.
- Produces: `sqlite.Open(path string) (*Store, error)`, `Store.WriteBatch(context.Context, []model.Span) (WriteResult, error)`, `Store.SpansForExecution(context.Context, string) ([]model.Span, error)`, and `Store.Close() error`.

- [ ] **Step 1: Pin the pure-Go SQLite driver and write failing persistence tests**

Run: `go get modernc.org/sqlite@v1.56.0`

```go
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
```

- [ ] **Step 2: Run the SQLite tests and verify they fail**

Run: `go test ./internal/store/sqlite -v`  
Expected: FAIL because `Store` is undefined.

- [ ] **Step 3: Implement the embedded schema and transactional batch writes**

```sql
-- internal/store/sqlite/schema.sql
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
```

```go
type WriteResult struct {
    Inserted   int
    Duplicates int
}

type Store struct{ database *sql.DB }

var ErrConflictingDuplicate = errors.New("same trace_id and span_id have different content")

//go:embed schema.sql
var schema string

func Open(path string) (*Store, error) {
    database, err := sql.Open("sqlite", path)
    if err != nil {
        return nil, err
    }
    database.SetMaxOpenConns(1)
    if _, err := database.Exec(schema); err != nil {
        _ = database.Close()
        return nil, err
    }
    return &Store{database: database}, nil
}

func (s *Store) Close() error { return s.database.Close() }

func (s *Store) WriteBatch(ctx context.Context, spans []model.Span) (result WriteResult, returned error) {
    transaction, err := s.database.BeginTx(ctx, nil)
    if err != nil {
        return result, err
    }
    defer func() {
        if returned != nil {
            _ = transaction.Rollback()
        }
    }()
    for _, span := range spans {
        payload, err := json.Marshal(span)
        if err != nil {
            return result, err
        }
        semantic := span
        semantic.ArrivedAt = time.Time{}
        hashPayload, err := json.Marshal(semantic)
        if err != nil {
            return result, err
        }
        hash := sha256.Sum256(hashPayload)
        inserted, err := transaction.ExecContext(ctx, `
            INSERT OR IGNORE INTO spans
                (trace_id, span_id, execution_id, content_hash, payload, arrived_at_unix_nano)
            VALUES (?, ?, ?, ?, ?, ?)`,
            span.TraceID, span.SpanID, span.ExecutionID, hash[:], payload, span.ArrivedAt.UnixNano(),
        )
        if err != nil {
            return result, err
        }
        affected, err := inserted.RowsAffected()
        if err != nil {
            return result, err
        }
        if affected == 1 {
            result.Inserted++
            continue
        }
        var storedHash []byte
        err = transaction.QueryRowContext(ctx,
            `SELECT content_hash FROM spans WHERE trace_id = ? AND span_id = ?`,
            span.TraceID, span.SpanID,
        ).Scan(&storedHash)
        if err != nil {
            return result, err
        }
        if !bytes.Equal(storedHash, hash[:]) {
            return result, ErrConflictingDuplicate
        }
        result.Duplicates++
    }
    if err := transaction.Commit(); err != nil {
        return result, err
    }
    return result, nil
}

func (s *Store) SpansForExecution(ctx context.Context, executionID string) ([]model.Span, error) {
    rows, err := s.database.QueryContext(ctx, `
        SELECT payload FROM spans
        WHERE execution_id = ?
        ORDER BY arrived_at_unix_nano, trace_id, span_id`, executionID)
    if err != nil {
        return nil, err
    }
    defer rows.Close()
    var spans []model.Span
    for rows.Next() {
        var payload []byte
        if err := rows.Scan(&payload); err != nil {
            return nil, err
        }
        var span model.Span
        if err := json.Unmarshal(payload, &span); err != nil {
            return nil, fmt.Errorf("decode stored span: %w", err)
        }
        spans = append(spans, span)
    }
    return spans, rows.Err()
}
```

- [ ] **Step 4: Add rollback, corrupt-row, and cancellation tests**

```go
func TestWriteBatchRollsBackOnConflict(t *testing.T) {
    store := openTestStore(t)
    original := model.Span{TraceID: "trace", SpanID: "span", ExecutionID: "exec", Name: "original"}
    if _, err := store.WriteBatch(context.Background(), []model.Span{original}); err != nil {
        t.Fatal(err)
    }
    changed := original
    changed.Name = "changed"
    newSpan := model.Span{TraceID: "new", SpanID: "new", ExecutionID: "exec", Name: "new"}
    if _, err := store.WriteBatch(context.Background(), []model.Span{newSpan, changed}); !errors.Is(err, ErrConflictingDuplicate) {
        t.Fatalf("got %v", err)
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
    if _, err := store.SpansForExecution(context.Background(), "exec"); err == nil {
        t.Fatal("expected corrupt JSON to fail")
    }
}

func TestWriteBatchHonorsCancelledContext(t *testing.T) {
    store := openTestStore(t)
    ctx, cancel := context.WithCancel(context.Background())
    cancel()
    _, err := store.WriteBatch(ctx, []model.Span{{TraceID: "trace", SpanID: "span"}})
    if !errors.Is(err, context.Canceled) {
        t.Fatalf("got %v", err)
    }
}
```

Run: `gofmt -w internal/store/sqlite && go mod tidy && go test -race ./internal/store/sqlite -v && go test ./...`  
Expected: PASS.

- [ ] **Step 5: Commit temporary persistence**

```bash
git add go.mod go.sum internal/store/sqlite
git commit -m "feat: persist captured spans in sqlite"
```

## Task 5: Assemble Complete Scenario Traces

**Files:**
- Create: `internal/assemble/assembler.go`
- Create: `internal/assemble/assembler_test.go`

**Interfaces:**
- Consumes: all accepted spans for one execution from `Store.SpansForExecution`.
- Produces: `assemble.Assemble(spans []model.Span, config Config) (Result, error)`, where `Result` contains the selected `model.RootSelector`, complete traces, incomplete count, and unmatched count.

- [ ] **Step 1: Write failing root-discovery and correlation tests**

```go
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
    if len(result.Traces) != 1 || len(result.Traces[0].Spans) != 2 {
        t.Fatalf("unexpected result: %#v", result)
    }
    if result.Root.Service != "planner" || result.Root.Span != "trip.plan" {
        t.Fatalf("unexpected root: %#v", result.Root)
    }
}
```

```go
func TestAssembleRejectsZeroAndAmbiguousRoots(t *testing.T) {
    ended := time.Unix(20, 0)
    _, err := Assemble(nil, Config{ExecutionID: "exec", CaptureEndedAt: ended, ExpectedRuns: []string{"run-1"}, ExpectedPerRun: 1})
    if !errors.Is(err, ErrRootNotFound) {
        t.Fatalf("zero roots: %v", err)
    }
    spans := []model.Span{
        {TraceID: "t1", SpanID: "a", ServiceName: "one", Name: "root", ExecutionID: "exec", RunID: "run-1", EndUnixNano: 1, ArrivedAt: ended.Add(-time.Second)},
        {TraceID: "t2", SpanID: "b", ServiceName: "two", Name: "root", ExecutionID: "exec", RunID: "run-1", EndUnixNano: 1, ArrivedAt: ended.Add(-time.Second)},
    }
    _, err = Assemble(spans, Config{ExecutionID: "exec", CaptureEndedAt: ended, ExpectedRuns: []string{"run-1"}, ExpectedPerRun: 1})
    if !errors.Is(err, ErrAmbiguousRoot) {
        t.Fatalf("ambiguous roots: %v", err)
    }
}

func TestAssembleExplicitSelectorAndExpectedCount(t *testing.T) {
    ended := time.Unix(20, 0)
    spans := []model.Span{
        markedAssembleRoot("t1", "a", "run-1", ended),
        markedAssembleRoot("t2", "b", "run-1", ended),
        {TraceID: "unrelated", SpanID: "x", ServiceName: "other", Name: "noise", EndUnixNano: 1, ArrivedAt: ended.Add(-time.Second)},
    }
    result, err := Assemble(spans, Config{
        ExecutionID: "exec", Root: model.RootSelector{Service: "planner", Span: "trip.plan"},
        CaptureEndedAt: ended, QuietPeriod: 500 * time.Millisecond,
        ExpectedRuns: []string{"run-1"}, ExpectedPerRun: 2,
    })
    if err != nil {
        t.Fatal(err)
    }
    if len(result.Traces) != 2 || result.Unmatched != 1 {
        t.Fatalf("unexpected result: %#v", result)
    }
}

func TestAssembleRejectsTwoMatchingRootsInOneTrace(t *testing.T) {
    ended := time.Unix(20, 0)
    spans := []model.Span{
        markedAssembleRoot("t1", "a", "run-1", ended),
        markedAssembleRoot("t1", "b", "run-1", ended),
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

func TestAssembleClassifiesIncompleteTraces(t *testing.T) {
    ended := time.Unix(20, 0)
    tests := []struct {
        name  string
        spans []model.Span
    }{
        {"root has no end", []model.Span{{TraceID: "t", SpanID: "root", ServiceName: "planner", Name: "trip.plan", ExecutionID: "exec", RunID: "run-1", ArrivedAt: ended.Add(-time.Second)}}},
        {"late arrival", []model.Span{{TraceID: "t", SpanID: "root", ServiceName: "planner", Name: "trip.plan", ExecutionID: "exec", RunID: "run-1", EndUnixNano: 1, ArrivedAt: ended.Add(-100 * time.Millisecond)}}},
        {"missing parent", []model.Span{
            markedAssembleRoot("t", "root", "run-1", ended),
            {TraceID: "t", SpanID: "child", ParentSpanID: "absent", ServiceName: "inventory", Name: "reserve", EndUnixNano: 1, ArrivedAt: ended.Add(-time.Second)},
        }},
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
            if result.Incomplete != 1 || len(result.Traces) != 0 {
                t.Fatalf("unexpected result: %#v", result)
            }
        })
    }
}

func markedAssembleRoot(traceID, spanID, runID string, ended time.Time) model.Span {
    return model.Span{
        TraceID: traceID, SpanID: spanID, ServiceName: "planner", Name: "trip.plan",
        ExecutionID: "exec", RunID: runID, EndUnixNano: 1,
        ArrivedAt: ended.Add(-time.Second),
    }
}
```

- [ ] **Step 2: Run the assembler tests and verify they fail**

Run: `go test ./internal/assemble -v`  
Expected: FAIL because `Assemble` is undefined.

- [ ] **Step 3: Implement batch assembly after the flush window**

```go
type Config struct {
    ExecutionID    string
    Root           model.RootSelector
    CaptureEndedAt time.Time
    QuietPeriod    time.Duration
    ExpectedRuns   []string
    ExpectedPerRun int
}

type Trace struct {
    TraceID string
    RunID   string
    RootID  string
    Spans   []model.Span
}

type Result struct {
    Root       model.RootSelector
    Traces     []Trace
    Incomplete int
    Unmatched  int
}

var (
    ErrRootNotFound = errors.New("marked root not found")
    ErrAmbiguousRoot = errors.New("multiple marked root selectors")
    ErrUnexpectedRootCount = errors.New("trace contains multiple matching roots")
    ErrUnexpectedTraceCount = errors.New("unexpected complete trace count")
)

func Assemble(spans []model.Span, config Config) (Result, error) {
    byTrace := groupByTraceID(spans)
    roots := markedRoots(byTrace, config.ExecutionID, config.Root)
    selector, err := selectRoot(roots, config)
    if err != nil {
        return Result{}, err
    }

    result := Result{Root: selector}
    for traceID, traceSpans := range byTrace {
        root, rootCount := matchingRoot(traceSpans, config.ExecutionID, selector)
        if rootCount == 0 {
            result.Unmatched++
            continue
        }
        if rootCount != 1 {
            return Result{}, fmt.Errorf("%w: trace %s has %d", ErrUnexpectedRootCount, traceID, rootCount)
        }
        if !isComplete(traceSpans, root, config.CaptureEndedAt, config.QuietPeriod) {
            result.Incomplete++
            continue
        }
        result.Traces = append(result.Traces, Trace{
            TraceID: traceID, RunID: root.RunID, RootID: root.SpanID,
            Spans: append([]model.Span(nil), traceSpans...),
        })
    }
    if err := validateRunCounts(result.Traces, config.ExpectedRuns, config.ExpectedPerRun); err != nil {
        return Result{}, err
    }
    sort.Slice(result.Traces, func(i, j int) bool { return result.Traces[i].TraceID < result.Traces[j].TraceID })
    return result, nil
}

func groupByTraceID(spans []model.Span) map[string][]model.Span {
    grouped := map[string][]model.Span{}
    for _, span := range spans {
        grouped[span.TraceID] = append(grouped[span.TraceID], span)
    }
    return grouped
}

func markedRoots(grouped map[string][]model.Span, executionID string, explicit model.RootSelector) []model.Span {
    var roots []model.Span
    for _, spans := range grouped {
        for _, span := range spans {
            if span.ExecutionID != executionID || span.RunID == "" || span.ParentSpanID != "" {
                continue
            }
            if explicit.Service != "" && (span.ServiceName != explicit.Service || span.Name != explicit.Span) {
                continue
            }
            roots = append(roots, span)
        }
    }
    return roots
}

func selectRoot(roots []model.Span, config Config) (model.RootSelector, error) {
    if len(roots) == 0 {
        return model.RootSelector{}, ErrRootNotFound
    }
    selectors := map[string]model.RootSelector{}
    for _, root := range roots {
        selector := model.RootSelector{Service: root.ServiceName, Span: root.Name, ExpectedPerRun: config.ExpectedPerRun}
        selectors[root.ServiceName+"\x00"+root.Name] = selector
    }
    if len(selectors) != 1 {
        return model.RootSelector{}, ErrAmbiguousRoot
    }
    for _, selector := range selectors {
        return selector, nil
    }
    return model.RootSelector{}, ErrRootNotFound
}

func matchingRoot(spans []model.Span, executionID string, selector model.RootSelector) (model.Span, int) {
    var matched model.Span
    count := 0
    for _, span := range spans {
        if span.ExecutionID == executionID && span.RunID != "" && span.ParentSpanID == "" && span.ServiceName == selector.Service && span.Name == selector.Span {
            matched = span
            count++
        }
    }
    return matched, count
}

func isComplete(spans []model.Span, root model.Span, captureEndedAt time.Time, quietPeriod time.Duration) bool {
    if root.EndUnixNano == 0 || root.ArrivedAt.After(captureEndedAt.Add(-quietPeriod)) {
        return false
    }
    identities := make(map[string]struct{}, len(spans))
    for _, span := range spans {
        identities[span.SpanID] = struct{}{}
        if span.ArrivedAt.After(captureEndedAt.Add(-quietPeriod)) {
            return false
        }
    }
    for _, span := range spans {
        if span.SpanID == root.SpanID {
            continue
        }
        if _, exists := identities[span.ParentSpanID]; !exists {
            return false
        }
    }
    return true
}

func validateRunCounts(traces []Trace, expectedRuns []string, expectedPerRun int) error {
    if expectedPerRun == 0 {
        return nil
    }
    counts := make(map[string]int, len(expectedRuns))
    expected := make(map[string]struct{}, len(expectedRuns))
    for _, runID := range expectedRuns {
        expected[runID] = struct{}{}
    }
    for _, trace := range traces {
        if _, exists := expected[trace.RunID]; !exists {
            return fmt.Errorf("%w: unexpected run %s", ErrUnexpectedTraceCount, trace.RunID)
        }
        counts[trace.RunID]++
    }
    for _, runID := range expectedRuns {
        if counts[runID] != expectedPerRun {
            return fmt.Errorf("%w: run %s got %d, want %d", ErrUnexpectedTraceCount, runID, counts[runID], expectedPerRun)
        }
    }
    return nil
}
```

- [ ] **Step 4: Run assembler tests and the full suite**

Run: `gofmt -w internal/assemble && go test ./internal/assemble -v && go test ./...`  
Expected: PASS.

- [ ] **Step 5: Commit deterministic trace assembly**

```bash
git add internal/assemble
git commit -m "feat: assemble complete scenario traces"
```

## Task 6: Normalize Traces and Aggregate Deterministic Observations

**Files:**
- Create: `internal/analyze/types.go`
- Create: `internal/analyze/normalize.go`
- Create: `internal/analyze/normalize_test.go`
- Create: `internal/analyze/aggregate.go`
- Create: `internal/analyze/aggregate_test.go`
- Create: `internal/analyze/percentile.go`
- Create: `internal/analyze/percentile_test.go`

**Interfaces:**
- Consumes: `assemble.Trace` and assembly diagnostics from Task 5.
- Produces: `analyze.Normalize(assemble.Trace) (TraceSummary, error)`, `analyze.Aggregate([]TraceSummary, Evidence) Observation`, and `analyze.NearestRank([]model.Duration, float64) model.Duration`.

- [ ] **Step 1: Write failing normalization and percentile tests**

```go
func TestNormalizeBuildsNodesEdgesCountsAndErrors(t *testing.T) {
    trace := assemble.Trace{TraceID: "trace", RootID: "root", Spans: []model.Span{
        {SpanID: "root", ServiceName: "gateway", Name: "checkout", Kind: model.SpanKindServer},
        {SpanID: "db-1", ParentSpanID: "root", ServiceName: "gateway", Name: "db.query", Kind: model.SpanKindClient, StartUnixNano: 10, EndUnixNano: 30},
        {SpanID: "db-2", ParentSpanID: "root", ServiceName: "gateway", Name: "db.query", Kind: model.SpanKindClient, Status: model.StatusError, StartUnixNano: 40, EndUnixNano: 70},
    }}
    summary, err := Normalize(trace)
    if err != nil {
        t.Fatal(err)
    }
    db := model.NodeKey{Service: "gateway", Name: "db.query", Kind: model.SpanKindClient}
    if summary.NodeCounts[db] != 2 || summary.ErrorCounts[db] != 1 {
        t.Fatalf("unexpected summary: %#v", summary)
    }
    edge := model.EdgeKey{
        Parent: model.NodeKey{Service: "gateway", Name: "checkout", Kind: model.SpanKindServer},
        Child:  db,
    }
    if summary.EdgeCounts[edge] != 2 || len(summary.Durations[db]) != 2 {
        t.Fatalf("unexpected edge/durations: %#v", summary)
    }
}

func TestNearestRank(t *testing.T) {
    samples := []model.Duration{10, 20, 30, 40, 50, 60, 70, 80, 90, 100}
    if got := NearestRank(samples, 0.50); got != 50 {
        t.Fatalf("p50=%d", got)
    }
    if got := NearestRank(samples, 0.95); got != 100 {
        t.Fatalf("p95=%d", got)
    }
}

func TestNormalizeUsesOnlyCapturedAllowlistedAttributesAsStableDimensions(t *testing.T) {
    trace := assemble.Trace{TraceID: "trace", RootID: "root", Spans: []model.Span{{
        SpanID: "root", ServiceName: "inventory", Name: "db.query", Kind: model.SpanKindClient,
        AllowedAttributes: map[string]string{"db.system": "postgresql", "db.operation.name": "INSERT"},
    }}}
    summary, err := Normalize(trace)
    if err != nil {
        t.Fatal(err)
    }
    key := model.NodeKey{
        Service: "inventory", Name: "db.query", Kind: model.SpanKindClient,
        Attributes: "db.operation.name=INSERT&db.system=postgresql",
    }
    if summary.NodeCounts[key] != 1 {
        t.Fatalf("canonical allowlisted dimensions missing: %#v", summary.NodeCounts)
    }
}
```

- [ ] **Step 2: Run the analyzer tests and verify they fail**

Run: `go test ./internal/analyze -run 'Test(Normalize|NearestRank)' -v`  
Expected: FAIL because the analyzer API is undefined.

- [ ] **Step 3: Implement normalized per-trace summaries**

```go
// internal/analyze/types.go
package analyze

import (
    "github.com/RafaelPanisset/tracebudget/internal/model"
)

type TraceSummary struct {
    TraceID     string
    RunID       string
    NodeCounts  map[model.NodeKey]int
    EdgeCounts  map[model.EdgeKey]int
    ErrorCounts map[model.NodeKey]int
    Durations   map[model.NodeKey][]model.Duration
}

type CountRange struct {
    Min    int     `yaml:"min"`
    Max    int     `yaml:"max"`
    Median float64 `yaml:"median"`
}

type DurationSummary struct {
    Samples int            `yaml:"samples"`
    P50     model.Duration `yaml:"p50"`
    P95     model.Duration `yaml:"p95"`
}

type NodeObservation struct {
    Key       model.NodeKey `yaml:"key"`
    Counts    CountRange    `yaml:"counts"`
    Total     int           `yaml:"total"`
    Errors    int           `yaml:"errors"`
    ErrorRate float64       `yaml:"error_rate"`
    Durations DurationSummary `yaml:"durations"`
}

type EdgeObservation struct {
    Key    model.EdgeKey `yaml:"key"`
    Counts CountRange    `yaml:"counts"`
}

type Evidence struct {
    Complete   int `yaml:"complete"`
    Incomplete int `yaml:"incomplete"`
    Unmatched  int `yaml:"unmatched"`
}

type Observation struct {
    Nodes    []NodeObservation `yaml:"nodes"`
    Edges    []EdgeObservation `yaml:"edges"`
    Evidence Evidence          `yaml:"evidence"`
}
```

```go
func Normalize(trace assemble.Trace) (TraceSummary, error) {
    summary := TraceSummary{
        TraceID: trace.TraceID, RunID: trace.RunID,
        NodeCounts: map[model.NodeKey]int{}, EdgeCounts: map[model.EdgeKey]int{},
        ErrorCounts: map[model.NodeKey]int{}, Durations: map[model.NodeKey][]model.Duration{},
    }
    nodesBySpanID := make(map[string]model.NodeKey, len(trace.Spans))
    for _, span := range trace.Spans {
        key := model.NodeKey{Service: span.ServiceName, Name: span.Name, Kind: span.Kind, Attributes: canonicalAttributes(span.AllowedAttributes)}
        nodesBySpanID[span.SpanID] = key
        summary.NodeCounts[key]++
        if span.Status == model.StatusError || span.HasException {
            summary.ErrorCounts[key]++
        }
        summary.Durations[key] = append(summary.Durations[key], model.Duration(span.EndUnixNano-span.StartUnixNano))
    }
    for _, span := range trace.Spans {
        if span.ParentSpanID == "" {
            continue
        }
        parent, ok := nodesBySpanID[span.ParentSpanID]
        if !ok {
            return TraceSummary{}, fmt.Errorf("missing parent %s", span.ParentSpanID)
        }
        child := nodesBySpanID[span.SpanID]
        summary.EdgeCounts[model.EdgeKey{Parent: parent, Child: child}]++
    }
    return summary, nil
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
```

- [ ] **Step 4: Write a failing aggregate test that proves stable sorting**

```go
func TestAggregateProducesSortedStableObservation(t *testing.T) {
    client := model.NodeKey{Service: "gateway", Name: "inventory.call", Kind: model.SpanKindClient}
    server := model.NodeKey{Service: "gateway", Name: "checkout", Kind: model.SpanKindServer}
    summaries := []TraceSummary{
        {NodeCounts: map[model.NodeKey]int{client: 2, server: 1}, EdgeCounts: map[model.EdgeKey]int{{Parent: server, Child: client}: 2}, Durations: map[model.NodeKey][]model.Duration{client: []model.Duration{20, 30}}, ErrorCounts: map[model.NodeKey]int{}},
        {NodeCounts: map[model.NodeKey]int{client: 1, server: 1}, EdgeCounts: map[model.EdgeKey]int{{Parent: server, Child: client}: 1}, Durations: map[model.NodeKey][]model.Duration{client: []model.Duration{10}}, ErrorCounts: map[model.NodeKey]int{}},
    }
    observation := Aggregate(summaries, Evidence{Complete: 2})
    if observation.Nodes[0].Key.String() > observation.Nodes[1].Key.String() {
        t.Fatalf("nodes are not sorted: %#v", observation.Nodes)
    }
    var clientObservation NodeObservation
    for _, node := range observation.Nodes {
        if node.Key == client {
            clientObservation = node
        }
    }
    if clientObservation.Counts.Min != 1 || clientObservation.Counts.Max != 2 {
        t.Fatalf("unexpected range: %#v", clientObservation.Counts)
    }
}
```

Run: `go test ./internal/analyze -run TestAggregateProducesSortedStableObservation -v`  
Expected: FAIL because `Aggregate` is undefined.

- [ ] **Step 5: Implement aggregation and nearest-rank percentiles**

```go
func NearestRank(samples []model.Duration, percentile float64) model.Duration {
    if len(samples) == 0 {
        return 0
    }
    sorted := append([]model.Duration(nil), samples...)
    sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
    rank := int(math.Ceil(percentile*float64(len(sorted)))) - 1
    if rank < 0 {
        rank = 0
    }
    return sorted[rank]
}

func Aggregate(summaries []TraceSummary, evidence Evidence) Observation {
    nodeKeys := map[model.NodeKey]struct{}{}
    edgeKeys := map[model.EdgeKey]struct{}{}
    for _, summary := range summaries {
        for key := range summary.NodeCounts {
            nodeKeys[key] = struct{}{}
        }
        for key := range summary.ErrorCounts {
            nodeKeys[key] = struct{}{}
        }
        for key := range summary.Durations {
            nodeKeys[key] = struct{}{}
        }
        for key := range summary.EdgeCounts {
            edgeKeys[key] = struct{}{}
        }
    }

    observation := Observation{Evidence: evidence}
    for key := range nodeKeys {
        counts := make([]int, len(summaries))
        errorsTotal := 0
        var durations []model.Duration
        for index, summary := range summaries {
            counts[index] = summary.NodeCounts[key]
            errorsTotal += summary.ErrorCounts[key]
            durations = append(durations, summary.Durations[key]...)
        }
        total := sumInts(counts)
        errorRate := 0.0
        if total > 0 {
            errorRate = float64(errorsTotal) / float64(total)
        }
        observation.Nodes = append(observation.Nodes, NodeObservation{
            Key: key, Counts: summarizeCounts(counts), Total: total, Errors: errorsTotal, ErrorRate: errorRate,
            Durations: DurationSummary{
                Samples: len(durations), P50: NearestRank(durations, 0.50), P95: NearestRank(durations, 0.95),
            },
        })
    }
    for key := range edgeKeys {
        counts := make([]int, len(summaries))
        for index, summary := range summaries {
            counts[index] = summary.EdgeCounts[key]
        }
        observation.Edges = append(observation.Edges, EdgeObservation{Key: key, Counts: summarizeCounts(counts)})
    }
    sort.Slice(observation.Nodes, func(i, j int) bool {
        return observation.Nodes[i].Key.String() < observation.Nodes[j].Key.String()
    })
    sort.Slice(observation.Edges, func(i, j int) bool {
        return edgeKeyString(observation.Edges[i].Key) < edgeKeyString(observation.Edges[j].Key)
    })
    return observation
}

func summarizeCounts(values []int) CountRange {
    if len(values) == 0 {
        return CountRange{}
    }
    sorted := append([]int(nil), values...)
    sort.Ints(sorted)
    median := float64(sorted[len(sorted)/2])
    if len(sorted)%2 == 0 {
        median = float64(sorted[len(sorted)/2-1]+sorted[len(sorted)/2]) / 2
    }
    return CountRange{Min: sorted[0], Max: sorted[len(sorted)-1], Median: median}
}

func sumInts(values []int) int {
    total := 0
    for _, value := range values {
        total += value
    }
    return total
}

func edgeKeyString(key model.EdgeKey) string {
    return key.Parent.String() + " -> " + key.Child.String()
}
```

- [ ] **Step 6: Run analyzer tests and the full suite**

Run: `gofmt -w internal/analyze && go test ./internal/analyze -v && go test ./...`  
Expected: PASS.

- [ ] **Step 7: Commit deterministic analysis**

```bash
git add internal/analyze
git commit -m "feat: normalize and aggregate trace behavior"
```

## Task 7: Define and Persist the Schema-v1 YAML Baseline

**Files:**
- Create: `internal/baseline/types.go`
- Create: `internal/baseline/defaults.go`
- Create: `internal/baseline/repository.go`
- Create: `internal/baseline/repository_test.go`
- Create: `internal/baseline/testdata/baseline.golden.yaml`
- Modify: `go.mod`
- Modify: `go.sum`

**Interfaces:**
- Consumes: `model.RootSelector` and `analyze.Observation`.
- Produces: `baseline.Document`, `baseline.DefaultDocument(...)`, `baseline.Load(path string) (Document, error)`, `baseline.WriteAtomic(path string, document Document, force bool) error`, and `baseline.Path(projectRoot, scenario string) string`.

- [ ] **Step 1: Pin YAML v3 and write failing round-trip/strictness tests**

Run: `go get go.yaml.in/yaml/v3@v3.0.5`

```go
func TestRepositoryRoundTripMatchesGoldenFile(t *testing.T) {
    document := fixtureDocument()
    path := filepath.Join(t.TempDir(), "checkout.yaml")
    if err := WriteAtomic(path, document, false); err != nil {
        t.Fatal(err)
    }
    actual, err := os.ReadFile(path)
    if err != nil {
        t.Fatal(err)
    }
    expected, err := os.ReadFile("testdata/baseline.golden.yaml")
    if err != nil {
        t.Fatal(err)
    }
    if diff := cmpText(string(expected), string(actual)); diff != "" {
        t.Fatal(diff)
    }
    loaded, err := Load(path)
    if err != nil || !reflect.DeepEqual(loaded, document) {
        t.Fatalf("loaded=%#v err=%v", loaded, err)
    }
}

func TestLoadRejectsUnknownAndNewerSchema(t *testing.T) {
    path := writeTemp(t, "schema_version: 2\nscenario: checkout\n")
    if _, err := Load(path); !errors.Is(err, ErrUnsupportedSchema) {
        t.Fatalf("got %v", err)
    }
    path = writeTemp(t, "schema_version: 1\nscenario: checkout\nunknown: true\n")
    if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "field unknown not found") {
        t.Fatalf("got %v", err)
    }
}

func fixtureDocument() Document {
    root := model.RootSelector{Service: "scenario", Span: "checkout", ExpectedPerRun: 1}
    observed := analyze.Observation{Evidence: analyze.Evidence{Complete: 20}}
    return DefaultDocument("checkout", 20, root, observed)
}

func writeTemp(t *testing.T, contents string) string {
    t.Helper()
    path := filepath.Join(t.TempDir(), "baseline.yaml")
    if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
        t.Fatal(err)
    }
    return path
}

func cmpText(expected, actual string) string {
    if expected == actual {
        return ""
    }
    return fmt.Sprintf("expected:\n%s\nactual:\n%s", expected, actual)
}
```

Create `internal/baseline/testdata/baseline.golden.yaml` with this exact content:

```yaml
schema_version: 1
scenario: checkout
runs: 20
root:
  service: scenario
  span: checkout
  expected_per_run: 1
policies:
  new_service_edge: fail
  new_external_dependency: fail
  new_internal_node: warn
  new_error: fail
  removed_node: warn
  removed_edge: warn
  count_increase: fail
  latency_increase: warn
  incomplete_trace_rate:
    max: 0
observed:
  nodes: []
  edges: []
  evidence:
    complete: 20
    incomplete: 0
    unmatched: 0
limitations:
  - Upstream SDK sampling or drops before the TraceBudget receiver cannot be detected.
```

- [ ] **Step 2: Run the baseline tests and verify they fail**

Run: `go test ./internal/baseline -v`  
Expected: FAIL because the baseline package is undefined.

- [ ] **Step 3: Implement the complete schema-v1 types and defaults**

```go
const SchemaVersion = 1

type Policies struct {
    NewServiceEdge       model.Severity `yaml:"new_service_edge"`
    NewExternalDependency model.Severity `yaml:"new_external_dependency"`
    NewInternalNode      model.Severity `yaml:"new_internal_node"`
    NewError             model.Severity `yaml:"new_error"`
    RemovedNode          model.Severity `yaml:"removed_node"`
    RemovedEdge          model.Severity `yaml:"removed_edge"`
    CountIncrease        model.Severity `yaml:"count_increase"`
    LatencyIncrease      model.Severity `yaml:"latency_increase"`
    IncompleteTraceRate  RatePolicy     `yaml:"incomplete_trace_rate"`
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
    Spans       []SpanBudget `yaml:"spans,omitempty"`
    MaxErrorRate *float64    `yaml:"max_error_rate,omitempty"`
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

func DefaultPolicies() Policies {
    return Policies{
        NewServiceEdge: model.SeverityFail, NewExternalDependency: model.SeverityFail,
        NewInternalNode: model.SeverityWarn, NewError: model.SeverityFail,
        RemovedNode: model.SeverityWarn, RemovedEdge: model.SeverityWarn,
        CountIncrease: model.SeverityFail, LatencyIncrease: model.SeverityWarn,
        IncompleteTraceRate: RatePolicy{Max: 0},
    }
}

func DefaultDocument(scenario string, runs int, root model.RootSelector, observed analyze.Observation) Document {
    copied := observed
    copied.Nodes = append([]analyze.NodeObservation(nil), observed.Nodes...)
    copied.Edges = append([]analyze.EdgeObservation(nil), observed.Edges...)
    return Document{
        SchemaVersion: SchemaVersion,
        Scenario: scenario,
        Runs: runs,
        Root: root,
        Policies: DefaultPolicies(),
        Observed: copied,
        Limitations: []string{
            "Upstream SDK sampling or drops before the TraceBudget receiver cannot be detected.",
        },
    }
}

func Path(projectRoot, scenario string) string {
    return filepath.Join(projectRoot, ".tracebudget", scenario+".yaml")
}
```

- [ ] **Step 4: Implement strict reads and atomic writes**

```go
var (
    ErrBaselineExists    = errors.New("baseline already exists")
    ErrUnsupportedSchema = errors.New("unsupported baseline schema")
)

func Load(path string) (Document, error) {
    file, err := os.Open(path)
    if err != nil {
        return Document{}, err
    }
    defer file.Close()
    decoder := yaml.NewDecoder(file)
    decoder.KnownFields(true)
    var document Document
    if err := decoder.Decode(&document); err != nil {
        return Document{}, fmt.Errorf("decode baseline: %w", err)
    }
    if document.SchemaVersion != SchemaVersion {
        return Document{}, fmt.Errorf("%w: got %d, support %d", ErrUnsupportedSchema, document.SchemaVersion, SchemaVersion)
    }
    var trailing any
    if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
        if err == nil {
            return Document{}, errors.New("baseline contains multiple YAML documents")
        }
        return Document{}, fmt.Errorf("decode trailing YAML: %w", err)
    }
    return document, validate(document)
}

func validate(document Document) error {
    if document.SchemaVersion != SchemaVersion {
        return fmt.Errorf("%w: got %d, support %d", ErrUnsupportedSchema, document.SchemaVersion, SchemaVersion)
    }
    if err := model.ValidateScenarioName(document.Scenario); err != nil {
        return err
    }
    if document.Runs < 1 || document.Root.Service == "" || document.Root.Span == "" || document.Root.ExpectedPerRun < 1 {
        return errors.New("baseline requires positive runs and a complete root selector")
    }
    if document.Policies.IncompleteTraceRate.Max < 0 || document.Policies.IncompleteTraceRate.Max > 1 {
        return errors.New("incomplete trace rate must be between 0 and 1")
    }
    severities := map[string]model.Severity{
        "new_service_edge": document.Policies.NewServiceEdge,
        "new_external_dependency": document.Policies.NewExternalDependency,
        "new_internal_node": document.Policies.NewInternalNode,
        "new_error": document.Policies.NewError,
        "removed_node": document.Policies.RemovedNode,
        "removed_edge": document.Policies.RemovedEdge,
        "count_increase": document.Policies.CountIncrease,
        "latency_increase": document.Policies.LatencyIncrease,
    }
    for name, severity := range severities {
        if severity != model.SeverityIgnore && severity != model.SeverityWarn && severity != model.SeverityFail {
            return fmt.Errorf("invalid severity for %s: %q", name, severity)
        }
    }
    for _, budget := range document.Budgets.Spans {
        if budget.Service == "" || budget.Name == "" {
            return errors.New("span budget requires service and name")
        }
        if budget.P95 != nil && *budget.P95 <= 0 {
            return errors.New("p95 budget must be positive")
        }
        if budget.MaxPerTrace != nil && *budget.MaxPerTrace < 0 {
            return errors.New("max_per_trace cannot be negative")
        }
    }
    if document.Budgets.MaxErrorRate != nil && (*document.Budgets.MaxErrorRate < 0 || *document.Budgets.MaxErrorRate > 1) {
        return errors.New("max_error_rate must be between 0 and 1")
    }
    return nil
}

func WriteAtomic(path string, document Document, force bool) error {
    if !force {
        if _, err := os.Stat(path); err == nil {
            return ErrBaselineExists
        } else if !errors.Is(err, os.ErrNotExist) {
            return err
        }
    }
    if err := validate(document); err != nil {
        return err
    }
    if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
        return err
    }
    temporary, err := os.CreateTemp(filepath.Dir(path), ".tracebudget-*.tmp")
    if err != nil {
        return err
    }
    temporaryName := temporary.Name()
    defer os.Remove(temporaryName)
    encoder := yaml.NewEncoder(temporary)
    encoder.SetIndent(2)
    if err := encoder.Encode(document); err != nil {
        _ = temporary.Close()
        return err
    }
    if err := temporary.Sync(); err != nil {
        _ = temporary.Close()
        return err
    }
    if err := temporary.Close(); err != nil {
        return err
    }
    if err := os.Chmod(temporaryName, 0o644); err != nil {
        return err
    }
    return os.Rename(temporaryName, path)
}
```

- [ ] **Step 5: Add exact tests for overwrite protection and atomic cleanup**

```go
func TestWriteAtomicRequiresForceAndLeavesNoTemporaryFile(t *testing.T) {
    directory := t.TempDir()
    path := filepath.Join(directory, "checkout.yaml")
    first := fixtureDocument()
    if err := WriteAtomic(path, first, false); err != nil {
        t.Fatal(err)
    }
    changed := first
    changed.Runs = 30
    if err := WriteAtomic(path, changed, false); !errors.Is(err, ErrBaselineExists) {
        t.Fatalf("got %v", err)
    }
    if err := WriteAtomic(path, changed, true); err != nil {
        t.Fatal(err)
    }
    matches, _ := filepath.Glob(filepath.Join(directory, ".tracebudget-*.tmp"))
    if len(matches) != 0 {
        t.Fatalf("temporary files leaked: %v", matches)
    }
}
```

- [ ] **Step 6: Run baseline tests and the full suite**

Run: `gofmt -w internal/baseline && go mod tidy && go test ./internal/baseline -v && go test ./...`  
Expected: PASS.

- [ ] **Step 7: Commit the baseline schema**

```bash
git add go.mod go.sum internal/baseline
git commit -m "feat: add versioned YAML baselines"
```

## Task 8: Compare Candidate Behavior Against Policies and Budgets

**Files:**
- Create: `internal/compare/comparator.go`
- Create: `internal/compare/comparator_test.go`

**Interfaces:**
- Consumes: `baseline.Document` and `analyze.Observation`.
- Produces: `compare.Result{Outcome model.Outcome, Findings []model.Finding}` and `compare.Compare(document baseline.Document, candidate analyze.Observation) (Result, error)`.

- [ ] **Step 1: Write a failing table test for every default policy**

```go
func TestCompareDefaultPolicies(t *testing.T) {
    baselineDocument := fixtureBaseline()
    tests := []struct {
        name     string
        mutate   func(*analyze.Observation)
        code     string
        severity model.Severity
    }{
        {"new error", addNewError, "new_error", model.SeverityFail},
        {"new cross service edge", addCrossServiceEdge, "new_service_edge", model.SeverityFail},
        {"new client dependency", addClientNode, "new_external_dependency", model.SeverityFail},
        {"new internal span", addInternalNode, "new_internal_node", model.SeverityWarn},
        {"count increase", increaseNodeMaximum, "count_increase", model.SeverityFail},
        {"latency increase", increaseP95, "latency_increase", model.SeverityWarn},
        {"removed node", removeObservedNode, "removed_node", model.SeverityWarn},
        {"removed edge", removeObservedEdge, "removed_edge", model.SeverityWarn},
    }
    for _, test := range tests {
        t.Run(test.name, func(t *testing.T) {
            candidate := cloneObservation(baselineDocument.Observed)
            test.mutate(&candidate)
            result, err := Compare(baselineDocument, candidate)
            if err != nil {
                t.Fatal(err)
            }
            finding := requireFinding(t, result, test.code)
            if finding.Severity != test.severity {
                t.Fatalf("got %s", finding.Severity)
            }
        })
    }
}
```

- [ ] **Step 2: Write failing latency/evidence tests**

```go
func TestCompareRequiresTwentySamplesForBlockingP95(t *testing.T) {
    document := fixtureBaselineWithP95Budget(600 * time.Millisecond)
    candidate := cloneObservation(document.Observed)
    candidate.Nodes[0].Durations.Samples = 19
    if _, err := Compare(document, candidate); !errors.Is(err, ErrInsufficientSamples) {
        t.Fatalf("got %v", err)
    }
}

func TestCompareFailsExplicitP95Budget(t *testing.T) {
    document := fixtureBaselineWithP95Budget(600 * time.Millisecond)
    candidate := cloneObservation(document.Observed)
    candidate.Nodes[0].Durations.Samples = 20
    candidate.Nodes[0].Durations.P95 = model.Duration(601 * time.Millisecond)
    result, err := Compare(document, candidate)
    if err != nil {
        t.Fatal(err)
    }
    requireFinding(t, result, "p95_budget_exceeded")
    if result.Outcome != model.OutcomeFail {
        t.Fatalf("got %s", result.Outcome)
    }
}

func fixtureBaseline() baseline.Document {
    server := model.NodeKey{Service: "gateway", Name: "checkout", Kind: model.SpanKindServer}
    client := model.NodeKey{Service: "gateway", Name: "db.query", Kind: model.SpanKindClient}
    observed := analyze.Observation{
        Nodes: []analyze.NodeObservation{
            {Key: server, Counts: analyze.CountRange{Min: 1, Max: 1, Median: 1}, Total: 20, Durations: analyze.DurationSummary{Samples: 20, P95: model.Duration(100 * time.Millisecond)}},
            {Key: client, Counts: analyze.CountRange{Min: 1, Max: 1, Median: 1}, Total: 20, Durations: analyze.DurationSummary{Samples: 20, P95: model.Duration(50 * time.Millisecond)}},
        },
        Edges: []analyze.EdgeObservation{{
            Key: model.EdgeKey{Parent: server, Child: client},
            Counts: analyze.CountRange{Min: 1, Max: 1, Median: 1},
        }},
        Observation: analyze.Observation{Nodes: []analyze.NodeObservation{{
            Key: model.NodeKey{Service: "gateway", Name: "db.query", Kind: model.SpanKindClient},
            Durations: analyze.DurationSummary{Samples: 20},
        }}},
        Evidence: analyze.Evidence{Complete: 20},
    }
    return baseline.DefaultDocument("checkout", 20, model.RootSelector{Service: "gateway", Span: "checkout", ExpectedPerRun: 1}, observed)
}

func fixtureBaselineWithP95Budget(limit time.Duration) baseline.Document {
    document := fixtureBaseline()
    value := model.Duration(limit)
    document.Budgets.Spans = []baseline.SpanBudget{{Service: "gateway", Name: "checkout", Kind: model.SpanKindServer, P95: &value}}
    return document
}

func cloneObservation(input analyze.Observation) analyze.Observation {
    output := input
    output.Nodes = append([]analyze.NodeObservation(nil), input.Nodes...)
    output.Edges = append([]analyze.EdgeObservation(nil), input.Edges...)
    return output
}

func addNewError(observation *analyze.Observation) { observation.Nodes[0].Errors = 1 }

func addCrossServiceEdge(observation *analyze.Observation) {
    observation.Edges = append(observation.Edges, analyze.EdgeObservation{
        Key: model.EdgeKey{
            Parent: observation.Nodes[0].Key,
            Child: model.NodeKey{Service: "inventory", Name: "reserve", Kind: model.SpanKindServer},
        },
        Counts: analyze.CountRange{Min: 1, Max: 1, Median: 1},
    })
}

func addClientNode(observation *analyze.Observation) {
    observation.Nodes = append(observation.Nodes, analyze.NodeObservation{
        Key: model.NodeKey{Service: "gateway", Name: "inventory.price", Kind: model.SpanKindClient},
        Counts: analyze.CountRange{Min: 1, Max: 1, Median: 1}, Total: 20,
    })
}

func addInternalNode(observation *analyze.Observation) {
    observation.Nodes = append(observation.Nodes, analyze.NodeObservation{
        Key: model.NodeKey{Service: "gateway", Name: "validate", Kind: model.SpanKindInternal},
        Counts: analyze.CountRange{Min: 1, Max: 1, Median: 1}, Total: 20,
    })
}

func increaseNodeMaximum(observation *analyze.Observation) { observation.Nodes[0].Counts.Max = 2 }
func increaseP95(observation *analyze.Observation) { observation.Nodes[0].Durations.P95++ }
func removeObservedNode(observation *analyze.Observation) { observation.Nodes = observation.Nodes[1:] }
func removeObservedEdge(observation *analyze.Observation) { observation.Edges = nil }

func requireFinding(t *testing.T, result Result, code string) model.Finding {
    t.Helper()
    for _, finding := range result.Findings {
        if finding.Code == code {
            return finding
        }
    }
    t.Fatalf("finding %q missing from %#v", code, result.Findings)
    return model.Finding{}
}

func observationWithMultipleRegressions(input analyze.Observation) analyze.Observation {
    output := cloneObservation(input)
    addNewError(&output)
    addClientNode(&output)
    removeObservedEdge(&output)
    return output
}
```

- [ ] **Step 3: Run comparator tests and verify they fail**

Run: `go test ./internal/compare -v`  
Expected: FAIL because `Compare` and comparator errors do not exist.

- [ ] **Step 4: Implement deterministic indexes, finding order, and outcome reduction**

```go
type Result struct {
    Outcome  model.Outcome
    Findings []model.Finding
}

var ErrInsufficientSamples = errors.New("insufficient complete samples for blocking p95 budget")

func Compare(document baseline.Document, candidate analyze.Observation) (Result, error) {
    findings := make([]model.Finding, 0)
    baseNodes := indexNodes(document.Observed.Nodes)
    candidateNodes := indexNodes(candidate.Nodes)
    baseEdges := indexEdges(document.Observed.Edges)
    candidateEdges := indexEdges(candidate.Edges)

    findings = append(findings, compareNodes(document, baseNodes, candidateNodes)...)
    findings = append(findings, compareEdges(document, baseEdges, candidateEdges)...)
    findings = append(findings, compareEvidence(document.Policies.IncompleteTraceRate, candidate.Evidence)...)
    budgetFindings, err := compareBudgets(document.Budgets, candidateNodes, candidate.Evidence)
    if err != nil {
        return Result{}, err
    }
    findings = append(findings, budgetFindings...)
    sort.Slice(findings, func(i, j int) bool {
        if findings[i].Severity != findings[j].Severity {
            return severityOrder(findings[i].Severity) < severityOrder(findings[j].Severity)
        }
        if findings[i].Code != findings[j].Code {
            return findings[i].Code < findings[j].Code
        }
        return findings[i].Subject < findings[j].Subject
    })
    return Result{Outcome: reduceOutcome(findings), Findings: findings}, nil
}

func indexNodes(nodes []analyze.NodeObservation) map[model.NodeKey]analyze.NodeObservation {
    indexed := make(map[model.NodeKey]analyze.NodeObservation, len(nodes))
    for _, node := range nodes {
        indexed[node.Key] = node
    }
    return indexed
}

func indexEdges(edges []analyze.EdgeObservation) map[model.EdgeKey]analyze.EdgeObservation {
    indexed := make(map[model.EdgeKey]analyze.EdgeObservation, len(edges))
    for _, edge := range edges {
        indexed[edge.Key] = edge
    }
    return indexed
}

func compareNodes(document baseline.Document, base, candidate map[model.NodeKey]analyze.NodeObservation) []model.Finding {
    var findings []model.Finding
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
            findings = appendFinding(findings, "latency_increase", document.Policies.LatencyIncrease, key.String(), time.Duration(previous.Durations.P95).String(), time.Duration(current.Durations.P95).String())
        }
    }
    for key := range base {
        if _, exists := candidate[key]; exists {
            continue
        }
        findings = appendFinding(findings, "removed_node", document.Policies.RemovedNode, key.String(), "present", "missing")
    }
    return findings
}

func compareEdges(document baseline.Document, base, candidate map[model.EdgeKey]analyze.EdgeObservation) []model.Finding {
    var findings []model.Finding
    for key, current := range candidate {
        previous, existed := base[key]
        subject := key.Parent.String() + " -> " + key.Child.String()
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
        if _, exists := candidate[key]; exists {
            continue
        }
        subject := key.Parent.String() + " -> " + key.Child.String()
        findings = appendFinding(findings, "removed_edge", document.Policies.RemovedEdge, subject, "present", "missing")
    }
    return findings
}

func compareEvidence(policy baseline.RatePolicy, evidence analyze.Evidence) []model.Finding {
    denominator := evidence.Complete + evidence.Incomplete
    if denominator == 0 {
        return []model.Finding{{
            Code: "no_complete_evidence", Severity: model.SeverityFail,
            Subject: "capture", Baseline: ">0 traces", Candidate: "0 traces",
            Message: "capture produced no comparable traces",
        }}
    }
    rate := float64(evidence.Incomplete) / float64(denominator)
    if rate <= policy.Max {
        return nil
    }
    return []model.Finding{{
        Code: "incomplete_trace_rate", Severity: model.SeverityFail,
        Subject: "capture", Baseline: formatRate(policy.Max), Candidate: formatRate(rate),
        Message: "incomplete trace rate exceeds policy",
    }}
}

func compareBudgets(budgets baseline.Budgets, nodes map[model.NodeKey]analyze.NodeObservation, _ analyze.Evidence) ([]model.Finding, error) {
    var findings []model.Finding
    for _, budget := range budgets.Spans {
        for key, node := range nodes {
            if key.Service != budget.Service || key.Name != budget.Name || (budget.Kind != "" && key.Kind != budget.Kind) {
                continue
            }
            if budget.MaxPerTrace != nil && node.Counts.Max > *budget.MaxPerTrace {
                findings = appendFinding(findings, "max_per_trace_exceeded", model.SeverityFail, key.String(), strconv.Itoa(*budget.MaxPerTrace), strconv.Itoa(node.Counts.Max))
            }
            if budget.P95 != nil {
                if node.Durations.Samples < 20 {
                    return nil, fmt.Errorf("%w: %s has %d", ErrInsufficientSamples, key.String(), node.Durations.Samples)
                }
                if node.Durations.P95 > *budget.P95 {
                    findings = appendFinding(findings, "p95_budget_exceeded", model.SeverityFail, key.String(), time.Duration(*budget.P95).String(), time.Duration(node.Durations.P95).String())
                }
            }
        }
    }
    if budgets.MaxErrorRate != nil {
        total, errorsTotal := 0, 0
        for _, node := range nodes {
            total += node.Total
            errorsTotal += node.Errors
        }
        rate := 0.0
        if total > 0 {
            rate = float64(errorsTotal) / float64(total)
        }
        if rate > *budgets.MaxErrorRate {
            findings = appendFinding(findings, "error_rate_exceeded", model.SeverityFail, "all spans", formatRate(*budgets.MaxErrorRate), formatRate(rate))
        }
    }
    return findings, nil
}

func appendFinding(findings []model.Finding, code string, severity model.Severity, subject, baselineValue, candidateValue string) []model.Finding {
    if severity == model.SeverityIgnore {
        return findings
    }
    return append(findings, model.Finding{
        Code: code, Severity: severity, Subject: subject,
        Baseline: baselineValue, Candidate: candidateValue,
        Message: strings.ReplaceAll(code, "_", " "),
    })
}

func formatRate(value float64) string { return fmt.Sprintf("%.2f%%", value*100) }

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
```

- [ ] **Step 5: Add deterministic ordering and no-change tests**

```go
func TestCompareNoChangePassesWithoutFindings(t *testing.T) {
    document := fixtureBaseline()
    result, err := Compare(document, cloneObservation(document.Observed))
    if err != nil {
        t.Fatal(err)
    }
    if result.Outcome != model.OutcomePass || len(result.Findings) != 0 {
        t.Fatalf("unexpected result: %#v", result)
    }
}

func TestCompareFindingOrderIsStable(t *testing.T) {
    document := fixtureBaseline()
    candidate := observationWithMultipleRegressions(document.Observed)
    first, _ := Compare(document, candidate)
    second, _ := Compare(document, candidate)
    if !reflect.DeepEqual(first, second) {
        t.Fatalf("results differ:\n%#v\n%#v", first, second)
    }
}
```

- [ ] **Step 6: Run comparator tests and the full suite**

Run: `gofmt -w internal/compare && go test ./internal/compare -v && go test ./...`  
Expected: PASS.

- [ ] **Step 7: Commit policy comparison**

```bash
git add internal/compare
git commit -m "feat: compare trace behavior against budgets"
```

## Task 9: Render Terminal and Markdown Reports

**Files:**
- Create: `internal/report/report.go`
- Create: `internal/report/terminal.go`
- Create: `internal/report/markdown.go`
- Create: `internal/report/report_test.go`
- Create: `internal/report/testdata/failure.terminal.golden`
- Create: `internal/report/testdata/failure.markdown.golden`

**Interfaces:**
- Consumes: `compare.Result`, `analyze.Evidence`, and `diagnostics.Snapshot`.
- Produces: `report.RenderTerminal(io.Writer, Input) error`, `report.RenderMarkdown(io.Writer, Input) error`, and `report.ExitCode(outcome model.Outcome) int`.

- [ ] **Step 1: Write failing golden and exit-code tests**

```go
func TestExitCode(t *testing.T) {
    tests := map[model.Outcome]int{
        model.OutcomePass: 0,
        model.OutcomeFail: 1,
        model.OutcomeError: 2,
    }
    for outcome, expected := range tests {
        if got := ExitCode(outcome); got != expected {
            t.Fatalf("%s: got %d", outcome, got)
        }
    }
}

func TestRenderFailureMatchesGoldenFiles(t *testing.T) {
    input := failureInput()
    assertGolden(t, "testdata/failure.terminal.golden", func(writer io.Writer) error {
        return RenderTerminal(writer, input)
    })
    assertGolden(t, "testdata/failure.markdown.golden", func(writer io.Writer) error {
        return RenderMarkdown(writer, input)
    })
}

func failureInput() Input {
    return Input{
        Scenario: "checkout",
        Result: compare.Result{Outcome: model.OutcomeFail, Findings: []model.Finding{
            {Code: "count_increase", Severity: model.SeverityFail, Subject: "gateway/db.query/CLIENT", Baseline: "1", Candidate: "2"},
            {Code: "removed_edge", Severity: model.SeverityWarn, Subject: "gateway/checkout/SERVER -> inventory/reserve/SERVER", Baseline: "present", Candidate: "missing"},
        }},
        Evidence: analyze.Evidence{Complete: 20},
        Diagnostics: diagnostics.Snapshot{Received: 80},
    }
}

func assertGolden(t *testing.T, path string, render func(io.Writer) error) {
    t.Helper()
    var output bytes.Buffer
    if err := render(&output); err != nil {
        t.Fatal(err)
    }
    expected, err := os.ReadFile(path)
    if err != nil {
        t.Fatal(err)
    }
    if output.String() != string(expected) {
        t.Fatalf("expected:\n%s\nactual:\n%s", expected, output.String())
    }
}
```

- [ ] **Step 2: Run report tests and verify they fail**

Run: `go test ./internal/report -v`  
Expected: FAIL because the renderers are undefined.

- [ ] **Step 3: Implement shared input and exit-code mapping**

```go
type Input struct {
    Scenario    string
    Result      compare.Result
    Observation analyze.Observation
    Evidence    analyze.Evidence
    Diagnostics diagnostics.Snapshot
    Limitations []string
    ArtifactPath string
}

func ExitCode(outcome model.Outcome) int {
    switch outcome {
    case model.OutcomePass:
        return 0
    case model.OutcomeFail:
        return 1
    default:
        return 2
    }
}

func RenderTerminal(writer io.Writer, input Input) error {
    if _, err := fmt.Fprintf(writer, "%s %s\n\n", strings.ToUpper(string(input.Result.Outcome)), input.Scenario); err != nil {
        return err
    }
    if len(input.Result.Findings) == 0 {
        if _, err := fmt.Fprintln(writer, "No regressions."); err != nil {
            return err
        }
    } else {
        for _, finding := range input.Result.Findings {
            if _, err := fmt.Fprintf(writer, "[%s] %s %s baseline=%s candidate=%s\n",
                strings.ToUpper(string(finding.Severity)), finding.Code, finding.Subject,
                finding.Baseline, finding.Candidate); err != nil {
                return err
            }
        }
    }
    if _, err := fmt.Fprintf(writer,
        "\nEvidence: complete=%d incomplete=%d received=%d duplicate=%d unmatched=%d\n",
        input.Evidence.Complete, input.Evidence.Incomplete, input.Diagnostics.Received,
        input.Diagnostics.Duplicates, input.Evidence.Unmatched,
    ); err != nil {
        return err
    }
    if _, err := fmt.Fprintf(writer, "Timing: sqlite=%s assembly=%s normalization=%s comparison=%s\n",
        input.Diagnostics.SQLiteWriteDuration, input.Diagnostics.AssemblyDuration,
        input.Diagnostics.NormalizationDuration, input.Diagnostics.ComparisonDuration); err != nil {
        return err
    }
    if len(input.Observation.Nodes) > 0 {
        if _, err := fmt.Fprintln(writer, "Samples:"); err != nil {
            return err
        }
        for _, node := range input.Observation.Nodes {
            if _, err := fmt.Fprintf(writer, "- %s=%d\n", node.Key.String(), node.Durations.Samples); err != nil {
                return err
            }
        }
    }
    if input.ArtifactPath != "" {
        if _, err := fmt.Fprintf(writer, "Artifacts: %s\n", input.ArtifactPath); err != nil {
            return err
        }
    }
    return nil
}

func RenderMarkdown(writer io.Writer, input Input) error {
    if _, err := fmt.Fprintf(writer, "# TraceBudget: %s — %s\n\n", markdownText(input.Scenario), strings.ToUpper(string(input.Result.Outcome))); err != nil {
        return err
    }
    if _, err := fmt.Fprintln(writer, "| Severity | Code | Subject | Baseline | Candidate |\n| --- | --- | --- | --- | --- |"); err != nil {
        return err
    }
    for _, finding := range input.Result.Findings {
        if _, err := fmt.Fprintf(writer, "| %s | `%s` | `%s` | `%s` | `%s` |\n",
            strings.ToUpper(string(finding.Severity)), markdownCode(finding.Code),
            markdownCode(finding.Subject), markdownCode(finding.Baseline), markdownCode(finding.Candidate)); err != nil {
            return err
        }
    }
    if _, err := fmt.Fprintf(writer,
        "\n## Evidence\n\n- Complete traces: %d\n- Incomplete traces: %d\n- Received spans: %d\n- Exact duplicates: %d\n- Unmatched traces: %d\n",
        input.Evidence.Complete, input.Evidence.Incomplete, input.Diagnostics.Received,
        input.Diagnostics.Duplicates, input.Evidence.Unmatched); err != nil {
        return err
    }
    if _, err := fmt.Fprintf(writer,
        "\n## Timing\n\n- SQLite writes: %s\n- Assembly: %s\n- Normalization: %s\n- Comparison: %s\n",
        input.Diagnostics.SQLiteWriteDuration, input.Diagnostics.AssemblyDuration,
        input.Diagnostics.NormalizationDuration, input.Diagnostics.ComparisonDuration); err != nil {
        return err
    }
    if len(input.Observation.Nodes) > 0 {
        if _, err := io.WriteString(writer, "\n## Metric samples\n\n| Span | Duration samples |\n| --- | ---: |\n"); err != nil {
            return err
        }
        for _, node := range input.Observation.Nodes {
            if _, err := fmt.Fprintf(writer, "| `%s` | %d |\n", markdownCode(node.Key.String()), node.Durations.Samples); err != nil {
                return err
            }
        }
    }
    if input.ArtifactPath != "" {
        if _, err := fmt.Fprintf(writer, "\n## Retained artifacts\n\n`%s`\n", markdownCode(input.ArtifactPath)); err != nil {
            return err
        }
    }
    if len(input.Limitations) > 0 {
        if _, err := fmt.Fprintln(writer, "\n## Limitations"); err != nil {
            return err
        }
        for _, limitation := range input.Limitations {
            if _, err := fmt.Fprintf(writer, "\n- %s", markdownText(limitation)); err != nil {
                return err
            }
        }
        _, err := fmt.Fprintln(writer)
        return err
    }
    return nil
}

func markdownText(value string) string {
    value = strings.ReplaceAll(value, "|", "\\|")
    return strings.ReplaceAll(value, "\n", " ")
}

func markdownCode(value string) string {
    return strings.ReplaceAll(markdownText(value), "`", "'")
}
```

- [ ] **Step 4: Create exact golden output and implement renderers**

```text
FAIL checkout

[FAIL] count_increase gateway/db.query/CLIENT baseline=1 candidate=2
[WARN] removed_edge gateway/checkout/SERVER -> inventory/reserve/SERVER

Evidence: complete=20 incomplete=0 received=80 duplicate=0 unmatched=0
Timing: sqlite=0s assembly=0s normalization=0s comparison=0s
Samples:
- gateway/db.query/CLIENT=20
```

```markdown
# TraceBudget: checkout — FAIL

| Severity | Code | Subject | Baseline | Candidate |
| --- | --- | --- | --- | --- |
| FAIL | `count_increase` | `gateway/db.query/CLIENT` | `1` | `2` |
| WARN | `removed_edge` | `gateway/checkout/SERVER -> inventory/reserve/SERVER` | `present` | `missing` |

## Evidence

- Complete traces: 20
- Incomplete traces: 0
- Received spans: 80
- Exact duplicates: 0
- Unmatched traces: 0

## Timing

- SQLite writes: 0s
- Assembly: 0s
- Normalization: 0s
- Comparison: 0s

## Metric samples

| Span | Duration samples |
| --- | ---: |
| `gateway/db.query/CLIENT` | 20 |
```

- [ ] **Step 5: Run report tests and the full suite**

Run: `gofmt -w internal/report && go test ./internal/report -v && go test ./...`  
Expected: PASS.

- [ ] **Step 6: Commit reports**

```bash
git add internal/report
git commit -m "feat: render terminal and markdown reports"
```

## Task 10: Supervise Linux Child Processes Safely

**Files:**
- Create: `internal/supervisor/env.go`
- Create: `internal/supervisor/env_test.go`
- Create: `internal/supervisor/runner.go`
- Create: `internal/supervisor/runner_test.go`
- Create: `internal/supervisor/process_linux.go`

**Interfaces:**
- Consumes: command argv, base environment, overrides, and a timeout.
- Produces: `supervisor.MergeEnv(base []string, overrides map[string]string) []string` and `supervisor.Runner.Run(context.Context, Command) Result`.

- [ ] **Step 1: Write failing deterministic environment-merge tests**

```go
func TestMergeEnvOverridesWithoutDuplicates(t *testing.T) {
    actual := MergeEnv(
        []string{"PATH=/bin", "OTEL_RESOURCE_ATTRIBUTES=deployment.environment=test", "KEEP=yes"},
        map[string]string{
            "OTEL_RESOURCE_ATTRIBUTES": "deployment.environment=test,tracebudget.run_id=run-1",
            "OTEL_TRACES_SAMPLER": "always_on",
        },
    )
    expected := []string{
        "KEEP=yes", "OTEL_RESOURCE_ATTRIBUTES=deployment.environment=test,tracebudget.run_id=run-1",
        "OTEL_TRACES_SAMPLER=always_on", "PATH=/bin",
    }
    if !reflect.DeepEqual(actual, expected) {
        t.Fatalf("got %#v", actual)
    }
}
```

- [ ] **Step 2: Run the environment test and verify it fails**

Run: `go test ./internal/supervisor -run TestMergeEnvOverridesWithoutDuplicates -v`  
Expected: FAIL because `MergeEnv` is undefined.

- [ ] **Step 3: Implement deterministic environment merging**

```go
func MergeEnv(base []string, overrides map[string]string) []string {
    values := make(map[string]string, len(base)+len(overrides))
    for _, entry := range base {
        key, value, ok := strings.Cut(entry, "=")
        if ok {
            values[key] = value
        }
    }
    for key, value := range overrides {
        values[key] = value
    }
    keys := make([]string, 0, len(values))
    for key := range values {
        keys = append(keys, key)
    }
    sort.Strings(keys)
    result := make([]string, 0, len(keys))
    for _, key := range keys {
        result = append(result, key+"="+values[key])
    }
    return result
}
```

- [ ] **Step 4: Write failing subprocess tests for success, failure, timeout, and cancellation**

Write this helper-process implementation and its assertions:

```go
func TestRunnerReturnsChildExitStatus(t *testing.T) {
    runner := Runner{}
    result := runner.Run(context.Background(), helperCommand("failure", time.Second))
    if result.ExitCode != 7 || result.TimedOut || result.Err != nil {
        t.Fatalf("unexpected result: %#v", result)
    }
}

func TestRunnerKillsProcessGroupOnTimeout(t *testing.T) {
    runner := Runner{}
    pidFile := filepath.Join(t.TempDir(), "child.pid")
    command := helperCommand("spawn-child", 100*time.Millisecond)
    command.Env = append(command.Env, "TRACEBUDGET_PID_FILE="+pidFile)
    result := runner.Run(context.Background(), command)
    if !result.TimedOut || result.ExitCode != -1 {
        t.Fatalf("unexpected result: %#v", result)
    }
    assertRecordedChildPIDIsGone(t, pidFile)
}

func TestRunnerSuccessAndCancellation(t *testing.T) {
    runner := Runner{}
    success := runner.Run(context.Background(), helperCommand("success", time.Second))
    if success.ExitCode != 0 || success.Err != nil {
        t.Fatalf("success: %#v", success)
    }
    ctx, cancel := context.WithCancel(context.Background())
    cancel()
    cancelled := runner.Run(ctx, helperCommand("sleep", time.Second))
    if !cancelled.Cancelled || cancelled.ExitCode != -1 {
        t.Fatalf("cancelled: %#v", cancelled)
    }
}

func TestHelperProcess(t *testing.T) {
    if os.Getenv("TRACEBUDGET_HELPER") != "1" {
        return
    }
    switch os.Getenv("TRACEBUDGET_HELPER_MODE") {
    case "success":
        os.Exit(0)
    case "failure":
        os.Exit(7)
    case "sleep":
        time.Sleep(30 * time.Second)
        os.Exit(0)
    case "spawn-child":
        child := exec.Command("sleep", "30")
        if err := child.Start(); err != nil {
            os.Exit(90)
        }
        if err := os.WriteFile(os.Getenv("TRACEBUDGET_PID_FILE"), []byte(strconv.Itoa(child.Process.Pid)), 0o600); err != nil {
            os.Exit(91)
        }
        _ = child.Wait()
        os.Exit(0)
    default:
        os.Exit(92)
    }
}

func helperCommand(mode string, timeout time.Duration) Command {
    executable, _ := os.Executable()
    environment := append(os.Environ(), "TRACEBUDGET_HELPER=1", "TRACEBUDGET_HELPER_MODE="+mode)
    return Command{Argv: []string{executable, "-test.run=TestHelperProcess"}, Env: environment, Timeout: timeout}
}

func assertRecordedChildPIDIsGone(t *testing.T, path string) {
    t.Helper()
    contents, err := os.ReadFile(path)
    if err != nil {
        t.Fatal(err)
    }
    pid, err := strconv.Atoi(string(contents))
    if err != nil {
        t.Fatal(err)
    }
    deadline := time.Now().Add(time.Second)
    for time.Now().Before(deadline) {
        if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
            return
        }
        time.Sleep(10 * time.Millisecond)
    }
    t.Fatalf("child process %d still exists", pid)
}
```

Run: `go test ./internal/supervisor -run TestRunner -v`  
Expected: FAIL because `Runner` is undefined.

- [ ] **Step 5: Implement process-group supervision**

```go
type Command struct {
    Argv    []string
    Env     []string
    Dir     string
    Timeout time.Duration
    Stdout  io.Writer
    Stderr  io.Writer
}

type Result struct {
    ExitCode  int
    TimedOut  bool
    Cancelled bool
    Duration  time.Duration
    Err       error
}

func (Runner) Run(parent context.Context, command Command) Result {
    ctx, cancel := context.WithTimeout(parent, command.Timeout)
    defer cancel()
    started := time.Now()
    process := exec.Command(command.Argv[0], command.Argv[1:]...)
    process.Env, process.Dir, process.Stdout, process.Stderr = command.Env, command.Dir, command.Stdout, command.Stderr
    configureProcessGroup(process)
    if err := process.Start(); err != nil {
        return Result{ExitCode: -1, Err: err, Duration: time.Since(started)}
    }
    done := make(chan error, 1)
    go func() { done <- process.Wait() }()
    select {
    case err := <-done:
        return resultFromWait(err, time.Since(started))
    case <-ctx.Done():
        terminationErr := terminateProcessGroup(process.Process.Pid)
        <-done
        return Result{ExitCode: -1, TimedOut: errors.Is(ctx.Err(), context.DeadlineExceeded), Cancelled: errors.Is(ctx.Err(), context.Canceled), Duration: time.Since(started), Err: terminationErr}
    }
}

func resultFromWait(err error, duration time.Duration) Result {
    if err == nil {
        return Result{ExitCode: 0, Duration: duration}
    }
    var exitError *exec.ExitError
    if errors.As(err, &exitError) {
        return Result{ExitCode: exitError.ExitCode(), Duration: duration}
    }
    return Result{ExitCode: -1, Duration: duration, Err: err}
}
```

```go
// internal/supervisor/process_linux.go
//go:build linux

package supervisor

import (
    "errors"
    "os/exec"
    "syscall"
    "time"
)

func configureProcessGroup(command *exec.Cmd) {
    command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func terminateProcessGroup(pid int) error {
    if err := syscall.Kill(-pid, syscall.SIGTERM); err != nil && err != syscall.ESRCH {
        return err
    }
    deadline := time.NewTimer(500 * time.Millisecond)
    defer deadline.Stop()
    ticker := time.NewTicker(10 * time.Millisecond)
    defer ticker.Stop()
    for {
        if err := syscall.Kill(-pid, 0); errors.Is(err, syscall.ESRCH) {
            return nil
        } else if err != nil {
            return err
        }
        select {
        case <-ticker.C:
        case <-deadline.C:
            if err := syscall.Kill(-pid, syscall.SIGKILL); err != nil && err != syscall.ESRCH {
                return err
            }
            return nil
        }
    }
}
```

- [ ] **Step 6: Run Linux supervisor tests with the race detector**

Run: `gofmt -w internal/supervisor && go test -race ./internal/supervisor -v && go test ./...`  
Expected: PASS on Linux.

- [ ] **Step 7: Commit process supervision**

```bash
git add internal/supervisor
git commit -m "feat: supervise scenario processes on linux"
```

## Task 11: Orchestrate a Trustworthy Capture Session

**Files:**
- Create: `internal/capture/markers.go`
- Create: `internal/capture/markers_test.go`
- Create: `internal/capture/session.go`
- Create: `internal/capture/session_test.go`

**Interfaces:**
- Consumes: receiver, bounded buffer, SQLite store, process supervisor, capture configuration, and command argv.
- Produces: `capture.Run(context.Context, Request) (Result, error)` and `capture.MergeResourceAttributes(string, map[string]string) (string, error)`.

- [ ] **Step 1: Write failing marker-merge tests**

```go
func TestMergeResourceAttributesPreservesAndSortsValues(t *testing.T) {
    got, err := MergeResourceAttributes(
        "deployment.environment=test,service.version=1.2.3",
        map[string]string{
            "tracebudget.execution_id": "exec-1",
            "tracebudget.run_id":       "run-1",
        },
    )
    if err != nil {
        t.Fatal(err)
    }
    want := "deployment.environment=test,service.version=1.2.3,tracebudget.execution_id=exec-1,tracebudget.run_id=run-1"
    if got != want {
        t.Fatalf("got %q, want %q", got, want)
    }
}

func TestMergeResourceAttributesRejectsConflictsAndMalformedInput(t *testing.T) {
    tests := []string{
        "missing-equals",
        "tracebudget.run_id=user-value",
        "duplicate=one,duplicate=two",
    }
    for _, input := range tests {
        if _, err := MergeResourceAttributes(input, map[string]string{"tracebudget.run_id": "run-1"}); err == nil {
            t.Fatalf("expected %q to fail", input)
        }
    }
}
```

- [ ] **Step 2: Run the marker tests and verify they fail**

Run: `go test ./internal/capture -run TestMergeResourceAttributes -v`  
Expected: FAIL because `MergeResourceAttributes` is undefined.

- [ ] **Step 3: Implement deterministic marker merging**

```go
// internal/capture/markers.go
package capture

import (
    "fmt"
    "sort"
    "strings"
)

func MergeResourceAttributes(existing string, additions map[string]string) (string, error) {
    values := map[string]string{}
    if strings.TrimSpace(existing) != "" {
        for _, entry := range strings.Split(existing, ",") {
            key, value, ok := strings.Cut(strings.TrimSpace(entry), "=")
            if !ok || strings.TrimSpace(key) == "" || strings.TrimSpace(value) == "" {
                return "", fmt.Errorf("invalid OTEL_RESOURCE_ATTRIBUTES entry %q", entry)
            }
            if _, duplicate := values[key]; duplicate {
                return "", fmt.Errorf("duplicate resource attribute %q", key)
            }
            values[key] = value
        }
    }
    for key, value := range additions {
        if _, conflict := values[key]; conflict {
            return "", fmt.Errorf("TraceBudget marker %q already exists", key)
        }
        values[key] = value
    }
    keys := make([]string, 0, len(values))
    for key := range values {
        keys = append(keys, key)
    }
    sort.Strings(keys)
    entries := make([]string, 0, len(keys))
    for _, key := range keys {
        entries = append(entries, key+"="+values[key])
    }
    return strings.Join(entries, ","), nil
}
```

Document this deliberate v1 constraint in the command help: resource-attribute keys and values containing literal commas are rejected; callers may use percent-encoded values.

- [ ] **Step 4: Write a failing session test proving end-to-end lifecycle and failure semantics**

```go
type fakeRunner struct {
    commands []supervisor.Command
    results  []supervisor.Result
}

func (f *fakeRunner) Run(_ context.Context, command supervisor.Command) supervisor.Result {
    f.commands = append(f.commands, command)
    result := f.results[0]
    f.results = f.results[1:]
    return result
}

func TestRunInjectsUniqueMarkersAndReturnsCapturedSpans(t *testing.T) {
    fixture := newSessionFixture(t)
    fixture.runner.results = []supervisor.Result{{ExitCode: 0}, {ExitCode: 0}}
    fixture.receiver.onStart = func(endpoint string) {
        fixture.sink([]model.Span{
            markedRoot("trace-1", "run-1"),
            markedRoot("trace-2", "run-2"),
        })
    }

    result, err := fixture.session.Run(context.Background(), Request{
        Argv: []string{"scenario"}, Runs: 2, Config: testCaptureConfig(),
    })
    if err != nil {
        t.Fatal(err)
    }
    if len(result.RunIDs) != 2 || result.RunIDs[0] == result.RunIDs[1] || len(result.Spans) != 2 {
        t.Fatalf("unexpected result: %#v", result)
    }
    for index, command := range fixture.runner.commands {
        environment := envMap(command.Env)
        if environment["OTEL_TRACES_SAMPLER"] != "always_on" || environment["OTEL_EXPORTER_OTLP_TRACES_PROTOCOL"] != "http/protobuf" {
            t.Fatalf("run %d missing telemetry environment: %#v", index, environment)
        }
    }
}

func TestRunInvalidatesEvidenceWhenChildFails(t *testing.T) {
    fixture := newSessionFixture(t)
    fixture.runner.results = []supervisor.Result{{ExitCode: 9}}
    _, err := fixture.session.Run(context.Background(), Request{
        Argv: []string{"scenario"}, Runs: 1, Config: testCaptureConfig(),
    })
    if !errors.Is(err, ErrChildFailed) {
        t.Fatalf("got %v", err)
    }
    if !fixture.tracker.Snapshot().IntegrityFailed {
        t.Fatal("child failure must invalidate the execution")
    }
}

func TestRunPropagatesReceiverAndPersistenceFailures(t *testing.T) {
    t.Run("receiver", func(t *testing.T) {
        fixture := newSessionFixture(t)
        fixture.receiver.startError = errors.New("bind failed")
        if _, err := fixture.session.Run(context.Background(), validCaptureRequest()); err == nil || !strings.Contains(err.Error(), "bind failed") {
            t.Fatalf("got %v", err)
        }
    })
    t.Run("persistence", func(t *testing.T) {
        fixture := newSessionFixture(t)
        fixture.store.writeError = errors.New("disk full")
        fixture.receiver.onStart = func(string) { fixture.sink([]model.Span{markedRoot("trace-1", "run-1")}) }
        if _, err := fixture.session.Run(context.Background(), validCaptureRequest()); !errors.Is(err, ErrPersistenceFailed) {
            t.Fatalf("got %v", err)
        }
    })
}

func TestRunInvalidatesOverloadAndFlushTimeout(t *testing.T) {
    t.Run("overload", func(t *testing.T) {
        fixture := newSessionFixture(t)
        request := validCaptureRequest()
        request.Config.BufferSpans = 1
        fixture.receiver.onStart = func(string) {
            _ = fixture.sink([]model.Span{{SpanID: "one"}, {SpanID: "two"}})
        }
        if _, err := fixture.session.Run(context.Background(), request); !errors.Is(err, ErrUntrustworthy) {
            t.Fatalf("got %v", err)
        }
    })
    t.Run("flush timeout", func(t *testing.T) {
        fixture := newSessionFixture(t)
        request := validCaptureRequest()
        request.Config.FlushTimeout = 10 * time.Millisecond
        request.Config.QuietPeriod = time.Second
        if _, err := fixture.session.Run(context.Background(), request); !errors.Is(err, ErrUntrustworthy) {
            t.Fatalf("got %v", err)
        }
    })
}

func TestRunHonorsCancellationAndArtifactPolicy(t *testing.T) {
    fixture := newSessionFixture(t)
    fixture.runner.results = []supervisor.Result{{ExitCode: -1, Cancelled: true}}
    ctx, cancel := context.WithCancel(context.Background())
    cancel()
    if _, err := fixture.session.Run(ctx, validCaptureRequest()); !errors.Is(err, ErrChildFailed) {
        t.Fatalf("got %v", err)
    }

    retained := newSessionFixture(t)
    retained.receiver.onStart = func(string) { retained.sink([]model.Span{markedRoot("trace-1", "run-1")}) }
    request := validCaptureRequest()
    request.Config.KeepArtifacts = true
    result, err := retained.session.Run(context.Background(), request)
    if err != nil {
        t.Fatal(err)
    }
    if _, err := os.Stat(result.ArtifactPath); err != nil {
        t.Fatalf("retained artifact missing: %v", err)
    }
    t.Cleanup(func() { _ = os.RemoveAll(result.ArtifactPath) })
}

func TestRunVerboseWritesStructuredLifecycleEvents(t *testing.T) {
    fixture := newSessionFixture(t)
    fixture.receiver.onStart = func(string) { fixture.sink([]model.Span{markedRoot("trace-1", "run-1")}) }
    request := validCaptureRequest()
    request.Config.Verbose = true
    if _, err := fixture.session.Run(context.Background(), request); err != nil {
        t.Fatal(err)
    }
    records := decodeJSONLines(t, fixture.debug.String())
    for _, expected := range []string{"receiver_started", "run_started", "run_finished", "flush_finished", "capture_finished"} {
        if !containsEvent(records, expected) {
            t.Fatalf("event %q missing from %s", expected, fixture.debug.String())
        }
    }
}

type fakeReceiver struct {
    endpoint   string
    startError error
    onStart    func(string)
}

func (receiver *fakeReceiver) Start() (string, error) {
    if receiver.startError != nil {
        return "", receiver.startError
    }
    if receiver.onStart != nil {
        receiver.onStart(receiver.endpoint)
    }
    return receiver.endpoint, nil
}
func (*fakeReceiver) Shutdown(context.Context) error { return nil }

type fakeStore struct {
    mutex      sync.Mutex
    spans      []model.Span
    writeError error
}

func (store *fakeStore) WriteBatch(_ context.Context, spans []model.Span) (sqlite.WriteResult, error) {
    if store.writeError != nil {
        return sqlite.WriteResult{}, store.writeError
    }
    store.mutex.Lock()
    defer store.mutex.Unlock()
    store.spans = append(store.spans, spans...)
    return sqlite.WriteResult{Inserted: len(spans)}, nil
}
func (store *fakeStore) SpansForExecution(_ context.Context, executionID string) ([]model.Span, error) {
    store.mutex.Lock()
    defer store.mutex.Unlock()
    var result []model.Span
    for _, span := range store.spans {
        if span.ExecutionID == executionID {
            result = append(result, span)
        }
    }
    return result, nil
}
func (*fakeStore) Close() error { return nil }

type sessionFixture struct {
    session  Session
    runner   *fakeRunner
    receiver *fakeReceiver
    store    *fakeStore
    tracker  *diagnostics.Tracker
    sink     func([]model.Span) error
    debug    *bytes.Buffer
}

func newSessionFixture(t *testing.T) *sessionFixture {
    t.Helper()
    fixture := &sessionFixture{
        runner: &fakeRunner{results: []supervisor.Result{{ExitCode: 0}}},
        receiver: &fakeReceiver{endpoint: "http://127.0.0.1:4318/v1/traces"},
        store: &fakeStore{},
        debug: new(bytes.Buffer),
    }
    identifiers := []string{"exec-1", "run-1", "run-2"}
    fixture.session = NewSession(Dependencies{
        IDs: func() string { value := identifiers[0]; identifiers = identifiers[1:]; return value },
        Now: time.Now,
        Runner: fixture.runner,
        OpenStore: func(string) (SpanStore, error) { return fixture.store, nil },
        NewReceiver: func(buffer *ingest.Buffer, tracker *diagnostics.Tracker, _ []string, _ string) Receiver {
            fixture.tracker, fixture.sink = tracker, buffer.Offer
            return fixture.receiver
        },
        TempDir: func() (string, error) { return os.MkdirTemp(t.TempDir(), "capture-") },
        RemoveAll: os.RemoveAll,
        Debug: fixture.debug,
    })
    return fixture
}

func validCaptureRequest() Request {
    return Request{Argv: []string{"scenario"}, BaseEnv: []string{"PATH=/bin"}, Runs: 1, Config: testCaptureConfig()}
}

func testCaptureConfig() model.CaptureConfig {
    return model.CaptureConfig{FlushTimeout: time.Second, QuietPeriod: time.Millisecond, BufferSpans: 10, CommandTimeout: time.Second, ListenAddress: "127.0.0.1:0"}
}

func markedRoot(traceID, runID string) model.Span {
    return model.Span{TraceID: traceID, SpanID: "root-" + runID, Name: "checkout", ServiceName: "scenario", ExecutionID: "exec-1", RunID: runID, ArrivedAt: time.Now(), EndUnixNano: 1}
}

func envMap(entries []string) map[string]string {
    result := map[string]string{}
    for _, entry := range entries {
        key, value, _ := strings.Cut(entry, "=")
        result[key] = value
    }
    return result
}

func decodeJSONLines(t *testing.T, value string) []map[string]any {
    t.Helper()
    decoder := json.NewDecoder(strings.NewReader(value))
    var records []map[string]any
    for {
        var record map[string]any
        if err := decoder.Decode(&record); errors.Is(err, io.EOF) {
            break
        } else if err != nil {
            t.Fatal(err)
        }
        records = append(records, record)
    }
    return records
}

func containsEvent(records []map[string]any, event string) bool {
    for _, record := range records {
        if record["event"] == event {
            return true
        }
    }
    return false
}
```

- [ ] **Step 5: Run the session tests and verify they fail**

Run: `go test ./internal/capture -run 'TestRun' -v`  
Expected: FAIL because `Session`, `Request`, and lifecycle errors are undefined.

- [ ] **Step 6: Implement the capture contract and lifecycle**

```go
type Request struct {
    Argv       []string
    Dir        string
    BaseEnv    []string
    Runs       int
    Config     model.CaptureConfig
}

type Result struct {
    ExecutionID   string
    RunIDs        []string
    Spans         []model.Span
    Diagnostics   diagnostics.Snapshot
    CaptureEndedAt time.Time
    ArtifactPath  string
}

var (
    ErrChildFailed        = errors.New("scenario command failed")
    ErrUntrustworthy      = errors.New("capture evidence is untrustworthy")
    ErrPersistenceFailed  = errors.New("persist captured spans")
)

type Runner interface {
    Run(context.Context, supervisor.Command) supervisor.Result
}

type SpanStore interface {
    WriteBatch(context.Context, []model.Span) (sqlite.WriteResult, error)
    SpansForExecution(context.Context, string) ([]model.Span, error)
    Close() error
}

type Receiver interface {
    Start() (string, error)
    Shutdown(context.Context) error
}

type Dependencies struct {
    IDs         func() string
    Now         func() time.Time
    Runner      Runner
    OpenStore   func(string) (SpanStore, error)
    NewReceiver func(*ingest.Buffer, *diagnostics.Tracker, []string, string) Receiver
    TempDir     func() (string, error)
    RemoveAll   func(string) error
    Debug       io.Writer
}

type Session struct{ dependencies Dependencies }

func NewSession(dependencies Dependencies) Session { return Session{dependencies: dependencies} }
```

Use this exact lifecycle in `Session.Run`:

```go
func (s Session) Run(ctx context.Context, request Request) (result Result, returned error) {
    if len(request.Argv) == 0 || request.Runs < 1 {
        return Result{}, fmt.Errorf("invalid capture request")
    }
    artifactDirectory, err := s.dependencies.TempDir()
    if err != nil {
        return Result{}, err
    }
    result.ArtifactPath = artifactDirectory
    tracker := diagnostics.NewTracker()
    buffer := ingest.NewBuffer(request.Config.BufferSpans, tracker)
    store, err := s.dependencies.OpenStore(filepath.Join(artifactDirectory, "spans.sqlite"))
    if err != nil {
        if !request.Config.KeepArtifacts {
            _ = s.dependencies.RemoveAll(artifactDirectory)
        }
        return Result{}, err
    }
    drainDone := startDrainWorker(ctx, buffer, store, tracker, 500)
    drainConsumed := false

    receiver := s.dependencies.NewReceiver(buffer, tracker, request.Config.AllowedAttributes, request.Config.ListenAddress)
    receiverStarted := false
    defer func() {
        if receiverStarted {
            shutdownContext, cancel := context.WithTimeout(context.Background(), request.Config.FlushTimeout)
            if shutdownErr := receiver.Shutdown(shutdownContext); shutdownErr != nil {
                tracker.MarkIntegrityFailure("receiver shutdown failed")
                if returned == nil {
                    returned = fmt.Errorf("%w: %v", ErrUntrustworthy, shutdownErr)
                }
            }
            cancel()
        }
        buffer.Close()
        if !drainConsumed {
            if drainErr := <-drainDone; drainErr != nil && returned == nil {
                tracker.MarkIntegrityFailure("persistence failed")
                returned = fmt.Errorf("%w: %v", ErrPersistenceFailed, drainErr)
            }
        }
        if closeErr := store.Close(); closeErr != nil && returned == nil {
            returned = closeErr
        }
        result.Diagnostics = tracker.Snapshot()
        if !request.Config.KeepArtifacts {
            if cleanupErr := s.dependencies.RemoveAll(artifactDirectory); returned == nil && cleanupErr != nil {
                returned = cleanupErr
            }
            result.ArtifactPath = ""
        }
    }()

    localEndpoint, err := receiver.Start()
    if err != nil {
        return Result{}, err
    }
    receiverStarted = true
    s.debug(request.Config.Verbose, "receiver_started", map[string]any{"endpoint": localEndpoint})
    endpoint := localEndpoint
    if request.Config.ExportEndpoint != "" {
        endpoint = request.Config.ExportEndpoint
    }
    result.ExecutionID = s.dependencies.IDs()

    for run := 0; run < request.Runs; run++ {
        runID := s.dependencies.IDs()
        result.RunIDs = append(result.RunIDs, runID)
        s.debug(request.Config.Verbose, "run_started", map[string]any{"run": run + 1, "run_id": runID})
        existing := envValue(request.BaseEnv, "OTEL_RESOURCE_ATTRIBUTES")
        attributes, err := MergeResourceAttributes(existing, map[string]string{
            "tracebudget.execution_id": result.ExecutionID,
            "tracebudget.run_id":       runID,
        })
        if err != nil {
            tracker.MarkIntegrityFailure(err.Error())
            return result, fmt.Errorf("%w: %v", ErrUntrustworthy, err)
        }
        environment := supervisor.MergeEnv(request.BaseEnv, map[string]string{
            "OTEL_EXPORTER_OTLP_TRACES_ENDPOINT": endpoint,
            "OTEL_EXPORTER_OTLP_TRACES_PROTOCOL": "http/protobuf",
            "OTEL_RESOURCE_ATTRIBUTES":           attributes,
            "OTEL_TRACES_SAMPLER":                "always_on",
        })
        child := s.dependencies.Runner.Run(ctx, supervisor.Command{
            Argv: request.Argv, Env: environment, Dir: request.Dir,
            Timeout: request.Config.CommandTimeout, Stdout: os.Stdout, Stderr: os.Stderr,
        })
        if child.Err != nil || child.ExitCode != 0 || child.TimedOut || child.Cancelled {
            tracker.MarkIntegrityFailure("scenario command failed")
            flushContext, cancelFlush := context.WithTimeout(context.Background(), request.Config.FlushTimeout)
            _ = waitForQuietPeriod(flushContext, tracker, request.Config.FlushTimeout, request.Config.QuietPeriod)
            cancelFlush()
            return result, fmt.Errorf("%w: exit=%d", ErrChildFailed, child.ExitCode)
        }
        s.debug(request.Config.Verbose, "run_finished", map[string]any{"run": run + 1, "duration": child.Duration.String()})
    }

    if err := waitForQuietPeriod(ctx, tracker, request.Config.FlushTimeout, request.Config.QuietPeriod); err != nil {
        tracker.MarkIntegrityFailure("telemetry flush timed out")
        return result, fmt.Errorf("%w: %v", ErrUntrustworthy, err)
    }
    result.CaptureEndedAt = s.dependencies.Now()
    s.debug(request.Config.Verbose, "flush_finished", map[string]any{"received": tracker.Snapshot().Received})
    shutdownContext, cancelShutdown := context.WithTimeout(context.Background(), request.Config.FlushTimeout)
    err = receiver.Shutdown(shutdownContext)
    cancelShutdown()
    receiverStarted = false
    if err != nil {
        tracker.MarkIntegrityFailure("receiver shutdown failed")
        return result, fmt.Errorf("%w: %v", ErrUntrustworthy, err)
    }
    buffer.Close()
    if err := <-drainDone; err != nil {
        drainConsumed = true
        tracker.MarkIntegrityFailure("persistence failed")
        return result, fmt.Errorf("%w: %v", ErrPersistenceFailed, err)
    }
    drainConsumed = true
    result.Spans, err = store.SpansForExecution(ctx, result.ExecutionID)
    result.Diagnostics = tracker.Snapshot()
    if err != nil || result.Diagnostics.IntegrityFailed {
        return result, fmt.Errorf("%w: %v", ErrUntrustworthy, err)
    }
    s.debug(request.Config.Verbose, "capture_finished", map[string]any{"spans": len(result.Spans), "peak_buffer": result.Diagnostics.PeakBuffer})
    return result, nil
}

func (s Session) debug(enabled bool, event string, fields map[string]any) {
    if !enabled || s.dependencies.Debug == nil {
        return
    }
    record := make(map[string]any, len(fields)+1)
    record["event"] = event
    for key, value := range fields {
        record[key] = value
    }
    _ = json.NewEncoder(s.dependencies.Debug).Encode(record)
}

func startDrainWorker(ctx context.Context, buffer *ingest.Buffer, store SpanStore, tracker *diagnostics.Tracker, batchSize int) <-chan error {
    done := make(chan error, 1)
    go func() {
        defer close(done)
        for {
            batch, err := buffer.Take(ctx, batchSize)
            if errors.Is(err, ingest.ErrClosed) {
                done <- nil
                return
            }
            if err != nil {
                tracker.MarkIntegrityFailure(err.Error())
                done <- err
                return
            }
            writeStarted := time.Now()
            written, err := store.WriteBatch(ctx, batch)
            tracker.AddSQLiteWriteDuration(time.Since(writeStarted))
            if err != nil {
                tracker.MarkIntegrityFailure(err.Error())
                done <- err
                return
            }
            tracker.AddDuplicates(written.Duplicates)
        }
    }()
    return done
}

func waitForQuietPeriod(ctx context.Context, tracker *diagnostics.Tracker, timeout, quiet time.Duration) error {
    if timeout <= 0 || quiet <= 0 || quiet > timeout {
        return errors.New("flush timeout must be positive and at least the quiet period")
    }
    deadline := time.NewTimer(timeout)
    defer deadline.Stop()
    ticker := time.NewTicker(25 * time.Millisecond)
    defer ticker.Stop()
    previous := tracker.Snapshot().Received
    unchangedSince := time.Now()
    for {
        select {
        case <-ctx.Done():
            return ctx.Err()
        case <-deadline.C:
            return errors.New("flush deadline exceeded")
        case now := <-ticker.C:
            current := tracker.Snapshot().Received
            if current != previous {
                previous = current
                unchangedSince = now
            }
            if now.Sub(unchangedSince) >= quiet {
                return nil
            }
        }
    }
}

func envValue(environment []string, key string) string {
    prefix := key + "="
    for _, entry := range environment {
        if strings.HasPrefix(entry, prefix) {
            return strings.TrimPrefix(entry, prefix)
        }
    }
    return ""
}
```

- [ ] **Step 7: Run lifecycle tests, races, and the full suite**

Run: `gofmt -w internal/capture && go test -race ./internal/capture -v && go test ./...`  
Expected: PASS; no accepted span is lost, and every failure path returns an untrustworthy result.

- [ ] **Step 8: Commit capture orchestration**

```bash
git add internal/capture
git commit -m "feat: orchestrate trustworthy trace captures"
```

## Task 12: Expose `record` and `compare` Through the CLI

**Files:**
- Create: `internal/app/service.go`
- Create: `internal/app/service_test.go`
- Create: `internal/cli/root.go`
- Create: `internal/cli/common.go`
- Create: `internal/cli/record.go`
- Create: `internal/cli/compare.go`
- Create: `internal/cli/root_test.go`
- Create: `cmd/tracebudget/main.go`
- Modify: `go.mod`
- Modify: `go.sum`

**Interfaces:**
- Consumes: Tasks 5–11.
- Produces: `app.Service.Record`, `app.Service.Compare`, `cli.Execute(context.Context, []string, io.Writer, io.Writer) int`, and the public `tracebudget` binary.

- [ ] **Step 1: Pin Cobra and write failing application-use-case tests**

Run: `go get github.com/spf13/cobra@v1.10.2`

```go
func TestRecordWritesBaselineOnlyAfterTrustworthyCapture(t *testing.T) {
    directory := t.TempDir()
    service := fixtureService(t, successfulCapture())
    result, err := service.Record(context.Background(), RecordRequest{
        Scenario: "checkout", ProjectRoot: directory, Runs: 20,
        Root: model.RootSelector{Service: "scenario", Span: "checkout", ExpectedPerRun: 1},
        Argv: []string{"scenario"},
    })
    if err != nil {
        t.Fatal(err)
    }
    if result.Path != filepath.Join(directory, ".tracebudget", "checkout.yaml") {
        t.Fatalf("got %q", result.Path)
    }
    if _, err := baseline.Load(result.Path); err != nil {
        t.Fatal(err)
    }
}

func TestRecordDoesNotWriteBaselineAfterCaptureFailure(t *testing.T) {
    directory := t.TempDir()
    service := fixtureService(t, failedCapture(ingest.ErrOverloaded))
    _, err := service.Record(context.Background(), RecordRequest{
        Scenario: "checkout", ProjectRoot: directory, Runs: 1, Argv: []string{"scenario"},
    })
    if err == nil {
        t.Fatal("expected capture failure")
    }
    if _, statErr := os.Stat(filepath.Join(directory, ".tracebudget", "checkout.yaml")); !errors.Is(statErr, os.ErrNotExist) {
        t.Fatalf("baseline should not exist: %v", statErr)
    }
}

func TestComparePassesWithoutRewritingBaseline(t *testing.T) {
    directory := t.TempDir()
    service := fixtureService(t, successfulCapture())
    recorded, err := service.Record(context.Background(), RecordRequest{
        Scenario: "checkout", ProjectRoot: directory, Runs: 20,
        Root: model.RootSelector{Service: "scenario", Span: "checkout", ExpectedPerRun: 1},
        Argv: []string{"scenario"},
    })
    if err != nil {
        t.Fatal(err)
    }
    before, _ := os.ReadFile(recorded.Path)
    compared, err := service.Compare(context.Background(), CompareRequest{
        Scenario: "checkout", ProjectRoot: directory, Argv: []string{"scenario"},
    })
    if err != nil || compared.Comparison.Outcome != model.OutcomePass {
        t.Fatalf("result=%#v err=%v", compared, err)
    }
    after, _ := os.ReadFile(recorded.Path)
    if !bytes.Equal(before, after) {
        t.Fatal("compare rewrote the baseline")
    }
}

type fakeCapturer struct {
    result capture.Result
    err    error
}

func (capturer fakeCapturer) Run(context.Context, capture.Request) (capture.Result, error) {
    return capturer.result, capturer.err
}

func fixtureService(t *testing.T, capturer Capturer) Service {
    t.Helper()
    return Service{Capturer: capturer}
}

func successfulCapture() Capturer {
    ended := time.Unix(100, 0)
    result := capture.Result{ExecutionID: "exec", CaptureEndedAt: ended}
    for index := 0; index < 20; index++ {
        runID := fmt.Sprintf("run-%02d", index)
        result.RunIDs = append(result.RunIDs, runID)
        result.Spans = append(result.Spans, model.Span{
            TraceID: fmt.Sprintf("trace-%02d", index), SpanID: "root",
            ServiceName: "scenario", Name: "checkout", Kind: model.SpanKindInternal,
            ExecutionID: "exec", RunID: runID, StartUnixNano: 1, EndUnixNano: 2,
            ArrivedAt: ended.Add(-time.Second),
        })
    }
    return fakeCapturer{result: result}
}

func failedCapture(err error) Capturer { return fakeCapturer{err: err} }
```

- [ ] **Step 2: Run the application tests and verify they fail**

Run: `go test ./internal/app -v`  
Expected: FAIL because `Service`, `RecordRequest`, and `CompareRequest` are undefined.

- [ ] **Step 3: Implement the high-level use cases**

```go
type Capturer interface {
    Run(context.Context, capture.Request) (capture.Result, error)
}

type Service struct{ Capturer Capturer }

type RecordRequest struct {
    Scenario, ProjectRoot string
    Argv                   []string
    Dir                    string
    Env                    []string
    Runs                   int
    Root                   model.RootSelector
    Capture                model.CaptureConfig
    Force                  bool
}

type RecordResult struct {
    Path       string
    Document   baseline.Document
    Diagnostics diagnostics.Snapshot
    ArtifactPath string
}

type CompareRequest struct {
    Scenario, ProjectRoot string
    Argv                   []string
    Dir                    string
    Env                    []string
    Capture                model.CaptureConfig
}

type CompareResult struct {
    Comparison  compare.Result
    Observation analyze.Observation
    Evidence    analyze.Evidence
    Diagnostics diagnostics.Snapshot
    Limitations []string
    ArtifactPath string
}

func (s Service) Record(ctx context.Context, request RecordRequest) (RecordResult, error) {
    if err := model.ValidateScenarioName(request.Scenario); err != nil {
        return RecordResult{}, err
    }
    captured, err := s.Capturer.Run(ctx, capture.Request{
        Argv: request.Argv, Dir: request.Dir, BaseEnv: request.Env,
        Runs: request.Runs, Config: request.Capture,
    })
    if err != nil {
        return RecordResult{}, err
    }
    assemblyStarted := time.Now()
    assembled, err := assemble.Assemble(captured.Spans, assemble.Config{
        ExecutionID: captured.ExecutionID, Root: request.Root,
        CaptureEndedAt: captured.CaptureEndedAt, QuietPeriod: request.Capture.QuietPeriod,
        ExpectedRuns: captured.RunIDs, ExpectedPerRun: request.Root.ExpectedPerRun,
    })
    if err != nil || assembled.Incomplete > 0 {
        return RecordResult{}, fmt.Errorf("untrustworthy assembly: %w", err)
    }
    captured.Diagnostics.AssemblyDuration = time.Since(assemblyStarted)
    normalizationStarted := time.Now()
    summaries := make([]analyze.TraceSummary, 0, len(assembled.Traces))
    for _, trace := range assembled.Traces {
        summary, err := analyze.Normalize(trace)
        if err != nil {
            return RecordResult{}, err
        }
        summaries = append(summaries, summary)
    }
    captured.Diagnostics.NormalizationDuration = time.Since(normalizationStarted)
    observed := analyze.Aggregate(summaries, analyze.Evidence{
        Complete: len(assembled.Traces), Incomplete: assembled.Incomplete, Unmatched: assembled.Unmatched,
    })
    document := baseline.DefaultDocument(request.Scenario, request.Runs, assembled.Root, observed)
    path := baseline.Path(request.ProjectRoot, request.Scenario)
    if err := baseline.WriteAtomic(path, document, request.Force); err != nil {
        return RecordResult{}, err
    }
    return RecordResult{Path: path, Document: document, Diagnostics: captured.Diagnostics, ArtifactPath: captured.ArtifactPath}, nil
}

func (s Service) Compare(ctx context.Context, request CompareRequest) (CompareResult, error) {
    if err := model.ValidateScenarioName(request.Scenario); err != nil {
        return CompareResult{}, err
    }
    document, err := baseline.Load(baseline.Path(request.ProjectRoot, request.Scenario))
    if err != nil {
        return CompareResult{}, err
    }
    captured, err := s.Capturer.Run(ctx, capture.Request{
        Argv: request.Argv, Dir: request.Dir, BaseEnv: request.Env,
        Runs: document.Runs, Config: request.Capture,
    })
    if err != nil {
        return CompareResult{}, err
    }
    assemblyStarted := time.Now()
    assembled, err := assemble.Assemble(captured.Spans, assemble.Config{
        ExecutionID: captured.ExecutionID, Root: document.Root,
        CaptureEndedAt: captured.CaptureEndedAt, QuietPeriod: request.Capture.QuietPeriod,
        ExpectedRuns: captured.RunIDs, ExpectedPerRun: document.Root.ExpectedPerRun,
    })
    if err != nil {
        return CompareResult{}, fmt.Errorf("untrustworthy assembly: %w", err)
    }
    captured.Diagnostics.AssemblyDuration = time.Since(assemblyStarted)
    normalizationStarted := time.Now()
    summaries := make([]analyze.TraceSummary, 0, len(assembled.Traces))
    for _, trace := range assembled.Traces {
        summary, err := analyze.Normalize(trace)
        if err != nil {
            return CompareResult{}, err
        }
        summaries = append(summaries, summary)
    }
    captured.Diagnostics.NormalizationDuration = time.Since(normalizationStarted)
    evidence := analyze.Evidence{Complete: len(assembled.Traces), Incomplete: assembled.Incomplete, Unmatched: assembled.Unmatched}
    observed := analyze.Aggregate(summaries, evidence)
    comparisonStarted := time.Now()
    comparison, err := compare.Compare(document, observed)
    if err != nil {
        return CompareResult{}, err
    }
    captured.Diagnostics.ComparisonDuration = time.Since(comparisonStarted)
    return CompareResult{
        Comparison: comparison, Observation: observed, Evidence: evidence,
        Diagnostics: captured.Diagnostics,
        Limitations: append([]string(nil), document.Limitations...),
        ArtifactPath: captured.ArtifactPath,
    }, nil
}

func NewDefaultService() (Service, error) {
    var fallback atomic.Uint64
    identifiers := func() string {
        buffer := make([]byte, 16)
        if _, err := cryptorand.Read(buffer); err == nil {
            return hex.EncodeToString(buffer)
        }
        return fmt.Sprintf("%d-%d-%d", time.Now().UnixNano(), os.Getpid(), fallback.Add(1))
    }
    session := capture.NewSession(capture.Dependencies{
        IDs: identifiers,
        Now: time.Now,
        Runner: supervisor.Runner{},
        OpenStore: func(path string) (capture.SpanStore, error) { return sqlite.Open(path) },
        NewReceiver: func(buffer *ingest.Buffer, tracker *diagnostics.Tracker, allowed []string, bind string) capture.Receiver {
            return ingest.NewReceiver(bind, ingest.NewDecoder(allowed), buffer, tracker)
        },
        TempDir: func() (string, error) { return os.MkdirTemp("", "tracebudget-*") },
        RemoveAll: os.RemoveAll,
        Debug: os.Stderr,
    })
    return Service{Capturer: session}, nil
}
```

- [ ] **Step 4: Write failing CLI contract and exit-code tests**

```go
func TestCLIExitCodes(t *testing.T) {
    tests := []struct {
        name    string
        result app.CompareResult
        err     error
        want    int
    }{
        {"pass", app.CompareResult{Comparison: compare.Result{Outcome: model.OutcomePass}}, nil, 0},
        {"regression", app.CompareResult{Comparison: compare.Result{Outcome: model.OutcomeFail}}, nil, 1},
        {"untrustworthy", app.CompareResult{}, capture.ErrUntrustworthy, 2},
    }
    for _, test := range tests {
        t.Run(test.name, func(t *testing.T) {
            stdout, stderr := new(bytes.Buffer), new(bytes.Buffer)
            exit := executeWithService(context.Background(), []string{"compare", "checkout", "--", "scenario"}, stdout, stderr, fakeService{compareResult: test.result, err: test.err})
            if exit != test.want {
                t.Fatalf("got %d, stderr=%q", exit, stderr.String())
            }
        })
    }
}

func TestCLIRejectsMissingCommandSeparator(t *testing.T) {
    exit, stderr := executeForTest("record", "checkout")
    if exit != 2 || !strings.Contains(stderr, "command after -- is required") {
        t.Fatalf("exit=%d stderr=%q", exit, stderr)
    }
}

type fakeService struct {
    compareResult app.CompareResult
    recordResult  app.RecordResult
    err           error
}

func (service fakeService) Record(context.Context, app.RecordRequest) (app.RecordResult, error) {
    return service.recordResult, service.err
}
func (service fakeService) Compare(context.Context, app.CompareRequest) (app.CompareResult, error) {
    return service.compareResult, service.err
}

func executeForTest(arguments ...string) (int, string) {
    stdout, stderr := new(bytes.Buffer), new(bytes.Buffer)
    exit := executeWithService(context.Background(), arguments, stdout, stderr, fakeService{})
    return exit, stderr.String()
}
```

- [ ] **Step 5: Implement Cobra commands, shared flags, and main**

```go
// internal/cli/root.go
func Execute(ctx context.Context, arguments []string, stdout, stderr io.Writer) int {
    service, err := app.NewDefaultService()
    if err != nil {
        fmt.Fprintln(stderr, err)
        return 2
    }
    return executeWithService(ctx, arguments, stdout, stderr, service)
}

func executeWithService(ctx context.Context, arguments []string, stdout, stderr io.Writer, service Service) int {
    exitCode := 0
    root := newRootCommand(service, stdout, stderr, &exitCode)
    root.SetArgs(arguments)
    root.SetOut(stdout)
    root.SetErr(stderr)
    root.SilenceErrors = true
    root.SilenceUsage = true
    if err := root.ExecuteContext(ctx); err != nil {
        fmt.Fprintln(stderr, err)
        return 2
    }
    return exitCode
}

type Service interface {
    Record(context.Context, app.RecordRequest) (app.RecordResult, error)
    Compare(context.Context, app.CompareRequest) (app.CompareResult, error)
}

func newRootCommand(service Service, stdout, stderr io.Writer, exitCode *int) *cobra.Command {
    root := &cobra.Command{
        Use: "tracebudget",
        Version: Version,
        Short: "Detect architectural and reliability drift from OpenTelemetry traces",
        Long: "TraceBudget records or compares complete scenario traces. Exit codes: 0 pass, 1 policy regression, 2 untrustworthy evidence or tool failure.",
    }
    root.AddCommand(newRecordCommand(service, stdout), newCompareCommand(service, stdout, exitCode))
    return root
}

var Version = "dev"
```

```go
// cmd/tracebudget/main.go
package main

import (
    "context"
    "os"

    "github.com/RafaelPanisset/tracebudget/internal/cli"
)

var version = "dev"

func main() {
    cli.Version = version
    os.Exit(cli.Execute(context.Background(), os.Args[1:], os.Stdout, os.Stderr))
}
```

```go
// internal/cli/common.go
type captureOptions struct {
    flushTimeout, quietPeriod, commandTimeout time.Duration
    bufferSpans                               int
    listen, exportEndpoint                   string
    allowedAttributes                        []string
    keepArtifacts                            bool
    verbose                                  bool
}

func defaultCaptureOptions() captureOptions {
    return captureOptions{
        flushTimeout: 5 * time.Second, quietPeriod: 500 * time.Millisecond,
        commandTimeout: 5 * time.Minute, bufferSpans: 10000, listen: "127.0.0.1:0",
    }
}

func (options *captureOptions) bind(command *cobra.Command) {
    flags := command.Flags()
    flags.DurationVar(&options.flushTimeout, "flush-timeout", options.flushTimeout, "maximum wait for final telemetry")
    flags.DurationVar(&options.quietPeriod, "quiet-period", options.quietPeriod, "required period with no new spans")
    flags.IntVar(&options.bufferSpans, "buffer-spans", options.bufferSpans, "maximum accepted spans waiting for persistence")
    flags.DurationVar(&options.commandTimeout, "command-timeout", options.commandTimeout, "timeout for each scenario command")
    flags.StringVar(&options.listen, "listen", options.listen, "OTLP/HTTP receiver address; non-loopback binding is explicit")
    flags.StringVar(&options.exportEndpoint, "export-endpoint", "", "endpoint injected into the child; defaults to the bound receiver")
    flags.StringSliceVar(&options.allowedAttributes, "allow-attribute", nil, "span attribute allowed into the baseline; repeatable")
    flags.BoolVar(&options.keepArtifacts, "keep-artifacts", false, "retain temporary SQLite evidence")
    flags.BoolVar(&options.verbose, "verbose", false, "emit structured JSON lifecycle diagnostics to stderr")
}

func (options captureOptions) config() (model.CaptureConfig, error) {
    if options.flushTimeout <= 0 || options.quietPeriod <= 0 || options.quietPeriod > options.flushTimeout {
        return model.CaptureConfig{}, errors.New("flush-timeout must be positive and at least quiet-period")
    }
    if options.bufferSpans < 1 || options.commandTimeout <= 0 {
        return model.CaptureConfig{}, errors.New("buffer-spans and command-timeout must be positive")
    }
    return model.CaptureConfig{
        FlushTimeout: options.flushTimeout, QuietPeriod: options.quietPeriod,
        BufferSpans: options.bufferSpans, CommandTimeout: options.commandTimeout,
        KeepArtifacts: options.keepArtifacts, ListenAddress: options.listen,
        ExportEndpoint: options.exportEndpoint,
        AllowedAttributes: append([]string(nil), options.allowedAttributes...),
        Verbose: options.verbose,
    }, nil
}

func scenarioCommand(command *cobra.Command, arguments []string) (string, []string, error) {
    dash := command.ArgsLenAtDash()
    if dash != 1 || len(arguments) <= dash {
        return "", nil, errors.New("exactly one scenario and a command after -- is required")
    }
    if err := model.ValidateScenarioName(arguments[0]); err != nil {
        return "", nil, err
    }
    return arguments[0], append([]string(nil), arguments[dash:]...), nil
}
```

```go
// internal/cli/record.go
func newRecordCommand(service Service, stdout io.Writer) *cobra.Command {
    captureFlags := defaultCaptureOptions()
    runs, expected := 1, 1
    rootService, rootSpan := "", ""
    force := false
    command := &cobra.Command{
        Use: "record <scenario> [flags] -- <command>",
        Short: "Capture a scenario and create its YAML baseline",
        Args: func(command *cobra.Command, arguments []string) error {
            _, _, err := scenarioCommand(command, arguments)
            return err
        },
        RunE: func(command *cobra.Command, arguments []string) error {
            scenario, argv, err := scenarioCommand(command, arguments)
            if err != nil {
                return err
            }
            if runs < 1 || expected < 1 {
                return errors.New("runs and expected-traces-per-run must be positive")
            }
            if (rootService == "") != (rootSpan == "") {
                return errors.New("root-service and root-span must be supplied together")
            }
            config, err := captureFlags.config()
            if err != nil {
                return err
            }
            workingDirectory, err := os.Getwd()
            if err != nil {
                return err
            }
            result, err := service.Record(command.Context(), app.RecordRequest{
                Scenario: scenario, ProjectRoot: workingDirectory, Argv: argv,
                Dir: workingDirectory, Env: os.Environ(), Runs: runs,
                Root: model.RootSelector{Service: rootService, Span: rootSpan, ExpectedPerRun: expected},
                Capture: config, Force: force,
            })
            if err != nil {
                return err
            }
            _, err = fmt.Fprintf(stdout, "Recorded %s\n", result.Path)
            if err == nil && result.ArtifactPath != "" {
                _, err = fmt.Fprintf(stdout, "Retained artifacts: %s\n", result.ArtifactPath)
            }
            return err
        },
    }
    captureFlags.bind(command)
    command.Flags().IntVar(&runs, "runs", 1, "scenario executions captured into the baseline")
    command.Flags().IntVar(&expected, "expected-traces-per-run", 1, "matching root traces required per execution")
    command.Flags().StringVar(&rootService, "root-service", "", "explicit root service name")
    command.Flags().StringVar(&rootSpan, "root-span", "", "explicit root span name")
    command.Flags().BoolVar(&force, "force", false, "replace an existing baseline")
    return command
}
```

```go
// internal/cli/compare.go
func newCompareCommand(service Service, stdout io.Writer, exitCode *int) *cobra.Command {
    captureFlags := defaultCaptureOptions()
    format, output := "terminal", ""
    command := &cobra.Command{
        Use: "compare <scenario> [flags] -- <command>",
        Short: "Compare a scenario with its committed baseline",
        Args: func(command *cobra.Command, arguments []string) error {
            _, _, err := scenarioCommand(command, arguments)
            return err
        },
        RunE: func(command *cobra.Command, arguments []string) error {
            scenario, argv, err := scenarioCommand(command, arguments)
            if err != nil {
                return err
            }
            if format != "terminal" && format != "markdown" {
                return errors.New("report must be terminal or markdown")
            }
            config, err := captureFlags.config()
            if err != nil {
                return err
            }
            workingDirectory, err := os.Getwd()
            if err != nil {
                return err
            }
            result, err := service.Compare(command.Context(), app.CompareRequest{
                Scenario: scenario, ProjectRoot: workingDirectory, Argv: argv,
                Dir: workingDirectory, Env: os.Environ(), Capture: config,
            })
            if err != nil {
                return err
            }
            input := report.Input{
                Scenario: scenario, Result: result.Comparison, Observation: result.Observation, Evidence: result.Evidence,
                Diagnostics: result.Diagnostics, Limitations: result.Limitations, ArtifactPath: result.ArtifactPath,
            }
            render := report.RenderTerminal
            if format == "markdown" {
                render = report.RenderMarkdown
            }
            if err := renderOutput(output, stdout, func(writer io.Writer) error { return render(writer, input) }); err != nil {
                return err
            }
            *exitCode = report.ExitCode(result.Comparison.Outcome)
            return nil
        },
    }
    captureFlags.bind(command)
    command.Flags().StringVar(&format, "report", "terminal", "report format: terminal or markdown")
    command.Flags().StringVar(&output, "output", "", "report file; defaults to stdout")
    return command
}

func renderOutput(path string, stdout io.Writer, render func(io.Writer) error) error {
    if path == "" {
        return render(stdout)
    }
    directory := filepath.Dir(path)
    if err := os.MkdirAll(directory, 0o755); err != nil {
        return err
    }
    temporary, err := os.CreateTemp(directory, ".tracebudget-report-*.tmp")
    if err != nil {
        return err
    }
    temporaryPath := temporary.Name()
    defer os.Remove(temporaryPath)
    if err := render(temporary); err != nil {
        _ = temporary.Close()
        return err
    }
    if err := temporary.Sync(); err != nil {
        _ = temporary.Close()
        return err
    }
    if err := temporary.Close(); err != nil {
        return err
    }
    if err := os.Chmod(temporaryPath, 0o644); err != nil {
        return err
    }
    return os.Rename(temporaryPath, path)
}
```

Expose these exact flags:

| Flag | Default | Commands |
| --- | ---: | --- |
| `--runs` | `1` | record |
| `--expected-traces-per-run` | `1` | record |
| `--root-service` / `--root-span` | empty | record |
| `--flush-timeout` | `5s` | both |
| `--quiet-period` | `500ms` | both |
| `--buffer-spans` | `10000` | both |
| `--command-timeout` | `5m` | both |
| `--listen` | `127.0.0.1:0` | both |
| `--export-endpoint` | empty | both |
| `--allow-attribute` | empty repeatable | both |
| `--keep-artifacts` | `false` | both |
| `--verbose` | `false` | both |
| `--force` | `false` | record |
| `--report` | `terminal` | compare |
| `--output` | stdout | compare |

`compare` gets its run count and root selector from the baseline. Cobra errors remain code `2`; policy failures become code `1` without being reported as command errors.

- [ ] **Step 6: Run CLI tests, build the binary, and inspect help**

Run:

```bash
gofmt -w internal/app internal/cli cmd/tracebudget
go mod tidy
go test -race ./internal/app ./internal/cli -v
go test ./...
go build -o ./bin/tracebudget ./cmd/tracebudget
./bin/tracebudget --help
./bin/tracebudget record --help
./bin/tracebudget compare --help
```

Expected: tests and build PASS; help shows the exact syntax, defaults, exit-code meanings, always-on sampling requirement, and comma restriction for resource attributes.

- [ ] **Step 7: Commit the usable CLI**

```bash
git add go.mod go.sum internal/app internal/cli cmd/tracebudget
git commit -m "feat: expose record and compare commands"
```

## Task 13: Build a Deterministic Docker Compose Demonstration

**Files:**
- Create: `demo/compose.yaml`
- Create: `demo/Dockerfile`
- Create: `demo/postgres/init.sql`
- Create: `demo/internal/telemetry/telemetry.go`
- Create: `demo/cmd/scenario/main.go`
- Create: `demo/cmd/gateway/main.go`
- Create: `demo/cmd/inventory/main.go`
- Create: `test/e2e/demo_test.go`
- Modify: `go.mod`
- Modify: `go.sum`

**Behavior:** Twenty root traces named `checkout` flow from `scenario` to `gateway` to `inventory` and PostgreSQL. `DEMO_VARIANT` deterministically introduces `repeat-call`, `new-dependency`, `error`, or `latency` regressions.

- [ ] **Step 1: Pin demo telemetry dependencies and write the skipped-by-default E2E test**

Run:

```bash
go get go.opentelemetry.io/otel@v1.45.0
go get go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp@v1.45.0
go get go.opentelemetry.io/otel/sdk@v1.45.0
go get github.com/jackc/pgx/v5@v5.10.0
```

```go
func TestDemoRecordAndControlledRegressions(t *testing.T) {
    if os.Getenv("TRACEBUDGET_E2E") != "1" {
        t.Skip("set TRACEBUDGET_E2E=1 to run Docker Compose acceptance tests")
    }
    binary := buildTraceBudget(t)
    repository := repositoryRoot(t)
    project := t.TempDir()
    environment := append(os.Environ(), "COMPOSE_PROJECT_NAME=tracebudget_e2e")
    t.Cleanup(func() {
        runCompose(t, repository, environment, "down", "--volumes", "--remove-orphans")
    })
    runCompose(t, repository, environment, "down", "--volumes", "--remove-orphans")
    baselineArgs := demoArgs(binary, repository, "record")
    runCommand(t, 0, project, append(append([]string(nil), environment...), "DEMO_VARIANT=baseline"), baselineArgs...)
    addDemoLatencyBudget(t, filepath.Join(project, ".tracebudget", "checkout.yaml"))

    tests := []struct {
        variant string
        code    string
    }{
        {"repeat-call", "count_increase"},
        {"new-dependency", "new_external_dependency"},
        {"error", "new_error"},
        {"latency", "p95_budget_exceeded"},
    }
    repetitions := e2eRepetitions(t)
    for repetition := 1; repetition <= repetitions; repetition++ {
        for _, test := range tests {
            t.Run(fmt.Sprintf("%02d-%s", repetition, test.variant), func(t *testing.T) {
                runCompose(t, repository, environment, "down", "--volumes", "--remove-orphans")
                result := runCommand(t, 1, project, append(append([]string(nil), environment...), "DEMO_VARIANT="+test.variant), demoArgs(binary, repository, "compare")...)
                if !strings.Contains(result.Stdout, test.code) {
                    t.Fatalf("missing %q in output:\n%s", test.code, result.Stdout)
                }
            })
        }
    }
}

func e2eRepetitions(t *testing.T) int {
    t.Helper()
    value := os.Getenv("TRACEBUDGET_E2E_REPETITIONS")
    if value == "" {
        return 1
    }
    repetitions, err := strconv.Atoi(value)
    if err != nil || repetitions < 1 {
        t.Fatalf("invalid TRACEBUDGET_E2E_REPETITIONS=%q", value)
    }
    return repetitions
}

type commandResult struct{ Stdout, Stderr string }

func repositoryRoot(t *testing.T) string {
    t.Helper()
    root, err := filepath.Abs(filepath.Join("..", ".."))
    if err != nil {
        t.Fatal(err)
    }
    return root
}

func buildTraceBudget(t *testing.T) string {
    t.Helper()
    binary := filepath.Join(t.TempDir(), "tracebudget")
    command := exec.Command("go", "build", "-o", binary, "./cmd/tracebudget")
    command.Dir = repositoryRoot(t)
    if output, err := command.CombinedOutput(); err != nil {
        t.Fatalf("build: %v\n%s", err, output)
    }
    return binary
}

func demoArgs(binary, repository, mode string) []string {
    arguments := []string{binary, mode, "checkout"}
    if mode == "record" {
        arguments = append(arguments,
            "--runs", "1", "--root-service", "scenario", "--root-span", "checkout",
            "--expected-traces-per-run", "20",
        )
    }
    arguments = append(arguments,
        "--listen", "0.0.0.0:4318",
        "--export-endpoint", "http://host.docker.internal:4318/v1/traces",
        "--", "docker", "compose", "-f", filepath.Join(repository, "demo", "compose.yaml"),
        "up", "--build", "--abort-on-container-exit", "--exit-code-from", "scenario",
    )
    return arguments
}

func runCommand(t *testing.T, wantedExit int, directory string, environment, arguments []string) commandResult {
    t.Helper()
    command := exec.Command(arguments[0], arguments[1:]...)
    command.Dir, command.Env = directory, environment
    stdout, stderr := new(bytes.Buffer), new(bytes.Buffer)
    command.Stdout, command.Stderr = stdout, stderr
    err := command.Run()
    actualExit := 0
    if err != nil {
        var exitError *exec.ExitError
        if !errors.As(err, &exitError) {
            t.Fatalf("run: %v\n%s", err, stderr.String())
        }
        actualExit = exitError.ExitCode()
    }
    if actualExit != wantedExit {
        t.Fatalf("exit=%d want=%d\nstdout:\n%s\nstderr:\n%s", actualExit, wantedExit, stdout.String(), stderr.String())
    }
    return commandResult{Stdout: stdout.String(), Stderr: stderr.String()}
}

func runCompose(t *testing.T, repository string, environment []string, arguments ...string) {
    t.Helper()
    complete := append([]string{"compose", "-f", filepath.Join(repository, "demo", "compose.yaml")}, arguments...)
    command := exec.Command("docker", complete...)
    command.Dir, command.Env = repository, environment
    if output, err := command.CombinedOutput(); err != nil {
        t.Fatalf("compose %v: %v\n%s", arguments, err, output)
    }
}

func addDemoLatencyBudget(t *testing.T, path string) {
    t.Helper()
    document, err := baseline.Load(path)
    if err != nil {
        t.Fatal(err)
    }
    limit := model.Duration(600 * time.Millisecond)
    document.Budgets.Spans = append(document.Budgets.Spans, baseline.SpanBudget{
        Service: "inventory", Name: "db.query", Kind: model.SpanKindClient, P95: &limit,
    })
    if err := baseline.WriteAtomic(path, document, true); err != nil {
        t.Fatal(err)
    }
}
```

The helper executes this exact shape, setting `DEMO_VARIANT` for Compose and using the temporary project as the CLI working directory:

```text
tracebudget record checkout --runs 1 --root-service scenario --root-span checkout \
  --expected-traces-per-run 20 --listen 0.0.0.0:4318 \
  --export-endpoint http://host.docker.internal:4318/v1/traces -- \
  docker compose -f demo/compose.yaml up --build --abort-on-container-exit --exit-code-from scenario
```

Before every run, the helper executes `docker compose -f demo/compose.yaml down --volumes --remove-orphans`; after recording it adds the explicit `inventory/db.query/CLIENT` p95 budget and writes the baseline atomically.

- [ ] **Step 2: Run the E2E test and verify its first failure**

Run: `TRACEBUDGET_E2E=1 go test ./test/e2e -run TestDemoRecordAndControlledRegressions -v`  
Expected: FAIL because the Compose file and demo programs do not exist.

- [ ] **Step 3: Implement telemetry bootstrap shared by all demo programs**

```go
// demo/internal/telemetry/telemetry.go
package telemetry

func Start(ctx context.Context, service string) (func(context.Context) error, error) {
    resource, err := resource.New(ctx,
        resource.WithFromEnv(),
        resource.WithAttributes(semconv.ServiceName(service)),
    )
    if err != nil {
        return nil, err
    }
    exporter, err := otlptracehttp.New(ctx)
    if err != nil {
        return nil, err
    }
    provider := sdktrace.NewTracerProvider(
        sdktrace.WithSampler(sdktrace.AlwaysSample()),
        sdktrace.WithBatcher(exporter),
        sdktrace.WithResource(resource),
    )
    otel.SetTracerProvider(provider)
    otel.SetTextMapPropagator(propagation.TraceContext{})
    return provider.Shutdown, nil
}
```

- [ ] **Step 4: Implement the scenario and controlled variants**

The scenario driver creates exactly 20 roots in one process invocation, propagates each root context to gateway, and flushes before exiting:

```go
func main() {
    root := context.Background()
    shutdown, err := telemetry.Start(root, "scenario")
    if err != nil {
        log.Fatal(err)
    }
    if err := waitForReady(root, "http://gateway:8080/healthz", 30*time.Second); err != nil {
        _ = shutdown(context.Background())
        log.Fatal(err)
    }
    tracer := otel.Tracer("demo/scenario")
    for index := 0; index < 20; index++ {
        ctx, span := tracer.Start(root, "checkout", trace.WithSpanKind(trace.SpanKindInternal))
        err := postScenario(ctx, "http://gateway:8080/checkout")
        if err != nil {
            span.RecordError(err)
            span.SetStatus(codes.Error, "gateway failed")
            span.End()
            _ = shutdown(context.Background())
            log.Fatal(err)
        }
        span.End()
    }
    shutdownContext, cancel := context.WithTimeout(context.Background(), 10*time.Second)
    defer cancel()
    if err := shutdown(shutdownContext); err != nil {
        log.Fatal(err)
    }
}

func postScenario(ctx context.Context, endpoint string) error {
    request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, nil)
    if err != nil {
        return err
    }
    otel.GetTextMapPropagator().Inject(ctx, propagation.HeaderCarrier(request.Header))
    response, err := http.DefaultClient.Do(request)
    if err != nil {
        return err
    }
    defer response.Body.Close()
    if response.StatusCode != http.StatusNoContent {
        return fmt.Errorf("gateway status %d", response.StatusCode)
    }
    return nil
}

func waitForReady(ctx context.Context, endpoint string, timeout time.Duration) error {
    deadline := time.Now().Add(timeout)
    for {
        request, _ := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
        response, err := http.DefaultClient.Do(request)
        if err == nil {
            _ = response.Body.Close()
            if response.StatusCode == http.StatusNoContent {
                return nil
            }
            err = fmt.Errorf("health status %d", response.StatusCode)
        }
        if time.Now().After(deadline) {
            return err
        }
        time.Sleep(100 * time.Millisecond)
    }
}
```

Gateway extracts context, starts `checkout` as `SERVER`, calls inventory, and introduces one additional `CLIENT` span only for `new-dependency`:

```go
var tracer trace.Tracer

func main() {
    processContext, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
    defer stop()
    shutdownTelemetry, err := telemetry.Start(processContext, "gateway")
    if err != nil {
        log.Fatal(err)
    }
    tracer = otel.Tracer("demo/gateway")
    mux := http.NewServeMux()
    mux.HandleFunc("POST /checkout", checkout)
    mux.HandleFunc("GET /healthz", health)
    server := &http.Server{Addr: ":8080", Handler: mux, ReadHeaderTimeout: 5 * time.Second}
    serveDone := make(chan error, 1)
    go func() { serveDone <- server.ListenAndServe() }()
    select {
    case err := <-serveDone:
        if !errors.Is(err, http.ErrServerClosed) {
            log.Print(err)
        }
    case <-processContext.Done():
    }
    shutdownContext, cancel := context.WithTimeout(context.Background(), 10*time.Second)
    defer cancel()
    _ = server.Shutdown(shutdownContext)
    if err := shutdownTelemetry(shutdownContext); err != nil {
        log.Print(err)
    }
}

func health(writer http.ResponseWriter, request *http.Request) {
    response, err := http.Get("http://inventory:8081/healthz")
    if err != nil {
        http.Error(writer, err.Error(), http.StatusServiceUnavailable)
        return
    }
    defer response.Body.Close()
    if response.StatusCode != http.StatusNoContent {
        http.Error(writer, "inventory unavailable", http.StatusServiceUnavailable)
        return
    }
    writer.WriteHeader(http.StatusNoContent)
}

func checkout(writer http.ResponseWriter, request *http.Request) {
    parent := otel.GetTextMapPropagator().Extract(request.Context(), propagation.HeaderCarrier(request.Header))
    ctx, span := tracer.Start(parent, "checkout", trace.WithSpanKind(trace.SpanKindServer))
    defer span.End()
    if os.Getenv("DEMO_VARIANT") == "new-dependency" {
        dependencyContext, dependency := tracer.Start(ctx, "inventory.price", trace.WithSpanKind(trace.SpanKindClient))
        if err := getPriceWithTrace(dependencyContext); err != nil {
            dependency.RecordError(err)
            dependency.SetStatus(codes.Error, err.Error())
            dependency.End()
            http.Error(writer, err.Error(), http.StatusBadGateway)
            return
        }
        dependency.End()
    }
    if err := postWithTrace(ctx, "http://inventory:8081/reserve"); err != nil {
        span.RecordError(err)
        span.SetStatus(codes.Error, err.Error())
        http.Error(writer, err.Error(), http.StatusBadGateway)
        return
    }
    writer.WriteHeader(http.StatusNoContent)
}

func getPriceWithTrace(ctx context.Context) error {
    request, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://inventory:8081/price", nil)
    otel.GetTextMapPropagator().Inject(ctx, propagation.HeaderCarrier(request.Header))
    response, err := http.DefaultClient.Do(request)
    if err != nil {
        return err
    }
    defer response.Body.Close()
    if response.StatusCode != http.StatusNoContent {
        return fmt.Errorf("price status %d", response.StatusCode)
    }
    return nil
}

func postWithTrace(ctx context.Context, endpoint string) error {
    request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, nil)
    if err != nil {
        return err
    }
    otel.GetTextMapPropagator().Inject(ctx, propagation.HeaderCarrier(request.Header))
    response, err := http.DefaultClient.Do(request)
    if err != nil {
        return err
    }
    defer response.Body.Close()
    if response.StatusCode != http.StatusNoContent {
        return fmt.Errorf("inventory status %d", response.StatusCode)
    }
    return nil
}
```

Inventory starts `reserve` as `SERVER`, emits `db.query` as `CLIENT`, and applies the deterministic variant:

```go
var (
    tracer   trace.Tracer
    database *pgxpool.Pool
)

func main() {
    processContext, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
    defer stop()
    shutdownTelemetry, err := telemetry.Start(processContext, "inventory")
    if err != nil {
        log.Fatal(err)
    }
    tracer = otel.Tracer("demo/inventory")
    database, err = pgxpool.New(processContext, os.Getenv("DATABASE_URL"))
    if err != nil {
        log.Fatal(err)
    }
    if err := waitForDatabase(processContext, database, 30*time.Second); err != nil {
        log.Fatal(err)
    }
    mux := http.NewServeMux()
    mux.HandleFunc("POST /reserve", reserve)
    mux.HandleFunc("GET /price", price)
    mux.HandleFunc("GET /healthz", func(writer http.ResponseWriter, _ *http.Request) { writer.WriteHeader(http.StatusNoContent) })
    server := &http.Server{Addr: ":8081", Handler: mux, ReadHeaderTimeout: 5 * time.Second}
    serveDone := make(chan error, 1)
    go func() { serveDone <- server.ListenAndServe() }()
    select {
    case err := <-serveDone:
        if !errors.Is(err, http.ErrServerClosed) {
            log.Print(err)
        }
    case <-processContext.Done():
    }
    shutdownContext, cancel := context.WithTimeout(context.Background(), 10*time.Second)
    defer cancel()
    _ = server.Shutdown(shutdownContext)
    database.Close()
    if err := shutdownTelemetry(shutdownContext); err != nil {
        log.Print(err)
    }
}

func price(writer http.ResponseWriter, request *http.Request) {
    parent := otel.GetTextMapPropagator().Extract(request.Context(), propagation.HeaderCarrier(request.Header))
    _, span := tracer.Start(parent, "price", trace.WithSpanKind(trace.SpanKindServer))
    defer span.End()
    writer.WriteHeader(http.StatusNoContent)
}

func waitForDatabase(ctx context.Context, pool *pgxpool.Pool, timeout time.Duration) error {
    deadline := time.Now().Add(timeout)
    for {
        if err := pool.Ping(ctx); err == nil {
            return nil
        } else if time.Now().After(deadline) {
            return err
        }
        time.Sleep(100 * time.Millisecond)
    }
}

func reserve(writer http.ResponseWriter, request *http.Request) {
    parent := otel.GetTextMapPropagator().Extract(request.Context(), propagation.HeaderCarrier(request.Header))
    ctx, span := tracer.Start(parent, "reserve", trace.WithSpanKind(trace.SpanKindServer))
    defer span.End()
    calls := 1
    if os.Getenv("DEMO_VARIANT") == "repeat-call" {
        calls = 2
    }
    for index := 0; index < calls; index++ {
        queryCtx, query := tracer.Start(ctx, "db.query", trace.WithSpanKind(trace.SpanKindClient))
        if os.Getenv("DEMO_VARIANT") == "latency" {
            time.Sleep(650 * time.Millisecond)
        }
        _, err := database.Exec(queryCtx, "INSERT INTO reservations (run_id) VALUES ($1)", fmt.Sprintf("%d-%d", time.Now().UnixNano(), index))
        if os.Getenv("DEMO_VARIANT") == "error" {
            err = errors.New("controlled inventory failure")
        }
        if err != nil {
            query.RecordError(err)
            query.SetStatus(codes.Error, err.Error())
        }
        query.End()
    }
    writer.WriteHeader(http.StatusNoContent)
}
```

```sql
-- demo/postgres/init.sql
CREATE TABLE reservations (
    id BIGSERIAL PRIMARY KEY,
    run_id TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
```

- [ ] **Step 5: Add the reproducible Compose topology**

```yaml
services:
  postgres:
    image: postgres:18-alpine
    environment:
      POSTGRES_DB: tracebudget
      POSTGRES_USER: tracebudget
      POSTGRES_PASSWORD: tracebudget
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U tracebudget"]
      interval: 1s
      timeout: 2s
      retries: 30
    volumes:
      - ./postgres/init.sql:/docker-entrypoint-initdb.d/init.sql:ro
  inventory:
    build:
      context: ..
      dockerfile: demo/Dockerfile
      target: inventory
    environment: &telemetry
      OTEL_EXPORTER_OTLP_TRACES_ENDPOINT: ${OTEL_EXPORTER_OTLP_TRACES_ENDPOINT}
      OTEL_EXPORTER_OTLP_TRACES_PROTOCOL: http/protobuf
      OTEL_TRACES_SAMPLER: always_on
      OTEL_RESOURCE_ATTRIBUTES: ${OTEL_RESOURCE_ATTRIBUTES}
      DEMO_VARIANT: ${DEMO_VARIANT:-baseline}
      DATABASE_URL: postgres://tracebudget:tracebudget@postgres:5432/tracebudget?sslmode=disable
    depends_on:
      postgres:
        condition: service_healthy
    extra_hosts:
      - "host.docker.internal:host-gateway"
  gateway:
    build:
      context: ..
      dockerfile: demo/Dockerfile
      target: gateway
    environment: *telemetry
    depends_on:
      - inventory
    extra_hosts:
      - "host.docker.internal:host-gateway"
  scenario:
    build:
      context: ..
      dockerfile: demo/Dockerfile
      target: scenario
    environment: *telemetry
    extra_hosts:
      - "host.docker.internal:host-gateway"
    depends_on:
      - gateway
```

```dockerfile
# demo/Dockerfile
FROM golang:1.26-alpine AS build
WORKDIR /source
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -o /out/scenario ./demo/cmd/scenario \
 && CGO_ENABLED=0 GOOS=linux go build -trimpath -o /out/gateway ./demo/cmd/gateway \
 && CGO_ENABLED=0 GOOS=linux go build -trimpath -o /out/inventory ./demo/cmd/inventory

FROM gcr.io/distroless/static-debian13:nonroot AS scenario
COPY --from=build /out/scenario /scenario
ENTRYPOINT ["/scenario"]

FROM gcr.io/distroless/static-debian13:nonroot AS gateway
COPY --from=build /out/gateway /gateway
EXPOSE 8080
ENTRYPOINT ["/gateway"]

FROM gcr.io/distroless/static-debian13:nonroot AS inventory
COPY --from=build /out/inventory /inventory
EXPOSE 8081
ENTRYPOINT ["/inventory"]
```

- [ ] **Step 6: Run the controlled E2E matrix and full suite**

Run:

```bash
gofmt -w demo test/e2e
go mod tidy
go test ./...
TRACEBUDGET_E2E=1 go test ./test/e2e -run TestDemoRecordAndControlledRegressions -v
```

Expected: PASS. Baseline returns `0`; every controlled regression returns `1` with its expected finding; no run returns `2`.

- [ ] **Step 7: Commit the proof-oriented demonstration**

```bash
git add go.mod go.sum demo test/e2e
git commit -m "feat: add deterministic system design demo"
```

## Task 14: Add CI, Verified Releases, a GitHub Action, and Public Documentation

**Files:**
- Create: `.github/workflows/ci.yml`
- Create: `.github/workflows/release.yml`
- Create: `.github/workflows/stability.yml`
- Create: `action.yml`
- Create: `scripts/install.sh`
- Create: `README.md`
- Create: `docs/quickstart.md`
- Create: `LICENSE`

- [ ] **Step 1: Write a failing release/install smoke test**

```go
func TestInstallScriptVerifiesChecksum(t *testing.T) {
    architecture := map[string]string{"amd64": "amd64", "arm64": "arm64"}[runtime.GOARCH]
    if architecture == "" {
        t.Skip("installer supports Linux amd64 and arm64")
    }
    version := "v0.1.0"
    archiveName := fmt.Sprintf("tracebudget_%s_linux_%s.tar.gz", version, architecture)
    archive := fakeReleaseArchive(t, fmt.Sprintf("tracebudget_%s_linux_%s/tracebudget", version, architecture), []byte("fake-binary"))
    checksum := sha256.Sum256(archive)
    var corrupt atomic.Bool
    server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
        switch path.Base(request.URL.Path) {
        case archiveName:
            _, _ = writer.Write(archive)
        case "SHA256SUMS":
            value := checksum
            if corrupt.Load() {
                value[0] ^= 0xff
            }
            _, _ = fmt.Fprintf(writer, "%x  %s\n", value, archiveName)
        default:
            http.NotFound(writer, request)
        }
    }))
    defer server.Close()

    binaryDirectory := t.TempDir()
    command := exec.Command("sh", filepath.Join(repositoryRoot(t), "scripts", "install.sh"), version, binaryDirectory)
    command.Env = append(os.Environ(), "TRACEBUDGET_RELEASE_BASE_URL="+server.URL)
    if output, err := command.CombinedOutput(); err != nil {
        t.Fatalf("install: %v\n%s", err, output)
    }
    installed := filepath.Join(binaryDirectory, "tracebudget")
    contents, err := os.ReadFile(installed)
    if err != nil || string(contents) != "fake-binary" {
        t.Fatalf("contents=%q err=%v", contents, err)
    }
    information, _ := os.Stat(installed)
    if information.Mode().Perm() != 0o755 {
        t.Fatalf("mode=%o", information.Mode().Perm())
    }

    corrupt.Store(true)
    command = exec.Command("sh", filepath.Join(repositoryRoot(t), "scripts", "install.sh"), version, t.TempDir())
    command.Env = append(os.Environ(), "TRACEBUDGET_RELEASE_BASE_URL="+server.URL)
    if output, err := command.CombinedOutput(); err == nil {
        t.Fatalf("corrupt checksum was accepted:\n%s", output)
    }
}

func fakeReleaseArchive(t *testing.T, name string, contents []byte) []byte {
    t.Helper()
    var output bytes.Buffer
    gzipWriter := gzip.NewWriter(&output)
    tarWriter := tar.NewWriter(gzipWriter)
    if err := tarWriter.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(contents))}); err != nil {
        t.Fatal(err)
    }
    if _, err := tarWriter.Write(contents); err != nil {
        t.Fatal(err)
    }
    if err := tarWriter.Close(); err != nil {
        t.Fatal(err)
    }
    if err := gzipWriter.Close(); err != nil {
        t.Fatal(err)
    }
    return output.Bytes()
}
```

Run:

`go test ./test/e2e -run TestInstallScriptVerifiesChecksum -v`  
Expected: FAIL because `scripts/install.sh` does not exist.

- [ ] **Step 2: Implement CI with unit, race, vet, build, and Docker acceptance jobs**

```yaml
# .github/workflows/ci.yml
name: CI
on:
  pull_request:
  push:
    branches: [main]
permissions:
  contents: read
jobs:
  test:
    runs-on: ubuntu-24.04
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v6
        with:
          go-version: 1.26.x
          cache: true
      - run: go test -race ./...
      - run: go vet ./...
      - run: go build ./cmd/tracebudget
      - run: git diff --exit-code
  e2e:
    runs-on: ubuntu-24.04
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v6
        with:
          go-version: 1.26.x
          cache: true
      - run: TRACEBUDGET_E2E=1 go test ./test/e2e -run TestDemoRecordAndControlledRegressions -v
```

```yaml
# .github/workflows/stability.yml
name: Demo stability
on:
  workflow_dispatch:
  schedule:
    - cron: "17 4 * * 1"
permissions:
  contents: read
jobs:
  repeat-demo:
    runs-on: ubuntu-24.04
    timeout-minutes: 90
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v6
        with:
          go-version: 1.26.x
          cache: true
      - run: TRACEBUDGET_E2E=1 TRACEBUDGET_E2E_REPETITIONS=50 go test ./test/e2e -run TestDemoRecordAndControlledRegressions -v -timeout 80m
```

- [ ] **Step 3: Implement reproducible release artifacts and checksum verification**

```yaml
# .github/workflows/release.yml
name: Release
on:
  push:
    tags: ["v*"]
permissions:
  contents: write
jobs:
  release:
    runs-on: ubuntu-24.04
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v6
        with:
          go-version: 1.26.x
      - name: Build Linux archives
        shell: bash
        run: |
          set -euo pipefail
          for architecture in amd64 arm64; do
            directory="tracebudget_${GITHUB_REF_NAME}_linux_${architecture}"
            mkdir -p "dist/${directory}"
            CGO_ENABLED=0 GOOS=linux GOARCH="${architecture}" go build \
              -trimpath -ldflags "-s -w -X main.version=${GITHUB_REF_NAME}" \
              -o "dist/${directory}/tracebudget" ./cmd/tracebudget
            tar -C dist -czf "dist/${directory}.tar.gz" "${directory}"
          done
          cd dist
          sha256sum *.tar.gz > SHA256SUMS
      - uses: softprops/action-gh-release@v2
        with:
          files: |
            dist/*.tar.gz
            dist/SHA256SUMS
```

```bash
# scripts/install.sh
#!/usr/bin/env sh
set -eu
version="${1:?usage: install.sh VERSION [BIN_DIR]}"
bin_directory="${2:-${HOME}/.local/bin}"
case "$(uname -m)" in
  x86_64) architecture=amd64 ;;
  aarch64|arm64) architecture=arm64 ;;
  *) echo "unsupported architecture: $(uname -m)" >&2; exit 2 ;;
esac
base_url="${TRACEBUDGET_RELEASE_BASE_URL:-https://github.com/RafaelPanisset/tracebudget/releases/download/${version}}"
archive="tracebudget_${version}_linux_${architecture}.tar.gz"
temporary="$(mktemp -d)"
trap 'rm -rf "${temporary}"' EXIT HUP INT TERM
curl --fail --location --silent --show-error "${base_url}/${archive}" -o "${temporary}/${archive}"
curl --fail --location --silent --show-error "${base_url}/SHA256SUMS" -o "${temporary}/SHA256SUMS"
(cd "${temporary}" && grep " ${archive}$" SHA256SUMS | sha256sum --check --strict)
tar -xzf "${temporary}/${archive}" -C "${temporary}"
mkdir -p "${bin_directory}"
install -m 0755 "${temporary}/tracebudget_${version}_linux_${architecture}/tracebudget" "${bin_directory}/tracebudget"
```

- [ ] **Step 4: Add a checksum-verifying composite GitHub Action**

```yaml
# action.yml
name: TraceBudget Compare
description: Compare an instrumented scenario against a committed TraceBudget baseline
inputs:
  version:
    description: TraceBudget release tag
    required: true
  scenario:
    description: Baseline scenario name
    required: true
  command:
    description: Scenario command executed by a shell
    required: true
runs:
  using: composite
  steps:
    - name: Install TraceBudget
      shell: bash
      env:
        TRACEBUDGET_VERSION: ${{ inputs.version }}
      run: '"${{ github.action_path }}/scripts/install.sh" "$TRACEBUDGET_VERSION" "${{ runner.temp }}/tracebudget-bin"'
    - name: Compare behavior
      shell: bash
      env:
        TRACEBUDGET_SCENARIO: ${{ inputs.scenario }}
        TRACEBUDGET_COMMAND: ${{ inputs.command }}
      run: |
        set +e
        report="${{ runner.temp }}/tracebudget-report.md"
        "${{ runner.temp }}/tracebudget-bin/tracebudget" compare "$TRACEBUDGET_SCENARIO" \
          --report markdown --output "$report" -- \
          bash -euo pipefail -c "$TRACEBUDGET_COMMAND"
        status=$?
        set -e
        if [[ -f "$report" ]]; then
          cat "$report" >> "$GITHUB_STEP_SUMMARY"
        fi
        exit "$status"
```

```go
func TestActionMetadataHasRequiredInputsAndVerifiedInstaller(t *testing.T) {
    contents, err := os.ReadFile(filepath.Join(repositoryRoot(t), "action.yml"))
    if err != nil {
        t.Fatal(err)
    }
    var metadata struct {
        Inputs map[string]struct {
            Required bool `yaml:"required"`
        } `yaml:"inputs"`
        Runs struct {
            Using string `yaml:"using"`
            Steps []struct {
                Run string `yaml:"run"`
            } `yaml:"steps"`
        } `yaml:"runs"`
    }
    if err := yaml.Unmarshal(contents, &metadata); err != nil {
        t.Fatal(err)
    }
    for _, input := range []string{"version", "scenario", "command"} {
        if !metadata.Inputs[input].Required {
            t.Fatalf("input %s is not required", input)
        }
    }
    if metadata.Runs.Using != "composite" || len(metadata.Runs.Steps) != 2 {
        t.Fatalf("unexpected action: %#v", metadata.Runs)
    }
    text := string(contents)
    if strings.Contains(text, "uses: main") || !strings.Contains(text, "scripts/install.sh") || !strings.Contains(text, "GITHUB_STEP_SUMMARY") || !strings.Contains(readInstaller(t), "sha256sum --check") {
        t.Fatal("action must install a pinned version through the checksum-verifying installer")
    }
}

func readInstaller(t *testing.T) string {
    t.Helper()
    contents, err := os.ReadFile(filepath.Join(repositoryRoot(t), "scripts", "install.sh"))
    if err != nil {
        t.Fatal(err)
    }
    return string(contents)
}
```

- [ ] **Step 5: Write the recruiter-facing README and practical quickstart**

Create `README.md` with this content:

````markdown
# TraceBudget

TraceBudget turns a repeatable end-to-end scenario into a versioned OpenTelemetry behavior contract and fails CI when architecture, reliability, or latency drifts.

```console
$ tracebudget record checkout --runs 1 \
    --root-service scenario --root-span checkout \
    --expected-traces-per-run 20 -- ./run-checkout-scenario
Recorded .tracebudget/checkout.yaml

$ tracebudget compare checkout -- ./run-checkout-scenario
FAIL checkout

[FAIL] count_increase inventory/db.query/CLIENT baseline=1 candidate=2

Evidence: complete=20 incomplete=0 received=80 duplicate=0 unmatched=0
$ echo $?
1

$ git diff -- .tracebudget/checkout.yaml
# The baseline stays unchanged: the candidate is compared, never recorded.
```

## Why this exists

Trace backends help inspect an incident. TraceBudget answers a different pull-request question: “Did this scenario start making a new dependency call, repeat a query, emit errors, or exceed a declared latency budget?” The committed YAML is reviewable evidence, not a dashboard snapshot.

## How it works

```mermaid
flowchart TD
    A[Scenario command] --> B[OTLP/HTTP receiver]
    B --> C[Bounded buffer]
    C --> D[Temporary SQLite]
    D --> E[Assemble and normalize]
    E --> F[YAML baseline or comparison]
```

The single Linux binary supervises the scenario, injects an always-on OTLP/HTTP endpoint, waits for a quiet flush window, rejects incomplete evidence, and removes raw SQLite artifacts by default.

## Five-minute start

```bash
go install github.com/RafaelPanisset/tracebudget/cmd/tracebudget@latest
git clone https://github.com/RafaelPanisset/tracebudget.git
cd tracebudget
tracebudget record checkout --runs 1 \
  --root-service scenario --root-span checkout \
  --expected-traces-per-run 20 \
  --listen 0.0.0.0:4318 \
  --export-endpoint http://host.docker.internal:4318/v1/traces -- \
  docker compose -f demo/compose.yaml up --build --abort-on-container-exit --exit-code-from scenario
DEMO_VARIANT=repeat-call tracebudget compare checkout \
  --listen 0.0.0.0:4318 \
  --export-endpoint http://host.docker.internal:4318/v1/traces -- \
  docker compose -f demo/compose.yaml up --build --abort-on-container-exit --exit-code-from scenario
```

See [the practical quickstart](docs/quickstart.md) for host processes, Docker networking, and explicit budgets.

## Default schema-v1 policies

| Change | Default |
| --- | --- |
| New cross-service edge | fail |
| New CLIENT or PRODUCER dependency | fail |
| New INTERNAL node | warn |
| New errors | fail |
| Count above the observed maximum | fail |
| Removed node or edge | warn |
| Incomplete trace rate | 0% |
| Latency drift without an explicit budget | informational only |

Explicit `p95` budgets block only with at least 20 complete samples. Exit `0` means pass, `1` means a policy regression, and `2` means the tool, child process, or evidence was not trustworthy.

## Trust and privacy model

- TraceBudget requires always-on sampling for the supervised scenario.
- Every expected root and every captured parent must be present before comparison.
- The receiver uses a bounded buffer; overload invalidates the run instead of dropping accepted telemetry silently.
- Raw spans live in temporary SQLite and are deleted unless `--keep-artifacts` is set.
- Baselines keep names, topology, counts, status, durations, and explicitly allowlisted attributes—not arbitrary attribute values.
- SDK-side sampling or drops that happen before the receiver cannot be detected.

## Support and evidence

The first release supports Linux amd64 and arm64, including Ubuntu GitHub Actions runners. See the [reproducible benchmark method](docs/benchmarks/method.md) and [reference Linux amd64 run](docs/benchmarks/reference-linux-amd64.md). Throughput results are evidence for the recorded machine, not a universal guarantee.

## Roadmap

After v1: dogfood against `trip-reality-check`, evaluate broker-backed distributed ingestion from benchmark data, and design multi-repository baselines. These are intentionally outside the current release.

## Contributing

Run `go test -race ./...`, `go vet ./...`, and the opt-in Docker suite with `TRACEBUDGET_E2E=1 go test ./test/e2e -v`. Open an issue before changing schema semantics.

Apache-2.0 licensed. See [LICENSE](LICENSE).
````

Create `docs/quickstart.md` with this content:

````markdown
# TraceBudget quickstart

## Prerequisites

- Linux amd64 or arm64
- Go 1.26 or a downloaded TraceBudget release
- an OpenTelemetry-instrumented scenario command
- Docker Compose only for the included demo

## Host processes

If the scenario starts its services on the host, TraceBudget injects the receiver endpoint and always-on sampler into the command:

```bash
tracebudget record checkout --runs 20 \
  --root-service scenario --root-span checkout -- \
  ./scripts/run-checkout-e2e.sh

tracebudget compare checkout -- ./scripts/run-checkout-e2e.sh
```

Commit `.tracebudget/checkout.yaml`. Add explicit budgets by editing its `budgets` section:

```yaml
budgets:
  spans:
    - service: inventory
      name: db.query
      kind: CLIENT
      p95: 600ms
      max_per_trace: 1
  max_error_rate: 0
```

A blocking p95 budget requires at least 20 complete samples. Use `--report markdown --output tracebudget-report.md` for a pull-request artifact.

## Docker Compose demo

Containers cannot reach a receiver bound only to host loopback. `--listen 0.0.0.0:4318` explicitly exposes the receiver to Docker, while `--export-endpoint http://host.docker.internal:4318/v1/traces` injects the address containers can resolve. The demo maps that hostname to the Linux host gateway.

```bash
tracebudget record checkout --runs 1 \
  --root-service scenario --root-span checkout \
  --expected-traces-per-run 20 \
  --listen 0.0.0.0:4318 \
  --export-endpoint http://host.docker.internal:4318/v1/traces -- \
  docker compose -f demo/compose.yaml up --build --abort-on-container-exit --exit-code-from scenario

docker compose -f demo/compose.yaml down --volumes --remove-orphans

DEMO_VARIANT=repeat-call tracebudget compare checkout \
  --listen 0.0.0.0:4318 \
  --export-endpoint http://host.docker.internal:4318/v1/traces -- \
  docker compose -f demo/compose.yaml up --build --abort-on-container-exit --exit-code-from scenario
```

Expected comparison exit: `1`, with `count_increase` for `inventory/db.query/CLIENT`. Exit `2` means the scenario or evidence failed; do not treat it as a regression result.
````

Fetch the canonical Apache-2.0 text without editing it:

```bash
curl --fail --location --silent --show-error \
  https://www.apache.org/licenses/LICENSE-2.0.txt -o LICENSE
```

- [ ] **Step 6: Verify packaging, metadata, links, and documentation commands**

Run:

```bash
chmod +x scripts/install.sh
go test ./test/e2e -run TestInstallScriptVerifiesChecksum -v
go test ./...
go vet ./...
go build ./cmd/tracebudget
docker compose -f demo/compose.yaml config --quiet
git diff --check
```

Expected: PASS; the install test proves checksum enforcement, Compose validates, and every README command uses the implemented flags.

- [ ] **Step 7: Commit distribution and documentation**

```bash
git add .github action.yml scripts README.md docs/quickstart.md LICENSE test/e2e/install_test.go
git commit -m "docs: publish TraceBudget workflows and quickstart"
```

## Task 15: Publish Load Evidence and Perform Final Acceptance

**Files:**
- Create: `internal/ingest/receiver_benchmark_test.go`
- Create: `internal/store/sqlite/store_benchmark_test.go`
- Create: `scripts/benchmark.sh`
- Create: `tools/loadtest/main.go`
- Create: `scripts/load-test.sh`
- Create: `docs/benchmarks/method.md`
- Create: `docs/benchmarks/reference-linux-amd64.md`
- Create: `docs/benchmarks/reference-load-linux-amd64.md`

- [ ] **Step 1: Write receiver and SQLite benchmarks with integrity assertions**

```go
func BenchmarkReceiverOTLPHTTP(b *testing.B) {
    body := benchmarkExportRequest(b, 100)
    tracker := diagnostics.NewTracker()
    sink := newCountingSink()
    receiver := NewReceiver("127.0.0.1:0", NewDecoder(nil), sink, tracker)
    endpoint, err := receiver.Start()
    if err != nil {
        b.Fatal(err)
    }
    b.Cleanup(func() { _ = receiver.Shutdown(context.Background()) })
    client := &http.Client{Timeout: 5 * time.Second}
    b.SetBytes(int64(len(body)))
    b.ResetTimer()
    b.RunParallel(func(parallel *testing.PB) {
        for parallel.Next() {
            request, _ := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(body))
            request.Header.Set("Content-Type", "application/x-protobuf")
            response, err := client.Do(request)
            if err != nil {
                b.Error(err)
                continue
            }
            _ = response.Body.Close()
            if response.StatusCode != http.StatusOK {
                b.Errorf("status=%d", response.StatusCode)
            }
        }
    })
    b.StopTimer()
    if tracker.Snapshot().IntegrityFailed {
        b.Fatal("benchmark invalidated capture integrity")
    }
    if sink.count.Load() != int64(b.N*100) {
        b.Fatalf("accepted=%d want=%d", sink.count.Load(), b.N*100)
    }
}

type countingSink struct{ count atomic.Int64 }

func newCountingSink() *countingSink { return &countingSink{} }
func (sink *countingSink) Offer(spans []model.Span) error {
    sink.count.Add(int64(len(spans)))
    return nil
}

func benchmarkExportRequest(b *testing.B, count int) []byte {
    b.Helper()
    traceID := make([]byte, 16)
    traceID[15] = 1
    spans := make([]*tracepb.Span, 0, count)
    for index := 0; index < count; index++ {
        spanID := make([]byte, 8)
        binary.BigEndian.PutUint64(spanID, uint64(index+1))
        spans = append(spans, &tracepb.Span{
            TraceId: traceID, SpanId: spanID, Name: "benchmark",
            Kind: tracepb.Span_SPAN_KIND_INTERNAL, StartTimeUnixNano: 1, EndTimeUnixNano: 2,
        })
    }
    serviceName := &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: "benchmark"}}
    request := &collectortracepb.ExportTraceServiceRequest{ResourceSpans: []*tracepb.ResourceSpans{{
        Resource: &resourcepb.Resource{Attributes: []*commonpb.KeyValue{{Key: "service.name", Value: serviceName}}},
        ScopeSpans: []*tracepb.ScopeSpans{{Spans: spans}},
    }}}
    body, err := proto.Marshal(request)
    if err != nil {
        b.Fatal(err)
    }
    return body
}
```

```go
func BenchmarkStoreWriteBatch100(b *testing.B) {
    store := openBenchmarkStore(b)
    template := benchmarkSpans(100)
    b.ResetTimer()
    for iteration := 0; iteration < b.N; iteration++ {
        batch := cloneWithUniqueTraceIDs(template, iteration)
        result, err := store.WriteBatch(context.Background(), batch)
        if err != nil {
            b.Fatal(err)
        }
        if result.Inserted != len(batch) {
            b.Fatalf("inserted=%d want=%d", result.Inserted, len(batch))
        }
    }
}

func openBenchmarkStore(b *testing.B) *Store {
    b.Helper()
    store, err := Open(filepath.Join(b.TempDir(), "benchmark.sqlite"))
    if err != nil {
        b.Fatal(err)
    }
    b.Cleanup(func() { _ = store.Close() })
    return store
}

func benchmarkSpans(count int) []model.Span {
    spans := make([]model.Span, count)
    for index := range spans {
        spans[index] = model.Span{
            TraceID: "template", SpanID: fmt.Sprintf("span-%03d", index),
            ExecutionID: "benchmark", ServiceName: "benchmark", Name: "work",
            StartUnixNano: 1, EndUnixNano: 2, ArrivedAt: time.Unix(1, int64(index)),
        }
    }
    return spans
}

func cloneWithUniqueTraceIDs(template []model.Span, iteration int) []model.Span {
    spans := append([]model.Span(nil), template...)
    for index := range spans {
        spans[index].TraceID = fmt.Sprintf("trace-%09d", iteration)
    }
    return spans
}
```

- [ ] **Step 2: Run benchmarks once and verify useful metrics are emitted**

Run: `go test -run '^$' -bench 'Benchmark(ReceiverOTLPHTTP|StoreWriteBatch100)$' -benchmem ./internal/ingest ./internal/store/sqlite`  
Expected: PASS and output includes `ns/op`, `B/op`, `allocs/op`, and receiver throughput in `MB/s`.

- [ ] **Step 3: Add a reproducible benchmark script and method document**

```bash
# scripts/benchmark.sh
#!/usr/bin/env bash
set -euo pipefail
repository_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
output="${1:-${repository_root}/docs/benchmarks/reference-linux-amd64.md}"
temporary="$(mktemp)"
trap 'rm -f "${temporary}"' EXIT
cd "${repository_root}"
go test -count=5 -run '^$' \
  -bench 'Benchmark(ReceiverOTLPHTTP|StoreWriteBatch100)$' -benchmem \
  ./internal/ingest ./internal/store/sqlite > "${temporary}"
{
  echo '# TraceBudget reference benchmark — Linux amd64'
  echo
  echo '- Date: '"$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  printf -- '- Commit: `%s`\n' "$(git rev-parse HEAD)"
  printf -- '- Go: `%s`\n' "$(go version)"
  printf -- '- Kernel: `%s`\n' "$(uname -srmo)"
  printf -- '- CPU: `%s`\n' "$(lscpu | awk -F: '/Model name/ {sub(/^[ \t]+/, "", $2); print $2; exit}')"
  echo
  echo '```text'
  cat "${temporary}"
  echo '```'
} > "${output}"
```

Create `docs/benchmarks/method.md` with this exact content:

```markdown
# Benchmark method

TraceBudget publishes two microbenchmarks:

- `BenchmarkReceiverOTLPHTTP`: parallel OTLP/HTTP protobuf requests with exactly 100 valid spans per request.
- `BenchmarkStoreWriteBatch100`: one SQLite transaction containing exactly 100 unique spans.

`scripts/benchmark.sh` runs each benchmark five times with `-benchmem` and records UTC time, source commit, Go version, kernel, architecture, and CPU model. The receiver benchmark fails if its accepted-span count differs from `b.N × 100` or capture integrity becomes invalid. The store benchmark fails unless every transaction reports exactly 100 inserts.

Compare results only on equivalent CPU, storage, Go, and kernel configurations. Containerized and virtualized hosts must identify their limits separately. These numbers are reproducible evidence for one environment, not a production throughput guarantee or a substitute for end-to-end load testing.
```

Run `chmod +x scripts/benchmark.sh` before the first benchmark.

- [ ] **Step 4: Add the deterministic increasing-rate load generator**

```go
// tools/loadtest/main.go
type measurement struct {
    TargetSpansPerSecond   int     `json:"target_spans_per_second"`
    AchievedSpansPerSecond float64 `json:"achieved_spans_per_second"`
    AcceptedSpans          int     `json:"accepted_spans"`
    CompleteTraces         int     `json:"complete_traces"`
    PeakBufferSpans        int     `json:"peak_buffer_spans"`
    HeapGrowthBytes        uint64  `json:"heap_growth_bytes"`
    SQLiteWriteMillis      float64 `json:"sqlite_write_millis"`
    AssemblyMillis         float64 `json:"assembly_millis"`
    IntegrityFailed        bool    `json:"integrity_failed"`
    Sustained              bool    `json:"sustained"`
}

type output struct {
    Measurements             []measurement `json:"measurements"`
    MaximumSustainedSpansSec int           `json:"maximum_sustained_spans_per_second"`
    SaturationInvalidated    bool          `json:"saturation_invalidated"`
}

func main() {
    result := output{SaturationInvalidated: saturationInvalidates()}
    for _, target := range []int{1000, 5000, 10000, 25000} {
        measured, err := profileTarget(context.Background(), target, 2*time.Second)
        if err != nil {
            log.Fatal(err)
        }
        result.Measurements = append(result.Measurements, measured)
        if measured.Sustained {
            result.MaximumSustainedSpansSec = target
        }
    }
    encoder := json.NewEncoder(os.Stdout)
    encoder.SetIndent("", "  ")
    if err := encoder.Encode(result); err != nil {
        log.Fatal(err)
    }
}

func profileTarget(ctx context.Context, target int, duration time.Duration) (measurement, error) {
    const spansPerRequest = 100
    directory, err := os.MkdirTemp("", "tracebudget-load-")
    if err != nil {
        return measurement{}, err
    }
    defer os.RemoveAll(directory)
    store, err := sqlite.Open(filepath.Join(directory, "spans.sqlite"))
    if err != nil {
        return measurement{}, err
    }
    defer store.Close()
    tracker := diagnostics.NewTracker()
    buffer := ingest.NewBuffer(10000, tracker)
    drainDone := drainLoad(ctx, buffer, store, tracker)
    receiver := ingest.NewReceiver("127.0.0.1:0", ingest.NewDecoder(nil), buffer, tracker)
    endpoint, err := receiver.Start()
    if err != nil {
        return measurement{}, err
    }
    client := &http.Client{Timeout: 5 * time.Second}
    requestsPerSecond := target / spansPerRequest
    ticker := time.NewTicker(time.Second / time.Duration(requestsPerSecond))
    defer ticker.Stop()
    deadline := time.NewTimer(duration)
    defer deadline.Stop()
    executionID := fmt.Sprintf("load-%d", target)
    accepted, sequence := 0, uint64(0)
    runIDs := make([]string, 0, target*int(duration/time.Second))
    runtime.GC()
    var before, after runtime.MemStats
    runtime.ReadMemStats(&before)
    started := time.Now()
loop:
    for {
        select {
        case <-ctx.Done():
            return measurement{}, ctx.Err()
        case <-deadline.C:
            break loop
        case <-ticker.C:
            body, runs, err := newLoadBody(executionID, sequence, spansPerRequest)
            if err != nil {
                return measurement{}, err
            }
            sequence += spansPerRequest
            request, _ := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
            request.Header.Set("Content-Type", "application/x-protobuf")
            response, err := client.Do(request)
            if err != nil {
                return measurement{}, err
            }
            _ = response.Body.Close()
            if response.StatusCode != http.StatusOK {
                return measurement{}, fmt.Errorf("receiver status %d", response.StatusCode)
            }
            accepted += spansPerRequest
            runIDs = append(runIDs, runs...)
        }
    }
    elapsed := time.Since(started)
    if err := receiver.Shutdown(context.Background()); err != nil {
        return measurement{}, err
    }
    buffer.Close()
    if err := <-drainDone; err != nil {
        return measurement{}, err
    }
    runtime.ReadMemStats(&after)
    spans, err := store.SpansForExecution(ctx, executionID)
    if err != nil {
        return measurement{}, err
    }
    assemblyStarted := time.Now()
    assembled, err := assemble.Assemble(spans, assemble.Config{
        ExecutionID: executionID,
        Root: model.RootSelector{Service: "loadtest", Span: "root", ExpectedPerRun: 1},
        CaptureEndedAt: time.Now().Add(time.Millisecond),
        ExpectedRuns: runIDs, ExpectedPerRun: 1,
    })
    if err != nil {
        return measurement{}, err
    }
    snapshot := tracker.Snapshot()
    achieved := float64(accepted) / elapsed.Seconds()
    return measurement{
        TargetSpansPerSecond: target, AchievedSpansPerSecond: achieved,
        AcceptedSpans: accepted, CompleteTraces: len(assembled.Traces),
        PeakBufferSpans: snapshot.PeakBuffer,
        HeapGrowthBytes: maxUint64(after.Alloc, before.Alloc) - before.Alloc,
        SQLiteWriteMillis: float64(snapshot.SQLiteWriteDuration) / float64(time.Millisecond),
        AssemblyMillis: float64(time.Since(assemblyStarted)) / float64(time.Millisecond),
        IntegrityFailed: snapshot.IntegrityFailed,
        Sustained: !snapshot.IntegrityFailed && achieved >= float64(target)*0.95,
    }, nil
}

func newLoadBody(executionID string, sequence uint64, count int) ([]byte, []string, error) {
    resourceSpans := make([]*tracepb.ResourceSpans, 0, count)
    runIDs := make([]string, 0, count)
    for index := 0; index < count; index++ {
        identity := sequence + uint64(index) + 1
        traceID := make([]byte, 16)
        spanID := make([]byte, 8)
        binary.BigEndian.PutUint64(traceID[8:], identity)
        binary.BigEndian.PutUint64(spanID, identity)
        runID := fmt.Sprintf("run-%d", identity)
        runIDs = append(runIDs, runID)
        resourceSpans = append(resourceSpans, &tracepb.ResourceSpans{
            Resource: &resourcepb.Resource{Attributes: []*commonpb.KeyValue{
                stringAttribute("service.name", "loadtest"),
                stringAttribute("tracebudget.execution_id", executionID),
                stringAttribute("tracebudget.run_id", runID),
            }},
            ScopeSpans: []*tracepb.ScopeSpans{{Spans: []*tracepb.Span{{
                TraceId: traceID, SpanId: spanID, Name: "root",
                Kind: tracepb.Span_SPAN_KIND_INTERNAL, StartTimeUnixNano: 1, EndTimeUnixNano: 2,
            }}}},
        })
    }
    body, err := proto.Marshal(&collectortracepb.ExportTraceServiceRequest{ResourceSpans: resourceSpans})
    if err != nil {
        return nil, nil, err
    }
    return body, runIDs, nil
}

func stringAttribute(key, value string) *commonpb.KeyValue {
    return &commonpb.KeyValue{Key: key, Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: value}}}
}

func drainLoad(ctx context.Context, buffer *ingest.Buffer, store *sqlite.Store, tracker *diagnostics.Tracker) <-chan error {
    done := make(chan error, 1)
    go func() {
        defer close(done)
        for {
            spans, err := buffer.Take(ctx, 500)
            if errors.Is(err, ingest.ErrClosed) {
                done <- nil
                return
            }
            if err != nil {
                done <- err
                return
            }
            started := time.Now()
            result, err := store.WriteBatch(ctx, spans)
            tracker.AddSQLiteWriteDuration(time.Since(started))
            tracker.AddDuplicates(result.Duplicates)
            if err != nil {
                done <- err
                return
            }
        }
    }()
    return done
}

func saturationInvalidates() bool {
    tracker := diagnostics.NewTracker()
    buffer := ingest.NewBuffer(10, tracker)
    err := buffer.Offer(make([]model.Span, 11))
    return errors.Is(err, ingest.ErrOverloaded) && tracker.Snapshot().IntegrityFailed
}

func maxUint64(left, right uint64) uint64 {
    if left > right {
        return left
    }
    return right
}
```

- [ ] **Step 5: Publish increasing-rate, saturation, memory, assembly, and abrupt-failure evidence**

```bash
# scripts/load-test.sh
#!/usr/bin/env bash
set -euo pipefail
repository_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
output="${1:-${repository_root}/docs/benchmarks/reference-load-linux-amd64.md}"
profile="$(mktemp)"
failure="$(mktemp)"
trap 'rm -f "${profile}" "${failure}"' EXIT
cd "${repository_root}"
go run ./tools/loadtest > "${profile}"
go test ./internal/supervisor -run TestRunnerKillsProcessGroupOnTimeout -count=1 -v > "${failure}"
{
  echo '# TraceBudget load/failure profile — Linux amd64'
  echo
  printf -- '- Date: `%s`\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  printf -- '- Commit: `%s`\n' "$(git rev-parse HEAD)"
  printf -- '- Go: `%s`\n' "$(go version)"
  printf -- '- Kernel: `%s`\n' "$(uname -srmo)"
  printf -- '- CPU: `%s`\n' "$(lscpu | awk -F: '/Model name/ {sub(/^[ \t]+/, "", $2); print $2; exit}')"
  echo
  echo '## Increasing-rate profile'
  echo
  echo '```json'
  cat "${profile}"
  echo '```'
  echo
  echo '## Abrupt child-process recovery'
  echo
  echo '```text'
  cat "${failure}"
  echo '```'
} > "${output}"
```

Extend `docs/benchmarks/method.md` with these exact paragraphs:

```markdown
## Increasing-rate and failure profile

`scripts/load-test.sh` sends batches of 100 complete root spans for two seconds at targets of 1,000, 5,000, 10,000, and 25,000 spans/second through the full HTTP → bounded buffer → SQLite → assembler path. A target is “sustained” only when achieved throughput is at least 95% of target with no integrity failure. The highest sustained target is reported, never extrapolated.

The profile records accepted and complete counts, Go heap growth, peak buffered spans, cumulative SQLite write time, and assembly time. A separate saturation probe proves an over-capacity batch irreversibly invalidates evidence. The Linux process-group test kills a supervised tree and proves its child PID disappears, covering recovery after abrupt termination.
```

Run: `chmod +x scripts/load-test.sh && ./scripts/load-test.sh`  
Expected: PASS; JSON contains all four rates, `saturation_invalidated: true`, complete count equals accepted count for every rate, and the abrupt-child test passes.

- [ ] **Step 6: Run the final acceptance matrix with fresh evidence**

Run every command independently and preserve its output in the implementation session:

```bash
gofmt -w .
go mod tidy
go test -race ./...
go vet ./...
go build ./cmd/tracebudget
TRACEBUDGET_E2E=1 go test ./test/e2e -v
./scripts/benchmark.sh
./scripts/load-test.sh
docker compose -f demo/compose.yaml config --quiet
git diff --check
```

Expected: every command exits `0`; the controlled-regression E2E assertions still observe compare exit `1`; the benchmark document identifies the exact source commit measured and its environment.

- [ ] **Step 7: Audit schema, privacy, failure semantics, and public claims**

Run:

```bash
rg -n 'TO''DO|FIX''ME|T''BD|panic\(|log\.Fatal' --glob '!demo/**' --glob '!tools/loadtest/**' --glob '!docs/**' .
rg -n 'tracebudget\.(execution_id|run_id)|service\.name' internal
rg -n 'exit code|sampling|silent|SQLite|Linux|p95|20' README.md docs/quickstart.md docs/benchmarks
```

Expected: the first command finds no production placeholders or process-terminating library code; baseline persistence contains only approved keys; public claims match tests and benchmark evidence.

- [ ] **Step 8: Commit benchmark evidence and tag the local candidate**

```bash
git add internal/ingest/receiver_benchmark_test.go internal/store/sqlite/store_benchmark_test.go tools/loadtest scripts/benchmark.sh scripts/load-test.sh docs/benchmarks
git commit -m "perf: publish reproducible TraceBudget benchmarks"
git tag v0.1.0-rc1
git status --short
```

Expected: the working tree is clean. Do not push the tag or create the GitHub repository without Rafael's explicit authorization.

## Specification Coverage Matrix

| Approved requirement | Implemented and verified in |
| --- | --- |
| OTLP/HTTP only, bounded intake, no silent accepted-span loss | Tasks 2–4, 11, 15 |
| Root/run correlation, late spans, trace completeness | Tasks 5 and 11 |
| Stable nodes, edges, counts, errors, p50/p95 | Task 6 |
| Versioned human-readable YAML with strict parsing | Task 7 |
| Fail new edges/errors/external dependencies; warn new internal nodes/removals | Task 8 |
| Blocking p95 requires at least 20 complete samples | Tasks 8 and 13 |
| `record`, `compare`, reports, and exit codes `0/1/2` | Tasks 9 and 12 |
| Structured verbose logs, internal timings, buffer depth, and metric sample counts | Tasks 2, 9, 11, and 12 |
| Temporary SQLite and cleanup/retention behavior | Tasks 4 and 11 |
| Linux amd64/arm64 distribution and CI | Task 14 |
| Markdown GitHub job summary and 50-run scheduled stability proof | Tasks 13 and 14 |
| Increasing-rate, saturation, memory, assembly, and abrupt-failure evidence | Task 15 |
| No distributed mode or `trip-reality-check` work in v1 | Global Constraints and final acceptance audit |
