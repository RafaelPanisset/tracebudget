package ingest

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	collectortracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/RafaelPanisset/tracebudget/internal/model"
)

// Decoder converts OTLP trace export requests into TraceBudget spans.
type Decoder struct {
	allowed map[string]struct{}
}

// NewDecoder creates a decoder that retains only the named span attributes.
func NewDecoder(allowed []string) *Decoder {
	values := make(map[string]struct{}, len(allowed))
	for _, value := range allowed {
		values[value] = struct{}{}
	}
	return &Decoder{allowed: values}
}

// Decode parses an OTLP trace request with the exact supplied media type.
func (d *Decoder) Decode(contentType string, body []byte, arrivedAt time.Time) ([]model.Span, error) {
	request := new(collectortracepb.ExportTraceServiceRequest)
	switch contentType {
	case "application/x-protobuf":
		if err := proto.Unmarshal(body, request); err != nil {
			return nil, fmt.Errorf("decode OTLP protobuf: %w", err)
		}
	case "application/json":
		if err := unmarshalOTLPJSON(body, request); err != nil {
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

func unmarshalOTLPJSON(body []byte, message proto.Message) error {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return err
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return err
	}
	converted, err := convertOTLPJSONMessage(value, message.ProtoReflect().Descriptor())
	if err != nil {
		return err
	}
	encoded, err := json.Marshal(converted)
	if err != nil {
		return fmt.Errorf("encode normalized OTLP JSON: %w", err)
	}
	if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(encoded, message); err != nil {
		return err
	}
	return nil
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return fmt.Errorf("multiple JSON values")
		}
		return err
	}
	return nil
}

func convertOTLPJSONMessage(value any, descriptor protoreflect.MessageDescriptor) (map[string]any, error) {
	object, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("message %s must be an object", descriptor.FullName())
	}
	converted := make(map[string]any, len(object))
	fields := descriptor.Fields()
	for name, fieldValue := range object {
		field := fields.ByJSONName(name)
		if field == nil {
			continue
		}
		value, err := convertOTLPJSONField(fieldValue, field)
		if err != nil {
			return nil, fmt.Errorf("field %q: %w", name, err)
		}
		converted[name] = value
	}
	return converted, nil
}

func convertOTLPJSONField(value any, field protoreflect.FieldDescriptor) (any, error) {
	if value == nil {
		return nil, nil
	}
	if field.IsList() {
		values, ok := value.([]any)
		if !ok {
			return nil, fmt.Errorf("must be an array")
		}
		converted := make([]any, len(values))
		for index, item := range values {
			value, err := convertOTLPJSONSingular(item, field)
			if err != nil {
				return nil, fmt.Errorf("item %d: %w", index, err)
			}
			converted[index] = value
		}
		return converted, nil
	}
	return convertOTLPJSONSingular(value, field)
}

func convertOTLPJSONSingular(value any, field protoreflect.FieldDescriptor) (any, error) {
	if field.Kind() == protoreflect.MessageKind {
		return convertOTLPJSONMessage(value, field.Message())
	}
	if field.Kind() == protoreflect.EnumKind {
		number, ok := value.(json.Number)
		if !ok {
			return nil, fmt.Errorf("enum value must be an integer")
		}
		if _, err := strconv.ParseInt(number.String(), 10, 32); err != nil {
			return nil, fmt.Errorf("enum value must be an integer: %w", err)
		}
		return value, nil
	}
	if field.Kind() == protoreflect.BytesKind && isOTLPIdentity(field) {
		text, ok := value.(string)
		if !ok {
			return nil, fmt.Errorf("identity must be a hex string")
		}
		identity, err := hex.DecodeString(text)
		if err != nil {
			return nil, fmt.Errorf("decode hex identity: %w", err)
		}
		return base64.StdEncoding.EncodeToString(identity), nil
	}
	return value, nil
}

func isOTLPIdentity(field protoreflect.FieldDescriptor) bool {
	name := string(field.Name())
	return strings.HasSuffix(name, "trace_id") || strings.HasSuffix(name, "span_id")
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
