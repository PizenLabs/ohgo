# OHGO Architecture Rules

> **This is the permanent development contract.** It answers one question for
> every future change: *where is this allowed to live, and what is it allowed to
> depend on?*
>
> Read `OHGO_ARCHITECTURE_MAP.md` for the current topology.
> `OHGO_ARCHITECTURE_MIGRATION.md` records how the tree got here.
>
> **Enforcement.** Rules marked **\[ENFORCED]** are checked by
> `internal/architecture/rules_test.go` and fail `go test ./...`. A rule that
> cannot be machine-checked is still a rule; it is enforced in review.

---

## 0. The one rule everything else serves

```text
New behaviour requires an owner.
New state requires a source of truth.
New capability requires an authority path.
New persistence requires a lifecycle owner.
New knowledge requires an intelligence owner.
New UI requires a projection boundary.
New dependency requires justification.
New package requires a semantic boundary.
New folder requires a domain.
New file requires cohesion.
```

**Decide ownership first, then write the code.** Adding code and deciding where
it belongs afterwards is how the previous tree acquired a 2,898-line
`run.go` and eleven definitions of one persisted format.

---

## 1. Ownership is declared before the package exists **\[ENFORCED]**

Every package under `internal/` appears exactly once in the manifest
(`domainOf` in `internal/architecture/rules_test.go`), tagged with the domain
that owns it. A package that is not declared fails
`TestManifestDeclaresEveryPackage`.

To add a package:

1. Name the decision or state it owns.
2. Prove no existing package owns it (search the map, then the code).
3. Declare it in the manifest.
4. Write down: owned state, owned invariants, allowed imports, forbidden
   imports, test boundary.

If step 1 or 2 is unclear, **do not create the package**. Stopping is a
successful outcome.

---

## 2. Implementation is encapsulated under `internal/` **\[ENFORCED]**

OHGO exposes no public Go API: it ships a binary. Therefore every production
package except

```
cmd/ohgo          the command
bench/*           benchmark harnesses
scripts/*         repository tooling
```

lives under `internal/`. A production package at the module root is an
*accidental* compatibility promise, and an accidental promise is worse than no
promise.

`internal/` is an encapsulation boundary, **not** an architecture. It says
"nobody outside may depend on this", not "this belongs together". Grouping
inside `internal/` follows §1 and §10, not convenience.

---

## 3. The runtime kernel has a closed allow-list **\[ENFORCED]**

`internal/agent` may import exactly:

```
internal/control/contract     internal/control/budget
internal/security             internal/jsonschema
internal/provider/ai          internal/tokens
```

Nothing else. The engine must not know about:

* concrete capabilities (`execution/*`)
* workspace policy or intent (`control/workspace`)
* task shape or evidence projection (`control/cognition`)
* persistence (`continuity/*`)
* presentation (`tui`)
* configuration or model facts (`config`, `catalog`)

Capabilities arrive as `agent.Tool`; projection as a function field; streaming as
`ai.StreamFn`; authority as `agent.Authorizer`; persistence as `agent.EditJournal`.
**If a feature needs the engine to know about a new domain, the answer is a new
seam, not a new import.**

---

## 4. A leaf that imports the module is not a leaf **\[ENFORCED]**

These packages must import **no** module package:

```
control/contract  control/budget  security      jsonschema  tokens  diff  edit
state             vcs             memory        knowledge   sandbox shell
intelligence/ast  intelligence/ignore
```

The value of each is that it knows nothing about its consumers. If one of them
genuinely needs a dependency, the declaration is wrong — change the declaration
deliberately, in the same commit, and say why. Do not add an import and leave the
label lying.

---

## 5. Dependency direction is a plane rule **\[ENFORCED]**

```
presentation → application → continuity → execution → control → runtime → provider → foundation
```

Concretely, the following are forbidden:

| From | Must not import |
|---|---|
| `internal/control/**` | `execution/**`, `continuity/**`, `commit`, `startup`, `update`, `history`, `gryhook`, `tui` |
| `internal/execution/**` | `continuity/**`, `tui`, `commit`, `startup`, `update`, `history` |
| `internal/continuity/**` | `tui`, `commit`, `startup`, `update`, `history` |
| `internal/intelligence/**` | `agent`, `control/**`, `execution/**`, `continuity/**`, `provider/**`, `tui` |

A new edge that points *up* is an architecture change. It needs the map and the
manifest updated, plus a reason recorded in the migration document.

---

## 6. The provider plane stays below the runtime **\[ENFORCED]**

`internal/provider/**` may import only `internal/provider/**` and
`internal/state`.

Providers speak wire protocols. They do not know about turns, tasks,
workspaces, approvals, or cost policy. Model *facts* (context window, pricing,
reasoning ladder) belong to `catalog`; *secrets* belong to `creds`; *transport*
belongs to `ai`. None of the three may know what a turn is.

**No policy by model identity.** Never branch on a model id, provider name, or a
hardcoded per-model table. Capabilities come from `ai.Model` fields the catalog
resolves.

---

## 7. The shell layer is a sink **\[ENFORCED]**

`commit`, `startup`, `update`, `history`, `gryhook`, and `tui` are application
and presentation. They may import the tree; **nothing under `internal/` except
`tui` may import them back.**

A shell that becomes a dependency of a lower layer has stopped being a shell: its
lifecycle now constrains the domain. If a domain needs something currently in a
shell, move that *thing* down (into a package that owns it), never the shell.

---

## 8. Presentation projects; it does not decide **\[ENFORCED]**

`internal/tui` must not import:

```
internal/execution/tools     concrete capabilities
internal/execution/mcp       concrete capabilities
internal/continuity/task     the durable task controller
```

Task state arrives through `tui.TaskHooks`; the task *vocabulary* is a deliberate
string projection (`pending`/`active`/`done`, `COMPLETED`/`FAILED`/`CANCELLED`)
so the UI never imports the domain type. Completion arrives as
`agent.RunVerdict` and is projected into `taskAction`; the TUI never evaluates
completion itself.

Additional non-mechanical rules:

* The TUI may collect a human decision and pass it to a hook. It may not turn
  that decision into authority.
* Approval memory (`Program.sessionAllowed`) may suppress a *prompt*. It may
  never widen `security.Policy`.
* A projection must be rebuildable from its source. If it cannot be, it is state,
  and state has an owner.

---

## 9. `cmd/ohgo` is a leaf **\[ENFORCED]**

Nothing imports the command. It is the composition root: it chooses tools,
models, budgets, and the security request derivation. Domain logic that lives in
the command is a bug **only** when a package already owns that decision — then
call the owner.

Composition itself (which tools exist, which budgets apply, which model
resolves) legitimately lives here, because there is no other package that can
see all of them without inverting a dependency.

---

## 10. Vocabulary is not a dumping ground **\[ENFORCED]** by §4

The leaf vocabulary packages exist because they have *no* dependencies and a
single concept. They are not a place to park an unrelated helper.

Forbidden package names, unless the brief is genuinely a new domain:
`common`, `shared`, `util`, `utils`, `helpers`, `manager`, `controller`,
`engine`, `pipeline`, `orchestration`, `runtime`, `core`, `foundation`.

If code cannot be assigned to an owner, **stop**. Hiding uncertainty in a
generic package converts a design question into permanent ambiguity.

---

## 11. One source of truth per fact

| Fact | Owner | Everyone else |
|---|---|---|
| Live authority | `authorityGate` (`cmd/ohgo`) | reads it at call time |
| Persisted authority snapshot | `session.Store` via **one** writer | projects it |
| Completion verdict | `agent` evaluators + `ClassifyRun` | projects it |
| Task objective | `contract.IntentContract` in `progress.json` | never re-derives it from model output |
| Budget ledger | `budget.Budget` | reads `Snapshot()` |
| Transcript | `session` | mirrors it, never re-stores it |
| Repository symbols | one `ast.Index` | queries it |
| Model reference format | `ai.Model.Ref()` | never formats its own |

A second writer of a persisted fact is a defect even when both writers agree
today. The second pass fixed two of these; do not introduce a third.

**A mirror is allowed only when it is a projection**: derived, disposable, and
rebuildable. A mirror that can be edited independently is a second source of
truth and must be removed or given a lifecycle owner.

---

## 12. Persistence is a compatibility surface

Before changing anything that is serialized:

```text
source of truth · owner · serialization format · restore semantics ·
projection semantics · compatibility requirement
```

Serialized today: `progress.json` (contract, target snapshot, evidence state,
budget state, status), `task.md` (projection only, never authoritative),
`sessions.db`, `recovery.db`, `settings.json`, `credentials.json`,
`project.yml`, `cache/*.json`, `update*.json`.

Rules:

* **No silent migration.** A shape change is a version bump plus a migration note
  in `OHGO_ARCHITECTURE_MIGRATION.md`. If it cannot be migrated, it does not ship
  as a refactor.
* Moving a type between packages changes its JSON only if the tag changes — but
  moving it into a different *package* changes nothing about the file format, so
  a type move is allowed **only** when the ownership genuinely changes, and it is
  still a migration-review item.
* `progress.json` outranks `task.md`, always.
* A resumed session restores recorded authority/model, not current configuration.

---

## 13. Every capability crosses authorization

The sequence in `internal/agent` is load-bearing:

```
lookup → schema validation → host hook → semantic admission (grounding)
      → authorization (securityGate) → cancellation → budget
      → Tool.Execute → post-hook → evidence
```

and inside the authority gate:

```
capability → authority permits → mutation requires write → scope outside
          → ReadOnly derivation → covering grant → risk → AUTO/APPROVE/DENY
```

Never reorder, never bypass, never let a helper execute on the model's behalf.
A **DENY is never overridden**, by settings, by `--yes`, or by approval memory.

Human-initiated commands (`!`, `/undo`, `/redo`, `/commit`, `/update`) are
intentionally outside the tool authorizer and have their own boundaries
(`escapeAuthority` + sandbox, recovery preconditions, commit revalidation,
checksum + lock). Do not "simplify" them into the tool path without a product
decision.

---

## 14. There are five read-only mechanisms; none is a duplicate

| Layer | Mechanism | Role |
|---|---|---|
| Semantic | grounding / intent | the request did not ask for this mutation |
| Security | `security.Policy` | the authority does not permit it |
| Capability | tool tier + `ReadOnlyToolNames` | the tool set offered to a child |
| OS | `sandbox.Command` | the process cannot write |
| Delegation | tool-set filtering | a child cannot reach a writer at all |

Collapsing them into one boolean removes the strongest effective boundary.
Keep each one; keep them consistent; change them together or not at all.

---

## 15. Tool protocol strings are an API

`tools` and `cognition` communicate through **strings**: tool names and
`Result.Details` keys (`totalLines`, `offset`, `limit`, exit status, verification
fields, `mutation_evidence`). They are not exported, and they are still a
protocol.

Renaming a tool or a detail key silently degrades evidence and completion. Trace
every consumer (`cognition/evidence.go`, `completion.go`, `tui`) before touching
one, and prefer adding a key over renaming one.

---

## 16. Repository intelligence is one index

* One `ast.Index` per session, shared by `symbols`, `graph`, `refs`, and the
  completion resolver (`tools.StructuralSource`).
* Rebuild on staleness; never serve stale coordinates; never swallow a rebuild
  error.
* Repository-relative coordinates; symlink and ignore behaviour preserved.
* **Never build a second index** to satisfy a consumer, and never move index
  ownership into `agent`.

---

## 17. New file, folder, package

**New file** is justified by: semantic cohesion, independent lifecycle, a real
subsystem boundary, test isolation, platform or protocol separation, or
persistence-format separation. *Not* by length.

> A large file first triggers **ownership analysis**, not splitting. `agent/loop.go`
> is 1,460 lines and stays one file because it is one state machine; `run.go` was
> split because it held seven unrelated responsibilities.

**New folder** is justified only by a domain boundary. State the folder's
responsibility, owner, allowed and forbidden dependencies, and expected future
contents. If you cannot, do not create it.

**New package** additionally needs: a stable API, independent ownership, a
meaningful dependency boundary, and a test boundary. A package created to break
an import cycle is a design failure — invert or relocate instead.

---

## 18. Architecture tests are part of the definition of done

1. Add or update the manifest entry (§1).
2. Add a rule only when the boundary is intended to be permanent. A rule that
   will be relaxed next sprint is documentation, not a test.
3. **Prove the rule bites**: inject a violation, watch the test fail with a
   useful message, remove the violation. A rule that cannot fail is not a rule.
4. Never weaken a rule to make a refactor pass. If the refactor is right, the
   rule changes deliberately, with the map and the manifest.

The test package is `internal/architecture`; it parses production Go files and
has no production code.

---

## 19. Review checklist

```
[ ] Does this change introduce a new decision or a new state?
[ ] Who owns it? Is that package already in the manifest?
[ ] Does every new import point downward?
[ ] Does any new code create a second source of truth, index, authority path,
    completion decision, or persisted representation?
[ ] Is a persisted shape changed? If yes: version + migration note.
[ ] Does a capability still cross authorization?
[ ] Is presentation still a projection?
[ ] Does the architecture test still pass — and did any rule change on purpose?
[ ] gofmt · go build ./... · go vet ./... · go test ./... · go test -race ./...
```

---

## 20. Stop conditions

Stop — and write down why — when the next step requires any of:

* uncertain semantic ownership
* a persistence migration
* an authority redesign
* a public-API decision
* a provider/model behaviour change
* an execution-model change
* a new abstraction with no provable owner
* a generic package
* a large-scale rewrite

A clean stopping point is better than a completed wrong migration. This
repository has already reached one: see
`OHGO_ARCHITECTURE_MIGRATION.md` §7.
