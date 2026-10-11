# UI Implementation Plan

Bounded vertical slices, in the order the evidence supports. Each slice fixes the
earliest incorrect owner, lands with a test that fails without it, and ends with
a real CLI check. Unrelated slices are never combined.

---

## Gate for every slice

```sh
go build ./...
go test ./...        # full suite must stay safe
go vet ./...
gofmt -l .           # must be empty
```

Plus a real CLI check (§ Real CLI validation). "Compiles" is never completion.

---

## Slice 1 — Frame correctness: fenced code overflowed the terminal — **DONE**

**Defect.** `tui/markdown.go` `renderCodeLine`, `tui/width.go`.
**Why first.** It is the only finding that corrupts *every* frame containing a
code block, and it explains the ghosted/misaligned screenshots. It sits at the
earliest owner and has no design ambiguity.

**Done.**
- `expandTabs` in `tui/width.go`; called at the top of `renderCodeLine`.
- `TestCodeFenceTabsNeverWidenTheFrame` — the specific regression.
- `TestRenderedFrameFitsTerminalExactly` — the pipeline-wide invariant
  (10 content shapes × 6 sizes), so no future block can repeat it.

**Result.** At 100×30: widest row 106 → 99 cells; frame exactly 30 rows. Both
tests fail without the fix (42 assertion failures). Build, vet, gofmt, and the
full suite green.

---

## Slice 2 — Approval boundary — **DONE (one residual open)**

**Defects.** BUG-004 (navigation always reaches "Yes"), BUG-005 (Ctrl+C
swallowed), BUG-006 (hint row contradicts the dialog). Approved as "clamp
approval navigation".

**Done.**
- `PendingApproval.Move` clamps at both ends (`tui/model.go`).
- `ctrl+c` inside the approval switch routes to the existing `cancelTurnNow`,
  which denies the blocked tool before cancelling.
- The composer hint row is dropped in `ComputeLayout` while `Pending != nil`, so
  reserved and rendered rows still agree.

**Tests that prove it.**
1. `TestApprovalNavigationStopsAtTheEndInsteadOfWrapping` — forward keys
   (`down`, `right`, `tab`, `j`, `l`) cannot leave the conservative default.
2. `TestApprovalNavigationStaysWithinTheChoiceList` — the cursor never leaves the
   list under repeated movement in either direction.
3. `TestApprovalCursorStopsAtTheEnds` — the clamp arithmetic (replaces
   `TestApprovalCursorWraps`, which asserted the old wrap).
4. `TestCtrlCDuringApprovalDeniesAndCancels` — the blocked tool is denied and the
   prompt closes.
5. `TestComposerHintsNeverAdvertiseKeysTheApprovalHasCaptured` — the rendered
   frame advertises nothing the dialog has taken, and still shows the dialog.

**Result.** All green, including `go test -race ./tui/`. Real CLI: exit 0,
alt-screen entered/exited once, no over-wide rows.

**Residual.** A *backward* move still reaches "Yes" in a two-option dialog, and
Enter confirms it. No movement policy can prevent that; it needs an explicit `y`
to approve. Tracked in `UI_DECISIONS.md` § OPEN.

---

## Slice 3 — Turn lifecycle integrity

**Defects.** BUG-002, BUG-003 (concurrent turns), BUG-008 (droppable lifecycle
events).

**Why.** These corrupt history and can strand the UI in a permanently false
state. BUG-002/003 are small and self-contained: guard `drainQueue` on `Status.Busy`
and set `Busy`/clear `Input` synchronously in `submit`. BUG-008 needs a design
decision (`UI_DECISIONS.md`).

**Tests.**
1. Enter on an empty composer during a live turn starts no second `Loop.Run`
   (assert with a concurrent-run counter, and run under `-race`).
2. Double-Enter submits once.
3. A dropped `AssistantDone` still reaches a terminal state.
4. A dropped `ToolFinished` still resolves the tool card.

**Risks.** Changing the channel's drop policy touches `TestPublishOnFullBufferNeverBlocks`.

---

## Slice 4 — Truthfulness polish

**Defects.** BUG-007 (`context 0%`), BUG-009 (premature cancellation),
BUG-010 (fence delimiters).

**Tests.** `0%` is absent until a measurement exists; a notice claiming
cancellation appears only after the runtime confirms; the chosen fence treatment
renders identically across chunk splits.

---

## Slice 5 — Diagnostics that the spec asks for but the runtime cannot supply

Truncation remainder inspection, and stale-event detection. Both require runtime
contract changes, so per spec §14 they are **reported, not designed**, until
approved.

---

## Real CLI validation

Performed on Slice 1 with the actual `ohgo` binary:

| Check | Result |
| --- | --- |
| `ohgo run` against a live local model, full agent loop | exit 0, correct output |
| `ohgo doctor` | 8 structured checks, each failure naming its fix |
| TUI launched in a real pty (100×30) | alt-screen entered **once**, exited **once**, exit 0 |
| TUI frame width in a real run | 0 rows wider than 100 cells |
| Frame via `Render(State)` with a streamed answer | exactly 30 rows, widest 99 cells |

**Honest limits.**
- A pty run could not be driven with injected keystrokes — Bubble Tea did not
  receive them — so a *streamed answer inside the live alt-screen* was validated
  through `Render(State)`, which `Program.View()` calls directly
  (`tui/app.go:1259`), rather than through screen scraping.
- No before/after performance measurement has been taken for any optimisation,
  so no speed claim is made.
- The `img-bugs/` screenshots predate the current branch; two reported symptoms
  (cascading list indent, raw `**` leakage) do not reproduce on it and were not
  "fixed" on the strength of an image alone. See `UI_BUG_MATRIX.md` § Not
  reproduced.

---

## Acceptance checklist status

- [x] Existing runtime and CLI semantics preserved
- [x] No new UI-owned execution state machine
- [x] Streaming content neither lost, duplicated nor reordered — *in the renderer*;
      BUG-008 shows the event path can still corrupt it
- [x] Markdown fixtures render per documented terminal behaviour
- [x] Terminal resize, wrapping, scrolling and Unicode tested
- [x] Slash autocomplete reflects the real registry (unchanged)
- [x] Keyboard behaviour and visible hints agree — BUG-006 fixed; residual
      backward-to-"Yes" in BUG-004 remains open
- [x] Alt-screen and terminal restoration validated
- [ ] Tool/approval displays fully backed by facts — **BUG-008 open**
- [x] No fabricated progress or verification introduced
- [x] No dependency changes
- [x] Real CLI validation recorded