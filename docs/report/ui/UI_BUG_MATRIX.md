# UI Bug Matrix

Each row is a defect traced to its earliest incorrect owner. **Confidence**
is evidence, not opinion:

- **verified** — reproduced by running the code (a test, a probe, or the real CLI)
- **hypothesis** — traced in source, not executed

Status: `FIXED` = landed with a regression test. `OPEN` = not yet changed.

---

## BUG-001 — Fenced code with tabs overflows the terminal width

| | |
| --- | --- |
| **Class** | Terminal layout / cell-width |
| **Owner** | `tui/markdown.go` `renderCodeLine` (measurement), `tui/width.go` |
| **Confidence** | verified |
| **Status** | **FIXED** |

**Reproduce** — render any tab-indented fence:
`RenderMarkdownWrapped("```go\nfunc main() {\n\tfmt.Println(1)\n}\n```", pal, 80)`
returns rows of 84 cells.

**Expected** every row ≤ the width it was measured for.
**Actual** `CellWidth("\tfmt…")` counts the tab as 0 cells; lipgloss expands it to
4; `padCode` pads to the stale width → row is 4 cells per tab too wide.

**User impact** a row wider than the terminal is soft-wrapped into two physical
rows. The frame grows past the viewport, the terminal scrolls, and the status
line and composer drift out of position while stale rows remain. Matches
`img-bugs/3.png`, `img-bugs/Screenshot 2026-10-10 at 3.48.04 PM.png`.

**Regression test** `TestCodeFenceTabsNeverWidenTheFrame`,
`TestRenderedFrameFitsTerminalExactly`.

---

## BUG-002 — Enter on an empty composer starts a second concurrent turn

| | |
| --- | --- |
| **Class** | Event flow / ordering / lifecycle |
| **Owner** | `tui/app.go:466-472` (`enter`), `app.go:1334-1341` (`drainQueue`) |
| **Confidence** | verified (`-race` DATA RACE on `agent.(*Loop).SetTextDeltaHook`) |
| **Status** | OPEN |

**Reproduce** — during a live turn, type a prompt (it queues), then press Enter on
the now-empty composer.

**Expected** Enter with empty input does nothing while busy.
**Actual** `drainQueue` has no `Status.Busy` guard, so it starts a second
`Loop.Run` while the first is in flight. Histories interleave and clobber each
other's rebase. The comment at `app.go:1321-1323` states the invariant this
breaks.

**User impact** corrupted transcript, interleaved history, a data race.

---

## BUG-003 — Double-Enter submits the same prompt twice

| | |
| --- | --- |
| **Class** | Event flow / ordering |
| **Owner** | `tui/app.go:480` (`submit`), `model.go:233-237` |
| **Confidence** | verified |
| **Status** | OPEN |

**Reproduce** — press Enter twice quickly on a prompt.

**Expected** the second Enter is queued or ignored.
**Actual** the non-busy path returns `p.submit(text)` without clearing `Input` or
setting `Busy`; both happen only when `UserSubmitted` round-trips back through the
channel. Inside that window a second Enter submits the same text again.
Same `-race` report as BUG-002.

---

## BUG-004 — Approval navigation always lands on "Yes"

| | |
| --- | --- |
| **Class** | Keyboard / focus routing |
| **Owner** | `tui/app.go:378-385`, `model.go:123-132` (`Move`), `model.go:599-604` |
| **Confidence** | **verified by execution** |
| **Status** | **FIXED — with a documented residual** |

**Reproduce** — trigger any approval, then press a navigation key.

**Expected** navigation moves between choices and can return to the safe default.
**Actual** `approvalChoices = {"Yes","No"}` and the cursor starts on "No"
(`defaultApprovalSelection` → `len-1`). `Move` **wrapped**: `(1±1) % 2 == 0` → "Yes".

Executed result *before* the fix — every navigation key moved onto "Yes":

```
default="No"  key down      -> "Yes"   <<< LANDS ON AFFIRMATIVE
default="No"  key up        -> "Yes"   <<< LANDS ON AFFIRMATIVE
default="No"  key tab       -> "Yes"   <<< LANDS ON AFFIRMATIVE
default="No"  key shift+tab -> "Yes"   <<< LANDS ON AFFIRMATIVE
default="No"  key right     -> "Yes"   <<< LANDS ON AFFIRMATIVE
default="No"  key left      -> "Yes"   <<< LANDS ON AFFIRMATIVE
default="No"  key j / k / h / l -> "Yes"   <<< LANDS ON AFFIRMATIVE
```

**Fix.** `PendingApproval.Move` clamps at both ends instead of wrapping. The
conservative default is the *last* choice, so it is also an end: with clamping,
**forward navigation (`down`, `right`, `tab`, `j`, `l`) can no longer leave it at
all**. Tab — the conventional "move focus away" key that previously approved
outright — now does nothing.

**Residual, still open.** In a two-option dialog the affirmative is *adjacent* to
the default, so a **backward** move (`up`, `left`, `k`, `h`, `shift+tab`) still
reaches "Yes", and Enter then confirms it. No movement policy can prevent this in
a two-item list — it requires approving only on an explicit `y`. That is a
product decision; see `UI_DECISIONS.md` § OPEN.

**Regression tests** `TestApprovalNavigationStopsAtTheEndInsteadOfWrapping`,
`TestApprovalNavigationStaysWithinTheChoiceList`,
`TestApprovalCursorStopsAtTheEnds` (replaces `TestApprovalCursorWraps`, which
asserted the old wrap behaviour).

---

## BUG-005 — Ctrl+C is swallowed while an approval is pending

| | |
| --- | --- |
| **Class** | Keyboard / focus routing |
| **Owner** | `tui/app.go:376-392` |
| **Confidence** | **verified by execution** |
| **Status** | **FIXED** |

**Reproduce** — open an approval, press Ctrl+C.

```
ctrl+c during approval: pending before=true after=true  <<< SWALLOWED
```

**Fix.** The approval switch now routes `ctrl+c` into `cancelTurnNow`, which was
already the correct path — it denies the blocked tool *before* cancelling, so
aborting can never approve the work it abandons. The denial logic was written and
simply unreachable; nothing new was invented.

**Regression test** `TestCtrlCDuringApprovalDeniesAndCancels`.

---

## BUG-006 — Composer hint row contradicts the open approval dialog

| | |
| --- | --- |
| **Class** | Styling / information hierarchy + keyboard routing |
| **Owner** | `tui/view.go:166-171`, `tui/layout.go:116` |
| **Confidence** | verified |
| **Status** | **FIXED** |

**Reproduce** — open an approval during a turn.

**Actual** the composer hint row still rendered under the dialog and advertised
`enter queue · esc cancel · alt+enter break · pgup/pgdn scroll`, while the dialog
had captured `enter` (confirm the highlighted choice), `esc` (deny the tool), and
swallowed `alt+enter`/`pgup`/`pgdn`.

**Fix.** The row is dropped in `ComputeLayout` (not at render time) so reserved and
rendered rows still agree, and `composerHintsFor` returns empty for the same
state. The dialog prints its own key line, so nothing is left untaught.

**Regression test** `TestComposerHintsNeverAdvertiseKeysTheApprovalHasCaptured`.

---

## BUG-007 — `context 0%` shown before any usage is measured

| | |
| --- | --- |
| **Class** | Runtime contract / missing authoritative data |
| **Owner** | `tui/model.go:155-164` (`Percent`), `tokens.Percent` |
| **Confidence** | verified for the start-of-session case |
| **Status** | OPEN — scope needs confirming |

**Reproduce** — start a session against a model with a known window; the status
line reads `context 0%`.

**Expected** unknown/unavailable, or omitted. Spec §2.2 forbids inventing
percentages; §8.3 prefers explicit unknown.
**Actual** the window is known so `Percent()` returns a real `0`, but the usage
ledger holds no measurement yet — the number reads as authoritative when it is
merely unmeasured.

**Caveat** `img-bugs/4.png` shows `0%` after eight messages, which would be a
worse lie. **That case is not reproduced on this branch** and must be re-checked
against a live multi-turn session before being treated as the same defect.

---

## BUG-008 — Dropped lifecycle events leave the UI permanently busy

| | |
| --- | --- |
| **Class** | Event flow / lifecycle |
| **Owner** | `tui/app.go:1587-1596` (`publishOK`), `app.go:1480`, `app.go:1395-1404` |
| **Confidence** | verified |
| **Status** | OPEN |

**Reproduce** — saturate the event buffer and drop `AssistantDone` or
`ToolFinished`.

**Expected** the turn reaches a terminal state.
**Actual** both go through the same drop-on-full channel. `AssistantDone` is the
*only* writer of `Busy = false`, so a dropped one leaves the header reading
`● working` and the spinner running forever. A dropped `ToolFinished` leaves the
card spinning `running` with a duration that climbs indefinitely, and
`DeriveRuntime` keeps the header on `running tool` even after Busy clears.

**User impact** the UI asserts a state the runtime has left. Directly contradicts
spec §2.2 and §6.4.

**Adjacent risk** when `AssistantDelta` is dropped mid-stream, the reconciliation
at `app.go:1449-1463` walks the full turn text against a `streamed` prefix that
has a hole in it, and `commonPrefixLen` stops at the hole — republishing text
that already rendered, and welding it onto the *next* assistant bubble.

---

## BUG-009 — Cancellation is claimed before the runtime confirms it

| | |
| --- | --- |
| **Class** | Runtime contract |
| **Owner** | `tui/app.go:909-925` |
| **Confidence** | verified |
| **Status** | OPEN |

`cancelTurnNow` publishes a `"cancelled"` notice whether or not a turn was live;
a live turn then publishes a second `"turn cancelled"` from `app.go:1421`. Spec
§9: *"Do not claim an operation is cancelled until the runtime confirms the
cancellation outcome."*

---

## BUG-010 — Fence delimiters leak as literal Markdown

| | |
| --- | --- |
| **Class** | Markdown rendering |
| **Owner** | `tui/markdown.go:193-215` (`renderFence`) |
| **Confidence** | verified |
| **Status** | OPEN |

**Reproduce** — `RenderMarkdownWrapped("```go\nx := 1\n```", pal, 80)`.
**Actual** the rendered rows are ```` ```go ````, `x := 1`, ```` ``` ````.
**Expected** the delimiters are consumed; spec §11.4 requires that "code fences
… do not routinely leak raw Markdown markers". Visible in
`img-bugs/5.png`.

Deliberate today (the block reads as a labelled surface), but it is a fidelity
gap against the spec and needs a decision, not a shrug.

---

## BUG-011 — Approval dialog overflows the viewport below ~11 rows

| | |
| --- | --- |
| **Class** | Terminal layout |
| **Owner** | `tui/view.go:292-299`, `view.go:1069-1094` |
| **Confidence** | verified |
| **Status** | OPEN |

When `approvalLines(...) > ConversationHeight`, the transcript region clamps to
zero rows but the dialog still emits its full claimed count, so the frame exceeds
the terminal. Measured: `80×10 → 11 rows`, `50×9 → 11 rows`.

Choices still render (they follow the preview), so this is not a wrong-answer
hazard — the header and scroll marker are pushed off and the frame no longer
fits. Existing test `TestTinyViewportStaysBounded` uses a 2-row tolerance
(`got > h+2`) and therefore passes this.

---

## BUG-012 — `--no-tools` always fails

| | |
| --- | --- |
| **Class** | Runtime contract / CLI |
| **Owner** | `cmd/ohgo` (outside the sanctioned `tui` scope) |
| **Confidence** | verified — real CLI run |
| **Status** | OPEN — reported, not fixed (out of scope) |

```
$ ohgo run --model ollama/qwen2.5:1.5b --no-tools "hi"
ohgo: agent: no tools registered
```

`--no-tools` is a documented flag that cannot succeed. Reported per spec §14
("stop and report the contract gap … before expanding scope").

---

## BUG-013 — No wheel scrolling, reported from the real terminal

| | |
| --- | --- |
| **Class** | Keyboard / focus routing + terminal lifecycle |
| **Owner** | `tui/app.go` (`Run`, arrow routing), `tui/mode.go` |
| **Confidence** | verified in a real pty |
| **Status** | **FIXED** |

**Reproduce** — run the full-screen UI, load a long transcript, scroll the wheel.
Nothing happens; the transcript only moves on pgup/pgdn.

**Cause.** Not a bug in the scroll path — `handleMouse` handled wheel ticks
correctly all along. The program simply never asked for the mouse, because
requesting it trades away drag-select (the terminal stops doing its own
selection) *and* forces parsing of a report stream bubbletea v1.3.10 can split
across reads. The code documented the wheel as unobtainable.

**Fix.** Alternate scroll mode (DEC 1007), bracketed by `Run` and always turned
back off. Selection and copy are untouched — no mouse reporting is requested at
all — while the terminal forwards wheel ticks as plain arrow keys.
`wheelScrolls` decides what an arrow means: it scrolls the transcript only when
the composer is empty and no overlay owns the keyboard, so typing and overlays
keep their arrow behaviour.

**Verified in a real pty (100×30):**

```
alternate scroll ON  (CSI ?1007h): 1
alternate scroll OFF (CSI ?1007l): 1
alt-screen enter/exit:            1 / 1     exit=0
mouse reporting requested (?1000h/?1002h/?1003h): 0  <- selection not hijacked
```

**Tests** `TestRunBracketsAlternateScrollMode`,
`TestWheelArrowsScrollTheTranscriptWhenTheComposerIsIdle`,
`TestArrowsKeepEditingAndHistoryWhileTyping`,
`TestWheelArrowsDoNotStealKeysFromAnOpenOverlay`,
`TestHistoryRecallMovesToTheCtrlBindingsOnAnIdleComposer`.

**Honest limit.** Keystroke injection into the pty does not reach Bubble Tea in
this harness, so the end-to-end "wheel scrolls a long transcript" was proved by
running the real `handleKey` path rather than by screen scraping. The terminal
mode sequences themselves *were* observed on the wire.

**Follow-up, BUG-014.** Mode 1007 alone did not fix this for every user — see
BUG-014, which is why the mouse mode is now a setting.

---

## BUG-014 — Mode 1007 cannot reach every terminal, so the wheel stayed dead

| | |
| --- | --- |
| **Class** | Terminal lifecycle |
| **Owner** | `tui/app.go` (`Run`), `tui/mode.go`, `config.Settings.Mouse` |
| **Confidence** | verified on the wire |
| **Status** | **FIXED** (selectable mode) |

**Reproduce** — run inside **tmux** (or any terminal without DEC 1007) and scroll.

**Cause.** Not a defect in BUG-013's fix. Mode 1007 is honoured by iTerm2,
Ghostty, kitty and WezTerm directly, but:

- Terminals that do not implement 1007 (macOS Terminal.app, the VS Code
  integrated terminal) never forward the wheel.
- A **multiplexer** consumes wheel events for its own copy-mode before the
  pane's terminal emulation sees them, so the application never learns the wheel
  moved — no terminal mode can fix this from inside the pane.

**Fix.** The mouse mode became a setting instead of a hardcoded choice:

| Mode | Wheel path | Drag-select |
| --- | --- | --- |
| `select` (default) | mode 1007 → arrow keys | works |
| `report` | real mouse events (`?1002`) | needs Shift/Option |

Exposed as `--mouse` and `mouse` in `~/.ohgo/settings.json`. `report` is the
path tmux leaves open, since tmux forwards mouse events to a pane that requests
them. Unrecognised values degrade to `select`.

**Verified on the wire, graceful exit (exit 0):**

```
default:          1007 on/off 1/1   1002 on/off 0/0   <- selection preserved
--mouse report:   1007 on/off 1/1   1002 on/off 1/1   <- real wheel events
--mouse repot:    1007 on/off 1/1   1002 on/off 0/0   <- typo keeps copy
```

**Tests** `TestMouseReportingIsOffByDefaultAndOptIn`,
`TestAlternateScrollIsEmittedInBothModes`,
`TestNormalizeMouseKeepsSelectionOnAnythingUnrecognised`.

**Follow-up, BUG-015.** Mode 1007 alone did not fix this for every user — see
BUG-015, which is why the default now follows the environment.

---

## BUG-015 — Inside tmux the wheel never reached the app, and no ohgo setting could fix it

| | |
| --- | --- |
| **Class** | Terminal lifecycle (multiplexer boundary) |
| **Owner** | `tui/mode.go` (`MouseDefaultFor`), `cmd/ohgo/run.go` |
| **Confidence** | verified end-to-end against tmux 3.7c |
| **Status** | **FIXED** (environment-aware default) |

**Reproduce** — run ohgo inside tmux with default settings and scroll.

**First incorrect boundary: tmux, not the application.** Established by
injection, not inference — a real SGR wheel event (`ESC [ < 64 ; x ; y M`) was
injected into a real terminal context and the pane's actual content read back.

| Step | Boundary | Result |
| --- | --- | --- |
| SGR wheel → `handleMouse` → `ScrollMoved` → viewport | app | **works** |
| pane requests mouse reporting (`?1002`) inside tmux | tmux forwards | **works** |
| pane requests alternate scroll only (`?1007`) inside tmux | tmux drops | **broken** |

Isolated with a minimal program independent of ohgo, which printed every byte it
received:

```
1007 before  tmux=True -> received=0    pane sample: PROBE-READY
1007 after   tmux=True -> received=0    pane sample: PROBE-READY
1007 noalt   tmux=True -> received=0    pane sample: PROBE-READY
```

The pane received **nothing at all** — not mouse bytes, not arrows — regardless
of:

- whether `?1007h` was emitted before or after alt-screen (this rules out an
  ordering bug in the original implementation),
- whether the pane was in the alternate screen or the normal screen,
- `set -g mouse off` versus the default,
- tmux's own copy-mode (never entered; `pane_in_mode` stayed `0`).

tmux 3.7c simply does not forward wheel events to a pane that has enabled
alternate scroll alone.

**Two corrections to my own earlier work.** An earlier harness reported this as
an application failure by parsing the *outer* terminal's rendering — a false
negative; `tmux capture-pane` is authoritative and the outer stream is not. And
a bare pty is **not** a valid environment for `select` mode: it forwards bytes
verbatim and honours no terminal modes, so bubbletea parses injected mouse bytes
that no real terminal would send there. Every result below comes from the app's
own scroll marker read from the pane.

**Fix.** The default now follows the environment (`MouseDefaultFor`). With a
multiplexer in the path, reporting is the only mode that delivers the wheel, so
defaulting to `select` there would leave it dead exactly where the user has it.
tmux exports `TMUX` to every pane, making this a signal rather than a guess. An
explicit `--mouse` or settings value still wins, so nothing is silently
re-enabled behind the user's back.

**Verified — tmux, no flag, the user's real invocation:**

```
app running            : True
viewport changed       : True
app reports scroll pos : True    <- authoritative
returns to live edge   : True
VERDICT                : PASS
```

**Verified — outside tmux, no flag, drag-copy intact:**

```
alternate scroll (1007) requested : 1
mouse reporting  (1002) requested : 0   <- selection intact
```

**Full matrix (tmux 3.7c, injected SGR, app's own scroll marker):**

| Environment | Mode | Result |
| --- | --- | --- |
| bare pty (not an emulator) | select | invalid — artifact, see above |
| bare pty (not an emulator) | report | **PASS** |
| inside tmux | select | FAIL — tmux limitation; not the default there |
| inside tmux | report | **PASS** (and now the default) |
| inside tmux, no flag | default | **PASS** |
| Ghostty / kitty / WezTerm / iTerm2, direct | select | **unverified** — no headless emulator available |

**Residual requirement, documented not hidden:** inside tmux the wheel works via
mouse reporting, so drag-select needs a modifier (Shift-drag, or Option on
macOS). That is inherent — a terminal hands every mouse event to the
application or performs its own selection, never both.

**Tests** `TestMouseDefaultFollowsTheEnvironment`,
`TestExplicitMouseModeBeatsTheEnvironment`.

---

## Not reproduced

Recorded so they are not "fixed" on the strength of a screenshot alone.

- **Cascading list indentation / raw `**` leakage** (`img-bugs/3.png`).
  Rendered identically clean at `HEAD` and on this branch at widths 180/120/80.
  The staircase is *explained* by BUG-001: a too-wide row soft-wraps and the
  terminal scrolls, which visually shifts each row. The `**` leakage likewise
  appears only on rows that were cut by the viewport, not in the rendered
  string. Treated as one defect, fixed.
- **Duplicated `Run this tool? ls` lines and mid-transcript status line**
  (`img-bugs/Screenshot 2026-10-10 at 3.47.39 PM.png`). Consistent with the same
  scroll/overflow mechanism; not separately reproduced, and the capture predates
  the current branch. Needs a live re-check.
- **Transparent/ghost background** (`img-bugs/4.png`). No background fill is
  emitted by design; whether that is a defect depends on terminal transparency
  support and is not yet decided.