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
