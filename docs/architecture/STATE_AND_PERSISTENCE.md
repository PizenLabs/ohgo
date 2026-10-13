# State, Persistence & Memory Boundaries

How OHGO separates machine, project, session, derived, and runtime state — and
where each boundary is drawn, why, and what owns it.

This document is the ownership contract for persisted state and memory. The
runtime authority contract lives in [`ARCHITECTURE_CONTRACT_V1.md`](ARCHITECTURE_CONTRACT_V1.md);
this file answers a narrower question: **who owns each datum, where does it
live, what survives what, and what is never allowed to become durable.**

Two rules frame everything below:

> **The source tree is the authority. Everything derived is disposable.**
>
> **Model output is input, never authority.**

---

## A. Current Architecture (audit)

### Machine scope — `~/.ohgo`

| Path | Meaning | Owner | Lifecycle |
| --- | --- | --- | --- |
| `settings.json` | machine-global user settings: global user defaults (`identity.name`), model/role bindings, approvals, theme, compaction | `config` | machine; authoritative |
| `credentials.json` | provider API keys, `0600`, atomic write | `creds` | machine; authoritative; never in git |
| `sessions.db` (+ `-wal`, `-shm`) | SQLite session store: sessions, messages, artifacts | `session` | machine; authoritative; survives every project |
| `cache/<provider>-models.json` | provider/model discovery rosters, `schema_version`-stamped | `catalog` | machine; **derived**, rebuildable |
| `history` | plain-REPL prompt audit trail, last 1000 lines, commands excluded | `cmd/ohgo` | machine; ephemeral audit, best-effort |

`~/.ohgo` is resolved through one owner, the `state` package (`state.Dir()`,
`state.Path(...)`). Before that change `config`, `session`, `creds`, `catalog`,
and `cmd/ohgo` each joined `home + ".ohgo"` privately; five copies was five
chances to drift, and `config.Dir()` was dead code. Path resolution is now one
function.

### Project scope — `<root>/.ohgo`

| Path | Meaning | Owner | Lifecycle |
| --- | --- | --- | --- |
| `project.yml` | `{id, name, root, user.name}` — stable project identity, human-readable project name, and the project worker identity | `project` | project; authoritative; machine-local, gitignored |

The project root is the Git worktree root when the directory is inside a repo,
otherwise the directory itself (`project.Root`). A nested directory and the
repository it lives in are one project, not two. `id` is the identity; `name`,
`root`, and `user.name` are metadata that tolerate change — `root` may go stale
when the tree moves, and renaming `name` or `user.name` never changes `id`.

**Project worker identity is project-local.** `user.name` is the human working
with *this* project, collected by the initialization gate. It is deliberately
*not* machine-global: Project A can name Alice while Project B on the same
machine names Bob. The machine-global `settings.json` may carry a default
`identity.name` that seeds the gate's prompt, but it is never the authority for
a project's worker, and the gate never writes it.

### Session scope — `sessions.db`

```text
sessions   (id, created_at, updated_at, cwd, model, workspace, project, title)
messages   (id, session_id → sessions, seq, role, payload, created_at)
artifacts  (id, session_id → sessions, artifact_id, kind, origin, payload, created_at)
```

One machine-global database holds every project's sessions. Project association
is the `project` column — logical ownership — not a database per project.

### Project instructions — human-authored

| Structure | Status |
| --- | --- |
| `<project>/AGENTS.md` (primary; nearest, walked upward from root) | **exists**; read first by `prompt.ProjectContext`; human-authored, versioned, authoritative |
| `<project>/.ohgo/AGENTS.md` (optional, supplementary OHGO-local) | **read**; appended after the primary file; never written by OHGO |
| project-memory database | **does not exist**; the `memory` package defines the promotion seam without storage |

There is no LLM-authored durable project knowledge anywhere in the codebase.
The only durable knowledge about a project is a file a human wrote.

### Derived code intelligence — stateless today

| Structure | Status |
| --- | --- |
| `ast` package | a **stateless** per-file searcher (`go/ast`); no index, no graph, no storage |
| `graph.db` | **does not exist**; the lifecycle is defined below, the store is deferred |

### Runtime scope

`options`, the harness, the agent loop, TUI `State`, composer, viewport, jobs,
streaming buffers, the `$sup` conversation — all process-local, none persisted
except through the explicit session/artifact writers.

### Problems found by this audit

The prior persistence slice fixed the only concrete defects (five machine-path
owners; an unversioned model cache; no project-scoped session query). This slice
corrected one ownership defect the prior audit mis-classified: the human display
name collected during project initialization was being persisted to the
machine-global `settings.json` as `identity.name`, even though the gate is
establishing the relationship between a human and *one* project. The name now
belongs to `<project>/.ohgo/project.yml` (`user.name`), where two projects on one
machine can name two different workers. The machine-global `identity.name`
survives only as a suggested default for the gate's prompt.

Everything else stayed put: credentials never enter project state, `.ohgo`
deletion re-initialises, project identity survives a move, and no code promotes
model output into durable state because no such path exists.

---

## B. Final Architecture

```text
              OHGO STATE
                  │
      ┌───────────┼───────────────┐
      │           │               │
   MACHINE     PROJECT         SESSION
      │           │               │
 settings     project.yml      sessions.db
 credentials  (id, name,       ├─ messages
 caches        worker)         └─ artifacts
 history
      │           │               │
      └───────────┼───────────────┘
                  │
   PROJECT MEMORY (AGENTS.md, human-authored)
     promotion seam: `memory` (no store)
                  │
   DERIVED CODE INTELLIGENCE (ast, stateless)
     future graph.db: deferred, disposable
                  │
               RUNTIME
                  │
           never persisted
```

```text
~/.ohgo/                          <project>/.ohgo/
├── settings.json   (global)      ├── project.yml   (project identity +
├── credentials.json              │                  project worker identity)
├── sessions.db      (session)    └── [graph.db]    (future; derived; disposable)
├── cache/           (derived)       [memory.db]    (only if ever justified)
└── history          (audit)
```

**No new files are created by this slice.** `graph.db` and `memory.db` appear
here as reserved locations with a defined lifecycle, not as permission to write
them. A store is added only when something reads it; a database with no reader
is a speculative structure.

Five scopes, plus the memory seam, no more:

- **MACHINE** — one per user/machine, independent of any project.
- **PROJECT** — one per checkout, carries identity.
- **SESSION** — one per conversation, machine-global storage, project-labelled.
- **DERIVED** — rebuildable from an authoritative source; never a source of truth.
- **RUNTIME** — dies with the process.
- **MEMORY SEAM** — decides what may become durable project knowledge; owns no storage.

### Ownership contract

The authoritative layout is fixed and minimal:

```text
Global state            ~/.ohgo/                     machine/user scope
Project state           <project>/.ohgo/             one per checkout
Project identity        <project>/.ohgo/project.yml  the sole authority
Primary instructions    <project>/AGENTS.md          human-authored
Supplementary instrs    <project>/.ohgo/AGENTS.md    optional, OHGO-local
Session storage         ~/.ohgo/sessions.db          scoped by project_id
Derived state           ~/.ohgo/cache, future index  disposable, rebuildable
```

Two separations carry the whole contract:

```text
global defaults   ≠   project authority
    A name in ~/.ohgo/settings.json (`identity.name`) may seed the
    initialization prompt. After initialization the project identity resolves
    from project.yml only; global settings are never its runtime authority.

project identity   ≠   project instructions
    project.yml answers "what project is this / who works on it".
    AGENTS.md answers "how should an agent operate here".
    Neither is written into the other.
```

Rule of thumb: **put each piece of state in the smallest scope that owns it, and
never let global state become an accidental authority for project-local state.**

---

## C. Ownership Matrix

| Concern | Owner | Physical storage | Authority | Lifecycle | Recovery |
| --- | --- | --- | --- | --- | --- |
| global user defaults (`identity.name`) | `config` | `~/.ohgo/settings.json` | suggestion only | machine | backup file |
| user preferences | `config` | `~/.ohgo/settings.json` | authoritative | machine | backup file |
| credentials | `creds` | `~/.ohgo/credentials.json` (`0600`) | authoritative | machine | re-enter |
| session history | `session` | `~/.ohgo/sessions.db` | authoritative | machine | backup DB |
| session memory (task context, artifacts) | `session` | `~/.ohgo/sessions.db` | authoritative | session | backup DB |
| project identity (`id`) | `project` | `<project>/.ohgo/project.yml` | authoritative | project | re-mint |
| project name (`name`) | `project` | `<project>/.ohgo/project.yml` | authoritative | project | re-establish via gate |
| project worker identity (`user.name`) | `project` | `<project>/.ohgo/project.yml` | authoritative | project | re-establish via gate |
| project durability (human knowledge) | `prompt` (reader) | `AGENTS.md` in git | authoritative | project (versioned) | `git` |
| promotion trust boundary | `memory` | **none** (seam) | authoritative decision | process | n/a |
| project memory store | — | **not implemented** | — | — | — |
| code intelligence | `ast` | **none** (stateless) | derived from source | process | n/a |
| code graph | — | `<project>/.ohgo/graph.db` (reserved) | derived; source wins | derived | delete + rebuild |
| model cache | `catalog` | `~/.ohgo/cache/*.json` | derived | machine | delete + refetch |
| prompt history | `cmd/ohgo` | `~/.ohgo/history` | derived | machine | delete |
| runtime (loop, turn, stream, jobs) | runtime | none | authoritative while live | process | n/a |
| TUI state | TUI/application | none | display only | process | n/a |

Rules this matrix makes true:

- **One owner per concern.** No renderer persists memory; no provider writes
  project state; no command handler invents a storage path (all machine paths
  go through `state`).
- **Derived is disposable.** Deleting `cache/`, a future `graph.db`, or
  `history` costs a rebuild, never source truth.
- **Memory has a trust gate.** Project knowledge either a human authored
  (`AGENTS.md`) or passed `memory.Promote` with sufficient provenance.

---

## D. Session Memory Boundary

A session owns everything needed to resume **one conversation**:

- messages and turns (the transcript);
- compaction summaries and the rebased history they produced;
- structured artifacts (`workspace.Artifact`: plan, finding, change, verification);
- active task context, files discussed, unresolved questions, observations.

Physical storage stays machine-global at `~/.ohgo/sessions.db`. Logical
ownership is the `project` column, queried by `session.Store.ListByProject`.
Sessions are stored globally because:

- a project may have many sessions, and a project-local database per session
  would duplicate the store;
- `sessions.db` is authoritative and must survive deleting `<project>/.ohgo`;
- moving sessions into each project would make session history part of derived
  project state, which it is not.

**Session memory is not project memory.** A decision made in one conversation is
visible only when that conversation is resumed. It becomes project knowledge only
through the explicit promotion seam (§G), never by existing.

`ListByProject` includes rows with `project = ''` (sessions recorded before
project identity existed) so history is never hidden; a session naming another
project is excluded and remains reachable by id. This is deliberate and tested.

---

## E. Project Memory Boundary

Project memory answers:

> What durable knowledge about this project should survive across sessions?

Today that knowledge lives in **`AGENTS.md`**, and only there:

- it is **human-authored** — the strongest provenance there is;
- it is **versioned in git**, so it has history and review;
- it is **read, never written** by the runtime: `prompt.ProjectContext` renders
  it into the system prompt each turn. No tool or model writes it.

There are exactly two instruction locations, with a fixed, deterministic
precedence:

```text
<project>/AGENTS.md            primary project instructions
        │                      discovered first (nearest at or above the cwd)
        ▼
<project>/.ohgo/AGENTS.md      supplementary OHGO-local instructions
                               appended after the primary file
```

The root file is the file users normally create and maintain; it is the primary
project-level instruction contract. The optional `.ohgo/AGENTS.md` is
OHGO-local supplementary instruction and is concatenated **after** the root
file, so its presence can never silently discard the root instructions. There
is no conflict-resolution system beyond that ordering: both files are rendered
verbatim, root first. `.ohgo/AGENTS.md` is read from the project root, not from
whatever directory the process started in.

One caveat, recorded rather than hidden: the primary file is found by walking
**up** from the working directory and taking the first `AGENTS.md`, so a file
in an ancestor directory (outside the project) can be read when the project
carries none. That is intended for a monorepo whose root carries shared
instructions, but it means "the nearest `AGENTS.md`" is not strictly confined to
the project. A future project-memory store must be strictly project-local; an
ancestor file is context, not project memory, and must never be written as a
project fact.

Consequences:

- There is **no `memory.db`** and this slice does not create one. No consumer
  exists — nothing assembles project memory into context except the AGENTS.md
  reader — and a store with no reader is speculative.
- The **seam** that would own a store, if one is ever justified, is the
  `memory` package (§G). It is the single place the trust boundary is decided.
- When a store is justified it belongs at `<project>/.ohgo/memory.db`:
  project-local, durable, independently queryable, provenance-aware, isolated
  between projects, and deleted only by explicit user action. It must **not** be
  merged with `graph.db`: one holds durable knowledge, the other derived
  structure, and they have different recovery semantics.

What does **not** belong to project memory: raw conversation, session summaries,
code structure, or anything a model asserted without confirmation.

---

## F. Graph Boundary

Code intelligence is **derived**, not memory. The source tree is the authority.

Today the `ast` package is a stateless per-file searcher: it parses a file,
returns declarations, and writes nothing. There is no index, no cache, and no
`graph.db`. That is sufficient for the current `ast-grep` tool and is preserved.

**A future `graph.db` (`<project>/.ohgo/graph.db`) is deferred**, with this
lifecycle fixed now so the store is not invented ad hoc later:

```text
No graph → initial index → graph.db
graph.db → validate metadata
             ├── fresh   → reuse
             ├── stale   → incremental update
             └── invalid → rebuild
```

Metadata sufficient to decide reuse (reserved, not yet written):

```text
project_id                 # binds the graph to the project identity
source_root                # absolute root it indexed
schema_version             # store layout this build wrote
indexer_version            # parser/indexer that produced it
source fingerprint         # content/revision the graph reflects
generated_at               # when
```

Storage policy, when implemented:

- project-local, derived, disposable, rebuildable;
- **never required for basic project startup** — lazily created, never on the
  startup path;
- corruption must never prevent opening the project (treat as absent, rebuild);
- no coupling to providers or LLM code, and no conversation history inside it;
- the graph answers *what symbols exist, where a function is defined, what calls
  it, what a package depends on* — never what the user decided.

`graph.db` and `memory.db` stay separate even though both are project-local:
derived structure and durable knowledge recover differently.

---

## G. Promotion & Provenance (the trust boundary)

The runtime must never silently turn a model's claim into durable, authoritative
project knowledge. What a session observed is session state; what a human
confirmed may become project state.

### Provenance tiers

| Tier | Meaning | Authoritative? |
| --- | --- | --- |
| `Observed` | deterministic fact from project/source/tool state | yes (reproducible) |
| `Confirmed` | a human explicitly established it | yes |
| `Inferred` | a model-derived hypothesis | **no** |

### The promotion seam

`memory.Promote(candidate, now)` is the only door from session knowledge to
durable project memory:

```text
session observation
      │  explicit promote / confirmation
      ▼
memory.Promote  ── reject ──▶  Inferred without confirmation
      │
      ▼
project fact (durable, provenance preserved)
```

- `Observed` and `Confirmed` promote directly.
- `Inferred` with `Confirmed` set promotes and **keeps** `Provenance = Inferred`
  with `Confirmed = true`: a hypothesis a human accepted stays labelled as one,
  so the trust trail is preserved rather than rewritten.
- `Inferred` without confirmation returns `ErrUnconfirmed` — never stored.

A promoted `Fact` answers the questions a durable record must: *what is the
fact* (`Statement`), *why does OHGO believe it* (`Provenance`), *where did it
come from* (`Source`), *when* (`Established`), *was it human-confirmed*
(`Confirmed`), *is it still valid* (`Valid`).

The seam owns **no storage**. Storage is deferred until something reads it; the
seam exists so the trust rule has exactly one owner when that day comes.

---

## H. Recovery Semantics

| Event | Global user defaults | Sessions | Project identity + worker | Project `.ohgo` |
| --- | --- | --- | --- | --- |
| `rm <project>/.ohgo/project.yml` | survive | survive | re-minted/re-established on next run | re-created |
| `rm -rf <project>/.ohgo` | survive | survive | re-minted (new id); worker re-established project-locally | re-created |
| delete `graph.db` (future) | survive | survive | survives | project still opens; graph rebuilds |
| delete `memory.db` (future) | survive | survive | survives | re-created empty; **durable knowledge is lost** — explicit action only |
| project directory deleted | survive | survive (machine-global) | gone with the tree | gone |
| project renamed/moved | survive | survive (keyed by id) | **survives** (`.ohgo` moves with the tree; stale `root` tolerated) | survives |
| cache deleted | survive | survive | survives | rebuilt on discovery |
| one session deleted | survive | that session gone | survives | survives |
| `~/.ohgo` corrupted | possibly lost | possibly lost | survives | survives |

**Historical-session reassociation is not supported and is not inferred.** If
`<project>/.ohgo` is deleted, the project is re-initialised with a **new** id;
existing session rows keep the old id and do **not** silently reattach to the new
project merely because the directory name, path, or project name matches. Any
future reattachment must be an explicit, provenance-carrying operation, not a
guess. This slice documents the gap rather than inventing inference.

`rm -rf .ohgo` ≠ delete session history. Project identity — the id, the project
name, and the project worker identity — is what a deleted `.ohgo` loses,
re-established on the next launcher run. The machine-global default name
survives in `settings.json` and only seeds the re-initialization prompt; it is
never copied in as authority.

**Backup unit:** the smallest *safe* machine backup is `settings.json` +
`credentials.json` + a **SQLite-consistent** copy of `sessions.db` (never copy
`-wal`/`-shm` independently). A project backup is `project.yml` plus `AGENTS.md`;
it must never be treated as a backup of session history.

---

## I. Migration

No destructive migration was performed and none is needed.

- **Settings, credentials, `history`** — unchanged on disk; only path resolution
  moved behind `state` (prior slice).
- **Sessions** — schema is applied idempotently on open; `migrate` adds
  `workspace` and `project` additively without rewriting rows. `project = ''`
  means "unidentified" and is included by `ListByProject`.
- **Cache** — a version-0 file is still read; a cache this build cannot read is
  ignored and refetched; deletion is always safe.
- **`project.yml`** — gains `name` and `user.name`. An id/root-only file from an
  earlier build stays `Valid` and is completed through the initialization gate:
  the project name and worker are collected, and `project.id` is preserved
  byte-for-byte. No project's id is rotated by the schema change, and no missing
  semantic value is fabricated.
- **`settings.json`** — an existing `identity.name` becomes a suggested default
  for the worker prompt. It is never reinterpreted as authoritative project
  worker identity, never auto-copied into every project, and the gate never
  rewrites it.
- **`memory` package** — adds a pure-Go seam with no storage, so there is
  nothing on disk to migrate.

Every step is explicit, idempotent, and non-destructive.

---

## J. Tests & Validation

### New tests (this slice)

| Test | Contract it defends |
| --- | --- |
| `memory.TestInferredRequiresConfirmation` | model output never becomes durable project knowledge by being written down |
| `memory.TestConfirmedInferenceIsPromotedAndKeepsProvenance` | a human-approved hypothesis stays labelled as a hypothesis (trail preserved) |
| `memory.TestObservedIsPromotedWithoutConfirmation` | a reproducible fact stays distinguishable from a human-confirmed one |
| `memory.TestConfirmedProvenanceImpliesConfirmation` | a human statement is reported as human-confirmed |
| `memory.TestPromoteRecordsEstablishmentTimeInUTC` | establishment time is injected and comparable |
| `memory.TestPromoteRejectsMalformedCandidates` | a candidate missing a claim, tier, or source is refused at the seam |
| `project.TestDistinctProjectsHaveDistinctIDs` | two checkouts never share a project id |
| `project.TestProjectLocalMetadataShape` | the file's shape is id/name/root/user.name and absorbs no machine-global defaults |
| `session.TestStoreSchemaExcludesProjectKnowledge` | `sessions.db` never grows a project-knowledge or graph table |

Mutation checks: removing the `ErrUnconfirmed` branch fails
`TestInferredRequiresConfirmation`; forcing a constant project id fails
`TestDistinctProjectsHaveDistinctIDs`.

### Identity ownership tests (this slice)

| Test | Contract it defends |
| --- | --- |
| `startup.TestProjectWorkerIsolation` | Project A names Alice while Project B names Bob; reload confirms each from disk |
| `startup.TestWorkerIdentityNeverWrittenToGlobalSettings` | the gate persists the worker project-locally and never writes global settings |
| `startup.TestGlobalDefaultSeedsWorkerPrompt` | a global default only seeds the prompt; the confirmed value is project-local |
| `startup.TestIncompleteProjectRecoveredWithoutChangingID` | an id/root-only file is completed without rotating `project.id` |
| `startup.TestNeedsInitWithIncompleteProject` | a valid-but-incomplete project returns to the gate, not the fast path |
| `startup.TestDeleteOhgoInGitRepoRepromptsWorker` | deleting `.ohgo` re-establishes the project worker rather than resurrecting a global one |
| `project.TestEnsureWithRecordsMetadata` | `EnsureWith` persists name and worker atomically |
| `project.TestEnsureWithPreservesIDAndCompletes` | recovery adds metadata and preserves a fixed id |
| `project.TestMetadataChangeKeepsID` | changing name/worker never changes `id` |
| `project.TestIdentitySurvivesProjectMove` | id, name, and worker survive a move |
| `project.TestCompleteRequiresNameAndWorker` | completeness requires both metadata fields |
| `startup.TestFastPathDoesNotRewriteProjectFile` | a complete project.yml is byte-identical on restart |
| `cmd/ohgo.TestSessionContextResolvesProjectIdentity` | sessions stay project-scoped; A resolves Alice, never B's worker |

### Instruction hierarchy tests (this slice)

| Test | Contract it defends |
| --- | --- |
| `prompt.TestProjectContextReadsBothInstructionFiles` | root `AGENTS.md` is primary and present, `.ohgo/AGENTS.md` is supplementary, root read first |
| `prompt.TestProjectContextReadsOhgoLocalAlone` | the optional `.ohgo/AGENTS.md` contributes when it is the only file |
| `prompt.TestProjectContextReadsRootAlone` | a project with only the root file behaves as before; no empty supplementary section |
| `prompt.TestProjectContextEmptyWhenNoInstructions` | an unconfigured project emits no instructions block |
| `prompt.TestProjectContextAnchorsOhgoLocalAtProjectRoot` | `.ohgo/AGENTS.md` resolves at the project root from a nested working directory; nearest root `AGENTS.md` stays primary |

Mutation checks: ignoring `.ohgo/AGENTS.md` fails
`TestProjectContextReadsBothInstructionFiles` and
`TestProjectContextReadsOhgoLocalAlone`; anchoring the supplementary file at the
working directory instead of the project root fails
`TestProjectContextAnchorsOhgoLocalAtProjectRoot`; discarding the root file when
`.ohgo/AGENTS.md` exists fails the ordering assertion in
`TestProjectContextReadsBothInstructionFiles`.

Mutation checks: fast-pathing an incomplete project (ignoring `Identity.Complete`)
fails `TestNeedsInitWithIncompleteProject` and
`TestIncompleteProjectRecoveredWithoutChangingID`; re-minting the id whenever
metadata is supplied fails `TestEnsureWithPreservesIDAndCompletes`,
`TestMetadataChangeKeepsID`, and `TestIncompleteProjectRecoveredWithoutChangingID`.

### Existing tests that already defend these boundaries

- `project.TestIdentitySurvivesProjectMove` — project id survives rename/move.
- `session.TestStoreListByProjectScopesSessions` — project-scoped listing
  excludes other projects, keeps unidentified legacy rows.
- `state.TestDirIsUnderHome`, `state.TestDirReportsMissingHome` — machine state
  resolves under home and fails loudly without one.
- `catalog.TestCacheWithUnknownSchemaReadsAsAbsent` — a cache from a future
  schema is ignored, not decoded.
- `cmd/ohgo.TestMachineStateSurvivesProjectDeletion` — `rm -rf .ohgo`
  re-initialises the project while identity and session history survive.
- `startup` fast-path tests, `session` migration/persistence/replay, `config`
  malformed-config handling.

### Separation proven

```text
session memory  ≠ project memory   sessions.db has no knowledge table;
                                   project memory is AGENTS.md (human-authored)
project memory  ≠ graph            memory is durable human knowledge (seam);
                                   graph is derived structure (stateless today)
graph           ≠ source authority source is authoritative; ast writes nothing,
                                   a future graph.db is disposable
runtime state   ≠ durable state    TUI/loop run without a session store
```

### Repository gate

`gofmt -l .` empty, `go vet ./...`, `go build ./...`, `go test ./...`, and
`go test -race ./...` — all green. Real-CLI validation is run against an
isolated `HOME` and temporary Git projects (never the user's real `~/.ohgo`):

1. **Fresh project** — creates `<project>/.ohgo/project.yml` and
   `$HOME/.ohgo/sessions.db` only. No speculative directories, and `settings.json`
   stays absent: the worker is project-local.
2. **Second project** — distinct `project.yml` id; sessions shared physically,
   labelled by project. `project-a` records `worker = Alice`, `project-b` records
   `worker = Bob`, with no global name becoming authority for either.
3. **Restart + move** — the fast path prompts nothing and leaves `project.yml`
   byte-identical; moving the project preserves `id`, `name`, and `user.name`.
4. **Cancel** — EOF at the gate creates no `.ohgo/`, no project identity, and no
   global user-default mutation.
5. **`rm -rf proj-a/.ohgo` then run** — `project.yml` re-created with a fresh id
   and the worker re-established; `proj-b` untouched; session rows survive.
6. **Legacy global name** — an existing `settings.json` `identity.name` seeds the
   worker prompt only; the confirmed value lands in `project.yml` and the file is
   left unchanged.
7. **Delete the model cache** — `ohgo doctor` still reports the catalog and the
   session store; `cache/` stays absent until discovery refetches.
8. **Instruction hierarchy** — a project with both `<project>/AGENTS.md` and
   `<project>/.ohgo/AGENTS.md` submits the root instructions first and the
   OHGO-local instructions after it; with only one present, that one is read.

---

## Architectural Invariants

1. Global user defaults are machine/global state and only ever suggest a name;
   they are never the authority for a project worker identity.
2. Project identity (`id`), project name (`name`), and project worker identity
   (`user.name`) are project-local state. Changing name or worker never changes
   `id`; moving the project changes neither.
3. Session memory belongs to the session.
4. Project memory belongs to the project.
5. Graph data is derived code intelligence, not memory.
6. Source code remains the authority over graph-derived facts.
7. LLM output is never automatically authoritative persistent memory.
8. Promotion from session knowledge to project memory is explicit and
   provenance-preserving (`memory.Promote`).
9. Sessions remain physically global but logically project-scoped.
10. Runtime/TUI state is ephemeral.
11. Derived graph/cache state is rebuildable.
12. Deleting derived state must not destroy authoritative source or unrelated
    global state.
13. Project identity must not depend on the current filesystem path.
14. No persistent state may have multiple competing owners.
15. No speculative storage is introduced without a demonstrated lifecycle and
    consumer.
16. Project identity and project instructions are separate authorities:
    identity lives in `project.yml`, instructions in `AGENTS.md`; neither is
    written into the other.
17. Root `<project>/AGENTS.md` is the primary project instruction source and is
    read first; `<project>/.ohgo/AGENTS.md` is optional supplementary
    OHGO-local instruction, appended after it and never replacing it.
18. Project discovery is read-only; only the explicit initialization gate may
    create `<project>/.ohgo`.
19. Initialization requires explicit user commitment; a cancelled
    initialization leaves no partial project state.

## Explicit Non-Goals

This slice did **not** build: `graph.db`, `memory.db`, embeddings, semantic
search, autonomous memory learning, agent memory agents, vector stores, a generic
repository abstraction, multiple storage backends, or any new global directory.
It did not redesign execution, providers, model selection, approval semantics,
the TUI, the `ast` searcher, or the SQLite layer.
