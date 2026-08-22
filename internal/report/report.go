// Package report renders comparison results for people and automation.
package report

import (
	"fmt"
	"strings"
	"unicode"

	"github.com/RafaelPanisset/tracebudget/internal/analyze"
	"github.com/RafaelPanisset/tracebudget/internal/compare"
	"github.com/RafaelPanisset/tracebudget/internal/diagnostics"
	"github.com/RafaelPanisset/tracebudget/internal/model"
)

// Input contains the immutable inputs needed to render a comparison report.
type Input struct {
	Scenario     string
	Result       compare.Result
	Observation  analyze.Observation
	Evidence     analyze.Evidence
	Diagnostics  diagnostics.Snapshot
	Limitations  []string
	ArtifactPath string
}

// ExitCode returns the CLI exit code for an outcome.
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

func oneLine(value string) string {
	var output strings.Builder
	output.Grow(len(value))
	previousCarriageReturn := false
	for _, character := range value {
		switch {
		case character == '\r':
			output.WriteByte(' ')
			previousCarriageReturn = true
		case character == '\n':
			if !previousCarriageReturn {
				output.WriteByte(' ')
			}
			previousCarriageReturn = false
		case character == '\u2028' || character == '\u2029':
			output.WriteByte(' ')
			previousCarriageReturn = false
		case unicode.IsControl(character):
			fmt.Fprintf(&output, "\\u%04X", character)
			previousCarriageReturn = false
		default:
			output.WriteRune(character)
			previousCarriageReturn = false
		}
	}
	return output.String()
}

func markdownText(value string) string {
	value = strings.ReplaceAll(oneLine(value), "|", "\\|")
	return strings.ReplaceAll(value, "`", "'")
}

func markdownCode(value string) string {
	return markdownText(value)
}

func upper(value string) string {
	return strings.ToUpper(oneLine(value))
}

func terminalSeverity(value model.Severity) string {
	severity := upper(string(value))
	if severity == "" {
		return "UNKNOWN"
	}
	return severity
}
