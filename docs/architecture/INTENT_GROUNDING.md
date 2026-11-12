# Intent Grounding and Semantic Integrity

> **Status:** implemented.
> **Scope:** user request → semantic contract → tool proposal → completion.
> **Non-goal:** a second agent, a planner, a prompt framework, or an LLM judge
> on every request.
> **Canonical topology:** [`OHGO_ARCHITECTURE_MAP.md`](../../OHGO_ARCHITECTURE_MAP.md).
> **Permanent rules:** [`OHGO_ARCHITECTURE_RULES.md`](../../OHGO_ARCHITECTURE_RULES.md) §11, §13.
> **Runtime behaviour:** [`ARCHITECTURE.md`](ARCHITECTURE.md).

## 1. Why this layer exists

A capable model can use tools correctly and still fail semantically:

```text
User:  Additional projects are k8s and opencode.
Model: (inspects a README, finds nothing)
       The repository contains no README documentation, so I cannot describe
       the test-web2 project.
```

Tool competence is not the problem. The model invented an objective, dropped
the named entities, turned a local observation into a global negative
conclusion, and the runtime accepted a non-empty answer as completion.

The runtime now carries a deterministic **grounding contract** built from the
user's own text. The model still interprets language; the contract makes that
interpretation checkable before it becomes execution state.

## 2. Placement

```text
USER TEXT
   │
   ▼
workspace.BuildIntentContract ──────────────► contract.IntentContract
   │                                             ├─ targets / verification (existing)
   │                                             └─ Grounding (new)
   ▼
agent.Loop semantic admission ───────────────► ValidateProposal
   │                                             REJECT a mutation the request
   │                                             did not ground
   ▼
authority (ASK / FUZZ / BUILD) ──────────────► unchanged security gate
   │
   ▼
tools + evidence + verification
   │
   ▼
agent.EvaluateTaskCompletion ────────────────► AssessGrounding
                                                 entity / action / scope /
                                                 ambiguity / negative-claim /
                                                 claim-scope review
```

Ownership is deliberately split:

| Concern | Owner |
| --- | --- |
| Request classification and entity extraction | `workspace` |
| Grounding data shape | `contract` |
| Semantic admission and answer validation | `agent` |
| Task-shape projection | `cognition` |
| Durable continuation | `task` (inside the existing contract) |
| Authority | existing security gate — unchanged |

No package owns two of these responsibilities. `cognition` does not authorize,
`agent` does not grant authority, and `workspace` does not execute.

## 3. The semantic contract

`contract.IntentGrounding` carries:

```text
objective           the user's request, trimmed
action              unknown | read | investigate | execute | mutate
actions[]           every action in a compound request
entities[]          named projects, paths, symbols, packages,
                    repository concepts ("checkout flow")
scope               unknown | named | repository, plus conditional relevance
expectedOutcome     what truthful completion would look like
constraints[]       explicit limits the user stated
assumptions[]       grounded | inferred | unsupported
ambiguities[]       unknown_action | unresolved_reference |
                    unresolved_alternative, with material flag
mutationIntent      true only when the request clearly asks for a change
confidence          high | medium | low
```

It is persisted with the completion contract. A resumed task therefore keeps
the entities and ambiguities the user stated; a later prompt cannot silently
replace them.

## 4. Validation rules

### 4.1 Entity preservation

A named project/entity is preserved in the contract and projected into the
runtime state block. An informational answer must mention each required
(project-kind, non-conditional) entity. Conditional entities (`if relevant`)
are preserved but not forced into the answer.

An entity is a named thing, never the request's verb. In “investigate the
bottleneck and evaluate”, `evaluate` heads the second clause, so it is not a
required entity; requiring it would make the answer preserve a verb instead of
a subject. The extractor uses the router's clause-head vocabulary, so a
coordinated phrase whose second part starts with a request verb is two clauses,
not a list of names.

Repository coordinates (paths, symbols, packages) are not required in prose:
mutation completion already proves them through target evidence.

A repository **concept** (`checkout flow`, `order/checkout flow`) is a
different entity kind. It is context for a repository-scoped question, not a
filesystem coordinate and not a named project scope. It does not require a
literal path, and it is not a required prose token; the evidence boundary is
`repository_overview`, satisfied by observing the implementation. A concept is
extracted only when the request names no explicit path, symbol, or package: a
literal coordinate is more specific than the topic it happens to mention.

### 4.2 Action fidelity

A model proposal is derived from trusted tool metadata and call arguments, not
from model narration.

```text
request mutationIntent = false
    + call mutates        → REJECT (semantic admission)
request action = unknown
    + call mutates        → REJECT
    + call reads          → PASS (bounded observation)
    + call executes       → REVIEW (allowed, completion stays guarded)
    + external access     → REVIEW
```

A rejection is returned to the model as a structured tool error, and the loop
allows one recovery request. The refusal itself is not a completion event: the
model must see the reason before its accompanying narration can be judged as an
answer. If the recovery also proposes only refused actions and emits no answer,
the loop closes the still-open action surface and makes one answer-only request
instead of blocking. The same recovery applies when the model stops with no text
at all immediately after a refusal-only turn: an empty stop is not an answer, so
the loop drops the empty assistant message and asks once for the answer from the
evidence. Nothing can execute, so the evidence already gathered is the only basis
for the answer; completion still decides whether it is sufficient.

A second empty stop can happen on the answer-only request itself: the surface
is already closed and the model returns only reasoning. The runtime has asked
for the answer, nothing executed, and the run still has no visible text to hand
the user; ending there would swallow the result after a complete investigation.
The loop drops the empty assistant turn and makes one further answer-only
request. The host may append a runtime-authored user-role request that states
the surface is closed;
it is a request view, never a persisted user turn or a grant of authority. The
retry never reopens tools and never becomes a second action surface, so it
cannot grant authority or turn silence into a completion claim.

For a shell redirect, "mutates" is bounded by the workspace root. A redirect to
an absolute path outside the workspace writes elsewhere, not into the
repository: starting a local server that logs to a temporary file is execution,
and FUZZ may perform it. A relative target, an unresolved expansion, or an
absolute target inside the workspace remains a mutation. The root-aware
classifier is used by admission and by the human shell escape; the root-less form
stays conservative where no workspace is known.

A clause that asks about an operation grounds `read`, not the operation. The
extractor works on the same clause model as the router: mutation, execution, and
investigation vocabulary inside a question ("does this update GitHub?") is
descriptive, so it cannot ground a mutation or an execution. Without that rule a
read-only question would carry a mutation objective, and completion would wait
for a change nobody requested.

A read under an actionless request is allowed because evidence gathering is
low-impact. It cannot become the objective: an answer that narrates an action
the request never asked for is reviewed, and the request's action remains
`unknown`.

Delegation is treated as an action with a potential objective. A request that
did not ground a mutation may delegate only as bounded observation: the loop
forces the child read-only, so the model cannot create a writable child under
an ungrounded objective.

### 4.3 Scope fidelity

A request that names `k8s and opencode` carries a named scope. An answer that
narrows the scope to “only the current repository” is reviewed. An unresolved
reference (“the other two repositories”) is material ambiguity: completion is
reviewed rather than inventing which repositories were meant.

### 4.4 Ambiguity preservation

`unknown` is a valid action. The runtime does not derive an action from the
availability of a tool. An actionless request may be acknowledged or answered
with clarification, and it completes only when the answer preserves the named
entities and does not invent an action or conclusion.

An unresolved alternative (“A vs B”) cannot be decided by the model on the
user's behalf; completion stays reviewable.

### 4.5 Evidence integrity

Existing evidence classes remain authoritative. Grounding adds the missing
answer-level distinction:

```text
tool error                     ≠ absence
failed read at one path        ≠ global absence
local directory listing        ≠ repository-wide absence
complete recursive file scan   → may prove a concrete file is absent
scan pruned by .gitignore      ≠ complete recursive file scan
timed-out or truncated scan    ≠ complete recursive file scan
file absence                   ≠ concept absence
search returned nothing        ≠ concept does not exist
local configuration            ≠ external state
```

A supported file absence (for example a complete `glob "**/*"` with no
`README.md`) can complete. The scan must actually cover the tree: a recursive
glob that pruned `.gitignore` paths, a timed-out scan, or a truncated result
cannot prove absence. The glob tool names an exclusion so the answer can widen
the scan with `"gitignore": false` instead of concluding the pruned subtree does
not exist. A concept claim (“no documentation”, “no tests”)
cannot be proven by a file scan and stays `NEEDS_REVIEW`. The one narrow
exception is Go's mandatory `_test.go` suffix: a complete recursive scan of
every `.go` file that contains no `_test.go` file proves the narrowed claim “no
Go test files”. A complete, unscoped `structure` tests inventory with scope,
inventory, and structural completeness all `COMPLETE` and zero test files is
accepted as the structural equivalent of that scan. A bare “no tests” claim, and
frontend test absence where naming is convention-based, remain concept
absences. A snapshot that reports `scope: PARTIAL`, an excluded root, a pending
parse, or unsupported source cannot prove a repository-wide absence. An answer that only reports “I
could not find …” is an unverified negative result, not completion. The failure
form includes passive voice (“could not be read”), and a qualifier does not turn
a category into a file coordinate: “no Go source files” is still a category
absence, not a concrete file claim.

A report scoped to what the probes surfaced is not a repository-wide absence
claim. “No test files were surfaced in the probes — worth verifying” describes
this run's observations and names the limitation; it completes. The same
sentence with a global scope (“no test files exist in the repository”) is still
an absence conclusion and needs coverage. “Not evaluated”, “not checked”, “no
evidence”, and “worth verifying” are explicit lower-assertion forms. A claim
that a check did not run (“no tests were run”) is a fact about the run, not a
repository property. The same holds for a change report: “no files were
modified” describes what the run did to the workspace. It is not an absence
claim about repository contents, and no file scan could ever cover it.

A **local configuration** claim is different again from an **external state**
claim. A workflow file proves what is configured: the schedule, the trigger, the
command the job runs. It does not prove that the remote executed it. An answer
that turns local evidence into a remote fact ("the workflow ran successfully
today", "it was deployed", "the release is published") is reviewed unless the
run observed the remote surface itself: a knowledge call, an MCP capability, or
a command that talked to a remote (`gh`, `git push`, `git ls-remote`, `curl`,
`ssh`, a cloud CLI). A sentence that names the claim only to report it as
unverified ("I cannot establish whether it ran") is an epistemic report, not a
claim, and completes.

### 4.6 Claim scope

The evidence class a claim relies on is part of its meaning:

```text
go.mod / package.json     dependency / language declaration
docker-compose.yml        declared topology
README / docs             what the project says about itself
source read               what the implementation does
successful execution      what actually ran
directory listing         names and sizes, not content or line counts
```

A runtime-behavior claim (“cart validates against product”, “order publishes
events”, “delivery fulfills”) needs a source observation containing the
behavior; a dependency or topology declaration is not enough. Documentation
attribution (“the README describes …”) and an explicit limitation (“not
verified”) are lower-assertion forms and pass. A configuration mechanism can
support its own claim: a Compose mount into `docker-entrypoint-initdb.d` proves
that PostgreSQL init scripts are supplied, even without a source read.

A concrete line count needs the file's read `totalLines` (or a line-count
command). A directory listing's byte size is not a line count; a gross
contradiction between the answer and the observed total is reviewed. An
independent-deployment conclusion needs a Dockerfile, Kubernetes, Helm, or
deploy manifest observation; separate Compose service declarations are
topology, not a deployment boundary. A path-scoped inference (“`supabase/`
likely contains schema/edge functions”) needs the inferred category in an
observation of that path; a general inference with no path is left to the
evidence and provenance layers.

The claim audit applies to informational answers only. A mutation answer is
judged by target, mutation, and verification evidence, not by its prose. The
informational runtime state block carries the same boundary as a bounded line,
so the model sees it before writing the answer.

### 4.7 Completion integrity

`AssessGrounding` runs at the end of `agent.EvaluateTaskCompletion`, after
structural target/verification checks and before `COMPLETED`. It can only
downgrade. A grounding `REVIEW` becomes `NEEDS_REVIEW`; a grounding `REJECT`
(discussed below) is never reachable in production because semantic admission
already refused the action.

The completion evaluator still owns truth. Grounding does not replace target
snapshots, mutation evidence, verification scopes, or the evidence-sufficiency
veto.

## 5. Escalation thresholds

| Risk | Behaviour |
| --- | --- |
| Low-risk ambiguity (actionless context, observation only) | Continue; no interruption; completion checks the answer |
| Material ambiguity that affects mutation (invented mutation, unresolved scope) | Block the mutation before the security boundary |
| Unsupported conclusion in the final answer | `NEEDS_REVIEW`, with the reason recorded |
| Authority violation | Existing security gate: `DENY` |

The runtime does not ask the user to approve every interpretation. It blocks
only actions that would change state under an ungrounded objective; ordinary
reading and clarification remain uninterrupted.

## 6. `$sup` decision

`$sup` is **not** called automatically.

The existing `$sup` path is deliberately context-light: minimal system prompt,
the user query, no tools, no repository context, no main history. It can judge
whether a sentence paraphrases another sentence, but it cannot judge whether a
repository-wide negative conclusion is supported by evidence, because it never
sees the evidence. Using it as a completion judge would make it a second model
call on the common path and would still require the runtime to validate its
answer.

Deterministic checks cover the failure class that motivated the layer:
entities, action, scope, ambiguity, and negative-evidence coverage. `$sup`
therefore remains a human-invoked ephemeral surface. A host that wants a
semantic second opinion can still ask `$sup` explicitly; it is not on the
request path.

## 7. Cost

- No extra model call.
- No extra tool call.
- One deterministic extraction pass (roughly 0.09 ms on the benchmark host,
  versus model latency).
- A compact grounding line in the existing runtime state block; the measured
  estimate is ~116 tokens per request in the benchmark corpus.
- No repository index, no embeddings, no remote service.

## 8. Limits

- Extraction is finite and regex-backed. It preserves explicit lists and
  coordinates; it does not understand every noun phrase.
- A concept absence is not proven by a generic file scan. This is intentional:
  documentation or tests may exist in a form the scan cannot name. The only
  exception is Go's mandatory `_test.go` suffix, checked against a complete Go
  file scan.
- The answer check is English-pattern based. A paraphrased global conclusion,
  an unusual behavior verb, or an inference without an explicit marker outside
  the patterns is not detected; the structural completion rules still apply.
- Claim scope depends on the observations in the tool turns. Content supplied
  only in the user's `@` file context is not a tool observation, so a behavior
  claim about it may be reviewed; the conservative direction is a review, not a
  silent acceptance.
- The external-state check matches event vocabulary near a remote or CI subject.
  A descriptive sentence it cannot classify as configuration ("the release is
  published by CI") may be reviewed, and a remote observation proves only that
  the run talked to a remote, not that the specific claim is true. Both
  directions can only downgrade completion.
- The evidence gate requires a content-bearing observation, not a relevant one.
  It can establish that the run looked at the repository; it cannot judge whether
  the file it read answers the question.
- `$sup` is not an automatic escalation path.
- The layer narrows action space and completion; it does not make a wrong but
  verified change semantically correct.
