# GRY lifecycle hooks in OHGO

> Status: opt-in, headless (`ohgo run`) integration. The TUI path is not wired.
> GRY is a read-only evidence provider; OHGO keeps all authority.

## Contract

OHGO invokes the documented GRY CLI, not a shared library and not GRY's MCP
surface:

```text
gry hook <stage> --project <name> --json --no-cache --max-tokens <n> \
    [--task <task>] [--task-id <id>] [--target <qn>] [--changed-path <path>]
```

Stages: `before_plan`, `before_edit`, `after_edit`, `before_finish`.

The response envelope is `{hook, context, failure}`. `context.status` is the
query outcome; `context.evidence` is the freshness/coverage contract.
`complete_for_scope=false` or `repository_scan=false` is not an error but must
not be shown as complete evidence.

## Enablement

The adapter is disabled by default. Enable it for one run:

```sh
export OHGO_GRY_HOOK=1
export OHGO_GRY_BIN=/path/to/gry
export OHGO_GRY_PROJECT=<indexed-project-name>
export OHGO_GRY_CACHE_DIR=<same cache used to index>   # optional
export OHGO_GRY_TIMEOUT_MS=1500                        # optional, 100..30000
export OHGO_GRY_MAX_TOKENS=6000                        # optional, 128..1000000
export OHGO_GRY_DEBUG=1                                # optional status lines
ohgo --dir <repo> --workspace build --plain --no-tui -y run "<task>"
```

`OHGO_GRY_PROJECT` is not guessed from the directory: GRY owns project identity.
A missing binary or project disables the adapter instead of creating a silent
dependency.

## Lifecycle and identity

| Stage | Trigger | Invocation count |
|---|---|---|
| `before_plan` | once at the start of `ohgo run`, before the first model request | one |
| `before_edit` | before a write/patch tool with a path argument runs | one per distinct edit target |
| `after_edit` | after a successful, mutating tool result | one per distinct edit target |
| `before_finish` | after the run ends, including a cancelled/limited run | one |

Duplicate suppression is keyed by session id, stage, task, sorted targets and
sorted changed paths. Two edits to different files are separate events. Two
identical events in one session reuse the first result and report
`cached=true`; they do not start a second process. A different session, stage,
task or file is never suppressed.

`before_plan` evidence is injected as a system block. `after_edit` evidence is
appended, bounded and clearly labelled, to the successful edit result so the
next model request sees it. `before_edit` evidence is not a permission check.

## Failure contract

| Condition | Adapter result | OHGO behaviour |
|---|---|---|
| adapter disabled | `status=disabled`, no process | unchanged |
| GRY missing / non-zero exit | `failure.code=unavailable` | run continues; evidence is absent and observable |
| timeout | `failure.code=timeout`, retryable | run continues; a later event retries |
| invalid JSON / missing envelope | `failure.code=invalid_output` | treated as failure, never empty success |
| `not_indexed` | `failure.code=not_indexed` | treated as failure, never empty success |
| output over 128 KiB | `failure.code=output_too_large` | no partial payload is parsed |
| `partial` / `stale` context | success with explicit freshness | evidence is labelled; OHGO decides whether to continue |
| cancellation | `failure.code=cancelled` | no retry |

None of these paths can widen authority. The security boundary (`Authorize`),
approval gate, grounding, budget and mutation execution remain entirely in OHGO.
A GRY failure cannot grant a capability or make an unapproved edit execute.

## Security and data minimisation

- `exec.CommandContext` with an argument vector; no shell, so repository content
  is never interpreted as a command.
- The child receives only `PATH`, `HOME`, `TMPDIR`, locale, `GRY_DB` and
  `GRY_CACHE_DIR`; model credentials and unrelated secrets are not inherited.
- Project names are validated against `[A-Za-z0-9][A-Za-z0-9._-]{0,127}`.
- stdout is capped at 128 KiB, stderr at 4 KiB.
- Values of secret-bearing environment variables are replaced with
  `<redacted-env>` if they ever appear in output.
- Injected evidence contains names, paths, statuses and uncertainty only, never
  source bodies, prompts or environment values. It is labelled untrusted
  repository evidence, not policy.

## Verification

```sh
go test ./internal/gryhook
go test ./...
go vet ./...
gofmt -l .          # must be empty
```

Deterministic lifecycle coverage lives in
`internal/gryhook/install_test.go`: a scripted model calls a real write tool and
the fake GRY records the stage order
`before_plan → before_edit → after_edit → before_finish`. The test also asserts
that the edit result carries both evidence blocks and the authority boundary.

The opt-in live adapter test is `internal/gryhook/live_test.go`:

```sh
GRY_BIN=/path/to/gry GRY_CACHE=/path/to/cache GRY_PROJECT=goca \
  go test ./internal/gryhook -run TestGryHookLive -v
```

It runs `before_plan` and `before_finish` against a real index and asserts that
a bounded hook never claims `repository_scan=true`. The separate
`mcp/gry_live_test.go` covers the MCP read path.

## Controlled end-to-end run (2026-10-27)

A copy of `go-backend-clean-architecture` was indexed as `goca-e2e4` and given
`.ohgo/project.yml`. OHGO was asked to add one comment above
`taskRepository.Create`, under `--workspace build -y`.

Observed stages:

```text
gry before_plan:  status=ok     freshness=unverified complete_for_scope=false
gry before_edit:  status=ok     freshness=unverified complete_for_scope=false
gry after_edit:   status=stale  freshness=stale      complete_for_scope=false
gry before_finish:status=stale  freshness=stale      complete_for_scope=false
```

The final hook correctly refused to claim completeness because the edit was not
reindexed. The diff was exactly one added comment. `go test ./...` passed after
the run. OHGO reported `NEEDS_REVIEW` because the model did not run verification
inside the session; that is OHGO's completion policy doing its job, not a hook
failure. The original fixture was never modified.
