# Phase UI — Logical Selection Geometry + MouseUp Clipboard Closure

## 1. Verdict

**PASS**

Same-row partial selections highlight exactly the selected columns. Multi-row
selections use first/middle/last geometry with no padding painted. Unicode,
wrapped rows, and styled content agree between paint and copy. MouseUp
reliably finalizes the logical selection and emits one OSC 52 copy through
the existing clipboard path; empty drags copy nothing and the selection
stays visible after copy. `go test ./...` and `go test -race ./...` pass.

Verification tree: commit `1f6d71a` plus the working-tree changes described
here. Go 1.27.2, darwin/arm64. `gofmt -l .` is empty; `go build ./...`,
`go vet ./...`, `golangci-lint run ./...`, `go test ./...`, and
`go test -race ./...` all pass.

---

## 2. Root causes

### Defect A — highlight overreach (three compounding causes, one layer each)

1. **Missing row guards in `ApplySelectionHighlight`** (`tui/selection.go`).
   The painter checked only the block range, never the row range inside the
   endpoint blocks. Any row of the anchor block above the anchor row, and any
   row of the focus block below the focus row, rendered as fully selected.
   A mid-block drag therefore painted entire visual rows outside the logical
   range, while `CopySelection` (which iterates bounded rows) copied the
   correct text. First incorrect layer: highlight geometry, not the range.

2. **No content-width clamp in the paint path.** `ApplySelectionHighlight`
   passed raw normalized columns to `HighlightSelection`, which clamped the
   end column to the *styled* width. User-block rows are padded to the full
   content width before styling, so any end column past the logical text
   painted the renderer padding as selected. The copy path clamped to the
   plain width and was correct. First incorrect layer: highlight geometry.

3. **Divergent wide-glyph boundary rules.** `HighlightSelection` cut the
   styled string with `ansi.Cut` while `CopySelection` cut plain text with
   `CellSlice` ("include a wide glyph when any of its cells intersect the
   range"). For a range starting or ending mid-glyph the two disagreed: e.g.
   cells `[0,2)` over `A你B` copied `A你` but painted only `A`, and
   `[1,2)` copied `你` but painted nothing. First incorrect layer: the
   highlight cutter, which did not share the copy's cell semantics.
   Combining marks had the same class of split: a trailing mark evaluated at
   the already-advanced column was dropped by a half-open end boundary in
   both paths.

### Defect B — MouseUp clipboard failure

`handleTranscriptMouse` (`tui/mouse_selection.go`) finalized only releases
carrying `Button == MouseButtonLeft`. SGR encodes a left release as
`ButtonLeft`, but X10 encodes it as `ButtonNone` (and Windows Terminal
reports some motion as release-shaped events). On those terminals every
release was ignored: `p.drag` leaked, no `Selection` was finalized, and no
clipboard write was queued — while SGR terminals worked, hence "not
reliably". First incorrect layer: the release event guard, not the
clipboard path (OSC 52 framing, Base64, tmux/screen packaging, and the
disabled-clipboard no-op were all verified correct and untouched).

---

## 3. Implementation

### Files changed

- `tui/selection.go` — single canonical range, unified cell semantics.
- `tui/mouse_selection.go` — release/motion button acceptance.
- `tui/selection_closure_test.go` — new: full A–L requirement matrix (14 tests).

### Selection changes

- Added `rowSelectionInterval`: the **one definition** of per-row geometry.
  Both `selectionRows` (copy) and `ApplySelectionHighlight` (paint) consume
  it. Half-open `[startCol, endCol)` clamped to the row's plain content
  width; rows outside the canonical range (including endpoint-block rows
  outside `[startRow, endRow]`) report unselected; blank rows select no
  cells (interior blanks are still preserved as line breaks for the copy).
- `normalizeSelection` remains the single canonicalization (document-order
  swap, so reversed drags share one range). Unchanged.

### Geometry changes

- `ApplySelectionHighlight` now resolves its interval through
  `rowSelectionInterval` and paints nothing for unselected rows. Padding
  past the content is never painted (columns clamp to plain width).
- `HighlightSelection` rewritten as a rune walk over the styled string with
  `CellSlice`-identical cell semantics: wide glyphs included on any cell
  intersection, tabs advancing to `CodeTabWidth` stops, combining marks
  attached to their base cell, ANSI escapes passed through untouched.
  Reverse video is emitted as zero-width `7m`/`27m` markers at selection
  transitions (re-asserted after in-span resets), so width is unchanged and
  a grapheme is never split between paint and copy.
- `CellSlice` given the same tab-stop and combining-mark attachment rules
  so copy and paint cannot diverge again.

### MouseUp changes

- `MouseActionRelease` finalizes whenever a drag is active and the button
  is `Left` **or** `None` (X10-style release). Any other button leaves the
  drag in place instead of finalizing or dropping it.
- `MouseActionMotion` updates the focus on `Left` or `None` while a drag is
  active; hover without a press is still ignored.
- Lifecycle is otherwise unchanged: release re-resolves the endpoint
  through the current layout, sets `Selection` from anchor to focus,
  projects logical text via `CopySelection`, and queues one OSC 52 write.
  Selection is preserved after copy; empty ranges remain a no-op upstream
  (`CopySelection` returns `!ok`); clipboard errors stay silent with the
  selection intact.

### Clipboard changes

None. OSC 52 framing, UTF-8 → Base64 payload, tmux/screen packaging,
`ClipboardAvailable`, and the `OHGO_CLIPBOARD=off` / dumb-terminal no-op
were verified correct by existing tests plus throwaway constructor checks
and left untouched.

### Tests added (`tui/selection_closure_test.go`)

A. `TestSelectionClosureSameRowPartial` — `world` / `ell` paint + copy.
B. `TestSelectionClosureReversedDrag` — reversed range, per-row parity.
C. `TestSelectionClosureFirstMiddleLast` — `[4,12)/[0,12)/[0,3)` intervals,
   highlight/copy agreement, blank separators unpainted.
C2. `TestSelectionClosureSameBlockRowGuards` — out-of-range rows unpainted.
D. `TestSelectionClosureWrappedRows` — cross-row paint == copy lines.
E. `TestSelectionClosureUnicode` — ASCII, é, CJK, emoji, combining marks,
   full-range and half-cell agreement.
F. `TestSelectionClosureStyledContent` — visible highlight, plain copy.
G. `TestSelectionClosurePadding` — overshoot select paints/copies content only.
H. `TestSelectionClosureMouseUpCopiesOnce` — event path, exactly one call,
   selection retained, payload decodes to `world`.
H2. `TestSelectionClosureMouseUpButtonNone` — X10 release copies, drag cleared.
I. `TestSelectionClosureEmptyDragNoCopy` — press/release, zero calls.
J. `TestSelectionClosureScrollDuringSelection` — wheel mid-drag, one copy,
   retained copy equals payload.
K. `TestSelectionClosureDetachedStreaming` — detached anchor stable across
   `AssistantDelta` appends, copy unchanged.
L. `TestSelectionClosureResize` — BlockID, copy, and highlight stable across
   30/80/45 widths.

---

## 4. Verification

```text
gofmt -l .                       → empty
go build ./...                   → pass
go vet ./...                     → pass
golangci-lint run ./...          → 0 issues
go test ./...                    → all packages ok (tui 21.9s)
go test -race ./...              → all packages ok (tui 29.2s)
go test ./tui/ -run 'TestSelection|TestMouse|TestClipboard|TestProgramCopy|TestOSC|TestHitTest' → all pass
BenchmarkViewport                → scroll 0 rendered/op, hit-test 0 rendered/op,
                                   stream/append/expand/collapse 1 rendered/op
OHGO_CLIPBOARD=off constructor   → clipboard nil (silent no-op), throwaway check
TERM=dumb constructor            → clipboard nil (silent no-op), throwaway check
```

Manual adversarial equivalent (deterministic simulation, no terminal
required): multi-block transcript with wrapped, styled, short, long, and
Unicode rows; scroll to historical content; drag mid-line to mid-line across
a wrapped row; wheel-scroll mid-drag; release; highlight spans and the
Base64-decoded OSC 52 payload asserted equal to the logical text; repeated
reversed, wide-viewport short-line, post-resize, and clipboard-disabled.
All covered by tests H–L above plus G.

---

## 5. Regression status

- Viewport scrolling: unchanged; `scroll` renders 0 blocks (bench).
- Hit testing: unchanged; renders 0 blocks (bench); scroll/expand/survive
  tests pass.
- Selection + scrolling: preserved and extended (J, K).
- Incremental rendering: unchanged; stream/append/expand/collapse render 1
  block (bench).
- Expand/collapse: unchanged; alt-click, `ctrl+o`, historical-expansion
  tests pass.
- Resize: selection stable, highlight reflows (L + existing resize tests).
- `--live`: untouched — no mouse mode, terminal owns selection.
- `mouse=select`: untouched — default mode ignores pointer drags
  (`TestSelectionModeIgnoresPointerDrags`).
- `mouse=report`: fixed per above; wheel routing unchanged.

State isolation: selection/copy touch only `State.Selection`, `drag`, and
`pendingClipboard`. No task, session, runtime-authority, execution, or
document mutation (covered by `TestMouseClickNeverSubmitsOrAuthorizes` and
`TestMouseOtherButtonsLeaveStateAlone`).

---

## 6. Known limitations

- Tool-block selections copy the rendered projection (badge/outcome chrome
  such as the `⎿` line and expanded-body indent), not the canonical
  `BlockCopyText`. Paint and copy still agree with each other; only the
  definition of "logical" for tool chrome is unchanged pre-existing
  behavior, out of scope for this fix.
- `HighlightSelection` re-asserts reverse video after bare `\x1b[0m` /
  `\x1b[m` resets only; exotic reset sequences inside a styled span would
  drop the attribute to the row end. No renderer emits those today.
- Grapheme attachment covers base + combining marks; extended grapheme
  clusters (e.g. ZWJ emoji sequences) are handled per-rune with the same
  rule on both paths, so paint and copy still agree.
