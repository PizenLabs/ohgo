package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/PizenLabs/ohgo/internal/agent"
	"github.com/PizenLabs/ohgo/internal/control/cognition"
	"github.com/PizenLabs/ohgo/internal/control/contract"
)

// ExperienceObservation is the structured runtime state the host may hand to
// the execution-experience layer at a turn boundary.
//
// The TUI does not own the experience domain. This is a projection input: the
// host maps it onto the engine's observation type, and the engine returns only
// advisory guidance. It contains completed turns and bounded evidence, never a
// raw transcript replay.
type ExperienceObservation struct {
	TaskID            string
	ProjectID         string
	SessionID         string
	Workspace         string
	PriorWorkspace    string
	Intent            contract.IntentContract
	Shape             cognition.TaskShape
	Turns             []agent.Turn
	Projection        agent.ProjectionReport
	Evidence          *cognition.EvidenceState
	PriorEvidence     *cognition.EvidenceState
	Outcome           string
	Reason            string
	PriorStatus       string
	PriorReviewReason string
	StartedAt         time.Time
	FinishedAt        time.Time
	Scope             string
}

// ExperienceView is the read-only projection of one experience record.
type ExperienceView struct {
	ID             string
	Status         string
	Scope          string
	Kind           string
	LessonKind     string
	Statement      string
	Hint           string
	Context        string
	Observations   int
	Contradictions int
	Regression     string
	Active         bool
	LastValidated  string
	RetiredAt      string
}

// ExperienceTrace is the complete explanation of one promoted or rejected
// record. It is read-only and never reaches the model as instruction.
type ExperienceTrace struct {
	View           ExperienceView
	Applied        bool
	Regression     string
	LastValidated  string
	Contradictions []string
	Transitions    []string
}

// ExperienceHooks wire the optional experience layer into the program.
//
// Every field is nil-safe: a program built without them behaves exactly as a
// cold-start runtime with no learned experience. Retire and Reject are human
// commands; no model-facing tool can call them.
type ExperienceHooks struct {
	// Advisor is consulted by the task projector for promoted path guidance.
	Advisor cognition.ExperienceAdvisor
	// Observe records one completed turn's structured outcome.
	Observe func(ExperienceObservation)
	// List returns every record for inspection, active and historical.
	List func() []ExperienceView
	// Explain returns one record and its validation trail.
	Explain func(id string) (ExperienceTrace, bool)
	// Retire disables one record.
	Retire func(id, reason string) error
	// Reject marks one record as rejected.
	Reject func(id, reason string) error
}

// SetExperienceHooks installs the optional execution-experience layer.
func (p *Program) SetExperienceHooks(hooks ExperienceHooks) { p.experience = hooks }

// experienceOutcome maps the TUI's terminal action to the experience
// vocabulary. A phase completion is a completed informational step; the task
// itself remains active and the mapped outcome makes no completion claim.
func experienceOutcome(action taskAction) string {
	switch action {
	case taskActionComplete, taskActionPhaseComplete:
		return "completed"
	case taskActionReview:
		return "needs_review"
	case taskActionPause:
		return "paused"
	case taskActionFail:
		return "failed"
	default:
		return "needs_review"
	}
}

// experienceCommand handles /experience [list|show|retire|reject].
//
// It is read-only unless the human explicitly retires or rejects a record. The
// handler is not model-reachable and cannot change authority, evidence, or a
// task's completion.
func (p *Program) experienceCommand(args []string) {
	action := "list"
	rest := []string{}
	if len(args) > 0 {
		action = strings.ToLower(args[0])
		rest = args[1:]
	}
	if p.experience.List == nil {
		p.state.Apply(Notice{Text: "execution experience is unavailable"})
		return
	}
	switch action {
	case "list":
		views := p.experience.List()
		if len(views) == 0 {
			p.state.Apply(Notice{Text: "no execution experience recorded"})
			return
		}
		var sb strings.Builder
		sb.WriteString("execution experience (active first):")
		for _, view := range views {
			sb.WriteString("\n  ")
			sb.WriteString(formatExperienceRow(view))
		}
		p.state.Apply(Notice{Text: sb.String()})
		return
	case "show":
		if len(rest) == 0 {
			p.state.Apply(Notice{Text: "usage: /experience show <id>"})
			return
		}
		target := strings.Join(rest, " ")
		if p.experience.Explain == nil {
			p.state.Apply(Notice{Text: "experience explanation is unavailable"})
			return
		}
		trace, ok := p.experience.Explain(p.resolveExperienceID(target))
		if !ok {
			p.state.Apply(Notice{Text: "no experience record matches " + target})
			return
		}
		p.state.Apply(Notice{Text: formatExperienceTrace(trace)})
		return
	case "retire", "reject":
		if len(rest) == 0 {
			p.state.Apply(Notice{Text: "usage: /experience " + action + " <id>"})
			return
		}
		target := p.resolveExperienceID(strings.Join(rest, " "))
		if action == "retire" {
			if p.experience.Retire == nil {
				p.state.Apply(Notice{Text: "experience retirement is unavailable"})
				return
			}
			if err := p.experience.Retire(target, "retired by user"); err != nil {
				p.state.Apply(Notice{Text: "experience retire: " + err.Error()})
				return
			}
			p.state.Apply(Notice{Text: "experience retired; it will not guide future turns"})
			return
		}
		if p.experience.Reject == nil {
			p.state.Apply(Notice{Text: "experience rejection is unavailable"})
			return
		}
		if err := p.experience.Reject(target, "rejected by user"); err != nil {
			p.state.Apply(Notice{Text: "experience reject: " + err.Error()})
			return
		}
		p.state.Apply(Notice{Text: "experience rejected; it will not be promoted"})
		return
	default:
		p.state.Apply(Notice{Text: "usage: /experience [list|show <id>|retire <id>|reject <id>]"})
	}
}

// resolveExperienceID turns an exact ID or a unique prefix into a full ID.
func (p *Program) resolveExperienceID(query string) string {
	query = strings.TrimSpace(query)
	if query == "" || p.experience.List == nil {
		return query
	}
	views := p.experience.List()
	for _, view := range views {
		if strings.EqualFold(view.ID, query) {
			return view.ID
		}
	}
	var match string
	for _, view := range views {
		if strings.HasPrefix(strings.ToLower(view.ID), strings.ToLower(query)) {
			if match != "" {
				return query
			}
			match = view.ID
		}
	}
	if match != "" {
		return match
	}
	return query
}

// formatExperienceRow renders one bounded list row.
func formatExperienceRow(view ExperienceView) string {
	status := string(view.Status)
	active := " "
	if view.Active {
		active = "*"
	}
	return fmt.Sprintf("%s %s %-10s %-20s obs=%d contras=%d %s",
		active, shortExperienceID(view.ID), status, view.Kind, view.Observations,
		view.Contradictions, oneLineText(view.Statement))
}

// formatExperienceTrace renders the complete explanation without raw payloads.
func formatExperienceTrace(trace ExperienceTrace) string {
	view := trace.View
	var sb strings.Builder
	sb.WriteString("experience ")
	sb.WriteString(shortExperienceID(view.ID))
	sb.WriteString("\nstatus: ")
	sb.WriteString(view.Status)
	if trace.Applied {
		sb.WriteString(" (applied)")
	}
	sb.WriteString("\nscope: ")
	sb.WriteString(view.Scope)
	sb.WriteString("\nkind: ")
	sb.WriteString(view.Kind)
	sb.WriteString("\ncontext: ")
	sb.WriteString(view.Context)
	sb.WriteString("\nlesson: ")
	sb.WriteString(view.Statement)
	if view.Hint != "" {
		sb.WriteString("\nhint: ")
		sb.WriteString(view.Hint)
	}
	if trace.Regression != "" {
		sb.WriteString("\nregression: ")
		sb.WriteString(trace.Regression)
	}
	if trace.LastValidated != "" {
		sb.WriteString("\nlast validated: ")
		sb.WriteString(trace.LastValidated)
	}
	if len(trace.Contradictions) > 0 {
		sb.WriteString("\ncontradictions: ")
		sb.WriteString(strings.Join(trace.Contradictions, ", "))
	}
	if len(trace.Transitions) > 0 {
		sb.WriteString("\ntransitions:")
		for _, transition := range trace.Transitions {
			sb.WriteString("\n  ")
			sb.WriteString(transition)
		}
	}
	return sb.String()
}

// shortExperienceID renders the stable prefix used by inspection commands.
func shortExperienceID(id string) string {
	if len(id) > 24 {
		return id[:24]
	}
	return id
}

// oneLineText collapses whitespace and bounds a display line.
func oneLineText(text string) string {
	return TruncateToWidth(strings.Join(strings.Fields(text), " "), 120)
}
