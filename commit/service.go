package commit

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/PizenLabs/ohgo/ai"
	"github.com/PizenLabs/ohgo/prompt"
	"github.com/PizenLabs/ohgo/vcs"
)

// DefaultProposalTimeout bounds one COMMIT proposal request.
//
// The timeout is a completion-time budget, not a token budget: a reasoning
// model may spend a long time producing hidden tokens before the first visible
// delta, and a queued free endpoint can add provider-side waiting on top of
// that. The old two-minute deadline was shorter than that legitimate window and
// surfaced as "the COMMIT model request timed out" for requests that were still
// generating.
//
// Five minutes keeps a bounded interactive wait while leaving room for 8192
// reasoning tokens and provider queueing. It is deliberately below the
// transport's ten-minute HTTP ceiling, so a genuinely stuck connection is
// reported as a COMMIT timeout instead of being killed by the HTTP client. The
// user can cancel at any time; the deadline only bounds how long a request may
// run unattended.
const DefaultProposalTimeout = 5 * time.Minute

// Request causes. They are attached to the proposal context so a failure can
// say why it stopped instead of collapsing user cancellation, session shutdown,
// and a real deadline into one generic timeout.
var (
	// ErrProposalTimeout is the cause recorded when the bounded COMMIT
	// deadline expires.
	ErrProposalTimeout = errors.New("commit: proposal request deadline expired")
	// ErrCancelledByUser is the cause recorded when the human cancels /commit.
	ErrCancelledByUser = errors.New("commit: proposal cancelled by user")
	// ErrSessionClosed is the cause recorded when the session is left or quit.
	ErrSessionClosed = errors.New("commit: session closed")
	// ErrSuperseded is the cause recorded when a newer /commit replaces an
	// in-flight proposal.
	ErrSuperseded = errors.New("commit: proposal superseded")
)

// Errors that describe a refused or failed execution.
var (
	// ErrStateChanged means HEAD, the index, or the worktree changed after the
	// proposal. The commit is aborted; the change is never absorbed.
	ErrStateChanged = errors.New("commit: repository state changed after proposal")
	// ErrStagingMismatch means the index does not hold exactly the approved
	// paths and content.
	ErrStagingMismatch = errors.New("commit: staged content does not match the approved scope")
	// ErrCommitVerification means Git reported success but the resulting
	// commit does not match the approved message or parent.
	ErrCommitVerification = errors.New("commit: committed result failed verification")
)

// Errors that classify a failed proposal request. They exist so a caller can
// tell a model failure from a UI failure without matching error strings; the
// user-facing text is normalized separately by AbortText.
var (
	// ErrModel means the transport or provider failed before returning text.
	ErrModel = errors.New("commit: commit model request failed")
	// ErrModelEmpty means the model finished without any visible text.
	ErrModelEmpty = errors.New("commit: commit model returned an empty response")
	// ErrModelTruncated means the output budget was exhausted before any
	// visible text arrived. A reasoning model can spend the whole budget on
	// hidden tokens, which is why this is distinct from a genuinely empty
	// reply.
	ErrModelTruncated = errors.New("commit: commit model output was truncated")
	// ErrModelTimeout means the bounded COMMIT request deadline expired.
	ErrModelTimeout = errors.New("commit: commit model request timed out")
	// ErrModelProviderTimeout means the provider or transport timed out while
	// the COMMIT deadline was still in the future. It is a timeout subtype so
	// callers that only care that a deadline expired still match, but the
	// diagnostics and the user-facing line name the provider as the source.
	ErrModelProviderTimeout = fmt.Errorf("%w: the provider timed out", ErrModelTimeout)
	// ErrModelCancelled means the caller cancelled the request.
	ErrModelCancelled = errors.New("commit: commit model request was cancelled")
)

// ProposalDiagnostics records the observable facts of one COMMIT request. It
// is carried by ProposalError and handed to Service.OnDiagnostics, so a timeout
// can be attributed without parsing rendered text.
type ProposalDiagnostics struct {
	// Provider is the resolved provider key, e.g. "openrouter".
	Provider string
	// Model is the resolved model id.
	Model string
	// MaxOutputTokens is the completion budget sent to the provider.
	MaxOutputTokens int
	// PromptBytes is the byte length of the system prompt plus the user
	// evidence message on the final attempt.
	PromptBytes int
	// EvidenceBytes is the byte length of the bounded repository evidence.
	EvidenceBytes int
	// InputTokens, OutputTokens, and ReasoningTokens are provider-reported
	// totals across every attempt in this request. ReasoningTokens stays zero
	// when the provider does not report it separately.
	InputTokens     int
	OutputTokens    int
	ReasoningTokens int
	// Reasoning reports whether an effort was sent. ReasoningEffort is the
	// value sent, including "none" when the provider has an explicit disable.
	Reasoning       bool
	ReasoningEffort string
	// Regenerations is the number of validation-triggered retries.
	Regenerations int
	// RetryReasons records the deterministic rule that rejected each attempt,
	// in order. Rejected model text is never retained.
	RetryReasons []string
	// ProposalValid reports that a validated proposal was produced.
	ProposalValid bool
	// StartedAt is when the proposal request began.
	StartedAt time.Time
	// EndedAt is when it finished, failed, or was cancelled.
	EndedAt time.Time
	// Timeout is the configured request budget.
	Timeout time.Duration
	// Deadline is the effective deadline the request ran under.
	Deadline time.Time
	// ContextDeadline is the caller context's own deadline, if it had one.
	// It is recorded separately because an inherited deadline must never be
	// mistaken for the COMMIT policy's own deadline.
	ContextDeadline time.Time
	// Elapsed is EndedAt minus StartedAt.
	Elapsed time.Duration
	// Attempts is the number of model calls made in this request.
	Attempts int
	// CancellationReason names why the request stopped when it did:
	// "timeout", "user", "session", "superseded", or "cancelled".
	CancellationReason string
	// ProviderStatus is the HTTP status when the provider returned one.
	ProviderStatus int
}

// ProposalError classifies one failed proposal request and preserves the
// provider's original cause alongside the diagnostics.
type ProposalError struct {
	// Kind is one of the ErrModel* sentinels.
	Kind error
	// Message is the internal classification sentence.
	Message string
	// Cause is the transport or validation error that triggered it.
	Cause error
	// Diagnostics describes the request that failed.
	Diagnostics ProposalDiagnostics
}

func (e *ProposalError) Error() string {
	if e == nil {
		return ""
	}
	if e.Cause != nil {
		return e.Message + ": " + e.Cause.Error()
	}
	return e.Message
}

// Unwrap exposes both the classification and the original cause to errors.Is
// and errors.As.
func (e *ProposalError) Unwrap() []error {
	if e == nil {
		return nil
	}
	out := make([]error, 0, 2)
	if e.Kind != nil {
		out = append(out, e.Kind)
	}
	if e.Cause != nil {
		out = append(out, e.Cause)
	}
	return out
}

// DiagnosticsOf returns the diagnostics attached to a proposal failure.
func DiagnosticsOf(err error) (ProposalDiagnostics, bool) {
	var proposalErr *ProposalError
	if errors.As(err, &proposalErr) {
		return proposalErr.Diagnostics, true
	}
	return ProposalDiagnostics{}, false
}

// DiagnosticText renders the internal facts in one line. It is for logs and
// tests, never for the rendered transcript.
func DiagnosticText(d ProposalDiagnostics) string {
	return fmt.Sprintf("commit request provider=%s model=%s max_output_tokens=%d configured_timeout=%s started_at=%s elapsed=%s deadline=%s context_deadline=%s attempts=%d regenerations=%d cancellation=%s status=%d prompt_bytes=%d evidence_bytes=%d input_tokens=%d output_tokens=%d reasoning_tokens=%d reasoning=%t effort=%s valid=%t",
		d.Provider, d.Model, d.MaxOutputTokens, d.Timeout,
		d.StartedAt.Format(time.RFC3339Nano), d.Elapsed.Round(time.Millisecond),
		formatDeadline(d.Deadline), formatDeadline(d.ContextDeadline),
		d.Attempts, d.Regenerations, d.CancellationReason, d.ProviderStatus,
		d.PromptBytes, d.EvidenceBytes, d.InputTokens, d.OutputTokens,
		d.ReasoningTokens, d.Reasoning, d.ReasoningEffort, d.ProposalValid)
}

func formatDeadline(t time.Time) string {
	if t.IsZero() {
		return "none"
	}
	return t.Format(time.RFC3339Nano)
}

// Request is one reviewable proposal plus the frozen state it came from.
type Request struct {
	// Proposal is the validated commit text.
	Proposal Proposal
	// Snapshot is the exact scope and concurrency guard.
	Snapshot *Snapshot
}

// Result is the verified outcome of an approved commit.
type Result struct {
	// SHA is the commit Git created.
	SHA string
	// Tree is the exact tree the verified index held.
	Tree string
	// Subject is the approved first line.
	Subject string
	// Message is the exact approved message.
	Message string
	// Files is the scope that was committed.
	Files []FileChange
	// Remaining lists non-internal changes still present afterwards.
	Remaining []FileChange
	// Clean reports that `git status` proved no relevant change remains.
	Clean bool
}

// Service proposes and executes commits in one repository.
//
// Stream is the shared provider transport, and Model is resolved from the
// dedicated COMMIT role by the caller. Neither a tool set nor an agent loop is
// involved: the model can only return text.
type Service struct {
	// Root is the repository working directory.
	Root string
	// Stream opens one completion.
	Stream ai.StreamFn
	// Model is the COMMIT-role model.
	Model ai.Model
	// Effort is the role's thinking effort ("", "off", or a level).
	Effort string
	// MaxTokens bounds the proposal completion.
	MaxTokens int
	// Timeout bounds one proposal request. Zero uses DefaultProposalTimeout.
	Timeout time.Duration
	// OnDiagnostics receives the observable facts of every proposal request,
	// including successful ones. It runs on the request goroutine and must
	// not block the UI.
	OnDiagnostics func(ProposalDiagnostics)
	// MaxAttempts bounds validation retries. Zero uses three.
	MaxAttempts int
	// OnStaged runs after staging and before verification. It exists so a
	// test can inject the race the verifier must refuse; production always
	// leaves it nil.
	OnStaged func()
}

// DefaultProposalTokens is the dedicated COMMIT completion budget.
//
// A complete message is roughly 100 tokens, so this cap is a guardrail rather
// than a target. It is deliberately far below the coding-turn ceiling: COMMIT
// proposes text and does no reasoning over the repository. A model whose
// reasoning cannot be disabled may still spend hidden tokens here; that case is
// reported as ErrModelTruncated rather than accepted as a partial message. The
// value is clamped to the model's own ceiling where the catalog knows it.
const DefaultProposalTokens = 1024

// defaultAttempts is the bounded regeneration policy for invalid output.
const defaultAttempts = 3

// Propose inspects the repository and asks the commit model for one validated
// proposal. It is read-only.
//
// The caller's context owns lifecycle (user cancellation, session shutdown).
// The bounded COMMIT deadline applies to the model request itself, so a slow
// local Git inspection is never misreported as a model timeout and the policy
// is not duplicated in every host.
func (s *Service) Propose(ctx context.Context) (request *Request, err error) {
	started := time.Now()
	timeout := s.Timeout
	if timeout <= 0 {
		timeout = DefaultProposalTimeout
	}
	diag := ProposalDiagnostics{
		Provider:        s.Model.Provider,
		Model:           s.Model.ID,
		MaxOutputTokens: proposalBudget(s.MaxTokens, s.Model),
		StartedAt:       started,
		Timeout:         timeout,
		ContextDeadline: deadlineOf(ctx),
		ProviderStatus:  0,
	}

	defer func() {
		diag.EndedAt = time.Now()
		diag.Elapsed = diag.EndedAt.Sub(started)
		// Attach the completed timeline to the classified failure. The error
		// is built while the request is failing, before elapsed time exists.
		var proposalErr *ProposalError
		if errors.As(err, &proposalErr) {
			proposalErr.Diagnostics = diag
		}
		if s.OnDiagnostics != nil {
			s.OnDiagnostics(diag)
		}
	}()

	repo, err := vcs.Discover(s.Root)
	if err != nil {
		return nil, err
	}
	if repo == nil {
		return nil, fmt.Errorf("commit: %s is not inside a Git repository", s.Root)
	}
	snapshot, err := BuildSnapshot(ctx, repo)
	if err != nil {
		return nil, err
	}
	diag.EvidenceBytes = len(snapshot.Evidence())

	requestCtx, cancel := context.WithTimeoutCause(ctx, timeout, ErrProposalTimeout)
	defer cancel()
	diag.Deadline = deadlineOf(requestCtx)

	proposal, err := s.proposeText(requestCtx, snapshot, &diag)
	if err != nil {
		return nil, err
	}
	return &Request{Proposal: proposal, Snapshot: snapshot}, nil
}

// proposeText runs the bounded generation/validation loop.
func (s *Service) proposeText(ctx context.Context, snapshot *Snapshot, diag *ProposalDiagnostics) (Proposal, error) {
	if s.Stream == nil {
		return Proposal{}, fmt.Errorf("commit: the commit model transport is unavailable")
	}
	if s.Model.ID == "" || s.Model.Provider == "" {
		return Proposal{}, fmt.Errorf("commit: the commit model is not configured")
	}

	attempts := s.MaxAttempts
	if attempts <= 0 {
		attempts = defaultAttempts
	}
	enabled, level := reasoning(s.Effort)
	maxTokens := proposalBudget(s.MaxTokens, s.Model)
	diag.Reasoning = enabled
	diag.ReasoningEffort = level

	evidence := snapshot.Evidence()
	fullText := prompt.CommitEvidence(evidence)
	systemText := prompt.Commit
	userText := fullText
	// shortening means the next request fixes only an overlong subject. The
	// body is already the message the human will review, so it is reattached
	// unchanged and never regenerated by the focused correction.
	shortening := false
	shortBody := ""

	var lastErr error
	lastCandidate := ""
	for attempt := 0; attempt < attempts; attempt++ {
		diag.Attempts++
		diag.PromptBytes = len(systemText) + len(userText)
		stream, err := s.Stream(ctx, ai.Request{
			Model: s.Model,
			System: []ai.SystemPromptBlock{
				{Text: systemText, Cache: true},
			},
			Messages: []ai.Message{ai.UserMessage{Content: []ai.ContentBlock{
				ai.TextContent{Text: userText},
			}}},
			MaxTokens:       maxTokens,
			Reasoning:       enabled,
			ReasoningEffort: level,
		})
		if err != nil {
			return Proposal{}, modelError(ctx, err, diag)
		}
		message, err := ai.DrainStream(ctx, stream, nil)
		if err != nil {
			return Proposal{}, modelError(ctx, err, diag)
		}
		diag.InputTokens += message.Usage.Input
		diag.OutputTokens += message.Usage.Output
		diag.ReasoningTokens += message.Usage.Reasoning
		diag.Regenerations = diag.Attempts - 1

		// A budget-exhausted reply is incomplete by definition, even when it
		// happens to parse. Accepting it would let a small cap silently shorten
		// the message, so it is a classified failure, never a retry.
		if message.StopReason == ai.StopMaxTokens {
			return Proposal{}, truncatedResponse(message, maxTokens, diag)
		}
		raw := message.Text()
		if strings.TrimSpace(raw) == "" {
			// Empty visible content is not an invalid message the model can
			// correct: there is nothing to correct. Re-asking would only hide
			// the real cause, so the classified failure is returned at once.
			return Proposal{}, emptyResponse(message, diag)
		}
		candidate := raw
		if shortening {
			subject, _ := splitSubjectBody(normalize(raw))
			candidate = subject + shortBody
		}
		lastCandidate = candidate
		proposal, err := Parse(candidate, snapshot.Refs)
		if err == nil {
			diag.ProposalValid = true
			return proposal, nil
		}
		lastErr = err
		diag.RetryReasons = append(diag.RetryReasons, invalidCause(err))

		if subject, body, overlong := overlongSubject(candidate, snapshot.Refs); overlong {
			if !shortening {
				shortBody = body
			}
			shortening = true
			systemText = prompt.CommitShorten
			userText = prompt.CommitShortenSubject(subject)
			continue
		}
		// Another rule failed (missing scope, invented reference, a bad
		// body). That needs the evidence again, not a subject rewrite.
		shortening = false
		systemText = prompt.Commit
		userText = fullText + "\n\n" + prompt.CommitRetry(invalidCause(err), raw)
	}
	// A small model can fix every rule except the scope or the total subject
	// length. Rather than fail an otherwise valid proposal, fill a missing
	// scope from the approved paths and fit an overlong summary into the
	// budget. This is deterministic, preserves the reviewed body, and still
	// goes through Parse so no other rule is bypassed.
	if proposal, ok := repairProposal(lastCandidate, snapshot.Refs, snapshot.SuggestedScope()); ok {
		diag.ProposalValid = true
		return proposal, nil
	}
	// Keep the sentinel and the final reason in one chain: wrapping the
	// already-sentinel-wrapped Parse error would print "commit: invalid
	// proposal: commit: invalid proposal: ..." to the user.
	return Proposal{}, fmt.Errorf("%w: %s", ErrInvalidProposal, invalidCause(lastErr))
}

// splitSubjectBody splits an untrusted message at the first line while keeping
// the original separator. The returned body starts with the newline it followed,
// so reattaching a rewritten subject preserves the reviewed body byte for byte.
func splitSubjectBody(text string) (subject, body string) {
	first, rest, found := strings.Cut(text, "\n")
	if !found {
		return first, ""
	}
	return first, "\n" + rest
}

// overlongSubject reports the precise case a focused rewrite can fix. A
// malformed type or scope is deliberately excluded: shortening cannot repair
// it, and the full evidence is the better second attempt.
//
// The body must already be valid. Parse validates the subject first, so an
// overlong subject can hide an invalid bullet; preserving that body while
// asking the model to rewrite only the subject would make the next proposal
// fail for a reason the model was never told about.
func overlongSubject(text string, refs map[string]bool) (subject, body string, ok bool) {
	subject, body = splitSubjectBody(normalize(text))
	match := subjectPattern.FindStringSubmatch(subject)
	if match == nil || !scopePattern.MatchString(match[2]) {
		return "", "", false
	}
	if utf8.RuneCountInString(subject) <= MaxSubjectRunes {
		return "", "", false
	}
	if _, err := Parse("fix(scope): fit"+body, refs); err != nil {
		return "", "", false
	}
	return subject, body, true
}

// proposalBudget resolves the completion budget for one proposal.
//
// A caller-supplied positive value is honoured, but the default is the
// COMMIT-specific small budget. The model's own ceiling wins when known,
// because a provider rejects a request above it outright.
func proposalBudget(configured int, model ai.Model) int {
	budget := configured
	if budget <= 0 {
		budget = DefaultProposalTokens
	}
	if model.MaxOutputTokens > 0 && budget > model.MaxOutputTokens {
		budget = model.MaxOutputTokens
	}
	if budget <= 0 {
		budget = DefaultProposalTokens
	}
	return budget
}

// modelError classifies a transport failure without losing the provider's
// original message. Classification reads the request context first: an error
// that merely mentions a deadline is not the same event as the COMMIT budget
// expiring, and a provider stall must not be reported as an interactive timeout.
func modelError(ctx context.Context, err error, diag *ProposalDiagnostics) error {
	if diag != nil {
		diag.ProviderStatus = providerStatusCode(err)
	}
	cause := context.Cause(ctx)
	switch {
	case errors.Is(cause, ErrCancelledByUser):
		return newProposalError(ErrModelCancelled, err, diag, "user")
	case errors.Is(cause, ErrSessionClosed):
		return newProposalError(ErrModelCancelled, err, diag, "session")
	case errors.Is(cause, ErrSuperseded):
		return newProposalError(ErrModelCancelled, err, diag, "superseded")
	case errors.Is(cause, ErrProposalTimeout):
		return newProposalError(ErrModelTimeout, err, diag, "timeout")
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		// The caller supplied a shorter deadline than the COMMIT policy. It
		// is still a timeout, but the diagnostics name the layer that expired.
		return newProposalError(ErrModelTimeout, err, diag, "context-deadline")
	case errors.Is(ctx.Err(), context.Canceled):
		return newProposalError(ErrModelCancelled, err, diag, "cancelled")
	case errors.Is(err, context.Canceled):
		// The transport reported a cancellation while the request context was
		// still alive; it is still a cancellation, never a timeout.
		return newProposalError(ErrModelCancelled, err, diag, "cancelled")
	case errors.Is(err, context.DeadlineExceeded):
		// A deadline surfaced by the transport while the COMMIT context is
		// still alive comes from the provider or its HTTP client. It remains
		// a timeout, but the diagnostics name the provider as the source.
		return newProposalError(ErrModelTimeout, err, diag, "provider-timeout")
	case isTimeout(err):
		return newProposalError(ErrModelProviderTimeout, err, diag, "provider-timeout")
	default:
		return newProposalError(ErrModel, err, diag, "")
	}
}

// newProposalError builds the classified failure and records why it stopped.
func newProposalError(kind error, cause error, diag *ProposalDiagnostics, reason string) error {
	if diag != nil {
		diag.CancellationReason = reason
	}
	out := &ProposalError{Kind: kind, Message: kind.Error(), Cause: cause}
	if diag != nil {
		out.Diagnostics = *diag
	}
	return out
}

// isTimeout reports a transport-level timeout. It is checked only after the
// request context is known to be alive, so a real COMMIT deadline is never
// mistaken for a provider stall.
func isTimeout(err error) bool {
	var timeout interface{ Timeout() bool }
	return errors.As(err, &timeout) && timeout.Timeout()
}

// providerStatusCode returns the HTTP status carried by a provider error, when
// the transport exposes one.
func providerStatusCode(err error) int {
	var status interface{ StatusCode() int }
	if errors.As(err, &status) {
		return status.StatusCode()
	}
	return 0
}

// truncatedResponse classifies a budget-exhausted reply. The visible text, if
// any, is discarded: a partial commit message is not a proposal.
func truncatedResponse(message ai.AssistantMessage, budget int, diag *ProposalDiagnostics) error {
	detail := "before any message text"
	if strings.TrimSpace(message.Text()) != "" {
		detail = "after a partial message"
	}
	return newProposalError(ErrModelTruncated,
		fmt.Errorf("the %d-token budget was exhausted %s (stop reason %q)",
			budget, detail, message.StopReason), diag, "")
}

// emptyResponse classifies a reply that ended naturally without visible text.
func emptyResponse(message ai.AssistantMessage, diag *ProposalDiagnostics) error {
	return newProposalError(ErrModelEmpty,
		fmt.Errorf("stop reason %q", message.StopReason), diag, "")
}

// deadlineOf returns a context's deadline, or the zero time when it has none.
func deadlineOf(ctx context.Context) time.Time {
	if deadline, ok := ctx.Deadline(); ok {
		return deadline
	}
	return time.Time{}
}

// invalidCause removes the sentinel prefix from a validation error so it can be
// re-wrapped exactly once.
func invalidCause(err error) string {
	if err == nil {
		return "the proposal was empty"
	}
	text := err.Error()
	prefix := ErrInvalidProposal.Error() + ": "
	return strings.TrimPrefix(text, prefix)
}

// Execute commits exactly the approved request, at most once.
func (s *Service) Execute(ctx context.Context, request *Request) (Result, error) {
	if request == nil || request.Snapshot == nil {
		return Result{}, fmt.Errorf("commit: no approved proposal")
	}
	// Revalidate the exact bytes that will reach Git. Approval cannot make an
	// invalid or edited message valid.
	if err := request.Proposal.Validate(request.Snapshot.Refs); err != nil {
		return Result{}, err
	}

	root := request.Snapshot.Root
	if strings.TrimSpace(root) == "" {
		root = s.Root
	}
	repo, err := vcs.Discover(root)
	if err != nil {
		return Result{}, err
	}
	if repo == nil {
		return Result{}, fmt.Errorf("commit: %s is not inside a Git repository", root)
	}

	// The first guard: nothing may have moved since the human was shown the
	// proposal. This catches both content edits and index changes.
	fresh, err := BuildSnapshot(ctx, repo)
	if err != nil {
		return Result{}, executionStateError(err)
	}
	if fresh.Fingerprint != request.Snapshot.Fingerprint {
		return Result{}, ErrStateChanged
	}

	if request.Snapshot.Mode == ModeWorktree {
		if err := repo.StagePaths(ctx, request.Snapshot.StagePaths()); err != nil {
			return Result{}, fmt.Errorf("commit: stage approved paths: %w", err)
		}
		// Staging moved index codes, so compare the worktree form: content,
		// paths, and HEAD must still be what the human approved.
		stagedState, err := BuildSnapshot(ctx, repo)
		if err != nil {
			return Result{}, executionStateError(err)
		}
		if stagedState.WorktreeFingerprint != request.Snapshot.WorktreeFingerprint {
			return Result{}, ErrStateChanged
		}
	} else {
		// The index is the approved scope, so it must still match exactly.
		current, err := BuildSnapshot(ctx, repo)
		if err != nil {
			return Result{}, executionStateError(err)
		}
		if current.Fingerprint != request.Snapshot.Fingerprint {
			return Result{}, ErrStateChanged
		}
	}

	if s.OnStaged != nil {
		s.OnStaged()
	}

	if err := verifyStaged(ctx, repo, request.Snapshot); err != nil {
		return Result{}, err
	}

	head, err := repo.HeadSHA(ctx)
	if err != nil {
		return Result{}, err
	}
	if head != request.Snapshot.Head {
		return Result{}, ErrStateChanged
	}

	// Pin the verified index as a tree before committing. If the index moves
	// in the last window, the commit's tree will not match and verification
	// refuses to call it the approved commit.
	indexTree, err := repo.IndexTree(ctx)
	if err != nil {
		return Result{}, err
	}
	// Re-check after writing the tree: if the index moved between the first
	// verification and write-tree, the tree is not the approved content.
	if err := verifyStaged(ctx, repo, request.Snapshot); err != nil {
		return Result{}, err
	}

	sha, err := repo.CommitMessage(ctx, request.Proposal.Message())
	if err != nil {
		// The real Git failure is reported as-is. No second commit, amend,
		// or cleanup commit is attempted.
		return Result{}, fmt.Errorf("commit: %w", err)
	}

	result := Result{
		SHA:     sha,
		Tree:    indexTree,
		Subject: request.Proposal.Subject(),
		Message: request.Proposal.Message(),
		Files:   append([]FileChange(nil), request.Snapshot.Scope...),
	}
	if err := verifyCommit(ctx, repo, request, sha, indexTree); err != nil {
		// The commit exists; returning its SHA keeps the failure truthful
		// instead of pretending nothing happened.
		return result, err
	}
	result.Remaining, _ = remainingChanges(ctx, repo)
	result.Clean = len(result.Remaining) == 0
	return result, nil
}

// executionStateError maps a vanished scope during execution to the explicit
// state-change refusal. "No changes" is correct before a proposal, but after
// approval it means the repository moved away from what was approved.
func executionStateError(err error) error {
	if errors.Is(err, ErrNoChanges) {
		return ErrStateChanged
	}
	return err
}

// verifyStaged proves the index holds exactly the approved paths and blobs.
func verifyStaged(ctx context.Context, repo *vcs.RepoInfo, snapshot *Snapshot) error {
	expectedPaths := snapshot.ExpectedStagedPaths()
	staged, err := repo.StagedPaths(ctx)
	if err != nil {
		return err
	}
	if !sameStrings(staged, expectedPaths) {
		return fmt.Errorf("%w: approved %s; staged %s",
			ErrStagingMismatch, displayList(expectedPaths), displayList(staged))
	}
	hashes, err := repo.IndexHashes(ctx, expectedPaths)
	if err != nil {
		return err
	}
	for _, path := range expectedPaths {
		if hashes[path] != snapshot.Expected[path] {
			return fmt.Errorf("%w: %q", ErrStagingMismatch, path)
		}
	}
	return nil
}

// verifyCommit proves the new commit is exactly the approved one on top of the
// previously approved HEAD.
func verifyCommit(ctx context.Context, repo *vcs.RepoInfo, request *Request, sha, expectedTree string) error {
	head, err := repo.HeadSHA(ctx)
	if err != nil {
		return err
	}
	if head != sha {
		return fmt.Errorf("%w: HEAD is %q, commit returned %q", ErrCommitVerification, head, sha)
	}
	tree, err := repo.CommitTree(ctx, sha)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrCommitVerification, err)
	}
	if expectedTree != "" && tree != expectedTree {
		return fmt.Errorf("%w: tree %q is not the verified index tree %q",
			ErrCommitVerification, tree, expectedTree)
	}
	details, err := repo.CommitDetails(ctx, sha)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrCommitVerification, err)
	}
	if strings.TrimRight(details.Message, "\n") != request.Proposal.Message() {
		return fmt.Errorf("%w: message differs from the approved text", ErrCommitVerification)
	}
	expectedParents := []string{}
	if request.Snapshot.Head != "" {
		expectedParents = append(expectedParents, request.Snapshot.Head)
	}
	if !sameStrings(details.Parents, expectedParents) {
		return fmt.Errorf("%w: parents %s are not exactly the previous HEAD",
			ErrCommitVerification, displayList(details.Parents))
	}
	return nil
}

// remainingChanges lists what `git status` still reports after the commit.
// Ignored files are not changes by git's own definition; internal .ohgo state
// is, so it is listed even though /commit never includes it. Clean is therefore
// a claim git status can prove, not a claim that the scope is exhausted.
func remainingChanges(ctx context.Context, repo *vcs.RepoInfo) ([]FileChange, error) {
	entries, err := repo.StatusEntries(ctx, vcs.StatusOptions{Untracked: "all"})
	if err != nil {
		return nil, err
	}
	var remaining []FileChange
	for _, entry := range entries {
		change := classify(entry)
		if change.Ignored {
			continue
		}
		remaining = append(remaining, change)
	}
	return remaining, nil
}

// sameStrings compares two string sets independent of order and duplicates.
func sameStrings(a, b []string) bool {
	left := uniqueSorted(a)
	right := uniqueSorted(b)
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

// uniqueSorted returns the distinct values in stable order.
func uniqueSorted(values []string) []string {
	seen := make(map[string]bool, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		if seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

// displayList renders a path list for an error, bounded so a huge change set
// does not produce an unreadable message.
func displayList(values []string) string {
	const shown = 8
	if len(values) == 0 {
		return "(none)"
	}
	if len(values) <= shown {
		return strings.Join(values, ", ")
	}
	return fmt.Sprintf("%s, … (%d more)", strings.Join(values[:shown], ", "), len(values)-shown)
}

// reasoning maps a role effort to the provider's thinking fields.
//
// "none" is a provider-native disable value, not a level: it must be sent so a
// model that reasons by default stops doing so. Empty and "off" omit the field
// entirely, which is the disable for providers that only reason when asked.
func reasoning(effort string) (bool, string) {
	switch strings.TrimSpace(effort) {
	case "", "off":
		return false, ""
	default:
		return true, strings.TrimSpace(effort)
	}
}

// AbortText maps a classified error to the short, non-nested line a UI should
// show. The original cause remains available to logs and tests through the
// wrapped error; the human sees one sentence that names the layer that failed.
func AbortText(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, ErrModelEmpty):
		return "Commit proposal failed:\nthe COMMIT model returned an empty response."
	case errors.Is(err, ErrModelTruncated):
		return "Commit proposal failed:\nthe COMMIT model ran out of output budget."
	case errors.Is(err, ErrModelProviderTimeout):
		return "Commit proposal failed:\nthe provider timed out before returning a proposal."
	case errors.Is(err, ErrModelTimeout):
		return timeoutText(err)
	case errors.Is(err, ErrModelCancelled):
		return "Commit proposal cancelled."
	case errors.Is(err, ErrModel):
		return "Commit proposal failed:\n" + trimSentinel(err, ErrModel)
	case errors.Is(err, ErrInvalidProposal):
		return invalidProposalText(err)
	case errors.Is(err, ErrStateChanged):
		return "Commit aborted: repository state changed after proposal."
	case errors.Is(err, ErrNoChanges):
		return "No changes to commit."
	case errors.Is(err, ErrStagedExcluded), errors.Is(err, ErrTooManyChanges):
		return err.Error()
	default:
		return err.Error()
	}
}

// timeoutText names the elapsed wait when the failure carries diagnostics. A
// bare timeout still gets a truthful sentence, so an older or synthetic error
// never renders an empty duration.
func timeoutText(err error) string {
	diag, ok := DiagnosticsOf(err)
	if !ok || diag.Elapsed < time.Second {
		return "Commit proposal failed:\nthe COMMIT model request timed out."
	}
	return fmt.Sprintf("Commit proposal failed:\nthe COMMIT model request timed out after %s.", formatElapsed(diag.Elapsed))
}

// formatElapsed renders a user-facing wait. Sub-second waits are described
// rather than rounded down to a misleading "0s".
func formatElapsed(elapsed time.Duration) string {
	if elapsed < time.Second {
		return "less than 1s"
	}
	return elapsed.Round(time.Second).String()
}

// invalidProposalText names the layer and the final validation rule. Without
// the rule the sentence is the same for every malformed reply, which makes a
// model that keeps missing one rule impossible to diagnose from the transcript.
// The reason is one bounded line: it can quote model text but must not turn the
// notice into a paste of the whole reply.
func invalidProposalText(err error) string {
	const limit = 180
	reason := strings.Join(strings.Fields(ValidationText(err)), " ")
	if reason == ErrInvalidProposal.Error() {
		reason = ""
	}
	if n := utf8.RuneCountInString(reason); n > limit {
		reason = string([]rune(reason)[:limit]) + "…"
	}
	const sentence = "Commit proposal failed.\nThe COMMIT model did not produce a valid proposal."
	if reason == "" {
		return sentence
	}
	return sentence + "\n" + reason
}

// trimSentinel removes "sentinel: " from the front of an error message, keeping
// the underlying provider text without a second layer of commit vocabulary.
func trimSentinel(err error, sentinel error) string {
	text := err.Error()
	return strings.TrimPrefix(text, sentinel.Error()+": ")
}

// FormatResult renders one verified commit for a transcript notice.
func FormatResult(result Result) string {
	var sb strings.Builder
	short := result.SHA
	if len(short) > 7 {
		short = short[:7]
	}
	fmt.Fprintf(&sb, "Committed %s %s\n%d %s changed",
		short, result.Subject, len(result.Files), plural("file", len(result.Files)))
	if result.Clean {
		sb.WriteString("\nWorking tree is clean.")
		return sb.String()
	}
	if len(result.Remaining) > 0 {
		sb.WriteString("\nChanges remain outside this commit:")
		for _, change := range result.Remaining {
			sb.WriteString("\n  " + change.Display())
		}
	}
	return sb.String()
}

// FormatProposal renders the review block shown before approval.
func FormatProposal(request *Request) string {
	if request == nil || request.Snapshot == nil {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("Commit proposal\n\n")
	sb.WriteString(request.Proposal.Message())
	sb.WriteString("\n\nFiles:")
	for _, change := range request.Snapshot.Scope {
		sb.WriteString("\n  " + change.Display())
	}
	if note := ScopeNote(request.Snapshot); note != "" {
		sb.WriteString("\n\n" + note)
	}
	return sb.String()
}

// ScopeNote explains what the scope does and does not include, without hiding
// the decision in prose the reviewer cannot verify.
func ScopeNote(snapshot *Snapshot) string {
	if snapshot == nil {
		return ""
	}
	var notes []string
	switch snapshot.Mode {
	case ModeStaged:
		notes = append(notes, "Only the staged changes will be committed.")
		if len(snapshot.Unstaged) > 0 || len(snapshot.Untracked) > 0 {
			notes = append(notes, "Unstaged and untracked changes are not included.")
		}
	default:
		notes = append(notes, "Tracked and untracked changes will be staged explicitly.")
	}
	if len(snapshot.Internal) > 0 {
		notes = append(notes, ".ohgo/ internal state is excluded.")
	}
	if len(snapshot.Ignored) > 0 {
		notes = append(notes, "Ignored files are excluded.")
	}
	return strings.Join(notes, " ")
}

// plural picks the singular or plural noun.
func plural(noun string, count int) string {
	if count == 1 {
		return noun
	}
	return noun + "s"
}
