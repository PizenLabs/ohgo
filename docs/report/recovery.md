# Safe Edit Recovery — Undo + Redo

Status: **implemented**. Scope is recovery only. `/commit`, commit proposals,
branches, and push/pull are deliberately absent and remain a separate future
capability.

## Product model

Two records, two lifetimes:

```
Session                          Edit timeline (one workspace)
├── messages                     ├── checkpoint #1  (immutable)
├── task state                   ├── checkpoint #2  (immutable)
└── identity                     └── cursor + redo stack
```

`/undo` and `/redo` move the **workspace cursor**. They never rewrite, delete,
truncate, or reorder conversation messages. The only conversation change is one
appended `EntryNotice` line (`↶ Undid edit #3 · 2 files`), which is UI-only and
never reaches the model or the session store.

## Where edit checkpoints are created

`recovery.Controller.BeginEdit` is invoked by `agent.Loop.dispatch` through the
`agent.EditJournal` seam (`agent/journal.go`). The host installs the journal:

- full-screen UI: `Program.installRecoveryJournal` at the turn boundary in
  `tui.runTurn`, bound to the session that owns the turn;
- plain REPL: `harness.journal()` immediately before `runHeadlessTurnAs`.

`cmd/ohgo/recovery.go` is the only adapter that knows both the store and the
workspace root. A subagent never receives a journal: `RunSubagent` builds its
child `Config` without one, so the parent turn remains the single edit boundary
(the child's writes land inside the parent's before/after capture).

## What constitutes an edit boundary

**One completed agent turn** — one `Loop.Run`, which is one user request. A turn
that writes `a.go`, writes `b.go`, deletes `c.go`, and creates `d.go` produces
one checkpoint. Filesystem syscalls are not undo points.

The journal is opened lazily, immediately before the first tool batch that can
change the workspace (`agent.batchMayMutate`: declared `ActionMutate` /
`ActionDelegate`, falling back to the approval tier). A read-only turn creates
no checkpoint and pays no snapshot cost.

Checkpoint order is fixed in `agent.Loop.dispatch`:

```
OBSERVE → CAPTURE BEFORE → VERIFY DURABLE → (authorize) → EXECUTE
        → OBSERVE AFTER → DIFF → PUBLISH (one SQLite transaction)
```

A `BeginEdit` failure refuses the whole batch: every call gets
`recovery checkpoint unavailable: …; no files were modified`, and no tool runs.
`Commit` is attempted even after cancellation, because the mutation already
happened.

## What state is captured

Per completed edit:

- `id`, `session_id`, repository identity, workspace root, sequence, parent
- before-tree hash and after-tree hash
- the changed paths (`A`/`M`/`D`)
- metadata (turn index, tool names, interrupted flag, skipped paths)
- timestamp and verification state (audit table)

A **tree** is a sorted manifest of every path inside the workspace boundary:
kind (file / directory / symlink), permission bits, file size, and a SHA-256 of
the content (or of the symlink target). Directory entries are included, so an
agent-created empty directory is reversible. Modes are included, so an
executable bit survives a restore.

The workspace boundary excludes, by construction:

- `.git` and `.ohgo` directories (VCS metadata and ohgo's own state);
- every path the repository's own `.gitignore` rules exclude, using the same
  matcher the search tools use (`internal/ignore`, extracted so recovery and
  tools cannot disagree);
- files larger than 64 MiB and non-file node types, which are named in
  `Tree.Skipped` and recorded in checkpoint metadata rather than silently
  truncated.

Git is an **observation** input only. The repository identity comes from
`vcs.Discover` (the shared git dir), and nothing in recovery shells out to
`git checkout`, `git restore`, or `git reset`. A checkpoint captures tracked and
untracked-but-not-ignored files identically, and `.git` is never touched, so the
index, staged state, and HEAD are preserved exactly.

## Storage

`recovery.Open` creates a SQLite database (pure-Go `modernc.org/sqlite`, WAL,
`synchronous=FULL`) at:

```
~/.ohgo/recovery/<repository-key>/recovery.db
```

It is outside every workspace, mode `0600`, never inside the working tree, and
therefore cannot be git-tracked. Tables:

| table | purpose |
| --- | --- |
| `blobs` | content-addressed file content, `hash` primary key |
| `trees` | immutable tree manifests |
| `checkpoints` | published edits, `UNIQUE(session_id, repository, seq)` |
| `timelines` | cursor + redo stack per `(session, repository)` |
| `pending_edits` | durable before-state of an open turn |
| `inflight` | durable plan of a restore in progress |
| `transitions` | audit log: PLANNED / EXECUTING / VERIFIED / BLOCKED / FAILED / INTERRUPTED |

Content is deduplicated: a blob is stored once and referenced by every tree that
contains it. A scan keeps an in-memory `(size, mtime, mode) → hash` cache, so an
unchanged file is not re-read; a checkpoint capture only reads content for blobs
the store does not already hold.

## How untracked files are handled

Untracked-but-not-ignored files are ordinary workspace state. They are in every
manifest and in every blob capture:

- a user file that predates the edit is in the before-tree and the after-tree,
  so undo leaves it untouched;
- a file the agent created is absent from the before-tree, so undo deletes it;
- a file the user creates after the edit is an unexpected path and blocks the
  undo (see below);
- an ignored build artifact is outside the boundary and is never captured,
  restored, or deleted.

## How user changes are protected

Before any restore, `Controller.move` re-scans the workspace and requires

```
actual tree hash == expected source tree hash
```

where the expected source is the checkpoint's `after_tree` (undo) or
`before_tree` (redo). Any difference — a modified file, a deleted file, a new
untracked file, a mode change — produces:

```
Undo blocked · workspace changed outside ohgo
No files were modified.
M auth.go
```

No file is written, no merge is attempted, no intent is guessed, and no
`git reset --hard` exists to fall back on. The same check guards `/redo` with
the message `workspace changed after undo`.

`Apply` only touches paths in the verified diff, so paths outside the edit
boundary are never opened for writing. Directory removal that would require
deleting out-of-scope content is left in place and reported as a note instead of
being forced.

Known limit, disclosed: if a human edits the workspace *while a turn is running*,
that change lands between `BeginEdit` and `Commit` and is attributed to the
turn. The TUI queues input during a turn, so this requires an external editor.

## How undo selects its target

1. Resolve any interrupted restore first (below).
2. Read the session's timeline cursor. Empty → `Nothing to undo`.
3. Load that checkpoint. Undo restores its **`before_tree`** — by construction
   the state immediately before that edit (equal to the parent's after-state).
4. Require the live workspace to equal the checkpoint's `after_tree`.
5. Record the plan durably, restore, re-scan, compare with `before_tree`.
6. Only then move the cursor to `checkpoint.parent` and push the checkpoint onto
   the redo stack.

Undo never consults the model, never re-runs a turn, and never reconstructs a
patch.

## How redo selects its target

1. Read the redo stack. Empty → `Nothing to redo`.
2. Take its last entry, and require `checkpoint.parent == timeline.cursor`
   (a mismatch is reported as corrupt rather than applied to the wrong state).
3. Require the live workspace to equal the checkpoint's `before_tree`.
4. Restore the checkpoint's **`after_tree`**, verify, pop the stack, move the
   cursor.

Redo restores recorded bytes. It never asks the model to repeat the edit.

## How session isolation works

Every checkpoint and every timeline row is keyed by `(session_id, repository)`;
the store path is additionally keyed by repository identity. `Store.Controller`
binds a controller to one session and one workspace, and `Store.Checkpoint`
refuses a checkpoint whose session or repository differ (`ErrWrongWorkspace`).

`/undo` uses the session currently attached to the program. Session A's
checkpoints are unreachable from Session B: B's cursor is empty, so `/undo`
reports `Nothing to undo` and no file is touched. A session whose recorded
workspace root no longer resolves to the same repository gets a fresh timeline
rather than a cross-workspace restore.

## How crash recovery works

Two durable records make interruption detectable:

1. **Before the mutation** (`BeginEdit`): blobs, the before-tree manifest, and a
   `pending_edits` row are committed. Only after that is the batch authorized.
2. **Before a restore** (`beginInflight`): a plan row records op, checkpoint,
   expected and target trees, the intended cursor and redo stack, and the file
   list. It is committed before the first workspace write.

On the next controller creation:

- **Interrupted checkpoint**: a `pending_edits` row whose before-tree verifies is
  turned into a published checkpoint marked `interrupted`, with the *observed*
  current workspace as its after-state. The recovery point was already durable;
  only the after-observation was deferred. Undo then restores the verified
  before-state — which is exactly what an interrupted multi-file mutation needs.
  If the before-tree or any of its blobs fails verification, nothing is
  published: the row is marked `corrupt` and recovery refuses with
  `RECOVERY REQUIRES INSPECTION`.
- **Interrupted restore**: the workspace is scanned and compared with the plan.
  Exact target → the restore had completed; only bookkeeping is finished and a
  `VERIFIED (completed before interruption)` audit row is written. Exact source
  → nothing was applied; the record is cleared and the interruption recorded.
  Anything else → `RECOVERY REQUIRES INSPECTION`, the plan row is kept, and no
  further undo/redo is allowed until the workspace is inspected. No destructive
  action is taken automatically.

A checkpoint is never visible as a valid edit until its row, both tree
manifests, and every referenced blob are committed. Publication is one SQLite
transaction. A published checkpoint is immutable: undo/redo append transitions
and update only `timelines`.

## How corruption is detected

- `verifyTree` re-reads every blob a checkpoint references and checks both the
  recorded size and the SHA-256 of the content against the manifest. A missing,
  truncated, or tampered blob yields `ErrCorrupt`, and the operation fails with
  the workspace untouched.
- A missing tree manifest, undecodable metadata, a bad timestamp, or a redo
  stack that does not follow the cursor is `ErrCorrupt` / `StateInterrupted`.
- A pending capture whose before-tree cannot be verified is marked `corrupt` and
  blocks recovery for that session.
- Every failure is fail-closed: `StateFailed` with the reason stated, never a
  success message.

## How LLM authority is prevented

Recovery is a Control Plane operation reachable only from explicit human
commands:

- the loop's seam is `agent.EditJournal`, which has exactly one method,
  `BeginEdit`, and the returned transaction exposes only `Commit` and `Abort` —
  neither can restore anything;
- `Controller.Undo` / `Controller.Redo` are not on any interface the loop, a
  tool, or a projector receives;
- no tool named `undo`, `redo`, `recovery`, `checkpoint`, or `restore` is built
  (`TestNoRecoveryToolIsRegistered`);
- `/undo` and `/redo` are slash commands parsed only from the composer line;
  model text, tool output, and subagent output never enter `Program.runCommand`
  (`TestModelTextCannotInvokeRecovery`);
- subagents do not inherit the journal
  (`TestSubagentDoesNotInheritTheParentJournal`).

## What happens when state cannot be proven safe

```
BLOCKED        workspace differs from the expected source; zero writes
FAILED         restore ran but did not verify, or data is corrupt
INTERRUPTED    an earlier operation never reached a terminal state
EMPTY          nothing on the timeline in this direction
```

The system prefers `BLOCK` over `GUESS` and `PRESERVE USER STATE` over `FORCE
COMPLETION`. No recovery operation is reported successful without a post-restore
re-scan proving the target tree.

## UI

```
↶ Undid edit #12 · 3 files
M internal/agent/loop.go
M internal/tools/edit.go
A internal/recovery/state.go

↷ Redid edit #12 · 3 files

Undo blocked · workspace changed outside ohgo
No files were modified.

RECOVERY REQUIRES INSPECTION
<reason>
No automatic destructive action was taken.
```

`/undo` and `/redo` refuse while a turn is running. Recovery runs on the update
goroutine as an explicit, exclusive human action.

## Observability

Every attempted transition writes bounded metadata to `transitions`:
session, repository, op, checkpoint id, source checkpoint, target checkpoint,
source tree, target tree, changed paths, state, reason, timestamp. File
contents are never logged. `Store.History` and `Store.Transitions` expose the
audit trail.

## Tests

`recovery/recovery_test.go` (units, real filesystem + real SQLite):

- create / modify / delete / rename / multi-file undo
- undo → redo, two-undo/two-redo, redo exhaustion
- user edits after an edit block undo; user edits after undo block redo
- user untracked file survives; agent-created file disappears
- new edit after undo keeps the undone branch and invalidates redo explicitly
- session isolation
- checkpoint immutability across undo/redo
- corrupt blob, missing blob, wrong workspace, missing workspace
- interrupted checkpoint repair on restart; unrecoverable before-state refused
- interrupted restore: neither state → inspection; completed → reconciled
- `.git` and ignored files untouched
- blob deduplication, executable bit, symlink
- audit states VERIFIED / BLOCKED distinguished
- timeline and redo stack survive restart

`agent/journal_test.go` (seam):

- begin before the mutating batch, commit at the end, read-only runs skipped
- capture failure refuses the mutation
- commit after cancellation
- subagent inherits no journal
- the transaction exposes no recovery method

`tui/recovery_test.go` (commands + end-to-end):

- `/undo` reaches the authority with the current session id
- refuses while busy, without a session, or without a recoverer
- notices: verified / blocked / failed / interrupted / empty
- model text cannot invoke recovery
- a real agent turn creates one checkpoint, `/undo` restores the file, and the
  conversation slice is unchanged (`reflect.DeepEqual`)

`cmd/ohgo/recovery_test.go`:

- full service round trip (journal → commit → undo → redo)
- unavailable store refuses mutating batches
- no recovery tool is registered

## Deliberate limitations

- Racy external edits during a running turn are attributed to that turn.
- Background shell jobs that mutate after their turn commit appear as unexpected
  drift on the next undo (the undo blocks rather than guessing).
- Files above 64 MiB and special node types are excluded from checkpoints and
  named in metadata.
- The redo stack is invalidated by a new edit (explicit, tested); the undone
  checkpoints remain in the store and are reachable through `Store.History`.
- Authored store protection is in-process capability routing (tools are rooted at
  the workspace). A shell command running with the user's own OS permissions can
  still `rm` the store; recovery then fails closed rather than lying.
