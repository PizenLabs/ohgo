package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/PizenLabs/ohgo/commit"
	tea "github.com/charmbracelet/bubbletea"
)

// commitRequestFixture is a valid request with a visible message and scope.
func commitRequestFixture() *commit.Request {
	return &commit.Request{
		Proposal: commit.Proposal{
			Type:    "feat",
			Scope:   "commit",
			Summary: "safe proposal",
			Body:    []string{"- validate the message", "- stage one exact scope"},
		},
		Snapshot: &commit.Snapshot{
			Mode: commit.ModeWorktree,
			Scope: []commit.FileChange{
				{Path: "agent/loop.go", Index: ' ', Worktree: 'M'},
				{Path: "commit/message.go", Untracked: true, Index: '?', Worktree: '?'},
			},
			Internal: []commit.FileChange{{Path: ".ohgo/project.yml", Untracked: true, Index: '?', Worktree: '?', Internal: true}},
		},
	}
}

func TestCommitIsADiscoverableCommand(t *testing.T) {
	spec, ok := LookupCommand("commit")
	if !ok {
		t.Fatal("/commit is not registered")
	}
	if spec.Hidden {
		t.Error("/commit is hidden from discovery")
	}
	if !strings.Contains(spec.Description, "commit") {
		t.Errorf("description = %q", spec.Description)
	}
}

func TestCommitProposalIsVisibleAndRequiresApproval(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	var executes atomic.Int32
	p.SetCommitHooks(CommitHooks{
		Propose: func(context.Context) (*commit.Request, error) {
			return commitRequestFixture(), nil
		},
		Execute: func(ctx context.Context, request *commit.Request) (commit.Result, error) {
			executes.Add(1)
			return commit.Result{
				SHA:     "abcdef123456",
				Subject: request.Proposal.Subject(),
				Files:   request.Snapshot.Scope,
				Clean:   true,
			}, nil
		},
	})

	cmd := p.runCommand(Command{Name: "commit"})
	if cmd == nil {
		t.Fatal("/commit produced no proposal command")
	}
	p.Update(cmd())

	if got := executes.Load(); got != 0 {
		t.Fatalf("Execute ran %d times before approval", got)
	}
	if p.state.Pending == nil || p.state.Pending.Commit == nil {
		t.Fatal("the proposal dialog did not open")
	}
	out := stripANSI(Render(p.state))
	for _, want := range []string{
		"Commit Proposal",
		"feat(commit): safe proposal",
		"- validate the message",
		"- stage one exact scope",
		"Files:",
		"M agent/loop.go",
		"?? commit/message.go",
		"Create this commit?",
		"Cancel (default)",
		"e edit · r retry",
		".ohgo/ internal",
		"state is excluded.",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("proposal dialog = %q, want %q", out, want)
		}
	}
	if strings.Contains(out, ".ohgo/project.yml") {
		t.Error("the dialog listed .ohgo as a commit path")
	}
}

func TestOneApprovalProducesExactlyOneExecution(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	var executes atomic.Int32
	p.SetCommitHooks(CommitHooks{
		Propose: func(context.Context) (*commit.Request, error) {
			return commitRequestFixture(), nil
		},
		Execute: func(ctx context.Context, request *commit.Request) (commit.Result, error) {
			executes.Add(1)
			return commit.Result{
				SHA:     "abcdef123456",
				Subject: request.Proposal.Subject(),
				Files:   request.Snapshot.Scope,
				Clean:   true,
			}, nil
		},
	})

	p.Update(p.runCommand(Command{Name: "commit"})())
	// The resting cursor is Cancel; one move lands on Create commit.
	p.Update(tea.KeyMsg{Type: tea.KeyUp})
	_, execCmd := p.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if execCmd == nil {
		t.Fatal("approval produced no execution command")
	}
	p.Update(execCmd())

	if got := executes.Load(); got != 1 {
		t.Fatalf("Execute ran %d times after one approval, want exactly 1", got)
	}
	if p.commitRunning {
		t.Error("the execution was left marked running")
	}
	if p.state.Pending != nil {
		t.Error("the proposal dialog stayed open after execution")
	}
	text := noticesText(p)
	if !strings.Contains(text, "Committed abcdef1 feat(commit): safe proposal") {
		t.Errorf("notices = %q, want the verified SHA and subject", text)
	}
	if !strings.Contains(text, "Working tree is clean.") {
		t.Errorf("notices = %q, want the proven clean state", text)
	}
}

func TestCancellingACommitProposalDoesNotExecute(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	var executes atomic.Int32
	p.SetCommitHooks(CommitHooks{
		Propose: func(context.Context) (*commit.Request, error) {
			return commitRequestFixture(), nil
		},
		Execute: func(context.Context, *commit.Request) (commit.Result, error) {
			executes.Add(1)
			return commit.Result{}, nil
		},
	})
	p.Update(p.runCommand(Command{Name: "commit"})())
	p.Update(tea.KeyMsg{Type: tea.KeyEsc})

	if got := executes.Load(); got != 0 {
		t.Fatalf("cancel executed %d times", got)
	}
	if p.commitRequest != nil || p.state.Pending != nil {
		t.Error("cancel left the proposal live")
	}
	if text := noticesText(p); !strings.Contains(text, "commit cancelled") {
		t.Errorf("notices = %q", text)
	}
}

func TestCtrlCCancelsACommitProposal(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	var executes atomic.Int32
	p.SetCommitHooks(CommitHooks{
		Propose: func(context.Context) (*commit.Request, error) {
			return commitRequestFixture(), nil
		},
		Execute: func(context.Context, *commit.Request) (commit.Result, error) {
			executes.Add(1)
			return commit.Result{}, nil
		},
	})
	p.Update(p.runCommand(Command{Name: "commit"})())
	p.Update(tea.KeyMsg{Type: tea.KeyCtrlC})

	if got := executes.Load(); got != 0 {
		t.Fatalf("ctrl+c executed %d times", got)
	}
	if p.commitRequest != nil || p.state.Pending != nil {
		t.Error("ctrl+c left the proposal live")
	}
}

func TestNoChangesSkipsTheProposalDialog(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	p.SetCommitHooks(CommitHooks{
		Propose: func(context.Context) (*commit.Request, error) {
			return nil, commit.ErrNoChanges
		},
		Execute: func(context.Context, *commit.Request) (commit.Result, error) {
			t.Fatal("Execute ran despite no changes")
			return commit.Result{}, nil
		},
	})
	p.Update(p.runCommand(Command{Name: "commit"})())

	if p.state.Pending != nil {
		t.Error("a clean tree opened a commit dialog")
	}
	if text := noticesText(p); !strings.Contains(text, "No changes to commit.") {
		t.Errorf("notices = %q", text)
	}
}

func TestInvalidProposalIsReportedAndNeverExecuted(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	p.SetCommitHooks(CommitHooks{
		Propose: func(context.Context) (*commit.Request, error) {
			return nil, commit.ErrInvalidProposal
		},
		Execute: func(context.Context, *commit.Request) (commit.Result, error) {
			t.Fatal("Execute ran for an invalid proposal")
			return commit.Result{}, nil
		},
	})
	p.Update(p.runCommand(Command{Name: "commit"})())

	if p.state.Pending != nil {
		t.Error("an invalid proposal opened an approval dialog")
	}
	if text := noticesText(p); !strings.Contains(text, "did not produce a valid proposal") {
		t.Errorf("notices = %q", text)
	}
}

func TestFailedExecutionIsReportedWithoutASecondAttempt(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	var executes atomic.Int32
	p.SetCommitHooks(CommitHooks{
		Propose: func(context.Context) (*commit.Request, error) {
			return commitRequestFixture(), nil
		},
		Execute: func(context.Context, *commit.Request) (commit.Result, error) {
			executes.Add(1)
			return commit.Result{}, errors.New("pre-commit hook rejected the commit")
		},
	})
	p.Update(p.runCommand(Command{Name: "commit"})())
	p.Update(tea.KeyMsg{Type: tea.KeyUp})
	_, execCmd := p.Update(tea.KeyMsg{Type: tea.KeyEnter})
	p.Update(execCmd())

	if got := executes.Load(); got != 1 {
		t.Fatalf("Execute ran %d times after a failure, want exactly 1", got)
	}
	if text := noticesText(p); !strings.Contains(text, "pre-commit hook rejected the commit") {
		t.Errorf("notices = %q, want the real Git failure", text)
	}
}

func TestCommitRefusesWhileATurnIsRunning(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	p.state.Status.Busy = true
	p.SetCommitHooks(CommitHooks{
		Propose: func(context.Context) (*commit.Request, error) {
			t.Fatal("Propose ran while a turn was busy")
			return nil, nil
		},
		Execute: func(context.Context, *commit.Request) (commit.Result, error) {
			return commit.Result{}, nil
		},
	})
	if cmd := p.runCommand(Command{Name: "commit"}); cmd != nil {
		t.Error("/commit started while a turn was running")
	}
	if text := noticesText(p); !strings.Contains(text, "wait for the turn") {
		t.Errorf("notices = %q", text)
	}
}

// TestCommitGenericArgsCannotBecomeAgentCommits is the regression for the
// observed failure class: a request like "git commit help me" was handed to a
// generic BUILD turn, which produced several commits and a separate .ohgo
// cleanup commit. /commit refuses unknown words, asks the dedicated model for
// one proposal, and one approval can only call Execute once.
func TestCommitGenericArgsCannotBecomeAgentCommits(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	var executes atomic.Int32
	p.SetCommitHooks(CommitHooks{
		Propose: func(context.Context) (*commit.Request, error) {
			t.Fatal("unknown free text reached the commit model")
			return commitRequestFixture(), nil
		},
		Execute: func(ctx context.Context, request *commit.Request) (commit.Result, error) {
			executes.Add(1)
			return commit.Result{}, nil
		},
	})

	// The argument text that previously became an agent instruction.
	cmd := p.runCommand(Command{
		Name: "commit",
		Args: []string{"git", "commit", "help", "me"},
	})
	if cmd != nil {
		t.Fatal("/commit accepted generic free text as an instruction")
	}
	if p.state.Pending != nil || p.commitRequest != nil {
		t.Fatal("the generic argument text opened a commit proposal")
	}
	if len(p.history) != 0 || len(p.state.Queued) != 0 {
		t.Errorf("free text became a turn: history=%d queued=%d", len(p.history), len(p.state.Queued))
	}
	if got := executes.Load(); got != 0 {
		t.Fatalf("Execute ran %d times", got)
	}
	if text := noticesText(p); !strings.Contains(text, "usage: /commit") {
		t.Errorf("notices = %q, want the argument contract", text)
	}
}

// TestCommitShowsImmediateProgressAndKeepsTheLoopResponsive is the timing
// regression: before the fix, /commit changed no state until the provider
// answered, so the screen showed nothing for the whole request. The spinner
// must exist immediately and the shared tick must animate while the provider
// call is still blocked in its command goroutine.
func TestCommitShowsImmediateProgressAndKeepsTheLoopResponsive(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	release := make(chan struct{})
	done := make(chan *commit.Request, 1)
	p.SetCommitHooks(CommitHooks{
		Propose: func(context.Context) (*commit.Request, error) {
			<-release
			done <- commitRequestFixture()
			return commitRequestFixture(), nil
		},
		Execute: func(context.Context, *commit.Request) (commit.Result, error) {
			return commit.Result{}, nil
		},
	})

	cmd := p.runCommand(Command{Name: "commit"})
	if cmd == nil {
		t.Fatal("/commit produced no command")
	}
	if p.state.CommitPhase() != CommitProposing || !p.state.CommitActive() {
		t.Fatalf("phase = %q active=%v immediately after /commit, want proposing", p.state.CommitPhase(), p.state.CommitActive())
	}
	out := stripANSI(Render(p.state))
	if !strings.Contains(out, "Preparing commit") {
		t.Errorf("render = %q, want the commit activity line before the model answers", out)
	}
	if !strings.Contains(out, spinnerFrames[0]) {
		t.Errorf("render = %q, want the first spinner frame", out)
	}

	// The provider is still blocked; the update loop must still process ticks.
	before := p.state.Status.Spinner
	p.Update(tickMsg(time.Now()))
	if p.state.Status.Spinner == before {
		t.Error("the commit spinner did not advance while the provider was pending")
	}
	if out := stripANSI(Render(p.state)); !strings.Contains(out, spinnerFrames[p.state.Status.Spinner]) {
		t.Errorf("render = %q, want the advanced spinner frame", out)
	}

	// Release the command and deliver its result through the normal Update
	// path, exactly as Bubble Tea would.
	result := make(chan tea.Msg, 1)
	go func() { result <- cmd() }()
	close(release)
	msg := <-result
	<-done
	p.Update(msg)
	if p.state.CommitPhase() != CommitReview {
		t.Fatalf("phase = %q, want review", p.state.CommitPhase())
	}
	if p.state.Pending == nil || p.state.Pending.Commit == nil {
		t.Fatal("the proposal dialog did not open as soon as the answer arrived")
	}
	if p.state.CommitActive() {
		t.Error("the spinner is still active in review")
	}
}

// TestCommitModelFailureStopsTheSpinnerAndStaysConcise covers the exact
// observed error class and proves no proposal dialog opens.
func TestCommitModelFailureStopsTheSpinnerAndStaysConcise(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	p.SetCommitHooks(CommitHooks{
		Propose: func(context.Context) (*commit.Request, error) {
			return nil, commit.ErrModelEmpty
		},
		Execute: func(context.Context, *commit.Request) (commit.Result, error) {
			t.Fatal("Execute ran for a failed proposal")
			return commit.Result{}, nil
		},
	})
	cmd := p.runCommand(Command{Name: "commit"})
	p.Update(cmd())

	if p.state.CommitPhase() != CommitFailed || p.state.CommitActive() {
		t.Fatalf("phase = %q active=%v, want failed", p.state.CommitPhase(), p.state.CommitActive())
	}
	if p.state.Pending != nil {
		t.Error("a failed proposal opened the approval dialog")
	}
	text := noticesText(p)
	if !strings.Contains(text, "the COMMIT model returned an empty response") {
		t.Errorf("notices = %q, want the classified empty-response line", text)
	}
	if strings.Contains(text, "invalid proposal: commit:") {
		t.Errorf("notices = %q, want no nested sentinel", text)
	}
	out := stripANSI(Render(p.state))
	if strings.Contains(out, "Preparing commit") {
		t.Error("the activity line survived a terminal failure")
	}
	if strings.Contains(out, "Creating commit") {
		t.Error("an execution spinner appeared for a failed proposal")
	}
	before := p.state.Status.Spinner
	p.Update(tickMsg(time.Now()))
	if p.state.Status.Spinner != before {
		t.Error("the spinner kept advancing after the operation failed")
	}
}

// TestCommitProviderErrorIsReportedVerbatim proves a transport/provider
// failure is neither retried silently nor mislabelled as an invalid proposal.
func TestCommitProviderErrorIsReportedVerbatim(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	p.SetCommitHooks(CommitHooks{
		Propose: func(context.Context) (*commit.Request, error) {
			return nil, fmt.Errorf("%w: %w", commit.ErrModel, errors.New("HTTP 401: credential rejected"))
		},
		Execute: func(context.Context, *commit.Request) (commit.Result, error) {
			t.Fatal("Execute ran after a provider failure")
			return commit.Result{}, nil
		},
	})
	p.Update(p.runCommand(Command{Name: "commit"})())

	if p.state.CommitPhase() != CommitFailed || p.state.CommitActive() {
		t.Fatalf("phase = %q active=%v, want failed", p.state.CommitPhase(), p.state.CommitActive())
	}
	text := noticesText(p)
	if !strings.Contains(text, "HTTP 401: credential rejected") {
		t.Errorf("notices = %q, want the real provider error", text)
	}
	if strings.Contains(text, "invalid proposal") {
		t.Errorf("notices = %q, misclassified as an invalid proposal", text)
	}
}

// TestCommitCancellationDropsADelayedProposal proves a cancelled request can
// never reopen a dialog when its provider answer arrives later.
func TestCommitCancellationDropsADelayedProposal(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	release := make(chan struct{})
	result := make(chan tea.Msg, 1)
	p.SetCommitHooks(CommitHooks{
		Propose: func(context.Context) (*commit.Request, error) {
			<-release
			return commitRequestFixture(), nil
		},
		Execute: func(context.Context, *commit.Request) (commit.Result, error) {
			t.Fatal("Execute ran for a cancelled proposal")
			return commit.Result{}, nil
		},
	})
	cmd := p.runCommand(Command{Name: "commit"})
	go func() { result <- cmd() }()

	// Esc while the request is pending cancels the read-only operation.
	p.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if p.state.CommitPhase() != CommitCancelled {
		t.Fatalf("phase = %q, want cancelled", p.state.CommitPhase())
	}
	if p.commitProposing {
		t.Error("the proposal was left marked in flight after cancellation")
	}

	close(release)
	p.Update(<-result)
	if p.state.Pending != nil || p.commitRequest != nil {
		t.Fatal("a stale provider answer reopened the proposal dialog")
	}
	if p.state.CommitPhase() != CommitCancelled {
		t.Errorf("stale answer changed the terminal phase to %q", p.state.CommitPhase())
	}
}

// TestRepeatedCommitDoesNotStartASecondGeneration proves the command is a
// single-operation boundary, not a way to race two proposals.
func TestRepeatedCommitDoesNotStartASecondGeneration(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	var calls atomic.Int32
	release := make(chan struct{})
	p.SetCommitHooks(CommitHooks{
		Propose: func(context.Context) (*commit.Request, error) {
			calls.Add(1)
			<-release
			return commitRequestFixture(), nil
		},
		Execute: func(context.Context, *commit.Request) (commit.Result, error) {
			return commit.Result{}, nil
		},
	})
	first := p.runCommand(Command{Name: "commit"})
	result := make(chan tea.Msg, 1)
	go func() { result <- first() }()
	deadline := time.Now().Add(time.Second)
	for calls.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	second := p.runCommand(Command{Name: "commit"})
	if second != nil {
		t.Error("a second /commit started while the first was still proposing")
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("Propose calls = %d, want 1", got)
	}
	if text := noticesText(p); !strings.Contains(text, "already being prepared") {
		t.Errorf("notices = %q", text)
	}
	close(release)
	p.Update(<-result)
}

// TestCommitRetryRegeneratesWithoutExecuting proves --retry asks the model
// again, replaces the proposal, and never stages or commits.
func TestCommitRetryRegeneratesWithoutExecuting(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	var calls atomic.Int32
	var executes atomic.Int32
	p.SetCommitHooks(CommitHooks{
		Propose: func(context.Context) (*commit.Request, error) {
			request := commitRequestFixture()
			if calls.Add(1) == 2 {
				request.Proposal.Summary = "second proposal"
			}
			return request, nil
		},
		Execute: func(context.Context, *commit.Request) (commit.Result, error) {
			executes.Add(1)
			return commit.Result{}, nil
		},
	})

	p.Update(p.runCommand(Command{Name: "commit"})())
	if calls.Load() != 1 {
		t.Fatalf("Propose calls = %d, want 1", calls.Load())
	}

	retry := p.runCommand(Command{Name: "commit", Args: []string{"--retry"}})
	if retry == nil {
		t.Fatal("/commit --retry produced no generation command")
	}
	if p.state.CommitPhase() != CommitProposing {
		t.Fatalf("phase = %q during retry, want proposing", p.state.CommitPhase())
	}
	if p.state.Pending != nil {
		t.Error("the old proposal dialog stayed open during retry")
	}
	p.Update(retry())
	if got := calls.Load(); got != 2 {
		t.Errorf("Propose calls = %d, want 2", got)
	}
	if got := executes.Load(); got != 0 {
		t.Errorf("Execute calls = %d during retry, want 0", got)
	}
	if p.state.Pending == nil || p.state.Pending.Commit == nil {
		t.Fatal("retry did not return to review")
	}
	if got := p.state.Pending.Commit.Subject; got != "feat(commit): second proposal" {
		t.Errorf("subject = %q, want the regenerated proposal", got)
	}
}

// TestCommitEditValidatesLocallyAndNeverCallsTheModel proves --edit is a text
// operation: it rejects a contract violation, then accepts a valid edit and
// reopens review without a provider call or a Git mutation.
func TestCommitEditValidatesLocallyAndNeverCallsTheModel(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	var calls atomic.Int32
	var executes atomic.Int32
	p.SetCommitHooks(CommitHooks{
		Propose: func(context.Context) (*commit.Request, error) {
			calls.Add(1)
			return commitRequestFixture(), nil
		},
		Execute: func(context.Context, *commit.Request) (commit.Result, error) {
			executes.Add(1)
			return commit.Result{}, nil
		},
	})
	p.Update(p.runCommand(Command{Name: "commit"})())

	p.runCommand(Command{Name: "commit", Args: []string{"--edit"}})
	if p.state.CommitPhase() != CommitEditing {
		t.Fatalf("phase = %q, want editing", p.state.CommitPhase())
	}
	if p.state.Pending != nil {
		t.Error("the review dialog stayed open during the edit")
	}
	if !p.commitEditing {
		t.Fatal("the program did not enter the edit mode")
	}
	if got := p.state.Input; got != commitRequestFixture().Proposal.Message() {
		t.Errorf("editor input = %q, want the current message", got)
	}

	// Too long a subject must be rejected in place, keeping the edit live.
	p.state.Apply(ComposerChanged{Text: "feat(commit): " + strings.Repeat("x", 30), Cursor: 45})
	p.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if p.state.CommitPhase() != CommitEditing || p.state.Pending != nil {
		t.Fatal("an invalid edit left the edit mode")
	}
	if text := noticesText(p); !strings.Contains(text, "commit message rejected") {
		t.Errorf("notices = %q, want the validation rule", text)
	}

	// A valid edit reopens review with the exact edited text.
	edited := "fix(commit): edited message\n\n- keep the repository safe"
	p.state.Apply(ComposerChanged{Text: edited, Cursor: len([]rune(edited))})
	p.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if p.state.CommitPhase() != CommitReview {
		t.Fatalf("phase = %q after a valid edit, want review", p.state.CommitPhase())
	}
	if p.state.Pending == nil || p.state.Pending.Commit == nil {
		t.Fatal("a valid edit did not reopen the review dialog")
	}
	if got := p.state.Pending.Commit.Subject; got != "fix(commit): edited message" {
		t.Errorf("subject = %q, want the edited subject", got)
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("Propose calls = %d, want 1 (editing must not call the model)", got)
	}
	if got := executes.Load(); got != 0 {
		t.Errorf("Execute calls = %d before approval, want 0", got)
	}
}

// TestStaleExecutionResultReportsWithoutClaimingTheNewSession proves a
// session boundary cannot be mutated by an earlier commit's completion, while
// the real commit outcome is still reported truthfully.
func TestStaleExecutionResultReportsWithoutClaimingTheNewSession(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	p.SetCommitHooks(CommitHooks{
		Propose: func(context.Context) (*commit.Request, error) {
			return commitRequestFixture(), nil
		},
		Execute: func(context.Context, *commit.Request) (commit.Result, error) {
			return commit.Result{SHA: "abcdef123456", Subject: "feat(commit): safe proposal", Clean: true}, nil
		},
	})
	p.Update(p.runCommand(Command{Name: "commit"})())
	p.Update(tea.KeyMsg{Type: tea.KeyUp})
	_, execCmd := p.Update(tea.KeyMsg{Type: tea.KeyEnter})
	staleSeq := p.commitSeq
	// The user changes sessions while the Git operation is in flight.
	p.commitSeq++
	msg := execCmd().(commitFinishedMsg)
	if msg.seq != staleSeq {
		t.Fatalf("execution seq = %d, want %d", msg.seq, staleSeq)
	}
	p.Update(msg)

	if p.state.CommitPhase() == CommitDone {
		t.Error("a stale execution claimed the new session's lifecycle")
	}
	if text := noticesText(p); !strings.Contains(text, "Committed abcdef1 feat(commit): safe proposal") {
		t.Errorf("notices = %q, want the truthful commit result", text)
	}
}

// TestCommitLifecyclePhasesAreExplicit walks the required state machine.
func TestCommitLifecyclePhasesAreExplicit(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	p.SetCommitHooks(CommitHooks{
		Propose: func(context.Context) (*commit.Request, error) {
			return commitRequestFixture(), nil
		},
		Execute: func(context.Context, *commit.Request) (commit.Result, error) {
			return commit.Result{SHA: "abcdef123456", Subject: "feat(commit): safe proposal", Clean: true}, nil
		},
	})

	cmd := p.runCommand(Command{Name: "commit"})
	if p.state.CommitPhase() != CommitProposing {
		t.Fatalf("phase = %q, want proposing", p.state.CommitPhase())
	}
	p.Update(cmd())
	if p.state.CommitPhase() != CommitReview {
		t.Fatalf("phase = %q, want review", p.state.CommitPhase())
	}

	p.Update(tea.KeyMsg{Type: tea.KeyUp})
	_, execCmd := p.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if p.state.CommitPhase() != CommitExecuting {
		t.Fatalf("phase = %q, want executing", p.state.CommitPhase())
	}
	p.Update(execCmd())
	if p.state.CommitPhase() != CommitDone {
		t.Fatalf("phase = %q, want done", p.state.CommitPhase())
	}
}

// noticesText concatenates transcript notices for assertions.
func noticesText(p *Program) string {
	var text string
	for _, entry := range p.state.Entries {
		if entry.Kind == EntryNotice {
			text += entry.Notice + "\n"
		}
	}
	return text
}

// TestUnrelatedTurnCancellationDoesNotCancelCommit pins context ownership: a
// /commit proposal is an independent interactive request, so cancelling an
// unrelated agent turn must not abort it.
func TestUnrelatedTurnCancellationDoesNotCancelCommit(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	release := make(chan struct{})
	captured := make(chan context.Context, 1)
	p.SetCommitHooks(CommitHooks{
		Propose: func(ctx context.Context) (*commit.Request, error) {
			captured <- ctx
			<-release
			return commitRequestFixture(), nil
		},
		Execute: func(context.Context, *commit.Request) (commit.Result, error) {
			t.Fatal("Execute ran for a read-only proposal")
			return commit.Result{}, nil
		},
	})
	cmd := p.runCommand(Command{Name: "commit"})
	result := make(chan tea.Msg, 1)
	go func() { result <- cmd() }()

	var ctx context.Context
	select {
	case ctx = <-captured:
	case <-time.After(time.Second):
		t.Fatal("the proposal never started")
	}

	// A turn cancellation is a different lifetime.
	p.cancelTurnNow("cancelled")
	select {
	case <-ctx.Done():
		t.Fatalf("an unrelated turn cancelled the commit request: %v", context.Cause(ctx))
	case <-time.After(30 * time.Millisecond):
	}

	close(release)
	p.Update(<-result)
	if p.state.CommitPhase() != CommitReview {
		t.Fatalf("phase = %q, want review after the independent request completed", p.state.CommitPhase())
	}
	if p.state.Pending == nil || p.state.Pending.Commit == nil {
		t.Fatal("the independent proposal dialog did not open")
	}
}

// TestQuitCancelsCommitWithSessionCause proves session shutdown still reaches
// the request immediately, and that its late answer cannot reopen a dialog.
func TestQuitCancelsCommitWithSessionCause(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	release := make(chan struct{})
	captured := make(chan context.Context, 1)
	p.SetCommitHooks(CommitHooks{
		Propose: func(ctx context.Context) (*commit.Request, error) {
			captured <- ctx
			<-release
			return commitRequestFixture(), nil
		},
		Execute: func(context.Context, *commit.Request) (commit.Result, error) {
			return commit.Result{}, nil
		},
	})
	cmd := p.runCommand(Command{Name: "commit"})
	result := make(chan tea.Msg, 1)
	go func() { result <- cmd() }()

	var ctx context.Context
	select {
	case ctx = <-captured:
	case <-time.After(time.Second):
		t.Fatal("the proposal never started")
	}
	p.quitNow()
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("quitting did not cancel the commit request")
	}
	if got := context.Cause(ctx); !errors.Is(got, commit.ErrSessionClosed) {
		t.Errorf("cancel cause = %v, want the session cause", got)
	}
	close(release)
	p.Update(<-result)
	if p.state.Pending != nil || p.commitRequest != nil {
		t.Fatal("a late answer reopened a proposal after quit")
	}
}

// TestUserCancelStopsTheSpinnerWithAUserCause covers the immediate cancel path
// independently from a deadline.
func TestUserCancelStopsTheSpinnerWithAUserCause(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	release := make(chan struct{})
	captured := make(chan context.Context, 1)
	p.SetCommitHooks(CommitHooks{
		Propose: func(ctx context.Context) (*commit.Request, error) {
			captured <- ctx
			<-release
			return commitRequestFixture(), nil
		},
		Execute: func(context.Context, *commit.Request) (commit.Result, error) {
			return commit.Result{}, nil
		},
	})
	cmd := p.runCommand(Command{Name: "commit"})
	result := make(chan tea.Msg, 1)
	go func() { result <- cmd() }()

	var ctx context.Context
	select {
	case ctx = <-captured:
	case <-time.After(time.Second):
		t.Fatal("the proposal never started")
	}
	p.Update(tea.KeyMsg{Type: tea.KeyEsc})
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("user cancellation did not reach the request")
	}
	if got := context.Cause(ctx); !errors.Is(got, commit.ErrCancelledByUser) {
		t.Errorf("cancel cause = %v, want the user cause", got)
	}
	if p.state.CommitPhase() != CommitCancelled || p.state.CommitActive() {
		t.Fatalf("phase = %q active=%v, want cancelled and stopped", p.state.CommitPhase(), p.state.CommitActive())
	}
	close(release)
	p.Update(<-result)
	if p.state.Pending != nil {
		t.Fatal("a late answer reopened the dialog after the user cancelled")
	}
}

// TestCommitTimeoutIsClassifiedAndStopsTheSpinner proves a timeout is not
// rendered as cancellation or an empty proposal, and that no stale spinner
// survives the terminal transition.
func TestCommitTimeoutIsClassifiedAndStopsTheSpinner(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	p.SetCommitHooks(CommitHooks{
		Propose: func(context.Context) (*commit.Request, error) {
			return nil, &commit.ProposalError{
				Kind:    commit.ErrModelTimeout,
				Message: commit.ErrModelTimeout.Error(),
				Cause:   context.DeadlineExceeded,
				Diagnostics: commit.ProposalDiagnostics{
					Provider:           "openrouter",
					Model:              "cohere/north-mini-code:free",
					MaxOutputTokens:    8192,
					Timeout:            5 * time.Minute,
					Elapsed:            45 * time.Second,
					Attempts:           1,
					CancellationReason: "timeout",
				},
			}
		},
		Execute: func(context.Context, *commit.Request) (commit.Result, error) {
			t.Fatal("Execute ran for a timed-out proposal")
			return commit.Result{}, nil
		},
	})
	cmd := p.runCommand(Command{Name: "commit"})
	if !p.state.CommitActive() {
		t.Fatal("the proposal did not start")
	}
	p.Update(cmd())

	if p.state.CommitPhase() != CommitFailed || p.state.CommitActive() {
		t.Fatalf("phase = %q active=%v, want failed and stopped", p.state.CommitPhase(), p.state.CommitActive())
	}
	if p.state.Pending != nil {
		t.Error("a timed-out proposal opened the approval dialog")
	}
	text := noticesText(p)
	if !strings.Contains(text, "timed out after 45s") {
		t.Errorf("notices = %q, want the elapsed timeout line", text)
	}
	if strings.Contains(text, "cancelled") || strings.Contains(text, "empty response") {
		t.Errorf("notices = %q, timeout was misclassified", text)
	}
	if p.state.Commit == nil || !strings.Contains(p.state.Commit.Text, "cancellation=timeout") {
		t.Errorf("internal diagnostics = %+v, want the timeout record", p.state.Commit)
	}
	before := p.state.Status.Spinner
	p.Update(tickMsg(time.Now()))
	if p.state.Status.Spinner != before {
		t.Error("the spinner kept advancing after the timeout")
	}
}

// TestStaleTimedOutResponseDoesNotReplaceTheNewerProposal covers sequence
// invalidation across a cancelled timeout and a fresh /commit.
func TestStaleTimedOutResponseDoesNotReplaceTheNewerProposal(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	firstRelease := make(chan struct{})
	var calls atomic.Int32
	p.SetCommitHooks(CommitHooks{
		Propose: func(context.Context) (*commit.Request, error) {
			if calls.Add(1) == 1 {
				<-firstRelease
				return nil, &commit.ProposalError{
					Kind:        commit.ErrModelTimeout,
					Message:     commit.ErrModelTimeout.Error(),
					Cause:       context.DeadlineExceeded,
					Diagnostics: commit.ProposalDiagnostics{CancellationReason: "timeout", Elapsed: time.Minute},
				}
			}
			request := commitRequestFixture()
			request.Proposal.Summary = "second proposal"
			return request, nil
		},
		Execute: func(context.Context, *commit.Request) (commit.Result, error) {
			return commit.Result{}, nil
		},
	})

	first := p.runCommand(Command{Name: "commit"})
	firstResult := make(chan tea.Msg, 1)
	go func() { firstResult <- first() }()
	deadline := time.Now().Add(time.Second)
	for calls.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	p.Update(tea.KeyMsg{Type: tea.KeyEsc})

	second := p.runCommand(Command{Name: "commit"})
	if second == nil {
		t.Fatal("the second /commit was refused")
	}
	p.Update(second())
	if p.state.Pending == nil || p.state.Pending.Commit == nil {
		t.Fatal("the second proposal did not open")
	}
	if got := p.state.Pending.Commit.Subject; !strings.Contains(got, "second proposal") {
		t.Fatalf("subject = %q, want the newer proposal", got)
	}

	close(firstRelease)
	p.Update(<-firstResult)
	if p.state.Pending == nil || p.state.Pending.Commit == nil {
		t.Fatal("the stale timeout closed the newer proposal")
	}
	if got := p.state.Pending.Commit.Subject; !strings.Contains(got, "second proposal") {
		t.Errorf("subject = %q, the stale timeout replaced the newer proposal", got)
	}
	if p.state.CommitPhase() != CommitReview {
		t.Errorf("phase = %q, want review", p.state.CommitPhase())
	}
}

// TestCommitRetryAfterTimeoutStartsAFreshRequest proves retry generation is
// available after a failed proposal, preserves the read-only path, and never
// executes the failed attempt.
func TestCommitRetryAfterTimeoutStartsAFreshRequest(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	var calls atomic.Int32
	var executes atomic.Int32
	p.SetCommitHooks(CommitHooks{
		Propose: func(context.Context) (*commit.Request, error) {
			if calls.Add(1) == 1 {
				return nil, &commit.ProposalError{
					Kind:        commit.ErrModelTimeout,
					Message:     commit.ErrModelTimeout.Error(),
					Cause:       context.DeadlineExceeded,
					Diagnostics: commit.ProposalDiagnostics{CancellationReason: "timeout", Elapsed: time.Minute},
				}
			}
			request := commitRequestFixture()
			request.Proposal.Summary = "recovered proposal"
			return request, nil
		},
		Execute: func(context.Context, *commit.Request) (commit.Result, error) {
			executes.Add(1)
			return commit.Result{}, nil
		},
	})

	p.Update(p.runCommand(Command{Name: "commit"})())
	if p.state.CommitPhase() != CommitFailed {
		t.Fatalf("phase = %q, want failed", p.state.CommitPhase())
	}
	retry := p.runCommand(Command{Name: "commit", Args: []string{"--retry"}})
	if retry == nil {
		t.Fatal("/commit --retry did not start a fresh request")
	}
	if p.state.CommitPhase() != CommitProposing || !p.state.CommitActive() {
		t.Fatalf("phase = %q active=%v, want a fresh proposal", p.state.CommitPhase(), p.state.CommitActive())
	}
	p.Update(retry())
	if p.state.Pending == nil || p.state.Pending.Commit == nil {
		t.Fatal("retry did not open the recovered proposal")
	}
	if got := p.state.Pending.Commit.Subject; !strings.Contains(got, "recovered proposal") {
		t.Errorf("subject = %q", got)
	}
	if got := calls.Load(); got != 2 {
		t.Errorf("Propose calls = %d, want 2", got)
	}
	if got := executes.Load(); got != 0 {
		t.Errorf("Execute calls = %d, retry must remain read-only", got)
	}
}

// TestCommitEditIsRefusedAfterATimeout proves no empty or fabricated proposal
// is offered to the editor when there is nothing valid to edit.
func TestCommitEditIsRefusedAfterATimeout(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	p.SetCommitHooks(CommitHooks{
		Propose: func(context.Context) (*commit.Request, error) {
			return nil, &commit.ProposalError{
				Kind:        commit.ErrModelTimeout,
				Message:     commit.ErrModelTimeout.Error(),
				Diagnostics: commit.ProposalDiagnostics{CancellationReason: "timeout"},
			}
		},
		Execute: func(context.Context, *commit.Request) (commit.Result, error) {
			return commit.Result{}, nil
		},
	})
	p.Update(p.runCommand(Command{Name: "commit"})())

	p.runCommand(Command{Name: "commit", Args: []string{"--edit"}})
	if p.commitEditing || p.state.CommitPhase() == CommitEditing {
		t.Fatal("the editor opened without a valid proposal")
	}
	if p.state.Input != "" {
		t.Errorf("composer input = %q, want no fabricated message", p.state.Input)
	}
	if p.commitRequest != nil {
		t.Error("a request was fabricated for the editor")
	}
	if text := noticesText(p); !strings.Contains(text, "no proposal to edit") {
		t.Errorf("notices = %q, want the refusal", text)
	}
}
