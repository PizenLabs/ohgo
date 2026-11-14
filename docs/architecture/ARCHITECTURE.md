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

`symbols`, `graph`, `refs`, and `structure` are four read-only surfaces over one
`tools.StructuralSource` and one `ast.Index`. One parse pass per supported file
produces canonical declarations, package/namespace facts, imports, references,
calls, explicit inheritance/implementation clauses, test declarations, and
entrypoints; Go keeps its native `go/ast` backend and every other configured
language uses a pinned tree-sitter adapter. There is no second parser, index,
daemon, graph database, or evidence path.

- `symbols` returns exact declaration coordinates across Go, TypeScript/TSX,
  JavaScript/JSX, Java, C#, Rust, Kotlin, Ruby, C, and C++. A language filter is
  a query convenience; the per-file language and native kind stay explicit.
- `graph` remains the Go package/import graph. It reports the snapshot's
  language coverage and points a non-Go selector at `structure`/`refs` instead
  of pretending the Go package graph covers another language.
- `refs` returns Go package-scope references and statically provable direct
  callers/callees, plus name-resolved non-Go call sites only when the target is
  unique. Method dispatch, function values, dot imports, reflection, unresolved
  names, and ambiguous names are counted as unknown or returned as
  `unsupported`; they are never guessed. Callers/callees traversal is bounded
  by depth, node, edge, and work limits, is cycle-safe, and is
  cancellation-aware.
- `structure` returns bounded project inventory — languages and coverage,
  files, directories/packages, configs, test declarations, entrypoints,
  explicit relations, and an impact composition of references, callers,
  importers, and tests. Its `overview` and `languages` views are metadata-first;
  targeted/impact views parse the pending supported files on demand. Semantic
  activation is reserved and fails explicitly because no semantic resolver is
  configured.
- `structure` also exposes the adaptive analysis layer as two views:
  `analysis` (intent-aware plan, multi-resolution summary, architecture and
  boundary evidence, coverage, issues, cache outcome) and `expand` (the direct
  children of one node). The engine behind them is
  `intelligence/structural`; the capability only renders what the planner
  selected. A project intent stays metadata-first, while architecture,
  locate, flow, impact, and isolation raise the activation floor and require
  edges or parsed declarations. Evidence states (`OBSERVED`,
  `PARTIALLY_OBSERVED`, `INFERRED`, `VERIFIED`, `UNKNOWN`) travel with the
  result; no analysis can promote an inferred fact or a path convention into
  proof. Planner tables, strategy versions, cache invalidation, and failure
  semantics are canonical in
  [`STRUCTURAL_INTELLIGENCE.md`](STRUCTURAL_INTELLIGENCE.md).

The index is an incremental in-process snapshot, not a stateless per-file
searcher. A normal refresh stats the tree, parses only added or changed source
files, retains raw relationship syntax so unchanged files are re-resolved
without a second parse, and republishes atomically with a monotonic generation,
deterministic snapshot id, explicit `CURRENT`/`UPDATING`/`STALE`/`INVALID`
freshness, parse-failure count, per-language coverage, structural completeness,
and the last update's resolution scope (`inventory_only`,
`targeted_parse`, `global_reresolve`, or `full_snapshot`).

Production uses adaptive activation: inventory and language mix are available
immediately, source parsing happens only when a targeted or impact query needs
it, and a broad project question does not build a full cross-reference graph.
The Control Plane derives `NONE` / `OVERVIEW` / `TARGETED` / `IMPACT` from the
existing task shape and withholds structural tools at `NONE`; the runtime
enforces the query's own activation and bounds. `.gitignore` membership comes
through an injected `Scope` seam: `intelligence/ast` stays dependency-free and
`intelligence/ignore` remains the only matcher. The index root remains the
working directory; the nearest `go.mod` above it is recorded as metadata and
never silently widens the walk. The snapshot is in memory only;
`<project>/.ohgo/graph.db` stays a reserved, deferred location.

Structural scope is project-boundary-aware. A directory carrying its own
`.git` entry is a nested repository. Structural discovery includes that
boundary even when the enclosing repository's `.gitignore` excludes the
directory, and evaluates the nested repository's own ignore files for its
contents; otherwise the common root rule `backend/` makes a whole nested Go
project disappear from every overview, package, test, and impact answer. An
included nested repository contributes to the one shared snapshot and is not a
second index; its boundary names the parent scope rule it overrides. Ordinary excluded directories remain out of scope, but are
published as `excluded_roots` with the decisive rule and shallow build markers,
and the `scope` view explains them without recursively walking or parsing their
contents.

Completeness has four independent axes: `scope` (every source-bearing path
inspected or explicitly named as excluded, including file-level ignore rules),
`inventory` (every in-scope path readable), `structural` (every supported source
parsed, with unsupported source disclosed), and the query's own `result`
truncation. A `CURRENT` snapshot can be structurally or
scope-partial. A zero file or test count is not absence unless scope,
inventory, and structural completeness all hold and the result was not
truncated; a complete `structure tests` inventory is accepted as the
structural equivalent of a complete Go file scan for the narrow Go test-file
claim. These axes govern the answer, not only the tool result: an overview's
zero test count remains a metadata fact, and only a complete filtered test
inventory can support a test-file absence claim.

The answer side classifies each observation by what it can establish:
documentation, configuration/manifest, inventory, source path, source content,
source import, source use, structural evidence, or runtime. A Compose file,
Makefile, dependency manifest, README, directory listing, glob result, or
structural inventory never counts as implementation content; only source
content does, and only a live runtime observation proves execution. A source
path is not source content, an import is not a use, and a constructed client is
not an operation. A broad project account therefore cannot complete from
documentation plus metadata, and the pre-answer runtime state block reports the
classes the run actually supports, plus an actionable evidence frontier.

The claim classes are explicit: project identity, topology, configuration,
implementation status, dependency use, behavior, runtime state, structural
relationships, absence, and test inventory. `cognition.ClaimSummary.Coverage`
projects each class as `SUPPORTED`, `PARTIAL`, or `NOT_OBSERVED` from the same
ledger the answer audit reads; there is no second evidence store or planner
database. A runtime-state claim ("the service is running") needs a live runtime
observation, not configuration or source. A concrete test count needs a
complete current unscoped test inventory, not a README or metadata overview. A
structural relationship needs an observation that binds both endpoints.
Recovery is ordered as recovery → evidence update → answer audit → completion:
a corrected call may add evidence while the earlier request rejection remains
diagnostic history, and recovery never promotes a task to `PROVEN` by itself.

Component identity is derived from the path's project structure, never from an
arbitrary substring: a read of `product-service` cannot support a claim about
`order-service`, and a file called `api.go` inside `cart/` is not the API
gateway. Strong completeness wording ("fully implemented", "feature-complete",
"production-ready") additionally requires an observed, current, complete source
scope for the named component; one isolated file only supports a claim about
what that file implements. A capability list inside a status claim ("with CRUD,
Redis caching, graceful shutdown, and structured logging") additionally needs
source evidence for each asserted capability, and a mock/scaffold claim needs
the marker in the component's source. Dependency claims follow a separate
ladder: `DECLARED`, `IMPORTED`, `INITIALIZED`, `USED_IN_SOURCE`, and
`OBSERVED_AT_RUNTIME`. The completion audit applies the same boundaries, so an
unsupported runtime claim, an unobserved test count, an unbound structural
relationship, an absence claim without complete coverage, a concept absence
without a scoped proof, a documentation/configuration claim presented as
implementation, or a dependency declaration presented as use is
`NEEDS_REVIEW`. A behavior claim that names a port must find that port in a
source observation for the named component, or in the command/address of a live
runtime observation; reading one service's listener does not prove another
service's port.

The Go backend is always available. The tree-sitter adapters are compiled only
with `cgo`; a no-cgo build keeps the Go path, reports other supported extensions
as unsupported, and never substitutes lexical exploration for structural
coverage. All four tools share the same freshness check, workspace path
containment, authority admission, tool-call budget, and child tool set. Their
evidence is advisory: it cannot mutate, execute, grant authority, or complete a
task. Structural results enter `cognition.EvidenceState` as structural
observations with snapshot, language, adapter, resolution, structural
completeness, project-scope completeness, and truncation provenance; test
associations are static, never coverage, and impact is static reachability,
never a behavioural claim. The per-language capability
and resolution table is
[`STRUCTURAL_LANGUAGES.md`](STRUCTURAL_LANGUAGES.md).

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
  mutation (a redirect into the repository, a mutating program, a mutating git
  subcommand) so FUZZ cannot use the shell as a back door. A redirect to an
  absolute path outside the workspace is execution, not a source change: a local
  server may log to a temporary file without becoming a mutation.
- The execution plane enforces a deterministic workspace write boundary for
  shell commands independently of the model's spelling. A target the command
  itself names (a redirect, `rm`, `cp`/`mv` destination, `mkdir`, `chmod`,
  `sed -i`, `tee`, `dd of=`) is resolved against the command's tracked `cd`
  directory, including `..` and symlinks, and is refused before the process
  starts when it lands outside both the workspace and the operating system's
  temporary area. Temporary staging (`/tmp/nextapp`) remains execution, and a
  later copy into the workspace is the attributable mutation. This is a
  boundary for named targets, not a sandbox for arbitrary programs: command
  substitution, globs, and an interpreter whose own arguments are opaque remain
  residual risks. BUILD intentionally permits ordinary tool caches (npm, Go,
  cargo) to write outside the workspace; the journal diff attributes only the
  workspace changes those commands produce.
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
  reads a concrete call — a finding or change artifact, or a call that mutates
  this invocation — and applies the same policy before execution. The loop asks
  after semantic grounding, the host applies the target through the single
  switch owner and records it on the same task, and the loop re-authorizes the
  call under the new live gate. A plan artifact under FUZZ would cross ASK and is
  therefore not automatic.

The full matrix, state model, and invariants are in
[`WORKSPACE_AUTHORITY.md`](WORKSPACE_AUTHORITY.md).

Semantic identity (`workspace.Style`) is teal/violet/blue for ASK/FUZZ/BUILD.
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
- `cognition` projects the entities, ambiguity, and the evidence-derived claim
  classes into the runtime state block so the model sees what it must preserve
  and which observations support which claim before it writes.
- `agent.EvaluateTaskCompletion` calls `AssessGrounding` after structural
  evidence and before `COMPLETED`. Dropped entities, an invented action
  narrative, narrowed scope, unresolved references, unsupported negative
  conclusions, and an unsupported claim scope (configuration or documentation
  promoted to implementation, behavior, or runtime state; a documentation-only
  implementation status; an unobserved capability, test count, or structural
  relationship; an unmeasured metric; independent deployment; or path-scoped
  speculation) downgrade the result to `NEEDS_REVIEW`. Recovery only updates
  evidence; it never bypasses this audit or the completion evaluator.

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
   constant. Compaction runs first when the *projected* request (messages,
   system blocks, tool definitions, durable projection, expected output, and a
   safety margin) crosses the policy band.
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
Compaction itself is injected, never constructed in the loop. `agent.CompactWith`
prefers deterministic trimming and a state-first rebuild from
`agent.ContextProjection`; the semantic summarizer is the fallback, and `New`
refuses an enabled compaction with neither seam. The projection is supplied by
the control/continuity plane (`cognition.CompactionProjection`), validated
before use, and never includes model prose. Replaced tool output is addressable
through the `recall` tool. The full contract is in
[`TASK_CONTINUITY.md`](TASK_CONTINUITY.md) §5.1.

### Capability preflight

Before a turn is admitted, the control plane compares what the objective needs
against what this runtime can actually do:

```text
contract.RequiredCapabilities     read, and the operations the objective needs
agent.AvailableOperations(tools)  what the constructed tool set can perform
workspace.Workspace.Permits       what the live authority allows
```

`workspace.PreflightTurn` is the host-side verdict and
`cognition.CapabilityGap` the per-request one. Both are deterministic: no model
call, no repository probe, no second planner. A missing *mandatory* capability
(mutation, command execution) stops the turn before the provider is called and
is reported as `CAPABILITY_UNAVAILABLE` with a BLOCKED task — never as a model
failure, a verification failure, or a review. Reading is required but not
mandatory: an observation-free answer is already a review verdict the evidence
layer reports.

The contract's requirement is minimal on purpose: a documentation change does
not require command execution unless the contract itself requires a check, and
an undirected change is satisfied by either a create or an edit. Capabilities
are never derived from the workspace name (`BUILD implies write` is exactly the
inference this removes), and never from model output.

A capability answer is about the runtime, not the environment. `exec` being
available says the runtime can run commands; it says nothing about whether
`node`, `npm`, or a network exists. Environment facts stay the objective's own
work, discovered through the capability when the objective actually needs them,
and are not part of the preflight: probing every environment before every turn
would spend the tool calls the preflight exists to save.

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
- Every failed decision also becomes a structured
  `ToolNormalizationFailure` on the assembled message: `INCOMPLETE`, `INVALID`,
  or `UNSUPPORTED`, with the provider, protocol, a sanitized reason, a bounded
  raw fragment for audit, and recoverability. The raw fragment is never executed
  or shown as assistant text. The loop classifies a run whose envelope never
  resolves as `TOOL_NORMALIZATION_FAILED`, after at most one bounded recovery
  request that asks the model to re-emit the operation canonically. Malformed
  syntax therefore cannot reach authorization or execution, and the retry
  cannot become a loop.
- The model announcing an action in prose is a separate lifecycle state. If a
  turn contains an execution promise and no tool call, the loop spends one
  bounded recovery request asking for the actual call. If the next turn still
  has no call, it reports `EXECUTION_INTENT_WITHOUT_TOOL`; the prose is never
  interpreted as a command. A turn with neither text nor a call reports
  `NO_MODEL_OUTPUT`. Both are review verdicts, never execution evidence.

### Outbound message contract

The canonical message model is validated before any transport serializes it.
`ai.ValidateMessages` is the provider-independent contract:
`ai.Registry.StreamFn` enforces it once at the production boundary, and the
agent loop enforces the same check with per-message provenance (`history`,
`user`, `assistant`, `tool_result`, `recovery`, `synthetic`) before opening a
stream.

- A text content part always carries a non-empty string. An empty part is a
  construction defect, not "no output": an empty tool result is encoded as an
  empty string content, never as a text part without text.
- A user turn has at least one sendable block.
- An assistant turn has non-empty text or a complete tool call (id and name).
  Reasoning alone is display content, never conversation content: a turn that
  produced only reasoning stays in the run's turns as an observation, and
  `agent.ConversationMessages` excludes it when a host rebuilds the next
  request. That single projection is shared by the live history, persistence,
  and session replay.
- A tool result names the call it answers.

A violation is `agent.ErrInvalidProviderContext` — an internal
construction/serialization failure reported before the provider call, naming
the message index, role, provenance, and issue kind. It is never translated
into an empty text part, dropped silently, weakened, or retried, and no adapter
fabricates content to make an invalid message sendable. Adapters emit a
canonical empty shape only where the wire protocol allows it (an assistant turn
with tool calls omits content; a tool result with no output is the empty
string).
- The registry recognizes the provider's observed spellings
  (`<dotsfunctioncall>`, `<dots_function_call>`, and their mixed closing
  markers) as one protocol family. A new spelling is a descriptor there, never
  a TUI or execution change.
- LongCat's `<longcat_tool_call>` envelope is registered in both documented
  payload shapes: a JSON function object and LongCat 2.5's bare tool name
  followed by `<longcat_arg_key>`/`<longcat_arg_value>` pairs. The template does
  not XML-escape string values, so the key/value form is parsed as a bounded
  token grammar rather than XML. A malformed or incomplete LongCat envelope
  follows the same diagnostic-and-residue rule as every other protocol.
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
or a click expands it to `- Thought · 6.5s` and a `│`-guided, muted amber
italic body, bounded so one long trace cannot displace the answer. The completed
`AssistantMessage` keeps the same thinking block, so a resumed transcript
renders it without re-parsing provider output.

## Completion, task state, and exit status

Completion is a state transition, not the model's stop reason. The one
authoritative evaluator is `agent.EvaluateTaskCompletion`; every production
path that reports a task outcome calls it with the contract built from the
user's request before execution and the evidence the runtime observed.

- **Target semantics.** `contract.Target` distinguishes `technology`,
  `repository`, `command`, and `resource` from the filesystem kinds
  `path`/`symbol`/`package`. A technology target describes the desired result;
  it is never a path and completion never requires a changed filename to contain
  it. `IntentContract.MutationScope` is the separate dimension that says where a
  persistent change is allowed: `workspace` for a migration or behavioural
  objective, `paths` (files and directories) only when the request named a
  filesystem boundary.
- **Mutation provenance.** A shell command can change paths its own text never
  names. The host edit journal already captures the workspace before the first
  mutating batch and diffs it after the run; `agent.MutationProvenance` carries
  that diff, the checkpoint identity, and the mutating turn into completion.
  Each change is classified `EXPECTED`, `UNEXPECTED`, `UNATTRIBUTED`, or
  `OUTSIDE_WORKSPACE`. A tool's own `AffectedPaths` is direct evidence; the
  journal diff attributes the rest. A mutation with neither remains
  `UNATTRIBUTED` and `NEEDS_REVIEW`, never silently accepted. The same journal
  diff is folded into the durable evidence state, so a shell-only mutation can
  support the task's completion proof.
- **Evidence.** A mutation objective needs an observed target change and a
  successful verification after it. An execution authorized read-only is not a
  potential mutation, so an investigation probe cannot create a verification
  debt. A checkless execution objective needs a process that reached a
  successful terminal state (`Executes` + `Finished`); a started background
  spawn is not evidence. A **runtime** execution objective (`run this project`,
  `run the app`, `npm run dev`, `start the dev server`) is stronger than that:
  the contract records `ExecutionRuntime`, and completion requires the runtime
  itself to be observed. A finished environment probe such as
  `node -v && npm -v` is real execution, but it is not the requested execution,
  so it cannot prove the objective. The only accepted observation is the
  `Runtime` evidence a successful `verify` plan reports: the service reached
  readiness, at least one check passed against it while it was alive, and it
  did not exit mid-verification. An informational objective needs
  non-empty visible assistant text in the final turn. Empty or whitespace text
  is `NEEDS_REVIEW`. A request that explicitly required external knowledge also
  needs a successful external observation; a local answer does not satisfy it.
  A broad repository account keeps documentation claims distinguishable from
  observed implementation facts: a term that appears only in README/docs, never
  in a source or structural observation, must be attributed ("the README
  describes ...") or the verdict is `NEEDS_REVIEW`. It also keeps evidence
  classes distinguishable: a manifest declares a dependency, Compose declares
  topology, a glob names a source path, and only source content or execution
  establishes behavior. Dependency wording is checked against its own strength
  ladder (`DECLARED`/`IMPORTED`/`INITIALIZED`/`USED_IN_SOURCE`/runtime) and is
  bound to the component the evidence came from. A concept absence ("JWT is not
  implemented") remains bounded to the inspected scope unless a scoped proof is
  available; a complete file inventory is not a semantic absence proof. A
  concrete line count needs the read's measurement, an independent-deployment
  conclusion needs a deployment artifact, and a path-scoped inference needs an
  observation of that path. The evaluator does not judge answer relevance beyond
  these deterministic provenance and claim-scope boundaries.
- **TUI task path.** `task.Begin` persists the contract, the pre-run target
  snapshot, and later the review reason. `NEEDS_REVIEW` is non-terminal. A
  continuation reuses the persisted contract and snapshot; a materially
  different prompt is refused until the human runs `/task cancel` or
  `/task accept`. Both close the task as `CANCELLED`; neither claims the old
  objective completed, and the next prompt starts a new identity. An
  implementation refinement of the open mutation contract is a continuation,
  not a new informational task: it keeps the task, its evidence, and the write
  surface when the live authority is BUILD. A read or investigation request, an
  explicit no-mutation limit, or an explicit mutation prompt remains a new
  objective; see [`WORKSPACE_AUTHORITY.md`](WORKSPACE_AUTHORITY.md).
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
  outside the completion boundary. A `verify` result is different: its service
  reached a declared readiness state, its checks ran, and cleanup completed, so
  it reports `Executes` + `Finished` + `Verifies` only when every non-optional
  check passed. A passing plan with a service also reports `Runtime` evidence
  (command, owned pid, observed address, passing-check count); a command-only
  plan does not, because no live process was observed. A service start failure,
  a failed check, a service that exits while checks run, or an unavailable
  optional browser never produces verification or runtime evidence.
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
  declares no such need. The evidence state's byte bound evicts transient
  diagnostic command logs before content-bearing file reads and listings,
  because those observations are what the declared needs are satisfied by; a
  long investigation must not erase its own answer evidence while retaining
  dozens of shell transcripts.
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
  class, and retryability. The taxonomy separates request-level rejection from
  capability health. Invalid arguments, an invalid selector, an unknown tool,
  an unsupported operation, an authority denial, budget exhaustion, and stale
  evidence are request-level: the operation was never exercised with a valid
  request, so the capability remains available. The identical invocation is
  refused after one rejection, a corrected invocation has a different signature
  and is eligible immediately, and the rejection never trips a provider or task
  circuit breaker. Capability classes keep their own policy: authentication,
  not-configured, and protocol failures are fatal; a retryable class gets one
  retry; an unclassified or non-retryable class is refused on the identical
  repeat. The ledger is persisted in `EvidenceState`, so a resumed turn cannot
  replay a spent failure. A definite workspace change invalidates non-fatal
  failures, because the same command observes a different state after the fix.
  A refusal is a control decision: nothing executes and completion does not
  count it as a failed call. This includes a tool's own authority refusal, such
  as the artifact tool rejecting a kind the live workspace cannot produce. A
  malformed call is the same boundary: the capability rejected the arguments
  before running anything, so it is neither work nor an observation about the
  project, and it cannot veto a proof real evidence already supports.
- **A failed provider is not a failed task.** Sufficiency sees evidence, not
  the constructed action surface, so a capability block is reconciled against
  the providers the task actually offers. If every missing requirement still has
  an independent authorized provider, the block becomes ordinary `INSUFFICIENT`
  exploration with concrete `Next evidence` guidance; only a requirement with
  no remaining provider keeps the terminal capability verdict. The mapping is
  by semantic requirement and capability class, never by vendor or fixed tool
  name: documentation, manifests, root listings, and source reads all satisfy
  observation requirements, while external and execution requirements keep
  their own class. A successful overview is never rewritten by a later targeted
  failure; the failed call adds its own diagnostic and failure record.
- **Closure is one semantic state, not an empty tool slice.** A projection's
  `AnswerEligible` or `Terminal` verdict is the control plane's declaration that
  the action surface is closed. A complete evidence lifecycle (`StageComplete`)
  is answer-eligible for every objective kind, not only informational ones, so
  a continuation call cannot be dispatched after the proving observation. The
  loop owns the request shape, so it withdraws every tool itself; a projector
  cannot leave a callable tool on a surface it has already closed. A blocked
  capability (`BlockCapabilityFailure`) is terminal only after acquisition
  reconciliation finds no independent provider for every missing requirement;
  the first stale proposal without an answer then reports
  `agent.ErrTaskBlocked`, which classifies as `NEEDS_REVIEW`. A substantive
  answer is also terminal on a partially narrowed surface when the rest of the
  message cannot add evidence: every call is either a capability the stage
  withheld or an exact repetition of an observation already current in the run.
  The loop drops those continuation calls before dispatch, so a finished answer
  is not followed by refusal or reuse cards. A new offered capability keeps the
  normal path because the text may be narration before real work. A message
  that only announces an action keeps the bounded recovery, and a
  policy-sensitive proposal is still dispatched so its refusal remains visible.
  The loop can also force the boundary itself: after a refused-proposal
  recovery it asks the projection to close the surface (`ForceAnswer`), even
  for a shape such as an investigation that would otherwise keep probing. A
  refusal caused by the request-level failure ledger gets one bounded
  corrected-invocation window before that forced boundary, so malformed
  proposals cannot consume turns but a valid correction is never mistaken for a
  repeated outage.
- **An unproductive turn is counted from outcomes, never from the offered
  slice.** A refusal is a control decision, not work. If every call in a turn was
  refused, the turn could not change the runtime's state; this is true even when
  the projection still offered other tools. One recovery request is allowed so
  the model can see the structured refusal and choose another action; narration
  emitted alongside a rejected call is not a final answer. The exception is an
  `AnswerRequired` refusal: the runtime offered no tool at all, so the text is
  the candidate answer and the run stops. A second consecutive refusal-only turn
  with visible text ends the run and offers that text to completion. Without
  text, the runtime distinguishes the refusal source: if the refused calls named
  capabilities the projection actually offered, the surface is still open and
  the runtime closes it for one answer-only request instead of blocking. If the
  model stops with no text at all immediately after that refusal-only turn, the
  empty stop is not an answer either: the loop drops the empty assistant message
  and makes the same single answer-only request. If the surface was already
  closed, or the model kept calling tools the projection had withheld, the run
  reports `agent.ErrTaskBlocked`. A turn with any non-refused outcome is work and
  resets the count. The presentation layer publishes the terminal verdict after
  the turn's outcomes and text, so the transcript ends at the decision instead of
  appearing to execute work after it.
- **Required external knowledge is real evidence.** An explicit external
  requirement declares an `external_observation` need, so local files cannot
  make the task answer-eligible. While it is the only missing requirement, the
  projector offers only external-capable tools; discovery is not an observation.
  If the constructed action space contains no external capability, the
  projection is terminally blocked and names that gap. Once the external
  capability produces a successful observation, normal sufficiency and the
  answer boundary apply.
- **Durable execution state crosses a resume.** A run starts with no completed
  turns, but the task may already carry a change and a check from a prior run.
  `ExecutionFromEvidence` reconstructs the execution state from the durable
  evidence, so a resumed mutation whose change has no following check is in
  `verify`, not `act`. The projection therefore tells the continuation to run
  the check the contract requires instead of rediscovering the repository.
- **Verification is a stage, not a hint.** While a mutation has a change with
  no following check, the state block names the missing verification and its
  next evidence. A successful check advances the stage; a corrective change is
  also progress. New observations alone cannot close that obligation.
- **Consecutive turns without a task transition are a semantic stop.** Outside
  verification, a turn that adds new evidence, changes the stage or unmet
  requirements, applies a change, observes a successful check, or establishes
  an explicit blocker is progress. During verification, only a change or a
  check is progress; another read is not. After
  `cognition.MaxConsecutiveNoProgress` consecutive turns without one of those
  transitions, the projection becomes `BlockProgressStalled`, withdraws the
  action surface, and asks for a report. That is a control-plane verdict
  (`NEEDS_REVIEW`), not a resource stop and not the model-turn budget. A task
  that keeps changing state is never stopped by this counter.
- **The stop is evidence-driven, not a call budget.** The action surface closes
  when sufficiency reaches `answer` or `complete`, when a required capability is
  blocked, or when consecutive turns no longer advance the task. A completed
  turn that observed the repository but added no new fact does not close the
  surface while the shape still declares missing evidence: the projector keeps
  the capabilities the current authority permits and appends a bounded
  escalation reason plus the existing `Next evidence` guidance, so the next
  request changes the coordinate or capability instead of repeating it. A turn
  that learned something — including a different window of the same file —
  keeps the full surface. Errors, refusals, and generic tool output are control
  signals, so a model that is correcting itself is never mistaken for one that
  is repeating itself. A deferred phase that still needs execution its
  authority permits is not at the answer boundary either; its next step may be
  a probe or a command. A change objective's next action is the change, but
  once the change is applied its next action is the check.

Budgets remain safety bounds, never the reason a run concludes. A weaker model
may therefore spend more turns reaching the same evidence; the stop follows the
evidence, not the model. The semantic no-progress bound exists so a model that
only repeats an already-known observation reaches review in a few turns instead
of spending the whole grant.

## Commit workflow (`/commit`)

`/commit` is a bounded transaction, not an agent turn. `internal/commit` owns
the control plane, the proposal contract, staging, and verification;
`internal/tui/commit.go` only projects the held request and the approved edit.
The COMMIT model has no tools, no shell, and no path to the index.

The pipeline is: discover repository state → classify every relevant path →
identify logical groups → protect unsafe or unrelated work → build bounded
semantic evidence → ask the model for text → validate it deterministically and
semantically → human review/edit → stage exactly the approved paths → commit →
verify Git's result.

Classification is deterministic and fail-closed. Every path is one of
`committable`, `protected`, `unrelated`, `ambiguous`, `blocked`, or
`insufficient_evidence`.
`.ohgo/`, ignored paths, credential names and private-key material, generated
or machine-local artifacts, operating-system files, nested repositories, and
submodules never enter the model diff. A deletion of a protected file is
committable because it includes no protected bytes; its content is still
redacted from the evidence. An unresolved conflict, or an in-progress merge,
rebase, cherry-pick, revert, or bisect, blocks the operation; a nested boundary
or submodule is excluded and reported. A staged protected path
is refused rather than reset behind the human's back. Staged changes are the
human's explicit selection and keep precedence; otherwise the dominant logical
group is selected and the remaining groups are excluded with a visible note.

A file, directory, and package are each **not** a commit. A commit is one
logical change, and one logical change may span many files. Grouping is a
connected-component decision over proven evidence, never a directory bucket:

- a bounded `StructuralEvidenceProvider` supplies module-local Go import edges;
  a future AST/graph runtime can replace it without changing `/commit` semantics;
- a declaration/reference relationship between changed files is symbol evidence;
- an exact or subsystem-backed test stem joins a test to its implementation;
- a document whose stem names the changed subsystem joins that implementation;
- a distinctive basename token that names the same subsystem joins files across
  directories;
- co-location alone is insufficient: two independent `components/*` changes, or
  two packages that merely share a parent directory, stay separate.

Grouping records why the files belong together. `unrelated`, `ambiguous`, and
`insufficient_evidence` are distinct: the control plane calls a path unrelated
only when it has established a different logical change; a change with no
semantic evidence (an empty or opaque blob) is surfaced as
`insufficient_evidence`, never manufactured into a one-file commit. A task
objective may select a named group but never overrides the working tree. Recent
commit subjects teach the type/scope and body conventions without copying a
message or granting invented references.

### Automatic plan (`/commit -a`)

`/commit -a` builds a complete `Plan` first, read-only, before asking the model
for anything. The plan covers the whole repository change universe and accounts
for every relevant path exactly once: logical groups, protected, blocked,
unrelated, ambiguous, and insufficient evidence. Group order is deterministic:
an imported group precedes its importer, then implementation before tests,
docs, and build/CI, with the group key as the final tie-break.

The host then walks the plan one commit at a time. Each group gets its own
bounded model invocation, its own deterministic validation, and the existing
proposal dialog. `Enter` authorizes exactly the displayed commit, never the
remaining plan; there is no "approve all". After a verified commit the host
re-reads the repository and rebuilds the remaining plan, because HEAD, the
index, and the worktree changed. A stale plan is refused (`ErrPlanStale`) rather
than staged; cancellation stops future proposals and leaves the remaining
changes untouched. Partial completion is a valid state, and a failure stops the
walk without hidden recovery. The plan is a proposal: Git remains authoritative.

A staged index is still special because committing it commits the whole index.
`/commit -a` refuses a partially staged path, or staged changes that span more
than one logical change, and asks the human to commit the index with `/commit`
or unstage it first. It never runs `git add -A` or `git add .`; execution uses
the existing explicit literal path list and rollback.

The model receives bounded evidence: selected scope, diffstat, the selected
diff (never excluded content), exclusions with reasons, group plan, branch and
upstream, bounded recent subjects, the canonical type vocabulary, and the
message limits. It returns only `type(scope): summary` plus an optional body.
`Parse` owns syntax and length; `Snapshot.ValidateProposal` owns meaning: the
type must belong to the repository vocabulary and match the shape of the whole
change set (docs-only, tests-only, build-only, implementation), removals and
additions must be supported by churn, file claims must name approved paths, and
agent-process narration is rejected. A rejection is repaired at most once with
the concrete code (`INVALID_TYPE`, `UNSUPPORTED_CLAIM`, ...), never by restarting
discovery. A reasoning model that exhausts its completion budget gets one
bounded retry with reasoning disabled; a partial message is never accepted.

Execution revalidates the exact bytes and the semantic alignment, compares a
content fingerprint of HEAD, index, and worktree, stages only the approved
paths, verifies the index paths and blob hashes, pins the index tree, commits
once, and verifies HEAD, tree, parents, and message. A stale workspace, staging
mismatch, commit failure, and post-commit mismatch are distinct failures. The UI
renders the verified result and any remaining changes; it is never the source of
truth for success.

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

A detached job survives the turn so `wait` can observe it, but it does not
outlive the session that owns it: session shutdown terminates every running
job's process group. "Detached from the turn" is not "orphaned".

## Verification runtime

`verify` is one execution capability for a structured verification plan, not a
browser-specific hack. The model declares intent; the capability performs the
mechanical lifecycle:

```
service command
  → STARTING → RUNNING → READY (HTTP / TCP / output / process)
  → independent checks (HTTP, command, optional headless browser)
  → bounded evidence
  → STOPPING → STOPPED (process group killed on every path)
```

The service and every check run in owned process groups. Readiness is a
dependency: no check starts until it succeeds. Independent checks run with a
bounded concurrency cap and results keep plan order. A service that exits before
readiness, or never becomes ready, is `TOOL_START_FAILED` with captured output;
an HTTP/command assertion failure is `VERIFICATION_FAILED`; an unavailable
optional browser is `VERIFICATION_INSUFFICIENT`, never a fabricated pass.
Cleanup is deferred and idempotent, so cancellation, a failed check, or a failed
start cannot leave an orphaned server.

HTTP targets are loopback-only. The browser check is optional: it discovers a
headless Chromium/Chrome binary (`OHGO_CHROMIUM`/`OHGO_BROWSER` may override),
runs it against the already-ready local URL, and reports unsupported when none
is available. The core plan never requires Chromium.

Plan arguments accept the equivalent shapes models routinely emit: a service
`cwd` may be workspace-relative or an absolute path inside the workspace (an
absolute path outside it is still an escape and is refused), and a single
literal for `checks[].contains` is the same assertion as a one-element list.
These are contract normalisations, not relaxations: the containment and
assertion semantics are unchanged, and a shape mistake no longer costs a turn
that teaches the model nothing about the project.

The result carries the same `Executes`/`Finished`/`Verifies` evidence every
other capability uses, plus compact structured details (service state, pid,
discovered URL, per-check status/bytes/duration). A passing plan with a service
additionally reports `agent.RuntimeEvidence`, and that observation is what a
runtime execution objective accepts. Attribution is outcome-based, not
command-name-based: the runtime does not need to know that `npm run dev` is
this project's entry point, only that something the model launched became ready
and answered a check while it stayed alive. The model sees a short summary, not
a transcript dump. Completion still decides PROVEN; readiness, a tool call, or a
zero exit code alone is not proof.

## Concurrency

- Shared tools run in parallel; `Exclusive(args)` tools drain the batch
  before and after. Results are reassembled in model emission order, so the
  transcript always matches the assistant message.
- Tool → loop progress flows through `Context.OnProgress`; loop → UI through
  `Config.OnToolProgress` (`started → update → finished`). Every
  transcript-visible outcome gets the pair, including a reused or refused call
  that never reached execution; an `AnswerRequired` refusal is excluded
  because the transcript projection removes it. The final report carries the
  post-`AfterToolCall` result, so the UI never shows output the model will not
  receive. Progress callbacks run outside locks.
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

Protocol/lifecycle failures are classified before a generic verdict:
`NO_MODEL_OUTPUT`, `EXECUTION_INTENT_WITHOUT_TOOL`, and
`TOOL_NORMALIZATION_FAILED` stop a run in which no requested action executed and
are reported as review (non-terminal), preserving the task identity. Tool
outcomes carry a provider-agnostic `FailureClass` such as `tool_start_failed`,
`tool_runtime_failed`, `tool_timeout`, `tool_cancelled`,
`tool_authorization_denied`, `tool_capability_unavailable`,
`verification_failed`, or `verification_insufficient`, so the UI can say whether
failure happened before or after execution. A model claim, a tool emission, and a
zero exit code remain distinct from verification and proof.

An unavailable capability is its own terminal class. `agent.ErrCapabilityUnavailable`
is returned before any provider request when the objective needs an operation no
constructed capability can perform; it classifies as `RunBlocked`, the task
records `CAPABILITY_UNAVAILABLE`, and its `ObjectiveState` is `BLOCKED`. It is
not `NEEDS_REVIEW` (nothing happened to review), not `FAILED` (nothing failed),
and not a model failure. An operation the live authority forbids is
`AUTHORITY_DENIED`, decided by `RouteTurn` or by projecting the objective to its
informational phase.

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
per-provider work is a base URL, a credential name, and (for the gateways)
the `x-opencode-session` header named in catalog metadata and filled from
the loop's session identity. `ai.ProbeProvider` checks reachability +
authentication without spending a completion and backs `/connect` validation,
`/usage`, `/status`, and the TUI probe view. `xai` and Responses-only model
lines are excluded on purpose (no Responses transport in this build),
recorded in `TestExcludedProvidersAreDocumented`.

## CLI and harness (`cmd/ohgo`)

Root runs the interactive session; subcommands: `run`, `sessions` (+
`show`), `models [provider]`, `doctor`, `init`, `docs`, `version`. Flags:
`--model`, `--workspace` (ask|fuzz|build), `--resume`, `--dir`, `--no-tui`,
`--plain`, `--no-tools`, `--yes`, `--reasoning`/`--effort`, `--max-depth`,
`--deadline`.

Both interactive paths share one backend (`harness.go`): the full-screen
UI and the plain REPL call the same code for `/model` (static then live
discovery), `/compact` (state-first manual compaction with a concise outcome),
`/sessions`/`/resume`, `/jobs`, `/usage`,
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

The visual contract — identity colours, semantic tokens, Dark/Light values,
appearance preference, system detection, and fallback behaviour — lives in
[`../ui/DESIGN_SYSTEM.md`](../ui/DESIGN_SYSTEM.md). This section describes the
execution architecture around it.

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
  probe/doctor/usage wiring. The theme picker stores an appearance preference
  (`auto`, `dark`, or `light`) separately from the resolved palette; Auto is the
  default for new and unset settings. `internal/tui/appearance.go` resolves the
  host appearance on macOS, GNOME/KDE Linux, and Windows, returns unknown for
  SSH or unsupported hosts, and falls back deterministically to dark. An
  explicit preference never probes; Auto probes once at startup and on a
  bounded 10-second poll. Theme resolution lives in one program seam and updates
  only presentation state, so it never resets the session or the composer. Slash
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
  the single owner decision for concrete activity: a running tool or open
  reasoning trace presents its action and prose through its own entry.
  `internal/tui/telemetry_bar.go` owns the one live phase and turn-metrics
  surface: a fixed row immediately above the composer, reserved even while
  idle, so starting, handing off, and finishing a turn cannot move the
  composer. The
  bar's left group is the shared spinner, one space, and the shimmering
  runtime intent (`preparing workspace…`, `reasoning internals…`, `executing
  tools…`, `formatting response…`); the middle is pure whitespace; the right
  group pins measured elapsed time and the compact downward output volume
  (`3.9s · ↓1.1k tokens`) to the frame edge. Width math runs on plain text
  before shimmer escapes are applied, so the right block cannot jitter.
  Concrete tool summaries and reasoning prose never appear there, and the
  phase is not repeated in the hint or status row. Output-first mode paints the
  same bar immediately above its composer. The active runtime phase is
  absent from the header, and the workspace authority (`ASK`, `BUILD`, `FUZZ`)
  appears only at the prompt boundary. A tool finalizes into that same block,
  so no live copy survives next to its stable representation.
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
  mini <40×8), content width, conversation height, a fixed one-row telemetry
  slot above the composer, and the bounded composer (two framing rules + input
  rows + slash suggestion rows + state line, all counted so reserved and
  rendered rows agree). Suggestion rows carry a
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
  border token. The telemetry bar sits immediately above the composer only
  while a turn runs and carries the live intent only; an idle frame reserves
  no row, so the transcript meets the composer directly. The measured turn
  timer and session telemetry own the left edge of the hint/status row below
  the composer; the keys that act on the running turn pin to its right edge in
  the faint tier, so the footer reports instead of reading as one long
  telemetry sentence. The
  upper rule is a
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
