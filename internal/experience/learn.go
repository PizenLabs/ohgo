package experience

import (
	"fmt"
	"strings"
	"time"
)

// lessonFor turns one deterministic incident into a bounded lesson.
//
// This is the only place the initial lesson vocabulary lives. It states what the
// runtime learned in operational terms, never a fact about the repository and
// never a policy the model may treat as authority.
func lessonFor(event Event, ctx Context, now time.Time) Lesson {
	statement, _, _, _ := candidateFor(event)
	key := strings.Join([]string{event.Kind, ctx.Key(), event.Tool, event.Target}, "|")
	return Lesson{
		ID:         stableID("lesson", key),
		Key:        key,
		Kind:       event.Kind,
		Statement:  boundText(statement, 512),
		Context:    ctx,
		Regression: RegressionFor(event.Kind),
		At:         now,
	}
}

// candidateFor returns the lesson statement, candidate kind, hint, and preferred
// tools for one event kind.
func candidateFor(event Event) (string, CandidateKind, string, []string) {
	tool := strings.TrimSpace(event.Tool)
	switch event.Kind {
	case EventRepeatedProbe:
		return "An identical successful probe was repeated without new evidence. Reuse the earlier observation or change the coordinate.",
			CandidateDuplicateWarning,
			"Do not repeat " + toolOrProbe(tool) + " with identical arguments; use the existing observation or change the coordinate.",
			nil
	case EventRecoverableFailure:
		if event.NonMaterial {
			return "A failed directory read is not material when an equivalent structural observation already satisfies the task requirement.",
				CandidateRecoveryHint,
				"A directory read failure covered by a successful listing is non-material; continue without opening a review.",
				nil
		}
		return "A failed probe is recoverable when a later observation covers the same coordinate. Retry only with new evidence.",
			CandidateRecoveryHint,
			"Use the observation that covered " + toolOrProbe(tool) + " instead of repeating the failed call; change the coordinate or capability if the observation was incomplete.",
			nil
	case EventMaterialFailure:
		return "A required capability failure with no equivalent observation is material. Escalate or report the evidence gap instead of repeating it.",
			CandidateRecoveryHint,
			"Do not repeat " + toolOrProbe(tool) + "; report the missing evidence or escalate through the normal decision surface.",
			nil
	case EventSuccessfulShortcut:
		return "A low-cost observation path satisfied the objective with no repeated probes. Prefer its already-offered probes under matching conditions.",
			CandidatePreferredToolOrder,
			"Prefer the observed low-cost probes first when the task shape matches.",
			event.Tools
	case EventExpensivePath:
		return "A successful path spent redundant calls. Prefer the shorter sufficient observation sequence under matching conditions.",
			CandidatePreferredToolOrder,
			"Prefer observed probes that produced evidence without repeats; a cheaper path must still acquire the required evidence.",
			event.Tools
	case EventEvidenceGap:
		return "An answer produced without the required evidence enters review. Acquire the named observation before answering.",
			CandidateEvidencePriority,
			"The answer boundary was reached without the required observation; acquire the missing evidence before writing the answer.",
			nil
	case EventStaleReview:
		return "A repeated review boundary means the missing evidence has not been acquired. Change the probe or escalate instead of repeating the answer.",
			CandidateRecoveryHint,
			"The same review boundary recurred; change the probe or escalate through the normal decision surface.",
			nil
	case EventHandoffRedundancy:
		return "A BUILD/FUZZ handoff already carries shared evidence. Re-probing the same coordinate duplicates the investigation.",
			CandidateDuplicateWarning,
			"Use the evidence already carried across the handoff before probing the same coordinate again.",
			nil
	case EventAuthorityRefusal:
		return "A call refused by the active authority must not be retried in the same workspace. Change the capability or request the required authority through the normal decision surface.",
			CandidateRecoveryHint,
			"Do not retry " + toolOrProbe(tool) + " in the same workspace; change the capability or request the required authority through the normal decision surface.",
			nil
	case EventContextGrowth:
		return "Large projected context preceded avoidable exploration. Keep evidence summaries bounded and answer once sufficient.",
			CandidateContextHint,
			"Prefer bounded observations and answer once the evidence is sufficient; do not accumulate another copy of the transcript.",
			nil
	default:
		return "An execution pattern was observed but has no bounded lesson yet.",
			CandidateEvidencePriority,
			"",
			nil
	}
}

// toolOrProbe renders a safe tool reference for a hint.
func toolOrProbe(tool string) string {
	if tool == "" {
		return "the same probe"
	}
	return tool
}

// newRecord builds the first candidate record for an incident. A single
// observation is never applied; it starts the explicit validation lifecycle.
func newRecord(event Event, ctx Context, scope Scope, scopeKey string, now time.Time) Record {
	_, kind, hint, tools := candidateFor(event)
	lesson := lessonFor(event, ctx, now)
	candidate := Candidate{
		ID:             stableID("candidate", event.Key),
		LessonID:       lesson.ID,
		Key:            event.Key,
		Kind:           kind,
		Statement:      lesson.Statement,
		Hint:           boundText(hint, 320),
		PreferredTools: boundedTools(tools),
		Context:        ctx,
		Scope:          scope,
		ScopeKey:       scopeKey,
		Status:         StatusCandidate,
		Observations:   1,
		Cost:           event.Cost,
		Regression:     lesson.Regression,
		Transitions: []Transition{{
			To:     StatusCandidate,
			Reason: "observed once; validation required before application",
			At:     now,
		}},
	}
	return Record{
		ID: stableID("experience", event.Key+"|"+scopeKey),
		Incident: Incident{
			ID:           stableID("incident", event.Key),
			Key:          event.Key,
			Kind:         event.Kind,
			Summary:      boundText(event.Summary, 512),
			Context:      ctx,
			Tool:         event.Tool,
			Target:       event.Target,
			FailureClass: event.FailureClass,
			Evidence:     append([]string(nil), event.Refs...),
			Cost:         event.Cost,
			Occurrences:  1,
			FirstSeen:    now,
			LastSeen:     now,
		},
		Lesson:    lesson,
		Candidate: candidate,
		Scope:     scope,
		ScopeKey:  scopeKey,
		CreatedAt: now,
		UpdatedAt: now,
	}
}

// observeRecord folds one matching observation into an existing record.
//
// Repetition is the default validation: an event seen twice under the same
// structural context is a repeated successful scenario, not a single anecdote.
// A contradiction blocks promotion and can retire an already-active record.
func observeRecord(record Record, event Event, ctx Context, now time.Time, minObservations int, autoPromote bool) Record {
	record.Candidate.Observations++
	record.Incident.Occurrences++
	record.Incident.LastSeen = now
	record.UpdatedAt = now
	if event.Cost.LowerThan(record.Candidate.Cost) {
		record.Candidate.Cost = event.Cost
		if len(event.Tools) > 0 {
			record.Candidate.PreferredTools = boundedTools(event.Tools)
		}
	}
	if record.Candidate.Regression.Name == "" {
		record.Candidate.Regression = RegressionFor(record.Lesson.Kind)
		record.Lesson.Regression = record.Candidate.Regression
	}

	switch record.Candidate.Status {
	case StatusCandidate:
		if record.Candidate.Observations >= minObservations && record.Candidate.Contradictions == 0 {
			record = validateRecord(record, now)
			if autoPromote && promotable(record) {
				record = promoteRecord(record, now, "recurrence validated; regression linked")
			}
		}
	case StatusValidated:
		if autoPromote && promotable(record) {
			record = promoteRecord(record, now, "validated recurrence; regression linked")
		}
	case StatusPromoted:
		record = monitorRecord(record, now)
	case StatusMonitored:
		record.Candidate.LastValidatedAt = now
		record.UpdatedAt = now
	}
	return record
}

// validateRecord moves a candidate to Validated with explicit recurrence
// evidence. It does not apply the candidate.
func validateRecord(record Record, now time.Time) Record {
	if record.Candidate.Status != StatusCandidate {
		return record
	}
	from := record.Candidate.Status
	record.Candidate.Status = StatusValidated
	record.Candidate.LastValidatedAt = now
	if record.Candidate.Observations >= 2 {
		record.Candidate.Validation = append(record.Candidate.Validation, Validation{
			Kind:         ValidationRecurrence,
			Ref:          fmt.Sprintf("%d independent matching observation(s)", record.Candidate.Observations),
			Passed:       true,
			Observations: record.Candidate.Observations,
			At:           now,
		})
	}
	record.Candidate.Transitions = append(record.Candidate.Transitions, Transition{
		From:   from,
		To:     StatusValidated,
		Reason: "repeated matching observation; contradictions=0",
		At:     now,
	})
	record.UpdatedAt = now
	return record
}

// promotable reports whether a validated record has the minimum audit trail to
// become active.
func promotable(record Record) bool {
	if record.Candidate.Status != StatusValidated {
		return false
	}
	if record.Candidate.Contradictions != 0 {
		return false
	}
	if record.Candidate.Regression.Name == "" {
		return false
	}
	for _, validation := range record.Candidate.Validation {
		if validation.Passed && (validation.Kind == ValidationRecurrence || validation.Kind == ValidationRegression) {
			return true
		}
	}
	return false
}

// promoteRecord moves a validated candidate to Promoted.
func promoteRecord(record Record, now time.Time, reason string) Record {
	if record.Candidate.Status != StatusValidated {
		return record
	}
	from := record.Candidate.Status
	record.Candidate.Status = StatusPromoted
	record.Candidate.PromotedAt = &now
	record.Candidate.LastValidatedAt = now
	record.Candidate.Transitions = append(record.Candidate.Transitions, Transition{
		From:   from,
		To:     StatusPromoted,
		Reason: reason,
		At:     now,
	})
	record.UpdatedAt = now
	return record
}

// monitorRecord records continued success after promotion.
func monitorRecord(record Record, now time.Time) Record {
	if record.Candidate.Status != StatusPromoted {
		return record
	}
	from := record.Candidate.Status
	record.Candidate.Status = StatusMonitored
	record.Candidate.LastValidatedAt = now
	record.Candidate.Transitions = append(record.Candidate.Transitions, Transition{
		From:   from,
		To:     StatusMonitored,
		Reason: "continued success after promotion",
		At:     now,
	})
	record.UpdatedAt = now
	return record
}

// contradiction applies one adverse observation to a record.
func contradiction(record Record, event Event, ctx Context, now time.Time) (Record, bool) {
	if !Contradicts(record, event, ctx) {
		return record, false
	}
	record.Candidate.LastContradiction = event.ID
	record.Candidate.Contradictions++
	record.Candidate.Validation = append(record.Candidate.Validation, Validation{
		Kind:   ValidationContradiction,
		Ref:    event.ID,
		Passed: false,
		At:     now,
	})
	record.UpdatedAt = now
	if record.Candidate.Status == StatusCandidate || record.Candidate.Status == StatusValidated {
		if record.Candidate.Contradictions >= record.Candidate.Observations {
			from := record.Candidate.Status
			record.Candidate.Status = StatusRejected
			record.Candidate.Transitions = append(record.Candidate.Transitions, Transition{
				From:   from,
				To:     StatusRejected,
				Reason: "contradictory observations met or exceeded successful observations",
				At:     now,
			})
		}
	}
	if record.Candidate.Status.Active() &&
		record.Candidate.Contradictions >= maxInt(2, record.Candidate.Observations) {
		record = retireRecord(record, now, "contradictory observations exceeded successful observations")
	}
	return record, true
}

// Contradicts reports whether event is adverse evidence for a record.
//
// The relation is deliberately narrow and structural. It never treats a
// different task shape as a contradiction, and it never turns an authority
// refusal into an authority change.
func Contradicts(record Record, event Event, ctx Context) bool {
	if record.Candidate.Key == event.Key || record.Candidate.Context.Key() != ctx.Key() {
		return false
	}
	switch record.Candidate.Kind {
	case CandidatePreferredToolOrder:
		return event.Kind == EventMaterialFailure || event.Kind == EventEvidenceGap
	case CandidateRecoveryHint:
		sameTool := record.Incident.Tool == "" || record.Incident.Tool == event.Tool
		if !sameTool {
			return false
		}
		if record.Lesson.Kind == EventRecoverableFailure && event.Kind == EventMaterialFailure {
			return true
		}
		if record.Lesson.Kind == EventMaterialFailure && event.Kind == EventRecoverableFailure {
			return true
		}
		if record.Lesson.Kind == EventAuthorityRefusal && event.Kind == EventSuccessfulShortcut {
			// The same capability succeeded in the same structural context, so
			// the blanket "do not retry" lesson is too strong.
			return true
		}
		return false
	default:
		return false
	}
}

// retireRecord retires a record and records why.
func retireRecord(record Record, now time.Time, reason string) Record {
	if record.Candidate.Status == StatusRetired {
		return record
	}
	from := record.Candidate.Status
	record.Candidate.Status = StatusRetired
	record.Candidate.RetiredAt = &now
	record.Candidate.RetirementReason = boundText(reason, 320)
	record.Candidate.Transitions = append(record.Candidate.Transitions, Transition{
		From:   from,
		To:     StatusRetired,
		Reason: record.Candidate.RetirementReason,
		At:     now,
	})
	record.UpdatedAt = now
	return record
}

// rejectRecord records an explicit human/host rejection.
func rejectRecord(record Record, now time.Time, reason string) Record {
	if record.Candidate.Status == StatusRetired {
		return record
	}
	from := record.Candidate.Status
	record.Candidate.Status = StatusRejected
	record.Candidate.RetirementReason = boundText(reason, 320)
	record.Candidate.Transitions = append(record.Candidate.Transitions, Transition{
		From:   from,
		To:     StatusRejected,
		Reason: record.Candidate.RetirementReason,
		At:     now,
	})
	record.UpdatedAt = now
	return record
}

// boundedTools trims a preferred-tool list and removes duplicates.
func boundedTools(tools []string) []string {
	tools = uniqueStrings(tools)
	if len(tools) > maxEventTools {
		tools = tools[:maxEventTools]
	}
	return tools
}

// maxInt returns the larger value.
func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
