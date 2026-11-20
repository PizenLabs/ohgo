# Repository intelligence audit

Independent audit of OHGO's runtime path from model interaction to structural
evidence, run against a real Go ecommerce repository and a real Next.js /
TypeScript blog.

Start with `FINAL_REPORT.md`. The supporting documents are:

| Document | Contents |
|---|---|
| `ENVIRONMENT.md` | Host, toolchain, OHGO revision, target commits, runtime state |
| `AUDIT_PLAN.md` | Method, baselines, safety rules |
| `BUILD_MATRIX.md` | Portable/multi-language/release builds and adapter lifecycle |
| `STRUCTURE_CORE.md` | Go AST and shared Structure Core measurements and limits |
| `MULTILANGUAGE.md` | Tree-sitter adapters, alias/JSX/type resolution |
| `TEST_MATRIX.md` | Baseline A–E, scenarios, failure classification |
| `MODEL_EVALUATION.md` | Eligible free models, run matrix, per-model findings |
| `GRY_EVALUATION.md` | Native vs GRY, marginal gain, freshness, cost |
| `BUGS.md` | Confirmed defects with repro, root cause, fix, regression |
| `FIXES.md` | Implementation changes by owning layer |
| `REGRESSION_TESTS.md` | Contract defended by each new test |
| `FINAL_REPORT.md` | Consolidated answers and readiness assessment |

Status at publication: the fixes in `FIXES.md` are implemented and verified;
`OHGO-COMP-001`, `OHGO-CTRL-001`, and the limitations in the final report
remain open. No commit or release was made.
