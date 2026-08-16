package ingest

import (
	"encoding/hex"
	"reflect"
	"strings"
	"testing"
	"time"

	collectortracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/proto"

	"github.com/RafaelPanisset/tracebudget/internal/model"
)

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

func fixtureJSONRequest() []byte {
	return []byte(`{
  "resourceSpans": [{
    "resource": {
      "attributes": [{"key": "service.name", "value": {"stringValue": "planner"}}],
      "unknownResourceField": true
    },
    "scopeSpans": [{
      "spans": [{
        "traceId": "00112233445566778899aabbccddeeff",
        "spanId": "0011223344556677",
        "name": "trip.plan",
        "kind": 2,
        "startTimeUnixNano": "1",
        "endTimeUnixNano": "2",
        "status": {"code": 2},
        "unknownSpanField": "ignored"
      }],
      "unknownScopeField": 1
    }]
  }],
  "unknownRequestField": {}
}`)
}

func TestDecoderAcceptsProtobuf(t *testing.T) {
	request := fixtureRequest(t)
	binaryBody, _ := proto.Marshal(request)
	spans, err := NewDecoder(nil).Decode("application/x-protobuf", binaryBody, time.Unix(10, 0))
	if err != nil {
		t.Fatal(err)
	}
	if len(spans) != 2 || spans[1].Status != model.StatusError || !spans[1].HasException {
		t.Fatalf("unexpected spans: %#v", spans)
	}
}

func TestDecoderAcceptsOTLPJSONHexIDsNumericEnumsAndUnknownFields(t *testing.T) {
	spans, err := NewDecoder(nil).Decode("application/json", fixtureJSONRequest(), time.Unix(10, 0))
	if err != nil {
		t.Fatal(err)
	}
	if len(spans) != 1 {
		t.Fatalf("got %d spans", len(spans))
	}
	span := spans[0]
	if span.TraceID != "00112233445566778899aabbccddeeff" || span.SpanID != "0011223344556677" {
		t.Fatalf("unexpected identities: trace=%q span=%q", span.TraceID, span.SpanID)
	}
	if span.Kind != model.SpanKindServer || span.Status != model.StatusError {
		t.Fatalf("kind=%q status=%q", span.Kind, span.Status)
	}
}

func TestDecoderTreatsNullRepeatedFieldsAsUnset(t *testing.T) {
	body := bytesReplace(
		fixtureJSONRequest(),
		`"status": {"code": 2},`,
		`"status": {"code": 2}, "attributes": null, "events": null, "links": null,`,
	)
	spans, err := NewDecoder(nil).Decode("application/json", body, time.Unix(10, 0))
	if err != nil {
		t.Fatal(err)
	}
	if len(spans) != 1 {
		t.Fatalf("got %d spans", len(spans))
	}
}

func TestDecoderRejectsNamedEnumsInOTLPJSON(t *testing.T) {
	body := bytesReplace(fixtureJSONRequest(), `"kind": 2`, `"kind": "SPAN_KIND_SERVER"`)
	_, err := NewDecoder(nil).Decode("application/json", body, time.Unix(10, 0))
	if err == nil || !strings.Contains(err.Error(), "kind") {
		t.Fatalf("got %v", err)
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

func TestDecoderRejectsInvalidSpans(t *testing.T) {
	tests := []struct {
		name    string
		allowed []string
		mutate  func(*collectortracepb.ExportTraceServiceRequest)
	}{
		{
			name: "invalid identity",
			mutate: func(request *collectortracepb.ExportTraceServiceRequest) {
				request.ResourceSpans[0].ScopeSpans[0].Spans[0].TraceId = []byte("short")
			},
		},
		{
			name: "negative duration",
			mutate: func(request *collectortracepb.ExportTraceServiceRequest) {
				span := request.ResourceSpans[0].ScopeSpans[0].Spans[0]
				span.StartTimeUnixNano = 3
				span.EndTimeUnixNano = 2
			},
		},
		{
			name:    "allowlisted non-scalar",
			allowed: []string{"safe.collection"},
			mutate: func(request *collectortracepb.ExportTraceServiceRequest) {
				request.ResourceSpans[0].ScopeSpans[0].Spans[0].Attributes = []*commonpb.KeyValue{{
					Key:   "safe.collection",
					Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_ArrayValue{ArrayValue: &commonpb.ArrayValue{}}},
				}}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := proto.Clone(fixtureRequest(t)).(*collectortracepb.ExportTraceServiceRequest)
			test.mutate(request)
			body, err := proto.Marshal(request)
			if err != nil {
				t.Fatal(err)
			}
			spans, err := NewDecoder(test.allowed).Decode("application/x-protobuf", body, time.Unix(10, 0))
			if err == nil {
				t.Fatalf("unexpected spans: %#v", spans)
			}
			if spans != nil {
				t.Fatalf("invalid payload leaked spans: %#v", spans)
			}
		})
	}
}

func TestDecoderDoesNotReturnPartialBatch(t *testing.T) {
	request := proto.Clone(fixtureRequest(t)).(*collectortracepb.ExportTraceServiceRequest)
	request.ResourceSpans[0].ScopeSpans[0].Spans[1].SpanId = []byte("bad")
	body, err := proto.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	spans, err := NewDecoder(nil).Decode("application/x-protobuf", body, time.Unix(10, 0))
	if err == nil {
		t.Fatal("mixed valid/invalid batch must fail")
	}
	if spans != nil {
		t.Fatalf("partial batch escaped: %#v", spans)
	}
}

func bytesReplace(body []byte, old, replacement string) []byte {
	return []byte(strings.Replace(string(body), old, replacement, 1))
}
