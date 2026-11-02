# TASK CONTINUITY
## Three-Phase Architecture and Implementation Specification

**Project:** ohgo  
**Domain:** `internal/continuity/task` and its existing integration points  
**Status:** Phase 1 implemented — pending architectural review and acceptance  
**Execution model:** Strictly phase-gated  
**Authority:** Human-controlled phase authorization

---

# 1. Mission

Build a reliable task-continuity capability that allows ohgo to preserve and resume a single objective across different execution roles without losing its identity, relevant context, evidence, progress, or authority boundaries.

The system must support continuous work across:

- ASK — understanding and reasoning
- FUZZ — investigation and experimentation
- BUILD — implementation and verification

The objective must remain continuous even when the active execution role changes, the application restarts, or work is interrupted and resumed.

The system must not depend on reconstructing the objective from the entire conversation transcript.

## Core Principle

**One Objective. Continuous Context. Multiple Execution Authorities. One Verifiable Outcome.**

The three phases develop one capability progressively:

1. **Foundation:** Establish durable, structured task context.
2. **Handoff:** Enable controlled transitions between execution roles.
3. **Continuity:** Make task progression observable, recoverable, and verifiable end to end.

These are architectural dependencies, not three independent features.

---

# 2. Problem Definition

ohgo must distinguish four concepts that must not become interchangeable.

## 2.1 Task

A Task represents a durable, project-scoped objective.

It owns:

- Objective identity and description.
- Task lifecycle and durable progress.
- Shared findings and questions.
- Evidence references and their validity.
- Role-specific working context.
- Relevant artifacts and progress references.
- Continuity across sessions and application restarts.

A Task must remain the same task when the active role changes.

## 2.2 Session

A Session represents a conversation instance.

A session may be interrupted, replaced, or resumed independently of the task's identity.

A task must not depend on a single session remaining alive.

Do not merge session identity and task identity.

## 2.3 Role

A Role defines the active execution authority and operating constraints.

A role is not a task, a task state, or an independent objective.

Changing roles must not implicitly create a new task or discard the previous role's context.

## 2.4 Context

Context is the information required to continue working on a task without reconstructing its entire history.

Context has two scopes:

**Shared Task Context**

Information that remains relevant across roles:

- Verified facts.
- Unverified hypotheses.
- Evidence and provenance.
- Open questions.
- Constraints.
- Relevant progress and artifact references.

**Role-Specific Context**

Information relevant to resuming work within a particular role:

- ASK: reasoning context and read-only findings.
- FUZZ: investigation strategy, hypotheses, reproductions, experiments, and benchmark references.
- BUILD: implementation plan, planned mutation scope, patch references, and verification progress.

Role-specific context is continuity data, not authority.

---

# 3. Non-Negotiable Architectural Invariants

Every phase must preserve these invariants.

## 3.1 Existing Ownership

Reuse existing architecture before introducing new ownership.

Known existing owners include:

- `internal/continuity/task`: durable task identity, lifecycle, persistence, and resumability.
- `internal/continuity/session`: conversation/session identity.
- `internal/control/workspace` and existing security mechanisms: role authority and workspace permissions.
- Existing cognition and evidence mechanisms: run-local observation and reasoning projections.
- Existing budget and context infrastructure: bounded context assembly and resource control.

The task-continuity capability must extend these owners where appropriate.

Do not create:

- A second task lifecycle.
- A second session store.
- A parallel authority system.
- A second general-purpose observation store.
- A new generic memory subsystem.
- A second orchestration engine.
- A speculative event-sourcing architecture.
- A parallel knowledge graph.

A new package or persistent store requires explicit architectural justification and human approval.

## 3.2 Authority Is Independent of Context

Context may describe what a role intends to do.

Context must never grant permission to do it.

The active workspace authority and existing security mechanisms remain authoritative.

No fact, hypothesis, evidence record, task checkpoint, handoff contract, or role context may expand execution permissions.

## 3.3 Model Assertions Are Untrusted

A model-generated statement is not automatically a verified fact.

The system must distinguish:

- Observed information.
- Evidence-supported conclusions.
- Unverified hypotheses.
- Contradictory findings.
- Rejected hypotheses.
- Invalidated conclusions.

The model must not be able to certify its own claims merely by writing them into persistent task context.

## 3.4 Evidence Must Preserve Provenance

Evidence must retain enough information to identify:

- What was observed.
- Where it came from.
- Which role produced or recorded it.
- Which artifact or execution it refers to.
- Whether it is current, invalidated, superseded, supporting, contradicting, or inconclusive.

Large tool outputs must not be copied into task context unnecessarily.

Use references, bounded summaries, and existing artifact mechanisms.

## 3.5 Context Must Be Bounded

Task context must be deterministic, relevant, and bounded.

Do not solve continuity by repeatedly injecting the entire conversation history or every historical tool result into the model.

Context assembly must prioritize:

1. Objective and current lifecycle state.
2. Active constraints and authority boundaries.
3. Current verified facts and their evidence.
4. Relevant unresolved hypotheses.
5. Open questions and outstanding work.
6. Relevant role-specific context.
7. Necessary artifact and progress references.

Historical information must remain available when relevant, but must not dominate the active context.

## 3.6 Preserve Existing Role Semantics

**ASK — Understand and Reason**

- Read-only.
- May inspect and reason.
- Must not mutate project source.
- Must not execute or investigate through FUZZ authority.
- Must not acquire mutation authority through task context.

**FUZZ — Explore and Investigate**

- Supports exploratory and adversarial investigation.
- May execute permitted commands, tests, benchmarks, and reproductions within existing limits.
- Must not mutate project source or acquire BUILD authority.
- Experimental artifacts must remain appropriately isolated.
- External side effects require their own explicit authorization.

**BUILD — Implement and Verify**

- Sole project-source mutation authority.
- Implements the requested change.
- Executes relevant verification within its existing authority.
- Must not become an unrestricted discovery or investigation mode.

COMMIT remains an independent role with its existing semantics.

Task continuity must not redefine, weaken, or bypass these boundaries.

---

# 4. Three-Phase Development Plan

## Phase 1 — Foundation: Shared Task Context

**Status: IMPLEMENTED — PENDING ACCEPTANCE**

### 4.1 Objective

Establish a durable, typed, evidence-aware shared context owned by the existing task subsystem.

The task must preserve its identity and relevant information independently of the active role and conversation session.

This phase establishes the data and persistence foundation required by later phases.

### 4.2 Required Capabilities

#### A. Durable Shared Context

The task must support structured information for:

- Verified facts.
- Unverified hypotheses.
- Evidence records.
- Open questions.
- Shared constraints.
- Role-specific contexts.

Each record must have stable identity and appropriate lifecycle metadata.

#### B. Fact and Hypothesis Lifecycle

The system must distinguish a fact from a hypothesis.

A hypothesis remains unverified until an explicit evidence-based resolution occurs.

A fact must not be accepted solely because a model asserted it.

The lifecycle must support:

- Hypothesis creation.
- Evidence attachment.
- Hypothesis confirmation.
- Hypothesis rejection.
- Hypothesis supersession.
- Evidence invalidation.
- Evidence supersession.
- Fact dispute and reassessment.

A confirmed hypothesis must retain its origin and supporting evidence.

#### C. Evidence Association

Evidence must be associated with the relevant claim or hypothesis.

The system must distinguish:

- Observed.
- Supporting.
- Contradicting.
- Inconclusive.

Evidence existence alone must not imply that it supports an arbitrary fact.

Contradictory evidence must not silently overwrite an existing conclusion.

Invalidating one evidence record must not invalidate a fact if other valid supporting evidence remains.

Fact disposition must be deterministic and independent of evidence insertion order.

#### D. Role-Specific Context

Each role must have an isolated context area.

ASK context may contain:

- Reasoning progress.
- Relevant fact references.
- Read-only findings.
- Relevant artifact references.

FUZZ context may contain:

- Investigation strategy.
- Hypothesis references.
- Reproduction progress.
- Experiment and benchmark references.

BUILD context may contain:

- Implementation plan.
- Planned mutation scope.
- Patch references.
- Verification progress.

One role must not overwrite another role's context.

Role-specific context must not become an authority source.

#### E. Persistence and Recovery

Reuse the existing task persistence mechanism.

The task must preserve shared and role-specific context through:

- Checkpoint saves.
- Detach and resume.
- Application restart.
- Archive and restoration, where supported by the existing lifecycle.

Persistence must preserve backward compatibility with existing task data.

Malformed optional context must be handled safely without silently corrupting the task.

Future unsupported context versions must not be interpreted as valid current data.

Failed writes must not leave in-memory and persisted task state inconsistent.

#### F. Deterministic Context Assembly

Provide role-aware context assembly.

The result must:

- Include shared context.
- Include only the active role's private working context.
- Preserve relevant constraints.
- Distinguish verified facts from hypotheses.
- Include evidence references and claim dispositions.
- Include continuity warnings when information is missing or repaired.
- Exclude irrelevant role-specific context.
- Respect existing token and context budgets.

Legacy untyped findings must not be presented as verified facts.

### 4.3 Phase 1 Non-Goals

Do not implement:

- Automatic role switching.
- Bidirectional handoff orchestration.
- A handoff approval workflow.
- Cross-role workspace transfer.
- A unified task timeline.
- A new checkpoint orchestration engine.
- New completion semantics.
- New model-facing tools for self-certifying evidence or promoting claims.
- A new general-purpose memory system.

### 4.4 Phase 1 Acceptance Criteria

Phase 1 is accepted only when all of the following are verified against the actual implementation.

**Data integrity**

- Task identity remains stable across role changes.
- Shared context survives checkpoint, restart, detach, and resume.
- Role-specific contexts remain isolated.
- Facts and hypotheses cannot be confused.
- Model assertions cannot self-certify as facts.
- Evidence provenance is preserved.
- Contradictory evidence is handled deterministically.
- Invalidating one evidence record correctly reevaluates affected claims.
- Multiple supporting and contradicting records produce order-independent dispositions.

**Runtime integration**

- Actual runtime lifecycle paths use the task context APIs where required.
- Role context is not merely defined and persisted but meaningfully populated by host/control-plane lifecycle operations.
- Existing authority remains the source of the active role.
- Task context does not independently infer or grant authority.

**Context quality**

- Context assembly is deterministic.
- Context is bounded.
- Relevant older findings are not discarded solely because newer records exist.
- Irrelevant role-specific context is excluded.
- Missing or repaired context is surfaced truthfully.

**Recovery**

- Restart and resume restore the same task identity and structured context.
- No transcript reconstruction is required to recover the durable objective.
- Missing structured context is reported rather than silently invented.

**Integration acceptance scenario**

The following scenario must be exercised using existing runtime entrypoints or an appropriate test harness. It does not authorize Phase 2 implementation.

1. ASK establishes task understanding, verified findings, and open questions.
2. FUZZ creates a hypothesis, performs a reproduction or experiment, records evidence, and resolves the hypothesis.
3. BUILD consumes shared context and BUILD-specific context, performs an implementation, and records patch and verification progress.
4. FUZZ resumes investigation against the latest implementation state without source mutation.
5. BUILD resumes using its previous context and the newly relevant shared findings.
6. The application restarts.
7. The same task is resumed with its identity, shared context, evidence, and all role-specific contexts intact.

Acceptance requires:

- No task identity loss.
- No role-context loss.
- No authority escalation.
- No hypothesis/fact confusion.
- No unnecessary transcript reconstruction.
- No loss of relevant evidence or artifact references.

### 4.5 Phase 1 Completion Gate

Before Phase 2 is authorized, provide:

- An architectural audit of the actual implementation.
- A review of evidence association and invalidation semantics.
- A review of runtime RoleContext integration.
- Results of the integration acceptance scenario.
- Verification results.
- Remaining limitations and risks.
- Explicit confirmation that no Phase 2 functionality was implemented prematurely.

A passing unit-test suite alone is insufficient.

---

## Phase 2 — Handoff: Bidirectional Role Transition

**Status: IMPLEMENTED — PENDING ARCHITECTURAL REVIEW AND ACCEPTANCE**

### 5.1 Objective

Enable controlled, bidirectional transitions between ASK, FUZZ, and BUILD while preserving task identity, relevant shared context, role-specific progress, workspace state, and authority boundaries.

A role transition changes the active execution authority.

It must not create a new objective or silently reset the task.

### 5.2 Core Requirement

The system must support transitions in both directions, especially:

- FUZZ → BUILD.
- BUILD → FUZZ.

It must also preserve the existing ASK behavior and support transitions involving ASK where appropriate.

The transition must be controlled by the existing authority and approval mechanisms.

### 5.3 Handoff Contract

Introduce a structured handoff representation only if the existing task and runtime models cannot reasonably express the required transition.

The handoff contract should describe:

- Stable task identity.
- Source role.
- Target role.
- Current objective.
- Relevant shared context references.
- Source-role progress.
- Target-role resume context.
- Relevant facts and hypotheses.
- Evidence and artifact references.
- Outstanding questions.
- Constraints and known risks.
- Workspace state relevant to the transition.
- Required authority validation.
- Human approval state, where required.

The contract is a continuity and transition description.

It is not an authority grant.

The model may propose a transition or contribute information, but the host/control plane must validate and control the transition.

### 5.4 Transition Requirements

#### A. Source Context Preservation

Before transitioning away from a role:

- Preserve its relevant working context.
- Preserve outstanding work and artifact references.
- Preserve relevant evidence associations.
- Preserve the task's shared context.
- Do not discard unfinished work silently.

The source role must remain resumable.

#### B. Target Context Restoration

When entering a role:

- Load shared task context.
- Restore the target role's previous context if one exists.
- Include relevant findings from other roles through shared context.
- Exclude unrelated private working context.
- Provide a concise transition summary.
- Preserve the active objective and constraints.

The target role must not need to reconstruct the complete task from conversation history.

#### C. Authority Validation

Every transition must use the existing authority system.

The system must verify that:

- The requested target role is permitted.
- The transition does not widen authority implicitly.
- The target role receives only its existing permissions.
- Context does not grant additional permissions.
- Required human approval is obtained.
- Failed or rejected transitions do not alter authority.

FUZZ must not gain BUILD mutation authority through a handoff.

BUILD must not inherit FUZZ's investigative authority as an implicit permission expansion.

#### D. Workspace Continuity

Workspace state must remain truthful and recoverable.

In particular:

- FUZZ must be able to inspect the relevant current project state without acquiring source mutation permission.
- BUILD must be able to resume with its existing changes and artifacts.
- A transition must not silently reset, discard, overwrite, or replace dirty work.
- Experimental artifacts must remain distinguishable from project-source changes.
- Conflicts and incompatible workspace states must be surfaced explicitly.

Do not introduce a second workspace authority or mutation mechanism.

#### E. Failure and Recovery

The transition must handle:

- Rejected proposals.
- Failed authority validation.
- Incomplete context persistence.
- Workspace conflicts.
- Application interruption.
- Failed target-role initialization.
- Failed checkpoint operations.

A failed transition must not leave the task in an ambiguous or falsely advanced state.

The last valid task and authority state must remain recoverable.

### 5.5 UI/UX Requirements

Preserve ohgo's existing interaction model.

Do not introduce a separate StrategyPicker or a new centered workflow popup.

Use the existing in-chat Proposal Permission interaction for transition proposals where applicable.

The interface must make the following clear:

- Current task.
- Current active role.
- Proposed target role.
- Why the transition is needed.
- What context and progress will be carried forward.
- What authority will change.
- Whether approval is required.
- Whether the transition succeeded or failed.

A transition must not be visually presented as successful before the host confirms the state change.

Maintain one continuous task experience rather than creating disconnected role-specific conversations.

### 5.6 Phase 2 Non-Goals

Do not implement:

- A general-purpose workflow orchestration framework.
- Autonomous role switching without existing authorization.
- A new agent hierarchy.
- Agent cloning or parallel autonomous role instances.
- A generic event-sourcing system.
- A second approval system.
- A new workspace authority.
- Phase 3 timeline and recovery features beyond what is strictly required for safe handoff.

### 5.7 Phase 2 Acceptance Criteria

**Bidirectional continuity**

- FUZZ → BUILD preserves the objective, relevant evidence, investigation results, and artifacts.
- BUILD → FUZZ preserves implementation progress and the current project state.
- Returning to a previously active role restores its role-specific context.
- Repeated transitions do not duplicate, corrupt, or lose task context.

**Authority**

- Every transition is validated by the existing authority system.
- No transition implicitly widens permissions.
- FUZZ cannot mutate project source.
- ASK remains read-only.
- BUILD remains the sole project-source mutation authority.
- External side effects continue to require their own authorization.

**Workspace**

- Dirty changes are not silently discarded.
- Source and experimental artifacts remain distinguishable.
- Conflicts are reported truthfully.
- Failed transitions preserve recoverable state.

**UX**

- One task identity remains visible throughout transitions.
- The current and target roles are unambiguous.
- Approval is explicit when required.
- The existing proposal interaction is reused.
- Success and failure states reflect actual host outcomes.

**Recovery**

- An interrupted transition can be diagnosed and safely recovered.
- The system can identify the last valid role and task state.
- A failed transition cannot falsely report a successful role change.

### 5.8 Phase 2 Completion Gate

Before Phase 3 is authorized, provide:

- A transition architecture audit.
- A documented state-transition model.
- Bidirectional integration test results.
- Authority-boundary test results.
- Workspace preservation test results.
- Failure and interruption recovery test results.
- UI/UX verification.
- Verification results.
- Remaining risks and limitations.

Do not begin Phase 3 automatically.

---

## Phase 3 — Continuity: Timeline, Recovery, and Verifiable Outcome

**Status: NOT STARTED — NOT AUTHORIZED**

### 6.1 Objective

Complete the task-continuity lifecycle so that a user can understand what happened, inspect the evidence, recover interrupted work, and determine whether the original objective has actually been achieved.

The system must provide truthful, traceable task progression across sessions and role transitions.

### 6.2 Task Progression and Timeline

Provide a coherent view of task progression using existing task and audit infrastructure.

The timeline should represent meaningful task events, such as:

- Task creation.
- Objective changes, where permitted.
- Progress checkpoints.
- Important findings.
- Hypothesis resolution.
- Evidence attachment or invalidation.
- Role transitions.
- Approval decisions.
- Artifact creation or replacement.
- Verification results.
- Detach and resume.
- Completion or failure.

The timeline must not become a duplicate copy of every conversation message or tool call.

Store meaningful state transitions and references rather than duplicating large execution payloads.

Existing authoritative state remains authoritative.

### 6.3 Evidence and Artifact Lineage

Users must be able to understand how a conclusion or outcome was reached.

Relevant facts, hypotheses, patches, experiments, and verification results should retain traceable relationships to their supporting evidence and artifacts.

The system must distinguish:

- What was proposed.
- What was executed.
- What was observed.
- What was verified.
- What remains uncertain.
- What was invalidated or superseded.

Do not treat a successful command exit code as proof of semantic correctness.

Verification evidence must describe what was actually checked and observed.

### 6.4 Checkpoint and Recovery

Provide reliable recovery from interrupted or failed task execution using existing persistence and lifecycle mechanisms.

Recovery must account for:

- The last valid task checkpoint.
- Current role and role-specific progress.
- Relevant workspace state.
- Outstanding operations.
- Evidence and artifact references.
- Pending or interrupted transitions.
- Known continuity warnings.

Recovery must not invent progress or mark incomplete work as completed.

The user must be able to distinguish:

- Resumed from a valid checkpoint.
- Reconstructed with missing information.
- Blocked by an unrecoverable inconsistency.
- Requiring human intervention.

### 6.5 Objective State and Completion

Completion must be evidence-based.

The system must distinguish at least:

- Active.
- Blocked.
- Needs review.
- Interrupted.
- Completed and proven.

Use existing objective-state and run-outcome mechanisms where possible.

Do not create competing completion semantics.

`PROVEN` must mean that the objective's required conditions have been checked against relevant evidence and that the observed result supports completion.

The system must not mark an objective as proven merely because:

- The model claims it is complete.
- A plan was generated.
- A patch was written.
- A command exited successfully.
- A task was detached or archived.
- A verification step was skipped.

If required verification is unavailable, incomplete, or inconclusive, completion must remain unproven.

### 6.6 User Observability

The user must be able to understand:

- What the task is trying to achieve.
- What has been completed.
- What evidence supports the current conclusions.
- Which roles contributed to the result.
- What changed in the workspace.
- What remains unresolved.
- Why the task is blocked or needs review.
- Whether the objective is actually proven.

Observability should use existing TUI conventions and task views.

Do not introduce a separate dashboard or a new interaction model without a concrete requirement.

### 6.7 Phase 3 Non-Goals

Do not turn task continuity into:

- A general-purpose project management platform.
- A generic long-term memory engine.
- A complete event-sourcing framework.
- A second execution history database.
- A second verification engine.
- An autonomous self-improvement system.
- An unrestricted multi-agent orchestration framework.

### 6.8 Phase 3 Acceptance Criteria

**Timeline**

- Meaningful task events are traceable.
- Events retain appropriate provenance.
- Timeline data does not duplicate large tool outputs or conversation transcripts.
- Historical state is not silently rewritten.

**Evidence lineage**

- Important conclusions can be traced to evidence.
- Artifact references remain resolvable.
- Contradictions and invalidations remain visible.
- Proposed, executed, observed, and verified states are distinguishable.

**Recovery**

- Interrupted work resumes from a valid checkpoint.
- Incomplete transitions are detected.
- Workspace state is not silently discarded.
- Missing information is reported explicitly.
- Recovery never fabricates successful progress.

**Completion**

- Objective completion is based on defined conditions and relevant evidence.
- `PROVEN` is only reached after the required verification succeeds.
- Inconclusive or missing evidence prevents false completion.
- The final outcome is explainable and traceable.

**End-to-end acceptance scenario**

1. Create a task with a clearly defined objective.
2. Use ASK to establish understanding and constraints.
3. Use FUZZ to investigate an uncertainty and produce evidence.
4. Transition to BUILD through the authorized handoff mechanism.
5. Implement the required change and record verification.
6. Return to FUZZ if additional investigation is necessary.
7. Resume BUILD with preserved implementation context.
8. Interrupt the application and restore the task.
9. Inspect the task timeline and evidence lineage.
10. Verify the objective against its required conditions.
11. Mark the task proven only when the evidence supports completion.

Acceptance requires continuous identity, recoverable state, preserved authority, traceable evidence, and truthful final outcome.

### 6.9 Phase 3 Completion Gate

Provide:

- End-to-end architecture review.
- Timeline and lineage verification.
- Recovery and interruption test results.
- Objective-state transition test results.
- Evidence-based completion test results.
- Authority-boundary regression results.
- Full project verification results.
- Documentation updates.
- Remaining limitations and explicit operational constraints.

---

# 7. Global Execution Protocol

These rules are mandatory for every implementation phase.

## 7.1 Understand Globally, Execute Locally

Read and understand this complete specification before making implementation decisions.

The complete specification defines:

- The final architectural direction.
- Dependencies between phases.
- Global invariants.
- Future constraints.
- Final acceptance expectations.

However, understanding future phases does not authorize implementing them.

Only the explicitly authorized phase may be implemented.

## 7.2 Explicit Phase Authorization

At the beginning of each execution, identify:

- The currently authorized phase.
- Its current status.
- The exact scope permitted.
- Its acceptance criteria.
- Explicitly prohibited future-phase work.

Do not advance to another phase without explicit human authorization.

A phase being technically ready does not constitute permission to start it.

## 7.3 No Speculative Implementation

Do not implement future-phase functionality, speculative abstractions, or infrastructure merely because they may be useful later.

A future requirement may influence a current design only when doing so is necessary to preserve a documented architectural invariant or avoid a demonstrable compatibility problem.

Document such dependencies rather than implementing future functionality prematurely.

## 7.4 Reuse Before Adding

Before adding a new abstraction:

1. Identify the existing owner of the relevant responsibility.
2. Inspect its lifecycle and integration points.
3. Determine whether it can reasonably support the requirement.
4. Explain the concrete limitation.
5. Demonstrate that the requirement belongs to the authorized phase.
6. Prefer the smallest additive change that preserves existing contracts.

Do not introduce a new package, store, manager, registry, engine, or orchestration layer without explicit justification.

## 7.5 No Silent Scope Expansion

If implementation reveals a requirement outside the authorized phase:

- Stop at the boundary.
- Explain the dependency.
- Identify the affected existing owner.
- Describe the minimum change needed.
- State whether the current phase can still be completed safely.
- Request human authorization if scope expansion is necessary.

Do not silently implement the next phase.

## 7.6 Preserve Existing Behavior

Do not alter existing role semantics, permission boundaries, task lifecycle contracts, persistence ownership, or session behavior unless the authorized phase explicitly requires a change.

Any required compatibility change must be documented and tested.

## 7.7 Evidence-Based Reporting

Distinguish:

- Implemented.
- Tested.
- Verified.
- Not verified.
- Blocked.
- Deferred.

Do not claim completion based solely on code changes or passing unit tests.

Reports must describe actual inspected code and executed verification, not intended behavior.

---

# 8. Required Engineering Standards

Follow the repository's current `AGENTS.md`, architecture rules, and established conventions.

At minimum:

- Keep implementation in Go and follow existing package ownership.
- Run `gofmt`.
- Run `go build ./...`.
- Run `go vet ./...`.
- Run `go test ./...`.
- Run relevant race tests.
- Run the repository's configured linter.
- Add focused unit and integration tests.
- Preserve error identity and wrapping conventions using `errors.Is`, `errors.As`, and `%w` where appropriate.
- Avoid introducing `any` without a documented and justified need.
- Avoid leaking global state across tests.
- Avoid unnecessary dependencies.
- Avoid duplicating large tool outputs.
- Keep persistence backward-compatible where required.
- Update architecture documentation only when the implementation changes the documented architecture.
- Remove or correct stale documentation introduced by the change.

Do not report a verification command as passed unless it was actually executed and its result inspected.

---

# 9. Required Deliverables for Every Phase

Every phase must produce:

1. **Architecture audit**
   - Existing owners.
   - Existing contracts.
   - Relevant lifecycle and integration points.
   - Constraints and dependencies.

2. **Implementation summary**
   - Files and packages changed.
   - Responsibilities added or modified.
   - Persistence and runtime effects.

3. **Invariant review**
   - Authority preservation.
   - Task/session/role separation.
   - Evidence integrity.
   - Context isolation.
   - Backward compatibility.

4. **Verification report**
   - Commands executed.
   - Test results.
   - Integration scenarios.
   - Known unverified areas.

5. **Scope report**
   - Requirements completed.
   - Requirements deferred.
   - Future-phase dependencies.
   - Any deviation from the specification.

6. **Acceptance recommendation**
   - PASS.
   - PASS WITH DOCUMENTED LIMITATIONS.
   - FAIL.
   - BLOCKED.

A phase must not be considered accepted merely because the implementation report recommends PASS.

Human review is the final acceptance authority.

---

# 10. Current Phase Status

| Phase | Status | Authorization |
|---|---|---|
| Phase 1 — Foundation | Implemented, correction reviewed; pending final acceptance | Phase 1 corrections authorized |
| Phase 2 — Handoff | Implemented, pending review and acceptance | Explicitly authorized |
| Phase 3 — Continuity | Not started | Not authorized |

The existing Phase 1 implementation is the starting point for review.

Do not rewrite it merely to match this document.

First inspect the actual implementation, identify gaps against Phase 1 acceptance criteria, and propose the minimum necessary corrections.

Do not assume the implementation report proves correctness.

---

# 11. Initial Execution Instruction

The first execution of this specification must be limited to **Phase 1 review and acceptance**.

The agent must:

1. Read this complete document.
2. Read the repository's `AGENTS.md` and relevant architecture rules.
3. Inspect the existing Phase 1 implementation.
4. Trace actual runtime integration points that populate shared and role-specific context.
5. Review evidence association, contradiction, invalidation, and supersession semantics.
6. Verify deterministic and bounded context assembly.
7. Verify persistence, restart, detach, resume, and archive behavior.
8. Execute the Phase 1 integration acceptance scenario.
9. Run the required verification commands.
10. Report findings with file references and concrete evidence.
11. Identify the minimum required corrections, if any.
12. Stop and request human review before Phase 2.

**Do not implement Phase 2 or Phase 3.**

**Do not introduce speculative infrastructure for future phases.**

**Do not treat a passing test suite as sufficient evidence of architectural acceptance.**

The immediate objective is not to maximize the amount of code written.

It is to establish that the existing Phase 1 foundation is correct, durable, bounded, and capable of supporting the next phase without architectural rework.

---

# 12. Final Architectural Direction

Task continuity must remain a capability of ohgo's existing control and continuity architecture.

It must not become an independent orchestration system.

The task owns continuity.

The active role defines execution authority.

The control plane validates transitions and governs state changes.

The execution plane performs only authorized work.

Evidence supports conclusions.

The human retains control over phase authorization and consequential authority changes.

**Understand the complete objective. Preserve the existing architecture. Implement only the authorized phase. Verify every state transition.**
