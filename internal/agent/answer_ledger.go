package agent

import "github.com/PizenLabs/ohgo/internal/control/contract"

// answerLedger is the per-run claim-preserving draft history.
//
// It keeps the strongest draft, not merely the latest one. A later revision
// that drops an independently supported claim is not allowed to displace the
// stronger draft: preservation is measured against the best valid claim set,
// and the fallback repair starts from that draft rather than from the weaker
// rewrite.
type answerLedger struct {
	nextVersion int
	drafts      []AnswerDraft
	best        AnswerDraft
	haveBest    bool
	last        AnswerDraft
	haveLast    bool
}

// observe records a candidate and its review issues, returning the draft the
// turn carries. The caller owns the disposition (supersede, reject, commit).
func (l *answerLedger) observe(text string, issues []contract.GroundingIssue) AnswerDraft {
	l.nextVersion++
	supersedes := 0
	if l.haveLast {
		supersedes = l.last.Version
	}
	draft := newAnswerDraft(l.nextVersion, supersedes, text, issues)
	if l.haveBest {
		// A review of the newest text also invalidates the same assertion in
		// the strongest draft. A claim that has become unsupported must not be
		// resurrected by the fallback. A finding about text the strongest
		// draft does not contain stays scoped to the newer draft.
		l.best.invalidateClaims(issues)
		missing := missingClaims(l.best, text)
		if len(missing) > 0 {
			draft.MissingPreserved = claimTexts(missing)
			draft.PreservationLost = true
		}
		// The best draft is the one that keeps every currently valid claim and
		// carries at least as much independently supported content.
		if len(missing) == 0 && len(draft.ValidClaims()) >= len(l.best.ValidClaims()) {
			l.best = draft
		}
	} else {
		l.best = draft
		l.haveBest = true
	}
	l.last = draft
	l.haveLast = true
	l.drafts = append(l.drafts, draft)
	return draft
}

// supersede closes a draft a correction replaced. Its claims remain in the
// ledger; only its disposition changes.
func (l *answerLedger) supersede(version int) {
	for index := range l.drafts {
		if l.drafts[index].Version == version {
			l.drafts[index].State = AnswerDraftSuperseded
			break
		}
	}
	if l.haveBest && l.best.Version == version {
		l.best.State = AnswerDraftSuperseded
	}
	if l.haveLast && l.last.Version == version {
		l.last.State = AnswerDraftSuperseded
	}
}

// newVersion allocates the next draft version for a repair that composes a new
// semantic answer from earlier drafts.
func (l *answerLedger) newVersion() int {
	l.nextVersion++
	return l.nextVersion
}

// commit closes the answer lifecycle. The committed draft is the one the turn
// and the response audit expose.
func (l *answerLedger) commit(draft AnswerDraft) AnswerDraft {
	draft.State = AnswerDraftCommitted
	found := false
	for index := range l.drafts {
		if l.drafts[index].Version == draft.Version {
			l.drafts[index] = draft
			found = true
			break
		}
	}
	if !found {
		l.drafts = append(l.drafts, draft)
	}
	// The committed draft is the authoritative claim set. Keeping an earlier
	// draft as "best" after commit would make the audit claim preservation the
	// published answer does not have.
	l.best = draft
	l.haveBest = true
	if l.haveLast && l.last.Version == draft.Version {
		l.last = draft
	}
	return draft
}

// reject closes a draft as a terminal refusal.
func (l *answerLedger) reject(version int) {
	for index := range l.drafts {
		if l.drafts[index].Version == version {
			l.drafts[index].State = AnswerDraftRejectedTerminal
			break
		}
	}
	if l.haveLast && l.last.Version == version {
		l.last.State = AnswerDraftRejectedTerminal
	}
}

// salvage removes the rejected claims from the strongest draft. It returns the
// surviving text and a draft that records which claims were removed.
func (l *answerLedger) salvage() (string, AnswerDraft, bool) {
	if !l.haveBest {
		return "", AnswerDraft{}, false
	}
	return salvageDraft(l.best)
}

// preserved returns the valid claim text a revision must carry forward.
func (l *answerLedger) preserved() []string {
	if !l.haveBest {
		return nil
	}
	return l.best.ClaimTexts()
}

// preservedCount counts the strongest draft's valid factual claims.
func (l *answerLedger) preservedCount() int {
	if !l.haveBest {
		return 0
	}
	return len(l.best.ValidClaims())
}

// removedCount counts the unsupported claims the fallback repair removed.
func (l *answerLedger) removedCount() int {
	if !l.haveBest {
		return 0
	}
	removed := 0
	for _, claim := range l.best.Claims {
		if claim.Repairable() && claim.State == ClaimRemoved {
			removed++
		}
	}
	return removed
}

// summaries returns the bounded lineage for the response audit.
func (l *answerLedger) summaries() []DraftAudit {
	out := make([]DraftAudit, 0, len(l.drafts))
	for _, draft := range l.drafts {
		out = append(out, summarizeDraft(draft))
	}
	sortDraftAudits(out)
	return out
}

// claimTexts returns the text of a claim list.
func claimTexts(claims []AnswerClaim) []string {
	out := make([]string, 0, len(claims))
	for _, claim := range claims {
		out = append(out, claim.Text)
	}
	return out
}
