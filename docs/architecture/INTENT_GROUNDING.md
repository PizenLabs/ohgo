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
answer. A tool call that accompanies a substantive answer is not an
action-fidelity rejection when the stage withholds it or the run already has
the exact observation: it is continuation after the answer and is dropped
before dispatch rather than executed or returned as a tool error (the closure
contract lives in [`ARCHITECTURE.md`](ARCHITECTURE.md)). If the recovery also
proposes only refused actions and emits no answer, the loop closes the
still-open action surface and makes one answer-only request instead of
blocking. The same recovery applies when the model stops with no text at all
immediately after a refusal-only turn: an empty stop is not an answer, so the
loop drops the empty assistant message and asks once for the answer from the
evidence. Nothing can execute, so the evidence already gathered is the only
basis for the answer; completion still decides whether it is sufficient.

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
not exist. A **root-file** absence (`README`, `LICENSE`, `package.json`, …) is
narrower: a complete root listing covers it, and the claim subject may carry the
artifact noun ("no LICENSE file") without becoming a category claim. The claim's
scope is its own sentence, with parenthetical asides removed; a repository word
in a neighbouring sentence or in an explanation ("nested backend/.git (root repo
ignores backend/)") does not promote it to a repository-wide claim. A concept
claim (“no documentation”, “no tests”)
cannot be proven by a file scan and stays `NEEDS_REVIEW`. The one narrow
exception is Go's mandatory `_test.go` suffix: a complete recursive scan of
every `.go` file that contains no `_test.go` file proves the narrowed claim “no
Go test files”. A complete, unscoped, `CURRENT` `structure` tests inventory
with scope, inventory, and structural completeness all `COMPLETE`, an
untruncated result, and zero test files is accepted as the structural
equivalent of that scan. A bare “no tests” claim, and frontend test absence
where naming is convention-based, remain concept absences. A snapshot that
reports `scope: PARTIAL`, an excluded root, a pending parse, unsupported
source, or `STALE` freshness cannot prove a repository-wide absence. A
metadata overview is not a filtered test inventory, even when it prints
`tests: 0`. An answer that only reports “I could not find …” is an unverified
negative result, not completion. The failure
form includes passive voice (“could not be read”), and a qualifier does not turn
a category into a file coordinate: “no Go source files” is still a category
absence, not a concrete file claim.

A manifest declaration ("no test scripts declared", "package.json declares no
test script") reports what the observed declaration list contains; it is not a
repository-wide existence claim, and the source-coverage gate does not review
it. It is also not proof that tests do not exist: a static test inventory
reports "0 test files", which is a measurement of what the inventory could
associate, not a concept absence. A frontend test absence remains unproven
because naming is convention-based.

A report scoped to what the probes surfaced is not a repository-wide absence
claim. “No test files were surfaced in the probes — worth verifying” describes
this run's observations and names the limitation; it completes. The same
sentence with a global scope (“no test files exist in the repository”) is still
an absence conclusion and needs coverage. “Not evaluated”, “not checked”, “no
evidence”, and “worth verifying” are explicit lower-assertion forms. The bound
may also sit in the claim itself: “no tests detected in inspected scope”,
“nothing in the files read”, “the test inventory wasn't queried”. Those name
what was inspected or what never ran, so they are reports, not repository
conclusions. The global-scope veto outranks every bounded form: one bounded
phrase cannot excuse a repository-wide claim in the same sentence. A claim
that a check did not run (“no tests were run”, “no tests, server, or binary were
executed”) is a fact about the run, not a repository property. The coordinated
form distributes the negator over its items; a separate existence predicate
(“no tests exist, server was executed”) keeps the repository conclusion. The
same holds for a change report: “no files were modified” describes what the run
did to the workspace. It is not an absence
claim about repository contents, and no file scan could ever cover it. An
inspection report follows the same rule: “no source files were read beyond the
README” reports this run's observations, not that the repository has no source
files.

A behavior condition ("redirects to `/cart` when the cart is missing") is a
branch description, not a repository absence. A package directory does not
refute a claim about a check inside it: `backend/productcatalog/` does not
establish that a productcatalog existence check is present. An explicit open
question and next read ("stays open pending a read of …") is a limitation, not
an unsupported claim.

A **local configuration** claim is different again from an **external state**
claim. A workflow file proves what is configured: the schedule, the trigger, the
command the job runs. It does not prove that the remote executed it. An answer
that turns local evidence into a remote fact ("the workflow ran successfully
today", "it was deployed", "the release is published") is reviewed unless the
run observed the remote surface itself: a knowledge call, an MCP capability, or
a command that talked to a remote (`gh`, `git push`, `git ls-remote`, `curl`,
`ssh`, a cloud CLI). A sentence that names the claim only to report it as
unverified ("I cannot establish whether it ran") is an epistemic report, not a
claim, and completes. The same holds for a comparison or report the run cannot
offer (“there is no static-vs-runtime comparison to report”, “no runtime
behavior to describe”): the noun names the missing analysis, not a repository
artifact. “I make no claim about test coverage” is the same kind of limit, not a
statement that the repository has no tests. The same applies to a named
identifier the answer explicitly disowns (“No claim is made about
`cart.domain.Product`”): the name is the subject of the disclaimer, not an
assertion about the repository. The same is true of a negated
event: “no server/database was executed” reports non-occurrence, not use. A
negator that introduces a coordinated subject list distributes over every item,
so “no tests, server, or binary were executed” reports what the run did not do.
The negator governs its own clause only: a clause with its own predicate ends
the scope, so “no tests were run, but the deployment was executed” is still a
claim about the deployment. A discourse marker followed by a determiner (“No,
the server was executed”) is not a list and stays a claim.

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

A shell content read (`cat`, `sed`, `head`, `tail`, `nl`, `bat`) is the same
source observation as the `read` tool: the model saw the bytes, so the claim
and evidence audits credit it. A paged read is content, not a whole-file
measurement; only a whole-file read or a line-count command establishes a
file's total.

The runtime keeps the claim classes explicit. The status is a projection of the
same evidence ledger the answer audit uses, never a second evidence store:

| claim class | evidence it requires | coverage it needs |
| --- | --- | --- |
| project identity | documentation, configuration/manifest, inventory, or source | the project the answer names |
| topology | configuration or structural inventory | the named snapshot and scope |
| configuration | a configuration or manifest observation | the configuration path |
| implementation status | source content for the named component | each named component; whole-component completeness needs a current complete source scope |
| dependency use | `DECLARED` < `IMPORTED` < `INITIALIZED` < `USED_IN_SOURCE` < `OBSERVED_AT_RUNTIME` | the component that owns the evidence |
| behavior | source content or live runtime observation | the named component; a named port must appear in the same observation |
| runtime state | a live runtime observation | the named component's live process |
| structural relationship | a structural observation or source call site binding both endpoints | both named endpoints |
| absence | current complete untruncated scope, complete inventory, relevant query | the exact file or complete repository scope |
| test inventory | a complete current unscoped test inventory | the test inventory scope |

The project name a document establishes in its title or opening definition is
identity. Documentation is the required evidence for a project-identity claim,
so the answer may name the project without attributing an implementation fact.
The rest of the document's technical vocabulary is a documented claim about the
implementation and stays attribution-bound.

A runtime-behavior claim (“cart validates against product”, “order publishes
events”, “delivery fulfills”, “code listens on :8082”, “the worker connects to
Redis”) needs source content that carries the behavior, or a live runtime
observation that carries it. A named port must appear in the same source
observation or runtime address; reading product-service's listener does not
prove that cart-service uses a different port. A runtime observation without a
port can support “connects” only when the observed process output carries that
behavior. A dependency, topology, or documentation declaration is not enough.
A transitive “is running the job” claim is behavior evidence, not the live
process predicate.

A runtime-state claim (“the service is running”, “is live”, “is healthy”) is
different again: configuration declares that a service exists and source
describes what it would do, but only a live runtime observation establishes
that a process is running now. A source observation never implies runtime
execution.

A concrete test count (“the project has 120 tests”, “there are 42 test files”)
is a test-inventory claim. It needs a complete, current, unscoped test
inventory; a README, metadata overview, partial query, or stale snapshot cannot
establish the count. Zero is a valid observed count only under the same
completeness gate that supports test absence.

A structural relationship claim (“Order Checkout calls payment.Authorize”)
needs an observation that binds both endpoints: a structural graph/reference
result, or a source call site containing both names and the call syntax.
Reading one endpoint is not evidence of the relation, and documentation or
configuration is not a code relationship. A port in the behavior clause must appear in the same source
observation or runtime address: reading product-service's listener does not
prove that cart-service uses a different port. Documentation attribution (“the
README describes …”) and an explicit limitation (“not verified”) are
lower-assertion forms and pass. A configuration mechanism can support its own
claim: a Compose mount into `docker-entrypoint-initdb.d` proves that PostgreSQL
init scripts are supplied, even without a source read.

Configuration, manifests, documentation, directory listings, and structural
inventory are not implementation content. A broad project survey therefore
needs at least one source read; documentation plus metadata cannot close it.
The evidence ledger and the answer audit use the same classification, so the
model's pre-answer state block can report `implementation=NOT_OBSERVED` and
`runtime=NOT_OBSERVED` instead of letting a configuration file stand in for
execution.

An implementation-status claim (“fully implemented”, “stubs (health checks
only)”, “mock data”, “scaffolded”, “order-service implements a checkout API”,
“auth is mock”) is a positive claim about source content. A README, manifest,
Compose file, directory listing, glob result, or structural inventory does not
establish it. The plain verb form counts because it attaches a delivered object
to a component; the copula form counts because the adjective asserts the
component's state. The audit extracts the components the claim
names — a kebab coordinate such as `product-service`, an explicit list after
the colon (`Fully implemented: …`, `Stubs: …`), or a recognized component
noun — and requires a source read or
source-scoped grep for each. An adjective-first status names its subject after
the adjective ("feature-complete UI" is the frontend), and a negation that binds
to the status ("not production-ready", "is not fully implemented") asserts the
inverse of a completion claim, so it is not audited as one. Reading
product-service source supports
product-service, not order-service or the frontend. Component identity is
derived from the path's project structure: a file named `api.go` inside
`cart/` is cart, not the API gateway, and an absolute user directory is never a
component.

The subject is bounded so the audit cannot invent a coordinate. It ends at the
copula that introduces the predicate, so "go-ecommerce is a full-stack
e-commerce platform, currently a work-in-progress scaffold" is a statement
about the project, not claims about `go-ecommerce`, `full-stack`, and
`e-commerce`. A hyphenated token must carry a component role
(`product-service`, `api-gateway`, `admin-console`); a project name or a domain
adjective does not qualify. A route (`/admin`) is a screen, not a coordinate,
and a colon inside a parenthetical does not start a component list. Every item
of an explicit colon list must itself be a component name, because the same
colon often introduces an explanation ("a scaffold: a React storefront and a Go
microservices backend") rather than subjects. An em-dash clause after the names
explains the status and is not part of the list: "Stubs (health-check only):
api-gateway, cart-service — plain net/http" still names both services. A
surface noun
(`frontend`, `backend`, `client`, `web`, `ui`, `storefront`) owns the feature
phrase it qualifies: "frontend auth" is the frontend, since a path under
`src/` derives to the frontend, never to `auth`.

Strong completeness wording (“fully implemented”, “completely implemented”,
“feature-complete”, “production-ready”, “fully functional”) additionally
requires an observed, current, complete source scope for the named component:
a scoped structural file inventory with every completeness axis and every
returned source file read, or an equivalent complete unpruned component-scoped
glob. One source file supports a claim about the operation observed in that
file, not a claim that the whole component is complete. When the scope is not
observed, the admissible form is narrower: “the inspected handler implements
the observed CRUD endpoints” or “I have not verified whether the component is
fully implemented.”

A capability list inside a status claim is audited separately. “Product-service
is fully implemented with CRUD, Redis caching, graceful shutdown, and structured
logging” asserts each named capability; a complete component scope proves what
was inspected, not that every capability is present. The list is read from a
“with/including/featuring” clause or from a parenthetical after the claim
(“implements a checkout API (idempotent order creation, status updates)”). Each
aspect needs source
evidence: CRUD verbs, a cache operation, shutdown/signal handling, a logging
client, and so on. A capability outside the bounded vocabulary remains covered
by the ordinary implementation and dependency audits. A mock, scaffold, or
placeholder claim needs the marker in the component's source, not merely a read
of the component path.

Dependency claims are a separate category with their own strength ladder:
`DECLARED` (manifest/configuration), `IMPORTED` (source import), `INITIALIZED`
(constructed client or opened connection), `USED_IN_SOURCE` (a concrete source
operation), and `OBSERVED_AT_RUNTIME`. Wording must match evidence strength:
“declares” needs a manifest/configuration, “imports” needs an import, and
“uses/caches/publishes/consumes” needs a source operation or runtime
observation. The evidence is bound to the component that owns the file:
product-service's Redis operations do not support “order-service uses Redis.”
A README statement is documentation, not dependency evidence.

A concept-level negative conclusion (“JWT is not implemented”, “RabbitMQ is
unused”, “there is no authentication”, “no database integration”, “no real
JWT/auth”, “no RabbitMQ client code”, “no gRPC server wired up”, “No JWT,
RabbitMQ client code, …”) is not
proved by a complete file inventory or one empty search. It remains
`NEEDS_REVIEW` unless the answer bounds it to what was inspected (“no JWT
implementation was found in the inspected authentication source”, “the
inspected source did not show RabbitMQ operations”) or explicitly says it could
not verify the claim. File and test absence keep their stricter completeness
gate; a concept absence has no deterministic proof mechanism in this layer.
A bare “no X” list item that names no implementation mechanism (“no
Cache-Control”, “no database”) is an inventory fact the run demonstrated and is
not promoted to a concept absence. Only the leading item of an absence list is
read as the conclusion (“No JWT, RabbitMQ client code, …”); a bare “no X”
elsewhere in the sentence stays with the inventory facts.

A concrete line count needs the file's read `totalLines`, a whole-file shell
read, or a line-count command. A `wc -l` measurement is bound to the path on
the same output line; another file's count cannot satisfy the claim. A paged shell read reports the page, not the
file total, and a phrase such as "first ~90 lines" is a read window rather than
a total. A count inside a parenthetical annotation belongs to the path that
introduces the parenthesis ("`order_factory.go` (read, 97 lines) and
`order.go` (read, 413 lines)"), not to the next path in the sentence. A directory listing's byte size is not a line count; a gross
contradiction between the answer and the observed total is reviewed. An
independent-deployment conclusion needs a Dockerfile, Kubernetes, Helm, or
deploy manifest observation; separate Compose service declarations are
topology, not a deployment boundary. A path-scoped inference (“`supabase/`
likely contains schema/edge functions”) needs the inferred category in an
observation of that path; a general inference with no path is left to the
evidence and provenance layers.

An explicit source citation (`path`, optionally with a line range) is supported
by a whole or paged read, a shell content read, or a grep-shaped observation
that reports `path:line: content` for a line inside the cited window. A
listing or `find` result that only names the path is not content; an observed
match outside the cited window does not support it. A sentence that names the
path only to say it was not opened, read, or inspected is a limitation, not a
citation. A package- or type-qualified reference (`checkout.New`,
`StockReserver.Reserve`, `httpHandler.Checkout`) is supported by its package
declaration, interface block, or receiver method in the observed source even
though the dotted spelling never appears there. Runtime status tokens
(`NOT_OBSERVED`, `PARTIAL`) are the runtime's vocabulary, not code entities.

The claim audit applies to informational answers only. A mutation answer is
judged by target, mutation, and verification evidence, not by its prose. The
informational runtime state block carries the same boundary as evidence-derived
statuses: observed implementation, runtime, configuration, documentation,
inventory, structural relationships, source paths, dependency strength, scope,
and the test-inventory support state. The same projection exposes per-class
coverage (`SUPPORTED`, `PARTIAL`, `NOT_OBSERVED`) so the planner and the answer
audit agree about which claim classes the current ledger can justify. It also carries a bounded, actionable evidence frontier derived
from the observations themselves: which components lack source content, whether
the test inventory is complete, whether scope is partial or truncated, and
whether dependency evidence is only declared or imported. The frontier never
hardcodes project names; component identities come from the structural and
source coordinates. The model sees what the run can claim before writing, and
the completion audit still reviews a claim that exceeds it. The block also names
the documentation-only vocabulary — terms the documentation uses and the
observed source does not — so the answer can attribute them or inspect the
source that carries them before the correction boundary. The observed-term
comparison scans the full retained observation; it is not capped by the bounded
output vocabulary, because a term past the output cap is still observed source.
A repository-wide grep is attributed by the files it matched, not by the
directory it searched: documentation sections in the same result do not
corroborate an implementation term, and matched source sections do.

### 4.6.1 Investigation requirements (evidence facets)

A claim class says what an answer sentence may assert. An **evidence facet** says
what the control plane requires before the task may reach an answer boundary.
Facets are derived from the task shape, not from a model turn and not from the
presence of a tool. They are the deterministic answer to "does this task need
Structure Core?".

The policy is deliberately narrow. A broad account ("inspect the architecture",
"what is this project") remains an overview. A relationship claim is explicit by
vocabulary or named target: dependency/depends, caller/callee/call graph,
import, reference, implement/interface/adapter, impact, request/data flow, or a
"who calls" question. Only then does the shape acquire facets and force the
structural impact depth.

| facet | required observation | not enough |
| --- | --- | --- |
| target | a current structural coordinate identifying the entity or module | a file name or a lexical hit |
| relationship | a structural edge/reference binding two endpoints | source text that mentions both names |
| interface boundary | an observed IMPLEMENTS/EXTENDS edge or named boundary | an interface word in prose |
| implementation | observed source content or a concrete IMPLEMENTS/OVERRIDES edge | a manifest, README, or declaration |
| flow | an observed call/reference chain from the target | a documentation statement about flow |

Facet requirements are added to `TaskShape.RequiredKnowledge` before supporting
repository requirements. That ordering is the repair policy: mandatory intent
facets first, then direct claims, then supporting/enrichment claims. An
observation is tagged with the facets it actually establishes from structured
result fields; `EvidenceState` derives the status from those tags, with bounded
fallbacks for older checkpoints. Every facet status carries the observation ids
and snapshot provenance that satisfy it.

Sufficiency is therefore:

```text
required facets satisfied
AND no unresolved mandatory requirement
AND capability availability accounted for
```

Confidence remains a provider-local estimate of trust. It is not a completion
predicate. The `investigate` tool's own result is labelled
`provider_sufficient` when its planner reaches its confidence target, with an
explicit note that the control plane's facets still gate task completion.

The model can propose a structural query or a symbol. It cannot decide that a
relationship task does not need structural evidence.

### 4.7 Completion integrity

`AssessGrounding` runs at the end of `agent.EvaluateTaskCompletion`, after
structural target/verification checks and before `COMPLETED`. It can only
downgrade. A grounding `REVIEW` becomes `NEEDS_REVIEW`; a grounding `REJECT`
(discussed below) is never reachable in production because semantic admission
already refused the action.

The completion evaluator still owns truth. Grounding does not replace target
snapshots, mutation evidence, verification scopes, or the evidence-sufficiency
veto.

### 4.8 Recovery integrity

Tool recovery preserves the evidence lifecycle:

```text
recovery
  → evidence update
  → answer audit
  → completion evaluation
```

A request-level rejection (invalid argument, invalid selector, unknown tool,
unsupported operation, authority denial, budget exhaustion, stale evidence) is
diagnostic history, not a repository fact and not a capability outage. It must
not poison unrelated provider health or block a task that still has an
independent evidence path. A corrected invocation may add valid evidence; the
earlier failure remains in the durable ledger. Recovery never fabricates the
evidence the failed call would have produced, never erases a valid observation,
and never promotes a task to `PROVEN` by itself. Only the completion evaluator,
using the answer and the evidence contract, can do that.

An answer correction is a bounded, claim-level recovery, not a second
investigation. Each repairable audit issue carries the claim, its location, its
category, the evidence class it needs, a reference, and the permitted action.
The correction names at most `MaxRepairInstructions` claims and tells the model
to keep every other sentence, reuse the observations already in the
conversation, and stay within the answer lifecycle's tool allowance
(`CorrectionToolCalls`, default 4). The directive is re-rendered on every
request in the correction window, not only the first: a window can span several
model turns, and a later turn without the named claims follows an unrelated
evidence frontier. Calls beyond the allowance fail closed before execution; a
reused observation does not consume it. The allowance is
per answer lifecycle, not per correction request, so a second repair pass cannot
reopen the investigation the initial budget already paid for. When the
allowance is spent, the projection closes the tool surface and states the
exhausted budget instead of advertising a call the loop will refuse.

A correction also withholds the generic evidence frontier carried by the
informational state block. The review already named the claims to repair; a
component frontier derived from unrelated structural inventory competes with
that bounded instruction and can spend the remaining allowance on the wrong
coordinate. The claim classes and documentation-only vocabulary remain visible,
because those are the repair's evidence boundary.

The pre-answer review also runs the documentation-only provenance audit when the
host supplies the last projection's provenance, so a documented claim is
repaired before completion instead of surfacing as a terminal review. The
correction is evidence-first: the reopened tool surface exists to obtain the
missing observation, and a generic coverage disclaimer does not satisfy a
specific claim. When the evidence still cannot be obtained, the model qualifies
or removes only that sentence; the runtime never accepts the claim to finish
the task.

The corrected answer is reviewed by the same audit before publication. A repair
can introduce a new unsupported claim, so a second correction pass is admitted
only when the review names a different claim set (`answerRepairKey`); repeating
the same rejection terminates at `NEEDS_REVIEW`. `MaxAnswerCorrections` bounds
the passes. This is a convergence rule, not a retry loop: an unchanged claim set
earns no further turn.

A correction is claim-preserving. The candidate carries an `AnswerDraft` whose
claims are individually `PROVEN`, `DOCUMENTED`, or `REPAIR_REQUIRED`; only the
refused claims enter the repair request, and the independently supported claims
are named as preserved context. A revision that drops them is not published as
a poorer answer: the runtime commits the strongest draft with only the refused
claims removed. A candidate that a correction supersedes is internal repair
context. It is removed from the answer projection and is never rendered as a
draft the user must read; only the candidate that ends the review loop can be
shown. When correction is exhausted and no valid text remains, the final
candidate is not published. The response transaction records `REVIEW` and the
host removes the provisional stream from the answer projection; it may keep
that one terminal candidate as a labelled withdrawn draft so a streamed answer
does not vanish without a trace. The uncertified claims are carried as
`ReviewIssues` into `NEEDS_REVIEW`. A
facet-only rejection is the exception: the answer may honestly state the
missing deliverable, so the finalizer publishes it and completion records the
facet gap. Deleting sentences and appending a runtime-authored "Evidence
limitations" section
produced a second, competing conclusion; it also removed supported detail the
user asked for. The model's raw candidate remains in the completion input so the
review can name exactly what was not established, but no host may display it as
the final answer. The answer-finalization gate (`AnswerAudit.Approved`) and the
completion authority use the same claim audits, so the transcript, the response
transaction, and the completion verdict cannot disagree about whether the
published text was approved.

Coverage is presentation, not a claim. The turn footer reports measured
structural coverage, but an answer is never rejected merely because it does not
repeat a coverage disclaimer. A project-wide sentence is judged by the claim it
makes and the evidence that claim requires; requiring a disclaimer taught the
model to qualify prose instead of acquiring the missing observation.

A paged read keeps the document's opening window. Different windows are not a
conflict, and a later window no longer overwrites the definition that names the
project. The merged value is bounded and is not completeness evidence.

## 5. Escalation thresholds

| Risk | Behaviour |
| --- | --- |
| Low-risk ambiguity (actionless context, observation only) | Continue; no interruption; completion checks the answer |
| Material ambiguity that affects mutation (invented mutation, unresolved scope) | Block the mutation before the security boundary |
| Unsupported conclusion in the final answer | the claim is withheld and a bounded limitation is published; an unresolved required claim still ends as `NEEDS_REVIEW` |
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
- Implementation-status subject extraction is finite: it recognizes kebab
  coordinates, an explicit list after the status marker, and a bounded
  component vocabulary. Component identity is path-derived; an ambiguous path
  is left as a weaker aggregate rather than guessed. A strong completeness
  claim without a scoped component inventory is reviewed, not silently
  downgraded.
- Dependency detection uses a bounded technology vocabulary and
  language-shaped import/constructor/operation markers. A dependency outside
  the vocabulary is not recognized by this layer; the evidence and provenance
  gates still apply.
- Runtime-state detection is English-pattern based and distinguishes the
  live-process predicate (“is running”) from transitive behavior (“is running
  the job”). A paraphrase outside the pattern is left to the behavior and
  provenance gates.
- Structural-relationship detection requires a dotted endpoint, an explicit
  relation verb, and a code-shaped operand on the other side (a dotted
  coordinate or an identifier with an uppercase letter). A conjunction or a
  plural noun that happens to sit next to the verb (“… and calls
  s.stock.Reserve”, “the Save calls are state transitions”) is prose, not a
  claim; a paraphrase or a prose relation without a code coordinate is not
  audited here. The structural and provenance gates still apply.
- Test-count detection recognizes an explicit project/inventory count. A count
  embedded in an unusual phrase may escape the pattern; a recognized count
  without a complete current inventory is reviewed, never accepted.
- Implementation-aspect detection uses a bounded capability vocabulary. A
  capability outside the vocabulary is not forced into an aspect check; the
  component-level status and source-scope rules still apply.
- Concept absence uses a bounded vocabulary and scope markers. A paraphrased
  unbounded absence can escape detection; the gate is conservative in the
  review direction, never an absence proof.
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
