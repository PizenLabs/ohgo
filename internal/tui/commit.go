package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/PizenLabs/ohgo/internal/commit"
	tea "github.com/charmbracelet/bubbletea"
)

// CommitPhase is the explicit /commit lifecycle. It is stored in State so the
// activity line, the header, and tests all read the same transition instead of
// inferring progress from whichever async flag happens to be set.
type CommitPhase string

const (
	// CommitIdle means no commit workflow is active.
	CommitIdle CommitPhase = ""
	// CommitProposing means bounded evidence is being collected and the
	// COMMIT model is writing a proposal. Nothing is staged.
	CommitProposing CommitPhase = "proposing"
	// CommitReview means a validated proposal is waiting for the human.
	CommitReview CommitPhase = "review"
	// CommitEditing means the human is editing the proposed message. The
	// repository is untouched and no model call is in flight.
	CommitEditing CommitPhase = "editing"
	// CommitExecuting means the approved scope is being staged and committed.
	CommitExecuting CommitPhase = "executing"
	// CommitFailed is a terminal proposal or execution failure.
	CommitFailed CommitPhase = "failed"
	// CommitCancelled is a terminal human cancellation.
	CommitCancelled CommitPhase = "cancelled"
	// CommitDone is a terminal verified commit.
	CommitDone CommitPhase = "done"
)

// CommitActivity is the current lifecycle state plus the truthful start time
// the activity line uses for its elapsed segment.
type CommitActivity struct {
	// Phase is the lifecycle state.
	Phase CommitPhase
	// StartedAt is when the active phase began. Zero means unknown, which
	// suppresses elapsed time rather than fabricating one.
	StartedAt time.Time
	// Text is an internal diagnostic or the terminal notice, never rendered
	// as a spinner label.
	Text string
}

// Active reports whether the phase represents work still in flight. Terminal
// and human-waiting phases stop the spinner.
func (c *CommitActivity) Active() bool {
	if c == nil {
		return false
	}
	return c.Phase == CommitProposing || c.Phase == CommitExecuting
}

// CommitActive reports an in-flight /commit operation.
func (s State) CommitActive() bool { return s.Commit.Active() }

// CommitPhase reports the current lifecycle phase, or CommitIdle.
func (s State) CommitPhase() CommitPhase {
	if s.Commit == nil {
		return CommitIdle
	}
	return s.Commit.Phase
}

// CommitChanged moves the /commit lifecycle. It is the only write path, so a
// renderer can never observe a phase the runtime did not publish.
type CommitChanged struct {
	// Phase is the next lifecycle state.
	Phase CommitPhase
	// At is when the phase began. Zero keeps the previous start for an active
	// phase and means "unknown" for a new one.
	At time.Time
	// Text is an internal diagnostic or terminal notice.
	Text string
}

func (CommitChanged) eventKind() string { return "commit_changed" }

// CommitHooks are the host's /commit workflow.
//
// Prepare/Propose are read-only and own the commit model. Execute is called
// only after the human answers the proposal; the program never stages or
// commits on its own. Hooks are nil-safe, so a program without a repository
// reports the command unavailable instead of panicking.
type CommitHooks struct {
	// Prepare snapshots host configuration on the update goroutine and
	// returns the asynchronous proposal function bound to that snapshot. It
	// must be quick and must not perform network I/O: the returned function
	// runs off the update goroutine. Prepare is preferred over Propose so a
	// concurrent /model assignment cannot race the configuration read.
	Prepare func() (func(ctx context.Context) (*commit.Request, error), error)
	// Propose inspects the repository and asks the dedicated commit model for
	// one validated proposal. It is the simple host/test seam used when
	// Prepare is nil.
	Propose func(ctx context.Context) (*commit.Request, error)
	// PreparePlan snapshots host configuration for an automatic plan, like
	// Prepare does for a single proposal. Preferred over Plan/ProposeGroup.
	PreparePlan func() (CommitPlanHooks, error)
	// Plan builds the complete logical commit plan. It is read-only.
	Plan func(ctx context.Context) (*commit.Plan, error)
	// ProposeGroup asks the COMMIT model for a message for one planned group.
	// The host revalidates the plan against current Git state.
	ProposeGroup func(ctx context.Context, plan *commit.Plan, key string) (*commit.Request, error)
	// Execute stages and commits exactly the approved request.
	Execute func(ctx context.Context, request *commit.Request) (commit.Result, error)
}

// CommitPlanHooks are one automatic plan bound to a configuration snapshot.
//
// Both functions must use the same snapshot so a /model reassignment cannot
// split the plan builder's scope from the proposer's model.
type CommitPlanHooks struct {
	Plan         func(ctx context.Context) (*commit.Plan, error)
	ProposeGroup func(ctx context.Context, plan *commit.Plan, key string) (*commit.Request, error)
}

// CommitProposalView is the review payload rendered inside the permission
// dialog. It is a projection of commit.Request, not a second source of truth:
// the exact text executed is the Proposal the host validated.
type CommitProposalView struct {
	// Subject is the approved first line.
	Subject string
	// Body is the optional bullet list.
	Body []string
	// Files is the exact path scope, preformatted with status codes.
	Files []string
	// Note explains what the scope includes and excludes.
	Note string
	// Progress is the "Commit 2 / 4" line for an automatic plan.
	Progress string
}

// commitFilesShown bounds the path list in one dialog. A larger scope is
// printed in full to the transcript first, so the dialog stays readable
// without hiding the exact paths the approval covers.
const commitFilesShown = 12

// commitChoices put the affirmative first and the conservative answer last, so
// the resting cursor stays on cancel.
var commitChoices = []string{"Create commit", "Cancel"}

// commitApproveChoice is the exact label that authorizes execution.
const commitApproveChoice = "Create commit"

// commitProposedMsg delivers the result of the read-only proposal phase.
type commitProposedMsg struct {
	seq     int
	request *commit.Request
	err     error
}

// commitPlannedMsg delivers a complete logical commit plan.
type commitPlannedMsg struct {
	seq   int
	plan  *commit.Plan
	err   error
	after bool
}

// commitFinishedMsg delivers the one attempted execution.
type commitFinishedMsg struct {
	seq    int
	result commit.Result
	err    error
}

// commitUsage is the one-line argument contract for the specialized command.
const commitUsage = "usage: /commit [--all|-a] [--retry|--edit]"

// startCommit dispatches the /commit workflow from the composer.
//
// Nothing is staged here. The command is refused while a turn or another
// proposal/execution owns the session, because two commits racing would make
// the exactly-one invariant impossible to state.
func (p *Program) startCommit(args []string) tea.Cmd {
	options, argErr := parseCommitOptions(args)
	if argErr != "" {
		p.state.Apply(Notice{Text: argErr})
		return nil
	}
	if options.edit {
		p.beginCommitEdit()
		return nil
	}
	if options.all {
		return p.startCommitPlan(options.retry)
	}
	return p.startCommitProposal(options.retry)
}

// commitOptions is the parsed /commit argument contract.
type commitOptions struct {
	retry bool
	edit  bool
	all   bool
}

// parseCommitOptions accepts the documented flags only. A free-text argument
// is refused rather than forwarded to a generic turn: /commit is the Git
// operator, never a BUILD prompt.
func parseCommitOptions(args []string) (commitOptions, string) {
	var options commitOptions
	for _, raw := range args {
		switch strings.ToLower(strings.TrimSpace(raw)) {
		case "--retry", "-r":
			options.retry = true
		case "--edit", "-e":
			options.edit = true
		case "--all", "-a":
			options.all = true
		default:
			return commitOptions{}, fmt.Sprintf("commit: unknown argument %q; %s", raw, commitUsage)
		}
	}
	if options.retry && options.edit {
		return commitOptions{}, "commit: choose either --retry or --edit"
	}
	if options.all && (options.retry || options.edit) {
		return commitOptions{}, "commit: --all cannot be combined with --retry or --edit"
	}
	return options, ""
}

// parseCommitArgs is the legacy single-proposal parser retained for callers
// that only need the retry/edit shape.
func parseCommitArgs(args []string) (retry, edit bool, message string) {
	options, message := parseCommitOptions(args)
	return options.retry, options.edit, message
}

// startCommitProposal begins one read-only proposal generation. retry
// invalidates an earlier proposal or in-flight generation; the repository is
// never touched by either path.
func (p *Program) startCommitProposal(retry bool) tea.Cmd {
	if (p.commitHooks.Propose == nil && p.commitHooks.Prepare == nil) || p.commitHooks.Execute == nil {
		p.state.Apply(Notice{Text: "commit is unavailable in this session"})
		return nil
	}
	if p.state.Status.Busy {
		p.state.Apply(Notice{Text: "wait for the turn to finish before /commit"})
		return nil
	}
	if p.commitRunning {
		p.state.Apply(Notice{Text: "a commit is already executing"})
		return nil
	}
	if p.commitProposing && !retry {
		p.state.Apply(Notice{Text: "a commit proposal is already being prepared"})
		return nil
	}
	if !retry && p.commitRequest != nil {
		p.state.Apply(Notice{Text: "a commit proposal is already awaiting review"})
		return nil
	}
	if p.commitEditing {
		if !retry {
			p.state.Apply(Notice{Text: "finish or cancel the commit message edit first"})
			return nil
		}
		p.cancelCommitEdit()
	}
	// A retry supersedes the previous proposal. Clear its dialog before the
	// generic overlay check, because that dialog is the surface being
	// replaced rather than an unrelated panel.
	if retry && p.state.Pending != nil && p.state.Pending.Commit != nil {
		p.dropCommitProposal()
	}
	if p.otherOverlayOpen() {
		p.state.Apply(Notice{Text: "close the open panel before /commit"})
		return nil
	}
	if retry {
		p.dropCommitProposal()
	}
	// Cancel any earlier generation before starting the next one. The stale
	// message is still guarded by seq when it arrives.
	p.stopCommitRequest(commit.ErrSuperseded)
	p.commitSeq++
	seq := p.commitSeq
	p.commitProposing = true

	propose := p.commitHooks.Propose
	if p.commitHooks.Prepare != nil {
		prepared, err := p.commitHooks.Prepare()
		if err != nil {
			p.commitProposing = false
			p.state.Apply(CommitChanged{Phase: CommitFailed, Text: err.Error()})
			p.state.Apply(Notice{Text: commit.AbortText(err)})
			return nil
		}
		propose = prepared
	}
	if propose == nil {
		p.commitProposing = false
		p.state.Apply(CommitChanged{Phase: CommitFailed, Text: "proposal hook unavailable"})
		p.state.Apply(Notice{Text: "commit is unavailable in this session"})
		return nil
	}

	// Lifecycle only: the bounded wait belongs to commit.Service.Propose.
	// Keeping the parent context free of a turn deadline is what lets an
	// independent interactive /commit survive an unrelated turn.
	ctx, cancel := context.WithCancelCause(context.Background())
	p.commitCancel = cancel
	p.state.Apply(CommitChanged{Phase: CommitProposing, At: time.Now()})

	return func() tea.Msg {
		request, err := propose(ctx)
		return commitProposedMsg{seq: seq, request: request, err: err}
	}
}

// commitPlanAvailable reports the automatic multi-commit workflow.
func (p *Program) commitPlanAvailable() bool {
	planHook := p.commitHooks.PreparePlan != nil ||
		(p.commitHooks.Plan != nil && p.commitHooks.ProposeGroup != nil)
	return planHook && p.commitHooks.Execute != nil
}

// startCommitPlan begins /commit -a: analyze the whole change universe, plan
// every logical commit, and ask for approval one commit at a time. retry keeps
// the number of commits already created and rebuilds the remaining plan from
// current repository state.
func (p *Program) startCommitPlan(retry bool) tea.Cmd {
	if !p.commitPlanAvailable() {
		p.state.Apply(Notice{Text: "automatic commit planning is unavailable in this session"})
		return nil
	}
	if p.state.Status.Busy {
		p.state.Apply(Notice{Text: "wait for the turn to finish before /commit"})
		return nil
	}
	if p.commitRunning {
		p.state.Apply(Notice{Text: "a commit is already executing"})
		return nil
	}
	if p.commitProposing && !retry {
		p.state.Apply(Notice{Text: "a commit proposal is already being prepared"})
		return nil
	}
	if p.commitEditing {
		if !retry {
			p.state.Apply(Notice{Text: "finish or cancel the commit message edit first"})
			return nil
		}
		p.cancelCommitEdit()
	}
	if p.commitAuto && p.commitRequest != nil && !retry {
		p.state.Apply(Notice{Text: "a commit plan is already awaiting review"})
		return nil
	}
	if p.commitRequest != nil && !retry {
		p.state.Apply(Notice{Text: "a commit proposal is already awaiting review"})
		return nil
	}
	if retry && p.state.Pending != nil && p.state.Pending.Commit != nil {
		p.dropCommitProposal()
	}
	if p.otherOverlayOpen() {
		p.state.Apply(Notice{Text: "close the open panel before /commit"})
		return nil
	}
	if retry {
		p.dropCommitProposal()
	}
	p.stopCommitRequest(commit.ErrSuperseded)
	p.commitSeq++
	seq := p.commitSeq
	p.commitProposing = true
	p.commitAuto = true
	p.commitPlan = nil
	if !retry {
		p.commitAutoDone = 0
		p.commitAutoTotal = 0
	}
	return p.beginPlanRequest(seq, retry)
}

// startCommitPlanRefresh rebuilds the remaining plan after one commit, because
// HEAD, the index, and the worktree all changed.
func (p *Program) startCommitPlanRefresh() tea.Cmd {
	if !p.commitAuto || !p.commitPlanAvailable() {
		return nil
	}
	p.stopCommitRequest(commit.ErrSuperseded)
	p.commitSeq++
	seq := p.commitSeq
	p.commitProposing = true
	p.commitPlan = nil
	return p.beginPlanRequest(seq, true)
}

// beginPlanRequest starts the read-only plan request and publishes the phase.
func (p *Program) beginPlanRequest(seq int, after bool) tea.Cmd {
	planFn := p.commitHooks.Plan
	proposeFn := p.commitHooks.ProposeGroup
	if p.commitHooks.PreparePlan != nil {
		prepared, err := p.commitHooks.PreparePlan()
		if err != nil {
			p.commitProposing = false
			p.clearCommitPlan()
			p.state.Apply(CommitChanged{Phase: CommitFailed, Text: err.Error()})
			p.state.Apply(Notice{Text: commit.AbortText(err)})
			return nil
		}
		planFn = prepared.Plan
		if prepared.ProposeGroup != nil {
			proposeFn = prepared.ProposeGroup
		}
	}
	p.commitPlanPropose = proposeFn
	if planFn == nil {
		p.commitProposing = false
		p.clearCommitPlan()
		p.state.Apply(CommitChanged{Phase: CommitFailed, Text: "plan hook unavailable"})
		p.state.Apply(Notice{Text: "automatic commit planning is unavailable in this session"})
		return nil
	}
	ctx, cancel := context.WithCancelCause(context.Background())
	p.commitCancel = cancel
	p.state.Apply(CommitChanged{Phase: CommitProposing, At: time.Now()})
	return func() tea.Msg {
		plan, err := planFn(ctx)
		return commitPlannedMsg{seq: seq, plan: plan, err: err, after: after}
	}
}

// onCommitPlanned opens the first proposal of a plan, reports completion, or
// reports why no plan could be built.
func (p *Program) onCommitPlanned(msg commitPlannedMsg) (tea.Model, tea.Cmd) {
	if msg.seq != p.commitSeq {
		return p, nil
	}
	p.commitProposing = false
	p.stopCommitRequest(nil)
	if msg.err != nil {
		if errors.Is(msg.err, commit.ErrNoChanges) {
			if msg.after && p.commitAutoDone > 0 {
				p.finishCommitPlan("")
				return p, nil
			}
			p.clearCommitPlan()
			p.state.Apply(CommitChanged{Phase: CommitDone})
			p.state.Apply(Notice{Text: "No changes to commit."})
			return p, nil
		}
		partial := p.commitPartialNotice()
		p.clearCommitPlan()
		p.state.Apply(CommitChanged{Phase: CommitFailed, Text: commitFailureText(msg.err)})
		p.state.Apply(Notice{Text: partial + commit.AbortText(msg.err)})
		return p, nil
	}
	if msg.plan == nil || len(msg.plan.Groups) == 0 {
		text := "No logical commits were planned."
		if msg.plan != nil {
			text = commit.FormatPlan(msg.plan)
		}
		if msg.after && p.commitAutoDone > 0 {
			note := ""
			if msg.plan != nil && planHasRemaining(msg.plan) {
				note = commit.PlanSummary(msg.plan)
			}
			p.finishCommitPlan(note)
			return p, nil
		}
		p.clearCommitPlan()
		p.state.Apply(CommitChanged{Phase: CommitDone})
		p.state.Apply(Notice{Text: text})
		return p, nil
	}
	if p.state.Status.Busy || p.commitRunning || p.otherOverlayOpen() {
		p.clearCommitPlan()
		p.state.Apply(CommitChanged{Phase: CommitCancelled})
		p.state.Apply(Notice{Text: "commit plan discarded: the session state changed"})
		return p, nil
	}
	p.commitPlan = msg.plan
	p.commitStaleRetries = 0
	p.commitAutoTotal = p.commitAutoDone + len(msg.plan.Groups)
	p.state.Apply(Notice{Text: commit.FormatPlanFrom(msg.plan, p.commitAutoDone)})
	return p, p.startCommitGroupProposal(msg.plan, msg.plan.Groups[0].Key)
}

// startCommitGroupProposal asks for the message for one planned group.
func (p *Program) startCommitGroupProposal(plan *commit.Plan, key string) tea.Cmd {
	propose := p.commitPlanPropose
	if propose == nil {
		propose = p.commitHooks.ProposeGroup
	}
	if plan == nil || propose == nil {
		p.clearCommitPlan()
		p.state.Apply(CommitChanged{Phase: CommitFailed, Text: "proposal hook unavailable"})
		p.state.Apply(Notice{Text: "automatic commit planning is unavailable in this session"})
		return nil
	}
	p.commitSeq++
	seq := p.commitSeq
	p.commitProposing = true
	ctx, cancel := context.WithCancelCause(context.Background())
	p.commitCancel = cancel
	p.state.Apply(CommitChanged{Phase: CommitProposing, At: time.Now()})
	return func() tea.Msg {
		request, err := propose(ctx, plan, key)
		return commitProposedMsg{seq: seq, request: request, err: err}
	}
}

// finishCommitPlan reports a complete plan without proposing anything else.
// remaining names the non-committable work that intentionally stayed out.
func (p *Program) finishCommitPlan(remaining string) {
	done := p.commitAutoDone
	p.clearCommitPlan()
	p.state.Apply(CommitChanged{Phase: CommitDone})
	text := fmt.Sprintf("Commit plan complete: %d %s created.", done, pluralWord("commit", done))
	if strings.TrimSpace(remaining) != "" {
		text += "\n" + remaining
	}
	p.state.Apply(Notice{Text: text})
}

// planHasRemaining reports paths that the plan intentionally did not commit.
func planHasRemaining(plan *commit.Plan) bool {
	return plan != nil && (len(plan.Protected) > 0 || len(plan.Blocked) > 0 ||
		len(plan.Ambiguous) > 0 || len(plan.Insufficient) > 0 || len(plan.Unrelated) > 0)
}

// commitPartialNotice names what a cancelled or failed plan actually created.
func (p *Program) commitPartialNotice() string {
	if !p.commitAuto || p.commitAutoDone == 0 {
		return ""
	}
	return fmt.Sprintf("Commit plan stopped after %d of %d commits.\n",
		p.commitAutoDone, p.commitAutoTotal)
}

// clearCommitPlan drops automatic-plan state without touching Git state.
func (p *Program) clearCommitPlan() {
	p.commitPlan = nil
	p.commitAuto = false
	p.commitAutoDone = 0
	p.commitAutoTotal = 0
	p.commitStaleRetries = 0
	p.commitPlanPropose = nil
}

// commitReviewReason explains what Enter authorizes. In an automatic plan it
// authorizes exactly the displayed commit, never the remaining plan.
func commitReviewReason(request *commit.Request) string {
	if request != nil && request.Progress != nil {
		return fmt.Sprintf("Commit %d of %d in the plan. Enter authorizes only this commit.",
			request.Progress.Position, request.Progress.Total)
	}
	return "One Git commit will be created with the exact scope below."
}

// pluralWord is the local noun helper for user-facing plan text.
func pluralWord(noun string, count int) string {
	if count == 1 {
		return noun
	}
	return noun + "s"
}

// onCommitProposed opens the proposal dialog, or reports why no proposal
// exists. A clean tree never opens a dialog.
func (p *Program) onCommitProposed(msg commitProposedMsg) (tea.Model, tea.Cmd) {
	if msg.seq == p.commitSeq {
		p.commitProposing = false
		// The request finished; release its context. The cause is irrelevant
		// now because no service is still reading it.
		p.stopCommitRequest(nil)
	} else {
		// A stale generation from a cancelled or superseded /commit must not
		// mutate the newer state or reopen a dialog.
		return p, nil
	}
	if msg.err != nil {
		if p.commitAuto && errors.Is(msg.err, commit.ErrPlanStale) {
			// The plan is a proposal; Git is authoritative. Re-read the
			// repository and rebuild the remaining plan rather than touching
			// the stale scope. Bound the replans so a continuously mutating
			// worktree cannot spin forever.
			p.commitStaleRetries++
			if p.commitStaleRetries > 3 {
				p.clearCommitPlan()
				p.state.Apply(CommitChanged{Phase: CommitFailed, Text: commitFailureText(msg.err)})
				p.state.Apply(Notice{Text: "Commit plan changed repeatedly; no commit was created."})
				return p, nil
			}
			p.state.Apply(Notice{Text: "commit plan changed; replanning the remaining commits"})
			return p, p.startCommitPlanRefresh()
		}
		p.clearCommitPlan()
		p.state.Apply(CommitChanged{Phase: CommitFailed, Text: commitFailureText(msg.err)})
		p.state.Apply(Notice{Text: commit.AbortText(msg.err)})
		return p, nil
	}
	if msg.request == nil || msg.request.Snapshot == nil {
		p.clearCommitPlan()
		p.state.Apply(CommitChanged{Phase: CommitFailed, Text: "empty proposal"})
		p.state.Apply(Notice{Text: commit.AbortText(commit.ErrInvalidProposal)})
		return p, nil
	}
	// The proposal is a review boundary, not a queue. If a turn or another
	// overlay started while the model was answering, the state it describes
	// is no longer the state the user is looking at.
	if p.state.Status.Busy || p.commitRequest != nil || p.commitRunning || p.otherOverlayOpen() {
		p.clearCommitPlan()
		p.state.Apply(CommitChanged{Phase: CommitCancelled})
		p.state.Apply(Notice{Text: "commit proposal discarded: the session state changed"})
		return p, nil
	}
	if p.commitAuto && msg.request.Progress != nil {
		// The residual plan numbers its groups from 1. Present the user's
		// actual position in the whole walk, not a fresh local count.
		msg.request.Progress.Position += p.commitAutoDone
		if p.commitAutoTotal >= msg.request.Progress.Position {
			msg.request.Progress.Total = p.commitAutoTotal
		} else {
			msg.request.Progress.Total = msg.request.Progress.Position
		}
	}
	p.commitRequest = msg.request
	p.showCommitProposal(msg.request, commitReviewReason(msg.request))
	return p, nil
}

// showCommitProposal publishes the review state and opens the existing
// permission dialog. It is shared by proposal generation, a validated edit,
// and an edit cancellation so all three present the same surface.
func (p *Program) showCommitProposal(request *commit.Request, reason string) {
	p.state.Apply(CommitChanged{Phase: CommitReview})
	if len(request.Snapshot.Scope) > commitFilesShown {
		// The dialog shows a bounded excerpt; the transcript carries the full
		// proposal so every approved path is inspectable before execution.
		p.state.Apply(Notice{Text: commit.FormatProposal(request)})
	}
	p.state.Apply(ApprovalAsked{
		ToolName:   "commit",
		Summary:    request.Proposal.Subject(),
		Reason:     reason,
		Capability: "git commit",
		Risk:       "elevated",
		Mutating:   true,
		Choices:    append([]string(nil), commitChoices...),
		Commit:     commitView(request),
	})
}

// dropCommitProposal forgets the held proposal without a notice. It is used by
// a retry, which is already a deliberate replacement.
func (p *Program) dropCommitProposal() {
	if p.commitRequest == nil {
		return
	}
	p.commitRequest = nil
	if p.state.Pending != nil && p.state.Pending.Commit != nil {
		p.state.Apply(ApprovalAnswered{})
	}
}

// beginCommitEdit closes the review dialog and moves the exact message into
// the composer. No model call and no repository mutation happen here.
func (p *Program) beginCommitEdit() {
	request := p.commitRequest
	if request == nil {
		p.state.Apply(Notice{Text: "commit: no proposal to edit; run /commit first"})
		return
	}
	if p.state.Status.Busy || p.commitRunning || p.commitProposing {
		p.state.Apply(Notice{Text: "commit: wait for the current operation to finish"})
		return
	}
	p.commitEditing = true
	p.state.Apply(CommitChanged{Phase: CommitEditing})
	p.state.Apply(ApprovalAnswered{})
	message := request.Proposal.Message()
	p.state.Apply(ComposerChanged{Text: message, Cursor: len([]rune(message))})
	p.state.Apply(Notice{Text: "editing commit message · enter validate · esc cancel"})
}

// submitCommitEdit validates the edited text deterministically and returns to
// review. It never calls the model and never mutates the repository.
func (p *Program) submitCommitEdit() (tea.Model, tea.Cmd) {
	// The edit is local text; the plan and the exact group scope stay frozen
	// until the human approves the reviewed message.

	request := p.commitRequest
	if request == nil || request.Snapshot == nil {
		p.commitEditing = false
		p.state.Apply(CommitChanged{Phase: CommitFailed, Text: "no proposal"})
		p.state.Apply(Notice{Text: "commit: the proposal disappeared before the edit was validated"})
		return p, nil
	}
	text := strings.TrimSpace(expandPastes(p.state.Pastes, p.state.Input))
	proposal, err := commit.Parse(text, request.Snapshot.Refs)
	if err == nil {
		// The human owns the wording, not the meaning boundary: an edit may
		// not claim a change the approved scope does not contain.
		err = request.Snapshot.ValidateProposal(proposal)
	}
	if err != nil {
		// The edit stays live so the human can correct the same text. The
		// repository is untouched and the previous proposal is not executed.
		p.state.Apply(Notice{Text: "commit message rejected: " + commit.ValidationReason(err)})
		return p, nil
	}
	request.Proposal = proposal
	p.commitEditing = false
	p.state.Apply(InputChanged{Text: ""})
	p.showCommitProposal(request, "The edited message passed validation. "+commitReviewReason(request))
	return p, nil
}

// cancelCommitEdit restores the original proposal. Editing is purely local
// text manipulation, so cancelling it cannot change Git state.
func (p *Program) cancelCommitEdit() {
	if !p.commitEditing {
		return
	}
	p.commitEditing = false
	p.state.Apply(InputChanged{Text: ""})
	if request := p.commitRequest; request != nil && request.Snapshot != nil {
		p.showCommitProposal(request, commitReviewReason(request))
		return
	}
	p.state.Apply(CommitChanged{Phase: CommitCancelled})
	p.state.Apply(Notice{Text: "commit edit cancelled"})
}

// retryCommit regenerates the proposal from fresh bounded evidence. It is the
// dialog's "r" action; the /commit --retry command reaches the same path.
func (p *Program) retryCommit() (tea.Model, tea.Cmd) {
	if p.commitAuto {
		// A retry re-reads authoritative state and rebuilds the remaining
		// plan; it never reuses a stale grouping or blindly regenerates a
		// message against the old scope.
		p.dropCommitProposal()
		return p, p.startCommitPlan(true)
	}
	if p.commitRequest == nil {
		p.state.Apply(Notice{Text: "commit: nothing to retry; run /commit first"})
		return p, nil
	}
	p.dropCommitProposal()
	return p, p.startCommitProposal(true)
}

// commitView projects the validated request for the dialog.
func commitView(request *commit.Request) *CommitProposalView {
	view := &CommitProposalView{
		Subject:  request.Proposal.Subject(),
		Body:     append([]string(nil), request.Proposal.Body...),
		Note:     commit.ScopeNote(request.Snapshot),
		Progress: commit.FormatProposalProgress(request),
	}
	for i, change := range request.Snapshot.Scope {
		if i == commitFilesShown {
			view.Note = strings.TrimSpace(fmt.Sprintf("%s %d files in scope; showing the first %d.",
				view.Note, len(request.Snapshot.Scope), commitFilesShown))
			break
		}
		view.Files = append(view.Files, change.Display())
	}
	return view
}

// handleCommitKey answers the commit proposal. It is the permission dialog's
// interaction, not the tool approval's: arrows move, Enter confirms, Esc or
// Ctrl+C cancels, "e" edits the message, and "r" regenerates the proposal.
// y/n are deliberately not accepted here, matching the footer and keeping a
// commit a two-step, deliberate decision.
func (p *Program) handleCommitKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "up", "k", "shift+tab", "left", "h":
		p.state.Apply(ApprovalMoved{Delta: -1})
		return p, nil
	case "down", "j", "tab", "right", "l":
		p.state.Apply(ApprovalMoved{Delta: 1})
		return p, nil
	case "enter":
		selected := ""
		if p.state.Pending != nil {
			selected = p.state.Pending.Selected()
		}
		return p.answerCommit(strings.EqualFold(selected, commitApproveChoice), "denied by user")
	case "e", "ctrl+e":
		p.beginCommitEdit()
		return p, nil
	case "r", "ctrl+r":
		return p.retryCommit()
	case "esc", "ctrl+c":
		p.cancelCommitProposal()
		return p, nil
	}
	return p, nil
}

// answerCommit resolves the proposal dialog. Approval starts exactly one
// execution; cancellation performs no Git mutation.
func (p *Program) answerCommit(approved bool, reason string) (tea.Model, tea.Cmd) {
	request := p.commitRequest
	p.commitRequest = nil
	p.state.Apply(ApprovalAnswered{})
	if !approved {
		if p.commitAuto {
			partial := p.commitPartialNotice()
			p.clearCommitPlan()
			p.state.Apply(CommitChanged{Phase: CommitCancelled})
			text := "commit plan cancelled"
			if partial != "" {
				text = strings.TrimSpace(partial) + "\nremaining changes are untouched"
			}
			p.state.Apply(Notice{Text: text})
			return p, nil
		}
		p.state.Apply(CommitChanged{Phase: CommitCancelled})
		text := "commit cancelled"
		if reason != "" && reason != "denied by user" {
			text = "commit cancelled: " + reason
		}
		p.state.Apply(Notice{Text: text})
		return p, nil
	}
	if request == nil {
		p.clearCommitPlan()
		p.state.Apply(CommitChanged{Phase: CommitCancelled})
		return p, nil
	}
	if p.commitHooks.Execute == nil {
		p.state.Apply(CommitChanged{Phase: CommitFailed, Text: "execute hook unavailable"})
		p.state.Apply(Notice{Text: "commit is unavailable in this session"})
		return p, nil
	}
	p.commitRunning = true
	p.state.Apply(CommitChanged{Phase: CommitExecuting, At: time.Now()})
	return p, p.executeCommit(request)
}

// executeCommit runs the approved request once. The command has no retry and
// no follow-up commit: a failure is reported as it happened.
func (p *Program) executeCommit(request *commit.Request) tea.Cmd {
	execute := p.commitHooks.Execute
	seq := p.commitSeq
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		result, err := execute(ctx, request)
		return commitFinishedMsg{seq: seq, result: result, err: err}
	}
}

// onCommitFinished reports the verified result, or the real failure. A commit
// that exists but failed verification still reports its SHA; a result that
// belongs to an earlier session reports the truth without claiming the new
// session's lifecycle.
func (p *Program) onCommitFinished(msg commitFinishedMsg) (tea.Model, tea.Cmd) {
	p.commitRunning = false
	auto := p.commitAuto
	terminal := CommitDone
	if msg.err != nil {
		terminal = CommitFailed
	}
	if msg.seq == p.commitSeq {
		text := ""
		if msg.err != nil {
			text = msg.err.Error()
		}
		p.state.Apply(CommitChanged{Phase: terminal, Text: text})
	}
	if msg.err != nil {
		partial := ""
		if auto {
			partial = p.commitPartialNotice()
			p.clearCommitPlan()
		}
		text := partial + commit.AbortText(msg.err)
		if msg.result.SHA != "" {
			text += "\n" + commit.FormatResult(msg.result)
		}
		p.state.Apply(Notice{Text: text})
		return p, nil
	}
	p.state.Apply(Notice{Text: commit.FormatResult(msg.result)})
	if auto && msg.seq == p.commitSeq {
		// The commit exists. Re-read the repository before showing the next
		// proposal; the previous grouping may no longer describe the tree.
		p.commitAutoDone++
		return p, p.startCommitPlanRefresh()
	}
	if auto {
		p.clearCommitPlan()
	}
	return p, nil
}

// stopCommitRequest cancels an in-flight proposal with a cause. A nil reason is
// used when the request has already finished and only its context needs
// releasing; lifecycle cancellations name themselves so diagnostics never
// collapse user cancellation into a timeout.
func (p *Program) stopCommitRequest(reason error) {
	if p.commitCancel == nil {
		return
	}
	p.commitCancel(reason)
	p.commitCancel = nil
}

// commitFailureText is the internal diagnostic string stored on the activity
// state. The transcript uses commit.AbortText; this line keeps provider,
// model, deadline, and elapsed facts available without rendering them.
func commitFailureText(err error) string {
	text := err.Error()
	if diag, ok := commit.DiagnosticsOf(err); ok {
		text += "\n" + commit.DiagnosticText(diag)
	}
	return text
}

// cancelCommitProposal drops a proposal or an in-flight generation without
// mutating anything. The sequence bump is what makes a late provider answer
// harmless: it can no longer reopen a dialog for a command the user cancelled.
func (p *Program) cancelCommitProposal() {
	if p.commitEditing {
		p.cancelCommitEdit()
		return
	}
	if p.commitRequest == nil && !p.commitProposing {
		if p.commitAuto {
			partial := p.commitPartialNotice()
			p.clearCommitPlan()
			p.state.Apply(CommitChanged{Phase: CommitCancelled})
			text := "commit plan cancelled"
			if partial != "" {
				text = strings.TrimSpace(partial) + "\nremaining changes are untouched"
			}
			p.state.Apply(Notice{Text: text})
		}
		return
	}
	partial := ""
	if p.commitAuto {
		partial = p.commitPartialNotice()
	}
	p.commitSeq++
	p.commitProposing = false
	p.stopCommitRequest(commit.ErrCancelledByUser)
	p.commitRequest = nil
	if p.state.Pending != nil && p.state.Pending.Commit != nil {
		p.state.Apply(ApprovalAnswered{})
	}
	if p.commitAuto {
		p.clearCommitPlan()
	}
	p.state.Apply(CommitChanged{Phase: CommitCancelled})
	text := "commit cancelled"
	if partial != "" {
		text = strings.TrimSpace(partial) + "\nremaining changes are untouched"
	}
	p.state.Apply(Notice{Text: text})
}
