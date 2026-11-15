# Runtime Reliability — `golang-ecommerce` workload

> **Scope.** End-to-end reliability of the Ohgo runtime (control plane,
> execution plane, evidence, sufficiency, completion, continuation) against a
> real Go repository, `golang-ecommerce`, across the task classes in the
> hardening matrix.
> **Workload:** `/Users/anvndev/Documents/Project/Golang/ecommerce` (Go 1.26
> module, 305 Go files, 110 test files, all tests green, `go vet` and
> `golangci-lint` clean).
> **Models:** `opencode-go/beam-free` (configured role model) and
> `opencode-go/kimi-k3.1-pro` for the mutation scenarios.

## Executive verdict

**STABLE FOR TESTED SURFACE.**

Five confirmed Ohgo defects were repaired at their owning layer, each with a
regression that fails before the fix and passes after it, and each affected
real-project scenario was re-executed. No critical or high-severity runtime
defect remains in the exercised surface. The residual failures below are model
or environment limits, not runtime invariant violations.

One workload defect was fixed by the runtime itself under BUILD authority:
the cart and checkout order-summary per-line total now renders unit price ×
quantity (`layout/tmpl/cart/show.gohtml`, `layout/tmpl/checkout/show.gohtml`,
plus a rendering regression in `layout/http_checkout_test.go`). Its regression
test fails on the pre-fix templates and passes on the fixed ones.

## Test matrix

| ID | Class | Role | Model | Calls | Verdict | Classification |
| --- | --- | --- | --- | ---: | --- | --- |
| A1 | repository understanding ("analyze what this project is about") | ASK | beam-free | 17 | NEEDS_REVIEW | MODEL_BEHAVIOR |
| A2 | architecture + entry points | ASK | beam-free | 17 | NEEDS_REVIEW | MODEL_BEHAVIOR |
| B | checkout investigation, reproduce | FUZZ | beam-free | 42 | PAUSED 30/30 | MODEL_BEHAVIOR |
| C1 | free-shipping bug report (ambiguous intent) | BUILD | beam-free | 68 | PAUSED 60/60 | MODEL_BEHAVIOR |
| C2 | cart line-total bug report | BUILD | beam-free | 43 | PAUSED 40/40 | MODEL_BEHAVIOR |
| C3 | cart line-total bug report | BUILD | kimi-k3.1-pro | 85 | NEEDS_REVIEW | **RUNTIME_COMPLETION** |
| C4 | C3 after the fix, same model and prompt | BUILD | kimi-k3.1-pro | 57 | COMPLETED | — |
| D | existing implementation, no change requested | BUILD | beam-free | 8 | COMPLETED | — |
| D3 | trivial verified mutation (smoke) | BUILD | beam-free | 1 | NEEDS_REVIEW | MODEL_BEHAVIOR |
| F | verification-only ("is rate limiting implemented?") | ASK | beam-free | 5 | COMPLETED | — |
| G | git-state inspection | BUILD | beam-free | 2 | NEEDS_REVIEW | **RUNTIME_EVIDENCE** |
| G2 | G after the evidence fix, before the sufficiency fix | BUILD | beam-free | 2 | NEEDS_REVIEW | **RUNTIME_SUFFICIENCY** |
| G3 | G after both fixes | BUILD | beam-free | 5 | COMPLETED | — |
| H | ignored files / hidden metadata | ASK | beam-free | 2 | COMPLETED | — |
| R | resume a PAUSED FUZZ task (session 380) | FUZZ | beam-free | 7 | COMPLETED | — |

Counts: 15 scenarios, 6 COMPLETED, 6 NEEDS_REVIEW, 3 PAUSED, 0 FAILED, 0
BLOCKED. Five runtime defects, two model-behavior limits, zero environment
failures, zero target-project failures after the fix.

## Runtime defects discovered

### D1 — git mutations classified as read

| Field | Value |
| --- | --- |
| Severity | High |
| Subsystem | RUNTIME_EXECUTION (`internal/execution/tools`) |
| Observed | `CommandMutatesInRoot` returned `false` for `git -C . commit -m x`, `git -c core.pager=cat commit`, `git --git-dir .git commit`, `git branch feature-x`, `git remote add origin …`, `git notes add`, and `find . -name '*.tmp' -delete`. |
| First incorrect transition | git subcommand resolution: `firstNonFlag` returned the *value* of a value-taking global option, and positional `branch`/`remote`/`notes` writes were never inspected. |
| Root cause | The classifier assumed the first non-flag word is the subcommand and modelled only destructive flags. |
| Fix | `gitSubcommand` skips `-C`, `-c`, `--git-dir`, `--work-tree`, … ; `branchMutates`, `configMutates`, `remote`/`notes`/`worktree` subcommand tables; `-u/--set-upstream-to/--edit-description`; `config -e/--edit`; `findMutates` for `-delete` and the `-fprint` family (`-exec` stays opaque). |
| Regression | `TestCommandCapabilitySeesGitMutationsBehindGlobalOptions`, `TestCommandCapabilitySeesPositionalGitWrites`, `TestBashReportsGitWritesAsMutationAndGitReadsAsExecution` |
| Verification | Focused tests fail on the old classifier (probe: `false` for every command above) and pass now; read-only git and `find` without a write predicate still classify exec. |

### D2 — complete git enumeration could not support an absence conclusion

| Field | Value |
| --- | --- |
| Severity | Medium |
| Subsystem | RUNTIME_EVIDENCE (`internal/agent`, `internal/execution/tools`) |
| Observed | Scenario G: `git status` said "No commits yet", `git tag -l` and `git branch -a` returned empty; the answer was reviewed with `the answer treats "commits" as absent without evidence covering the claim`. |
| First incorrect transition | The grounding coverage model had no representation for a completed VCS enumeration, so every VCS absence claim was unprovable. |
| Root cause | Coverage only knew glob scans, root listings, and structure test counts. |
| Fix | The bash tool publishes `git_absence` (complete, unpiped, successful enumerations scoped to the workspace; truncated/piped/other-repo invocations excluded; explicit "No commits yet" accepted on a failed `git log`). Grounding maps VCS nouns to those namespaces. |
| Regression | `TestGitAbsenceNamespacesRequiresACompleteLocalEnumeration`, `TestEvaluateTaskCompletionAcceptsObservedVCSAbsence` (with a control that still reviews without the observation) |
| Verification | G3 COMPLETED with the same words that were reviewed in G; re-executed on the final binary as G4 (COMPLETED, 6.6 s). |

### D3 — every repository-scoped question demanded a source survey

| Field | Value |
| --- | --- |
| Severity | Medium |
| Subsystem | RUNTIME_SUFFICIENCY (`internal/control/cognition`) |
| Observed | Scenario G2: a complete, correct git-state answer was stopped as `runtime evidence was not sufficient for the objective (INSUFFICIENT) at stage explore`. |
| First incorrect transition | `TaskShape` added `repository_overview` for any repository-scoped informational objective, including one whose evidence is VCS metadata. |
| Root cause | Requirement derivation read scope alone, not what the objective asks about. |
| Fix | `TaskShape.VCSInspection`; the survey requirement is suppressed for a pure VCS question (kept when the request also asks for content structure); a `vcs_observation` requirement is satisfied by a completed git read (`GitObservation`, scoped to the workspace). |
| Regression | `TestVCSQuestionRequiresAGitObservationNotASourceSurvey`, `TestGitObservationExcludesMutationsAndFailures` |
| Verification | G3 and G4 COMPLETED; broad-architecture shapes still require `repository_survey`, and "explain git hooks" does not become a VCS inspection. |

### D4 — a targetless mutation with a stated expected outcome could never complete

| Field | Value |
| --- | --- |
| Severity | High |
| Subsystem | RUNTIME_COMPLETION (`internal/control/workspace`) |
| Observed | Scenario C3: the model changed both templates, added a rendering regression, confirmed it failed before the fix, and ran it green. Verdict: `NEEDS_REVIEW: the request does not name a verifiable target or behavioural check`. |
| First incorrect transition | `BuildIntentContract` marked the objective `Ambiguous` because it named no path, ignoring the expected-outcome clause ("… so it equals unit price times quantity") that is the oracle. |
| Root cause | Behavioural detection was a phrase whitelist (`fix the failing tests`, …) plus a website-creation regex; it did not read a stated expected result. |
| Fix | `expectedOutcomeObjective` recognises a stated value/comparison or a failure-free outcome; such a mutation becomes behavioural without being forced to a repo-wide check (the evaluator bounds it by the changed packages). Unbounded ("update the code to be more robust") and subjective ("so it is cleaner") requests still stay ambiguous. |
| Regression | `TestBuildIntentContractAcceptsAStatedExpectedOutcome`, `TestEvaluateTaskCompletionAcceptsStatedOutcomeWithALocalCheck`; the existing `TestBuildIntentContractMarksUnboundedMutationAmbiguous` remains green as the negative control. |
| Verification | Contract test fails with the old resolver (Ambiguous=true) and passes now. Scenario C4, replayed on the reverted workload with the same model and prompt, COMPLETED. |

### D5 — TUI animation tests depended on the developer's environment

| Field | Value |
| --- | --- |
| Severity | Medium (baseline suite was red) |
| Subsystem | TEST_INFRASTRUCTURE (`internal/tui`) |
| Observed | `TestFadeStepsThroughThreeRGBPhases` and `TestAssistantFinalLineFadesOnSettle` failed deterministically when `NO_COLOR` is set or `TERM=dumb`. |
| Root cause | The tests forced the colour profile but not the environment gates `fadeSupported` honours. |
| Fix | `forceFadeRendering` clears `NO_COLOR`/`REDUCED_MOTION`/`NO_ANIMATION`, sets a colour-capable `TERM`, forces TrueColor, and restores both. The skip in `TestCompletedToolTweensStatusColor` was removed so the contract is always exercised. |
| Regression | Those three tests, run under this machine's `NO_COLOR=1 TERM=dumb`. |

## Invariant results

| Invariant | Result | Evidence |
| --- | --- | --- |
| Authority | PASS | ASK/FUZZ runs never mutated the workspace; BUILD run C4 mutated only the two templates and one test. A stale task objective does not grant authority (existing suite). |
| Execution semantics | **FIXED** | D1: the mutating git forms and `find -delete`/`-fprint` now classify write; read-only git and `find` predicates stay exec. `sh -c` and `find -exec` remain opaque (see risks). |
| Evidence | **FIXED** | D2: a complete enumeration supports absence; without it the same claim is still reviewed. |
| Sufficiency | **FIXED** | D3: VCS observations satisfy VCS questions; source surveys remain required for architecture questions. |
| Verification | PASS | D3 smoke run was refused completion after a shell redirect without a check; C4 completed only after a green test. |
| Completion | **FIXED** | D4: observed change + named oracle completes; an unbounded mutation stays reviewable; misalignment and unexpected-path checks are unchanged. |
| Continuation | PASS | Session 380 (PAUSED at 30/30) resumed with the same FUZZ authority and reached the correct checkout root cause from retained evidence in 7 calls. |
| Budget | PASS | B, C1, C2 stopped as PAUSED at the configured turn cap with the reason reported, never as success. |
| Persistence | PASS | Sessions, transcripts, and `--resume` agree on model, workspace, and history after process restarts. |

## Regression status

All new regressions pass, and each runtime regression was verified to fail with
the fix reverted:

- `TestCommandCapabilitySeesGitMutationsBehindGlobalOptions`
- `TestCommandCapabilitySeesPositionalGitWrites`
- `TestGitAbsenceNamespacesRequiresACompleteLocalEnumeration`
- `TestGitObservationExcludesMutationsAndFailures`
- `TestBashReportsGitWritesAsMutationAndGitReadsAsExecution`
- `TestEvaluateTaskCompletionAcceptsObservedVCSAbsence`
- `TestVCSQuestionRequiresAGitObservationNotASourceSurvey`
- `TestBuildIntentContractAcceptsAStatedExpectedOutcome`
- `TestExpectedOutcomeDistinguishesResultsFromSubjectiveImprovement`
- `TestEvaluateTaskCompletionAcceptsStatedOutcomeWithALocalCheck`
- `internal/tui` fade tests under `NO_COLOR=1 TERM=dumb`

Full validation: `gofmt -l` empty, `go build ./...`, `go vet ./...`,
`golangci-lint run ./...`, `go test ./...`, and `go test -race ./...` all pass.

## Known limitations (evidence-backed)

- **Model capability, not runtime.** `beam-free` explored without acting on two
  bounded mutation tasks (C1: 60 turns; C2: 40 turns) and produced a one-call
  mutation with no structured check (D3). The same C-class task completed with
  `kimi-k3.1-pro` (C4). No runtime invariant was relaxed to compensate.
- **Documentation attribution.** A1/A2 answers relied on README claims for
  runtime behavior and were reviewed. The runtime-state briefing already states
  the claim rule ("configuration declares topology, not execution; implementation
  status and behavior need source/runtime evidence"); the model did not follow it.
- **Honest-gap answers are completable for informational objectives.** D and H
  were COMPLETED while stating an evidence gap. This is pinned behavior: a
  bounded uncertainty report ("could not verify X from the inspected files") is
  explicitly not the unverified-search failure (`TestExplicitlyUnverifiedBehaviorIsNotAnObservation`).
- **Hidden entries need `hidden`.** `ls` defaults to no dotfiles, so a question
  about `.github` requires `hidden: true` or a glob. The tool documents both; H
  stopped after one default listing.

## Efficiency

| Scenario | Calls | Wall time |
| --- | ---: | ---: |
| A1 / A2 | 17 / 17 | 10.5 s / 14.8 s |
| G / G2 / G3 | 2 / 2 / 5 | 4.8 s / 4.9 s / 3.4 s |
| D / F / H | 8 / 5 / 2 | 9.9 s / 10.1 s / 7.3 s |
| C3 / C4 (same task) | 85 / 57 | 196 s / 85 s |
| B / C1 / C2 (turn-capped) | 42 / 68 / 43 | 58 s / 274 s / 69 s |
| R (resume) | 7 | 55 s |

Duplicate reads were observed only in model-driven loops (A1 replayed three
identical observations; C2 repeated the same test-hunting search). The runtime
replayed those observations with an explicit "unchanged since your earlier call"
notice; it did not fabricate new evidence. Token usage is not persisted in the
session store, so no token totals are claimed.

## Remaining risks

- **Opaque interpreters and predicates.** `sh -c 'rm …'` and `find … -exec rm …`
  are not classified as mutations by static parsing; the OS sandbox is the
  boundary there. A read-only authority still cannot be bypassed, but a BUILD
  run of these forms is not attributed as a mutation.
- **Target extraction over-reach.** "Add README.md to .gitignore" extracts
  `README.md` as a change target, not `.gitignore`; dot-prefixed paths are not
  extracted at all. This is pre-existing and could cause a false target review;
  it was not exercised by the matrix.
- **Untested surfaces.** MCP tools, browser verification, HTTP runtime checks,
  delegation/subagents, and compaction under long real-project runs were not
  exercised. The continuation test covered one resume of one PAUSED task.
