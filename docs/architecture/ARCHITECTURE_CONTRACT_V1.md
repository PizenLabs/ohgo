# OHGO Core Architecture Contract v1

> **Status:** Architecture Baseline  
> **Scope:** Core runtime, authority model, workspace model, persistence, model resolution, cross-workspace handoff  
> **Non-goal:** Rewriting or destabilizing the existing execution engine  
> **Principle:** Audit existing behavior first. Change only the seams required to conform to this contract.

---

## 1. Purpose

This document defines the architectural contract for OHGO.

OHGO is a human-controlled agent runtime for real codebases.

The architecture is intentionally organized around **three execution authorities** rather than multiple task-specific modes:

```text
ASK    → Understand
FUZZ   → Discover
BUILD  → Change
```

The three workspaces are not UI modes.

They are **runtime authority boundaries**.

The central rule is:

> A workspace determines what the agent is allowed to do, not merely how the terminal looks.

The architecture must preserve the existing working execution path. This contract defines the target boundaries that the current implementation must converge toward through incremental, audit-first changes.

---

# 2. Core Philosophy

OHGO follows:

> **Dynamic Path, Static Authority, Truthful State Transition.**

Meaning:

- The path through a task may change dynamically.
- Authority boundaries do not change implicitly.
- Runtime state must represent what actually happened.
- Model output is untrusted input.
- The runtime, not the model, decides whether an action is permitted.
- Humans remain the ultimate authority over mutation.
- Execution state must be durable enough to resume truthfully.

The runtime must never infer authority from model intent.

For example:

```text
Model:
"I need to modify foo.go."
```

does not grant write authority.

The runtime evaluates:

```text
current workspace
    ↓
capability policy
    ↓
tool admission
    ↓
execution
```

---

# 3. Three Workspaces

OHGO has exactly three primary workspaces.

```text
┌──────────────┬──────────────────────┬──────────────────────────┐
│ Workspace    │ Primary Purpose      │ Mutation Authority       │
├──────────────┼──────────────────────┼──────────────────────────┤
│ ASK          │ Understand / reason  │ None                    │
│ FUZZ         │ Explore / discover   │ None                    │
│ BUILD        │ Change / implement   │ Full                    │
└──────────────┴──────────────────────┴──────────────────────────┘
```

These boundaries are architectural invariants.

There must not be a fourth workspace for:

- plan
- investigate
- review
- debug
- test
- research

Those are activities performed inside one of the three authorities.

---

# 4. ASK

## 4.1 Purpose

ASK is the read-only reasoning workspace.

Its purpose is:

> Understand the user, the repository, the current state, and possible solutions without mutating the repository.

ASK may perform:

- repository inspection
- file reading
- symbol search
- code search
- dependency inspection
- git inspection
- test inspection
- architecture analysis
- planning
- review
- explanation
- reasoning

ASK must not mutate the repository.

---

## 4.2 ASK Authority

Conceptually:

```text
READ              ✓
SEARCH            ✓
INSPECT           ✓
GIT READ          ✓
ANALYZE           ✓
PLAN              ✓

WRITE             ✗
PATCH             ✗
DELETE            ✗
MOVE              ✗
COMMIT            ✗
```

The exact tool matrix may evolve, but the mutation invariant cannot:

> **ASK cannot mutate the project.**

---

## 4.3 Planning is an ASK activity

There is no PLAN workspace.

For example:

```text
ASK
 ├── explain
 ├── inspect
 ├── analyze
 ├── plan
 └── review
```

A plan is information produced by ASK.

It does not acquire mutation authority.

---

# 5. FUZZ

## 5.1 Purpose

FUZZ is the exploratory and adversarial investigation workspace.

Its purpose is:

> Discover unexpected behavior, reproduce failures, probe edge cases, stress the system, and produce evidence without modifying the repository.

FUZZ is intentionally broader than traditional fuzz testing.

It may perform:

- exploratory testing
- bug hunting
- reproduction
- stress testing
- race detection
- malformed input testing
- benchmark execution
- test execution
- build execution
- shell/Bash observation
- runtime probing
- security-oriented investigation
- regression exploration

---

## 5.2 FUZZ Authority

Conceptually:

```text
READ              ✓
SEARCH            ✓
INSPECT           ✓
GIT READ          ✓
BASH              ✓*
TEST              ✓
BUILD             ✓
RUN               ✓
STRESS            ✓

WRITE             ✗
PATCH             ✗
DELETE            ✗
MOVE              ✗
COMMIT            ✗
```

`*` Bash is subject to runtime capability policy.

The critical invariant is:

> **FUZZ may execute commands, but FUZZ may not mutate the repository.**

Execution authority and mutation authority are different concepts.

---

## 5.3 FUZZ must not become BUILD-lite

A model must never be able to bypass the FUZZ boundary by claiming that a mutation is necessary for investigation.

Example:

```text
FUZZ:
"Create tmp/test.go so I can reproduce this."
```

The runtime must reject repository mutation:

```text
capability denied
workspace=fuzz
operation=write
reason=mutation_not_permitted
```

No implicit escalation is allowed.

---

# 6. BUILD

## 6.1 Purpose

BUILD is the mutation authority.

Its purpose is:

> Implement changes, modify the repository, execute required verification, and produce a truthful resulting state.

BUILD may perform:

- read
- search
- analysis
- patch
- file creation
- file modification
- file deletion
- refactoring
- migration
- test execution
- build execution
- verification
- git operations subject to policy

---

## 6.2 BUILD Authority

Conceptually:

```text
READ              ✓
SEARCH            ✓
BASH              ✓
TEST              ✓
BUILD             ✓
WRITE             ✓
PATCH             ✓
DELETE            ✓
MOVE              ✓
```

BUILD is the **only** workspace with repository mutation authority.

Invariant:

> **All repository mutation must pass through BUILD authority.**

No other workspace may acquire mutation authority implicitly.

---

# 7. Authority Model

Workspace authority must be enforced below the UI.

The authoritative path is:

```text
Workspace
    ↓
Capability Policy
    ↓
Tool Admission
    ↓
Execution
    ↓
Truthful State Transition
```

The UI may display authority.

The UI does not grant authority.

A command, model response, or tool request cannot bypass the runtime authority layer.

---

# 8. Workspace Is Not a UI Mode

Workspace state must exist in the core runtime.

The following is insufficient:

```text
ui.workspace = "build"
```

The runtime must know:

```text
runtime.workspace = BUILD
```

because workspace determines:

- capability policy
- tool admission
- model binding
- session identity
- cross-workspace handoff rules
- runtime behavior

UI presentation consumes workspace state; it does not own it.

---

# 9. Workspace Identity Colors

OHGO uses fixed semantic workspace colors.

```text
ASK   → Teal
BUILD → Blue
FUZZ  → Magenta
```

These colors are semantic identity, not user-theme colors.

They must remain stable regardless of:

- terminal theme
- user-selected terminal colors
- light/dark terminal
- Catppuccin
- Dracula
- Gruvbox
- Solarized
- other terminal palettes

The implementation should expose semantic workspace styling rather than scattering raw color values throughout rendering code.

Example conceptual API:

```go
type WorkspaceStyle struct {
    Accent Color
    Label  string
}
```

The UI consumes this semantic identity.

The color system must never participate in authority decisions.

---

# 10. `$sup` — Ephemeral Support Query

OHGO provides a transient quick-reference interaction:

```text
$sup <question>
```

Example:

```text
$sup what is Golang?
```

The command opens a temporary interaction surface.

Conceptually:

```text
Normal ASK session
        │
        │ $sup
        ▼
┌─────────────────────────┐
│      Support Query      │
│                         │
│ What is Golang?         │
│                         │
│ ...answer...            │
│                         │
│ >                       │
└─────────────────────────┘
        │
        │ ESC
        ▼
Normal ASK session
```

---

## 10.1 `$sup` Is Not a Workspace

`$sup` does not create a fourth workspace.

It is an ephemeral capability of ASK.

```text
ASK
 ├── normal conversation
 ├── planning
 └── $sup
```

---

## 10.2 `$sup` Must Not Pollute the Main Session

A `$sup` interaction must not automatically:

- append to the main ASK conversation
- create a task
- create an objective
- create a plan
- modify repository state
- modify execution state
- modify workspace state

Its conversation is ephemeral.

ESC returns the user to the previous interaction without changing the main task context.

---

## 10.3 `$sup` Model

By default:

```text
$sup → ASK model
```

A separate `$sup` model is not required for v1.

This keeps `$sup` an ASK capability rather than another model authority.

---

# 11. Model Binding

OHGO supports workspace-specific model selection.

Conceptually:

```text
                 Model Registry
                      │
        ┌─────────────┼─────────────┐
        ↓             ↓             ↓
       ASK          FUZZ          BUILD
        │             │             │
      Model A       Model C       Model B
```

This allows users to optimize models for different workloads.

For example:

```yaml
models:
  ask: provider/fast-model
  build: provider/strong-code-model
  fuzz: provider/local-model
```

---

# 12. Default Model

A global/default model remains available.

Resolution:

```text
workspace-specific model
        ↓ fallback
global/default model
        ↓ fallback
built-in/default provider policy
```

Therefore:

```text
ASK   → ASK model   or default
FUZZ  → FUZZ model  or default
BUILD → BUILD model or default
```

The presence of workspace-specific models must not require all three to be configured.

---

# 13. Session Model Snapshot

Once a session resolves its effective model, the session must retain that effective model identity.

Example:

```text
Session #42

workspace: BUILD

model:
    provider: openrouter
    model: provider/model-x
```

If the user later changes the global configuration:

```text
default model = provider/model-y
```

Session #42 must not silently switch to model-y.

The rule is:

> Configuration resolves session execution. A running/resumable session owns its resolved model identity.

Therefore:

```text
new session
    → current configuration

existing session
    → its persisted effective configuration
```

This prevents restart from silently changing execution semantics.

---

# 14. Configuration Resolution

Configuration precedence must be deterministic.

Baseline:

```text
Explicit CLI override
        ↓
Project-local configuration
        ↓
Global configuration
        ↓
Built-in default
```

For model selection:

```text
CLI model override
        ↓
workspace-specific model
        ↓
global default model
        ↓
built-in fallback
```

The exact existing implementation must be audited before changing this order.

This document defines the target contract, not permission to rewrite the existing provider system.

---

# 15. Global OHGO State

OHGO already has a global state directory:

```text
~/.ohgo/
├── cache/
│   ├── ollama-cloud-models.json
│   ├── ollama-models.json
│   ├── opencode-go-models.json
│   ├── opencode-zen-models.json
│   └── openrouter-models.json
├── sessions.db
├── sessions.db-shm
└── sessions.db-wal
```

This existing persistence infrastructure must be preserved.

OHGO must not create a parallel session persistence system.

---

# 16. Global State Responsibilities

`~/.ohgo/` is the user-global OHGO state boundary.

Conceptually:

```text
~/.ohgo/
├── cache/        External discovery cache
├── config.yml    User configuration
└── sessions.db   Durable session/state database
```

The exact file layout must be established by auditing the existing implementation first.

---

## 16.1 Cache

The existing cache contains provider/model discovery data.

Examples:

```text
ollama-models.json
ollama-cloud-models.json
openrouter-models.json
opencode-go-models.json
opencode-zen-models.json
```

These are external discovery caches.

They must not be confused with:

- user configuration
- session state
- workspace authority
- execution state

---

## 16.2 Configuration

Global configuration should eventually live under:

```text
~/.ohgo/config.yml
```

It represents:

> What the user wants OHGO to do by default.

It may contain:

```yaml
model: ...

models:
  ask: ...
  build: ...
  fuzz: ...
```

Only configuration belongs here.

Runtime history should remain in the session database.

---

## 16.3 Session Database

The existing:

```text
~/.ohgo/sessions.db
```

is the durable session store.

It should remain the authoritative persistence mechanism for session history and resumable runtime state.

Do not introduce:

```text
~/.ohgo/sessions/
```

as a second persistence system unless a future architectural requirement proves that SQLite is insufficient.

---

# 17. Project-Local `.ohgo/`

A project-local:

```text
<project>/.ohgo/
```

provides project identity and project-local OHGO metadata.

It must not duplicate the entire global session database.

Conceptually:

```text
project/
├── .git/
├── .ohgo/
│   └── project.yml
├── ...
```

The primary responsibility of `.ohgo/` is:

> Establish a stable project identity and provide a project-local boundary for OHGO metadata.

---

# 18. Project Identity

Project identity must be stable across restarts.

The runtime must distinguish:

```text
Project A
```

from:

```text
Project B
```

even if they contain similar source trees.

The exact identity mechanism must be determined by audit.

Possible inputs include:

- stable project ID
- repository root
- Git root
- local project metadata

The implementation must not infer a project identity from transient process state.

---

# 19. Global vs Project State

The conceptual separation is:

```text
~/.ohgo/
    ↓
"What does this user configure globally?"

<project>/.ohgo/
    ↓
"What project is this?"

sessions.db
    ↓
"What has OHGO been doing?"
```

This separation must be maintained.

---

# 20. Session Continuity

Restarting OHGO must preserve meaningful execution continuity.

If the user previously worked in:

```text
BUILD
model = model-B
```

then restarting OHGO in the same project must not silently produce:

```text
BUILD
model = default-model
```

unless the previous session did not contain a persisted model binding.

The intended behavior is:

```text
startup
  ↓
project discovery
  ↓
session discovery
  ↓
session restore
  ↓
workspace restore
  ↓
effective model restore
  ↓
runtime resume
```

The restored state must be truthful.

---

# 21. Session, Workspace, and Model Are Different Concepts

These concepts must not be collapsed.

```text
Workspace
    ↓
Authority

Session
    ↓
Continuity

Model
    ↓
Execution configuration
```

Conceptually:

```text
Workspace
   │
   ├── capability policy
   ├── current model binding
   └── session
          │
          ├── context
          ├── task
          ├── artifacts
          ├── findings
          └── execution history
```

---

# 22. Cross-Workspace Workflow

The three workspaces are intentionally connected.

The canonical relationship is:

```text
                 ASK
                  │
             plan/evidence
                  ↓
                 FUZZ
                  │
               finding
                  ↓
                BUILD
                  │
            change + verify
                  ↓
                 FUZZ
                  │
             regression
                  ↓
                 ASK
```

This is not a mandatory linear pipeline.

The runtime may dynamically choose the appropriate workspace based on user intent.

The authority boundaries remain static.

---

# 23. Artifact-Based Handoff

Cross-workspace communication must be based on structured artifacts, not raw conversation transfer.

Examples:

```text
ASK  → Plan
FUZZ → Finding
BUILD → Change / Verification
```

A handoff should reference the artifact identity.

Example:

```text
Finding
────────────────────
ID: fuzz-8f31

target:
    internal/runtime.go

status:
    reproduced

expected:
    ...

observed:
    ...

reproduction:
    go test -race ./...

evidence:
    ...
```

BUILD receives the finding.

It does not automatically receive authority from FUZZ.

---

# 24. FUZZ → BUILD

The canonical bug-fixing workflow is:

```text
FUZZ
  ↓
discover
  ↓
reproduce
  ↓
produce Finding
  ↓
BUILD
  ↓
diagnose
  ↓
patch
  ↓
verify
  ↓
FUZZ
```

The finding is the contract between discovery and mutation.

---

# 25. BUILD → FUZZ

BUILD may hand a change back to FUZZ for regression exploration.

Example:

```text
BUILD
  ↓
change
  ↓
verification
  ↓
FUZZ
  ↓
reproduce original failure
  ↓
probe adjacent failure modes
  ↓
regression result
```

FUZZ must remain mutation-free during this process.

---

# 26. ASK ↔ FUZZ

ASK can formulate investigative goals.

Example:

```text
ASK:
"Analyze the parser and identify likely edge cases."

        ↓

FUZZ:
"Explore and reproduce those edge cases."
```

FUZZ returns evidence.

ASK may then interpret that evidence.

Neither operation grants mutation authority.

---

# 27. No Conversation-Based Authority Transfer

The runtime must never infer:

```text
"Because FUZZ discovered a bug,
FUZZ may now fix it."
```

Nor:

```text
"Because ASK created a plan,
ASK may now execute it."
```

Instead:

```text
artifact
   ↓
explicit workspace transition
   ↓
new authority policy
```

Authority is always determined by the destination workspace.

---

# 28. Execution Preservation Rule

This architecture is not a rewrite specification.

The current OHGO execution engine is considered a working foundation.

Therefore:

> **Do not replace working execution behavior merely to match the conceptual architecture.**

Before modifying execution:

1. Trace the existing execution path.
2. Identify the current authority owner.
3. Identify the smallest seam required.
4. Preserve existing behavior outside that seam.
5. Add regression coverage.
6. Validate real CLI behavior.

The architecture is the destination.

The existing execution path is the asset to preserve.

---

# 29. Audit-First Rule

Every architectural change must begin with an audit.

Required sequence:

```text
AUDIT
  ↓
TRACE
  ↓
IDENTIFY OWNER
  ↓
DEFINE SEAM
  ↓
MINIMAL CHANGE
  ↓
REGRESSION TEST
  ↓
REAL CLI VALIDATION
```

Do not begin with:

```text
rewrite
refactor
move files
replace subsystem
```

unless the audit proves that the existing implementation cannot satisfy the contract incrementally.

---

# 30. Single Authority Principle

For every architectural responsibility there must be one authoritative owner.

Examples:

```text
Workspace authority
    → Runtime

Tool permission
    → Capability / admission layer

Execution
    → Existing execution owner

Session persistence
    → sessions.db / session subsystem

Project identity
    → project identity subsystem

Model resolution
    → configuration/model-resolution subsystem

Rendering
    → UI layer
```

Multiple competing owners are prohibited.

---

# 31. Truthful State

OHGO must distinguish:

```text
requested
admitted
executing
completed
failed
cancelled
unknown
```

The runtime must never report successful completion merely because a model produced a plausible response.

Likewise:

```text
model says fixed
```

does not mean:

```text
system state = fixed
```

Verification determines truth.

---

# 32. Model Output Is Untrusted

LLM output is proposal/input.

The runtime owns:

- authority
- capability checks
- execution
- persistence
- state transitions
- artifact identity
- verification

Therefore:

```text
LLM
  ↓ proposal
Runtime
  ↓ admission
Execution
  ↓ result
Runtime
  ↓ state transition
```

Never:

```text
LLM
  ↓
direct authority
```

---

# 33. Workspace Transition

A workspace transition must be explicit.

Examples:

```text
ASK → BUILD
FUZZ → BUILD
BUILD → FUZZ
```

must produce a new authoritative runtime context.

A transition may preserve relevant artifacts:

```text
Finding
Plan
Change
Verification
```

but must not preserve capabilities from the previous workspace.

Example:

```text
FUZZ
  ↓
BUILD
```

does not mean:

```text
FUZZ capabilities + BUILD capabilities
```

It means:

```text
BUILD capabilities
```

---

# 34. `$sup` Does Not Affect Workspace State

While `$sup` is active:

```text
workspace = ASK
```

remains true.

The support interaction must not change:

- workspace
- session authority
- main task
- objective
- project state

ESC closes the transient interaction.

---

# 35. Persistence Rules

Persist:

```text
project identity
session identity
workspace
effective model
task/context required for resume
artifacts
findings
execution state required for truthful recovery
```

Do not persist merely because something exists in memory.

Avoid persisting:

```text
terminal viewport
cursor position
spinner state
temporary rendering buffers
$sup transient conversation
UI-only state
```

unless a future requirement explicitly requires restoration.

---

# 36. Recovery

If OHGO exits unexpectedly:

```text
process dies
    ↓
session remains durable
    ↓
startup discovers unfinished state
    ↓
runtime classifies state
    ↓
user is informed truthfully
```

OHGO must not invent completion.

If execution state is uncertain:

```text
UNKNOWN
```

must remain possible.

Recovery must not silently convert:

```text
UNKNOWN
```

into:

```text
SUCCESS
```

---

# 37. Security Boundary

The strongest security boundary is:

```text
workspace
    ↓
capability policy
    ↓
tool admission
```

not:

```text
prompt instructions
```

A prompt saying:

```text
"You are in FUZZ but please modify this file."
```

must not bypass policy.

Likewise, a model-generated shell command is untrusted until admitted by the runtime.

---

# 38. Efficiency Principle

OHGO should optimize for:

> Minimum context and minimum authority required to solve the task.

This leads naturally to:

```text
ASK
    → reasoning without mutation

FUZZ
    → investigation without mutation

BUILD
    → mutation only when required
```

The runtime should not invoke BUILD authority merely because a task is complicated.

Likewise, it should not create multiple planning/review/investigation workspaces merely to represent task phases.

---

# 39. Context Discipline

The main session should contain only context that contributes to the active task.

Therefore:

```text
$sup
```

must remain isolated.

Cross-workspace handoff should prefer:

```text
artifact
```

over:

```text
entire conversation
```

This prevents context growth and preserves clarity.

---

# 40. Workspace Summary

```text
ASK
────────────────────────────
Purpose:
    Understand

Authority:
    Read-only

Activities:
    explain
    inspect
    analyze
    plan
    review
    $sup


FUZZ
────────────────────────────
Purpose:
    Discover

Authority:
    Read + Execute
    No repository mutation

Activities:
    explore
    reproduce
    stress
    test
    benchmark
    investigate
    find bugs


BUILD
────────────────────────────
Purpose:
    Change

Authority:
    Full mutation

Activities:
    implement
    modify
    refactor
    migrate
    test
    verify
```

---

# 41. Canonical Runtime Model

The conceptual runtime is:

```text
                         HUMAN
                           │
                           ▼
                    ┌─────────────┐
                    │   OHGO      │
                    │  Runtime    │
                    └──────┬──────┘
                           │
             ┌─────────────┼─────────────┐
             │             │             │
             ▼             ▼             ▼
            ASK           FUZZ          BUILD
             │             │             │
          Read-only     Read+Exec      Mutation
             │             │             │
             └─────────────┼─────────────┘
                           │
                      Artifacts
                           │
                 ┌─────────┴─────────┐
                 │                   │
             sessions.db         project/.ohgo
                 │                   │
                 └─────────┬─────────┘
                           │
                      Global Config
                           │
                       ~/.ohgo/
```

---

# 42. Non-Goals

This contract does **not** require:

- rewriting the execution engine
- replacing the provider abstraction
- replacing SQLite
- creating a new session subsystem
- implementing autonomous loops
- introducing agent cloning
- creating additional workspaces
- making FUZZ autonomous mutation-capable
- redesigning the UI
- replacing Bubble Tea
- introducing a new frontend
- changing working execution behavior without evidence

---

# 43. Migration Strategy

The architecture must be adopted incrementally.

Recommended order:

```text
1. Audit current core
        ↓
2. Audit session DB schema
        ↓
3. Audit project identity
        ↓
4. Audit workspace representation
        ↓
5. Audit capability/tool admission
        ↓
6. Audit model resolution
        ↓
7. Audit session restore
        ↓
8. Define minimal missing seams
        ↓
9. Add regression tests
        ↓
10. Implement smallest changes
        ↓
11. Validate real CLI
```

Do not perform a broad refactor before these audits are complete.

---

# 44. Architectural Invariants

The following invariants are mandatory.

### I1 — Exactly three primary workspaces

```text
ASK | FUZZ | BUILD
```

### I2 — ASK cannot mutate the repository

### I3 — FUZZ cannot mutate the repository

### I4 — BUILD is the sole repository mutation authority

### I5 — Workspace authority is enforced below the UI

### I6 — `$sup` is ephemeral and does not create a workspace

### I7 — Workspace-specific models may override the default model

### I8 — A resolved session model remains stable across restart/config changes

### I9 — Global state belongs under `~/.ohgo/`

### I10 — Existing `sessions.db` remains the durable session store

### I11 — `.ohgo/` identifies and anchors project-local OHGO state

### I12 — Cross-workspace communication uses structured artifacts

### I13 — Authority never transfers implicitly through conversation

### I14 — LLM output never directly grants authority

### I15 — Runtime state must represent actual execution truth

### I16 — Unknown execution state must remain representable

### I17 — `$sup` does not pollute the main task context

### I18 — Existing working execution behavior must be preserved unless an audit proves a change necessary

### I19 — Architectural changes are audit-first and seam-oriented

### I20 — No new workspace may be introduced to represent a task activity

---

# 45. Definition of Done

The architecture is considered implemented when:

- ASK, FUZZ, and BUILD are authoritative runtime boundaries.
- Capability enforcement is independent of UI rendering.
- ASK cannot mutate.
- FUZZ can execute approved exploration commands but cannot mutate.
- BUILD is the sole mutation authority.
- `$sup` is transient and isolated.
- Workspace-specific models resolve deterministically.
- Session model identity survives restart.
- Global configuration has a deterministic resolution path.
- Project identity survives restart.
- Existing `~/.ohgo/sessions.db` remains the durable session store.
- `.ohgo/` does not duplicate session storage.
- ASK/FUZZ/BUILD can exchange structured artifacts.
- FUZZ findings can become BUILD inputs.
- BUILD changes can return to FUZZ for regression exploration.
- Existing execution behavior remains intact.
- Real CLI workflows validate the contracts.
- No authority is granted by model output or UI state.

---

# 46. Final Architectural Statement

OHGO is not organized around five modes of conversation.

It is organized around three authority boundaries:

```text
ASK
    Understand without changing.

FUZZ
    Explore without changing.

BUILD
    Change with authority.
```

Everything else is an activity, artifact, capability, or state transition inside those boundaries.

The core loop is:

```text
Understand
    ↓
Discover
    ↓
Change
    ↓
Verify
    ↓
Discover again
```

The runtime owns authority.

The model proposes.

Artifacts carry evidence.

Sessions preserve continuity.

The project owns its identity.

The human remains in control.

And the existing execution engine remains the foundation until an audit proves that a seam must change.
