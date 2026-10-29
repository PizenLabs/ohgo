# OHGO Architecture Migration

> **What this records:** the `internal/` topology migration and the governance
> it installed. Read `OHGO_ARCHITECTURE_MAP.md` for the resulting structure and
> `OHGO_ARCHITECTURE_RULES.md` for the permanent contract.
>
> **One-line result:** 38 implementation packages moved under `internal/`,
> grouped into 14 semantic domains, with **zero** behavioural change; a
> test-only package now machine-checks nine boundaries and the ownership
> manifest.

---

## 1. Before → after

### 1.1 Scale

| Metric | Before | After | Delta |
|---|---|---|---|
| Packages | 43 | 44 | +1 (`internal/architecture`, test-only, imports nothing) |
| Module-internal import edges | 127 | 127 | **0** |
| Go files | 558 | 560 | +2 (governance: `doc.go`, `rules_test.go`) |
| Production packages outside `internal/` | 38 | 3 (`cmd/ohgo`, `bench/*`, `scripts/*`) | −35 |
| Files relocated | — | 458 renames | — |
| Test / benchmark / example names | 2,909 | 2,909 | **0**, identical sha256 |

### 1.2 Package map

Every entry is a pure path move. Package **names** are unchanged, so every
identifier, doc comment, and JSON tag is untouched.

| Was | Now | Domain |
|---|---|---|
| `agent` | `internal/agent` | runtime |
| `contract` | `internal/control/contract` | control |
| `budget` | `internal/control/budget` | control |
| `workspace` | `internal/control/workspace` | control |
| `cognition` | `internal/control/cognition` | control |
| `security` | `internal/security` | control |
| `tools` | `internal/execution/tools` | execution |
| `mcp` | `internal/execution/mcp` | execution |
| `sandbox` | `internal/execution/sandbox` | execution |
| `shell` | `internal/execution/shell` | execution |
| `exec` | `internal/execution/exec` | execution |
| `ast` | `internal/intelligence/ast` | intelligence |
| `internal/ignore` | `internal/intelligence/ignore` | intelligence |
| `ai` | `internal/provider/ai` | provider |
| `catalog` | `internal/provider/catalog` | provider |
| `creds` | `internal/provider/creds` | provider |
| `session` | `internal/continuity/session` | continuity |
| `task` | `internal/continuity/task` | continuity |
| `recovery` | `internal/continuity/recovery` | continuity |
| `memory` | `internal/memory` | continuity |
| `knowledge` | `internal/external/knowledge` | external |
| `search` | `internal/external/search` | external |
| `config` | `internal/config` | configuration |
| `state` | `internal/state` | configuration |
| `project` | `internal/project` | configuration |
| `jsonschema` | `internal/jsonschema` | vocabulary |
| `tokens` | `internal/tokens` | vocabulary |
| `diff` | `internal/diff` | vocabulary |
| `edit` | `internal/edit` | vocabulary |
| `vcs` | `internal/vcs` | vcs |
| `prompt` | `internal/prompt` | prompt |
| `commit` | `internal/commit` | application |
| `startup` | `internal/startup` | application |
| `update` | `internal/update` | application |
| `internal/history` | `internal/history` | application |
| `internal/gryhook` | `internal/gryhook` | application |
| `tui` | `internal/tui` | presentation |
| `tui/diff` | `internal/tui/diff` | presentation |
| — | `internal/architecture` | governance *(new)* |

**Deliberately not moved**

| Path | Why |
|---|---|
| `cmd/ohgo` | `package main`; the install path (`go install .../cmd/ohgo`) is a user-facing contract |
| `bench/{intent,repository,phase9}` | Benchmark harnesses, not shipped implementation; they import the tree, nothing imports them |
| `scripts/updateversion` | Repository tooling, not part of the binary |

---

## 2. Slices

Each slice is an independent commit, verified before the next began.

### Slice 1 — `445a4c6` kernel leaves and the provider plane

Moved 16 packages: the dependency-free leaves, the repository-intelligence
index, and `ai`. Split from the rest because these are the packages whose
consumers are *everywhere* — establishing them first means later rounds only
rewrite the packages that depend on them.

Verification: `gofmt` clean, `go build ./...`, `go vet ./...`, `go test ./...`
(40 test packages pass), 3 packages have no tests (`bench/intent`, `tokens`,
`ignore`).

### Slice 2 — `b1a1f7f` control, execution, continuity, presentation

Moved the remaining 19 packages, including `agent`, `tools`, `tui`, and the
three continuity stores. This is the round where a mistake would be behavioural
rather than cosmetic, so it was verified with the full matrix (§5) and the
mechanical proof (§3).

### Slice 3 — `e69d906` governance

Added `internal/architecture`: a test-only package that parses every production
Go file and enforces the manifest and nine boundary rules. Added after the move,
not before, so the rules describe the tree that exists rather than a plan.

Verification: rules green on the real tree; each of nine rules proven to fail on
an injected violation (§6).

---

## 3. Proof of behaviour preservation

A package relocation is a *mechanical* change, so the proof is mechanical.
Four independent layers, strongest first:

### 3.1 The diff contains nothing but import paths

```
$ git diff --cached -M --unified=0 | grep '^[+-]' | grep -v '^+++\|^---' \
    | grep -v 'github.com/PizenLabs/ohgo/' | wc -l
0
```

Every added and removed line in the relocation is a module import path. No
declaration, comment, string literal, constant, struct tag, or control-flow line
changed. This single fact rules out behaviour change in the code itself.

### 3.2 Rename detection

`git diff --summary -M` reports **458 renames** across the two relocation
slices, i.e. git itself models these as moves, not delete+create. File contents
are preserved with a small edit distance (the import lines), which is why the
insertion/deletion counts are symmetric.

### 3.3 Identical test inventory

```
before: 2909 names  sha256 8cb20fd97fec29ea0e21820eb29194c8c8e38a11ccb684c0db658a828d7cac68
after : 2909 names  sha256 8cb20fd97fec29ea0e21820eb29194c8c8e38a11ccb684c0db658a828d7cac68
```

No test was renamed, dropped, or skipped. Since the suite is the behavioural
specification, an identical inventory plus a green run means the same assertions
ran against the same code.

### 3.4 Edge count

Module-internal import edges: **127 before, 127 after**. The one new package
imports nothing. No dependency was added or removed, so no authority path,
execution path, or state flow can have changed shape.

### 3.5 What could have broken, and did not

| Risk specific to relocation | Why it did not apply / what was checked |
|---|---|
| `go:embed` paths | Both embeds (`catalog.json`, `changelog.json`) are relative to the package directory, which moved with the package |
| Relative filesystem paths in tests | The only `../..` root discovery is in `cmd/ohgo`, which did not move. All other relative paths are arguments to containment tests |
| Type identity in persisted data | No `encoding/gob`, no `%T`/`reflect.TypeOf` in any persisted path. `progress.json`, `sessions.db`, `recovery.db` are JSON/SQL over field names and tags, which do not encode package paths |
| `internal/` visibility | All importers (`cmd/`, `bench/`, `scripts/`) are inside the module, so all remain legal |
| Test binary / cache staleness | Full `go build ./...` from a clean cache plus full `go test ./...` |

---

## 4. Compatibility decisions

| Surface | Decision | Evidence |
|---|---|---|
| `progress.json`, `task.md` | **Unchanged.** No type's fields, tags, or embedding changed; only its import path. JSON keys are field/tag-based | §3.5 |
| `sessions.db`, `recovery.db` | **Unchanged.** Schemas and queries are untouched | diff shows no non-import line |
| `settings.json`, `credentials.json`, `project.yml` | **Unchanged** | same |
| `provider/id` reference format | **Unchanged.** Already centralized in `ai.Model.Ref()` in the second pass; only its import path moved | `internal/provider/ai/model.go` |
| Go import paths | **Changed** (the point of the migration) | §1.2 |
| Public Go API | **Intentionally reduced.** The module never documented an external Go API; exposing implementation packages was accidental. `cmd/ohgo`, `bench/*`, `scripts/*` remain importable | §2 of the rules |
| CLI surface, flags, exit codes | **Unchanged** | binary smoke test |
| TUI behaviour | **Unchanged** | full `tui` suite + `-race` |

**No schema migration was required, so none was performed.**

---

## 5. Verification matrix

Run after the final slice, on the migrated tree.

| Check | Command | Result |
|---|---|---|
| Formatting | `gofmt -l .` | empty |
| Build | `go build ./...` | pass |
| Vet | `go vet ./...` | pass |
| Tests | `go test ./...` | pass — 44 packages, 40 with tests |
| Race | `go test -race ./...` | pass — run in two phases (`./internal/...`, then `./cmd/... ./bench/... ./scripts/...`) because linking all 41 race binaries at once exceeded the machine's free disk; 41/41 test packages green |
| Dependency graph | `go list -deps ./...` | pass; acyclic; 127 internal edges (unchanged) |
| Encapsulation | `internal/architecture` | 3 production roots outside `internal/`, as designed |
| Manifest | `TestManifestDeclaresEveryPackage` | 39/39 internal packages declared |
| Boundaries | 9 rules in `internal/architecture` | pass; each proven to fail on injection (§6) |
| Test inventory | name multiset hash | identical to pre-migration |
| Diff purity | non-import changed lines | 0 |
| Binary smoke test | `ohgo version` | `ohgo 0.0.3-dev` |
| CLI smoke test | `ohgo --help`, `ohgo models` | correct output, no path leakage |

---

## 6. Rules were proven to bite

A boundary test that cannot fail is decoration. Each rule was exercised by
injecting the violation it forbids, observing the failure, and removing it.

| Injected violation | Rule that failed |
|---|---|
| `internal/scratch` package with no manifest entry | `TestManifestDeclaresEveryPackage` |
| `agent` importing `control/workspace` | `TestRuntimeKernelImportsOnlyItsAllowList` |
| `tools` importing `continuity/task` | `TestExecutionPlaneDoesNotDependOnContinuityOrPresentation` |
| `session` importing `tui` | `TestContinuityDoesNotDependOnPresentation` |
| `ast` importing `agent` | `TestRepositoryIntelligenceDoesNotDependOnTheRuntime` **and** `TestDependencyFreePackagesImportNothingFromTheModule` |
| `catalog` importing `agent` | `TestProviderPlaneStaysBelowTheRuntime` |
| `prompt` importing `update` | `TestShellLayerIsASink` |
| `tui` importing `execution/tools` | `TestPresentationDoesNotConstructCapabilitiesOrTaskState` |

This exercise also **found a real bug in the first version of the rules**: the
plane helper compared `"internal/continuity/task"` against the prefix
`"internal/continuity/"` and appended another slash, so every plane rule was
silently vacuous. The rules passed on the real tree and on injected violations
until the injection exposed it. Fixed, then re-proven. This is why §18 of the
rules requires injecting a violation for every new rule.

---

## 7. Rejected migrations

These were considered during this pass and deliberately **not** done. The
reasoning matters more than the decision.

| Candidate | Why it was rejected |
|---|---|
| **Creating `internal/foundation/` (or `core/`) for the 15 leaf packages** | The leaves share only the property "imports nothing". That is not a domain, and a folder named after the absence of dependencies is the generic dumping ground the brief forbids. Each leaf keeps its own name. |
| **Merging `control/contract` into `control/workspace`** | The contract is persisted inside `progress.json` and consumed by `agent`, `cognition`, and `task`. `workspace` imports `agent`, so folding the contract into it would make `agent` → `workspace` → `agent` a cycle. The split is forced by dependency direction, not taste. |
| **Merging `control/cognition` into `agent`** | Would put task shape, evidence retention, and tool narrowing inside the engine, destroying the capability-agnostic kernel (`agent` may not import `workspace`/`cognition`). Enforced by §3. |
| **Merging `execution/tools` into `agent`** | Same inversion: the engine would own concrete capabilities and their `Approval` tiers. |
| **Renaming packages to remove collisions** (`diff` vs `tui/diff`; `config` vs `contract`; `task` package vs `task` tool) | Renaming changes every identifier and doc reference for zero structural gain. Package names are part of the reading experience; a qualified import already disambiguates. Rejected as churn masquerading as architecture. |
| **Adding a numeric layer/rank rule** ("a package may not import a higher rank") | With a cycle-free graph a topological ranking always exists, so the rule is **tautological** — it would restate the compiler's acyclicity check while looking like a boundary. Replaced with explicit semantic prohibitions (§5 of the rules), which can fail for a real reason. Recorded here because it is the most tempting wrong rule. |
| **Moving `bench/` under `internal/`** | `internal/` is the shipped implementation boundary; benchmarks are not shipped. Moving them would imply they are part of the binary's architecture. |
| **Moving `cmd/ohgo` under `internal/`** | It is `package main`; `go install github.com/PizenLabs/ohgo/cmd/ohgo@latest` is a documented user path. |
| **Splitting `cmd/ohgo/harness.go` (965 lines) and `internal/tui/app.go` (~5,174 lines) in this pass** | Structural, not semantic. Mixing a file split into a package relocation makes both harder to review and to revert. Recorded as SAFE NEXT in §8. |
| **Unifying turn preparation (`runTurnWithChoice` ↔ `runHeadlessTurnAs`)** | Investigated and rejected in the second optimization pass: the shared part is a call sequence over already-canonical owners; the divergent part is the durable-task and recovery lifecycle. The relocation changes nothing about that finding. **Do not reintroduce it** without a new architectural decision. |
| **Unifying the TUI task vocabulary with `task.Status`** | The TUI's string vocabulary is a deliberate projection seam. Moving `task` under `internal/` does not change the seam; `internal/architecture` now *enforces* that `tui` must not import `continuity/task`. |

---

## 8. Remaining debt

| Classification | Item |
|---|---|
| **SAFE NEXT** | Split `cmd/ohgo/harness.go` (965 lines, four clusters: model plane, session/recovery, reporting, workspace) — same package, fingerprint-verifiable |
| **SAFE NEXT** | Split `internal/tui/app.go` method families (support, model plane, task, recovery, sessions) — same package, fingerprint-verifiable |
| **SAFE NEXT** | Give `internal/architecture` a rule for "no new package outside the manifest's domains" once the domain list is stable enough (today: domain names are free strings) |
| **DEFERRED** | Turn-preparation sharing between the TUI and headless paths — needs a deliberate seam decision first |
| **DEFERRED** | `agent/loop.go` cohesion — one state machine; no cluster has independent semantic identity |
| **DEFERRED** | `control/cognition` and `continuity/task` importing `agent` for concrete types. Removing it needs a shared type package, which is a new abstraction with no proven owner |
| **LOAD-BEARING** | Authority mirrors; five read-only layers; callback installation order; TUI terminal CAS; journal-before-mutation; projection narrowing; task contract immutability; `progress.json` > `task.md` |
| **LOAD-BEARING** | `tui` orchestrating turn preparation (contract, shape, projection hooks). It is an orchestrator today; §8 of the rules constrains what it may *depend on*, not the fact that it orchestrates |
| **PUBLIC API** | Dropped accidental exposure of 38 packages. Any external Go consumer would break — that is the intended outcome and is recorded here as the compatibility cost of the migration |
| **PUBLIC API** | `prompt.ToolCatalog`, `tui.ModelID` — dead exported symbols, zero callers; removal still needs an explicit decision |
| **SCHEMA / COMPATIBILITY** | `progress.json` version; `sessions.db` / `recovery.db` schemas. Unchanged by this pass; any future change follows rules §12 |
| **BEHAVIOR-CHANGING** | `agent.ReadOnlyToolNames` drift; stale `workspace.Artifact` origin after a switch; mid-session `/sessions` resume not restoring authority/model. All three remain product decisions, untouched |

---

## 9. Scale test — does the new tree answer "where does this go?"

Eleven representative future changes were placed against the tree *without*
reading historical commits. All eleven resolved to a single owner; the table is
in `OHGO_ARCHITECTURE_MAP.md` §7. The results that mattered:

| Question | Answer | Ambiguity? |
|---|---|---|
| New tool | `execution/tools` + `cmd/ohgo/build.go` registration | none |
| New repo-intelligence query | `intelligence/ast` + thin tool | none |
| New provider | `provider/ai` + registry + `creds` env var + `catalog` facts | none |
| New model-selection policy | `catalog` (facts) / `config` (roles) / `cmd/ohgo/options.go` (resolution) | split by *kind* of decision, which is the intended answer |
| New persistence record | owning `continuity/*` + version bump | none |
| New task transition | `continuity/task` + `tui` projection | none |
| New completion signal | `agent` completion/evidence, consumed by `ClassifyRun` | none |
| New security policy | `security` kernel + `cmd/ohgo/security.go` derivation | none |
| New CLI command | `cmd/ohgo` (+ `tui` registry if interactive) | none |
| New TUI projection | `tui`, fed by hooks | none |
| New recovery mechanism | `continuity/recovery` + wiring | none |

The one deliberately split answer — model selection — is split because the
*decision types* differ (facts vs persisted configuration vs resolution
precedence), which is exactly the distinction the map is meant to preserve.

---

## 10. Final recommendation

**The migration is complete, and the repository is at a safe architectural
stopping point.**

* Encapsulation is total: 3 production roots outside `internal/`, all of them
  intentional and non-importable-as-API except the command.
* Topology is semantic: 14 domains, each with a stated responsibility; no
  invented hierarchy and no generic folder.
* Direction is enforced, not merely documented: nine rules, each proven to fail
  on a real violation.
* Ownership is declared before code exists: an undeclared package fails the
  suite.
* Behaviour is unchanged: zero non-import diff lines, identical test inventory,
  identical edge count, full suite green including `-race`.

What remains is structural file organisation (two large files) and
behaviour-changing product decisions — both explicitly out of scope for a
behaviour-preserving migration. A further *architecture* pass is not justified;
the architecture now defends itself.
