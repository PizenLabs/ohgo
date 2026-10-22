# Architecture

How `ohgo` is structured, and why. The port invariants live in
[`MEMORY.md`](MEMORY.md); this file is the map, that file is the law.

## Layers

```
cmd/ohgo     CLI: flags, session wiring, REPL, TUI bridge, doctor, init, docs.
             Builds a shared harness (slash-command backend) used by both UIs.
  │ builds
  ├─► agent    The model/tool loop. Sees only ai.StreamFn, never HTTP.
  │     ├─► workspace Execution authorities (ASK/FUZZ/BUILD): capability
  │     │             policy, tool admission, intent routing, semantic
  │     │             identity, artifacts.
  │     ├─► ai        Message model, three transports (Anthropic,
  │     │             OpenAI-compatible, Ollama native), Registry, probe.
  │     │             Imports creds for secrets, never catalog for facts.
  │     ├─► catalog   Bundled model facts (379 models / 12 providers) plus
  │     │             live discovery for 5 dynamic providers. Produces
  │     │             ai.Model values; imports ai for the type only.
  │     ├─► creds     Credential chain: explicit → file → env → shell text.
  │     │             Single owner of ProviderEnvVars; secrets never printed.
  │     ├─► tools     read write edit delete bash wait glob grep ls symbols
  │     │             graph refs webfetch todo task (task only when maxDepth > 1;
  │     │             wait shares bash's job table; artifact when a session is
  │     │             attached). task imports agent for the subagent spec.
  │     ├─► prompt    System prompt assembly + AGENTS.md discovery (root
  │     │             primary, <root>/.ohgo/AGENTS.md supplementary).
  │     ├─► project   Stable project identity + project-local metadata
  │     │             (project name, project worker identity) at
  │     │             <root>/.ohgo/project.yml.
  ├─► startup  Startup lifecycle. Discover is read-only and classifies a
  │             directory UNINITIALIZED/INITIALIZED; Ensure is the sole
  │             creation authority for <root>/.ohgo (Git + worker + project
  │             name decisions first, mutation last; a cancel creates nothing).
  │             The worker name is project-local, never written to settings.
  ├─► state    Machine-level state location (~/.ohgo): the one owner for
  │             every machine-scoped path (settings, credentials, cache,
  │             sessions, history).
  │     ├─► session   SQLite transcript store (pure Go, no cgo), plus the
  │     │             workspace/project/effective-model record and artifacts.
  │     ├─► tokens    Heuristic token estimator + Tokenizer seam.
  │     ├─► usage     (in agent/) Per-turn token/cost ledger.
  │     └─► tui       State machine (no TTY) + renderer + Bubble Tea bridge.
  ├─► vcs shell diff edit ast
  │           Ported Rust core: git awareness, shell parsing, Myers diff,
  │           fuzzy edit application, Go AST search. Pure Go, zero cgo.
  ├─► config   ~/.ohgo/settings.json: model (+ per-workspace models), tokens,
  │            reasoning, approvals, theme, compaction, and machine-global
  │            user defaults (identity.name). AutoApprove wins over Approvals.
  │            ModelFor(workspace) falls back to the global model.
  └─► jsonschema  Reflection-free validator for tool schemas.
```

Dependency arrows point one way: `catalog → ai` (type only), `ai → creds`
(secrets only), `tools → agent` (subagent spec only), `workspace → agent`
(tool admission only), `prompt → project` (project root for `.ohgo/AGENTS.md`
discovery), `cmd → everything`. Nothing in `ai` imports `catalog`;
nothing in `agent` constructs HTTP; nothing outside `creds` reads a provider
credential from env or shell. A cycle here is a bug, not a shortcut.

## Repository intelligence

`symbols`, `graph`, and `refs` are three read-only surfaces over one
`tools.StructuralSource` and one `ast.Index`. One `GoBackend.Parse` pass
produces symbol coordinates, package imports, resolved references, and static
direct-call sites. There is no second parser, index, daemon, or graph database.

- `symbols` returns exact declaration coordinates.
- `graph` returns bounded package/import relationships with `file:line`
  witnesses and explicit external/unresolved/ambiguous states.
- `refs` returns package-scope symbol references and statically provable direct
  callers/callees. Method dispatch, function values, dot imports, reflection,
  and unresolved targets are counted as unknown or returned as `unsupported`;
  they are never guessed. Callers/callees traversal is bounded by depth, node,
  edge, and work limits, is cycle-safe, and is cancellation-aware.

All three share the same freshness check, workspace path containment, authority
admission, tool-call budget, and child tool set. Their evidence is advisory: it
cannot mutate, execute, grant authority, or complete a task.

## Authority

The three workspaces are runtime authority boundaries, not UI modes. The
authoritative path is `workspace → capability policy → tool admission →
execution`:

- `workspace` owns the policy. `Ask` reads only; `Fuzz` reads and executes but
  cannot mutate; `Build` is the sole mutation authority. `Permits`/`Admit`
  decide, and `Authority` binds them to the agent loop.
- Production installs `Config.Authorize` (`securityGate`) as the **only**
  admission path. The loop evaluates every call through it before execution:
  DENY is terminal, APPROVE requires a fresh confirming evaluation, and AUTO
  executes directly. The legacy `Admit`/`Approve` pair remains only for
  embedders that supply no authorizer; it is not the production path. A
  forbidden call is refused outright rather than offered to the user. The UI
  may display the workspace; it cannot grant capability.
- A tool may refine its capability per invocation through
  `agent.CapabilityReporter`; `bash` uses this to classify a shell command as a
  mutation (a real redirect, a mutating program, a mutating git subcommand) so
  FUZZ cannot use the shell as a back door. The classifier is a strong default,
  not a sandbox: an interpreter one-liner that writes a file is a documented
  residual.
- The session records its workspace, project identity, and effective model.
  A resumed session restores its own snapshot rather than silently following a
  changed configuration.
- `$sup` is an ephemeral, context-light answer surface on the SUP model role: a
  dedicated path that sends a minimal system instruction plus the query through
  the shared provider transport, with no tools, no repository context, and no
  agent loop. Its conversation is never persisted and never enters the main
  history.
- Cross-workspace handoff is artifact-based: `workspace.Artifact` (plan,
  finding, change, verification) is validated and persisted per session; the
  `artifact` tool enforces that a workspace only authors its own kind.

### Intent-aware turns

Authority is a runtime property, not a construction-time constant. Each turn
is classified by a deterministic, model-free classifier
(`workspace.Classify` → `READ` / `EXECUTE` / `INVESTIGATE`) and routed through
an explicit transition table (`workspace.RouteTurn`). The `authorityGate` in
`cmd/ohgo` is the single live authority that both the loop's admission hook
and any subagent read at call time:

- BUILD and FUZZ answer an informational READ turn with ASK semantics for that
  turn only. The active workspace is unchanged, and the turn is projected
  truthfully as `BUILD · ASK / READ` in the composer prefix for the turn's
  duration, then cleared. The UI never represents delegation as a workspace
  switch.
- ASK + EXECUTE or ASK + INVESTIGATE is refused before the model is called;
  the user must switch workspace explicitly.
- FUZZ + EXECUTE is refused likewise: a discovered defect is never BUILD
  authority.
- Delegation may only narrow authority. Removing or changing the mode prompt
  does not widen it — the gate denies the call regardless.

The full matrix, state model, and invariants are in
[`WORKSPACE_AUTHORITY.md`](WORKSPACE_AUTHORITY.md).

Semantic identity (`workspace.Style`) is teal/magenta/blue for ASK/FUZZ/BUILD,
fixed across themes; it never participates in an authority decision.

## The turn

One `Loop.Run` call is one turn:

1. Resolve the model once at startup (`resolveModel`: flag → settings →
   env → catalog default; unknown is an error, never a fallback). The
   completion budget is the model's own ceiling, clamped, never a flat
   constant. Compaction runs first, if due.
2. Stream the assistant message; reassemble tool calls in emission order.
3. Approve each call (settings policy → hook → default deny; `--yes`
   skips), run approved calls with bounded concurrency, exclusive tools
   draining the batch on both sides.
4. Append results, repeat until the model stops calling tools or the
   wall-clock deadline (`--deadline`, default none) expires. Usage is added
   to the ledger per turn.

The caller owns the conversation: the REPL and the TUI accumulate history
across turns and rebase it after compaction (`rebaseHistory`), because
re-appending to a pre-compaction list would resurrect summarised context.
Compaction itself is injected, never constructed in the loop: `New` fails
without a summarizer when compaction is enabled, and the summarizer takes
its `StreamFn` as a parameter so tests substitute a stub.

## Completion, task state, and exit status

Completion is a state transition, not the model's stop reason. The one
authoritative evaluator is `agent.EvaluateTaskCompletion`; every production
path that reports a task outcome calls it with the contract built from the
user's request before execution and the evidence the runtime observed.

- **Evidence.** A mutation objective needs an observed target change and a
  successful verification after it. A checkless execution objective needs a
  process that reached a successful terminal state (`Executes` + `Finished`);
  a started background spawn is not evidence. An informational objective needs
  non-empty visible assistant text in the final turn. Empty or whitespace text
  is `NEEDS_REVIEW`. The evaluator does not judge answer relevance.
- **TUI task path.** `task.Begin` persists the contract, the pre-run target
  snapshot, and later the review reason. `NEEDS_REVIEW` is non-terminal. A
  continuation reuses the persisted contract and snapshot; a materially
  different prompt is refused until the human runs `/task cancel` or
  `/task accept`. Both close the task as `CANCELLED`; neither claims the old
  objective completed, and the next prompt starts a new identity.
- **Stateless paths.** `ohgo run` and the plain REPL use the same evaluator and
  print one verdict: `COMPLETED`, `NEEDS_REVIEW: reason`, `PAUSED: reason`,
  `FAILED: reason`, or `CANCELLED: reason`. They do not create a durable task.
  `ohgo run` exits `0` completed, `2` review, `3` paused, `1` failed, and `130`
  cancelled. The REPL prints the verdict and continues.
- **Background evidence.** A spawn reports `Executes` but not `Finished` and
  not `Mutates`. `wait` reports a finished job as `IsError` when it timed out
  or exited non-zero, reports a mutating job's mutation, and reports
  verification only for a successful recognised check. An un-awaited job is
  outside the completion boundary.
- **Cancellation.** The TUI turn owns one atomic terminal claim shared by
  Ctrl+C and the post-run transition. A cancellation that wins turns the
  action into `PAUSE`; a completion claim that wins first is not rewritten.
  Graph traversal checks cancellation every 256 edge inspections and returns
  the context error, never a truncated success.

## Subagents

Delegation is the one capability that creates another agent, so isolation
is structural, not advisory (`agent/subagent.go`, `tools/task.go`):

- The child inherits stream, model, tools, budget, approval, compaction,
  summarizer. It drops `OnTurn`, `OnToolProgress`, `TransformContext`,
  steering. It refuses: no system prompt (inheriting the parent's is an
  identity leak), an empty task, a write tool under a read-only request.
- The depth bound is baked into the tool set: a subagent at the cap is
  built without a task tool, so it cannot delegate however prompted.
  Default `--max-depth 1` means the top level may delegate once.
- The task tool is omitted entirely when `maxDepth <= 1` or `--no-tools`.

## Background jobs

`bash` with `background: true` spawns a detached job and returns its id;
`wait` collects output incrementally by cursor, under one lock, returning
only what arrived since the last call. Timeouts are kill-after (the call
already returned). Caps: 8 running (refusal names the limit), 32 retained
(oldest completed evicted), 30 KB output per job with a truncation notice.
A background spawn is shared (`Exclusive` false); foreground runs stay
serialised. Both tools are built from one `BashTool` instance — two job
tables would let `bash` start jobs no `wait` could ever see.

## Concurrency

- Shared tools run in parallel; `Exclusive(args)` tools drain the batch
  before and after. Results are reassembled in model emission order, so the
  transcript always matches the assistant message.
- Tool → loop progress flows through `Context.OnProgress`; loop → UI through
  `Config.OnToolProgress` (`started → update → finished`). The final report
  carries the post-`AfterToolCall` result, so the UI never shows output the
  model will not receive. Progress callbacks run outside locks.
- UI state is owned by the update goroutine. Loop goroutines send `Event`
  values through a buffered channel that drops on full — a stalled command
  is worse than a missing preview line. Approvals carry per-request reply
  channels, so an answer reaches the exact tool that blocked.
- Turns never overlap: a submit while busy queues (cap 5, refusal past it)
  and drains in order from the turn's end message on the update goroutine.
  Ctrl+c never quits: it denies a blocking approval and cancels a running
  turn (clearing the abandoned queue, reporting interruption), closes an
  open overlay, clears a non-empty draft, and on an empty idle composer
  points at the deliberate exit chords. Ctrl+d on an empty composer quits.

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
pool. A store that will not open degrades to running without persistence;
a resume id that does not exist is fatal, because an empty session would
look like wiped history.

## Startup and project creation authority

`<root>/.ohgo/` is created by exactly one function, `startup.Ensure`, and only
after the initialization gate has collected its decisions. The gate is
explicit; no startup, discovery, option-resolution, runtime, or TUI path may
create project-local state as a side effect.

- `startup.Discover` is **read-only** and safe to call repeatedly. It reports
  the project root, whether `.ohgo/` exists, whether `project.yml` is valid and
  complete, whether the directory is a Git worktree, and a `State` of
  `Uninitialized` or `Initialized` (`Invalid` distinguishes a broken `.ohgo/`
  from a missing one). A valid-but-incomplete `project.yml` — an id/root-only
  file from an earlier build — is `Uninitialized`: the gate owns completing it.
- `startup.Ensure` fast-paths when `State == Initialized`; otherwise it enters
  `Initializing`, prompts for the Git decision, the project worker name, and the
  project name, and only then commits: `git init` (if confirmed) and the
  project-local metadata to `<root>/.ohgo/project.yml` through
  `project.EnsureWith` (atomic, id preserving). Decisions come first so a cancel
  (`ErrCanceled`) leaves the directory exactly as it was found.
- `resolveOptions` resolves project identity with `project.Load` (read-only),
  never `project.Ensure`. `ohgo` runs the gate before constructing the runtime;
  `ohgo run` cannot show the gate, so it discovers read-only and refuses an
  uninitialized project rather than creating one.
- The project worker identity and project name are project-local
  (`<root>/.ohgo/project.yml`); Project A can name Alice while Project B on the
  same machine names Bob. A machine-global `settings.json` `identity.name` is
  only a suggested default for the gate's prompt and is never written by it.
  Existing project ids are never regenerated on a normal restart, and changing
  the name or worker never changes the id.

## Transports

Three protocols, not one:

- **Anthropic Messages** finalises tool calls inline (`content_block_stop`,
  explicit `message_stop`).
- **OpenAI Chat Completions** finalises after `[DONE]`/EOF because argument
  fragments have no terminator.
- **Ollama native** (`ollama-chat`: NDJSON `POST {base}/api/chat`, tool
  calls reassembled per message, `done_reason` mapped with load-as-error).

The accumulator strategies differ on purpose — unifying the first two drops
the first fragment of every call. One Chat Completions transport serves
groq, together, fireworks, deepseek, cerebras, mistral, plus OpenRouter
(with attribution headers) and the OpenCode gateways (zen/go); the
per-provider work is a base URL and a credential name. `ai.ProbeProvider`
checks reachability + authentication without spending a completion and backs
`/connect` validation, `/usage`, `/status`, and the TUI probe view.
`xai` and Responses-only model lines are excluded on purpose (no Responses
transport in this build), recorded in `TestExcludedProvidersAreDocumented`.

## CLI and harness (`cmd/ohgo`)

Root runs the interactive session; subcommands: `run`, `sessions` (+
`show`), `models [provider]`, `doctor`, `init`, `docs`, `version`. Flags:
`--model`, `--workspace` (ask|fuzz|build), `--resume`, `--dir`, `--no-tui`,
`--plain`, `--no-tools`, `--yes`, `--reasoning`/`--effort`, `--max-depth`,
`--deadline`.

Both interactive paths share one backend (`harness.go`): the full-screen
UI and the plain REPL call the same code for `/model` (static then live
discovery), `/compact`, `/sessions`/`/resume`, `/jobs`, `/usage`,
`/status`, `/doctor`, `/connect` (a Provider Manager modal browses the
credential roster, an API-key modal collects the credential — pasted or
detected — then a wizard: validated model list → saved key + settings),
`/disconnect`
(file/explicit keys only; env/shell keys say where to unset). Presentation
differs; decisions do not. Commands parse before anything reaches the
model; a bare `/` is a path. `$sup <question>` opens the ephemeral support
surface (a SUP-role interaction that is never persisted and never enters the
main history); a bare `$sup` opens it with input focus and starts no
provider call, and ESC closes it. The `$sup` trigger and the `Ctrl+S` chord
are the support surface's only entries. Prompt history (`~/.ohgo/history`,
last 1000, commands excluded, `OHGO_HISTORY_FILE` overrides, empty disables) is
an audit trail, best-effort.

`ohgo init` is a pure-planned wizard (`planInit`) writing
`~/.ohgo/settings.json`; it never overwrites without `--force`. `ohgo
doctor` checks workdir, config, catalog, model resolution, credentials,
session store, tool set — each failure naming its fix — as structured
reports shared with the in-TUI doctor. `ohgo docs --dir` generates man
pages + markdown from the live cobra tree.

## Terminal workspace (`tui`)

The workbench separates runtime truth from rendering: `State.Apply(Event)`
is the only transition (terminal-free, test-covered); `Render(State)` is
pure and performs no I/O; `Program` bridges the agent loop via buffered
event/approval channels plus Bubble Tea commands. In full-screen mode the
program owns turn output and persistence outright: it clears the loop's turn
and compaction hooks (`SetTurnHook(nil)`, notice-routed compact reports), so
no turn ever prints raw text over the alt-screen and no turn persists twice.
Derived display phases
(`DeriveRuntime`: idle, working, streaming, running tool, awaiting
approval, shutting down) are computed per render, never stored.

- `tui/model.go`: the state machine — composer, viewport (scroll + sticky
  follow), queue, overlays, sessions (Session Manager) and providers (Connect
  Provider Manager and its API-key modal) plus the models/jobs pickers and the
  two-tab Command & Help Center (Commands / Shortcuts), theme, workspace,
  probe/doctor/usage wiring. Slash
  discovery state filters the authoritative
  `Commands` registry inline — never a second catalog.
- `tui/composer.go`: multiline input (alt+enter / ctrl+j), cursor editing,
  prompt history with draft preservation.
- `tui/entries.go`: transcript entries rebuilt from replayed messages (tool
  cards from stored calls, orphans kept, UI-only skipped).
- `tui/markdown.go`: dependency-free chat prose (fences, code, headings with
  hierarchy, nested/ordered lists, quotes, rules, links, tables with
  terminal-width-aware columns and a stacked narrow fallback). One wrap
  ownership: plain cells are measured and folded first, ANSI applied after,
  so styling never inflates the budget and raw model ANSI is consumed at the
  boundary. Partial streaming constructs render provisionally (unclosed
  markers stay literal) and converge on completion; rendering is a pure
  function of the accumulated text, so chunk splits cannot diverge it.
- `tui/sanitize.go`: the trust boundary for everything else. Tool results,
  prompts, notices, composer input, and chrome fields pass here: SGR colors
  survive, cursor-movement CSI, OSC hyperlinks, carriage returns (folded to
  newlines, or spaces inline), and stray controls do not. A cursor-aware
  variant keeps composer cursor mapping exact across sanitization.
- `tui/mode.go`: terminal mode control — alternate scroll (DEC 1007) and the
  mouse-mode policy. Mouse reporting is **off by default** because requesting it
  makes the terminal stop doing its own selection. The default route to the
  wheel is 1007, which multiplexers do not forward (measured against tmux 3.7c:
  a pane using 1007 alone receives nothing), so `MouseDefaultFor` selects
  reporting when `$TMUX` is set. `--mouse select|report` and the `mouse` setting
  override it; anything unrecognised degrades to `select` so a typo never costs
  drag-copy. Both modes are bracketed on entry and restored on every exit path.
- `tui/width.go`: cell-width measurement, truncation, wrapping
  (ANSI-stripped, CJK/emoji-aware, hard-breaks unbroken runs, combining
  marks stay attached).
- `tui/layout.go`: responsive tiers (wide ≥100, medium 70–99, narrow <70,
  mini <40×8), content width, conversation height, bounded composer (two
  framing rules + input rows + slash suggestion rows + state line, all
  counted so reserved and rendered rows agree). Suggestion rows carry a
  viewport budget (`Layout.SlashLines`): on short screens they yield first
  so the transcript keeps a row. An open picker releases the composer rows
  it hides, and the picker area fills them instead.
- `tui/style.go`: two palettes (dark/light) behind one resolver plus
  semantic chrome (typo degrades to dark; text plus color, never color
  alone).
- `tui/view.go`: adaptive header/conversation/composer/status, bounded
  approval preview (cap derived from the computed chrome, not a constant;
  the separator blank yields on viewports too short to hold it),
  pickers, mini-mode notice, overlap-free slicing. The transcript and the
  picker each emit exactly their viewport rows — entry separators live
  inside the budget, and a zero-row transcript yields to modal pressure on
  tiny screens rather than overflowing.
  The composer is two restrained rules around the input with an inline
  slash-suggestion list and a queued-prompts line (busy and approval live
  in the header, status, and modal — never restated under the input). `?` on an empty composer opens the
  Command & Help Center from the registry; while composing it stays literal. Overlays take
  over (transcript + composer hidden) so streaming output never fights a
  moving cursor.
- `tui/commands.go`: the shared `/` registry (`ParseCommand`, `HelpText`,
  typo suggestions); both UIs dispatch through it.
- `tui/picker.go`: the generic list state machine (query + cursor,
  highlight, scroll, window rows from `WindowSizeMsg`; whitespace-AND
  search with `provider:` pins and verified-price `free` matching;
  value-preserving `SetItems` under a generation guard). The model picker
  resolves native `provider/id` through the catalog with caller-built
  detail (context/price/local/caps).
- Catalog freshness (`catalog/dynamic.go`, `snapshot`): `SnapshotAll`
  merges bundled + cached rows per (provider, id) with origins;
  `DiscoverModelsFresh` re-fetches one provider (24h disk cache in
  `~/.ohgo/cache`, stale fallback); pricing presence travels with each
  record so unknown is never presented as free. Dynamic set: `ollama`,
  `ollama-cloud`, `openrouter`, `opencode-zen`, `opencode-go` (13th
  provider overall: plain `ollama` has no bundled rows — local models exist
  only on the user's machine).

## Credentials and discovery

- Precedence is explicit → credential file (`~/.ohgo/credentials.json`,
  0600, atomic) → environment → parsed shell startup files. Shell files are
  read as text and never executed; dynamic lines are skipped and reported.
  `creds` owns the table; `ai.Registry` wraps it with base-URL overrides
  (`SetBaseURL` replaces the whole base, `/v1` included) and explicit keys
  (`SetAPIKey` beats the environment, which is what makes gateways and test
  servers usable).
- Sources are labeled everywhere (`environment (X)`); values never reach
  output, logs, transcripts, or errors (`Scrub` + a test scanning doctor
  output for the test key).
- Bundled catalog rows answer offline; ids the bundle never saw resolve
  through live discovery (bounded timeout, stale-cache fallback).
  Discovered rows face the same validation as bundled ones; context-less
  rows are skipped, minimal gateway ids merge with bundled facts or not at
  all.

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

Run `make bench` for the current numbers; `make check` for the full gate
(build + vet + gofmt + tests). Releases are `make release` (four static
tarballs: linux/darwin × amd64/arm64, `CGO_ENABLED=0`) installed via
`install.sh` or `make install` (`~/.local/bin` by default).
