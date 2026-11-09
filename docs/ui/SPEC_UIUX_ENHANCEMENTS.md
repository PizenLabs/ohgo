# Ohgo UI/UX Enhancements: Task Rail, Diff Surface, Telemetry

This document is the canonical contract for the UI enhancements layered on the
execution transcript. It supersedes SPEC_UIUX.md §7.1 (spinner animation), the
sidebar prohibition in §19, and §22–§23 (fast-operation and minimum-duration
rules) for the specific surfaces described here. The rest of SPEC_UIUX.md still
governs the transcript: one live activity, stable history, collapsed reasoning,
and bounded output.

## 1. Contextual task rail and sticky fallback

The full-screen canvas is responsive without changing execution state.

- The rail is transient chrome, not the default canvas. It appears only when an
  open execution task has at least one open todo. An idle session, an ordinary
  question, a task with no todo or only finished todos, and every terminal task
  (`COMPLETED`, `FAILED`, `CANCELLED`) render the ordinary full-width
  transcript; a completed task settles back into history rather than remaining
  pinned beside later turns.
- The rail is enabled by default. `/settings` opens the preferences surface and
  can disable it; the choice applies immediately and persists in
  `settings.json` as `taskRail`. Disabling it never hides task history: the
  transcript block and `/task` remain authoritative.
- **Wide terminals** (`LayoutWide`, at least 100 columns and 12 rows) split the
  body into the conversation column and a fixed right rail. The rail is 28% of
  the terminal width, clamped to 26–56 cells, with a one-cell divider. The
  conversation always keeps at least 48 cells.
- The rail renders, top to bottom: the current task tracker, active MCP/LSP
  integrations, and contextual task insights. Integrations ride along with the
  task; they never keep the rail on screen by themselves.
- The tracker is sticky by construction: scrolling the transcript changes only
  the conversation column. The rail is a projection, never a second viewport.
- **Narrow and medium terminals** hide the rail and reserve exactly one sticky
  task row immediately below the title separator while an open task has at
  least one open todo. That row names the objective, lifecycle status,
  progress, and the one active todo. With no such task, the row is absent and
  the transcript starts directly below the title bar.
- The transcript suppresses its own task block whenever the tracker owns the
  projection. One fact has one home on the same screen. When the task reaches a
  terminal state the tracker releases the projection, so the block returns to
  its chronological place and scrolling history stays stable.
- The rail never repeats global footer facts: token usage, context percentage,
  working directory, git branch, session id, and workspace authority stay in the
  footer, header, or composer prefix.

Data ownership stays in the existing layers:

- `TaskProjected` remains the task-domain event; `State` caches the latest
  projection so a canvas frame does not scan a long transcript.
- `IntegrationsUpdated` is a complete snapshot published from the existing MCP
  hooks and optional `LSPHooks`. The sidebar never connects or probes.
- `ComputeWorkspaceLayout` is the full-screen geometry seam.
  `ComputeLayout` remains the shared/output-first base layout.

### Settings surface

`/settings` opens the preferences modal, built on the same framed modal family
as the status and help cards: border-embedded title, one section header per
preference domain, and a ruled footer naming the live keys. It shows only
settings the runtime can apply immediately; `/theme`, `/model`, and
`/permissions` keep their own surfaces rather than being duplicated here. Rows
state their value as a word (`on` / `off`), so the surface never relies on colour
alone. Toggling applies immediately and persists through the host seam.

## 2. Permission and expanded code diffs

### Permission diff

The existing tool-approval surface keeps its amber/rose blast-radius border and
gains a two-level hierarchy:

1. **Metadata** — capability, risk, and reason. Empty producer fields derive
   from `Mutating`; the dialog never invents a capability tier.
2. **Payload** — the mutation preview, headed by operation, target path, and
   `[+added, -deleted]` metrics.

Added rows use a soft green tint, deleted rows a soft red tint, context rows are
dimmed, and hunk headers use the cyan metadata role. Tints span the dialog's
interior so each row reads as one stripe.

### Expanded code diff

A mutation tool's canonical diff stays copy material while collapsed. Ctrl+O
expands the block and renders:

- a minimal header with operation, middle-truncated path, change metrics, and
  `ctrl+o to collapse`;
- unified add/delete/context rows with the same tint semantics;
- syntax highlighting selected from the target path (Go, Markdown, shell,
  JSON, and the other languages the markdown highlighter already knows).

The existing `internal/tui/diff` parser owns hunk parsing and sanitization. The
transcript renderer does not introduce a second diff engine.

## 3. Responding and reasoning spinners

- The activity line uses the shared Braille frames
  `⠋ ⠙ ⠹ ⠸ ⠼ ⠴ ⠦ ⠧ ⠇ ⠏` for the `reason` and `respond` phases.
- A live reasoning trace replaces its static collapse marker with the same
  spinner. When the trace closes, the static `▸`/`▾` marker returns; settled
  history never animates.
- Running tools and shell escapes keep their established static liveness
  markers. The enhancement is scoped to model responding and reasoning, not a
  return to per-tool spinners.
- The program advances the shared tick every 90 ms. Stream deltas, tool
  progress, and metric updates cannot advance the glyph faster. Reduced-motion,
  `NO_COLOR`, and non-TrueColor terminals keep the throttled spinner but skip
  the optional shimmer.

## 4. Performance and verification

- Sidebar work is bounded: at most six task items, four integration rows, and
  four insight rows are rendered; the remainder is counted.
- The latest task is cached in `State`; a normal frame does not scan transcript
  history.
- Transcript block projections remain cached by revision, width, and frame. A
  presentation tick re-renders only blocks with a timed phase: the live clock,
  the open reasoning head, an entrance fade, or a dwell floor.
- Tests cover rail lifecycle gating (hidden when idle, question-only, todo-less,
  or terminal), the `/settings` preference and its persistence seam,
  split/sticky geometry, task de-duplication, footer-fact exclusion, truthful
  MCP integration projection, tinted permission previews, expanded diff
  metrics/highlighting, and the throttled spinner contract.

## 5. Smooth telemetry and progressive easing

This section is the exception to SPEC_UIUX.md §22–§23. The runtime never waits
for the UI: measured durations, outcomes, approval decisions, and cancellation
are recorded immediately. Only the already-known visual frame may be held or
eased.

- **Composer isolation.** `ComposerModel` input state and `TranscriptModel`
  telemetry share no locks, no scroll offset, and no row budget. Typing,
  editing, history recall, submission, and slash dispatch stay live while tools
  stream. A prompt entered during execution is queued; `/status`, `/usage`,
  `/theme`, `/cancel`, and the other commands run immediately. The composer is
  fixed geometry: activity mutations may change transcript rows, never the
  prompt's vertical position.
- **Minimum dwell.** A tool or reasoning block that reaches its terminal state
  inside the perception floor keeps its executing frame until 150ms from its
  real start. The semantic status, outcome, and duration are already final; a
  cancelled turn opts out and settles immediately. The floor is rendered from
  `DwellUntil`, never by sleeping a runtime goroutine.
- **Entrance easing.** A newly spawned block, and the trailing line of an
  answer when `AssistantDone` settles it, steps through three RGB phases over
  120ms: deep neutral vapour (0–30ms), a muted sage/teal solidifying phase
  (30–90ms), then the full palette target (reached at 100ms and stable through
  the 120ms window). The terminal has no alpha, so the short window flattens the
  affected rows to one interpolated foreground plus the ANSI dim flag; at 100ms
  the normal semantic palette returns. `NO_COLOR`, dumb terminals, reduced
  motion, and colorless profiles skip the effect. Text, wrapping, identities,
  and measured durations never change.
- **Batch telemetry footer.** Adjacent completed calls collapse into one
  persistent row: `+ Executed 8 tools in 420ms (ctrl+o to expand)`. The footer
  carries the batch's measured wall span, not a fabricated sum. Ctrl+O expands
  the tree in place with each call's invocation and measured duration, plus its
  compact outcome; collapsing restores the single row. A batch with a running
  member, or a trailing batch in a turn that may still grow, keeps the
  individual rows visible.
- **Stable viewport.** Collapsing or expanding a tree re-resolves
  `Viewport.Offset` in the same update. The reading anchor is `(BlockID, row)`,
  so expansion above the window moves the offset exactly enough to keep the
  anchored content under the top row instead of popping the viewport.
- **Telemetry integrity.** Completed tool and thought headers always render the
  runtime's measured duration (`0ms` included); an unknown start still renders
  no timer rather than fabricating one.
