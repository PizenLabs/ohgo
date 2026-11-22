# Structural Language Coverage

This document is the canonical capability table for the native structural
runtime's observation layer. The interpretation layer over it — the adaptive
planner, strategy portfolio, evidence semantics, and result cache — is canonical
in [`STRUCTURAL_INTELLIGENCE.md`](STRUCTURAL_INTELLIGENCE.md). Topology and ownership remain in
[`OHGO_ARCHITECTURE_MAP.md`](OHGO_ARCHITECTURE_MAP.md); runtime behaviour is in
[`ARCHITECTURE.md`](ARCHITECTURE.md).

## Build variants

- **Multi-language (default)** — `make build` uses the host toolchain. With a
  C compiler the pinned tree-sitter grammars in
  `github.com/smacker/go-tree-sitter` compile into the binary; no grammar is
  downloaded or loaded at runtime. `make build-multilang` is the explicit cgo
  target and fails loudly without a toolchain.
- **Portable (`CGO_ENABLED=0`)** — `make build-portable` keeps the native
  `go/ast` backend only. Every other supported extension is inventory metadata
  marked `UNSUPPORTED`; it is never parsed by a second lexical parser and never
  counted as structural completeness.

The release publishes both variants for every supported platform. `install.sh`
prefers the multi-language artifact, and `ohgo update` selects the artifact
matching the running binary so an update never silently removes adapters.

## Project scope

Structural scope follows project boundaries, not only the enclosing
repository's `.gitignore`. A directory carrying its own `.git` entry is a nested
repository and is included in the same snapshot even when a parent rule such as
`backend/` excludes it; its own ignore files govern its contents. The
`nested_projects` field reports the boundary and the `scope` view explains it.
Ordinary excluded directories are not indexed and are reported as
`excluded_roots` with the decisive ignore rule and shallow markers such as
`go.mod` or `go.work`. Scope, inventory, structural, and result completeness are
separate fields; a language table cannot make an excluded or unparsed path
appear covered.

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

Repeated names are narrowed by evidence the referring file actually provides,
in this order:

1. A declared member (`Class.method`) or a local qualified name.
2. A unique declaration in the referring file (local scope wins).
3. A unique declaration in a resolved import. Named and default imports bind a
   bare name; namespace and default alias imports bind a qualified name.
4. Otherwise the global name rule: one declaration resolves, several remain
   `AMBIGUOUS`, none is `UNRESOLVED`.

A qualified reference is stricter than a bare one: `<ui.Button />` and
`console.log()` do not prove a global `Button` or `log`. The qualifier must name
a declared member or an imported namespace/default whose target declares the
name; an unknown qualifier stays unresolved. This prevents a library namespace
from manufacturing an edge to an unrelated local declaration.

TypeScript/TSX references cover JSX element usage, type annotations, and
`typeof X` value queries. A `typeof Button` wrapper is therefore visible to
`refs` and `impact`, not only direct JSX usage.

## Package identity

Go has a package clause, so the clause is the selector identity. Other
languages have no repository-wide package identity; the `packages` projection
reports the containing directory as the package name. `files`, `packages`,
`tests`, `symbols`, and `structure` all accept that same directory spelling.
The two representations are one rule, not two selectors that disagree.

## Entrypoint conventions

A Next.js route module is a *discovery*, never proof of execution:
`Verified=false`. App Router modules are `page`, `layout`, `route`, `template`,
`default`, `loading`, `error`, `global-error`, and `not-found`; the Pages Router
treats every source file under `pages/` (including `_app`, `_document`, and
`pages/api/*`) as a route module; root or `src/` `middleware` is included.
`internal/intelligence/ast.WebRouteModule` is the single owner of that list, so
the metadata inventory and the parsed adapter cannot disagree.

## Snapshot disclosure

Every `structure` result header states three things the model cannot infer from
symbols alone:

- `structural:` carries the four-state verdict and the decisive limitation, so
  a snapshot whose sources have no adapter in this build reads
  `UNSUPPORTED (no adapter for .ts, .tsx; 0 of 5 source files parsed)` instead
  of `PARTIAL`. `PARTIAL` means some supported source produced no facts;
  `UNPARSED` means parsing is still pending; `UNSUPPORTED` means no adapter can
  ever parse it.
- `adapters:` names the adapters compiled into this binary. It is the
  difference between "this project has no symbols" and "this binary cannot see
  symbols".
- `result:` names the slice a truncated view cut, as `files 10 of 14`. Slices a
  view displayed in full are omitted. `result_reason` names the decisive bound
  when more than one applied.

Declaration totals (`symbols`, `references`, `calls`, `tests`) are observations,
not inventory. An overview prints them only when the snapshot could have
observed them: unparsed source renders `0 (source not parsed)` and adapter-less
source renders `unobservable (no adapter)`. `test_files` is classified from paths
at inventory time, so it stays a number even when no adapter can parse the file.
That separation is what lets a model distinguish "the inventory lists no test
files" from "the project has no tests".

The same counts are available as machine fields: `truncated`, `reason`, and the
`bounds` list (`slice`/`shown`/`total`) in the overview details.

## Deliberate limits

- Tree-sitter adapters do not perform type resolution, overload resolution,
  macro expansion, or compiler-level interface implementation checks.
- A non-Go call is materialised only when its target is unique globally or is
  proved by the referring file's import. Receiver dispatch, function values,
  shadowing, and dynamic calls remain unknown. A named import that renames the
  binding (`import { Post as BlogPost }`) is not mapped back to its original
  export name.
- TypeScript/TSX value references are collected for JSX elements, type
  annotations, and `typeof X` queries. A bare value read in an expression is
  not collected, so a component used only as a value (`const C = Button`) is
  outside the reference projection.
- Test associations are static references/calls, never behavioral coverage.
- `graph` remains the Go package-import graph; use `structure`/`refs` for
  language-neutral relationships.
- The semantic activation level is reserved and fails explicitly. No language
  server, compiler, or network parser is launched.
- The generated grammar for C# is upstream-marked incomplete; partial syntax
  errors are reported in `facts.Issues` and keep the snapshot structurally
  incomplete.
