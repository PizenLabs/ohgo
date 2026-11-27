package tui

import (
	"fmt"
	"strings"
)

// RenderDoctorLines formats a diagnostic report for either interface.
//
// The presentation is shared so /doctor cannot say one thing in the full-screen
// UI and another in the plain REPL. Sections are printed once, rows stay
// aligned, and recommendations are excluded from the failure tally.
func RenderDoctorLines(lines []DoctorLine) string {
	var sb strings.Builder
	failed, total := 0, 0
	section := ""
	for _, line := range lines {
		if !line.Recommendation {
			total++
			if line.Status == "ERROR" {
				failed++
			}
		}
		if line.Section != "" && line.Section != section {
			if section != "" {
				sb.WriteString("\n")
			}
			fmt.Fprintf(&sb, "%s\n", line.Section)
			section = line.Section
		}
		status := line.Status
		if status == "" {
			status = "OK"
		}
		fmt.Fprintf(&sb, "%-8s %-14s %s\n", status, line.Name, line.Detail)
	}
	if failed > 0 {
		fmt.Fprintf(&sb, "%d of %d checks failed", failed, total)
	}
	return strings.TrimRight(sb.String(), "\n")
}
