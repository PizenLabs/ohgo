# Ohgo Workspace Transition & Routing — Master Engineering Task

You are working on the Ohgo agent runtime and TUI.

An attached screenshot shows the current workspace-selection UI. Use that screenshot as a visual reference for the current behavior and UX, but do not assume the screenshot represents the desired final behavior.

This task is a **control-plane / workspace-routing correction**, not a cosmetic UI change.

The objective is to make ASK, FUZZ, and BUILD work together as three intentionally different workspaces while preserving their existing authority boundaries, model specialization, task continuity, evidence model, and human-control semantics.

Do not solve this by adding isolated conditionals to the picker or completion evaluator. First understand the existing architecture and identify the real transition/admission/routing boundaries.

---

# Core Design Intent

Ohgo has three workspaces:

## ASK

ASK is the primary human-facing reasoning workspace.

Its purpose is:

- understand the user's request
- answer questions
- inspect and reason over the repository
- explain findings
- perform read-only analysis
- interact primarily with the human

ASK must not mutate the repository.

ASK must not silently execute mutation-capable operations.

When a request genuinely crosses from human-facing/read-only work into an authority boundary that requires BUILD or FUZZ, Ohgo should ask the user before changing workspace.

ASK is therefore the strongest human-interaction boundary.

---

## BUILD

BUILD is the repository mutation authority.

BUILD may:

- inspect the repository
- execute allowed commands
- modify source files
- implement changes
- run tests
- verify changes
- continue a task across multiple implementation/verification steps

BUILD is the high-authority workspace.

However, BUILD is not synonymous with:

> "every request currently being processed is a mutation request."

A BUILD session may receive a request that is primarily investigative or exploratory.

BUILD should therefore be able to cooperate with FUZZ without repeatedly asking the user.

---

## FUZZ

FUZZ is the autonomous investigation / adversarial / reproduction workspace.

FUZZ may:

- inspect the repository
- execute investigation commands
- reproduce behavior
- run experiments
- perform benchmarks/reproduction work
- investigate deeply
- explore hypotheses

FUZZ must **not mutate repository source code**.

This safety boundary is fundamental and must not be weakened.

FUZZ exists specifically so Ohgo can perform deeper autonomous investigation without granting BUILD-level mutation authority.

---

# Critical Workspace Relationship

The most important architectural rule is:

```text
ASK <-> BUILD/FUZZ
    = human authority boundary

BUILD <-> FUZZ
    = cooperative internal transition
```

These two categories MUST NOT be treated the same way.

The desired behavior is:

```text
ASK
  |
  | request crosses authority boundary
  | -> ask human
  v
BUILD <-----> FUZZ
       automatic cooperation
```

BUILD and FUZZ complement each other.

The system should be able to move between BUILD and FUZZ naturally when the task requires investigation versus mutation.

The user should not be repeatedly interrupted by workspace-selection popups for normal BUILD <-> FUZZ cooperation.

---

# Current Problem

The current UI contains a modal similar to:

> Choose Workspace For This Request

with:

1. ASK (Read-Only)
2. BUILD (Workspace Authority)
3. FUZZ (Autonomous Assistance)

The current behavior appears to treat workspace selection too much like:

```text
request
  -> choose one workspace
  -> lock request into that workspace
```

This is not the intended model.

Workspace is the **current authority / interaction surface**, not necessarily a permanent classification of the entire task.

The task itself must remain continuous while workspace can change according to the request and the current phase.

---

# Important Example

Consider this request:

```text
Check this Go e-commerce project and understand how the main order/checkout flow works.

Start by investigating the project without changing anything. If you identify a concrete issue that needs reproduction or deeper investigation, investigate it and reproduce it without modifying source code.

If the issue is confirmed, switch to the appropriate workspace, implement the fix, run the relevant tests, and verify that the fix actually works.

Keep the same task context throughout the process. Do not create a new task for continuation or verification. Do not claim completion without evidence.
```

This request does NOT mean:

```text
Objective = repository mutation
Workspace = BUILD
Completion requires mutation
```

It means:

```text
Primary objective:
    understand the order/checkout flow

Conditional workflow:
    investigate
      -> if issue exists
         -> reproduce
         -> if confirmed
            -> obtain mutation authority
            -> implement
            -> test
            -> verify
```

The words:

> "If the issue is confirmed, implement the fix"

describe a **conditional future branch**.

They must not automatically convert the entire request into an unconditional mutation objective.

This distinction is critical.

---

# Desired Routing Semantics

The runtime should reason about:

- current workspace
- current user intent
- current task phase
- required capabilities
- whether the request crosses a human authority boundary
- whether the requested operation can be handled by the current workspace
- whether BUILD/FUZZ cooperation is sufficient
- whether a workspace transition requires user confirmation

Do NOT reduce this to a string search such as:

```text
contains "fix"
contains "implement"
contains "question"
```

Intent classification must account for the actual request structure and conditional branches.

---

# Required Transition Matrix

The implementation must establish behavior equivalent to the following.

| Current Workspace | Detected Intent / Requirement | Expected Behavior |
|---|---|---|
| ASK | question / explanation | stay ASK |
| ASK | read-only analysis | stay ASK |
| ASK | investigation that remains within ASK authority | stay ASK |
| ASK | FUZZ-level autonomous investigation | ask user before switching |
| ASK | mutation / BUILD authority | ask user before switching |
| BUILD | implementation | stay BUILD |
| BUILD | investigation / reproduction | transition to FUZZ automatically when beneficial |
| BUILD | mutation | stay BUILD |
| FUZZ | investigation / reproduction | stay FUZZ |
| FUZZ | implementation / mutation | transition to BUILD automatically |
| BUILD | human-facing question | ask whether user wants to switch to ASK |
| FUZZ | human-facing question | ask whether user wants to switch to ASK |
| BUILD | mixed task with investigation then implementation | BUILD <-> FUZZ as required, without repeated human confirmation |
| FUZZ | mixed task with investigation then implementation | FUZZ -> BUILD automatically, subject to existing BUILD authority policy |

The exact implementation may differ if the existing architecture already has a stronger abstraction, but the semantics must remain intact.

---

# Human Confirmation Boundary

Human confirmation is required when the transition crosses the ASK boundary.

Examples:

### ASK -> BUILD

User is currently in ASK and enters:

```text
Fix the checkout bug.
```

Ohgo should recognize that mutation authority is required and present an appropriate workspace-transition confirmation.

The user decides whether to move to BUILD.

---

### ASK -> FUZZ

User is currently in ASK and enters a request requiring autonomous investigation/reproduction beyond ASK's normal authority.

Ohgo should ask before entering FUZZ.

---

### BUILD/FUZZ -> ASK

User is currently doing autonomous work and enters something that is clearly a human-facing question or reasoning request.

Example:

```text
Why is this repository structured this way?
```

Ohgo should recognize the request as ASK-oriented and present the appropriate transition UI.

Do not silently turn a human-facing conversation into autonomous investigation.

---

# No Confirmation Between BUILD and FUZZ

This is a deliberate UX rule.

If BUILD is investigating and FUZZ is the better workspace:

```text
BUILD -> FUZZ
```

should be automatic.

If FUZZ confirms an issue and BUILD is required:

```text
FUZZ -> BUILD
```

should be automatic, subject to the existing authority policy.

Do not show a workspace picker for every BUILD/FUZZ transition.

The user should experience these as complementary capabilities within one continuous task.

---

# Task Continuity Requirement

Workspace transition must NOT create a new task.

The same task must retain:

- objective
- shared context
- facts
- hypotheses
- evidence
- open questions
- timeline
- role/workspace context
- completion state
- relevant budget state
- previous execution results
- verification evidence

A transition such as:

```text
FUZZ -> BUILD
```

must be a continuation of the same task.

Never implement this as:

```text
finish FUZZ task
create BUILD task
```

unless the existing architecture already has a formal internal continuation mechanism that preserves the same task identity and semantics.

The user explicitly expects one continuous task.

---

# Authority Boundary

Do not weaken the existing FUZZ safety boundary.

FUZZ must remain unable to mutate source code.

Do not solve BUILD/FUZZ cooperation by giving FUZZ BUILD capabilities.

Instead:

```text
FUZZ
  -> investigate
  -> produce evidence
  -> report/hand off
  -> transition to BUILD
  -> BUILD performs mutation
```

The Control Plane must remain responsible for the authority decision.

The model may propose a transition.

The Control Plane owns whether that transition is permitted.

---

# Model Specialization

Each workspace may have its own model configuration.

Preserve this design.

The purpose of workspace transitions is partly to allow Ohgo to use different models for different classes of work.

Do not collapse ASK, BUILD, and FUZZ into one generic model path merely to simplify routing.

The desired architecture is:

```text
ASK
  -> human-facing reasoning model

FUZZ
  -> investigation / adversarial / reproduction model

BUILD
  -> implementation / verification model
```

Workspace transition should preserve the appropriate model specialization.

Do not introduce a new generic "agent mode" abstraction that makes these three workspaces semantically equivalent.

---

# Picker UX Requirement

The workspace picker should become a **boundary confirmation mechanism**, not a mandatory request initialization mechanism.

Avoid forcing the user to choose:

```text
ASK / BUILD / FUZZ
```

for every request.

If the current workspace can safely handle the request, do not show the picker.

The picker should appear when the request crosses a meaningful workspace boundary, especially:

```text
ASK <-> BUILD/FUZZ
```

The picker should not unnecessarily interrupt:

```text
BUILD <-> FUZZ
```

---

# Important UX Principle

The user should feel:

```text
"These are three cooperating workspaces."
```

not:

```text
"I have to manually operate a mode switch every few minutes."
```

The transition should therefore be:

- explicit when authority matters
- automatic when the transition is safe
- reversible where appropriate
- continuous in task context
- visible enough to remain understandable
- not noisy
- not repetitive
- not hidden when human authority is being crossed

Do not optimize only for fewer popups.

The goal is **natural control**, not silent automation.

---

# Current Screenshot

Use the attached screenshot as evidence of the current UX.

The important visual behavior to investigate is the modal:

```text
Choose Workspace For This Request
```

with ASK / BUILD / FUZZ presented as a one-time workspace choice.

Do not blindly reproduce the screenshot.

Determine from the code:

1. where this modal is triggered
2. what state causes it to appear
3. whether it is request admission or transition handling
4. how the selected workspace is stored
5. how the workspace affects agent authority
6. how the workspace affects model selection
7. how workspace state interacts with task continuity
8. how workspace state interacts with completion contracts
9. how workspace state interacts with proposal/approval
10. whether the current workspace is incorrectly being treated as a static property of the entire task

---

# COMPLETION SEMANTICS

Audit the relationship between:

- intent classification
- objective classification
- workspace selection
- completion contract
- mutation expectation
- observed repository changes
- verification
- needs_review
- PROVEN / COMPLETED state

Do not allow a conditional phrase such as:

```text
If the issue is confirmed, implement the fix
```

to automatically imply:

```text
the current turn must mutate the repository
```

unless the task has actually entered the mutation branch.

For example:

```text
Primary objective:
    understand checkout flow

Phase:
    investigation

Observed:
    checkout is mocked and backend order flow does not exist

Result:
    evidence-backed explanation
```

This should not fail solely because no source mutation occurred.

Conversely:

```text
Primary objective:
    fix confirmed checkout defect

Phase:
    mutation

Expected:
    repository change

Observed:
    no repository change
```

should still correctly fail/review.

The completion contract must reflect the actual task phase and objective, not merely the presence of mutation-related words somewhere in the prompt.

---

# PHASE 0 — CODEBASE UNDERSTANDING AND PLAN

Before changing implementation code, perform a complete architectural investigation.

Do NOT begin implementation during Phase 0.

The only allowed write during Phase 0 is the required plan document.

First inspect the existing codebase and understand the actual implementation.

At minimum trace:

## Workspace representation

Find:

- workspace enum/type
- workspace state
- workspace initialization
- workspace persistence
- current workspace accessors
- workspace transitions
- workspace-specific capabilities
- workspace-specific model selection

## Request admission

Trace:

```text
user input
  -> request parsing
  -> intent classification
  -> workspace determination
  -> task creation/continuation
  -> agent execution
```

Identify the exact point where the current picker is triggered.

## Picker

Find:

- TUI view
- picker state
- trigger conditions
- confirmation handling
- cancellation handling
- selected workspace propagation
- task/session effects

Determine whether the picker currently means:

```text
"choose the initial workspace"
```

or:

```text
"authorize a workspace transition"
```

or both.

## Agent loop

Trace:

- how the current workspace reaches the agent
- how model selection happens
- how tool permissions are derived
- how execution permissions are enforced
- how FUZZ mutation restrictions are enforced
- how BUILD authority is enforced

## Workspace handoff

Find whether a formal transition/handoff abstraction already exists.

Do not create a second one if the existing one can be extended.

Trace:

```text
BUILD -> FUZZ
FUZZ -> BUILD
ASK -> BUILD
ASK -> FUZZ
BUILD -> ASK
FUZZ -> ASK
```

even if some transitions currently do not exist.

## Task continuity

Verify that workspace transitions preserve:

- task identity
- shared context
- evidence
- hypotheses
- timeline
- objective
- budgets
- role context
- verification state

## Intent / objective classification

Trace exactly how the system determines:

- question
- analysis
- investigation
- reproduction
- implementation
- verification
- mutation
- conditional mutation

Identify where conditional future behavior is incorrectly flattened into a current mutation requirement.

## Completion

Trace:

```text
objective
  -> completion contract
  -> observed changes
  -> verification
  -> final outcome
```

Find why a request like the provided Go e-commerce example can end with:

```text
the request asked for a repository change but no change was observed
```

even when the actual current phase is investigation.

## Existing tests

Find all relevant tests for:

- workspace routing
- intent classification
- task continuity
- BUILD/FUZZ handoff
- ASK confirmation
- mutation authority
- completion
- needs_review
- evidence
- model selection
- TUI picker

Do not rely only on unit tests. Identify existing integration/e2e/real-model or transcript-level tests if available.

---

# Phase 0 Deliverable

Create a detailed plan document:

```text
docs/WORKSPACE_TRANSITION_PLAN.md
```

If the repository already has an established planning/design-document convention, use the appropriate existing location instead, but make the document easy to find and explicitly identify it.

The plan must contain:

1. Current architecture
2. Current request flow
3. Current workspace flow
4. Current picker flow
5. Current intent/objective flow
6. Current completion flow
7. Exact root causes
8. Why the current implementation produces the observed behavior
9. Desired architecture
10. Transition matrix
11. Authority rules
12. Task-continuity rules
13. Completion-contract rules
14. Model-selection implications
15. TUI implications
16. Files/packages likely to change
17. Files/packages that should NOT change
18. Test strategy
19. Regression risks
20. Migration/implementation order
21. Explicit acceptance criteria

The plan must distinguish:

```text
observed fact
vs.
inference
vs.
proposed design
```

Do not write speculative conclusions as facts.

Do not begin Phase 1 until the plan is complete.

---

# PHASE 1 — IMPLEMENTATION

After Phase 0, implement the design using:

```text
docs/WORKSPACE_TRANSITION_PLAN.md
```

as the implementation contract.

Do not blindly follow the plan if the actual code disproves an assumption.

If implementation reveals that the plan is wrong:

1. update the plan
2. document the discovered architectural fact
3. adjust the implementation
4. continue

Do not create unrelated refactors.

Do not rewrite stable systems merely to make the implementation aesthetically cleaner.

Prefer extending existing control-plane abstractions.

---

# Implementation Requirements

## 1. Separate current workspace from task objective

A task must not be permanently classified as:

```text
ASK task
BUILD task
FUZZ task
```

merely because it started in one workspace.

The task has an objective and current workspace separately.

---

## 2. Make transitions explicit in the control plane

Workspace transition should be represented as a meaningful state transition.

At minimum the system should be able to explain:

```text
current workspace
requested workspace
reason
triggering intent
authority requirement
whether user confirmation is required
```

Do not bury this logic inside TUI rendering.

The TUI should render the control-plane decision rather than own the routing policy.

---

## 3. Preserve ASK as the human boundary

ASK must remain protected from silent authority escalation.

Do not silently transition:

```text
ASK -> BUILD
ASK -> FUZZ
```

when the transition materially changes authority/capability.

Use the existing approval/picker interaction model where appropriate.

---

## 4. Make BUILD/FUZZ cooperation smooth

Implement automatic transitions:

```text
BUILD -> FUZZ
FUZZ -> BUILD
```

when the current task requires the complementary capability.

Do not require a new task.

Do not discard context.

Do not require unnecessary human confirmation.

---

## 5. Preserve FUZZ safety

FUZZ must remain incapable of source mutation.

If FUZZ identifies a confirmed defect:

```text
FUZZ
  -> evidence
  -> handoff
  -> BUILD
```

not:

```text
FUZZ
  -> directly mutate source
```

---

## 6. Handle question detection from BUILD/FUZZ

If BUILD or FUZZ receives a clearly human-facing question, route it through the ASK boundary.

Do not silently answer it as an autonomous execution task.

Use the existing popup/confirmation mechanism rather than inventing an unrelated UI.

---

## 7. Fix conditional objective handling

Do not classify a request as mutation-only because it contains a conditional mutation branch.

Preserve the distinction between:

```text
current objective
```

and:

```text
conditional future action
```

Completion must correspond to the current objective/phase.

---

## 8. Preserve evidence-driven completion

Do not loosen completion merely to make the test pass.

The system must still refuse false completion.

A mutation task without an observed and verified change must remain incomplete/reviewable.

An investigation task that does not require mutation must not be rejected merely because mutation language exists in a conditional branch.

---

# Required Test Scenarios

Add or update tests that prove the complete routing model.

At minimum:

## Scenario A — ASK question

Current:

```text
ASK
```

Prompt:

```text
What is this project about?
```

Expected:

```text
remain ASK
no picker
no mutation
```

---

## Scenario B — ASK -> BUILD

Current:

```text
ASK
```

Prompt:

```text
Fix the checkout validation bug and run the tests.
```

Expected:

```text
workspace boundary detected
user confirmation requested
BUILD after approval
```

---

## Scenario C — ASK -> FUZZ

Current:

```text
ASK
```

Prompt requiring autonomous reproduction/investigation.

Expected:

```text
user confirmation requested
FUZZ after approval
```

---

## Scenario D — BUILD investigation

Current:

```text
BUILD
```

Prompt:

```text
Check this project and understand how the order/checkout flow works.
```

Expected:

```text
no unnecessary picker
investigation proceeds
no forced mutation
```

---

## Scenario E — BUILD -> FUZZ

Current:

```text
BUILD
```

Task requires deep reproduction.

Expected:

```text
BUILD -> FUZZ automatically
same task
same context
no human interruption
FUZZ cannot mutate
```

---

## Scenario F — FUZZ -> BUILD

Current:

```text
FUZZ
```

A defect is confirmed and requires source modification.

Expected:

```text
FUZZ -> BUILD automatically
same task
same evidence
same context
BUILD performs mutation
```

---

## Scenario G — BUILD/FUZZ -> ASK

Current:

```text
BUILD
```

or:

```text
FUZZ
```

User asks a human-facing question.

Expected:

```text
ASK transition confirmation
no silent workspace change
```

---

## Scenario H — Conditional mutation

Prompt:

```text
Investigate the project first.
If you confirm a concrete issue, fix it.
Otherwise explain what you found.
```

Expected:

```text
initial phase = investigation

If no issue:
    investigation completion
    no mutation required

If issue confirmed:
    transition toward mutation
    BUILD
    implement
    verify
```

This test is particularly important.

---

## Scenario I — Same task continuity

For a workflow:

```text
ASK -> FUZZ -> BUILD
```

prove that task identity and context remain continuous.

Verify that:

- no new task is created
- evidence remains available
- hypotheses remain available
- previous tool results remain available
- objective remains available
- budget accounting remains correct
- completion state remains coherent

---

## Scenario J — Model specialization

Prove that:

```text
ASK
FUZZ
BUILD
```

continue to use their intended model configurations.

Workspace transition must not accidentally collapse model selection into one global model.

---

# Real-Model / End-to-End Validation

Do not stop at unit tests.

After implementation, run the strongest available repository validation.

Where the existing project has real-model integration tests or transcript-level tests, use them.

At minimum validate the behavior against prompts representing:

1. pure ASK
2. ASK -> BUILD
3. ASK -> FUZZ
4. BUILD investigation
5. BUILD -> FUZZ
6. FUZZ -> BUILD
7. BUILD/FUZZ -> ASK
8. conditional investigation -> mutation
9. conditional investigation -> no mutation
10. task continuity across transitions

The important thing is not merely:

```text
tests pass
```

but:

```text
the actual control-flow behavior is correct
```

---

# Do Not Declare Success Prematurely

Do not report:

```text
fixed
complete
working
PRODUCT READY
```

merely because:

- the code compiles
- unit tests pass
- the picker renders
- a transition method exists
- one synthetic test passes

You must demonstrate the actual workspace-routing behavior and completion semantics with evidence.

If a scenario cannot be validated, explicitly report it as unverified.

---

# Regression Constraints

Do not regress:

- ASK read-only guarantees
- FUZZ no-mutation guarantees
- BUILD mutation authority
- task continuity
- shared context
- evidence tracking
- budget accounting
- completion proof
- proposal/approval semantics
- model selection
- existing TUI transcript behavior
- existing session behavior
- existing control-plane ownership

Do not solve this by moving authority logic into the TUI.

Do not solve this by allowing the LLM to directly control workspace authority.

Do not solve this by creating a second task for every workspace transition.

Do not solve this by disabling completion checks.

Do not solve this by globally removing the picker.

The picker remains necessary at genuine human authority boundaries.

---

# Architectural Principle

The final implementation should express this conceptual model:

```text
TASK
 |
 +-- objective
 +-- shared context
 +-- evidence
 +-- timeline
 +-- completion contract
 |
 +-- current workspace
       |
       +-- ASK
       |    human-facing / read-only
       |
       +-- FUZZ
       |    autonomous investigation
       |    no mutation
       |
       +-- BUILD
            mutation authority
```

Workspace is therefore:

```text
current operating surface
```

not:

```text
permanent task identity
```

And the transition policy is:

```text
ASK <-> execution workspaces
    explicit human boundary

BUILD <-> FUZZ
    automatic cooperative handoff
```

---

# Final Acceptance Criteria

The implementation is complete only when all of the following are true:

### Architecture

- [ ] Workspace is not treated as a permanent classification of the task.
- [ ] Current workspace and task objective are separate concepts.
- [ ] Workspace transition policy is owned by the control plane.
- [ ] TUI does not independently decide authority transitions.

### ASK

- [ ] ASK remains read-only.
- [ ] ASK does not silently escalate authority.
- [ ] ASK -> BUILD requires appropriate user confirmation.
- [ ] ASK -> FUZZ requires appropriate user confirmation when FUZZ authority is needed.
- [ ] Human-facing questions from BUILD/FUZZ can return to ASK through the appropriate confirmation UX.

### BUILD/FUZZ

- [ ] BUILD -> FUZZ can happen automatically when investigation is required.
- [ ] FUZZ -> BUILD can happen automatically when mutation is required.
- [ ] BUILD/FUZZ transitions do not create a new task.
- [ ] FUZZ cannot mutate source code.
- [ ] BUILD retains mutation authority.

### Intent

- [ ] Conditional mutation language does not automatically classify the entire request as mutation-required.
- [ ] Current objective is distinguished from conditional future branches.
- [ ] Workspace routing is based on actual task intent/capability requirements.

### Completion

- [ ] Investigation-only tasks can complete without mutation when the objective is satisfied with evidence.
- [ ] Mutation tasks still require observed and verified changes.
- [ ] False completion remains blocked.
- [ ] `needs_review` is not produced merely because a conditional mutation branch was not reached.

### Continuity

- [ ] Task identity survives workspace transitions.
- [ ] Shared context survives workspace transitions.
- [ ] Evidence survives workspace transitions.
- [ ] Timeline remains continuous.
- [ ] Budget accounting remains coherent.
- [ ] Completion state remains coherent.

### Model specialization

- [ ] ASK, FUZZ, and BUILD continue to use their intended model configurations.
- [ ] Workspace transitions do not collapse the three model roles into one generic model.

### UX

- [ ] The workspace picker is no longer shown merely because a new request arrived.
- [ ] The picker appears at meaningful human authority boundaries.
- [ ] BUILD/FUZZ cooperation does not produce unnecessary confirmation dialogs.
- [ ] Workspace transitions remain understandable to the user.
- [ ] The user does not need to manually micromanage normal BUILD/FUZZ cooperation.

### Validation

- [ ] Repository formatting passes.
- [ ] Build passes.
- [ ] Vet passes.
- [ ] Relevant unit tests pass.
- [ ] Relevant integration tests pass.
- [ ] Relevant race/concurrency tests pass where applicable.
- [ ] Workspace transition scenarios are explicitly tested.
- [ ] Conditional mutation scenarios are explicitly tested.
- [ ] Real-model/end-to-end validation is performed where available.
- [ ] No known regression remains in the affected control-flow paths.

---

# Final Report

At the end, provide a concise but evidence-based report containing:

1. Root cause
2. Architecture changed
3. Workspace transition behavior before/after
4. Completion behavior before/after
5. Files changed
6. Tests added/changed
7. Validation performed
8. Any limitations or unverified scenarios
9. Whether the implementation satisfies every acceptance criterion

Do not hide unresolved issues.

Do not call the task complete if an acceptance criterion remains unverified.

The goal is not merely to make the picker behave differently.

The goal is to make Ohgo's three-workspace control model behave as a coherent system:

```text
ASK
    human-facing authority boundary

FUZZ <-> BUILD
    safe, automatic cooperative execution

ONE CONTINUOUS TASK
    shared context and evidence throughout

CONTROL PLANE
    owns authority and transition decisions

LLM
    proposes work and intent

HUMAN
    remains in control when a meaningful authority boundary is crossed
```

Implement the smallest architecturally correct change that achieves this behavior without regressing existing control, continuity, evidence, or completion guarantees.
