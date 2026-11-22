package contract

import (
	"strings"
	"unicode"
)

// ConclusionVerdict is the terminal judgement an investigation records.
//
// SAFE and BUG describe a defect hypothesis. ANSWERED and PARTIAL describe an
// informational result. Keeping one vocabulary for both is a category error:
// an answer to "what is this project?" is not a safety verdict, and a defect
// verdict is not an answer. The active ConclusionMode decides which subset a
// request may use.
type ConclusionVerdict string

// Conclusion verdicts.
const (
	// VerdictAnswered means the request was answered by observed evidence.
	VerdictAnswered ConclusionVerdict = "ANSWERED"
	// VerdictPartial means a usable answer exists, but the answer itself names
	// what remains unverified or unsupported.
	VerdictPartial ConclusionVerdict = "PARTIAL"
	// VerdictUnresolved means the available evidence cannot establish the
	// answer or the defect verdict.
	VerdictUnresolved ConclusionVerdict = "UNRESOLVED"
	// VerdictSafe means the production flow prevents the failure.
	VerdictSafe ConclusionVerdict = "SAFE"
	// VerdictBug means a production-reachable path creates the failure.
	VerdictBug ConclusionVerdict = "BUG"
)

// ConclusionMode is the terminal-outcome vocabulary a request admits.
type ConclusionMode string

// Conclusion modes.
const (
	// ConclusionAnswer is a general informational request or an investigation
	// without a defect hypothesis. Its outcome is whether the question was
	// answered, not whether the code is safe.
	ConclusionAnswer ConclusionMode = "answer"
	// ConclusionDefect is a failure-mode investigation or a prompt that asks
	// for a safety verdict. Its outcome is whether the failure is reachable.
	ConclusionDefect ConclusionMode = "defect"
)

// answerVerdicts and defectVerdicts are the only admissible sets. Both share
// UNRESOLVED because either objective can honestly fail to reach a terminal
// finding.
var (
	answerVerdicts = []ConclusionVerdict{VerdictAnswered, VerdictPartial, VerdictUnresolved}
	defectVerdicts = []ConclusionVerdict{VerdictSafe, VerdictBug, VerdictUnresolved}
)

// ConclusionModeFor returns the conclusion vocabulary for an objective.
//
// Only informational objectives have an answer to record. A mutation or
// execution objective returns the empty mode, which keeps the legacy
// unrestricted vocabulary; those objectives have no investigation conclusion
// requirement.
func ConclusionModeFor(kind ObjectiveKind, objective string) ConclusionMode {
	if kind != ObjectiveInformational {
		return ""
	}
	if FailureInvestigation(objective) || ConclusionRequested(objective) {
		return ConclusionDefect
	}
	return ConclusionAnswer
}

// Verdicts returns the allowed verdicts in stable schema order.
func (m ConclusionMode) Verdicts() []ConclusionVerdict {
	switch m {
	case ConclusionAnswer:
		return append([]ConclusionVerdict(nil), answerVerdicts...)
	case ConclusionDefect:
		return append([]ConclusionVerdict(nil), defectVerdicts...)
	default:
		// An unspecified mode is an embedder that predates the vocabulary.
		// Accept every known verdict rather than inventing a restriction the
		// host did not install.
		return append(append([]ConclusionVerdict(nil), answerVerdicts...), VerdictSafe, VerdictBug)
	}
}

// Accepts reports whether the verdict belongs to the mode's vocabulary.
func (m ConclusionMode) Accepts(verdict ConclusionVerdict) bool {
	if !verdict.Known() {
		return false
	}
	for _, allowed := range m.Verdicts() {
		if allowed == verdict {
			return true
		}
	}
	return false
}

// Known reports whether the verdict is one of the defined values.
func (v ConclusionVerdict) Known() bool {
	switch v {
	case VerdictAnswered, VerdictPartial, VerdictUnresolved, VerdictSafe, VerdictBug:
		return true
	}
	return false
}

// VerdictList renders the allowed verdicts for an instruction or error.
func (m ConclusionMode) VerdictList() string {
	verdicts := m.Verdicts()
	names := make([]string, 0, len(verdicts))
	for _, verdict := range verdicts {
		names = append(names, string(verdict))
	}
	return joinAlternatives(names)
}

// Instruction states what a terminal conclusion must record for this mode.
func (m ConclusionMode) Instruction() string {
	switch m {
	case ConclusionAnswer:
		return "record ANSWERED, PARTIAL, or UNRESOLVED with the evidence that supports it and the material gaps that remain"
	case ConclusionDefect:
		return "record SAFE, BUG, or UNRESOLVED with the evidence that supports it"
	default:
		return "record the terminal outcome with the evidence that supports it"
	}
}

// ConclusionRequested reports an investigation prompt that explicitly asks for
// a terminal verdict.
func ConclusionRequested(objective string) bool {
	normalized := " " + NormalizeWords(objective) + " "
	for _, phrase := range []string{
		" conclude ", " conclusion ", " verdict ", " safe or bug ",
		" safe bug ", " safe or unresolved ", " bug or unresolved ",
	} {
		if strings.Contains(normalized, phrase) {
			return true
		}
	}
	return false
}

// FailureInvestigation reports an investigation whose subject is a failure
// mode. Those prompts need a verdict even when they do not spell out the word
// "conclude".
func FailureInvestigation(objective string) bool {
	normalized := " " + NormalizeWords(objective) + " "
	for _, phrase := range []string{
		" failure ", " failures ", " failing ", " bug ", " bugs ", " crash ",
		" panics ", " panic ", " deadlock ", " race condition ", " reproduce ",
	} {
		if strings.Contains(normalized, phrase) {
			return true
		}
	}
	return false
}

// IsProvisionalConclusion reports whether a conclusion payload explicitly says
// the investigation is not settled yet.
//
// This is a control-plane guard, not a semantic understanding of prose. It
// recognizes the small set of phrases a model uses when it is reporting
// progress, such as "placeholder, not yet sufficient", "preliminary", or
// "still investigating". A provisional payload must be recorded as an
// observation, never admitted as a terminal conclusion.
//
// The comparison is normalized so punctuation, case, and code formatting do
// not hide a marker. The markers are deliberately phrase-based: words such as
// "insufficient" and "incomplete" occur in legitimate findings about stock or
// implementation, so they do not trigger on their own.
func IsProvisionalConclusion(parts ...string) bool {
	text := normalizeConclusionText(strings.Join(parts, " "))
	for _, marker := range []string{
		" placeholder not yet sufficient ",
		" placeholder conclusion ",
		" not yet sufficient ",
		" not yet concluded ",
		" investigation remains open ",
		" investigation is still in progress ",
		" still investigating ",
		" still in progress ",
		" continuing investigation ",
		" preliminary ",
		" insufficient evidence ",
		" insufficient information ",
		" not enough evidence ",
		" not enough information ",
		" need more evidence ",
		" needs more evidence ",
		" more evidence is needed ",
		" more evidence needed ",
		" evidence is incomplete ",
		" incomplete evidence ",
		" investigation is incomplete ",
		" not yet established ",
		" further investigation ",
		" requires investigation ",
		" pending investigation ",
		" to be determined ",
	} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

// normalizeConclusionText lowercases and reduces punctuation and whitespace to
// single separators, so "Placeholder, not-yet sufficient." still matches.
func normalizeConclusionText(text string) string {
	var b strings.Builder
	b.Grow(len(text) + 2)
	b.WriteByte(' ')
	space := true
	for _, r := range strings.ToLower(text) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
			space = false
			continue
		}
		if !space {
			b.WriteByte(' ')
			space = true
		}
	}
	if !space {
		b.WriteByte(' ')
	}
	return b.String()
}
