# Build and capability matrix

## Targets

| Target | Command | CGO | Artifact | Structural adapters |
|---|---|---|---:|---|
| Default | `make build` | host default | `./ohgo` | all compiled adapters (Go + 10 Tree-sitter with a C toolchain) |
| Portable | `make build-portable` | 0 | `./ohgo-portable` | Go only |
| Multi-language | `make build-multilang` | 1 | `./ohgo-multilang` | Go + 10 Tree-sitter adapters |
| Portable release | `scripts/release-target.sh` | 0 | `dist/ohgo-<version>-<os>-<arch>.tar.gz` | Go only |
| Multi-language release | `VARIANT=multilang scripts/release-target.sh` | 1 | `dist/ohgo-<version>-<os>-<arch>-multilang.tar.gz` | Go + 10 Tree-sitter adapters |
| Docker | `docker build .` | 0 | distroless image | Go only |
| `go install` | default environment | host default | `$GOBIN/ohgo` | host default; not a pinned matrix entry |

The Go build metadata does not expose CGO. Capability must be asked of the
binary (`ohgo doctor`), not inferred from the filename. The 2026-11-20 audit
measured the portable artifact at 22,377,890 bytes and the multi-language
artifact at 42,939,090 bytes; those sizes are historical and change with the
grammar and dependency revisions.

## Post-fix doctor output

Portable:

```text
ok  structural  1 adapter (go); portable build: TypeScript/TSX/JavaScript, Java, C#, Rust, Kotlin, Ruby, C, and C++ are not compiled in; rebuild with `make build` (needs a C toolchain)
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

The Docker path remains intentionally portable. The release publishes both
artifact sets; `install.sh` prefers the multi-language artifact and
`ohgo update` selects the artifact matching the running binary. A release that
lacks the matching multi-language artifact fails the update rather than
silently removing ten adapters. (The 2026-11-20 audit predates the
multi-language release artifact; its portable-only release decision was
superseded on 2026-11-22.)

`make test` still runs the host default. The audit added
`make test-portable` and `make test-multilang`; both pass. `make bench` was
repaired after it was found pointing at the pre-`internal/` package paths.
