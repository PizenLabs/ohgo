# Visualization Engine

> **Canonical for:** chart data ownership, deterministic planning, terminal
> rendering, runtime telemetry integration, evidence provenance, the
> `render_chart` capability, and chart failure behaviour.
> **Topology:** [`OHGO_ARCHITECTURE_MAP.md`](OHGO_ARCHITECTURE_MAP.md) owns the
> package graph and domain manifest. This document does not restate it.

## Principle

Visualization is a deterministic projection of structured truth, not model
output. The chain is fixed:

```
Dataset + Request        what the runtime measured and what was asked for
        ↓
Plan                     deterministic presentation decision
        ↓
ChartSpec                validated structured data
        ↓
Renderer                 semantic text lines, no terminal state
        ↓
TUI / benchmark          product palette, viewport, scrollback
```

The model may name a registered source and a form. It may not supply values,
units, geometry, colors, provenance, or conclusions. A rendered chart is never
fed back into the model context; the model receives a one-line summary.

## Ownership

| Concern | Owner |
|---|---|
| Chart types, validation, planner, renderers | `internal/presentation/chart` |
| Product palette, transcript block, viewport, resize, live mode | `internal/tui` |
| Measured session token telemetry | `agent.UsageLog` |
| Benchmark measurements | the benchmark command that measured them |
| Chart request validation and capability tier | `internal/execution/tools` (`render_chart`) |
| Source registration and adapter to runtime data | `cmd/ohgo` composition |

`internal/presentation/chart` has no terminal I/O, terminal-size probe, ANSI
escape emission, model/provider dependency, or authority. It imports the
existing `x/ansi` stripper and `go-runewidth` for measurement only. Renderers
return `Line`/`Span` values with semantic `Role`s; the TUI maps those roles onto
`design.Tokens` through `Palette`. There is no second style system.

## Request, Plan, Spec

- **`ChartRequest`** (`tools.ChartRequest`) is the model-facing request:
  `source`, optional `kind`, optional `series`.
- **`PlanInput`** (`chart.PlanInput`) is the re-plannable payload the host
  hands to the UI: the `Dataset` and the request. It is not a rendered chart,
  so a resize re-plans the same truth at the new width.
- **`Plan`** records the decision: kind, whether the exact table is included,
  panels, and caveat notes.
- **`ChartSpec`** is the validated input to a renderer: strongly typed series,
  points, units, aggregation, panels, scales, width, notes, finding, and
  provenance. No `map[string]any` carries chart data.

`Build(input, width)` normalizes, validates, plans, and returns both `Plan` and
`ChartSpec`. A structural violation (unknown kind, duplicate series, misaligned
columns, non-finite value, declared log scale with a non-positive value) is an
error. Insufficient data is not an error: the exact table is a valid answer and
carries a note explaining what was omitted.

## Data model

- A `Value` is either measured or explicitly missing. Missing is never zero.
- A `Series` owns one `Unit` and one `Aggregation`; both survive to the
  renderer. A chart never computes a different aggregation.
- `Point.Note` carries a qualifier such as `capped`, rendered beside the exact
  value. A capped benchmark cannot look complete.
- `Point.Interval` preserves measured uncertainty for a future range renderer;
  it is not synthesized from two unrelated columns.
- Units: counts/files/tokens, bytes, milliseconds, microseconds, seconds,
  percent, ratio, USD, and none. Duration formatting may promote within its
  family (`1290 ms` → `1.29 s`) but never changes family.
- `Provenance` copies an evidence/runtime coordinate (`kind`, `id`, `source`,
  `state`, `freshness`, `ref`, `note`) verbatim. It never promotes a state.

## Deterministic planner

The planner has no randomness, model dependency, or clock. Rules, in order:

1. Width ≤ 0 uses `DefaultWidth` only for a caller with no measured terminal.
   Width < 40 renders the exact table and a note; a chart cannot fit a y-axis,
   plot, and x label without overlap below that budget.
2. No measured values → table only, with an explicit note.
3. Requested `table` → table only.
4. Requested `change` → requires exactly two series with one unit; otherwise
   table with a note. The change grid is its own exact table, so no raw table
   is repeated above it.
5. Requested `bar` → requires one series after selection; otherwise table with
   a note.
6. Requested `line` → requires an ordered dimension with at least two points;
   otherwise table with a note.
7. Automatic/`panels`:
   - Group series by unit first. Incompatible units are never placed on one
     axis.
   - Categorical data: one series becomes a bar panel; multiple series sharing
     a unit stay a table because they share no ordered axis.
   - Ordered data: series are ranked by maximum absolute magnitude. A drop of
     ≥10× starts an independent panel, so a query-only series two orders below
     cold latency is not flattened to the baseline.
   - At most 4 series share a panel; more are chunked into panels.
   - At most 3 panels are rendered. More independent scales render the exact
     table with a note instead of hiding data.
8. The exact table is included by default (`OmitTable` must be explicit), and
   is forced whenever the chart is omitted or the form is `table`.

Small multiples are produced by the same rules, not by a second layout path.

## Scale behavior

- Linear value axes include zero. A truncated baseline cannot imply a false
  comparison.
- Log value axes are used only when requested (or every series declares log)
  and every rendered value is positive. Zero or a negative value falls back to
  linear with a visible `log scale unavailable` note. A declared log scale with
  a non-positive value is a validation error.
- The x axis uses numeric positions when supplied. Log x is chosen only when
  every position is positive and the dimension spans ≥100×; otherwise linear.
  An ordinal dimension is evenly spaced.
- Tick steps are 1/2/5×10ⁿ. Panels own independent nice domains.
- Negative values are included; bars with both signs use a zero baseline and
  one shared scale. The shorter side caps the scale so no magnitude is silently
  clipped.

## Renderers

- **Table** — authoritative exact values, unit-aware, right-aligned numeric
  columns, notes attached to the measured cell, stable ordering. Labels shrink
  first; numeric columns never truncate. A numeric row label is data, so it is
  never shortened into a different number: the table falls back to the stacked
  `header` / exact value form when the grid cannot hold it.
- **Bar** — horizontal categorical comparison with Unicode block fractions.
  Missing values render no bar and `—`. Negative values use a centered
  baseline.
- **Line** — single series uses Braille dots for resolution. Multiple series
  use cell markers, a per-series digit at every measured point, and a legend,
  so series identity survives monochrome. A missing value breaks the line.
- **Panels** — each panel has its own title, scale, and chart; the table names
  every series once.
- **Change** — before/after values, absolute delta, and percentage delta when
  the baseline is nonzero. Direction is not colored success/failure: whether
  lower is better is domain knowledge the renderer does not own.
- **Finding** — a supplied `Finding` is labeled `Key finding:` and otherwise
  preserved. The renderer never generates one.

Every rendered line fits `ChartSpec.Width` for a real data-width budget. Charts
are plain Unicode plus semantic spans; no ANSI escape sequence is produced or
forwarded. `ExactText` is the copy/machine representation and never truncates.

## Machine-readable output

The reusable layer is the metric/evidence data, not a rendered chart.
`Dataset`, `PlanInput`, `ChartSpec`, and `Plan` carry JSON tags and contain no
maps or untyped payloads, so a host that already emits structured metrics can
serialize the same data without the renderer. `/chart <path>` is the one
user-facing dataset entry point: it reads a strict, size-bounded `chart.Dataset`
JSON file and renders it as an ordinary TUI block. It is a human command, not a
model capability; `render_chart` still accepts only registered source names.
`ExactText` remains the canonical plain-text form used by copy and benchmark
output. A future `ohgo benchmark --json` should serialize the metric dataset,
not a rendered chart.

Production sources are registered by the host. Today the only registered
source is `session_usage`; an evidence source must come from an owner that can
produce a `Dataset`, with its evidence coordinates copied into `Provenance`.
A user-supplied file is labeled `ProvenanceFile`, never promoted to runtime
telemetry or evidence. An unknown source is an error, never an empty chart.

## TUI integration

- `EntryChart` is an ordinary owned transcript block with a stable `BlockID`.
  It participates in the existing `TranscriptLayout`, viewport, scrolling,
  selection, and live mode. There is no second viewport, scroll model, or mouse
  path.
- `/chart` builds a `Dataset` from measured runtime state (`UsageLog` totals and
  transcript tool calls) and applies `ChartShown`. It costs zero model tokens.
- `/chart <path>` reads a strict `chart.Dataset` JSON file selected by the human,
  caps it at 1 MiB, and applies the same `ChartShown` path. A malformed,
  oversized, unknown-field, or trailing-content file becomes a notice; no chart
  is fabricated. A relative path resolves against the workspace root.
- A chart is re-planned at the current `textWidth(layout)` on resize/reflow.
  The chart engine never receives a stored terminal size.
- Chart blocks do not use the generic entrance fade: that fade repaints every
  row in one color and would erase series identity. They settle immediately.
- `render_chart` tool details are UI-only (`agent.Result.Details`). The TUI
  stores the `PlanInput`, expands the completed call, and renders it. The model
  sees only the summary string, e.g.
  `chart rendered: kind=bar series=1 points=4 panels=1 table=yes`.
- Charts are ephemeral projections. The exact table is available through
  `/copy` while the block exists; resuming a session does not replay a chart
  payload because the payload was never model context. The source telemetry
  remains authoritative and `/chart` regenerates the view.

## Agent capability

`render_chart` is a `TierRead`/`ActionObserve` capability. Its schema accepts
only `source`, `kind`, and `series`. The host validates the source name and
returns an error for an unknown source, an unmeasured ledger, an unknown form,
or a source with no measured data. It never fabricates a value or a baseline.

The capability is registered only when the session owns a measured
`agent.UsageLog`; a tool that could not produce a truthful chart is not
advertised. The execution plane depends on the `tools.ChartProvider` interface,
not on the presentation package; `cmd/ohgo` supplies the adapter.

## Failure behavior

A chart failure is a presentation notice, never a turn failure:

- structured data remains available;
- the exact table remains the representation on narrow widths or planner
  refusal;
- a renderer/planner error produces a warning line in place of the chart;
- runtime state, completion, authority, budgets, and persistence are unchanged.

Charts cannot execute, mutate, authorize, complete, or bypass a budget.

## Extension rules

A new chart kind requires: a concrete OHGO use case, a data shape in
`ChartSpec`, one planner rule, one renderer, deterministic width tests, and a
fallback. A new source requires a provider adapter and a provenance kind; it
must not be a second telemetry store. A new terminal renderer consumes the same
`ChartSpec` and must not change the planner or the data model.

## Verification

```sh
go test ./internal/presentation/chart
go test ./internal/tui -run 'ChartFile|ChartCommand|ChartEntry'
go test ./cmd/ohgo -run Chart
go test ./internal/presentation/chart -bench . -benchmem
```

The TUI end-to-end test carries the exact acceptance dataset through
`/chart <path>`, `ChartShown`, the one `TranscriptLayout`, and `Render`, and
asserts the table, four series, independent query-only panel, units, file
provenance, and line widths at 120/90/60/50. The chart adapter is additionally
exercised at 39/30/20 cells because the full TUI chrome refuses frames below
its own minimum.

Measured on an Apple M1 Pro (Go 1.27, `-benchtime=200ms`): build ≈12µs/op,
table render ≈14µs/op, bar ≈7µs/op, single-series Braille line ≈22µs/op,
three-panel benchmark render ≈70µs/op. Scaling checks (`-benchtime=100ms`):
4 series × 4 points build/render ≈8µs/33µs, 4 × 200 ≈257µs/462µs,
4 × 2000 ≈2.8ms/4.6ms, 50 × 50 ≈1.0ms/1.7ms. Build time is flat across
40–200 cell widths. These are measurements of this engine, not claims about
other machines. Reprofile before treating them as a budget.
