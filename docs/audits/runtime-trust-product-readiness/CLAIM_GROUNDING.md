# Claim grounding

Date: 2026-11-20
Status: **improved; deterministic, not exhaustive**

## 1. What the layer owns

The completion contract decides whether an answer is publishable. Claim
grounding is the answer-side half: it asks what the answer asserts and whether
the run observed the evidence class that assertion requires.

The layers are deliberately separate:

| Layer | Question | Owner |
|---|---|---|
| Required facets | Did the answer report the deliverables the user named? | `internal/agent/facets.go` |
| Claim scope | Is a behavior, count, relation, absence, or citation supported by the evidence class it needs? | `internal/agent/claims.go` and its claim files |
| Entity association | Did the run observe the code entity the answer names as code? | `internal/agent/entity_claims.go` |
| Provenance | Is a documentation claim being presented as an implementation fact? | `internal/control/cognition/provenance.go` |
| Correction | Can one bounded turn fix a rejected answer? | `internal/agent/review.go` |

Completion remains the final authority. None of these checks grants an action,
widens authority, or declares a mutation verified.

## 2. Entity-to-evidence association

The new general check does not enumerate predicates. It asks a question that
holds for any factual sentence:

> If the answer marks an identifier as code, did this run observe that
> identifier?

Rules:

- Only explicit code spans are considered.
- A bare lowercase word is too easily prose; a qualified, underscored, or
  capitalised identifier is a concrete code coordinate.
- Boundary matching prevents `Order` from being supported by `NewOrderView`.
- The check is inert when the run has no observations; absence of a baseline
  cannot refute a resumed answer.
- Imperative advice, bounded statements, documentation-attributed statements,
  and negative observations are skipped.
- A negative existential claim about an observed entity is a contradiction,
  not an absence.

The check catches a novel predicate the vocabulary does not know:

```text
The `PaymentReconciler` harmonizes ledgers before settlement.
```

If no observation contains `PaymentReconciler`, the answer is reviewed even
though `harmonizes` is not in any behavior list.

## 3. Evidence-gap calibration

An explicit limitation is not a repository absence. The model may say:

```text
No source contents were read, so there is no runtime or configuration evidence.
```

That is a statement about this run's evidence, not a claim that the project has
no runtime or configuration. `negativeClaims` now excludes subjects and
suffixes that name the run's evidence (`evidence`, `observation`,
`information`, `data`), so an honest limitation is not reviewed as a false
negative conclusion.

## 4. Prose adjective calibration

The adjacent-identifier form (`X prop`, `prop X`) no longer accepts a lowercase
prose adjective. The real-model failure was:

```text
Based on my investigation of the source code, TerminalBlog accepts a single prop.
```

The old audit read `single` as a member on a component named `Based`. The
filter now requires an unbackticked identifier to contain an uppercase letter or
an underscore, and a small quantifier set (`single`, `optional`, `additional`,
`first`, …) is excluded explicitly.

## 5. Contradiction and uncertainty

- A positive source observation refutes an answer that says the observed entity
  does not exist.
- A complete inventory can support a file absence; a concept absence still
  needs a semantic proof this runtime deliberately does not invent.
- A bounded statement (`not found in the inspected source`) remains publishable.
- A provider count that is missing is unknown, never zero.

## 6. Real-model validation

The closure phase added these samples:

- LongCat: an overclaiming “no tests” blog answer was reviewed; a later
  ecommerce caller answer completed with all 13 call sites after the
  evidence-gap calibration.
- Nemotron: the first props answer was reviewed for the false `single`/`Based`
  identifier; after the fix, the same prompt produced no entity false positive.
  The run still returned `NEEDS_REVIEW` for insufficient runtime evidence, which
  is a completion-sufficiency boundary, not a claim-grounding defect.
- Ling 3.2 Flash: upstream HTTP 429, zero provider turns, no claim sample.

## 7. Remaining limit

The mechanism is general for explicit code entities, but it is not a semantic
parser. An unsupported claim that names no code entity, or that uses a code
entity the run genuinely observed while asserting a behavior that observation
does not establish, can still pass if no narrower vocabulary check covers it.
The next layer would be a claim classifier that associates each sentence with a
specific evidence record and a required evidence class, rather than a global
observation corpus. That is a release-hardening item, not a silent success path
in the tested shapes.
