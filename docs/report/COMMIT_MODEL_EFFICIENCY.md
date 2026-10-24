# COMMIT Model Efficiency Pass

**Date:** 2026-10-25
**Scope:** `/commit` proposal path, prompt, evidence, completion budget, reasoning policy, diagnostics, and tests.
**Result:** PASS

## Conclusion

`/commit` is now a small, deterministic Git operation with a bounded
message-proposal model call. The model still only proposes text; staging, scope,
approval, execution, and verification remain deterministic.

```text
Git evidence
    -> small model prompt
    -> commit message proposal
    -> deterministic validation
    -> human approval
    -> deterministic Git execution
    -> deterministic verification
```

## What Changed

### Prompt

Reduced the COMMIT system prompt from a 1,169-byte agent-style instruction to a
639-byte message contract. It contains no tools, no ASK/BUILD/FUZZ context, no
repository-exploration instructions, no approval policy, and no Git execution
instructions.

### Evidence

The model now receives only:

- repository name,
- exact approved scope,
- per-file change summary,
- bounded relevant diff.

Removed duplicated staged/unstaged/untracked lists, ignored/internal lists,
HEAD/branch metadata, and recent-commit history.

### Bounds

- Diff: 24 KiB.
- Untracked preview: 4 KiB.
- Scope list: 100 paths.
- Truncation is explicit in the evidence; files are not silently omitted.

### Budget

COMMIT now has its own `commit.DefaultProposalTokens = 1024`, replacing the
shared `8192` coding budget.

### Reasoning

COMMIT asks for the lowest control the bound model exposes:

- provider-native explicit disable when available (`reasoning_effort: "none"`),
- otherwise the lowest published ladder level,
- Anthropic thinking omitted because a COMMIT-sized completion cannot hold a
  valid thinking budget,
- mandatory reasoners keep the role effort rather than claiming a false
  disable.

Capabilities are carried on `ai.Model` (`ReasoningEfforts`,
`ReasoningDisable`, `ReasoningMandatory`) and resolved from the catalog. The
call site never branches on a provider name or model id.

### Truncation Safety

Any `StopMaxTokens` reply is now `ErrModelTruncated`, even when the visible
prefix happens to satisfy the format. A partial message is never trimmed or
accepted.

### Focused Regeneration

When the only failure is an overlong but structurally valid subject, the retry
sends only the previous subject instead of the whole evidence. The already-valid
body is reattached byte-for-byte. Other validation failures still use the full
evidence and the deterministic retry reason.

### Diagnostics

Added internal diagnostics for:

- prompt bytes,
- evidence bytes,
- input/output/reasoning tokens,
- reasoning effort,
- attempts and regenerations,
- retry reasons,
- final proposal validity.

These remain internal; the normal TUI is unchanged.

## Measurements

### Method

- Real provider and real configured COMMIT model:
  `openrouter/dots-studio/dots-3-note-preview:free`.
- Baseline measured in a detached worktree at the pre-change commit.
- After measured on the final tree.
- The same four fixtures were used for both runs.
- Reasoning tokens were read from the provider's native
  `completion_tokens_details.reasoning_tokens`.

### Fixtures

| Case | Change |
|---|---|
| A | one new file `hana.txt` |
| B | one tracked file, one line added |
| C | three new files (`README.md`, `a.go`, `b.go`) |
| D | 40 new files under `pkg/` |

### Before

| Case | Input | Output | Reasoning (raw) | Latency | Calls | Budget |
|---|---:|---:|---:|---:|---:|---:|
| A | 391 | 1,487 | 1,206–1,618 | 20.5s | 1 | 8,192 |
| B | 446 | 1,259 | 1,137–1,963 | 14.0s | 1 | 8,192 |
| C | 438 | 2,717 | 1,846–2,285 | 26.2s | 1 | 8,192 |
| D | 2,126 | 2,216 | 1,086–1,341 | 26.4s | 1 | 8,192 |

Before prompt: 1,169 bytes. Before evidence A/B/C/D:
266/376/398/4,775 bytes.

The user-reported small-change baseline was **2,695 input / 3,035 output**.

### After

| Case | Input | Output | Reasoning | Latency | Calls | Final validity |
|---|---:|---:|---:|---:|---:|---|
| A | 188 | 20 | 0 | 1.5s | 1 | valid |
| B | 243 | 17 | 0 | 1.1s | 1 | valid |
| C | 296 | 45 | 0 | 1.8s | 2 | valid |
| D | 1,717 | 47 | 0 | 3.3s | 2 | valid |

After prompt: 639 bytes. After evidence A/B/C/D:
87/213/202/4,048 bytes. Max output: 1,024. Reasoning effort: `"none"` for
these OpenRouter calls.

C and D used the focused subject rewrite. Their bodies were preserved
unchanged.

### Reduction

| Case | Input | Output | Total | Latency |
|---|---:|---:|---:|---:|
| A | 391 -> 188 (**52%**) | 1,487 -> 20 (**99%**) | 1,878 -> 208 (**89%**) | 20.5s -> 1.5s (**93%**) |
| B | 446 -> 243 (**46%**) | 1,259 -> 17 (**99%**) | 1,705 -> 260 (**85%**) | 14.0s -> 1.1s (**92%**) |
| C | 438 -> 296 (**32%**) | 2,717 -> 45 (**98%**) | 3,155 -> 341 (**89%**) | 26.2s -> 1.8s (**93%**) |
| D | 2,126 -> 1,717 (**19%**) | 2,216 -> 47 (**98%**) | 4,342 -> 1,764 (**59%**) | 26.4s -> 3.3s (**87%**) |

Against the reported small-change baseline:

```text
input reduction:   2,695 -> 188   (93.0%)
output reduction:  3,035 -> 18    (99.4%)
total reduction:   5,730 -> 206   (96.4%)
```

## Real TUI E2E

Disposable repository with one tiny `hana.txt` change and an excluded
`.ohgo/project.yml`. Driven through the real TUI state machine and the actual
configured COMMIT model.

```text
provider: openrouter
model: dots-studio/dots-3-note-preview:free
input tokens: 188
output tokens: 18
reasoning tokens: 0
max output tokens: 1024
reasoning effort: "none"
prompt bytes: 768
evidence bytes: 87
attempts: 1
elapsed: 1.385s
proposal: "feat(hana): add hello message file

- introduce hana.txt with hello content"
commit SHA: 330f46019942bf21e2fdc8abc4863b10a8cfe4d9
commits: 2
committed files: "hana.txt"
diagnostics: ... attempts=1 regenerations=0 ... valid=true
```

Verified:

- spinner appeared before the proposal,
- proposal rendered normally,
- one approval produced exactly one commit,
- only `hana.txt` was committed,
- `.ohgo/` remained excluded,
- no manual retry was needed,
- post-commit verification passed.

## Test Coverage

Added or updated tests cover:

- prompt minimization (no tool/agent/approval context),
- dedicated small COMMIT budget,
- visible-but-truncated replies are never accepted,
- focused subject rewrite without re-sending evidence,
- precise overlong-subject detection; malformed subjects still use full
  regeneration,
- diagnostics token/byte/regeneration accounting,
- model binding,
- retry and edit behavior,
- cancellation and timeout behavior,
- no repository mutation before approval,
- the `git commit help me` regression.

Final checks:

```text
go build ./...   PASS
go test ./...    PASS
go vet ./...     PASS
gofmt -l .       clean
```

## Preserved Semantics

No change to:

- dedicated COMMIT role/model binding,
- settings snapshotting,
- `/commit`, `/commit --retry`, `/commit --edit`,
- human approval and default Cancel,
- exact approved scope,
- `.ohgo/` exclusion,
- staged-scope rules,
- fingerprint and pre-execution index verification,
- one invocation -> at most one commit,
- no amend, split, corrective second commit, or cleanup commit,
- post-commit SHA/tree/parent/message/status verification,
- stale-result protection,
- cancellation isolation and session cancellation,
- timeout classification,
- no repository mutation before approval.

## Residual Note

The free configured model occasionally ignores the 36-character subject limit
on the first attempt for multi-file changes. The focused subject correction is
cheap (~60 tokens, no evidence re-send) and preserves the reviewed body. If the
correction still fails, `/commit` reports a safe invalid proposal and performs
no Git mutation.

## Final Answer

**Yes.** `/commit` has become a lightweight, precise Git-message proposal
operation rather than a mini reasoning agent, without weakening authority,
validation, execution, or verification.
