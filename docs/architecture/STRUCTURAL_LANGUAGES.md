# Structural Language Coverage

This document is the canonical capability table for the native structural
runtime. Topology and ownership remain in
[`OHGO_ARCHITECTURE_MAP.md`](OHGO_ARCHITECTURE_MAP.md); runtime behaviour is in
[`ARCHITECTURE.md`](ARCHITECTURE.md).

## Build variants

- **Go-only (`CGO_ENABLED=0`)** — the native `go/ast` backend is always present.
  Every other supported extension is inventory metadata marked `UNSUPPORTED`;
  it is never parsed by a second lexical parser and never counted as structural
  completeness.
- **Multi-language (`CGO_ENABLED=1`)** — the pinned tree-sitter grammars in
  `github.com/smacker/go-tree-sitter` compile into the binary. No grammar is
  downloaded or loaded at runtime.

The dependency is pinned in `go.mod`; grammar versions are fixed by that module
revision. The release matrix stays CGO-free for portability; use
`make build-multilang` for the complete adapter build.

## Adapter coverage

| Language | Extensions | Symbols | Imports | Calls | Inheritance | Tests | Entrypoints |
|---|---|:---:|:---:|:---:|:---:|:---:|:---:|
| Go | `.go` | native | native graph | native resolver | unsupported | native | native |
| TypeScript | `.ts`, `.mts`, `.cts` | yes | yes | name-resolved | yes | common conventions | Next.js routes |
| TSX | `.tsx` | yes | yes | name-resolved | yes | common conventions | Next.js routes |
| JavaScript | `.js`, `.mjs`, `.cjs` | yes | yes | name-resolved | yes | common conventions | Next.js routes |
| JSX | `.jsx` | yes | yes | name-resolved | yes | common conventions | Next.js routes |
| Java | `.java` | yes | yes | name-resolved | explicit syntax | JUnit annotations | `main` |
| C# | `.cs` | yes | yes | name-resolved | base list | xUnit/NUnit/MSTest attributes | `Main`, top-level statements |
| Rust | `.rs` | yes | yes | name-resolved | trait impl | `#[test]` and `tests/` | `main` |
| Kotlin | `.kt`, `.kts` | yes | yes | name-resolved | delegation clauses | `@Test` and test paths | `main` |
| Ruby | `.rb` | yes | require/load | name-resolved | superclass/mixins | RSpec/Minitest conventions | script guard |
| C | `.c`, `.h` | yes | include | name-resolved | unsupported | common test macros | `main` |
| C++ | `.cc`, `.cpp`, `.cxx`, `.hpp`, `.hh`, `.hxx` | yes | include | name-resolved | base clauses | common test macros | `main` |

## Resolution vocabulary

Every materialised edge carries `RESOLVED`. A call or inheritance target that
matches no declaration is `UNRESOLVED`; a target that matches more than one is
`AMBIGUOUS`. Neither is emitted as an edge; both are disclosed in the result's
unknown summary. `UNSUPPORTED` is used for a form the adapter cannot resolve
(e.g. a Go method reference or semantic dispatch). This is syntax-level
resolution, not compiler/type-checker certainty.

## Deliberate limits

- Tree-sitter adapters do not perform type resolution, overload resolution,
  macro expansion, or compiler-level interface implementation checks.
- A non-Go call is materialised only when its target name is unique. Receiver
  dispatch and dynamic calls remain unknown.
- Test associations are static references/calls, never behavioral coverage.
- `graph` remains the Go package-import graph; use `structure`/`refs` for
  language-neutral relationships.
- The semantic activation level is reserved and fails explicitly. No language
  server, compiler, or network parser is launched.
- The generated grammar for C# is upstream-marked incomplete; partial syntax
  errors are reported in `facts.Issues` and keep the snapshot structurally
  incomplete.
