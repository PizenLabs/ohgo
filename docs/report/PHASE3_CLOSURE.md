# Phase 3 Closure — Final Runtime Integration & Hardening

> **Date:** 2026-10-22
> **Baseline commit:** `41fe9b8` (clean working tree at start)
> **Spec:** [`../benchmarks/PHASE3_SPEC.md`](../benchmarks/PHASE3_SPEC.md)
> **Verdict:** **HOLD** — see §13.

## 1. Scope

Audit the integrated runtime, try to falsify the hypothesis that Ohgo turns
LLM proposals into bounded, authorized, observable, verifiable work, fix only
demonstrated defects, and document the rest. No new capability was built.

## 2. Initial architecture audit

Read directly from source (not from prior reports):

- **Loop:** `agent/loop.go`. `Run` iterates until a turn has no tool calls.
  Bounds are `ctx` cancellation and an optional wall-clock `Deadline`. There is
  **no turn cap**. Expiry returns turns with a nil error.
- **Admission:** `Config.Authorize` is the only admission path in production.
  `execute` → `authorize`: `Evaluate` → `AUTO` runs; `APPROVE` → `Approve` → a
  second `Evaluate` must return `AUTO`; `DENY` never runs. The legacy
  `Admit`/`Approve` pair is consulted only when `Authorize` is nil; production
  always sets `Authorize` (`cmd/ohgo/run.go`).
- **Gate:** `cmd/ohgo/security.go` `securityGate` builds the request from the
  tool's declared tier, an optional per-call `CapabilityReporter`, and the
  normalised scope. The model cannot set capability, risk, or the approval
  flag. Grants are one-shot, keyed to the tool call id, and consumed on
  evaluation.
- **Authority:** `workspace.Permits` is the single permission table. `ASK` is
  read/search/inspect; `FUZZ` adds exec; `BUILD` adds mutation. The live
  `authorityGate` is read at call time by both the loop and subagents.
- **Routing:** `workspace.RouteTurn` narrows the gate per turn. Refused routes
  install nothing and never run the loop, in all three entry points (REPL,
  one-shot, TUI `runTurn`).
- **Subagents:** `agent.RunSubagent` inherits `Authorize`, `Admit`, `Approve`,
  and `Deadline`. The `task` tool sets `Depth = parent + 1`.
- **Completion:** `tui.runTurn` → `turnCompleted` → `task.Controller.Complete`.
  Completion is decided from the final turn shape and the todo snapshot. No
  verification step runs.

Causal map as implemented:

```text
request → RouteTurn (gate narrowed) → loop turn → model proposal
  → Authorize (Evaluate→Approve→Evaluate) → tool.Execute → ToolOutcome
  → turnCompleted / Controller.Complete (todo snapshot only)
```

The implementation matches the architecture contract on authority and
admission. It diverges on completion, which is model-driven (see §10).

## 3. Tests performed

| Area | Method | Outcome |
|---|---|---|
| Baseline | `go build`, `go vet`, `go test ./...` before any change | green |
| Intent | probe of 18 phrasings through `ClassifyPrediction` | 1 defect |
| Subagent deadline | probe: child with expired parent deadline | 1 provider call made → defect |
| Nested delegation | probe: depth-1 child `task` tool, maxDepth 3 | `no subagent spawner configured` → defect |
| Completion | probe: `turnCompleted` after failed edit; `Controller.Complete` with no todos | defect; contract gap |
| Shell authority | probe: `tools.CommandMutates` on nested shell / interpreter writes | FUZZ bypass |
| Mutation of fixes | each fix broken in turn; its regression test must go red | 4/4 red |

## 4. Adversarial scenarios

Probes were run for the rows marked **run** below. Rows marked **not run** are
open (see §15).

| Scenario | Status | Result |
|---|---|---|
| read-only inspection | run | classified READ |
| explicit file creation | run | classified EXECUTE |
| mixed read + mutation | run | fixed (`then` case) |
| ambiguous symbol / discovery | not run | — |
| nonexistent target / malformed source | not run | — |
| huge line / oversized file (grep) | not run | `grep.go` regression tests exist, not re-audited here |
| timeout as negative result | not run | — |
| tool failure observable | run | failed tool → IsError result; now blocks completion |
| verification failure → not complete | run | **not a real state**: no verifier exists (§10) |
| model false completion | run | **defect**: failed tool then clean stop → COMPLETED (fixed) |
| partial mutation | not run | — |
| cancellation | not run | — |
| repeated failure bounded retry | not run | — |
| subagent mutation authority | run | child inherits live gate (code review + existing tests) |
| long-running loop | not run | no turn cap; deadline optional |
| stale/deleted symbol | not run | — |

## 5. Defects found

| ID | Defect | Contract | Severity |
|---|---|---|---|
| D-1 | `Inspect the server and then add a timeout.` classified READ; the mutation clause is never attempted. | Intent must not drop explicit mutation clauses (spec §6). | High (silent non-action) |
| D-2 | Subagent child dropped the parent `Deadline`; child made provider calls after the budget expired. | `Config.Deadline` "bounds the whole run". | High (resource bound bypass) |
| D-3 | Child at depth < maxDepth−1 was offered a `task` tool with a nil spawner: nested delegation always failed. | Subagent depth contract (`--max-depth` allows delegation one level further). | Medium (functional, observable) |
| D-4 | A run with a failed tool call and a clean closing stop was recorded COMPLETED. | Truthful state, contract §31: never complete on a plausible response. | High (false success) |

## 6. Fixes

| ID | File | Change |
|---|---|---|
| D-1 | `workspace/intent.go` | `splitClauses` strips a leading `then` from each clause, since `normalizeRequest` removes the comma that would otherwise separate it. |
| D-2 | `agent/subagent.go` | `RunSubagent` passes `parent.Deadline` to the child config. |
| D-3 | `cmd/ohgo/run.go` | `childToolSet` takes the registry and gives the nested `task` tool a real spawner (`newSubagentSpawner`). |
| D-4 | `tui/app.go` | `turnCompleted` returns false when any tool outcome has `IsError`. The pause reason is "tool call failed; objective not confirmed" when a clean finish followed a failure, so it is not reported as a deadline. |

All fixes are local. None is a refactor.

## 7. Regression coverage

| Fix | Test | Mutation check |
|---|---|---|
| D-1 | `workspace/intent_test.go` `TestClassifyKeepsMutationAfterSequencedReadClause` | removing the strip → FAIL |
| D-2 | `agent/subagent_test.go` `TestRunSubagentHonoursParentDeadline` | deadline disabled (`AddDate(100,0,0)`) → FAIL: "the child made 1 provider calls" |
| D-3 | `cmd/ohgo/subagent_test.go` `TestChildTaskToolBelowCapHasASpawner` | nil spawner → FAIL |
| D-4 | `tui/completion_test.go` `TestTurnCompletedRejectsRunWithFailedToolCall`, `TestTurnCompletedAcceptsCleanRun`, `TestTurnCompletedRejectsRunStoppedMidWork` | removing the failure check → FAIL |

Existing `cmd/ohgo` tests `TestChildToolSetBoundsTheTreeStructurally` and
`TestChildToolSetDoesNotMutateTheParent` were updated for the new signature and
still pass.

## 8. Runtime and security findings

- **Authority gate design is sound.** Admission precedes approval; `DENY` is
  terminal; the second `Evaluate` after approval prevents a stale grant from
  executing. Refused routes never reach the loop in any of the three entry
  points.
- **Command classification is heuristic (§9).**
- **`--yes` (auto-approve) and settings `allow` rules skip the prompt for APPROVE
  decisions.** This is a user decision and is by design. It does not bypass
  `DENY`, because policy is evaluated first.
- **Settings are read from the project.** A repository-local config that sets
  `allow` for `bash` would auto-approve exec in that repository. Not verified in
  this pass (§15).

## 9. Resource findings

- The loop has no turn cap. With no `--deadline` and no interruption, a model
  that keeps calling tools runs indefinitely. This is the documented default
  (deadline 0 = no limit). Recorded as a limitation, not changed.
- Subagent recursion is bounded structurally by `--max-depth` (default 1, so
  delegation is opt-in). D-3 made nested delegation actually run, so the
  bound now has real traffic. Its cost has not been measured.
- Race detection passed across the module (§17), but no leak or memory profile
  was taken.

## 10. Remaining limitations

**L-1 — FUZZ no-mutation is bypassable through nested shells and interpreters
(authority gap).** Probe results:

```text
"echo hi > notes.txt"                             mutates=true
"bash -c 'echo hi > notes.txt'"                   mutates=false
"sh -c \"rm -f notes.txt\""                        mutates=false
"python3 -c \"open('notes.txt','w').write('x')\"" mutates=false
```

`tools/capability.go` quotes redirects and treats `sh -c` as an opaque program.
The file's own comment acknowledges this ("a strong default, not a sandbox").
Mitigation: every FUZZ exec is APPROVE (medium risk), so a human sees the
command, unless `--yes` or a settings allow is set. Not fixed here: a partial
`sh -c` fix would be misleading, because interpreters (python, perl, node, and
others) cannot be enumerated. Real closure needs a sandbox or an exec
authority that does not depend on command text. That is a new capability and
a new hypothesis.

**L-2 — Completion is not verified.** No verification step exists. A run that
finishes cleanly with no open todos and no failed tool call is COMPLETED,
including when the todo list is empty. `Controller.Complete` checks only
`HasOpenTodos()`, so an empty list counts as done. The todo tool is TierRead
and the model can mark its own items DONE. D-4 closes the case where runtime
evidence (a failed tool call) contradicts completion. It does not close the
case of a clean stop with no evidence either way. Contract §31 is therefore
only partly met. Closing it needs a verification step, which is out of scope.

**L-3 — Intent vocabulary misses some mutation phrasings.** `Tidy up the
imports in main.go.` and `Make sure the server times out after 5s.` classify as
READ with ambiguous strength. In BUILD this routes the turn to ASK semantics, so
the requested change is not attempted in that turn. This is a silent
non-action, not a bypass: the user can rephrase or switch workspace. Extending
the verb list is a vocabulary change, not a defect fix, so it was not done.

**L-4 — No turn cap; deadline is optional** (see §9).

**L-5 — Narrowed gate is not restored after a turn.** `RouteTurn` narrows the
live gate and nothing restores it until the next routed turn. Every path that
runs the loop calls `RouteTurn` first, so no path observed in this pass runs
with a stale gate. The narrowing only ever lowers authority, so this is a
correctness note, not a bypass.

**L-6 — Repeated-task and recovery behaviour not measured.** No retry explosion
test was run. Tool-error results are returned to the model, which may retry.
The number of retries is not bounded by the runtime.

## 11. Explicitly not built

- Automated verification after mutation (run tests, check file state).
- A shell sandbox or per-interpreter classifier.
- A loop turn cap.
- References, call graph, semantic search, MCP, browser search.
- New index or context engine.
- Vocabulary expansion for intent.

## 12. Verification results

Run after all fixes and mutation checks, on the final tree:

| Check | Command | Result |
|---|---|---|
| gofmt | `gofmt -l .` | empty |
| build | `go build ./...` | pass |
| vet | `go vet ./...` | pass |
| test | `go test ./... -count=1` | pass, no failures |
| race | `go test -race ./... -count=1` | pass |
| integration / adversarial | probe matrix (§3, §4) | partial, see §15 |

## 13. Verdict

**HOLD.**

Reasons:

1. Four defects were found and fixed with regression tests. Each one was
   confirmed red when its fix was broken (§7).
2. Two contract gaps remain open and are not safe to freeze over:
   - **L-1** is a demonstrated FUZZ no-mutation bypass. It is mitigated by
     human approval, not closed.
   - **L-2** means "COMPLETED" still rests partly on the model's own work
     claims. §31 is only partly met.
3. The adversarial matrix is about half run (§4). Cancellation, long-loop
   behaviour, discovery edge cases, and resource measurements are unexercised.

This is not a FAIL. No property was shown to be violated that could not be
bounded within this phase. Moving to **PASS WITH LIMITATIONS** requires either
(a) accepting L-1 and L-2 as documented limitations after the remaining
matrix rows are run, or (b) a decision to fix them. Both are the user's call.

## 14. Freeze recommendation

Do not freeze yet. The minimal path to a freeze is:

1. Run the not-run rows in §4 (cancellation, long-loop, discovery edge cases,
   huge-line grep, partial mutation).
2. Decide L-1 and L-2 explicitly: accept as limitations, or fix.

## 15. Open items

- Matrix rows not run (§4).
- Settings `allow` rule interaction with project-local config (§8).
- Nested end-to-end delegation through a real provider (D-3 was checked only
  for the spawner's presence and that it does not fail with the nil-spawner
  error).
- Cancellation at each boundary and goroutine leak check.

## Process note

During the D-2 mutation check I ran `git checkout` on `agent/subagent.go`,
which also discarded the uncommitted fix in that file. The fix was reapplied
with the same comment and test, and the final diff was re-read and verified.
No other file was reverted. Every later check ran against the final tree.

---

# Final Closure Addendum

> **Date:** 2026-10-22
> **Baseline:** `41fe9b8` plus the Phase 3 working tree
> **Final verdict:** **PASS WITH LIMITATIONS**
> **Scope:** close L-1 through L-4, run the missing matrix, freeze.

The original HOLD above is preserved. This addendum records the follow-up pass
and supersedes only its verdict and open items.

## A1. What was found after the HOLD

The original four defects were re-read in the current source before changing
anything. Three further defects surfaced while closing the gaps:

| ID | Defect | Contract | Severity |
|---|---|---|---|
| D-5 | `newSubagentSpawner` built its own parent `Config` without `opts.deadline`. The D-2 inheritance fix was therefore vacuous in production: a child spawned through the CLI received a zero deadline even though the top-level loop had one. | The parent's deadline bounds the whole delegated tree. | High (resource bound bypass) |
| D-6 | `BashTool` foreground and background processes were not placed in their own process group. A timeout or cancellation killed the shell but could leave descendants running and holding the output pipes. | Cancellation must stop the process tree and not leak resources. | Medium (resource/cancellation) |
| D-7 | A delegated child's clean answer was returned to the parent as a normal success even when the child's own objective was unverified or unfulfilled. | Completion cannot be inherited; delegation is not a proof. | High (false success) |

D-7 is the delegation half of L-2. D-5 and D-6 were found by implementation and
adversarial testing, not by static suspicion.

## A2. L-1 — the authority boundary

**Design.** `security.Policy` sets `Decision.ReadOnly` for an execution
capability when the authority in force cannot write. The decision is not based
on the command text. `agent.Loop.execute` copies the bit to
`agent.Context.ReadOnly`; `BashTool` passes it to `sandbox.Command`. The
`sandbox` package wraps the child in an OS boundary:

- **darwin:** `sandbox-exec -p` with `(deny file-write* (subpath ROOT))`.
- **linux:** `bubblewrap` with a read-only `/`, writable temp/cache binds, and
  a final read-only bind of the workspace.
- **other platforms, or a missing sandbox:** the command is refused. There is
  no unguarded fallback.

Background jobs and human `!` escapes carry the same bit. The shell-escape
runner derives it from `securityGate.EvaluateCommand`, so a FUZZ/ASK escape is
sandboxed even though the human typed it.

**Tests.** `tools/bash_sandbox_test.go` executes 22 spellings under
`ReadOnly: true`: `>`, `>>`, `rm`, `mv`, `cp`, `sed -i`, `awk`, Python
`open`, Python `Path.write_text`, Python file creation, Python directory
creation, Perl, Ruby, `bash -c`, `sh -c`, `tee`, `dd`, `truncate`, `chmod`,
`touch`, `ln`, and `osascript`. Every protected file's
content, mode, and mtime stayed unchanged. A
read-only `cat` still succeeds. The same command with `ReadOnly: false` writes,
proving the boundary follows authority rather than disabling the shell. A
background job is covered too. `exec/exec_test.go` covers the human escape;
`cmd/ohgo/security_test.go` proves the gate marks FUZZ exec read-only and BUILD
exec not; `agent/authorize_test.go` proves the bit reaches the tool.

**Boundary caveat.** The OS boundary protects filesystem writes by the
command's process tree. An unrelated system service reached over IPC (for
example a GUI application opened by LaunchServices) is outside what
sandbox-exec can promise. That is an OS limitation, not a command-text
classifier. On Linux, read-only execution requires `bwrap`; without it FUZZ
shell execution is refused. No unapproved execution reaches the workspace.

## A3. L-2 — completion evidence

`agent.Result` now carries three evidence bits set by the tool, never by model
text:

- `Mutates`: a definite change (write/edit/delete, or a shell command the
  runtime classified as mutating).
- `Executes`: an arbitrary process started. In a mutation objective this is a
  *potential* change that must be verified, because the runtime cannot prove
  the process did not write.
- `Verifies`: a successful test/build/vet command (`tools.IsVerificationCommand`).

`agent.EvaluateCompletion` rules:

1. no turns, a turn error, or a final tool-calling turn → `NEEDS_REVIEW`;
2. an unrecovered tool failure → `NEEDS_REVIEW` (a later success of the same
   tool or a later verification recovers it);
3. a mutation objective with no definite observed change → `NEEDS_REVIEW`;
4. a change or potential change with no successful verification after it →
   `NEEDS_REVIEW`;
5. otherwise → `COMPLETED`.

The task lifecycle gained the non-terminal `NEEDS_REVIEW` state. `tui.runTurn`
maps the verdict to `Complete`, `NeedsReview`, `Pause` (cancellation or turn
limit), or `Fail` (real execution error). The model's stop, an empty todo list,
and the model's closing words are never sufficient.

**Tests.** `agent/completion_test.go` covers the eight required cases plus
verification-before-mutation, verification-after-the-last-change, recovered
retry, cancellation, outstanding tool calls, potential execution, and
read-only execution. `cmd/ohgo/completion_integration_test.go` runs the real
`write` and `bash` tools against a real temporary Go module: verified mutation
completes, unverified mutation is reviewable, and a failing `go test` is
reviewable. `tui/completion_test.go` covers the task transition mapping.
`task/controller_test.go` proves `NEEDS_REVIEW` persists and resumes without
becoming `COMPLETED`.

**Deliberate contract limit.** Verification proves the *defined check* passed
(tests/build/vet) and that a change was observed. It does not prove the change
is semantically the one the user imagined. A wrong change with a passing test
suite can still complete. That is a genuine limitation of evidence-based
completion without a semantic oracle.

## A4. L-3 — finite mutation intent

`workspace.RequestsMutation` is the completion objective's mutation test. It is
narrower than routing: "run the tests" is EXECUTE for routing but not a
mutation objective. `Classify` now recognises `tidy`, `ensure`,
`organize`/`organise`, `make sure`, `tidy up`, and `clean up`, including a
mutation phrase in a later clause after a read opener. Informational forms
("explain how to make sure", "what does tidy up mean", "how do I fix this")
stay READ.

`workspace/intent_test.go` covers the representative forms, compound forms, and
negative forms. The classifier remains deterministic and finite; no NLP
subsystem was added.

## A5. L-4 — runaway behaviour and the bound

A scripted model that always proposes a tool call was run with no deadline. It
did not terminate on its own, confirming the theoretical concern. The smallest
bound was added: `agent.Config.MaxTurns` (default `DefaultMaxTurns = 100`,
CLI `--max-turns`). Reaching the cap returns the completed turns plus
`ErrTurnLimit`, so the work is resumable. The cap is inherited by subagents.
`ErrTurnLimit` maps to `PAUSE("turn limit reached")`, not failure.

**Measurements.** `agent/turnlimit_test.go`:

- 5, 10, 20, and 50 tool-turn workloads all complete within the default;
- an always-calling model stops at the configured cap (5 and 20 tested);
- repeated identical calls stop at the cap;
- repeated tool failures stop at the cap;
- a subagent inherits the parent's cap.

The deadline remains the wall-clock bound. The two limits are independent:
the cap bounds provider calls, the deadline bounds elapsed time.

## A6. Complete adversarial matrix

Every row below was exercised in this pass. "Test" means a reproducible Go
test; "probe" means a measurement command whose output is recorded here.

| Scenario | Result | Evidence |
|---|---|---|
| read-only inspection | no mutation | `TestReadOnlyBashStillReads`; read/grep/symbols tests |
| explicit file creation | authorized mutation | `TestFileToolsReportMutationEvidence`; write tool tests |
| explicit file modification | authorized mutation | `TestFileToolsReportMutationEvidence`; edit tool tests |
| explicit deletion | authorized mutation | `tools/delete_test.go` |
| mixed read + mutation | mutation preserved | `TestClassifyKeepsMutationAfterSequencedReadClause` |
| ambiguous mutation wording | safe handling | `TestClassifyFallsBackToTheLowerAuthority` |
| compound mutation wording | mutation preserved | `TestClassifyPreservesCompoundMutationVocabulary` |
| exact symbol | bounded discovery | `TestSymbolsToolFindsExactCoordinate` |
| ambiguous symbol | ambiguity preserved | `TestSymbolsToolReportsAmbiguous` |
| negative symbol | truthful negative | `TestSymbolsToolReportsNotFound` |
| text literal | lexical discovery | `TestStructuralAndLexicalDiscoveryCoverDifferentEvidence` |
| malformed source | truthful partial state | `TestIndexSurvivesMalformedSource`, `TestSymbolsToolReportsParseFailures` |
| huge lexical line | no silent truncation | `TestGrepReachesMatchesPastALongLine` |
| oversized file | no silent partial result | `TestGrepSaysWhenAFileExceedsTheSearchWindow` |
| timeout | not a complete negative | `TestGrepReportsTimeoutInsteadOfAnEmptyAnswer`, `TestLoopDeadlineIsDistinctFromContextCancellation` |
| tool error | not completed | `TestEvaluateCompletionRejectsUnrecoveredToolFailure` |
| model false completion | not completed | `TestTurnTaskOutcomeRejectsFailedToolCall` |
| empty TODO list | not sufficient | `TestEvaluateCompletionRejectsMissingMutation`; `TestControllerCompletionRequiresAllWorkDone` |
| mutation without verification | not falsely completed | `TestEvaluateCompletionRejectsMutationWithoutVerification` |
| failed verification | not completed | `TestCompletionIntegrationRejectsFailedVerification` |
| successful verification | completion allowed | `TestCompletionIntegrationAcceptsVerifiedMutation` |
| cancellation | safe non-complete state | cancellation suite below |
| repeated tool failure | bounded | `TestRepeatedToolFailureIsBounded` |
| repeated identical action | bounded | `TestRepeatedIdenticalCallsAreBounded` |
| nested delegation | authority preserved | `TestNestedDelegationRunsThreeLevels` |
| nested deadline | propagated | `TestSpawnerPropagatesTheParentDeadline`, `TestRunSubagentHonoursParentDeadline` |
| depth limit | enforced | `TestChildToolSetBoundsTheTreeStructurally` |
| large repository | bounded lifecycle | `TestIndexStress` probe below |
| malformed model output | observable failure | `TestInvalidToolArgumentsAreAnObservableFailure` |

### Cancellation

Exercised before discovery, after discovery, before the model call, before tool
execution, during tool execution, after mutation, and during verification:

- `agent/cancellation_test.go` — cancellation before and during tool execution;
- `agent/loop_test.go` — context cancellation stops the loop;
- `agent/deadline_test.go` — deadline and cancellation are distinct;
- `agent/subagent_test.go` — child cancellation propagates;
- `tools` — read, glob, grep, web_fetch, symbols, wait, and task tools honour
  cancellation;
- `exec` — the shell escape aborts promptly;
- `tools/bash_sandbox_test.go` — a timed-out shell's process group is dead;
- completion — a cancelled tool is an error outcome and a cancelled run is
  `NEEDS_REVIEW`/`PAUSE`, never `COMPLETED`.

### Discovery edge cases

`ast` and `tools` tests cover exact, ambiguous, negative, truncated, modified,
same-size rewrite, deleted, newly created, malformed, unreadable, non-Go,
symlinked root, symlink escape, deterministic output, concurrent queries,
long lexical lines, oversized files, and timeout-as-partial. Structural and
lexical discovery were exercised together on the same fixture.

### Large repository

`OHGO_INDEX_STRESS=2000 go test ./ast -run TestIndexStress -v`:

```text
files=2000 symbols=62000 cold_build=181.998625ms heap_retained=10.3MiB
warm_lookup_avg=8.044766ms freshness_walk_avg=7.579066ms
rebuild_after_one_edit=172.4835ms
rebuild_after_delete=213.519917ms symbols_now=61969
```

This is a synthetic repository, not a production-scale claim. The index is one
in-process coordinate store; there is no second index. The index build itself
is not context-cancellable (Phase 1 is frozen); cancellation before and after a
lookup is checked, and a cancelled tool returns an error rather than a partial
negative.

### Nested delegation

`TestNestedDelegationRunsThreeLevels` drives the real Anthropic SSE transport
(an `httptest` server, not a live external API) through three levels:

```text
parent requests = 2
level-one requests = 2
level-two requests = 1
```

The grandchild's answer reached the child, and the child's answer reached the
parent. `TestChildInheritsFuzzAuthority` proves a child in FUZZ cannot write
through the inherited gate. `TestSpawnerPropagatesTheParentDeadline` proves the
production spawner copies the deadline. `TestChildToolSetBoundsTheTreeStructurally`
proves the depth cap is structural. A child that mutates without verifying is
returned to the parent as a reviewable error (`TestTaskToolReportsNeedsReviewAsAnError`).

### Malformed model output

`TestInvalidToolArgumentsAreAnObservableFailure` sends arguments that violate
the tool schema. The loop records an error outcome, does not execute the tool,
and lets the model see the failure on the next turn.

## A7. Fix-discipline mutation checks

Each fix was broken intentionally and its regression test confirmed red:

| Fix | Broken change | Observed failure |
|---|---|---|
| L-1 sandbox | `ReadOnly: ctx.ReadOnly` → `false` in `BashTool` | `read-only execution mutated the workspace: file.txt = "hacked\n"` |
| L-1 propagation | `ReadOnly` dropped from `agent.Context` | `tool read-only modes = [false], want [true]` |
| L-2 verification | verification check removed from `EvaluateCompletion` | `a mutation with no verification was reported complete` |
| L-2 failure | pending-failure check removed | `a failed tool call followed by a clean stop was reported complete` |
| L-3 vocabulary | `tidy`/`ensure`/`make sure` removed | `Classify("Tidy up the imports.") = read, want execute` |
| L-4 turn cap | cap check removed | test timed out after 5s (the runaway) |
| D-2 spawner deadline | `Deadline` removed from the spawner's parent config | `spawn err = <nil>, want ErrNoSubagentResult` (child ran) |
| D-3 nested spawner | spawner replaced with `nil` | `the nested delegation was refused without a spawner` |

The files were restored after each check; the final tree is the fixed tree.

## A8. Final verification

On the final tree:

| Check | Command | Result |
|---|---|---|
| gofmt | `gofmt -l .` | empty |
| build | `go build ./...` | pass |
| vet | `go vet ./...` | pass |
| test | `go test ./... -count=1` | pass, all packages |
| race | `go test -race ./... -count=1` | pass, all packages |
| cross-build | `GOOS=linux`, `GOOS=windows`, `GOOS=darwin` | pass |
| integration | `cmd/ohgo` completion and nested-delegation tests | pass |
| adversarial | the matrix in §A6 | pass |
| lifecycle | `OHGO_INDEX_STRESS=2000` | measurements in §A6 |

## A9. Remaining limitations

1. **Semantic verification.** Completion proves an observed change and a
   successful defined verification. It does not prove the change matches the
   user's intent. A wrong change with a passing test suite can complete.
2. **Platform support for L-1.** darwin uses `sandbox-exec` (deprecated by
   Apple); Linux uses `bubblewrap`; every other platform, or a missing sandbox,
   refuses read-only execution. The Linux path is compile-checked and
   fail-closed but was not executed on this host.
3. **IPC-mediated mutation.** The OS sandbox governs the command's process
   tree, not an unrelated system service reached over IPC.
4. **Index build cancellation.** A large `ast.Index` build cannot be
   interrupted mid-parse; the tool checks cancellation before and after.
5. **Read-only subagent flag.** `TaskTool` advertises `readOnly`, but the
   production spawner passes the full tool set and `RunSubagent` refuses a
   read-only request against a writable set. The refusal is observable; the
   flag is not a working restriction.
6. **Turn budget scope.** The cap is per run, not a shared tree-wide token
   budget. Depth and turn caps bound the tree; a child gets its own turn
   budget.
7. **No retry policy.** Failures are returned to the model; the turn cap
   bounds retries but there is no per-tool retry counter.
8. **NEEDS_REVIEW has no accept action.** The user resolves it by sending a
   later turn; there is no one-key "accept as reviewed" control.
9. **Live provider not used.** Nested delegation was exercised through the real
   provider transport with a scripted SSE model, not a live external API call.
10. **Synthetic scale.** The 2,000-file/62,000-symbol measurement is synthetic
    and does not establish a production-scale claim.

## A10. Explicitly not built

- references, callers/callees, call graphs, import graphs;
- semantic retrieval or a second repository index;
- MCP or browser search;
- a universal verification framework;
- a general container/sandbox runtime beyond the minimal read-only boundary;
- a new orchestration layer;
- changes to the Phase 2 benchmark or its evidence.

## A11. Final verdict

**PASS WITH LIMITATIONS.**

The four blockers are closed:

- L-1: an unapproved FUZZ execution cannot mutate the protected workspace; the
  boundary is authority-derived and OS-enforced, not a command blacklist.
- L-2: completion requires observed mutation and verification evidence; model
  stop, empty todos, and model language are insufficient; unverified work is
  `NEEDS_REVIEW`.
- L-3: the defined mutation vocabulary and compound forms are preserved;
  ambiguous language stays READ.
- L-4: a runaway model was demonstrated and is bounded by an inherited turn
  cap.

The remaining limitations are explicit, bounded, and do not reopen the
authority or false-completion contracts. The integration cycle can freeze.

## A12. Freeze recommendation

Freeze this integration cycle at the final tree. Any future capability —
semantic verification, read-only subagent filtering, a shared tree budget, or a
different sandbox backend — starts as a separate engineering cycle with its own
hypothesis and benchmark.

