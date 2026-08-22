package report

import (
	"fmt"
	"io"
)

// RenderMarkdown writes a stable Markdown report for saved CI artifacts.
func RenderMarkdown(writer io.Writer, input Input) error {
	if _, err := fmt.Fprintf(writer, "# TraceBudget: %s — %s\n\n", markdownText(input.Scenario), markdownText(upper(string(input.Result.Outcome)))); err != nil {
		return err
	}
	if len(input.Result.Findings) == 0 {
		if _, err := fmt.Fprintln(writer, "No regressions."); err != nil {
			return err
		}
	} else {
		if _, err := fmt.Fprintln(writer, "| Severity | Code | Subject | Baseline | Candidate |\n| --- | --- | --- | --- | --- |"); err != nil {
			return err
		}
		for _, finding := range input.Result.Findings {
			if _, err := fmt.Fprintf(writer, "| %s | `%s` | `%s` | `%s` | `%s` |\n",
				markdownText(upper(string(finding.Severity))), markdownCode(finding.Code), markdownCode(finding.Subject),
				markdownCode(finding.Baseline), markdownCode(finding.Candidate)); err != nil {
				return err
			}
		}
	}
	if _, err := fmt.Fprintf(writer,
		"\n## Evidence\n\n- Complete traces: %d\n- Incomplete traces: %d\n- Received spans: %d\n- Exact duplicates: %d\n- Unmatched traces: %d\n- Invalid spans: %d\n- Current buffer depth: %d\n- Peak buffer depth: %d\n",
		input.Evidence.Complete, input.Evidence.Incomplete, input.Diagnostics.Received,
		input.Diagnostics.Duplicates, input.Evidence.Unmatched, input.Diagnostics.Invalid,
		input.Diagnostics.CurrentBuffer, input.Diagnostics.PeakBuffer); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(writer,
		"\n## Timing\n\n- SQLite writes: %s\n- Assembly: %s\n- Normalization: %s\n- Comparison: %s\n",
		input.Diagnostics.SQLiteWriteDuration, input.Diagnostics.AssemblyDuration,
		input.Diagnostics.NormalizationDuration, input.Diagnostics.ComparisonDuration); err != nil {
		return err
	}
	if len(input.Observation.Nodes) > 0 {
		if _, err := io.WriteString(writer, "\n## Metric samples\n\n| Span | Duration samples |\n| --- | ---: |\n"); err != nil {
			return err
		}
		for _, node := range input.Observation.Nodes {
			if _, err := fmt.Fprintf(writer, "| `%s` | %d |\n", markdownCode(node.Key.String()), node.Durations.Samples); err != nil {
				return err
			}
		}
	}
	if input.ArtifactPath != "" {
		if _, err := fmt.Fprintf(writer, "\n## Retained artifacts\n\n`%s`\n", markdownCode(input.ArtifactPath)); err != nil {
			return err
		}
	}
	if len(input.Limitations) > 0 {
		if _, err := fmt.Fprintln(writer, "\n## Limitations"); err != nil {
			return err
		}
		for _, limitation := range input.Limitations {
			if _, err := fmt.Fprintf(writer, "\n- %s", markdownText(limitation)); err != nil {
				return err
			}
		}
		if _, err := fmt.Fprintln(writer); err != nil {
			return err
		}
	}
	return nil
}
