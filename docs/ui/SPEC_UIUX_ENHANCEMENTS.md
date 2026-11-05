# Ohgo UI/UX Enhancements: Task Rail, Diff Surface, Spinners

This document is the canonical contract for the UI enhancements layered on the
execution transcript. It supersedes SPEC_UIUX.md §7.1 (spinner animation) and
the sidebar prohibition in §19 for the specific surfaces described here. The
rest of SPEC_UIUX.md still governs the transcript: one live activity, stable
history, collapsed reasoning, bounded output, and no artificial delays.

## 1. Persistent task rail and sticky fallback

The full-screen canvas is responsive without changing execution state.

- **Wide terminals** (`LayoutWide`, at least 100 columns and 12 rows) split the
  body into the conversation column and a fixed right rail. The rail is 28% of
  the terminal width, clamped to 26–56 cells, with a one-cell divider. The
  conversation always keeps at least 48 cells.
- The rail renders, top to bottom: the current task tracker, active MCP/LSP
  integrations, and contextual task insights.
- The tracker is sticky by construction: scrolling the transcript changes only
  the conversation column. The rail is a projection, never a second viewport.
- **Narrow and medium terminals** hide the rail and reserve exactly one sticky
  task row immediately below the title separator. That row names the objective,
  lifecycle status, progress, and the one active todo.
- The transcript suppresses its own task block whenever the tracker owns the
  projection. One fact has one home on the same screen.
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
- Transcript block projections remain cached by revision, width, and tick. A
  tick re-renders only the live block, the open reasoning head, and the activity
  line.
- Tests cover split/sticky geometry, task de-duplication, footer-fact
  exclusion, truthful MCP integration projection, tinted permission previews,
  expanded diff metrics/highlighting, and the throttled spinner contract.
