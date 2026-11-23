# Completion policy

## 1. Contract

Every turn carries an `IntentContract` derived from the user's own words. The
contract is data, not prompt text, and never grants authority.

A completed informational answer must satisfy:

1. **Objective** — the request kind and scope.
2. **Required facets** — the answer fields the user explicitly asked for
   (`callers`, `references`, `call sites`, `importers`, `dependencies`,
   `tests`, `test tooling`, `entry points`, `impact`).
3. **Required evidence** — a facet claim must be supported by the run's own
   observations when it is a code-level fact.
4. **Grounding** — entities, scope, action fidelity, negative claims, and
   provenance.
5. **Run shape** — no unrecovered failed call, no outstanding call, no
   protocol residue.
6. **Authority** — no mutation under a non-mutation objective.

A mutation or execution contract additionally requires the observed change and
a successful check that covers the named target. Those rules predate this audit
and are unchanged.

### 1.1 Required-facet extraction

`workspace.BuildIntentContract` records a facet only when a clause asks to
report it. A weak noun (`tests`) requires a reporting verb (`report`, `list`,
`identify`, `which`, `what`, `how many`, `count`, `find`, `show`, `include`,
`map`). “Investigate why the tests fail” therefore does not create a test
inventory obligation; “report the tests” does.

List items inherit the request shape of the leading clause:
`"report the callers, the tests, and affected packages"` is three deliverables,
not one deliverable and two incidental nouns.

### 1.2 Required evidence

`AssessRequiredFacets` returns REVIEW when:

- the answer does not report a requested facet;
- the only mentions explicitly mark it unverified, unknown, partial,
  truncated, “only N of M”, “not individually identified”, or similar;
- the answer names fewer observed items than the run found and does not state
  the total;
- the answer asserts zero but a trusted observation reports a positive count.

It accepts a bounded zero observation
(`"No test files were observed in the available inventory; test tooling was not
inspected"`) and a complete static list with a caveat about what static
analysis cannot see.

## 2. Claim grounding

Code-level claims are checked against observed evidence:

- A prop/field/parameter/member marked as code must appear in a source
  observation from this run.
- If the declaration of the named component/type was observed and the member is
  absent, that is a contradiction and a review boundary.
- An explicit code span asserted as fact must appear in a successful
  observation from this run, even when the sentence uses a predicate outside
  the behavior vocabulary. Boundary matching prevents `Order` from being
  supported by `NewOrderView`.
- A negative existential claim about an observed entity is a contradiction.
- If no observation exists, the check does not invent a review from absence: a
  resumed answer may rely on earlier evidence.

An explicit evidence gap is not a repository absence. “No runtime or
configuration evidence” describes what this run observed; it does not assert
that the project has no runtime or configuration. The negative-claim layer
excludes evidence/observation/information/data subjects for that reason.

Documentation-only terms, runtime-behavior claims, deployment claims, line
counts, test counts, and global negative conclusions keep their existing
checks in `internal/agent/claims.go`, `concept_absence.go`, and
`provenance.go`.

## 3. States

| State | Runtime meaning | Enforcement |
|---|---|---|
| `COMPLETED` | The contract is satisfied and required evidence is sufficient. | `agent.ClassifyRun` → `EvaluateTaskCompletion` → `RunCompleted`. |
| `NEEDS_REVIEW` | A material evidence or grounding gap remains, or the model produced a non-answer. | `RunNeedsReview`; the durable task records the reason. |
| `PAUSED` | A budget, deadline, turn limit, or interruption stopped the run. | `RunPaused`; a resource stop is not success unless `EvaluateBudgetCompletion` proves the contract first. |
| `FAILED` | An execution, provider, protocol, or authority failure prevented progress. | `RunFailed`; blocked capabilities are reported as `CAPABILITY_UNAVAILABLE`. |
| `CANCELLED` | The human interrupted the run. | `RunCancelled`; verified work is not relabelled complete. |

The names and transitions are the existing lifecycle vocabulary; no parallel
state system was added.

## 4. Bounded correction loop

When a candidate answer fails the answer-level review:

1. The loop records the reason in a runtime-authored directive.
2. It admits a bounded correction pass, at most `MaxAnswerCorrections` (2).
3. It reopens the loop's constructed tool set for the correction window, so the
   model can acquire missing evidence; authority and grounding still gate every
   call. The correction tool allowance is enforced silently, not announced as a
   remaining-call counter.
4. It re-runs the same review on the corrected answer. A further pass is
   admitted only when the review names a different claim set.
5. An exhausted or repeated rejection returns to the host, which reports
   `NEEDS_REVIEW`. A candidate superseded by a correction is internal and is
   never displayed; only the terminal candidate may remain as one labelled
   withdrawn draft, carrying the control-plane review reason verbatim. The
   renderer never invents or substitutes a reason.

The correction is not a completion bypass: it cannot mark the task complete,
widen authority, or turn an unsupported claim into evidence. An empty
correction directive disables the retry.

## 5. Tested examples

| Answer | Result | Why |
|---|---|---|
| “Caller count, test count, and two call sites remain unverified.” | REVIEW | Required facets explicitly unverified. |
| “13 callers, including a.go:1 and b.go:2.” | PASS | Total stated; bounded example list. |
| “3 call sites: a.go:1, b.go:2, c.go:3” when 13 were observed. | REVIEW | Partial list without the total. |
| “No test files were observed in the available inventory; tooling was not inspected.” | PASS | Bounded observation; no repository-wide absence. |
| “The project has no tests.” | REVIEW | Complete inventory does not prove concept absence. |
| “`TerminalBlog` accepts a `showDetails` prop” with only `posts` in the observed declaration. | REVIEW | Contradicts observed source. |
| “`TerminalBlog` accepts a `posts` prop.” | PASS | Present in the observed declaration. |
| A copied `# Runtime state …` block only. | REVIEW | Sanitized to empty; no project finding. |
| “The `PaymentReconciler` harmonizes ledgers” with no observation of that name. | REVIEW | Entity-to-evidence association; the predicate is irrelevant. |
| “The `PaymentReconciler` does not exist” while source contains it. | REVIEW | Negative existential claim contradicted by observation. |
| “Based on the source, TerminalBlog accepts a single prop.” | PASS after calibration | `single` is prose, not a member name. |
| “No source was read, so there is no runtime or configuration evidence.” | PASS after calibration | Evidence gap, not repository absence. |

## 6. Limits

- Claim grounding is general for explicit code entities, but it is not a
  semantic parser. It does not judge whether a supported claim is true.
- A claim that names no code entity, or that names an observed entity while
  asserting a behavior the observation does not establish, can still pass. The
  tested high-risk shapes are named in the bug ledger.
- The correction loop is bounded to one turn; it is not an unbounded agent
  retry.
- A resumed answer's prior evidence is not replayed into the code-claim check;
  the check skips when no current source was observed rather than guessing.
