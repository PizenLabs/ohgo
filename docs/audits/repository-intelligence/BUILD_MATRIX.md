# Build and capability matrix

## Targets

| Target | Command | CGO | Artifact | Structural adapters |
|---|---|---|---:|---|
| Portable | `make build` | 0 | `./ohgo` | Go only |
| Multi-language | `make build-multilang` | 1 | `./ohgo-multilang` | Go + 10 Tree-sitter adapters |
| Release | `make release` / `scripts/release-target.sh` | 0 | `dist/ohgo-<version>-<os>-<arch>.tar.gz` | Go only |
| Docker | `docker build .` | 0 | distroless image | Go only |
| `go install` | default environment | host default | `$GOBIN/ohgo` | host default; not a pinned matrix entry |

Both local builds succeed with Go 1.27.4 and Apple clang 21.0.0. Final
artifacts: portable 22,377,890 bytes (`sha256
b7b925d1fbb5f7a2d3ed905aadcbc928e95d69360c1cb8251764e99e7b337568`),
multi-language 42,939,090 bytes (`sha256
f4f8e0fcd137258e6fb7fc3e4374f1636802d4c3c8c051838c0ba06f4600e52e`).
The version string is identical (`v0.1.2-9-gade60d9`), and the Go build
metadata does not expose CGO. Capability must be asked of the binary, not
inferred from the filename.

## Post-fix doctor output

Portable:

```text
ok  structural  1 adapter (go); portable build: other language adapters are unsupported
```

Multi-language:

```text
ok  structural  11 adapters (go, tree-sitter-c, tree-sitter-cpp, tree-sitter-csharp,
                tree-sitter-java, tree-sitter-javascript, tree-sitter-kotlin,
                tree-sitter-ruby, tree-sitter-rust, tree-sitter-tsx, tree-sitter-typescript)
```

Before this change doctor reported the same nine checks for both binaries and
no structural row at all.

## Adapter lifecycle verification

Each documented language was given a source file and parsed through the
multi-language binary's `ast.DefaultAdapters()` path.

| Language | Extensions tested | Adapter | Parsed | Symbols | Import/call/reference facts |
|---|---|---|---:|---:|---|
| Go | `.go` | native `go/ast` | yes | yes | package imports, references, calls, tests, entrypoints |
| TypeScript | `.ts`, `.mts`, `.cts` | `tree-sitter-typescript` | yes | yes | imports, calls, calls resolved by unique name |
| TSX | `.tsx` | `tree-sitter-tsx` | yes | yes | JSX component references, type references |
| JavaScript | `.js`, `.mjs`, `.cjs` | `tree-sitter-javascript` | yes | yes | imports, calls |
| JSX | `.jsx` | `tree-sitter-javascript` | yes | yes | imports, calls, JSX references |
| Java | `.java` | `tree-sitter-java` | yes | yes | package imports, calls, heritage, entrypoint |
| C# | `.cs` | `tree-sitter-csharp` | yes | yes | using imports, calls, heritage, entrypoint |
| Rust | `.rs` | `tree-sitter-rust` | yes | yes | use imports, calls, tests, entrypoint |
| Kotlin | `.kt`, `.kts` | `tree-sitter-kotlin` | yes | yes | imports, calls, heritage, entrypoint |
| Ruby | `.rb` | `tree-sitter-ruby` | yes | yes | require imports, calls |
| C | `.c`, `.h` | `tree-sitter-c` | yes | yes | includes, calls, entrypoint |
| C++ | `.cc`, `.cpp`, `.cxx`, `.hpp`, `.hh`, `.hxx` | `tree-sitter-cpp` | yes | yes | includes, calls, heritage, entrypoint |

The regression `TestLanguageAdaptersEmitCanonicalFacts` now also requires every
fixture to produce at least one call or reference site. A grammar that parses
but yields no structural relationships is not counted as a working adapter.

## Portable behavior

The portable binary on the blog reports:

```text
structural: UNSUPPORTED (no adapter for .mjs, .ts, .tsx; 0 of 77 source files parsed)
adapters: go
languages: JavaScript=UNSUPPORTED/2, TypeScript JSX=UNSUPPORTED/68, TypeScript=UNSUPPORTED/7
```

It still discovers the two Next.js route modules by filename convention
(`app/layout.tsx`, `app/page.tsx`) and marks them `verified: false`. It does not
claim symbols, references, calls, or tests; those are printed as unobservable.

## Release and update behavior

The release and Docker paths are intentionally portable. `ohgo update` now
refuses to replace a non-development multi-language binary with a portable
release artifact, because that would silently remove ten adapters while
reporting a successful version update. `ohgo update --check` remains allowed.

`make test` still runs the host default. The audit added
`make test-portable` and `make test-multilang`; both pass. `make bench` was
repaired after it was found pointing at the pre-`internal/` package paths.
