# Free hosted model evaluation

## Eligibility verification

Pricing was read from the provider's live roster on 2026-11-20. Only hosted
models with verified zero prompt **and** completion pricing were eligible.
Local inference (Ollama, LM Studio, llama.cpp, local endpoints) was excluded.
No paid fallback was used.

| Model | Provider | Prompt | Completion | Context | Tools | Status |
|---|---|---|---:|---:|---:|---|---|
| `opencode-go/beam-free` | OpenCode Go | 0 | 0 | 1,048,576 | yes | eligible |
| `opencode-go/longcat-2.5-free` | OpenCode Go | 0 | 0 | 1,000,000 | yes | eligible; rate-limited in runs |
| `openrouter/cohere/north-mini-code:free` | OpenRouter (Cohere upstream) | 0 | 0 | 256,000 | yes | eligible |
| `openrouter/baidu/ernie-5.5-flash-preview:free` | OpenRouter (Baidu upstream) | 0 | 0 | 1,024,000 | yes | eligible |
| `openrouter/inclusionai/ling-3.2-flash:free` | OpenRouter (Ant upstream) | 0 | 0 | 1,048,576 | yes | eligible; repeated upstream 429, not in suite |

`opencode-go` free rows are bundled as zero input/output/cache prices and the
endpoint accepted the credential and listed 31 models. OpenRouter prices were
read from `https://openrouter.ai/api/v1/models`; the four tested rows report
`"prompt":"0"` and `"completion":"0"` and advertise `tools`. This is the
"verified zero-cost model" category, not a promotional credit or a paid model.
Groq was present in the environment but its free tier is quota-based rather
than a verified zero-priced model, so it was not used.

## Build and repository state

All final runs used the multi-language binary built from commit
`ade60d9` with the audit fixes (`sha256
775fce04c86165dca43c266f583938d70a1bf20389a44bd578b55a7e78780e99`; the
final source adds only a verify-sufficiency guard after those runs). The
repositories were sandbox copies at the recorded commits; `node_modules`,
`.next`, and pre-existing `.ohgo` were excluded. The task prompts are in
`bench/` runner output, and the raw traces are retained as JSONL per session.

Token usage is not persisted by the headless session store. The table records
latency, tool calls, error tool results, and answer size; token counts are
marked unavailable rather than estimated.

## Suite

Each model ran the same four tasks on both repositories:

1. **overview** — purpose, boundaries, entrypoints, evidence.
2. **impact** — NewOrder (Go) or TerminalBlog (TSX): declaration, callers/tests
   or prop-dependent behavior, coordinates, static-versus-runtime distinction.
3. **coverage** — supported/unsupported source, one conclusion the evidence
   does not support.
4. **docs** — checkout outbox versus inline publication (Go), or Markdown
   versus MDX and the implemented loader (blog).

## Final run matrix

`exit` is OHGO's verdict (0 COMPLETED, 2 NEEDS_REVIEW, 3 PAUSED, 1 FAILED).
`tools` is the number of tool calls. `err` is the number of failed tool
results. `ans` is answer characters.

| Repo | Model | Task | Exit | Tools | Err | Sec | Ans | Tool selection |
|---|---|---|---:|---:|---:|---:|---:|---|
| ecommerce | beam-free | overview | 0 | 6 | 0 | 8.2 | 1246 | structure, ls, read, glob |
| ecommerce | beam-free | impact | 0 | 3 | 0 | 7.1 | 1352 | symbols, refs, structure |
| ecommerce | beam-free | coverage | 0 | 6 | 0 | 13.9 | 1082 | structure, ls, read |
| ecommerce | beam-free | docs | 2 | 3 | 0 | 7.0 | 751 | grep, ls, structure |
| ecommerce | cohere | overview | 2 | 11 | 2 | 22.7 | 0 | structure/ls/read; two failed reads |
| ecommerce | cohere | impact | 2 | 2 | 0 | 10.0 | 975 | symbols, investigate |
| ecommerce | cohere | coverage | 2 | 1 | 0 | 5.5 | 978 | structure |
| ecommerce | cohere | docs | 2 | 3 | 0 | 6.1 | 0 | structure/ls/read |
| ecommerce | ernie | overview | 0 | 8 | 0 | 8.2 | 1603 | structure, ls, read |
| ecommerce | ernie | impact | 2 | 2 | 0 | 4.4 | 1618 | symbols, refs |
| ecommerce | ernie | coverage | 0 | 7 | 0 | 9.2 | 217 | structure, ls, read |
| ecommerce | ernie | docs | 2 | 6 | 0 | 8.5 | 2030 | retrieve, ls, read |
| blog | beam-free | overview | 0 | 10 | 0 | 7.1 | 1342 | structure, ls, read, glob |
| blog | beam-free | impact | 0 | 5 | 0 | 8.5 | 869 | symbols, grep, read, glob |
| blog | beam-free | coverage | 2 | 8 | 0 | 26.1 | 649 | structure, ls, read |
| blog | beam-free | docs | 0 | 3 | 0 | 5.3 | 595 | structure, grep, glob |
| blog | cohere | overview | 2 | 2 | 0 | 7.0 | 0 | structure, read |
| blog | cohere | impact | 2 | 1 | 0 | 5.4 | 0 | symbols |
| blog | cohere | coverage | 2 | 1 | 0 | 8.0 | 1241 | structure |
| blog | cohere | docs | 2 | 5 | 0 | 8.7 | 0 | investigate, structure, ls, read |
| blog | ernie | overview | 2 | 5 | 0 | 6.3 | 1406 | structure, ls, read |
| blog | ernie | impact | 0 | 5 | 0 | 9.3 | 987 | symbols, retrieve, grep, read |
| blog | ernie | coverage | 0 | 6 | 0 | 8.6 | 1190 | structure, glob, ls, read |
| blog | ernie | docs | 2 | 2 | 0 | 9.2 | 100 | ls, read |

LongCat's attempted runs are retained: two ecommerce tasks completed, two
were cut off by provider 429; no blog runs completed. It is reported as a
provider restriction, not as an OHGO verdict.

## Model findings

### beam-free

- Correctly used `refs` and `structure` for the Go impact task in the final
  run: 13 callers, 4 tests, two correct coordinates.
- Correctly identified Markdown (not MDX), the loader, and the outbox staging
  path from source.
- Correctly separated discovered entrypoints from verified execution.
- One review verdict on ecommerce docs because it did not read the standalone
  docs; it stated that limitation instead of inventing a comparison.
- Blog overview described `content/` as "likely MDX" before reading source;
  that was corrected in the final docs run, but it shows a tendency to infer
  from directory names.

### ernie-5.5-flash-preview

- Strongest source-grounded overview: read `app/layout.tsx`,
  `app/page.tsx`, and the loader, and cited line ranges.
- Correct Go impact: 13 call sites, all test-side, with two coordinates and a
  correct static-versus-runtime caveat.
- Correct outbox trace: `bounded_context.go`, `outbox/dispatcher.go`,
  `eventbus/eventbus.go`.
- Weakest coverage run: the ecommerce coverage answer was a two-sentence
  meta statement ("I'll avoid using...") rather than the requested report.
- The provider emitted `<ernie:tool_call>` XML as final text in earlier runs.
  OHGO published it as an answer and marked the run COMPLETED. The audit
  registered that envelope (OHGO-PROV-001); the final docs run normalized it
  and produced a real source-grounded answer instead.

### cohere/north-mini-code

- When it answered, it found the correct TerminalBlog page and the correct
  NewOrder call sites.
- Reliability was poor in the final matrix: 5 of 8 tasks ended with no answer,
  including both overviews and both docs tasks. Several runs used one tool and
  stopped.
- An earlier blog impact answer invented a `showDetails` prop that does not
  exist in `TerminalBlogProps`; the run was marked COMPLETED. This is a model
  hallucination and an evidence-completion gap, not a parser failure.
- Two ecommerce overview tool calls failed on invalid read paths before the
  model stopped.

### longcat-2.5-free

- The provider returned repeated HTTP 429 across a 150-second attempt. The
  runtime reported the provider failure correctly; no model conclusion is
  drawn.
- The two completed ecommerce tasks were not enough to establish tool-use
  reliability or accuracy.

## Shared runtime observations

- The `symbols` tool returns declarations only. In the first impact attempts,
  all three models stopped after `symbols` and answered that callers were
  unavailable. After the prompt was phrased with an explicit read verb and the
  tool description acquired a "declarations only; use refs/impact" sentence,
  beam and ernie used `refs` in the final runs. Cohere still stopped early.
  This is primarily a model tool-selection failure; the tool schema was not
  invalid.
- A model can produce a factually wrong answer and still receive `COMPLETED`
  when the answer sounds grounded (cohere's invented `showDetails`; ernie's
  earlier incomplete `NewOrder` answer). The completion evaluator checks
  evidence shape and failure recovery, not whether every requested field was
  actually established. This remains an open runtime limitation.
- The runtime-state projection is model-facing but not isolated from the final
  answer. Cohere copied a large "Runtime State" block into its public answer
  in an earlier run. The final runs did not repeat it, but the boundary is not
  enforced.
- Free gateway rate limits and empty completions are provider failures. OHGO
  reported the 429 as a failed stream; it did not retry indefinitely.

## Verdict

No single model wins every task. `beam-free` is the most reliable tool user on
this suite, `ernie` produces the strongest source-grounded explanations but is
more variable and had one protocol-envelope failure that OHGO has now fixed,
and `cohere` is the least reliable. All three benefited from the structural
fixes: the native Go index now returns 13 callers for `NewOrder`, and the TSX
index now returns the page→TerminalBlog reference.
