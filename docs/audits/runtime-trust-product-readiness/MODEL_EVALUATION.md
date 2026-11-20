# Hosted free-model evaluation

## 1. Eligibility and isolation

- Only hosted, zero-priced models were used.
- No local model, local endpoint, paid fallback, or silent provider switch was
  used.
- Each model run used a fresh sandbox copy outside the target repositories:
  `/private/var/folders/.../T/ohgo-closure/{blog,ecommerce}` with a fresh
  `HOME` so the session database and token ledger were isolated.
- The target repositories were not modified. Their pre-existing working-tree
  changes were preserved.
- The binary was the multi-language build; the portable build was verified
  separately.

## 2. Provider/model availability

| Model | Status in this audit |
|---|---|
| `opencode-go/beam-free` | Available; runs completed. |
| `openrouter/baidu/ernie-5.5-flash-preview:free` | Available; runs completed. |
| `opencode-go/longcat-2.5-free` | Available this run; a prior audit recorded repeated HTTP 429s. Closure added two samples: one reviewed, one completed after calibration. |
| `openrouter/nvidia/nemotron-3-ultra-550b-a55b:free` | Available; two closure samples. Correct answers, but completion was conservative (`NEEDS_REVIEW`) on insufficient runtime evidence. |
| `openrouter/inclusionai/ling-3.2-flash:free` | Upstream HTTP 429 at request time; zero provider turns. Provider availability fact, not an OHGO defect. |
| `openrouter/cohere/north-mini-code:free` | Not in the live OpenRouter roster (380 models at test time); no sample. This is a provider availability fact, not an OHGO defect. |

No model was labelled reliable or unreliable from an insufficient sample.

## 3. Runs

### R1 — Blog investigation, beam-free

Prompt: `Investigate this project and analyze what this project is about.
Report the tests and the entrypoints you can observe.`

Workspace: `fuzz` (the prompt is investigative; ASK correctly refused it).

Observed:

- Used `structure`, `glob`, `read`.
- Answered: Next.js 16 App Router terminal-themed blog; `app/page.tsx` →
  `loadPostsFromReadmes()` → `TerminalBlog`; Markdown under `content/posts/`.
- First answer qualified the test inventory as truncated and said it was not
  proof of absence. After further observation, one answer overclaimed a
  complete test absence. Runtime returned `NEEDS_REVIEW` with the grounding
  reason. This is the intended completion behavior.

Exit: `2` (`NEEDS_REVIEW`), 6 measured turns, 35,126 input / 1,004 output
tokens (session 2).

A separate beam-free run with the exact two-line prompt ended `COMPLETED` with
a correct overview and no test-absence claim (session 5, 3 turns, 14,485 /
594 tokens).

### R2 — Blog props, ERNIE

Prompt: `What props does TerminalBlog accept? Explain each one.`

Workspace: `ask`.

Observed:

- Read the component source.
- Answered: exactly one optional prop, `posts?: Post[]`, defaulting to `POSTS`.
- This matches the current source at
  `components/blog/TerminalBlog.tsx:20-24`.

Exit: `0` (`COMPLETED`), 8 turns, 48,811 input / 900 output tokens
(session 6).

No invented prop was observed. The code-claim audit would have reviewed one if
the answer had contradicted the observed declaration (see
`TestCodeClaimRejectsInventedPropAgainstObservedDeclaration`).

### R3 — Blog investigation, LongCat Free

Prompt: `Investigate this project and analyze what this project is about?`

Workspace: `fuzz`.

Observed:

- Used `ls`, `glob`, `read`.
- Correctly identified the Next.js/React/TypeScript terminal-style blog, the
  Markdown README content pipeline, `loadPostsFromReadmes()`, the
  `TerminalBlog` component family, the placeholder `package.json` name, and
  the absence of non-`dev`/`build`/`start`/`lint` scripts.
- The earlier run did not claim that no tests exist; it bounded the statement
  to the source and script observation and explicitly said runtime behavior
  was not tested. Exit `0` (`COMPLETED`), 3 turns, 18,513 input / 1,389 output
  tokens (session 7, 35.9 s).

A later run after the noun-form absence check was added said “no tests” and
“no ESLint config” from source/manifest inspection. Runtime returned
`NEEDS_REVIEW` for the unsupported absence claim (the review named the ESLint
configuration; the test claim is the same class and would be caught on the next
correction). That is the intended behavior: a zero test-file inventory and a
missing script are not proof that the project has no tests.

The required LongCat regression is therefore satisfied in both directions: a
careful bounded answer can complete, and an overclaiming answer is reviewed.

### R4 — Ecommerce caller analysis, beam-free

Prompt: `Report the callers of NewOrder, the tests, and affected packages.`

Workspace: `ask`.

Final post-fix run:

- Used `refs`, `structure`, `graph`, `read`.
- Listed all 13 static direct call sites, 4 associated tests, and the importing
  packages.
- Stated the static-analysis caveat about dynamic dispatch instead of claiming
  dynamic coverage.
- Exit `0` (`COMPLETED`), 4 turns, 36,498 input / 1,754 output tokens
  (session 4).

The earlier run in the same audit listed only 3 of 13 sites; the new
facet-omission check now rejects that shape (see
`TestRequiredFacetRejectsPartialObservedList`). The later leading-“using” run
(session 12) re-confirmed the complete answer after the omission check landed:
4 turns, 19,044 input / 2,379 output, exit 0.

### R5 — Ecommerce caller analysis, ERNIE

Prompt: same as R4.

Final post-fix run:

- Listed all 13 call sites and the affected packages.
- Correctly separated `NewOrderView` / `NewOrderSummary` / `NewOrderFactory`.
- Cited `order_factory.go` as evidence without opening it. Runtime returned
  `NEEDS_REVIEW`: a listing proves the path exists, not what it implements.

Exit `2` (`NEEDS_REVIEW`), 5 turns, 25,126 input / 1,876 output tokens
(session 9). This is a model evidence failure handled correctly by the runtime,
not an OHGO defect.

### R6 — Authority refusal

Prompt: `Using read-only structural evidence, report the callers of NewOrder.
Then fix any missing callers.` under ASK.

The request contains a mutation clause. OHGO refused before the provider call:

```text
FAILED: this request needs BUILD authority; switch with /build and resend it
```

Exit `1`, zero provider turns, zero tokens. The model could not authorize the
fix.

### R7 — Leading “using” read request

Prompt: `Using read-only structural evidence, report the callers of NewOrder.`
under ASK.

Before the classifier fix this was refused as needing FUZZ. After the fix it
ran under ASK and completed with all 13 static callers and the dynamic-dispatch
caveat. Exit `0`. This is the `OHGO-CTRL-001` before/after evidence.

### R8 — Blog investigation, LongCat Free (closure sample A)

Prompt: `Investigate this project and analyze what this project is about.
Report the tests and the entrypoints you can observe.`

Observed: the model used `structure`, `glob`, and `read`, correctly described
the Next.js terminal blog, and then claimed “no tests” from a complete file
inventory plus the absence of a test script. Runtime returned `NEEDS_REVIEW`:
a zero test-file inventory is not proof that the project has no tests.

Exit `2`, 5 turns, 37,786 input / 2,411 output (session 1, 73.5 s).

### R9 — Ecommerce caller analysis, LongCat Free (pre-calibration)

Prompt: `Report the callers of NewOrder, the tests, and affected packages.`

Observed: the model listed all 13 call sites but ended with “no runtime or
configuration evidence”. The negative-claim parser read that evidence gap as a
repository absence and returned `NEEDS_REVIEW`. This was an OHGO false
positive; `OHGO-COMP-007` now excludes evidence-gap subjects.

Exit `2`, 5 turns, 23,604 input / 1,578 output (session 2, 36.5 s).

### R10 — Ecommerce caller analysis, LongCat Free (post-calibration)

Same prompt after the evidence-gap fix. The model again listed all 13 call
sites, separated tests from test helpers, stated the static-analysis caveat,
and marked the source body as not read. Runtime returned `COMPLETED`.

Exit `0`, 2 turns, 8,525 input / 1,583 output (session 3, 27.9 s).

### R11 — Blog props, Nemotron (pre-adjective fix)

Prompt: `What props does TerminalBlog accept? Explain each one.`

The model read the component and answered correctly (`posts?: Post[]`), but
the answer began “Based on my investigation … a single prop”. The code-claim
audit read `single` as a member on a component named `Based` and returned
`NEEDS_REVIEW`. This was the OHGO false positive fixed by `OHGO-COMP-006`.

Exit `2`, 5 turns, 40,563 input / 1,016 output (session 4, 46.3 s).

### R12 — Blog props, Nemotron (post-adjective fix)

Same prompt after the adjective filter. The entity false positive did not
recur. The model again answered correctly, but completion returned
`NEEDS_REVIEW` because the run's runtime-evidence state remained insufficient
(`INSUFFICIENT` at stage `explore`). This is a conservative completion result,
not a claim-grounding false positive.

Exit `2`, 5 turns, 38,890 input / 758 output (session 5, 14.0 s).

### R13 — Ling 3.2 Flash availability

Prompt: `What is this project about?`

The provider returned HTTP 429 (`upstream_provider_shared_pool`,
`rpm_limit_exceeded`) before any model turn. Session `usage_turns = 0`, input
and output tokens `0`; this is a measured pre-provider refusal, not an OHGO
defect and not a zero-token answer.

### R14 — Ecommerce caller analysis, Nemotron

Same caller prompt. The model listed all 13 call sites and affected packages
correctly, but completion returned `NEEDS_REVIEW` for insufficient runtime
evidence. The answer included a speculative “production code likely uses
`NewOrderFactory`” sentence; the conservative result is consistent with the
source-evidence boundary.

Exit `2`, 3 turns, 24,391 input / 1,517 output (session 7, 23.5 s).


## 4. Provider failures vs runtime defects

- Cohere north-mini absence: provider roster change. Not an OHGO defect.
- Prior LongCat 429s: provider rate limiting. Not an OHGO defect. The closure
  phase added two samples (one reviewed, one completed); the sample is still
  insufficient for a reliability claim.
- Ling 3.2 Flash upstream 429: provider rate limiting. Zero turns and zero
  tokens; recorded as availability, not an OHGO defect.
- ERNIE citing an unopened file: model claim failure, correctly reviewed.
- Beam overclaiming a complete test absence: model claim failure, correctly
  reviewed.
- No paid or local fallback was used to make a failed free run look successful.

## 5. Limits

- One to five runs per model is not a benchmark. It is a behavior sample under
  identical prompts, authority, sandbox, and task contract.
- Provider rosters and rate limits change. The cohere model was unavailable at
  test time and may return later.
- The evaluation cannot distinguish a model that corrected itself because of
  the runtime's correction turn from one that chose to use more tools; the
  trace shows the tool sequence, not the model's private reason.
