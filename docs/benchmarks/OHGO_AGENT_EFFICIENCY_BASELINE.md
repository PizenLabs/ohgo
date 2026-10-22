# OHGO Agent Efficiency Baseline (Phase 1)

> **Historical baseline.** Captured before the `symbols` tool existed; every
> measurement below is lexical-tool behavior. See
> `docs/report/PHASE1_CLOSURE.md` for the post-implementation micro-benchmark.

Audit/measurement phase only. No ohgo source code was modified during
benchmarking.

## 1. Environment

- **ohgo binary version:** `b8b3785-dirty` (from `ohgo version`)
- **Repository HEAD:** `c6bbd02a007b894898b8a5bd8ca7cfce20145402` (`feat(core): mutation previews`) — the installed binary lags HEAD by one commit, so the `mutation previews` feature was *not* exercised at runtime; invariants were checked via the test suite and source inspection instead.
- **Model:** `openrouter/qwen/qwen3.6-27b` (262k context) for every scenario.
- **Provider:** OpenRouter (OpenAI chat-completions protocol).
- **Model settings:** `maxTokens=8192`, `reasoningEffort=medium`, compaction enabled (`keepRecent=8`, `thresholdPercent=80`).
- **OS/runtime:** macOS 27.0.0 (Darwin, arm64), Go 1.27.2, `CGO_ENABLED=0`.
- **Invocation:** `ohgo run "<objective>" --no-tui --plain --yes --deadline 180s -d <repo>` (one-shot, non-interactive).
- **Workspace authority:** default `build` for all runs; effective handler could be narrowed by intent routing (see T5).
- **Test fixtures:** six disposable copies of a small Go module (`example.com/greetapp`: `main.go`, `greet.go`, `config.go`, `version.go`, plus tests) under `/tmp/{t1..t6}-repo`, each initialized as a git repo with a `.ohgo/project.yml`. Fixtures were tailored per scenario (e.g. T3 had the empty-path guard removed; T6 had a one-character punctuation bug injected).
- **Date/time:** Mon Oct 19 2026 (runs ~20:32–20:36 +07).
- **Token measurement:** ohgo's one-shot path (`cmd/ohgo/run.go`) discards `agent.Turn.Assistant.Usage` and does not print or persist provider usage; session storage (`session.StoredMessage`) also omits usage. Therefore provider-billed input/output/total tokens could **not** be measured reliably and are recorded as `N/A`. Tool-call counts, file reads, turns, mutations, and verification calls were recovered from the session SQLite store (`~/.ohgo/sessions.db`), which persists tool names and arguments.

## 2. Per-Test Metrics

| Test | Result | Turns | Tool Calls | Input Tok | Output Tok | Total Tok | Unique Files | Repeated Reads | Mutations | Verification |
|------|--------|-------|------------|-----------|------------|-----------|--------------|----------------|-----------|--------------|
| T1 | Success | 3 | 7 | N/A | N/A | N/A | 5 | 0 | 0 | 0 |
| T2 | Success | 7 | 7 | N/A | N/A | N/A | 2 | 0 | 2 | 1 |
| T3 | Success | 5 | 6 | N/A | N/A | N/A | 2 | 0 | 1 | 1 |
| T4 | Success | 11 | 12 | N/A | N/A | N/A | 5 | 0 | 1 | 3 |
| T5 | Blocked (no mutation) | 4 | 5 | N/A | N/A | N/A | 3 | 0 | 0 | 0 |
| T6 | Success | 5 | 8 | N/A | N/A | N/A | 4 | 0 | 1 | 2 |

Tool-count breakdown:

- T1: `ls`×1, `glob`×1, `read`×5 (main.go, go.mod, greet.go, config.go, version.go).
- T2: `ls`×1, `grep`×1, `read`×2 (version.go, version_test.go), `edit`×2, `bash`×1 (`go test -run TestVersion`).
- T3: `ls`×1, `glob`×1, `read`×2 (config.go, config_test.go), `edit`×1, `bash`×1 (`go test -run TestLoadConfig`).
- T4: `ls`×1, `read`×5 (main.go, greet.go, config.go, greet_test.go, go.mod), `edit`×3 (main.go; greet_test.go added then reverted), `bash`×3 (build/run twice, `go test ./...`).
- T5: `glob`×1, `read`×3 (config.go, config_test.go, main.go), `edit`×1 (denied by ask authority).
- T6: `ls`×1, `bash`×2 (`go test ./...` twice), `read`×4 (config.go, config_test.go, greet.go, greet_test.go), `edit`×1.

## 3. Efficiency Analysis

### T1 — Simple Repository Read
- What ohgo did: `ls` + `glob("*")`, then read five source files in one parallel batch, then a concise summary with no mutation.
- Necessary: reading main.go (and go.mod) to identify language/entry point.
- Redundant: reading config.go and version.go was not required to answer the question; glob over the whole (tiny) tree was harmless but unnecessary for a single entry-point question.
- Context growth: modest (5 small files); no repeated reads.
- Cause: MODEL BEHAVIOR (broad positive discovery) in a tiny repo; not architectural. Acceptable.

### T2 — Targeted Mutation
- What ohgo did: `ls` + repo-wide `grep("1\\.0\\.0")`, read the two matches, edited version.go, then also edited version_test.go so the old-version assertion would pass, then `go test -run TestVersion`.
- Necessary: locating the authoritative declaration, editing it, and focused verification.
- Redundant: the test-file edit (`version_test.go`) was not part of "change only that value"; it was a scope expansion to make verification pass. The grep was repo-wide but cheap.
- Context growth: small.
- Cause: MODEL BEHAVIOR (scope creep into tests) / TASK CONTEXT (test encodes the old version). Not runtime.

### T3 — Read → Reason → Mutation
- What ohgo did: `ls` + `glob("**/*")`, read config.go and config_test.go, added the empty-path guard using the pre-existing `ErrEmptyPath`, ran the focused test package.
- Necessary: inspecting config loading and the adjacent test/style, implementing, focused verification.
- Redundant: `glob("**/*")` duplicated the `ls` listing in a tiny repo; the guard was already half-wired (constant + test existed), so the change was minimal and correct.
- Context growth: small; no repeated reads.
- Cause: MODEL BEHAVIOR (extra discovery) / AGENT LOOP (no de-dup between ls and glob). Low impact.

### T4 — Multi-File Change
- What ohgo did: read main.go, greet.go, config.go, greet_test.go, go.mod; added a `--whisper` flag in main.go; attempted (then reverted) a test edit; built and ran the binary three ways; ran `go test ./...`.
- Necessary: understanding the CLI path and verifying behavior.
- Redundant: reading config.go and go.mod was unnecessary for this feature; a test was added and then reverted (2 extra tool calls); the binary was built twice because the first build used the wrong binary name.
- Context growth: moderate (5 files, multi-turn reasoning).
- Cause: MODEL BEHAVIOR (transient edit/revert, wrong build name) / AGENT LOOP (no retention of module name). No mutation beyond main.go in the final tree.

### T5 — Ambiguous Requirement / Scope Control
- What ohgo did: `glob("**/*")`, read three files, identified the improvement, then attempted an edit that was denied by the ask authority; produced a written proposal instead of applying it.
- Necessary: the analysis and proposed change were correct and minimal.
- Redundant: none in discovery; the block was external.
- Root cause of non-completion: intent routing classified "Improve the error message…" as `IntentRead`, so under a `build` workspace the turn was narrowed to the `ask` (read-only) handler and `filesystem.write` was denied (fail-closed). This is an INTENT ROUTING effect, not a model failure — the model both proposed the correct edit and attempted it.
- Context growth: small.
- Cause: INTENT ROUTING (primary). The model behaved correctly given the constraint.

### T6 — Investigation → Implementation
- What ohgo did: `ls`, ran `go test ./...` (reproduced the failure), read four files, identified a one-character punctuation bug in greet.go, fixed it, re-ran tests (pass).
- Necessary: reproduction, tracing, minimal fix, focused verification.
- Redundant: read config.go and config_test.go although the failure was in greet; could have narrowed to greet.go after the test name. First reproduction ran the whole suite instead of the failing test.
- Context growth: moderate; no repeated reads.
- Cause: MODEL BEHAVIOR (slightly broad investigation) / AGENT LOOP (no targeted test selection). Acceptable for a first reproduction.

## 4. Causal Classification

Observed inefficiencies and their classification:

| Observation | Classification |
|-------------|----------------|
| T5 mutation blocked despite build workspace (intent misclassified as read) | INTENT ROUTING |
| T1 read all 5 source files for an entry-point question | MODEL BEHAVIOR |
| T2 edited version_test.go beyond the requested value | MODEL BEHAVIOR |
| T3 redundant `glob("**/*")` after `ls` | AGENT LOOP |
| T4 transient test edit then revert; double build | AGENT LOOP / MODEL BEHAVIOR |
| T4 read config.go and go.mod unnecessarily | MODEL BEHAVIOR |
| T6 read config files and ran full suite for a greet bug | MODEL BEHAVIOR |
| Provider token usage not surfaced in one-shot runs or session store | RUNTIME |
| No mutation preview shown in `--plain`/`--yes` one-shot path | RUNTIME / TOOL (binary also predates HEAD's preview commit) |

No evidence of: authorization bypass, mutation-without-preview in BUILD mode, task-continuity loss, or fail-open security behavior.

## 5. Baseline Hypotheses

1. **Problem:** Intent routing misclassifies small mutation tasks (e.g. "improve an error message") as read-only, blocking legitimate edits under a `build` workspace.
   **Evidence:** T5 (edit attempted, denied with `capability_not_permitted`).
   **Likely owner:** INTENT ROUTING.
   **Impact:** High (prevents task completion).
   **Confidence:** High.

2. **Problem:** One-shot runs and session storage do not expose provider token usage, so cost/efficiency cannot be measured or attributed.
   **Evidence:** `cmd/ohgo/run.go` discards `agent.Turn.Assistant.Usage`; `session.StoredMessage` omits usage; session `Stats` only estimates from text.
   **Likely owner:** RUNTIME.
   **Impact:** High (blocks benchmarking and optimization feedback).
   **Confidence:** High.

3. **Problem:** The agent performs broad positive discovery (reading unrelated modules, full-suite runs) even for targeted tasks.
   **Evidence:** T4 (config.go, go.mod), T6 (config files, full test suite), T1 (all files).
   **Likely owner:** AGENT LOOP / MODEL BEHAVIOR (prompt could narrow).
   **Impact:** Medium.
   **Confidence:** Medium.

4. **Problem:** The agent expands scope into tests to make verification pass (editing version_test.go), and makes transient edits it then reverts.
   **Evidence:** T2 (version_test.go edit), T4 (added then removed a test).
   **Likely owner:** MODEL BEHAVIOR / PROMPT (scope discipline).
   **Impact:** Medium.
   **Confidence:** Medium.

5. **Problem:** Redundant directory traversal (`ls` followed by `glob("**/*")`) recurs in small repos.
   **Evidence:** T3, T5.
   **Likely owner:** AGENT LOOP / TOOL (tool selection).
   **Impact:** Low–Medium.
   **Confidence:** Medium.

## 6. Hard Invariants

Verified during benchmarking (no source modified):

- **Authorization:** Held. T5's edit under an `ask`-narrowed turn was denied with `authorization denied (capability_not_permitted): filesystem.write is not permitted by the ask authority`. Fail-closed confirmed.
- **Mutation preview:** Not observed at runtime in the one-shot `--plain --yes` path (approvals were auto-approved by `--yes`, and the installed binary predates HEAD's `feat(core): mutation previews`). Invariant not violated: no mutation occurred without an explicit tool call recorded in the session store; the full ohgo test suite (including security/mutation tests) passes.
- **Mutation evidence:** Every attempted mutation is recorded in `sessions.db` with tool name, arguments, and result text (verified by querying sessions 134–139).
- **Task continuity:** Sessions were created and persisted per run (`sessions` + `messages` tables); no cross-run contamination observed (each repo got a distinct session).
- **BUILD/ASK/FUZZ semantics:** Held. T1 and T5 were narrowed to `ask` (read-only) under a `build` workspace; T2/T3/T4/T6 ran under `build` and mutated; the indicator `BUILD · ASK / READ` was emitted truthfully. No implicit escalation observed.
- **Terminal state semantics:** All six runs terminated with a final assistant summary (no hang; `--deadline 180s` never triggered).
- **Fail-closed security behavior:** Held (T5 denial above). `go test ./...` passes for `security`, `workspace`, `agent`, `tools`, and `cmd/ohgo`.

No ohgo implementation files were changed. Only disposable test repositories under `/tmp` and the report file were created. User settings were restored to their pre-benchmark state.
EOF
echo "written"