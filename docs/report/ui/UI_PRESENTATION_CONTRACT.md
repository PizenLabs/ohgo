# UI Audit — ohgo TUI

Scope: the Go + Bubble Tea workbench in `tui/`, audited against
[`OHGO_UI_UX_SPEC.md`](OHGO_UI_UX_SPEC.md). Audit date: 2026-10-11, branch
`fix/tui`. Everything below was traced in source and, where marked *verified*,
reproduced by running the code.

Status of this document: **audit of the current worktree complete for the
render/markdown, layout, keyboard, and event-lifecycle areas.** Streaming
throughput baselines are partial (§ Performance). Findings are carried in
[`UI_BUG_MATRIX.md`](UI_BUG_MATRIX.md); this file is the map.

---

## 1. Ownership and data paths

The architecture the spec asks to preserve is intact and is the right one:

| Concern | Owner | Terminal-free? |
| --- | --- | --- |
| Presentation state | `State` + `State.Apply(Event)` (`tui/model.go:231`) | yes |
| Rendering | `Render(State)` (`tui/view.go:144`) — pure, no I/O | yes |
| Bubble Tea bridge | `Program` (`tui/app.go`) | no |
| Runtime truth | `agent.Loop`, `ai.StreamFn`, tools | yes |

The critical boundary holds: `Program.View()` is a one-line delegation to
`Render(p.state)` (`tui/app.go:1259`), so **any test that calls `Render(State)`
exercises exactly what the terminal receives.** This is what makes headless
frame assertions valid evidence rather than a proxy.

### Pipeline

```
provider deltas ──► ai.Stream ──► agent loop ──► Program.publish
                                                      │  buffered channel, drops on full
                                                      ▼
                                              State.Apply(Event)
                                                      ▼
                                    renderTranscriptBody → renderEntry
                                                      ▼
                             RenderMarkdownWrapped(text, pal, textWidth(layout))
                                                      ▼
                                       wrapChunks → parseInline → padCode
                                                      ▼
                              renderTranscriptArea (slice to ConversationHeight)
                                                      ▼
                                    Render(State) → Program.View() → terminal
```

Width is computed once per render (`ComputeLayout` → `textWidth(layout)`) and
handed to the markdown layer, which measures **plain cells first and applies
styling afterwards**. That is the invariant the whole layout rests on: ANSI must
never inflate the budget. The defect in Slice 1 was a violation of exactly this
invariant (see §3).

### Key map (current)

| File | Responsibility |
| --- | --- |
| `tui/model.go` | `State`, `Apply`, transcript entries, scroll/follow, queue |
| `tui/view.go` | `Render`, header/transcript/composer/approval/picker regions |
| `tui/layout.go` | Responsive tiers, `ConversationHeight`, `ComposerLines` |
| `tui/markdown.go` | Markdown → styled terminal lines; width ownership |
| `tui/width.go` | `CellWidth`, `WrapToWidth`, cell slicing |
| `tui/sanitize.go` | Trust boundary for untrusted text |
| `tui/style.go` | Semantic tokens, runtime phases, composer hints |
| `tui/app.go` | Bubble Tea bridge, event channel, turn lifecycle, approvals |

---

## 2. Verified-correct (do not regress)

These were checked and found sound; several are easy to "fix" wrongly.

- **Markdown streaming convergence.** The claim in `RenderMarkdownWrapped`'s doc
  comment (`markdown.go:36-39`) holds. A sweep of 5 texts × 3 widths × 7 chunk
  splits (byte splits including mid-rune and mid-fence) showed **no counterexample
  and zero line-count jump**. Already covered by
  `TestMarkdownConvergesRegardlessOfChunkSplits` and
  `TestMarkdownCRConvergesAcrossStreamPrefixes`.
- **Follow is respected.** A new delta never force-scrolls to the bottom:
  `growScroll` is gated on `!s.Follow` and `Scroll` is never zeroed. (The
  *magnitude* of the growth is wrong — BUG-006.)
- **Alt-screen lifecycle.** Delegated to `tea.WithAltScreen()` (`app.go:1283`);
  Bubble Tea restores the terminal on normal exit, error, **and panic**. Approvals
  are released on shutdown so no tool is stranded (`app.go:1289-1291`).
- **Queue cap and ordering.** Cap 5, FIFO, refusal is explicit; the queue is
  cleared on cancel.
- **Layout derivation.** `ComputeLayout` is derived per render and never stored,
  so a resize cannot leave stale geometry.

---

## 3. Slice 1 — fixed: fenced code overflowed the frame

**Root cause (verified).** `renderCodeLine` measured the code line *before*
styling, and `CellWidth` counts a tab as **zero** cells. lipgloss expands tabs to
four spaces while styling. The newly added `padCode` then padded to the
mis-measured width, so every tab-indented row landed 4 cells per tab past its
budget.

This is a measurement/render disagreement, not a padding bug — it violates the
documented "measure plain, style after" invariant, so the fix belongs at the
measurement, not at the padding.

```go
// tui/width.go
func expandTabs(s string) string // tabs → spaces at the next CodeTabWidth stop

// tui/markdown.go — renderCodeLine
line = expandTabs(line) // before any measurement
```

**Before/after, at the real terminal size (100×30), same branch:**

| | widest row | frame rows |
| --- | --- | --- |
| before | **106 cells** (limit 100) | 30 + 4 soft-wrapped rows |
| after | 99 cells | 30 |

Four code rows overran the terminal, so the terminal soft-wrapped each into two
physical rows; the frame became 34 rows in a 30-row viewport, the screen scrolled,
and the status line and composer drifted while stale rows stayed on screen. This
is the ghosted, misaligned screen in `img-bugs/3.png` and
`img-bugs/Screenshot 2026-10-10 at 3.48.04 PM.png`.

**Regression tests:** `TestCodeFenceTabsNeverWidenTheFrame`,
`TestRenderedFrameFitsTerminalExactly`. Both fail without the fix (42 assertion
failures) and pass with it.

**Note on history:** `padCode` is part of the *uncommitted* work on this branch.
At `HEAD` the same mis-measurement existed but was latent (the block was not
padded, so rows ran *under* budget). Slice 1 fixed the measurement itself, so
both the padded and unpadded paths are now correct.

---

## 3a. Slice 2 — fixed: the approval boundary

Three defects on the human decision boundary, fixed together because they are one
boundary (BUG-004, BUG-005, BUG-006).

**Approval navigation.** `Move` wrapped, so with `{"Yes","No"}` and the cursor
opening on "No", *every* navigation key — both directions, plus `Tab` — resolved
to "Yes". Pressing an arrow to look around and then Enter reflexively ran the
tool. It now clamps. Because the conservative default is the *last* choice, it is
also an end, so **forward navigation can no longer leave it at all** and `Tab`
does nothing.

**Ctrl+C.** The approval switch had no `ctrl+c` case, so the reflex abort key was
swallowed and the turn stayed parked forever. It now routes to the existing
`cancelTurnNow`, which already denied the blocked tool before cancelling — that
logic was written and simply unreachable.

**Hint row.** The composer hint row rendered under the dialog advertising
`esc cancel` (which denied the tool) and `enter queue` (which confirmed the
highlighted choice). It is now dropped in `ComputeLayout` so reserved and
rendered rows still agree; the dialog prints its own key line.

**Residual, still open.** In a two-option dialog the affirmative is adjacent to
the default, so a *backward* move still reaches "Yes" and Enter confirms it. No
movement policy prevents that — it needs an explicit `y` to approve. See
`UI_DECISIONS.md`.

Verified by `go test -race ./tui/` and a real CLI run (exit 0, alt-screen
entered/exited once, no over-wide rows).

---

## 4. Presentation-truth findings

These are contract gaps, not layout bugs. They matter because the spec's central
rule is that the UI must never be more certain than the runtime.

- **BUG-007 — `context 0%` before any usage is recorded.** Reproduced in a real
  run: the status line reads `context 0%` at session start. The window is known
  (32768) so `Percent()` returns a real 0, but the ledger has no measurement yet.
  Spec §8.3 prefers an explicit unknown over a value that reads as authoritative.
  `img-bugs/4.png` shows the same line reading `0%` after eight messages, which
  would be a genuine lie — **that specific case is not reproduced on this branch**
  and should be re-checked against a live turn before acting.
- **BUG-009 — cancellation is claimed before the runtime confirms it.**
  `cancelTurnNow` publishes a `"cancelled"` notice whether or not a turn was live,
  and a live turn then publishes a second `"turn cancelled"`. Spec §9 forbids
  claiming cancellation before confirmation.

---

## 5. Performance

Partial. `BenchmarkAssistantStreaming` reports ~2.1 ms per full-frame render for
a 200-paragraph transcript; `tick()` reschedules unconditionally, so an idle
session re-renders at ~4 Hz forever (BUG-010). No before/after measurement has
been taken for any optimisation, so no optimisation is claimed.