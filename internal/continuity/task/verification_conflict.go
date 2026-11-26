package task

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/PizenLabs/ohgo/internal/control/cognition"
	"github.com/PizenLabs/ohgo/internal/control/contract"
	"github.com/PizenLabs/ohgo/internal/control/workspace"
)

// verificationConflictSummaryPrefix is the durable observation wording. The
// view trims it so the user's report is the claim, not the runtime's envelope.
const verificationConflictSummaryPrefix = "user reported behavior contradicting verification: "

// VerificationConflict is the durable view of a user report that disputes a
// recorded verification.
//
// It is a projection of the contradiction evidence, never a second record. The
// claim is the user's bounded report; Evidence names the check it disputes.
// The task context remains the owner of both observations.
type VerificationConflict struct {
	// Claim is what the user reported observing.
	Claim string
	// Evidence is the disputed verification summary.
	Evidence string
	// EvidenceID is the task evidence record for that verification.
	EvidenceID string
	// ContradictionID is the current contradiction record.
	ContradictionID string
}

// VerificationConflict returns the current user-reported contradiction of a
// verification, or false when no such conflict is current.
//
// A resolved conflict is retired by invalidating its contradiction record; the
// observation remains in history but no longer owns the next action.
func (t Task) VerificationConflict() (VerificationConflict, bool) {
	for i := len(t.Context.Evidence) - 1; i >= 0; i-- {
		record := t.Context.Evidence[i]
		if record.Validity != EvidenceCurrent || record.Outcome != EvidenceContradicting {
			continue
		}
		for _, target := range record.Contradicts {
			fact, ok := t.Context.factByID(target)
			if !ok || fact.Status != FactActive {
				continue
			}
			if _, ok := verificationSupport(t, fact.EvidenceIDs); !ok {
				continue
			}
			claim := strings.TrimPrefix(record.Summary, verificationConflictSummaryPrefix)
			return VerificationConflict{
				Claim:           claim,
				Evidence:        fact.Text,
				EvidenceID:      fact.ID,
				ContradictionID: record.ID,
			}, true
		}
	}
	return VerificationConflict{}, false
}

// RecordVerificationConflict opens an explicit conflict between a user's
// reported behavior and the latest current verification.
//
// The user report is evidence of a disputed observation, not a new objective.
// It is recorded as contradicting evidence so the original check is preserved
// and the task can route to behavioral investigation. If no current
// verification exists, no conflict is created: there is nothing to dispute.
func (c *Controller) RecordVerificationConflict(claim string) (Task, error) {
	if err := c.ensureLoaded(); err != nil {
		return Task{}, err
	}
	if c.task.ID == "" || c.task.Status.IsTerminal() {
		return c.task.Clone(), nil
	}
	claim = strings.TrimSpace(claim)
	if claim == "" {
		return c.task.Clone(), nil
	}
	// Project the just-finished observations first, so the verification the
	// user is disputing is visible to the same boundary that records the
	// contradiction.
	c.projectTaskContext()
	var verification EvidenceRecord
	if c.task.Contract != nil && c.task.Contract.Kind == contract.ObjectiveMutation {
		if change, ok := c.latestChangeRecord(); ok {
			after := c.verificationsAfterChange(change)
			if len(after) > 0 {
				verification = after[len(after)-1]
			}
		}
	} else if verifications := c.verificationEvidence(); len(verifications) > 0 {
		verification = verifications[0]
	}
	if verification.ID == "" {
		// A check that did not follow the change cannot verify the state the
		// user is disputing.
		return c.task.Clone(), nil
	}
	if c.evidence.OpenVerificationConflict() != nil {
		// An open conflict already owns the next action. Re-recording the same
		// dispute would only add duplicate evidence.
		return c.task.Clone(), nil
	}

	before := c.snapshotState()
	now := c.now().UTC()
	role := c.currentRole()
	if !role.Valid() {
		role = workspace.Build
	}
	fact, err := c.verificationFact(verification, role)
	if err != nil {
		c.restoreState(before)
		return c.task.Clone(), err
	}
	contradiction, err := c.task.RecordEvidence(EvidenceInput{
		Kind:     EvidenceHuman,
		Role:     role,
		Source:   "user:verification-contradiction",
		Artifact: verification.Artifact,
		Summary:  verificationConflictSummaryPrefix + claim,
		Outcome:  EvidenceContradicting,
		Contradicts: []string{
			fact.ID,
		},
		// The same verification can be disputed again after a resolution;
		// the timestamp makes each report a distinct observation instead of
		// replaying an invalidated contradiction.
		DedupKey: "verification-conflict:" + verification.ID + ":" +
			strconv.FormatInt(now.UnixNano(), 10),
		At: now,
	})
	if err != nil {
		c.restoreState(before)
		return c.task.Clone(), err
	}
	evidenceID := strings.TrimPrefix(verification.Artifact, "cognition:")
	if evidenceID == "" {
		evidenceID = verification.ID
	}
	seq, _ := c.observationSeq(verification)
	if seq == 0 {
		seq = c.evidence.Seq
	}
	c.evidence.RecordVerificationConflict(claim, evidenceID, verification.Summary, seq, contradiction.ID)
	c.task.ReviewReason = fmt.Sprintf(
		"verification conflict: the user reported behavior contradicting the last verification: %s", claim)
	// A new review boundary supersedes a prior terminal conclusion. The user's
	// report reopens the work; it is not the same run that ended unresolved.
	c.task.Conclusion = nil
	if c.task.Status != StatusPending && c.task.Status != StatusNeedsReview {
		if err := c.task.NeedsReview(now); err != nil {
			c.restoreState(before)
			return c.task.Clone(), err
		}
	}
	c.task.UpdatedAt = now
	c.lastAction = "verification conflict recorded"
	c.task.AppendEvent(TimelineEvent{
		At:      now,
		Kind:    TimelineReview,
		Role:    role,
		Status:  string(c.task.Status),
		Summary: "verification conflict: " + claim,
		Refs:    []string{contradiction.ID, fact.ID, verification.ID},
	})
	return c.commitState(before)
}

// verificationFact returns the active fact backed by one verification record,
// creating it when the projection has not recorded one yet. A fact is the
// contradiction model's claim coordinate; evidence records are its support,
// not a claim target.
func (c *Controller) verificationFact(verification EvidenceRecord, role workspace.Workspace) (Fact, error) {
	for _, fact := range c.task.Context.Facts {
		if fact.Status != FactActive {
			continue
		}
		for _, id := range fact.EvidenceIDs {
			if id == verification.ID {
				return fact, nil
			}
		}
	}
	return c.task.RecordFact(FactInput{
		Text:        verification.Summary,
		Role:        role,
		EvidenceIDs: []string{verification.ID},
		At:          c.now().UTC(),
	})
}

// verificationSupport returns the verification evidence backing a fact.
func verificationSupport(t Task, evidenceIDs []string) (EvidenceRecord, bool) {
	for _, id := range evidenceIDs {
		evidence, ok := t.Context.evidenceByID(id)
		if !ok || !isSupporting(evidence) || !isVerificationEvidence(evidence) {
			continue
		}
		return evidence, true
	}
	return EvidenceRecord{}, false
}

// VerificationConflict returns the open runtime conflict, when one exists.
//
// The cognition evidence owns the status because a behavioral observation can
// resolve it mid-turn. The task projection still carries the contradiction
// record for audit and persistence.
func (c *Controller) VerificationConflict() (VerificationConflict, bool) {
	if c == nil || c.evidence == nil {
		return VerificationConflict{}, false
	}
	open := c.evidence.OpenVerificationConflict()
	if open == nil {
		return VerificationConflict{}, false
	}
	conflict := VerificationConflict{
		Claim:      open.Claim,
		Evidence:   open.EvidenceSummary,
		EvidenceID: open.EvidenceID,
	}
	if current, ok := c.task.VerificationConflict(); ok {
		conflict.ContradictionID = current.ContradictionID
	}
	return conflict, true
}

// retireResolvedVerificationConflict invalidates the contradiction record once
// a behavioral check has answered it. The history remains inspectable; it no
// longer makes the proof materially contradicted.
func (c *Controller) retireResolvedVerificationConflict() {
	if c == nil || c.evidence == nil {
		return
	}
	conflict := c.evidence.VerificationConflict
	if conflict == nil || conflict.Status != cognition.VerificationConflictResolved ||
		strings.TrimSpace(conflict.ContradictionID) == "" {
		return
	}
	reason := "resolved by behavioral verification"
	if conflict.Resolution != "" {
		reason += ": " + conflict.Resolution
	}
	if _, err := c.task.InvalidateEvidence(conflict.ContradictionID, reason, c.now().UTC()); err == nil {
		conflict.ContradictionID = ""
	}
}
