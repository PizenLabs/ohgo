# Changelog

Format: `Added`, `Changed`, `Fixed` under `Unreleased`. Released sections are
immutable.

## [Unreleased]

### Added

- Structured verification runtime: the `verify` capability owns a service
  lifecycle (`STARTING`/`RUNNING`/`READY`/`FAILED`/`STOPPING`/`STOPPED`/
  `TIMED_OUT`), HTTP/TCP/output readiness, port discovery, bounded HTTP and
  command checks, an optional headless-browser check, and process-group cleanup
  on every exit path. Independent checks run with bounded concurrency; browser
  verification is optional and reports `VERIFICATION_INSUFFICIENT` when no
  headless browser exists.
- Structured normalization evidence: provider envelopes that fail to parse now
  carry `ToolNormalizationFailure` records (`INCOMPLETE`, `INVALID`,
  `UNSUPPORTED`) with a bounded raw fragment, protocol, provider, and
  recoverability. The raw fragment is audit-only and is never executed or
  published as an answer.
- Project structural runtime: `ast.Index` is now an incremental snapshot over
  files, packages, configs, tests, and entrypoints, with `CURRENT`/`UPDATING`/
  `STALE`/`INVALID` freshness, generation, snapshot identity, parse-failure
  disclosure, and a bounded `structure` read-only tool. Incremental refresh
  parses only changed Go files and re-resolves relationships from retained raw
  syntax. `graph`, `refs`, and `structure` now enter `cognition` as structural
  evidence carrying snapshot provenance and completeness.
- Adaptive multi-language structural runtime: Go keeps its native backend;
  pinned tree-sitter adapters add TypeScript/TSX, JavaScript/JSX, Java, C#,
  Rust, Kotlin, Ruby, C, and C++ over the same `ast.Index`. Canonical facts
  preserve language-specific kinds, visibility, containers, imports, explicit
  inheritance/implementation clauses, calls, tests, entries, and source
  coordinates. `structure` adds `languages` and `relations` views; `symbols`
  and `refs` expose language/native-kind/exported provenance. A no-cgo build
  keeps Go and reports the other extensions as unsupported.
- Adaptive activation: `TaskShape` derives `NONE`/`OVERVIEW`/`TARGETED`/
  `IMPACT` from existing shape signals, withholds structural tools at `NONE`,
  and lets overviews stay metadata-first. Targeted/impact queries parse pending
  supported files on demand; semantic escalation is explicitly unsupported.
- Structural evidence now carries language, adapter, resolution, structural
  completeness, project-scope completeness, and truncation in addition to
  snapshot id and generation (`EvidenceVersion` 4). Snapshots disclose
  per-language parsed/pending/failed counts, inventory completeness, structural
  completeness, and unsupported source extensions.
- Project-boundary scope: a nested repository is included in the same
  structural snapshot even when the enclosing repository's `.gitignore` hides
  its directory, and the nested repository's own ignore rules govern its
  contents. `structure` adds a metadata-only `scope` view, and snapshots report
  `nested_projects`, `excluded_roots`, file-level `excluded_source_files`, the
  decisive rule, shallow build markers, `test_files`, and independent
  scope/inventory/structural/result completeness.
- Evidence claim classes: `cognition` now classifies each observation as
  documentation, configuration/manifest, implementation source, or runtime, and
  retains bounded structural counts such as `test_files` (`EvidenceVersion` 5).
  The pre-answer runtime state block reports which classes the run supports, so
  the model sees the claim boundary before it writes instead of learning it
  only from a completion review.
- Evidence-to-claim completion: provenance now distinguishes documentation,
  configuration/manifest, inventory, source path, source content, source
  import, source use, structural evidence, and runtime (`EvidenceVersion` 6);
  the state block derives an actionable evidence frontier and dependency
  strength. Component identity is path-derived and shared with the answer
  audit, so source from one component cannot support another. Strong
  completeness claims require an observed component source scope; dependency
  wording follows the `DECLARED`/`IMPORTED`/`INITIALIZED`/`USED_IN_SOURCE`/
  `OBSERVED_AT_RUNTIME` ladder; unbounded concept absences stay review; and an
  identical read-only observation in a later turn is reused instead of
  re-executed.

### Changed

- Repository scoping is injected into the structural index from the shared
  `ignore.Matcher`, so `.gitignore` membership has one owner and `ast` stays a
  dependency-free intelligence leaf. A rule or scope fingerprint change
  refreshes membership without re-parsing unchanged source files. Structural
  scope treats a nested repository as a project boundary and evaluates that
  repository's own ignore rules inside it.
- `structure` overview no longer repeats `parsed_files`, `unsupported_source`,
  or the module line in both the header and the body. A repeated overview of
  the same snapshot is marked `repeated: true` so the model can recognize that
  it adds no new inventory facts.
- `graph` remains the Go package import graph; a non-Go selector is pointed at
  `structure`/`refs` instead of being folded into a false package edge. The
  structural snapshot remains in memory. `<project>/.ohgo/graph.db` is still
  reserved and unimplemented; no derived persistence was added.
- Configuration and manifest observations are no longer implementation
  evidence. A Makefile, Compose file, dependency manifest, README, directory
  listing, or structural inventory declares topology, dependencies, or
  metadata; a broad project survey now requires an observed source read before
  it can close. The state block names that missing class directly.

### Fixed

- A reused or refused tool outcome now publishes its start/finish pair in its
  own turn. A stale read that had actually run earlier can no longer be
  replayed after the final answer by the host's post-run fallback, which now
  publishes per completed turn.
- A substantive answer that shares one assistant message with a batch that
  cannot add evidence—every call is withheld by the task stage or repeats an
  observation already current in the run—is now the agent loop's terminal
  transition. Those continuation calls are dropped before dispatch, so a
  finished answer is no longer followed by refusal or reuse cards. A new
  offered capability keeps the normal path because the text may be narration
  before real work; a message that only announces an action keeps the bounded
  recovery, and policy-sensitive proposals remain visible.
- An invalid tool invocation no longer poisons capability health or blocks a
  recoverable task. Argument, selector, unknown-tool, unsupported-operation,
  authority, budget, and stale-evidence failures are request-level: the
  identical request is refused once with schema-grounded correction, a corrected
  request remains eligible, and only genuine capability failures enter provider
  health. A failed structural provider is reconciled against independent
  authorized providers (README, manifests, listings, source reads), a successful
  overview survives a later targeted failure, and completion only excuses a
  pending provider failure when the evidence contract is actually satisfied.
  This closes the session-224 failure mode where `structure backend` was
  reported as a `remote_tool_error` and blocked the project analysis.
- A project-analysis request that opens with an ambiguous read verb and adds a
  later explicit read clause (`check this project and analyze what this project
  is about?`) now carries a strong READ signal and reaches the ASK authority
  boundary instead of silently staying in BUILD.
- A truncated metadata overview's `tests: 0` is no longer treated as proof that
  no tests exist. Only a complete, unscoped structural test inventory (or the
  narrow complete Go scan) can support a test-file absence claim, and the
  state block reports a partial inventory as `absence unproven`.
- A configuration-derived runtime claim is no longer promoted by a
  Makefile/Compose/README observation. The runtime state block reports
  `implementation=NOT_OBSERVED` and `runtime=NOT_OBSERVED`, and the completion
  audit still rejects an active behavior claim without source or runtime
  evidence.
- An implementation-status claim ("fully implemented", "stubs (health checks
  only)", "scaffolded") can no longer be supported by a README, manifest,
  Compose file, directory listing, or structural inventory. The answer audit
  requires a source observation for each named component and names the missing
  component in the review reason; reading one component does not support a
  claim about another.
- A behavior claim that names a port must be supported by a source observation
  containing that same port or by a live runtime address on that port. Reading
  one service's listener no longer supports a claim about a different service's
  port.
- Structural inventory no longer loses a nested repository to the enclosing
  repository's `.gitignore`. A root rule such as `backend/` no longer reports
  `go: 0` when `backend/` is a nested Go project; the boundary is included,
  disclosed, and its tests remain discoverable.
- A malformed call can no longer downgrade a proof the runtime already holds.
  A schema violation never executed, so it is neither work nor an observation
  about the project: completion ignores it for pending failures and outstanding
  work, and a late malformed continuation after a successful runtime
  verification still reports the objective as proven.
- A complete evidence lifecycle is an answer-only surface. The projection now
  marks `StageComplete` as answer-eligible in addition to withdrawing every
  tool, so a continuation call cannot be dispatched once the objective's
  evidence is complete even if a future narrowing leaves a capability behind.
- `verify` accepts the equivalent argument shapes models routinely emit instead
  of spending a turn on a harness contract error: a single literal for
  `checks[].contains` is the same assertion as a one-element list, and a
  service `cwd` may be an absolute path inside the workspace (an absolute path
  outside it is still refused as an escape).
- The recap attributes the outcome to the observation that proved it. A live
  runtime is narrated as "Ran <service> and verified <url> answered", not as
  the environment probes that ran while the model oriented itself; a sentence
  announcing an observation still to come is no longer quoted as a finding.
- Running the project is no longer reported `PROVEN` by a command that merely
  finished. `run this project`, `run it`, `run the app`, `start the dev
  server`, and package-manager `start`/`dev` scripts are runtime execution
  contracts: completion requires a live runtime observation (service reached
  readiness, a check passed while it was alive, and it stayed up). An
  environment probe such as `node -v && npm -v` leaves the task active with the
  runtime gap named instead of closing the tool surface, and an older
  checkpoint whose command-only proof supported a false `PROVEN` now reports
  `COMPLETED_UNPROVEN`.
- `run it` after a runtime or execution task is an execution follow-up, not a
  verification. It continues an open task instead of aborting with an objective
  conflict, and a finished task no longer answers “already proven” instead of
  running again. `is it running?` remains a verification question.
- Service requests now route to execution instead of ASK. `use npx live-server
  test this project`, `serve this project and verify it`, and `start the
  development server and check the page` are execution contracts, and a bare
  “test this project” accepts behavioural HTTP/browser evidence rather than
  requiring a code-test check.
- Execution protocol failures are classified at the stage they occurred:
  `NO_MODEL_OUTPUT`, `EXECUTION_INTENT_WITHOUT_TOOL`, and
  `TOOL_NORMALIZATION_FAILED`. Each gets at most one bounded recovery request;
  prose is never executed, malformed syntax is never dispatched, and a second
  failure ends in a review verdict rather than a retry loop.
- Stale checkpoints no longer present a contradictory runtime state. A
  non-terminal task drops an impossible completion proof, and an older
  informational projection of an explicit service-execution objective is
  re-derived from the same user text before the resumed turn is routed.
- Background jobs are no longer orphaned: session shutdown terminates every
  running job's process group, while normal turns still leave detached jobs
  available to `wait`.

## [v0.1.1] - 2026-11-12

### Added

- Automatic appearance: `internal/tui/design` is now the single token source for
  a Dark and a Light palette, and `theme` is stored as a preference — `auto`,
  `dark`, or `light` — resolved once at startup. `auto` follows the host
  appearance on macOS, Linux, and Windows behind a bounded poll with a dark
  fallback, so a light terminal is legible without per-view overrides.
  `/theme` switches the appearance in place.
- Inline composer hints: the binding suggestion is painted at the input origin
  ahead of the caret while the composer is empty, so the prompt reads as
  embedded instead of displaced. Hints are grouped by interaction (onboarding,
  ASK, BUILD, FUZZ) and advance within the active group on submit and on
  authority change; the first keystroke replaces the hint without touching the
  buffer. The quickstart command grid and its per-project random tip are
  removed in favour of one framed panel with a static tip.

### Fixed

- Split-pane task visibility: on narrow and medium terminals the task area is
  now pinned below the title bar for the whole open task instead of collapsing
  to a single row and releasing when the last todo is marked done. It renders
  the bounded projection block and a spacer, so scrolling moves only the
  transcript below it. The rail now closes on the terminal task transition,
  matching the wide sidebar, rather than on the last completed todo.
- Interrupted BUILD continuation: a resumed task now derives its execution
  stage from durable evidence, so a change applied before the interruption
  stays in post-change verification. The task briefing carries a resume
  directive naming the last observed change, the pending check, and the rule
  against re-planning or repeating completed discovery.
- Runaway continuation: the control plane now stops a run after
  `MaxConsecutiveNoProgress` completed turns that do not change the task
  boundary. During verification only a change or a check counts; elsewhere new
  evidence, a stage/requirement change, or an explicit blocker counts. The
  stop is reported as `NEEDS_REVIEW` with its semantic reason, not as a
  model-turn limit. The model-turn cap remains the last-resort resource bound.
- BUILD implementation continuity: after a change is applied but not yet
  verified, a refinement prompt such as “I want to use green and black” now
  inherits the open mutation contract instead of being rebuilt as a new
  informational task. The task identity, change evidence, and write surface
  continue under BUILD, and the refinement still owes the same post-change
  verification. Explicit mutation prompts keep BUILD and the write surface as
  new objectives. Read questions, investigation requests, and explicit
  no-mutation limits remain new objectives; a no-mutation request that also
  asks to understand offers the ASK boundary.
- Rework follow-ups: `redesign`, `rework`, `revamp`, `restyle`, `redo`,
  `overhaul`, `modernize`, `redraw`, `reimplement`, and `rearchitect` are
  repository-change heads. After a design turn, “redesign it” now derives a
  mutation contract in BUILD, offers the write surface, and cannot be proven
  from another plan or a recorded change artifact without an observed change
  and a later check.
- Task plan isolation: a new task starts with an empty session todo plan. The
  previous task keeps its todo snapshot in its archive, but its completed items
  no longer appear as the new task's progress or get imported into its
  checkpoint.
- Resumed investigation tasks: repeating a task's own objective no longer
  bypasses workspace prediction. A BUILD task handed back with a strong
  investigation request now moves to FUZZ instead of staying in BUILD until a
  new session is started.
- FUZZ scratch execution: a shell redirect to an absolute path outside the
  workspace is execution, not a repository mutation. Starting a local server
  that logs to a temporary file is no longer refused by semantic admission.
- Refused-proposal recovery: a model that stops with no visible text immediately
  after a refusal-only turn now gets one answer-only request from the evidence
  already gathered, rather than ending the task with an empty final message.
- Answer-only silence: when the already-closed answer surface returns only
  reasoning, the loop makes one bounded answer-only retry, with a
  runtime-authored request that no tool is available, instead of ending the run
  with an empty final assistant message after a completed investigation.
- Investigation evidence retention: trimming the durable evidence state now
  evicts transient command diagnostics before content-bearing file reads and
  directory listings. A long FUZZ run no longer loses the observations that
  satisfy `repository_overview` and `repository_survey`, which previously kept
  the answer boundary closed and the investigation reported as insufficient.
- LongCat serialized tool calls: `<longcat_tool_call>` envelopes in assistant
  text are now normalized at the provider boundary in both the JSON and
  key/value spellings. Previously the raw protocol reached the transcript as
  prose and the proposed calls were never authorized or executed.

### Changed

- Composer footer hierarchy: while a turn runs, the measured timer and session
  telemetry now hold the left edge and the queue/cancel/scroll keys pin to the
  right edge in the faint tier. Joined into one sentence, the keys trailed the
  metrics and read as more telemetry, so the footer competed with the
  transcript and left the right half of the row empty.

## [v0.1.0] - 2026-11-09

### Added

- Adaptive execution experience: `internal/experience` extracts deterministic
  incidents from completed turns, derives bounded lessons and candidate path
  preferences, and applies only promoted experience. The projector may reorder
  already-offered tools and append bounded advisory hints; authority, evidence,
  completion, budgets, and task state are unchanged. `/experience` inspects or
  retires records, and `<project>/.ohgo/experience.json` is a versioned,
  bounded, cold-start-safe store.

### Fixed

- Transcript markdown fences and wrapping: an indented fence under a list item
  stays a code block, wrapped bullet continuations are joined before wrapping,
  and an ordered marker is budgeted by its real width. A long continuation
  previously bypassed the wrap pass, reached the terminal as one oversized
  row, and was truncated at the right edge; nested examples also printed their
  fence delimiters literally instead of rendering as code.

## [v0.0.9] - 2026-11-08

### Added

- Hybrid master-prompt offloading: prompts with at least 50 lines or 4000 runes
  are written to `<project>/.ohgo/scratch/prompt_<session>_<timestamp>.md` and
  their bytes are injected directly into the model payload. A durable history
  copy is written first, so Up/Down and Ctrl+P/N still recall the full prompt
  after scratch is removed. Scratch is cleaned on session close/switch, on
  SIGINT/SIGTERM/SIGHUP, and by a 24-hour startup sweep.
- Session protection: `t` in the Session Manager toggles a durable `Protected`
  tag, shown as a star in the status column and as `Protected · yes/no` in the
  details pane. Tagged sessions are never removed by automatic pruning while
  explicit deletion still works.
- Retention controls: `/settings` gains a CLEANUP section with auto-prune,
  keep-recent count, and a manual-clean action. The new `ohgo clean` command
  performs the same protected-aware pruning from the shell.
- Deterministic prompt optimization: `internal/promptopt` compresses oversized
  scratch prompts and sanitizes local session titles without a model call or a
  third-party dependency. Compression removes greetings, politeness, and
  markdown decoration while preserving fenced code, inline code, paths, and
  identifiers; local titles drop conversational noise, respect hard word/rune
  bounds at word boundaries, and never end on a dangling article or preposition.
- Local-first session titling: every session title is derived synchronously from
  the submitted prompt by `promptopt.SanitizeTitle` before any provider request.
  The background ASK-model titler, its timeout and backfill workers, and the
  "generating title…" placeholder are removed. Legacy untitled sessions are
  labeled locally when the Session Manager opens, so titles appear instantly
  with zero token cost.

## [v0.0.8] - 2026-11-05

### Added

- Provider tool-call normalization: assistant text that carries a serialized
  `<invoke>` or `<tool_call>` envelope becomes a canonical tool call before the
  agent loop or transcript sees it. Every provider stream is normalized, native
  calls pass through unchanged, and malformed, incomplete, or oversized
  envelopes stay visible as ordinary text instead of being fabricated or
  silently dropped.
- Contextual task rail: a responsive right-hand rail shows the active task, its
  open todos, active integrations, and task insights only while an execution
  request has open todos. Idle, question-only, and completed or cancelled tasks
  keep the full-width transcript; a sticky row keeps the task anchored when the
  rail does not fit, and every section is row-bounded for steady frame cost.
- `/settings` preferences: the new preferences surface toggles the task rail in
  place and persists it as `taskRail` in `settings.json`, defaulting to enabled.
  It participates in the shared modal, keyboard, wheel, overlay, composer, and
  welcome ownership rules.
- Syntax-highlighted diffs: approvals and expanded tool blocks render tinted
  additions and removals so a mutation can be reviewed without leaving the
  transcript.
- Live reasoning and activity: throttled Braille spinners mark responding and
  live-reasoning phases, and one live activity row carries a measured duration
  plus streaming tool output.

### Changed

- Transcript presentation: tool runs stay contiguous across streamed text,
  markdown paragraph breaks survive, reasoning collapses behind a toggle, paths
  and URLs middle-truncate, and filesystem errors reduce to one-line summaries.
  Reasoning and tool identity survive session resume, and the recap footer comes
  from observed evidence and the answer's finding rather than the tool list.
- Evidence and grounding: pruned, truncated, or timed-out scans are incomplete
  evidence rather than proof of absence; glob output names gitignore-skipped
  paths, passive search failures are detected, and implementation language is
  derived from nested manifests the run actually read.
- Task closure and final answers: an answer-only surface withdraws every tool,
  unproductive turns are bounded, and a failure ledger with provenance
  attribution keeps the final answer truthful. A blocked task is reported as
  review rather than success.
- Provider stream ordering: OpenAI and Ollama keep text emitted after a native
  tool call in its logical position instead of merging it into the trailing
  block.

### Fixed

- Closed-surface answers: post-answer tool calls are dropped instead of
  appearing as refusal cards, while policy refusals remain visible in the
  transcript and session.
- Informational completion: deferred-plan narration ("I will …") no longer
  completes an informational objective; the run needs an answer.
- File-count claims: exclusive counts are checked against the files the run
  actually observed.
- Tool-call leakage: serialized calls inside assistant prose no longer leak into
  visible content or bypass execution; diagnostics name malformed or incomplete
  envelopes rather than guessing.
- Session concurrency: session startup and sequence allocation are hardened for
  concurrent writers.

## [v0.0.6] - 2026-11-03

### Fixed

- A quit now cancels an in-flight background update check: the observer is bound
  to the TUI program or plain-REPL lifetime, so leaving ohgo stops the request
  instead of leaving it running behind a surface that no longer exists.
- The background check and `/update` now share one update service: discovery and
  installation read and write the same cache, so a hint and an explicit update
  cannot disagree about which release is current.
- A cancelled update check is no longer persisted: quitting during a check no
  longer records a failed attempt that suppressed the next eligible check for a
  TTL.

## [v0.0.5] - 2026-11-03

### Added

- Task continuity Phase 1: the durable task now owns structured, role-independent
  context (verified facts, hypotheses, evidence references, questions) and
  isolated ASK/FUZZ/BUILD execution contexts in `progress.json`. Context is
  assembled deterministically for the active role with existing character and
  token bounds; it survives restart, session continuation, and task resume, and
  it never grants authority. Model assertions remain hypotheses until current
  evidence confirms them, and contradictory evidence is retained without
  overwriting facts.
- Task continuity runtime population: material tool observations are folded into
  task evidence references at the turn boundary, successful checks become
  evidence-backed facts, and ASK/FUZZ/BUILD role contexts carry their own
  observed progress. A role change on the same objective keeps the task and its
  contexts instead of detaching it. Semantic hypotheses, questions, reasoning,
  and strategy remain host writes because the runtime has no trustworthy signal
  for them.

- Task continuity Phase 2: a widening role transition is now a controlled
  handoff. `workspace.ProposeHandoff` asks for the authority the request needs;
  the human approves through the existing in-chat transition decision; the
  existing `SwitchWorkspace` seam performs the only authority move. The task
  records a bounded handoff (roles, objective, truthfully measured workspace
  state, and references to the shared facts, hypotheses, questions, evidence,
  and artifacts) in `progress.json`. Applied handoffs keep one task identity
  and all role contexts in both directions; rejected, failed, and interrupted
  handoffs never change the recorded role. A restart reconciles an approved
  handoff against the authority that was actually restored: a matching target is
  recorded as applied without a second switch or a resumed turn, a matching
  source is reported as not applied, and any third role is reported as a
  recovery discrepancy without rewriting the recorded role. A transition's
  source is the live authority, so recovered stale task metadata cannot block a
  valid narrowing, and a checkpoint recovered without `task.md` reconstructs the
  objective from the contract so its handoff stays valid and writable.

- Task continuity Phase 3: progression, lineage, recovery, and evidence-backed
  completion. `progress.json` now carries a bounded timeline of meaningful task
  events (creation, checkpoints, findings, evidence and hypothesis
  dispositions, handoffs, verification, detach/resume, completion, failure)
  that references authoritative records instead of copying payloads, plus an
  evidence proof for COMPLETED tasks. `Task.ObjectiveState` distinguishes
  `ACTIVE`, `INTERRUPTED`, `NEEDS_REVIEW`, `BLOCKED`, `PROVEN`,
  `COMPLETED_UNPROVEN`, and `CANCELLED`. Completion is explicit per contract kind:
  an informational objective needs an answer, an execution objective needs a
  projected successful execution (an answer alone is refused), and a mutation
  objective needs an observed change plus a verification that followed it. A
  proof that is invalidated, superseded, evicted, or materially contradicted
  downgrades to `COMPLETED_UNPROVEN` without rewriting the lifecycle record.
  Load reports whether recovery was intact, reconstructed with named missing
  information, in need of a human decision, or blocked; detached archives carry
  the same classification and corrupt archive files are reported instead of
  silently skipped. `/task status`, `/task show`, `/task timeline`, and
  `/task lineage` expose the same read-only facts through the existing
  interaction surface.

### Changed

- `progress.json` is now schema version 3. The bump is a downgrade barrier, not
  a shape change: a newer binary still reads version 1 and 2 checkpoints, while
  an older binary refuses version 3 instead of silently rewriting it without
  the timeline and completion proof it cannot parse. The projection is written
  before the checkpoint, and the checkpoint write is the commit point.
- The task timeline elides observation events before lifecycle history and
  counts dropped events from the retained sequence numbers rather than assuming
  the first retained event is the creation event. Repeated pause and review
  boundaries are idempotent, and task-context mutations use the injected clock.

### Fixed

- A failed completion checkpoint can no longer be published as
  `COMPLETED`/`PROVEN`: the controller restores the prior task and the TUI
  refuses to surface an uncommitted completion. The same task stays resumable.
- A successful command is projected as an observation, so an execution
  objective can be backed by real execution evidence rather than only by a
  final answer. The durable boundary also carries a check's reported scope and
  refuses a verification that does not cover the contract's declared kind and
  scope.
- Recovery no longer reports `INTACT` after dropping or repairing a completion
  proof, archive sanitization reports its repairs, and resuming an archive
  preserves that classification.
- A contradiction now downgrades `PROVEN` when it targets a claim the proof
  depends on, and leaves an unrelated proof intact.
- Task context selection now uses the fixed budget with relevance ordering
  instead of recency-only caps: older verified facts survive, the active role's
  section is reserved, stale evidence cannot displace current evidence, and an
  overfull section reports how many items it omitted.
- Task-context repair now deduplicates role contexts across the whole
  collection, clears dangling supersession pointers, rewrites invalidated
  confirmations, and refuses to hand back a context that would make every later
  checkpoint fail validation.
- A dedup key can no longer silently reuse an earlier record for a different
  observation, and a fact confirmed from a hypothesis records the confirming
  role while the hypothesis keeps the proposing role.

## [v0.0.4] - 2026-11-02

### Added

- `space` in the full-screen transcript returns to the live edge when the
  composer is idle and the view is scrolled up, so a reader inspecting earlier
  activity can jump back to the latest line with one key. The scrolled-up marker
  advertises it, and a non-empty draft keeps space literal.
- One-line install: `curl -fsSL https://raw.githubusercontent.com/PizenLabs/ohgo/main/install.sh | sh`
  resolves the latest stable release (or a pinned `OHGO_VERSION`), verifies
  the tarball against the published `sha256sums.txt`, smoke-tests the binary,
  and installs into `$HOME/.local/bin` by default. A mismatched checksum or
  version leaves an existing installation untouched.
- `/copy` in both interfaces: the default copies the most valuable part of the
  latest answer (its largest code block, else a unified diff from the answer or
  the turn's mutation tools, else the whole text) without fences; `-a`/`--all`
  keeps the explanation and every code block, and `-c`/`--context` copies the
  conversation context for transfer to another tool. The copy travels over the
  existing OSC 52 transport, and a terminal that cannot accept it is reported
  rather than claiming success.
- Evidence benchmark suite (`go run ./bench/phase9 -suite evidence`): the five
  runtime requirement tasks plus the authority regression matrix, measured with
  the real router, security gate, evidence projector, and completion evaluator.
  It records routed authority, the ASK route decision, withheld exploration,
  duplicate reads, tokens, and answer facts, and runs scripted or against a
  live provider (`-live-model`, including a local Ollama model).
- External-state claim review: an answer that turns local configuration into a
  remote fact ("the workflow ran today", "it was pushed", "the release is
  published") is `NEEDS_REVIEW` unless the run observed the remote surface
  through a knowledge call, an MCP capability, or a command that talked to a
  remote. A sentence that names the claim to report it as unverified is not a
  claim and completes.
- Evidence-aware stop: after a run's first observation turn, once a completed
  turn observes the repository without adding a new fact, the next request for
  an informational objective is offered no tools and the state block explains
  why. The decision is derived from the turn's evidence gain, never from a
  tool-call count, so a model that is still learning is never cut off.
- Questions about this repository's behaviour ("does this workflow run
  automatically?") now declare a `repository_overview` need, satisfied only by a
  content-bearing observation. A listing or an answer from memory is
  `NEEDS_REVIEW`, not `COMPLETED`.

### Fixed

- Authority admission no longer escalates an informational question that
  contains execution or mutation vocabulary. "I want to ask if this
  automatically runs and updates on GitHub, or if I have to run it manually"
  classifies as READ and runs in ASK: a clause inside a question is not a
  request, and the same clause model governs the mutation contract and the
  grounding action extraction.
- "Investigate …", "diagnose …", and "troubleshoot …" now classify as
  INVESTIGATE (FUZZ) instead of falling back to READ.
- Hosting and product names ("GitHub") are no longer extracted as code symbol
  targets, so a request that mentions one no longer demands impossible
  index evidence. "Implement the fix for this failing test" is recognised as a
  behavioural objective whose success condition is the test suite.

## [v0.0.3] - 2026-10-29

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
  embedded in the binary (`internal/update/changelog.json`), acknowledged with Enter
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
  adds a model call. See `docs/architecture/INTENT_GROUNDING.md`; benchmark with
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
- The port plan still listed `todo` as remaining though ported, and
  `notebookEdit` as remaining though it does not exist in the source revision.
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
