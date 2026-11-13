package tui

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/PizenLabs/ohgo/internal/commit"
	tea "github.com/charmbracelet/bubbletea"
)

// planFixture builds a plan with n logical groups and a stable fingerprint.
func planFixture(n int) *commit.Plan {
	plan := &commit.Plan{Total: n, Fingerprint: "fp"}
	for i := 0; i < n; i++ {
		key := fmt.Sprintf("group-%d", i+1)
		plan.Groups = append(plan.Groups, commit.ChangeGroup{
			Key:        key,
			Label:      key,
			Scope:      "scope",
			Order:      i + 1,
			Files:      []commit.FileChange{{Path: fmt.Sprintf("src/file-%d.go", i+1)}},
			Confidence: commit.GroupStrong,
		})
	}
	return plan
}

// plannedRequestFixture projects one planned group as the existing proposal
// surface presents it.
func plannedRequestFixture(plan *commit.Plan, key string) *commit.Request {
	group, ok := plan.Group(key)
	if !ok {
		group = plan.Groups[0]
	}
	return &commit.Request{
		Proposal: commit.Proposal{
			Type:    "feat",
			Scope:   "scope",
			Summary: "update files",
			Body:    []string{"- update the planned files"},
		},
		Snapshot: &commit.Snapshot{
			Mode:  commit.ModeWorktree,
			Scope: group.Files,
			Auto:  true,
		},
		Progress: &commit.PlanProgress{
			Position: group.Order,
			Total:    len(plan.Groups),
			Key:      group.Key,
		},
	}
}

// planHooksFixture wires a two-commit plan that shrinks after each execute.
func planHooksFixture(remaining *atomic.Int32, executes *atomic.Int32, propose func(*commit.Plan, string) (*commit.Request, error)) CommitHooks {
	return CommitHooks{
		PreparePlan: func() (CommitPlanHooks, error) {
			return CommitPlanHooks{
				Plan: func(context.Context) (*commit.Plan, error) {
					return planFixture(int(remaining.Load())), nil
				},
				ProposeGroup: func(_ context.Context, plan *commit.Plan, key string) (*commit.Request, error) {
					if propose != nil {
						return propose(plan, key)
					}
					return plannedRequestFixture(plan, key), nil
				},
			}, nil
		},
		Execute: func(_ context.Context, request *commit.Request) (commit.Result, error) {
			executes.Add(1)
			remaining.Add(-1)
			return commit.Result{
				SHA:     "abcdef123456",
				Subject: request.Proposal.Subject(),
				Files:   request.Snapshot.Scope,
			}, nil
		},
	}
}

// startPlannedCommit drives the command and returns the first proposal command.
func startPlannedCommit(t *testing.T, p *Program) tea.Cmd {
	t.Helper()
	cmd := p.runCommand(Command{Name: "commit", Args: []string{"-a"}})
	if cmd == nil {
		t.Fatal("/commit -a produced no plan command")
	}
	_, proposal := p.Update(cmd())
	if proposal == nil {
		t.Fatal("/commit -a produced no first proposal")
	}
	return proposal
}

// approveCurrent moves the resting cursor to Create commit and confirms.
func approveCurrent(t *testing.T, p *Program) tea.Cmd {
	t.Helper()
	p.Update(tea.KeyMsg{Type: tea.KeyUp})
	_, cmd := p.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("approval produced no execution command")
	}
	return cmd
}

// TestCommitAllRequiresAnApprovalForEveryCommit proves /commit -a automates
// planning and sequencing, never authorization: one Enter creates one commit.
func TestCommitAllRequiresAnApprovalForEveryCommit(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	var remaining atomic.Int32
	remaining.Store(2)
	var executes atomic.Int32
	p.SetCommitHooks(planHooksFixture(&remaining, &executes, nil))

	first := startPlannedCommit(t, p)
	p.Update(first())
	if p.state.Pending == nil || p.state.Pending.Commit == nil {
		t.Fatal("the first proposal did not open")
	}
	view := stripANSI(Render(p.state))
	if !strings.Contains(view, "Commit 1 / 2") {
		t.Errorf("first proposal = %q, want its plan position", view)
	}
	if got := executes.Load(); got != 0 {
		t.Fatalf("executed %d commits before the first approval", got)
	}

	execCmd := approveCurrent(t, p)
	_, refresh := p.Update(execCmd())
	if got := executes.Load(); got != 1 {
		t.Fatalf("executed %d commits after one approval, want 1", got)
	}
	if refresh == nil {
		t.Fatal("a successful commit did not revalidate the remaining plan")
	}
	_, second := p.Update(refresh())
	if second == nil {
		t.Fatal("the second proposal did not start")
	}
	p.Update(second())
	if p.state.Pending == nil || p.state.Pending.Commit == nil {
		t.Fatal("the second proposal did not open")
	}
	view = stripANSI(Render(p.state))
	if !strings.Contains(view, "Commit 1 / 1") && !strings.Contains(view, "Commit 2 / 2") {
		t.Errorf("second proposal = %q, want its plan position", view)
	}
	if got := executes.Load(); got != 1 {
		t.Fatalf("the second commit ran without a second approval (%d)", got)
	}

	execCmd = approveCurrent(t, p)
	_, done := p.Update(execCmd())
	if got := executes.Load(); got != 2 {
		t.Fatalf("executed %d commits after two approvals, want 2", got)
	}
	if done != nil {
		p.Update(done())
	}
	if p.commitAuto {
		t.Error("the plan stayed active after the last commit")
	}
	if !strings.Contains(lastNotice(p), "Commit plan complete: 2 commits created.") {
		t.Errorf("notice = %q, want the completion line", lastNotice(p))
	}
}

// TestCommitAllEmptyTreeExitsCleanly proves /commit -a opens no proposal when
// there is nothing logical to commit.
func TestCommitAllEmptyTreeExitsCleanly(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	p.SetCommitHooks(CommitHooks{
		PreparePlan: func() (CommitPlanHooks, error) {
			return CommitPlanHooks{
				Plan: func(context.Context) (*commit.Plan, error) {
					return nil, commit.ErrNoChanges
				},
				ProposeGroup: func(context.Context, *commit.Plan, string) (*commit.Request, error) {
					return nil, nil
				},
			}, nil
		},
		Execute: func(context.Context, *commit.Request) (commit.Result, error) {
			return commit.Result{}, nil
		},
	})
	cmd := p.runCommand(Command{Name: "commit", Args: []string{"-a"}})
	if cmd == nil {
		t.Fatal("/commit -a produced no plan command")
	}
	p.Update(cmd())
	if p.state.Pending != nil || p.commitAuto || p.commitRequest != nil {
		t.Errorf("empty tree opened a proposal: pending=%v auto=%v request=%v",
			p.state.Pending != nil, p.commitAuto, p.commitRequest != nil)
	}
	if !strings.Contains(lastNotice(p), "No changes to commit.") {
		t.Errorf("notice = %q, want the clean empty-tree line", lastNotice(p))
	}
}

// TestCommitAllCancelAfterOneCommitLeavesTheRestUntouched proves cancellation
// is not approval and does not continue in the background.
func TestCommitAllCancelAfterOneCommitLeavesTheRestUntouched(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	var remaining atomic.Int32
	remaining.Store(2)
	var executes atomic.Int32
	p.SetCommitHooks(planHooksFixture(&remaining, &executes, nil))

	first := startPlannedCommit(t, p)
	p.Update(first())
	execCmd := approveCurrent(t, p)
	_, refresh := p.Update(execCmd())
	_, second := p.Update(refresh())
	p.Update(second())
	if p.state.Pending == nil || p.state.Pending.Commit == nil {
		t.Fatal("the second proposal did not open")
	}

	p.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if got := executes.Load(); got != 1 {
		t.Errorf("executed %d commits, want only the approved one", got)
	}
	if p.commitAuto || p.commitPlan != nil || p.commitRequest != nil {
		t.Errorf("plan state survived cancellation: auto=%v plan=%v request=%v",
			p.commitAuto, p.commitPlan != nil, p.commitRequest != nil)
	}
	if p.state.Pending != nil {
		t.Error("the cancelled proposal is still answerable")
	}
	if !strings.Contains(lastNotice(p), "stopped after 1 of 2") ||
		!strings.Contains(lastNotice(p), "remaining changes are untouched") {
		t.Errorf("notice = %q, want truthful partial completion", lastNotice(p))
	}
}

// TestCommitAllEditDoesNotCallTheModel proves human wording changes stay local:
// editing validates deterministically and does not regenerate or alter scope.
func TestCommitAllEditDoesNotCallTheModel(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	var remaining atomic.Int32
	remaining.Store(2)
	var executes, proposals atomic.Int32
	hooks := planHooksFixture(&remaining, &executes, func(plan *commit.Plan, key string) (*commit.Request, error) {
		proposals.Add(1)
		return plannedRequestFixture(plan, key), nil
	})
	p.SetCommitHooks(hooks)

	first := startPlannedCommit(t, p)
	p.Update(first())
	before := proposals.Load()
	p.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("e")})
	if !p.commitEditing {
		t.Fatal("the edit action did not enter the composer editor")
	}
	p.state.Apply(ComposerChanged{Text: "feat(scope): edited wording", Cursor: 27})
	p.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if p.commitEditing {
		t.Fatal("the validated edit did not return to review")
	}
	if got := proposals.Load(); got != before {
		t.Errorf("editing called the model %d more times", got-before)
	}
	if p.commitRequest == nil || p.commitRequest.Proposal.Subject() != "feat(scope): edited wording" {
		t.Errorf("edited proposal = %+v", p.commitRequest)
	}
	if got := executes.Load(); got != 0 {
		t.Errorf("editing executed %d commits", got)
	}
}

// TestCommitAllStaleProposalReplansWithoutExecuting proves stale scope is never
// staged; the control plane rebuilds the remaining plan instead.
func TestCommitAllStaleProposalReplansWithoutExecuting(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	var remaining atomic.Int32
	remaining.Store(2)
	var executes, proposals atomic.Int32
	hooks := planHooksFixture(&remaining, &executes, func(plan *commit.Plan, key string) (*commit.Request, error) {
		if proposals.Add(1) == 1 {
			return nil, commit.ErrPlanStale
		}
		return plannedRequestFixture(plan, key), nil
	})
	p.SetCommitHooks(hooks)

	first := startPlannedCommit(t, p)
	_, refresh := p.Update(first())
	if refresh == nil {
		t.Fatal("a stale proposal did not trigger a replan")
	}
	_, second := p.Update(refresh())
	if second == nil {
		t.Fatal("the replanned proposal did not start")
	}
	p.Update(second())
	if got := executes.Load(); got != 0 {
		t.Errorf("a stale plan executed %d commits", got)
	}
	if p.state.Pending == nil || p.state.Pending.Commit == nil {
		t.Fatal("the replanned proposal did not open for review")
	}
}

// TestCommitAllFailureStopsLaterProposals proves a failed commit does not
// continue down the plan.
func TestCommitAllFailureStopsLaterProposals(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	var remaining atomic.Int32
	remaining.Store(2)
	var executes, proposals atomic.Int32
	hooks := planHooksFixture(&remaining, &executes, func(plan *commit.Plan, key string) (*commit.Request, error) {
		if proposals.Add(1) == 2 {
			return nil, commit.ErrModel
		}
		return plannedRequestFixture(plan, key), nil
	})
	p.SetCommitHooks(hooks)

	first := startPlannedCommit(t, p)
	p.Update(first())
	execCmd := approveCurrent(t, p)
	_, refresh := p.Update(execCmd())
	_, second := p.Update(refresh())
	if second == nil {
		t.Fatal("the second proposal did not start")
	}
	p.Update(second())
	if got := executes.Load(); got != 1 {
		t.Errorf("executed %d commits, want only the successful one", got)
	}
	if p.commitAuto || p.commitPlan != nil {
		t.Error("the plan continued after a proposal failure")
	}
	if p.state.Pending != nil {
		t.Error("a proposal dialog remained open after the failure")
	}
}
