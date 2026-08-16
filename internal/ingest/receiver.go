package ingest

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"reflect"
	"sync"
	"time"

	collectortracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	statuspb "google.golang.org/genproto/googleapis/rpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"github.com/RafaelPanisset/tracebudget/internal/diagnostics"
	"github.com/RafaelPanisset/tracebudget/internal/model"
)

// SpanSink accepts complete decoded OTLP request batches.
type SpanSink interface {
	Offer([]model.Span) error
}

var (
	// ErrReceiverNotStarted reports Shutdown before the receiver has started.
	ErrReceiverNotStarted = errors.New("OTLP HTTP receiver not started")
	// ErrReceiverStarted reports a second Start while the receiver is running.
	ErrReceiverStarted = errors.New("OTLP HTTP receiver already started")
	// ErrReceiverStopped reports Start after shutdown or terminal server failure.
	ErrReceiverStopped = errors.New("OTLP HTTP receiver stopped")
)

type receiverState uint8

const (
	receiverReady receiverState = iota
	receiverRunning
	receiverStopping
	receiverStopped
)

// Receiver accepts OTLP/HTTP trace export requests.
type Receiver struct {
	mu        sync.Mutex
	bind      string
	decoder   *Decoder
	sink      SpanSink
	tracker   *diagnostics.Tracker
	listener  net.Listener
	serveDone chan struct{}
	state     receiverState
	server    *http.Server
	maxBody   int64
}

// NewReceiver creates an OTLP/HTTP receiver bound by Start.
func NewReceiver(bind string, decoder *Decoder, sink SpanSink, tracker *diagnostics.Tracker) (*Receiver, error) {
	if decoder == nil {
		return nil, fmt.Errorf("create OTLP HTTP receiver: decoder is nil")
	}
	if isNilSpanSink(sink) {
		return nil, fmt.Errorf("create OTLP HTTP receiver: sink is nil")
	}
	if tracker == nil {
		return nil, fmt.Errorf("create OTLP HTTP receiver: tracker is nil")
	}
	receiver := &Receiver{bind: bind, decoder: decoder, sink: sink, tracker: tracker, maxBody: 16 << 20}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/traces", receiver.handleTraces)
	receiver.server = &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	return receiver, nil
}

// Start listens for OTLP/HTTP requests and returns the trace export endpoint.
func (r *Receiver) Start() (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	switch r.state {
	case receiverRunning:
		return "", ErrReceiverStarted
	case receiverStopping, receiverStopped:
		return "", ErrReceiverStopped
	}
	listener, err := net.Listen("tcp", r.bind)
	if err != nil {
		return "", fmt.Errorf("listen for OTLP HTTP: %w", err)
	}
	r.listener = listener
	r.serveDone = make(chan struct{})
	r.state = receiverRunning
	go r.serve(listener, r.serveDone)
	return "http://" + listener.Addr().String() + "/v1/traces", nil
}

// Shutdown gracefully stops the OTLP/HTTP receiver.
func (r *Receiver) Shutdown(ctx context.Context) error {
	r.mu.Lock()
	switch r.state {
	case receiverReady:
		r.mu.Unlock()
		return ErrReceiverNotStarted
	case receiverRunning:
		r.state = receiverStopping
	}
	done := r.serveDone
	r.mu.Unlock()

	if err := r.server.Shutdown(ctx); err != nil {
		return fmt.Errorf("shutdown OTLP HTTP: %w", err)
	}
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("wait for OTLP HTTP receiver: %w", ctx.Err())
	}
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
			r.writeOTLPStatus(writer, contentType, http.StatusRequestEntityTooLarge, "OTLP request exceeds 16 MiB")
		} else {
			r.writeOTLPStatus(writer, contentType, http.StatusBadRequest, "read OTLP request")
		}
		r.tracker.AddInvalid(1)
		r.tracker.MarkIntegrityFailure("invalid OTLP request body")
		return
	}
	spans, err := r.decoder.Decode(contentType, body, time.Now())
	if err != nil {
		r.tracker.AddInvalid(1)
		r.tracker.MarkIntegrityFailure("malformed OTLP payload")
		r.writeOTLPStatus(writer, contentType, http.StatusBadRequest, err.Error())
		return
	}
	if err := r.sink.Offer(spans); err != nil {
		r.tracker.MarkIntegrityFailure(err.Error())
		r.writeOTLPStatus(writer, contentType, http.StatusServiceUnavailable, "capture unavailable")
		return
	}
	response := new(collectortracepb.ExportTraceServiceResponse)
	encoded, err := marshalOTLPResponse(contentType, response)
	if err != nil {
		r.tracker.MarkIntegrityFailure(err.Error())
		r.writeOTLPStatus(writer, contentType, http.StatusInternalServerError, "encode OTLP response")
		return
	}
	if err := writeOTLPBytes(writer, contentType, http.StatusOK, encoded); err != nil {
		r.tracker.MarkIntegrityFailure(fmt.Sprintf("write OTLP response: %v", err))
	}
}

func (r *Receiver) serve(listener net.Listener, done chan struct{}) {
	err := r.server.Serve(listener)
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		r.tracker.MarkIntegrityFailure(fmt.Sprintf("serve OTLP HTTP: %v", err))
	}
	r.mu.Lock()
	if r.listener == listener {
		r.listener = nil
		r.state = receiverStopped
	}
	close(done)
	r.mu.Unlock()
}

func (r *Receiver) writeOTLPStatus(writer http.ResponseWriter, contentType string, statusCode int, message string) {
	status := &statuspb.Status{Message: message}
	if err := writeOTLPResponse(writer, contentType, statusCode, status); err != nil {
		r.tracker.MarkIntegrityFailure(fmt.Sprintf("write OTLP status response: %v", err))
	}
}

func writeOTLPResponse(writer http.ResponseWriter, contentType string, statusCode int, message proto.Message) error {
	encoded, err := marshalOTLPResponse(contentType, message)
	if err != nil {
		return err
	}
	return writeOTLPBytes(writer, contentType, statusCode, encoded)
}

func marshalOTLPResponse(contentType string, message proto.Message) ([]byte, error) {
	var (
		encoded []byte
		err     error
	)
	if contentType == "application/json" {
		encoded, err = protojson.Marshal(message)
	} else {
		encoded, err = proto.Marshal(message)
	}
	if err != nil {
		return nil, fmt.Errorf("marshal OTLP response: %w", err)
	}
	return encoded, nil
}

func writeOTLPBytes(writer http.ResponseWriter, contentType string, statusCode int, encoded []byte) error {
	writer.Header().Set("Content-Type", contentType)
	writer.WriteHeader(statusCode)
	if _, err := writer.Write(encoded); err != nil {
		return fmt.Errorf("write response: %w", err)
	}
	return nil
}

func isNilSpanSink(sink SpanSink) bool {
	if sink == nil {
		return true
	}
	value := reflect.ValueOf(sink)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}
