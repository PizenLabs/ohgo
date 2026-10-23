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

---

## Automatic Copy UX Closure

### 1. Automatic copy behavior

`MouseUp` finalizes the logical selection (`Start`/`End` from anchor to
focus), projects it via `CopySelection`, and queues one OSC 52 write through
the existing clipboard path. `queueClipboard` now returns the confirmation's
expiry command: on success `markCopySuccess` sets `State.CopyNotice =
"Copied"` and schedules `copyNoticeExpiredMsg`; empty selections, a nil
clipboard (disabled/unsupported), and emission failures (`""` sequence)
return `nil`, show nothing, and keep the selection intact. Alt-click block
copies share the same success gate. Files: `tui/mouse_selection.go`,
`tui/copy_notice.go`, `tui/app.go` (`handleMouse` propagates the command).

### 2. Status rendering location

`renderHeader` (`tui/view.go`) layers the transient notice over the permanent
header at the far right: the model segment's budget is reduced by
`CellWidth(notice)+2` before truncation, so permanent shedding order is
unchanged, then the left is padded with spaces to `displayWidth` and the
notice renders in success bold. Unknown widths join with two spaces and never
invent a width. No new panel, popup, status bar, viewport-height, or document
mutation; `ComputeLayout` never reads the notice.

### 3. Status lifetime

`copyNoticeTTL = 1500ms` (inside the required 1.5–2.0s window,
`TestCopyNoticeTTLInRange`). No busy loop, no render-path sleep, one one-shot
`tea.Tick` per copy; the update goroutine owns the clear.

### 4. Disabled/unsupported clipboard behavior

`clipboard == nil` (explicit `OHGO_CLIPBOARD=off` and dumb/empty `TERM` via
`ClipboardAvailable`) traces unsupported and returns `nil`: no queue, no
`Copied`, selection preserved (`TestCopyNoticeDisabledClipboardNoFalseSuccess`,
`TestCopyNoticeUnsupportedTerminalNoFalseSuccess`). An empty sequence from the
backend is treated as emission failure with the same no-false-success contract
(`TestCopyNoticeEmissionFailureNoFalseSuccess`).

### 5. Stale timer protection

Every confirmation carries `State.CopyGen`; each success increments it and
schedules its own expiry. `update` handles `copyNoticeExpiredMsg` via
`clearCopyNotice`, which ignores generations trailing the current one. The
latest copy always owns the expiry (`TestCopyNoticeRepeatedCopyStaleExpiryIgnored`,
`TestCopyNoticeExpiryClears`). Session boundaries (`Cleared`,
`HistoryReplaced`) clear the notice without touching the transcript.

### 6. Tests

New `tui/copy_notice_test.go`: auto-copy payload + status, partial `world`,
reversed drag, wrapped rows with line break, Unicode CJK, empty drag no-copy,
disabled/unsupported/emission-failure no-false-success with selection intact,
expiry clear without sleeps, repeated-copy stale-expiry guard, resize
far-right positioning with viewport validity, no transcript mutation (block
count/IDs, no `Copied` block/row), runtime isolation (workspace/session/busy,
history, approvals), header chrome composition, TTL range. Break-check:
blanking the notice fails `TestCopyNoticeAutoCopyShowsCopied`.

### 7. Manual verification

Deterministic headless equivalent of the acceptance path (no terminal
required): multi-block transcript, scroll-detached historical content, drag
mid-line to mid-line across wrapped rows, wheel-scroll mid-drag, release;
Base64-decoded OSC 52 payload asserted equal to the logical text; header
plain line asserted to end with `Copied` at `displayWidth`; repeat-copy,
disabled, post-resize, wrapped-Unicode, and reversed-drag variants all
covered by the tests above plus the existing H–L closure matrix.

### 8. Regression verification

```text
gofmt -l .                       → empty
go build ./...                   → pass
go vet ./...                     → pass
golangci-lint run ./...          → 0 issues
go test ./...                    → all packages ok (tui 21.8s)
go test -race ./...              → all packages ok (tui 28.9s)
go test ./tui/ -run 'TestCopyNotice|TestSelectionClosure|TestMouse|TestClipboard|TestProgramCopy|TestHeader' → pass
BenchmarkViewport                → scroll 0 rendered/op, selection cheap, stream/append 1 rendered/op
```

Selection geometry, logical extraction, OSC 52, tmux/screen packaging,
mouse report/select, live, following/detached, historical expansion,
incremental rendering, and Fenwick layout unchanged; the notice never enters
`Document`/`Block`/`Selection`/`Task`/`Session`/`Runtime` and never rebuilds
layout.

---

## Real Clipboard Transport Closure

### 1. Original false-positive mechanism

The previous PASS showed `Copied` whenever an internal string was queued:
`queueClipboard` encoded the text, stored it in `pendingClipboard`, and
`markCopySuccess` ran unconditionally; `View` prepended the string to the
frame for the Bubble Tea renderer. Unit tests asserted on `pendingClipboard`
and the `View` prefix, so they passed while the real clipboard stayed
unchanged. Two defects hid behind the green suite: (a) inside tmux the
emitted bytes were a DCS passthrough wrapper the multiplexer drops by
default, and (b) success meant "queued", not "emitted to the terminal
transport" — a renderer drop or writer failure would still show `Copied`.

### 2. Exact OSC 52 call path

```text
tui/mouse_selection.go: handleTranscriptMouse (MouseUp)
  → copySelection → CopySelection (tui/selection.go: logical text)
  → queueClipboard → p.clipboard(text) (tui/clipboard.go: OSC52SequenceFor)
  → io.WriteString(p.termOut, seq) (os.Stdout in production)
  → markCopySuccess → State.CopyNotice = Copied + expiry Cmd
  → app.go: update handles copyNoticeExpiredMsg via clearCopyNotice
```

`tui/copy_notice.go` owns the notice, generation, TTL, and expiry message.
`tui/view.go:renderHeader` renders the notice only. No transcript, document,
session, runtime, or authority state is touched.

### 3. Exact output writer

`p.termOut` (`os.Stdout` in production, a recording `bytes.Buffer` in
tests). `queueClipboard` performs one synchronous unbuffered
`io.WriteString` with no queue, no next-frame delay, and no flush boundary
to trap bytes. A non-nil error or short write traces unsupported and returns
`nil` without `Copied`; only a complete write earns the notice. `View`
never carries clipboard bytes, so renderer truncation, diff-skipping, and
line splitting cannot drop them.

### 4. Buffering

None in ohgo's path. The old `pendingClipboard` + `View`-prepend queue was
removed (`tui/app.go:View` no longer references it). Emission is proven
synchronous by `TestClipboardTransportFlushesImmediately`: the writer holds
the framed bytes immediately after the release `Update` returns, before any
further `View`/render.

### 5. Tmux wrapping correctness

`OSC52SequenceFor` with `tmux==true` previously returned the DCS wrapper
`\x1bPtmux;\x1b…\x1b\\`, which requires `allow-passthrough on`. The
environment runs tmux 3.8 with `allow-passthrough off` (default) and
`set-clipboard on`. Controlled test on the active pane's TTY: raw
`\x1b]52;c;BASE64\x07` updated the OS clipboard (`pbpaste` matched);
the DCS-wrapped form left it unchanged. The fix returns the raw sequence
inside tmux so the multiplexer forwards it via `set-clipboard`; GNU screen
keeps its documented DCS wrapper, direct terminals stay raw
(`TestClipboardTransportTmuxUsesRaw`,
`TestClipboardTransportScreenAndDirect`).

### 6. Renderer alteration

Bubble Tea 1.3.10 `standard_renderer` truncates each line with
`ansi.Truncate`, which preserves a leading OSC 52
(`TestClipboardRendererPreservesOSC52`), and diff-skips unchanged lines —
which is why the old frame-riding design looked correct in unit tests while
tmux dropped the (wrongly wrapped) bytes downstream. The new path bypasses
the renderer entirely; `TestClipboardTransportNeverRidesTheFrame` asserts no
frame ever contains `\x1b]52;c;`.

### 7. Direct Ghostty result

The tmux E2E terminates in Ghostty (the outer terminal): raw OSC forwarded
by tmux updated `pbpaste` to the exact token (`OHGO_CLIPBOARD_E2E_7F31A`,
then isolated `ISO_*` tokens). A tmux-free Ghostty pane was unavailable in
this environment (all panes are tmux clients); the direct case is classified
as inferred-pass: the byte sequence is identical (raw OSC 52) with fewer
intermediaries, and Ghostty already accepted it as the outer terminal.
No double wrapper is ever emitted.

### 8. Ghostty + tmux result

PASS on the attached/active window's pane: MouseUp through production
`queueClipboard` with `termOut` set to the real pane TTY wrote the exact
framed bytes and `pbpaste` returned the selected text verbatim
(`OHGO_CLIPBOARD_E2E_7F31A` full-token direct write; MouseUp-driven
`[0,24)` selection decoded to the same 24 chars in-process with `Copied`
set). Inactive windows' output is (correctly) not forwarded by tmux, so E2E
must target the visible window — expected multiplexer behavior, not an ohgo
defect.

### 9. Minimal OSC 52 probe result

Standalone probe (no ohgo): raw `\x1b]52;c;BASE64\x07` to the active pane
TTY updated `pbpaste`; `\x1bPtmux;\x1b…\x1b\\` did not. This separates
terminal capability (Ghostty + tmux `set-clipboard on` accept raw OSC 52)
from the old ohgo defect (emitted the wrapper). Probe removed after
diagnosis; the permanent coverage is `tui/clipboard_transport_test.go`.

### 10. Final success semantics for `Copied`

`Copied = the OSC 52 sequence was synchronously and completely written to
the application's terminal output transport (termOut).` It is emission
success, not an OS-level acknowledgement — OSC 52 provides no portable
synchronous ack. Writer failure, empty text, disabled (`OHGO_CLIPBOARD=off`)
or unsupported (`dumb`/empty `TERM`) terminals, and a nil writer never show
`Copied` and never disturb the selection
(`TestProgramCopyWriterFailureIsSilent`,
`TestClipboardTransportNegativeMatrix`).

### 11. Tests

New `tui/clipboard_transport_test.go`: exact bytes + base64 round-trip for
short/1KB/10KB/Unicode/multiline payloads, ANSI exclusion, tmux-raw vs
screen-wrapper vs direct, BEL termination (no ST), synchronous flush before
`View`, frame never carries OSC, renderer preservation note, and the full
negative matrix (writer failure, empty, disabled, unsupported) with
selection preserved. Updated `tui/clipboard_test.go`,
`tui/selection_closure_test.go`, `tui/copy_notice_test.go` to capture real
writer bytes via a recording `termOut` instead of the removed queue;
`TestOSC52TmuxAndScreenPackaging` now pins the raw-tmux contract.
Break-checks: blanking the write fails `TestCopyNoticeAutoCopyShowsCopied`;
restoring the DCS wrapper fails `TestClipboardTransportTmuxUsesRaw` and the
live `pbpaste` E2E.

### 12. Manual E2E verification

Active-window pane TTY + `pbpaste`: (a) raw probe token round-tripped
exactly; (b) fixed `OSC52SequenceFor(..., tmux=true)` round-tripped
`OHGO_CLIPBOARD_E2E_7F31A`; (c) production MouseUp path with `termOut` set
to the real TTY set `Copied` and `CopySelection` decoded to the same 24
chars. ASCII, Unicode, multiline, wrapped, and reversed selections are
covered by the transport + notice suites (payloads asserted byte-exact);
BEL termination and no-truncation (1KB/10KB) covered without real sleeps.
Diagnostic logging was never added; no selected text or base64 payload is
logged — only lengths, byte counts, and transport state.
