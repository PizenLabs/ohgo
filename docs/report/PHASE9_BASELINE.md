# Phase 9 — Baseline

## 1. Purpose

This report establishes what the runtime actually did **before** Phase 9
changed behaviour. It is an audit, not a justification for a token target.

The motivating observation was a real task:

```text
check this project and write README.md help me
```

The provider recorded approximately **35,941 input tokens** and **1,485 output
tokens**. That number is not itself proof of runtime inefficiency. It is a
provider-specific total that mixes system instructions, tool definitions,
conversation state, tool results, runtime metadata, and provider accounting.

The baseline therefore asks four narrower questions:

1. How much of a request is fixed overhead?
2. How much is the growing tool-result transcript?
3. How much context is actually repeated between turns?
4. Which runtime decisions produced the sequence of tool calls?

## 2. Method

The baseline uses the controlled harness in `bench/phase9`.

- A deterministic fixture repository is created for each scenario.
- A **scripted model** emits the same tool calls a model emitted in the
  motivating task (and the same calls for the other matrix scenarios).
- The real production tools execute against the fixture.
- The real `agent.Loop` builds every request.
- Every request is recorded before it reaches the transport.
- Byte counts are exact; token counts use the runtime's documented
  `tokens.ModeEstimate` heuristic (bytes/4 plus framing). They are **runtime
  estimates**, not provider billing.
- The script is fixed, so a difference between baseline and projected runs is
  attributable to the runtime, not to a model choosing differently.

The reported provider total and the harness estimate are the same order of
magnitude for the README task (35,941 provider tokens vs **38,004** estimated
tokens for the nine-call scripted sequence). This is directional alignment, not
calibration: the fixtures and tokenizer differ.

Reproduce:

```sh
go run ./bench/phase9 -projection off \
  -out docs/benchmarks/results/phase9-baseline.json
```

## 3. Measured baseline

Selected scenarios from the full matrix; the complete table is in
`PHASE9_BENCHMARK.md` and `docs/benchmarks/results/phase9-baseline.json`.

| Scenario | Requests | Tool calls | Est. input tokens | Tool-schema bytes | Context bytes | Repeated bytes | First request | Last request |
|---|---:|---:|---:|---:|---:|---:|---:|---:|
| A pure read | 7 | 6 | 24,604 | 69,615 | 23,838 | 77,760 | 11,751 | 15,693 |
| B READ → BUILD (README) | 10 | 9 | **38,004** | 99,450 | 45,050 | 127,041 | 11,722 | 17,459 |
| G debugging | 6 | 5 | 19,587 | 59,670 | 14,522 | 61,345 | 11,770 | 12,847 |
| H refactoring | 10 | 9 | 34,031 | 99,450 | 29,165 | 114,884 | 11,873 | 13,731 |
| J external knowledge | 5 | 4 | 16,880 | 49,725 | 14,373 | 50,266 | 11,844 | 13,832 |
| K1 irrelevant large file | 4 | 3 | 121,867 | 39,780 | 445,027 | 326,525 | 11,751 | 158,282 |
| K6 large-source context | 7 | 6 | 43,834 | 69,615 | 100,754 | 124,897 | 11,751 | 45,472 |

### 3.1 Where the bytes go

Across the full baseline matrix:

| Component | Bytes | Share of request bytes |
|---|---:|---:|
| Tool definitions (name + description + JSON schema) | 1,084,005 | **54.8%** |
| System prompt + conversation/tool-result context | 894,401 | 45.2% |
| Raw tool output actually produced by tools | 213,631 | (included in context) |

For the README task specifically:

- First request: **11,722 bytes**, of which **9,945 bytes (84.8%)** were tool
  definitions. The user message was 39 bytes; the system prompt was 1,738
  bytes.
- Last request: **17,459 bytes**. The tool schema stayed at 9,945 bytes; the
  conversation grew by the read results and the write.
- **127,041 bytes** of requested content were repeats of content already sent
  on an earlier request. The full accumulated transcript is reconstructed and
  transmitted on every model turn.
- The final `ls` after writing and reading `README.md` was unnecessary; the
  task was already provable.

For the pure-read scenario:

- First request: 11,751 bytes; 10,203 bytes (86.8%) were tool definitions.
- The six tool calls added only 3,723 raw output bytes, but each request
  re-sent the accumulated conversation.

### 3.2 The large-file case

K1 reads a 146 KB irrelevant log once. The last request is **158,282 bytes**,
13.5× the first request, and total estimated input is 121,867 tokens. The
runtime did exactly what the loop design says: it replayed the exact tool
result every turn.

This is not a model failure. It is a runtime retention policy that had no
concept of evidence value.

## 4. What the baseline establishes

These are measured facts, not inferences:

1. **Tool definitions are the largest fixed cost** for short coding tasks.
   The README task spent 84.8% of its first request on tool schemas.
2. **The full transcript is re-sent every turn.** Repeated bytes grow to more
   than the sum of unique content; the last request is larger than the first
   in every scenario that reads more than one file.
3. **Tool-result volume is the growth driver**, not the system prompt. A single
   large read can multiply the last request by an order of magnitude.
4. **Model output is a small fraction of input.** The README task estimated
   1,169 output tokens against 38,004 input tokens. Optimizing output style
   would not address the cost.
5. **A clean model stop is not completion evidence.** The README baseline is
   `NEEDS_REVIEW`: a change was observed, but no successful check followed it.
   The file was written; the runtime did not claim the task was proven.

## 5. Runtime inference (clearly not measured)

- The provider may cache part of the repeated prefix. The baseline measures
  what the runtime sent, not what the provider billed after cache accounting.
  Cache discounts do not change the cognitive surface: the model still sees
  the full request unless the provider's cache semantics are relied upon.
- The scripted sequence is one plausible model trajectory, not the only one.
  A different model may read fewer files, read different files, or stop
  earlier. That is why Phase 9 also ships a live-provider mode and why the
  scripted comparison isolates runtime cost rather than claiming to measure
  model quality.
- The provided 35,941-token transcript did not include the exact tool
  descriptions, system prompt, or provider accounting. Its decomposition
  cannot be reconstructed exactly. The harness reproduces the same class of
  cost with the same task shape.

## 6. Why token count alone is not the objective

The baseline shows three different costs:

- **fixed action-surface cost** (tool schemas),
- **accumulated working context** (re-sent transcript),
- **unnecessary exploration** (a final listing after proof).

A change that reduces one while losing exact diagnostics, mutating outside
scope, or making completion less truthful is not an improvement. Phase 9
therefore optimizes verified task success per unit of model cognition and
context, measured alongside correctness, authority, evidence retention, and
completion.

## 7. Baseline limits

- No live provider run was performed in this environment. The live harness
  exists (`go run ./bench/phase9 -live-model provider/model`) but its model
  behaviour and billing results are **unmeasured** here.
- The fixtures are synthetic and deliberately exercise a mixed static-web /
  small-Go repository. Absolute token counts are fixture-specific.
- Token counts are estimates from the runtime, not provider tokenizer output.
- Wall-clock times for scenarios that run `go test`/`go build` include tool
  execution, not model latency, in the scripted mode.

## 8. Baseline verdict

The baseline is sufficient to reject the assumption that the README task's
cost was primarily the model "thinking too much". The dominant measured costs
were the fixed tool surface, repeated transmission of accumulated tool
results, and an unnecessary post-proof exploration call. Phase 9 consequently
targets the runtime boundary, not the prompt.
