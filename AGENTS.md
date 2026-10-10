# AGENTS.md

Conventions for working in this Go port.

## Scope

When a behaviour is ambiguous, the TypeScript original is the authority. Port
its **contract**, not its implementation detail: a subsystem that had no clean
Go equivalent should be redesigned rather than transliterated.

## Build, test, check

```sh
go build ./...
go test ./...
go vet ./...
gofmt -l .          # must be empty
```

Run all four before declaring work done. `go test ./...` must be full-suite
safe: never leave global mutable state, `os.Chdir`, or an un-restored
`t.Setenv` that can leak into another file's tests.

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

## Conventions inherited from the original port plan

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
- **Centralise before adding a helper.** Check `tools/base.go`,
  `ai/sse.go`, and the domain package before writing a utility. Two
  implementations of the same thing is a bug even when both work.

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

1. Implement `ai.Provider` in `ai/<name>.go`, reusing `doJSON` and `readSSE`
   from `ai/sse.go`.
2. Add the `api` constant and register it in `Registry.ProviderFor`.
3. Add the credential lookup to `envAPIKeyVars`.
4. Add SSE round-trip tests with an `httptest` server: text, tool calls with
   streamed argument fragments, usage, stop reason, and an error event.

## Adding a tool

1. Create the type in `tools/<name>.go`, embedding `tools.Base`.
2. Write the JSON Schema as a `map[string]any` in the constructor. A malformed
   schema must panic at construction — never reach a call site.
3. Declare the `agent.Approval` tier honestly. Omitted means "prompt every
   time", which is the correct default.
4. Register it in `cmd/ohgo/run.go`'s `buildTools`.
5. Test: the happy path, each rejection, cancellation, and root containment.
