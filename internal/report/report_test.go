package report

import (
	"bytes"
	"errors"
	"io"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/RafaelPanisset/tracebudget/internal/analyze"
	"github.com/RafaelPanisset/tracebudget/internal/compare"
	"github.com/RafaelPanisset/tracebudget/internal/diagnostics"
	"github.com/RafaelPanisset/tracebudget/internal/model"
)

func TestExitCode(t *testing.T) {
	tests := map[model.Outcome]int{
		model.OutcomePass:  0,
		model.OutcomeFail:  1,
		model.OutcomeError: 2,
		"unknown":          2,
	}
	for outcome, expected := range tests {
		if got := ExitCode(outcome); got != expected {
			t.Fatalf("%s: got %d, want %d", outcome, got, expected)
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

func TestRenderPassWithoutFindings(t *testing.T) {
	input := Input{Scenario: "checkout", Result: compare.Result{Outcome: model.OutcomePass}}
	tests := []struct {
		name   string
		render func(io.Writer, Input) error
		want   string
	}{
		{name: "terminal", render: RenderTerminal, want: "PASS checkout\n\nNo regressions.\n\nEvidence: complete=0 incomplete=0 received=0 duplicate=0 unmatched=0 invalid=0 buffer_current=0 buffer_peak=0\nTiming: sqlite=0s assembly=0s normalization=0s comparison=0s\n"},
		{name: "markdown", render: RenderMarkdown, want: "# TraceBudget: checkout — PASS\n\nNo regressions.\n\n## Evidence\n\n- Complete traces: 0\n- Incomplete traces: 0\n- Received spans: 0\n- Exact duplicates: 0\n- Unmatched traces: 0\n- Invalid spans: 0\n- Current buffer depth: 0\n- Peak buffer depth: 0\n\n## Timing\n\n- SQLite writes: 0s\n- Assembly: 0s\n- Normalization: 0s\n- Comparison: 0s\n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var output bytes.Buffer
			if err := test.render(&output, input); err != nil {
				t.Fatal(err)
			}
			if got := output.String(); got != test.want {
				t.Fatalf("got:\n%s\nwant:\n%s", got, test.want)
			}
		})
	}
}

func TestRenderTerminalGroupsContiguousSeveritiesWithoutReorderingFindings(t *testing.T) {
	input := Input{Result: compare.Result{Findings: []model.Finding{
		{Code: "first_fail", Severity: model.SeverityFail, Subject: "one", Baseline: "1", Candidate: "2"},
		{Code: "second_fail", Severity: model.SeverityFail, Subject: "two", Baseline: "1", Candidate: "2"},
		{Code: "warning", Severity: model.SeverityWarn, Subject: "three", Baseline: "1", Candidate: "2"},
		{Code: "last_fail", Severity: model.SeverityFail, Subject: "four", Baseline: "1", Candidate: "2"},
	}}}
	var output bytes.Buffer
	if err := RenderTerminal(&output, input); err != nil {
		t.Fatal(err)
	}
	want := "FAIL:\n[FAIL] first_fail one baseline=1 candidate=2\n[FAIL] second_fail two baseline=1 candidate=2\nWARN:\n[WARN] warning three baseline=1 candidate=2\nFAIL:\n[FAIL] last_fail four baseline=1 candidate=2\n"
	if !strings.Contains(output.String(), want) {
		t.Fatalf("findings are not grouped in supplied order:\n%s", output.String())
	}
}

func TestRenderTerminalGroupsZeroAndCustomSeveritiesWithoutReorderingFindings(t *testing.T) {
	input := Input{Result: compare.Result{Findings: []model.Finding{
		{Code: "zero", Subject: "one", Baseline: "1", Candidate: "2"},
		{Code: "custom", Severity: model.Severity("custom\nseverity"), Subject: "two", Baseline: "1", Candidate: "2"},
	}}}
	var output bytes.Buffer
	if err := RenderTerminal(&output, input); err != nil {
		t.Fatal(err)
	}
	want := "UNKNOWN:\n[UNKNOWN] zero one baseline=1 candidate=2\nCUSTOM SEVERITY:\n[CUSTOM SEVERITY] custom two baseline=1 candidate=2\n"
	if !strings.Contains(output.String(), want) {
		t.Fatalf("zero and custom severities did not form stable groups in supplied order:\n%s", output.String())
	}
}

func TestRenderIncludesOptionalArtifactsAndLimitations(t *testing.T) {
	input := failureInput()
	input.ArtifactPath = "artifacts/checkout.trace"
	input.Limitations = []string{"one complete trace was excluded", "sampling was disabled"}
	tests := []struct {
		name   string
		render func(io.Writer, Input) error
		want   []string
	}{
		{name: "terminal", render: RenderTerminal, want: []string{"Artifacts: artifacts/checkout.trace", "Limitations:\n- one complete trace was excluded\n- sampling was disabled"}},
		{name: "markdown", render: RenderMarkdown, want: []string{"## Retained artifacts\n\n`artifacts/checkout.trace`", "## Limitations\n\n- one complete trace was excluded\n- sampling was disabled"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var output bytes.Buffer
			if err := test.render(&output, input); err != nil {
				t.Fatal(err)
			}
			for _, want := range test.want {
				if !strings.Contains(output.String(), want) {
					t.Fatalf("output does not contain %q:\n%s", want, output.String())
				}
			}
		})
	}
}

func TestRenderSanitizesUserControlledValues(t *testing.T) {
	input := Input{
		Scenario:     "check|out\r\n# escaped",
		Result:       compare.Result{Outcome: model.OutcomeFail, Findings: []model.Finding{{Code: "code|`\r\nnext", Severity: model.SeverityFail, Subject: "subject|`\r\nnext", Baseline: "base|`\r\nnext", Candidate: "candidate|`\r\nnext"}}},
		Observation:  analyze.Observation{Nodes: []analyze.NodeObservation{{Key: model.NodeKey{Service: "service|`\r\nnext", Name: "span", Kind: model.SpanKindClient}, Durations: analyze.DurationSummary{Samples: 1}}}},
		ArtifactPath: "artifact|`\r\nnext", Limitations: []string{"limit|`\r\nnext"},
	}
	tests := []struct {
		name   string
		render func(io.Writer, Input) error
	}{{name: "terminal", render: RenderTerminal}, {name: "markdown", render: RenderMarkdown}}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var output bytes.Buffer
			if err := test.render(&output, input); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(output.String(), "\r") {
				t.Fatalf("output contains carriage return: %q", output.String())
			}
			if strings.Contains(output.String(), "\nnext") {
				t.Fatalf("user-controlled newline escaped its context: %q", output.String())
			}
			if test.name == "markdown" && !strings.Contains(output.String(), "| FAIL | `code\\|' next` |") {
				t.Fatalf("markdown did not keep pipe and backtick inside its table context: %q", output.String())
			}
		})
	}
}

func TestRenderMarkdownPlainTextEscapesBackticks(t *testing.T) {
	input := Input{
		Scenario:    "checkout `scenario`",
		Result:      compare.Result{Outcome: model.OutcomePass},
		Limitations: []string{"```"},
	}
	var output bytes.Buffer
	if err := RenderMarkdown(&output, input); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "# TraceBudget: checkout 'scenario' — PASS\n") {
		t.Fatalf("scenario did not remain plain heading text: %q", output.String())
	}
	if !strings.Contains(output.String(), "## Limitations\n\n- '''\n") {
		t.Fatalf("limitation did not remain an ordinary list item: %q", output.String())
	}
	if strings.Contains(output.String(), "```") {
		t.Fatalf("output contains a fenced-code delimiter: %q", output.String())
	}
}

func TestRenderMarkdownSanitizesTypedOutcomeAndSeverity(t *testing.T) {
	input := Input{
		Scenario: "checkout",
		Result: compare.Result{
			Outcome: model.Outcome("out|`\r\ncome"),
			Findings: []model.Finding{{
				Code: "code", Severity: model.Severity("sev|`\r\nity"), Subject: "subject", Baseline: "1", Candidate: "2",
			}},
		},
	}
	var output bytes.Buffer
	if err := RenderMarkdown(&output, input); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "# TraceBudget: checkout — OUT\\|' COME\n") {
		t.Fatalf("outcome escaped its heading context: %q", output.String())
	}
	if !strings.Contains(output.String(), "| SEV\\|' ITY | `code` |") {
		t.Fatalf("severity escaped its table context: %q", output.String())
	}
	if strings.Contains(output.String(), "\r") || strings.Contains(output.String(), "\nCOME") || strings.Contains(output.String(), "\nITY") {
		t.Fatalf("typed value escaped a Markdown line context: %q", output.String())
	}
}

func TestRenderTerminalSanitizesLayoutControlsInAllUserFields(t *testing.T) {
	input := Input{
		Scenario: terminalControlValue("scenario"),
		Result: compare.Result{
			Outcome: model.Outcome(terminalControlValue("outcome")),
			Findings: []model.Finding{{
				Code: terminalControlValue("code"), Severity: model.Severity(terminalControlValue("severity")),
				Subject: terminalControlValue("subject"), Baseline: terminalControlValue("baseline"), Candidate: terminalControlValue("candidate"),
			}},
		},
		Observation: analyze.Observation{Nodes: []analyze.NodeObservation{{
			Key:       model.NodeKey{Service: terminalControlValue("node"), Name: "span", Kind: model.SpanKindClient},
			Durations: analyze.DurationSummary{Samples: 1},
		}}},
		ArtifactPath: terminalControlValue("artifact"), Limitations: []string{terminalControlValue("limitation")},
	}
	var output bytes.Buffer
	if err := RenderTerminal(&output, input); err != nil {
		t.Fatal(err)
	}
	for _, label := range []string{"scenario", "outcome", "severity", "code", "subject", "baseline", "candidate", "node", "artifact", "limitation"} {
		expected := escapedTerminalControlValue(label)
		if label == "outcome" || label == "severity" {
			expected = strings.ToUpper(expected)
		}
		if !strings.Contains(output.String(), expected) {
			t.Fatalf("%s was not visibly escaped: %q", label, output.String())
		}
	}
	for _, control := range []string{"\x1b", "\v", "\f", "\u0085", "\u2028", "\u2029"} {
		if strings.Contains(output.String(), control) {
			t.Fatalf("output contains terminal layout control %q: %q", control, output.String())
		}
	}
}

func TestRenderDoesNotMutateInputAndIsStable(t *testing.T) {
	input := failureInput()
	input.Limitations = []string{"first", "second"}
	before := cloneInput(input)
	tests := []struct {
		name   string
		render func(io.Writer, Input) error
	}{{name: "terminal", render: RenderTerminal}, {name: "markdown", render: RenderMarkdown}}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var first, second bytes.Buffer
			if err := test.render(&first, input); err != nil {
				t.Fatal(err)
			}
			if err := test.render(&second, input); err != nil {
				t.Fatal(err)
			}
			if first.String() != second.String() {
				t.Fatalf("output is unstable:\nfirst:\n%s\nsecond:\n%s", first.String(), second.String())
			}
			if !reflect.DeepEqual(input, before) {
				t.Fatalf("render mutated input:\ngot: %#v\nwant: %#v", input, before)
			}
		})
	}
}

func TestRenderPropagatesWriterErrorsFromMandatoryAndOptionalSections(t *testing.T) {
	input := failureInput()
	input.ArtifactPath = "artifact"
	input.Limitations = []string{"limitation"}
	tests := []struct {
		name    string
		render  func(io.Writer, Input) error
		markers []string
	}{
		{name: "terminal", render: RenderTerminal, markers: []string{"Evidence:", "Timing:", "Samples:", "Artifacts:", "Limitations:"}},
		{name: "markdown", render: RenderMarkdown, markers: []string{"## Evidence", "## Timing", "## Metric samples", "## Retained artifacts", "## Limitations"}},
	}
	for _, test := range tests {
		for _, marker := range test.markers {
			t.Run(test.name+"/"+marker, func(t *testing.T) {
				want := errors.New("writer failed at " + marker)
				writer := markerErrorWriter{marker: marker, err: want}
				if err := test.render(&writer, input); !errors.Is(err, want) {
					t.Fatalf("error = %v, want %v", err, want)
				}
				if !strings.Contains(writer.output.String(), previousSectionMarker(test.markers, marker)) && marker != test.markers[0] {
					t.Fatalf("renderer did not reach the section before %q: %q", marker, writer.output.String())
				}
			})
		}
	}
}

func failureInput() Input {
	return Input{
		Scenario: "checkout",
		Result: compare.Result{Outcome: model.OutcomeFail, Findings: []model.Finding{
			{Code: "count_increase", Severity: model.SeverityFail, Subject: "gateway/db.query/CLIENT", Baseline: "1", Candidate: "2"},
			{Code: "removed_edge", Severity: model.SeverityWarn, Subject: "gateway/checkout/SERVER -> inventory/reserve/SERVER", Baseline: "present", Candidate: "missing"},
		}},
		Observation: analyze.Observation{Nodes: []analyze.NodeObservation{{Key: model.NodeKey{Service: "gateway", Name: "db.query", Kind: model.SpanKindClient}, Durations: analyze.DurationSummary{Samples: 20}}}},
		Evidence:    analyze.Evidence{Complete: 20}, Diagnostics: diagnostics.Snapshot{Received: 80},
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

func cloneInput(input Input) Input {
	copy := input
	copy.Result.Findings = append([]model.Finding(nil), input.Result.Findings...)
	copy.Observation.Nodes = append([]analyze.NodeObservation(nil), input.Observation.Nodes...)
	copy.Observation.Edges = append([]analyze.EdgeObservation(nil), input.Observation.Edges...)
	copy.Limitations = append([]string(nil), input.Limitations...)
	copy.Diagnostics.IntegrityReasons = append([]string(nil), input.Diagnostics.IntegrityReasons...)
	return copy
}

func terminalControlValue(label string) string {
	return label + " café 東京 🐙\x1b[31mred\x1b[0m\v\f\u0085\u2028\u2029"
}

func escapedTerminalControlValue(label string) string {
	return label + " café 東京 🐙\\u001B[31mred\\u001B[0m\\u000B\\u000C\\u0085  "
}

func previousSectionMarker(markers []string, marker string) string {
	for index, value := range markers {
		if value == marker && index > 0 {
			return markers[index-1]
		}
	}
	return ""
}

type markerErrorWriter struct {
	marker string
	err    error
	output bytes.Buffer
}

func (writer *markerErrorWriter) Write(value []byte) (int, error) {
	if strings.Contains(string(value), writer.marker) {
		return 0, writer.err
	}
	return writer.output.Write(value)
}
