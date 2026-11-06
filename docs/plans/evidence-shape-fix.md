# Evidence-Shape Fix: Literal Coordinates vs Repository Concepts

> **Status:** proposed.
> **Scope:** request contract → task shape → required knowledge → sufficiency →
> projection → completion.
> **Non-goal:** weakening `INSUFFICIENT`, adding an LLM judge, changing workspace
> routing, or making completion easier.

## 1. Problem statement

A repository-scoped understanding request that names a conceptual slash phrase
is misread as a literal filesystem coordinate:

```text
"Check this Go e-commerce project and understand how the main
 order/checkout flow works."
```

`order/checkout` is extracted as `contract.Target{Kind: targetPath}`. The task
shape then declares `target_state:order/checkout`. That requirement is satisfied
only by observing the literal path `order/checkout`, not by reading the files
that implement the checkout flow. The result is a false evidence gap:

```text
runtime evidence was not sufficient for the objective (INSUFFICIENT) at stage explore
```

A second defect runs in the opposite direction. A similar prompt without a
canonical repository-subject phrase declares no evidence requirement at all:

```text
"Explain how the checkout flow works."
```

The runtime can answer it from the first model turn without observing the
repository, even though the question is about code in the workspace. The same
asymmetry makes the first prompt lose its repository fallback: `repositoryScope`
does not recognise `this Go e-commerce project`, because an adjective sits
between the determiner and the repository noun.

This is an actual policy defect, not an intentional design choice. The existing
architecture documents the intended distinction: a named coordinate is a
targeted objective, while a repository subject plus an understanding concept is
repository-scoped; breadth is semantic, not a phrase whitelist
(`docs/architecture/ARCHITECTURE.md`, "Evidence sufficiency and the exploration
stop"). The implementation does not honour that distinction for conceptual
slash phrases or modified repository subjects.

## 2. Observed current behavior

Probe results against the current tree (temporary overlay test, no source
modified):

| Prompt | `Kind` | Targets | `RepositoryScope` | Broad | Required knowledge | Empty evidence |
| --- | --- | --- | --- | --- | --- | --- |
| `Check this Go e-commerce project and understand how the main order/checkout flow works.` | informational | `path order/checkout` | false | false | `target_state:order/checkout` | `INSUFFICIENT/explore` |
| `Check this project and understand how the checkout flow works.` | informational | none | true | true | `repository_overview`, `repository_survey` | `INSUFFICIENT/explore` |
| `Check this project and understand how the order/checkout flow works.` | informational | `path order/checkout` | true | false | `target_state:order/checkout`, `repository_overview` | `INSUFFICIENT/explore` |
| `Understand how the checkout flow works.` | informational | none | false | false | none | `SUFFICIENT/answer` |
| `Explain how the checkout flow works.` | informational | none | false | false | none | `SUFFICIENT/answer` |
| `Read order/checkout.go` | informational | `path order/checkout.go` | false | false | `target_state`, `target_content` | `INSUFFICIENT/explore` |
| `Inspect the order/checkout directory` | informational | `path order/checkout` | false | false | `target_state:order/checkout` | `INSUFFICIENT/explore` |
| `Explain this project.` | informational | none | true | true | `repository_overview`, `repository_survey` | `INSUFFICIENT/explore` |

With realistic checkout evidence (root listing plus reads of
`order/controller.go`, `order/checkout.go`, `checkout/service.go`, `go.mod`),
the first prompt is still `INSUFFICIENT/explore` because only the literal
`order/checkout` path can satisfy its only requirement. The conceptual prompts
with no requirement are `SUFFICIENT/answer` with **zero** observations.

A genuine literal path request is already strict and correct: reading a sibling
file does not satisfy it, and reading the exact file does. A genuine directory
request is accepted as a literal target because it is introduced by the word
"directory".

## 3. Exact root cause(s)

### Root cause A — the path extractor treats any slash token as a filesystem coordinate

`internal/control/workspace/contract.go`:

- `extractPathTargets` accepts every `pathToken` containing `/`.
- `looksLikeFile` is only consulted for extensionless tokens without `/`.
- Therefore `order/checkout flow` contributes `TargetPath{"order/checkout"}`.

The extractor has no notion of a conceptual slash compound. It cannot tell
`order/checkout` (a flow) from `order/checkout/` or `order/checkout.go` (a
coordinate).

### Root cause B — repository scope is phrase-bound to adjacent words

`internal/control/cognition/shape.go`:

- `repositoryScope` and `broadRepositoryAnalysis` use exact phrases such as
  `" this project "`.
- `this Go e-commerce project` normalises to `this go e commerce project`, so
  the phrase does not match.
- Consequence: the request loses `repository_overview`, leaving only the wrong
  literal target. It also loses the broad/understanding classification.

### Root cause C — conceptual topics do not exist as contract vocabulary

`internal/control/workspace/grounding.go` / `contract.IntentContract`:

- The contract has path, symbol, and package entities.
- It has no representation for a named repository concept such as
  `checkout flow` or `order/checkout flow`.
- A conceptual request therefore either becomes a path target (A) or declares
  nothing at all.

### Root cause D — broad classification ignores a targeted concept object

`broadRepositoryAnalysis` treats any understanding verb near a repository
subject as a whole-repository survey. `Check this project and understand how
the checkout flow works` therefore demands `repository_survey`, even though the
object is one flow. This is over-grounding, not the cause of the original
`INSUFFICIENT`, but it is the same conceptual boundary and must be fixed.

**Not root causes:**

- **Workspace routing.** `AnalyzeRequest` / `RouteTurn` / `DecideTransition`
  classify the request as investigate/read. They do not consume targets and are
  correct.
- **Evidence sufficiency.** `EvaluateSufficiency` correctly reports the
  requirements the shape declared. `INSUFFICIENT/explore` is the truthful
  verdict for a shape that demands a literal path that was never observed.
- **Projection and completion.** `ProjectionReport.EvidenceReview()` and the
  `CompletionInput.EvidenceReview` veto are correct. They must keep vetoing an
  answer when the shape genuinely needs missing evidence.
- **Intent grounding admission.** `ValidateProposal` / `AssessGrounding` do not
  create the target; they inherit the same bad target. They are not the layer to
  patch.

## 4. Why the current behavior is incorrect or insufficient

- It confuses a conceptual entity with a filesystem coordinate. The user named
  a flow, not a path. Reading the implementing files is valid evidence for the
  flow; the literal path `order/checkout` may not exist at all.
- It punishes correct repository work. The agent can inspect the checkout
  implementation across several files and still be reported as lacking evidence.
- It under-grounds the same question when phrased without `this project`. A
  question about the repository's checkout flow can be answered with zero
  observations.
- It over-grounds a targeted concept as a repository survey when a canonical
  subject phrase is present.
- It makes evidence sufficiency depend on superficial wording. The same
  objective produces opposite evidence requirements depending on whether the
  user wrote `this project`, `this Go e-commerce project`, or no subject.

## 5. What behavior should be preserved

- A token with a file extension or known filename (`order/checkout.go`,
  `README.md`, `Makefile`) remains a literal `TargetPath`.
- A token explicitly framed as a filesystem object (`.../directory`,
  `.../folder`, `.../file`, `.../path`), preceded by a path verb
  (`read`, `open`, `inspect`, `edit`, `write`, `delete`, ...), preceded by a
  location preposition (`in`, `under`, `within`, `at`, ...), or backticked,
  remains a literal `TargetPath`.
- A literal path request remains strict: `target_state` is required, and an
  explicit content/inspection request also requires `target_content`.
- A literal directory inspection cannot be satisfied by a listing of its
  parent; it needs the directory's own complete listing.
- `repository_overview` still requires a content-bearing observation, not a
  directory listing.
- `repository_survey` still requires two content observations, or one content
  observation plus structure, and at least one implementation observation in a
  code repository.
- A mutation request still requires an observed change and verification.
- An execution request still requires a successful terminal process.
- `INSUFFICIENT`, `CONFLICTING`, `BLOCKED`, `NEEDS_REVIEW`, grounding review,
  and provenance review remain completion vetoes. None is weakened.
- Workspace routing and workspace authority are unchanged.

## 6. What behavior must change

1. A slash token is a literal path only when it carries a filesystem signal.
   Otherwise it is a conceptual topic, not a target.
2. A modified repository subject (`this Go e-commerce project`, `our legacy
   codebase`) is recognised as repository scope.
3. A conceptual topic in an understanding request is repository-scoped and
   declares `repository_overview`; it is not a literal coordinate and does not
   declare `target_state`.
4. A repository-scoped request whose object is a named concept (`checkout
   flow`, `order/checkout flow`) is targeted, not a whole-repository survey.
   It does not require `repository_survey`.
5. A bare conceptual question (`Explain how the checkout flow works.`) declares
   repository evidence instead of answering from nothing.
6. An explicit literal directory inspection requires `target_content`, so a
   parent listing cannot satisfy it; a complete listing of the target directory
   itself establishes both its state and its contents.
7. A mutation branch that names a repository concept and an explicit check is
   bounded by that check. It is not turned into a literal path, and it is not
   left ambiguous merely because the concept has no filename.

## 7. Proposed architecture-level fix

The boundary is the **request contract**: `workspace` classifies the user's
words into repository-coordinate targets and repository-concept entities;
`cognition` derives task shape and evidence needs from that contract. Do not
parse the objective a second time in `cognition`.

### 7.1 Coordinate vs concept classification (workspace)

Add a deterministic classification in `workspace`:

- `literalPathTarget(text, start, end, value) bool` decides whether a candidate
  slash token is a filesystem coordinate.
  - `true` for a recognised filename/extension, a trailing `/`, a backticked
    token, a following path noun (`file`, `directory`, `folder`, `path`), or a
    preceding path verb/location preposition (with one optional determiner
    between the cue and the token).
  - `false` for a slash compound followed by a concept noun (`flow`,
    `pipeline`, `handler`, `service`, `module`, `subsystem`, `endpoint`,
    `controller`, `worker`, `middleware`, `router`, `parser`, `validator`,
    `lifecycle`, `logic`, `behaviour`, `event`, `job`, `process`, `workflow`,
    `mechanism`) unless it also carries a stronger filesystem signal.
- `extractPathTargets` uses this predicate instead of accepting every `/`.
- `extractConceptTopics(text)` extracts named repository concepts:
  - a slash compound followed by a concept noun (`order/checkout flow`);
  - a modified concept phrase with a definite/demonstrative reference
    (`the checkout flow`, `this order/checkout flow`);
  - a concept phrase introduced by an understanding/request head
    (`understand the checkout flow`, `explain checkout flow`).
  - It is conservative: indefinite generic framing (`a checkout flow`,
    `how do checkout flows work`) and non-concept slash tokens (`and/or`,
    `24/7`, URLs) are not topics.
- Concept extraction runs only when the contract has no path/symbol/package
  target. A literal coordinate is more specific than a topic; a request that
  names an actual path must stay a path request.

Add `contract.EntityTopic` and add topics to `IntentGrounding.Entities` with
that kind. `RequiredEntities` continues to preserve only non-optional
`EntityProject` entities, so a topic is context, not a forced prose token.
`groundingScope` does not promote a topic to a named entity scope; topics are
repository-scoped context.

### 7.2 Repository scope (cognition)

Add `hasConceptTopic(intent)` reading `intent.Grounding.Entities`.

- `repositoryScope` returns true when the contract carries a concept topic.
- Replace the exact-phrase subject check with a word-aware
  `hasRepositorySubject(normalized)`:
  - keeps all existing canonical phrases;
  - also recognises a reference determiner followed within a bounded window by
    a repository noun (`project`, `repository`, `repo`, `codebase`,
    `workspace`, `system`, `application`, `app`, `program`, `product`, `tool`,
    `code`), stopping at an auxiliary/verb so `this is a checkout project`
    does not match.
- `broadRepositoryAnalysis` returns false when the contract carries a concept
  topic and the request does not explicitly widen to the whole repository
  (`whole project`, `as a whole`, `end to end`). The object is the concept, not
  the project.
- A canonical repository subject with no concept topic still produces
  `repository_overview` and, with an understanding/overview/architecture
  concept, `repository_survey`.

### 7.3 Required knowledge

No new requirement key is introduced. A repository concept is satisfied by
`repository_overview`, the existing content-bearing repository evidence:
one file read, one content-bearing search, or one structural symbol query. This
is deliberately the same boundary the architecture already documents: the
runtime can establish that the repository was observed, not that a particular
file answers the question.

- `Explain how the checkout flow works.` → `repository_overview`.
- `Check this Go e-commerce project ... order/checkout flow` →
  `repository_overview`, no `target_state`, no `repository_survey`.
- `Read order/checkout.go` → `target_state:order/checkout.go` and
  `target_content:order/checkout.go`.
- `Inspect the order/checkout directory` → `target_state:order/checkout` and
  `target_content:order/checkout`.
- `Explain this project.` → `repository_overview` and `repository_survey`.
- `Fix the failing tests.` → `repository_overview`, `verification`.
- `Run the tests.` → `execution_result`, `verification`.
- `Update note.txt.` → `target_state:note.txt`, `verification`.

### 7.4 Strict content for inspection

Extend `requestsContent` with explicit inspection verbs (`inspect`, `examine`,
`look at`, `open`, `review`, `analyze`, `analyse`) so an explicit literal
inspection requires `target_content`. This is not a new evidence rule; it makes
the existing `target_state` vs `target_content` distinction apply to the verbs
that actually ask for contents.

A complete listing of the target directory itself is target-state evidence for
that directory (`EvidenceState.targetStateKnown`). Previously only a parent
listing could establish it, so an inspected directory could prove its contents
but not its own existence.

### 7.5 Conceptual mutation scope

Removing the accidental path exposes a real contract question for staged
requests: the full e-commerce prompt says "investigate the order/checkout flow;
if confirmed, implement the fix and run the tests", and previously the false
`order/checkout` path happened to bound the mutation. A concept is not a path,
so the branch must still be bounded without inventing one.

If a mutation has no literal target, names a repository concept, and carries an
explicit verification requirement from the request itself, treat that named
check as the behavioural oracle (`BehavioralObjective = true`) and scope it to
the repository (`VerifyScope "*"`) when no scope was named. The branch still
requires an observed change and a successful check. A concept without a check
(`Fix the checkout flow.`) and a generic "update the code" with a check both
remain ambiguous, preserving the existing review boundary.

### 7.6 Layer ownership kept intact

| Concern | Owner | Change |
| --- | --- | --- |
| Workspace routing | `workspace/intent.go`, `decision.go` | none |
| Request contract / target + concept extraction | `workspace` | coordinate predicate + concept entities |
| Grounding data shape | `contract` | additive `EntityTopic` |
| Task shape / required knowledge | `cognition` | consume topics, word-aware subject, concept narrows survey |
| Evidence sufficiency | `cognition` | unchanged rules; only requirements change |
| Projection / `EvidenceReview` | `cognition`, `agent` | unchanged |
| Completion / `NEEDS_REVIEW` | `agent` | unchanged; veto preserved |
| Workspace authority | security gate | unchanged |

## 8. Exact files/packages/functions expected to change

- `internal/control/contract/grounding.go`
  - add `EntityTopic EntityKind = "topic"`;
  - add `IntentGrounding.HasConcept` as the one concept predicate shared by
    `workspace` and `cognition`.
- `internal/control/workspace/contract.go`
  - update `extractPathTargets` to call `literalPathTarget`;
  - add `literalPathTarget`, `nextPathWord`, `previousPathWord`,
    `backtickedAt`, path noun/verb/preposition vocabulary;
  - add `extractConceptTopics` and its concept vocabulary;
  - keep `looksLikeFile`, `knownFileExtensions`, `knownFileNames`,
    `verificationArgumentSpans` semantics.
- `internal/control/workspace/grounding.go`
  - add concept entities in `groundingEntities` when `len(intent.Targets) == 0`;
  - keep `groundingScope` from treating topics as named project scope.
- `internal/control/cognition/shape.go`
  - add `hasConceptTopic`;
  - add `hasRepositorySubject` and use it in `repositoryScope` and
    `broadRepositoryAnalysis`;
  - make `broadRepositoryAnalysis` concept-aware;
  - extend `requestsContent`.
- `internal/control/cognition/evidence.go`
  - let `targetStateKnown` accept a complete listing of the target directory
    itself, not only a parent listing.
- Tests:
  - `internal/control/workspace/contract_test.go` or a new focused test file:
    literal path vs concept classification.
  - `internal/control/cognition` new regression test file:
    shape + required knowledge + sufficiency + evidence-review completion for
    the full matrix.
  - `internal/tui/completion_test.go` or a new focused test:
    phase completion with the original prompt and realistic checkout reads
    must not produce `NEEDS_REVIEW`.
- Documentation:
  - `docs/architecture/ARCHITECTURE.md` evidence section: coordinate vs
    concept distinction.
  - `docs/architecture/INTENT_GROUNDING.md` entities section: topic entity.
  - this plan file is the implementation record.

No changes to `internal/control/workspace/intent.go`, `decision.go`, `agent/loop.go`
admission wiring, `agent/completion.go` rules, `cognition/sufficiency.go`
verdict rules, `agent/projection.go`, `cmd/ohgo/outcome.go`, or
`cmd/ohgo/taskcontinuity.go`.

## 9. Test strategy

Test the observable contract, not the final string.

### Workspace contract tests

- `Read order/checkout.go` → exactly `TargetPath{"order/checkout.go"}`.
- `Inspect the order/checkout directory` → exactly
  `TargetPath{"order/checkout"}`.
- `Implement the fix in order/checkout` → `TargetPath{"order/checkout"}`.
- `Check this Go e-commerce project and understand how the main
  order/checkout flow works.` → no path target; grounding contains an
  `EntityTopic` whose value includes `order/checkout`; no `EntityPath`.
- `Explain how the checkout flow works.` → no path target; one `EntityTopic`
  `checkout flow`.
- `What is a cron expression?` → no topic, no target.
- `Explain how a checkout flow works.` (indefinite/general) → no topic.
- `Read order/checkout.go and explain the checkout flow.` → the literal path
  wins; no topic is needed because the coordinate is more specific.
- The staged e-commerce prompt → no path target, a concept entity,
  `BehavioralObjective=true`, non-ambiguous, verification scoped to `*` with
  the named test check.
- `Fix the checkout flow.` → concept but no check, still ambiguous.
- `Update the code to be more robust and run go test ./...` → no concept,
  still ambiguous.

### Shape and evidence tests (each case asserts shape *and* resulting verdict)

1. `Explain how the checkout flow works.`
   - shape: `RepositoryScope=true`, `BroadRepositoryAnalysis=false`,
     `RequiredKnowledge` contains `repository_overview`, no `target_state`.
   - empty evidence: `INSUFFICIENT/explore`.
   - one content read: `SUFFICIENT/answer`.
   - projector `EvidenceReview` empty after one read; completion `COMPLETED`.
2. `Understand how the checkout flow works.`
   - same shape and verdicts as (1).
3. `Check this project and understand how the checkout flow works.`
   - shape: repository scope, targeted (no `repository_survey`), overview only.
   - empty: `INSUFFICIENT/explore`; one read: `SUFFICIENT/answer`.
4. `Check this Go e-commerce project and understand how the main
   order/checkout flow works.`
   - shape: no `TargetPath`, no `target_state`, repository scope, targeted,
     overview only.
   - empty: `INSUFFICIENT/explore`; realistic checkout reads:
     `SUFFICIENT/answer`; completion `COMPLETED`.
5. `Read order/checkout.go`
   - shape: `target_state` and `target_content` for the exact path.
   - empty: `INSUFFICIENT/explore`.
   - read `order/controller.go`: still `INSUFFICIENT/explore`.
   - read `order/checkout.go`: `SUFFICIENT/answer`.
6. `Inspect the order/checkout directory`
   - shape: `target_state` and `target_content` for `order/checkout`.
   - `ls order` (parent): still `INSUFFICIENT`.
   - complete `ls order/checkout`: `SUFFICIENT/answer`.
7. Broad repository survey: `Explain this project.`
   - `repository_overview` + `repository_survey`.
   - empty: `INSUFFICIENT/explore`; one read: `INSUFFICIENT/explore`;
     `README.md` + `main.go` with `Facts{Go:true}`: `SUFFICIENT/answer`.
8. Named symbol/package: `Who calls parseConfig?`
   - `target_coordinates:parseConfig`; empty `INSUFFICIENT/explore`;
     a symbols observation whose summary contains `parseConfig`:
     `SUFFICIENT/answer`.
9. Execution: `Run the tests.`
   - `execution_result` (and `verification` when a check is named);
     static reads are `INSUFFICIENT/explore`;
     a successful terminal verification makes it `SUFFICIENT/complete`.
10. Mutation: `Update note.txt.`
    - `target_state:note.txt` + `verification`;
      read + no change: `SUFFICIENT/act` (pre-change evidence known);
      change without check: `INSUFFICIENT/verify`;
      change + successful check: `SUFFICIENT/complete`.
11. Missing evidence still `INSUFFICIENT`: the literal path and directory
    cases above, plus broad survey with one observation.
12. Sufficient static evidence still `SUFFICIENT/answer`: the concept and
    targeted repository cases above.

### TUI regression

Use the production `turnTaskOutcomeForAuthority` path with an `EvidenceReview`
computed by the projector for the original prompt under FUZZ. The scripted turn
reads an implementation file and answers. The phase must be
`taskActionPhaseComplete`, not `taskActionReview`. This reproduces the original
symptom at the completion boundary rather than only checking the shape struct.

### Test quality rules

- Assert required-knowledge keys and target kinds, not just a string.
- Assert `Sufficiency.State` and `Stage`.
- For informational cases, feed the projector's real `EvidenceReview` into
  `EvaluateTaskCompletion` so the completion veto is exercised.
- Break each new test's mechanism once during development and confirm it fails.

## 10. Regression cases

Mandatory matrix (all in the tests above):

1. `Explain how the checkout flow works.`
2. `Understand how the checkout flow works.`
3. `Check this project and understand how the checkout flow works.`
4. `Check this Go e-commerce project and understand how the main
   order/checkout flow works.`
5. `Read order/checkout.go`
6. `Inspect the order/checkout directory`
7. `Explain this project.`
8. `Who calls parseConfig?`
9. `Run the tests.`
10. `Update note.txt.`
11. Missing evidence still `INSUFFICIENT` (literal path, directory, broad
    survey).
12. Sufficient static evidence `SUFFICIENT/answer` (concept and targeted
    repository).

Additional negatives:

- `What is a cron expression?` still declares no repository requirement.
- `What is the best CI for a Go project?` still declares no repository
  requirement.
- `Explain how a checkout flow works.` (indefinite) does not become a
  repository topic.
- `Read internal/agent/loop.go and explain the retry logic` remains a literal
  path request; the concept mention does not widen it to a survey.
- `Fix the failing tests.` remains a behavioural mutation with
  `repository_overview` + `verification`.
- `Fix GrepTool in tools/grep.go so go test ./tools passes.` keeps its path and
  symbol targets and its verification scope.

## 11. Acceptance criteria

1. The original prompt produces no literal path target for `order/checkout`; it
   produces a repository concept and a targeted repository evidence need.
2. With realistic checkout inspection (a content-bearing read of at least one
   implementation file), sufficiency is `SUFFICIENT/answer`, the projection's
   `EvidenceReview()` is empty, and completion is `COMPLETED`.
3. With no repository observation, the same prompt is still
   `INSUFFICIENT/explore`. The fix does not make an unobserved answer complete.
4. `Explain how the checkout flow works.` and
   `Understand how the checkout flow works.` require a repository observation
   and are `SUFFICIENT/answer` only after content-bearing evidence.
5. `Read order/checkout.go` requires the exact file's state and content; a
   sibling read does not satisfy it.
6. `Inspect the order/checkout directory` requires the exact directory; a
   parent listing does not satisfy its content requirement.
7. `Explain this project.` still requires a survey and still rejects one
   observation.
8. `Run the tests.` still requires execution evidence; `Update note.txt.` still
   requires a change and a post-change verification.
9. Intent Grounding, Evidence Sufficiency, Task Shape, Completion, and Workspace
   Routing remain separate: only the request-contract and shape inputs change.
10. `go build ./...`, `go test ./...`, `go vet ./...`, `go test -race ./...`,
    `gofmt -l .` (empty), and `golangci-lint run ./...` pass.
11. The final diff contains no routing change, no weakened sufficiency rule, and
    no weakened completion veto.

## 12. Risks and compatibility considerations

- **Over-extraction of topics.** A concept vocabulary can turn general
  knowledge questions into repository questions. Mitigation: require a
  definite/demonstrative reference or an understanding/request head; reject
  indefinite generic framing; reject non-concept slash tokens; test
  `What is a cron expression?`, `What is GitHub Actions?`, and
  `How does a checkout flow work?`.
- **Under-extraction.** Unusual concept wording may still declare no topic.
  That is safer than turning every noun phrase into a repository requirement,
  and the literal path behavior is unchanged. Add vocabulary only with a test
  that demonstrates a real false negative.
- **Path-cue false positives.** A path verb immediately before a conceptual
  slash compound could keep it literal. Mitigation: a following concept noun
  wins over a preceding path verb; a following path noun wins over a following
  concept noun; file extensions/backticks/trailing slash always win.
- **Content strictness regression.** Adding inspection verbs to
  `requestsContent` can make previously accepted answers reviewable when the
  model only listed a parent directory. That is the intended truthfulness
  boundary, not a regression. Existing read/content tests already pin the
  distinction.
- **Persistence compatibility.** `EntityTopic` is an additive entity kind;
  older checkpoints with no topic entities remain valid. No schema version bump
  is needed because the JSON shape is unchanged.
- **Contract consumers.** `TargetPath` semantics are narrowed (fewer false
  positives), not widened. Snapshot capture, mutation scope, verification
  scopes, and structural target checks continue to see only genuine
  coordinates.
- **Workspace routing.** No change. The observed prompt still routes as an
  investigation/read; this fix only changes what evidence the answer needs.
- **External requirement policy.** The separate `website` word match in
  `externalRequirement` is not part of this root cause and is not changed here.
