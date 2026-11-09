package main

import (
	"path/filepath"
	"strings"
	"time"

	"github.com/PizenLabs/ohgo/internal/experience"
	"github.com/PizenLabs/ohgo/internal/state"
	"github.com/PizenLabs/ohgo/internal/tui"
)

// installExperience constructs the project-scoped execution-experience engine
// and installs its advisory seam behind the TUI.
//
// The engine owns no authority, evidence, completion, or task state. Observe is
// called after a turn's terminal transition and only records candidates;
// Advise may reorder already-offered tools and append bounded hints. A learning
// or persistence failure is reported as a notice and never rewrites the turn.
func installExperience(program *tui.Program, root, projectID string) *experience.Engine {
	cfg := experience.Config{
		ProjectRoot: root,
		ProjectID:   projectID,
		AutoPromote: true,
	}
	if dir, err := state.Dir(); err == nil {
		// Global experience is never written automatically. The path is
		// configured so an explicit, validated promotion can be read by a
		// later project; no normal turn writes it.
		cfg.GlobalPath = filepath.Join(dir, "experience", "global.json")
	}
	engine := experience.New(cfg)
	program.SetExperienceHooks(tui.ExperienceHooks{
		Advisor: engine,
		Observe: func(obs tui.ExperienceObservation) {
			// Learning is an optimization, never a prerequisite for a turn.
			// A defect in the observer is reported and dropped rather than
			// crashing the turn after its terminal transition.
			defer func() {
				if recover() != nil {
					program.PublishNotice("experience: observation failed")
				}
			}()
			if _, err := engine.Observe(toExperienceObservation(obs)); err != nil {
				program.PublishNotice("experience: " + err.Error())
			}
		},
		List: func() []tui.ExperienceView {
			summaries := engine.List("")
			out := make([]tui.ExperienceView, 0, len(summaries))
			for _, summary := range summaries {
				out = append(out, tui.ExperienceView{
					ID:             summary.ID,
					Status:         string(summary.Status),
					Scope:          string(summary.Scope),
					Kind:           string(summary.Kind),
					LessonKind:     summary.LessonKind,
					Statement:      summary.Statement,
					Hint:           summary.Hint,
					Context:        summary.Context,
					Observations:   summary.Observations,
					Contradictions: summary.Contradictions,
					Regression:     summary.Regression,
					Active:         summary.Status.Active(),
					LastValidated:  formatExperienceTime(summary.LastValidated),
					RetiredAt:      formatExperienceTimePtr(summary.RetiredAt),
				})
			}
			return out
		},
		Explain: func(id string) (tui.ExperienceTrace, bool) {
			explanation, ok := engine.Explain(id)
			if !ok {
				return tui.ExperienceTrace{}, false
			}
			record := explanation.Record
			trace := tui.ExperienceTrace{
				View: tui.ExperienceView{
					ID:             record.ID,
					Status:         string(record.Candidate.Status),
					Scope:          string(record.Scope),
					Kind:           string(record.Candidate.Kind),
					LessonKind:     record.Lesson.Kind,
					Statement:      record.Lesson.Statement,
					Hint:           record.Candidate.Hint,
					Context:        record.Candidate.Context.Summary(),
					Observations:   record.Candidate.Observations,
					Contradictions: record.Candidate.Contradictions,
					Regression:     record.Candidate.Regression.Name,
					Active:         record.Candidate.Status.Active(),
					LastValidated:  formatExperienceTime(record.Candidate.LastValidatedAt),
					RetiredAt:      formatExperienceTimePtr(record.Candidate.RetiredAt),
				},
				Applied:        explanation.Applied,
				Regression:     explanation.Regression.Name,
				LastValidated:  formatExperienceTime(explanation.LastValidatedAt),
				Contradictions: append([]string(nil), explanation.Contradictions...),
			}
			for _, transition := range record.Candidate.Transitions {
				line := strings.TrimSpace(string(transition.From) + " -> " + string(transition.To))
				if transition.Reason != "" {
					line += ": " + transition.Reason
				}
				if !transition.At.IsZero() {
					line += " (" + transition.At.UTC().Format(time.RFC3339) + ")"
				}
				trace.Transitions = append(trace.Transitions, line)
			}
			return trace, true
		},
		Retire: func(id, reason string) error {
			_, err := engine.Retire(id, reason)
			return err
		},
		Reject: func(id, reason string) error {
			_, err := engine.Reject(id, reason)
			return err
		},
	})
	return engine
}

// toExperienceObservation maps the presentation-neutral host observation onto
// the engine's domain observation. The mapping adds no policy.
func toExperienceObservation(obs tui.ExperienceObservation) experience.Observation {
	scope := experience.Scope(obs.Scope)
	if !scope.Valid() {
		scope = experience.ScopeProject
	}
	outcome := experience.Outcome(obs.Outcome)
	if !outcome.Valid() {
		outcome = experience.OutcomeNeedsReview
	}
	return experience.Observation{
		TaskID:            obs.TaskID,
		ProjectID:         obs.ProjectID,
		SessionID:         obs.SessionID,
		Workspace:         obs.Workspace,
		PriorWorkspace:    obs.PriorWorkspace,
		Intent:            obs.Intent,
		Shape:             obs.Shape,
		Turns:             obs.Turns,
		Projection:        obs.Projection,
		Evidence:          obs.Evidence,
		PriorEvidence:     obs.PriorEvidence,
		Outcome:           outcome,
		Reason:            obs.Reason,
		PriorStatus:       obs.PriorStatus,
		PriorReviewReason: obs.PriorReviewReason,
		StartedAt:         obs.StartedAt,
		FinishedAt:        obs.FinishedAt,
		Scope:             scope,
	}
}

// formatExperienceTime renders a timestamp deterministically, or "".
func formatExperienceTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339)
}

// formatExperienceTimePtr renders an optional timestamp.
func formatExperienceTimePtr(value *time.Time) string {
	if value == nil {
		return ""
	}
	return formatExperienceTime(*value)
}
