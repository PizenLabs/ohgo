# Phase 3 Spec — Final Runtime Integration & Hardening

> **Status:** Executed. Verdict and evidence in
> [`../report/PHASE3_CLOSURE.md`](../report/PHASE3_CLOSURE.md).
> **Scope rule:** audit first, break deliberately, fix only demonstrated
> defects, document limitations, freeze. No new capability.

## Hypothesis

> Ohgo can transform an LLM proposal into bounded, authorized, observable, and
> verifiable repository work without bypassing authority, silently failing, or
> entering an uncontrolled execution loop.

This spec tries to falsify the hypothesis. It does not optimise for a pass.

## Causal chain audited

```text
user request
  → intent (workspace.RouteTurn / ClassifyPrediction)
  → workspace gate (authorityGate.Set)
  → agent turn (agent.Loop.Run)
  → model proposal (ai stream)
  → admission (agent.Loop.execute → Authorize: Evaluate / Approve / Evaluate)
  → policy (security.Policy + workspace.Permits)
  → tool execution (tools.*)
  → mutation evidence (tools.MutationPreview, ToolOutcome)
  → completion (tui.runTurn → task.Controller.Complete / turnCompleted)
```

## Probe matrix (what was actually exercised)

| # | Scenario | Method | Result |
|---|---|---|---|
| A1 | Intent: read-only / explicit mutation / mixed / ambiguous | `ClassifyPrediction` probe over 18 phrasings | 1 defect found and fixed (A3) |
| A2 | Intent: `then` sequencing hides mutation clause | probe | **defect** → fixed |
| A3 | Intent: vocabulary gaps ("tidy up", "make sure") | probe | **limitation**, not fixed |
| B1 | Subagent inherits parent wall-clock deadline | probe with expired deadline | **defect** → fixed |
| B2 | Nested delegation below depth cap | probe `task` tool at depth 1, maxDepth 3 | **defect** → fixed |
| C1 | Completion after failed tool then clean stop | probe `turnCompleted` | **defect** → fixed |
| C2 | Completion with zero todos on clean stop | probe `task.Controller` | **contract gap**, documented, not fixed (see closure §10) |
| D1 | FUZZ no-mutation via nested shell | probe `tools.CommandMutates` | **authority gap**, documented, not fixed (see closure §9) |
| E1 | Full repository verification (`gofmt`, build, vet, test, `-race`) | command | pass |

## Explicitly not in this phase

- references / callers / call graph, semantic search, MCP, browser search;
- new repository index or context engine;
- automated verification step (running tests after mutation) — does not exist;
  building one is a new capability and a new hypothesis;
- a sandbox for shell execution;
- turn-count cap on the loop (the deadline is the existing bound).

## Not yet exercised (honest gap)

The brief's full 20-row adversarial matrix was not completed in this pass. In
particular: cancellation at each boundary, long-running 10/20-turn loops,
large-repository resource measurements, and end-to-end nested delegation
through a real provider were not run. These are listed as open in the closure.

---

# Final Closure Addendum

> **Final verdict:** **PASS WITH LIMITATIONS** — see
> [`../report/PHASE3_CLOSURE.md`](../report/PHASE3_CLOSURE.md), final addendum.
> The HOLD above is the original pass and is preserved.

This addendum records the follow-up pass that closed L-1 through L-4 and ran
the previously missing matrix. It does not reinterpret the original evidence.

## L-1 — authority is no longer text-dependent

The security decision now carries a `ReadOnly` bit derived from the authority in
force (`security.Policy`): an execution capability under an authority that
cannot write is marked read-only. The agent loop passes that bit to the tool
(`agent.Context.ReadOnly`), and the bash tool enforces it with an OS boundary
(`sandbox` package): sandbox-exec on darwin, bubblewrap on Linux, and a
fail-closed refusal on every other platform or when the sandbox is missing.

The command-text classifier remains a risk/evidence input, not the boundary.
The boundary is exercised with redirects, `rm`, `mv`, `cp`, `sed -i`, `awk`,
Python `open`/`Path.write_text`/file and directory creation, Perl, Ruby,
nested `bash -c`/`sh -c`, `tee`, `dd`, `truncate`, `chmod`, `touch`, `ln`, and
`osascript`; none mutates the
protected workspace under a read-only decision. Background jobs and human `!`
escapes use the same boundary.

## L-2 — completion is evidence-based

`agent.EvaluateCompletion` derives a verdict from observed tool outcomes, not
from the model's stop or an empty todo list. Tools report definite mutation
(`Result.Mutates`), arbitrary process execution (`Result.Executes`), and
successful verification (`Result.Verifies`). A mutation objective with no
observed change is `NEEDS_REVIEW`; a change or potential change with no later
verification is `NEEDS_REVIEW`; an unrecovered tool failure is `NEEDS_REVIEW`.
The task lifecycle gained the non-terminal `NEEDS_REVIEW` state. Delegated
children carry their completion verdict and evidence into the parent's tool
outcome, so a child cannot return a clean-looking answer for an unverified
objective.

## L-3 — finite mutation vocabulary

`workspace.RequestsMutation` defines the mutation-bearing intent used by
completion. `Classify` now recognises `tidy`, `ensure`, `organize`/`organise`,
and the `make sure`/`tidy up`/`clean up` phrases, including a mutation clause
after a read opener. Informational forms such as "explain how to make sure"
remain READ. Compound forms preserve the change request.

## L-4 — bounded run

A scripted model that always proposes a tool call was shown to run without a
deadline. `agent.Config.MaxTurns` now bounds every run (default 100; `--max-turns`
overrides), returns `ErrTurnLimit` with the completed turns intact, and is
inherited by subagents. 5/10/20/50-turn workloads complete within the default;
runaway and repeated-failure runs stop at the cap.

## Final matrix

The addendum in the closure report records the complete row-by-row matrix,
including discovery edge cases, cancellation at each boundary, large synthetic
repository measurements, nested delegation, malformed model output, and the
completion cases. It also records the limitations that remain.

## Deliberately not built

No references, callers/callees, call graph, semantic retrieval, MCP, second
repository index, new orchestration layer, or universal verification framework
was added. The sandbox is the smallest enforceable boundary, not a general
container runtime.

