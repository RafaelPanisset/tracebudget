// Package report renders comparison results for people and automation.
package report

import (
	"strings"

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
	return strings.NewReplacer("\r\n", " ", "\r", " ", "\n", " ").Replace(value)
}

func markdownText(value string) string {
	return strings.ReplaceAll(oneLine(value), "|", "\\|")
}

func markdownCode(value string) string {
	return strings.ReplaceAll(markdownText(value), "`", "'")
}

func upper(value string) string {
	return strings.ToUpper(oneLine(value))
}
