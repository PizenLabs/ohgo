package tui

import (
	"strings"
	"testing"
)

func TestStatusShowsTheWorkspaceAuthority(t *testing.T) {
	// The authority is identity, not decoration: the footer must name the
	// boundary the session runs under (§9).
	s := New()
	s.Status.Model = "groq/m"
	s.Apply(TerminalResized{Width: 100, Height: 24})
	s.Apply(WorkspaceUpdated{
		Cwd: "/work/project", HasCwd: true,
		Session: "#4", HasSession: true,
		Workspace: "FUZZ", WorkspaceAccent: "200", HasWorkspace: true,
	})

	out := Render(s)
	if !strings.Contains(out, "FUZZ") {
		t.Fatalf("status does not name the workspace:\n%s", out)
	}
	if strings.Contains(out, "BUILD") {
		t.Errorf("status invented a workspace:\n%s", out)
	}
}

func TestWorkspaceBadgeUsesItsAccent(t *testing.T) {
	// Two authorities with different accents must render differently; a badge
	// that ignored the accent would make the semantic identity meaningless.
	if WorkspaceBadge("33", "BUILD") == WorkspaceBadge("200", "FUZZ") {
		t.Error("badge ignored the workspace accent")
	}
	if !strings.Contains(WorkspaceBadge("37", "ASK"), "ASK") {
		t.Error("badge lost its label")
	}
}

func TestWorkspaceFooterKeepsTheViewportBudget(t *testing.T) {
	// The authority badge is prefixed onto the one-row footer; it must not push
	// a line past the terminal width at any tier.
	for _, width := range []int{120, 90, 70, 50} {
		s := New()
		s.Status.Model = "groq/m"
		s.Apply(TerminalResized{Width: width, Height: 24})
		s.Apply(WorkspaceUpdated{
			Cwd: "/a/very/long/project/path/that/keeps/going", HasCwd: true,
			Session: "#1234", HasSession: true,
			UsageText: "12.4k tok · $0.02", HasUsage: true,
			Workspace: "BUILD", WorkspaceAccent: "33", HasWorkspace: true,
		})
		for _, line := range strings.Split(Render(s), "\n") {
			if got := CellWidth(line); got > width {
				t.Errorf("width %d: line %q is %d cells", width, line, got)
			}
		}
	}
}
