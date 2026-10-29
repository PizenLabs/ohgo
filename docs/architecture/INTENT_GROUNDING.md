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
                                                 ambiguity / negative-claim review
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
entities[]          named projects, paths, symbols, packages
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

Repository coordinates (paths, symbols, packages) are not required in prose:
mutation completion already proves them through target evidence.

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
file absence                   ≠ concept absence
search returned nothing        ≠ concept does not exist
```

A supported file absence (for example a complete `glob "**/*"` with no
`README.md`) can complete. A concept claim (“no documentation”, “no tests”)
cannot be proven by a file scan and stays `NEEDS_REVIEW`. An answer that only
reports “I could not find …” is an unverified negative result, not completion.

### 4.6 Completion integrity

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
- A concept absence is never proven automatically. This is intentional:
  documentation or tests may exist in a form a file scan cannot name.
- The answer check is English-pattern based. A paraphrased global conclusion
  outside the patterns is not detected; the structural completion rules still
  apply.
- `$sup` is not an automatic escalation path.
- The layer narrows action space and completion; it does not make a wrong but
  verified change semantically correct.
