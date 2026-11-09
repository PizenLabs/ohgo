# Runtime Architecture

> **Scope:** runtime behaviour — repository intelligence, authority, the turn,
> completion, subagents, concurrency, errors, storage, startup, transports,
> CLI/harness, terminal workspace, credentials, external knowledge, and
> performance.
> **Canonical topology:** [`OHGO_ARCHITECTURE_MAP.md`](../../OHGO_ARCHITECTURE_MAP.md)
> is the single source for packages, domains, ownership, and dependency
> direction.
> **Permanent rules:** [`OHGO_ARCHITECTURE_RULES.md`](../../OHGO_ARCHITECTURE_RULES.md).

This document explains how the current runtime behaves. It does not restate the
package map; when a package or dependency question arises, the map and the
executable governance in `internal/architecture` decide. A dependency cycle or
an upward edge is a bug, not a shortcut.

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
  `artifact` tool reads the live workspace on every call and enforces that a
  workspace only authors its own kind.
- `workspace.DecideTransition` owns the transition policy: stay, automatic
  BUILD ↔ FUZZ cooperation, or a human confirmation when ASK is crossed.
  `harness.SwitchWorkspace` remains the single authority move. `task.Handoff`
  records both human and control-plane transitions and their carried references
  in `progress.json`; it is recovery metadata, never a capability.

### Intent-aware turns

Authority is a runtime property, not a construction-time constant. Each turn
is classified by a deterministic, model-free classifier
(`workspace.Classify` → `READ` / `EXECUTE` / `INVESTIGATE`) and analysed for the
capability it needs (`workspace.AnalyzeRequest` → `READ` / `EXECUTE` /
`INVESTIGATE` / `MUTATE`). `workspace.RouteTurn` is the capability guard and
`workspace.DecideTransition` is the transition policy. The `authorityGate` in
`cmd/ohgo` is the single live authority that both the loop's admission hook
and any subagent read at call time:

- ASK can READ. EXECUTE and MUTATE require a confirmed BUILD switch;
  INVESTIGATE requires a confirmed FUZZ switch.
- BUILD can READ, EXECUTE, and INVESTIGATE. A strong human-facing question
  offers a confirmed ASK switch; a strong investigation moves to FUZZ
  automatically.
- FUZZ can READ, EXECUTE, and INVESTIGATE. A repository-change request moves to
  BUILD automatically. Mutation is still denied inside FUZZ by the per-call
  gate.
- Every automatic transition is limited to BUILD ↔ FUZZ. Crossing ASK is always
  a human decision. Removing or changing the mode prompt does not widen
  authority — the gate denies the call regardless.
- A staged request ("investigate first; if confirmed, implement") starts in
  investigation. Its conditional mutation is retained for the later branch but
  does not make the current turn a mutation objective.
- A staged task also moves phase at the tool boundary. `workspace.ToolCallTransition`
  reads a concrete call — a finding or change artifact, or a mutating tool — and
  applies the same policy before execution. The loop asks after semantic
  grounding, the host applies the target through the single switch owner and
  records it on the same task, and the loop re-authorizes the call under the new
  live gate. A plan artifact under FUZZ would cross ASK and is therefore not
  automatic.

The full matrix, state model, and invariants are in
[`WORKSPACE_AUTHORITY.md`](WORKSPACE_AUTHORITY.md).

Semantic identity (`workspace.Style`) is teal/magenta/blue for ASK/FUZZ/BUILD.
The hue is fixed across themes while its depth adapts for contrast on dark and
light terminals; it never participates in an authority decision.

### Intent grounding

Before a turn executes, `workspace.BuildIntentContract` attaches a
deterministic `contract.IntentGrounding` to the completion contract: action,
named entities, scope, expected outcome, constraints, assumptions, and
ambiguities. It is built from the user's text, never from model output, and it
grants nothing.

The same contract is consumed at three seams:

- `agent.Loop` semantic admission refuses a mutating call when the request did
  not ground a mutation, before the security gate is consulted. A read may
  proceed as bounded observation; it cannot turn into the objective.
- `cognition` projects the entities and ambiguity into the runtime state block
  so the model sees what it must preserve.
- `agent.EvaluateTaskCompletion` calls `AssessGrounding` after structural
  evidence and before `COMPLETED`. Dropped entities, an invented action
  narrative, narrowed scope, unresolved references, unsupported negative
  conclusions, and an unsupported claim scope (configuration promoted to
  behavior, an unmeasured metric, independent deployment, or path-scoped
  speculation) downgrade the result to `NEEDS_REVIEW`.

Task persistence carries the grounding inside the existing `IntentContract`, so
a resumed task keeps the same semantic boundary. `$sup` is deliberately not
automatically invoked: it has no repository evidence, and a second model call
on the common path would add cost without replacing the deterministic checks.
The full contract is in [`INTENT_GROUNDING.md`](INTENT_GROUNDING.md).

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

### Provider stream normalization

`ai.Stream` is the canonical event boundary between a provider's wire
representation and the rest of ohgo. `Registry.StreamFn` is the production
adapter that crosses it: every provider stream is wrapped by
`ai.NormalizeStream` before the agent loop, authority gate, projection,
persistence, or TUI can observe it.

The rule is:

> Provider-specific model output is normalized into canonical `ai` events
> before it enters agent execution or rendering.

Native tool calls and serialized tool calls inside assistant text are two
transport representations of the same logical intent. Both converge on
`ai.ToolCall`. A recognized serialized envelope is converted at the boundary;
it is not executed there. The resulting call still passes through grounding,
authorization, budget, execution, evidence, failure classification, and run
outcome exactly like a native call.

Recognition is an incremental, bounded state machine:

- Opening and closing markers may be split across arbitrary provider chunks;
  an ambiguous suffix is retained, never rendered, until it is resolved.
- Ordinary text continues streaming; only a possible marker prefix is held
  back, so normalization does not buffer the whole answer.
- Recognized protocols live behind a small registry in `provider/ai`. Another
  provider representation is a new descriptor there, never a TUI or execution
  change and never a model-id branch.
- A complete envelope is consumed only when its payload parses into canonical
  tool calls. A recognized envelope that is malformed, incomplete, oversized,
  duplicate, or otherwise ambiguous becomes a bounded diagnostic record, never
  assistant prose: the provider's serialization is not persisted as an answer
  and no call is fabricated. The message is marked as protocol residue, so
  completion reports `NEEDS_REVIEW` instead of success.
- The registry recognizes the provider's observed spellings
  (`<dotsfunctioncall>`, `<dots_function_call>`, and their mixed closing
  markers) as one protocol family. A new spelling is a descriptor there, never
  a TUI or execution change.
- Recognition is family-based, not spelling-list based. An opening tag that
  names the dots/call family but is not a registered descriptor is consumed
  through its family closing tag, replaced by one bounded diagnostic, and
  marked as residue. Because the family prefix and the closing tag are subject
  to the same retained-suffix rule, this holds under arbitrary byte
  fragmentation, including one byte per chunk; an unterminated family opening
  is bounded rather than buffered without limit.
- Markdown quoting is content, not residue: protocol-looking text inside a
  fenced block, an inline code span, or after a backslash escape is preserved
  verbatim and is not executed. Raw provider bytes outside a quote are protocol
  and are never published. The distinction is lexical and deterministic; it
  does not depend on a second model call or on provider identity.
- Text/tool ordering is preserved, including multiple and interleaved calls.

The TUI receives only canonical assistant text and tool events. It does not
parse protocol markers, and `internal/architecture` fails the build if provider
tool syntax appears in production presentation code.

Reasoning is a distinct canonical channel, not prose the renderer mines for a
marker. `EventThinkingDelta` reaches the runtime through its own hook
(`ai.DrainStreamHooks`), becomes its own presentation block, and is collapsed
by default into a one-line badge (`+ Thought · 1.1s`). The badge's `+`/`-`
marker is the disclosure affordance: the shared presentation-only Ctrl+O chord
or a click expands it to `- Thought · 6.5s` and an indented, desaturated body,
bounded so one long trace cannot displace the answer. The completed
`AssistantMessage` keeps the same thinking block, so a resumed transcript
renders it without re-parsing provider output.

## Completion, task state, and exit status

Completion is a state transition, not the model's stop reason. The one
authoritative evaluator is `agent.EvaluateTaskCompletion`; every production
path that reports a task outcome calls it with the contract built from the
user's request before execution and the evidence the runtime observed.

- **Evidence.** A mutation objective needs an observed target change and a
  successful verification after it. An execution authorized read-only is not a
  potential mutation, so an investigation probe cannot create a verification
  debt. A checkless execution objective needs a process that reached a
  successful terminal state (`Executes` + `Finished`); a started background
  spawn is not evidence. An informational objective needs
  non-empty visible assistant text in the final turn. Empty or whitespace text
  is `NEEDS_REVIEW`. A request that explicitly required external knowledge also
  needs a successful external observation; a local answer does not satisfy it.
  A broad repository account keeps documentation claims distinguishable from
  observed implementation facts: a term that appears only in README/docs, never
  in a source or structural observation, must be attributed ("the README
  describes ...") or the verdict is `NEEDS_REVIEW`. It also keeps evidence
  classes distinguishable: a manifest declares a dependency, Compose declares
  topology, and only a source or execution observation establishes behavior.
  A concrete line count needs the read's measurement, an independent-deployment
  conclusion needs a deployment artifact, and a path-scoped inference needs an
  observation of that path. The evaluator does not judge answer relevance beyond
  these deterministic provenance and claim-scope boundaries.
- **TUI task path.** `task.Begin` persists the contract, the pre-run target
  snapshot, and later the review reason. `NEEDS_REVIEW` is non-terminal. A
  continuation reuses the persisted contract and snapshot; a materially
  different prompt is refused until the human runs `/task cancel` or
  `/task accept`. Both close the task as `CANCELLED`; neither claims the old
  objective completed, and the next prompt starts a new identity.
- **Phase completion.** A turn can run under ASK or FUZZ while the persisted
  contract needs BUILD, or under BUILD before a conditional mutation branch is
  entered. `workspace.PhaseContract` and `contract.IntentContract.
  InformationalPhase` narrow that turn to an informational phase; a substantive
  answer leaves the task `RUNNING` (`ACTIVE`) with the same identity and no
  proof. It is not `NEEDS_REVIEW` for a change the authority never permitted or
  the branch has not reached. The projector builds its task shape from that
  phase contract too, so it does not ask the model for evidence the authority
  can never acquire; a phase that still permits execution keeps its execution
  surface until its own evidence boundary. A staged conditional mutation
  defers its literal target-state requirement to the change branch: the
  investigation is not a failed read of a future target. The next turn under
  the required authority continues the same task. See
  [`WORKSPACE_AUTHORITY.md`](WORKSPACE_AUTHORITY.md) and
  [`TASK_CONTINUITY.md`](TASK_CONTINUITY.md).
- **Task context.** The same checkpoint carries structured, role-independent
  context: evidence-backed facts, hypotheses and dispositions, task-linked
  evidence, outstanding questions, and isolated ASK/FUZZ/BUILD execution
  contexts. Context is assembled for the active role only, never grants
  authority, and never completes a task. See [`TASK_CONTINUITY.md`](TASK_CONTINUITY.md).
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

### Evidence sufficiency and the exploration stop

The projector (`control/cognition`) maintains what the runtime knows, not what
the model said. Two properties keep the loop honest and small:

- **Required knowledge is derived from the request.** A question about this
  repository's behaviour ("does this workflow run automatically?") declares a
  `repository_overview` need, satisfied only by a content-bearing observation: a
  file read, a search that returned content, or a structural query. A directory
  listing is structure, not an answer. The final projection's sufficiency
  verdict vetoes completion, so a model that answers without observing the
  repository is `NEEDS_REVIEW`, not `COMPLETED`. A general knowledge question
  declares no such need.
- **Known workspace facts are surfaced before they are re-queried.** The cheap,
  model-free root probe already knows the project's markers, detected
  languages, and root entry count; the runtime state block carries that bounded
  line so the model does not spend a tool call rediscovering its own
  environment. The probe is orientation, never evidence: completion still
  requires an observation.
- **A coordinate and a concept are different claims.** A slash compound is not
  a filesystem path by itself. A token with a file extension, a following path
  noun ("directory", "file"), a direct path verb/preposition, or backticks is a
  literal target and requires `target_state` (plus `target_content` when the
  request asks for contents or inspection). A compound such as
  `order/checkout flow` names a repository concept instead: it declares
  `repository_overview`, not a literal target, because the answer lives in
  whichever implementation files realize the flow. An explicit literal path is
  still satisfied only by that coordinate; a parent listing does not prove an
  inspected directory's contents.
- **Breadth is semantic, not a phrase list.** A repository subject ("this
  project", "the codebase", "the system", including a modified form such as
  "this Go e-commerce project") combined with an
  understanding/overview/architecture concept declares a `repository_survey`
  need: at least two content observations or one content observation plus
  structure. A named coordinate, a specific artifact ("this project's license",
  the test suite, one file), or a named repository concept ("the checkout
  flow") stays targeted even when the sentence contains a broad verb. The
  classification is deterministic and derived from the objective and the
  contract's targets and concept entities, so unusual wording does not fall
  back to the single-observation policy and narrow questions are not forced
  into a repository-wide survey. A repository that has source code also needs
  at least one implementation observation; two documentation artifacts describe
  what the project says about itself, not what its implementation does.
- **A path-scoped mutation already has its coordinate.** When a mutation names
  a concrete path, the path is the edit boundary; completion re-resolves every
  named symbol after the change, so the run does not also demand a separate
  `symbols` lookup before editing. A targetless mutation keeps the coordinate
  requirement.
- **A conceptual mutation branch is bounded by its check.** A mutation that
  names no literal target but does name a repository concept and an explicit
  check ("investigate the checkout flow; if confirmed, fix it and run the
  tests") becomes a behavioural objective. It still requires an observed change
  and a successful check; the concept is a scope, not a synthetic path.
- **Failure is durable evidence, not a transient error string.** Every failed
  execution is recorded under `agent.CallSignature` with its target, failure
  class, and retryability. A class is fatal (`authentication_failure`,
  `not_configured`, `unsupported_operation`, `protocol_error`) when no retry can
  recover it; a retryable class gets one retry; an unclassified or
  non-retryable class is refused on the identical repeat. The ledger is
  persisted in `EvidenceState`, so a resumed turn cannot replay a spent failure.
  A definite workspace change invalidates non-fatal failures, because the same
  command observes a different state after the fix. A refusal is a control
  decision: nothing executes and completion does not count it as a failed call.
  This includes a tool's own authority refusal, such as the artifact tool
  rejecting a kind the live workspace cannot produce.
- **Closure is one semantic state, not an empty tool slice.** A projection's
  `AnswerEligible` or `Terminal` verdict is the control plane's declaration that
  the action surface is closed. The loop owns the request shape, so it withdraws
  every tool itself; a projector cannot leave a callable tool on a surface it
  has already closed. A blocked capability (`BlockCapabilityFailure`) is
  terminal: the first stale proposal without an answer reports
  `agent.ErrTaskBlocked`, which classifies as `NEEDS_REVIEW`.
- **An unproductive turn is counted from outcomes, never from the offered
  slice.** A refusal is a control decision, not work. If every call in a turn was
  refused and the model produced no answer, the turn could not change the
  runtime's state; this is true even when the projection still offered other
  tools. One recovery request is allowed. A second consecutive unproductive turn
  reports `agent.ErrTaskBlocked` before another model request, so a model that
  keeps proposing a withdrawn capability cannot walk the turn grant. A turn with
  any non-refused outcome is work and resets the count. A refused proposal
  accompanied by visible text is the candidate answer, and the run stops there:
  the refusal leaves no outstanding work. The presentation layer publishes the
  terminal verdict after the turn's outcomes and text, so the transcript ends at
  the decision instead of appearing to execute work after it.
- **Required external knowledge is real evidence.** An explicit external
  requirement declares an `external_observation` need, so local files cannot
  make the task answer-eligible. While it is the only missing requirement, the
  projector offers only external-capable tools; discovery is not an observation.
  If the constructed action space contains no external capability, the
  projection is terminally blocked and names that gap. Once the external
  capability produces a successful observation, normal sufficiency and the
  answer boundary apply.
- **The stop is evidence-driven, not a call budget.** The action surface closes
  when sufficiency reaches `answer` or `complete`, or when a required
  capability is blocked; those are the states in which another call cannot
  change the conclusion. A completed turn that observed the repository but
  added no new fact does not close the surface while the shape still declares
  missing evidence: the projector keeps the capabilities the current authority
  permits and appends a bounded escalation reason plus the existing `Next
  evidence` guidance, so the next request changes the coordinate or capability
  instead of repeating it. A turn that learned something — including a
  different window of the same file — keeps the full surface. Errors, refusals,
  and generic tool output are control signals, so a model that is correcting
  itself is never mistaken for one that is repeating itself. A deferred phase
  that still needs execution its authority permits is not at the answer
  boundary either; its next step may be a probe or a command. Change objectives
  are exempt from the informational boundary: their next action is the change,
  not another observation.

Budgets remain safety bounds, never the reason a run concludes. A weaker model
may therefore spend more turns reaching the same evidence; the stop follows the
evidence, not the model.

## Execution experience (AES)

The runtime may learn **execution strategy** from completed turns. It may never
learn authority, evidence requirements, or completion. The learning layer is
`internal/experience`; its full contract is
[`ADAPTIVE_EXPERIENCE.md`](ADAPTIVE_EXPERIENCE.md).

The turn boundary supplies a structured observation: completed `agent.Turn`s,
the completion contract, the last `agent.ProjectionReport`, the durable
`cognition.EvidenceState`, the workspace label, the prior task status/review and
the evaluator's outcome. No transcript is replayed. A deterministic extractor
recognizes repeated probes, recoverable and material failures, successful
low-cost and expensive paths, evidence gaps, stale review boundaries, handoff
redundancy, authority refusals, and context growth.

An incident becomes a lesson and a candidate, not a policy. A candidate becomes
`Validated` after repeated matching success (or an explicit regression check)
and only then `Promoted`; contradiction can reject it or retire an active
record. Records are scoped to task, project, runtime, or global. The project
store is `<project>/.ohgo/experience.json`; it is versioned, bounded,
temp-then-rename atomic, and safe to delete. Project experience becomes global
only through an explicit `PromoteToGlobal` after three independent observations,
zero contradictions, and a passed regression check.

Application is a narrow advisory seam. `cognition.Projector.Advisor` may move
tool names already in the offered surface earlier and append at most three
bounded hints to the runtime state block. It cannot add or remove a tool, grant
a capability, bypass the security gate, change evidence/sufficiency, or mark a
task complete. The model still chooses the action; the control plane still
decides; the safety boundary is unchanged. `/experience` is a read-only
inspection surface except for explicit human `retire`/`reject` commands.

Cold start is a no-op: an empty store and a nil advisor preserve the previous
behaviour exactly.

## Subagents

Delegation is the one capability that creates another agent, so isolation
is structural, not advisory (`internal/agent/subagent.go`,
`internal/execution/tools/task.go`):

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
extensions. An assistant turn's content is persisted as **ordered blocks**
(text, thinking, tool call) rather than a flattened text-plus-calls pair: the
order is what makes a resumed transcript render the same sequence the live
turn did, and reasoning survives the round trip. Legacy rows without ordered
blocks still replay through their stored text and calls. UI-only messages are
dropped at write time. Writes use one
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
  complete, whether the directory is a Git worktree, whether `.gitignore` exists
  and already ignores `.ohgo/`, and a `State` of `Uninitialized` or
  `Initialized` (`Invalid` distinguishes a broken `.ohgo/` from a missing one).
  A valid-but-incomplete `project.yml` — an id/root-only file from an earlier
  build — is `Uninitialized`: the gate owns completing it.
- `startup.Ensure` fast-paths when `State == Initialized`; otherwise it enters
  `Initializing`, prompts for the Git decision, the `.ohgo/` ignore decision
  (skipped when already satisfied), the project worker name, and the project
  name, and only then commits: `git init -b main` (if confirmed), the
  confirmed `.gitignore` rule, and the project-local metadata to
  `<root>/.ohgo/project.yml` through `project.EnsureWith` (atomic, id
  preserving). Decisions come first so a cancel (`ErrCanceled`) leaves the
  directory exactly as it was found. A new repository therefore starts on
  `main`; an existing repository keeps its branch, and neither discovery nor
  the fast path rewrites `.gitignore`.
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
`/status`, `/update` (the same `update.Service` authority as `ohgo update`),
`/doctor`, `/connect` (a Provider Manager modal browses the
credential roster, an API-key modal collects the credential — pasted or
detected — then a wizard: validated model list → saved key + settings),
`/disconnect`
(file/explicit keys only; env/shell keys say where to unset), `/experience`
(inspect or retire verified execution experience). Presentation
differs; decisions do not. Commands parse before anything reaches the
model; a bare `/` is a path. `$sup <question>` opens the ephemeral support
surface (a SUP-role interaction that is never persisted and never enters the
main history); a bare `$sup` opens it with input focus and starts no
provider call, and ESC closes it. The `$sup` trigger and the `Ctrl+S` chord
are the support surface's only entries. Prompt history (`~/.ohgo/history` plus
the project scope, bounded, commands excluded, `OHGO_HISTORY_FILE` overrides,
empty disables) is the durable recall source. It is written and flushed before
an oversized prompt is copied to `.ohgo/scratch`, so Up/Down and Ctrl+P/N keep
returning the full text after scratch is removed.

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

- `internal/tui/model.go`: the state machine — composer, viewport (scroll + sticky
  follow), queue, overlays, sessions (Session Manager) and providers (Connect
  Provider Manager and its API-key modal) plus the models/jobs/theme pickers and
  the two-tab Command & Help Center (Commands / Shortcuts), workspace,
  probe/doctor/usage wiring. Slash
  discovery state filters the authoritative
  `Commands` registry inline — never a second catalog.
- `internal/tui/composer.go`: multiline input (alt+enter / ctrl+j), cursor editing,
  prompt history with draft preservation. Up/down move the caret and only cross
  into history at the prompt's first/last display row; alt+up/down and
  ctrl+p/ctrl+n browse history from any row. The composer is capped at six
  display rows and follows the caret, so recalled or pasted multiline text
  never expands into a full-screen editor.
- `internal/tui/entries.go`: transcript entries rebuilt from replayed messages in
  canonical content order (text, reasoning, tool cards), with tool results
  paired by call id, orphans kept, and UI-only messages skipped. Stored
  assistant content is re-canonicalized here, at the history boundary, so a
  session written before stream normalization cannot leak provider protocol
  into the viewport; the renderer never sees that syntax.
- `internal/tui/markdown.go`: dependency-free chat prose (fences, code, headings with
  hierarchy, nested/ordered lists, quotes, rules, links, tables with
  terminal-width-aware columns and a stacked narrow fallback). One wrap
  ownership: plain cells are measured and folded first, ANSI applied after,
  so styling never inflates the budget and raw model ANSI is consumed at the
  boundary. Partial streaming constructs render provisionally (unclosed
  markers stay literal) and converge on completion; rendering is a pure
  function of the accumulated text, so chunk splits cannot diverge it.
- `internal/tui/sanitize.go`: the trust boundary for everything else. Tool results,
  prompts, notices, composer input, and chrome fields pass here: SGR colors
  survive, cursor-movement CSI, OSC hyperlinks, carriage returns (folded to
  newlines, or spaces inline), and stray controls do not. A cursor-aware
  variant keeps composer cursor mapping exact across sanitization.
- `internal/tui/mode.go`: terminal mode control — alternate scroll (DEC 1007) and the
  mouse-mode policy. Mouse reporting is **off by default** because requesting it
  makes the terminal stop doing its own selection. The default route to the
  wheel is 1007, which multiplexers do not forward (measured against tmux 3.7c:
  a pane using 1007 alone receives nothing), so `MouseDefaultFor` selects
  reporting when `$TMUX` is set. `--mouse select|report` and the `mouse` setting
  override it; anything unrecognised degrades to `select` so a typo never costs
  drag-copy. `select` leaves drag-copy with the terminal; `report` activates the
  application-owned hit-test, expansion, and logical selection path. Both modes
  are bracketed on entry and restored on every exit path. Bubble Tea decodes a
  whole SGR report, but a report split across reads can degrade to an esc plus
  a printable `[<Cb;Cx;CyM` tail, to an `Alt+[` followed by the remainder, or to
  several reports concatenated into one rune batch; `terminal_input.go` is an
  explicit `Normal → Escape → CSI → SGRMouse → Complete` recogniser at the input
  boundary. It consumes any number of reports, retains a partial one until its
  terminator arrives, and never returns protocol bytes as composer text. A lone
  ESC is deferred for one frame while reporting is on so a fragmented report can
  claim it; a real Escape still dispatches through the semantic key path.
- `internal/tui/document.go`: the canonical transcript block model. `Entry` is the
  domain block; `BlockID` is allocated when a block enters the document and is
  never derived from a screen row. Tools and reasoning share the same
  presentation-only expansion transition, selected by Ctrl+O or a click.
  `BlockCopyText` projects logical content, stripping SGR/OSC and padding.
- `internal/tui/transcript_layout.go`: the geometry authority. Each block is projected
  once per revision and width into cached visual rows; a Fenwick tree maps
  document rows to blocks in O(log n). Width/theme/tier changes reflow all
  blocks; streaming and expansion render only the dirty block. Regex never
  determines row count, wrapping, or hit-testing.
- Frame coalescing: one `Update` applies the event it received and every event
  already queued before the renderer paints, so a burst of stream deltas, tool
  progress, and metrics becomes one state transition and one layout pass. Bubble
  Tea's renderer then paints at its own 60 Hz cadence from the latest `View`, so
  runtime event rate and presentation frame rate are independent. The bound only
  prevents one producer from monopolising a frame; no event is dropped and the
  next event resumes the same path.
- Activity grouping: adjacent completed tool calls collapse into one
  hierarchical telemetry footer
  (`+ [Phase: BUILD → FUZZ] · N tools · Xms`, Ctrl+O to expand). The phase is
  the measured workspace-authority span captured when the run opens and when
  its last member finishes; an unknown workspace omits the segment. The
  expanded tree uses `├─`/`└─` branches, and each node carries its own status
  glyph, invocation, measured duration, and compact outcome. The macro tree and
  an individual node body are separate presentation flags: clicking a node
  toggles only its bounded detail, while Ctrl+O still toggles the batch. A
  running member, or a trailing batch in a turn that may still grow, keeps
  individual rows. Every tool row reserves an invariant one-cell status slot,
  so the shared spinner can become a check in place without shifting the
  invocation. The footer and each live block use presentation-only dwell/fade
  floors: a fast call holds its executing frame for 150ms, a newly spawned
  block eases through a 120ms three-phase RGB entrance, and a terminal frame
  interpolates its status colour into the success/error role over the same
  120ms window. None of these phases changes semantic status, timing, ordering,
  or runtime scheduling. Reasoning remains its own accordion block; a tool
  batch never hides the provider's thinking channel behind a footer.
  `ProjectActivity` remains
  the single owner decision: a running tool or open reasoning trace presents
  activity through its own entry. The full-screen canvas reserves no separate
  activity band, so starting, handing off, and finishing a turn cannot move the
  composer; the stable hint/status row carries telemetry and keys. Output-first
  mode may show the one-row activity line in its footer only when no entry owns
  the phase. The active runtime phase is absent from the header, and the
  workspace authority (`ASK`, `BUILD`, `FUZZ`) appears only at the prompt
  boundary. A tool finalizes into that same block, so no live copy survives
  next to its stable representation.
- Non-blocking composer: typing, editing, history recall, and slash dispatch
  stay live while a turn streams. A normal prompt entered during execution is
  queued; commands (`/status`, `/usage`, `/theme`, `/cancel`, …) run
  immediately. `/cancel` reaches the foreground operation in the same
  precedence as Ctrl+C, so a background turn never locks the prompt.
- Guarded Escape: during a running turn the first `Esc` arms a two-second
  confirmation and only the second `Esc` inside that window interrupts, using
  the same cancellation path as Ctrl+C and `/cancel`. The warning owns the
  hint/status row in the warning role, and expiry clears it silently. Ctrl+C
  and `/cancel` remain immediate single-step pathways because they are
  deliberate control chords rather than a reflex.
- `internal/tui/viewport.go`: explicit FOLLOWING/DETACHED state. Detached mode anchors on
  `(BlockID, row)`, so appended output, expansion above the window, and resize
  preserve the reading position. The marker consumes a row and `MaxOffset` is
  clamped exactly.
- `internal/tui/selection.go`: logical selection by block/row/column, cell-aware copy
  (Unicode, wrapping, cross-block, reversed drags), and ANSI-aware reverse-video
  highlighting. Copy projects plain text; no renderer artifact can enter it.
- `internal/tui/clipboard.go`: OSC 52 framing (plain, tmux DCS passthrough, screen
  chunking), Base64 encoding, and capability gating. No external clipboard
  process is spawned. Both the full-screen UI and the plain REPL resolve the
  transport through `ClipboardForEnv`, so the same terminal writes the same
  bytes in either interface.
- `internal/tui/copycommand.go`: `/copy` extraction. The smart default selects
  the latest answer's largest fenced body, a unified diff from the answer or the
  turn's mutation tools, or the whole text in that order; `-a` keeps the answer
  verbatim and `-c` renders the conversation context. The extraction functions
  are pure and never touch the transport, so a failed copy cannot look like a
  success.
- `internal/tui/mouse_selection.go`: screen cell → visible frame row → `BlockID` +
  local row + column. Click toggles presentation state; drag updates the
  selection; release copies through OSC 52. Never maps a mouse Y to a
  scrollback line.
- `internal/tui/width.go`: cell-width measurement, truncation, wrapping
  (ANSI-stripped, CJK/emoji-aware, hard-breaks unbroken runs, combining
  marks stay attached).
- `internal/tui/layout.go`: responsive tiers (wide ≥100, medium 70–99, narrow <70,
  mini <40×8), content width, conversation height, bounded composer (two
  framing rules + input rows + slash suggestion rows + state line, all
  counted so reserved and rendered rows agree). Suggestion rows carry a
  viewport budget (`Layout.SlashLines`): on short screens they yield first
  so the transcript keeps a row. An open picker releases the composer rows
  it hides, and the picker area fills them instead.
- `internal/tui/style.go`: two palettes (dark/light) behind one resolver plus
  semantic chrome (typo degrades to dark; text plus color, never color
  alone).
- `internal/tui/view.go`: adaptive header/conversation/composer/status, bounded
  approval preview (cap derived from the computed chrome, not a constant;
  the separator blank yields on viewports too short to hold it),
  pickers, mini-mode notice, overlap-free slicing. The transcript and the
  picker each emit exactly their viewport rows — entry separators live
  inside the budget, and a zero-row transcript yields to modal pressure on
  tiny screens rather than overflowing. The interactive transcript is a
  projection of `TranscriptLayout` and `Viewport`; ordinary scroll, drag, and
  streaming frames slice cached rows instead of re-rendering the document.
  The composer is two neutral border rules around the input with an inline
  slash-suggestion list and a queued-prompts line. Only the lowercase mode word
  before the `›` pointer carries authority hue; the frame stays on the adaptive
  border token. Activity, token telemetry, and the hint bar sit below the
  composer, never on the transcript edge above the prompt. The upper rule is a
  border block header naming the live session
  (`──────────── [Fix failing parser] ───`), with a discrete `★` when
  the session is retention-protected; the title is `promptopt`-sanitized and
  width-bounded so it can never break the frame. `?` on an empty composer opens the
  Command & Help Center from the registry; while composing it stays literal. Overlays take
  over (transcript + composer hidden) so streaming output never fights a
  moving cursor.
- `internal/tui/commands.go`: the shared `/` registry (`ParseCommand`, `HelpText`,
  typo suggestions); both UIs dispatch through it.
- `internal/tui/picker.go`: the generic list state machine (query + cursor,
  highlight, scroll, window rows from `WindowSizeMsg`; whitespace-AND
  search with `provider:` pins and verified-price `free` matching;
  value-preserving `SetItems` under a generation guard). The model picker
  resolves native `provider/id` through the catalog with caller-built
  detail (context/price/local/caps).
- Catalog freshness (`internal/provider/catalog/dynamic.go`, `snapshot`): `SnapshotAll`
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

## External knowledge

External knowledge is a capability family, not an escape from the control
plane. Search, MCP, and Browser specialize one boundary:

```text
CapabilityRequest → admission → root budget → bounded execution
                  → provenance/evidence → verification → completion
```

- `knowledge` owns the shared vocabulary: capability/operation classes,
  mandatory provenance, evidence kinds, explicit bounds, and truthful failure
  classes. It is data only; it never authorizes or completes.
- **Search** (`search`, `search`) uses configured Brave/Tavily providers over
  HTTPS. Query, result page, snippet, response bytes, timeout, and retries are
  bounded; retries are separately charged calls.
- **MCP** (`mcp`) is a bounded stdio or streamable-HTTP JSON-RPC client. A
  configured server is an external actor: discovery describes capabilities but
  grants nothing, the classification map is authoritative, missing/typo classes
  are `unknown` and fail closed, and every capability carries the stable
  `<server>:<capability>` identity. Tool arguments are schema- and
  structure-checked, responses are byte/item/text bounded, discovery is cached
  with an explicit refresh, and cancellation kills the process. See
  [`MCP.md`](MCP.md).
- **Browser** (`internal/execution/tools/browser.go`) is GET-only read/navigation. Sessions are
  named and explicit, with an exact host policy, navigation/redirect/byte/
  char/link/session limits, a deadline, and a bounded history. Loopback,
  private, link-local, and metadata addresses are blocked; HTTPS-to-HTTP
  redirects are refused. There is no JavaScript, form, click, or mutation
  surface.
- **Budget** adds `external_calls` and `external_bytes` to the existing
  root-owned grant. Per-request caps bound one response; the root grant bounds
  the tree. A refused admission consumes nothing.
- **Authority** adds distinct capabilities: `external.search`,
  `external.mcp.read`, `external.mcp.mutate`, `external.browser.read`, and a
  reserved `external.browser.mutate`. External reads are medium risk and
  approved; external mutation is high risk, requires the strongest workspace
  authority, and is recorded as `ExternalMutation` with `Mutates=false` so it
  cannot become repository completion evidence.
- **Evidence** is untrusted and clearly delimited: every content line is
  prefixed inside an `[EXTERNAL KNOWLEDGE — UNTRUSTED DATA]` envelope. A
  search hit, MCP response, or page never sets `Mutates`, `Executes`, or
  `Verifies`. Cancellation publishes no evidence; truncation is explicit with
  a reason.
- **Credentials** never enter model context: search stores an environment
  variable name, MCP passes only named variables and logs none, and browser
  results exclude cookies, headers, and URL credentials.

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
`install.sh` or `make install` (`~/.local/bin` by default). The complete
versioned-document and upgrade-verification checklist is
[`docs/RELEASING.md`](../RELEASING.md).
