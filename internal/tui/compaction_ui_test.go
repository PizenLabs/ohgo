package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/PizenLabs/ohgo/internal/agent"
)

// TestCompactionActivityOwnsOneSurface proves /compact uses the existing
// activity line while it runs and leaves no second status representation.
func TestCompactionActivityOwnsOneSurface(t *testing.T) {
	s := New()
	started := time.Now()
	s.Apply(CompactionChanged{Phase: CompactionRunning, At: started})

	if !s.CompactionActive() {
		t.Fatal("running compaction was not active")
	}
	if got := DeriveRuntime(s); got != RuntimeCompacting {
		t.Fatalf("runtime = %q, want %q", got, RuntimeCompacting)
	}
	activity := ProjectActivity(s)
	if activity.Surface != ActivityLine {
		t.Fatalf("surface = %s, want the one activity line", activity.Surface)
	}
	if !activity.Thinking.Active || activity.Thinking.Phase != ThinkingCompaction {
		t.Fatalf("thinking = %+v, want the compaction phase", activity.Thinking)
	}
	if got := projectLiveTelemetry(s).Label; got != "compacting context…" {
		t.Fatalf("telemetry = %q", got)
	}
	s.Apply(TerminalResized{Width: 96, Height: 30})
	out := stripANSI(Render(s))
	if got := strings.Count(out, "compacting context…"); got != 1 {
		t.Fatalf("activity appeared %d times, want once:\n%s", got, out)
	}

	s.Apply(CompactionChanged{Phase: CompactionDone})
	if s.CompactionActive() {
		t.Fatal("completed compaction still owns the activity")
	}
	if activity := ProjectActivity(s); activity.Surface != ActivityNone {
		t.Fatalf("surface = %s after completion, want none", activity.Surface)
	}
	if out := stripANSI(Render(s)); strings.Contains(out, "compacting context…") {
		t.Fatalf("completed compaction left a stale activity:\n%s", out)
	}
}

// TestCompactionOutcomeIsDerivedFromStructuredResult defends the claims the
// renderer may make. A degraded summary must not claim durable state, and an
// unchanged result must not claim a rebuild.
func TestCompactionOutcomeIsDerivedFromStructuredResult(t *testing.T) {
	stateFirst := CompactionOutcomeFrom(agent.CompactResult{
		Mode:         agent.ModeReconstructed,
		BeforeTokens: 1000,
		AfterTokens:  200,
		Compacted:    4,
		Metrics: agent.CompactionMetrics{
			EvidencePreserved:   2,
			StateItemsPreserved: 5,
		},
	})
	if stateFirst.Kind != CompactionComplete || !stateFirst.StatePreserved || !stateFirst.ContextRebuilt {
		t.Fatalf("state-first outcome = %+v", stateFirst)
	}
	if stateFirst.EvidencePreserved != 2 || stateFirst.RedundancyRemoved != 4 {
		t.Fatalf("state-first preserved counts = %+v", stateFirst)
	}

	degraded := CompactionOutcomeFrom(agent.CompactResult{
		Mode:         agent.ModeSummarized,
		BeforeTokens: 1000,
		AfterTokens:  100,
		Compacted:    6,
	})
	if degraded.Kind != CompactionDegraded || degraded.StatePreserved || !degraded.ContextRebuilt {
		t.Fatalf("degraded outcome = %+v", degraded)
	}

	unchanged := CompactionOutcomeFrom(agent.CompactResult{
		Mode:         agent.ModeUnchanged,
		BeforeTokens: 365,
		AfterTokens:  365,
	})
	if unchanged.Kind != CompactionUnchanged || unchanged.ContextRebuilt || !unchanged.StatePreserved {
		t.Fatalf("unchanged outcome = %+v", unchanged)
	}
}

// TestCompactionResultRendersTruthfulOutcome checks the terminal block a user
// actually reads: preserved categories are named, the pressure band is not
// leaked, and the quantitative result comes from the result itself.
func TestCompactionResultRendersTruthfulOutcome(t *testing.T) {
	text := CompactionResultText(agent.CompactResult{
		Mode:         agent.ModeReconstructed,
		BeforeTokens: 27928,
		AfterTokens:  365,
		Compacted:    3,
		Metrics: agent.CompactionMetrics{
			EvidencePreserved: 2,
			Trigger:           agent.PressureCritical,
		},
	})
	for _, want := range []string{
		"Context compacted",
		"✓ Task state preserved",
		"✓ Evidence preserved (2 items)",
		"✓ Conversation context rebuilt from task state",
		"✓ Redundant history removed",
		"27,928 → 365 est. tokens · 98.69% reduced",
		"Ready to continue.",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("result = %q, want %q", text, want)
		}
	}
	if strings.Contains(text, "CRITICAL") || strings.Contains(text, "NORMAL") {
		t.Fatalf("result leaked the internal pressure band: %q", text)
	}
	if strings.Contains(text, "Compacted ·") {
		t.Fatalf("result is still the legacy telemetry line: %q", text)
	}

	s := New()
	s.Apply(TerminalResized{Width: 100, Height: 40})
	s.Apply(Notice{Text: text})
	rendered := stripANSI(Render(s))
	for _, want := range []string{
		"Context compacted",
		"Task state preserved",
		"Conversation context rebuilt from task state",
		"Ready to continue.",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("rendered transcript = %q, want %q", rendered, want)
		}
	}
}

// TestCompactionDegradedResultDoesNotClaimDurableState checks the fallback
// outcome is honest about what it could not preserve.
func TestCompactionDegradedResultDoesNotClaimDurableState(t *testing.T) {
	text := CompactionResultText(agent.CompactResult{
		Mode:         agent.ModeSummarized,
		BeforeTokens: 1000,
		AfterTokens:  100,
		Compacted:    8,
	})
	for _, want := range []string{
		"Context compacted with reduced continuity",
		"Durable task state was unavailable",
		"Conversation context rebuilt from the summary",
		"Ready to continue.",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("result = %q, want %q", text, want)
		}
	}
	if strings.Contains(text, "Task state preserved") || strings.Contains(text, "Evidence preserved") {
		t.Fatalf("degraded result claimed preservation it did not have: %q", text)
	}
}

// TestCompactionFailureRendersNoSuccess checks the failure presentation never
// claims a compacted context and names the untouched boundary.
func TestCompactionFailureRendersNoSuccess(t *testing.T) {
	text := CompactionErrorText(agent.ErrInvalidProjection)
	for _, want := range []string{
		"Context compaction incomplete",
		"Task state was not modified",
		"conversation left unchanged",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("failure = %q, want %q", text, want)
		}
	}
	if strings.Contains(text, "Context compacted") || strings.Contains(text, "✓") {
		t.Fatalf("failure claimed success: %q", text)
	}
}
