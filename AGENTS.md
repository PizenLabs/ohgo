# AGENTS.md

Operating conventions for contributing to ohgo.

## Scope

The current Go implementation, its tests, and the canonical architecture
documents define the contract.

When existing implementation does not cleanly support a requirement, redesign
the subsystem around its current ownership and invariants rather than
translating or preserving obsolete structure.

## Build, test, check

```sh
go build ./...
go test ./...
go vet ./...
go test -race ./...
gofmt -l .          # must be empty
```

Run all five before declaring work done. `go test ./...` must be full-suite
safe: never leave global mutable state, `os.Chdir`, or an un-restored
`t.Setenv` that can leak into another file's tests.

A green test suite is necessary but does not by itself prove that the requested
behaviour or architecture was implemented correctly.

## Style

- **No `any` in new code** unless a boundary genuinely requires it
  (decoding `map[string]any` from provider JSON is fine).
- Prefer `errors.Is` / `errors.As` over string matching on errors.
- Wrap with `%w` so cause chains survive: `fmt.Errorf("read %q: %w", path, err)`.
- Sentinel errors are package-level `var ErrX = errors.New(...)`.
- **Comments explain why, not what.** A comment that restates the code is
  noise. Document the contract, the invariant, and the non-obvious failure
  mode. Every exported identifier gets a doc comment.
- Do not add a dependency to solve a problem the standard library already
  covers. Intentional external dependencies: `cobra` (CLI), `bubbletea` /
  `lipgloss` (TUI rendering), `modernc.org/sqlite` (pure-Go session store, no
  cgo), `golang.org/x/term` (terminal detection).

## Engineering conventions

- **Logic in a testable seam.** Anything that talks to the network, the
  filesystem, or the clock goes behind an interface or a function field, so a
  test can substitute it. `ai.StreamFn` and `agent.Config.Approve` are the
  models to copy.
- **No policy by model identity.** Never branch on a model id, provider name
  string, or a hardcoded per-model table. Capabilities come from `ai.Model`
  fields that the catalog resolves. An id no selector can isolate is data, not
  a special case.
- **No prompts in code.** System prompts and tool descriptions live in the
  `prompt` package or in the tool's own constructor. No inline prompt strings
  at call sites.
- **Centralise before adding a helper.** Check `internal/execution/tools/base.go`,
  `internal/provider/ai/sse.go`, and the domain package before writing a
  utility. Two implementations of the same thing is a bug even when both work.
  A persisted format (a model reference, a session snapshot) has **one**
  formatter and one parser.
- **Use existing repository-intelligence seams.** Prefer the repository's
  existing structural discovery mechanisms when they provide the coordinates
  required by the task. Do not introduce a second discovery, index, or search
  path for a problem an existing domain already owns.

## Agent authority and completion

ASK, FUZZ, and BUILD are authority boundaries, not conversational labels.

- **ASK** is read-only repository understanding. It must not mutate repository
  source or durable project state.
- **FUZZ** is exploratory and adversarial execution. It may run commands,
  benchmarks, stress tests, and reproduction probes, but must not mutate
  repository source.
- **BUILD** is the sole repository mutation authority. It implements and
  verifies the requested change rather than treating implementation as an
  open-ended exploration task.

Do not infer execution authority from the first verb, model preference, tool
availability, or an inferred user intention. Effective authority comes from
the current workspace and turn intent.

Never widen authority because a mutation, delegation, or additional capability
appears useful. Model output is a proposal, not an authority grant.

Tool approval does not replace workspace or intent authority. A tool must not
provide a mutation path unavailable to the current execution mode.

Do not declare completion from model confidence, intent, or tool success alone.
Completion requires the requested state transition and appropriate verification
evidence.

## Testing rules

Each test must defend one externally observable contract and name the failure
mode a consumer would see. Specifically:

- **Good:** a transformation, a branch/boundary, an external contract (exact
  bytes when downstream parses them), precedence/negative contracts,
  regressions that trigger the prior real failure path.
- **Bad:** asserting a constructor copied a fixture; `fn(x) === x` passthrough;
  wording/default checks; "the code ran"; bare `not.toThrow()`; length growth.
- **Termination:** for cyclic or unbounded inputs, assert bounded output, a
  surfaced error, or a state change — not just "did not hang".
- **No source grepping.** Never read an implementation file and assert on its
  text. Assert the observable contract by running the code.
- **No `mock.module`-style global patching.** Construct the dependency
  directly or inject a function.
- Prefer one test per invariant over many tiny ones asserting fields from the
  same transition.
- After adding a test, break the thing it covers and confirm it fails. A test
  that cannot fail is not a test.

## Adding a provider

1. Implement `ai.Provider` in `internal/provider/ai/<name>.go`, reusing
   `doJSON` and `readSSE` from `internal/provider/ai/sse.go`.
2. Add the `api` constant and register it in `Registry.ProviderFor`.
3. Add the credential lookup to `creds.ProviderEnvVars`
   (`internal/provider/creds`).
4. Add SSE round-trip tests with an `httptest` server: text, tool calls with
   streamed argument fragments, usage, stop reason, and an error event.

## Adding a tool

1. Create the type in `internal/execution/tools/<name>.go`, embedding
   `tools.Base`.
2. Write the JSON Schema as a `map[string]any` in the constructor. A malformed
   schema must panic at construction — never reach a call site.
3. Declare the `agent.Approval` tier honestly. Omitted means "prompt every
   time", which is the correct default. Approval does not grant authority
   beyond the current workspace and turn intent.
4. Register it in `cmd/ohgo/build.go`'s `buildTools`.
5. For mutating tools, preserve the existing mutation preview/evidence contract.
6. Test the happy path, each rejection, cancellation, root containment, and
   any mutation-preview/evidence contract.

## Where code goes

`OHGO_ARCHITECTURE_MAP.md` answers "where does feature X belong" and
`OHGO_ARCHITECTURE_RULES.md` is the permanent contract. Both are enforced by
`internal/architecture` (a test-only package that fails `go test ./...`).

In short:

- Implementation lives under `internal/`, grouped by domain
  (`control`, `execution`, `continuity`, `intelligence`, `provider`, `tui`, …).
  `cmd/ohgo` is composition only; nothing imports it.
- `internal/agent` is a closed kernel: it may import only `contract`, `budget`,
  `security`, `jsonschema`, `ai`, `tokens`. Capabilities, workspace policy,
  persistence and presentation reach it through interfaces, never imports.
- Every new package must be declared in the manifest in
  `internal/architecture/rules_test.go`, with the domain that owns it.
- Do not create generic architectural buckets such as `common/`, `shared/`,
  `util/`, `helpers/`, `runtime/`, `core/`, `engine/`, `manager/` or
  `pipeline/`. If a concept has no owner, deciding the owner is the task.

## Documentation

The canonical sources are `OHGO_ARCHITECTURE_MAP.md` (topology and ownership),
`OHGO_ARCHITECTURE_RULES.md` (permanent constraints), and
`OHGO_ARCHITECTURE_MIGRATION.md` (history). Behavioural detail lives in
`docs/architecture/`; those documents defer topology to the map.

- One fact has one canonical document. Link to it; do not restate it.
- Update the canonical document in the same change that alters the fact.
- Move superseded or historical material to `old_docs/`; never leave it
  competing with current guidance. `old_docs/` is a temporary archive, not an
  active documentation domain.
- Use stable, purpose-named files. Do not create versioned architecture names
  (`*_V2`, `*_FINAL`, `*_NEW`) or generic dumping files (`NOTES.md`).
