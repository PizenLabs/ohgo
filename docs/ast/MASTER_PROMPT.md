# OHGO — Master Prompt
## Build the Project Structural Runtime and Complete AST Graph

You are working inside the OHGO repository.

Your objective is to design and implement a **native Project Structural Runtime** for OHGO, with a complete AST-backed project graph that becomes the local structural substrate for project discovery, context grounding, impact analysis, and future runtime intelligence.

This is an architectural implementation task, not a simple feature addition.

The most important constraint is:

> **Understand the existing codebase before designing new abstractions. Reuse existing logic where appropriate. Do not create a second implementation of behavior that already exists.**

The implementation must integrate with OHGO's existing architecture rather than becoming an isolated indexing subsystem.

---

# Core Architectural Goal

Build a native structural runtime that allows OHGO to understand the structure of a project cheaply, deterministically, incrementally, and with source-level provenance.

The intended architecture is:

```text
                    Project
                       │
                Filesystem / Git
                       │
                       ▼
             ┌───────────────────┐
             │ Structural Runtime│
             │                   │
             │ Files             │
             │ Directories       │
             │ Packages          │
             │ Modules           │
             │ Symbols           │
             │ Definitions       │
             │ References        │
             │ Imports           │
             │ Tests             │
             │ Configs           │
             │ Entrypoints       │
             └─────────┬─────────┘
                       │
                       ▼
                 Project Graph
                       │
          ┌────────────┼────────────┐
          ▼            ▼            ▼
      Discovery      Context      Impact
          │            │            │
          └────────────┼────────────┘
                       ▼
                  LLM Proposal
                       │
                       ▼
                  Control Plane
                       │
                       ▼
                 Execution Plane
```

The structural runtime is **not** the authority layer.

It must not decide whether an operation is permitted.

Its responsibility is to provide accurate structural facts and provenance.

The architecture remains:

```text
AST / Structural Runtime
        ↓
structural facts
        ↓
LLM reasoning / proposal
        ↓
Control Plane authority
        ↓
Execution Plane
        ↓
Verification / evidence
```

The LLM must never be treated as the source of structural truth merely because it inferred something from the graph.

---

# Non-Negotiable Principles

## 1. Existing-code-first

Before implementing anything:

- inspect the repository architecture;
- identify existing project discovery;
- identify existing filesystem abstractions;
- identify existing language parsing;
- identify existing symbol/index logic;
- identify existing Git integration;
- identify existing configuration discovery;
- identify existing MCP/code-search integrations;
- identify existing caches;
- identify existing watchers;
- identify existing evidence/provenance mechanisms;
- identify existing query abstractions;
- identify existing test infrastructure;
- identify existing project/context models.

Do not duplicate an existing subsystem without first proving that the existing subsystem cannot satisfy the required responsibility.

---

## 2. One source of truth per responsibility

Do not create parallel implementations such as:

```text
existing project scanner
        +
new project scanner
```

or:

```text
existing symbol discovery
        +
new symbol discovery
```

or:

```text
existing dependency resolver
        +
new dependency resolver
```

If existing functionality already owns a responsibility, either:

1. extend it,
2. adapt it,
3. extract a reusable abstraction,
4. or explicitly replace it.

Do not silently create a second source of truth.

---

## 3. Structural runtime is not context dumping

The graph must not be designed around producing a huge model prompt.

Instead, it must support cheap deterministic queries.

Examples:

```text
Where is symbol X defined?

What imports package Y?

Which files belong to package Z?

What symbols are defined in this file?

Which tests are associated with this symbol?

What are the project entrypoints?

What changed?

What depends on this symbol?

What is the likely impact of changing this file?
```

The LLM receives only the relevant graph slice.

---

## 4. Provenance is mandatory

Structural facts must retain enough information to trace them back to source.

At minimum, structural entities should be capable of identifying:

```text
file
package/module
symbol
location
source range
```

Edges should retain provenance where applicable.

A result should be capable of becoming evidence such as:

```text
Symbol: Runtime
File: internal/control/runtime.go
Location: line 87
Graph state: CURRENT
```

Do not produce structural claims that cannot be traced back to source.

---

## 5. Freshness must be explicit

The graph must never silently present stale information as current.

The runtime should have an explicit freshness/state model, for example:

```text
CURRENT
UPDATING
STALE
INVALID
```

The exact representation should be determined during Phase 1 based on existing OHGO state models.

Do not introduce redundant state if OHGO already has an appropriate mechanism.

---

## 6. Incremental updates are a first-class requirement

The architecture must not depend on:

```text
one file changed
    ↓
re-index entire repository
```

The intended model is:

```text
changed file
    ↓
detect change
    ↓
parse changed file
    ↓
replace its structural contribution
    ↓
update affected edges
    ↓
invalidate dependent facts when necessary
    ↓
graph becomes CURRENT
```

The implementation must distinguish between:

- changed source;
- unchanged source;
- affected structural facts;
- invalidated facts;
- graph freshness.

---

# PHASE 1 — CODEBASE UNDERSTANDING AND IMPLEMENTATION PLAN

## Objective

Do not implement the AST graph yet.

The purpose of Phase 1 is to understand OHGO sufficiently to determine:

1. what already exists;
2. what can be reused;
3. what must be extended;
4. what must be replaced;
5. what must be newly introduced;
6. where the structural runtime belongs architecturally;
7. how it should integrate with existing OHGO systems;
8. how to prevent overlapping or duplicate logic.

---

## Phase 1A — Architecture Reconnaissance

Read and map the relevant repository structure.

Start from:

```text
go.mod
README
AGENTS / contributor rules
architecture documentation
internal/
tests
configuration
startup
project initialization
state
Git integration
MCP/external integrations
```

Then trace the actual implementation.

Do not rely solely on documentation.

Documentation is a hypothesis until confirmed by source.

---

## Phase 1B — Find Existing Responsibilities

Explicitly investigate the following.

### Project discovery

Find:

```text
project root detection
workspace detection
repository detection
project metadata
project configuration
```

### Filesystem

Find:

```text
directory traversal
file discovery
file watching
file hashing
file metadata
```

### Language awareness

Find:

```text
Go parsing
AST usage
symbol discovery
package discovery
imports
references
types
tests
```

Also search for language-specific logic that may currently be hidden inside unrelated packages.

### Git

Find:

```text
changed files
working tree state
commit state
branch state
diffs
```

Determine whether existing Git functionality can feed incremental graph updates.

### Discovery/search

Find:

```text
grep
search
MCP search
codebase-memory integration
Gry integration
repository exploration
```

Determine which discovery mechanisms should remain complementary and which responsibilities can move into the structural runtime.

### Evidence

Find existing:

```text
evidence
provenance
source locations
observations
facts
verification
objective state
```

Determine how graph facts should integrate with these mechanisms.

### Context

Find:

```text
SharedContext
task context
role context
continuity
model context construction
```

Determine how graph queries can provide compact structural context without becoming another uncontrolled context dump.

---

# Phase 1C — Trace Existing Data Flow

Build a concrete implementation map:

```text
User
 ↓
Agent
 ↓
Control Plane
 ↓
Discovery
 ↓
Context
 ↓
Model
```

Then identify where project structure is currently discovered.

Document:

```text
current mechanism
owner
inputs
outputs
consumers
limitations
```

Do the same for:

```text
filesystem
Git
AST
symbols
search
MCP
project metadata
```

---

# Phase 1D — Detect Architectural Overlap

This is mandatory.

Explicitly identify every area where the new structural runtime could duplicate existing functionality.

For each candidate:

```text
Existing component
Responsibility
Current consumers
Potential overlap
Reuse?
Extend?
Replace?
Leave separate?
Reason
```

Do not implement until this table is understood.

---

# Phase 1E — Define the Boundary

Produce a precise architectural boundary.

The structural runtime should own structural facts.

It should not own:

```text
LLM reasoning
authority
tool permissions
execution
task ownership
commit policy
human approval
```

It may provide information to these systems.

The intended responsibility boundary is:

```text
Structural Runtime
    = what the project structurally is

LLM
    = what the model proposes or reasons

Control Plane
    = what OHGO authorizes

Execution Plane
    = what actually runs

Verifier
    = what can be proven
```

---

# Phase 1F — Design the Canonical Model

Design a language-neutral canonical project graph.

The initial model should consider entities such as:

```text
Project
Directory
File
Module
Package
Symbol
Test
Config
Entrypoint
```

And relationships such as:

```text
CONTAINS
DEFINES
IMPORTS
REFERENCES
IMPLEMENTS
CALLS
TESTS
DEPENDS_ON
ENTRYPOINT
```

Do not implement all of these merely because they are listed.

Classify them:

```text
MVP
Phase 2
Future
```

The initial implementation should remain intentionally small.

---

# Phase 1G — Language Adapter Boundary

Define a language-neutral interface:

```text
Source Language
      ↓
Language Adapter
      ↓
Canonical Structural Model
      ↓
Project Graph
```

The graph must not contain Go-specific concepts where a canonical concept is sufficient.

The first implementation target is Go.

The architecture must leave room for:

```text
Go
TypeScript
JavaScript
...
```

Do not implement additional languages unless required by the actual OHGO repository or explicitly justified by Phase 1 findings.

---

# Phase 1H — Incremental Update Design

Determine how the graph will remain current.

Investigate existing:

```text
file watchers
Git status
file hashes
mtime
cache
startup scanning
project lifecycle
```

Design:

```text
initial indexing
incremental file update
multiple-file update
file creation
file deletion
rename
Git checkout
branch switch
recovery from stale index
```

Do not create a new watcher if OHGO already has an appropriate lifecycle mechanism.

---

# Phase 1I — Query Contract

Define the smallest useful query API.

Initial candidates:

```text
packageTree()
files(package)
symbols(file)
definitions(name)
imports(package)
importers(package)
changedFiles()
entrypoints()
```

Determine the final API from actual OHGO requirements.

Each query should return deterministic structural information.

Avoid exposing internal storage details.

---

# Phase 1J — Persistence Decision

Do not assume a database.

Evaluate:

```text
in-memory graph
+
persistent metadata/cache
```

first.

Only introduce a database if measurements or architectural requirements justify it.

Document:

```text
what persists
what is rebuilt
what is cached
how stale state is detected
how invalid state is recovered
```

---

# Phase 1K — Phase 1 Deliverable

At the end of Phase 1, produce an implementation plan containing:

```text
1. Existing architecture map
2. Existing reusable components
3. Overlapping logic
4. Components to extend
5. Components to replace
6. New components required
7. Canonical graph model
8. Language adapter boundary
9. Incremental update design
10. Query API
11. Persistence strategy
12. Integration points
13. Test strategy
14. Benchmark strategy
15. Migration strategy
16. Risks
17. Explicit non-goals
18. Implementation order
```

Also provide a dependency-ordered implementation sequence.

The plan must be concrete enough that Phase 2 can be executed without rediscovering the architecture.

---

# PHASE 1 STOP CONDITION

**STOP after the plan.**

Do not begin implementation automatically.

The plan must be reviewable before Phase 2 begins.

If an architectural contradiction is discovered, surface it rather than silently choosing an implementation.

---

# PHASE 2 — BUILD THE COMPLETE OHGO STRUCTURAL RUNTIME

Only begin after Phase 1 has established the architecture and implementation boundary.

---

## Phase 2A — Build the Structural Core

Implement the canonical graph and its invariants.

The graph must provide:

```text
nodes
edges
identity
source locations
freshness
version/update semantics
```

Define clear ownership.

Avoid global mutable state unless the existing architecture explicitly requires it.

---

# Phase 2B — Go Language Adapter

Implement Go structural extraction.

At minimum:

```text
files
packages
imports
symbols
definitions
locations
tests
```

Use the Go standard library AST/parser/type machinery where appropriate.

Do not introduce heavyweight dependencies without architectural justification.

---

# Phase 2C — Graph Construction

Implement:

```text
filesystem
    ↓
language adapter
    ↓
canonical model
    ↓
graph
```

Ensure graph construction is deterministic.

The same source state must produce the same structural graph.

---

# Phase 2D — Incremental Graph Updates

Implement:

```text
file create
file modify
file delete
file rename
multiple file changes
```

Ensure updates replace only affected structural information where practical.

Do not rebuild the entire repository for a single-file change unless the implementation can prove that broader invalidation is structurally required.

---

# Phase 2E — Freshness and Recovery

Implement explicit graph state.

Handle:

```text
startup
index missing
index corrupted
source changed
partial update
interrupted update
branch switch
Git checkout
```

The runtime must fail safely.

A stale graph must be identifiable.

A corrupt graph must be recoverable.

---

# Phase 2F — Query Runtime

Implement the approved query API.

Queries must be:

```text
deterministic
cheap
bounded
source-aware
```

Do not return massive graph dumps by default.

Return only the relevant structural slice.

---

# Phase 2G — Evidence Integration

Integrate structural results with OHGO's existing evidence/provenance model.

A graph result should be capable of becoming a grounded observation such as:

```text
FACT
  symbol = Runtime
  source = internal/control/runtime.go
  location = ...
  graph_state = CURRENT
```

Do not create a parallel evidence model if OHGO already has one.

---

# Phase 2H — Discovery Integration

Integrate the structural runtime into OHGO discovery only after the graph itself is correct.

The intended behavior is:

```text
LLM needs project structure
        ↓
OHGO queries structural runtime
        ↓
structural slice
        ↓
targeted source read if needed
        ↓
LLM reasoning
```

Do not force every discovery task through the graph.

Filesystem tools, search, MCP, and other mechanisms remain available where structural queries are insufficient.

The graph should reduce unnecessary exploration, not artificially constrain discovery.

---

# Phase 2I — Role Integration

Respect OHGO's existing authority model.

### ASK

May use structural queries for understanding.

Must remain read-only.

### FUZZ

May use structural queries to accelerate investigation.

May perform approved investigative execution.

Must not gain source mutation authority merely because it has graph access.

### BUILD

May use structural queries for implementation and impact analysis.

Graph information must not bypass existing BUILD authority controls.

### COMMIT

May use graph/evidence information where useful.

Do not make graph access a new mutation authority.

---

# Phase 2J — Impact Analysis

After the structural foundation is stable, implement the strongest useful dependency relationships supported by reliable source analysis.

Potential relationships:

```text
references
implementations
callers
callees
tests
package dependencies
```

Do not claim semantic relationships that the implementation cannot reliably establish.

If a relationship is uncertain:

```text
UNKNOWN
```

is preferable to a fabricated edge.

---

# Phase 2K — Benchmark

Benchmark the actual OHGO repository.

Measure at minimum:

```text
initial indexing time
incremental single-file update
incremental multi-file update
query latency
memory usage
graph size
cache size
```

Measure realistic repository states:

```text
clean tree
large change
small change
large refactor
branch switch
cold start
warm start
```

Do not optimize theoretical metrics while ignoring real OHGO workloads.

---

# Phase 2L — Integration Verification

Verify that the structural runtime does not introduce:

```text
duplicate discovery
duplicate parsing
context bloat
startup regression
TUI latency
authority bypass
stale structural facts
silent failure
incorrect completion evidence
```

Run the repository's existing required gates:

```text
gofmt
go build
go vet
go test
go test -race
```

plus any project-specific validation required by AGENTS or architecture rules.

---

# Phase 2M — Real Runtime Validation

Do not stop at unit tests.

Exercise real OHGO flows where structural discovery matters.

At minimum validate scenarios such as:

```text
ASK → understand project structure

FUZZ → investigate an unfamiliar subsystem

BUILD → modify a symbol with dependencies

BUILD → assess affected tests

project file modification → graph update

Git branch/change → graph freshness

stale/corrupt graph → recovery
```

Verify both:

```text
structural correctness
+
runtime integration correctness
```

---

# Architectural Invariants

The final implementation must preserve these invariants.

### Invariant 1

```text
Graph facts are derived from project state.
```

### Invariant 2

```text
Graph facts have provenance.
```

### Invariant 3

```text
Stale state is explicit.
```

### Invariant 4

```text
A single source of truth exists for each structural responsibility.
```

### Invariant 5

```text
Incremental updates do not unnecessarily rebuild the repository.
```

### Invariant 6

```text
Structural knowledge does not grant execution authority.
```

### Invariant 7

```text
LLM inference is not silently converted into structural fact.
```

### Invariant 8

```text
Unknown is preferable to an unsupported structural claim.
```

### Invariant 9

```text
The graph provides slices, not uncontrolled context dumps.
```

### Invariant 10

```text
The structural runtime remains useful even when the LLM is unavailable.
```

---

# Explicit Non-Goals

Do not turn this task into:

```text
a general-purpose IDE
a full code intelligence platform
a replacement for Git
a replacement for grep
a replacement for MCP
a semantic AI database
a remote indexing service
a model training system
```

Do not add features merely because they are technically interesting.

Every extension must have a concrete OHGO use case.

---

# Final Success Criterion

The work is complete only when OHGO can reliably answer structural questions about the project through its own native runtime with substantially less exploratory overhead than generic filesystem/search discovery.

The final architecture should look conceptually like:

```text
                 Project State
                      │
             ┌────────┴────────┐
             │                 │
        Filesystem            Git
             │                 │
             └────────┬────────┘
                      ▼
             Structural Runtime
                      │
        ┌─────────────┼─────────────┐
        ▼             ▼             ▼
      Files        Symbols       Dependencies
        │             │             │
        └─────────────┼─────────────┘
                      ▼
                Project Graph
                      │
                Query Runtime
                      │
          ┌───────────┼───────────┐
          ▼           ▼           ▼
         ASK         FUZZ        BUILD
          │           │           │
          └───────────┼───────────┘
                      ▼
                 LLM Proposal
                      │
                      ▼
                Control Plane
                      │
                      ▼
               Execution Plane
                      │
                      ▼
                  Evidence
```

The strategic goal is not simply:

> “OHGO has an AST index.”

The goal is:

> **OHGO has a fast, native, incremental, provenance-aware structural runtime that lets the harness understand the project before asking the model to explore it.**

That structural runtime becomes the foundation on which future discovery, context optimization, impact analysis, and semantic project intelligence can be built.
