package report

import (
	"fmt"
	"io"
)

// RenderTerminal writes a stable, line-oriented report suitable for terminals.
func RenderTerminal(writer io.Writer, input Input) error {
	if _, err := fmt.Fprintf(writer, "%s %s\n\n", upper(string(input.Result.Outcome)), oneLine(input.Scenario)); err != nil {
		return err
	}
	if len(input.Result.Findings) == 0 {
		if _, err := fmt.Fprintln(writer, "No regressions."); err != nil {
			return err
		}
	} else {
		var previousSeverity string
		hasPreviousSeverity := false
		for _, finding := range input.Result.Findings {
			severity := terminalSeverity(finding.Severity)
			currentSeverity := string(finding.Severity)
			if !hasPreviousSeverity || currentSeverity != previousSeverity {
				if _, err := fmt.Fprintf(writer, "%s:\n", severity); err != nil {
					return err
				}
				previousSeverity = currentSeverity
				hasPreviousSeverity = true
			}
			if _, err := fmt.Fprintf(writer, "[%s] %s %s baseline=%s candidate=%s\n",
				severity, oneLine(finding.Code), oneLine(finding.Subject),
				oneLine(finding.Baseline), oneLine(finding.Candidate)); err != nil {
				return err
			}
		}
	}
	if _, err := fmt.Fprintf(writer,
		"\nEvidence: complete=%d incomplete=%d received=%d duplicate=%d unmatched=%d invalid=%d buffer_current=%d buffer_peak=%d\n",
		input.Evidence.Complete, input.Evidence.Incomplete, input.Diagnostics.Received,
		input.Diagnostics.Duplicates, input.Evidence.Unmatched, input.Diagnostics.Invalid,
		input.Diagnostics.CurrentBuffer, input.Diagnostics.PeakBuffer); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(writer, "Timing: sqlite=%s assembly=%s normalization=%s comparison=%s\n",
		input.Diagnostics.SQLiteWriteDuration, input.Diagnostics.AssemblyDuration,
		input.Diagnostics.NormalizationDuration, input.Diagnostics.ComparisonDuration); err != nil {
		return err
	}
	if len(input.Observation.Nodes) > 0 {
		if _, err := fmt.Fprintln(writer, "Samples:"); err != nil {
			return err
		}
		for _, node := range input.Observation.Nodes {
			if _, err := fmt.Fprintf(writer, "- %s=%d\n", oneLine(node.Key.String()), node.Durations.Samples); err != nil {
				return err
			}
		}
	}
	if input.ArtifactPath != "" {
		if _, err := fmt.Fprintf(writer, "Artifacts: %s\n", oneLine(input.ArtifactPath)); err != nil {
			return err
		}
	}
	if len(input.Limitations) > 0 {
		if _, err := fmt.Fprintln(writer, "Limitations:"); err != nil {
			return err
		}
		for _, limitation := range input.Limitations {
			if _, err := fmt.Fprintf(writer, "- %s\n", oneLine(limitation)); err != nil {
				return err
			}
		}
	}
	return nil
}
