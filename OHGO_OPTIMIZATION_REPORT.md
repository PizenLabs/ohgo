# OHGO Optimization Report

> **Scope:** Behaviour-preserving structural optimization of the ohgo codebase.
> **Authority for "must not change":** `docs/OHGO_ARCHITECTURE_BASELINE.md`
> (read in full before any change).
> **Authority for "what actually happens":** the source and its tests.
> **Constraint honoured throughout:** no authority, execution, persistence,
> completion, or repository-intelligence semantics were altered.

---

## 1. Summary

Two slices were applied, both independently revertible.

| Slice | Kind | What changed | Semantic risk |
|---|---|---|---|
| 1 | Semantic consolidation | One run-outcome decision tree existed twice (`cmd/ohgo/outcome.go: projectRun` and `tui/completion.go: turnTaskOutcomeWithReview`). The decision now lives once in `agent.ClassifyRun`; both shells only project its verdict into their own vocabulary. | Low — pure function, no I/O, no shared state |
| 2 | God-file reduction | `cmd/ohgo/run.go` (2,898 lines, 7 unrelated responsibilities) was split into cohesive files at responsibility boundaries, with declaration text moved byte-for-byte. | Very low — same package, verified text-preserving |

Baseline §17.4 ("Completion has three vocabularies") and §18 hazard 1 ("Two
orchestrators for one turn") identified the first slice directly. Baseline §12
explicitly names `cmd/ohgo/run.go` as a valid extraction target and requires
extraction "based on semantic boundaries".

Nothing else was touched. The remaining duplication catalogued in baseline §17
is either load-bearing, deliberately distinct, or behaviour-changing to
consolidate — see [§9](#9-remaining-architectural-debt).

---

## 2. Structural Changes

### 2.1 Slice 1 — one run-outcome decision

**Added**

| File | Contents |
|---|---|
| `agent/runoutcome.go` | `RunVerdict` (`RunCompleted`, `RunNeedsReview`, `RunPaused`, `RunFailed`, `RunCancelled`), `ClassifyRun`, `budgetVerdict` |
| `agent/runoutcome_test.go` | 6 characterisation tests pinning the cross-shell contract |

**Modified**

| File | Before | After | Change |
|---|---|---|---|
| `cmd/ohgo/outcome.go` | 275 lines | 242 lines | `projectRun` 46 → 31 lines; `resourceOutcome` deleted; `errors` and `budget` imports removed |
| `tui/completion.go` | 95 lines | 58 lines | `turnTaskOutcomeWithReview` 60 → 23 lines; `context`, `errors`, `budget` imports removed |

`ClassifyRun` is the single place that decides *which* evaluator applies and
*why* a run stopped:

```text
cancelled      → RunCancelled, "interrupted"
deadline       → budget rule (evidence may still complete)
budget out     → budget rule, reason from *budget.ExhaustedError
turn limit     → RunPaused, "turn limit reached"
other error    → RunFailed, error text
no stop        → EvaluateTaskCompletion → RunCompleted | RunNeedsReview
```

`projectRun` maps that to `runState` + exit code; `turnTaskOutcomeWithReview`
maps it to `taskAction`. The vocabularies stay separate exactly as before.

**Why this was safe to merge.** Both implementations were verified line by line
to have the same branch order, the same evaluator calls, and the same reason
strings. The only genuine divergences were:

* the shell-specific vocabulary (`runState` vs `taskAction`), preserved as a
  projection;
* the deadline representation — the TUI normalises a silent deadline into
  `context.DeadlineExceeded` before calling, while the stateless path passes a
  `deadlineExpired` flag. `ClassifyRun` accepts both and treats them identically
  (pinned by `TestClassifyRunSilentDeadlineIsAResourceStop`).

### 2.2 Slice 2 — `cmd/ohgo/run.go` split

`run.go` was the outlier in a package that otherwise already follows
one-responsibility-per-file (`security.go`, `recovery.go`, `support.go`,
`taskcontinuity.go`, `commit.go`, `update.go`, `mcpui.go`, …).

| File | Lines | Top-level decls | Responsibility |
|---|---|---|---|
| `run.go` | 546 | 6 | Command entry points: `runCmd`, `bootProject`, `runOnce`, `runSession`, `useInteractiveUI`, `isTerminal` |
| `options.go` | 467 | 15 | Flag/env/config resolution, authority defaulting, budgets, model resolution |
| `build.go` | 430 | 12 | Tool construction, subagent spawner, tool-set bounds, loop construction, compaction, admission seam, summarizer |
| `session.go` | 246 | 6 | Store/session open, session manager, resume authority, history load, turn persistence |
| `repl.go` | 426 | 7 | Plain REPL, prompt history, history rotation, rebase |
| `wizard.go` | 392 | 3 | First-run onboarding and its commit path |
| `modelcatalog.go` | 263 | 8 | Catalog snapshot, refresh, model-plane hooks, mention roster |
| `turn.go` | 117 | 3 | Turn routing authority, transition prediction, proposal facts |
| `presentation.go` | 75 | 3 | Plain-output prompt blocks, turn printing, approval prompt |
| `escape.go` | 46 | 1 | Human `!` shell-escape wiring |
| `support.go` | 295 (+25) | 17 | Existing `$sup` surface; `supportBuilder` moved in to join its only consumer |

**Proof that this was a pure move.** A purpose-built AST/line fingerprint tool
(`declhash`) hashes the sorted multiset of every non-blank, non-import,
non-`package` line in `cmd/ohgo`'s non-test files. It excludes file boundaries
and import blocks by design, so redistribution between files cannot change it.

```text
before split: lines=6839 sha256=3de68a50ed5700958bddc467901f81df994a1e75eb0146e8f1f57b331d1d16f4
after  split: lines=6839 sha256=3de68a50ed5700958bddc467901f81df994a1e75eb0146e8f1f57b331d1d16f4
```

Identical. No function body, comment, doc string, or constant changed.

Additionally verified: the original `run.go` declared **no package-level `var`
and no `func init`**, so moving declarations between files cannot change
package initialisation order.

---

## 3. Semantic Ownership Changes

| Concept | Before | After |
|---|---|---|
| "Why did this run stop?" | Owned twice: `cmd/ohgo.projectRun` and `tui.turnTaskOutcomeWithReview`, each with its own copy of the same switch | Owned once by `agent.ClassifyRun` (next to the evaluators it delegates to) |
| Completion evidence rules | `agent.EvaluateTaskCompletion` / `EvaluateBudgetCompletion` (unchanged) | Unchanged — `ClassifyRun` only selects between them |
| Stateless exit vocabulary | `cmd/ohgo.runState` (owned by `cmd/ohgo`) | Unchanged — now a projection of `RunVerdict` |
| Durable task transition vocabulary | `tui.taskAction` (owned by `tui`) | Unchanged — now a projection of `RunVerdict` |
| CLI option resolution | `cmd/ohgo/run.go` | `cmd/ohgo/options.go` (same package, same ownership) |
| Loop/tool construction | `cmd/ohgo/run.go` | `cmd/ohgo/build.go` |
| Session lifecycle | `cmd/ohgo/run.go` | `cmd/ohgo/session.go` |

No ownership was moved *between packages*. `RunVerdict` is a classification,
not a new domain object: it names the five outcomes the two shells already had,
and it deliberately does not replace either shell's vocabulary.

---

## 4. Preserved Invariants

Each baseline invariant that could plausibly be affected was checked against
source and tests.

| # | Baseline invariant | Status |
|---|---|---|
| 1 | Every model-proposed call crosses the authorizer before execution | Untouched (`agent/loop.go: execute` unchanged) |
| 2 | An installed authorizer supersedes `Admit`/`Approve` | Untouched |
| 3 | Policy order: capability → authority → mutation → scope → `ReadOnly` → grant → risk | Untouched (`security/policy.go` unchanged) |
| 4 | Grants are exact and one-shot; cannot widen authority | Untouched |
| 5 | Model cannot supply capability/scope/risk/authorization | Untouched |
| 6 | Live authority read at call time | Untouched |
| 7 | Subagents inherit the authorizer; cannot widen authority | Untouched |
| 8 | Delegation depth is structural | Untouched (`readOnlyToolSet`/`childToolSet` moved verbatim) |
| 9 | Read-only delegation enforced by the tool set | Untouched |
| 10 | Delegation under non-mutation grounding is forced read-only | Untouched |
| 11 | Semantic admission precedes authorization and only refuses | Untouched |
| 12 | Projection narrows, never widens | Untouched |
| 13 | Completion is evidence-derived | Decision *location* consolidated; rules unchanged |
| 14 | Mutation requires observed change + later verification | Unchanged (`EvaluateTaskCompletion` not edited) |
| 15 | A refusal is not a failed execution | Unchanged |
| 16 | Budget admission is atomic and precedes spending | Untouched |
| 17 | Checkpoint before mutation; failure refuses the batch | Untouched |
| 18 | Recovery restore is verified and refuses on mismatch | Untouched |
| 19 | Task contract immutability; terminal states absorbing | Untouched |
| 20 | TUI task block is a projection | Untouched |
| 21 | Interrupted task never restored as completed | Untouched |
| 22 | MCP classification from config, unknown fails closed | Untouched |
| 23 | Read-only execution is an OS boundary | Untouched |
| 24 | One index shared by coordinates and completion | Untouched |
| 25 | Derived repo state never serves stale coordinates | Untouched |
| 26 | Package graph is acyclic | Verified after both slices |
| 27 | Startup is the sole project-local creator | Untouched |
| 28 | Resume restores recorded authority/model | Untouched |

New invariants pinned by this work:

* **Cancellation outranks evidence in the shared classifier.** Verified work is
  not reported `COMPLETED` after a user interrupt
  (`TestClassifyRunCancellationOutranksVerifiedEvidence`).
* **A resource stop cannot fabricate or erase completion.** Exhaustion upgrades
  to `RunCompleted` only when a mutation was already observed and verified;
  otherwise it stays `RunPaused` with its cause
  (`TestClassifyRunResourceStopDependsOnEvidence`).
* **A silent deadline is the same resource stop as an explicit one**
  (`TestClassifyRunSilentDeadlineIsAResourceStop`).
* **A real failure keeps its cause**, so an operator sees the provider or
  execution error rather than a generic verdict
  (`TestClassifyRunKeepsTheFailureCause`).

---

## 5. Behavior Verification

Every command below was run to completion after both slices.

| Command | Result |
|---|---|
| `gofmt -l .` | empty |
| `go build ./...` | pass |
| `go vet ./...` | pass |
| `go test ./...` | pass (all 44 packages) |
| `go test -race ./agent ./cmd/ohgo ./tui` | pass |
| `go list -deps ./...` | pass |
| `declhash cmd/ohgo` before/after | identical fingerprint |
| Built binary: `ohgo version`, `ohgo --help` | correct output |

Behaviour comparison performed at each decision boundary that changed shape:

| Behaviour | How it was compared |
|---|---|
| Cancellation verdict | Existing `TestHeadlessCancellationIsNotSuccess`, `TestTurnTaskOutcomePausesOnCancellation`; new classifier test |
| Deadline verdict | Existing `TestHeadlessDeadlineIsNotSuccess`; new silent-deadline test |
| Budget exhaustion verdict | Existing `tui/budget_test.go` (3 tests) + `TestHeadlessBudget*`; new resource-stop test |
| Turn-limit verdict | Existing `TestTurnTaskOutcome*`; new classifier test |
| Generic failure reason text | New `TestClassifyRunKeepsTheFailureCause` |
| Evidence-gap verdict + reason | Existing completion suites; new `TestClassifyRunEvidenceDecidesWithoutAStop` |
| Exit-code mapping (0/1/2/3/130) | Unchanged source in `runOutcome.exitCode` |
| Task transition mapping | Existing `tui/completion_test.go`, `tui/budget_test.go` |

**Test-the-test.** Both new invariant tests were confirmed to fail when the
behaviour they defend was broken:

* forcing cancellation to fall through to the evidence verdict fails
  `TestClassifyRunCancellationOutranksVerifiedEvidence`;
* forcing a resource stop to ignore already-complete evidence fails
  `TestClassifyRunResourceStopDependsOnEvidence` and
  `TestClassifyRunSilentDeadlineIsAResourceStop`.

Both were then reverted and the suite re-run green.

---

## 6. Persistence Verification

**No serialized state was affected.**

* No type carrying a JSON tag was moved or renamed. `contract.IntentContract`,
  `agent.TargetSnapshot`, `cognition.EvidenceState`, and `budget.State` are
  untouched, so `progress.json` keys are byte-identical.
* `sessions.db` schema and `session.Store` are untouched.
* `recovery.db` schema, checkpoint ordering, and restore verification are
  untouched.
* Task transition vocabulary and terminal-state semantics are unchanged: the
  projection maps `RunVerdict` to the same `taskActionComplete` /
  `taskActionReview` / `taskActionPause` / `taskActionFail` set that
  `task.Controller` already consumed.
* Resume precedence (`progress.json` > `task.md`) and `reflect.DeepEqual`
  contract immutability are untouched.
* `RunVerdict` is process-local and is never persisted.

---

## 7. Authority Verification

Authority and execution paths were not modified. Specifically untouched:
`agent/loop.go` (`Run`, `dispatch`, `execute`, `authorize`), `agent/authorize.go`,
`cmd/ohgo/security.go` (`securityGate`, `securityRequest`, `authorityGate`,
`escapeAuthority`), `security/policy.go`, `workspace/` (all routing, permission,
contract, and grounding builders), and `cmd/ohgo/authority.go`.

The single touched decision path is **post-run**: it consumes turns and an error
and returns a verdict. It cannot authorize, execute, widen scope, or grant
anything. The authorization sequence, the policy order, the one-shot grant rule,
and the live-authority read are all unchanged, and their invariant tests
(`agent/authorize_test.go`, `security/security_test.go`,
`cmd/ohgo/mcp_authority_test.go`, `cmd/ohgo/authority_route_test.go`) pass.

The one authority-adjacent property this slice could have broken is *ordering*
between "the run was interrupted" and "the run produced verified evidence".
That ordering is now pinned in one place instead of two, and the new test fails
if it is reversed.

`cmd/ohgo/build.go` (formerly part of `run.go`) still constructs the tool set,
the subagent spawner, and the security wiring verbatim; only the file changed.

---

## 8. Dependency Changes

**Package graph: unchanged.** Slice 1 added a file to `agent` and removed
imports from two packages; slice 2 moved code within one package.

| Package | Before | After |
|---|---|---|
| `agent` | imports `ai`, `budget`, `contract`, `security`, `jsonschema`, `tokens` | unchanged (the new file uses already-present `context`, `errors`, `budget`, `contract`) |
| `cmd/ohgo` | 27 internal imports | unchanged (import *set* per package is identical; only per-file distribution changed) |
| `tui` | 15 internal imports | unchanged |

Per-file import reductions:

* `cmd/ohgo/outcome.go` — dropped `errors`, `budget`
* `tui/completion.go` — dropped `context`, `errors`, `budget`

No new package, no generic `runtime/`/`core/`/`shared/` package, no cycle
(`go list -deps ./...` succeeds and the package graph is unchanged from the
baseline measurement).

`agent` still does not import `tools`, `workspace`, `cognition`, or `tui`. Its
capability-agnostic boundary is intact.

---

## 9. Remaining Architectural Debt

Deliberately left untouched, with the reason.

| Baseline item | Why it was not changed |
|---|---|
| §18.1 Duplicated **turn preparation** (`tui/app.go: runTurnWithChoice` vs `cmd/ohgo/outcome.go: runHeadlessTurnAs`) | Genuinely shared: contract extraction → snapshot → shape/projection → grounding → request. But `tui` cannot import `cmd/ohgo` (package `main`), and no existing package can own it without either making `agent` know `cognition`/`workspace`, or making pure `cognition` orchestrate a live `*agent.Loop`. Both are layering regressions. Requires a deliberate new seam plus cross-path characterisation tests first. See [§11](#11-next-safe-refactor). |
| §17.1 Three "intent" concepts | Not the same concept. `workspace.Intent`, `contract.ObjectiveKind`, and `TaskShape.Intent` answer different questions; merging would lose information. |
| §17.2 Three "proposal" concepts | Different domain objects that happen to share a word. |
| §17.3 Two authority table implementations | `workspaceAuthority.Permits` already delegates to `workspace.Permits`. The remaining variants (`escapeAuthority`, legacy `Admit`) encode genuinely different human-vs-model policy. Consolidating them is an authority change. |
| §17.4 Three completion vocabularies | Vocabularies stay because they serve different consumers (process exit status, durable lifecycle, runtime verdict). Only the duplicated *decision* was consolidated. |
| §17.5 Two evidence computations | `cognition.EvidenceState` is bounded and durable for projection; `agent.scanCompletionEvidence` is complete and in-memory for the verdict. Different retention is intentional. |
| §17.8 Two mutation representations | Alias fields (`Operation`/`MutationType`/`Type`/`Op`, `Diff`/`Delta`/`Content`/`Text`) are compatibility scaffolding consumed by different readers. Removing them is a public-API change, and `Details["mutation_evidence"]` is read through several `Details` keys that `cognition` depends on. |
| §17.11 Read-only tool allow-list drift | `agent.ReadOnlyToolNames` omits `ls`, `web_fetch`, `artifact`, `mcp_call`. Deriving it from `Tier` would *widen* what a read-only subagent may call — a behaviour change, not a refactor. Reported for an explicit product decision. |
| §17.12 Tool-name / `Details`-key coupling | A string-based protocol between `tools` and `cognition`. A registry would be a new abstraction over a working contract; renaming requires tracing every consumer. |
| §17.15 Dead API (`prompt.ToolCatalog`, `var _` markers) | Zero callers, but removing exported symbols is an API-surface decision, not a refactor. `tools.MutationEvidence` is not dead after all — it is embedded in `Details["mutation_evidence"]`. |
| §18.20 Stale `workspace.Artifact` origin after a `/workspace` switch | A latent defect. Fixing it changes behaviour (which artifacts are accepted after a switch) and needs its own decision + test. |
| §18.23 Mid-session `/sessions` resume does not restore authority/model | Deliberate asymmetry between `--resume` and `adoptSession`. Changing it changes resume semantics. |
| `tui/app.go` (5,174 lines) | Next-largest god file, but splitting a type's methods across files is lower value than the `cmd/ohgo` split, and `runTurnWithChoice` (534 lines) shares ~15 locals across phases, so intra-method extraction is riskier than it looks (baseline hazard 25: the terminal CAS). |
| `agent/loop.go`, `targets.go`, `completion.go` | High-risk core. Baseline §13 requires incremental extraction with per-step behaviour comparison; nothing here was ready for a safe single slice. |

---

## 10. Risks

| Risk | Assessment | Mitigation |
|---|---|---|
| A future verdict value is added to `RunVerdict` and a shell's `switch` falls into `default` | Low; both defaults are fail-safe (`RunFailed` / `taskActionPause`) | The `default` arms map to the conservative outcome; a new value would need a deliberate mapping in both shells |
| `ClassifyRun` becomes a dumping ground for shell-specific policy | Low | It takes only `(err, CompletionInput, deadlineExpired)` and returns only `(verdict, reason)`; no shell type appears in its signature |
| The `cmd/ohgo` file split makes it harder to see that two functions are related | Low | Files are named after the responsibility; all symbols remain in one package, so `go doc`/`gopls` navigation is unaffected |
| An untested behaviour existed only in a branch of the removed switches | Low; branches were compared line-by-line and the union of both shells' tests covers every arm | `RunCancelled`, `RunPaused`, `RunNeedsReview`, `RunFailed`, `RunCompleted` each have at least one pinning test |
| Race-detector coverage of `tui` was run, not the whole tree | Accepted; the touched code adds no new concurrency | `-race` was run on the three touched packages; no shared state was introduced |

No known behavioural regression. No unexplained change.

---

## 11. Next Safe Refactor

**Candidate:** consolidate the duplicated *turn preparation* between
`tui.Program.runTurnWithChoice` and `cmd/ohgo.runHeadlessTurnAs`.

This is the highest remaining structural win: it is the same six-step sequence
in two places, and it is the part of baseline hazard 1 that this session did not
address.

**Why it was not done now.** It cannot be done safely as a mechanical slice,
because there is no correct home for the shared code:

* `tui` cannot import `cmd/ohgo` (package `main`).
* Putting it in `agent` would break the capability-agnostic boundary
  (`agent` would need `workspace` and `cognition`).
* Putting it in `cognition` would make a pure narrowing package orchestrate a
  live `*agent.Loop`.
* A new package would need a defined ownership boundary, not a "shared helpers"
  role.

**Recommended sequence before extracting:**

1. Write cross-path characterisation tests that assert both paths derive the
   same shape authority, install the same projection/grounding hooks, apply the
   same snapshot-recapture policy (resumed task keeps its baseline), and send
   the same request.
2. Choose one of: (a) move `runHeadlessTurnAs` into a new library package that
   both shells import, or (b) give the preparation step a narrow interface in
   `agent` (a `PrepareTurn` function field, matching the existing `Project` /
   `Summarize` seam style) and implement it in `cmd/ohgo` for both shells.
3. Extract one phase per commit, running `tui/completion_test.go`,
   `cmd/ohgo/headless_outcome_test.go`, and the route/authority suites after each.

Option (b) matches the codebase's existing callback-seam idiom and keeps
`agent` free of `workspace`/`cognition` types.

---

## Appendix — Baseline Claims Affected

Per the rule that architecture history is not silently erased.

| Baseline claim | Status |
|---|---|
| §11.2: "`turnTaskOutcomeWithReview` is a second mapping of the same evaluators used by `projectRun`. They agree today but are separate code paths." | **Partially superseded.** The *decision* is no longer duplicated: both call `agent.ClassifyRun`. The *projection* into `runState` and `taskAction` remains separate by design. Tests: the six `TestClassifyRun*` tests plus the existing shell suites. |
| §17.4: "Completion has three vocabularies … Mappings exist in `tui/completion.go` and `cmd/ohgo/outcome.go`." | **Still accurate.** Three vocabularies remain; only the shared decision moved. |
| §18.1: "Two orchestrators for one turn." | **Still accurate.** Turn *preparation* is still duplicated; only the post-run evaluation tail was consolidated. |
| §12: "`run.go` (one file) contains options, tool building, loop building, harness backend, headless runner, REPL, TUI wiring, wizard, and history rotation." | **Superseded.** `run.go` now contains only the command entry points; the rest was extracted into nine cohesive files in the same package. Verified text-preserving by fingerprint. |
| §20: "`cmd/ohgo` → everything, 27 internal imports; `run.go` mixes parsing, wiring, orchestration, and persistence." | **Partially superseded.** The package still imports 27 internal packages (the dependency graph is unchanged), but `run.go` no longer mixes the responsibilities. |

The baseline document itself was **not** edited; it remains the historical
evidence of the pre-optimization state.
