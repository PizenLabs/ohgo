package tui

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/PizenLabs/ohgo/internal/commit"
	tea "github.com/charmbracelet/bubbletea"
)

func commitHooksFixture() CommitHooks {
	return CommitHooks{
		Propose: func(context.Context) (*commit.Request, error) {
			return commitRequestFixture(), nil
		},
		Execute: func(context.Context, *commit.Request) (commit.Result, error) {
			return commit.Result{SHA: "abcdef123456", Clean: true}, nil
		},
	}
}

func openCommitProposal(t *testing.T, p *Program) {
	t.Helper()
	p.commitRequest = commitRequestFixture()
	p.showCommitProposal(p.commitRequest, "One Git commit will be created with the exact scope below.")
}

func TestParseCommitArgsRejectsConflictingFlags(t *testing.T) {
	if _, _, message := parseCommitArgs([]string{"--retry", "--edit"}); !strings.Contains(message, "choose either") {
		t.Errorf("conflicting args = %q, want the explicit refusal", message)
	}
	if _, _, message := parseCommitArgs([]string{"--unknown"}); !strings.Contains(message, "unknown argument") {
		t.Errorf("unknown arg = %q, want the usage line", message)
	}
	if retry, edit, message := parseCommitArgs([]string{"-r"}); !retry || edit || message != "" {
		t.Errorf("retry args = %v/%v/%q", retry, edit, message)
	}
	if retry, edit, message := parseCommitArgs([]string{"-e"}); retry || !edit || message != "" {
		t.Errorf("edit args = %v/%v/%q", retry, edit, message)
	}
}

func TestStartCommitRefusesUnavailableAndRacingStates(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	if cmd := p.startCommitProposal(false); cmd != nil {
		t.Error("a program without commit hooks started a proposal")
	}
	if text := noticesText(p); !strings.Contains(text, "unavailable") {
		t.Errorf("notices = %q, want the unavailable refusal", text)
	}

	p = NewProgram(nil, "test", 0)
	p.SetCommitHooks(commitHooksFixture())
	p.commitRunning = true
	if cmd := p.startCommitProposal(false); cmd != nil {
		t.Error("a second commit execution started while one was running")
	}
	if text := noticesText(p); !strings.Contains(text, "already executing") {
		t.Errorf("notices = %q, want the execution refusal", text)
	}

	p = NewProgram(nil, "test", 0)
	p.SetCommitHooks(commitHooksFixture())
	p.commitRequest = commitRequestFixture()
	if cmd := p.startCommitProposal(false); cmd != nil {
		t.Error("a new proposal replaced one awaiting review")
	}
	if text := noticesText(p); !strings.Contains(text, "awaiting review") {
		t.Errorf("notices = %q, want the review refusal", text)
	}

	p = NewProgram(nil, "test", 0)
	p.SetCommitHooks(commitHooksFixture())
	p.commitEditing = true
	if cmd := p.startCommitProposal(false); cmd != nil {
		t.Error("a proposal started while the message editor was open")
	}
	if text := noticesText(p); !strings.Contains(text, "finish or cancel the commit message edit") {
		t.Errorf("notices = %q, want the edit refusal", text)
	}

	p = NewProgram(nil, "test", 0)
	p.SetCommitHooks(commitHooksFixture())
	p.state.Apply(ApprovalAsked{ToolName: "other"})
	if cmd := p.startCommitProposal(false); cmd != nil {
		t.Error("a commit proposal started behind another overlay")
	}
	if text := noticesText(p); !strings.Contains(text, "close the open panel") {
		t.Errorf("notices = %q, want the overlay refusal", text)
	}
}

func TestStartCommitReportsPrepareFailures(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	p.SetCommitHooks(CommitHooks{
		Prepare: func() (func(context.Context) (*commit.Request, error), error) {
			return nil, errors.New("no configuration")
		},
		Execute: commitHooksFixture().Execute,
	})
	if cmd := p.startCommitProposal(false); cmd != nil {
		t.Error("a failed preparation still started a request")
	}
	if p.state.CommitPhase() != CommitFailed || p.commitProposing {
		t.Errorf("phase = %q proposing=%v, want a stopped failure", p.state.CommitPhase(), p.commitProposing)
	}
	if text := noticesText(p); !strings.Contains(text, "no configuration") {
		t.Errorf("notices = %q, want the preparation failure", text)
	}

	p = NewProgram(nil, "test", 0)
	p.SetCommitHooks(CommitHooks{
		Prepare: func() (func(context.Context) (*commit.Request, error), error) {
			return nil, nil
		},
		Execute: commitHooksFixture().Execute,
	})
	if cmd := p.startCommitProposal(false); cmd != nil {
		t.Error("a nil prepared function still started a request")
	}
	if text := noticesText(p); !strings.Contains(text, "unavailable") {
		t.Errorf("notices = %q, want the unavailable refusal", text)
	}
}

func TestStartCommitEditRequiresAProposal(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	if cmd := p.startCommit([]string{"--edit"}); cmd != nil {
		t.Error("editing started without a proposal")
	}
	if text := noticesText(p); !strings.Contains(text, "no proposal to edit") {
		t.Errorf("notices = %q, want the edit refusal", text)
	}
}

func TestOnCommitProposedRejectsEmptyAndRacingAnswers(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	p.commitProposing = true
	p.onCommitProposed(commitProposedMsg{seq: p.commitSeq})
	if p.state.CommitPhase() != CommitFailed || p.commitProposing {
		t.Errorf("phase = %q proposing=%v, want a stopped failure", p.state.CommitPhase(), p.commitProposing)
	}
	if text := noticesText(p); !strings.Contains(text, "did not produce a valid proposal") {
		t.Errorf("notices = %q, want the invalid-proposal notice", text)
	}

	p = NewProgram(nil, "test", 0)
	p.state.Status.Busy = true
	p.onCommitProposed(commitProposedMsg{seq: p.commitSeq, request: commitRequestFixture()})
	if p.state.CommitPhase() != CommitCancelled || p.commitRequest != nil || p.state.Pending != nil {
		t.Errorf("phase = %q request=%v pending=%v, want the proposal discarded",
			p.state.CommitPhase(), p.commitRequest, p.state.Pending)
	}
	if text := noticesText(p); !strings.Contains(text, "discarded") {
		t.Errorf("notices = %q, want the state-change notice", text)
	}
}

func TestCommitViewBoundsAHugeScope(t *testing.T) {
	request := commitRequestFixture()
	request.Snapshot.Scope = nil
	for i := 0; i < commitFilesShown+1; i++ {
		request.Snapshot.Scope = append(request.Snapshot.Scope, commit.FileChange{
			Path:     "file" + string(rune('a'+i)),
			Worktree: 'M',
		})
	}
	view := commitView(request)
	if len(view.Files) != commitFilesShown {
		t.Errorf("files shown = %d, want the bounded %d", len(view.Files), commitFilesShown)
	}
	if !strings.Contains(view.Note, "13 files in scope; showing the first 12.") {
		t.Errorf("note = %q, want the bound stated explicitly", view.Note)
	}
}

func TestBeginCommitEditRefusesBusyStates(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	p.commitRequest = commitRequestFixture()
	p.state.Status.Busy = true
	p.beginCommitEdit()
	if p.commitEditing {
		t.Error("the message editor opened during a busy turn")
	}
	if text := noticesText(p); !strings.Contains(text, "wait for the current operation") {
		t.Errorf("notices = %q", text)
	}
}

func TestSubmitCommitEditRequiresALiveProposal(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	p.commitEditing = true
	p.submitCommitEdit()
	if p.commitEditing || p.state.CommitPhase() != CommitFailed {
		t.Errorf("editing=%v phase=%q, want a stopped failure", p.commitEditing, p.state.CommitPhase())
	}
	if text := noticesText(p); !strings.Contains(text, "proposal disappeared") {
		t.Errorf("notices = %q", text)
	}
}

func TestCancelCommitEditRestoresOrCancels(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	p.commitEditing = true
	p.cancelCommitEdit()
	if p.commitEditing || p.state.CommitPhase() != CommitCancelled {
		t.Errorf("editing=%v phase=%q, want cancellation without a request", p.commitEditing, p.state.CommitPhase())
	}
	if text := noticesText(p); !strings.Contains(text, "commit edit cancelled") {
		t.Errorf("notices = %q", text)
	}

	p = NewProgram(nil, "test", 0)
	openCommitProposal(t, p)
	p.commitEditing = true
	p.state.Apply(ComposerChanged{Text: "partial edit"})
	p.cancelCommitEdit()
	if p.commitEditing || p.state.CommitPhase() != CommitReview {
		t.Errorf("editing=%v phase=%q, want the original review restored", p.commitEditing, p.state.CommitPhase())
	}
	if p.state.Pending == nil || p.state.Pending.Commit == nil {
		t.Error("cancelling the edit did not reopen the review dialog")
	}
	if p.state.Input != "" {
		t.Errorf("input = %q, want the editor cleared", p.state.Input)
	}
}

func TestRetryCommitRequiresAndReplacesAProposal(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	p.retryCommit()
	if text := noticesText(p); !strings.Contains(text, "nothing to retry") {
		t.Errorf("notices = %q, want the retry refusal", text)
	}

	p = NewProgram(nil, "test", 0)
	p.SetCommitHooks(commitHooksFixture())
	openCommitProposal(t, p)
	if _, cmd := p.retryCommit(); cmd == nil {
		t.Fatal("retry did not start a new proposal")
	}
	if p.state.CommitPhase() != CommitProposing {
		t.Errorf("phase = %q, want proposing", p.state.CommitPhase())
	}
	if p.state.Pending != nil || p.commitRequest != nil {
		t.Error("retry kept the replaced proposal live")
	}
}

func TestHandleCommitKeyRoutesEverySecondaryAction(t *testing.T) {
	for _, key := range []tea.KeyMsg{
		{Type: tea.KeyDown},
		{Type: tea.KeyTab},
		{Type: tea.KeyRunes, Runes: []rune("j")},
	} {
		p := NewProgram(nil, "test", 0)
		openCommitProposal(t, p)
		if _, cmd := p.handleCommitKey(key); cmd != nil {
			t.Errorf("%s produced a command, want only cursor movement", key.String())
		}
	}

	p := NewProgram(nil, "test", 0)
	openCommitProposal(t, p)
	if _, cmd := p.handleCommitKey(tea.KeyMsg{Type: tea.KeyCtrlE}); cmd != nil {
		t.Error("ctrl+e produced a command")
	}
	if !p.commitEditing || p.state.CommitPhase() != CommitEditing {
		t.Errorf("editing=%v phase=%q, want the message editor", p.commitEditing, p.state.CommitPhase())
	}

	p = NewProgram(nil, "test", 0)
	p.SetCommitHooks(commitHooksFixture())
	openCommitProposal(t, p)
	if _, cmd := p.handleCommitKey(tea.KeyMsg{Type: tea.KeyCtrlR}); cmd == nil {
		t.Error("ctrl+r did not start a retry")
	}

	p = NewProgram(nil, "test", 0)
	_, cmd := p.handleCommitKey(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil {
		t.Error("enter without a dialog executed something")
	}
	if p.state.CommitPhase() != CommitCancelled {
		t.Errorf("phase = %q, want cancellation", p.state.CommitPhase())
	}
}

func TestAnswerCommitExplainsDenialAndMissingHooks(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	p.answerCommit(false, "superseded by a new session")
	if text := noticesText(p); !strings.Contains(text, "commit cancelled: superseded by a new session") {
		t.Errorf("notices = %q, want the explicit reason", text)
	}

	p = NewProgram(nil, "test", 0)
	p.commitRequest = commitRequestFixture()
	p.answerCommit(true, "")
	if p.state.CommitPhase() != CommitFailed || p.commitRunning {
		t.Errorf("phase = %q running=%v, want a stopped failure", p.state.CommitPhase(), p.commitRunning)
	}
	if text := noticesText(p); !strings.Contains(text, "unavailable") {
		t.Errorf("notices = %q, want the execute-hook refusal", text)
	}
}

func TestOnCommitFinishedReportsAFailedVerificationWithItsSHAs(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	msg := commitFinishedMsg{
		seq:    p.commitSeq,
		result: commit.Result{SHA: "abcdef123456", Subject: "fix(scope): x", Files: []commit.FileChange{{Path: "a.go"}}},
		err:    errors.New("commit verification failed"),
	}
	p.onCommitFinished(msg)
	if p.state.CommitPhase() != CommitFailed || p.commitRunning {
		t.Errorf("phase = %q running=%v, want a stopped failure", p.state.CommitPhase(), p.commitRunning)
	}
	text := noticesText(p)
	if !strings.Contains(text, "commit verification failed") || !strings.Contains(text, "Committed abcdef1") {
		t.Errorf("notices = %q, want both the failure and the real SHA", text)
	}
}

func TestCancelCommitProposalIsANoOpWhenIdle(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	before := p.state.CommitPhase()
	p.cancelCommitProposal()
	if p.state.CommitPhase() != before {
		t.Errorf("phase = %q, want the idle phase unchanged", p.state.CommitPhase())
	}
	if text := noticesText(p); text != "" {
		t.Errorf("notices = %q, want no notice for an idle cancel", text)
	}
}

func TestEditingEscapeCancelsTheEditNotTheProposal(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	openCommitProposal(t, p)
	p.beginCommitEdit()
	p.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if p.commitEditing {
		t.Error("esc left the editor open")
	}
	if p.state.CommitPhase() != CommitReview {
		t.Errorf("phase = %q, want the proposal restored", p.state.CommitPhase())
	}
	if p.commitRequest == nil {
		t.Error("esc cancelled the proposal instead of only the edit")
	}
}

func TestProjectThinkingNamesCommitExecution(t *testing.T) {
	state := State{Commit: &CommitActivity{Phase: CommitExecuting, StartedAt: time.Now()}}
	got := ProjectThinking(state)
	if got.Phase != ThinkingCommit || got.Label != "Creating commit…" {
		t.Errorf("ProjectThinking = %+v, want the execution label", got)
	}
	if got.Interruptible {
		t.Error("an execution was advertised as interruptible")
	}
}
