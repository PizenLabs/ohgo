# OHGO Architecture Baseline

> **Status:** Reconnaissance baseline of the current implementation.
> **Scope:** Runtime, control plane, execution plane, repository intelligence, state,
> persistence, agent loop, providers, application shells, tests, coupling, hazards.
> **Non-goal:** This document proposes no changes. It describes what exists.
>
> **Method.** The working tree was inspected read-only: `go list` for the import
> graph, `go/parser`-based AST extraction for every package's declarations and
> signatures, direct source reading along the runtime path, and the test suite as
> evidence of protected behaviour. Evidence tiers: **Verify** (Tier 2) for the
> runtime, authority, persistence, repository-intelligence and provider paths;
> **Scout** (Tier 1, provisional) for peripheral packages (`update`, `startup`
> internals, `commit` internals, benchmark harnesses).
>
> **Conventions.** `INFERRED` marks a conclusion drawn from structure rather than
> observed behaviour. `UNKNOWN` marks something that could not be proven from the
> code reviewed. When documentation and implementation disagree, both are reported
> in [Appendix A](#appendix-a--documentation-vs-implementation-mismatches).

---

## Contents

1. [Executive Summary](#1-executive-summary)
2. [Repository Topology](#2-repository-topology)
3. [Runtime Architecture](#3-runtime-architecture)
4. [Control Plane](#4-control-plane)
5. [Execution Plane](#5-execution-plane)
6. [Repository Intelligence](#6-repository-intelligence)
7. [State Model](#7-state-model)
8. [Persistence and Recovery](#8-persistence-and-recovery)
9. [Agent Loop](#9-agent-loop)
10. [Provider / Model Architecture](#10-provider--model-architecture)
11. [TUI / CLI Architecture](#11-tui--cli-architecture)
12. [Dependency Graph](#12-dependency-graph)
13. [Semantic Ownership Map](#13-semantic-ownership-map)
14. [Architectural Invariants](#14-architectural-invariants)
15. [Protected Behavior](#15-protected-behavior)
16. [Coupling Map](#16-coupling-map)
17. [Semantic Duplication / Boundary Overlap](#17-semantic-duplication--boundary-overlap)
18. [Refactoring Hazards](#18-refactoring-hazards)
19. [Unknowns](#19-unknowns)
20. [Refactor Baseline](#20-refactor-baseline)
- [Appendix A — Documentation vs Implementation Mismatches](#appendix-a--documentation-vs-implementation-mismatches)

---

## 1. Executive Summary

Ohgo is a single-binary, terminal-resident coding agent written in Go (module
`github.com/PizenLabs/ohgo`, `go 1.27`, VERSION `0.0.3`), organised around **three
execution authorities** (`ask` read-only, `fuzz` execute-without-mutation, `build`
sole mutation authority) rather than around UI modes.

The *execution engine* is `agent`: a turn-based model/tool loop (`agent.Loop.Run`)
that streams one completion, interprets tool calls, and for each call runs a fixed
gate sequence — argument validation → optional host hook → **semantic grounding
admission** → **security authorization** → **budget admission** → `Tool.Execute` —
then appends results and repeats. The engine never speaks HTTP; it consumes an
`ai.StreamFn` seam.

The *authorization kernel* is `security.Policy`, a stateless function whose inputs
are all derived by trusted runtime code (`cmd/ohgo/security.go: securityRequest`).
The live authority is a `workspace.Workspace` value resolved per call through
`authorityGate`. The *only* production authorizer is `securityGate`
(`cmd/ohgo/security.go`), installed as `agent.Config.Authorize`; the older
`Config.Admit`/`Config.Approve` pair survives as an embedder/test seam and is
skipped whenever an authorizer is present (`agent/loop.go: execute`, protected by
`TestAuthorizeSupersedesLegacyHooks`).

Around the engine sit four largely independent state systems:

* **conversation** — `session` (SQLite transcript, machine-scoped),
* **task continuity** — `task` (project-local `task.md` projection +
  `progress.json` checkpoint),
* **workspace recovery** — `recovery` (machine-scoped SQLite content-addressed
  checkpoints with verified undo/redo),
* **machine configuration** — `config`/`creds`/`state` under `~/.ohgo`.

Context economy is a first-class subsystem: `cognition` derives a deterministic
`TaskShape`, accumulates a durable `EvidenceState`, decides evidence sufficiency,
narrows the offered tool set, and projects a smaller request. It is a *narrowing*
layer only: the loop intersects the projected tool set with its own constructed
instances and still authorizes every surviving call.

Completion is evidence-based, not model-declared:
`agent.EvaluateTaskCompletion` consumes observed `Turn`s, a deterministic
`contract.IntentContract` built from the user's text, and a pre-run
`TargetSnapshot`; it can only return `COMPLETED` or `NEEDS_REVIEW`.

The two application shells are large and not purely presentational. `cmd/ohgo`
(≈20.9k lines, `run.go` alone 105 KB) is the wiring hub, plain REPL, headless
runner, harness command backend, security gate, recovery service, task wiring, MCP
UI and update/commit/doctor/init commands. `tui` (≈67k lines, 157 files) owns the
state machine, renderer, approvals, command registry, model control plane, commit
workflow, recovery commands, task projection — and also invokes control-plane
functions (`workspace.Classify`, `workspace.BuildIntentContract`,
`agent.CaptureTargetsContext`, `agent.EvaluateTaskCompletion`,
`cognition.BuildShape`).

There are **no import cycles** in the package graph. The dominant structural facts
for a future refactor are: control-plane orchestration is duplicated between
`tui/app.go` and `cmd/ohgo/outcome.go`; the human-initiated mutation paths (`!`
shell escape, `/undo`/`/redo`, `/commit`, `/update`) do not pass through the tool
authorizer; and several "phase" compatibility layers remain (mirror fields on
mutation types, legacy `Admit`/`Approve`, `workspace` aliases of `contract`,
name-based read-only tool allow-list).

---

## 2. Repository Topology

`go list ./...` reports 44 packages. Sizes are source lines excluding tests.

| Package | Responsibility | Public API (representative) | State? | Decides? | Executes? | Layer |
|---|---|---|---|---|---|---|
| `agent` (12.4k) | Model/tool loop, grounding, target evidence, completion, projection types, subagent | `Loop`, `New`, `Run`, `Config`, `Tool`, `Result`, `Turn`, `EvaluateTaskCompletion`, `RunSubagent`, `CaptureTargets` | run-local | completion + admission sequence | calls tools | runtime |
| `contract` (0.7k) | Data-only completion vocabulary: objective kinds, targets, expected changes, verification requirements, grounding structs | `IntentContract`, `Target`, `ChangeOp`, `VerificationRequirement`, `IntentGrounding` | no | no | no | vocabulary |
| `workspace` (5.1k) | Three authorities, operation policy, intent classifier, intent-contract extraction, transition/proposal policy, artifact model, style | `Workspace`, `Admit`, `Permits`, `Classify`, `RouteTurn`, `BuildIntentContract`, `ProposeTransition`, `AssessProposal`, `Artifact` | no (pure values) | routing + policy table | no | control |
| `security` (1.0k) | Deterministic authorization kernel, scope normalization, risk classification, grant store | `Policy.Evaluate`, `NormalizeScope`, `ClassifyRisk`, `GrantStore`, `Capability`, `Decision`, `Grant` | grant store (ephemeral) | authorization | no | control |
| `cognition` (4.1k) | Task shape, evidence state, sufficiency, candidate tools, request projection, audit render | `BuildShape`, `EvidenceState`, `EvaluateSufficiency`, `Candidates`, `Projector.Project`, `ProbeFacts` | evidence state (durable via task) | narrowing | no | control |
| `budget` (0.6k) | Atomic multi-dimensional root grant + holder | `Budget`, `Admit`, `AdmitAll`, `AdmitN`, `Holder`, `Limits`, `Snapshot`, `State` | mutable counters | resource admission | no | infrastructure |
| `tools` (16.4k) | All built-in capabilities, mutation previews, shell command classification, verification detection, MCP tools, background jobs | `New*Tool`, `Base`, `MutationPreviewForTool`, `CommandMutates`, `VerificationFor` | tool-local state (jobs, todo, browser sessions, structural source) | no (except local validation) | yes | capability |
| `mcp` (4.6k) | MCP client: stdio/HTTP transports, discovery cache, classification, bounded calls | `Manager`, `ServerConfig`, `Classification`, `Mode` | connection/cache state | classification metadata | yes (remote) | capability/infra |
| `search` (0.8k) / `knowledge` (0.6k) | External search client / bounded external-evidence vocabulary, envelope, failure classes | `search.Client`, `knowledge.Evidence`, `Fail`, `Envelope` | client config | no | network | capability |
| `ai` (4.3k) | Provider-neutral message model, three transports, registry, SSE, probe | `Provider`, `Registry`, `StreamFn`, `Model`, `Request`, `Stream` | provider cache | no | HTTP | provider infra |
| `catalog` (2.8k + 208 KB JSON) | Bundled model facts (13 providers, 379 models) + live discovery for 5 dynamic providers | `Resolve`, `List`, `SnapshotAll`, `DiscoverModels`, `ThinkingFor` | discovery cache | model facts | network | provider infra |
| `creds` (0.75k) | Credential file + env-var name table + shell-file discovery | `Store`, `ProviderEnvVars`, `DiscoverShellAssignments` | credential file | no | no | infrastructure |
| `config` (1.06k) | Machine-global settings, roles, approvals, MCP/search/browser/compaction blocks | `Load`, `Save`, `Settings`, `ApprovalFor`, `RoleModel` | settings file | configuration | no | persistence |
| `session` (1.53k) | SQLite transcript, session metadata, artifacts | `Store`, `CreateWith`, `Append`, `Messages`, `Replay`, `SaveArtifact` | authoritative conversation | no | no | persistence |
| `task` (3.76k) | Durable task continuity, lifecycle, checkpoints, archive, markdown projection | `Task`, `Controller`, `Store`, `Progress` | authoritative task state | lifecycle transitions | no | persistence/domain |
| `recovery` (3.2k) | Content-addressed workspace checkpoints, undo/redo, interrupted-restore repair | `Store`, `Controller`, `Scanner`, `Tree`, `Apply`, `Diff` | authoritative edit history | restore eligibility | filesystem restore | persistence |
| `ast` (6.5k) | In-process Go symbol index + package graph + static references/call edges | `Index`, `NewIndex`, `Lookup`, `Graph`, `Relationships` | derived index | no | parser | repository intelligence |
| `tools.StructuralSource` (in `tools/source.go`) | Lazy shared handle over one `ast.Index` | `Index()` | derived | no | no | repository intelligence |
| `prompt` (0.5k) | System prompt text, workspace contract copy, AGENTS.md discovery, mention context | `Build`, `WorkspaceContract`, `ProjectContext`, `MentionContext` | no | no | reads files | presentation support |
| `exec` (0.44k) | Human `!command` escape parsing + shell runner | `ParseEscape`, `ShellRunner.Run`, `Decide` | no | no | yes | capability |
| `sandbox` (0.36k) | OS read-only execution boundary (sandbox-exec / bwrap / refuse) | `Command`, `Configure`, `Kill` | no | no | process launch | infrastructure |
| `vcs` (1.87k) | Git discovery, status, diff, staging, commit primitives | `Discover`, `RepoInfo`, `StatusEntry` | no | no | git | infrastructure |
| `diff`, `edit`, `shell`, `tokens`, `jsonschema`, `project`, `state`, `memory`, `internal/ignore`, `internal/history` | Small leaf utilities: unified diff, fuzzy edit, shell output decoding/minimization, token estimate, schema validation, project identity, `~/.ohgo` path owner, memory promotion seam, gitignore matcher, REPL prompt history | — | little | no | no | vocabulary/infra |
| `startup` (2.4k) | Read-only discovery + the single initialization gate that creates `<root>/.ohgo` | `Discover`, `Ensure`, `InitGit`, `EnsureGitignore` | no | initialization authority | creates project files | application infra |
| `update` (4.6k) | Release observation, verified download, atomic binary replacement, transition record | `Service`, `Installer`, `CacheStore`, `TransitionStore` | machine state | install eligibility | replaces binary | application infra |
| `commit` (4.5k) | `/commit` propose→validate→execute workflow | `Service`, `Proposal`, `Snapshot` | in-memory proposal | revalidation | git commit | capability |
| `internal/gryhook` | Opt-in GRY lifecycle-hook adapter (evidence only) | `FromEnv`, `Runner.Run`, `Session`, `Install` | memo cache | no | subprocess | evidence integration |
| `cmd/ohgo` (20.9k) | CLI entry, options resolution, loop construction, REPL, TUI bridge, harness, security gate, recovery service, task wiring, MCP UI, doctor/init/docs/update/commit | `main`, `rootCmd`, `runSession`, `runOnce`, `buildLoop`, `securityGate` | owns process-level wiring and live authority | effectively yes (wires policy) | via loop/tools | application |
| `tui` (67.3k) | State machine + renderer + Bubble Tea bridge + command registry + overlays + live mode | `Program`, `NewProgram`, `State.Apply`, `Commands`, `TaskHooks` | UI state, history mirror, approvals | presentation + several control decisions | drives loop off-UI-goroutine | presentation/application |
| `bench/{intent,repository,phase9}` | Benchmark harnesses that import production packages | `main` | fixtures | no | yes | benchmark-only |

Nothing outside `bench/*` and `scripts/updateversion` imports the benchmark
packages; they are leaves.

---

## 3. Runtime Architecture

### 3.1 Actual end-to-end path (interactive, full-screen)

```text
os.Args
  → cmd/ohgo.main → rootCmd().Execute()
  → rootCmd.RunE → runSession                         (cmd/ohgo/run.go)
      → resolveOptions: flags + env + config.Load → options{root, workspace, model, budgets, settings}
      → startup.Ensure (initialization gate; creates <root>/.ohgo only here)
      → openSession (SQLite) + optional applySessionAuthority(--resume)
      → buildTools + buildLoop (agent.New, Authorize = securityGate)
      → tui.NewProgram(loop, model, window, …)
      → program.Run() / RunLive()
  → tui.Program.submit(text)                           (tui/app.go)
      → optional turnTransition prediction (workspace narrow/ambiguous/proposal)
      → tui.Program.startTurnWithChoice → goroutine → runTurnWithChoice
```

### 3.2 What `runTurnWithChoice` actually does (the real per-turn pipeline)

1. **Route guard** (`tui/app.go`): `p.workspace.PrepareTurn(text)` (wired to
   `cmd/ohgo.turnAuthority` → `harness.RouteTurn` → `workspace.RouteTurn`).
   `Blocked` publishes a refusal and stops.
2. **Contract extraction**: `workspace.BuildIntentContract(text)` — deterministic,
   from user text only.
3. **Task continuation decision**: if a non-terminal task exists,
   `continuationPrompt(text)` decides whether to reuse the persisted
   contract/snapshot or `Detach` the old task and start a new one.
4. **Pre-run target capture**: `agent.CaptureTargetsContext` for mutation
   objectives (skipped on a resumed task, which keeps its original baseline).
5. **Task begin**: `p.task.Begin(intent, snapshot)` → `task.Controller.Begin`.
6. **Fresh run budget**: `p.task.RunBudget()` → `holder.Set(grant)` →
   `loop.SetBudget`.
7. **Shape + projection**: `cognition.ProbeFacts`, `cognition.BuildShape`,
   `cognition.NewProjector(shape, task.Evidence(), facts)`,
   `loop.SetProjectionHook`, `loop.SetProjectionReportHook`.
8. **Grounding**: `loop.SetGrounding(intent.Grounding)` with
   `defer SetGrounding(nil)`.
9. **Context assembly**: `@` mentions → `mentionContext`, task continuity block
   from `task.Context()`, proposal decision block; `loop.SetSystemContext(blocks)`;
   `loop.SetTextDeltaHook`, `SetToolProgressHook`, `SetTurnHook` (metrics only).
10. **Recovery journal**: `installRecoveryJournal(turnSessID)` →
    `journal.BeginEdit` happens later inside the loop.
11. **Execute**: `p.loop.Run(ctx, request)`.
12. **Outcome**: `p.usage.Add`, `accumulateSessionMetrics`; deadline re-check
    converts a silent deadline stop into `context.DeadlineExceeded`;
    `turnTaskOutcomeWithReview` maps (error, turns, contract, snapshot, resolver,
    projection review) → task action; atomic terminal claim vs cancellation;
    `task.Complete/NeedsReview/Pause/Fail`.
13. **Presentation + persistence**: assistant text published as `AssistantDelta`;
    every turn persisted via `persistTo` (assistant + tool results), then
    `AssistantDone`; history rebased from the request actually sent (`rebase`).

### 3.3 Headless path (`ohgo run`, plain REPL)

`runOnce` → `workspace.RouteTurn` (refuses before the model) → `buildLoop` →
`runHeadlessTurnAs`:

```text
BuildIntentContract
  → CaptureTargetsContext (mutation only)
  → BuildShape + NewProjector(ephemeral EvidenceState)
  → SetGrounding / SetProjectionHook / SetProjectionReportHook
  → loop.Run
  → projectRun → EvaluateTaskCompletion or EvaluateBudgetCompletion
  → one verdict line + exit code (0/1/2/3/130)
```

It does **not** create a durable task, does **not** checkpoint recovery (the REPL
installs the journal itself around the call), and keeps evidence only for the run.

### 3.4 Entry points inventory

| Entry point | File | Notes |
|---|---|---|
| Interactive session | `cmd/ohgo/run.go: runSession` | TUI if TTY and not `--no-tui`/`--plain`, else REPL; wizard when uninitialized + TUI |
| One-shot prompt | `cmd/ohgo/run.go: runCmd → runOnce` | requires initialized project (`bootProject`), refuses uninitialized |
| Onboarding | `runSessionWizard` / `initCmd` / `startup.Ensure` | only creator of `.ohgo` |
| Plain REPL | `cmd/ohgo/run.go: repl` | uses headless turn function; `/` commands via harness |
| Harness backend | `cmd/ohgo/harness.go` | command implementations shared by REPL and TUI |
| MCP control | `tui/mcp.go` + `cmd/ohgo/mcpui.go` | enable/disable/refresh, writes config |
| Doctor / docs / version / models / sessions | `doctor.go`, `docs.go`, `main.go` | read-only diagnostics |
| Update | `cmd/ohgo/update.go`, `update_notify.go` + `update.Service` | explicit install; background observation is non-installing |
| Commit | `cmd/ohgo/commit.go` + `commit.Service` | separate human workflow |
| Benchmarks | `bench/*` | measure agent efficiency and repo intelligence; not runtime |

---

## 4. Control Plane

### 4.1 What is represented, and where

| Concept | Type | Owner | Built by |
|---|---|---|---|
| Routing intent | `workspace.Intent` (`read`/`execute`/`investigate`) | `workspace` | `Classify`/`ClassifyPrediction` (deterministic keyword/phrase classifier) |
| Completion objective | `contract.ObjectiveKind` (`informational`/`execution`/`mutation`) | `contract` | `workspace.BuildIntentContract` |
| Completion contract | `contract.IntentContract` (targets, expected changes, verification, ambiguity, `Grounding`) | `contract` (data) | `workspace.BuildIntentContract` |
| Semantic reading | `contract.IntentGrounding` (action, entities, scope, assumptions, ambiguities) | `contract` | `workspace/grounding.go: buildGrounding` |
| Task requirements | `cognition.TaskShape` | `cognition` | `BuildShape(intent, ShapeParams{authority, intent, facts, snapshot, budget})` |
| Executable proposal | `contract.ActionProposal` (Action, Targets, Mutates, Executes, External, Delegate) | `contract` | `agent.ProposalForCall(tool, args)` from trusted tool metadata |
| Human decision proposal | `workspace.Proposal` (dimension, verb, subject, options) | `workspace` | `AssessProposal` |
| Authority change proposal | `workspace.TransitionProposal` | `workspace` | `ProposeTransition` |
| Durable objective | `task.Task` (+ `Progress`) | `task` | `Controller.Begin` |

The *contract* is built **once, from user text**, before any model call. It is
never rebuilt from model output. `workspace.BuildIntentContract` limits targets to
8, separates command arguments from mutation targets, and marks an unbounded
mutation ambiguous.

### 4.2 Authority representation

* `workspace.Workspace` — `ask ⊂ fuzz ⊂ build`; `Permits(op)` is the single
  permission table (`workspace/workspace.go`). Unknown operation is denied.
* `security.Authority` interface (`Name`, `Permits(Capability)`) — implemented by:
  * `workspaceAuthority` in `cmd/ohgo/security.go` (delegates to
    `workspace.Permits` via `operationForCapability`);
  * `escapeAuthority` in `cmd/ohgo/security.go` (human `!` escape: read/exec/network
    in every workspace, write only in BUILD).
* `cmd/ohgo.authorityGate` — the live, mutex-guarded workspace read at call time by
  both `securityGate.authorityInForce` and `options.authority()`.
* `security.Decision` / `security.Grant` — outcome (`AUTO`/`APPROVE`/`DENY`),
  capability, scope, risk, `Mutates`, `ExternalMutation`, `ReadOnly`, reason/rule;
  grants are one-shot and cover an exact request.
* `securityGate` — the only production `agent.Authorizer`.

**Where mutation authority is established**: explicit workspace selection
(`--workspace`, `/ask`, `/fuzz`, `/build`, `/permissions`, a `TransitionProposal`
the human accepts) → `harness.SwitchWorkspace` → `authorityGate.Set` +
`securityGate.SetFallback` + session `SetAuthority`. Default is BUILD
(`options.authority`, `session.DefaultWorkspace = "build"`).

**Resume**: `applySessionAuthority` restores the session's recorded workspace and
model unless the user explicitly passed `--workspace`/`--model`/`OHGO_MODEL`.

### 4.3 Proposal → admission → authorization → execution

```text
model emits tool call
        │
        ▼
agent.Loop.execute
  1. tool lookup (unknown → error result; known-but-projected-out → CandidateRejected)
  2. schema validation (jsonschema)         → error result, model may correct
  3. BeforeToolCall host hook               → may block or rewrite args (args revalidated)
  4. semantic admission (Grounding)         → reject mutation the request did not ground
     • delegation under non-mutation intent is forced args["readOnly"]=true
  5. context cancellation re-check
  6. securityGate.Evaluate(call, tool):
       securityRequest:
         capability  = tool.Approval().Tier → security.Capability
                       (or CapabilityNamer / InvocationCapabilityNamer)
         operation   = workspace.OperationForTool(tool,args) → Mutates
         scope       = NormalizeScope / ExternalScope
         risk        = ClassifyRisk(cap, scope, mutates, destructive)
       policy.Evaluate(req, authority, grant?):
         unknown capability      → DENY
         authority.Permits?      → DENY otherwise
         mutation w/o write      → DENY
         outside workspace       → DENY
         ReadOnly = exec && !write authority
         covering grant          → AUTO
         risk LOW/MEDIUM/HIGH    → AUTO/APPROVE/APPROVE
     AUTO  → execute
     DENY  → refusal result (decision recorded on the outcome)
     APPROVE → securityGate.Approve:
         mutation preview from tools.MutationPreviewForTool (fail closed if unavailable)
         human asks (unless --yes / settings allow)
         grant recorded; then Evaluate again and require AUTO
  7. cancellation re-check
  8. budget.Admit(ToolCalls)                → refusal result + recorded admission failure
  9. tool.Execute(agent.Context{ReadOnly, Budget, Deadline, OnProgress})
 10. AfterToolCall host hook               → may override Text/IsError/Useless/Details
 11. progress report (final), outcome returned with *security.Decision
```

This sequence is the load-bearing authority path. `agent/authorize.go` documents it
and `agent/authorize_test.go` pins it.

### 4.4 Bypass analysis

| Path | Reaches the tool authorizer? | What governs it instead |
|---|---|---|
| Model tool call | **Yes** | `securityGate` + budget |
| Subagent tool call | **Yes** (inherited `Authorize`) | same gate instance (`newSubagentSpawner` passes `gate`) |
| MCP tool call | **Yes** | same gate; capability refined from trusted config (`SecurityCapabilityFor`) |
| Human `!command` shell escape | **No** | `securityGate.EvaluateCommand` + `escapeAuthority` + OS read-only sandbox (`exec.ShellRunner.ReadOnly`) |
| `/undo`, `/redo` | **No** | `recovery.Controller` preconditions (live state must equal recorded after-state) |
| `/commit` | **No** | human approval + proposal fingerprint/staged-state revalidation in `commit.Service.Execute` |
| `/update` | **No** | explicit human command, checksum verification, install lock, version probe |
| `/model`, `/mcp` enable/disable, `/connect`, `/disconnect`, theme | **No** | config/credential writers |
| Startup project creation | **No** | `startup.Ensure` decision gate |
| GRY lifecycle hooks | N/A | evidence only; `BeforeToolCall` returns no block |

So the accurate invariant is: **every model-proposed tool call crosses the
authorizer; human-initiated commands cross their own boundaries, not the tool
authorizer.** This is documented behaviour in code comments but is a real
architectural seam (see [§18](#18-refactoring-hazards)).

---

## 5. Execution Plane

### 5.1 Capability inventory (`tools`)

Built by `cmd/ohgo/run.go: buildTools`:

`read`, `write`, `edit`, `delete`, `bash`, `wait`, `glob`, `grep`, `ls`,
`symbols`, `graph`, `refs`, `web_fetch`, `todo`; plus
`search`/`browser`/`mcp_list`/`mcp_read`/`mcp_call` from `externalTools`; plus
`task` when `maxDepth > 1`; plus `artifact` when a session store is attached
(`buildLoop`).

| Capability | Tier / classes | Validation | Boundary enforcement | Evidence produced |
|---|---|---|---|---|
| `read` | read | path arg, limit/offset | `resolvePath` (symlink-resolved containment) | `Details` with path/totalLines/offset/limit; exact text |
| `ls` | read | path/limit/hidden | `resolvePath` | listing details |
| `glob` | read | pattern/limit | root-scoped walk, ignore matcher | match list, truncation |
| `grep` | read | pattern/literal/limit | root-scoped, ignore matcher, worker bound | matches + truncation |
| `write` | write / mutate / create+modify | path + content required | `resolvePath`; preview before approval | `Mutates=true`, details, preview/evidence |
| `edit` | write / mutate / modify | `old_string` non-empty, differs, must match (fuzzy) | `resolvePath`; preview computed pre-approval | `Mutates=true` |
| `delete` | write / mutate / delete | path | `resolvePath`; preview | `Mutates=true` |
| `bash` | exec, classes execute+mutate | command non-empty, timeout bounded | `sandbox.Command`; `ReadOnly` from decision; process-group kill; deadline clamp | `Executes`, `Finished`, `Mutates` (classified), `Verifies`+scope |
| `wait` | read | job id | shares `BackgroundJobs` table with `bash` | finish/exit/timeout, mutation, verification |
| `symbols`/`graph`/`refs` | read, `RequiredLanguage=go`, graph/refs relationship | query bounds | `ast.Index` containment, cancellation | coordinates / edges / witnesses |
| `web_fetch` | read, external | URL | allowlist config, size cap | untrusted envelope |
| `search` | read, external | query/provider | provider config, retry/byte caps | untrusted envelope |
| `browser` | read/mutate (reserved), external | operations, sessions | allowed hosts, redirects, bytes, TTL | page/links, history |
| `mcp_list`/`mcp_read` | read, external | closed arg sets | manager bounds, metered | discovery/resource |
| `mcp_call` | **write** default, exec when classified execute | server/capability/arguments | `SecurityCapabilityFor`; mutation boundary `ModeMutate` unless execute | tool result |
| `todo` | read, plan | phase/task JSON | in-memory list only | snapshot, progress |
| `artifact` | read, plan | kind/target; finding requires reproduction+evidence | producer kind must equal the instance's `origin` workspace | persisted artifact |
| `task` (subagent) | read (delegation) / delegate | prompt non-empty; depth | child tool set + inherited authorizer | child answer + completion evidence |

### 5.2 Shell execution

Two distinct shell paths:

1. **`bash` tool** — `tools/bash.go`. `Capability(args)` classifies the command via
   `tools/capability.go` (`commandWrites`, `CommandDestructive`) because the
   declared tier is only `exec`. Classification feeds risk and `Mutates` evidence,
   **not** the no-mutation boundary. The boundary is `sandbox.Command` with
   `ReadOnly` from the security decision (`security.Policy.Evaluate` sets
   `ReadOnly = exec && !authority.Permits(Write)`). On an unsupported platform the
   command is refused (`sandbox.ErrUnsupported`), never downgraded.
2. **`!command` human escape** — `exec/exec.go` + `cmd/ohgo/run.go:
   attachShellEscape`. Same classifier, different authority (`escapeAuthority`),
   same sandbox. Permitted in ASK but read-only there.

Foreground bash is `Exclusive`; background spawn is not. Background jobs: 8
running, 32 retained, 30 KB output, timeout bounded by the root deadline,
`DimensionBackgroundJobs` charged before spawn.

### 5.3 Mutation preview vs mutation evidence

`agent.MutationPreview` is produced pre-execution by `tools/mutation.go`
(`MutationPreviewForTool`) for `write`/`edit`/`delete`, and by the security gate for
non-file mutations (command text). `agent.MutationEvidence` is the post-execution
analogue (`evidenceFromPreview`; currently assigned to a package-level blank var —
see [§17](#17-semantic-duplication--boundary-overlap)).

`Result.Mutates` is set by the tool from what it actually did, never from model
text. Completion uses `Result.Mutates` **or** `Authorization.Mutates` as definite
change evidence (`agent/completion.go: scanCompletionEvidence`).

### 5.4 Verification detection

`tools/verification.go` recognizes `go test`, `go build`, `go vet` (and
equivalents) as checks, walks `cd`/`pushd` to rebase package scopes, rejects
`popd`/outside-root/`|| true`-masked commands, and returns
`*agent.VerificationEvidence{Command, Check, Scopes}`. That evidence is matched
against `contract.VerificationRequirement` by `agent.AssessBehavior`.

### 5.5 Capabilities that bypass the canonical path

* `bash` and the human escape are the only paths that start arbitrary processes.
  Both go through `sandbox`.
* `recovery` can rewrite the workspace but is reachable only from `/undo`/`/redo`,
  and refuses unless the live tree matches the recorded state
  (`recovery.Controller.move`).
* `commit.Service` stages and commits without the tool authorizer.
* `update.Installer` replaces the running binary without the tool authorizer.
* No tool holds authority: `tools` never implements `agent.Authorizer`, and the
  loop owns `Execute` dispatch.

---

## 6. Repository Intelligence

### 6.1 Actual architecture

```text
request (symbols / graph / refs / completion resolver)
   ↓
tools.StructuralSource.Index()            one lazy handle, one ast.Index
   ↓
ast.Index.ensureFresh / ensureFreshGraph  stat+size+mtime snapshot compare
   ↓  stale
ast.GoBackend.Parse (one go/parser pass per file)
   ├─ symbols    → fileRecord.symbols (coordinates incl. line ranges)
   ├─ imports    → packageGraph (module-local edges only)
   └─ references → symbolGraph (static refs, direct call edges + certainty)
   ↓
tools.SymbolsTool / GraphTool / RefsTool / agent.StructuralResolver
   ↓
model tool result  →  (optionally) target evidence / completion
```

* **One parser, one index.** `tools/source.go` says the handle is the ownership
  point; `tools/symbols.go: LocateSymbol/LocatePackage` implements
  `agent.StructuralResolver` over it. `cmd/ohgo/taskcontinuity.go:
  structuralResolver` finds the `*tools.SymbolsTool` in the live loop's tool set,
  so completion verifies against the same snapshot the model queried.
* **No daemon, no graph DB.** Derived state is in-process, rebuilt on staleness.
  (`docs/architecture/STATE_AND_PERSISTENCE.md` still says the index does not exist
  — see [Appendix A](#appendix-a--documentation-vs-implementation-mismatches).)
* **Freshness:** `staleLockedCtx` walks the tree checking size+mtime and count,
  plus unreadable-entry changes; `ensureFreshGraph` additionally fingerprints
  `go.mod`. Stale → **full rebuild** (`buildLockedCtx`); there is no incremental
  update. A rebuild error is returned, never swallowed.
* **Bounded, cancellation-aware:** graph/refs enforce depth (1–4), limit (≤200),
  edge (≤400), work (200k) bounds; cancellation is checked every 256 edge
  inspections (1024 while building).
* **Symlinks:** the walk root resolves a root-level symlink; nested symlinks are not
  followed by `WalkDir`. `ast.Index.Root` keeps the lexical spelling so coordinates
  stay workspace-relative.
* **Skip rules:** `shouldSkipIndexDir` skips vendor/testdata-ish directories;
  non-Go files are ignored entirely (the index is Go-only).
* **Unresolved is explicit:** ambiguous qualifiers, dot imports, method dispatch,
  function values and reflection become `unresolved`/`unsupported`, never guessed
  edges.

### 6.2 Repository facts for shaping

`cognition.ProbeFacts(root)` does a cheap probe (Go/JS/Python/Rust/Make markers,
root files) used only to hide irrelevant languages. `tools.WorkspaceMentions` feeds
`@` completion; `internal/ignore` is shared by grep/glob and recovery so they agree
on what belongs to the repository.

### 6.3 Coupling to the runtime

`agent` does not import `ast` or `tools`. It depends on
`agent.StructuralResolver` (an interface) for target checks; production injects
`*tools.SymbolsTool`. This is a healthy inversion. `cognition` depends on `agent`
types (`Turn`, `Tool`, `ToolOutcome`) directly, and `task` depends on `cognition`;
that is a tighter coupling.

---

## 7. State Model

| Domain | Type(s) | Owner | Lifetime | Mutation authority | Persistence | Readers | Sync |
|---|---|---|---|---|---|---|---|
| Live execution authority | `authorityGate` (`workspace.Workspace`) | `cmd/ohgo` | process | explicit switch only | session row (`workspace`) | `securityGate`, `options`, `harness` | `sync.RWMutex` |
| Security grants | `security.GrantStore` | `securityGate` | ephemeral | `Approve` | none | `securityGate.Evaluate` | mutex; consume-on-read |
| Model/provider selection | `ai.Model` in `Loop.cfg.Model`, `options.model`, `harness.model` | `cmd/ohgo`/`tui` | session | `/model`, role assignment, workspace switch | `settings.Roles`, session row (`model`) | loop, harness, TUI header | plain fields (no lock; set between runs) |
| Loop run state | `agent.Loop` (`budget`, `admissionErr`, `lastProviderTokens`, `cfg`) | `agent` | one `Run` | loop | none | loop only | mutex on admission error; atomic budget |
| Conversation | `[]agent.Message` (`Program.history`, REPL `history`) | `tui`/`cmd` | session | caller (append/rebase) | `session` DB | loop (as request), UI | `Program.mu` in TUI |
| Transcript | rows in `messages` | `session` | durable | `Append` | SQLite WAL | replay, UI | DB transaction per append |
| Session metadata | `session.Session` | `session` | durable | `CreateWith`, `SetAuthority`, `SetTitle`, `SetArchived` | SQLite | opener, browser, UI | DB |
| Task | `task.Task` / `Progress` | `task.Controller` | durable | `Begin/Sync/Pause/NeedsReview/Complete/Fail/Cancel/Detach/Resume` plus legal transitions | `<root>/.ohgo/progress.json` (authoritative) + `task.md` (projection) + `tasks/` archive | TUI, prompt continuity, gate task ID | controller in-process; files atomic |
| Evidence | `cognition.EvidenceState` | `Projector` → `task.Controller` | task | `ObserveOutcome` during projection | embedded in `progress.json` | projection, completion review | single run goroutine |
| Budget | `budget.Budget` (shared pointer) + `budget.Holder` | `options`/`task.Controller` | per run | `Admit*` under mutex | cumulative `budget.State` in task progress | loop, spawner, external tools, UI | `sync.Mutex` |
| Edit history | `recovery.Checkpoint`/`Tree`/blobs | `recovery.Store` | durable | `BeginEdit`/`Commit`/`Undo`/`Redo` | `~/.ohgo/recovery/<repo-hash>/recovery.db` | TUI/REPL recovery commands | store mutex + SQLite TX |
| UI state | `tui.State` | `tui.Program` update goroutine | process | `State.Apply(event)` | none | renderer | single goroutine |
| UI history mirror | `Program.history` | `tui.Program` | session | turn goroutine + update goroutine | via `persistTo` | loop request | `Program.mu` |
| Approval memory | `Program.sessionAllowed` | `tui` | session | human choice | none | `ApprovalFunc`, shell escape | `Program.mu` |
| MCP manager | `mcp.Manager` | `cmd/ohgo.mcpRuntime` | session | tools/UI | connection + discovery cache (`~/.ohgo/cache`) | tools, `/mcp` UI | manager mutex |
| Config | `config.Settings` | `config` | machine | `Save` | `~/.ohgo/settings.json` | options, UI, harness | atomic write |
| Model cache | `catalog` snapshot cache | `catalog` | machine, derived | discovery refresh | `~/.ohgo/cache/*.json` | pickers, resolution | atomic write |
| Update state | `update.Cache`, `VersionTransition` | `update` | machine | observation / explicit install | `~/.ohgo/update*.json`, lock | header chrome, `/update` | file lock |
| Prompt history | `internal/history` | `cmd/ohgo` | machine, audit | append + rotate | `~/.ohgo/history` | REPL recall | mutex |
| GRY evidence | `gryhook.Session` | headless caller | one run | hooks | none | `PlanEvidence`, hook logs | mutex |

Multiple packages that appear to own the same semantic state:

* **Authority** is owned by `workspace` (policy) but *held* by `authorityGate` and
  *persisted* by `session.Session.Workspace`; `options.workspace`,
  `harness.workspace` (label string), and `authorityGate` can disagree transiently
  (see [§18](#18-refactoring-hazards)).
* **Completion** is computed by `agent`, projected by `tui`
  (`turnTaskOutcomeWithReview`) / `cmd` (`projectRun`), and stored by `task.Status`;
  three vocabularies (`CompletionState`, `task.Status`, `runState`).
* **Todo** is owned by `tools.TodoTool` (in-memory), snapshotted by
  `task.Controller.syncTodos`, and displayed by `tui.TaskProjection`.
  `task.Todo.ID` is regenerated when labels change
  (`Controller.SyncKeepsTodoIDsByLabel` test).
* **Evidence** is observed by `cognition.EvidenceState` *and* independently
  recomputed by `agent.scanCompletionEvidence` from the same turns; they can
  disagree (the projection is bounded, the completion scan is complete).
* **Current session** is held in `Program.session`, `session.Store`,
  `securityGate.sessionID` (set once at `buildLoop`), and the recovery scope; a
  mid-session resume does not update all of them (see
  [§18](#18-refactoring-hazards)).

---

## 8. Persistence and Recovery

### 8.1 Durable layout

```text
~/.ohgo/                              (state.Dir; one path owner)
  settings.json                       config          machine, authoritative
  credentials.json (0600)             creds           machine, authoritative
  sessions.db (+ -wal, -shm)          session         machine, authoritative conversation
  cache/<provider>-models.json        catalog         derived
  recovery/<repo-hash>/recovery.db    recovery        machine, authoritative edit history
  history                             internal/history ephemeral prompt audit
  update.json / update-transition.json / update.lock   update

<project>/.ohgo/                      (project.Root; gitignored)
  project.yml                         project         project identity (id/name/root/user.name)
  task.md                             task            human/model projection
  progress.json                       task            authoritative checkpoint
  tasks/<id>/…                        task            detached/archived tasks
```

### 8.2 Source-of-truth chain

```text
user request text
  → workspace.BuildIntentContract            (deterministic; never model-derived)
  → task.Controller.Begin(contract,snapshot) → progress.json (authoritative) + task.md (projection)
  → agent.Loop.Run                           → agent.Turn evidence
  → agent.EvaluateTaskCompletion             → task status transition
  → task.Controller.persist                  → progress.json (status, contract, snapshot,
                                               budget ledger, evidence state, audit, review reason)
```

Conversation durability is separate: `session.Store.Append` writes the agent-level
transcript (user/assistant/toolResult; UI-only notifications dropped; thinking
blocks and images are **not** persisted). Task state is not a transcript copy, and
the transcript is not a task log; they are joined only by session id and by the
task's objective/contract.

### 8.3 Recovery semantics

* **Checkpoint before mutation.** `Loop.dispatch` calls `journal.BeginEdit` before
  the first batch that *may* mutate (`batchMayMutate`), before any call in that
  batch is authorized. A journal error refuses the entire batch with "no files were
  modified".
* **Commit at run end, including after cancellation.** `Loop.Run`'s deferred
  `txn.Commit(context.WithoutCancel(ctx))` publishes the checkpoint; a failure is
  appended to the run error.
* **Publish only a real change.** `publishEdit` diffs before/after trees; no change
  deletes the pending row instead of creating a phantom undo point.
* **Undo/redo preconditions.** Restore only if the live tree equals the recorded
  `after_tree` (undo) or the expected predecessor (redo); the tree is re-scanned and
  verified after apply. A mismatch is a refusal that touches nothing.
* **Interrupted work repair.** `pending_edits` and `inflight` rows are repaired on
  `BeginEdit`; a corrupt/missing blob fails closed with `ErrNeedsInspection`.
* **Scope.** Capture excludes `.git`, the ohgo state directory, and anything
  ignored by `.gitignore` (shared `internal/ignore`); symlinks are captured by
  target; executable bits preserved; identical content stored once.
* **Reachability.** Only `/undo`, `/redo` and `Inspection` reach restore; no tool
  exposes recovery (`TestNoRecoveryToolIsRegistered`,
  `TestRecoveryIsNotInTheToolSurface`).

### 8.4 Other recovery/continuity behaviours

* **Session continuation:** `--resume <id>` loads metadata + replays messages; a
  missing id is fatal (an empty history would look like a wiped session).
* **Task continuation:** a non-terminal task with a contract is resumed by an
  explicit continuation prompt; a materially different prompt requires `Detach`
  (preserved via `tasks/`, reachable by `/task list`/`/task resume`) or
  `/task cancel`/`/task accept` (both close as `CANCELLED`). Contract immutability
  is enforced with `reflect.DeepEqual`.
* **Crash normalization:** a task left `RUNNING` on disk is restored as `PAUSED`
  (`Restore`), then persisted on the next transition.
* **Stale-write refusal:** `SaveCheckpoint` refuses a checkpoint older than the
  on-disk stamp.
* **Migration:** `session.migrate` adds `workspace`/`project` columns to legacy
  DBs; `progress.Version` validates schema (0 = corrupt, >current = version error).

---

## 9. Agent Loop

### 9.1 The loop, exactly

`agent.Loop.Run` (`agent/loop.go`) per iteration:

1. `ctx.Err()` check.
2. Turn cap: `len(turns) >= cfg.MaxTurns` → `turnBudgetError` if an explicit budget
   exists, else `ErrTurnLimit`.
3. Deadline: stricter of `cfg.Deadline` and `budget.Deadline()`; expired → return
   turns, `nil` (a budget stop, not a failure).
4. `compactIfNeeded`: if enabled and estimate (floored by provider-reported tokens)
   exceeds threshold → `Compact(...)` and **restart the iteration** (the compaction
   itself is not a turn; the production summarizer charges
   `DimensionModelCalls`).
5. Projection: `cfg.Project(ProjectionRequest{Messages, Turns, Turn, Tools,
   System})`. The loop keeps `live` untouched; it uses the projected
   messages/tools/system only for this request, ignores an empty message slice,
   intersects tools with its own built-ins (replacing by name with its own
   instance), records audit fields itself, and reports via `OnProjection`.
6. `convert` messages (UI-only dropped).
7. `budget.AdmitAll(Turns, ModelCalls)` — atomic, before the provider call.
8. `cfg.Stream(ctx, ai.Request{...})`.
9. `drain` → `ai.DrainStream`, forwarding text deltas; provider errors surface from
   `Stream.Result()`.
10. Append assistant to `live`; if `ToolCalls()` empty → return turns (natural
    stop).
11. Steering messages injected (if configured) — only at a boundary.
12. `dispatch`:
    * `BeginEdit` before the first mutating batch,
    * exclusive tools drain the batch before and after; shared calls run
      concurrently,
    * results reassembled in emission order,
    * admission failure from a tool's `Result.Exhausted` recorded and returned
      after the batch.
13. Append `ToolResultMessage` per outcome; append `Turn`; `OnTurn`.
14. If an admission failure was recorded, return turns + error; otherwise loop.

### 9.2 Grounding (semantic admission and review)

* Admission: `loop.execute` → `ProposalForCall(tool,args)` →
  `ValidateProposal(grounding, proposal)`. A mutation under a non-mutation intent is
  `GroundingReject` (`CandidateRejected=true`, no execution). An actionless request
  still allows reads/execution as bounded observation with a `GroundingReview`
  note; the verdict only rejects mutations.
* Delegation narrowing: a `delegate` tool under non-mutation grounding gets
  `readOnly=true` forced into args before validation.
* Review: `AssessGrounding` (deterministic regex over the final answer + observed
  evidence) can only downgrade completion: invented action narrative, dropped
  required entities, narrowed scope, unresolved ambiguity, unsupported global
  negative conclusion, unverified "could not find" result.

### 9.3 Completion

Two evaluators, one shared evidence scan:

* `EvaluateCompletion(turns, mutationIntent)` — legacy/general rules.
* `EvaluateTaskCompletion(CompletionInput)` — contract-aware; used by every
  production reporting path.

Rules (in order): non-empty turns; no turn error; last turn has no outstanding tool
calls; no unrecovered tool failure (a later success of the same tool or any
successful verification clears it); mutation objective requires a definite
mutation; execution objective without a check requires `Executes && Finished`;
informational objective requires non-empty final assistant text; last change must
precede the last successful verification; a non-mutation objective must not have
mutated; structural target checks (`CheckTargets` + `UnexpectedMutations`);
behavioural scope matching (`AssessBehavior`); ambiguity; `AssessAlignment`;
`AssessGrounding`; finally the cognitive `EvidenceReview` veto.

`EvaluateBudgetCompletion` injects a synthetic closing turn **only** when a
mutation was definitely observed and verified; exhaustion itself never becomes
success.

### 9.4 Failure classification and retries

| Failure | Shape | Effect |
|---|---|---|
| Provider/stream error (including malformed tool-argument JSON in OpenAI finalize) | `error` from `Run` | run aborts; completed turns returned |
| Schema violation | error `Result` | model may retry next turn |
| Unknown tool | error `Result` | model may retry |
| Tool withheld by projection | `Result.CandidateRejected` | refusal, not a failed action |
| Grounding rejection | `Result.CandidateRejected` | refusal |
| Authorization DENY / approval rejected / changed after approval | refusal `Result` + `Authorization` | refusal |
| Budget exhaustion | `*budget.ExhaustedError` | after the batch: run stops with resource error |
| Deadline | nil error, turns returned | caller turns it into PAUSED/CANCELLED |
| Cancellation | `ctx.Err()` | turns returned; caller maps to PAUSED/CANCELLED |
| Recovery checkpoint failure | error `Result` for the batch | no tool ran |

There is **no automatic retry** of a failed model call inside `agent`; retries are
model-driven (next turn) or absent. `search.ProviderConfig.Retries` exists for
external search only. `commit` has proposal retry semantics of its own.

### 9.5 Concurrency, cancellation, budgets, subagents

* Tool calls in a batch may run concurrently; exclusive ones serialize. `dispatch`
  joins with a `sync.WaitGroup`, records outcomes under a mutex, and restores model
  order.
* Cancellation is re-checked before and after authorization, before budget charge,
  and after the tool returns contextually. Tools must honour `Context.Ctx`;
  `checkCancelled` gives uniform error text.
* Deadline: absolute; children inherit the same instant;
  `tools.effectiveDeadline` also consults `ctx.Budget.Deadline()`.
* Budget: one root `*budget.Budget` shared by pointer through `Holder`; `AdmitAll`
  is atomic; tool calls, model calls, children, background jobs, external calls and
  external bytes are metered.
* Subagents (`RunSubagent`): child starts from exactly one user message + its own
  system prompt; refuses empty task, missing system prompt, no tools, and a
  read-only request whose tool set can write; refuses at construction if
  `IsReadOnly(tools)` is false. Inherits stream/model/budget/limits/hooks/authorizer/
  compaction, but clears `OnTurn`, `OnToolProgress`, `TransformContext`, steering,
  `GetToolChoice`, journal. Completion is the child's own contract-based verdict;
  the parent's tool result carries the child's text plus child completion evidence
  fields (`Completion`, `Mutated`, `Executed`, `Verified`, `Verification`).
* Depth is structural, not runtime-checked: the task tool is omitted at the cap
  (`childToolSet`), so a prompt cannot re-enable it. Default `--max-depth 1`
  removes delegation entirely.

---

## 10. Provider / Model Architecture

### 10.1 Dependency direction

```text
catalog (bundled JSON + discovery) ──→ ai.Model (type only)
        │
        ▼
cmd/ohgo.resolveOptions ──→ options.model
        │
        ▼
agent.Config.Model ──→ ai.Request.Model ──→ Registry.ProviderFor(Model)
                                                  │
                                                  ├─ AnthropicProvider (messages API, SSE)
                                                  ├─ OpenAIProvider (chat completions, SSE)
                                                  └─ OllamaProvider (native /api/chat, NDJSON)
```

* `ai` imports only `creds` (for the credential chain) plus stdlib. It never
  imports `catalog`.
* `agent` imports `ai`, never `net/http`; the only HTTP is inside `ai`.
* `catalog` imports `ai` and `state`; it is the authority for model facts (context
  window, max output, pricing, reasoning ladder, modalities) and for live discovery
  (`DiscoverModels`, 5 dynamic providers).
* `creds` is the sole owner of `ProviderEnvVars`; resolution precedence: explicit
  registry key → credential file → environment → parsed shell startup files
  (`Registry.ResolveAPIKeySource`).
* `ai.Registry.ProviderFor` caches per `provider|effective-baseURL`; a registry
  override wins over the model's bundled base URL.

### 10.2 Selection and roles

* Precedence: `--model` flag → `settings.Roles[role].Model` → legacy
  `settings.Models[workspace]` → `settings.Model` → `OHGO_MODEL` → catalog
  default. Unknown references are errors, never silent fallbacks
  (`resolveModelRef`).
* Roles (`config.Role`): `ask`, `fuzz`, `build`, `sup`, `commit`. First three
  mirror workspaces; `sup`/`commit` are model roles with no authority.
* Runtime selection is **split**:
  * `cmd/ohgo.resolveOptions` resolves the initial model;
  * `harness.applyModel`/`SetModel`/`SwitchModel` call `loop.SetModel` and update
    cost accounting;
  * `/model` in the TUI goes through `ModelControlHooks.Assign` →
    `harness.AssignRole` → config;
  * a workspace switch re-resolves the role's model and effort
    (`harness.SwitchWorkspace`).
* So model selection is simultaneously **configuration authority**
  (config/Settings), **runtime state** (`Loop.cfg.Model`), and **application
  concern** (CLI/TUI). There is no single owner; the loop is a passive holder.
* `agent.Config.ModelFn` exists but is unused in production; `SetModel` clears it.

### 10.3 Streaming contract

`ai.Provider.Stream(ctx, Request) (*Stream, error)`; `Stream.Events` yields
`EventStart`, `EventTextDelta`, `EventThinkingDelta`, `EventToolCallStart/Delta/End`,
`EventDone`/`EventError`; `Stream.Result()` blocks for the assembled
`AssistantMessage`. `DrainStream` is the single consumer used by the loop and
`$sup`. Tool-argument JSON is decoded once at `EventToolCallEnd` (or provider
finalize); a decode error fails the stream.

### 10.4 `$sup`

An ephemeral answer surface: dedicated SUP model/effort (`supportBuilder`), minimal
`prompt.Sup` system instruction, shared provider transport, **no tools, no
repository context, no loop, no persistence** (`tui/app.go:
startSupportRun/runSupport`, `tui/supportview.go`, `supportSurface` in
`cmd/ohgo/support.go`).

---

## 11. TUI / CLI Architecture

### 11.1 Responsibility split as implemented

| Concern | `cmd/ohgo` | `tui` |
|---|---|---|
| Flag/env/config parsing | yes (`resolveOptions`) | no |
| Init gate | `startup.Ensure`, wizard | renders wizard, commits through host hooks |
| Loop construction | yes (`buildLoop`) | receives `*agent.Loop` |
| Live authority | `authorityGate`, `securityGate` | displays; switches via host hook |
| Policy evaluation | `securityGate` | supplies human approval callback only |
| Intent classification | `turnAuthority`/`turnTransition` (harness) | calls `workspace.Classify` for choice turns; displays `TurnAuthority` |
| Contract extraction | `runHeadlessTurnAs` | **also** calls `workspace.BuildIntentContract` in `runTurnWithChoice` |
| Target capture | `runHeadlessTurnAs` | **also** calls `agent.CaptureTargetsContext` |
| Completion evaluation | `projectRun` | **also** calls `agent.EvaluateTaskCompletion` via `turnTaskOutcomeWithReview` |
| Task lifecycle | wires `task.Controller` into `TaskHooks` | calls hooks (`Begin/Complete/NeedsReview/Pause/Fail/Detach/ResumeTask`) |
| Persistence | headless `persistTurn`; REPL persists user turns | `persistTo` per turn (assistant + tool results) |
| Recovery | `recoveryService` adapter | `/undo`, `/redo`, `Recoverer` hooks |
| Command dispatch | harness methods (`SwitchWorkspace`, `Undo`, `CompactHistory`, `SetModel`, `ConnectREPL`, …) | `Program.runCommand` switch |
| Presentation | plain output, plain REPL | state machine + renderer + live mode + overlays |

### 11.2 Business rules that live in the UI layer

* **Turn admission policy**: `tui.Program.runTurnWithChoice` decides refusal,
  continuation vs new task, snapshot recapture, run-budget install, projection
  install, terminal claim. These are control-plane decisions implemented in
  `tui/app.go`.
* **Completion → task transition**: `tui/completion.go: turnTaskOutcomeWithReview`
  is a second mapping of the same evaluators used by `cmd/ohgo/outcome.go:
  projectRun`. They agree today but are separate code paths.
* **Approval memory**: `Program.sessionAllowed` + `ApprovalFunc` can auto-answer a
  later `securityGate.Approve` without a prompt for that tool. Policy still runs
  first, so it cannot widen authority; it does change the human-in-the-loop
  frequency.
* **Proposal/ambiguity orchestration**: `cmd/ohgo.turnTransition` (app shell, not
  `tui`) composes `ClassifyPrediction`, `ProposeTransition`, `AssessProposal`, and
  the "no-signal choice" rule; `tui` presents and resolves it.
* **Recovery command gating**: `/undo` refuses while a turn runs
  (`tui/recovery.go`), not in `recovery`.
* **Session-switch policy**: which overlays/proposals must close on `/new` or
  resume (`adoptSession`), and that approval memory is cleared.
* **Live-mode presentation split** (`tui/live.go`) changes ownership of scrollback
  but not the state machine.

### 11.3 Rules that live in CLI wiring

* Which tools exist (order, sharing of `StructuralSource`/`BashTool`, artifact tool
  only with a store, task tool only at depth).
* Default budgets and their derivations (`budgetLimits`).
* The security gate's entire construction (`securityRequest`, `scopeFor`,
  `summarize`, `approvalFor`).
* The summarizer (compaction provider call + budget charge).
* Session creation/restore and authority reconciliation (`applySessionAuthority`).
* MCP/search/browser tool construction from config and the fail-closed
  config-error tools.

### 11.4 Command surface

`tui.Commands` is the single registry (workspace: `/ask`, `/fuzz`, `/build`;
control: `/new`, `/help`, `/clear`, `/model`, `/permissions`, `/sessions`, `/jobs`,
`/task`, `/audit`, `/theme`, `/connect`, `/disconnect`, `/status`, `/update`,
`/usage`, `/info`, `/doctor`, `/mcp`, `/diff`, `/commit`, `/undo`, `/redo`,
`/retry`, `/compact`, `/quit` and aliases). `LookupCommand` is also used by
`main.go` to keep CLI help text in sync.

---

## 12. Dependency Graph

Measured from `go list` (internal edges only; Tarjan SCC over the package graph
returned **no cycles**).

```text
presentation/application
    cmd/ohgo ────────────────┐
      │                      │
    tui ────────────────┐    │
      │                 │    │
application services    │    │
  startup   update      │    │
  commit    harness ────┘    │
      │                      │
control / domain             │
  workspace ──→ agent        │
  cognition ──→ agent        │
  task ──→ cognition,agent,budget,contract,project
      │                      │
runtime                      │
  agent ──→ ai,budget,contract,security,jsonschema,tokens
      │                      │
capability                   │
  tools ──→ agent,ai,ast,budget,contract,diff,edit,ignore,
            jsonschema,knowledge,mcp,sandbox,search,security,workspace
  mcp ──→ jsonschema,knowledge      search ──→ knowledge
      │
persistence / infrastructure
  session ──→ agent,ai,state,tokens,workspace
  recovery ─→ agent,ignore,state,vcs
  config ──→ state      creds ──→ state      catalog ──→ ai,state
  project ─→ vcs        prompt ─→ project     state, budget, tokens,
  diff, edit, shell, jsonschema, knowledge, security, contract (leaves)
```

**Fan-in (internal):** `ai` 12, `agent` 12, `contract` 8, `state` 8, `budget` 7,
`workspace` 6, `knowledge` 5, `project` 5, `jsonschema` 4, `security` 4,
`tokens` 4, `cognition` 4, `prompt` 4, `vcs` 4.

**Fan-out (internal):** `cmd/ohgo` 27, `tools` 15, `tui` 15, `agent` 6,
`cognition` 5, `session` 5, `task` 5, `recovery` 4, `commit` 3, `startup` 3.

Healthy directions:

* `contract`, `knowledge`, `jsonschema`, `budget`, `tokens`, `state`, `sandbox`,
  `ast`, `diff`, `edit`, `shell` are leaves (stdlib only or one tiny dependency).
* `agent` depends on vocabulary + kernel, never on `tools`, `workspace`,
  `cognition`, or `tui`. Tool capability is injected through `agent.Tool`;
  projection through a function field; streaming through `ai.StreamFn`.
* `security` imports only stdlib, so the kernel cannot depend on the engine it
  guards.
* `catalog → ai` is type-only; `ai → creds` is secrets-only.

Boundary concerns:

* `tools` imports `workspace` (artifact origin) — a capability package depending on
  the authority package. Acyclic, but it means the capability layer knows domain
  vocabulary.
* `cognition` and `task` import `agent` for concrete types (`Turn`, `ToolOutcome`,
  `TargetSnapshot`); changes to those types ripple into shaping and persistence.
* `tui` reaches into `workspace`, `cognition`, `agent`, `session`, `recovery`,
  `commit`, `exec`, `update`, `prompt` — a presentation package with the fan-out of
  an application layer.
* `cmd/ohgo` is the only package that imports everything; `run.go` (one file)
  contains options, tool building, loop building, harness backend, headless
  runner, REPL, TUI wiring, wizard, and history rotation.

Likely god packages: `cmd/ohgo` (especially `run.go` 2,884 lines / 105 KB,
`harness.go` 33 KB), `tui/app.go` (5,174 lines), `agent/loop.go` (1,500+ lines)
plus `agent/targets.go` (30 KB) and `agent/completion.go` (28 KB), `tools`
(16.4k lines across 28 files), `cognition/evidence.go` (33 KB) and
`cognition/shape.go` (27 KB).

---

## 13. Semantic Ownership Map

| Concept | Canonical representation | Owner | Readers | Writers |
|---|---|---|---|---|
| User request text | `string` on the turn | caller (`tui`/`cmd`) | contract builder, route, prompt | user |
| Routing intent | `workspace.Intent` | `workspace.Classify` | `RouteTurn`, `ProposeTransition`, shape, TUI chrome | nobody (pure function) |
| Completion objective | `contract.ObjectiveKind` | `workspace.BuildIntentContract` | `agent.EvaluateTaskCompletion`, `cognition.BuildShape` | contract builder |
| Completion contract | `contract.IntentContract` | `workspace` (build) / `task` (durable) | agent, cognition, TUI, task | contract builder; task controller persists |
| Semantic grounding | `contract.IntentGrounding` | `workspace/grounding.go` | loop admission, completion review, cognition state block | grounding builder |
| Action proposal | `contract.ActionProposal` | `agent.ProposalForCall` | `ValidateProposal`, audit | loop (from trusted tool metadata) |
| Human decision proposal | `workspace.Proposal` | `workspace.AssessProposal` | TUI, prompt block | proposal assessment |
| Authority transition recommendation | `workspace.TransitionProposal` | `workspace.ProposeTransition` | TUI, `cmd/ohgo.turnTransition` | prediction |
| Execution authority | `workspace.Workspace` | `workspace` policy; live value in `authorityGate` | security gate, options, harness, prompt, cognition shape | explicit switch only; session persistence |
| Authorization decision | `security.Decision` | `security.Policy.Evaluate` | loop, audit, completion | gate |
| Grant | `security.Grant` | `security.GrantStore` | policy | `securityGate.Approve` |
| Risk / scope | `security.Risk` / `security.Scope` | `security.ClassifyRisk` / `NormalizeScope` | policy, approval UI | gate (trusted inputs) |
| Budget grant | `budget.Budget` | `options` / `task.Controller` | loop, spawner, tools, projection | `Admit*` during execution; `Holder.Set` at run boundary |
| Turn | `agent.Turn` | `agent.Loop.Run` | completion, cognition, TUI, persistence | loop |
| Tool outcome | `agent.ToolOutcome` | loop `execute` | completion, cognition, TUI | loop |
| Mutation preview | `agent.MutationPreview` | `tools` / `securityGate.Approve` | approval UI | previewer tools |
| Mutation evidence | `agent.MutationEvidence` | intended: `tools` | (no production reader found) | `tools` (currently unused helper) |
| Verification evidence | `agent.VerificationEvidence` | `tools.VerificationFor*` | completion, cognition, subagent | bash / wait / child result |
| Evidence item/state | `cognition.EvidenceItem` / `EvidenceState` | `Projector` | projection, task checkpoint, completion review | `ObserveTurns`/`ObserveOutcome` |
| Task shape | `cognition.TaskShape` | `BuildShape` | candidates, state block, sufficiency | shape builder |
| Task | `task.Task` | `task.Controller` | TUI, prompt continuity, gate task ID | controller |
| Task checkpoint | `task.Progress` | controller | recovery on start | controller `persist` |
| Session | `session.Session` | `session.Store` | opener, browser, harness | store |
| Transcript | `session.StoredMessage` | `session.Store` | replay, UI | TUI/REPL `persistTo`, headless `persistTurn` |
| Edit checkpoint | `recovery.Checkpoint` | `recovery.Store` | undo/redo, inspection | controller |
| Workspace snapshot | `recovery.Tree` | recovery scanner | diff/restore | scanner |
| Repository symbols | `ast.Match` / `ast.Index` | `ast` | symbols/graph/refs, completion resolver | index build |
| Package graph | `ast.packageGraph` | `ast` | graph tool | index build |
| Call/reference graph | `ast.symbolGraph` | `ast` | refs tool | index build |
| Model facts | `ai.Model` | `catalog` | resolution, UI, loop | catalog/discovery |
| Settings/roles | `config.Settings` | `config` | options, harness, UI | `/model`, `/mcp`, theme, connect flows |
| Credentials | `creds.Store` | `creds` | `ai.Registry` | connect/disconnect |
| Project identity | `project.Identity` | `project` | startup, session, prompts | init gate/wizard |
| UI transcript | `tui.State.Entries` | `tui.State.Apply` | renderer | events |
| UI conversation mirror | `Program.history` | `Program` | loop request, retry/compact | turn completion, `/clear`, resume |
| Approval memory | `Program.sessionAllowed` | `Program` | `ApprovalFunc`, shell escape | human "Allow for entire session" |
| External evidence | `knowledge.Evidence` | `knowledge` | MCP/search/browser results | external tools |
| GRY evidence | `gryhook.Result` | `gryhook` | prompt block (plan only), audit log | hook subprocess |

---

## 14. Architectural Invariants

Each entry is demonstrably enforced in code and (where noted) pinned by a test.
Wording is deliberately narrow.

1. **Every model-proposed tool call crosses the authorizer before execution.**
   `agent/loop.go: execute` calls `l.authorize` immediately before `budget.Admit`
   and `tool.Execute`; `authorize` calls `Evaluate`, and on APPROVE `Approve` then a
   fresh `Evaluate` that must be AUTO. Tests: `TestAuthorizeAutoExecutes`,
   `TestAuthorizeDenyNeverExecutes`, `TestAuthorizeApproveThenGrantExecutes`,
   `TestAuthorizeChangedOperationAfterApprovalNeverExecutes`.

2. **An installed authorizer supersedes the legacy hooks.**
   `execute` branches on `l.cfg.Authorize != nil` and never consults
   `Admit`/`Approve` otherwise. Test: `TestAuthorizeSupersedesLegacyHooks`.

3. **Authority is checked before risk, scope before grants, in the policy kernel.**
   `security/policy.go: Evaluate`. Tests:
   `TestPolicyDeniesMutationWithoutWriteAuthority`,
   `TestPolicyDeniesUnpermittedCapability`, `TestPolicyHonoursCoveringGrant`,
   `TestPolicyDeniesOutsideWorkspace`.

4. **A grant covers exactly the approved operation once and cannot widen
   authority.** `security.Grant.Covers` + one-shot `GrantStore.Consume`; policy
   denies authority/scope violations before consulting a grant. Tests:
   `TestGrantCoversExactlyTheApprovedOperation`,
   `TestGrantDoesNotLeakAcrossOperations`,
   `TestGrantStoreConsumeIsOneShotAndConcurrent`.

5. **The model cannot supply capability, scope, risk, or authorization.**
   `securityRequest` derives everything from the tool interface, arguments, and
   config; unknown capability `""` is denied. Test:
   `TestSecurityRequestIgnoresModelSuppliedSecurityFields`.

6. **The live authority is read at call time, not captured at construction.**
   `securityGate.authorityInForce()` calls the injected `authority` function;
   `authorityGate` is the shared value. Test: `TestSwitchWorkspaceMovesTheLiveGate`.

7. **Subagents inherit the parent's authorizer and admission seam; they cannot
   widen authority.** `RunSubagent` copies `Admit`, `Approve`, `Authorize`,
   `Grounding`, `Budget`, `Deadline`, `MaxTurns`. Tests:
   `TestSubagentInheritsTheAuthorizer`, `TestSubagentInheritsLegacyAdmit`,
   `TestChildInheritsFuzzAuthority`.

8. **Delegation depth is structural.** `cmd/ohgo/run.go: childToolSet` omits the
   task tool at the cap; `buildTools` omits it entirely at `maxDepth <= 1`. Tests:
   `TestBuildToolsOmitsDelegationAtDepthOne`,
   `TestChildToolSetBoundsTheTreeStructurally`,
   `TestNestedDelegationRunsThreeLevels`.

9. **A read-only delegation request is enforced by the tool set, not a flag.**
   `newSubagentSpawner` filters via `readOnlyToolSet`; `RunSubagent` refuses if
   `IsReadOnly(tools)` is false. Tests:
   `TestRunSubagentRefusesAWriteToolUnderAReadOnlyRequest`,
   `TestIsReadOnlyIsFailSafe`.

10. **Delegation under a non-mutation grounding is forced read-only.**
    `loop.execute` sets `args["readOnly"]=true` for `ActionDelegate` when
    `!Grounding.MutationIntent`. Test:
    `TestLoopForcesDelegationReadOnlyUnderActionlessRequest`.

11. **Semantic admission precedes security authorization and can only refuse, never
    grant.** `execute` runs `ValidateProposal` before `authorize`; the assessment
    has `Pass`/`Review`/`Reject`. Tests:
    `TestLoopRefusesInventedMutationAtSemanticAdmission`,
    `TestValidateProposalAllowsObservationUnderAmbiguity`.

12. **Projection may narrow the request, never widen capabilities.** `loop.Run`
    intersects projected tools with constructed tools and resolves by name to the
    loop's instance; an empty projected message slice is ignored; audit fields are
    overwritten by the loop. Tests: `TestProjectionCannotAddTools`,
    `TestProjectionCannotSubstituteAToolWithTheSameName`,
    `TestEmptyProjectionFallsBackToLiveContext`,
    `TestProjectionDoesNotBypassAuthorizer`.

13. **Completion is evidence-derived, not model-declared.**
    `EvaluateTaskCompletion` is the sole production evaluator. Tests:
    `TestEvaluateCompletionRejectsMutationWithoutVerification`,
    `TestEvaluateCompletionRejectsMissingMutation`,
    `TestEvaluateCompletionRejectsUnexpectedMutation`,
    `TestHeadlessMutationRequiresVerification`.

14. **A mutation objective requires an observed target change and a verification
    after the last change.** `scanCompletionEvidence` + `AssessBehavior` +
    `CheckTargets`. Tests:
    `TestEvaluateCompletionRequiresVerificationAfterTheLastMutation`,
    `TestEvaluateCompletionRequiresVerificationAfterPotentialMutation`,
    `TestEvaluateCompletionRejectsMutationTaskThatOnlyRanTests`.

15. **A refusal is not a failed execution.** `ToolOutcome.Refused()` (candidate
    rejected or authorization DENY) is excluded from pending failures. Code path
    `scanCompletionEvidence`; covered indirectly by
    `TestAuthorizeDenyNeverExecutes` and the completion tests.

16. **Budget admission is atomic and precedes spending.** `budget.AdmitAll` checks
    all dimensions then increments; `Loop.Run` admits Turns+ModelCalls before
    `Stream`; `execute` admits ToolCalls after authorization. Tests:
    `agent/budget_test.go`, `budget/budget_test.go`.

17. **A recovery checkpoint is captured before a mutating batch is authorized;
    failure refuses the batch.** `Loop.dispatch` `BeginEdit` before goroutines;
    tests `TestJournalOpensBeforeTheMutatingBatchAndCommitsAtTheEnd`,
    `TestJournalFailureRefusesTheMutation`.

18. **Recovery restore is verified and refuses on mismatch.**
    `recovery.Controller.move` + `verifyRestore`. Tests:
    `TestUndoBlocksOnUserChangeAndTouchesNothing`,
    `TestRedoRestoresTheExactAgentState`, `TestFailedCaptureRefusesTheEdit`.

19. **A non-terminal task's contract is immutable; terminal states are absorbing.**
    `task.Controller.Begin` (DeepEqual), `task.canTransition`. Tests:
    `TestControllerRefusesDifferentContractOnResume`,
    `TestTerminalTaskIsNeverResumable`.

20. **The TUI task block is a projection, not execution state.**
    `tui.TaskProjection` doc + `TaskProjected` event only. Test:
    `TestTaskProjectionDoesNotChangeExecutionState`.

21. **An interrupted task is never restored as completed.**
    `Controller.Restore`/`NormalizeRecovered` normalize RUNNING → PAUSED. Test:
    `TestControllerRecoversCrashedTaskAsPaused`.

22. **MCP classification comes from configuration, and unknown fails closed to
    mutation.** `mcp.ServerConfig.Classify` (unknown default),
    `tools.MCPCallTool.SecurityCapabilityFor` (only `ClassExecute` lowers to
    external execute; everything else mutate). Tests:
    `TestMCPExecuteUsesExecutionAuthority`,
    `TestMCPUnknownClassFailsClosedToMutation`.

23. **Read-only execution is an OS boundary, not a text classification.**
    `security.Policy.Evaluate` sets `ReadOnly` from authority; `tools.BashTool`
    passes it to `sandbox.Command`; unsupported platform → refusal. Tests:
    `TestGateMarksFuzzExecReadOnly`, `TestGateMarksFuzzEscapeReadOnly`; `sandbox`
    tests.

24. **Repository coordinates and completion verification share one index.**
    `tools.StructuralSource` is constructed once in `buildTools`;
    `cmd/ohgo.structuralResolver` finds the same `*tools.SymbolsTool`. Tests:
    `cmd/ohgo/phase5_wiring_test.go`, `TestSymbolsToolSharesTheStructuralSource`
    (tools).

25. **Derived repository state never serves stale coordinates silently.**
    `ast.refreshGraphLocked` returns the rebuild error; `staleLockedCtx` treats
    inspection errors as stale. Tests: `ast/index_test.go`,
    `ast/index_stress_test.go`.

26. **The package graph is acyclic.** Verified by Tarjan over `go list` output; no
    SCC of size > 1.

27. **Startup is the sole creator of project-local state.** `startup.Ensure` is the
    only caller of `project.EnsureWith` in production; `runOnce` uses
    `bootProject` (read-only discovery) and refuses an uninitialized directory.
    Tests: `TestNonInteractiveNeverCreatesOrPrompts`,
    `TestExistingOhgoSkipsEntireInitialization`.

28. **A resumed session restores its recorded authority/model unless explicitly
    overridden.** `applySessionAuthority`. Tests:
    `TestCrossWorkspaceSessionKeepsAuthority`,
    `TestResumeSeedsHistoryFromTheStore`.

---

## 15. Protected Behavior

Selected invariants and the tests that pin them. This is not the full suite
(≈2,700 test functions; `tui` alone ≈1,096).

| Invariant | Implementation | Protecting tests |
|---|---|---|
| DENY never executes; approval must be re-confirmed | `agent/loop.go: authorize` | `agent/authorize_test.go` (all) |
| Authorizer supersedes legacy hooks | `execute` branch | `TestAuthorizeSupersedesLegacyHooks` |
| Legacy admit still inherited by children | `RunSubagent` copy | `TestSubagentInheritsLegacyAdmit`, `TestLoopAdmitBlocksTheCallBeforeApproval` |
| Policy order and grant one-shotness | `security/policy.go` | `security/security_test.go` (policy/grant group) |
| Scope normalization resists symlink escape | `security/scope.go` | `TestNormalizeScopeClassifiesTargets`, `TestNormalizeScopeIsCanonical` |
| Settings cannot widen authority; `--yes` cannot override DENY | `securityGate.Evaluate` | `TestGateSettingsCannotWidenAuthority`, `TestGateAutoApproveStillDenies` |
| Gate fails closed with no approver / unresolved authority | `securityGate` | `TestGateWithNoApproverFailsClosed`, `TestGateUnresolvedAuthorityDeniesEverything` |
| MCP execute is execution, not repository mutation | `SecurityCapabilityFor` / policy | `cmd/ohgo/mcp_authority_test.go` |
| Route matrix refuses cross-authority work | `workspace.RouteTurn` | `TestRouteTurnTransitionMatrix`, `TestAskRefusesExecutionWithoutCallingTheModel`, `TestBuildRequiresAnExplicitAskSwitch`, `TestFuzzRefusesExecutionWithoutTheModel` |
| Route never moves authority | `RouteTurn` | `TestRouteTurnNeverChangesTheActiveWorkspace`, `TestRefusedRouteLeavesAuthorityUntouched` |
| Prediction never escalates; strong evidence required | `ProposeTransition` | `TestProposeTransitionMatrix`, `TestProposeNeverEscalates`, `TestProposalThresholdRequiresStrongEvidence` |
| Transition is applied only through the authoritative switch | `cmd/ohgo`/`tui` | `TestAcceptedTransitionUsesTheAuthoritativeSwitch`, `TestRejectedTransitionLeavesAuthorityUntouched` |
| Intent contract extraction is finite and conservative | `workspace/contract.go` | `workspace/contract_test.go` (15 tests incl. bounds/ambiguity) |
| Grounding rejects invented mutation, allows bounded observation | `agent/grounding.go` | `agent/grounding_test.go` (admission + review groups) |
| Completion requires evidence; budget stops do not fabricate success | `agent/completion.go`, `budget_completion.go` | `agent/completion_test.go`, `cmd/ohgo/headless_outcome_test.go` |
| Cancellation is not success; deadline is PAUSED | `projectRun`, `turnTaskOutcomeWithReview` | `TestHeadlessCancellationIsNotSuccess`, `TestHeadlessDeadlineIsNotSuccess`, `TestTurnTaskOutcomePausesOnCancellation` |
| Journal opens before mutation, commits after cancel, refused on failure | `agent/loop.go: dispatch/Run` | `agent/journal_test.go` |
| Recovery verifies, refuses mismatch, repairs interruptions | `recovery/controller.go` | `recovery/recovery_test.go` (27 tests) |
| Recovery is not model-reachable | no tool registration | `cmd/ohgo/recovery_test.go: TestNoRecoveryToolIsRegistered`, `tui/recovery_test.go: TestRecoveryIsNotInTheToolSurface`, `TestModelTextCannotInvokeRecovery` |
| Task contract immutability and resumability | `task/controller.go` | `task/controller_test.go`, `task/postfreeze_test.go` |
| TUI task projection is display-only | `tui/task.go` | `TestTaskProjectionDoesNotChangeExecutionState` |
| Subagent isolation and bounds | `agent/subagent.go`, `cmd/ohgo/run.go` | `agent/subagent_test.go`, `cmd/ohgo/subagent_test.go` |
| Projection narrows only | `Loop.Run` intersect | `agent/projection_test.go` |
| Session replay round-trips and migration | `session/session.go` | `session/session_test.go`, `session/migration_test.go` |
| Symbol/graph/refs bounds and unresolved semantics | `ast` | `ast/index_test.go`, `graph_test.go`, `relationships_test.go` |
| Tool containment and argument validation | `tools/base.go`, each tool | `tools/*_test.go` (≈249 tests) |
| Startup creates nothing on cancel/non-interactive | `startup.Ensure` | `startup/startup_test.go` |
| Update is explicit, verified, locked | `update.Service/Installer` | `update/*_test.go` (70 tests) |

---

## 16. Coupling Map

| Coupling | Mechanism | Why it matters |
|---|---|---|
| `agent` → host via `Config` callbacks | `Admit`, `Approve`, `Authorize`, `BeforeToolCall`, `AfterToolCall`, `Project`, `SystemFn`, `OnTurn`, `OnToolProgress`, `OnTextDelta`, `Summarize`, `Journal`, `GetSteeringMessages`, `GetToolChoice`, `TransformContext` | The loop is a hub of 14 function seams, most installed after construction. A refactor that changes installation order (`SetX before Run`) breaks turns subtly. |
| `cognition` → `agent` concrete types | `Turn`, `Tool`, `ToolOutcome`, `ProjectionRequest/Result`, `TargetSnapshot` | Tool-name switches and `Details` map keys in `cognition/evidence.go` are an undocumented contract with `tools`. |
| `task` → `cognition` | `Progress.EvidenceState`, `Context()`, `RecordProjection` | Task checkpoints persist cognition's schema; changing `EvidenceState` changes the on-disk format. |
| `tui` → control/runtime | direct calls to `workspace.Classify`, `BuildIntentContract`, `agent.CaptureTargetsContext`, `agent.EvaluateTaskCompletion`, `cognition.BuildShape/NewProjector`, `recovery.Op*`, `session.Store` | Presentation layer owns orchestration; moving it requires matching `cmd/ohgo/outcome.go`. |
| `tools` → `workspace`/`security`/`mcp`/`search`/`sandbox`/`ast` | artifact origin, capability naming, external classification, verification, index | A capability package that knows five domain subsystems; tool construction lives in `cmd`, so the dependency is one-directional but broad. |
| `cmd/ohgo` → everything | 27 internal imports in one package | The single integration hub; `run.go` mixes parsing, wiring, orchestration, and persistence. |
| `session` ↔ `tui`/`cmd` | both append turns; different code | Two persistence implementations, selected by UI mode. |
| `securityGate` ← `authorityGate` | function closure resolved per call | Live authority flows through a callback; a stale or nil gate silently denies everything. |
| `gryhook.Install` → `Loop.SetBeforeToolCall/SetAfterToolCall` | setter overwrite | Any other host hook installed on the headless path would be replaced (only one writer today). |
| `recovery` ↔ `ignore` | shared matcher | If the two ever disagree on ignored paths, recovery may capture or restore files it should not. |
| `prompt` → `project` | reads `AGENTS.md` from project root | Prompt content depends on filesystem state, not configuration. |

---

## 17. Semantic Duplication / Boundary Overlap

These are observations, not recommendations.

1. **"Intent" is three concepts.** Routing intent (`workspace.Intent`), completion
   objective (`contract.ObjectiveKind`), and task-shape intent
   (`cognition.TaskShape.Intent`, a lowercased string). They are not the same: READ
   can still be informational; EXECUTE can be execution or mutation. `BuildShape`
   derives `Intent` from the kind when absent, so they can drift.
2. **"Proposal" is three concepts.** `contract.ActionProposal` (a tool call to
   validate), `workspace.Proposal` (a human decision about an unresolved design
   dimension), `workspace.TransitionProposal` (a recommended authority move). The
   TUI type `tui.ProposalResolution` holds the second. Names collide; semantics do
   not.
3. **"Authority" has two implementations of one table.** `workspace.Permits` is
   canonical; `cmd/ohgo.workspaceAuthority.Permits` delegates through
   `operationForCapability`, and `escapeAuthority` restates a small variant.
   `workspace.Authority.Admit` and `authorityGate.Admit` are a second adapter of the
   same check, used only by the legacy loop seam.
4. **"Completion" has three vocabularies.** `agent.CompletionState`
   (`COMPLETED`/`NEEDS_REVIEW`), `task.Status` (7 states), `cmd/ohgo.runState`
   (5 states incl. PAUSED/FAILED/CANCELLED). Mappings exist in
   `tui/completion.go` and `cmd/ohgo/outcome.go`.
5. **"Evidence" is computed twice.** `cognition.EvidenceState` (bounded, durable,
   for projection/sufficiency) and `agent.scanCompletionEvidence` (complete,
   in-memory, for the verdict). They observe the same turns with different
   retention and status rules.
6. **"State" of a turn exists in several places.** `agent.Turn`,
   `tui.State.Entries` + `Program.history`, `session` rows, `task.Progress`. Only the
   session is the transcript authority; only `progress.json` is the task authority;
   the TUI mirror is disposable.
7. **"Contract" exists at three stages of one type.** `contract.IntentContract` is
   aliased into `workspace` (same Go type), embedded in `task.Task.Contract`, and
   copied into `task.Progress.Contract`. The progress copy is authoritative on load;
   the task.md projection does not carry it.
8. **Two mutation representations.** `agent.MutationPreview` and
   `agent.MutationEvidence` have the same alias fields (`Operation`,
   `MutationType`, `Type`, `Op` + `Diff`, `Delta`, `Content`, `Text`). `Approval`
   duplicates `Preview`/`Diff` and `MutationPreview`/`PreviewMutation`. This is
   compatibility scaffolding, not redundancy to collapse casually.
9. **Two "recovery" notions.** Workspace edit recovery (`recovery`) vs task
   continuity recovery (`task.Restore`, `NormalizeRecovered`). They have different
   sources of truth and different guarantees.
10. **Two session-recovery paths.** `--resume` at process start
    (`applySessionAuthority` restores workspace/model) vs mid-session `/sessions`
    resume (`adoptSession` only replaces history/entries, leaves authority/model/gate
    binding alone).
11. **Read-only tool knowledge is duplicated.** `agent.ReadOnlyToolNames` (name
    allow-list) vs `agent.Tier` / `ActionClass` metadata. The allow-list omits `ls`,
    `webfetch`, `artifact`, `mcp_call`, so a read-only child loses some read
    capabilities.
12. **Tool surface knowledge is duplicated.** `cmd/ohgo.buildTools` names the tools;
    `cognition` special-cases tool names (`web_fetch`, `search`, `browser`, `mcp_*`,
    `read`, `bash`, …); `tui` special-cases `todo`, `bash` (jobs), `symbols`
    (resolver). No registry.
13. **Trigger/mode vocabulary is duplicated.** `workspace.Workspace` vs
    `config.Role` (first three mirror, two extra); `tui` maps workspace names to
    display modes and to `workspace.Parse`.
14. **Project path knowledge is duplicated.** `state.Dir` (machine),
    `project.DirName` (project), `task.Store.Dir`, `recovery.StorePath` all join
    `.ohgo`/state roots; only some go through `state.Path`.
15. **`prompt.ToolCatalog` is dead**, and `tools.MutationEvidence` has no reader
    (`var _ = evidenceFromPreview`), so "duplicated" API exists without a consumer.

---

## 18. Refactoring Hazards

Each hazard states the mechanism by which a structural change could silently alter
semantics.

1. **Two orchestrators for one turn.** `tui/app.go: runTurnWithChoice` and
   `cmd/ohgo/outcome.go: runHeadlessTurnAs` independently perform contract
   extraction, target capture, shape/projection installation, grounding
   installation, run, and evaluation. A change to one (e.g. adding a completion
   input, changing snapshot recapture rules) will diverge the other. The REPL shares
   the headless one; only the TUI is separate.

2. **Human mutation paths outside the authorizer.** `!` escape, `/undo`, `/redo`,
   `/commit`, `/update` mutate state without `securityGate`. Consolidating "all
   mutation goes through `securityGate`" would either break these commands or
   silently require approvals the product never asked for. Conversely, removing them
   from the list of "authority paths" hides real capability.

3. **Callback installation order is load-bearing.** `SetAuthorizeHook`,
   `SetAdmitHook`, `SetApprovalHook`, `SetProjectionHook`, `SetSystemContext`,
   `SetGrounding`, `SetJournal`, `SetBudget`, `SetModel`, `SetReasoning`,
   `SetTurnHook` are all documented "must be called before Run". Turning them into
   constructor-only fields is safe only if every host is changed together;
   installing one after the loop starts has no guard.

4. **`Authorize` silently disables legacy `Admit`/`Approve`.** Production sets both
   `Admit` and `Authorize` in the subagent parent config (`cmd/ohgo/run.go`), but the
   loop ignores `Admit` when `Authorize` is non-nil. A refactor that "simplifies" the
   branch could re-enable workspace admission *in addition to* policy (double denial)
   or drop the authorizer (open path).

5. **`securityGate` derives capability from the tool declaration.**
   `capabilityForTier(tool.Approval().Tier)` means changing any tool's
   `Approval.Tier` changes authorization (e.g. `artifact` is read so it is
   auto-approved; `mcp_call` defaults to write so it needs BUILD). Moving tier
   metadata to a registry is safe only if `CapabilityNamer`/
   `InvocationCapabilityNamer` overrides (`web_fetch`, `search`, `browser`,
   `mcp_*`) move with it.

6. **`OperationForTool` is used twice.** Both `workspace.Authority.Admit` and
   `securityRequest` call it for `Mutates`. A change to `CapabilityReporter` handling
   changes both authorization risk and completion mutation evidence.

7. **`ReadOnly` is computed from authority, then enforced by tools.**
   `security.Policy` sets `ReadOnly`; `tools.BashTool` uses it for the sandbox and to
   suppress `Mutates`; `web_fetch`/MCP tools may not honour it. Changing where
   `ReadOnly` comes from changes both the OS sandbox and completion evidence. A tool
   that ignores `ReadOnly` is a boundary hole that tests only cover for bash/shell.

8. **`Result.Mutates` is tool-declared.** Completion trusts it (or the
   authorization's `Mutates`). A refactor that derives mutation from tool name
   instead of `Result.Mutates` will miss `bash` redirects and may count reads as
   mutations.

9. **Command classification is a heuristic, documented as such.**
   `tools/capability.go` is not the mutation boundary. Moving classification into
   `security` would misleadingly promote a risk input to a policy decision; moving it
   later would change FUZZ/BUILD admission and completion evidence simultaneously.

10. **Projection is applied per request but evidence is observed per turn.**
    `Projector.Project` calls `Evidence.ObserveTurns(shape, req.Turns)`; if a
    refactor projects only once, or reuses a projector across turns without the
    durable `EvidenceState`, evidence retention and sufficiency change silently (and
    `progress.json` format changes).

11. **Tool set is intersected by name.** `intersectTools` resolves to the loop's own
    instances by name. Renaming a tool or introducing duplicate names changes which
    instance executes and which schema the model saw.

12. **`cognition` special-cases tool names and `Details` keys.** `ObserveOutcome`
    switches on `read`, `ls`, `glob`, `grep`, `symbols`, `write`, `edit`, `delete`,
    `bash`, `web_fetch`, `search`, `browser`, `mcp_*`; `addRead` reads
    `totalLines/offset/limit`; `addBash` reads command/exit/verification. Renaming a
    tool or changing `Result.Details` silently downgrades evidence and completion.

13. **Task checkpoint schema is a wire format.** `progress.json` embeds
    `contract.IntentContract`, `agent.TargetSnapshot`, `cognition.EvidenceState`,
    `budget.State`. Moving any of those types (e.g. out of `agent`) changes JSON keys
    and invalidates resumes; version bumps are required but not automated.

14. **Contract immutability uses `reflect.DeepEqual`.** Adding a
    non-comparable/derived field to `IntentContract` or changing slice ordering can
    make a legitimate resume look like a different objective
    (`ErrObjectiveConflict`), or vice versa.

15. **`progress.json` outranks `task.md`.** Any refactor that treats `task.md` as
    authoritative (it is described as human/model-readable) would lose the
    contract/snapshot/evidence and make resume impossible or wrong.

16. **Task ID and session ID are separate identities.** `task.Task.ID` is minted by
    `crypto/rand`; sessions use SQLite autoincrement. `securityGate` binds grants to
    `taskID` (current controller) and `sessionID` (set once at `buildLoop`). A
    mid-session resume does **not** call `SetSession`/`SetTaskSource` again, so grant
    provenance can name the old session. Grant matching ignores both IDs today
    (`Grant.Covers`), so this is audit-visible, not authorization-visible — but a
    future "bind grant to session" change would be silently inconsistent.

17. **Authority has three mirrors.** `options.workspace`, `harness.workspace` (a
    label string), `authorityGate`. `SwitchWorkspace` updates all three plus the
    session row; `resolveOptions` sets the first two; `activeWorkspace()` defaults an
    unknown label to BUILD. A refactor that removes one mirror must update
    `h.workspace` parsing, TUI `TurnAuthority.Active`, and the session snapshot
    together.

18. **Default-to-BUILD on unknown authority in two places.** `options.authority()`
    and `harness.activeWorkspace()` both fall back to BUILD when the value is
    unset/invalid, while `workspace.Parse` and `security.Policy` fail closed.
    Removing the fallback changes behaviour for tests and embedders.

19. **Headless shape authority is chosen from the route, not the gate.** `runOnce`
    passes `route.Active`; the direct-call fallback picks `ask` for informational. If
    the gate ever diverges from the route (a bug or a new entry point), the state
    block will describe an authority that is not enforced.

20. **`workspace.Artifact` origin is captured at tool construction.** `buildLoop`
    appends `tools.NewArtifactTool(..., opts.authority())`; a later `/build` switch
    does not rebuild the tool, so artifact kind checking compares against the
    *initial* workspace. This is exercised in fuzz-finding tests but not across a
    switch.

21. **Event bus is lossy.** `tui.publishOK` drops on a full 256-event buffer.
    `AssistantDelta` and `ToolFinished` can be lost; the completed turn is
    re-published at the end, but tool blocks are only published through
    progress/final events. A refactor that relies on events for persistence would
    lose data.

22. **Persistence differs by mode.** TUI persists user+assistant+toolResult per turn;
    plain REPL persists the user message separately and assistant/tool results via
    `OnTurn`; `ohgo run` persists assistant/tool results but **not** the user prompt.
    Unifying persistence changes stored transcripts and resume behaviour.

23. **Resume authority is restored only at process start.** `adoptSession` does not
    call `applySessionAuthority`, `security.SetSession`, or rebuild the loop; a
    mid-session resume can run under the current workspace/model while the session
    row records another. Any refactor that assumes session authority is always live
    is wrong.

24. **Approval memory is UI state that suppresses prompts.** `Program.sessionAllowed`
    is read by `securityGate.Approve` through the `ask` callback. Moving approval
    memory into the gate changes audit/behaviour; clearing it in the wrong lifecycle
    leaks or loses convenience.

25. **The TUI cancellation contract is an atomic CAS.** `turnTerminal` decides PAUSE
    vs COMPLETE vs CANCELLED; `beforeTaskTransition` is a test seam. Reordering the
    post-run evidence read, task transition, and CAS changes whether a cancel wins
    over a verified completion.

26. **Compaction runs outside the turn count.** `compactIfNeeded` restarts the loop
    without incrementing turns; the summarizer charges ModelCalls in production but
    is uncharged if a host supplies a nil holder. Moving compaction into the provider
    layer or making it a turn would change turn budgets and `ErrTurnLimit` semantics.

27. **`ErrTurnLimit` is conditional.** `explicitBudget` changes the error from
    `ErrTurnLimit` to `*budget.ExhaustedError`; callers distinguish PAUSED reasons.
    Changing `New`'s default-grant creation changes exit codes and task transitions.

28. **Provider-specific behaviour is in `ai`, not `agent`.** `ProviderFor` caches by
    provider+baseURL; `anthropic` bearer-token fallback; `openrouter` extra headers;
    `ollama` base URL normalization; OpenAI finalize errors abort the stream. A
    provider refactor can change whether a malformed tool call is recoverable.

29. **`ai.DrainStream` ignores `EventError` in the loop body.** The error surfaces
    only from `Stream.Result()`. A refactor that returns early on `EventError` would
    change cancellation/error ordering.

30. **Session replay is lossy by design.** Thinking blocks, images, and UI-only
    messages are not persisted; a resumed conversation is not byte-identical. Any
    feature that depends on reasoning continuity would break silently.

31. **`ast` rebuilds the whole index on any change.** No incremental path; a
    "performance" refactor that adds one changes freshness/coherence guarantees and
    the "never serve stale coordinates" invariant.

32. **`ast` and `ignore` scope must agree.** Grep/glob use `internal/ignore`;
    recovery uses the same matcher with `enabled=true`. Divergence changes what the
    model sees vs what undo restores.

33. **`gryhook.Install` overwrites the loop's tool hooks.** Only the headless path
    installs it today; a second consumer would silently replace it.

34. **Settings approval is a policy shortcut, not authority.**
    `securityGate.Evaluate` evaluates the base policy first and only then applies
    `settings` allow/deny. Reordering this makes `autoApprove`/`Approvals` able to
    widen authority (currently tested against).

35. **`sandbox` availability is environment-dependent.** On non-darwin/non-linux
    platforms, read-only execution is refused. Refactoring bash to "fall back" would
    create an unguarded path.

---

## 19. Unknowns

* **Runtime platform behaviour of `sandbox`.** The code refuses when
  `sandbox-exec`/`bwrap` is unavailable, but whether either is present and
  functional on the user's machines is environment-dependent (not provable from
  source). `sandbox.ReadOnlySupported` is the only signal.
* **Whether every provider's `finalize` treats malformed tool JSON identically.**
  OpenAI aborts the stream; Anthropic and Ollama were not fully traced for the same
  path (Scout).
* **Exact behaviour of `catalog` live discovery under partial failure** (cache
  staleness, provider outages) beyond what `SnapshotAll` reports; `DynamicProvider`
  rosters are read from cache with a stale flag.
* **Whether `mcp` configured `Tools` maps are expected to enumerate every advertised
  tool.** Unknown classification fails closed, but the operational expectation is
  documentation, not code.
* **Whether the `bench` harnesses use the same projection/authority wiring as
  production.** They import production packages, but their host code was only
  sampled.
* **All semantics of `commit.Service` beyond ordering** (fingerprint/staged-state
  revalidation, repair rules) — sampled, not audited.
* **All semantics of `update`** (signature/checksum policy, transition
  acknowledgement) — verified by code but not exhaustively read.
* **Whether `tui`'s lossy event bus ever loses a `ToolFinished` in practice** —
  depends on burst rates; the drop is intentional.
* **Whether any host outside this repo consumes `agent.Config`** — the API supports
  it; no such host is in-tree.
* **Whether the persisted `session.Model` is meant to be authoritative over
  `settings.Roles` on resume** — code prefers the session unless a flag is passed,
  but the product intent is not stated in code.

---

## 20. Refactor Baseline

### What must remain unchanged

1. **The authority sequence inside `agent.Loop.execute`**: validation → hook →
   grounding → authorization → cancellation → budget → execute → post-hook. Any
   reordering changes who can execute what.
2. **Policy order in `security.Policy.Evaluate`**: capability known → authority
   permits → mutation requires write → filesystem scope → `ReadOnly` derivation →
   grant → risk. Grants must remain one-shot and unable to widen authority.
3. **Live authority resolution**: `authorityGate` read at call time; explicit switch
   only; unknown/unresolved denies.
4. **Subagent inheritance**: `Authorize`/`Admit`/`Grounding`/`Budget`/`Deadline`/
   `MaxTurns` copied; depth baked into the tool set; read-only enforced by tool set
   plus `RunSubagent` refusal.
5. **Completion rules**: evidence-derived; mutation requires observed target change
   and later verification; refusals are not failures; budget stop never fabricates
   success.
6. **Checkpoint-before-mutation** and verified restore/refusal semantics in
   `recovery`.
7. **Task contract immutability, terminal absorption, and `progress.json` >
   `task.md` precedence.**
8. **Projection narrowing only** (intersection by name with loop-owned instances;
   empty projection ignored; audit fields owned by the loop).
9. **One `ast.Index` per session, shared by symbols/graph/refs/completion, rebuilt
   on staleness, never serving stale coordinates.**
10. **Trusted derivation of security request fields** (capability, scope, risk,
    mutation) — never from model-supplied fields.
11. **`startup.Ensure` as the only project-local creator**, and `bootProject` staying
    read-only.
12. **No import cycles**, and `agent` never importing
    `tools`/`workspace`/`cognition`/`tui`.

### Boundaries that currently exist

* `agent` ↔ hosts: 14 callback seams; construction order is a contract.
* `security` ↔ runtime: kernel is stdlib-only, authority injected.
* `tools` ↔ runtime: `agent.Tool` + optional reporter interfaces
  (`ActionClassifier`, `CapabilityNamer`, `InvocationCapabilityNamer`,
  `CapabilityReporter`, `MutationPreviewer`, `MutationReporter`, `LanguageReporter`,
  `StructuralResolver`).
* `cognition` ↔ runtime: `agent.ProjectionRequest/Result` callbacks;
  `EvidenceState` persisted by `task`.
* `task` ↔ UI: `tui.TaskHooks` (no `task` import in `tui`).
* `session` ↔ UI: direct `session.Store` use in both shells (no hook seam).
* `recovery` ↔ UI: `tui.Recoverer` interface.
* Provider ↔ runtime: `ai.StreamFn`.

### Behaviors protected by tests

The 28 invariants in [§14](#14-architectural-invariants) and the table in
[§15](#15-protected-behavior). The highest-risk ones to preserve are: authorizer
supersession, deny/approve/confirm sequence, grant narrowness, subagent inheritance
and depth, grounding rejection of invented mutation, journal-before-mutation,
recovery refusal on mismatch, task contract immutability, projection narrowing, and
completion evidence rules.

### Areas requiring special care

* **Duplicated turn orchestration** (`tui/app.go` vs `cmd/ohgo/outcome.go`): any
  change must be applied to both or unified deliberately, with tests in
  `tui/completion_test.go` and `cmd/ohgo/headless_outcome_test.go` kept in
  agreement.
* **Human mutation paths** (`!`, `/undo`, `/redo`, `/commit`, `/update`): they are
  intentionally outside the tool authorizer; do not "simplify" them into it without
  deciding the product policy.
* **Authority mirrors** (`options.workspace`, `harness.workspace`, `authorityGate`,
  `session.Workspace`, `securityGate.sessionID`): unify only with a single-source
  design and tests for resume, switch, and mid-session session adoption.
* **On-disk schemas**: `progress.json` (contract/snapshot/evidence/budget),
  `sessions.db` (messages/sessions/artifacts), `recovery.db`
  (trees/blobs/checkpoints/pending/inflight). Versioning is manual; type moves
  change wire formats.
* **Tool-name and `Details`-key coupling in `cognition`**: renaming tools or detail
  keys silently degrades evidence and completion.
* **Read-only semantics**: `ReadOnly` from policy, OS sandbox for bash/escape,
  `ReadOnlyToolNames` for subagents — three mechanisms; keep them consistent or
  replace all at once.
* **Compaction/turn/budget interaction**: `explicitBudget`, `ErrTurnLimit`,
  summarizer charging, and deadline-as-nil-error are all observable through exit
  codes and task status.
* **Phase-derived compatibility code**: alias fields on mutation/approval types,
  legacy `Admit`/`Approve`, `workspace` aliases, `prompt.ToolCatalog`, `ModelFn`,
  `TransformContext`, `GetToolChoice`, `GetSteeringMessages`,
  `tools.MutationEvidence`. Removing them is a public-API and test-surface change,
  not a cosmetic cleanup.

---

## Appendix A — Documentation vs Implementation Mismatches

These are reported, not reconciled.

1. **`docs/architecture/WORKSPACE_AUTHORITY.md`** states
   `BUILD + READ → ASK semantics, BUILD remains active`,
   `FUZZ + READ → ASK semantics, FUZZ remains active`, and references a
   `workspace.Route.Indicator` and `BUILD · ASK / READ` composer form.
   *Implementation:* `workspace.RouteTurn` **refuses** `BUILD+READ` and `FUZZ+READ`
   with `Required = ASK`; `Route` has no `Indicator` method; `tui` blocks the turn
   (`turn.Blocked`). Pinned by `workspace/intent_test.go:
   TestRouteTurnTransitionMatrix` (`{"BUILD+READ", Build, "why does this work?",
   false, Ask}`) and `cmd/ohgo/authority_route_test.go:
   TestBuildRequiresAnExplicitAskSwitch`. `docs/architecture/ARCHITECTURE.md`
   repeats the same claim ("BUILD and FUZZ answer an informational READ turn with
   ASK semantics for that turn only").

2. **`docs/architecture/STATE_AND_PERSISTENCE.md`** says the `ast` package is "a
   stateless per-file searcher … no index, no cache, and no `graph.db`", and that
   derived code intelligence is "stateless today".
   *Implementation:* `ast.Index` is an in-process repository index with symbol
   coordinates, a package graph, a static reference/call graph, freshness checks
   and cancellation (`ast/index.go`, `graph.go`, `relationships.go`);
   `tools.StructuralSource` shares one instance.

3. **`docs/architecture/STATE_AND_PERSISTENCE.md`** project-scope table lists only
   `project.yml`.
   *Implementation:* `<root>/.ohgo/` also holds `task.md`, `progress.json`, and
   `tasks/<id>/…`, and the machine scope holds
   `recovery/<repo-hash>/recovery.db`. None appear in that table.

4. **`docs/architecture/ARCHITECTURE.md`** describes the turn as "Approve each call
   (settings policy → hook → default deny; `--yes` skips)".
   *Implementation:* policy runs first; `--yes` skips only the human prompt; a DENY
   is never overridden (`securityGate.Approve`,
   `TestGateAutoApproveStillDenies`). Settings `allow` also evaluates the base
   policy first (`TestGateSettingsCannotWidenAuthority`).

5. **`docs/architecture/ARCHITECTURE.md`** layer diagram annotates `tools → agent`
   as "(subagent spec only)" and `workspace → agent` as "(tool admission only)".
   *Implementation:* `tools` imports `agent`, `ai`, `ast`, `budget`, `contract`,
   `diff`, `edit`, `internal/ignore`, `jsonschema`, `knowledge`, `mcp`, `sandbox`,
   `search`, `security`, `workspace` (15 internal edges); `workspace` imports
   `agent` and `contract`.

6. **`docs/architecture/ARCHITECTURE.md`** says there are 379 models / 12 providers.
   *Implementation:* `catalog/catalog.json` has **13** providers and 379 model rows
   (5 of them dynamic-discovery providers).

7. **`docs/architecture/ARCHITECTURE.md`** subagent section says the child inherits
   "stream, model, tools, budget, approval, compaction, summarizer".
   *Implementation:* it also inherits `Admit`, `Authorize`, `Grounding` (from spec),
   `Deadline`, `MaxTurns`, `BeforeToolCall`, `AfterToolCall`, and drops `OnTurn`,
   `OnToolProgress`, `TransformContext`, steering, `GetToolChoice`, journal.
   "Approval" understates the authority inheritance.

8. **`docs/architecture/ARCHITECTURE_CONTRACT_V1.md`** is marked historical and
   already carries a Phase 7 note that `Config.Admit` is legacy. The body still
   describes admission as `Authority.Admit`; production uses `Config.Authorize`.
   The note is accurate; the body is stale.

9. **`docs/architecture/ARCHITECTURE.md`** says "nothing in `agent` constructs
   HTTP" — correct — and "nothing outside `creds` reads a provider credential from
   env or shell" — `ai.Registry` reads `os.Getenv` for `ResolveAPIKeySource` and
   `ANTHROPIC_AUTH_TOKEN`, and `mcp` reads env for headers; the claim is true for
   *storage/resolution policy* but not literally for env reads.
