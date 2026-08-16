package baseline

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"

	"github.com/RafaelPanisset/tracebudget/internal/model"
	"go.yaml.in/yaml/v3"
)

var (
	ErrBaselineExists    = errors.New("baseline already exists")
	ErrUnsupportedSchema = errors.New("unsupported baseline schema")
)

func Path(projectRoot, scenario string) string {
	return filepath.Join(projectRoot, ".tracebudget", scenario+".yaml")
}

func Load(path string) (Document, error) {
	contents, err := os.ReadFile(path)
	if err != nil {
		return Document{}, err
	}

	var node yaml.Node
	if err := yaml.NewDecoder(bytes.NewReader(contents)).Decode(&node); err != nil {
		return Document{}, fmt.Errorf("decode baseline: %w", err)
	}
	if err := validateIntegerScalars(&node, nil); err != nil {
		return Document{}, err
	}

	decoder := yaml.NewDecoder(bytes.NewReader(contents))
	decoder.KnownFields(true)
	var document Document
	if err := decoder.Decode(&document); err != nil {
		return Document{}, fmt.Errorf("decode baseline: %w", err)
	}
	if document.SchemaVersion != SchemaVersion {
		return Document{}, unsupportedSchema(document.SchemaVersion)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return Document{}, errors.New("baseline contains multiple YAML documents")
		}
		return Document{}, fmt.Errorf("decode trailing YAML: %w", err)
	}
	if len(document.Observed.Nodes) == 0 {
		document.Observed.Nodes = nil
	}
	if len(document.Observed.Edges) == 0 {
		document.Observed.Edges = nil
	}
	return document, validate(document)
}

func WriteAtomic(path string, document Document, force bool) error {
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
	if err := encoder.Close(); err != nil {
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
	if force {
		return os.Rename(temporaryName, path)
	}
	if err := os.Link(temporaryName, path); err != nil {
		if errors.Is(err, os.ErrExist) {
			return ErrBaselineExists
		}
		return err
	}
	return nil
}

func validate(document Document) error {
	if document.SchemaVersion != SchemaVersion {
		return unsupportedSchema(document.SchemaVersion)
	}
	if err := model.ValidateScenarioName(document.Scenario); err != nil {
		return err
	}
	if document.Runs < 1 || document.Root.Service == "" || document.Root.Span == "" || document.Root.ExpectedPerRun < 1 {
		return errors.New("baseline requires positive runs and a complete root selector")
	}
	if invalidRate(document.Policies.IncompleteTraceRate.Max) {
		return errors.New("incomplete trace rate must be between 0 and 1")
	}
	for _, policy := range []struct {
		name     string
		severity model.Severity
	}{
		{"new_service_edge", document.Policies.NewServiceEdge},
		{"new_external_dependency", document.Policies.NewExternalDependency},
		{"new_internal_node", document.Policies.NewInternalNode},
		{"new_error", document.Policies.NewError},
		{"removed_node", document.Policies.RemovedNode},
		{"removed_edge", document.Policies.RemovedEdge},
		{"count_increase", document.Policies.CountIncrease},
		{"latency_increase", document.Policies.LatencyIncrease},
	} {
		if policy.severity != model.SeverityIgnore && policy.severity != model.SeverityWarn && policy.severity != model.SeverityFail {
			return fmt.Errorf("invalid severity for %s: %q", policy.name, policy.severity)
		}
	}
	for _, budget := range document.Budgets.Spans {
		if budget.Service == "" || budget.Name == "" {
			return errors.New("span budget requires service and name")
		}
		if !validSpanBudgetKind(budget.Kind) {
			return fmt.Errorf("invalid span budget kind: %q", budget.Kind)
		}
		if budget.P95 != nil && *budget.P95 <= 0 {
			return errors.New("p95 budget must be positive")
		}
		if budget.MaxPerTrace != nil && *budget.MaxPerTrace < 0 {
			return errors.New("max_per_trace cannot be negative")
		}
	}
	if document.Budgets.MaxErrorRate != nil && invalidRate(*document.Budgets.MaxErrorRate) {
		return errors.New("max_error_rate must be between 0 and 1")
	}
	return nil
}

func invalidRate(value float64) bool {
	return math.IsNaN(value) || value < 0 || value > 1
}

func unsupportedSchema(version int) error {
	return fmt.Errorf("%w: got %d, support %d", ErrUnsupportedSchema, version, SchemaVersion)
}

func validateIntegerScalars(node *yaml.Node, path []string) error {
	switch node.Kind {
	case yaml.DocumentNode:
		for _, child := range node.Content {
			if err := validateIntegerScalars(child, path); err != nil {
				return err
			}
		}
	case yaml.MappingNode:
		for index := 0; index < len(node.Content); index += 2 {
			key := node.Content[index]
			value := node.Content[index+1]
			fieldPath := append(append([]string(nil), path...), key.Value)
			if integerField(strings.Join(fieldPath, ".")) && (value.Kind != yaml.ScalarNode || value.Tag != "!!int") {
				return fmt.Errorf("baseline field %s must be an integer", strings.Join(fieldPath, "."))
			}
			if err := validateIntegerScalars(value, fieldPath); err != nil {
				return err
			}
		}
	case yaml.SequenceNode:
		for _, child := range node.Content {
			if err := validateIntegerScalars(child, append(path, "[]")); err != nil {
				return err
			}
		}
	}
	return nil
}

func integerField(path string) bool {
	switch path {
	case "schema_version", "runs", "root.expected_per_run",
		"budgets.spans.[].max_per_trace",
		"observed.nodes.[].counts.min", "observed.nodes.[].counts.max",
		"observed.nodes.[].total", "observed.nodes.[].errors", "observed.nodes.[].durations.samples",
		"observed.edges.[].counts.min", "observed.edges.[].counts.max",
		"observed.evidence.complete", "observed.evidence.incomplete", "observed.evidence.unmatched":
		return true
	default:
		return false
	}
}

func validSpanBudgetKind(kind model.SpanKind) bool {
	switch kind {
	case "", model.SpanKindUnspecified, model.SpanKindInternal, model.SpanKindServer,
		model.SpanKindClient, model.SpanKindProducer, model.SpanKindConsumer:
		return true
	default:
		return false
	}
}
