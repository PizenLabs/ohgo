# UI Decisions

Decisions taken, trade-offs accepted, and the questions that still need an
answer. Anything marked **OPEN** is an approval gate: it changes interaction
semantics or crosses out of the `tui` boundary, so it is not being decided
unilaterally.

---

## D1 — Fix the measurement, not the padding (Slice 1)

**Decision.** Expand tabs to spaces in `renderCodeLine` before anything measures
them, rather than clamping `padCode`'s output.

**Why.** The defect was a disagreement between measurement and rendering: a tab
is counted as zero cells by `CellWidth` and painted as four by lipgloss. Clamping
the padded output would have hidden the symptom while leaving the invariant
broken, so the next content shape that pads differently would reintroduce it. The
spec is explicit — *"Fix the earliest incorrect owner in the pipeline."*

**Trade-off.** Go code is tab-indented; the block now shows 4-space indents.
This matches what lipgloss already did, so nothing changed on screen that was not
already changing. It is a presentational normalisation, and it keeps measurement
honest, which is worth more.

**Rejected.** (a) Clamp in `padCode` — treats the symptom. (b) Change
`CodeTabWidth` to 8 to match terminal defaults — lipgloss would still expand to
4, so measurement and rendering would still disagree.

---

## D2 — Add the frame invariant as a permanent test

**Decision.** `TestRenderedFrameFitsTerminalExactly` sweeps content shapes ×
terminal sizes and asserts the frame is exactly `Height` rows and no row exceeds
`Width` cells.

**Why.** BUG-001 was invisible to every existing test because each asserted one
block or one size. The user-visible contract is about the whole frame, so the
test has to be about the whole frame.

**Cost.** ~10 ms. Worth it — this is the invariant whose violation produced the
user-visible screenshots.

**Note.** Mini mode is excluded: it concedes the viewport on purpose.

---

## D3 — Preserve `State.Apply` / `Render` / `Program` separation

**Decision.** No changes to the ownership model.

**Why.** Verified sound, and the spec asks to preserve it unless evidence proves
a defect. `Program.View()` is a one-line delegation to `Render(p.state)`, which
is what makes headless frame assertions real evidence rather than a proxy.

---

## D4 — Record, do not fix, the runtime-contract gaps

**Decision.** BUG-002, BUG-003, BUG-008 and BUG-012 are reported, not fixed in
this slice.

**Why.** Each needs a design decision, not a patch (see the OPEN items below),
and the spec requires review between slices. BUG-012 lives outside `tui`.

---

## D5 — Clamp approval navigation; reuse the existing cancel path (Slice 2)

**Decision.** `PendingApproval.Move` clamps at both ends instead of wrapping.
`ctrl+c` inside the approval switch routes into the existing `cancelTurnNow`.
The composer hint row is dropped (in `ComputeLayout`) while a prompt is open.

**Why.** Approval is the one interaction where a mis-routed key has an
irreversible effect. Wrapping meant every navigation key was an escape from the
conservative default, and `Tab` — the conventional "move focus away" key —
approved the tool outright.

The conservative default is the *last* choice, so it is also an end. Clamping
therefore means **forward navigation cannot leave it at all**, and Tab now does
nothing. This is strictly toward the default the code already documents as the
intent (`defaultApprovalSelection`: *"the safest choice, which is the last one"*).

The Ctrl+C and hint-row fixes add no new logic: `cancelTurnNow` already denied the
blocked tool before cancelling, it was simply unreachable from this switch.

**Trade-off accepted.** `TestApprovalCursorWraps` asserted the old wrap
arithmetic and was rewritten to `TestApprovalCursorStopsAtTheEnds`. That is a
deliberate behaviour change, approved for this slice.

**Residual, still open.** In a two-option dialog the affirmative is *adjacent* to
the default, so a **backward** move (`up`, `left`, `k`, `h`, `shift+tab`) still
reaches "Yes" and Enter then confirms it. No movement policy can prevent this in a
two-item list; it needs approving only on an explicit `y`, which is a larger
change to the dialog's contract.

---

## D6 — Serve the wheel with alternate scroll mode, not mouse reporting

**Decision.** `Run` brackets the session with DEC private mode 1007 and leaves
mouse reporting off. Arrow keys scroll the transcript when, and only when, the
composer cannot use them.

**Why.** The two things the user needs — wheel scrolling and drag-select copy —
are in direct conflict under mouse reporting. Once the terminal is sending mouse
events it stops doing its own selection, so enabling 1000/1002/1003 costs
selection *and* forces the program to parse a report stream that bubbletea
v1.3.10 can split across input reads (a fragment then lands in the composer as
typed text). The previous code reached the right conclusion — reporting off —
but concluded the wheel was unobtainable, and said so in the comment.

Mode 1007 dissolves the conflict: the terminal keeps selection and copy exactly
as they are and translates wheel ticks into plain arrow keys, which bubbletea
already parses as whole sequences. No mouse protocol is involved at all.

**Trade-off, stated plainly.** The terminal cannot tell us an arrow came from the
wheel, so the policy is state-based (`wheelScrolls`): arrows scroll only when the
composer is empty and no overlay owns the keyboard — the state a reader is in.

- `↑`/`↓` on an idle empty composer now scroll instead of recalling history.
- **History recall is not lost.** `ctrl+p`/`ctrl+n` were already bound to the
  same handlers and still reach it, including the return-to-draft step;
  `TestHistoryRecallMovesToTheCtrlBindingsOnAnIdleComposer` pins both directions.
- With text in the composer, or an open picker/approval/secret, the arrows keep
  their editing and overlay behaviour untouched.

`TestUpFallsBackToHistoryOnASingleLine` asserted the old alias and was rewritten.
That is a real, if small, key-semantics change, taken because the alternative is
no wheel at all.

**Residual.** A user who recalls history with `↑` now scrolls instead. If that
proves annoying in practice, the alternative is to recall only while the
transcript has nothing to scroll.

---

## D7 — Make the mouse mode a setting, not a hardcoded guess

**Decision.** Two selectable modes: `select` (default, no mouse reporting, wheel
via mode 1007) and `report` (mouse reporting on). Exposed as `--mouse` and as
`mouse` in `~/.ohgo/settings.json`; any unrecognised value degrades to `select`.

**Why this was necessary.** D6 was reported as still not scrolling. The cause is
not a bug in D6 — it is that **mode 1007 cannot reach every terminal**, and
no application can fix that:

- Terminals that do not implement 1007 (macOS Terminal.app, the VS Code
  integrated terminal) never forward the wheel.
- A **multiplexer** such as tmux consumes wheel events for its own copy-mode
  before the pane's terminal sees them, so the application never learns the
  wheel moved at all.

There is no single setting that satisfies both needs, because they are mutually
exclusive by construction: the terminal either hands mouse events to the program
or performs its own selection, never both. Baking in either choice would
silently cost a capability the user has, so the choice is theirs to make, per
terminal and per session.

`report` works inside tmux precisely because tmux forwards mouse events to a
pane that requests them. That is the path the multiplexer leaves open.

**Degradation is asymmetric on purpose.** An unrecognised value resolves to
`select`, because silently enabling reporting would take away drag-copy — a
capability a user notices immediately — over a typo. Losing the wheel is
quieter and cheaper than losing copy.

---

## D8 — The mouse default follows the environment

**Decision.** `MouseDefaultFor(TMUX)` resolves `report` when a multiplexer is in
the path and `select` otherwise. Applied only when neither `--mouse` nor
settings specify a mode; an explicit choice always wins.

**Why.** D7 made the mode choosable, and the user reported it still not
working. Injection testing showed why, and it was not the application:

```
1007 before  tmux=True -> received=0    pane sample: PROBE-READY
1007 after   tmux=True -> received=0    pane sample: PROBE-READY
1007 noalt   tmux=True -> received=0    pane sample: PROBE-READY
```

A minimal program independent of ohgo received **nothing** inside tmux when it
enabled alternate scroll — not mouse bytes, not arrows. tmux 3.7c does not
forward wheel events to such a pane. This held with `?1007h` emitted before or
after alt-screen (ruling out the ordering bug I first suspected), in normal
screen as well as alternate, and with `set -g mouse off`. The only path tmux
forwards is a pane that requests mouse reporting.

So the previous default was wrong precisely where the user was. Baking a single
default in would leave the wheel dead in every multiplexer.

**Why `$TMUX` specifically.** tmux exports it to every pane as its socket path.
That is a signal, not a heuristic — the alternative (detecting any multiplexer)
would mean guessing from `TERM`, which is far less reliable.

**Cost, stated plainly.** Inside tmux the wheel works via mouse reporting, so
drag-select needs Shift-drag (Option on macOS). The user gets the wheel by
default in their environment and keeps unmodified drag-copy everywhere else.
`--mouse select` restores full selection inside tmux at the cost of the wheel,
which is the honest trade rather than a hidden one.

**Tests** `TestMouseDefaultFollowsTheEnvironment`,
`TestExplicitMouseModeBeatsTheEnvironment`.

---

## OPEN — Residual: backward navigation still reaches "Yes" (BUG-004)

As above. Options:

1. **Accept.** Backward navigation is a deliberate press onto the affirmative
   option, and the dialog names what Enter will do (`enter confirm`).
2. **Explicit `y` to approve.** Enter confirms only a non-affirmative selection;
   approving needs `y`. Strongest guarantee; changes the dialog contract.
3. **Three-state.** Insert a neutral "Cancel" between Yes and No so the default
   has an affirmative neighbour that is not the approve action. Costs a row.

Recommendation: **(2)**, if the team wants the guarantee. Not taken unilaterally
because it changes how the dialog is completed.

---

## OPEN — Lifecycle events must not be droppable (BUG-008)

The channel drops on full by design, which is the right liveness trade-off for
*previews*. Applying it to `AssistantDone` and `ToolFinished` is not: losing those
leaves the UI permanently asserting "working" or "running".

Candidate designs: reserve capacity for lifecycle events; a separate
never-dropped path; or a resync on the next turn boundary. All three change the
event-flow contract, so this needs a decision and its own slice.

---

## OPEN — `context 0%` before first use (BUG-007)

Needs a product answer: omit the field entirely until a measurement exists, or
render it as an explicit `context —`. The screenshot in `img-bugs/4.png` shows
`0%` after eight messages, which would be a different and worse bug; that case is
**not reproduced** on this branch and must be re-checked live before either is
fixed.

---

## OPEN — Fence delimiters (BUG-010)

Today's ` ```go ` / ` ``` ` chrome is deliberate and arguably readable. The spec
calls it a leak. Either accept it as a documented deviation (§11.4 is then
amended) or consume the delimiters and render a subtle language label. A product
call, not a bug fix.

---

## OPEN — Transparent background (`img-bugs/4.png`)

No background fill is emitted, so whatever the terminal paints shows through.
Whether that is a defect depends on terminal transparency support. Low priority;
likely a deliberate "use the user's theme" choice.

---

## Unresolved, recorded as gaps

- **Tool output truncation** has no supported way to inspect the remainder. The
  marker is honest, but the diagnostic value the spec asks for is missing.
- **Events carry no run/session identity**, so a stale event cannot be detected.
  Not currently reachable, but it bounds what the UI can ever claim.