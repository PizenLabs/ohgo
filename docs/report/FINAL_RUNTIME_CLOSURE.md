# Final Runtime Closure

> **Date:** 2026-10-22
> **Verdict:** **HOLD** — not ready to freeze.
> **Detail:** [`PHASE3_CLOSURE.md`](PHASE3_CLOSURE.md)

## What is integrated

- Intent routing (`workspace.RouteTurn`) that narrows authority per turn and
  refuses requests the active workspace may not answer, in the REPL, one-shot,
  and TUI paths.
- A single admission boundary (`agent.Loop` → `Authorize`): evaluate, then
  execute only on AUTO, or obtain a one-shot approval grant for APPROVE, and
  refuse DENY.
- Deterministic policy (`security.Policy`) over a workspace permission table
  (`workspace.Permits`).
- Subagents that inherit the same gate, deadline, and depth bound.
- Durable task continuity with a lifecycle state machine.

## What is proven

Each of these was shown by a test that fails when the fix is removed:

- A clause after `then` is classified as a mutation (fixed; was READ).
- A subagent honours the parent's wall-clock deadline (fixed; child made a
  provider call after expiry).
- A depth-1 child can delegate (fixed; nested delegation always failed).
- A run with a failed tool call is never recorded COMPLETED (fixed).

Also shown by probe: ASK and FUZZ deny writes at the workspace table, and
refused routes never call the model.

Full module passes `gofmt`, `build`, `vet`, `test`, and `test -race`.

## What remains limited

- **FUZZ no-mutation is bypassable through nested shells and interpreters**
  (`bash -c '...> f'`, `python3 -c "open(...)"`). Mitigated only by human
  approval on every exec, which `--yes` removes.
- **Completion is not verified.** A clean stop with no open todos — including
  an empty todo list — is COMPLETED. No automated check runs after a mutation.
- **Intent vocabulary misses some mutation phrasings** ("tidy up", "make sure"),
  which route to READ and so silently skip the change in that turn.
- **No turn cap.** Without `--deadline` a tool-calling loop runs until
  interrupted.
- **Adversarial matrix about half run.** Cancellation, long-loop behaviour,
  discovery edge cases, and resource measurements are not yet exercised.

## What was deliberately not built

- Automated verification after mutation.
- A shell sandbox or per-interpreter classifier.
- A loop turn cap.
- References, call graph, semantic search, MCP, browser search.
- A new index or context engine.
- Intent vocabulary expansion.

These are new capabilities. Each needs its own hypothesis and benchmark cycle.

## Why the runtime is not yet ready to freeze

The authority boundary is structurally sound, and four defects were fixed. Two
contract gaps are still open and would be frozen as known behaviour:

1. FUZZ can mutate the repository through a nested shell, which the contract
   forbids.
2. "COMPLETED" can be reached without any evidence beyond the model's own
   stop and the todo list.

Freezing now would mean accepting both as documented limitations. The
remaining matrix rows should run first, so that the limitations are known from
evidence rather than from reading code. Then the user chooses between accepting
L-1 and L-2 and fixing them. Moving to **PASS WITH LIMITATIONS** depends on that
choice.

---

# Final Closure Addendum

> **Date:** 2026-10-22
> **Final verdict:** **PASS WITH LIMITATIONS**
> **Detail:** [`PHASE3_CLOSURE.md`](PHASE3_CLOSURE.md), §A1–§A12.

The HOLD above is preserved. This addendum records the follow-up pass that
closed L-1 through L-4, found three further defects, ran the missing matrix,
and froze the cycle.

## What changed

- **L-1 — authority.** The security decision carries a `ReadOnly` bit derived
  from the authority, not from command text. The bash tool, background jobs,
  and human `!` escapes enforce it with an OS boundary (sandbox-exec on darwin,
  bubblewrap on Linux, fail-closed elsewhere). The 22-spelling mutation suite
  leaves the protected workspace unchanged.
- **L-2 — completion.** `agent.EvaluateCompletion` derives completion from
  observed mutation, arbitrary execution, verification, and unrecovered
  failure. Model stop, empty todos, and model language are insufficient. The
  task lifecycle gained the non-terminal `NEEDS_REVIEW` state. Delegated
  children carry their verdict and evidence to the parent.
- **L-3 — intent.** `workspace.RequestsMutation` is a finite, clause-aware
  mutation vocabulary. `tidy`, `ensure`, `make sure`, and compound forms are
  recognised; informational forms remain READ.
- **L-4 — boundedness.** A runaway model was demonstrated with no deadline. A
  turn cap (default 100, `--max-turns`, inherited by subagents) now bounds
  every run; `ErrTurnLimit` pauses the task with the completed turns intact.
- **D-5.** The production subagent spawner did not copy `opts.deadline` into
  its own parent config, making the D-2 inheritance fix vacuous in production.
- **D-6.** Bash foreground/background processes were not process-grouped, so a
  timeout could leave descendants running. Process-group handling is now
  centralised in `sandbox`.
- **D-7.** A child's clean answer could be inherited as success even when the
  child's objective was unverified. The task tool now returns a reviewable
  error and propagates mutation/verification evidence.

## What is proven

- An unapproved FUZZ execution cannot mutate the protected workspace,
  regardless of command spelling (redirects, `rm`, `mv`, `cp`, `sed -i`,
  Python, Perl, Ruby, nested shells, `tee`, `dd`, `truncate`, `ln`, `osascript`).
- A mutation objective with no observed change, no later verification, an
  unrecovered failure, or a cancellation is never `COMPLETED`.
- A verified mutation can complete; a failed verification cannot.
- The mutation vocabulary and compound forms are preserved; ambiguous
  language stays READ.
- A runaway model is bounded; 5/10/20/50-turn workloads complete within the
  default cap.
- Nested delegation runs three levels through the real provider transport,
  inherits FUZZ authority and the parent deadline, enforces the depth cap, and
  cannot inherit false completion.
- Discovery edge cases, cancellation boundaries, malformed model output, and a
  2,000-file/62,000-symbol lifecycle were exercised.

## Final verification

```text
gofmt -l .                     empty
go build ./...                 pass
go vet ./...                   pass
go test ./... -count=1         pass
go test -race ./... -count=1   pass
GOOS=linux/windows/darwin      pass
```

## What remains limited

- Verification proves the defined check passed and a change was observed; it
  does not prove semantic intent. A wrong change with passing tests can
  complete.
- darwin uses deprecated `sandbox-exec`; Linux requires `bubblewrap`; other
  platforms fail closed. The Linux runtime path was compile-checked, not run on
  this host.
- The OS sandbox governs the command's process tree, not IPC-mediated writes by
  an unrelated system service.
- The AST index build is not context-cancellable mid-parse.
- The `task` tool's `readOnly` flag is not a working restriction against the
  production full tool set; it fails observably.
- The turn cap is per run, not a shared tree-wide budget.
- Nested delegation used a scripted SSE model over the real transport, not a
  live external API.
- The large-repository measurement is synthetic.

## What was deliberately not built

References, callers/callees, call graphs, semantic retrieval, a second index,
MCP, a universal verification framework, a general container runtime, a new
orchestration layer, and any change to the Phase 2 benchmark.

## Freeze

The authority and false-completion contracts are closed. The remaining
limitations are explicit and bounded. Freeze this integration cycle at the
final tree. Future capability work starts a new cycle with its own hypothesis
and benchmark.

