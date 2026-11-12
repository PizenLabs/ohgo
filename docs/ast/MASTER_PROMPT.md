# OHGO — Adaptive Multi-Language Project Structural Runtime
## Master Architecture & Implementation Prompt

## Mission

Design and implement the next evolution of OHGO's existing Project Structural Runtime into an **adaptive, incremental, multi-language structural intelligence system**.

The objective is not to build a more complicated AST parser, a compiler, an IDE backend, or a standalone knowledge graph.

The objective is to let OHGO understand a codebase structurally, retrieve only the information required by the current task, and provide the LLM with compact, deterministic, verifiable evidence.

The runtime must support multiple programming languages without creating separate structural systems for each language.

It must remain lightweight when structural intelligence is unnecessary and become progressively more capable when the task requires it.

The governing principle is:

> **Parse only when needed. Understand only as deeply as required. Return only relevant structure. Escalate only when evidence requires it.**

OHGO's existing philosophy remains authoritative:

- LLMs reason.
- The Engine decides.
- Capabilities execute.
- Humans remain in control.

The architecture must preserve:

**Dynamic Path, Static Authority, Truthful State Transition.**

---

# PART I — NON-NEGOTIABLE ARCHITECTURAL PRINCIPLES

## 1. Preserve the existing Structural Runtime

Phase 2 has already implemented and verified:

- Incremental project snapshot.
- `ast.Index`.
- `StructuralSource`.
- Canonical snapshot metadata.
- Freshness and generation.
- Source and scope fingerprints.
- Canonical node and edge vocabulary.
- `SourceRef`.
- Go parsing through the existing `GoBackend`.
- File, package, test, config and entrypoint extraction.
- Incremental updates and atomic publication.
- Structural query APIs.
- Read-only `structure` tool.
- Structural evidence integration through `cognition.EvidenceState`.
- Shared ignore scope.
- Snapshot provenance.
- Benchmark infrastructure.

These are the existing foundation.

**Do not rebuild, replace, duplicate, or casually redesign these components.**

Before implementation, inspect their actual current state and identify which contracts are already stable.

The intended architecture is an additive evolution of the existing system, not a rewrite.

Preserve:

```text
One StructuralSource
One canonical project snapshot/index
One shared scope implementation
One evidence model
One structural query boundary
One authority system
```

Do not introduce another independent index, graph store, parser registry, evidence store, or project-state owner.

If the current package layout is imperfect, prefer an additive solution. Extract or rename packages only when a concrete dependency or ownership problem requires it.

## 2. Separate syntax parsing from structural intelligence

The runtime must have three distinct conceptual layers:

```text
SOURCE
  |
  v
PARSING SUBSTRATE
  |
  v
LANGUAGE ADAPTER
  |
  v
CANONICAL STRUCTURAL FACTS
  |
  v
STRUCTURAL GRAPH
  |
  v
QUERY / EVIDENCE
```

Responsibilities:

**Parsing substrate**
- Converts source into syntax information.
- Knows grammar and syntax-tree details.
- Does not own project identity, freshness, evidence, authority, or task state.

**Language adapter**
- Interprets language-specific syntax.
- Extracts normalized structural facts.
- Preserves language-specific information where normalization would lose meaning.
- Must not expose raw parser nodes as the public structural API.

**Structural Runtime**
- Owns snapshots, scope, generations, graph projections, incremental updates, freshness, query execution, provenance and completeness.
- Must remain parser-agnostic.
- Does not contain language-specific grammar traversal logic.

The Control Plane must never need to understand Tree-sitter node types, grammar-specific queries, or parser implementation details.

## 3. Use the right parsing substrate for each language

Preserve the existing Go implementation:

```text
Go → existing GoBackend → Go parser
```

Do not replace it with Tree-sitter merely for technological uniformity.

For additional languages, evaluate Tree-sitter as the preferred syntax-parsing substrate.

The architecture should support:

```text
Go             → existing native Go adapter
TypeScript     → Tree-sitter adapter
TSX            → TypeScript/TSX grammar support
JavaScript     → Tree-sitter adapter
JSX            → JavaScript/JSX grammar support
Java           → Tree-sitter adapter
C#             → Tree-sitter adapter
Rust           → Tree-sitter adapter
Kotlin         → Tree-sitter adapter
Ruby           → Tree-sitter adapter
C / C++        → Tree-sitter adapter
```

The list is an intended language-coverage target, not permission to invent unsupported capabilities.

During Phase 1, verify the availability, maintenance, version compatibility, license, build constraints and Go integration of every required grammar.

During Phase 2, implement the adapters using compatible, pinned dependencies.

If a grammar cannot be integrated safely or reproducibly, report the exact limitation and preserve an explicit unsupported-language state. Never silently claim language support that does not exist.

Do not use floating dependency versions.

Do not add runtime network access, external parser installation, or machine-specific shared-library requirements.

Prefer reproducible build-time integration.

## 4. Keep syntax, structural and semantic knowledge separate

The runtime must expose three levels of structural knowledge.

### Level A — Syntax facts

Examples:

- Files and directories.
- Modules, packages and namespaces.
- Classes, structs, interfaces, traits and enums.
- Functions, methods, fields, constants and variables.
- Definitions and declarations.
- Imports and exports.
- Basic test declarations.
- Entrypoints.
- Configuration and project manifests.

This is the primary multi-language target.

### Level B — Static structural relationships

Examples:

- IMPORTS.
- EXPORTS.
- REFERENCES.
- CALLS.
- EXTENDS.
- IMPLEMENTS.
- TESTS.
- ENTRYPOINT.

Only emit relationships supported by concrete syntax or reliable static resolution.

For example:

```text
class PaymentService implements PaymentProvider
```

may provide explicit syntax evidence for an IMPLEMENTS relationship.

However, if the destination cannot be resolved unambiguously, represent the relationship as unresolved or partial.

Never manufacture a relationship to make the graph appear complete.

For TESTS, distinguish a static relationship from actual behavioral coverage. Finding a test file that references a symbol does not prove that the test covers that symbol.

### Level C — Semantic resolution

Examples:

- Resolved overloads.
- Generic type resolution.
- Interface implementation verification.
- Trait resolution.
- Method override resolution.
- C++ template instantiation.
- Macro expansion.
- Language-specific type inference.

This layer may eventually use:

```text
Go       → go/types
TypeScript → TypeScript compiler
C#       → Roslyn
Java     → Java semantic tooling
Rust     → rust-analyzer ecosystem
C++      → clang tooling
```

However, these engines are not prerequisites for the initial multi-language structural runtime.

Design a clean extension boundary for semantic resolvers, but do not implement expensive language servers or compiler integrations merely to increase feature count.

Semantic resolution must be an explicit escalation, never a hidden cost of ordinary structural queries.

---

# PART II — PHASE 1: COMPLETE ARCHITECTURE AUDIT

## Objective

Understand the complete existing OHGO architecture and produce one integrated implementation plan.

Phase 1 is read-only.

Do not modify production code, dependencies, generated files, configuration, or documentation.

Do not start implementation.

## 1. Understand the repository before deciding the design

Inspect the actual current repository.

Read and reconcile:

- `OHGO_ARCHITECTURE_MAP.md`
- `OHGO_ARCHITECTURE_RULES.md`
- `ARCHITECTURE.md`
- `STATE_AND_PERSISTENCE.md`
- `README.md`
- The completed Phase 1 structural-runtime plan.
- The Phase 2 implementation record and benchmark report.
- Relevant AGENTS and contribution instructions.
- Relevant package documentation and architecture tests.

Inspect the actual implementation, not just documentation.

At minimum, trace:

```text
cmd/ohgo/build.go
    |
    v
tool registration
    |
    v
role / stage / capability admission
    |
    v
StructuralSource
    |
    v
ast.Index
    |
    v
language parsing backend
    |
    v
canonical facts and relationships
    |
    v
structural queries
    |
    v
ToolOutcome
    |
    v
cognition.EvidenceState
    |
    v
task continuity / completion verification
```

Inspect the full path from user intent to structural-tool availability.

Do not assume that a registered tool is necessarily offered to the current role or task stage.

Identify where read-only structural tools can be accidentally excluded by admission rules.

## 2. Identify existing ownership and avoid duplication

Produce a concrete ownership map covering:

| Concern | Existing owner | Required evolution |
|---|---|---|
| Project identity | Existing project subsystem | Reuse |
| Repository scope | Existing workspace/scope | Reuse |
| Ignore rules | `intelligence/ignore` | Reuse |
| Parsing | Existing AST/backend | Extend |
| Language selection | Inspect current registry | Evolve if needed |
| Snapshot | Existing `ast.Index` | Preserve |
| Incremental updates | Existing index lifecycle | Extend |
| Freshness | Existing snapshot model | Preserve |
| Graph projection | Existing structural model | Extend |
| Query execution | Existing structural query layer | Extend |
| Tool availability | Existing tool/control admission | Verify |
| Evidence | `cognition.EvidenceState` | Extend additively |
| Task continuity | Existing task subsystem | Preserve |
| Git changes | Existing VCS subsystem | Reuse |
| Authority | Existing Control Plane | Preserve |

Identify any overlapping logic before proposing new abstractions.

The most important question is:

> Can this capability be implemented by extending an existing owner, or would the proposed design introduce a second source of truth?

Reject unnecessary duplication.

## 3. Audit language and parser integration options

For each target language, inspect:

- Grammar availability.
- Grammar maintenance.
- License.
- Tree-sitter ABI compatibility.
- Go binding compatibility.
- Dependency and binary-size impact.
- CGO/build implications.
- Cross-platform reproducibility.
- Parser and tree resource lifecycle.
- Query/extraction complexity.
- Syntax coverage for relevant language features.

Pay particular attention to resource lifecycle and concurrency if using the Go Tree-sitter binding. Every allocated parser, tree, query or cursor must have a clear ownership and cleanup path.

Do not choose a dependency solely because it is popular.

Select a versioned, reproducible combination that fits OHGO's Go-first, local-first runtime.

## 4. Audit adaptive activation

This is a core product requirement.

Find how OHGO currently determines:

- User intent.
- ASK / FUZZ / BUILD role.
- Task stage.
- Available read-only capabilities.
- Tool selection.
- Tool budgets.
- Context projection.
- Completion requirements.

Determine how structural intelligence can be activated only when useful.

Identify the smallest integration point that allows the Control Plane to select a structural depth without creating a second intent router.

Do not make the LLM responsible for deciding whether expensive indexing is globally enabled.

The LLM may propose deeper investigation, but the runtime and Control Plane must enforce capability, scope and budget.

## 5. Produce one integrated architecture plan

The plan must define:

1. Existing components that remain unchanged.
2. Components that need extension.
3. New components that are demonstrably necessary.
4. Dependency direction and ownership.
5. Language detection and registry design.
6. Canonical fact model.
7. Adapter and parser boundaries.
8. Syntax and relationship extraction.
9. Incremental update lifecycle.
10. Adaptive activation policy.
11. Query and context-projection model.
12. Freshness and completeness semantics.
13. Evidence integration.
14. Tool admission and role integration.
15. Failure handling and recovery.
16. Testing and benchmarking strategy.
17. Migration and rollback.
18. Known limitations and deferred work.

Include a dependency diagram and a complete source-to-evidence lifecycle diagram.

Explicitly state which files/packages will change and why.

Include an implementation sequence, but keep it as one integrated implementation objective rather than independent tasks.

### Phase 1 hard stop

Present the architecture audit, ownership map, risk assessment, dependency decisions, implementation plan and acceptance criteria.

Then STOP.

Do not modify code.

Wait for explicit human approval before Phase 2.

---

# PART III — PHASE 2: IMPLEMENT THE COMPLETE ADAPTIVE STRUCTURAL RUNTIME

## Objective

After approval, implement the complete multi-language and adaptive structural runtime as one coherent end-to-end system.

Do not turn this into isolated implementation tasks that lose the overall architecture context.

Keep the complete source-to-evidence lifecycle in context throughout implementation.

Use internal checkpoints to control correctness, but do not treat those checkpoints as independent architectural projects.

Do not request another approval at every checkpoint.

If an important architectural fact is missing or contradicts the approved plan:

```text
STOP
    |
    v
Inspect actual ownership and callers
    |
    v
Reconcile architecture rules
    |
    v
Update the implementation model
    |
    v
Continue safely
```

Never guess an ownership boundary and encode it into production code.

## 1. Establish a unified language adapter boundary

Evolve the existing adapter boundary only as needed.

A conceptual contract could look like:

```go
type LanguageAdapter interface {
    ID() string
    Languages() []LanguageID
    Extensions() []string

    Parse(
        ctx context.Context,
        input SourceInput,
    ) (FileFacts, error)
}
```

This is illustrative, not a requirement to copy blindly.

The final design must account for:

- Language and grammar identity.
- File detection.
- Parser version.
- Parser capability.
- Parse errors.
- Partial syntax trees.
- Source coordinates.
- Extracted facts.
- Supported relationship kinds.
- Provenance.
- Context cancellation.
- Resource cleanup.

The existing Go backend must continue operating through the same canonical snapshot and query system.

Do not create a second Go parsing path.

Do not let `ast.Index` depend directly on Tree-sitter.

Do not let language-specific packages depend on Control Plane or execution packages.

The registry must be deterministic and reject ambiguous adapter ownership rather than silently selecting an arbitrary parser.

## 2. Introduce multi-language Tree-sitter extraction

Implement Tree-sitter-backed adapters for the approved target languages.

Share parsing infrastructure where practical, but keep language-specific extraction logic explicit.

Do not build one giant generic parser that attempts to understand every grammar through fragile reflection or string matching.

Each language adapter must define the syntax patterns it supports and map those patterns to canonical facts.

Initial structural extraction should cover:

**TypeScript / TSX / JavaScript / JSX**
- Imports and exports.
- Classes, interfaces, type aliases, enums.
- Functions, methods, properties.
- Components and common declaration patterns.
- Basic calls and references.
- Routes and entrypoint conventions where reliably detectable.
- Test declarations and common test-file conventions.

**Java**
- Packages and imports.
- Classes, interfaces, enums, records where supported.
- Methods, fields and constructors.
- Explicit inheritance and implementation syntax.
- Basic references and calls.
- Test declarations and common test conventions.

**C#**
- Namespaces and using directives.
- Classes, structs, interfaces, records and enums.
- Methods, properties and fields.
- Explicit inheritance/interface declarations.
- Basic references and calls.
- Test declarations and common test conventions.

**Rust**
- Crates, modules and use declarations.
- Structs, enums, traits and impl blocks.
- Functions, methods and associated items.
- Explicit trait implementation syntax.
- Basic references and calls.
- `#[test]` and related test declarations.

**Kotlin**
- Packages and imports.
- Classes, objects, interfaces and data classes.
- Functions, properties and methods.
- Explicit inheritance and implementation syntax.
- Basic references and calls.
- Test declarations and common test conventions.

**Ruby**
- Modules and classes.
- Methods and constants.
- Require/load relationships.
- Basic call and reference extraction.
- Common test-file and test declaration conventions.
- Executable and entrypoint conventions where reliably detectable.

**C / C++**
- Includes and namespaces.
- Classes, structs, enums and templates.
- Functions, methods and declarations.
- Basic calls and references.
- Header/source relationships where reliably detectable.
- Main entrypoint detection.
- Common test declarations and conventions.

The implementation may expose different extraction completeness per language.

Do not fabricate a common capability simply because the language has a parser.

Where a language feature cannot be reliably extracted, return an explicit unsupported or unresolved state.

### Language-specific details

Do not flatten every language into `class/function`.

Preserve meaningful distinctions such as:

```text
Go:         package, method, interface
TypeScript: interface, type alias, namespace
Java:       package, record, constructor
C#:         namespace, record, property
Rust:       crate, trait, impl, associated type
Kotlin:     object, data class, companion object
Ruby:       module, class, method
C++:        namespace, template, class, struct
```

Use a shared canonical vocabulary for cross-language queries while preserving original language kind and attributes.

Do not create an enormous universal schema that loses language-specific meaning.

Do not use unbounded `map[string]any` structures to avoid designing the model.

Use explicit, typed, bounded structures.

## 3. Build the canonical multi-language structural model

Extend the existing node/edge model rather than creating another graph representation.

The canonical model should be able to express:

### Nodes

```text
PROJECT
DIRECTORY
FILE

MODULE
PACKAGE
NAMESPACE

SYMBOL
TYPE
CLASS
STRUCT
INTERFACE
TRAIT
ENUM

FUNCTION
METHOD
FIELD
PROPERTY
CONSTANT
VARIABLE

TEST
CONFIG
ENTRYPOINT
```

If the existing model uses a generic SYMBOL node with a symbol kind, preserve that design when it is more appropriate.

### Relationships

```text
CONTAINS
DEFINES

IMPORTS
EXPORTS

REFERENCES
CALLS

EXTENDS
IMPLEMENTS
OVERRIDES

TESTS
ENTRYPOINT
```

Only add additional edge types when a concrete cross-language query requires them.

Each relationship must record enough provenance to explain:

- Source and target.
- Source location.
- Target resolution state.
- Language.
- Adapter/parser identity.
- Resolver identity where applicable.
- Snapshot identity and generation.
- Whether the relationship is syntactic, statically resolved or semantically resolved.

Use explicit resolution states such as:

```text
RESOLVED
UNRESOLVED
AMBIGUOUS
UNSUPPORTED
```

Do not express UNKNOWN as a fabricated edge.

Do not present syntactic relationships as semantic certainty.

## 4. Preserve source provenance and coordinate correctness

Every extracted fact must be traceable to source.

Preserve the existing `SourceRef` contract and extend it only when needed.

Ensure consistent coordinate semantics across languages:

- File-relative coordinates.
- Defined line and column base.
- UTF-8 and multibyte character correctness.
- Start/end range correctness.
- Stable behavior across adapters.
- Snapshot-scoped identity where cross-generation identity cannot be guaranteed.

Do not assume all grammars use identical coordinate semantics.

Normalize coordinates at the adapter boundary.

Every graph relationship that is intended to serve as evidence must have a source witness.

## 5. Implement adaptive structural activation

This is one of the most important requirements.

**Structural intelligence must not be activated at maximum depth for every task.**

The runtime must support progressively deeper structural modes.

Conceptual policy:

### NONE — No structural activation

Use for:
- Simple conversational requests.
- Non-code tasks.
- Direct operations where structural knowledge adds no material value.
- Small, known-scope edits where a direct read is sufficient.

Requirements:
- No full repository parsing.
- No unnecessary graph construction.
- No structural tool overhead.
- Existing execution behavior remains unchanged.

### OVERVIEW — Lightweight project understanding

Use for:
- "What is this project?"
- "Analyze this codebase."
- "What technologies does it use?"
- "Show me the main project structure."

Requirements:
- Use existing project metadata and lightweight file inventory.
- Detect language mix, modules, manifests, major directories and likely entrypoints.
- Parse only the files necessary for a useful overview.
- Do not build full reference/call relationships.
- Do not parse every supported source file just to answer a high-level question.
- Return explicit language and inventory coverage.

### TARGETED — Focused structural understanding

Use for:
- Finding definitions.
- Understanding one package/module.
- Locating references.
- Understanding a specific function or class.
- Tracing a known route or feature.

Requirements:
- Parse or refresh only the relevant files and dependencies.
- Query the smallest relevant graph slice.
- Return targeted coordinates and provenance.
- Avoid full-project relationship expansion.

### IMPACT — Cross-file investigation

Use for:
- Impact analysis.
- Refactoring.
- Multi-file changes.
- Dependency investigation.
- Caller/callee analysis.
- Test association discovery.
- Architecture tracing.

Requirements:
- Expand references, callers, importers and test associations as needed.
- Bound graph traversal depth and result count.
- Preserve unresolved and ambiguous relationships.
- Do not imply behavioral or semantic impact beyond static evidence.

### SEMANTIC — Explicit semantic escalation

Use only when:
- The task specifically requires semantic resolution.
- Static structural facts are insufficient.
- A supported semantic resolver exists.
- The expected benefit justifies the additional cost.

Requirements:
- Semantic resolver activation must be explicit.
- Respect existing budgets and cancellation.
- Do not silently launch language servers or compilers.
- Do not pretend syntax parsing provides semantic certainty.

The actual names and implementation mechanism may follow existing architecture, but the behavioral distinction between these levels is mandatory.

### Activation policy

The Control Plane owns activation decisions.

The LLM may request deeper structural investigation, but the runtime must enforce:

- Task intent.
- Role authority.
- Scope.
- Capability availability.
- Budget.
- Freshness.
- Supported language coverage.

For a read-only request such as:

```text
check this project and analyze what this project is about?
```

the expected route is ASK, not BUILD.

The read-only `structure` capability must be available to eligible read-only stages. Tool registration alone is insufficient: verify the full registration → offer → admission → execution path.

For unsupported languages, use an explicit lexical fallback.

Do not disguise fallback exploration as native structural coverage.

## 6. Implement progressive project understanding

The runtime must not require complete parsing before it can provide useful information.

Use progressive availability:

```text
Repository discovery
      |
      v
Project metadata
      |
      v
Language inventory
      |
      v
Lightweight overview
      |
      v
Targeted syntax extraction
      |
      v
Structural relationships
      |
      v
Semantic escalation
```

Each stage should return useful results independently.

A user asking what a project does should not trigger a full cross-reference build.

A user asking which callers depend on a symbol should not need to parse unrelated languages or modules.

The runtime should be able to answer from partial but accurately described knowledge.

## 7. Preserve incremental operation

Extend the existing incremental lifecycle to cover multiple languages.

Expected behavior:

```text
source/scope change
      |
      v
detect affected files
      |
      v
resolve language adapter
      |
      v
parse changed supported files
      |
      v
extract canonical facts
      |
      v
replace affected facts
      |
      v
re-resolve affected relationships
      |
      v
publish one new generation atomically
```

Requirements:

- Unchanged files are not reparsed unnecessarily.
- A language-specific change does not reparse unrelated languages.
- File additions, deletions and renames are handled.
- Scope changes update membership and relationships correctly.
- Module/config changes invalidate only the derived facts they affect.
- Unsupported files remain visible in inventory.
- A parse failure cannot be mistaken for an empty file.
- Cancellation does not publish a half-updated generation.
- Concurrent readers see one coherent published snapshot.
- A failed update cannot masquerade as CURRENT.
- Existing freshness semantics remain valid.

Tree-sitter's internal incremental edit API may be used when it materially reduces work, but do not require persistent syntax-tree retention for every file.

First preserve project-level incremental correctness.

Retain parsed trees only when benchmark results justify their memory and lifecycle cost.

## 8. Separate freshness from completeness

This distinction must be explicit.

A snapshot can be CURRENT but only partially understood.

For example:

```text
snapshot:
  freshness: CURRENT
  inventory_complete: true
  language_coverage:
    Go: supported
    TypeScript: supported
    Ruby: unsupported
  parse_failures: 2
  structural_completeness: PARTIAL
```

CURRENT means the snapshot matches the runtime's observed source and scope state according to its detection contract.

It does not mean every file or every language was parsed successfully.

Every query must disclose relevant limitations:

- Unsupported languages.
- Files not parsed.
- Parse failures.
- Truncation.
- Ambiguous relationships.
- Unresolved relationships.
- Stale or updating state.
- Query-specific incompleteness.

Do not claim that tests, references, definitions or source files are absent unless the relevant inventory and query coverage are sufficient to support the absence claim.

## 9. Implement language-aware query projection

The LLM must never receive raw Tree-sitter syntax trees as its normal interaction interface.

It should receive compact structural projections.

Examples:

```text
Project:
  languages:
    - TypeScript
    - JavaScript

  framework:
    - Next.js

  entrypoints:
    - app/page.tsx
    - app/layout.tsx

  modules:
    - app/
    - lib/
    - components/

  tests:
    discovered: 14
```

Or:

```text
Symbol: getAllPosts

Defined:
  lib/content.ts:42

Calls:
  parseFrontmatter
  readingTime

Referenced by:
  app/page.tsx:18
  app/posts/[slug]/page.tsx:31

Snapshot:
  generation: 24
  state: CURRENT

Completeness:
  PARTIAL
  unresolved relationships: 2
```

The exact format should follow existing tool-output conventions.

Queries should support the existing views and evolve them as necessary:

```text
overview
files
packages/modules
symbols
entrypoints
configs
tests
references
callers
callees
importers
impact
```

Do not expose arbitrary grammar-node queries to the LLM.

Do not dump the entire graph.

Do not include unrelated file contents in structural query output.

Every query must support deterministic limits and bounded context cost.

## 10. Integrate structural evidence with the existing EvidenceState

All structural tools must continue using the existing evidence path.

Do not create a second evidence framework.

Evidence should identify:

- Snapshot ID.
- Generation.
- Freshness.
- Relevant source coordinates.
- Language and adapter provenance.
- Relationship type.
- Resolution state.
- Completeness.
- Truncation.
- Unsupported capability where applicable.

Preserve `agent.ClassifyPath` and existing provenance rules.

Evidence from a partial or unsupported language must not be upgraded into complete evidence.

Ensure evidence from different generations cannot silently combine into a single apparently current claim.

A structural observation does not automatically prove task completion.

Completion remains governed by the existing objective and verification model.

## 11. Integrate with task intent, tool admission and roles

Trace the entire path and fix any structural-tool availability mismatch.

Required behavior:

**ASK**
- Read-only structural queries are available.
- No mutation capability is introduced.
- Broad project understanding prefers OVERVIEW when useful.

**FUZZ**
- May use structural and lexical capabilities for investigation.
- May execute authorized investigation commands.
- Must not mutate repository source.

**BUILD**
- Can use structural information as read-only input.
- Existing BUILD mutation authority remains unchanged.
- Structural availability must not imply permission to mutate.

**COMMIT**
- May consume appropriate structural evidence.
- Does not acquire unrelated structural or source-mutation authority.

For read-only analysis, do not select BUILD merely because the task requires repository inspection.

Do not add a new strategy picker or a parallel routing system.

Integrate through the current Control Plane ownership.

## 12. Preserve lightweight runtime behavior

The design must remain local-first and operationally predictable.

Do not introduce:

- Always-on full indexing.
- Mandatory background daemon.
- Mandatory file watcher.
- Mandatory persistent graph database.
- Network-dependent parsing.
- Runtime dependency installation.
- Full semantic analysis on startup.
- Unbounded AST retention.
- Unbounded graph dumps.
- New authority paths.
- Model-authored structural facts.

Startup must remain cheap.

Project discovery should be metadata-first.

Structural indexing should be lazy and activated by task requirements.

Memory, CPU, parser resources and query output must be bounded.

Use explicit budgets and cancellation.

---

# PART IV — FAILURE HANDLING AND CORRECTNESS

Implement and test the complete lifecycle, not just the happy path.

Include at least:

1. Unsupported language.
2. Unknown file extension.
3. Ambiguous language detection.
4. Syntax error in a supported file.
5. Partially parseable file.
6. Missing grammar or unavailable adapter.
7. File added.
8. File removed.
9. File renamed.
10. File modified.
11. Multiple language files modified together.
12. Module boundary change.
13. Manifest/config change.
14. Ignore scope change.
15. Generated/vendor files.
16. Nested projects and monorepositories.
17. Unreadable file.
18. Source changed during parse.
19. Concurrent readers.
20. Concurrent refresh.
21. Cancellation during refresh.
22. Parser resource cleanup.
23. Unicode source coordinates.
24. Large source files.
25. Truncated query.
26. Ambiguous symbol names.
27. Cross-language references that cannot be resolved.
28. Current but incomplete snapshot.
29. Stale snapshot.
30. Evidence from an older generation.

The system must fail explicitly and recover without publishing misleading state.

---

# PART V — TESTING AND REAL-PROJECT VALIDATION

## 1. Adapter conformance tests

Each language adapter must pass a shared conformance suite for:

- Language detection.
- File parsing.
- Source coordinates.
- Symbol extraction.
- Import/export extraction.
- Relationship extraction.
- Test discovery.
- Entrypoint discovery.
- Parse-error handling.
- Unsupported syntax.
- Deterministic output.
- Cancellation.
- Resource cleanup.

Add language-specific fixtures covering important syntax forms.

Do not validate an adapter only by checking that the parser can produce a syntax tree.

Verify that extracted canonical facts are useful and correct.

## 2. Cross-language structural tests

Test that different languages can coexist in one project snapshot.

For example:

```text
frontend/
  app.tsx
  components/
  package.json

backend/
  cmd/
  internal/
  go.mod

services/
  PaymentService.java
  pom.xml
```

The same runtime must provide one coherent project overview and language-aware structural queries.

Do not create one independent snapshot per language.

## 3. Required real-project benchmarks

Use actual repositories, not only synthetic fixtures.

At minimum:

**Go repository**
- Existing OHGO repository.
- Verify the existing Go adapter and Phase 2 behavior remain intact.
- Confirm unchanged files are not reparsed.
- Confirm structural evidence generation is unchanged or additively improved.

**Next.js / TypeScript repository**
- Re-run the previously observed project-analysis test:

```text
check this project and analyze what this project is about?
```

Expected:
- ASK authority.
- Lightweight OVERVIEW activation.
- Project and framework discovery.
- Entrypoint and route discovery where supported.
- Relevant manifests and configs.
- Targeted source reads only where needed.
- No unconditional full-project graph construction.
- Honest test and source-coverage reporting.

**Mixed-language repository**
- Go plus TypeScript or Java.
- One snapshot.
- Language-aware inventory.
- No duplicate graph ownership.

Add representative Rust, C#, Kotlin, Ruby and C++ fixtures or repositories to validate their actual adapters.

## 4. Measure runtime usefulness, not just parser speed

Measure:

- Cold overview latency.
- Cold structural indexing latency.
- Warm overview latency.
- Targeted query latency.
- Impact query latency.
- Incremental one-file update latency.
- Incremental multi-file update latency.
- Files parsed per update.
- Files reused.
- Parse failures.
- Memory usage.
- Parser resource usage.
- Snapshot size.
- Tool output bytes.
- Estimated or measured model input tokens.

Compare:

```text
Existing lexical exploration
versus
Adaptive structural exploration
```

Record:

- Number of `ls` calls.
- Number of `glob` calls.
- Number of `grep` calls.
- Number of `read` calls.
- Number of structural queries.
- Number of model turns.
- Input tokens.
- Wrong-path attempts.
- Final evidence completeness.

The objective is not to eliminate every `read`, `glob` or `grep`.

The objective is to reduce unnecessary exploration while preserving verification quality.

## 5. Regression tests for truthful knowledge

Add cases that prevent conclusions unsupported by the snapshot.

Examples:

```text
There are no Go tests.
```

Must not be claimed if the indexed scope contains Go test files.

```text
This project has no TypeScript source.
```

Must not be claimed if TypeScript files are unsupported or unparsed.

```text
No callers exist.
```

Must not be claimed when the query is truncated, incomplete or unresolved.

```text
The project is fully understood.
```

Must not be inferred from a successful directory listing.

Absence claims require sufficient coverage of the relevant scope and language.

---

# PART VI — ARCHITECTURAL INVARIANTS

The following must remain true after implementation:

```text
One StructuralSource.
One canonical project snapshot/index.
One scope owner.
One evidence model.
One authority system.

Go continues using its existing parser path.
Non-Go languages use registered adapters.
Tree-sitter remains an implementation detail.
Raw grammar ASTs are not exposed to the LLM.
Canonical facts preserve language-specific meaning.
No fabricated relationships.
No unsupported semantic certainty.
No stale snapshot presented as CURRENT.
CURRENT does not imply complete.
No full parsing when the task does not require it.
No full graph dump.
No uncontrolled semantic escalation.
No startup indexing regression.
No mutation through structural queries.
No authority expansion.
No duplicate source of truth.
```

Treat these as architecture-test candidates wherever possible, not merely documentation statements.

---

# PART VII — IMPLEMENTATION CHECKPOINTS

Use these checkpoints internally while retaining the complete architecture context.

They are validation gates within one implementation objective, not independent projects or context boundaries.

### Checkpoint A — Architecture and compatibility

- Existing Go path preserved.
- Canonical model compatibility established.
- Language registry and ownership correct.
- Dependency and grammar decisions reproducible.

### Checkpoint B — Multi-language parsing

- Approved adapters operational.
- Canonical extraction works.
- Language-specific facts preserved.
- Parse failures reported honestly.

### Checkpoint C — Unified snapshot

- One multi-language snapshot.
- One freshness/generation lifecycle.
- Incremental updates correct.
- Atomic publication preserved.

### Checkpoint D — Adaptive activation

- NONE / OVERVIEW / TARGETED / IMPACT / SEMANTIC behavior enforced.
- No unnecessary full indexing.
- Structural activation driven by task requirements.
- Budgets and cancellation respected.

### Checkpoint E — Query and evidence

- Unified structural queries.
- Compact projections.
- Provenance retained.
- Completeness and truncation accurate.
- No unsupported claims.

### Checkpoint F — Control Plane integration

- Correct ASK/FUZZ/BUILD behavior.
- Read-only structural tool availability verified.
- No authority expansion.
- Existing lexical and external tools remain available as fallback.

### Checkpoint G — End-to-end verification

- Go regression tests pass.
- Next.js real-project test passes.
- Mixed-language tests pass.
- Performance and token benchmarks recorded.
- Full repository gates pass.
- Architecture documentation reconciled.

Do not move forward by declaring a checkpoint complete solely because its unit tests pass. Verify the relevant global invariants as well.

---

# PART VIII — FINAL ACCEPTANCE CRITERIA

The implementation is complete only when all of the following are demonstrated.

## Architecture

- One unified Project Structural Runtime.
- Existing Phase 2 foundation preserved.
- Parser-independent canonical model.
- Language-specific adapters with clear ownership.
- No duplicate index or evidence system.

## Language support

- Existing Go support remains intact.
- TypeScript/TSX and JavaScript/JSX work.
- Java, C#, Rust, Kotlin, Ruby and C++ have verified syntax-level adapters.
- Unsupported features and languages are explicit.
- No false semantic guarantees.

## Adaptive behavior

- Simple tasks do not activate heavy structural indexing.
- Overview tasks use lightweight project understanding.
- Targeted tasks retrieve bounded structural slices.
- Impact tasks expand relationships only as needed.
- Semantic tooling is explicitly escalated.
- Structural-tool availability is correct for eligible roles and stages.

## Correctness

- Incremental behavior is verified.
- Freshness and completeness remain distinct.
- Relationships retain provenance.
- Evidence identifies its snapshot and generation.
- Parse failures and unsupported coverage remain visible.
- Absence claims are coverage-aware.
- No stale or incomplete data is presented as authoritative.

## Efficiency

- No unconditional full-project parsing.
- No unnecessary graph expansion.
- No uncontrolled memory growth.
- No parser resource leaks.
- No unnecessary runtime network or daemon.
- Real-project benchmarks demonstrate practical benefits.
- Context projection materially reduces unnecessary input.

## Integration

- `StructuralSource` remains the shared integration point.
- Existing `symbols`, `graph`, `refs` and `structure` tools remain coherent.
- Read-only structural access works in ASK.
- Existing BUILD authority is unchanged.
- Existing lexical tools and MCP/GRY remain optional complementary capabilities.
- Evidence and completion behavior remain truthful.

## Repository quality

Run all project-required gates, including the existing required checks:

```bash
gofmt -l .
go build ./...
go vet ./...
go test ./...
go test -race ./...
golangci-lint run ./...
```

Also run adapter-specific and real-project tests.

Do not report a gate as passing unless it was actually executed and passed.

---

# PART IX — FINAL REPORT

At completion, report:

1. Architecture changes.
2. Existing components preserved.
3. New adapter and grammar coverage.
4. Adaptive activation behavior.
5. Snapshot and incremental behavior.
6. Query and evidence integration.
7. Control Plane and role-admission changes.
8. Failure handling.
9. Real-project validation.
10. Before/after exploration and token measurements.
11. Benchmark results.
12. Verification results.
13. Remaining limitations.
14. Deferred semantic capabilities.
15. Changed files and migration notes.

Clearly distinguish:

- Implemented and verified.
- Implemented but not fully verified.
- Designed but deferred.
- Unsupported.

Do not call the runtime complete merely because all parsers compile.

The final proof must demonstrate that the runtime improves how OHGO understands and works with real repositories.

---

# FINAL DESIGN INTENT

OHGO must not become a system that constantly parses everything because it can.

It must know when structural knowledge is useful, how much knowledge is necessary, which language capabilities are available, and when it has reached the limit of what static structure can prove.

The intended behavior is:

```text
USER INTENT
    |
    v
CONTROL PLANE
    |
    v
DETERMINE STRUCTURAL NEED
    |
    +--------------------------+
    |                          |
    v                          v
NO STRUCTURE NEEDED       STRUCTURE NEEDED
    |                          |
    v                          v
NORMAL TOOL PATH          SELECT DEPTH
                               |
                +--------------+--------------+
                |              |              |
                v              v              v
             OVERVIEW       TARGETED        IMPACT
                |              |              |
                +--------------+--------------+
                               |
                               v
                       LANGUAGE ADAPTERS
                               |
                               v
                      CANONICAL STRUCTURE
                               |
                               v
                     BOUNDED GRAPH QUERY
                               |
                               v
                       VERIFIED EVIDENCE
                               |
                               v
                              LLM
                               |
                               v
                       REASONED PROPOSAL
                               |
                               v
                        CONTROL PLANE
```

The LLM should not need to reconstruct an entire repository from thousands of files.

It should receive exactly the relevant structural knowledge required to reason about the current task.

**The parser understands syntax. The Structural Runtime understands project structure. The Control Plane decides when and how deeply to use it. The LLM reasons over the resulting evidence.**

That is the target architecture.



> notes: There are two things that should not be sacrificed for the sake of language coverage:
- Do not replace the existing Go parser with Tree-sitter simply to have a unified parser for all languages. The Go backend already incorporates established logic and real-world benchmarks; it makes sense to retain it as a native adapter.
- Do not equate multi-language support with parsing an entire repository for every task. Language coverage and structural depth are independent dimensions. It is entirely feasible to support eight languages ​​while only parsing metadata when a task requires an overview, or exploring relationships only when a task requires an impact analysis.
I will also use three existing test cases as a regression baseline: the analysis of a Next.js project, the analysis of a Go project (including test files), and Ohgo’s self-analysis. These cases respectively verify fallback capabilities, native structural coverage, and the system’s ability to comprehend its own architecture.
The goal is not 100% parser coverage. The goal is to ensure that every time Ohgo invokes its structural runtime, it does so for a specific reason, within defined limits, based on evidence, and in a way that delivers genuine value to the task at hand.
