# Structure Core and Go AST audit

All counts below were produced from the current target commits with the
multi-language binary. "Before" is the audit baseline; "After" includes the
fixes in `FIXES.md`.

## Go ecommerce — project root

| Metric | Before | After |
|---|---:|---:|
| Indexed files | 547 | 547 |
| Supported source files | 305 | 305 |
| Parsed source files | 305 | 305 |
| Go packages | 80 | 80 |
| Symbols | 3,440 | 3,440 |
| References | 4,801 | **9,090** |
| Calls | 1,035 | **2,412** |
| Tests | 461 | 461 |
| Test files | 110 | 110 |
| Entrypoints | 2 | 2 |
| Module | *(empty)* | `github.com/bkielbasa/go-ecommerce/backend` |
| Dependencies complete | true | false (unsupported templates/SQL) |
| Dependency edges | 0 | **234** |
| Architecture | independent modules (0 edges) | **layered (234 edges)** |
| Structural completeness | PARTIAL | PARTIAL (`.gohtml`, `.sql`, `.tmpl`) |

The after numbers are identical to indexing `…/ecommerce/backend` directly:
9,090 references, 2,412 calls, 234 dependency edges, 80 packages. That is the
control that isolates the nested-module defect: same source, same parser, only
the module root differs.

### What Go AST actually provides

- Declarations: functions, methods, types, vars, consts, packages.
- Receiver and method coordinates via `Receiver.Method`.
- Package graph from module-local imports.
- Resolved references and direct calls for package-scope declarations.
- Tests by convention and test declarations.
- Entrypoints: a non-test `main` in package `main`.

### Verified ground truth

`NewOrder` in `backend/checkout/domain/order.go:99` has 13 direct call sites:
1 in `backend/integration/order_emails_test.go`, 8 in `layout/http_*_test.go`,
plus 4 mock/e2e helper methods. After the fix `refs NewOrder` returns exactly
13 references and 13 caller edges; `tests NewOrder` returns 4 unique tests and
4 associations; `impact` reports 7 package importers.

`NewOrder` is not called from production code: the production path uses
`NewOrderFactory().FromCart`. The tool reports that truth rather than
confusing the factory with the constructor.

### Go limitations that remain

| Limitation | Evidence | Consequence |
|---|---|---|
| Method references unsupported | `References` returns `Unsupported` for a Go method target | Use `callers` for method dispatch; a method-as-value reference is not enumerated |
| Interface implementation not resolved | `IMPLEMENTS` is documented as future/`go/types` work | An interface's implementors are unknown |
| Dynamic dispatch | `impact NewOrder` reports 10,986 static resolution gaps in the repository (`method_dispatch` dominant) | Caller counts are direct static edges, not runtime reachability |
| Dot imports | disclosed as `DotImportFiles` | A dot-imported exported name may capture an unqualified reference |
| Multi-module workspaces | `discoverModule` accepts one unambiguous module; sibling modules yield no module | Imports across sibling modules are unresolved; the run must report that state |
| Subdirectory walk inside a module | target table maps only packages inside the walk root | Imports to sibling packages outside the root are unresolved |

These are boundaries of a syntax-level Go index, not a claim that the compiler
cannot answer them. `go/types` is not used and not silently emulated.

## Next.js blog

| Metric | Before | After |
|---|---:|---:|
| Indexed files | 113 | 113 |
| Supported source files | 77 | 77 |
| Parsed source files | 77 | 77 |
| Symbols | 529 | 529 |
| References | 0 | **817** |
| Calls | 1,035 | 1,035 |
| Dependency nodes / edges | 25 / 0 | 25 / **6** |
| Unresolved dependencies | 1 (`.next` generated import) | 0 |
| Entrypoints | 2 discovered | 2 discovered |
| Tests | 0 | 0 |
| Architecture | independent modules / not established | **mixed** (6 edges, one real hook/UI cycle) |
| Structural completeness | COMPLETE | COMPLETE |

### Next.js relationships now observed

```text
app -> components/blog
app -> lib
components/blog -> lib
components/ui -> hooks
components/ui -> lib
hooks -> components/ui
```

`app/page.tsx` imports `@/components/blog/TerminalBlog` and
`@/lib/post-loader`; both aliases now resolve. The page renders
`<TerminalBlog posts={posts} />` at line 7, so `impact TerminalBlog` reports
1 reference, 1 importer, and `relations` returns a `REFERENCES` edge from
`file:app/page.tsx` to the component symbol.

### Non-Go package counts

`components/blog` now reports `importers: 1` instead of the fixed zero. The
count comes from resolved language import edges, not from a second graph.

### What Tree-sitter does not provide

- Type resolution and overload resolution.
- Compiler-level interface implementation checks.
- Accurate receiver types for method dispatch.
- Macro expansion.
- A guarantee that a name resolved by unique global name is the runtime
  binding. The graph marks the resolution `RESOLVED` only when the name is
  unique; otherwise it is `AMBIGUOUS` and emits no edge.

The blog has no test files. The structure tool reports `test_files: 0` from
path classification and `tests: 0` only after parsing. It does not claim test
coverage.

## Snapshot and evidence behavior

- `SnapshotInfo` separates freshness (`CURRENT`), scope, inventory, structural
  and result completeness. A current snapshot can still be structurally
  partial.
- The ecommerce root has two excluded roots (`backend/seeds`,
  `data/uploads/seeds`) and reports `scope: PARTIAL`; a repository-wide
  absence claim is therefore qualified.
- The ecommerce snapshot now reports `.gohtml(57)`, `.sql(77)`, `.tmpl(16)` as
  unsupported source. Before, templates were invisible to the completeness
  calculation.
- Incremental behavior: a normal one-file edit parses one file and re-resolves
  retained syntax; the benchmark records one parsed file for a single-file
  update. No duplicate parser or second graph exists.

## Residual ambiguities

`GenericUnresolved=1297` and `GenericAmbiguous=61` on the blog are not errors:
they are the disclosed count of names a syntax-only adapter could not resolve
uniquely. `Complete` is false for reference answers that rely on those
unresolved names. This is a limitation of syntax-level analysis, not a parser
failure.
