# Proposal Permission Reset — Request-Derived Decisions on the Original Permission Surface

## 1. Verdict

**PASS**

The recent execution-strategy proposal work was reset. The strategy decision no
longer uses the centered transition picker. It is rendered by the original
Proposal Permission surface — the same bordered, amber permission dialog used
for tool approvals, in the chat/prompt area, with Up/Down navigation, Enter
confirmation, and Esc cancellation.

The former fixed question:

```text
How should this be structured?

[ Single file ]
[ Multiple files ]
[ Let the model decide ]
```

is gone. The new question is constructed from the user's actual request and the
decision that request leaves unresolved. There is no project-type trigger, no
website/Go/Python/Rust branch, and no questionnaire registry.

Verification tree: branch `fix/permission`, working-tree changes described here.
`gofmt -l .` is empty; `go build ./...`, `go vet ./...`, `go test ./...`, and
`go test -race ./...` pass. The codebase-memory index was stale
(`indexed_at 2026-10-11`) and did not track the new files, so this report is
source- and test-based; the graph was not used as evidence.

The reverted implementation is preserved only as git stash `stash@{0}`
(`recent-strategy-work-audit-backup`). It is not in the working tree.

---

## 2. What was reset

The recent implementation added strategy proposals to the pre-existing
workspace-transition picker:

- `workspace/strategy.go` — fixed `StrategyOptions`, fixed
  `Question() -> "How should this be structured?"`, and the
  empty-workspace/mutation trigger.
- `tui/model.go` — `TransitionKindStrategy`, strategy `Kind`/`Options` on
  `TransitionPrompt`, strategy `Move`.
- `tui/view.go` — `transitionOptions` / `transitionOptionHint`, a vertical
  strategy option list inside the centered `transitionCard`.
- `tui/app.go` — `handleStrategyKey`, `resolveStrategy`,
  `strategySystemBlock`, `strategyEvidence`.
- `prompt/prompt.go` — `StrategyDecision`.
- `cmd/ohgo/strategy_test.go`, `tui/strategy_test.go`,
  `workspace/strategy_test.go`.

All of that was removed or replaced. The pre-existing workspace-authority
transition (`TransitionPrompt` / `transitionCard`) remains unchanged and is not
used by the new proposal flow. That card is older authority-move behavior,
outside this task's scope; the strategy proposal never sets
`State.Transition`.

---

## 3. Original Proposal Permission

### 3.1 State owner

`tui.State.Pending *PendingApproval` (`tui/model.go:189`) owns the decision.
The proposal-specific payload is:

```go
// tui/model.go:217
Proposal *workspace.Proposal
```

The existing events own the lifecycle:

- `ApprovalAsked` (`tui/model.go:2427`) opens the prompt.
- `ApprovalMoved` shifts `PendingApproval.Cursor`.
- `ApprovalAnswered` closes it.

No new permission event type, modal, picker, or renderer was introduced.

### 3.2 Renderer

`renderApproval` (`tui/approval.go:333`) renders the dialog through the same
plan used by tool approvals:

- `approvalPlan` (`tui/approval.go:139`)
- `approvalBoxRows` (`tui/approval.go:255`)
- `renderApprovalRow` (`tui/approval.go:398`)
- `approvalTitleBorder`, `approvalFrame`, `approvalBottomBorder`

For a proposal, only the decision data changes:

- legend: `Proposal Permission` (`tui/approval.go:38`)
- question: `pending.Proposal.Question` (`tui/approval.go:223`)
- key line: `↑↓ move · enter confirm · esc cancel`
  (`tui/approval.go:42`)
- choices: the proposal's option labels
- frame accent: `ColorMutedAmber` / `ColorStatus` = `#C9A86A`
  (`tui/style.go:99–103`, `tui/approval.go:90`)

The proposal is submitted with `Mutating: false` (`tui/app.go:3458`), so it
uses the amber (yellow/orange) permission accent rather than the rose
mutation accent.

### 3.3 Input owner

`Program.handleKey` checks `p.state.Pending != nil`
(`tui/app.go:1007`) and routes proposals through `handleProposalKey`
(`tui/app.go:3321`):

| Key | Action |
| --- | --- |
| `up` / `k` / `shift+tab` / `left` / `h` | `ApprovalMoved{Delta:-1}` |
| `down` / `j` / `tab` / `right` / `l` | `ApprovalMoved{Delta:1}` |
| `enter` | `resolveProposal` (`tui/app.go:3344`) |
| `esc` / `ctrl+c` | `ApprovalAnswered{}`; no turn starts |

Tool approvals keep their original key branch and semantics; they are not routed
through `handleProposalKey`.

### 3.4 Chat/prompt integration

`renderState` (`tui/view.go:214`) calls `renderApproval`
(`tui/view.go:258`) immediately after the transcript area and before the
thinking/status/composer rows. The dialog is emitted inline, at the left edge;
it is not `lipgloss.Place`-centered. Live/output-first mode uses the same
renderer through `liveApprovalLines` (`tui/live.go:681`).

### 3.5 Evidence that this is the original permission surface

Existing tests continue to pass unchanged:

- `TestApprovalBoxDrawsBorderedLegendAndChoices`
- `TestApprovalBorderAccentFollowsBlastRadius`
- `TestApprovalCursorMarksOnlyTheDefaultRow`
- `TestApprovalNavigationStopsAtTheEndInsteadOfWrapping`
- `TestCtrlCDuringApprovalDeniesAndCancels`
- `TestApprovalDefaultsToTheSafestChoice`

New proposal tests pin the same contract:

- `TestProposalUsesTheOriginalPermissionSurface` — same box, amber accent,
  `❯` cursor, numbered choices, inline location, no transition-card arrows.
- `TestProposalSurfaceMatchesToolApproval` — tool approval and proposal share
  the rounded frame, side borders, cursor, and inline placement.
- `TestProposalNavigationUsesUpDownEnterEsc` — clamped Up/Down, Enter confirm,
  Esc cancel.
- `TestProposalQuestionVariesAtTheUISurface` — different questions render
  through the same surface.

---

## 4. UI integration

### 4.1 Connected path

```text
cmd/ohgo.turnTransition                 cmd/ohgo/run.go:1091
  ├─ workspace.ProposeTransition        workspace authority moves (unchanged)
  └─ workspace.AssessProposal           workspace/proposal.go:118
        ↓
tui.TurnTransition{                     tui/app.go:411
    Kind: TransitionKindProposal,
    Proposal: *workspace.Proposal,
}
        ↓
Program.submit                          tui/app.go:3413
  → ApprovalAsked{Proposal: ...}        tui/app.go:3444–3460
        ↓
State.Pending *PendingApproval          tui/model.go:189
  → PendingApproval.Proposal            tui/model.go:217
        ↓
renderApproval                          tui/approval.go:333
  via renderState                       tui/view.go:258
  via liveApprovalLines                 tui/live.go:681
        ↓
handleProposalKey                       tui/app.go:3321
  → resolveProposal                     tui/app.go:3344
        ↓
ProposalResolution                      tui/app.go:442
  { Option, Label, Directive, Dimension, Subject, Question, Request }
        ↓
proposalSystemBlock                     tui/app.go:3396
  → prompt.ProposalDecision             prompt/prompt.go:94
        ↓
execution
```

### 4.2 Proof that no center picker remains for proposals

- `TestUserProposalBlocksTheTurnUntilDecided` asserts
  `p.state.Pending.IsProposal()` and `p.state.Transition == nil`.
- `TestProposalUsesTheOriginalPermissionSurface` asserts:
  - the top border line starts at column 0;
  - the rendered output contains no `◀` / `▶` transition arrows;
  - the rendered output contains no transition-card key line;
  - the permission footer and `❯` cursor are present.
- A repository search for `transitionOptions`, `transitionOptionHint`,
  `TransitionKindStrategy`, `handleStrategyKey`, `resolveStrategy`,
  `StrategyOptions`, `AssessStrategy`, `StrategyFacts`, and
  `StrategyResolution` returns no matches.

### 4.3 Proof that no second proposal permission surface exists

The proposal and a tool approval are the same type and the same renderer:

- same `PendingApproval` struct;
- same `ApprovalAsked` event;
- same `renderApproval` function;
- same `approvalLines` / `approvalBoxRows` accounting;
- same `❯` cursor and active-row treatment.

No new event, modal, picker, renderer, or permission system was added.

The pre-existing workspace-authority transition card remains for authority
moves. It is not part of the strategy flow and was not rewritten; the proposal
never sets `State.Transition`.

---

## 5. Question-origin audit

> Is `"How should this be structured?"` a hard-coded template/heuristic, or is
> it derived from the actual user request and unresolved decision?

**It was a hard-coded template. It has been deleted. The new question is
request-derived.**

### 5.1 Exact code path

```text
workspace.AssessProposal                        workspace/proposal.go:118
  ├─ RequestsMutation / BuildIntentContract     authority + mutation gate
  ├─ alternativeProposal                         workspace/proposal.go:197
  │    ├─ splitAlternatives                      workspace/proposal.go:243
  │    └─ alternativeSubject                     workspace/proposal.go:330
  ├─ declaresStructure                           workspace/proposal.go:567
  └─ structureProposal                           workspace/proposal.go:154
       ├─ requestObject                          workspace/proposal.go:373
       ├─ concreteSubject                        workspace/proposal.go:489
       └─ Question = `How should I organize "<subject>"?`
```

The question is not selected from a project-type table. It is constructed from
the extracted verb/object (structure) or the user's two explicit approaches
(alternative).

### 5.2 Examples

| Actual request | Derived question | Options |
| --- | --- | --- |
| `Create a small portfolio site for John Smith.` | `How should I organize "small portfolio site for John Smith"?` | `One file`, `Multiple files`, `Let the model decide` |
| `Add support for this new configuration format.` | `How should I organize "support for this new configuration format"?` | same structural resolution space |
| `Add support for this new data source.` | `How should I organize "support for this new data source"?` | same structural resolution space |
| `Add validation for this input format.` | `How should I organize "validation for this input format"?` | same structural resolution space |
| `Implement config loading. Extend the existing loader vs introduce a new loader path.` | `Which approach should I use for "config loading"?` | `Extend the existing loader`, `Introduce a new loader path`, `Let the model decide` |

The structure labels are a **fixed option vocabulary** because they are the
resolution space of the structure dimension. They are not selected by project
type, language, or repository class. The alternatives dimension uses the user's
own words.

### 5.3 Evidence

- `TestProposalQuestionQuotesTheActualRequest` — the question and reason must
  quote the request object; the old fixed template fails this test.
- `TestProposalQuestionsVaryWithTheRequest` — six materially different requests
  produce six distinct questions.
- `TestProposalAlternativeDimensionUsesTheRequestsOwnOptions` — a different
  decision dimension uses the user's alternatives, not file-count vocabulary.
- `TestProposalIsLanguageAgnostic` — Go/Python/Rust-style requests produce the
  same fixed wording after substituting the subject; no language vocabulary is
  introduced.
- Mutation test: replacing the derived question with
  `"How should this be structured?"` caused
  `TestProposalQuestionQuotesTheActualRequest` and every structure row of
  `TestProposalGenericityMatrix` to fail.

---

## 6. Genericity matrix

| Case | Proposal? | Question source | Decision dimension | Result |
| --- | --- | --- | --- | --- |
| A — static website: `Create a small portfolio site for John Smith.` | yes | request object `small portfolio site for John Smith` | structure | PASS |
| B — Go-style config: `Add support for this new configuration format.` | yes | request object `support for this new configuration format` | structure | PASS |
| C — Python-style data source: `Add support for this new data source.` | yes | request object `support for this new data source` | structure | PASS |
| D — Rust-style validation: `Add validation for this input format.` | yes | request object `validation for this input format` | structure | PASS |
| E — backend/service: `Create a backend service that accepts webhooks and retries failed deliveries.` | yes | request object `backend service...` | structure | PASS |
| F — CLI/tooling: `Create a CLI that converts CSV to JSON.` | yes | request object `CLI that converts CSV to JSON` | structure | PASS |
| G — explicit structure: `Put this in internal/config/loader.go and add tests in internal/config/loader_test.go.` | no | targets detected; no unresolved decision | n/a | PASS |
| H — existing convention: portfolio request with visible entries | no | `ProposalFacts.HasConvention` | n/a | PASS |
| I — ordinary implementation detail: `Implement a helper function.` / ordinary change inside a convention | no | vague/ordinary subject; convention | n/a | PASS |
| J — different dimension: `Implement config loading. Extend the existing loader vs introduce a new loader path.` | yes | `splitAlternatives` → user's two approaches | alternative | PASS |

Cases B/C/D are deliberately language-neutral in their request text. The
Go/Python/Rust context is the repository class, and no proposal code path
consults it. This is the negative proof that the question is not project-type
driven.

Implementation: `TestProposalGenericityMatrix` in
`workspace/proposal_test.go`.

---

## 7. Hard-coded heuristic audit

| Occurrence | Location | Classification |
| --- | --- | --- |
| `proposalTitle = "Proposal Permission"` | `tui/approval.go:38` | legitimate presentation default |
| `proposalFooterHint` | `tui/approval.go:42` | legitimate presentation default / selection semantics |
| `ProposalDimension` values `structure`, `alternative` | `workspace/proposal.go:23–30` | other — control-plane decision dimension |
| `ProposalOptionOneFile` / `MultipleFiles` / `ModelDecides` | `workspace/proposal.go:34–42` | fixed option vocabulary — structure resolution space |
| `"One file"`, `"Multiple files"`, `"Let the model decide"` | `workspace/proposal.go:175–190` | fixed option vocabulary |
| `"Let the model decide"` (alternative) | `workspace/proposal.go:230` | fixed option vocabulary — conservative delegation |
| `creationHeads` | `workspace/proposal.go:450` | other — generic materiality rule (creation vs modification) |
| `ordinaryCodeUnitWords` | `workspace/proposal.go:462` | other — suppression of ordinary code units |
| `vagueSubjectWords` | `workspace/proposal.go:481` | other — suppression of vague objects |
| `structurePhrases` | `workspace/proposal.go:568` | user-evidence marker — suppresses only when the user declares a shape |
| `structureKindNouns` (`package`, `module`, `library`, `script`, `binary`, `tool`, `file`, `service`, `app`, `application`, `cli`, `program`) | `workspace/proposal.go:596` | user-evidence marker — matches `"as a <noun>"` / `"as an <noun>"`; suppression only |
| `slashCompoundPattern` | `workspace/proposal.go:605` | user-evidence marker — explicit `A/B/C` stack; suppression only |
| `knownFileExtensions` (`go`, `py`, `rs`, `ts`, `js`, `css`, `html`, …) | `workspace/contract.go:476` | user-evidence marker / target detector — only suppresses when the user names a path |
| `knownFileNames` (`Makefile`, `Dockerfile`, `README`, …) | `workspace/contract.go:486` | user-evidence marker / target detector |
| `RequestsMutation` / `ClassifyPrediction` | `workspace/intent.go` | other — generic authority/intent classifier |
| `ProposalFacts.HasConvention` | `workspace/proposal.go:93` | other — repository presence evidence |
| `proposalFacts` visible-entry count | `cmd/ohgo/run.go:1131` | other — presence only; no names, contents, or language |
| `prompt.ProposalDecision` copy | `prompt/prompt.go:94` | legitimate presentation default / control-plane system copy |
| `cognition.ProbeFacts` language markers | **removed from the proposal path**; still used elsewhere for task shape | other — unrelated to proposal detection |
| HTML / CSS / JavaScript / website / static / frontend / Go / Python / Rust / React in trigger logic | **none** | no project-specific heuristic; occurrences are in tests/comments as evidence |

### 7.1 Explicit negative result

There is no rule equivalent to:

```text
static web -> structure question
```

or:

```text
empty workspace + mutation -> generic structure question
```

independent of the request. `AssessProposal` does not inspect project type.
The only inputs are the request text, the deterministic intent/contract, and a
repository-presence fact (visible entries). The old `cognition.ProbeFacts`
language-marker probe was removed from the proposal path and replaced by a
visible-entry count.

---

## 8. Trusted state

Human resolution is stored as structured Control Plane state, not as
conversational text:

```go
// tui/app.go:442
type ProposalResolution struct {
    Option    string
    Label     string
    Directive string
    Dimension workspace.ProposalDimension
    Subject   string
    Question  string
    Request   string
}
```

`resolveProposal` writes this struct under `p.mu`. `runTurn` consumes it through
`proposalFor` and renders it as a control-plane-authored system block through
`proposalSystemBlock` / `prompt.ProposalDecision`. The user's message is not
rewritten. A delegated choice has an empty directive and adds no system block.
The decision serves the exact request it answered and explicit continuations;
an unrelated request clears it; a session boundary drops it.

The final flow:

```text
User request
    ↓
Control Plane assessment
    ↓
Proposal
    ↓
Original Proposal Permission
    ↓
Human resolution
    ↓
trusted structured state
    ↓
execution
```

---

## 9. LLM role and authority boundary

The Control Plane owns:

- whether a proposal is required;
- the decision dimension;
- the valid resolution space;
- the final trusted state.

The model does not decide any of those. No LLM call is involved in constructing
the question or options. The model receives only the human's chosen directive
in a system block with provenance. It remains free in reasoning and execution
language, but it cannot invent the decision or its authority.

---

## 10. Verification

Run and passed:

```text
gofmt -l .                                          -> empty
go build ./...                                      -> pass
go vet ./...                                        -> pass
go test ./...                                       -> pass
go test -race ./...                                 -> pass
go test -race ./workspace ./tui ./cmd/ohgo ./prompt -> pass
```

Focused proposal/UI tests:

```text
TestProposalQuestionQuotesTheActualRequest
TestProposalQuestionsVaryWithTheRequest
TestProposalIsLanguageAgnostic
TestProposalAlternativeDimensionUsesTheRequestsOwnOptions
TestProposalAlternativeUsesEitherForm
TestProposalSuppressesExplicitStructure
TestProposalSuppressesExistingConvention
TestProposalSuppressesOrdinaryImplementationDetail
TestProposalOnlyRunsUnderMutationAuthority
TestProposalGenericityMatrix
TestProposalUsesTheOriginalPermissionSurface
TestProposalSurfaceMatchesToolApproval
TestProposalNavigationUsesUpDownEnterEsc
TestProposalEscCancelsWithoutRecordingOrRunning
TestProposalDecisionReachesExecutionAsTrustedState
TestProposalDelegationAddsNoConstraint
TestProposalQuestionVariesAtTheUISurface
TestStandingProposalSkipsReproposal
TestNewSessionDropsStandingProposal
TestContinuationReusesStandingProposal
```

Mutation-tested critical paths:

1. Replacing the derived question with `"How should this be structured?"` made
   `TestProposalQuestionQuotesTheActualRequest` and the structure rows of
   `TestProposalGenericityMatrix` fail.
2. Removing the `HasConvention` guard made
   `TestProposalSuppressesExistingConvention` and matrix cell H fail.

Both mutations were reverted; the suite is green.

---

## 11. Remaining risks

1. **Deterministic wording skeleton.** The structure question's sentence
   pattern is fixed (`How should I organize "<subject>"?`), while the dimension
   and subject are request-derived. If the product requires free-form phrasing,
   an LLM presentation layer would be needed. It must remain unable to choose
   authority or options.
2. **Presence-only convention evidence.** `HasConvention` is true for any
   visible entry, including a lone `README`. This is deliberately conservative:
   it avoids asking in a non-empty workspace, but can over-suppress a genuine
   decision. It never inspects file names, contents, or languages.
3. **Alternative syntax coverage.** Only explicit `A vs B`, `A versus B`, and
   `either … or …` are treated as alternatives. A bare "or" or another
   connector is not, by design; that trades recall for precision.
4. **Pre-existing verb-classifier boundary.** `BuildIntentContract` treats a
   leading `build` as `ObjectiveExecution`, not `ObjectiveMutation`, so a bare
   "Build a backend service" command does not reach the proposal gate. The
   matrix uses `Create` for the backend/service and CLI cases. This is an
   existing classifier behavior, not a new project-type heuristic.
5. **Pre-existing workspace-transition card.** It remains for authority moves,
   unchanged. The strategy proposal never uses it and never sets
   `State.Transition`. If the product later wants one visual surface for all
   pending runtime decisions, that is a separate refactor; this task
   deliberately did not rewrite unrelated authority-transition behavior.

---

## 12. Final recommendation

**PASS.**

One original Proposal Permission surface. No newly invented picker center. The
question is grounded in the actual request and its unresolved decision. No
project-type/template heuristic masquerades as intelligence. The Control Plane
retains authority; the LLM remains free in reasoning and execution language.
