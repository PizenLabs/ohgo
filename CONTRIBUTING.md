# Contributing to ohgo

Thanks for taking the time to contribute. ohgo is a coding agent that runs
untrusted model output against real repositories, so correctness, containment,
and honest failure modes matter more than feature count. Small, well-tested
changes are the easiest to review and the safest to ship.

By participating you agree to follow the [Code of Conduct](CODE_OF_CONDUCT.md).

## Ways to contribute

- **Report a bug** with the
  [bug report template](https://github.com/PizenLabs/ohgo/issues/new?template=bug_report.yml).
  Include the version, OS, workspace, provider/model, and a minimal
  reproduction.
- **Request a feature** with the
  [feature request template](https://github.com/PizenLabs/ohgo/issues/new?template=feature_request.yml).
  Explain the contract you want, not only the implementation.
- **Ask a question** or propose a design in
  [GitHub Discussions](https://github.com/PizenLabs/ohgo/discussions).
- **Send a pull request** for a bug fix, test, documentation improvement, or a
  new provider/tool.

For anything larger than a bug fix, open an issue or discussion first. It is
cheaper to agree on the contract than to rework a finished patch.

**Do not report security vulnerabilities in a public issue.** Follow
[SECURITY.md](SECURITY.md) instead.

## Development setup

Requirements:

- Go **1.27+**
- `git`
- `golangci-lint` (optional, for `make lint`)
- No C toolchain — the build is `CGO_ENABLED=0`

```sh
git clone https://github.com/PizenLabs/ohgo.git
cd ohgo
make build
./ohgo --help
```

Useful targets:

```sh
make build    # static binary at ./ohgo
make test     # go test ./...
make vet      # go vet ./...
make fmt      # gofmt check (fails if files need formatting)
make check    # build + vet + gofmt + full test suite
make race     # go test -race ./...
make bench    # benchmarks for tools, agent, and ai
make lint     # golangci-lint
```

## Before you open a pull request

All four checks must pass:

```sh
go build ./...
go test ./...
go vet ./...
gofmt -l .          # must print nothing
```

`go test ./...` must be full-suite safe. Never leave global mutable state,
`os.Chdir`, or an un-restored `t.Setenv` behind; another file's tests will run
in the same process.

Read [`AGENTS.md`](AGENTS.md) for the project's engineering rules. The short
version:

- **Keep logic in a testable seam.** Anything that talks to the network, the
  filesystem, or the clock goes behind an interface or a function field, so a
  test can substitute it. `ai.StreamFn` and `agent.Config.Approve` are the
  models to copy.
- **No policy by model identity.** Never branch on a model id, provider name,
  or a hardcoded per-model table. Capabilities come from `ai.Model` fields that
  the catalog resolves.
- **No prompts in code.** System prompts and tool descriptions live in the
  `prompt` package or in the tool's own constructor.
- **Centralise before adding a helper.** Check
  `internal/execution/tools/base.go`, `internal/provider/ai/sse.go`, and the
  domain package before writing a utility. Two implementations of the same
  thing is a bug even when both work.
- **Prefer the standard library.** Do not add a dependency for something the
  standard library already covers. Intentional external dependencies are
  `cobra`, `bubbletea`/`lipgloss`, `modernc.org/sqlite`, and `golang.org/x/term`.

### Style

- No `any` in new code unless a boundary genuinely requires it (decoding
  provider JSON is fine).
- Prefer `errors.Is` / `errors.As` over string matching.
- Wrap with `%w` so cause chains survive: `fmt.Errorf("read %q: %w", path, err)`.
- Sentinel errors are package-level: `var ErrX = errors.New(...)`.
- Comments explain **why**, not what. Document the contract, the invariant, and
  the non-obvious failure mode.
- Every exported identifier has a doc comment.

### Tests

Each test must defend one externally observable contract and name the failure
mode a consumer would see.

**Good:** a transformation, a branch or boundary, an external contract (exact
bytes when downstream parses them), precedence and negative contracts, and
regressions that exercise the prior real failure path.

**Not enough:** asserting a constructor copied a fixture, `fn(x) === x`
passthrough, wording/default checks, "the code ran", or length growth.

For cyclic or unbounded inputs, assert bounded output, a surfaced error, or a
state change — not merely that the call returned.

Two rules that are not negotiable:

- **No source grepping.** Never read an implementation file and assert on its
  text. Assert the observable contract by running the code.
- **No global patching.** Construct the dependency directly or inject a
  function; do not replace package-level state for the duration of a test.

After adding a test, break the thing it covers and confirm the test fails. A
test that cannot fail is not a test.

## Adding a provider

1. Implement `ai.Provider` in `internal/provider/ai/<name>.go`, reusing
   `doJSON` and `readSSE` from `internal/provider/ai/sse.go`.
2. Add the `api` constant and register it in `Registry.ProviderFor`.
3. Add the credential lookup to `creds.ProviderEnvVars` in
   `internal/provider/creds`.
4. Add SSE round-trip tests with an `httptest` server: text, tool calls with
   streamed argument fragments, usage, stop reason, and an error event.

Model facts are data, not code. Update the catalog generator and regenerate
`internal/provider/catalog/catalog.json`; never add an `if model == "..."` branch.

## Adding a tool

1. Create the type in `internal/execution/tools/<name>.go`, embedding
   `tools.Base`.
2. Write the JSON Schema as a `map[string]any` in the constructor. A malformed
   schema must panic at construction — never reach a call site.
3. Declare the `agent.Approval` tier honestly. Omitted means "prompt every
   time", which is the correct default.
4. Register it in `cmd/ohgo/build.go`'s `buildTools`.
5. Test the happy path, each rejection, cancellation, and root containment.

If the tool reaches the network, it must go through the knowledge/external
boundary so results stay bounded, labelled, and untrusted.

## Commit messages

Use a short imperative subject with a conventional prefix, matching the
existing history:

```text
feat(tools): add refs for static call sites
fix(tui): keep viewport pinned on resize
docs(readme): document the completion verdicts
test(agent): cover cancellation during tool approval
```

Reference the issue in the body when one exists (`Fixes #123`). Keep unrelated
changes in separate commits.

## Pull request process

1. Fork the repository and create a topic branch.
2. Make the change and add the tests that defend it.
3. Run `make check` (and `make race` when touching concurrent code).
4. Fill in the [pull request template](.github/PULL_REQUEST_TEMPLATE.md):
   what changed, why, how, and which checks pass.
5. The CI workflow runs build, vet, gofmt, tests with coverage, and
   `golangci-lint`. A red check blocks review.
6. Address review comments by pushing additional commits; do not force-push a
   branch under review.

### What reviewers look for

- The change defends a contract, and the test fails if the contract breaks.
- Containment, approval, budget, and evidence rules still hold.
- No new dependency the standard library already covers.
- No model/provider special-casing, and no prompt strings outside `prompt/` or
  a tool constructor.
- Error paths name their cause and surface it; nothing fails silently.
- Public behaviour is documented, and `CHANGELOG.md` has an `Unreleased` entry
  for user-visible changes.

## Documentation

Canonical sources:

- Architecture topology and ownership:
  [`OHGO_ARCHITECTURE_MAP.md`](OHGO_ARCHITECTURE_MAP.md).
- Permanent architectural rules:
  [`OHGO_ARCHITECTURE_RULES.md`](OHGO_ARCHITECTURE_RULES.md).
- Migration history:
  [`OHGO_ARCHITECTURE_MIGRATION.md`](OHGO_ARCHITECTURE_MIGRATION.md).
- User-facing behaviour: [`README.md`](README.md).
- Release history: [`CHANGELOG.md`](CHANGELOG.md).
- Agent-facing conventions: [`AGENTS.md`](AGENTS.md).

Runtime behaviour, authority, persistence, intent grounding, and MCP are
explained by supporting documents in [`docs/architecture/`](docs/architecture/).
Those documents defer topology to the architecture map.

When behaviour changes, update the document that describes the contract. Keep
one canonical source per fact: link to it instead of restating it, and archive
superseded material under `old_docs/` rather than leaving it active. A comment
that only restates the code is noise; a missing invariant is a bug.

## Releases

Releases are cut from `main` with a `v*` tag. The release workflow builds
static tarballs for `linux/amd64`, `linux/arm64`, `darwin/amd64`, and
`darwin/arm64`, writes `sha256sums.txt`, and publishes a GitHub release with
generated notes. `CHANGELOG.md` is the human-authored record; released sections
are immutable.

A release also updates `internal/update/changelog.json`, the release notes embedded in
the binary for the one-shot What's New card. It is local content, never fetched
from GitHub, and its key must match the release tag. Follow
[`docs/RELEASING.md`](docs/RELEASING.md) for the complete edit checklist,
versioned-document rules, artifact verification, and update-lifecycle smoke
test. A version tag alone is not a release if those records disagree.

## License of contributions

ohgo is licensed under the [Apache License, Version 2.0](LICENSE). By
submitting a pull request you agree that your contribution is provided under
the same license and that you have the right to submit it.
