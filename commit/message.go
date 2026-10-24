// Package commit turns repository state into one validated Git commit.
//
// It owns the deterministic half of /commit: deciding the exact candidate
// scope, asking the dedicated commit model for text, validating that text, and
// executing exactly the approved commit. The model never receives a tool, a
// shell, or a path to the index; its output is untrusted proposal data.
package commit

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Length limits are measured in Unicode code points (runes), not bytes and not
// terminal cells. A rune is what a human counts as one character; bytes would
// unfairly shorten non-ASCII text, and display width depends on the terminal
// font. The contract is one number everywhere.
const (
	// MaxSubjectRunes bounds the complete first line.
	MaxSubjectRunes = 36
	// MaxBulletRunes bounds one complete body line, including "- ".
	MaxBulletRunes = 50
	// MaxBullets bounds the body so a proposal stays reviewable.
	MaxBullets = 5
)

// ErrInvalidProposal means the model's output did not satisfy the message
// contract. The caller may regenerate within a bounded retry policy; it must
// never execute a proposal that carries this error.
var ErrInvalidProposal = errors.New("commit: invalid proposal")

// subjectPattern is the conventional-commit shape. The scope and summary are
// validated separately so the error can name the failed rule. The type is any
// lowercase conventional token; the prompt recommends a small vocabulary, but
// validation does not force a repository to use only those words.
var (
	subjectPattern = regexp.MustCompile(`^([a-z][a-z0-9-]*)\(([^()\s]+)\): (.+)$`)
	// repairSubjectPattern is deliberately more permissive than the validation
	// pattern: repair needs the type and summary even when the scope is empty,
	// spaced, uppercase, or otherwise unusable. The scope is then replaced by
	// the deterministic value derived from the approved paths.
	repairSubjectPattern = regexp.MustCompile(`^([a-z][a-z0-9-]*)\s*\(([^()]*)\)\s*:\s*(.+)$`)
	// noScopeSubjectPattern is the conventional form without a scope. The
	// proposal contract requires a scope, but a model can omit it; repair
	// fills one from the approved paths instead of failing the whole commit.
	noScopeSubjectPattern = regexp.MustCompile(`^([a-z][a-z0-9-]*)\s*:\s*(.+)$`)
	// scopePattern keeps a scope to one lowercase token.
	scopePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._/-]*$`)
	// referencePattern finds the issue/PR forms that must never be invented.
	referencePattern = regexp.MustCompile(`#\d+|https?://[^\s]+`)
)

// Proposal is the model's untrusted commit text, split into the fields a
// reviewer needs to see. Body holds complete bullet lines, each starting "- ".
type Proposal struct {
	// Type is the conventional-commit type.
	Type string
	// Scope names the area changed.
	Scope string
	// Summary is the subject text after ": ".
	Summary string
	// Body is the optional bullet list, in order.
	Body []string
}

// Subject returns the complete first line.
func (p Proposal) Subject() string {
	if p.Type == "" || p.Scope == "" || p.Summary == "" {
		return ""
	}
	return fmt.Sprintf("%s(%s): %s", p.Type, p.Scope, p.Summary)
}

// Message returns the exact text passed to Git: subject, one blank line when a
// body exists, then the bullets. There is no trailing newline.
func (p Proposal) Message() string {
	subject := p.Subject()
	if len(p.Body) == 0 {
		return subject
	}
	return subject + "\n\n" + strings.Join(p.Body, "\n")
}

// Parse converts untrusted model text into a Proposal and validates it.
//
// refs is the set of references present in the evidence. A #123 or URL token
// absent from that set is rejected as invented. The parser accepts an optional
// single outer code fence because models routinely add one; the message inside
// it still has to pass every rule.
func Parse(text string, refs map[string]bool) (Proposal, error) {
	cleaned := normalize(text)
	if cleaned == "" {
		return Proposal{}, invalid("the reply was empty")
	}

	lines := strings.Split(cleaned, "\n")
	for i := range lines {
		lines[i] = strings.TrimRight(lines[i], " \t")
	}
	if len(lines) > MaxBullets+2 {
		return Proposal{}, invalid("the message has more than %d body lines", MaxBullets)
	}

	matches := subjectPattern.FindStringSubmatch(lines[0])
	if matches == nil {
		return Proposal{}, invalid("subject %q is not `type(scope): message`", lines[0])
	}
	proposal := Proposal{
		Type:    matches[1],
		Scope:   matches[2],
		Summary: matches[3],
	}

	if len(lines) == 1 {
		if err := proposal.Validate(refs); err != nil {
			return Proposal{}, err
		}
		return proposal, nil
	}
	if lines[1] != "" {
		return Proposal{}, invalid("exactly one blank line is required between subject and body")
	}
	for _, line := range lines[2:] {
		if line == "" {
			return Proposal{}, invalid("the body contains an empty line")
		}
		proposal.Body = append(proposal.Body, line)
	}
	if err := proposal.Validate(refs); err != nil {
		return Proposal{}, err
	}
	return proposal, nil
}

// Validate applies every deterministic rule to a proposal.
func (p Proposal) Validate(refs map[string]bool) error {
	subject := p.Subject()
	if subject == "" {
		return invalid("the subject is incomplete")
	}
	matches := subjectPattern.FindStringSubmatch(subject)
	if matches == nil || matches[1] != p.Type || matches[2] != p.Scope || matches[3] != p.Summary {
		return invalid("subject %q is not `type(scope): message`", subject)
	}
	if !scopePattern.MatchString(p.Scope) {
		return invalid("scope %q is not one lowercase token", p.Scope)
	}
	if strings.TrimSpace(p.Summary) != p.Summary {
		return invalid("the subject message has leading or trailing whitespace")
	}
	if hasControl(subject) {
		return invalid("the subject contains a control character")
	}
	if n := utf8.RuneCountInString(subject); n > MaxSubjectRunes {
		return invalid("the subject is %d characters; the limit is %d", n, MaxSubjectRunes)
	}
	if err := validateReferences(subject, refs); err != nil {
		return err
	}

	if len(p.Body) > MaxBullets {
		return invalid("the body has %d bullets; the limit is %d", len(p.Body), MaxBullets)
	}
	for _, line := range p.Body {
		if !strings.HasPrefix(line, "- ") {
			return invalid("body line %q must start with `- `", line)
		}
		if strings.TrimSpace(strings.TrimPrefix(line, "- ")) == "" {
			return invalid("body line %q has no summary", line)
		}
		if hasControl(line) {
			return invalid("body line %q contains a control character", line)
		}
		if n := utf8.RuneCountInString(line); n > MaxBulletRunes {
			return invalid("body line %q is %d characters; the limit is %d", line, n, MaxBulletRunes)
		}
		if err := validateReferences(line, refs); err != nil {
			return err
		}
	}
	return nil
}

// repairProposal is the deterministic last resort after bounded regeneration.
//
// A small model can repeatedly miss the scope or the line budgets while fixing
// every other rule. Repairing those content-free defects keeps an otherwise
// valid proposal instead of failing /commit. The scope comes from the
// already-approved paths, never from the model, and the fitted lines keep the
// model's type and summary.
//
// The repaired text is parsed again, so repair never bypasses a validation rule:
// an invalid body, an invented reference, or a malformed type still fails. The
// human reviews the result before execution.
func repairProposal(text string, refs map[string]bool, fallbackScope string) (Proposal, bool) {
	cleaned := normalize(text)
	if cleaned == "" {
		return Proposal{}, false
	}
	subject, body := splitSubjectBody(cleaned)
	typ, scope, summary, ok := parseSubjectParts(subject, fallbackScope)
	if !ok {
		return Proposal{}, false
	}
	prefix := typ + "(" + scope + "): "
	if utf8.RuneCountInString(prefix+summary) > MaxSubjectRunes {
		allowance := MaxSubjectRunes - utf8.RuneCountInString(prefix)
		if allowance <= 0 {
			return Proposal{}, false
		}
		summary = fitSummary(summary, allowance)
		if summary == "" {
			return Proposal{}, false
		}
	}
	body = normalizeBodySeparator(body)
	body = fitBullets(body)
	proposal, err := Parse(prefix+summary+body, refs)
	if err != nil {
		return Proposal{}, false
	}
	return proposal, true
}

// normalizeBodySeparator inserts the blank line a model sometimes omits between
// the subject and the first bullet. Other shapes are left untouched so Parse can
// still reject them.
func normalizeBodySeparator(body string) string {
	if strings.HasPrefix(body, "\n- ") || strings.HasPrefix(body, "\n* ") {
		return "\n" + body
	}
	return body
}

// fitBullets bounds the body to MaxBullets lines, normalizes `* ` markers to
// `- `, and shortens each overlong line to the bullet budget. Extra bullets are
// dropped, and other body shapes are left untouched so Parse can still reject
// them; repair only removes or fits content, never invents or reorders it.
func fitBullets(body string) string {
	if body == "" {
		return ""
	}
	lines := strings.Split(body, "\n")
	out := make([]string, 0, len(lines))
	bullets := 0
	for _, line := range lines {
		summary, isBullet := bulletSummary(line)
		if !isBullet {
			out = append(out, line)
			continue
		}
		bullets++
		if bullets > MaxBullets {
			continue
		}
		if utf8.RuneCountInString(line) > MaxBulletRunes {
			summary = fitSummary(summary, MaxBulletRunes-2)
			if summary == "" {
				return body
			}
		}
		out = append(out, "- "+summary)
	}
	return strings.Join(out, "\n")
}

// bulletSummary accepts the two markers models use for a body item.
func bulletSummary(line string) (summary string, ok bool) {
	switch {
	case strings.HasPrefix(line, "- "):
		return strings.TrimPrefix(line, "- "), true
	case strings.HasPrefix(line, "* "):
		return strings.TrimPrefix(line, "* "), true
	default:
		return "", false
	}
}

// parseSubjectParts accepts either the full `type(scope): summary` form or the
// scope-less `type: summary` form the model sometimes emits. A missing or
// malformed scope is replaced with the deterministic scope derived from the
// approved paths, never with a value guessed from the model. An unusable
// fallback leaves the proposal invalid.
func parseSubjectParts(subject, fallbackScope string) (typ, scope, summary string, ok bool) {
	fallback := strings.TrimSpace(fallbackScope)
	if match := repairSubjectPattern.FindStringSubmatch(subject); match != nil {
		scope = match[2]
		if !scopePattern.MatchString(scope) {
			scope = fallback
		}
		if !scopePattern.MatchString(scope) {
			return "", "", "", false
		}
		return match[1], scope, strings.TrimSpace(match[3]), true
	}
	match := noScopeSubjectPattern.FindStringSubmatch(subject)
	if match == nil {
		return "", "", "", false
	}
	if fallback == "" || !scopePattern.MatchString(fallback) {
		return "", "", "", false
	}
	return match[1], fallback, strings.TrimSpace(match[2]), true
}

// fitSummary shortens a summary to at most limit runes. It prefers the last
// word boundary so the fitted subject does not end mid-word; a single long token
// is cut at the rune limit because there is no boundary to preserve. A trailing
// conjunction or preposition is dropped so the fitted phrase reads as a phrase
// rather than a sentence cut off in the middle.
func fitSummary(summary string, limit int) string {
	summary = strings.TrimSpace(summary)
	if utf8.RuneCountInString(summary) <= limit {
		return summary
	}
	cut := []rune(summary)[:limit]
	for i := len(cut) - 1; i > 0; i-- {
		if unicode.IsSpace(cut[i]) {
			cut = cut[:i]
			break
		}
	}
	words := strings.Fields(string(cut))
	for len(words) > 1 && isTrailingStopWord(words[len(words)-1]) {
		words = words[:len(words)-1]
	}
	return strings.TrimRight(strings.Join(words, " "), ",;:-")
}

// isTrailingStopWord reports a word that should not end a shortened phrase.
func isTrailingStopWord(word string) bool {
	switch strings.ToLower(word) {
	case "a", "an", "and", "as", "at", "by", "for", "from", "in", "into",
		"of", "on", "or", "the", "to", "with":
		return true
	}
	return false
}

// ValidationText returns the concrete rule that rejected a message, without
// the sentinel prefix. A caller that asks the human to correct an edit needs
// the rule, not a second layer of "commit: invalid proposal".
func ValidationText(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, ErrInvalidProposal) {
		return strings.TrimPrefix(err.Error(), ErrInvalidProposal.Error()+": ")
	}
	return err.Error()
}

// validateReferences rejects a reference the evidence never contained.
func validateReferences(line string, refs map[string]bool) error {
	for _, token := range referencePattern.FindAllString(line, -1) {
		if !refs[token] {
			return invalid("reference %q is not present in the repository evidence", token)
		}
	}
	return nil
}

// normalize trims outer blank space, CRLF endings, and one optional code fence.
func normalize(text string) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	text = strings.TrimSpace(text)
	if !strings.HasPrefix(text, "```") {
		return text
	}
	lines := strings.Split(text, "\n")
	if len(lines) < 3 || strings.TrimSpace(lines[len(lines)-1]) != "```" {
		return text
	}
	// The opening fence may carry a language tag; it is not part of the
	// message either way.
	return strings.TrimSpace(strings.Join(lines[1:len(lines)-1], "\n"))
}

// hasControl reports control characters other than none: a commit line is one
// visible line, so tab and newline are as invalid as ESC.
func hasControl(value string) bool {
	for _, r := range value {
		if r == utf8.RuneError && !utf8.ValidString(value) {
			return true
		}
		if unicode.IsControl(r) {
			return true
		}
	}
	return false
}

// invalid wraps the sentinel so callers can classify without matching text.
func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidProposal, fmt.Sprintf(format, args...))
}
