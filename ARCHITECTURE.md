# Architecture

How `ohgo` is structured, and why. The port invariants live in
[`MEMORY.md`](MEMORY.md); this file is the map, that file is the law.

## Layers

```
cmd/ohgo     CLI: flags, session wiring, REPL, TUI bridge, doctor
  │ builds
  ├─► agent    The model/tool loop. Sees only ai.StreamFn, never HTTP.
  │     ├─► ai        Message model, transports (Anthropic, OpenAI-compatible),
  │     │             credential registry. No catalog imports.
  │     ├─► catalog   Embedded model facts. Produces ai.Model values.
  │     ├─► tools     read write edit bash glob grep ls webfetch todo task.
  │     ├─► prompt    System prompt assembly + AGENTS.md discovery.
  │     ├─► session   SQLite transcript store (pure Go, no cgo).
  │     ├─► tokens    Heuristic token estimator + Tokenizer seam.
  │     └─► tui       State machine (no TTY) + renderer + Bubble Tea bridge.
  ├─► vcs shell diff edit ast
  │           Ported Rust core: git awareness, shell parsing, Myers diff,
  │           fuzzy edit application, Go AST search. Pure Go, zero cgo.
  ├─► config   ~/.ohgo/settings.json + approval precedence.
  └─► jsonschema  Reflection-free validator for tool schemas.
```

Dependency arrows point one way: `catalog → ai`, `tools → agent`,
`cmd → everything`. Nothing in `ai` imports `catalog`; nothing in `agent`
constructs HTTP. A cycle here is a bug, not a shortcut.

## The turn

One `Loop.Run` call is one turn:

1. Convert agent messages to a provider request (compaction first, if due).
2. Stream the assistant message; reassemble tool calls in emission order.
3. Approve each call (policy → hook → default deny), run approved calls with
   bounded concurrency, exclusive tools draining the batch on both sides.
4. Append results, repeat until the model stops calling tools or the
   wall-clock deadline expires.

The caller owns the conversation: the REPL accumulates history across turns
and rebases it after compaction, because re-appending to a pre-compaction
list would resurrect summarised context.

## Concurrency

- Shared tools run in parallel; `Exclusive(args)` tools drain the batch
  before and after. Results are reassembled in model emission order, so the
  transcript always matches the assistant message.
- Tool → loop progress flows through `Context.OnProgress`; loop → UI through
  `Config.OnToolProgress` (`started → update → finished`). The final report
  carries the post-`AfterToolCall` result, so the UI never shows output the
  model will not receive.
- UI state is owned by the update goroutine. Loop goroutines send `Event`
  values through a buffered channel that drops on full — a stalled command
  is worse than a missing preview line. Approvals carry per-request reply
  channels, so an answer reaches the exact tool that blocked.

## Errors, by kind

| Kind | Shape | Example |
| --- | --- | --- |
| Infrastructure | Go `error`, aborts the run | transport failure, cancelled context |
| Tool outcome | `agent.Result{IsError}`, model retries | bad path, failing command |
| Schema violation | error `Result`, never raised | missing required argument |
| Misconfiguration | fail at construction | compaction without a summarizer |
| Spent budget | `nil` error, turns returned | wall-clock deadline expiry |

A deadline is not a failure: reporting it as one would make callers discard
work they were entitled to keep.

## Storage

The session store keeps the **agent view** of the transcript, not the wire
shape, so a later run with a different model is not fed host-specific
extensions. UI-only messages are dropped at write time. Writes use one
connection and one transaction per append (WAL on), which is what keeps
`modernc.org/sqlite` from reporting "database is locked" under its default
pool.

## Transports

Two SSE protocols, not one: Anthropic finalises tool calls inline
(`content_block_stop`, explicit `message_stop`); OpenAI-compatible endpoints
finalise after `[DONE]`/EOF because argument fragments have no terminator.
The accumulator strategies differ on purpose — unifying them drops the first
fragment of every call. Six providers (groq, together, fireworks, deepseek,
cerebras, mistral) share the Chat Completions transport; the per-provider
work is a base URL and a credential name. OpenRouter rides the same
transport with attribution headers; Ollama Cloud speaks native `ollama-chat`
(NDJSON `/api/chat`, tool calls reassembled per message, `done_reason`
mapped with load-as-error).

## Terminal workspace (`tui`)

The workbench separates runtime truth from rendering: `State.Apply(Event)`
is the only transition (terminal-free, test-covered); `Render(State)` is
pure and performs no I/O; `Program` bridges the agent loop via buffered
event/approval channels plus Bubble Tea commands. Derived display phases
(`DeriveRuntime`: idle, working, streaming, running tool, awaiting
approval, shutting down) are computed per render, never stored.

- `tui/width.go`: cell-width measurement, truncation, and wrapping
  (ANSI-stripped, CJK/emoji-aware, hard-breaks unbroken runs).
- `tui/layout.go`: responsive tiers (wide ≥100, medium 70–99, narrow <70,
  mini <40×8), content width, conversation height, bounded composer.
- `tui/style.go`: semantic chrome styles, separators, hints, runtime
  markers (text plus color, never color alone).
- `tui/view.go`: adaptive header/conversation/composer/status, bounded
  approval preview and pickers, mini-mode notice, overlap-free slicing.
- Pickers (`sessions`, `models`, `jobs`, `commands`, connect flow) share one
  overlay, filter, and keyboard contract; `/model` resolves native ids
  through the catalog with caller-built detail (context/price/local/caps).
- The model picker owns a list state machine (`tui/picker.go`): query plus
  text cursor, highlight index into the filtered set, scroll offset, and
  result-window rows recomputed from `WindowSizeMsg`. Navigation
  (arrows/page/home/end) always keeps the highlight rendered in-viewport;
  search tokenizes on whitespace (AND across name, id, provider, metadata)
  with `provider:` pins and verified-price `free` matching; refreshes swap
  rows by value-preserving `SetItems` under a generation guard.
- Catalog freshness (`catalog/dynamic.go`): `SnapshotAll` merges bundled
  and cached rows per (provider, id) with origins; `DiscoverModelsFresh`
  re-fetches one provider with stale fallback; pricing presence travels
  with each record so unknown is never presented as free.

## Credentials and discovery

- Precedence is explicit → credential file (`~/.ohgo/credentials.json`,
  0600, atomic) → environment → parsed shell startup files. Shell files are
  read as text and never executed; dynamic lines are skipped and reported.
- Sources are labeled everywhere (`environment (X)`); values never reach
  output, logs, transcripts, or errors.
- Bundled catalog rows answer offline; ids the bundle never saw resolve
  through live discovery (24h disk cache, stale fallback). Discovered rows
  face the same validation as bundled ones; context-less rows are skipped,
  minimal gateway ids merge with bundled facts or not at all.

## Performance notes

- Search prunes before it scans: built-in excludes (`.git`, `node_modules`,
  build caches) are pruned before any pattern or gitignore work.
- `grep` has a literal fast path (plain byte scan) gated on every option
  that affects the result; `glob` ranks by mtime with a deadline that
  returns partial results rather than nothing.
- `bash` output streams through a throttled (150 ms) sink; the UI never
  pays per-line for a build emitting thousands of lines per second.
- Token counting is heuristic (bytes/4, round up) behind a `Tokenizer`
  interface, so a real BPE encoder drops in without touching call sites.

Run `make bench` for the current numbers; `make check` for the full gate.
