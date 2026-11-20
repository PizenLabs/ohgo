# Audit plan and method

## Question

What does OHGO actually do between a model request and a repository-grounded
answer, where does that path fail on a real Go repository and a real
Next.js/TypeScript repository, and which failures are runtime defects rather
than model or provider limits?

## Method

1. Establish a reproducible baseline: commits, working trees, builds, adapter
   sets, and structural counts. No repository mutation.
2. Exercise the same seams the agent tools use, rather than reading code and
   assuming a capability. A temporary `cmd/auditprobe` harness called
   `ast.Index`, `structural.Analyzer`, `StructureTool`, `RefsTool`,
   `SymbolsTool`, and `investigate.Engine` directly and emitted JSON.
3. Compare every reported count and relationship with source. For the Go
   project, index both the project root (nested module) and `backend/` (module
   root) to isolate module-resolution effects.
4. Test the portable and multi-language binaries separately. Treat "no
   adapter" as a build property, not a parser-quality result.
5. Classify each failure as build, Structure Core, adapter, semantic
   resolution, runtime/tool, model, provider, or context/evidence. A model
   failure is not an OHGO bug unless the runtime misreported or lost evidence.
6. Fix confirmed defects at their owning layer and add a regression that fails
   on the old behavior.
7. Re-run the full test matrix in both CGO modes and re-run the affected
   repository scenarios.

## Baselines

| ID | Build | GRY | Target |
|---|---|---|---|
| A | portable (CGO=0) | disabled | both |
| B | multi-language (CGO=1) | disabled | both |
| C | multi-language | unavailable | both |
| D | multi-language | enabled where native insufficient | ecommerce |
| E | multi-language | native-first, external only on gap | both |

Baseline A on the blog is a capability result, not a parser benchmark: the
portable binary must report `UNSUPPORTED` for `.ts`, `.tsx`, and `.mjs`.

## Free-model protocol

Only hosted models with verified zero cost were eligible. Pricing was read from
the provider roster on the test date. Local inference (Ollama, LM Studio,
llama.cpp, local endpoints) was excluded. Every run recorded the exact model
reference, provider, repository commit, build, prompt, session id, tool calls,
tool results, answer, latency, and verdict.

The model suite is described in `MODEL_EVALUATION.md`; the runner writes JSON
per run and retains the raw session transcript.

## Safety

The benchmark used copies of both repositories under the session temp
directory. The copies preserve the recorded commits and source content but
exclude `node_modules`, `.next`, and any pre-existing `.ohgo` runtime state.
The originals were never reset, cleaned, committed, or pushed. The blog's
pre-existing `package-lock.json` modification was preserved in place.
