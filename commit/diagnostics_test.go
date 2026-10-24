package commit

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/PizenLabs/ohgo/ai"
)

// TestReasoningOnlyReplyIsATruncatedModelFailure pins the real observed
// failure: a reasoning model spent the whole completion budget on hidden
// tokens, so the visible content was empty and the old code called it an
// invalid proposal. The failure must be classified as a truncation, not
// retried as if the model could correct it, and never executed.
func TestReasoningOnlyReplyIsATruncatedModelFailure(t *testing.T) {
	repo := newTestRepo(t)
	initialCommit(t, repo, "a.txt", "one\n")
	writeTestFile(t, repo, "a.txt", "two\n")

	var calls atomic.Int32
	service := &Service{
		Root:  repo,
		Model: ai.Model{Provider: "test", ID: "commit-model", API: "test"},
		Stream: func(context.Context, ai.Request) (*ai.Stream, error) {
			calls.Add(1)
			return ai.NewCompletedStream(ai.AssistantMessage{
				StopReason: ai.StopMaxTokens,
			}), nil
		},
	}
	_, err := service.Propose(context.Background())
	if !errors.Is(err, ErrModelTruncated) {
		t.Fatalf("Propose = %v, want ErrModelTruncated", err)
	}
	if errors.Is(err, ErrInvalidProposal) {
		t.Errorf("truncation was misclassified as an invalid proposal: %v", err)
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("truncation was retried %d times; an empty reply has nothing to correct", got)
	}
	if !strings.Contains(err.Error(), "budget") {
		t.Errorf("internal error %q does not preserve the budget diagnostic", err)
	}
	if text := AbortText(err); text != "Commit proposal failed:\nthe COMMIT model ran out of output budget." {
		t.Errorf("AbortText = %q", text)
	}
	if staged := strings.TrimSpace(runTestGit(t, repo, "diff", "--cached", "--name-only")); staged != "" {
		t.Errorf("a failed proposal staged files: %q", staged)
	}
}

// TestEmptyReplyIsClassifiedSeparately proves a genuinely empty reply is not
// folded into the invalid-proposal retry loop.
func TestEmptyReplyIsClassifiedSeparately(t *testing.T) {
	repo := newTestRepo(t)
	initialCommit(t, repo, "a.txt", "one\n")
	writeTestFile(t, repo, "a.txt", "two\n")

	var calls atomic.Int32
	service := &Service{
		Root:  repo,
		Model: ai.Model{Provider: "test", ID: "commit-model", API: "test"},
		Stream: func(context.Context, ai.Request) (*ai.Stream, error) {
			calls.Add(1)
			return ai.NewCompletedStream(ai.AssistantMessage{StopReason: ai.StopEndTurn}), nil
		},
	}
	_, err := service.Propose(context.Background())
	if !errors.Is(err, ErrModelEmpty) {
		t.Fatalf("Propose = %v, want ErrModelEmpty", err)
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("empty reply made %d model calls, want 1", got)
	}
	if text := AbortText(err); text != "Commit proposal failed:\nthe COMMIT model returned an empty response." {
		t.Errorf("AbortText = %q", text)
	}
}

// TestProviderFailureKeepsTheOriginalCause proves the provider text survives
// classification and is not replaced by a generic "invalid proposal" line.
func TestProviderFailureKeepsTheOriginalCause(t *testing.T) {
	repo := newTestRepo(t)
	initialCommit(t, repo, "a.txt", "one\n")
	writeTestFile(t, repo, "a.txt", "two\n")

	providerErr := errors.New("HTTP 401: credential rejected")
	service := &Service{
		Root:  repo,
		Model: ai.Model{Provider: "test", ID: "commit-model", API: "test"},
		Stream: func(context.Context, ai.Request) (*ai.Stream, error) {
			return nil, providerErr
		},
	}
	_, err := service.Propose(context.Background())
	if !errors.Is(err, ErrModel) {
		t.Fatalf("Propose = %v, want ErrModel", err)
	}
	if !errors.Is(err, providerErr) {
		t.Errorf("the original provider error was lost: %v", err)
	}
	text := AbortText(err)
	if !strings.Contains(text, "HTTP 401") {
		t.Errorf("AbortText = %q, want the provider cause", text)
	}
	if strings.Contains(text, "invalid proposal") {
		t.Errorf("AbortText = %q, misclassified as an invalid proposal", text)
	}
}

// TestCancelledAndTimedOutRequestsAreDistinguishable proves the caller can
// tell a human cancellation from a provider timeout without string matching.
func TestCancelledAndTimedOutRequestsAreDistinguishable(t *testing.T) {
	repo := newTestRepo(t)
	initialCommit(t, repo, "a.txt", "one\n")
	writeTestFile(t, repo, "a.txt", "two\n")

	for _, tc := range []struct {
		name string
		err  error
		want error
		text string
	}{
		{"cancelled", context.Canceled, ErrModelCancelled, "Commit proposal cancelled."},
		{"timeout", context.DeadlineExceeded, ErrModelTimeout, "Commit proposal failed:\nthe COMMIT model request timed out."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service := &Service{
				Root:  repo,
				Model: ai.Model{Provider: "test", ID: "commit-model", API: "test"},
				Stream: func(context.Context, ai.Request) (*ai.Stream, error) {
					return nil, tc.err
				},
			}
			_, err := service.Propose(context.Background())
			if !errors.Is(err, tc.want) {
				t.Fatalf("Propose = %v, want %v", err, tc.want)
			}
			if got := AbortText(err); got != tc.text {
				t.Errorf("AbortText = %q, want %q", got, tc.text)
			}
		})
	}
}

// TestInvalidProposalIsNotDoubleWrapped is the regression for the observed
// "commit: invalid proposal: commit: invalid proposal: ..." line.
func TestInvalidProposalIsNotDoubleWrapped(t *testing.T) {
	repo := newTestRepo(t)
	initialCommit(t, repo, "a.txt", "one\n")
	writeTestFile(t, repo, "a.txt", "two\n")

	service := testService(repo, "not a commit message")
	_, err := service.Propose(context.Background())
	if !errors.Is(err, ErrInvalidProposal) {
		t.Fatalf("Propose = %v, want ErrInvalidProposal", err)
	}
	if strings.Count(err.Error(), ErrInvalidProposal.Error()) != 1 {
		t.Errorf("error nests the sentinel: %q", err)
	}
	if got := AbortText(err); got != "Commit proposal failed.\nThe COMMIT model did not produce a valid proposal." {
		t.Errorf("AbortText = %q", got)
	}
	if reason := ValidationText(err); !strings.Contains(reason, "not `type(scope): message`") {
		t.Errorf("ValidationText = %q, want the concrete rule", reason)
	}
}

// TestProposalBudgetIsDedicatedAndClamped proves COMMIT owns a small budget
// that is far below a coding turn, and never exceeds a known model ceiling.
func TestProposalBudgetIsDedicatedAndClamped(t *testing.T) {
	if got := proposalBudget(0, ai.Model{}); got != DefaultProposalTokens {
		t.Errorf("default budget = %d, want %d", got, DefaultProposalTokens)
	}
	if got := proposalBudget(0, ai.Model{MaxOutputTokens: 256}); got != 256 {
		t.Errorf("budget = %d, want the model ceiling 256", got)
	}
	if got := proposalBudget(512, ai.Model{MaxOutputTokens: 4096}); got != 512 {
		t.Errorf("configured budget = %d, want 512", got)
	}
	if DefaultProposalTokens > 1024 {
		t.Errorf("default budget %d is not a small COMMIT budget", DefaultProposalTokens)
	}
	if DefaultProposalTokens >= 8192 {
		t.Errorf("default budget %d is not smaller than the coding-turn budget", DefaultProposalTokens)
	}
}

// TestTruncatedReplyDiscardsVisibleText is the budget-safety contract: a
// smaller cap must never turn a partial commit message into an accepted one,
// even when the partial text happens to satisfy the format.
func TestTruncatedReplyDiscardsVisibleText(t *testing.T) {
	repo := newTestRepo(t)
	initialCommit(t, repo, "a.txt", "one\n")
	writeTestFile(t, repo, "a.txt", "two\n")

	var calls atomic.Int32
	service := &Service{
		Root:  repo,
		Model: ai.Model{Provider: "test", ID: "commit-model", API: "test"},
		Stream: func(context.Context, ai.Request) (*ai.Stream, error) {
			calls.Add(1)
			return ai.NewCompletedStream(ai.AssistantMessage{
				Content:    []ai.ContentBlock{ai.TextContent{Text: "fix(scope): complete looking"}},
				StopReason: ai.StopMaxTokens,
			}), nil
		},
	}
	_, err := service.Propose(context.Background())
	if !errors.Is(err, ErrModelTruncated) {
		t.Fatalf("Propose = %v, want ErrModelTruncated", err)
	}
	if errors.Is(err, ErrInvalidProposal) {
		t.Errorf("truncation was misclassified as invalid: %v", err)
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("truncated reply made %d calls; it must not be retried", got)
	}
	if staged := strings.TrimSpace(runTestGit(t, repo, "diff", "--cached", "--name-only")); staged != "" {
		t.Errorf("a truncated proposal staged files: %q", staged)
	}
}

// TestProposalDiagnosticsRecordTheEfficiencyFacts proves the request is
// measurable without reading the TUI: budget, prompt/evidence size, provider
// usage, reasoning, regeneration count, and final validity.
func TestProposalDiagnosticsRecordTheEfficiencyFacts(t *testing.T) {
	repo := newTestRepo(t)
	initialCommit(t, repo, "a.txt", "one\n")
	writeTestFile(t, repo, "a.txt", "two\n")

	var calls atomic.Int32
	var diagnostics []ProposalDiagnostics
	service := &Service{
		Root:   repo,
		Model:  ai.Model{Provider: "test", ID: "commit-model", API: "test"},
		Effort: "none",
		OnDiagnostics: func(d ProposalDiagnostics) {
			diagnostics = append(diagnostics, d)
		},
		Stream: func(_ context.Context, req ai.Request) (*ai.Stream, error) {
			if req.MaxTokens != DefaultProposalTokens {
				t.Errorf("request budget = %d, want %d", req.MaxTokens, DefaultProposalTokens)
			}
			if !req.Reasoning || req.ReasoningEffort != "none" {
				t.Errorf("reasoning = %v/%q, want the explicit disable", req.Reasoning, req.ReasoningEffort)
			}
			if calls.Add(1) == 1 {
				return ai.NewCompletedStream(ai.AssistantMessage{
					Content: []ai.ContentBlock{ai.TextContent{Text: "bad"}},
					Usage:   ai.Usage{Input: 11, Output: 7, Reasoning: 5},
				}), nil
			}
			return ai.NewCompletedStream(ai.AssistantMessage{
				Content: []ai.ContentBlock{ai.TextContent{Text: "fix(scope): regenerated"}},
				Usage:   ai.Usage{Input: 13, Output: 9, Reasoning: 6},
			}), nil
		},
	}
	if _, err := service.Propose(context.Background()); err != nil {
		t.Fatalf("Propose: %v", err)
	}
	if len(diagnostics) != 1 {
		t.Fatalf("diagnostics calls = %d, want 1", len(diagnostics))
	}
	d := diagnostics[0]
	if d.MaxOutputTokens != DefaultProposalTokens {
		t.Errorf("max output = %d, want %d", d.MaxOutputTokens, DefaultProposalTokens)
	}
	if d.PromptBytes == 0 || d.EvidenceBytes == 0 {
		t.Errorf("prompt/evidence bytes = %d/%d, want measured sizes", d.PromptBytes, d.EvidenceBytes)
	}
	if d.InputTokens != 24 || d.OutputTokens != 16 || d.ReasoningTokens != 11 {
		t.Errorf("tokens = %d/%d/%d, want the summed provider usage",
			d.InputTokens, d.OutputTokens, d.ReasoningTokens)
	}
	if !d.Reasoning || d.ReasoningEffort != "none" {
		t.Errorf("reasoning = %v/%q, want the explicit disable", d.Reasoning, d.ReasoningEffort)
	}
	if d.Attempts != 2 || d.Regenerations != 1 {
		t.Errorf("attempts/regenerations = %d/%d, want 2/1", d.Attempts, d.Regenerations)
	}
	if !d.ProposalValid {
		t.Error("a successful proposal was not recorded as valid")
	}
	if len(d.RetryReasons) != 1 || !strings.Contains(d.RetryReasons[0], "type(scope)") {
		t.Errorf("retry reasons = %v, want the deterministic rejection rule", d.RetryReasons)
	}
	if !strings.Contains(DiagnosticText(d), "regenerations=1") ||
		!strings.Contains(DiagnosticText(d), "output_tokens=16") {
		t.Errorf("DiagnosticText = %q", DiagnosticText(d))
	}
}

// TestProposalDeadlineIsExplicitAndDiagnosable proves the COMMIT policy owns a
// bounded deadline, that expiry is classified as a timeout, and that the
// diagnostics identify the deadline that fired instead of a generic failure.
func TestProposalDeadlineIsExplicitAndDiagnosable(t *testing.T) {
	repo := newTestRepo(t)
	initialCommit(t, repo, "a.txt", "one\n")
	writeTestFile(t, repo, "a.txt", "two\n")

	const budget = 60 * time.Millisecond
	var diagnostics []ProposalDiagnostics
	service := &Service{
		Root:    repo,
		Model:   ai.Model{Provider: "test", ID: "commit-model", API: "test"},
		Timeout: budget,
		OnDiagnostics: func(d ProposalDiagnostics) {
			diagnostics = append(diagnostics, d)
		},
		Stream: func(ctx context.Context, _ ai.Request) (*ai.Stream, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		},
	}
	_, err := service.Propose(context.Background())
	if !errors.Is(err, ErrModelTimeout) {
		t.Fatalf("Propose = %v, want ErrModelTimeout", err)
	}
	if errors.Is(err, ErrModelCancelled) {
		t.Errorf("deadline expiry was classified as cancellation: %v", err)
	}
	if text := AbortText(err); !strings.Contains(text, "timed out") {
		t.Errorf("AbortText = %q, want a timeout line", text)
	}
	if len(diagnostics) != 1 {
		t.Fatalf("diagnostics calls = %d, want 1", len(diagnostics))
	}
	d := diagnostics[0]
	if d.Timeout != budget || d.Deadline.IsZero() {
		t.Errorf("timeout/deadline = %s/%s, want the configured budget", d.Timeout, d.Deadline)
	}
	if d.ContextDeadline != (time.Time{}) {
		t.Errorf("context deadline = %s, want none for a lifecycle-only parent", d.ContextDeadline)
	}
	if d.Elapsed < budget {
		t.Errorf("elapsed = %s, want at least %s", d.Elapsed, budget)
	}
	if d.CancellationReason != "timeout" {
		t.Errorf("cancellation reason = %q, want timeout", d.CancellationReason)
	}
	if d.Provider != "test" || d.Model != "commit-model" || d.MaxOutputTokens == 0 {
		t.Errorf("model diagnostics = %+v, want provider/model/budget", d)
	}
	if d.Attempts != 1 {
		t.Errorf("attempts = %d, want 1", d.Attempts)
	}
	if !strings.Contains(DiagnosticText(d), "cancellation=timeout") {
		t.Errorf("DiagnosticText = %q", DiagnosticText(d))
	}
}

// TestDelayedResponseInsideTheDeadlineSucceeds pins the boundary between
// latency and timeout: a slow but valid answer must render as a proposal.
func TestDelayedResponseInsideTheDeadlineSucceeds(t *testing.T) {
	repo := newTestRepo(t)
	initialCommit(t, repo, "a.txt", "one\n")
	writeTestFile(t, repo, "a.txt", "two\n")

	var diagnostics []ProposalDiagnostics
	service := &Service{
		Root:    repo,
		Model:   ai.Model{Provider: "test", ID: "commit-model", API: "test"},
		Timeout: 2 * time.Second,
		OnDiagnostics: func(d ProposalDiagnostics) {
			diagnostics = append(diagnostics, d)
		},
		Stream: func(ctx context.Context, _ ai.Request) (*ai.Stream, error) {
			select {
			case <-time.After(100 * time.Millisecond):
				return ai.NewCompletedStream(ai.AssistantMessage{
					Content: []ai.ContentBlock{ai.TextContent{Text: "feat(test): delayed response\n\n- still valid"}},
				}), nil
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		},
	}
	request, err := service.Propose(context.Background())
	if err != nil {
		t.Fatalf("Propose = %v, want the delayed proposal", err)
	}
	if request.Proposal.Subject() != "feat(test): delayed response" {
		t.Errorf("subject = %q", request.Proposal.Subject())
	}
	if len(diagnostics) != 1 || diagnostics[0].CancellationReason != "" {
		t.Errorf("diagnostics = %+v, want a successful, non-cancelled record", diagnostics)
	}
	if diagnostics[0].Elapsed < 100*time.Millisecond {
		t.Errorf("elapsed = %s, want the provider wait", diagnostics[0].Elapsed)
	}
}

// TestCallerCancellationIsNeverReportedAsTimeout proves an explicit cancel is
// immediate and classified as cancellation even while the deadline is far away.
func TestCallerCancellationIsNeverReportedAsTimeout(t *testing.T) {
	repo := newTestRepo(t)
	initialCommit(t, repo, "a.txt", "one\n")
	writeTestFile(t, repo, "a.txt", "two\n")

	started := make(chan struct{})
	parent, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	service := &Service{
		Root:    repo,
		Model:   ai.Model{Provider: "test", ID: "commit-model", API: "test"},
		Timeout: time.Minute,
		Stream: func(ctx context.Context, _ ai.Request) (*ai.Stream, error) {
			close(started)
			<-ctx.Done()
			return nil, ctx.Err()
		},
	}
	go func() {
		<-started
		cancel(ErrCancelledByUser)
	}()
	_, err := service.Propose(parent)
	if !errors.Is(err, ErrModelCancelled) {
		t.Fatalf("Propose = %v, want ErrModelCancelled", err)
	}
	if errors.Is(err, ErrModelTimeout) {
		t.Errorf("cancellation was reported as a timeout: %v", err)
	}
	if got := AbortText(err); got != "Commit proposal cancelled." {
		t.Errorf("AbortText = %q", got)
	}
	if d, ok := DiagnosticsOf(err); !ok || d.CancellationReason != "user" {
		t.Errorf("diagnostics = %+v ok=%v, want the user cause", d, ok)
	}
}

// TestCallerDeadlineIsDistinctFromTheCommitPolicy proves an inherited deadline
// is reported as the caller's layer, not silently attributed to the COMMIT
// budget. The architecture must make that distinction deliberate.
func TestCallerDeadlineIsDistinctFromTheCommitPolicy(t *testing.T) {
	repo := newTestRepo(t)
	initialCommit(t, repo, "a.txt", "one\n")
	writeTestFile(t, repo, "a.txt", "two\n")

	parent, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	service := &Service{
		Root:    repo,
		Model:   ai.Model{Provider: "test", ID: "commit-model", API: "test"},
		Timeout: time.Minute,
		Stream: func(ctx context.Context, _ ai.Request) (*ai.Stream, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		},
	}
	_, err := service.Propose(parent)
	if !errors.Is(err, ErrModelTimeout) {
		t.Fatalf("Propose = %v, want ErrModelTimeout", err)
	}
	d, ok := DiagnosticsOf(err)
	if !ok {
		t.Fatal("the timeout carried no diagnostics")
	}
	if d.Timeout != time.Minute {
		t.Errorf("configured timeout = %s, want the COMMIT policy", d.Timeout)
	}
	if d.CancellationReason != "context-deadline" {
		t.Errorf("cancellation reason = %q, want context-deadline", d.CancellationReason)
	}
	if d.ContextDeadline.IsZero() || !d.ContextDeadline.Equal(d.Deadline) {
		t.Errorf("context deadline = %s, effective = %s; want the caller's earlier deadline", d.ContextDeadline, d.Deadline)
	}
}

// TestProviderTimeoutKeepsItsCause proves a transport timeout is a timeout
// subtype that names the provider, not the interactive budget.
func TestProviderTimeoutKeepsItsCause(t *testing.T) {
	repo := newTestRepo(t)
	initialCommit(t, repo, "a.txt", "one\n")
	writeTestFile(t, repo, "a.txt", "two\n")

	providerErr := timeoutError{message: "upstream stalled waiting for headers"}
	service := &Service{
		Root:    repo,
		Model:   ai.Model{Provider: "test", ID: "commit-model", API: "test"},
		Timeout: time.Minute,
		Stream: func(context.Context, ai.Request) (*ai.Stream, error) {
			return nil, providerErr
		},
	}
	_, err := service.Propose(context.Background())
	if !errors.Is(err, ErrModelProviderTimeout) {
		t.Fatalf("Propose = %v, want ErrModelProviderTimeout", err)
	}
	if !errors.Is(err, ErrModelTimeout) {
		t.Errorf("provider timeout lost the timeout hierarchy: %v", err)
	}
	if !errors.Is(err, providerErr) {
		t.Errorf("the provider cause was lost: %v", err)
	}
	if d, ok := DiagnosticsOf(err); !ok || d.CancellationReason != "provider-timeout" {
		t.Errorf("diagnostics = %+v ok=%v, want provider-timeout", d, ok)
	}
	if text := AbortText(err); !strings.Contains(text, "provider timed out") {
		t.Errorf("AbortText = %q, want the provider layer", text)
	}
}

// TestTimeoutTextNamesTheElapsedWait covers the user-facing duration without
// waiting for a real five-minute deadline.
func TestTimeoutTextNamesTheElapsedWait(t *testing.T) {
	err := &ProposalError{
		Kind:    ErrModelTimeout,
		Message: ErrModelTimeout.Error(),
		Diagnostics: ProposalDiagnostics{
			Elapsed: 45 * time.Second,
		},
	}
	want := "Commit proposal failed:\nthe COMMIT model request timed out after 45s."
	if got := AbortText(err); got != want {
		t.Errorf("AbortText = %q, want %q", got, want)
	}
}

// timeoutError is a transport timeout that does not wrap context.DeadlineExceeded,
// which is exactly the provider-side case the classifier must keep distinct.
type timeoutError struct{ message string }

func (e timeoutError) Error() string   { return e.message }
func (e timeoutError) Timeout() bool   { return true }
func (e timeoutError) Temporary() bool { return true }

// TestDefaultProposalTimeoutIsBoundedForInteractiveUse pins the policy: long
// enough for reasoning and provider queueing, still below the transport
// ceiling so a stuck connection is reported as a COMMIT timeout.
func TestDefaultProposalTimeoutIsBoundedForInteractiveUse(t *testing.T) {
	if DefaultProposalTimeout <= 2*time.Minute {
		t.Errorf("default timeout %s is the old too-short policy", DefaultProposalTimeout)
	}
	if DefaultProposalTimeout >= 10*time.Minute {
		t.Errorf("default timeout %s is indistinguishable from the transport ceiling", DefaultProposalTimeout)
	}
}
