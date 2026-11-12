# OHGO Architecture Map

> **Status:** authoritative description of the package topology **after** the
> `internal/` migration.
> **Companion documents:** `OHGO_ARCHITECTURE_RULES.md` (the permanent
> development contract) and `OHGO_ARCHITECTURE_MIGRATION.md` (what moved and
> how it was proven safe).
> **History:** the pre-migration tree and the behaviour-preservation proof are
> recorded in `OHGO_ARCHITECTURE_MIGRATION.md`. Superseded architecture
> snapshots are archived outside the active documentation set.
> **Behavioural detail:** `docs/architecture/` explains runtime behaviour,
> authority, persistence, intent grounding, and MCP. Those documents defer
> topology and ownership to this map.
> **Enforcement:** `internal/architecture/rules_test.go`. Every rule marked
> **\[ENFORCED]** fails `go test ./...` when broken.

---

## 1. The shape, bottom to top

```
                        cmd/ohgo                       entry point, wiring, package main
                            │                          (the only package main outside bench/scripts)
        ┌───────────────────┴───────────────────┐
        │                                       │
   internal/tui                          internal/{commit,startup,update,
   (+ tui/design, tui/diff)               history,gryhook}        application services
        │                                       │
        └───────────────┬───────────────────────┘
                        │
   ┌────────────────────┴─────────────────────┐
   │  continuity   session · task · recovery · memory      durable state
   │  experience   incidents · lessons · promotion         advisory learning
   └────────────────────┬─────────────────────┘
                        │
   ┌────────────────────┴─────────────────────┐
   │  execution    tools · mcp · exec · sandbox · shell    capabilities
   └────────────────────┬─────────────────────┘
                        │
   ┌────────────────────┴─────────────────────┐
   │  control      workspace · cognition                   intent, authority, shape
   │  runtime      agent                                   the turn engine
   └────────────────────┬─────────────────────┘
                        │
   ┌────────────────────┴─────────────────────┐
   │  provider     ai · catalog · creds                    wire, model facts, secrets
   └────────────────────┬─────────────────────┘
                        │
   ┌────────────────────┴─────────────────────┐
   │  foundation    contract · budget · security · state · config · project
   │                ast · ignore · knowledge · search · jsonschema · tokens
   │                diff · edit · vcs · prompt · promptopt · external knowledge
   └──────────────────────────────────────────┘
```

Arrows point **upward in privilege, downward in dependency**. A package may
import packages below it. Nothing imports packages above it. The compiler
guarantees acyclicity; the rules in §6 guarantee *direction*.

The single most important structural fact: **`internal/agent` is a closed
kernel.** It imports exactly six module packages and nothing else. Everything
else in the system is either a dependency of the kernel (vocabulary, provider
seam, authorization kernel) or a consumer of it.

---

## 2. Semantic domains

A domain is a group of packages that share a *decision*, not a directory-name
theme. Fifteen domains survive that test. Most have one package; that is a
result, not a gap — inventing a shared folder for `vcs` and `prompt` would
create a boundary that does not exist.

| Domain | Packages | Owns | Does **not** own |
|---|---|---|---|
| **runtime** | `agent` | The turn loop, tool dispatch, the authorization sequence, grounding admission, completion evaluation, subagent execution, the projection seam, the edit-journal seam | Tool implementations, workspace policy, persistence, rendering |
| **control** | `control/contract`, `control/budget`, `control/workspace`, `control/cognition`, `security` | Completion vocabulary; resource admission; authority policy + routing + intent contracts + transition decisions; task shape + evidence + narrowing; the authorization kernel | Execution, persistence, presentation |
| **execution** | `execution/tools`, `execution/mcp`, `execution/sandbox`, `execution/shell`, `execution/exec` | Concrete capabilities; MCP transport + classification; the OS read-only boundary; shell-output decoding; the human `!` escape | The decision to allow an operation |
| **intelligence** | `intelligence/ast`, `intelligence/ignore` | One in-process Go structural snapshot (symbols, packages, files, tests, entrypoints, refs) and repository scoping | Anything about the agent, models, or UI |
| **provider** | `provider/ai`, `provider/catalog`, `provider/creds` | Wire protocols and streams; canonical stream normalization (native and serialized tool calls); model facts and discovery; credential resolution | Policy, routing, cost decisions, execution authority |
| **continuity** | `continuity/session`, `continuity/task`, `continuity/recovery`, `memory` | The durable transcript; the durable task checkpoint; the edit history; the project-memory trust boundary | What should be recorded (that is control-plane) |
| **experience** | `experience` | Verified execution experience: deterministic incidents, lessons, candidate strategies, validation, promotion, scoped store, and advisory path advice | Authority, evidence, completion, task lifecycle, transcript, tool execution |
| **external** | `external/knowledge`, `external/search` | The external-evidence envelope; the web-search client | Repository knowledge |
| **configuration** | `config`, `state`, `project` | Machine settings; the single `~/.ohgo` path owner; project identity | Runtime authority (that is `workspace` + the gate) |
| **vocabulary** | `jsonschema`, `tokens`, `diff`, `edit` | Argument schemas; token estimation; text diff; fuzzy edit | Any consumer's policy |
| **vcs** | `vcs` | Git primitives | Whether a commit is allowed |
| **prompt** | `prompt`, `promptopt` | System prompt text, `AGENTS.md` discovery, mention context; deterministic prompt compression and title sanitization | Deciding what to do with it, model calls |
| **application** | `commit`, `startup`, `update`, `history`, `gryhook` | The `/commit` workflow; the initialization gate; the updater; durable prompt history and ephemeral oversized-prompt scratch; GRY lifecycle hooks | Execution authority |
| **presentation** | `tui`, `tui/design`, `tui/diff` | State machine, renderer, command registry, live mode, canonical design tokens, diff inspector | Task lifecycle, completion, authority |
| **governance** | `architecture` | The machine-checked manifest and boundaries | Production behaviour (it has no non-test code) |

---

## 3. Package ownership table

`May import` lists what the package is *allowed* to reach; `Must not` lists the
edges that are architecturally forbidden (the ones enforced today are marked).

| Package | Owns | May import | Must not |
|---|---|---|---|
| `internal/agent` | the engine | `control/{contract,budget}`, `security`, `provider/ai`, `tokens`, `jsonschema` — **exactly these** | `control/workspace`, `control/cognition`, `execution/*`, `continuity/*`, `tui`, `commit`, `prompt`, `catalog`, `ast` **\[ENFORCED]** |
| `internal/security` | authorization kernel | stdlib only **\[ENFORCED]** | every module package |
| `internal/promptopt` | deterministic prompt/title text transforms | stdlib only **\[ENFORCED]** | every module package |
| `internal/control/workspace` | authority + routing | `agent`, `control/contract` | `execution/*`, `continuity/*`, `tui` **\[ENFORCED]** |
| `internal/control/cognition` | shape + evidence | `agent`, `control/{contract,budget}`, `provider/ai`, `external/knowledge` | `execution/*`, `continuity/*`, `tui` **\[ENFORCED]** |
| `internal/control/contract` | completion vocabulary | stdlib only **\[ENFORCED]** | every module package |
| `internal/control/budget` | resource admission | stdlib only **\[ENFORCED]** | every module package |
| `internal/execution/tools` | capabilities | `agent`, `control/*`, `security`, `provider/ai`, `intelligence/*`, `execution/{mcp,sandbox}`, `external/*`, `jsonschema`, `diff`, `edit` | `continuity/*`, `tui` **\[ENFORCED]** |
| `internal/execution/mcp` | MCP client | `jsonschema`, `external/knowledge` | `agent` *(it does not import it today; classification is data, not policy)* |
| `internal/execution/sandbox` | OS boundary | stdlib only **\[ENFORCED]** | every module package |
| `internal/intelligence/ast` | the Go index | stdlib only **\[ENFORCED]** | `agent`, `control/*`, `execution/*`, `provider/*`, `tui` **\[ENFORCED]** |
| `internal/intelligence/ignore` | repo scoping | stdlib only **\[ENFORCED]** | every module package |
| `internal/provider/*` | wire, facts, secrets | `internal/provider/*`, `state` — **only** | `agent`, `control/*`, `execution/*`, `continuity/*`, `intelligence/*`, `tui` **\[ENFORCED]** |
| `internal/continuity/session` | transcript | `agent`, `provider/ai`, `state`, `tokens`, `control/workspace` | `tui`, `commit`, `startup`, `update`, `history` **\[ENFORCED]** |
| `internal/continuity/task` | durable task | `agent`, `control/{budget,cognition,contract,workspace}`, `project` | `tui` **\[ENFORCED]**; also `execution/*` by direction |
| `internal/continuity/recovery` | edit history | `agent`, `intelligence/ignore`, `state`, `vcs` | `tui`, `commit`, … **\[ENFORCED]** |
| `internal/experience` | verified execution experience | `agent`, `control/{budget,cognition,contract}`, `jsonschema`, `provider/ai` — **exactly these** | `execution/*`, `continuity/*`, `tui`, application services **\[ENFORCED]** |
| `internal/configuration` (`config`, `state`, `project`) | settings, paths, identity | `state`, `vcs` | `agent`, `execution/*`, `tui` |
| `internal/tui` | presentation | the whole tree **except** concrete capabilities and the task controller | `execution/tools`, `execution/mcp`, `continuity/task` **\[ENFORCED]** |
| `internal/{commit,startup,update,history,gryhook}` | application services | everything below them | being imported by anything below them **\[ENFORCED]** |
| `internal/architecture` | governance | nothing (test-only) | every production package |
| `cmd/ohgo` | composition | everything | being imported by anything **\[ENFORCED]** |

---

## 4. Boundaries that are architectural

### 4.1 Control plane ↔ execution plane

The control plane decides; the execution plane acts. `agent` owns the sequence

```
proposal → validation → host hook → semantic admission → authorization
        → cancellation re-check → budget → Tool.Execute → post-hook → evidence
```

`execution/*` never decides whether it may run. `control/*` never runs anything.
The only sanctioned crossing is `agent` calling the injected `agent.Tool`
interface, which `cmd/ohgo` satisfies with `execution/tools` instances and wraps
in `securityGate`.

**Consequence for new code:** a capability cannot grant itself authority, and a
policy cannot execute. If a proposed change needs both, the answer is a new
*decision* in the control plane plus a new *capability* in the execution plane —
not a shared package.

### 4.2 Continuity is not the control plane

`continuity/*` records and restores what the control plane decided. It may not
decide *whether* to record, *which* status is terminal, or *what* completes.
`task.Controller` enforces legality (transitions, contract immutability), but the
verdict comes from `agent.ClassifyRun` / `EvaluateTaskCompletion`.

### 4.3 Repository intelligence is a subsystem, not a helper

`intelligence/*` has no dependency on the runtime. The runtime reaches it only
through the `agent.StructuralResolver` interface, and the production
implementation is `execution/tools`' own `SymbolsTool`, which shares one
`ast.Index` through `tools.StructuralSource` with `GraphTool`, `RefsTool`, and
`StructureTool`. There is exactly one index; a second one would break the
invariant that coordinate evidence, relationship evidence, inventory, and
completion verification describe the same repository snapshot.

### 4.4 Provider plane is below the runtime

`provider/*` may depend only on itself and `state`. It has no notion of a turn,
a task, a workspace, or an approval. That is why `agent` can be tested with a
fake `StreamFn` and why a new provider is a leaf-level addition.

`provider/ai` also owns the canonical stream boundary. `Registry.StreamFn`
normalizes heterogeneous provider output — including serialized tool calls
embedded in text — into the same `ai.ToolCall` representation a native call
uses. Normalization is a transport concern, not an authority decision: it ends
at canonical events, and the call still crosses the agent's full authority,
budget, execution, and evidence sequence.

### 4.5 Presentation is a projection boundary

`tui` renders and routes; it does not own task lifecycle, completion, or
authority. Its enforced constraints: no `execution/tools`, no `execution/mcp`,
no `continuity/task`. Task state arrives through `tui.TaskHooks`; the task
vocabulary (`pending`/`active`/`done`, `COMPLETED`/`FAILED`/`CANCELLED`) is a
deliberate string projection so the UI never imports the domain type.

### 4.6 The command is a leaf

`cmd/ohgo` is `package main`. It composes: it is the only place that decides
*which* tools exist, *which* model resolves, *which* budgets apply, and *how*
the security request is derived. Nothing imports it.

---

## 5. State: who owns what

Read this before adding a field to anything.

| Fact | Source of truth | Held by | Projected by | Persisted by |
|---|---|---|---|---|
| Live execution authority | `workspace` (policy) | `authorityGate` (`cmd/ohgo`) | TUI header, `TurnAuthority` | `session.Session.Workspace` |
| Session authority snapshot | `session.Session` | in-memory session | harness rows | `session.Store.SetAuthority` (one writer) |
| Completion verdict | `agent.Evaluate*Completion` | the run | `runState`, `task.Action` | `task.Status` |
| Task objective/contract | `contract.IntentContract` | `task.Task` | `task.md` | `progress.json` |
| Task shared context, facts, hypotheses, evidence, questions | `task.SharedContext` | `task.Controller` | `task.ContextFor` | `progress.json` (`context`) |
| Role execution context | `task.RoleContext` | the task | `task.ContextFor` (active role only) | `progress.json` (`context.roles`) |
| Task active role (continuity metadata, never authority) | `task.Task.ActiveRole` | `task.Controller` | `task.ContextFor`, turn gate | `progress.json` (`activeRole`) |
| Role handoff (continuity and recovery metadata, never authority) | `task.Handoff` | `task.Controller` | `task.ContextFor`, TUI transition card | `progress.json` (`handoff`) |
| Transition policy | `workspace.DecideTransition` | `control/workspace` | TUI decision surface | never (derived) |
| Evidence for the verdict | `agent.scanCompletionEvidence` (run-local) | the run | — | never |
| Evidence for projection | `cognition.EvidenceState` | the task | state block, sufficiency | `progress.json` |
| Budget ledger | `budget.Budget` | run holder | TUI usage | `progress.json` (`budget.State`) |
| Transcript | `session` | the store | TUI entries, REPL history | `sessions.db` |
| Edit history | `recovery` | the store | `/undo` output | `recovery.db` |
| Verified execution experience | `experience` | the engine | `/experience`, advisory projection hints | `<project>/.ohgo/experience.json`; optional `~/.ohgo/experience/global.json` after explicit promotion |
| Repository structure | `ast.Index` | `tools.StructuralSource` | tool results, impact/inventory queries | never (derived; rebuilt incrementally) |
| Model facts | `catalog` | the catalog | pickers, cost | `cache/*.json` (derived) |
| Settings | `config` | the file | options | `settings.json` |

Rule of thumb: **a value that is persisted has exactly one writer.** A second
writer is a bug even when both writers agree — see the model reference and the
session authority snapshot, both fixed in the second pass.

---

## 6. Enforced dependency rules

These are the machine-checked subset; `OHGO_ARCHITECTURE_RULES.md` explains the
reasoning, and `internal/architecture/rules_test.go` is the executable form.

1. Every production package lives under `internal/` (except `cmd/`, `bench/`,
   `scripts/`). **\[ENFORCED]**
2. Every `internal/` package is declared in the domain manifest. **\[ENFORCED]**
3. `internal/agent` imports only its six-package allow-list. **\[ENFORCED]**
4. Declared dependency-free leaves import no module package. **\[ENFORCED]**
5. Control ↛ execution/continuity/shell; execution ↛ continuity/presentation;
   continuity ↛ presentation. **\[ENFORCED]**
6. The provider plane depends only on itself and `state`. **\[ENFORCED]**
7. The shell layer is a sink: nothing below it imports `commit`, `startup`,
   `update`, `history`, `gryhook`, or `tui`. **\[ENFORCED]**
8. The TUI does not import `execution/tools`, `execution/mcp`, or
   `continuity/task`. **\[ENFORCED]**
9. Nothing imports `cmd/ohgo`. **\[ENFORCED]**
10. `internal/experience` imports only its structured-outcome/evidence
    allow-list; it cannot reach execution, continuity, application, or
    presentation. **\[ENFORCED]**

---

## 7. Feature placement: the question this map must answer

> “I am adding X. Where does X belong?”

| Future change | Where it goes | What it must not do |
|---|---|---|
| **New tool / execution capability** | `internal/execution/tools/<name>.go`, embedding `tools.Base`; register in `cmd/ohgo/build.go: buildTools` | Declare a false `Approval` tier; know about the TUI; decide its own authorization |
| **New repository-intelligence query** | `internal/intelligence/ast` (snapshot/query capability) + a thin tool in `internal/execution/tools`; share `tools.StructuralSource` | Import `agent`; build a second index; return stale coordinates or a duplicate graph |
| **New provider** | `internal/provider/ai/<name>.go` implementing `ai.Provider`; register in `Registry.ProviderFor`; env var in `provider/creds`; facts in `provider/catalog` | Reach into `agent` or config policy; add a per-model special case in the loop |
| **New model-selection policy** | facts in `provider/catalog`; persisted roles in `config`; resolution in `cmd/ohgo/options.go`; `harness` applies it | Make `agent` choose a model; branch on a model id in the engine |
| **New persistence record** | the owning `continuity/*` package + a versioned schema; a migration note in the migration doc | Add a second source of truth; change a JSON shape without a version bump |
| **New task transition** | `internal/continuity/task` (legality) + `internal/tui` projection of it | Let the TUI decide the transition; let a tool set task status |
| **New completion signal** | `internal/agent` (`verification.go` / `completion.go` / evidence scan), consumed by `ClassifyRun` | Read a model claim; decide completion in `tui` or in a tool |
| **New security policy** | kernel rule in `internal/security`; derivation from trusted inputs in `cmd/ohgo/security.go` | Derive capability/scope from model-supplied fields; let settings widen authority |
| **New CLI command** | `cmd/ohgo/<command>.go`; if interactive, also `internal/tui` command registry | Put domain logic in the command that has a package owner |
| **New TUI projection** | `internal/tui` (state machine + renderer), fed by hooks/events | Make the projection authoritative; read `continuity/task` directly |
| **New execution-learning rule** | `internal/experience` (deterministic extractor + regression) and an advisory seam in `cognition` | Change authority, evidence, completion, or the task lifecycle |
| **New recovery mechanism** | `internal/continuity/recovery` + `cmd/ohgo/recovery.go` wiring | Expose it as a model-reachable tool |
| **New prompt text** | `internal/prompt` (or the tool's own constructor) | Inline a prompt string at a call site |
| **New deterministic prompt/title transform** | `internal/promptopt` | Perform I/O, call a model, or duplicate the transform at a call site |
| **New shared helper used by two packages** | the lower of the two owners, or the package that owns the concept | Create `common/`, `shared/`, `util/`, `helpers/`, `runtime/`, `core/` |

### 7.1 The placement test

Before creating a file, answer these. If any answer is "not sure", stop and
decide ownership first — that is a successful outcome, not a delay.

```
What decision or state does this introduce?
Which package already owns that decision or state?
  ├─ yes → extend it
  └─ no  → is the new owner genuinely a new domain?
            ├─ no  → find the closest existing owner
            └─ yes → new package, declared in the manifest, with:
                     owned state, owned invariants, allowed imports,
                     forbidden imports, and its test boundary written down
Is it control-plane, execution-plane, provider, continuity, intelligence,
configuration, application, or presentation?
Does it create a second source of truth / authority path / index /
completion decision / persistence representation?
```

---

## 8. Known coupling to watch (not violations, but load-bearing)

These edges exist today, are deliberate, and must not be "cleaned up" without a
decision:

| Edge | Why it exists | What breaking it would cost |
|---|---|---|
| `control/workspace` + `control/cognition` → `agent` | They are built on `agent.Turn`, `agent.ToolOutcome`, `agent.TargetSnapshot` | A type move becomes a persistence migration (`progress.json`) |
| `execution/tools` → `control/workspace` | The artifact tool checks the live producer workspace at call time | Artifact provenance |
| `tui` → `control/{workspace,cognition}` , `agent`, `continuity/{session,recovery}`, `commit`, `prompt` | The TUI *is* an orchestrator today (it installs projection hooks and calls `BuildShape`) | Turn preparation would need the missing seam described in the migration doc |
| `continuity/task` → `control/cognition` | The checkpoint embeds `EvidenceState` | Changing it changes `progress.json` |
| `continuity/session` + `tui` both append messages | Persistence differs by UI mode | Unified persistence would change stored transcripts |
| `cmd/ohgo` → everything | It is the composition root | Nothing; this is expected for a command |

---

## 9. What is not in this architecture, on purpose

* **No `internal/common`, `shared`, `util`, `helpers`, `runtime`, `core`,
  `manager`, `controller`, `engine`, or `pipeline`.** If a concept has no owner,
  the answer is a decision, not a folder.
* **No `agent` knowledge of concrete tools.** Capabilities arrive as
  `agent.Tool`; projection arrives as a function field; streaming arrives as
  `ai.StreamFn`.
* **No second repository index**, no second authority path, no second completion
  decision, no second formatter for a persisted reference.
* **No experience-as-authority.** `internal/experience` stores learned path
  preferences; `cognition.ExperienceAdvisor` may only reorder offered tools and
  append bounded hints. It cannot add capabilities, change evidence, or decide
  completion.
* **No presentation-owned domain semantics.** The TUI's vocabularies are
  projections of decisions made elsewhere.

---

## 10. Reading order for a new contributor

1. `OHGO_ARCHITECTURE_RULES.md` — the rules you must not break.
2. This map, §7 — where your feature goes.
3. `internal/agent/loop.go` — the execution sequence.
4. `cmd/ohgo/build.go` + `cmd/ohgo/security.go` — how capabilities and authority
   are wired.
5. `internal/control/workspace` — the authority table and the intent contract.
6. `internal/continuity/task` — the durable task, and `progress.json` as the
   source of truth.
7. `TASK_CONTINUITY.md` — task identity, shared context, evidence, and role
   context ownership.
8. `ADAPTIVE_EXPERIENCE.md` — the advisory learning layer and its hard
   boundaries.
9. `internal/architecture/rules_test.go` — the boundaries, executable.
