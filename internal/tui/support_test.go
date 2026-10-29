package tui

import (
	"strings"
	"testing"
)

func TestParseSupport(t *testing.T) {
	cases := []struct {
		line     string
		question string
		ok       bool
	}{
		{"$sup what is Golang?", "what is Golang?", true},
		{"  $sup   spaced  ", "spaced", true},
		{"$sup", "", true},
		{"$supfoo", "", false},
		{"/sup foo", "", false},
		{"what is $sup", "", false},
	}
	for _, tc := range cases {
		question, ok := ParseSupport(tc.line)
		if ok != tc.ok || question != tc.question {
			t.Errorf("ParseSupport(%q) = (%q, %v), want (%q, %v)", tc.line, question, ok, tc.question, tc.ok)
		}
	}
}

func TestSupportSurfaceIsEphemeral(t *testing.T) {
	// A support exchange must render but not survive closing, and closing it
	// must not touch the main transcript (§10.2, §34, I17).
	s := New()
	s.Apply(UserSubmitted{Text: "real question"})
	s.Apply(AssistantDone{})
	s.Apply(SupportTurn{Question: "what is Go?", Answer: "a compiled language"})

	if !s.Support {
		t.Fatal("the surface did not open")
	}
	if !strings.Contains(Render(s), "a compiled language") {
		t.Fatal("the support answer was not rendered")
	}

	s.Apply(SupportClosed{})
	if s.Support {
		t.Error("the surface is still open after close")
	}
	out := Render(s)
	if strings.Contains(out, "a compiled language") {
		t.Errorf("the ephemeral answer survived close:\n%s", out)
	}
	if !strings.Contains(out, "real question") {
		t.Errorf("closing support dropped the main transcript:\n%s", out)
	}
}

func TestSupportDoesNotTouchWorkspaceState(t *testing.T) {
	// $sup does not change the workspace the session runs under (§34).
	s := New()
	s.Apply(WorkspaceUpdated{Workspace: "BUILD", WorkspaceAccent: "33", HasWorkspace: true})
	s.Apply(SupportTurn{Question: "q", Answer: "a"})
	if s.Status.Workspace != "BUILD" {
		t.Errorf("workspace = %q, want BUILD while support is open", s.Status.Workspace)
	}
	s.Apply(SupportClosed{})
	if s.Status.Workspace != "BUILD" {
		t.Errorf("workspace = %q, want BUILD after support closed", s.Status.Workspace)
	}
}
