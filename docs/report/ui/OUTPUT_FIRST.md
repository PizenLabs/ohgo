# Output-first interaction prototype

Status: **prototype succeeded, with a verified boundary**. The interaction model
holts up under a real terminal, a real multiplexer, a real streaming turn and a
real approval. One claim in the brief could not be verified and is called out
below rather than assumed.

The prototype lives behind an opt-in flag:

```sh
ohgo --live
```

Nothing about the default path changed. `Program.Run` and `Render(State)` are
untouched; `tui/live.go` and `tui.live_test.go` / `live_pty_test.go` /
`live_tmux_test.go` are additive and delete cleanly.

## What was proven

The transcript is written to the terminal **once**. From then on the terminal
owns it: tmux's copy-mode scrolls back to the first line of a streamed answer,
and nothing in the byte stream asks for a mouse mode or the alternate screen.

Mechanically: Bubble Tea's standard renderer, in the **normal screen buffer**,
owns one small frame — the in-flight answer, the status line and the composer.
Transcript blocks are handed to that same renderer through `tea.Println`, which
it writes *above* its own frame, so the terminal scrolls them into its
scrollback. Nothing above the frame's top row is ever rewritten.

The live region is bounded to `screen height - 2` rows, so the renderer's own
"CUA up `linesRendered-1`, erase below, repaint" can always find it again. A
long answer commits a prefix to the scrollback, at a blank line wherever one
exists, so the same reading order is preserved and nothing is painted twice.

## Why not `tea.WithoutRenderer`

It was the first attempt and it is wrong. `initTerminal` in bubbletea v1.3.10
returns early for a nil renderer:

```go
if _, ok := p.renderer.(*nilRenderer); ok {
    // No need to initialize the terminal if we're not rendering
    return nil
}
```

No `initInput`, therefore **no raw mode, no `ttyInput`, no restore**. The app
would have to own the terminal lifecycle itself, which the previous lifecycle
work established Bubble Tea owns. Measured: the program painted, but the pty was
still in canonical mode, every keystroke was echoed by the tty, the composer
received nothing until a newline, and `ctrl+d` ended the read with EOF instead
of reaching `handleKey`.

## Ownership

| Concern | Before (`Run`) | After (`RunLive`) |
|---|---|---|
| Terminal lifecycle (raw mode, cursor, bracketed paste, restore) | Bubble Tea | Bubble Tea, unchanged |
| Alternate screen | ohgo, via Bubble Tea | nobody — the normal buffer |
| Scrollback | ohgo (`State.Entries` + `State.Scroll`) | the terminal |
| Wheel, drag selection, copy | contested: alternate scroll (1007) or mouse reporting (1002/1006) | the terminal, uncontested |
| Transcript content | ohgo, re-rendered per frame | ohgo, written once |
| Composer, status, overlay chrome | ohgo, pinned to a virtual viewport | ohgo, last thing on screen |
| `State.Apply` as the transition owner | unchanged | unchanged |
| Provider, streaming, approval | unchanged | unchanged |

`State.Scroll` and `State.Follow` become inert in live mode: the painter never
reads them and a `ScrollMoved` event cannot change the frame (test:
`TestLiveRegionIgnoresTheViewportScrollOffset`). They stay for the alt-screen
path.

## Interaction matrix

| Environment | Wheel | Select | Copy | Streaming | Composer |
|---|---|---|---|---|---|
| Ghostty | requires the human step below | " | " | verified under a PTY with the real binary | verified |
| Ghostty + tmux 3.7c | tmux copy-mode, verified | tmux | verified by capture-pane | verified | verified |

Evidence:

- **PTY, real binary.** `ohgo --live --model anthropic/claude-sonnet-4-5`
  driven through `/usr/bin/script` on a real 90x30 pty, real keystrokes. Emitted
  modes: `\x1b[?2004h`, `\x1b[?25l`. Nothing else. The composer took typed
  text, `[<64;67;30M` survived verbatim, `ctrl+d` quit cleanly.
- **PTY, scripted session.** A whole turn driven through bubbletea with a
  scripted `ai.StreamFn`: a markdown answer with a fenced Go block, a table and
  emphasis; an approval-gated tool call answered from the keyboard; a slash
  command; `ctrl+d`. See `TestLiveSessionUnderARrealPTY`.
- **tmux, real client.** A real tmux server 3.7c with a client attached on a
  real pty. The streamed answer landed in the pane's own history, and
  `copy-mode` + `top-of-history` scrolled back to it. The tool approval and
  `/status` worked. See `TestLiveSessionInsideTmux`.

The wheel and drag-select rows for Ghostty are the one cell that needs a person.
The reasoning is protocol-level: an emulator only sends mouse reports after the
application asks for them, and the byte stream above proves this application
never asks. Ghostty could not be driven from this environment — `ghostty
+new-window` reports "not supported on this platform" and `open -na
Ghostty.app --args -e ...` did not execute the command reliably.

Manual procedure that would close the cell:

```sh
cd <repo> && go build -o ohgo ./cmd/ohgo
./ohgo --live --model anthropic/claude-sonnet-4-5
# ask a question that returns a long answer with a code block
# wheel up / drag-select / cmd+C  — the terminal must do all three
# keep typing while scrolled up, then scroll back down
```

Inside tmux the same steps go through `prefix+[` (or `set -g mouse on`), which
is tmux owning the interaction — the desired outcome, not a workaround.

## Root causes found while prototyping

1. **`tea.WithoutRenderer` disables bubbletea's terminal init.** Not a workaround
   target; a reason to stay on the standard renderer. See above.
2. **Tool output never appeared in either UI.** `ToolProgress.Stage()` returns
   `ProgressUpdate` for the pre-Execute announcement — there is no path that
   returns `ProgressStarted` — so `runTurn`'s hook never published `ToolStarted`,
   and `ToolFinished` then landed with no entry to update. Found only because
   the PTY run drove a real tool call. Fixed in the consumer
   (`tui/app.go`) by recognising the announcement as a non-final report with no
   outcome; the alt-screen path is fixed by the same change.
3. **The composer hint row was not width-bounded.** A 47-cell hint on a
   40-column terminal soft-wraps, and a live row that silently becomes two
   breaks the erase-and-rewrite. Fixed by choosing the hint by width.

## What this does not do

- It does not migrate the runtime, the provider layer, the markdown renderer or
  the state machine. `RenderMarkdownWrapped`, `sanitizeBlock`, `State.Apply`
  and `Program.handleKey` are all reused as-is.
- It does not keep the fixed-bottom geometry. When the terminal is scrolled
  back, the composer is not visible, and that is the point: it is in the
  scrollback the terminal is showing.
- It does not solve a long answer's mid-paragraph seam. A single paragraph
  longer than the live budget has no blank line to commit at, so the block
  seals whole and the next block starts mid-thought. Code blocks and normal
  prose always commit at a paragraph boundary.
- It does not re-flow history on resize. Wrapping narrower re-renders only the
  live block; the scrollback keeps its original wrap, because the terminal owns
  those rows now.
- Shrinking the terminal below `liveSpareRows` from the composer's height
  drops the live answer (append-only mode): the answer is sealed whole when its
  turn ends, and the stream shows nothing until then.
- History is not searchable from the app, and no scroll offset is tracked. If a
  future slice wants "return to the live edge" as a key rather than a scroll,
  that is where a minimal terminal query would have to be justified.

## Next smallest slice

Make the *live region* height-aware everywhere and drop the alt-screen path's
mouse policy:

1. `tui/mode.go`, `MouseDefaultFor`, `WithMouseReporting`, `--mouse` and the
   `mouse` setting become dead once `--live` is the default. Deleting them is a
   small, reversible change and removes the last place the app can ask the
   terminal for the wheel.
2. `tui/view.go`'s viewport slicing (`State.Scroll`, `State.Follow`,
   `Layout.ConversationHeight`, `growScroll`, the `↑ N lines above` marker) can
   then go, which removes the whole emulation-the-terminal surface.
3. `--live` becomes the default behind a `--fullscreen` fallback for one
   release, then the fallback goes.

Risks in that order: (1) is behaviour-visible for `--mouse report` users and
should keep a deprecation path; (2) touches `Render`, which many rendering
tests assert on directly; (3) is where any regression becomes irreversible.
