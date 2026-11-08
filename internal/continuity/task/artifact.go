package task

import (
	"errors"
	"strings"
	"time"

	"github.com/PizenLabs/ohgo/internal/control/workspace"
)

// RecordArtifact projects a structured handoff artifact into the durable task.
//
// A finding is still a model proposal, not a runtime observation: it becomes an
// active hypothesis, never a fact. BUILD can consume the hypothesis and its
// reproduction, but confirming it still requires current supporting evidence.
// Other artifact kinds are recorded as role progress and references only.
//
// The call is a turn-boundary projection. A terminal or absent task is a
// no-op, and the artifact remains authoritative in the session store.
func (c *Controller) RecordArtifact(artifact workspace.Artifact) error {
	if err := c.ensureLoaded(); err != nil {
		return err
	}
	if c.task.ID == "" || c.task.Status.IsTerminal() {
		return nil
	}
	if err := artifact.Validate(); err != nil {
		return err
	}
	before := c.snapshotState()
	now := c.now().UTC()
	if artifact.Kind == workspace.KindFinding {
		text := findingHypothesisText(artifact)
		hypothesisID := c.hypothesisIDByText(text)
		if hypothesisID == "" {
			hypothesis, err := c.task.RecordHypothesis(HypothesisInput{
				Text: text,
				Role: artifact.Origin,
				At:   now,
			})
			if err != nil {
				if !errors.Is(err, ErrContextLimit) {
					return err
				}
				c.noteProjectionLimit("hypothesis")
			} else {
				hypothesisID = hypothesis.ID
			}
		}
		if err := c.appendRoleArtifact(artifact, hypothesisID, now); err != nil {
			return err
		}
	} else if err := c.appendRoleArtifact(artifact, "", now); err != nil {
		return err
	}
	c.task.UpdatedAt = now
	c.lastAction = "recorded " + string(artifact.Kind) + " artifact"
	_, err := c.commitState(before)
	return err
}

// appendRoleArtifact records the artifact reference (and, for a finding, the
// hypothesis identity) in the producing role's context. A context-limit stop is
// surfaced as a continuity warning but does not lose the artifact itself.
func (c *Controller) appendRoleArtifact(artifact workspace.Artifact, hypothesisID string, at time.Time) error {
	update := RoleContextUpdate{
		Progress:  []string{"recorded " + string(artifact.Kind) + " artifact " + artifact.ID + " for " + artifact.Target},
		Artifacts: []string{"artifact:" + artifact.ID},
	}
	if hypothesisID != "" {
		update.HypothesisIDs = []string{hypothesisID}
	}
	if _, err := c.task.UpdateRoleContext(artifact.Origin, update, at); err != nil {
		if errors.Is(err, ErrContextLimit) {
			c.noteProjectionLimit("role context")
			return nil
		}
		return err
	}
	return nil
}

// findingHypothesisText renders the bounded statement BUILD receives. It keeps
// the reproduction and observed result, which are the parts a later
// verification needs, and never claims the finding is proven.
func findingHypothesisText(artifact workspace.Artifact) string {
	parts := []string{artifact.Origin.Label() + " finding at " + strings.TrimSpace(artifact.Target)}
	if status := strings.TrimSpace(artifact.Status); status != "" {
		parts = append(parts, "status="+status)
	}
	if expected := strings.TrimSpace(artifact.Expected); expected != "" {
		parts = append(parts, "expected="+expected)
	}
	if observed := strings.TrimSpace(artifact.Observed); observed != "" {
		parts = append(parts, "observed="+observed)
	}
	if reproduction := strings.TrimSpace(artifact.Reproduction); reproduction != "" {
		parts = append(parts, "reproduce="+reproduction)
	}
	if evidence := strings.TrimSpace(artifact.Evidence); evidence != "" {
		parts = append(parts, "evidence="+evidence)
	}
	return boundText(strings.Join(parts, "; "), maxFactBytes)
}

// hypothesisIDByText deduplicates a repeated finding projection. The artifact
// identity is random by design, so the bounded statement is the stable key.
func (c *Controller) hypothesisIDByText(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	for i := len(c.task.Context.Hypotheses) - 1; i >= 0; i-- {
		if strings.TrimSpace(c.task.Context.Hypotheses[i].Text) == text {
			return c.task.Context.Hypotheses[i].ID
		}
	}
	return ""
}
