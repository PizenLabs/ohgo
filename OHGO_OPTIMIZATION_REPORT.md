# OHGO Optimization Report

> **Scope:** Behaviour-preserving structural optimization of the ohgo codebase.
> **Authority for "must not change":** `docs/OHGO_ARCHITECTURE_BASELINE.md`
> (read in full before any change).
> **Authority for "what actually happens":** the source and its tests.
> **Constraint honoured throughout:** no authority, execution, persistence,
> completion, or repository-intelligence semantics were altered.

> **Update — second pass (architectural completion).** Sections 1–11 document
> the first pass and remain historically accurate. The second pass is recorded
> additively in [§12](#12-second-optimization-pass--architectural-completion),
> which also states the final stopping-point recommendation.

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

---

## 12. Second Optimization Pass — Architectural Completion

> **Starting point:** commit `51c19ad`, clean tree. Sections 1–11 above were
> re-verified against the source: `agent.ClassifyRun` and the nine extracted
> `cmd/ohgo` files are present, `run.go` is 546 lines, and the suite is green.
>
> **Method this pass:** (a) a mechanical duplicate scan rather than intuition,
> (b) an explicit partition of the turn-preparation path before any extraction,
> (c) one semantic slice per canonical-owner question, (d) fingerprint/identity
> verification for every change that touched persisted values.

### 12.1 Refactoring ledger

| Candidate | Type | Current owners | Risk | Value | Correct owner known? | Action |
|---|---|---|---|---|---|---|
| duplicated turn preparation | orchestration | `tui/app.go`, `cmd/ohgo/outcome.go` | high | claimed high | **No** | investigated → rejected |
| model reference format `provider/id` | protocol / vocabulary | 31 ad-hoc sites in `cmd/ohgo`, `tui`, `bench` | low | high | **Yes** — `ai.Model` | **done** |
| session authority snapshot write | state transition | `SwitchWorkspace` inline + `recordSessionAuthority` | low | medium | **Yes** — existing `harness` method | **done** |
| terminal-status predicate | semantic | `tui.taskStatusTerminal` vs `task.Status.IsTerminal` | high | low | yes, but **seam forbids** it | rejected (documented projection) |
| unknown-authority defaulting | policy | `options.authority()` vs `harness.activeWorkspace()` | medium | low | no — different inputs | rejected |
| `agent/loop.go` cohesion split | structural | one file | high | low | no — one machine | rejected |
| authority mirrors | state | `options`, `harness`, gate, session | critical | high | no | preserve |
| persistence types | compatibility | `task`, `session`, `recovery` | critical | medium | n/a | preserve |
| repository intelligence | subsystem | `ast`, `tools.StructuralSource` | high | medium | n/a | preserve |
| tool/evidence coupling | protocol | `tools`, `cognition` | high | high | n/a (it is a contract) | preserve |

**Mechanical duplicate scan.** Two throwaway AST tools were built rather than
trusting eyeballing:

* `bodydup` — hashes each function/method body after `go/printer` normalisation.
* `bodydup2` — hashes the token stream with identifiers and literals replaced,
  so a copy that differs only in names still collides.

Both reported **0 duplicate groups across all production packages**. Conclusion:
the copy-paste duplication the baseline described has been eliminated (the first
pass removed the one literal case, `ClassifyRun`). Everything that remains in
baseline §17 is either a different concept with a shared name or a deliberate
projection — not duplicated code. This is textual evidence, not semantic proof,
which is why each remaining candidate below was still judged individually.

### 12.2 Completed slice 1 — the model reference has one owner

| | |
|---|---|
| **Target** | The `provider/model-id` reference format |
| **Problem** | `"provider" + "/" + id` was assembled at **31 sites** (`cmd/ohgo` 28, `tui` 1, `bench` 1, plus the pair form in `options.go`). Separately, `tui.ModelID` was exported and documented as "matching the CLI's own format" — while having **zero callers**. The type that owns the two fields, `ai.Model`, had no formatter. |
| **Old owner** | Nobody. 31 independent ad-hoc definitions, plus a dead `tui` helper claiming the role. |
| **New owner** | `ai.Model.Ref()` and `ai.Ref(provider, id)` — `ai/model.go`. `Model.Ref()` delegates to `Ref`, so the rule is implemented once. |
| **Why the owner is correct** | `ai.Model` owns `Provider` and `ID`; formatting their reference needs no other package. Direction stays `cmd/ohgo → ai` and `tui → ai` (both already existed). No new edge, no new package. The reference is **persisted** in `sessions.db` (`sessions.workspace/model`) and `settings.json` (`Roles`, `Model`), and parsed by `catalog.Resolve` and `config.splitModel`, both with `strings.Cut(ref, "/")`. Formatting it anywhere else is a second definition of a wire contract. |
| **Behaviour preserved** | Byte-identical output by construction. The existing assertion `h.sess.Model == "p/m"` (`TestTransitionKeepsTheSameSession`) still passes, and `TestApplySessionAuthorityRestoresWorkspaceAndModel` still reads back `anthropic/claude-sonnet-4-5`. `options.go`'s pair form now calls `ai.Ref(provider, id)`; `tui.ModelID` now delegates to `ai.Model.Ref()` instead of reimplementing (exported surface preserved). |
| **Tests added** | `ai/ref_test.go`: ref round-trips through the first-slash split; an OpenRouter-style id containing a slash stays one id; the pair form and the method agree. |
| **Verification** | Full suite green; `-race` on `ai`, `cmd/ohgo`, `tui`; zero `Provider + "/"` concatenations remain in non-test code (grep-verified). Test-the-test: changing the separator to `:` fails both round-trip tests, then restored. |

The OpenRouter case is the reason this matters: `openrouter/anthropic/claude-3.5-sonnet`
must survive a save/restore cycle, and every parser splits on the *first* slash.
A future formatter that escaped or dropped the inner slash would silently
retarget the model on resume. The named owner puts that decision in one place.

### 12.3 Completed slice 2 — one writer for the session authority snapshot

| | |
|---|---|
| **Target** | The persisted session authority transition |
| **Problem** | `harness.SwitchWorkspace` assembled and wrote the session's `workspace` + `model` snapshot inline (7 lines, including its own `store.SetAuthority` call and a duplicate warn-and-continue error path), while `harness.recordSessionAuthority` already did exactly that for `AssignRole` and `SetModel`. Two writers for one persisted fact. |
| **Old owner** | Split: `SwitchWorkspace` inline vs `recordSessionAuthority`. |
| **New owner** | `harness.recordSessionAuthority` — now the single writer. `SwitchWorkspace` sets only the workspace field it uniquely knows, then delegates. |
| **Why the owner is correct** | The method already existed and already carried the contract ("a storage failure is reported but never fails the assignment"). Adding a third caller is cheaper and safer than a third copy. The switch is the only path that also changes the workspace, so it sets that one field and lets the canonical writer persist. |
| **Behaviour preserved** | Exact equivalence: `recordSessionAuthority` writes `strings.ToLower(h.workspace)`, and `SwitchWorkspace` sets `h.workspace = ws.Label()` immediately before calling it, so `"BUILD" → "build" == ws.String()` for all three valid authorities. Nil `sess`/`store` handling and the warning text are unchanged. `h.sess.Workspace` is still set explicitly (the method does not own it). |
| **Tests added** | `cmd/ohgo/workspace_switch_test.go: TestSwitchWorkspaceRecordsTheSessionAuthority` — store-backed, asserts both the in-memory session and the persisted row. Written **first** and confirmed passing against the old implementation, then re-run after the change. |
| **Verification** | Switch/transition suite green; test-the-test: dropping the delegation fails with `stored session = build/…, want ask/…`, then restored. |

### 12.4 Rejected refactor — turn preparation (the Priority-1 candidate)

This was the main investigation. Both paths were read line by line and partitioned.

**Partition**

| Category | Operations | Same semantics? |
|---|---|---|
| **Common — already canonical calls** | `workspace.BuildIntentContract(text)`; `agent.CaptureTargetsContext`; `cognition.ProbeFacts`; `cognition.BuildShape`; `cognition.NewProjector`; `loop.SetProjectionHook`; `loop.SetGrounding`; `loop.Run` | Each is a *single call to a function that already has one owner*. No logic is duplicated. |
| **Common — repeated composition** | contract → snapshot → shape → projector → hook → grounding → request (≈7 lines) | **No.** Three of its five inputs differ in *source*, and one shell has an extra branch. |
| **TUI-specific** | route guard / `TurnAuthority`; task continuation decision (which can *replace* `intent` with the persisted contract); `task.Begin`; fresh `RunBudget` + `SetBudget`; authority label from `turn.Active`/`workspace.Current`; durable `EvidenceState`; `RecordProjection` sink; `persistTo(user)`; text/tool/turn hooks; mention + task-continuity + proposal context blocks; recovery journal; session metrics; terminal CAS; task transitions; assistant-tail publishing; result persistence; history rebase | — |
| **Headless-specific** | authority normalisation with `build`/`ask` fallback; ephemeral `EvidenceState`; `OHGO_DEBUG_CONTEXT` audit sink; `err == nil && ctx.Err() != nil` normalisation; `deadlineExpired` flag; `lastReport.EvidenceReview()`; `projectRun` → exit code | — |
| **Persistence-specific** | TUI persists the user message immediately and each assistant/tool result per turn; headless persists nothing here, and `ohgo run` deliberately does not store the user prompt | — |
| **Presentation-specific** | TUI publishes events; headless prints one verdict line | — |

**Why no shared owner can be correct**

1. **The only identical logic is already owned.** Contract extraction, snapshot
   capture, shape building, projection and grounding are each one call into a
   package that already owns them. Moving those calls into a new component does
   not remove a definition; it adds an indirection layer over existing seams.
2. **The composition's inputs differ in kind, not just in value.** Snapshot root
   and resolver differ (`root`/`structuralResolver(loop.Tools())` vs
   `p.completionRoot`/`p.completionResolver`); evidence differs (`NewEvidenceState()`
   vs `p.task.Evidence()`); the report sink differs (stderr audit vs
   `task.RecordProjection`). A shared function would need all of them as
   parameters, plus a branch for the TUI's nil-evidence fallback.
3. **Ordering is incompatible.** The TUI must interleave `task.Begin` and the
   fresh run grant *between* contract extraction and shape building: the shape
   consumes `loop.Budget().Snapshot()` after the new grant is installed, and
   `Begin` persists the contract. Even `SetGrounding` is installed at a different
   point in the two paths. A single shared "prepare" function would have to own
   the durable-task lifecycle or take it as a callback — i.e. it would need to
   know about durable tasks, which is exactly the domain knowledge that must not
   move into a neutral package.
4. **Every possible home is a layering regression.** `tui` cannot import
   `cmd/ohgo` (package `main`); `agent` must not import `workspace`/`cognition`;
   `cognition` is a pure narrowing package and must not orchestrate a live
   `*agent.Loop`; a new package would need a semantic boundary, and "the seven
   call-composition lines two shells happen to share" is not one.

**Conclusion:** this is category C (coincidentally similar), not category A.
The apparent duplication is a *repeated call sequence over already-canonical
owners*, not duplicated behaviour. Extracting it would add an abstraction with
no owner and encode shell differences as parameters. **Not extracted.**

Because this path is *not* being changed, no new characterization suite was
added for it: §19's requirement applies to seams that are about to move. Both
shells' verdicts remain pinned by the existing
`tui/completion_test.go`, `tui/budget_test.go`, and
`cmd/ohgo/headless_outcome_test.go` suites, plus the six `TestClassifyRun*`
tests from the first pass.

The one genuine divergence found while partitioning is worth recording: the
TUI lets `BuildShape` default an empty authority to `build`, while the headless
path applies an extra `informational → ask` rule. Those are different shell
policies, so they must not be unified as a "cleanup" — that would change what
the state block reports.

### 12.5 Other rejected candidates

| Apparent duplication | Why it is not safe to consolidate | Evidence |
|---|---|---|
| `tui.taskStatusTerminal` vs `task.Status.IsTerminal` | The TUI deliberately does not import `task`; the vocabulary is a projection. Consolidating would either import the domain type into the UI or export a string predicate purely to serve it. | `tui/task.go` documents the rule at the declaration: the task states "are duplicated here as plain strings rather than imported so the seam stays a projection". |
| `options.authority()` vs `harness.activeWorkspace()` | Different inputs (`workspace.Workspace` value vs a label string) and different failure modes (no parse vs `workspace.Parse` error). They share only the *value* `workspace.Build`, which is not a policy to abstract. | `workspace.Parse` is the canonical string parser; `options.authority` never parses. A merged helper would need both shapes and would hide which validation ran. |
| `cognition.EvidenceState` vs `agent.scanCompletionEvidence` | Bounded, durable, projection-facing vs complete, in-memory, verdict-facing. Different retention and different authority. | Baseline §17.5; the previous pass already rejected it, and nothing in this pass changed the inputs. |
| `agent/loop.go` cluster split | The file is **one state machine plus its configuration contract**. `Run`/`dispatch`/`execute`/`authorize` share state and ordering; the hook setters exist only to populate `Config`. Splitting them across files would make one machine read as several, which is the "split a state machine across arbitrary files" the brief forbids. | Distinct from `cmd/ohgo/run.go`, which held *unrelated* responsibilities (CLI parsing, wizard, REPL, history). There, splitting restored the package's own convention. |
| Read-only layers (semantic intent, security authority, tool capability, OS sandbox, delegation filter) | They protect different layers; collapsing them to one boolean would weaken the strongest boundary. | Baseline §17/§18.7; untouched this pass. |
| Authority mirrors (`options.workspace`, `harness.workspace`, `authorityGate`, `session.Workspace`) | Configuration vs live runtime value vs persisted snapshot. The invariant is that the execution path reads live authority at call time; merging the mirrors would require a redesign with new tests for resume/switch/adopt. | Baseline invariant 6 and hazard 17; `securityGate.authorityInForce` reads the gate per call. |
| Tool names / `Details` keys | A string protocol between `tools` and `cognition`. Renaming silently degrades evidence and completion. | Baseline hazard 12. |
| `prompt.ToolCatalog`, `tui.ModelID` | Dead exported API. Zero callers each. | Removing exported symbols is an API decision, not a refactor. `tui.ModelID` was *delegated* rather than deleted, so its surface is unchanged. |

#### 12.5.1 Semantic overlap record

The eight concepts the brief names, in the required shape. "Refactor safety"
records the decision this pass made, not a hypothetical.

**Intent**

| Field | Value |
|---|---|
| Representations | `workspace.Intent` (`read`/`execute`/`investigate`); `contract.ObjectiveKind`; `cognition.TaskShape.Intent` (lowercased string) |
| Authoritative | Each is authoritative **for its own question**. Routing intent decides whether the workspace can answer at all; objective kind decides what completion must prove; shape intent decides which capabilities look relevant |
| Derived | `TaskShape.Intent` is derived from kind when a caller omits it (`BuildShape`), so it is the only derived one |
| Creation | `workspace.Classify`; `workspace.BuildIntentContract`; `cognition.BuildShape` |
| Mutation | None after creation (pure functions) |
| Consumers | `RouteTurn`, `ProposeTransition`, shape, TUI chrome; completion; candidates/sufficiency |
| Persistence | Objective kind + contract are persisted in `progress.json`; routing intent is not persisted |
| Refactor safety | **Do not merge.** READ can be informational; EXECUTE can be execution or mutation. Merging loses the distinction completion depends on |

**Grounding**

| Field | Value |
|---|---|
| Representations | `contract.IntentGrounding` only |
| Authoritative | Same type; built once from user text |
| Derived | `cognition.TaskShape` copies entities/ambiguities into its own fields for display and narrowing |
| Creation | `workspace/grounding.go: buildGrounding` |
| Mutation | Read by the loop per call; never rewritten |
| Consumers | `ValidateProposal` (admission), `AssessGrounding` (review), cognition state block |
| Persistence | Embedded in `contract.IntentContract` → `progress.json` |
| Refactor safety | **No change.** Single representation already |

**Proposal**

| Field | Value |
|---|---|
| Representations | `contract.ActionProposal` (a tool call to validate); `workspace.Proposal` (a human decision); `workspace.TransitionProposal` (recommended authority move) |
| Authoritative | Each for its own decision; none overrides another |
| Derived | `tui.ProposalResolution` holds the second; nothing derives the others |
| Creation | `agent.ProposalForCall`; `workspace.AssessProposal`; `workspace.ProposeTransition` |
| Mutation | Immutable values |
| Consumers | Admission/audit; TUI prompt + prompt block; TUI authority selector |
| Persistence | None (decision text may ride the system context for one turn) |
| Refactor safety | **Name collision only.** Category C |

**Evidence**

| Field | Value |
|---|---|
| Representations | `cognition.EvidenceState` / `EvidenceItem`; `agent.completionEvidence` (private, from `scanCompletionEvidence`); `agent.VerificationEvidence`; `agent.MutationEvidence` |
| Authoritative | `agent.scanCompletionEvidence` for the **verdict**; `cognition.EvidenceState` for **projection and sufficiency**. Neither may be substituted for the other |
| Derived | `EvidenceState` is bounded and durable; the completion scan is complete and in-memory |
| Creation | `Projector.ObserveTurns/ObserveOutcome`; `scanCompletionEvidence`; tool results |
| Mutation | `EvidenceState` accumulates per turn; completion evidence is recomputed per verdict |
| Consumers | Projection, task checkpoint, cognitive review; completion |
| Persistence | `EvidenceState` in `progress.json`; the rest are run-local |
| Refactor safety | **Do not merge.** Different retention and authority; baseline §17.5 |

**Projection**

| Field | Value |
|---|---|
| Representations | `agent.ProjectionRequest/Result/Report`; `cognition.Projector`; `task.Progress` evidence |
| Authoritative | The loop owns the applied request; the projector may only narrow |
| Derived | The projected message/tool/system view is per-request and discarded |
| Creation | `cognition.NewProjector(...).Project` |
| Mutation | `ObserveTurns` mutates the durable evidence while projecting |
| Consumers | `Loop.Run` (intersect by name), `OnProjection` audit, task review |
| Persistence | Only the evidence state |
| Refactor safety | **Preserve narrowing-only.** Baseline invariant 12 |

**Completion**

| Field | Value |
|---|---|
| Representations | `agent.CompletionState` (`COMPLETED`/`NEEDS_REVIEW`); `task.Status` (7 states); `cmd/ohgo.runState` (5 states); `tui.taskAction` (4) |
| Authoritative | `agent.EvaluateTaskCompletion` / `EvaluateBudgetCompletion` decide; `agent.ClassifyRun` selects and classifies (first pass) |
| Derived | `runState`, `taskAction`, and `task.Status` are projections of that decision |
| Creation | `ClassifyRun` → shell projection → controller transition |
| Mutation | Task controller is the only writer of `Status` |
| Consumers | Exit code; durable lifecycle; UI |
| Persistence | `task.Status` in `progress.json` only |
| Refactor safety | **Decision already consolidated; vocabularies stay.** Category B |

**Task shape**

| Field | Value |
|---|---|
| Representations | `cognition.TaskShape` |
| Authoritative | Same; it is a narrowing input, never an execution authority |
| Derived | Candidate tool set, state block, sufficiency judgement |
| Creation | `cognition.BuildShape` per turn |
| Mutation | Immutable after build |
| Consumers | Projector, prompt state block, `EvaluateSufficiency` |
| Persistence | Embedded in projection reports; the audit is persisted, the shape is not |
| Refactor safety | **No change.** Single representation |

**State**

| Field | Value |
|---|---|
| Representations | `agent.Turn`; `tui.State.Entries` + `Program.history`; `session` rows; `task.Progress`; `authorityGate`; `session.Session.Workspace/Model` |
| Authoritative | `session` for the transcript; `progress.json` for the task; `authorityGate` for live authority; `session.Session` for the persisted snapshot |
| Derived | TUI entries and `Program.history` are disposable mirrors; `harness.workspace` is a label mirror of the gate |
| Creation | Loop (turns), event bus (UI state), store (transcript), controller (task), explicit switch (authority) |
| Mutation | Each owner only; UI mirrors are rebuildable |
| Consumers | Renderer, prompt continuity, resume, authorization |
| Persistence | Transcript, task checkpoint, session snapshot |
| Refactor safety | **Mirrors preserved.** Unifying them changes resume/switch semantics (baseline hazard 17/23) |

### 12.6 Remaining debt

| Classification | Item |
|---|---|
| **SAFE NEXT** | `cmd/ohgo/harness.go` (965 lines) still mixes four clusters: model plane, session/recovery, reporting, workspace. A same-package split is fingerprint-verifiable. |
| **SAFE NEXT** | `tui/app.go` (5,174 lines) method families (support, model plane, task, recovery, sessions) could be separated across files; same-package, fingerprint-verifiable. |
| **DEFERRED** | Turn-preparation sharing — blocked on a missing seam, not on effort. Requires a deliberate architecture decision first. |
| **DEFERRED** | `agent/loop.go` cohesion — no cluster has independent semantic identity. |
| **LOAD-BEARING** | Authority mirrors; five read-only layers; callback installation order; TUI terminal CAS; journal-before-mutation; projection narrowing; task contract immutability. |
| **PUBLIC API** | `prompt.ToolCatalog` (zero callers); `tui.ModelID` (now a one-line alias, zero callers). Removal needs an explicit decision. |
| **SCHEMA / COMPATIBILITY** | `progress.json` embedded types; `sessions.db` and `recovery.db` schemas; the `provider/id` reference format (now named, unchanged). |
| **BEHAVIOR-CHANGING** | `agent.ReadOnlyToolNames` drift (deriving from `Tier` would widen read-only children); stale `workspace.Artifact` origin after a switch; mid-session `/sessions` resume not restoring authority/model. All three are product decisions with observed behaviour to change, not refactors. |

### 12.7 Architectural delta

What became simpler — stated as ownership, not line count:

* **A persisted format rule went from 31 definitions to 1.** The model reference
  is now owned by the type whose fields it is made of. Before, `tui` exported a
  formatter nobody called while the CLI reimplemented it 28 times; a change to
  the separator or escaping would have required finding all of them.
* **A persisted state transition went from 2 writers to 1.** The session
  authority snapshot now has a single implementation, and its failure policy
  (report, continue) is stated once.
* **Nothing else moved.** No package was added or removed; no import edge
  changed; no type was relocated; no dependency direction was altered.

What deliberately did **not** change: authority, execution order, evidence,
completion, persistence representation, repository indexing, and the two
orchestration paths.

### 12.8 Verification

| Check | Command | Result |
|---|---|---|
| Formatting | `gofmt -l .` | empty |
| Build | `go build ./...` | pass |
| Vet | `go vet ./...` | pass |
| Tests | `go test ./...` | pass, all 43 packages |
| Race | `go test -race ./ai ./cmd/ohgo ./tui` | pass |
| Dependency graph | `go list -deps ./...` | pass; `go list ./...` reports 43 packages; Go rejects import cycles at compile time, so the acyclic invariant holds |
| Architecture/invariant tests | included in `go test ./...` | `agent/authorize_test.go`, `security`, `cmd/ohgo/authority_route_test.go`, `task`, `recovery`, `ast`, `tui/completion_test.go` all green |
| Duplicate scan | `bodydup`, `bodydup2` over production packages | 0 duplicate groups |
| Reference-format invariant | `grep 'Provider *+ *"/"'` in non-test code | 0 matches |
| Binary smoke test | `ohgo version` | `ohgo 0.0.3-dev` |
| Test-the-test | break each new test's subject | both new suites fail as expected, then restored |

*Reconciliation note:* the baseline (§2) and the first-pass §5 both report 44
packages; `go list ./...` reports **43** in the current tree. No package was
removed by either pass (no import edges changed), so the earlier count was
simply off by one. The package *graph* is unchanged.

### 12.9 Final recommendation

**The repository has reached a safe architectural stopping point.**

Justification, in the brief's own terms:

* The category-A duplication that remained has been removed. A token-level and
  body-level scan over every production package returns **zero** duplicate
  implementations, so further "de-duplication" would be inventing duplication
  rather than finding it.
* Every remaining candidate requires at least one of: a behaviour change, a
  persistence migration, an authority redesign, a public-API decision, or a new
  abstraction with no provable owner. Those are precisely the stop conditions.
* The two orchestration paths are *not* a shared owner waiting to be discovered.
  The partition above shows the shared part is a sequence of calls to functions
  that already have owners, and the differing part is the durable-task and
  recovery lifecycle. Unifying them would move domain knowledge into a neutral
  package — a net loss of clarity.
* The remaining large files (`harness.go`, `tui/app.go`) are structural, not
  semantic, and are recorded as SAFE NEXT rather than forced through now.

A further *optimization* pass is not justified. A further *structural* pass
(splitting the two remaining large files) is possible and low-risk, but it is
file organisation, not architecture, and should be scheduled on its own merits.
