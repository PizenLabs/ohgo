# Multi-language adapters

## What is compiled

`DefaultAdapters()` returns the native Go backend plus, under `CGO_ENABLED=1`,
these registered adapters:

| Adapter | Language identity | Extensions |
|---|---|---|
| `tree-sitter-typescript` | `typescript` | `.ts`, `.mts`, `.cts` |
| `tree-sitter-tsx` | `tsx` | `.tsx` |
| `tree-sitter-javascript` | `javascript`, `jsx` | `.js`, `.mjs`, `.cjs`, `.jsx` |
| `tree-sitter-java` | `java` | `.java` |
| `tree-sitter-csharp` | `csharp` | `.cs` |
| `tree-sitter-rust` | `rust` | `.rs` |
| `tree-sitter-kotlin` | `kotlin` | `.kt`, `.kts` |
| `tree-sitter-ruby` | `ruby` | `.rb` |
| `tree-sitter-c` | `c` | `.c`, `.h` |
| `tree-sitter-cpp` | `cpp` | `.cc`, `.cpp`, `.cxx`, `.hpp`, `.hh`, `.hxx` |

The runtime verification is in `BUILD_MATRIX.md`. Every adapter parsed a
representative fixture and produced declarations and at least one call or
reference site.

## Detection and failure states

The extension registry is the single path from a suffix to a language. Two
adapters claiming the same extension fail at construction; there is no
arbitrary parser selection.

- Portable build: the language is known, but no adapter is registered. The
  snapshot reports the language as `UNSUPPORTED` and lists the extension.
- Registered adapter, unparsed: `UNPARSED` / `pending`.
- Registered adapter, parse failure: `FAILED`; the file remains in inventory.
- Registered adapter, recoverable tree error: `PARTIAL` with bounded issues.
- Generated output referenced by authored source: `EXCLUDED` when the target
  is a known build-output directory.
- No registry entry (`.sql`, `.gohtml`, `.tmpl`, `.py`, …): reported under
  `unsupported_languages`, and structural completeness stays false.

These states are never collapsed into "no symbols".

## TypeScript / TSX

Verified on the blog:

- `.ts`: 7 files parsed, 93 symbols.
- `.tsx`: 68 files parsed, 434 symbols.
- `.mjs`: 2 files parsed, 2 symbols.
- Total: 529 symbols, 817 references, 1,035 call sites.

Alias resolution reads `tsconfig.json` (`compilerOptions.baseUrl`,
`compilerOptions.paths`) and `jsconfig.json`, accepts JSONC comments and
trailing commas, and tries `.ts`, `.tsx`, `.js`, `.jsx`, `.mjs`, `.cjs`,
`.mts`, `.cts`, and `index` files. A `@/*` import that resolves into the repo
produces a dependency edge; one that names a missing file is disclosed as an
unresolved local import.

JSX element names and type identifiers are references when they are
component/type-shaped (uppercase first rune). Intrinsic elements and type
parameters are skipped. This is what makes `page -> TerminalBlog` and
`Post` usage visible.

## Java, C#, Rust, Kotlin, Ruby, C, C++

- Declarations and calls are syntax-derived.
- Java package imports map to `com/example/File.java`; a standard Maven
  `src/main/java/...` root is not specially folded in.
- C# namespace imports map to a matching file; namespace-to-file resolution is
  convention-based, not compiler-based.
- Rust `use` resolution understands local `crate::`, `super::`, `self::` and
  relative paths; external crates remain external.
- Kotlin package imports map to a matching `.kt` file.
- Ruby `require_relative` and file-name require resolution are supported;
  gem requires remain external.
- C/C++ quoted includes resolve relative to the source and `.h`/`.hpp`
  extensions. Angle-bracket includes are external.

These adapters are syntax adapters. They do not claim type resolution,
overload resolution, macro expansion, or compiler-level interface checks.

## What changed in this audit

1. TypeScript path aliases now resolve (OHGO-ML-001).
2. JSX component usage becomes a reference (OHGO-ML-002).
3. Type identifiers become references (OHGO-ML-003).
4. Non-Go package importers are computed (OHGO-ML-004).
5. Generated output imports are excluded rather than unresolved (OHGO-ML-005).
6. Every adapter fixture must emit a call/reference fact in tests.

## What remains

- No general LSP semantics for non-Go languages. `investigate` starts gopls
  only for Go when installed; TypeScript has no language server in this build.
- No MDX parser. The blog's Markdown content is inventoried as documentation;
  `lib/post-loader.ts` is the implemented loader.
- `.h` is claimed by C, not C++; a C++ header using only `.h` is parsed with
  the C grammar unless another extension selects C++.
