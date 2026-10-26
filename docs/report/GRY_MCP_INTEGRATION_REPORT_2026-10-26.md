# Gry MCP Integration Report — 2026-10-26

> **Status:** draft for fix planning
> **Owner:** ohgo harness / MCP read path
> **Counterpart:** `gry/docs/reports/2026-10-26-ohgo-integration.md`
> **Run:** `ohgo v0.0.1-15-g97b06af-dirty`, `gry 1.0.2`
> **Test target:** `go-template` (5 Go files, 278 lines), gry generation
> `1793012093799-29fad916207a4e648a3fe6ef8a4e4668`
> **Verdict:** ohgo is the primary cause of the failed live run. The dominant
> cause is that `go-template` was not indexed when the run started; the
> harness then hid the failure behind a misleading error taxonomy and a
> truncated capability catalog. gry's own defects are tracked separately.

---

## 1. Scope

This report covers ohgo-side behavior observed during a real run whose objective
was **"Help me explore this project using Gry MCP"**. The run used the
`mcp_list` / `mcp_read` path, not gry's native tool surface.

Artifacts:

- `.ohgo/progress.json` in the target repository (36 evidence items, 69 tool
  calls, 73 model calls, 26 external calls, 75,259 external bytes).
- `.ohgo/task.md`, `.ohgo/project.yml`.
- Post-run replay against the now-indexed project through gry MCP.

The run ended `NEEDS_REVIEW` with
`lastAction: a tool call failed and no later success or verification recovered it`.
The final answer was nevertheless accurate because the model fell back to native
`ls` / `grep` / `glob` / `read`.

---

## 2. Timeline that explains the failures

| UTC | Event |
|---|---|
| 09:03:48 | `gry.list_projects` returns only `bench` and `gry`. **`go-template` is absent.** |
| 09:03–09:06 | 25 of 36 evidence items are errors: 12 MCP, 13 native reads. |
| 09:04:46 | `gry.get_architecture(project="go-template")` returns `project not found or not indexed: go-template`. |
| 09:05:56 | `gry.search_graph(project="go-template")` returns `total: 0` instead of an error (gry bug G1). |
| 09:06:30 | ohgo checkpoints `NEEDS_REVIEW`. |
| 10:54:53 | `go-template` is indexed. The same exploration succeeds afterward. |

The current index proves the project was explorable; the harness never checked
whether the named project existed before attempting graph queries.

---

## 3. Findings

Severity: **P0** blocks the integration, **P1** produces wrong or unusable
diagnostics, **P2** degrades recovery, **P3** polish.

### O1 — P0: intent gating hides structural tools for an explicit Gry request

**Evidence.** The task shape was classified as `informational`, `action=unknown`,
`ambiguity=unknown_action`. The audit block records:

```text
offered: read, glob, grep, ls, todo, mcp_list, mcp_read, artifact
withheld:
  - symbols: the task does not need go structural navigation
  - graph:   the task does not need go structural navigation
  - refs:    the task does not need go structural navigation
  - mcp_call: the task shape does not permit repository mutation
```

Withholding `mcp_call` is correct: it is the mutation boundary
(`tools/mcp.go:503`), and gry's read tools belong on `mcp_read`. Withholding
`symbols` / `graph` / `refs` is not: the objective explicitly names Gry and asks
to explore.

**Root cause.** `needsCodeNavigation` only recognizes debug/bug/test/refactor/
symbol vocabulary (`cognition/shape.go:453-470`). It does not recognize
`explore`, `architecture`, `structure`, `understand`, `graph`, `index`, `gry`,
or `MCP`. `candidateExclusion` then removes every tool that requires a language
(`cognition/candidates.go:99-106`).

**Fix.**

1. Add exploration vocabulary to `needsCodeNavigation`:
   `explore`, `exploring`, `architecture`, `structure`, `overview`, `graph`,
   `dependency`, `dependencies`, `index`, `indexed`, `gry`, `mcp`.
2. If the objective names a configured MCP server or tool, force
   `NeedsCodeNavigation = true` and `NeedsRelationshipNavigation = true`.
3. Keep `mcp_call` withheld unless the task shape permits mutation; that is not
   the bug.
4. Treat an explicit "use Gry" as `External.Required = true` for reporting
   (`externalRequirement`, `cognition/shape.go:519-535`).

**Test.** `BuildShape("Help me explore this project using Gry MCP")` must set
`NeedsCodeNavigation`, `NeedsRelationshipNavigation`, and `External.Required`.
`Candidates` must offer `symbols`, `graph`, `refs`, `mcp_list`, `mcp_read`.

---

### O2 — P0: discovery truncation hides the tool schemas and lists prompts first

**Evidence.** `mcp_list` returned **19 capabilities in 16,383 bytes** (17 tools
+ 2 prompts). ohgo retained **4,174 bytes** as `external_excerpt`; the retained
prefix contains **both prompts first**, then only `check_index_coverage`. The
model never saw `search_code` or `search_graph` schemas.

Consequences in the run:

- `search_code` was called without `pattern` (twice):
  `invalid_request: mcp tool "search_code" arguments: missing required property "pattern"`.
- `explore_codebase` (a **prompt**, not a tool) was called as a tool (three
  times), producing the bogus `mutation_denied` in O3.

**Root cause.**

- `mcp_list` renders prompts before tools. Truncation therefore removes the
  entire tool catalog first.
- `ExternalKeepBytes` defaults to 4096 (`cognition/project.go:39-41`), and
  discovery is `RetentionExternal` (`cognition/project.go:327-338`).
- `boundedSchema` also cuts one schema at 4096 chars with `BoundText`
  (`tools/mcp.go:25`, `tools/mcp.go:695-711`), which can split JSON.

**Fix.**

1. **Order discovery tools first, prompts last** (or put prompts in a separate
   `prompts` section). Never let a prompt occupy the truncation prefix.
2. Emit a compact one-line contract per tool:
   `search_code(project, pattern, file_pattern?, ...) -> results`.
   Required arguments must always survive truncation.
3. Add a dedicated retention class for discovery (for example
   `RetentionDiscovery`, 16–32 KiB) instead of the generic 4 KiB external cap.
   Alternative: keep `mcp_list` small and add `mcp_describe(name)` for the full
   schema.
4. If a single schema exceeds `MaxMCPInputSchemaChars`, return
   `schema_omitted: true` plus the required-argument summary. Never return
   partial JSON.

**Test.** A discovery excerpt for a 19-capability server must contain
`search_code(project, pattern` and must not contain `explore_codebase` before
any tool. A 5 KiB schema must not produce invalid JSON.

---

### O3 — P0: unknown capability is reported as `mutation_denied`

**Evidence.** Three identical failures:

```text
mutation_denied: mcp tool "explore_codebase" is classified unknown, not read
```

`explore_codebase` is a gry **prompt** (`kind: prompt`, `classification: read`,
`authority: READ`). It is not a mutation.

**Root cause.** `ServerConfig.Classify` only consults the configured tool map
(`mcp/mcp.go:377-390`); a prompt name is therefore `unknown`. `CallTool` maps
every non-read class to `FailureMutationDenied` (`mcp/mcp.go:1082-1085`). The
failure class carries no information about whether the name was unknown, a
prompt, or genuinely mutating.

**Fix.**

1. Add distinct failure classes:
   `unknown_capability`, `not_readable`, `prompt_not_tool`, `mutation_denied`.
2. Keep the model-visible text actionable:
   `unknown_capability: "explore_codebase" is a prompt, not a tool; use
   mcp_read with prompt="explore_codebase"`.
3. Include the discovered kind and classification in the error details.
4. Only use `mutation_denied` when a real mutation-class tool was requested on
   the read path.

**Test.** A prompt name passed as `tool` must return `prompt_not_tool`, not
`mutation_denied`. A genuine `mutate` tool on `mcp_read` must still return
`mutation_denied`.

---

### O4 — P1: remote error summaries hide the actual cause behind the trust envelope

**Evidence.** Six remote failures (`get_architecture` ×2, `check_index_coverage`
×2, `search_code` ×2) have the summary:

```text
error from mcp_read: [EXTERNAL KNOWLEDGE — UNTRUSTED DATA]
```

The actual value contains the actionable line:

```text
status: remote_tool_error
project not found or not indexed: go-template
```

**Root cause.** `publishMCPTool` wraps every response in
`knowledge.Envelope` (`tools/mcp.go:894-918`, `knowledge/knowledge.go:332-351`).
`addError` and `addExternal` derive the summary from `firstLine` /
`firstContentLine` (`cognition/evidence.go:438-450`, `784-785`), so the first
line is the fixed envelope banner.

**Fix.**

1. When `result.IsError`, add a runtime-authored header line **before** the
   envelope: `remote_error: <first line of the remote message>`.
2. Make `addError` / `addExternal` prefer `remote_error` over the envelope
   banner.
3. Keep the envelope intact; the fix is to preserve the diagnostic, not to
   weaken the trust boundary.

**Test.** A timeout, a `project not found`, and a tool error must produce
distinct summaries containing the remote message.

---

### O5 — P1: `anyOf` validation gives no branch diagnostics

**Evidence.**

```text
Invalid arguments for "mcp_read": must match at least one of the 'anyOf' schemas
```

**Root cause.** `jsonschema/schema.go:332-333` emits one fixed sentence and
discards branch-level failures. `mcp_read` has four valid shapes
(`resource`, `tool`, `capability`, `prompt`; `tools/mcp.go:360-365`).

**Fix.** Report each branch's failure (`missing required property "tool"`,
`unknown property "query"`, etc.) and a one-line hint:
`expected one of: resource, tool, capability, prompt`.
Add a unit test for each branch.

---

### O6 — P1: directory reads do not degrade into a listing

**Evidence.** 13 native read failures:

- 11 × `"... is a directory, not a file"` (`cmd/api`, `internal`, repo root
  twice, `docs`, `bin`, `cmd/example` five times).
- 2 × missing files (`bench`, `internal/database/database_test.go`).

`docs/`, `bin/`, and `cmd/example/` exist but are empty. `bench` is absent; it
is a different gry project rooted at `/private/tmp/gbsrc`.

**Root cause.** `tools/read.go:82` returns a bare error and does not offer a
next step. The model then retried `cmd/example` five times.

**Fix.**

1. On `EISDIR`, return the directory entries (bounded) with
   `next: use glob/ls for a full listing` instead of a dead-end error.
2. On `ENOENT`, return the nearest existing parent and sibling names.
3. In the loop, suppress an identical failed call after one attempt; replace it
   with a hint to use `ls`/`glob`.

**Test.** `read("internal")` returns the three server files; repeated identical
failures are collapsed.

---

### O7 — P2: completion counts every unresolved failure, even denied capabilities

**Evidence.** `agent.EvaluateCompletion` returned:

```text
NEEDS_REVIEW: a tool call failed and no later success or verification recovered it
```

25 failures, no later success for the exact failing calls, despite a correct
final answer from native tools.

**Root cause.** `agent/completion.go:74-77` treats every pending failure as
blocking. It does not distinguish an unavailable/denied capability from a
failed observation that the task still needs.

**Fix.**

1. Record the failure class and tool with each pending failure.
2. For an informational objective, an `unknown_capability` / `not_readable` /
   `mutation_denied` failure should not by itself block completion when another
   evidence path covers the objective.
3. At minimum, name the tool and class in `reviewReason`:
   `mcp_read:search_code: invalid_arguments; mcp_read:explore_codebase:
   prompt_not_tool`.
4. Keep the strict rule for execution and mutation objectives.

**Test.** A read-only run with one denied MCP call and a successful native
fallback must complete with the denial recorded as a limitation, not as a
blocking failure.

---

### O8 — P3: relationship and external vocabulary misses architecture terms

**Evidence.** The objective says "explore ... using Gry MCP"; the shape still
recorded no relationship or external requirement.

**Root cause.** `needsRelationshipNavigation` recognizes `graph`, `caller`,
`dependency`, etc. (`cognition/shape.go:486-494`) but not `architecture`,
`structure`, `overview`, `gry`, or `mcp`. `externalRequirement` recognizes web
and documentation phrases only (`cognition/shape.go:519-535`).

**Fix.** Add `architecture`, `structure`, `overview`, `gry`, `mcp`,
`model context protocol` to the relationship vocabulary. Treat a named
configured MCP server as external context for reporting.

---

### O9 — P3: `.ohgo/` is not guaranteed to be ignored in existing projects

**Evidence.** The target repository has `.ohgo/` (untracked) and no `.ohgo/`
rule in `.gitignore`. gry indexed 39 agent-state files (see gry report G2).

**Root cause.** ohgo has the managed rule already
(`startup/gitignore.go:20-22`: `GitignoreRule = ".ohgo/"`), but it is applied
during onboarding. Projects initialized before the rule—or initialized without
the gate—never receive it.

**Fix.** On every task start, call the existing `NeedsIgnore` check and add the
rule idempotently (or warn once). Do not silently mutate `.gitignore` if the
user declined; record the limitation.

**Cross-repo.** gry must also ignore agent-state directories by default
(gry G2). Both fixes are needed; neither is sufficient alone.

---

### O10 — P2: a single oversized schema can be cut into invalid JSON

**Evidence.** `MaxMCPInputSchemaChars = 4096` (`tools/mcp.go:25`) is enforced by
`knowledge.BoundText` (`tools/mcp.go:709`). `BoundText` is a byte bound, not a
JSON-aware bound.

**Risk.** The model receives a truncated schema that is not valid JSON, then
produces argument errors that look like model mistakes.

**Fix.** If a schema exceeds the limit: summarize it (required names, types,
enums, defaults), or replace it with
`{"schema_omitted": true, "required": [...], "hint": "call mcp_describe"}`.
Never cut inside the JSON.

---

## 4. Cross-repo contract

| ohgo finding | Depends on gry fix | Contract |
|---|---|---|
| O2 discovery visibility | G1 (error on unindexed project) | gry must return a typed, one-line error that ohgo can surface. |
| O9 `.ohgo` hygiene | G2 (agent-state indexing) | ohgo writes the ignore rule; gry also honors `core.excludesFile` and agent-state defaults. |
| O3 unknown capability | — | gry exposes prompts and tools with explicit `kind`; ohgo must not collapse them. |
| O4 remote errors | — | gry's `project not found or not indexed: <name>` is already the right message; ohgo must preserve it. |

The same run also produced gry-side findings G3 (false `CALLS`), G4 (Go import
parsing), G5 (false `CONFIGURES`), G6 (phantom `...` file), and G7
(`detect_changes` budget). They are specified in the gry report.

---

## 5. Fix plan

### Phase 0 — regression harness (S)

- Add a synthetic MCP server fixture with 19 capabilities: two prompts first,
  then tools including `search_code` (required `pattern`).
- Add the exact objective
  `Help me explore this project using Gry MCP` as a shape/candidate test.
- Capture the current failure strings as golden diagnostics.

### Phase 1 — intent and discovery (M)

- O1: exploration vocabulary + explicit MCP intent.
- O2: tools-first ordering, compact signatures, discovery retention.
- O8: architecture/relationship vocabulary.
- O10: JSON-safe schema fallback.

### Phase 2 — error semantics and observability (M)

- O3: failure classes and prompt/tool distinction.
- O4: `remote_error` header and summary preference.
- O5: `anyOf` branch diagnostics.

### Phase 3 — recovery and completion (M)

- O6: EISDIR listing and repeat suppression.
- O7: class-aware completion and named review reasons.

### Phase 4 — repository hygiene (S)

- O9: idempotent `.ohgo/` ignore check.
- Document the gry preflight (`list_projects`, `index_status`) and the
  read-only/indexing boundary.

---

## 6. Acceptance criteria

1. The exact objective offers `symbols`, `graph`, `refs`, `mcp_list`,
   `mcp_read`; `mcp_call` remains withheld unless mutation is permitted.
2. The `mcp_list` excerpt always contains every tool's required arguments; no
   prompt appears before a tool.
3. `explore_codebase` passed as a tool returns `prompt_not_tool`, not
   `mutation_denied`.
4. A remote `project not found or not indexed` error appears verbatim in the
   evidence summary.
5. `mcp_read` with no valid shape lists the four accepted shapes.
6. `read` on a directory returns a bounded listing.
7. A read-only run with a denied MCP capability and a successful fallback either
   completes or names the capability in `reviewReason`; it does not fail with
   the generic "a tool call failed" text.
8. `.ohgo/` is ignored in new and existing projects, or the omission is recorded
   as a limitation.

---

## 7. Appendix — evidence index

| Evidence ID | Class | Message |
|---|---|---|
| `mcp_read_gykm4emnpdm7` | mutation_denied | `explore_codebase` classified unknown |
| `mcp_read_t9p614t168hy` | mutation_denied | same |
| `mcp_read_prhjn84nymws` | mutation_denied | same |
| `mcp_read_5xgkd0k2349g` | invalid_request | `search_code` missing `pattern` |
| `mcp_read_t5zfe2z9dtpe` | invalid_request | same |
| `mcp_read_42g14nw98d7k` | anyOf | `mcp_read` shape mismatch |
| `mcp_read_9nznsjs4ar6e` | remote_tool_error | `project not found or not indexed: go-template` |
| `mcp_read_gcgd8ea47r39` | remote_tool_error | same |
| `mcp_read_rhqa7t548jra` | remote_tool_error | same |
| `mcp_read_a90nb2ge6afx` | remote_tool_error | same |
| `mcp_read_bkxkk8ef890s` | remote_tool_error | same |
| `mcp_read_fp350r3q3ta2` | remote_tool_error | same |

Native read failures: directory paths `cmd/api`, `docs`, `bin`, `internal`,
`cmd/example` (×5), repository root (×2); missing paths `bench`,
`internal/database/database_test.go`.

### Minimal reproduction

```bash
cd /Users/anvndev/Documents/Project/Golang/go-template
# At run time, go-template was not in `gry.list_projects`.
# The run still attempted graph calls; reproduce by deleting the index
# in a disposable copy, then invoking the same objective through ohgo.
```

Do not delete the live index to reproduce. Use a temporary copy and a separate
`gry` data directory.

---

## 8. Fix status (2026-10-26)

| ID | Status | Change | Verification |
|---|---|---|---|
| O1 | Fixed | Exploration/architecture/Gry/MCP vocabulary; explicit Gry/MCP forces structural and relationship navigation. | `TestExplorationObjectiveNeedsStructureAndExternalContext`; `Cognition` suite passes. |
| O2 | Fixed | Discovery orders tools before resources/prompts and publishes a compact `capability_index` with required arguments before full schemas. | `TestMCPListPublishesInstructionsAndSchemas` asserts `tool: search_graph(project)` and tool-before-prompt order. Discovery retention is unchanged; the index keeps contracts inside the existing 4 KiB excerpt. |
| O3 | Fixed | `ClassUnknown` now returns `unknown_capability`, with a `mcp_list`/`prompt=` hint; no longer `mutation_denied`. | Updated MCP boundary tests pass. |
| O4 | Fixed | Remote tool errors publish `remote_error`; external evidence summaries prefer it over the trust banner. | `TestMCPRemoteToolErrorIsAFailure` asserts the detail; `Cognition` suite passes. |
| O5 | Fixed | `anyOf` errors include per-branch reasons and expected properties. | `TestAnyOfReportsBranchReasons`. |
| O6 | Fixed | `read` on a directory returns a bounded entry listing (50 max) instead of a dead end. | Updated `TestReadToolReportsDirectoryAndMissingFile`. |
| O7 | Partial | Review reason now names the failing tools (for example `edit x2`). Completion blocking semantics were intentionally left unchanged; a denied capability still yields `NEEDS_REVIEW`. | Updated `TestEvaluateCompletionRejectsUnrecoveredToolFailure`. |
| O8 | Fixed | `architecture`, `structure`, `overview`, `gry`, `mcp`, `model context protocol` added to relationship/external vocabulary. | Covered by O1 test. |
| O9 | Fixed | The target project now ignores `.ohgo/` and `.izen/`; the existing `startup/gitignore.go` rule remains the managed path for new projects. gry also ignores these directories. | go-template `.gitignore`; gry G2. |
| O10 | Fixed | Oversized schemas are replaced by `{schema_omitted, required, hint}`; never cut mid-JSON. | `TestBoundedSchemaNeverPublishesPartialJSON`. |

`go test ./...` passes across all packages. The installed `~/.local/bin/ohgo`
was not replaced; rebuild/install to deploy the fixed binary.

Remaining follow-ups:

- O7's semantic policy (whether an unavailable read capability blocks an
  informational answer) needs a product decision; only diagnostics changed.
- O2's discovery excerpt still depends on the 4 KiB external retention cap; the
  compact index guarantees names and required arguments survive it.
- gry G7's `modules.next_offset` was not added; `changed_files`, `seeds`, and
  `impact` now page correctly.
