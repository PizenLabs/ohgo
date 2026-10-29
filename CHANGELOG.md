# Changelog

Format: `Added`, `Changed`, `Fixed` under `Unreleased`. Released sections are
immutable.

## [Unreleased]

### Added

- Explicit self-update: `ohgo update` (`--check` to observe only) resolves the
  published GitHub release, selects the tarball by the running `GOOS`/`GOARCH`,
  downloads to a temporary file beside the executable, requires the published
  SHA-256 entry, extracts only the `ohgo` entry with traversal/symlink/bomb
  guards, replaces the executable atomically, and keeps the previous binary
  until the new one reports the requested version. A failed verification
  restores the previous binary. Package-manager-owned installs are detected by
  receipt and refused with the owner's command; Windows and other unpublished
  platforms report unsupported. Normal invocations only read a 24-hour local
  cache (`~/.ohgo/update.json`) and may refresh it in the background; the hint
  is header chrome in the TUI and one line in the plain REPL, never transcript,
  never machine output. The TUI offers the same authority as `/update`; the
  hint reports availability only and never installs by itself. Disable
  automatic checks with `OHGO_UPDATE_CHECK=off` or
  `"update": { "enabled": false }`. Self-update is not exposed as an agent
  tool.

- Post-update lifecycle: a verified explicit update records a pending
  transition in `~/.ohgo/update-transition.json` and the TUI moves to a
  restart-required screen that exits only on Enter. The next interactive
  launch presents a centered, one-shot What's New card from release notes
  embedded in the binary (`update/changelog.json`), acknowledged with Enter
  and never shown again. Merely finding a newer release, a manual binary
  replacement, a normal restart, or a rollback creates no notice. Non-
  interactive commands such as `ohgo version` and `ohgo update --check`
  never consume it.

- MCP capability layer: normalized `<server>:<capability>` identities with
  server provenance, `read`/`execute`/`interact`/`mutate`/`unknown`
  classifications mapped to `READ`/`EXECUTE`/`EXTERNAL_SIDE_EFFECT`, explicit
  trust metadata that never grants authority, streamable-HTTP transport beside
  stdio, discovery caching with explicit refresh, prompt templates as untrusted
  context, progress propagation, human-visible `/mcp` inspection, and
  configured-credential redaction in results, descriptions, and errors.
  Execute-class MCP tools use execution authority (ASK cannot run them; FUZZ
  and BUILD can after approval), while interaction/mutation stays at the BUILD
  boundary. See `docs/architecture/MCP.md`.

- MCP interoperability: discovery follows `nextCursor` to completion,
  `mcp_list` publishes the server's `initialize` instructions and a bounded
  input schema per tool (`summary` by default, `full` for property
  descriptions, `none` to omit), and each server accepts a `dir` working
  directory (defaulting to the workspace root). A compatible client now sees
  every tool a paginated server such as gry advertises and can build valid
  arguments from discovery alone.
- Intent grounding and semantic integrity: `workspace.BuildIntentContract` now
  attaches a deterministic `contract.IntentGrounding` (action, entities, scope,
  constraints, assumptions, ambiguities). The loop refuses a mutation the
  request did not ground before the security boundary; the completion evaluator
  reviews dropped entities, invented action narratives, narrowed or unresolved
  scope, and unsupported negative conclusions. Grounding persists with the
  existing task contract, is projected into the runtime state block, and never
  adds a model call. See `docs/architecture/INTENT_GROUNDING.md` and
  `docs/report/INTENT_GROUNDING_CLOSURE.md`; benchmark with
  `go run ./bench/intent`.

- Open-source project documentation: a reorganized `README.md` with the project
  mascot, feature map, usage, authority model, and architecture links, plus
  `CONTRIBUTING.md`, `SECURITY.md`, and `CODE_OF_CONDUCT.md`. `LICENSE` remains
  Apache-2.0; the issue and pull-request templates link the new policies.
- Phase 8 external knowledge foundation: a shared `knowledge` boundary for
  untrusted external data, provenance, evidence kinds, result bounds, and
  truthful failure classes.
- `search` tool over configured Brave/Tavily providers: bounded query, result
  page, snippets, response bytes, retries, provenance, and failure
  classification. Results are wrapped as untrusted data and carry no
  completion flags.
- `mcp_list`, `mcp_read`, and `mcp_call` over configured stdio MCP servers:
  discovery is separate from authorization, every tool has an explicit
  read/interact/mutate/unknown classification, unknown tools fail closed,
  arguments are schema- and structure-validated, responses are bounded, and
  cancellation kills the server process.
- `browser` tool: bounded GET read/navigation with explicit named sessions,
  exact host policy, redirect/navigation/byte/character/link/session limits,
  SSRF blocking, and no JavaScript, form, click, or mutation surface.
- Root budget dimensions `external_calls` and `external_bytes`; every external
  attempt and observed byte is charged to the same tree grant. Search, MCP, and
  browser results add bounded audit details and an explicit `external` marker in
  the TUI call summary. The pre-existing `web_fetch` now returns the same
  untrusted envelope with `WEB_FETCH` provenance.

- `refs` read-only tool: exact symbol references and statically provable direct
  callers/callees over the shared `ast.Index`, with `ok`, `ambiguous`,
  `not_found`, `truncated`, and `unsupported` states. Method dispatch, function
  values, dot imports, and unresolved targets are reported as unknown rather
  than guessed.
- `ast.Index` now builds a reference and direct-call projection in the same
  parse as symbol coordinates and package imports. `symbols`, `graph`, and
  `refs` share one `StructuralSource`; there is no second parser or index.
- Phase 5 adversarial coverage for retry after model/tool/verification
  failure, cancellation, deadline, exhaustion, sibling child contention, and
  failed-child retry. No budget semantics changed.

- `ohgo init` — first-run wizard writing `~/.ohgo/settings.json` (model +
  theme). Never overwrites without `--force`; prefers the catalog default
  when its credential is present, else the first credentialed provider.
- Credential chain: explicit key, `~/.ohgo/credentials.json` (0600, atomic),
  environment, parsed shell startup files (never executed; dynamic lines
  skipped and reported). Sources are labeled everywhere; values never printed.
- Providers: OpenRouter (live discovery + attribution headers), OpenCode Zen
  and Go (shared `OPENCODE_API_KEY`, per-protocol endpoints, bundled seeds +
  live discovery), Ollama local (keyless, live tags + `/api/show` caps) and
  Ollama Cloud (native chat transport, Bearer auth). 164 catalog rows.
- Commands (both interfaces): `/connect` guided setup with validation,
  `/disconnect`, `/usage` (usage, cost, and provider status inspector),
  `/status`, `/doctor` in-TUI, `/retry`, `/compact`, typo suggestions. `/copy`
  deliberately omitted (no reliable cross-terminal clipboard without new
  platform code).
- `ohgo doctor` now probes the active endpoint (reachable/authenticated) and
  `ohgo docs --dir` continues to generate the reference.
- `ohgo docs --dir` — man pages and markdown reference generated from the
  live command tree, so flags cannot drift from their documentation.
- `make release` — versioned static tarballs (linux/darwin × amd64/arm64)
  plus sha256sums; `install.sh` installs one by version.
- `bash` background jobs: `background: true` spawns a detached job and returns
  its id at once, with the timeout as a kill-after rather than a wait bound.
- `wait` tool: blocks until a background job produces output or finishes,
  returning only what arrived since the last wait. A wait with nothing to wait
  for returns immediately instead of hanging until timeout.
- Full-screen UI rebuild: multiline composer with cursor editing and history,
  scrollback viewport, prompt queue while busy, cancellable turns, selection
  overlays (`/sessions` resume, `/model` browser, `/jobs` inspector),
  markdown transcript, dark/light palettes (`/theme`, persisted).

- `ohgo doctor` — setup diagnostics covering workdir, config, catalog, model
  resolution, credentials, session store, and tool set. Each failure names its
  fix.
- `/model provider/id` in the plain REPL, resolving through the catalog like
  the full-screen UI. Unknown names point at `ohgo models`.
- Plain-REPL prompt history at `~/.ohgo/history` (`OHGO_HISTORY_FILE`
  overrides, empty disables), capped at 1000 lines.
- `Makefile` (`build test vet fmt race check bench install clean docker
  snapshot`) and a multi-stage `Dockerfile` producing a static binary.
- `ARCHITECTURE.md` — layer map, turn lifecycle, concurrency, error taxonomy,
  storage, transports, performance notes.
- `catalog/models.md` — extraction source and regen procedure.

### Changed

- Configured MCP capabilities are no longer hidden by task-shape narrowing on
  ordinary code questions. They remain external evidence and still pass the
  security boundary, but a human-configured server is not optional web
  knowledge; the action space keeps `mcp_list`/`mcp_read` available. Read-only
  subagents may also use those two tools.
- A clean JSON-RPC error from an optional method (for example a tools-only
  server rejecting `resources/list`) no longer kills the process, so discovery
  and the following call reuse one handshake. External-call budgets have a
  small floor so paginated discovery is not refused before it advertises a
  single tool.
- Completed turns close with a semantic recap (`▧ recap:`) instead of duration
  and token telemetry. The narrative is derived from the run's own evidence —
  changed paths, verification commands, inspected files, and the assistant's
  closing prose — and adapts to greetings, explanations, and executed work. It
  renders italic in the dim `Palette.Recap` token, directly above the composer,
  and never enters copy payloads or the session log.
- Turns are intent-routed: a deterministic classifier maps each request to
  READ / EXECUTE / INVESTIGATE, and an explicit transition table decides the
  authority. BUILD/FUZZ answer informational turns with ASK semantics and keep
  their own active workspace; ASK refuses execution requests and FUZZ refuses
  mutation requests until the user switches workspace explicitly. Delegation
  only ever narrows authority, the active workspace never moves implicitly, and
  the runtime gate — not the prompt — is the boundary.
- `/usage` is now a dedicated usage & provider inspector modal: token metrics,
  last-request cache reads/writes, lifetime and session cost, and the provider
  roster with credential status, all in the same framed card style as
  `/status`. The redundant `/providers` and `/sup` slash commands were removed;
  `$sup` and `Ctrl+S` remain the support surface's triggers.
- `ohgo --version` now prints the version (previously only `ohgo version`).
- `flagValue` also consults persistent flags, so resolution works on commands
  built but not yet executed.
- Empty-conversation UI now shows a deliberate welcome (identity, one
  invitation, at most three examples) as chrome, never as a transcript entry,
  so it cannot enter the session log; it hides once content exists and never
  reappears over a resumed transcript.
- Header and status line no longer duplicate identity, model, and runtime
  phase; the header owns them and the status line carries workspace context.
- The transcript region fills the viewport, keeping the composer pinned to the
  bottom; the composer hard-wraps long input instead of overflowing.
- Theme tokens are centralized (muted dark surfaces, a single mint accent) and
  applied consistently across light and dark palettes.
- Header brands the release ("ohgo v1.0.0") and the welcome frame titles
  itself the same way; the greeting ("Good evening, …") renders bold while
  the brand keeps the heading style.
- The empty-conversation welcome shows a block-character mascot in a right
  column beside the metadata text when the viewport can host it whole, and
  falls back to the text-only header on a narrower or shorter terminal.
- Completion is now evidence-bound on every production path. A checkless
  execution objective needs a finished successful process; an informational
  objective needs non-empty visible answer text. `ohgo run` prints a
  `COMPLETED` / `NEEDS_REVIEW` / `PAUSED` / `FAILED` / `CANCELLED` verdict and
  exits 0 / 2 / 3 / 1 / 130; the plain REPL prints the same verdict.
- Task checkpoints persist the completion contract, the pre-run target
  snapshot, and the review reason. A resumed `NEEDS_REVIEW` task is evaluated
  against that contract, and `/task [status|cancel|accept]` is the explicit
  human abandonment/replacement path.
- Background jobs report truthful evidence: the spawn reports `Executes`, not
  `Mutates` or `Finished`; `wait` reports non-zero and timed-out jobs as
  errors, reports a mutating job's mutation, and only a successful recognised
  check as verification.
- The TUI terminal task transition and cancellation share one atomic claim, so
  a cancel that wins cannot be followed by `COMPLETED`.


### Fixed

- `/commit` failed with "the COMMIT model did not produce a valid proposal"
  after bounded retries when a small model omitted or malformed the scope (for
  example `.ohgo`), exceeded the subject/bullet budgets, emitted too many
  bullets, or used a common formatting variant. A deterministic final repair now
  derives a scope from the approved paths, fits overlong lines, caps the body,
  and normalizes the separator and `*` markers; the repaired text still goes
  through the full validator and still requires human approval.
- The focused subject rewrite preserved an invalid body because it checked only
  the subject. It now verifies the body before reattaching it.
- Plain REPL ignored `/model` (reported as unknown command); mid-session
  switching required a restart.
- `PORT.md` still listed `todo` as remaining though ported, and `notebookEdit`
  as remaining though it does not exist in the source revision.
- An execution request such as `run ls` could complete after a clean model stop
  with nothing executed.
- An informational request could complete on an empty or whitespace-only final
  answer.
- `ohgo run` could exit `0` after an unverified change, a refusal, a budget
  stop, or a deadline stop.
- A resumed `NEEDS_REVIEW` task could be completed by evidence belonging to a
  different objective.
- `wait` reported a failed or timed-out background job as a successful
  observation.
- Cancellation could land between the TUI's `ctx.Err()` check and
  `task.Complete`, leaving a `COMPLETED` task after the user cancelled.
- A cancelled reverse graph query could return a truncated result instead of
  the context error.
