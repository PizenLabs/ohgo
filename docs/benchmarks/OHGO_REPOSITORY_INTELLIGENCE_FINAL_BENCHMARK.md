# OHGO Repository Intelligence — Final Benchmark Report

> **Historical baseline (pre-Phase 1).** This audit was run at commit
> `c6bbd02` before the `symbols` tool existed. Its NO-GO conclusion is
> superseded by `docs/report/PHASE1_CLOSURE.md`, which records the implemented
> capability and its measured delta. This file is retained as baseline evidence.

**File:** `OHGO_REPOSITORY_INTELLIGENCE_FINAL_BENCHMARK.md`
**Date generated:** 2026-10-19
**Status:** Measurement phase complete (static + structural audit; see §2.4 for execution limits)

---

## 1. Executive Summary

**Answer to the benchmark question:**

> Does OHGO's current repository intelligence demonstrate a measurable advantage over traditional LLM repository exploration?

**No.** On the current codebase (commit `c6bbd02`), OHGO does **not** possess a runtime-owned repository map or repository-navigation capability beyond the conventional `ls` / `glob` / `grep` / `read` toolset. The only code that could be called "repository intelligence" — the `ast` package (`go/ast`-based, stateless, Go-only declaration finder) — is **not wired into any tool, agent, prompt, or TUI surface**, and the TUI's optional "SYMBOL ENGINE" status reporter is **never installed**. Consequently, Track A (traditional exploration) and Track B (OHGO "guided" exploration) are **functionally identical**: the model must reconstruct repository structure itself in both cases.

Because no differentiating capability exists in the current system, no measurable navigation advantage can be demonstrated by the current implementation. The benchmark therefore returns **NO-GO** for the *current* repository intelligence, with a recommendation to **BUILD** the capability (see §16). Security/authority invariants are untouched and remain intact (§9).

---

## 2. Experimental Setup

### 2.1 Model (required configuration)

| Field | Value |
| --- | --- |
| Requested model | MiniMax M3.1 Flash (free) |
| Actual provider/model identifier | **Not available in this environment.** No MiniMax "M3.1 Flash" entry exists in the OHGO catalog (`ohgo models`). Closest free MiniMax entries: `opencode-zen/minimax-m3-free` (200k ctx), `opencode-zen/minimax-m2.5-free`. |
| Credentials | No `OPENCODE_API_KEY` (required by `opencode-zen`/`opencode-go`) present in environment or shell discovery; `~/.ohgo/credentials.json` is empty. |
| Consequence | **No compliant live run could be executed.** See §2.4. |

Recorded environment facts that *were* obtainable:

| Field | Value |
| --- | --- |
| OHGO version (binary) | `c6bbd02` (rebuilt via `make build`) |
| Git SHA | `c6bbd02a007b894898b8a5bd8ca7cfce20145402` |
| OS/runtime | darwin (macOS), Go 1.27 |
| Invocation mode available | `ohgo run --plain --no-tui -y --deadline … -m <model> "<prompt>"` (one-shot) and interactive REPL |
| Context limit (model-dependent) | N/A (no compliant model run) |
| Max output tokens | configured default 8192 (`settings.json` `maxTokens`) |
| Reasoning/effort | `medium` (settings.json `reasoningEffort`) |
| Compaction | enabled, keepRecent 8, thresholdPercent 80 |
| Model settings | as in `~/.ohgo/settings.json` (see §2.3) |

### 2.2 Repositories

| Field | Value |
| --- | --- |
| Repository under test | `github.com/PizenLabs/ohgo` itself (the only locally available, deterministic repo of sufficient size) |
| Commit SHA | `c6bbd02a007b894898b8a5bd8ca7cfce20145402` |
| Language | Go (plus YAML, Bash, Python) |
| Approx. LOC | ~97,442 (all files incl. tests); 129 non-test Go files |
| File count | 290 Go files across 26 packages |
| Package/module count | 26 Go packages (top-level: agent, ai, ast, catalog, cmd/ohgo, config, creds, diff, edit, exec, jsonschema, memory, project, prompt, security, session, shell, startup, state, task, tokens, tools, tui, vcs, workspace) |
| Relevant architecture | Conventional coding agent: agent loop + tool set (read/write/edit/delete/bash/wait/glob/grep/ls/web_fetch/todo/task) + authority workspaces (ASK/FUZZ/BUILD) + session store + compaction |
| Known ground truth | Established per-scenario in §3; **not** exposed to any model (no run executed) |

No external repository was available that met the size/variety requirements and was deterministic. The ohgo repo itself is medium-to-large (~97k LOC) and contains the required cross-file calls, interfaces, tests, error paths, and misleading lexical matches (e.g. `ast` vs. `ast`-prefixed identifiers, multiple `search`/`grep` concepts).

### 2.3 Configuration snapshot

`~/.ohgo/settings.json`:

```json
{
  "identity": {},
  "model": "openrouter/cohere/north-mini-code:free",
  "roles": {
    "ask": { "model": "openrouter/dots-studio/dots-3-note-preview:free" },
    "build": { "model": "openrouter/cohere/north-mini-code:free" },
    "sup": { "model": "openrouter/dots-studio/dots-3-note-preview:free" }
  },
  "maxTokens": 8192,
  "reasoningEffort": "medium",
  "theme": "dark",
  "compaction": { "enabled": true, "keepRecent": 8, "thresholdPercent": 80 }
}
```

### 2.4 Execution limits (important)

- **No compliant live benchmark runs were executed.** The required model `MiniMax M3.1 Flash (free)` is not available, and no credentials for the only free MiniMax variants (`opencode-zen/minimax-*-free`) are configured. Fabricating runs or substituting a different model would violate the benchmark's single-variable rule (§1, §4) and the "do not present estimates as authoritative" rule (§8.10).
- Therefore all per-scenario **task-success, navigation, context, and token metrics are recorded as `N/A (not executed)`**. The findings below are derived from a **static/structural audit** of the current codebase, which is sufficient to answer the architectural question (§17) because the absence of the capability is a code-level fact, not a measurement.
- No source code was modified during this benchmark (§13). Only the binary was rebuilt (`make build`) to align the version string; no `.go` file changed.

### 2.5 Tracks

| Track | Description | Status |
| --- | --- | --- |
| A — Traditional Exploration | `ls` / `glob` / `grep` / `read` + shell/test commands | Baseline capability set of OHGO |
| B — OHGO Guided Exploration | OHGO's "repository-aware" capabilities | **Identical to Track A in current code** (no additional capability exists); see §3 |

### 2.6 Critical constraint compliance

External MCP repository intelligence (`codebase-memory-mcp`, etc.) was **not used** to inform the model-facing capability of OHGO during this benchmark. (Note: this agent's own exploration tools are separate from the benchmarked system; no MCP result was injected into any OHGO run, and no run was executed.)

---

## 3. Repository Intelligence Inventory (what OHGO actually has)

This section is the core finding and replaces the "ground truth discovery" that live runs would have produced.

### 3.1 Tool set exposed to the model (`cmd/ohgo/run.go:buildTools`)

| Tool | Type | Repo-aware? |
| --- | --- | --- |
| `read` | file read (root-contained) | No |
| `write` | file write | No |
| `edit` | file edit | No |
| `delete` | file delete | No |
| `bash` | shell | No |
| `wait` | job wait | No |
| `glob` | path pattern | Lexical only |
| `grep` | regex content search | Lexical only |
| `ls` | directory list | No |
| `web_fetch` | HTTP | No |
| `todo` | task list | No |
| `task` | subagent (when depth > 1) | Delegation only |

No symbol, reference, caller, implementation, or structural navigation tool exists.

### 3.2 The `ast` package (`ast/ast.go`)

- A stateless per-file declaration finder using Go's standard `go/ast` parser.
- Supports kinds: `func`, `type`, `var`, `const`, `import`.
- **Usage in production code: none.** `grep` for `PizenLabs/ohgo/ast` imports returns zero hits. The package has tests but no consumer.
- Limitations: Go-only; no cross-file resolution; no callers/references; no index; no persistence.

### 3.3 TUI "SYMBOL ENGINE"

- `tui.StatusEngine` + `SetSymbolStatusFunc` exist for an optional `/status` card section.
- `SetSymbolStatusFunc` is **never called** anywhere in the codebase (grep: zero call sites outside tests).
- Therefore the symbol engine is permanently hidden and reports nothing.

### 3.4 Memory / project intelligence

- `memory` package defines a provenance trust seam (`Promote`) but owns **no storage** (by design, §I66).
- `project` package provides stable project identity only.
- No project-memory database, no code graph, no index (`graph.db` is a reserved/deferred location per `docs/architecture/STATE_AND_PERSISTENCE.md`).

### 3.5 Prompt layer

- `prompt.Identity` instructs the model to "Prefer the dedicated tools … glob for finding files, and grep for finding text." There is no structural/navigation guidance and no repository map injection.
- `prompt.ProjectContext` injects `AGENTS.md` (human-authored) only.

### 3.6 Conclusion of inventory

OHGO's "repository intelligence" in the current build is **conventional lexical tooling only**. Track B cannot differ from Track A. Any measured difference between tracks would be noise, not capability.

---

## 4. Ground Truth (evaluator-only, not model context)

Ground truth was prepared for the scenario suite but, because no run was executed, it is recorded here as the *intended* evaluation targets for a future compliant run. Coordinates are repository-relative.

| Scenario | Intended authoritative files/symbols (illustrative) |
| --- | --- |
| T7 — Entry/Implementation Discovery | `cmd/ohgo/run.go:buildTools`, `tools/grep.go:GrepTool.Execute`, `tools/glob.go` |
| T8 — Bug Localization | Cross-file: `agent/loop.go` turn handling → `tools/*` execution → `security/*` gate |
| T9 — Precise Mutation | Single tool file (e.g. `tools/grep.go`) + its test |
| T10 — Cross-File Dependency | `resolvePath` usage across `tools/*.go`; `agent.Tool` interface in `agent/tool.go` |
| T11 — Misleading Keyword | "timeout" appears in `tools/grep.go` (`GrepTimeout`), `agent/deadline.go`, `tools/wait.go`; authoritative path is `agent/deadline.go` + `loop.go` |
| T12 — Large Repo Navigation | Whole `tools/` + `agent/` packages |
| T13 — Investigation → Patch | End-to-end across `agent/loop.go`, `tools/edit.go`, `diff/*` |
| T14 — Ambiguous Requirement | Intent classifier `workspace/intent.go:Classify` (ambiguous → READ default) |
| T15 — Structural Navigation | Interface `agent.Tool`, implementations in `tools/*`, callers in `cmd/ohgo/run.go` |
| T16 — False Lead / Misleading Impl | Multiple `search`/`grep`/`ast` symbols; obsolete `ast` package vs. live `grep` tool |

These are evaluator references; they were never placed in model context.

---

## 5. Raw Results

All live-run metrics are `N/A (not executed)` because no compliant model run was possible (§2.4). Structural facts are recorded where applicable.

| Scenario | Track | Status | Task success | Relevant files | Irrelevant files | Tool calls | Turns | Reads | Searches | Mutations | Verification | Wall time | Tokens |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| T7 | A | Not executed | N/A | N/A | N/A | N/A | N/A | N/A | N/A | N/A | N/A | N/A | N/A |
| T7 | B | Not executed | N/A | N/A | N/A | N/A | N/A | N/A | N/A | N/A | N/A | N/A | N/A |
| T8 | A | Not executed | N/A | N/A | N/A | N/A | N/A | N/A | N/A | N/A | N/A | N/A | N/A |
| T8 | B | Not executed | N/A | N/A | N/A | N/A | N/A | N/A | N/A | N/A | N/A | N/A | N/A |
| T9 | A | Not executed | N/A | N/A | N/A | N/A | N/A | N/A | N/A | N/A | N/A | N/A | N/A |
| T9 | B | Not executed | N/A | N/A | N/A | N/A | N/A | N/A | N/A | N/A | N/A | N/A | N/A |
| T10 | A | Not executed | N/A | N/A | N/A | N/A | N/A | N/A | N/A | N/A | N/A | N/A | N/A |
| T10 | B | Not executed | N/A | N/A | N/A | N/A | N/A | N/A | N/A | N/A | N/A | N/A | N/A |
| T11 | A | Not executed | N/A | N/A | N/A | N/A | N/A | N/A | N/A | N/A | N/A | N/A | N/A |
| T11 | B | Not executed | N/A | N/A | N/A | N/A | N/A | N/A | N/A | N/A | N/A | N/A | N/A |
| T12 | A | Not executed | N/A | N/A | N/A | N/A | N/A | N/A | N/A | N/A | N/A | N/A | N/A |
| T12 | B | Not executed | N/A | N/A | N/A | N/A | N/A | N/A | N/A | N/A | N/A | N/A | N/A |
| T13 | A | Not executed | N/A | N/A | N/A | N/A | N/A | N/A | N/A | N/A | N/A | N/A | N/A |
| T13 | B | Not executed | N/A | N/A | N/A | N/A | N/A | N/A | N/A | N/A | N/A | N/A | N/A |
| T14 | A | Not executed | N/A | N/A | N/A | N/A | N/A | N/A | N/A | N/A | N/A | N/A | N/A |
| T14 | B | Not executed | N/A | N/A | N/A | N/A | N/A | N/A | N/A | N/A | N/A | N/A | N/A |
| T15 | A | Not executed | N/A | N/A | N/A | N/A | N/A | N/A | N/A | N/A | N/A | N/A | N/A |
| T15 | B | Not executed | N/A | N/A | N/A | N/A | N/A | N/A | N/A | N/A | N/A | N/A | N/A |
| T16 | A | Not executed | N/A | N/A | N/A | N/A | N/A | N/A | N/A | N/A | N/A | N/A | N/A |
| T16 | B | Not executed | N/A | N/A | N/A | N/A | N/A | N/A | N/A | N/A | N/A | N/A | N/A |

Navigation precision/recall/F1/context efficiency are likewise `N/A`.

---

## 6. Traditional vs OHGO Comparison

| Metric | Traditional (A) | OHGO (B) | Delta |
| --- | --- | --- | --- |
| Task success | N/A | N/A | N/A |
| Correct diagnosis | N/A | N/A | N/A |
| Relevant files | N/A | N/A | N/A |
| Irrelevant files | N/A | N/A | N/A |
| Navigation precision | N/A | N/A | N/A |
| Navigation recall | N/A | N/A | N/A |
| Navigation F1 | N/A | N/A | N/A |
| Tool calls | N/A | N/A | N/A |
| Turns | N/A | N/A | N/A |
| Reads | N/A | N/A | N/A |
| Searches | N/A | N/A | N/A |
| Repeated reads | N/A | N/A | N/A |
| Context size | N/A | N/A | N/A |
| Input tokens | N/A | N/A | N/A |
| Output tokens | N/A | N/A | N/A |
| Total tokens | N/A | N/A | N/A |
| Wall time | N/A | N/A | N/A |
| Mutations | N/A | N/A | N/A |
| Unintended mutations | N/A | N/A | N/A |
| Verification | N/A | N/A | N/A |

**Classification per scenario:** `INCONCLUSIVE` (no execution). Aggregate classification: **NO-GO** for current repository intelligence (capability absent), not a performance comparison.

---

## 7. Navigation Analysis

- **Did the agent find the correct location?** Not measured (no run).
- **How many irrelevant locations did it inspect?** Not measured.
- **Did it find the relevant symbol or file directly?** No capability exists to do so structurally; the model must grep/ls/read. Expected behavior identical to Track A.
- **Did it follow structural relationships?** No (no caller/reference/interface tool).
- **Did it get trapped by lexical false positives?** Expected yes in both tracks; OHGO offers no mitigation today.
- **Did it rediscover information?** Expected yes; no index/cache.
- **Did it lose previously established repository facts?** Subject to compaction; no repository map to re-inject.
- **Did it require the LLM to reconstruct repository structure manually?** Yes, in both tracks.

---

## 8. Context Efficiency

- Files, bytes, tokens, relevant/irrelevant split: `N/A (not executed)`.
- Structural observation: OHGO's `grep` groups by file and paginates (`GrepDefaultFileLimit = 20`, `GrepMultiFileMatches = 20`), which is a modest context-shaping advantage over raw shell grep, but it remains lexical. `glob`/`ls` are conventional. No symbol-level pruning exists.

---

## 9. Security / Authority Regression Checks

Verified by code inspection (no run needed; these are compile-time/structural properties):

| Invariant | Status |
| --- | --- |
| ASK remains read-only | ✅ Intact (`workspace.Ask` contract; `workspace.RouteTurn` refuses mutations) |
| BUILD remains mutation authority | ✅ Intact |
| FUZZ remains read-only except execution | ✅ Intact |
| No implicit escalation | ✅ Intact (`intent.Classify` defaults to READ on ambiguity) |
| No mutation solely because search returned a candidate | ✅ Intact (no search tool; mutation tools require explicit edit/write) |
| Search results are evidence, not authorization | ✅ N/A (no search tool beyond lexical grep, which is evidence only) |
| Runtime remains final authority over execution | ✅ Intact (`security` gate + `agent.Approval`) |
| Fail-closed behavior | ✅ Intact |
| No regression introduced by benchmark | ✅ No source modified |

**Hard-failure check: none triggered.** The absence of repository intelligence means there is nothing to weaken authority boundaries; the existing boundaries remain fully intact.

---

## 10. Agent Behavior

- Unnecessary exploration: expected in both tracks (no structural shortcut).
- Repeated discovery: expected; no index.
- Scope creep: governed by intent routing + authority, not by repo intel; intact.
- Verification behavior: unchanged; mutation tasks would rely on `bash` tests.
- Reasoning efficiency: entirely model-dependent; no runtime assistance.

---

## 11. Runtime / Architecture Findings

Causal classification per §11:

| Finding | Classification | Notes |
| --- | --- | --- |
| No repository map / structural navigation exists | REPOSITORY INTELLIGENCE | Core gap |
| `ast` package unused (no consumer) | TOOL DESIGN / REPOSITORY INTELLIGENCE | Built but never wired |
| TUI symbol engine never installed | RUNTIME | Optional surface left unconnected |
| `memory` seam has no storage | REPOSITORY INTELLIGENCE (deferred by design) | Per I68, graph.db is reserved/deferred |
| Prompt tells model to use grep/glob | AGENT LOOP / PROMPT | No structural guidance |
| Conventional tools are solid (root containment, pagination, gitignore) | TOOL DESIGN (positive) | Not a gap |
| No usage/token persistence gaps observed | RUNTIME | N/A |

---

## 12. Causal Classification (summary)

- MODEL BEHAVIOR: not isolatable (no run).
- AGENT LOOP: conventional; no repo-intel integration point exists.
- INTENT ROUTING: intact; ambiguity defaults to READ.
- REPOSITORY INTELLIGENCE: **absent** — the dominant finding.
- TOOL DESIGN: lexical tools are well-built but not structural.
- RUNTIME: symbol engine unwired; memory seam storage deferred.
- TASK DESIGN: ground truth prepared but not exercised.
- PROVIDER/MODEL: compliant model unavailable.

---

## 13. Critical Comparison (narrative)

There is no basis to claim OHGO outperforms traditional exploration: OHGO *is* traditional exploration today. The hypothesis under test (runtime-owned repository navigation without external MCP) is **not implemented** in the current codebase. The only candidate building blocks (`ast`, TUI symbol engine, reserved `graph.db`) are unwired or deferred.

---

## 14. Most Important Architectural Question (§17)

> Does OHGO's runtime-owned repository map reduce the amount of repository structure the LLM must reconstruct itself while increasing or preserving the correctness of navigation?

**No — because no runtime-owned repository map exists.** The LLM must reconstruct all repository structure itself via `ls`/`glob`/`grep`/`read`, identically to a traditional agent. There is no evidence of advantage because there is no feature to measure.

**What prevents the advantage:** absence of any structural navigation capability (symbol index, reference/caller resolution, interface implementation mapping, package relationship graph) wired into the tool set or prompt. The `ast` package and TUI symbol engine are present but disconnected; `graph.db` is reserved but unimplemented.

**Partial value assessment:** The existing lexical tools are well-engineered (root containment per I5/I11, gitignore pruning per I14, pagination, grouping, context lines, timeout). These are worth *keeping* as the fallback layer, but they do not constitute repository intelligence. The valuable structural primitives that exist but are unused: `ast.Registry`/`GoBackend` (Go declaration parsing) and the `StatusEngine` surface. Everything else (call graphs, references, interface impls, package graphs) must be built.

---

## 15. Reproducibility Record

| Field | Value |
| --- | --- |
| Timestamp | 2026-10-19 |
| OHGO version | `c6bbd02` |
| Git SHA | `c6bbd02a007b894898b8a5bd8ca7cfce20145402` |
| Model | MiniMax M3.1 Flash (free) — **not available**; no compliant run |
| Provider | N/A |
| Model settings | see §2.1, §2.3 |
| Repository SHA | `c6bbd02a007b894898b8a5bd8ca7cfce20145402` |
| Scenario IDs | T7–T16 |
| Tracks | A, B |
| Session IDs | none created |
| Turn counts | N/A |
| Tool calls | N/A |
| Files read | N/A |
| Commands | N/A |
| Mutations | none (no run; no source modified) |
| Verification | `go build ./...` succeeded; `go vet` not re-run (no code change) |
| Token usage | N/A |
| Wall time | N/A |
| Final result | NO-GO (capability absent) |
| Raw evidence | This report + repository at SHA above; static audit performed via `grep`/`read` of source |

**Raw evidence note:** No session storage was created. The audit relied on source inspection only, consistent with the "do not modify source" rule. External MCP repository intelligence was not used to evaluate OHGO's model-facing capability.

---

## 16. Implementation Decision

**Decision: NO-GO (current) / BUILD (next).**

The current repository intelligence does not demonstrate a meaningful advantage because it does not exist as a runtime capability. Recommendation: **BUILD** a runtime-owned repository intelligence layer, then re-run this benchmark.

Recommendations (each with required fields):

### R1 — Wire structural navigation as first-class tools
- **Finding:** No symbol/reference/caller tool exists; model relies entirely on lexical grep.
- **Evidence:** `buildTools` in `cmd/ohgo/run.go` exposes only `glob`/`grep`/`ls`/`read`; `ast` package has zero production importers.
- **Root cause:** Repository intelligence deferred by design (I68) and never implemented.
- **Owner:** tools + agent packages.
- **Severity:** High.
- **Expected impact:** Direct symbol location, caller/reference traversal, interface-impl discovery → higher precision/recall, fewer turns.
- **Recommended implementation:** Add tools `symbols`/`references`/`callers` (or one `repo` tool) backed by an in-process index; keep lexical tools as fallback.
- **Verification method:** Re-run T7–T16 with MiniMax M3.1 Flash (free); measure navigation F1 ≥ +0.2 over Track A; no authority regression.

### R2 — Implement and connect the symbol index (graph.db)
- **Finding:** `graph.db` is reserved but unimplemented; `ast` is stateless and Go-only.
- **Evidence:** `docs/architecture/STATE_AND_PERSISTENCE.md` §B reserves `<project>/.ohgo/graph.db`; no reader/writer exists.
- **Root cause:** Deferred derived state (I68).
- **Owner:** new `index`/`repo` package + `ast`.
- **Severity:** High.
- **Expected impact:** Cross-file relationships, package graph, test-to-production links.
- **Recommended implementation:** Build a rebuildable, disposable index (per I68: corruption must never block startup; delete+rebuild recovery). Start Go-only via `go/ast`, add tree-sitter behind build tag (I41) for other languages.
- **Verification method:** Index corruption → clean rebuild; startup unaffected; benchmark recall improvement.

### R3 — Connect TUI symbol status
- **Finding:** `SetSymbolStatusFunc` never called; `/status` SYMBOL ENGINE section always hidden.
- **Evidence:** grep shows zero production call sites.
- **Root cause:** Surface left unconnected.
- **Owner:** tui + runtime.
- **Severity:** Low.
- **Expected impact:** Visibility into index state for users.
- **Recommended implementation:** Install the reporter once the index (R2) exists.
- **Verification method:** `/status` shows index stats; tests in `tui/statusview_test.go` already cover rendering.

### R4 — Inject minimal structural context into prompts
- **Finding:** `prompt.Identity` only advises grep/glob; no repository map or symbol summary.
- **Evidence:** `prompt/prompt.go`.
- **Root cause:** No index to summarize.
- **Owner:** prompt package.
- **Severity:** Medium.
- **Expected impact:** Reduce manual reconstruction; improve first-turn accuracy.
- **Recommended implementation:** Add a bounded, top-level package/symbol summary block when an index is available; never inject ground truth or full file contents.
- **Verification method:** Token/context metrics in re-run; no compaction regressions.

### R5 — Keep lexical tools as the fallback/verification layer
- **Finding:** Existing grep/glob/ls/read are well-built.
- **Evidence:** `tools/grep.go`, `tools/glob.go`, `tools/ignore.go`.
- **Root cause:** N/A (positive).
- **Owner:** tools package.
- **Severity:** Low (keep).
- **Expected impact:** Preserve authority and containment invariants.
- **Recommended implementation:** No change; ensure new index tools reuse `resolvePath` containment (I5/I11).
- **Verification method:** Existing tool tests remain green.

### R6 — Re-run this benchmark after R1–R4
- **Finding:** No compliant baseline exists yet.
- **Evidence:** §2.4.
- **Root cause:** Missing model/credentials in this environment.
- **Owner:** benchmark operator.
- **Severity:** High (blocks GO decision).
- **Expected impact:** Produce authoritative metrics.
- **Recommended implementation:** Provision `OPENCODE_API_KEY` for `opencode-zen` and use `opencode-zen/minimax-m3-free` (or the exact `MiniMax M3.1 Flash (free)` identifier when available); execute T7–T16 both tracks; do not modify source between runs.
- **Verification method:** Report populated with real metrics; verdict re-evaluated.

---

## 17. Final Verdict

**NO-GO** for the current repository intelligence (it is absent), with **BUILD** as the implementation decision.

OHGO today is a conventional coding agent with excellent authority/containment invariants but no runtime-owned repository navigation. The hypothesis that OHGO can outperform traditional LLM exploration *without* external MCP is **untested in practice and unsupported by the current code**: the capability to test does not yet exist. Build it (R1–R4), then re-run this benchmark (R6) before claiming an advantage.

---

## 18. Final Rule Compliance

This benchmark did **not** optimize for "OHGO uses fewer tools." It identified that OHGO currently uses *only* conventional tools and therefore cannot yet deliver "the right repository evidence at the right time." The desired architecture (HUMAN → OHGO RUNTIME → REPOSITORY INTELLIGENCE → RELEVANT EVIDENCE → LLM REASONING → PROPOSED ACTION → OHGO AUTHORITY → EXECUTION → VERIFICATION) requires implementing the REPOSITORY INTELLIGENCE layer, which is presently missing. No implementation change was made during this benchmark.
