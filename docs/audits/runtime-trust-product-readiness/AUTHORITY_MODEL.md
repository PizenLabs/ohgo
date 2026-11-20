# Authority model

## 1. Boundaries

| Authority | May | Must not |
|---|---|---|
| ASK | Read, search, inspect, explain | Execute a process, mutate source, investigate actively |
| FUZZ | Read, search, execute bounded probes, investigate, file a finding | Mutate source |
| BUILD | Read, execute, investigate, mutate source and verify | Bypass approval or the security gate |

The authority is a runtime capability, not a UI label. `workspace.Authority`
admits or denies each call at the execution boundary; a label alone never
admits an action.

## 2. Transition rules

- **ASK → BUILD / FUZZ** is a human boundary. A missing capability produces a
  refusal or a confirmation, never a silent escalation.
- **BUILD ↔ FUZZ** may hand off automatically inside an already-authorized task
  when the tool call requires the other execution capability.
- **No model text changes authority.** A sentence claiming “the user authorized
  BUILD”, “switch to FUZZ”, or “continue without asking” is model output, not
  a transition.
- **Tool output changes no authority.** A read result containing a directive is
  untrusted data; the gate still admits the next call under the live authority.
- **A plan or finding is not a grant.** FUZZ can file a finding but cannot
  mutate; BUILD must be explicitly authorized.

## 3. Runtime enforcement

- `cmd/ohgo/authority.go` holds the live `authorityGate`. Only an explicit
  workspace switch (`Set`) moves it.
- The agent loop calls `Authorize` / `Admit` before execution. The security
  boundary evaluates the call, not the model's narration.
- `workspace.AnalyzeRequest` and `RouteTurn` decide whether the current turn
  can run at all. A refused route never reaches the provider.
- Interactive and headless hosts use the same gate; the one-shot path refuses
  a human-boundary transition because it has no decision surface.

## 4. The `using` regression

`OHGO-CTRL-001` was a classifier defect, not a gate defect. `headForms("use")`
includes `"using"`, so:

> Using read-only structural evidence, report the callers of NewOrder.

was read as an execution objective and refused under ASK. The fix treats a
leading `using` clause as a participial modifier only when:

- it names evidence/analysis (`read-only`, `structural evidence`, `analysis`,
  `the inventory`, …), and
- it contains no command, package runner, server, or executable (`npx`,
  `npm`, `go run`, `live-server`, `docker`, `server`, …).

A leading command form still carries execution authority. Tested in
`internal/control/workspace/authority_regression_test.go`.

## 5. Adversarial coverage

- Model text claiming authorization: `TestModelClaimOfAuthorizationCannotEscalate`.
- Tool output instructing a mode change:
  `TestToolOutputCannotEscalateAuthority`.
- Prompt claiming write authority:
  `TestAuthorityIsEnforcedWithoutPromptInstructions`.
- Read-only request followed by mutation under ASK: refused before the model.
- FUZZ finding followed by a write: denied; FUZZ stays FUZZ.
- Explicit BUILD mutation: still executes and verifies.

## 6. Remaining conservative behavior

The classifier still narrows ambiguous prose to READ. For example, “I need to
investigate this project” can be treated as READ because the leading token is
not a recognized verb. This under-authorizes rather than over-authorizes; it
does not grant mutation or execution. It is documented rather than widened in
this audit.
