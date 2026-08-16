package ingest

import (
	"bytes"
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	statuspb "google.golang.org/genproto/googleapis/rpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"github.com/RafaelPanisset/tracebudget/internal/diagnostics"
	"github.com/RafaelPanisset/tracebudget/internal/model"
)

type fakeSink struct{ err error }

func (sink fakeSink) Offer([]model.Span) error { return sink.err }

type blockingSink struct {
	entered     chan struct{}
	release     chan struct{}
	enterOnce   sync.Once
	releaseOnce sync.Once
}

func newBlockingSink() *blockingSink {
	return &blockingSink{entered: make(chan struct{}), release: make(chan struct{})}
}

func (sink *blockingSink) Offer([]model.Span) error {
	sink.enterOnce.Do(func() { close(sink.entered) })
	<-sink.release
	return nil
}

func (sink *blockingSink) unblock() {
	sink.releaseOnce.Do(func() { close(sink.release) })
}

func TestNewReceiverRejectsNilDependencies(t *testing.T) {
	validDecoder := NewDecoder(nil)
	validSink := fakeSink{}
	validTracker := diagnostics.NewTracker()
	var typedNilSink *fakeSink
	tests := []struct {
		name    string
		decoder *Decoder
		sink    SpanSink
		tracker *diagnostics.Tracker
	}{
		{"decoder", nil, validSink, validTracker},
		{"sink", validDecoder, nil, validTracker},
		{"typed nil sink", validDecoder, typedNilSink, validTracker},
		{"tracker", validDecoder, validSink, nil},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if receiver, err := NewReceiver("127.0.0.1:0", test.decoder, test.sink, test.tracker); err == nil {
				t.Fatalf("got receiver %#v", receiver)
			}
		})
	}
}

func TestReceiverStatusContract(t *testing.T) {
	protobufBody, _ := proto.Marshal(fixtureRequest(t))
	oversized := bytes.Repeat([]byte("x"), (16<<20)+1)
	tests := []struct {
		name, method, path, contentType string
		body                            []byte
		sinkError                       error
		status                          int
		otlpError                       bool
	}{
		{"protobuf", http.MethodPost, "/v1/traces", "application/x-protobuf", protobufBody, nil, 200, false},
		{"json", http.MethodPost, "/v1/traces", "application/json; charset=utf-8", fixtureJSONRequest(), nil, 200, false},
		{"wrong path", http.MethodPost, "/wrong", "application/x-protobuf", protobufBody, nil, 404, false},
		{"unsupported", http.MethodPost, "/v1/traces", "text/plain", protobufBody, nil, 415, false},
		{"malformed protobuf", http.MethodPost, "/v1/traces", "application/x-protobuf", []byte("bad"), nil, 400, true},
		{"malformed JSON", http.MethodPost, "/v1/traces", "application/json", []byte("{"), nil, 400, true},
		{"oversized", http.MethodPost, "/v1/traces", "application/x-protobuf", oversized, nil, 413, true},
		{"overloaded", http.MethodPost, "/v1/traces", "application/json", fixtureJSONRequest(), ErrOverloaded, 503, true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			tracker := diagnostics.NewTracker()
			receiver := newTestReceiver(t, "127.0.0.1:0", NewDecoder(nil), fakeSink{err: test.sinkError}, tracker)
			request := httptest.NewRequest(test.method, test.path, bytes.NewReader(test.body))
			request.Header.Set("Content-Type", test.contentType)
			response := httptest.NewRecorder()
			receiver.server.Handler.ServeHTTP(response, request)
			if response.Code != test.status {
				t.Fatalf("got %d body=%q", response.Code, response.Body.String())
			}
			if test.otlpError {
				assertOTLPErrorResponse(t, response, test.contentType)
			}
			if test.sinkError != nil && !tracker.Snapshot().IntegrityFailed {
				t.Fatal("sink rejection must invalidate capture integrity")
			}
		})
	}
}

func TestReceiverStartServesOnLoopbackAndShutdownStopsListener(t *testing.T) {
	protobufBody, err := proto.Marshal(fixtureRequest(t))
	if err != nil {
		t.Fatal(err)
	}
	receiver := newTestReceiver(t, "127.0.0.1:0", NewDecoder(nil), fakeSink{}, diagnostics.NewTracker())
	endpoint, err := receiver.Start()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := receiver.server.Close(); err != nil {
			t.Errorf("close receiver: %v", err)
		}
	})
	parsed, err := url.Parse(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	host, _, err := net.SplitHostPort(parsed.Host)
	if err != nil {
		t.Fatal(err)
	}
	if address := net.ParseIP(host); address == nil || !address.IsLoopback() {
		t.Fatalf("receiver did not bind loopback: %q", parsed.Host)
	}

	response, err := http.Post(endpoint, "application/x-protobuf", bytes.NewReader(protobufBody))
	if err != nil {
		t.Fatal(err)
	}
	if err := response.Body.Close(); err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("got %d", response.StatusCode)
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if err := receiver.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	connection, err := net.DialTimeout("tcp", parsed.Host, 100*time.Millisecond)
	if err == nil {
		_ = connection.Close()
		t.Fatal("listener still accepts connections after shutdown")
	}
}

func TestReceiverRejectsRepeatedStartAndRestartAfterShutdown(t *testing.T) {
	receiver := newTestReceiver(t, "127.0.0.1:0", NewDecoder(nil), fakeSink{}, diagnostics.NewTracker())
	if _, err := receiver.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = receiver.server.Close() })
	if endpoint, err := receiver.Start(); !errors.Is(err, ErrReceiverStarted) {
		t.Fatalf("repeated Start returned endpoint %q and error %v", endpoint, err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if err := receiver.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	if endpoint, err := receiver.Start(); !errors.Is(err, ErrReceiverStopped) {
		t.Fatalf("restart returned endpoint %q and error %v", endpoint, err)
	}
}

func TestReceiverShutdownCanRetryDrainAfterContextExpires(t *testing.T) {
	sink := newBlockingSink()
	t.Cleanup(sink.unblock)
	receiver := newTestReceiver(t, "127.0.0.1:0", NewDecoder(nil), sink, diagnostics.NewTracker())
	endpoint, err := receiver.Start()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = receiver.server.Close() })
	body, err := proto.Marshal(fixtureRequest(t))
	if err != nil {
		t.Fatal(err)
	}
	requestDone := make(chan error, 1)
	go func() {
		response, err := http.Post(endpoint, "application/x-protobuf", bytes.NewReader(body))
		if err == nil {
			err = response.Body.Close()
		}
		requestDone <- err
	}()
	<-sink.entered

	expired, cancelExpired := context.WithCancel(t.Context())
	cancelExpired()
	if err := receiver.Shutdown(expired); !errors.Is(err, context.Canceled) {
		t.Fatalf("first Shutdown error=%v", err)
	}
	sink.unblock()
	if err := <-requestDone; err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if err := receiver.Shutdown(ctx); err != nil {
		t.Fatalf("retry Shutdown: %v", err)
	}
	if endpoint, err := receiver.Start(); !errors.Is(err, ErrReceiverStopped) {
		t.Fatalf("restart returned endpoint %q and error %v", endpoint, err)
	}
}

func TestReceiverConcurrentStartOpensOneListener(t *testing.T) {
	receiver := newTestReceiver(t, "127.0.0.1:0", NewDecoder(nil), fakeSink{}, diagnostics.NewTracker())
	t.Cleanup(func() { _ = receiver.server.Close() })
	const attempts = 8
	results := make(chan error, attempts)
	var starts sync.WaitGroup
	for range attempts {
		starts.Go(func() {
			_, err := receiver.Start()
			results <- err
		})
	}
	starts.Wait()
	close(results)
	succeeded := 0
	for err := range results {
		switch {
		case err == nil:
			succeeded++
		case errors.Is(err, ErrReceiverStarted):
		default:
			t.Fatalf("unexpected Start error: %v", err)
		}
	}
	if succeeded != 1 {
		t.Fatalf("got %d successful Starts", succeeded)
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if err := receiver.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestReceiverUnexpectedServeErrorInvalidatesIntegrity(t *testing.T) {
	tracker := diagnostics.NewTracker()
	receiver := newTestReceiver(t, "127.0.0.1:0", NewDecoder(nil), fakeSink{}, tracker)
	if _, err := receiver.Start(); err != nil {
		t.Fatal(err)
	}
	receiver.mu.Lock()
	listener := receiver.listener
	done := receiver.serveDone
	receiver.mu.Unlock()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Serve did not report listener failure")
	}
	if !tracker.Snapshot().IntegrityFailed {
		t.Fatal("unexpected Serve failure must invalidate integrity")
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if err := receiver.Shutdown(ctx); err != nil {
		t.Fatalf("cleanup after Serve failure: %v", err)
	}
}

func TestWriteOTLPResponseEncodesInternalErrorAsStatus(t *testing.T) {
	tests := []string{"application/x-protobuf", "application/json"}
	for _, contentType := range tests {
		t.Run(contentType, func(t *testing.T) {
			response := httptest.NewRecorder()
			status := &statuspb.Status{Message: "encode OTLP response"}
			if err := writeOTLPResponse(response, contentType, http.StatusInternalServerError, status); err != nil {
				t.Fatal(err)
			}
			if response.Code != http.StatusInternalServerError {
				t.Fatalf("got %d", response.Code)
			}
			assertOTLPErrorResponse(t, response, contentType)
		})
	}
}

func newTestReceiver(t *testing.T, bind string, decoder *Decoder, sink SpanSink, tracker *diagnostics.Tracker) *Receiver {
	t.Helper()
	receiver, err := NewReceiver(bind, decoder, sink, tracker)
	if err != nil {
		t.Fatal(err)
	}
	return receiver
}

func assertOTLPErrorResponse(t *testing.T, response *httptest.ResponseRecorder, requestContentType string) {
	t.Helper()
	contentType := requestContentType
	if contentType == "application/json; charset=utf-8" {
		contentType = "application/json"
	}
	if got := response.Header().Get("Content-Type"); got != contentType {
		t.Fatalf("Content-Type=%q want %q", got, contentType)
	}
	status := new(statuspb.Status)
	var err error
	if contentType == "application/json" {
		err = protojson.Unmarshal(response.Body.Bytes(), status)
	} else {
		err = proto.Unmarshal(response.Body.Bytes(), status)
	}
	if err != nil {
		t.Fatalf("decode OTLP status: %v; body=%q", err, response.Body.String())
	}
	if status.Message == "" {
		t.Fatal("OTLP status message is empty")
	}
}
