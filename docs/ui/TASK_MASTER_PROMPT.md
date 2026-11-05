# Master Task: Fix the Streaming → Runtime → Viewport → Terminal Presentation Pipeline

## Objective

Perform a complete, architecture-aligned fix for ohgo's current streaming presentation, viewport, scrolling, terminal-input, and runtime-observability problems.

The goal is **not** to patch individual visual symptoms.

The goal is to make the entire path deterministic and coherent:

```text
Provider wire stream
        ↓
Stream normalization
        ↓
Canonical runtime events
        ↓
Agent/runtime state
        ↓
Presentation blocks
        ↓
Layout / frame scheduling
        ↓
Viewport
        ↓
Terminal
```

At the same time, terminal input must remain isolated:

```text
Terminal input bytes
        ↓
Terminal / Bubble Tea event decoding
        ↓
Semantic input events
        ↓
Viewport / TUI interaction
```

No raw provider protocol, ANSI control sequence, or terminal mouse sequence may leak across these boundaries.

The final result must provide:

- correct provider-agnostic stream normalization;
- no raw serialized tool-call protocol in visible or historical content;
- no terminal mouse/ANSI escape leakage into text;
- deterministic scrolling and historical reconstruction;
- smooth, continuous rendering rather than block-by-block visual popping;
- reasoning/thinking visible but collapsed by default;
- compact, expandable tool-output blocks;
- real execution liveness instead of an indefinite meaningless spinner;
- stable auto-follow / manual-scroll behavior;
- observable runtime state during long-running tasks;
- actionable evidence when execution fails or loops;
- no provider-specific rendering logic;
- no regex-based cleanup in the viewport/rendering layer;
- preserved ohgo architecture and authority semantics.

This is a **three-phase implementation**. Do not treat the phases as unrelated tasks. Each phase is one layer of the same final architecture.

---

# Critical Context

Current symptoms include:

### 1. Dots serialized tool-call leakage

During some runs, Dots emits serialized tool-call syntax such as:

```text
<dotsfunctioncall>
  <invoke name="ls">
    <parameter name="path">...</parameter>
  </invoke>
</dotsfunctioncall>
```

This must become a canonical `ai.ToolCall` and must never appear as assistant text.

The problem is especially visible when scrolling through historical viewport content, where raw protocol text can reappear even though the normal live presentation may look correct.

This strongly suggests that at least one historical/reconstruction path may be bypassing canonicalized presentation state.

Do not assume this diagnosis is correct. Trace the actual code path and prove where the divergence occurs.

### 2. Terminal escape / mouse leakage

During scrolling, text such as:

```text
[<65;119;30m
```

can appear in the input area.

This is consistent with a leaked terminal mouse/CSI sequence.

The correct fix is to ensure terminal control sequences are decoded and consumed at the terminal-input boundary. Do not add regex cleanup to the renderer or input text.

### 3. Block-by-block visual popping

Current output can visually arrive as disconnected chunks:

```text
✓ ls ... 1ms

✓ ls ... 1ms

✓ ls ... 1ms

Let me explore the directory structure more deeply...
```

The desired behavior is continuous and calm.

The system should not repaint/reflow once per tiny event or stream fragment when events can be coalesced safely.

The goal is not decorative animation.

The goal is **continuous state transition without visual discontinuity**.

### 4. Reasoning should be observable but collapsed by default

Reasoning/thinking content may be useful for developers, but it should not dominate the viewport.

Default:

```text
▸ thinking · 4.8s
```

Expandable:

```text
▾ thinking · 4.8s

  Let me explore the directory structure more deeply...
```

Do not implement this as string heuristics. It must be represented as a real presentation block/state.

### 5. Tool output should have lifecycle semantics

A tool invocation should behave as one logical block.

Conceptually:

```text
ToolBlock
├── Running
├── Completed
└── Failed
```

For example:

```text
◌ ls /backend...

```

then:

```text
✓ ls /backend...                       1ms
  ⎿ 7 lines  (ctrl+o to expand)
```

Output should not require rebuilding the entire transcript.

### 6. Long-running execution must remain observable

A user may wait for an agent for 30 minutes.

The UI must never reduce this to:

```text
[spinner]
```

with no evidence of what the runtime is doing.

During long execution the user should continuously see meaningful state such as:

```text
BUILD · implementing · 18m42s

✓ read internal/agent/run.go
✓ read internal/control/gate.go
✓ search "NormalizeStream"
▸ thinking · 4.2s
✓ read internal/tui/viewport.go
◌ running go test ./...
```

The exact presentation can differ, but the principle is mandatory:

> The user must be able to tell that the runtime is alive, what it is doing, what it has already observed, and where it is currently blocked or executing.

If the runtime enters a failure, retry, or suspicious loop, the user must receive enough evidence to understand what happened and decide whether to stop, reject, retry, or investigate.

---

# Architectural Principles

## Principle 1 — Canonicalize once

Provider-specific wire syntax belongs at the provider/stream normalization boundary.

After normalization, downstream layers must consume canonical events.

Never make the viewport understand Dots, OpenRouter, OpenCode, or any other provider protocol.

Bad:

```go
if provider == "dots" { ... }
```

Bad:

```go
regexp.ReplaceAllString(content, "<dotsfunctioncall>...")
```

Bad:

```go
if strings.Contains(content, "<invoke") { ... }
```

Good:

```text
provider stream
    ↓
NormalizeStream
    ↓
canonical ToolCall / Text / Event
    ↓
runtime
    ↓
presentation
```

## Principle 2 — Historical and live views must use the same representation

This is critical.

Do not allow:

```text
live:
provider → normalized → blocks → viewport

scroll/history:
raw stored content → viewport
```

The architecture must be:

```text
provider
   ↓
normalized canonical events
   ↓
presentation blocks
   ↓
stored/reconstructable presentation state
   ↓
viewport
      ↙        ↘
   live      historical
```

Scrolling must not re-parse raw provider output.

## Principle 3 — Viewport renders blocks, not protocols

The viewport should receive semantic blocks/state.

Conceptually:

```text
Block
├── ID
├── Kind
├── State
├── Content
├── Summary
├── Metadata
├── Collapsed
└── Layout information
```

Kinds may include whatever the existing architecture naturally supports, for example:

- Assistant
- Reasoning
- Tool
- Status
- Error
- Progress

Do not introduce unnecessary abstractions merely to satisfy this list. Reuse existing architecture where possible.

## Principle 4 — Terminal control sequences are not text

Terminal control sequences must be handled at the terminal/input boundary.

They must never become:

```text
user input
assistant text
viewport content
history content
```

Do not solve this with renderer regexes.

## Principle 5 — Render by frame, not by event

Multiple state changes arriving within a short interval should be safely coalesced into one presentation frame.

Conceptually:

```text
runtime events
      ↓
mark presentation dirty
      ↓
schedule/coalesce frame
      ↓
layout once
      ↓
paint once
```

Do not optimize prematurely. First understand the current Bubble Tea/update/render architecture and make the smallest correct change.

## Principle 6 — Scrolling must be deterministic

The existing viewport/Fenwick approach should be preserved or improved rather than replaced without evidence.

The viewport must correctly support:

- append;
- update;
- expand;
- collapse;
- scroll up;
- scroll down;
- jump to bottom;
- content arriving while scrolled;
- content arriving while following bottom;
- variable-height blocks;
- historical rendering.

Never reconstruct provider semantics during scrolling.

## Principle 7 — Bottom-follow is distinct from manual scrolling

If the user is at the bottom:

```text
new content → follow automatically
```

If the user scrolls upward:

```text
new content → do not yank viewport back to bottom
```

The UI should indicate that new activity exists.

When the user returns to the bottom, live-follow resumes.

## Principle 8 — Observability is part of runtime correctness

A long-running agent must expose meaningful runtime state.

The UI is not merely decoration.

It is the human's observation surface for:

- current role;
- current activity;
- completed actions;
- tool execution;
- reasoning state where applicable;
- verification;
- failure;
- retry;
- blocked state;
- user decision points.

Do not fake progress.

Every displayed state should correspond to actual runtime state/evidence.

---

# Phase 1 — Establish Canonical Stream and Event Correctness

## Goal

Eliminate raw protocol/control leakage at the source and prove that all downstream consumers receive canonical events.

Do not redesign the viewport yet.

First make the data entering the viewport correct.

## Tasks

### 1. Trace the complete stream path

Identify the real path:

```text
provider wire stream
→ Registry.StreamFn
→ ai.NormalizeStream
→ agent/runtime
→ persistence/state
→ presentation/TUI
```

Determine exactly:

- where provider chunks are decoded;
- where tool calls are detected;
- where text is accumulated;
- where stream events are persisted;
- what representation the viewport currently consumes;
- whether current and historical paths differ.

Do not infer. Inspect the code.

### 2. Harden `NormalizeStream`

Ensure serialized tool-call protocols are normalized before they can become assistant text.

The parser must support arbitrary stream chunk boundaries.

For example:

```text
chunk 1:
<dotsfunc

chunk 2:
tioncall><invoke name="ls">

chunk 3:
<parameter name="path">...

chunk 4:
...</invoke></dotsfunctioncall>
```

must produce the same canonical event as an unfragmented stream.

Do not assume chunks align with protocol boundaries.

### 3. Preserve provider neutrality

Do not add provider/model-specific logic outside the normalization boundary.

Dots-specific syntax belongs in the registered protocol normalization implementation, if required.

The rest of ohgo must see only canonical events.

### 4. Identify and remove raw-content fallback paths

Search for all paths where raw provider stream/content may be:

- persisted;
- replayed;
- reconstructed;
- rendered;
- copied into history;
- passed to viewport.

If a historical viewport path bypasses canonical state, fix the architecture rather than sanitizing the output.

### 5. Terminal input isolation

Trace how terminal mouse/CSI sequences reach the TUI.

Ensure:

```text
terminal bytes
→ input decoder
→ semantic mouse/key event
→ TUI handler
```

and never:

```text
terminal bytes
→ string
→ input editor
```

If mouse tracking is enabled, ensure all mouse sequences are consumed by the proper event layer.

If a mode does not require mouse tracking, do not enable it there.

### 6. Add regression tests

At minimum:

#### Test A — serialized tool call

Input:

```text
<dotsfunctioncall>...</dotsfunctioncall>
```

Expected:

```text
ToolCall
```

and:

```text
AssistantText == ""
```

for the protocol frame.

#### Test B — arbitrary chunk boundaries

Split the same serialized tool call across many arbitrary boundaries.

Expected canonical event must be identical.

Prefer fuzz/property-style coverage if practical.

#### Test C — no raw protocol in presentation input

Canonical presentation input must not contain:

```text
<dotsfunctioncall>
<invoke
<parameter
```

for normalized tool-call events.

#### Test D — terminal mouse sequence

A mouse CSI sequence must become a semantic mouse event or be consumed, but must never become user text.

### Phase 1 acceptance criteria

Do not proceed until:

- Dots serialized tool calls are canonicalized;
- arbitrary chunk boundaries work;
- raw provider protocol does not enter assistant text;
- historical/replay paths are understood and no longer bypass canonical state;
- terminal mouse/control sequences cannot become input text;
- existing provider tests remain green;
- no provider-specific logic was introduced into viewport/rendering;
- `go build ./...`, `go vet ./...`, `gofmt`, and relevant tests pass.

At the end of Phase 1, report:

1. actual root causes found;
2. files changed;
3. data-flow before/after;
4. tests added;
5. remaining problems intentionally deferred to Phase 2/3.

Do not claim the whole task is complete.

---

# Phase 2 — Build a Deterministic Presentation/Viewport Pipeline

## Goal

Make the viewport a deterministic renderer of canonical runtime/presentation state.

This phase fixes the relationship between blocks, layout, scrolling, historical reconstruction, and terminal painting.

## Tasks

### 1. Establish semantic presentation blocks

Inspect the existing presentation model and introduce/reuse a block abstraction where necessary.

A block should have stable identity and lifecycle.

Conceptually:

```text
BlockID
Kind
State
Content
Summary
Metadata
Collapsed
```

Do not blindly copy this structure if the repository already has an equivalent abstraction.

Prefer extending existing concepts.

### 2. Tool lifecycle blocks

A tool call should remain one logical presentation unit.

Support:

```text
running
completed
failed
```

with incremental updates where the runtime provides them.

Avoid replacing a completed tool block with a newly inserted block.

The identity should remain stable.

### 3. Reasoning/thinking blocks

Represent reasoning as a distinct presentation block/state when the runtime provides it.

Default:

```text
collapsed
```

Expanded:

```text
full content
```

Do not detect reasoning by regex over arbitrary text.

### 4. Historical and live rendering must converge

There must be one canonical presentation representation.

Both:

```text
live viewport
```

and:

```text
historical scroll
```

must render from the same block/event state.

Do not allow scrolling to reconstruct from raw provider strings.

This is a mandatory acceptance condition.

### 5. Preserve deterministic viewport indexing

Audit the existing Fenwick/block-height implementation.

Verify:

- append;
- update;
- expansion;
- collapse;
- height delta;
- lookup by logical row;
- scroll offsets;
- boundary conditions;
- powers-of-two behavior;
- first/last block;
- empty content;
- large histories.

Do not replace the data structure unless the current implementation is demonstrably incapable of satisfying the requirements.

### 6. Separate logical selection from rendering

Scrolling/selection should operate on logical block/row state.

Rendering must not depend on parsing displayed strings.

ANSI styling and terminal presentation must not become part of the logical content model.

### 7. ANSI/control sequence safety

Audit every boundary where strings become terminal output.

Ensure:

- control sequences are intentionally generated by the renderer;
- raw control sequences from user/provider content cannot corrupt layout;
- stripping/normalization is performed at the correct boundary;
- terminal sequences are not accidentally treated as printable text.

Do not use a global regex sanitizer as a substitute for proper ownership.

### 8. Frame-based rendering

Audit Bubble Tea update/render behavior.

Introduce event coalescing/frame scheduling only where needed.

The desired flow is:

```text
state changes
    ↓
mark dirty
    ↓
coalesce
    ↓
layout
    ↓
paint
```

Avoid full history rebuilds for tiny incremental changes.

Do not optimize by dropping observable events.

### 9. Smoothness without fake animation

The goal is not 60 FPS animation.

The goal is:

- no visible tearing;
- no block-by-block popping;
- no unnecessary full-screen redraw;
- stable cursor/input area;
- stable scroll position;
- smooth incremental content arrival.

### Phase 2 acceptance criteria

Verify:

- scrolling never reveals raw Dots protocol;
- scrolling never reveals terminal escape sequences;
- live and historical rendering use the same representation;
- block expansion/collapse is stable;
- tool blocks update in place;
- reasoning blocks collapse by default;
- Fenwick/index calculations remain correct;
- no scroll jumps caused by unrelated block updates;
- bottom-follow behavior is deterministic;
- rendering no longer visually pops for every tiny event;
- existing TUI behavior remains intact.

Test at least:

```text
empty history
small history
large history
many tool calls
long reasoning
rapid streaming
slow streaming
tool output expansion
tool output collapse
scroll while streaming
scroll at top
scroll at bottom
append while scrolled
append while following bottom
```

Run race-sensitive tests where relevant.

Do not proceed to Phase 3 until Phase 2 is stable.

---

# Phase 3 — Continuous Runtime Observability and Final UX

## Goal

Turn the corrected presentation pipeline into a professional Agent Runtime observation surface.

The user must always understand what the runtime is doing without being forced to stare at a meaningless spinner.

## Tasks

### 1. Replace spinner-centric progress with semantic activity

Do not remove all activity indication.

Instead, make activity meaningful.

Examples:

```text
ASK · reasoning
FUZZ · investigating
BUILD · implementing
VERIFY · checking
COMMIT · preparing
```

Use the actual runtime state.

Do not invent state merely for visual feedback.

### 2. Continuous activity stream

During long tasks, preserve meaningful evidence:

```text
✓ completed action
◌ current action
▸ collapsed reasoning
⚠ warning
✗ failure
```

The exact glyphs/UI may follow existing ohgo conventions.

Avoid excessive visual noise.

### 3. Long-running task behavior

A task running for 30 minutes should remain understandable.

At any point the user should be able to determine:

- current role;
- current activity;
- elapsed time where appropriate;
- recent completed actions;
- current tool/action;
- whether execution is progressing;
- whether the runtime is waiting;
- whether a decision is required;
- whether an error occurred.

### 4. Failure observability

A failed run must leave enough evidence to explain:

```text
what was being attempted
what action failed
where it failed
what evidence was observed
what the runtime decided
```

Do not hide failures behind:

```text
spinner → stopped
```

### 5. Loop / stagnation visibility

Do not invent a new agent-control policy unless the existing runtime already supports it.

But the presentation layer must not conceal repeated activity.

If the runtime exposes retry/repetition/blocked state, render it meaningfully.

The user must be able to recognize when the agent is repeatedly investigating without making progress.

### 6. User decision surfaces

Where the runtime legitimately requires user authority, make the state visible and actionable.

Examples:

```text
[Accept] [Reject]
[Continue] [Stop]
[Review]
```

Do not create a new centralized StrategyPicker or unrelated interaction model.

Reuse existing ohgo permission/authority surfaces.

### 7. Auto-follow semantics

Implement/verify:

```text
user at bottom
→ follow live output

user scrolls up
→ preserve manual position

new content arrives
→ do not yank viewport

show new-activity indication

user returns bottom
→ resume live-follow
```

### 8. Subtle motion

Any motion should be driven by actual state changes.

Prefer:

```text
◌ running
→
✓ completed
```

over continuously animated decorative spinners.

Do not sacrifice readability for animation.

### 9. Final integration audit

Review the entire path:

```text
Provider
 ↓
NormalizeStream
 ↓
Canonical event
 ↓
Runtime
 ↓
Presentation state
 ↓
Block lifecycle
 ↓
Layout
 ↓
Frame scheduler
 ↓
Viewport
 ↓
Terminal
```

and separately:

```text
Terminal
 ↓
Input decoder
 ↓
Semantic event
 ↓
TUI
 ↓
Viewport interaction
```

There must be no accidental shortcut around these boundaries.

---

# Hard Constraints

Do not:

- patch the viewport with provider-specific regexes;
- add Dots-specific code to generic rendering;
- sanitize raw protocol only at display time;
- rebuild history from raw provider content during scrolling;
- convert terminal escape sequences into text and then clean them later;
- introduce a second parallel streaming pipeline;
- introduce a second update/render architecture;
- add fake progress states;
- hide runtime failures;
- replace deterministic runtime evidence with decorative animation;
- weaken authority semantics;
- modify BUILD/ASK/FUZZ/COMMIT authority rules as part of this task unless a discovered dependency makes it strictly necessary;
- perform unrelated refactors;
- rewrite the viewport from scratch without demonstrating why the current architecture cannot satisfy the requirements.

Prefer:

- existing abstractions;
- existing `ai.ToolCall`;
- existing stream registry;
- existing runtime state;
- existing BlockID/Fenwick architecture;
- existing Bubble Tea event model;
- existing authority/permission surfaces;
- small, explicit, testable changes.

---

# Testing Strategy

Do not rely only on manual visual testing.

Create deterministic regression tests for the discovered failure modes.

At minimum cover:

## Stream

- serialized tool call;
- split serialized tool call;
- multiple serialized tool calls;
- tool call followed by assistant text;
- assistant text followed by tool call;
- malformed/incomplete protocol;
- stream termination while a protocol frame is incomplete.

## Viewport

- append;
- scroll;
- historical reconstruction;
- expansion;
- collapse;
- dynamic height;
- Fenwick boundary cases;
- scroll while new events arrive;
- new content while manually scrolled;
- return to bottom.

## Terminal

- mouse press;
- mouse release;
- wheel/scroll;
- CSI sequences;
- ordinary keyboard input;
- mixed input sequences.

## Runtime observability

- short task;
- long task;
- multiple tool calls;
- reasoning;
- tool failure;
- runtime failure;
- retry;
- user decision point.

Use race tests where concurrency is involved.

---

# Performance Requirements

Do not make the UI smooth by hiding events.

Measure the actual cost of:

- block append;
- block update;
- height recalculation;
- Fenwick update;
- visible-range lookup;
- layout;
- terminal render.

Avoid:

```text
O(history) rebuild
```

for every tiny stream delta when an incremental update is possible.

Do not prematurely optimize.

First establish correctness, then measure, then optimize the proven hot path.

---

# Final Definition of Done

The task is complete only when all of the following are true:

### Correctness

- No raw `<dotsfunctioncall>` protocol appears in assistant text.
- No provider protocol reappears when scrolling through history.
- No terminal mouse/CSI sequence appears in the input field.
- Live and historical views render the same canonical representation.
- Tool calls have stable lifecycle state.
- Reasoning is represented explicitly and collapsed by default.
- Viewport scrolling is deterministic.
- Fenwick/index behavior remains correct.

### UX

- Output arrives continuously rather than as visually disconnected blocks.
- Rendering is calm and smooth.
- Tool activity is visible without excessive animation.
- Long-running tasks remain observable.
- The user can understand what the runtime is currently doing.
- Failures expose meaningful evidence.
- Manual scrolling does not get hijacked by incoming output.
- Returning to bottom resumes live-follow.
- Reasoning can be expanded when desired.
- Tool output can be expanded when desired.

### Architecture

- Provider normalization owns provider protocol parsing.
- Runtime owns semantic state.
- Presentation owns block representation.
- Viewport owns layout/scrolling.
- Terminal input owns control-sequence decoding.
- No layer reaches backward into another layer's protocol.
- No regex-based symptom cleanup exists in the viewport as a substitute for correct ownership.
- No duplicate streaming/rendering architecture was introduced.

### Validation

Run and pass the repository's normal validation suite, including where applicable:

```text
gofmt -l .
go build ./...
go vet ./...
go test -count=1 ./...
go test -race -count=1 ./...
```

Also run targeted tests for all newly fixed boundaries.

If any check fails, investigate and fix it rather than reporting partial success as completion.

---

# Execution Protocol

Before modifying code:

1. Inspect the current architecture.
2. Trace the real stream path.
3. Trace the real presentation/history path.
4. Trace terminal mouse/input handling.
5. Trace viewport append/update/scroll/layout behavior.
6. Identify the actual root causes.
7. Produce a concise implementation plan mapped to the three phases.

Then execute:

```text
PHASE 1
Canonical stream + terminal input correctness
        ↓
verification gate

PHASE 2
Presentation blocks + deterministic viewport + rendering
        ↓
verification gate

PHASE 3
Continuous observability + UX + final integration
        ↓
full verification
```

Do not stop after discovering a local fix.

Do not declare success because the screenshot symptom disappears.

The final question is:

> Can ohgo continuously expose truthful runtime state for a long-running agent while keeping provider protocols, terminal control sequences, history, scrolling, layout, and rendering cleanly separated?

If not, the task is not finished.

At the end, provide:

1. root causes;
2. architecture changes;
3. files changed;
4. tests added;
5. validation results;
6. remaining risks, if any;
7. a concise explanation of how the final data flow now works.
