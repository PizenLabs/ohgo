# Streaming output isolation

Date: 2026-11-20
Status: **closed**

## 1. The defect

The previous sanitizer ran on the completed answer. The loop's `OnTextDelta`
hook forwarded raw provider deltas to the TUI, so a model that copied the
request-side runtime state could publish it live before the completed answer
was cleaned. The final transcript and the completion input were safe; the
public stream was not.

The two outputs also had separate implementations, so a future change to the
final sanitizer could silently diverge from what the user had already seen.

## 2. The boundary

There is now one online sanitizer, `answerStreamSanitizer`, in
`internal/agent/presentation.go`.

- `Loop.drain` installs it on `OnTextDelta`. The hook never receives a delta
  that the completed-answer boundary would withhold.
- `SanitizeAnswerText` runs the same state machine over the completed answer.
- `SanitizeAssistantMessage` applies it to every text block a host may publish
  or persist.
- `TranscriptTurn` applies it to every turn, not only the final answer turn.

The three public streams are distinct:

| Stream | Owner | Contents |
|---|---|---|
| Progress | `OnToolProgress` / host events | tool start, finish, refusal, mutation, verification |
| Reasoning | `OnThinkingDelta` | model reasoning, shown as its own block and projected through the same runtime-state boundary |
| Answer | `OnTextDelta` | only text that survives the runtime-state boundary |

The runtime state block remains request-side only. It is never part of the
answer channel, the durable transcript, or the next request.

## 3. What is withheld

- The exact state header (`# Runtime state …`), including a truncated header
  at end of stream.
- Internal field lines: `Objective:`, `Workspace probe:`, `Claim evidence:`,
  `Claim rule:`, `Evidence frontier:`, `Task shape:`, `Named entities
  (preserve them):`, `Requested action:`, `Ambiguity (do not collapse into an
  invented objective):`, `Mutation scope:`, `Unrelated mutation:`, `Mutation:
  forbidden`, `Sufficiency:`, `Available actions:`, `Next action:`,
  `Withheld:`, `Available but cannot close the gap:`, `Next evidence:`,
  `Invocation corrections (…)`, `Failed capabilities (…)`, `Evidence missing:`,
  `Conflicting evidence:`, `External evidence (untrusted data …)`, and
  `Evidence known:`.
- Fixed no-tool directives:
  `No further tools are available.`, `Answer without tools.`, `Do not call
  more tools.`, `The runtime closed the action surface.`

The line is removed from the surrounding text; the sanitizer keeps line
separation so the remaining paragraphs do not collapse into each other.

## 4. What is preserved

- Ordinary prose, including the model's own `Verification:` and `Completion:`
  sections.
- A heading that merely begins with `# Run …`.
- Fenced code, including a fenced example that quotes `Sufficiency:` or
  `Available actions:` as data.
- The model's own uncertainty wording.
- A partial trigger that never completes at end of stream, unless it is a
  near-complete state header.

## 5. Chunk-boundary invariant

The sanitizer holds only the bytes at a line start that could still become a
runtime line. Once the line diverges from every trigger, the held prefix and
the rest of the line stream normally. This preserves token-level reveal for
ordinary prose while making a trigger split across deltas safe.

The regression feeds the same copied block at **every chunk size from 1 byte to
the full length** and asserts:

- the live output equals the completed sanitizer output;
- no forbidden field or header survives;
- a normal long answer still produces multiple deltas.

## 6. Evidence

Tests:

- `TestAnswerStreamWithholdsCopiedRuntimeStateAtEveryChunkBoundary`
- `TestAnswerStreamRemovesStandaloneRuntimeDirective`
- `TestAnswerStreamWithholdsStandaloneRuntimeFields`
- `TestAnswerStreamKeepsLegitimateProseAndFencedExamples`
- `TestLoopStreamsOnlyPublicAnswerText`
- `TestLiveTranscriptWithholdsRuntimeState` (TUI public delta channel)
- `TestLoopWithholdsRuntimeStateFromReasoning` (reasoning channel)
- `TestSanitizeAnswer*` (completed-answer boundary)
- `TestTranscriptTurn*` (published turn boundary)
- `TestLiveTextDeltasRevealWithoutDuplication` (TUI reveal is not one dump)
- `TestPseudoToolCallsEnterTheCanonicalExecutionPath` (live text converges on
  completed turns)

Run:

```sh
go test ./internal/agent -run 'TestAnswerStream|TestLoopStreamsOnlyPublicAnswerText|TestSanitize|TestTranscriptTurn'
go test ./internal/tui -run 'TestLiveTextDeltasRevealWithoutDuplication|TestLongTurnReachesTranscriptInOrder'
```

## 7. Remaining limit

A model can invent its own prose that resembles runtime state without matching
a fixed runtime line. That is model-authored text, not a leaked runtime
directive, and this boundary does not claim to detect it. Detection of
arbitrary internal-state paraphrase remains part of general claim grounding,
not this output filter.
