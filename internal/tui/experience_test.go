package tui

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/PizenLabs/ohgo/internal/agent"
	"github.com/PizenLabs/ohgo/internal/control/cognition"
	"github.com/PizenLabs/ohgo/internal/control/contract"
	"github.com/PizenLabs/ohgo/internal/provider/ai"
)

type experienceAdvisor struct{}

func (experienceAdvisor) Advise(cognition.AdviceRequest) cognition.Advice {
	return cognition.Advice{
		PreferredTools: []string{"grep"},
		Hints:          []string{"prefer the bounded probe"},
		Matched:        []string{"experience:test"},
	}
}

func TestExperienceCommandListsAndExplains(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	retired := ""
	p.SetExperienceHooks(ExperienceHooks{
		List: func() []ExperienceView {
			return []ExperienceView{{
				ID:           "experience:abc123",
				Status:       "promoted",
				Scope:        "project",
				Kind:         "preferred_tool_order",
				LessonKind:   "successful_shortcut",
				Statement:    "a low-cost path completed the objective",
				Context:      "ASK/informational",
				Observations: 2,
				Active:       true,
			}}
		},
		Explain: func(id string) (ExperienceTrace, bool) {
			if id != "experience:abc123" {
				return ExperienceTrace{}, false
			}
			return ExperienceTrace{
				View: ExperienceView{
					ID:         id,
					Status:     "promoted",
					Scope:      "project",
					Kind:       "preferred_tool_order",
					Statement:  "a low-cost path completed the objective",
					Context:    "ASK/informational",
					Active:     true,
					Regression: "TestSuccessfulShortcutCandidateIsPromotedAfterValidation",
				},
				Applied:       true,
				Regression:    "TestSuccessfulShortcutCandidateIsPromotedAfterValidation",
				LastValidated: "2026-11-09T12:00:00Z",
				Transitions:   []string{"candidate -> validated: repeated matching observation"},
			}, true
		},
		Retire: func(id, reason string) error {
			retired = id
			return nil
		},
	})
	p.experienceCommand([]string{"list"})
	list := lastExperienceNotice(p)
	if !strings.Contains(list, "execution experience") || !strings.Contains(list, "experience:abc123") {
		t.Fatalf("list notice = %q", list)
	}
	p.experienceCommand([]string{"show", "experience:abc"})
	trace := lastExperienceNotice(p)
	if !strings.Contains(trace, "regression:") || !strings.Contains(trace, "repeated matching observation") {
		t.Fatalf("trace notice = %q", trace)
	}
	p.experienceCommand([]string{"retire", "experience:abc123"})
	if retired != "experience:abc123" {
		t.Fatalf("retired id = %q, want the resolved record", retired)
	}
	if !strings.Contains(lastExperienceNotice(p), "retired") {
		t.Fatalf("retire notice = %q", lastExperienceNotice(p))
	}
}

func TestExperienceCommandRefusesWithoutLayer(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	p.experienceCommand([]string{"list"})
	if notice := lastExperienceNotice(p); !strings.Contains(notice, "unavailable") {
		t.Fatalf("notice = %q, want unavailable", notice)
	}
}

func TestRunTurnUsesAdvisorAndRecordsObservation(t *testing.T) {
	var (
		mu       sync.Mutex
		captured ai.Request
	)
	loop, err := agent.New(agent.Config{
		Model: ai.Model{ID: "m", Provider: "p"},
		Stream: func(_ context.Context, req ai.Request) (*ai.Stream, error) {
			mu.Lock()
			captured = req
			mu.Unlock()
			return ai.NewCompletedStream(ai.AssistantMessage{
				Content:    []ai.ContentBlock{ai.TextContent{Text: "It is a test project."}},
				StopReason: ai.StopEndTurn,
			}), nil
		},
		Tools: []agent.Tool{
			projectionTool{name: "read", tier: agent.TierRead},
			projectionTool{name: "grep", tier: agent.TierRead},
		},
	})
	if err != nil {
		t.Fatalf("agent.New: %v", err)
	}
	p := NewProgram(loop, "test", 0)
	evidence := cognition.NewEvidenceState()
	var (
		lastReport agent.ProjectionReport
		observed   ExperienceObservation
	)
	p.SetTaskHooks(TaskHooks{
		Begin: func(contract.IntentContract, *agent.TargetSnapshot) TaskProjection {
			return TaskProjection{ID: "t1", Status: "RUNNING"}
		},
		CompleteWithEvidence: func(CompletionEvidence) TaskProjection {
			return TaskProjection{ID: "t1", Status: "COMPLETED"}
		},
		Evidence: func() *cognition.EvidenceState { return evidence },
		RecordProjection: func(report agent.ProjectionReport) {
			lastReport = report
		},
		LastProjection: func() (agent.ProjectionReport, bool) { return lastReport, true },
	})
	p.SetExperienceHooks(ExperienceHooks{
		Advisor: experienceAdvisor{},
		Observe: func(obs ExperienceObservation) { observed = obs },
	})
	p.runTurn("Explain this project.")
	mu.Lock()
	names := make([]string, 0, len(captured.Tools))
	for _, tool := range captured.Tools {
		names = append(names, tool.Name)
	}
	mu.Unlock()
	if strings.Join(names, ",") != "grep,read" {
		t.Fatalf("offered tools = %v, want the advisor's preferred order", names)
	}
	if observed.Outcome != "completed" {
		t.Fatalf("observed outcome = %q, want completed", observed.Outcome)
	}
	if observed.TaskID != "t1" || observed.Shape.Kind != contract.ObjectiveInformational {
		t.Fatalf("observation = %+v, want the turn's structured state", observed)
	}
	if observed.Projection.ExperienceApplied == nil || observed.Projection.ExperienceApplied[0] != "experience:test" {
		t.Fatalf("projection experience = %+v, want the matched record", observed.Projection.ExperienceApplied)
	}
}

func TestExperienceOutcomeMappingDoesNotInventCompletion(t *testing.T) {
	if got := experienceOutcome(taskActionPhaseComplete); got != "completed" {
		t.Fatalf("phase outcome = %q, want completed", got)
	}
	if got := experienceOutcome(taskActionReview); got != "needs_review" {
		t.Fatalf("review outcome = %q, want needs_review", got)
	}
	if got := experienceOutcome(taskActionFail); got != "failed" {
		t.Fatalf("fail outcome = %q, want failed", got)
	}
}

func lastExperienceNotice(p *Program) string {
	for i := len(p.state.Entries) - 1; i >= 0; i-- {
		if p.state.Entries[i].Kind == EntryNotice {
			return p.state.Entries[i].Notice
		}
	}
	return ""
}
