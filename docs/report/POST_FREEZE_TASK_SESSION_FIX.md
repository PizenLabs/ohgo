# Post-Freeze Patch: Task/Session Fix and `/new`

## 1. Original bug

A persisted open task hijacked every new session in the same project:

```text
started new session 149

migrating a project to Next.js

an open task is waiting:
check this project and write README.md file help me.
Reply with a continuation (for example "continue"), or run /task cancel to abandon it.
```

A durable task was interpreted as `OPEN → MUST CONTINUE`. A new user intent
could not start without first continuing or cancelling the old task, even in
a brand-new session.

## 2. Root cause

Two layers were coupled:

- The task store is project-local (`.ohgo/task.md` + `.ohgo/progress.json`)
  with a single live slot. `task.Controller.Begin` enforces contract equality
  and returns `ErrObjectiveConflict` for a different objective.
- `tui.Program.runTurn` checked `Current()` before execution. For any
  non-terminal task whose contract did not match and was not an exact
  `continuationPrompt`, it aborted the turn with the blocking notice above.

Session creation (`session.Store.CreateWith` via `SessionActions.New`) never
detached the task controller. A new session ID therefore inherited the same
project-local task, and the first prompt in session 149 was judged against
task A from session 148.

## 3. Lifecycle model

No new `task.Status` values were added. The existing vocabulary already
carries the required semantics; the fix adds a session binding on top:

- `ACTIVE` = `RUNNING` attached to the current session.
- `RESUMABLE` = `PAUSED` or `NEEDS_REVIEW` (and `PENDING`), detached from the
  session but preserved durably in `.ohgo/tasks/<id>.json` plus the live slot
  when still bound.
- `COMPLETED`, `FAILED`, `CANCELLED` are terminal and never resumable.
- `BLOCKED`, `STALE`, `SUPERSEDED` are not separate states: a detached
  non-terminal task is stale relative to the live session, and superseded
  when a newer task replaces it. The archive preserves the full task,
  progress, budget, evidence, and audit, so none of those concepts need a new
  status.

Invariant:

```text
OPEN ≠ MUST CONTINUE
RESUMABLE ≠ ACTIVE IN CURRENT SESSION
RELATED ≠ CONTINUATION
```

## 4. `/new` design

`/new` is the canonical creator; `/session` (and the `/sessions` alias) is
the manager/lister.

- `/new` with no args opens the existing generic picker (`PickerNewSession`):
  `Start new session` / `Cancel`. It reuses `PickerState`, the same keyboard
  contract (`↑↓`/`enter`/`esc`), and the same `choosePicker` path as models,
  jobs, and tasks. No second picker implementation.
- `/new <name>` creates directly and applies the title via the existing
  rename seam. No new naming system.
- `Enter` creates exactly one session; `Esc` (or `Cancel`) creates none.
  Rapid `/new` only reopens the picker. A `newSessionCreating` guard plus
  close-before-create makes double-`Enter` impossible.
- While a turn runs, `/new` cancels it first (`cancelTurnForNewSession`
  checks the cancellation handle, not only the view `Busy` flag), then
  detaches the task and adopts the new session. The old turn's history and
  transcript stay with the old session ID.

## 5. Implementation

Task layer (`task/`):

- `task/archive.go`: `ArchiveTask`, `ListArchived`, `LoadArchived`,
  `RemoveArchived`, `ClearActive`, `IsResumable`. Archive dir
  `.ohgo/tasks/`, atomic writes, corrupt files skipped, deterministic order
  (stamp, then ID).
- `task/controller.go`: `Detach` (pause `RUNNING`, archive, clear live slot
  and in-memory evidence/audit), `ListResumable` (live + archived,
  non-terminal only), `ResumeTask` (latest when empty, archive-current-first,
  restore budget/evidence/audit, move to `RUNNING`, remove archive copy),
  `CancelResumable` (terminal, never listed again). `Begin` stays strict;
  the TUI detaches before calling it with a new intent.

TUI (`tui/`):

- `tui/task.go`: `TaskHooks` gains `Detach`, `ListResumable`, `ResumeTask`,
  `CancelResumable`. `continuationPrompt` is now explicit-intent: exact
  forms plus whole-word `continue`/`resume` and phrases (`where we left
  off`, `keep going`, `go on`, `carry on`, `previous task`). Relatedness
  never implies continuation. `/task` supports
  `status|list|show|resume|cancel|accept` with ID prefix or objective
  substring; ambiguous resume opens the `tasks` picker.
- `tui/app.go`: `runTurn` no longer blocks. Explicit continuation reuses the
  persisted contract/snapshot (`EXPLICIT_RESUME`); any other prompt detaches
  the old task and proceeds with the fresh contract (`NEW_INTENT`). Terminal
  tasks never prompt. `adoptSession` denies pending approvals, clears
  whole-session approvals, replaces history, and resets telemetry, so no
  proposal, authority, or transcript leaks. History rebase and persistence
  are pinned to the turn's session ID. `lastTaskDecision` records
  `NEW_SESSION`/`EXPLICIT_RESUME`/`NEW_INTENT`/`TASK_DETACH` for
  observability.
- `tui/model.go` + `tui/picker.go`: register `/new` and `/session`
  (keep `/sessions`), `PickerNewSession` and `PickerTasks`, help text
  exposes `/new`, `/session`, `/task`.

Host (`cmd/ohgo/`):

- `taskcontinuity.go` wires the new hooks and replaces the startup blocking
  notice with a non-blocking `1 resumable task available (see /task list)`.
- `run.go` REPL: `/new` creates a fresh session and clears history;
  `/session` aliases the lister. Task continuity stays full-screen-only.

## 6. Regression tests

`task/postfreeze_test.go`:

- Detach preserves and clears; detached work survives restart; resume
  restores durable state; cancel stops resumability; multiple list
  deterministically and resume picks latest; terminal never resumable;
  detach preserves objective/contract/review reason.

`tui/postfreeze_test.go` ( Dollhouse matrix):

1. New session with open task → new task, no blocking notice, old preserved.
2. Explicit `continue` → resumes same ID (`EXPLICIT_RESUME`).
3. `/task resume` → restores latest.
4. `/task cancel` → `CANCELLED`, not resumable.
5. Related intent (`write README.md` → `improve documentation`) → new task.
6. Completed task → no prompt.
7. Multiple resumable → deterministic `/task list`; ambiguous resume → picker.
8. Active task + `/new` → old preserved, new session clean.
9. New session → no transcript inheritance.
10. New session → preserves cwd/model config.
- Adversarial: `/new` while a tool runs (cancel, no leak); rapid `/new`
  (one picker, one session on `Enter`); authority leak (session approvals
  and pending proposals cleared); session IDs unique and refer to the new
  session; continuation classification (explicit vs new intent); picker
  surfaces `/new` and `Esc` cancels.
- Updated `TestResumedReviewRefusesDifferentObjective` to the new contract:
  a different objective detaches and begins a new task instead of refusing.
- Fixed `TestRenderSlashPaletteWindowsToTheCursor` to locate `/model`
  dynamically after the registry grew.

## 7. Verification

```bash
gofmt -l .
go build ./...
go vet ./...
go test ./...
go test -race ./task -count=1
go test -race ./tui -count=1
golangci-lint run ./...
```

All pass. Focused suites: `task`, `session`, TUI command picker, `/new`,
`/session`, `/task`, session/task persistence. Phase 9/10 cognition contracts
(`TaskShape`, `EvidenceState`, `Projector`, `Sufficiency`, authority gate,
completion evaluator) were not changed.

## 8. Compatibility behavior

- `/session` and `/sessions` both open the Session Manager (list, resume,
  new, rename, archive, delete). `/sessions` is kept as an alias.
- `/task status` with no active task reports the resumable count instead of
  `no task is active`, pointing at `/task list`.
- `/task resume [id]` with no ID resumes the latest; with an ambiguous
  substring it opens the picker rather than guessing.
- `/task cancel [id]` cancels the latest or the named task, including
  detached ones, without requiring a prior resume.
- Headless REPL: `/new` and `/session` work; `/task` remains full-screen-only
  and says so instead of pretending.
- Session IDs remain `AUTOINCREMENT`, unique, persisted, and displayed as
  `started new session <id>` for the newly created session only.

## 9. Final UX

Before:

```text
started new session 149
migrating a project to Next.js
an open task is waiting: ...
```

After:

```text
started new session 149
migrating a project to Next.js
```

The request becomes the current task. The README task stays durable:

```text
/task list
/task resume
```

Mental model:

```text
/new     → create a new conversation
/session → manage existing sessions
/task    → manage durable work
```

Sessions are conversational containers. Tasks are durable work. `/new`
creates a conversation. `/task resume` restores work. A persisted task never
hijacks a new intent.
