# Ohgo Execution UI/UX Redesign Specification

> Enhancement note: `SPEC_UIUX_ENHANCEMENTS.md` supersedes §7.1 for the
> throttled responding/reasoning spinner and the sidebar prohibition in §19 for
> the contextual task/integrations rail. Everything else here remains in force.

## 1. Objective

Redesign the Ohgo runtime execution interface so that active agent work is presented as a **clear, sequential, information-dense execution transcript**, rather than a stream of disconnected terminal-like output blocks.

The redesign must preserve Ohgo's identity as a **fast, lightweight CLI agent runtime**.

This is **not** an attempt to turn Ohgo into an IDE, dashboard, chat application, or animated agent UI.

The target experience is:

> Minimal visual surface, high information density, clear execution state, stable history, subtle motion, and optional depth.

The user should be able to understand what Ohgo is doing at a glance without needing to read every line, while still being able to expand details when necessary.

---

# 2. Core Design Principle

The UI must communicate:

> **What the runtime is doing, not what the model is saying.**

The execution transcript should therefore prioritize:

1. current runtime state
2. tool/action being executed
3. outcome
4. compact result
5. optional details
6. reasoning state as secondary information

Do not make model "thinking" the primary visual focus.

---

# 3. Primary UX Problem

The current UI behaves too much like a terminal log:

```text
event
event
event
event
event
thinking
event
event
```

Each event visually competes with every other event.

This creates several problems:

- execution appears as disconnected blocks
- transient states may flash too quickly to understand
- loading/spinner states are visually ambiguous
- `Thinking...` occupies attention without providing useful runtime context
- streamed output can cause viewport expansion
- completed operations remain visually similar to active operations
- long-running execution becomes difficult to follow
- the user cannot immediately identify the current operation
- excessive detail increases viewport noise
- transient UI should not be relied upon for important information

The redesign must turn the transcript into a **stable state transition narrative**:

```text
reason
  ↓
action
  ↓
result
  ↓
reason
  ↓
action
  ↓
result
```

rather than a log stream.

---

# 4. Design Goals

## 4.1 Sequential clarity

The user must be able to follow execution in chronological order.

Every action should have a clear lifecycle:

```text
queued
  ↓
running
  ↓
streaming / producing result
  ↓
completed
```

or:

```text
queued
  ↓
running
  ↓
failed
```

---

## 4.2 Stable history

Once an operation completes, its visual representation should become stable.

Completed entries must not continue animating.

Do not repeatedly redraw or animate historical operations merely because the agent is still running.

Historical content should feel "dead" and settled.

Only the current active operation may have live visual behavior.

---

## 4.3 One live execution state

At any point in time, the UI should have at most one visually prominent **current activity**.

The user should never see multiple competing spinners or animated "working" indicators.

Prefer:

```text
✓ read README.md                         2ms
  └─ 151 lines

✓ read package.json                      1ms
  └─ 35 lines

◌ inspect · project structure
```

over:

```text
spinner
spinner
spinner
Thinking...
Loading...
Processing...
```

---

# 5. Runtime State Vocabulary

Do not expose arbitrary model-generated status phrases.

Use a small, deterministic vocabulary.

Recommended runtime phases:

```text
prepare
inspect
reason
execute
verify
complete
```

The exact wording may be adapted if the current architecture has better established terminology, but the principle is mandatory:

> Status labels must describe Ohgo's execution phase, not generic AI activity.

Avoid:

```text
Thinking...
Loading...
Processing...
Working...
Please wait...
AI is thinking...
```

unless there is a specific semantic reason.

Prefer:

```text
◌ prepare · understanding task
◌ inspect · project structure
◌ reason · selecting relevant files
◌ execute · running tests
◌ verify · checking results
✓ complete · task finished
```

---

# 6. Active Activity Indicator

The current activity must be visually distinct from historical transcript entries.

Recommended form:

```text
◌ inspect · analyzing project structure
```

or:

```text
◌ execute · go test ./...
```

or:

```text
◌ verify · checking observed results
```

The active indicator may use subtle motion.

However:

> Motion is only allowed to communicate liveness.

It must never become decorative animation.

---

# 7. Animation Requirements

## 7.1 No permanent spinner animation

Do NOT use:

```text
| / - \
```

or:

```text
⠋ ⠙ ⠹ ⠸ ...
```

or:

```text
Thinking.
Thinking..
Thinking...
```

as the primary runtime animation.

This creates visual noise and communicates very little.

---

## 7.2 No animated ellipsis as information

Do not rely on:

```text
read ...
read ....
read .....
```

to indicate an active operation.

An ellipsis is not a meaningful execution state.

The operation itself should remain readable:

```text
◌ read /src/App.tsx
```

---

## 7.3 Subtle text motion is allowed

A small text/highlight sweep may be used for the current activity.

Example:

```text
◌ prepare · analyzing project structure
```

with a subtle visual sweep across the active label.

The animation must:

- be low frequency
- consume negligible CPU
- never alter the semantic content
- never obscure text
- stop immediately when the operation completes
- never affect historical entries
- never cause layout instability

If the terminal cannot render the animation reliably, the static representation is the required fallback:

```text
◌ prepare · analyzing project structure
```

---

# 8. Tool Execution Model

Every tool/action should have a compact stable row.

## Running

```text
◌ ls /go-ecommerce
```

## Completed

```text
✓ ls /go-ecommerce                         1ms
  └─ 21 lines
```

## Failed

```text
✗ read .env.example                         1ms
  └─ file not found
```

## Streaming

```text
◌ read README.md
  └─ receiving...
```

Then transition to:

```text
✓ read README.md                           1.2s
  └─ 151 lines
```

The final state must replace the transient state rather than leaving both visible.

---

# 9. Tool Row Requirements

Each tool row should expose only the information useful for scanning.

Primary information:

```text
status
operation
target
duration
```

Secondary information:

```text
compact outcome
```

Optional information:

```text
full output
arguments
errors
metadata
```

The default representation should therefore be compact.

Example:

```text
✓ glob src/**/*.go                         58ms
  └─ 17 files
```

not:

```text
✓ glob src/**/*.go
  pattern: ...
  working directory: ...
  options: ...
  result:
  ...
  metadata:
  ...
```

unless explicitly expanded.

---

# 10. Path Truncation

Long paths must be truncated deterministically.

Do not replace meaningful operation text with dynamic ellipsis animation.

Bad:

```text
◌ read /Users/anvndev/Documents/Project/Golang/go-ecommerce/...
```

Better:

```text
◌ read /Users/anvndev/.../go-ecommerce/src/App.tsx
```

The target should remain identifiable whenever possible.

Path truncation must be:

- deterministic
- width-aware
- stable
- non-animated

---

# 11. Result Summary

Tool output should normally be summarized into a single compact line.

Examples:

```text
└─ 21 lines
└─ 151 lines
└─ 17 files
└─ 24 packages · passed
└─ file not found
└─ no files matched · 4 ignored by gitignore
```

Do not dump full output into the default transcript.

The user must still be able to access full output through the existing expansion/detail mechanism.

---

# 12. Progressive Disclosure

The UI must follow a three-level information hierarchy.

## Level 1 — Always visible

The user sees:

```text
✓ read README.md                         2ms
  └─ 151 lines

◌ execute · go test ./...
```

This should be enough to understand execution.

## Level 2 — Compact detail

A result can be expanded:

```text
▾ read README.md                         2ms
  └─ 151 lines
     ...
```

Use the existing Ohgo interaction model where possible.

Do not introduce a completely new interaction system merely for this redesign.

## Level 3 — Full detail

The user can inspect:

- full tool output
- errors
- reasoning
- execution metadata
- additional evidence

The redesign must preserve access to existing diagnostic information.

---

# 13. Reasoning UI

Reasoning is secondary information.

It must never dominate the execution transcript.

Default:

```text
▸ reasoning · 3.2s
```

Expanded:

```text
▾ reasoning · 3.2s

  Let me inspect the backend structure and package
  configuration before determining the architecture.
```

Reasoning must be:

- collapsed by default
- visually subordinate to tool execution
- excluded from automatic viewport growth where possible
- stable after completion
- expandable on demand

Do not render long reasoning text continuously into the main transcript by default.

---

# 14. Do Not Treat Reasoning as a Tool

Reasoning is not equivalent to:

```text
✓ ls
✓ read
✓ glob
```

It must not visually compete with tool calls.

Use a lighter hierarchy:

```text
▸ reasoning · 3.2s
```

instead of a full output block.

---

# 15. Execution Narrative

The ideal execution sequence should visually communicate:

```text
▸ reasoning · 2.1s

◌ read README.md

✓ read README.md                         2ms
  └─ 151 lines

▸ reasoning · 1.8s

◌ inspect · backend structure

✓ glob backend/**/*.go                    9ms
  └─ 24 files

◌ verify · correlating observed structure
```

The important concept is:

> active state → outcome → next active state

The interface should make these transitions obvious without requiring large visual elements.

---

# 16. Completion State

Completion should be explicit and visually clean.

Example:

```text
✓ complete · project analysis ready

  8 actions · 8 completed · 21.4s
```

The summary should only appear when useful.

Do not add a permanent dashboard or statistics panel.

Completion must not look like another tool invocation.

---

# 17. Error Presentation

Errors must be concise and truthful.

Example:

```text
✗ read .env.example                         1ms
  └─ file not found
```

Do not visually treat every failed exploratory operation as a catastrophic task failure.

A tool failure is an **operation outcome**.

The overall task state is separate.

Therefore:

```text
✗ read docker/...                           1ms
  └─ file not found

◌ inspect · continuing with available evidence
```

is valid.

Do not automatically turn every red tool result into a red global task state.

---

# 18. Viewport Behavior

The viewport must remain stable during execution.

Avoid unnecessary viewport growth caused by:

- reasoning tokens
- streaming output
- repeated status updates
- animation frames
- duplicate state transitions

The user should see the newest meaningful activity without losing the ability to inspect previous output.

The existing viewport/history architecture should be reused wherever practical.

Do not implement a second independent scrolling system.

---

# 19. Layout Principles

The UI should remain terminal-native.

Do not introduce:

- IDE-style sidebars
- persistent panels
- cards around every event
- progress dashboards
- large headers
- tab systems
- graphical timelines
- modal execution dashboards
- decorative borders around every state
- excessive separators

Prefer whitespace and hierarchy over boxes.

Example:

```text
✓ ls /go-ecommerce                         1ms
  └─ 21 lines

✓ read package.json                        1ms
  └─ 35 lines

✗ read .env.example                        1ms
  └─ file not found

▸ reasoning · 2.4s

◌ inspect · backend structure
```

This should feel like a refined terminal, not an embedded IDE.

---

# 20. Visual Hierarchy

Use visual weight approximately in this order:

1. current activity
2. active tool
3. completed tool outcome
4. task identity
5. compact result
6. reasoning
7. metadata

Do not make all text equally bright.

Do not rely on color alone.

Status should remain understandable in monochrome or reduced-color terminal environments.

---

# 21. Color Semantics

Preserve Ohgo's existing semantic color system where possible.

Recommended semantic meaning:

```text
success  → green
failure  → red
active   → current accent
reasoning → subdued
secondary metadata → muted
```

Do not introduce arbitrary new colors merely for visual variety.

Do not use color as the only representation of state.

The glyph must communicate state independently:

```text
◌ running
✓ success
✗ failure
▸ collapsed
▾ expanded
```

---

# 22. Timing Rules

Transient states must not disappear before the user can reasonably understand them.

However, do not artificially delay execution merely to show animation.

Important principle:

> Never slow down the runtime for the sake of UI animation.

If an operation completes in 2ms:

```text
◌ ls ...
```

may never be visibly rendered as a separate frame.

That is acceptable.

The final stable state should simply be:

```text
✓ ls /go-ecommerce                         2ms
  └─ 21 lines
```

Do NOT introduce artificial minimum spinner durations.

This is critical for CLI responsiveness.

---

# 23. Fast Operations

For extremely fast operations:

```text
◌ ls
```

may never be visible.

This is intentional.

The user should see:

```text
✓ ls /go-ecommerce                         1ms
  └─ 21 lines
```

rather than a forced animation.

The interface must reflect actual runtime speed instead of pretending that work is slower.

---

# 24. Long-Running Operations

For operations exceeding a meaningful duration, expose elapsed time.

Example:

```text
◌ go test ./...                             8.7s
```

If useful, expose compact progress information:

```text
◌ go test ./...
  └─ running · 24 packages
```

Do not create fake percentage progress unless the underlying operation provides a real percentage.

Never invent progress.

---

# 25. Current Activity vs Transcript

The implementation should conceptually separate:

```text
historical execution transcript
```

from:

```text
current active activity
```

Historical transcript:

```text
✓ ls ...
✓ read ...
✗ read ...
✓ glob ...
```

Current activity:

```text
◌ verify · checking observed results
```

This separation should exist at the state/rendering level, even if the visual implementation remains a single terminal flow.

Do not append every activity update as a new historical line.

---

# 26. State Transition Invariants

The following invariants are mandatory.

### Invariant 1

A completed operation must not remain in a running state.

### Invariant 2

A completed operation must not continue animating.

### Invariant 3

A transient animation must never contain information that is not also represented by the final state.

### Invariant 4

The UI must never require animation to understand what happened.

### Invariant 5

The current activity must be distinguishable from historical entries.

### Invariant 6

Reasoning must remain secondary and collapsed by default.

### Invariant 7

Tool failures must remain tool-level failures unless the control plane determines that the overall task failed.

### Invariant 8

No artificial delays may be introduced solely for visual presentation.

### Invariant 9

No fake progress may be displayed.

### Invariant 10

Full diagnostic information must remain accessible through expansion/detail mechanisms.

---

# 27. Implementation Constraints

Before changing code, inspect the existing UI architecture.

Identify:

- transcript/event model
- execution state model
- tool lifecycle events
- streaming events
- reasoning events
- completion events
- viewport implementation
- expansion/collapse implementation
- terminal rendering loop
- refresh/tick mechanism
- existing semantic colors
- existing interaction bindings

Do not create a parallel UI architecture if the current architecture already contains an appropriate abstraction.

Prefer extending existing concepts over introducing duplicate state machines.

---

# 28. Important Architectural Constraint

Do not solve the visual problem by simply adding more strings to the existing renderer.

The renderer must be driven by meaningful state.

The implementation should distinguish at least conceptually:

```text
queued
running
streaming
completed
failed
reasoning
current_activity
```

If the existing event model already expresses these states, reuse it.

If it does not, introduce the smallest appropriate state representation.

Do not build a second agent lifecycle model solely for presentation.

---

# 29. Rendering Constraint

Rendering must be deterministic.

Given the same execution state, the renderer should produce the same logical representation.

Animation should be a presentation layer over stable state, not part of the underlying execution state.

For example:

```text
state:
  operation = "read"
  target = "/src/App.tsx"
  status = running
```

may render as:

```text
◌ read /src/App.tsx
```

Animation may alter only the visual presentation.

It must never mutate the operation state.

---

# 30. Performance Requirements

The redesign must remain appropriate for a CLI.

Avoid:

- expensive full-screen redraws
- unnecessary allocations per animation frame
- repeated layout calculations for unchanged history
- re-rendering the entire transcript for a single active-state change
- high-frequency animation ticks
- background work when nothing is changing

Historical entries should be effectively immutable after completion.

Only the current live region should require frequent updates.

The target should be:

> **high perceived responsiveness with minimal rendering work.**

---

# 31. Terminal Compatibility

The design must degrade gracefully.

If terminal capabilities are limited:

- glyphs remain understandable
- colors are optional
- animation may become static
- layout remains usable
- no critical information depends on ANSI animation
- no critical information depends on mouse input

The CLI must remain fully functional as a plain terminal application.

---

# 32. Interaction Requirements

Use existing Ohgo interaction conventions where possible.

Examples:

```text
ctrl+o → expand details
esc     → cancel
pgup    → scroll
pgdn    → scroll
```

Do not invent a new interaction model unless the current architecture cannot support the required behavior.

Expansion should reveal detail without permanently destroying the compact transcript layout.

---

# 33. Suggested Target Appearance

A representative execution should look approximately like:

```text
ohgo v0.0.6-3-g53a4a70-dirty   openrouter/stealth/crouton-alpha · ○ not checked
──────────────────────────────────────────────────────────────────────────────

session 573 · BUILD

task  check this project and analyze what this project is about?

  ✓ ls /go-ecommerce                                      1ms
    └─ 21 lines

  ✓ ls /go-ecommerce/supabase                             1ms
    └─ 2 lines

  ✓ read /go-ecommerce/package.json                       1ms
    └─ 35 lines

  ✗ read /go-ecommerce/.env.example                       1ms
    └─ file not found

  ✓ read /go-ecommerce/src/App.tsx                        2ms
    └─ 40 lines

  ✓ glob /go-ecommerce/src/**/*.go                      58ms
    └─ no files matched · 4 ignored by gitignore

  ▸ reasoning · 13s

  ◌ inspect · analyzing project structure


──────────────────────────────────────────────────────────────────────────────

build >
```

This is a conceptual target, not a pixel-perfect specification.

The implementation should adapt the exact spacing, typography, widths, and controls to the existing Ohgo TUI architecture.

---

# 34. What the Redesign Must NOT Become

Do not implement any of the following:

- full IDE-style UI
- permanent progress dashboard
- animated spinner per tool
- animated ellipsis
- fake loading delays
- large "Thinking" blocks
- continuously streaming reasoning into the viewport
- cards around every operation
- excessive borders
- decorative animation
- fake percentage progress
- duplicate execution state machines
- duplicate viewport systems
- large graphical progress indicators
- UI state that changes faster than humans can perceive without semantic value

---

# 35. Validation Requirements

The implementation is not complete merely because the new layout looks better in a screenshot.

It must be validated against real execution behavior.

At minimum, test:

### A. Fast tool

A tool completing in a few milliseconds.

Expected:

```text
✓ operation                              1ms
```

No artificial spinner delay.

---

### B. Slow tool

A tool taking several seconds.

Expected:

```text
◌ operation                              3.2s
```

The live state remains understandable.

---

### C. Streaming output

Expected transition:

```text
◌ read README.md
  └─ receiving...
```

to:

```text
✓ read README.md                         1.2s
  └─ 151 lines
```

No duplicate final entry.

---

### D. Tool failure

Expected:

```text
✗ read missing-file                      1ms
  └─ file not found
```

The task must not automatically become globally failed unless control-plane semantics say so.

---

### E. Multiple sequential tools

Expected:

```text
✓ tool A
✓ tool B
✓ tool C
◌ tool D
```

Only tool D is live.

---

### F. Reasoning

Default:

```text
▸ reasoning · 3.2s
```

Expanded:

```text
▾ reasoning · 3.2s
  ...
```

Reasoning must not continuously inflate the viewport.

---

### G. Long execution

Run enough operations to fill the viewport.

Verify:

- history remains readable
- current activity remains visible
- scrolling remains correct
- no duplicate rows appear
- no visual corruption occurs
- animation does not cause viewport jumps
- completed rows remain stable

---

### H. Cancellation

When the user presses `esc`:

- current activity stops
- no stale spinner remains
- no orphaned "running" row remains
- execution state and UI state converge

---

### I. Terminal resize

Resize the terminal during execution.

Verify:

- path truncation remains deterministic
- active activity remains readable
- transcript does not corrupt
- expansion remains usable
- no excessive redraw behavior occurs

---

# 36. Acceptance Criteria

The redesign is considered successful only if all of the following are true:

- The interface reads as a sequential execution transcript.
- The current operation is immediately identifiable.
- Completed operations become visually stable.
- There is at most one prominent live activity.
- No permanent spinner is required.
- No artificial execution delay is introduced.
- Fast operations remain fast.
- Long operations communicate liveness clearly.
- Streaming output has an explicit intermediate state.
- Reasoning is collapsed by default.
- Reasoning does not dominate the viewport.
- Tool output is compact by default.
- Full details remain accessible.
- Tool-level failures remain distinguishable from task-level failure.
- The UI remains terminal-native.
- The UI does not resemble an IDE dashboard.
- Animation is subtle and optional.
- The interface remains understandable without animation.
- The renderer does not require a duplicate execution/state architecture.
- Existing Ohgo viewport and interaction infrastructure is reused where appropriate.
- Real execution tests validate the lifecycle, not just static screenshots.

---

# 37. Design Decision Priority

When implementation trade-offs occur, prioritize in this order:

```text
semantic correctness
    ↓
execution truthfulness
    ↓
readability
    ↓
stability
    ↓
responsiveness
    ↓
information density
    ↓
subtle motion
    ↓
visual polish
```

Never sacrifice execution truthfulness or responsiveness for visual polish.

---

# 38. Final Instruction to the Implementing Agent

First inspect the existing Ohgo UI implementation and execution/event lifecycle.

Do not immediately start rewriting the renderer.

Produce a concise internal assessment of:

1. how execution events currently reach the TUI
2. how tool running/completion/failure are represented
3. how streaming is represented
4. how reasoning is represented
5. how viewport history is stored
6. how expansion/collapse works
7. where the current "Thinking..." state originates
8. which existing abstractions can support the target design
9. which minimal architectural changes are actually necessary

Then implement the redesign against the existing architecture.

Do not implement the conceptual example literally if the existing code provides a better structural solution.

The specification defines **behavior, hierarchy, invariants, and UX intent**.

The agent is responsible for determining the smallest maintainable implementation that satisfies those constraints.

Do not declare the work complete because the UI looks better.

Completion requires real execution validation covering fast tools, slow tools, streaming, failures, reasoning, sequential operations, cancellation, scrolling, and terminal resizing.

The final implementation should make Ohgo feel like:

> **a precise, calm, highly observable execution runtime — not a terminal log and not an IDE.**
