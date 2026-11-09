package main

import (
	"testing"
	"time"

	"github.com/PizenLabs/ohgo/internal/control/cognition"
	"github.com/PizenLabs/ohgo/internal/control/contract"
	"github.com/PizenLabs/ohgo/internal/experience"
	"github.com/PizenLabs/ohgo/internal/tui"
)

func TestToExperienceObservationMapsScopeAndOutcome(t *testing.T) {
	now := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	obs := toExperienceObservation(tui.ExperienceObservation{
		TaskID:            "task-a",
		Workspace:         "build",
		PriorWorkspace:    "fuzz",
		Intent:            contract.IntentContract{Objective: "fix it", Kind: contract.ObjectiveMutation},
		Shape:             cognition.TaskShape{Kind: contract.ObjectiveMutation},
		Outcome:           "completed",
		PriorStatus:       "NEEDS_REVIEW",
		PriorReviewReason: "verification missing",
		StartedAt:         now.Add(-time.Minute),
		FinishedAt:        now,
		Scope:             "project",
	})
	if obs.Scope != experience.ScopeProject {
		t.Fatalf("scope = %q, want project", obs.Scope)
	}
	if obs.Outcome != experience.OutcomeCompleted {
		t.Fatalf("outcome = %q, want completed", obs.Outcome)
	}
	if obs.TaskID != "task-a" || obs.PriorWorkspace != "fuzz" {
		t.Fatalf("observation = %+v, want the structured identity preserved", obs)
	}
}

func TestToExperienceObservationDefaultsWithoutInventingSuccess(t *testing.T) {
	obs := toExperienceObservation(tui.ExperienceObservation{})
	if obs.Scope != experience.ScopeProject {
		t.Fatalf("scope = %q, want project default", obs.Scope)
	}
	if obs.Outcome != experience.OutcomeNeedsReview {
		t.Fatalf("outcome = %q, want a non-success default", obs.Outcome)
	}
}

func TestInstallExperienceIsColdStartSafe(t *testing.T) {
	program := tui.NewProgram(nil, "test", 0)
	engine := installExperience(program, t.TempDir(), "project-a")
	if engine == nil {
		t.Fatal("installExperience returned nil")
	}
	if records := engine.List(""); len(records) != 0 {
		t.Fatalf("records = %d, want an empty cold start", len(records))
	}
}
