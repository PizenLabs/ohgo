# Phase UI — Owned Document, Virtual Viewport, Incremental Layout, Logical Selection, OSC 52

## 1. Verdict

**PASS**

The alternate-screen UI now owns a canonical document, a deterministic layout
index, an explicit viewport, application-owned hit testing and selection, and an
OSC 52 clipboard path. The implementation was verified with unit, invariant,
integration, race, benchmark, and protocol tests. Known limitations are listed
in §12; none is hidden behind the verdict.

Verification tree: commit `1f6d71a7a952cc479270816efb80c558f2372764` plus the
working-tree changes described here. Go 1.27.2, macOS arm64 (Apple M1 Pro).
`gofmt -l .` is empty; `go build ./...`, `go vet ./...`, `golangci-lint run
./...`, `go test ./...`, and `go test -race ./...` all pass.

---

## 2. Architecture

```text
Agent / session events
        │  State.Apply (UI goroutine)
        ▼
Canonical document  (State.Entries, stable BlockIDs, logical content)
        │
        ├──────────────────────────────┐
        ▼                              ▼
TranscriptLayout                 ViewportTracker
(cached visual rows,             (FOLLOWING / DETACHED,
 Fenwick row index)               block/row anchor)
        │                              │
        └──────────────┬───────────────┘
                       ▼
              Visible frame rows
             (TranscriptLayout.Rows)
                       │
                       ▼
             ANSI projection (view)
                       │
             ┌─────────┴─────────┐
             ▼                   ▼
        Terminal frame      OSC 52 payload
                                │
                                ▼
                      Terminal clipboard
```

The alternate-screen path (`Program.View`) uses one retained `TranscriptLayout`
and one `ViewportTracker`. `Render(state)` remains a one-shot pure projection for
tests and embedded callers; it builds a fresh layout and resolves the viewport
from the state's scroll fields. Both paths use the same layout and row code, so
tests cannot pass against a different geometry than production.

`--live` remains the separate output-first path: it prints sealed blocks to the
normal terminal buffer and deliberately leaves scrollback, drag selection, and
copy to the terminal.

---

## 3. Ownership

| Concern | Owner |
|---|---|
| Document / block content | `State.Entries` (`tui/model.go`), with IDs allocated by `State.apply` |
| Block identity | `BlockID` on `Entry`; assigned on entry, never derived from a row |
| Layout / geometry | `TranscriptLayout` (`tui/transcript_layout.go`) |
| Scroll position | `ViewportTracker` + explicit `scrollIntent` / `ScrollSeq` |
| Selection | `State.Selection` (`tui/selection.go`), block/row/column |
| Clipboard payload | `tui/clipboard.go` (OSC 52, Base64, no subprocess) |
| Terminal surface | Bubble Tea renderer and the terminal; the application never mutates historical scrollback |
| Execution / authority / task | unchanged runtime owners; UI only requests explicit commands |

The application never persists a terminal row, cursor position, or scrollback
line number. Geometry is recomputed from the document.

---

## 4. Scrolling

Two states are explicit:

- **FOLLOWING** — `offset = TotalRows - Height`; new output keeps the live edge
  visible.
- **DETACHED** — the viewport records an `Anchor{BlockID, Row}`. Content growth,
  expansion above the window, and width reflow resolve the anchor back to a
  logical row, so the reader's content does not move.

Detaching happens on an explicit `ScrollMoved`/wheel/PageUp transition.
Attaching happens on `end`, scrolling back to the bottom, `ScrollToBottom`, or
`/clear` / resume. `Scroll` remains an event-level compatibility mirror;
`scrollIntent` records only user scrolling, and `ScrollSeq` lets the tracker
distinguish a user scroll from reflow.

The marker consumes one row when detached. Offsets are clamped to
`[0, MaxOffset]`; the top edge shows row 0 rather than a phantom row. The
indicator reports the hidden distance, not the raw intent, and `end` reattaches.

`Home` scrolls to the transcript top when the composer is idle; with text in
the composer it remains the line-start chord. `PgUp`/`PgDn` and the wheel keep
their existing bindings.

Append while detached is tested at the block and raw layout level: the top
visible `(BlockID, Row)` and the numeric offset must not move. Streaming ten
chunks while detached is tested for the same invariant.

---

## 5. Expansion

Every `Entry` receives a stable `BlockID` when it enters the transcript,
including entries replayed by resume. Expansion is still the existing
presentation-only `ToolToggled` transition:

```text
hit-test (or Ctrl+O)
    → BlockID → tool call ID
    → State.Apply(ToolToggled)
    → bump the one block revision
    → TranscriptLayout re-renders that block only
    → ViewportTracker re-resolves the anchor
```

It never reruns a tool, reads a repository, starts provider work, mutates task
state, approves a mutation, or changes authority. `State.ToggleBlock` rejects
non-expandable and missing blocks rather than inventing state.

Historical expansion is tested with a tool inserted near the top of an
800-block document: scroll to the top, click the block, scroll back to the
bottom, and confirm the block is still expanded. There is no scrollback
mutation anywhere in the path.

---

## 6. Selection / Copy

Selection is logical:

```go
type Position struct { BlockID BlockID; Row, Column int }
type Selection struct { Start, End Position }
```

A drag maps a screen cell to the current visible frame row, then to
`BlockID + local row + column`. Normalization orders endpoints by block index
and local row, so reversed drags copy the same range. Copy projects each
selected row through `ansi.Strip`, trims right padding, and cuts by display
cells (`CellSlice`), so CJK/emoji boundaries are not split and renderer padding
never enters the payload. Cross-block, wrapped, boundary, Unicode, and
ANSI-styled selections are tested. An alt-click copies one block's canonical
logical content through `BlockCopyText`, without constructing a range.

OSC 52 is the only transport:

```text
ESC ] 52 ; c ; <base64(logical text)> BEL
```

tmux DCS passthrough and screen chunking are protocol wrappers only. No external
clipboard process is spawned. `OHGO_CLIPBOARD=off` or a `dumb`/unset `TERM`
disables copying; the defined behaviour is a silent no-op, not a false success
and not a platform-specific process fallback. Selection survives scrolling and
resize; it is cleared when the transcript is replaced or `/clear` removes the
blocks it names.

The default `mouse=select` mode leaves drag-copy with the terminal; `mouse=report`
(or the tmux default) enables the application-owned path. `--live` always leaves
selection to the terminal. This split is deliberate and documented in README
and `docs/architecture/ARCHITECTURE.md`.

---

## 7. Wrapping / Regex

Rows come from the same renderer that paints them:

1. `renderEntry` produces the block projection at the layout's content width.
2. `entryLines` removes exactly one terminating newline, preserving interior
   separators.
3. `TranscriptLayout` caches the resulting visual rows and heights.
4. A Fenwick tree maps document rows to blocks.

There is no regex in the geometry path. Regexes in the repository are limited to
diff hunk headers and support highlighting; neither computes height, offset,
hit testing, or selection. `strings.Count(text, "\n")` remains only in
`growScroll` as the compatibility mirror; the rendered viewport uses visual
rows from the layout.

Tested line-boundary cases: LF, trailing LF, CRLF (sanitized), a line exactly
at width, one cell over, empty final lines, blank middle lines, styled content
followed by a newline, CJK/combining marks, tabs expanded at fixed terminal
stops (including inside ANSI spans), long whitespace runs, and two adjacent
blocks. Each row must map to exactly one block and one local row; the second
block's text may never appear on a row owned by the first.

---

## 8. Resize

`WindowSizeMsg` updates the state and invalidates layout. A geometry-key change
(width, tier, theme) re-renders every block because every projection may
change; the viewport tracker then resolves its block/row anchor in the new
layout, clamping only when the anchored row no longer exists. Expanded state,
block IDs, selection endpoints, task state, and session state are untouched.

Tested widths: 120 → 80 → 120 → 40 → 160 → 80 → 120 with long wrapped
content. The anchor block is preserved, IDs are identical, expansion count is
unchanged, and mouse hit testing resolves the current frame after each resize.

Resize cost is global by nature (every width-dependent row changes); see §9 and
§12.

---

## 9. Performance

Command:

```sh
go test ./tui -run '^$' -bench 'BenchmarkViewport|BenchmarkProgramFrame|BenchmarkFullTranscriptRender' -benchtime=500x -count=1
```

Machine: Apple M1 Pro, macOS, Go 1.27.2. Times are per operation; `rendered/op`
is the number of block projections produced by the operation itself.

### Scroll (0 blocks rendered)

| blocks | p50 | p95 | p99 | allocs | bytes |
|---:|---:|---:|---:|---:|---:|
| 100 | 1.75 µs | 2.17 µs | 10.4 µs | 8 | 452 B |
| 1,000 | 0.71 µs | 0.88 µs | 1.29 µs | 8 | 324 B |
| 10,000 | 0.75 µs | 1.00 µs | 5.00 µs | 8 | 324 B |

### Expand middle (1 block rendered)

| blocks | p50 | p95 | p99 | allocs | bytes |
|---:|---:|---:|---:|---:|---:|
| 100 | 15.1 µs | 40.2 µs | 120 µs | 100 | 29.6 KB |
| 1,000 | 12.7 µs | 43.8 µs | 95.9 µs | 100 | 29.4 KB |
| 10,000 | 20.0 µs | 30.3 µs | 60.5 µs | 100 | 29.5 KB |

### Collapse middle (1 block rendered)

| blocks | p50 | p95 | p99 | allocs | bytes |
|---:|---:|---:|---:|---:|---:|
| 100 | 12.8 µs | 31.3 µs | 110 µs | 100 | 29.6 KB |
| 1,000 | 13.2 µs | 40.3 µs | 86.2 µs | 100 | 29.4 KB |
| 10,000 | 22.3 µs | 34.1 µs | 93.9 µs | 100 | 29.5 KB |

The expand and collapse runs differ only in the starting presentation state;
each iteration toggles the same middle block, so the projection cost covers
both directions. Both render exactly one block and allocate the same 100
objects.

### Hit test (screen cell → block/row)

| blocks | p50 | p95 | p99 | allocs | bytes |
|---:|---:|---:|---:|---:|---:|
| 100 | 250 ns | 625 ns | 2.00 µs | 2 | 124 B |
| 1,000 | 125 ns | 208 ns | 417 ns | 2 | 84 B |
| 10,000 | 125 ns | 208 ns | 458 ns | 2 | 84 B |

### Selection copy (3 visible rows)

| blocks | p50 | p95 | p99 | allocs | bytes |
|---:|---:|---:|---:|---:|---:|
| 100 | 6.58 µs | 12.0 µs | 33.5 µs | 36 | 3.8 KB |
| 1,000 | 1.75 µs | 4.75 µs | 6.88 µs | 34 | 1.77 KB |
| 10,000 | 1.83 µs | 4.71 µs | 11.7 µs | 34 | 1.77 KB |

### Append block (1 block rendered)

| blocks | p50 | p95 | p99 | allocs | bytes |
|---:|---:|---:|---:|---:|---:|
| 100 | 6.25 µs | 20.3 µs | 76.7 µs | 69 | 26.8 KB |
| 1,000 | 6.25 µs | 25.4 µs | 98.1 µs | 69 | 27.2 KB |
| 10,000 | 7.29 µs | 16.7 µs | 93.9 µs | 69 | 32.8 KB |

### Streaming delta (1 block re-rendered per chunk)

| blocks | p50 | p95 | p99 | allocs | bytes |
|---:|---:|---:|---:|---:|---:|
| 100 | 101 µs | 322 µs | 386 µs | 1,334 | 294 KB |
| 1,000 | 102 µs | 309 µs | 395 µs | 1,334 | 294 KB |
| 10,000 | 90.9 µs | 256 µs | 333 µs | 1,334 | 294 KB |

The stream figures are dominated by re-projecting the one growing assistant
block (markdown reflows as text arrives), not by document size. This is visible
in the flat result across 100/1,000/10,000 blocks.

### Resize (full reflow; `rendered/op` = block count)

| blocks | p50 | p95 | p99 | allocs | bytes |
|---:|---:|---:|---:|---:|---:|
| 100 | 0.59 ms | 0.96 ms | 1.02 ms | 6,029 | 610 KB |
| 1,000 | 6.23 ms | 6.78 ms | 7.14 ms | 59,063 | 5.72 MB |
| 10,000 | 59.1 ms | 61.3 ms | 68.5 ms | 590,121 | 56.96 MB |

### Whole-frame comparison

| document | old full projection | retained Program frame |
|---:|---:|---:|
| 100 | 0.54 ms | 42.0 µs |
| 1,000 | 5.20 ms | 39.4 µs |
| 10,000 | 47.5 ms | 35.9 µs |

A focused remeasure of the complete frame (wheel tick + layout refresh +
viewport resolution + chrome rendering) reports:

| blocks | p50 | p95 | p99 | allocs | bytes |
|---:|---:|---:|---:|---:|---:|
| 100 | 35.9 µs | 81.1 µs | 135 µs | 196 | 58.7 KB |
| 1,000 | 33.1 µs | 78.9 µs | 150 µs | 195 | 58.3 KB |
| 10,000 | 32.5 µs | 49.8 µs | 129 µs | 195 | 58.3 KB |

The retained frame is flat across two orders of magnitude. Ordinary scrolling
and hit testing render zero blocks. Idle runtime projection also uses
apply-time counters (`runningTools`, `assistantText`), so a frame does not scan
the transcript to decide its header phase. No benchmark asserts a wall-clock
threshold; the numbers are the measured contract.

---

## 10. Tests

### Unit

- `tui/document_test.go` — stable IDs, presentation-only toggles, non-expandable
  rejection, logical block copy, resume identity/selection reset.
- `tui/transcript_layout_test.go` — layout equals the rendered row sequence,
  exact wrap boundaries, CJK/combining marks, trailing/blank lines, ANSI
  zero-width, per-block row ownership, deterministic layout, local expansion,
  Fenwick correctness, incremental render counts.
- `tui/viewport_test.go` — empty/short/exact/one-row-taller documents, detach /
  reattach, follow append, detached append anchor, expansion/collapse above the
  anchor, resize anchor/IDs/expansion, clamped offsets, Home/End, streaming
  while detached.
- `tui/selection_test.go` — single line, padding exclusion, cross-block, reversed
  drag, wrapped multi-line, empty/missing blocks, ANSI stripping, Unicode cells,
  highlight width, scroll survival, resize survival, hit testing after scroll,
  expansion, and resize, drag clamping.
- `tui/clipboard_test.go` — exact OSC 52 framing/Base64, tmux/screen wrappers,
  availability, one-shot frame emission, disabled behaviour, logical payload,
  mouse drag copy, terminal-owned mode ignoring pointer drags, historical click
  expansion, no prompt/authority mutation.
- `tui/transcript_bench_test.go` — 100/1,000/10,000 mixed-document benchmarks.

### Integration

Program-level paths are exercised through `Program.Update`/`View`: mouse
press/motion/release, wheel, Home/End, resize messages, streaming deltas,
resume, and `/clear`. Existing PTY and tmux suites continue to cover the live
path; the alternate-screen frame tests continue to assert exact row counts and
pinned chrome.

### Race

`go test -race ./...` passes for the whole repository, including the TUI.
All retained layout, viewport, selection, drag, and clipboard state is
UI-goroutine owned. Agent/tool events enter through the existing event channel
and are folded by `State.Apply`; the render path never touches runtime state.

### Property / invariant

- `0 <= Offset <= MaxOffset` over empty/short/large documents and overshoot
  intents.
- Visible row → `(BlockID, Row)` → hit test returns the same block/row.
- Layout is deterministic for the same document and width.
- Expansion/collapse does not change tool semantics, execution state, or
  authority.
- Detached append does not move the anchored content.
- Resize preserves block IDs and expansion state.
- Copied selections contain no ANSI, CR, or renderer padding.

### Benchmark

`BenchmarkViewport` (scroll, expand middle, collapse middle, resize, hit test,
selection, stream, append), `BenchmarkProgramFrame`, and
`BenchmarkFullTranscriptRender` (comparison only).

### Terminal / adversarial

- Deterministic protocol tests for plain, tmux, and screen OSC 52 packaging.
- Existing terminal suites: PTY-based live frame tests and a real tmux live
  session test. The owned alternate-screen selection path is covered at the
  protocol and logical-coordinate level because the environment cannot automate
  a Ghostty/tmux drag reliably. tmux passthrough still requires
  `allow-passthrough on`; this is a terminal configuration limit, not a code
  path.
- Narrow/wide/rapid-resize behavior is covered by the width matrix and resize
  tests rather than a physical monitor.

---

## 11. Security

- No authority, admission, approval, task, evidence, completion, cancellation,
  or execution contract was changed.
- A mouse click can only call `State.ToggleBlock`/`ToolToggled`. It cannot submit
  a prompt, approve a mutation, start a turn, run a tool, or read the repository.
- Layout and rendering perform no network, filesystem, subprocess, provider, or
  tool work.
- Clipboard copy is literal logical text, stripped of ANSI/OSC/control bytes;
  no shell interpolation or external process is involved.
- Selection, expansion, scroll, and clipboard state are ephemeral UI state and
  never enter the session store or model context.
- The live mode's terminal-owned selection and scrollback remain untouched.

---

## 12. Remaining Risks

1. **Resize is O(document).** Every width-dependent projection changes, and
   10,000 blocks cost ~59 ms p50 to reflow. 100/1,000 blocks are ~0.59/6.2 ms.
   Ordinary scroll, hit test, selection, expansion, and streaming are bounded to
   the visible/dirty region; only full-width reflow scales with history.
2. **Streaming one very large assistant block re-renders that block per
   delta.** p99 grows with the block's accumulated markdown (measured ~333 µs at
   500 chunks), independent of document size. A future streaming markdown
   cache could lower it further.
3. **OSC 52 support is terminal-dependent.** A terminal that ignores OSC 52
   silently copies nothing; the code makes no success claim and has no external
   fallback. tmux additionally requires `allow-passthrough on` for the DCS
   wrapper. A very large payload may be rejected by a terminal's OSC limit;
   the implementation does not silently truncate.
4. **Application-owned selection is opt-in.** The default `mouse=select` keeps
   terminal-native drag-copy, and tmux defaults to `report`. Users outside tmux
   who want OSC 52/click-expansion must select `--mouse report` or set
   `mouse = report`. This preserves the existing CLI contract but means the
   owned selection path is not the default everywhere.
5. **Drag does not auto-scroll past the visible viewport.** Releasing outside
   the region clamps to the nearest visible row; selecting beyond the window
   requires scrolling and continuing the drag.
6. **One-shot `Render(state)` has no retained anchor.** It resolves scroll from
   the state mirror and can drift under wrapped growth while detached; the
   interactive `Program.View` path uses the retained tracker. Callers that need
   exact anchored scrolling should drive the program, not call `Render`
   repeatedly.
7. **Bubble Tea mouse-report splitting.** Reporting mode inherits the known
   Bubble Tea v1.3.10 split-read risk for SGR mouse reports; the existing mode
   policy and composer test coverage remain in place. Selection mode avoids the
   protocol entirely.
