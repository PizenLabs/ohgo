# Ohgo–gry Architecture Boundary Specification

**Status:** Architecture Rule  
**Scope:** Ohgo runtime, repository intelligence, PizenLabs ecosystem integration  
**Primary principle:** Ohgo may use gry; Ohgo must never require gry to remain correct, authoritative, or operational.

---

## 1. Purpose

This specification defines the architectural relationship between **Ohgo** and **gry**.

The purpose is not to make Ohgo depend on gry, nor to reproduce gry's capabilities inside Ohgo.

The purpose is to establish a clean division of responsibility:

- **Ohgo** owns agent orchestration, authority, task state, budgets, execution, evidence decisions, continuity, and completion.
- **gry** owns repository structural intelligence and exposes that intelligence through bounded queries.
- **LLMs** reason over supplied evidence but do not become authoritative.
- **Execution** remains controlled by Ohgo.
- **Human authority** remains above the runtime.

The integration MUST preserve this principle:

> **Ohgo decides. gry observes structure. LLMs reason. Execution performs authorized work. Humans remain in control.**

---

# 2. Architectural Position

gry is an **optional external intelligence provider** within the Ohgo ecosystem.

It is not:

- an Ohgo role;
- an Ohgo agent;
- an Ohgo planner;
- an Ohgo authority;
- an execution engine;
- an approval system;
- a task-state store;
- a completion authority;
- a session-continuity system;
- a replacement for Ohgo's Control Plane.

Conceptually:

```text
                         HUMAN
                           │
                           ▼
                    OHGO CONTROL PLANE
                           │
             ┌─────────────┼─────────────┐
             │             │             │
            ASK           FUZZ          BUILD
             │             │             │
             └─────────────┼─────────────┘
                           │
                  Intelligence Request
                           │
                           ▼
                REPOSITORY INTELLIGENCE
                           │
                    ┌──────┴──────┐
                    │             │
                 Native          gry
                capability     provider
                    │             │
                    └──────┬──────┘
                           │
                    Bounded Evidence
                           │
                           ▼
                         LLM
                           │
                    Untrusted Proposal
                           │
                           ▼
                    OHGO CONTROL PLANE
                           │
                           ▼
                   EXECUTION PLANE
```

gry MUST remain below the Ohgo authority boundary.

---

# 3. Ownership Model

Each subsystem MUST have one primary owner.

| Concern | Owner |
|---|---|
| Human approval | Ohgo / Human |
| Task intent | Ohgo |
| Role selection | Ohgo |
| Authority | Ohgo |
| Task continuity | Ohgo |
| Session state | Ohgo |
| Budget | Ohgo |
| Tool authorization | Ohgo |
| Execution | Ohgo |
| Mutation | Ohgo BUILD / Execution Plane |
| Completion state | Ohgo |
| Objective proof | Ohgo |
| Evidence acceptance | Ohgo |
| Repository structural graph | gry |
| Symbol resolution | gry |
| Structural relationships | gry |
| Repository indexing | gry |
| Structural provenance | gry |
| Structural change impact | gry |
| Structural test-impact analysis | gry |
| Model inference | Selected model/provider |

No integration MAY transfer ownership merely because another subsystem has a convenient implementation.

---

# 4. Independence Requirement

Ohgo MUST remain fully functional when gry is:

- not installed;
- unavailable;
- not configured;
- not indexed;
- indexing;
- stale;
- partially indexed;
- unable to resolve a target;
- missing language coverage;
- returning ambiguous results;
- returning unresolved results;
- returning an error;
- operating at reduced capability.

gry failure MUST NOT become an Ohgo architecture failure unless the user explicitly requested a gry-specific operation.

The expected degradation is:

```text
gry available
    ↓
higher-quality structural evidence

gry unavailable
    ↓
Ohgo falls back to its own intelligence/repository mechanisms

gry insufficient
    ↓
Ohgo reports the evidence limitation

gry ambiguous
    ↓
Ohgo preserves ambiguity

gry failed
    ↓
Ohgo classifies the failure and continues or asks for a decision
```

The following pattern is prohibited:

```text
Ohgo task
    ↓
gry required
    ↓
gry unavailable
    ↓
task cannot operate
```

---

# 5. Capability, Not Dependency

Ohgo MUST depend on a **capability contract**, not on gry itself.

The architectural abstraction SHOULD be conceptually equivalent to:

```text
StructuralIntelligence
```

rather than:

```text
GryService
```

The consumer asks for information:

```text
resolve target
get structural context
trace relationships
inspect architecture
assess change impact
assess test impact
```

It does not ask:

```text
run gry tool X
run gry hook Y
invoke gry agent Z
```

The implementation MAY use gry where appropriate.

This prevents the Ohgo architecture from becoming coupled to the current shape of gry's CLI, MCP surface, hooks, or internal storage.

---

# 6. Native Capability Has Priority Over External Capability

The existence of gry MUST NOT automatically justify implementing an Ohgo-native equivalent.

Likewise, the existence of an Ohgo-native capability MUST NOT automatically justify duplicating it through gry.

The decision rule is:

```text
Does the required capability already exist?
        │
        ├── Yes → reuse it.
        │
        └── No
             │
             ▼
Does gry already provide the capability adequately?
             │
        ┌────┴────┐
       Yes        No
        │          │
        ▼          ▼
 consume        design only
 capability     what is actually
                 missing
```

The default is:

> **Reuse before rebuild. Integrate before duplicate. Extend only where the boundary requires it.**

---

# 7. No Feature Parity Requirement

Ohgo MUST NOT attempt to reproduce the complete gry feature set.

gry already provides repository-wide structural intelligence including:

- symbols;
- calls;
- imports;
- dependencies;
- routes;
- infrastructure relationships;
- test relationships;
- documents;
- configuration;
- deployment structures;
- runtime observations;
- incremental indexing;
- bounded task context.

Its graph contains multiple node and edge types and preserves provenance/confidence where available.

Ohgo should consume those capabilities where they materially improve an Ohgo task.

Ohgo MUST NOT implement:

```text
"our own gry"
```

inside the Ohgo repository.

A capability is justified inside Ohgo only when it belongs intrinsically to Ohgo's authority/orchestration model or when an external provider cannot satisfy a required Ohgo-specific contract.

---

# 8. Structural Intelligence Boundary

gry's responsibility ends at structural intelligence.

Examples:

```text
gry:
    "Function A calls Function B."
    "Route X reaches Handler Y."
    "These tests are structurally related."
    "This target has these callers."
    "This dependency is unresolved."
```

Ohgo decides what those facts mean for the task.

For example:

```text
gry:
    A → B

Ohgo:
    "Changing B may affect A."

Ohgo:
    "This impact is relevant to the current objective."

Ohgo:
    "The available evidence is sufficient / insufficient."

Ohgo:
    "The objective is PROVEN / BLOCKED / NEEDS_REVIEW."
```

gry MUST NOT directly establish Ohgo's objective state.

---

# 9. Provenance Translation

gry provides provenance such as:

```text
verified
declared
inferred
observed
```

and explicitly reports uncertainty instead of guessing.

Ohgo MAY consume this provenance.

However:

> **gry provenance MUST NOT automatically become Ohgo objective proof.**

For example:

```text
gry:
    verified structural relationship

≠

Ohgo:
    objective verified
```

The first describes the confidence of a structural observation.

The second describes whether the user's objective has been adequately established.

Only Ohgo owns the second decision.

---

# 10. Bounded Context Rule

gry MUST be used to reduce context, not to increase it.

The preferred pattern is:

```text
task
  ↓
target
  ↓
bounded structural query
  ↓
bounded evidence
  ↓
LLM
```

not:

```text
task
  ↓
full repository graph
  ↓
large context dump
  ↓
LLM
```

gry already supports bounded task context, target resolution, callers, callees, imports, services, coverage, test gaps, change evidence, provenance, and hard output limits.

Ohgo SHOULD therefore treat gry as a **context compression and evidence targeting mechanism**, not as a reason to expose more repository data to the model.

---

# 11. Information-Need Routing

Routing MUST be based on the information required by the task, not on agent role.

Incorrect:

```text
ASK → gry
BUILD → native
FUZZ → gry hooks
```

Correct:

```text
Task requires structural evidence
        ↓
StructuralIntelligence
        ↓
choose the most efficient provider
```

The provider selection MAY consider:

- existing Ohgo evidence;
- existing cached context;
- target specificity;
- gry availability;
- indexing state;
- expected latency;
- expected output size;
- capability coverage;
- evidence quality.

Role determines authority.

Information need determines intelligence source.

---

# 12. ASK Integration

ASK is read-only.

gry MAY provide ASK with:

- project structure;
- symbol relationships;
- architecture context;
- call paths;
- dependency relationships;
- documentation relationships;
- bounded source context;
- test relationships.

ASK MUST NOT interpret gry availability as permission to execute.

ASK MUST NOT mutate through gry.

Example:

```text
ASK
  ↓
"What is this project?"
  ↓
StructuralIntelligence
  ↓
gry/native evidence
  ↓
LLM synthesis
  ↓
grounded answer
```

---

# 13. FUZZ Integration

FUZZ exists to investigate uncertainty.

gry SHOULD be used to narrow the search space before expensive exploration.

Preferred flow:

```text
FUZZ
  ↓
structural hypothesis
  ↓
gry/native structural evidence
  ↓
identify uncertainty
  ↓
targeted investigation
  ↓
behavioral evidence
```

gry MUST NOT replace FUZZ.

Structural certainty and behavioral certainty are different evidence classes.

gry can answer:

```text
"What appears connected?"
```

FUZZ can investigate:

```text
"Does it actually behave this way?"
```

---

# 14. BUILD Integration

BUILD remains the sole repository mutation authority.

gry MAY assist BUILD before, during, or after mutation with:

- target resolution;
- dependency context;
- impact analysis;
- affected tests;
- post-edit structural analysis.

gry MUST NOT authorize mutation.

gry MUST NOT execute BUILD's mutation plan.

gry MUST NOT determine whether BUILD is authorized.

Correct:

```text
BUILD
  ↓
request structural evidence
  ↓
gry
  ↓
evidence
  ↓
BUILD
  ↓
authorized mutation
```

Incorrect:

```text
BUILD
  ↓
gry hook
  ↓
implicit authorization
  ↓
mutation
```

---

# 15. Hook Policy

Ohgo MUST NOT adopt gry's hook model merely because other agents use it.

gry's lifecycle hooks are a compatibility mechanism for multiple consuming runtimes.

Ohgo is a different runtime with its own Control Plane.

Therefore:

> **gry hooks are an integration option, not an Ohgo architecture.**

If direct capability calls provide sufficient integration, Ohgo SHOULD prefer them.

Hooks SHOULD be introduced only when they solve a concrete Ohgo lifecycle problem that cannot be solved more cleanly through the existing Control Plane.

No hook MAY bypass:

- task ownership;
- authority checks;
- budgets;
- mutation authorization;
- evidence classification;
- execution authorization;
- completion logic.

---

# 16. No Hidden Second Orchestrator

The system MUST have exactly one authoritative orchestration path.

Prohibited architecture:

```text
Ohgo Control Plane
       │
       ├── agent orchestration
       │
       └── gry hooks
             │
             └── secondary decisions
```

Preferred architecture:

```text
Ohgo Control Plane
       │
       └── capability request
               │
               ▼
              gry
               │
               ▼
             result
               │
               ▼
       Ohgo Control Plane
```

gry may compute.

gry may resolve.

gry may observe.

gry may report uncertainty.

gry MUST NOT independently orchestrate Ohgo.

---

# 17. State Ownership

Ohgo and gry MUST NOT maintain duplicate authoritative state.

### Ohgo owns

```text
task
objective
role
authority
budget
continuity
session
approval
execution
evidence decision
completion
```

### gry owns

```text
repository index
graph
symbol relationships
structural provenance
index generation
structural analysis
structural change/test impact
```

The systems MAY exchange references and derived evidence.

They MUST NOT attempt to synchronize each other's internal databases.

gry's local SQLite graph and indexing lifecycle remain gry-owned. Its incremental index can update changed files and affected relationships while preserving a previous queryable generation if a run fails or is cancelled.

---

# 18. Failure and Degradation Contract

Every gry operation MUST have an explicit outcome.

At minimum:

```text
AVAILABLE
UNAVAILABLE
NOT_INDEXED
STALE
PARTIAL
AMBIGUOUS
UNRESOLVED
FAILED
SUCCESS
```

Ohgo MUST NOT silently translate:

```text
gry unavailable
```

into:

```text
no structural evidence exists
```

Likewise:

```text
gry unresolved
```

MUST NOT become:

```text
target does not exist
```

The distinction between:

```text
not found
```

and:

```text
not established
```

must remain explicit.

---

# 19. Performance Rule

gry integration MUST reduce total work, not merely move work between components.

An integration is justified only when it produces a measurable or structural benefit such as:

- fewer source reads;
- fewer redundant searches;
- smaller model context;
- fewer model calls;
- stronger target resolution;
- stronger impact analysis;
- faster verification;
- better evidence quality.

If:

```text
Ohgo
  → gry
  → source
  → model
```

is slower and larger than:

```text
Ohgo
  → existing evidence
```

the gry call SHOULD NOT occur.

The system MUST prefer already-established evidence over rediscovery.

---

# 20. Context Budget Rule

gry output MUST be bounded before entering model context.

The integration SHOULD expose:

```text
target
scope
distance
provenance
source location
relevance
omitted count
```

rather than arbitrary graph dumps.

Hard output limits SHOULD be enforced.

The model MUST receive only the evidence necessary for the current objective.

---

# 21. Cache and Generation Rule

gry's cache/index generation remains gry-owned.

Ohgo MAY cache the resulting evidence according to Ohgo's own context/evidence lifecycle.

Ohgo MUST NOT:

- modify gry's database;
- inspect private gry tables as an integration contract;
- depend on gry's SQLite schema;
- reconstruct gry state;
- maintain a second copy of the graph.

The stable interface is the capability contract, not the storage implementation.

---

# 22. Version and Compatibility Rule

Ohgo SHOULD integrate against stable semantic capabilities.

It SHOULD NOT depend on:

- gry internal modules;
- private database tables;
- undocumented CLI output;
- implementation-specific graph internals;
- hook side effects;
- exact tool ordering.

Where MCP is used, Ohgo SHOULD treat the MCP interface as a transport boundary.

Where direct integration is used, it MUST still preserve the same capability semantics.

---

# 23. MCP Position

MCP is a transport mechanism.

It MUST NOT become the architecture.

Conceptually:

```text
Ohgo
  ↓
StructuralIntelligence
  ↓
MCP adapter
  ↓
gry
```

or:

```text
Ohgo
  ↓
StructuralIntelligence
  ↓
native adapter
```

Both are valid implementations.

The orchestration layer MUST remain independent of the transport.

---

# 24. Capability Selection Rule

Before adding any new Ohgo feature, the implementation MUST answer:

### A. Does Ohgo already own this capability?

If yes:

> Reuse or extend the existing owner.

### B. Does gry already provide it?

If yes:

> Integrate it rather than reproduce it.

### C. Is the gry capability insufficient for Ohgo's semantic contract?

If yes:

> Add only the missing Ohgo-specific layer.

### D. Is the capability fundamentally an authority concern?

If yes:

> It belongs to Ohgo, even if gry has a superficially similar feature.

This prevents unnecessary feature duplication.

---

# 25. Anti-Patterns

The following are explicitly prohibited unless a future architecture decision supersedes this specification.

## 25.1 Building Ohgo's own full repository graph

```text
gry graph
+
Ohgo graph
```

with overlapping ownership.

## 25.2 Making gry mandatory

```text
Ohgo cannot perform repository tasks without gry
```

## 25.3 Mapping Ohgo roles to gry profiles

```text
ASK = scout
BUILD = context
FUZZ = auditor
```

## 25.4 Making gry hooks the Ohgo lifecycle

```text
gry before_plan
gry before_edit
gry after_edit
gry before_finish
```

as a replacement for Ohgo's Control Plane.

## 25.5 Letting gry establish completion

```text
gry says clean
→ Ohgo says PROVEN
```

This is invalid.

## 25.6 Letting structural evidence become behavioral proof

```text
CALLS edge exists
→ runtime behavior proven
```

This is invalid.

## 25.7 Dumping the graph into the model

```text
graph
→ huge context
→ model
```

This defeats bounded intelligence.

## 25.8 Creating a generic abstraction with no immediate owner

Do not introduce architecture merely because two components could theoretically share it.

Every abstraction MUST have a concrete responsibility and current consumer.

---

# 26. Preferred Integration Shape

The preferred Ohgo structure is conceptually:

```text
internal/
    intelligence/
        ...
        structural/
            contract
            routing
            native
            gry
```

The exact package placement MUST follow the existing Ohgo package ownership and architecture rules.

The integration SHOULD expose one Ohgo-facing contract.

Conceptually:

```text
StructuralIntelligence

ResolveTarget
GetContext
Trace
Architecture
ChangeImpact
TestImpact
```

The actual interface MUST be derived from current Ohgo use cases rather than copied mechanically from gry's MCP API.

---

# 27. Evidence Flow

The canonical flow is:

```text
                 USER OBJECTIVE
                       │
                       ▼
                 OHGO INTENT
                       │
                       ▼
             INFORMATION REQUIREMENT
                       │
                       ▼
             EXISTING EVIDENCE?
                  /          \
                yes           no
                 │             │
                 ▼             ▼
              reuse      Structural Provider
                              │
                       ┌──────┴──────┐
                       │             │
                     native         gry
                       │             │
                       └──────┬──────┘
                              ▼
                       bounded evidence
                              │
                              ▼
                         LLM reasoning
                              │
                              ▼
                       untrusted proposal
                              │
                              ▼
                     OHGO CONTROL PLANE
                              │
                              ▼
                    authorize / reject / ask
                              │
                              ▼
                      EXECUTION PLANE
```

This is the canonical architecture.

---

# 28. Completion Rule

Only Ohgo can transition an objective into its terminal state.

gry can contribute evidence.

gry cannot produce:

```text
COMPLETED
PROVEN
FAILED
BLOCKED
NEEDS_REVIEW
```

as authoritative Ohgo task states.

A gry result is always an input to Ohgo's state machine.

---

# 29. Human-Control Rule

No gry capability may reduce an existing human approval boundary.

Adding gry MUST NOT cause:

```text
more automatic mutation
more automatic authorization
more implicit execution
less evidence
less explainability
```

The integration is successful only if it improves efficiency while preserving authority.

---

# 30. Design Principle

The integration should follow:

> **Dynamic Path, Static Authority.**

The path may change:

```text
native intelligence
        ↓
gry
        ↓
fallback
        ↓
targeted investigation
```

The authority does not.

Authority remains:

```text
Human
  ↓
Ohgo Control Plane
  ↓
authorized execution
```

---

# 31. Architectural Decision

The PizenLabs ecosystem treats gry as a **shared repository-intelligence capability**.

Ohgo is a **specialized agent runtime and smart harness** that may consume that capability.

Therefore:

> **gry should be generalized for many agents; Ohgo should be specialized for its own control model.**

Ohgo MUST NOT be forced into the generic hook, lifecycle, routing, or interaction model of other gry consumers merely because those consumers use gry.

Instead:

```text
                    PizenLabs ecosystem
                           │
                 ┌─────────┴─────────┐
                 │                   │
                gry                 Ohgo
                 │                   │
       shared structural       specialized control
          intelligence             runtime
                 │                   │
                 └─────────┬─────────┘
                           │
                    shared ecosystem
                    without shared
                    authority
```

This is the intended relationship.

---

# 32. Long-Term Evolution Rule

Future Ohgo features MUST first identify the existing owner before implementation.

Before adding functionality, answer:

```text
1. Is this an Ohgo authority concern?
2. Is this an execution concern?
3. Is this an intelligence concern?
4. Does Ohgo already have this?
5. Does gry already provide this?
6. Can the existing capability be reused?
7. Is the proposed abstraction actually necessary?
8. Does the change add measurable value?
9. Does it add another state owner?
10. Does it add another orchestration path?
```

If the answer to 6 is yes, implementation SHOULD reuse.

If the answer to 7 is no, implementation MUST NOT introduce the abstraction.

If the answer to 9 or 10 is yes, the design requires explicit architecture review.

---

# 33. Success Criteria

The Ohgo–gry integration is considered architecturally successful when:

- Ohgo works correctly without gry.
- gry improves structural intelligence where it is materially better.
- Ohgo does not reproduce gry's repository graph unnecessarily.
- gry does not become an Ohgo authority.
- gry hooks do not become Ohgo lifecycle rules.
- model context becomes smaller or more relevant.
- redundant exploration decreases.
- structural evidence becomes more precise.
- uncertainty remains explicit.
- completion remains Ohgo-owned.
- mutation remains Ohgo-owned.
- budget remains Ohgo-owned.
- task continuity remains Ohgo-owned.
- there is one authoritative orchestration path.
- each subsystem has one clear responsibility.

---

# 34. Final Architectural Rule

The following statement is normative:

> **Ohgo MUST be able to operate independently of gry, but SHOULD exploit gry wherever gry provides materially better repository structural intelligence than Ohgo would otherwise need to reproduce.**
>
> **gry provides evidence; Ohgo determines meaning, authority, action, and completion.**
>
> **Integration MUST reduce duplicated capability, duplicated state, duplicated exploration, and unnecessary model context.**
>
> **No integration with gry may introduce a second authority, second orchestrator, second task state, or unnecessary feature implementation inside Ohgo.**

This boundary is the basis for future Ohgo architecture decisions.
