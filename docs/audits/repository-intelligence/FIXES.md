# Fixes

Every fix below is at the layer that owned the defect. No parser, graph, or
evidence path was duplicated. The native Go AST backend remains the Go owner;
Tree-sitter remains a syntax adapter behind the shared `Adapter`/`FileFacts`
contract; GRY remains optional.

## Structure Core

### Nested module identity and import targets

Files:
`internal/intelligence/ast/index.go`,
`internal/intelligence/ast/graph.go`,
`internal/intelligence/ast/relationships.go`,
`internal/intelligence/ast/dependencies.go`

- `discoverModule` resolves a root, unique nested, or parent `go.mod` and
  records the absolute module root. Ambiguous sibling modules produce no
  module instead of an arbitrary one.
- `importTargetTable` builds `import path → repository directory` from indexed
  Go package directories relative to the absolute module root. It replaces
  root-relative string arithmetic.
- Package graph, symbol graph, qualifier table, and reference resolution use
  the table. A module-local import with no indexed target is unresolved; an
  import outside the module is external.
- `Dependencies.Complete` is false with `go_module_unresolved` when Go source
  exists without a resolved module.

### Module-level architecture fallback

Files:
`internal/intelligence/structural/strategies.go`

- Counts Go module nodes, not every top-level directory.
- If exactly one Go module and the module graph has no edges, builds the
  architecture graph from the module's direct children. This restores the
  directory-layer shape without pretending a single module has multiple
  deployables.
- Zero-edge + incomplete dependency evidence now yields `not-established`,
  never `independent modules`.

### Focus correctness

Files:
`internal/intelligence/ast/query.go`,
`internal/intelligence/ast/model.go`,
`internal/intelligence/structural/analyze.go`,
`internal/intelligence/structural/strategies.go`,
`internal/intelligence/investigate/providers.go`,
`internal/execution/tools/structure.go`,
`internal/execution/tools/structure_analysis.go`

- `ImpactResult.Callees` and `impactCalleesLocked` provide forward call edges.
- `FocusReport` exposes `Callees` and counts `References` from the reference
  list only, not the whole neighbourhood.
- `investigate` and `structure view=analysis` request
  `LevelRelationship` so the mandatory symbol-neighborhood strategy is
  eligible; the planner still picks cheapest for project questions.
- `writeFocusText` names callees.

### Inventory honesty

Files:
`internal/intelligence/ast/index.go`,
`internal/intelligence/ast/dependencies.go`,
`internal/intelligence/ast/query.go`,
`internal/execution/tools/structure.go`

- `.gohtml` and `.tmpl` are unsupported source extensions.
- Generated TS/JS imports into `.next`, `dist`, `build`, etc. use
  `ResolutionExcluded`; they do not make the dependency projection incomplete.
- Non-Go package importers are computed from resolved language import edges.
- Overview qualifies `packages`/`entrypoints` when source is unparsed.
- Tests view reports unique tests and associations separately.

## Multi-language adapters

Files:
`internal/intelligence/ast/jsconfig.go` (new),
`internal/intelligence/ast/generic.go`,
`internal/intelligence/ast/lang_web.go`,
`internal/intelligence/ast/language.go`

- Loads `tsconfig.json`/`jsconfig.json` JSONC, `baseUrl`, and `paths`.
- Resolves extensionless aliases and index files for TS/JS/TSX/JSX.
- Discloses unresolved aliases as local gaps.
- Collects JSX component elements and uppercase type identifiers as
  `rawGenericReference`s; skips intrinsics, declaration names, type
  parameters, and heritage duplicates.
- Adapter test now requires at least one call/reference fact per language.

## Investigation and GRY

Files:
`internal/intelligence/investigate/semantic.go`,
`internal/intelligence/investigate/router.go`,
`internal/intelligence/investigate/providers.go`,
`cmd/ohgo/build.go`,
`internal/intelligence/gry/observe.go`

- Semantic `workspace/symbol` uses `Focus`, and gopls starts at the module
  root while reported paths remain workspace-relative.
- Semantic references exclude the declaration.
- Router is native-first with an intent-specific `Sufficient` signal. External
  evidence is merged when native is insufficient; sources stay per fact/record.
- Stale external evidence downgrades the combined outcome.
- GRY trace includes test call sites; locate uses exact `--name-pattern` before
  ranked fallback.

## Protocol and diagnostics

Files:
`internal/provider/ai/normalize.go`,
`cmd/ohgo/doctor.go`,
`cmd/ohgo/update.go`,
`Makefile`

- Registered `<ernie:tool_call>` XML invoke envelope.
- Doctor reports the compiled structural adapter set.
- Multi-language builds refuse a self-update that would install the portable
  release (unless it is a development build, which update already refuses).
- `make bench` targets existing packages; `make clean` removes both binaries;
  `make test-portable` / `make test-multilang` expose both build modes.

## Deliberately not changed

- Go methods are not given synthetic reference edges; callers/callees remain
  the correct query for methods.
- `go/types` and interface satisfaction were not added.
- No TypeScript language server was added.
- No MDX parser was added; the blog content is Markdown and the loader is
  source.
- GRY was not made mandatory or a startup dependency.
- The workspace classifier was not widened for the leading “Using …”
  formulation; that is documented as OHGO-CTRL-001.
