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

func TestRenderPropagatesWriterErrors(t *testing.T) {
	want := errors.New("writer failed")
	for name, render := range map[string]func(io.Writer, Input) error{"terminal": RenderTerminal, "markdown": RenderMarkdown} {
		t.Run(name, func(t *testing.T) {
			if err := render(errorWriter{err: want}, failureInput()); !errors.Is(err, want) {
				t.Fatalf("error = %v, want %v", err, want)
			}
		})
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

type errorWriter struct{ err error }

func (writer errorWriter) Write([]byte) (int, error) { return 0, writer.err }
