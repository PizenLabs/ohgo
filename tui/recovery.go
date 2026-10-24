package tui

import (
	"context"
	"fmt"
	"strings"

	"github.com/PizenLabs/ohgo/agent"
	"github.com/PizenLabs/ohgo/recovery"
)

// Recoverer is the host's workspace recovery authority.
//
// It is deliberately three narrow methods over one session identity. The
// program never receives a store, a checkpoint id, or a path list to apply:
// undo/redo select their own target from the session's recorded timeline, and
// the journal is only ever handed to the loop, never to a tool.
type Recoverer interface {
	// Journal returns the edit checkpoint seam for a session. Nil means the
	// host could not bind one, in which case the loop is left without a
	// journal and mutating turns are refused by the host's own policy.
	Journal(sessionID int64) agent.EditJournal
	// Undo restores the workspace to the state before the applied edit.
	Undo(ctx context.Context, sessionID int64) recovery.Result
	// Redo restores the workspace to the next undone edit's after-state.
	Redo(ctx context.Context, sessionID int64) recovery.Result
	// Inspection reports a leftover interrupted operation, if any.
	Inspection(ctx context.Context, sessionID int64) string
}

// recoveryFilesShown bounds the file list in a transcript notice.
const recoveryFilesShown = 12

// recoverWorkspace runs one explicit recovery command.
//
// It refuses while a turn is running: the running turn may be mid-mutation, and
// restoring underneath it would both corrupt the workspace and make the
// checkpoint's expected state meaningless. Recovery is a human command, so it
// is also never scheduled by the loop or by a tool.
func (p *Program) recoverWorkspace(op recovery.Op) {
	if p.recovery == nil {
		p.state.Apply(Notice{Text: "workspace recovery is unavailable in this session"})
		return
	}
	if p.state.Status.Busy {
		p.state.Apply(Notice{Text: fmt.Sprintf(
			"wait for the turn to finish before running /%s", op)})
		return
	}
	p.mu.Lock()
	var sessionID int64
	if p.session != nil {
		sessionID = p.session.ID
	}
	p.mu.Unlock()
	if sessionID == 0 {
		p.state.Apply(Notice{Text: fmt.Sprintf(
			"/%s needs a saved session; there is no edit timeline to move through", op)})
		return
	}

	ctx := context.Background()
	var result recovery.Result
	switch op {
	case recovery.OpUndo:
		result = p.recovery.Undo(ctx, sessionID)
	case recovery.OpRedo:
		result = p.recovery.Redo(ctx, sessionID)
	default:
		p.state.Apply(Notice{Text: "unknown recovery operation"})
		return
	}
	p.state.Apply(Notice{Text: FormatRecoveryResult(result)})
}

// reportRecoveryInspection surfaces a leftover interrupted operation.
//
// It never changes the workspace: an interrupted restore must be inspected by a
// human before any further recovery runs, so the notice states the condition
// and stops there.
func (p *Program) reportRecoveryInspection(sessionID int64) {
	if p.recovery == nil || sessionID == 0 {
		return
	}
	if message := p.recovery.Inspection(context.Background(), sessionID); message != "" {
		p.state.Apply(Notice{Text: "RECOVERY REQUIRES INSPECTION\n" + message})
	}
}

// installRecoveryJournal binds the loop's checkpoint seam to one session.
//
// It is called at the turn boundary, before Run, so a checkpoint can never be
// attributed to a session the program has since left. The journal is cleared
// after the run so nothing else in the program can capture under a stale scope.
func (p *Program) installRecoveryJournal(sessionID int64) {
	if p.loop == nil {
		return
	}
	if p.recovery == nil || sessionID == 0 {
		p.loop.SetJournal(nil)
		return
	}
	p.loop.SetJournal(p.recovery.Journal(sessionID))
}

// FormatRecoveryResult renders one recovery outcome as a transcript notice.
//
// The wording is part of the contract: a blocked request must say that no files
// were modified, and a failure must not read as success. The transcript is
// append-only here; nothing rewrites an earlier message.
func FormatRecoveryResult(result recovery.Result) string {
	op := result.Op
	if op == "" {
		op = recovery.OpUndo
	}
	verbPast := "Undid"
	arrow := "↶"
	if op == recovery.OpRedo {
		verbPast = "Redid"
		arrow = "↷"
	}
	name := strings.ToUpper(string(op[:1])) + string(op[1:])

	switch result.State {
	case recovery.StateVerified:
		var sb strings.Builder
		fmt.Fprintf(&sb, "%s %s edit #%d · %s",
			arrow, verbPast, result.Sequence, pluralFiles(len(result.Files)))
		for i, change := range result.Files {
			if i == recoveryFilesShown {
				fmt.Fprintf(&sb, "\n… %d more", len(result.Files)-i)
				break
			}
			fmt.Fprintf(&sb, "\n%s %s", change.Status, change.Path)
		}
		for _, note := range result.Notes {
			sb.WriteString("\n" + note)
		}
		return sb.String()

	case recovery.StateBlocked:
		var sb strings.Builder
		fmt.Fprintf(&sb, "%s blocked · %s\nNo files were modified.", name, result.Reason)
		appendUnexpected(&sb, result.Unexpected)
		return sb.String()

	case recovery.StateEmpty:
		return fmt.Sprintf("Nothing to %s · %s", op, result.Reason)

	case recovery.StateInterrupted:
		var sb strings.Builder
		sb.WriteString("RECOVERY REQUIRES INSPECTION\n")
		sb.WriteString(result.Reason)
		sb.WriteString("\nNo automatic destructive action was taken.")
		appendUnexpected(&sb, result.Unexpected)
		return sb.String()

	case recovery.StateFailed:
		var sb strings.Builder
		fmt.Fprintf(&sb, "%s failed · %s\nWorkspace state was not proven to match the target checkpoint.",
			name, result.Reason)
		appendUnexpected(&sb, result.Unexpected)
		return sb.String()

	default:
		return fmt.Sprintf("%s: %s", name, result.Reason)
	}
}

// appendUnexpected lists the paths that caused a refusal, bounded.
func appendUnexpected(sb *strings.Builder, changes []recovery.Change) {
	if len(changes) == 0 {
		return
	}
	sb.WriteString("\nUnexpected workspace changes:")
	for i, change := range changes {
		if i == recoveryFilesShown {
			fmt.Fprintf(sb, "\n… %d more", len(changes)-i)
			return
		}
		fmt.Fprintf(sb, "\n%s %s", change.Status, change.Path)
	}
}

// pluralFiles renders the file count without a stray "1 files".
func pluralFiles(count int) string {
	if count == 1 {
		return "1 file"
	}
	return fmt.Sprintf("%d files", count)
}
