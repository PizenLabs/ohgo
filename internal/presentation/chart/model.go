// Package chart is OHGO's deterministic terminal visualization engine.
//
// The package is deliberately split into three responsibilities:
//
//	Dataset + Request  — what the runtime measured and what was asked for
//	Plan               — the deterministic decision about presentation
//	ChartSpec          — the validated data a renderer is allowed to draw
//
// A renderer may reflow a ChartSpec, but it may never invent a value, upgrade
// provenance, or decide that a conclusion exists. The model is presentation-
// owned: it has no terminal I/O, no model/provider dependency, and no
// knowledge of the TUI's viewport or style system. Renderers return semantic
// spans so the caller applies the product palette exactly once.
package chart

import "errors"

// Kind names one presentation form.
type Kind string

// Chart kinds.
const (
	// KindTable is the authoritative exact-data form. It is always usable
	// without a graphical representation.
	KindTable Kind = "table"
	// KindBar is a horizontal categorical comparison.
	KindBar Kind = "bar"
	// KindLine is a multi-series chart over an ordered dimension.
	KindLine Kind = "line"
	// KindPanels is a small-multiple layout. Each panel owns an independent
	// Y scale; panels are never forced onto one misleading axis.
	KindPanels Kind = "panels"
	// KindChange is a before/after comparison. It is the P1 form justified by
	// benchmark and regression data; the change grid is its exact table.
	KindChange Kind = "change"
)

// Valid reports whether the kind is one this engine knows how to render.
func (k Kind) Valid() bool {
	switch k {
	case KindTable, KindBar, KindLine, KindPanels, KindChange:
		return true
	}
	return false
}

// ScaleKind names an axis mapping.
type ScaleKind string

// Axis scales.
const (
	// ScaleLinear maps values proportionally and always includes zero on a
	// value axis, so a truncated baseline cannot imply a false comparison.
	ScaleLinear ScaleKind = "linear"
	// ScaleLog maps positive values logarithmically. Zero and negative values
	// are rejected with an explicit error, never silently clamped.
	ScaleLog ScaleKind = "log"
)

// Valid reports whether the scale is recognized.
func (s ScaleKind) Valid() bool { return s == ScaleLinear || s == ScaleLog }

// Unit is the measurement unit carried by a series. It survives every layer:
// a value that reached the renderer as milliseconds is never silently printed
// as a bare float.
type Unit string

// Units.
const (
	UnitNone         Unit = ""
	UnitCount        Unit = "count"
	UnitFiles        Unit = "files"
	UnitTokens       Unit = "tokens"
	UnitBytes        Unit = "bytes"
	UnitMilliseconds Unit = "ms"
	UnitMicroseconds Unit = "us"
	UnitSeconds      Unit = "s"
	UnitPercent      Unit = "%"
	UnitRatio        Unit = "ratio"
	UnitUSD          Unit = "usd"
)

// Valid reports whether the unit is recognized. An unknown unit is a
// programming error at a data boundary; rendering it as a bare number would
// lose the semantics the unit exists to preserve.
func (u Unit) Valid() bool {
	switch u {
	case UnitNone, UnitCount, UnitFiles, UnitTokens, UnitBytes,
		UnitMilliseconds, UnitMicroseconds, UnitSeconds, UnitPercent,
		UnitRatio, UnitUSD:
		return true
	}
	return false
}

// Aggregation names the statistic a series already applied. It is metadata,
// not a transformation: the renderer never computes a different aggregation.
type Aggregation string

// Aggregations.
const (
	AggNone Aggregation = ""
	AggRaw  Aggregation = "raw"
	AggSum  Aggregation = "sum"
	AggMean Aggregation = "mean"
	AggP50  Aggregation = "p50"
	AggP95  Aggregation = "p95"
	AggMax  Aggregation = "max"
	AggMin  Aggregation = "min"
)

// Valid reports whether the aggregation is recognized.
func (a Aggregation) Valid() bool {
	switch a {
	case AggNone, AggRaw, AggSum, AggMean, AggP50, AggP95, AggMax, AggMin:
		return true
	}
	return false
}

// ProvenanceKind separates runtime telemetry from externally derived evidence.
// It never upgrades one into the other.
type ProvenanceKind string

// Provenance kinds.
const (
	ProvenanceRuntime   ProvenanceKind = "runtime_telemetry"
	ProvenanceBenchmark ProvenanceKind = "benchmark"
	ProvenanceEvidence  ProvenanceKind = "evidence"
	// ProvenanceFile marks a user-supplied structured dataset. It is neither
	// runtime telemetry nor evidence owned by an analysis subsystem, so it
	// must not borrow either label.
	ProvenanceFile ProvenanceKind = "file"
)

// Provenance is a projection of the source coordinates behind a value. It is
// not a second evidence model: Kind, Source, State, and Freshness are copied
// verbatim from the owning subsystem, and nothing here may promote them.
type Provenance struct {
	Kind      ProvenanceKind `json:"kind,omitempty"`
	ID        string         `json:"id,omitempty"`
	Source    string         `json:"source,omitempty"`
	State     string         `json:"state,omitempty"`
	Freshness string         `json:"freshness,omitempty"`
	Ref       string         `json:"ref,omitempty"`
	Note      string         `json:"note,omitempty"`
}

// Empty reports whether there is nothing to render.
func (p Provenance) Empty() bool {
	return p.Kind == "" && p.ID == "" && p.Source == "" && p.Note == ""
}

// Value is one measured number or an explicit absence.
//
// Present=false is the only representation of missing data. A zero value is
// a measurement; a missing value is not.
type Value struct {
	Number  float64 `json:"number"`
	Present bool    `json:"present"`
}

// Measured returns a present value.
func Measured(number float64) Value { return Value{Number: number, Present: true} }

// Missing returns an explicitly absent value.
func Missing() Value { return Value{} }

// Interval is a measured low/high range, e.g. a p50/p95 benchmark interval.
// It is retained so a future range renderer cannot reconstruct uncertainty
// from two unrelated columns.
type Interval struct {
	Low     float64 `json:"low"`
	High    float64 `json:"high"`
	Present bool    `json:"present"`
}

// Contains reports whether the interval brackets value.
func (i Interval) Contains(value float64) bool {
	return i.Present && value >= i.Low && value <= i.High
}

// Point is one observation on a series.
type Point struct {
	// Label is the category or ordered-dimension label, e.g. "20,000".
	Label string `json:"label"`
	// X is the numeric position when the dimension has one. HasX separates a
	// real zero from "no numeric position was supplied".
	X    float64 `json:"x,omitempty"`
	HasX bool    `json:"has_x,omitempty"`
	// Value is the measurement. Missing values are never replaced by zero.
	Value Value `json:"value"`
	// Interval is optional measured uncertainty. It is data, not decoration.
	Interval Interval `json:"interval,omitempty"`
	// Note is a bounded qualifier such as "capped". It is rendered next to
	// the value so a truncated benchmark cannot look complete.
	Note string `json:"note,omitempty"`
	// Provenance names this point's source when it differs from the series.
	Provenance Provenance `json:"provenance,omitempty"`
}

// Series is one named measurement channel.
type Series struct {
	Name        string      `json:"name"`
	Unit        Unit        `json:"unit,omitempty"`
	Aggregation Aggregation `json:"aggregation,omitempty"`
	Points      []Point     `json:"points"`
	Provenance  Provenance  `json:"provenance,omitempty"`
	// Scale optionally declares the series' natural scale. The planner still
	// validates it against the data; a declared log scale with a non-positive
	// value is an error rather than a silent fallback in the spec.
	Scale ScaleKind `json:"scale,omitempty"`
}

// Dataset is structured truth supplied by a runtime or evidence owner.
//
// Ordered marks a dimension whose order is meaningful (file count, turn,
// time). Categorical data has no order and therefore cannot become a line.
type Dataset struct {
	Title      string     `json:"title,omitempty"`
	XLabel     string     `json:"x_label,omitempty"`
	XUnit      Unit       `json:"x_unit,omitempty"`
	Ordered    bool       `json:"ordered,omitempty"`
	Series     []Series   `json:"series"`
	Provenance Provenance `json:"provenance,omitempty"`
	// Note is a bounded caveat attached to the whole dataset.
	Note string `json:"note,omitempty"`
	// Finding is interpretation supplied by an evidence/analysis layer. The
	// renderer preserves and labels it; it never generates one.
	Finding string `json:"finding,omitempty"`
}

// Request is a request for a visualization. It is not a spec: the planner
// decides whether the requested form is truthful for the data and width.
type Request struct {
	// Source names the registered dataset. Providers use it to reject an
	// unknown source rather than guessing one.
	Source string `json:"source,omitempty"`
	// Kind is an optional requested form. Empty means deterministic auto.
	Kind Kind `json:"kind,omitempty"`
	// Series selects named series. Empty selects every series.
	Series []string `json:"series,omitempty"`
	// LogY requests a logarithmic value axis. It is honored only when every
	// rendered value is positive; otherwise the planner records the fallback.
	LogY bool `json:"log_y,omitempty"`
	// OmitTable suppresses the exact table. The default includes it: the table
	// is authoritative and a chart is derived.
	OmitTable bool `json:"omit_table,omitempty"`
	// Compact asks for the most compressed truthful representation. The
	// planner still enforces minimum widths derived from the data.
	Compact bool `json:"compact,omitempty"`
}

// Panel is one independent visual surface inside a spec.
type Panel struct {
	Title  string    `json:"title"`
	Kind   Kind      `json:"kind"`
	Series []int     `json:"series"`
	XScale ScaleKind `json:"x_scale"`
	YScale ScaleKind `json:"y_scale"`
	// ScaleNote explains a scale decision the reader must know, e.g. "log
	// scale unavailable: a zero value is present".
	ScaleNote string `json:"scale_note,omitempty"`
}

// ChartSpec is the validated presentation input. It contains no map and no
// untyped payload: every value that affects interpretation is explicit.
type ChartSpec struct {
	Title  string   `json:"title,omitempty"`
	Kind   Kind     `json:"kind"`
	XLabel string   `json:"x_label,omitempty"`
	XUnit  Unit     `json:"x_unit,omitempty"`
	Series []Series `json:"series"`
	Panels []Panel  `json:"panels,omitempty"`
	// Width is the cell budget the spec was validated against.
	Width int `json:"width"`
	// IncludeTable reports that the exact table precedes the chart.
	IncludeTable bool `json:"include_table"`
	// Notes are bounded caveats, e.g. missing values or an omitted chart.
	Notes []string `json:"notes,omitempty"`
	// Finding is interpretation supplied by the data owner, never the
	// renderer.
	Finding string `json:"finding,omitempty"`
	// Provenance is the dataset-level source coordinate.
	Provenance Provenance `json:"provenance,omitempty"`
}

// Plan is the deterministic presentation decision. It is separate from the
// spec so a caller can report or test the decision without rendering.
type Plan struct {
	Kind         Kind     `json:"kind"`
	IncludeTable bool     `json:"include_table"`
	Panels       []Panel  `json:"panels,omitempty"`
	Notes        []string `json:"notes,omitempty"`
}

// PlanInput is the complete input needed to reproduce a plan and spec. It is
// the only payload a capability may hand to the UI: the UI re-plans at its
// actual width instead of trusting a chart rendered for another terminal.
type PlanInput struct {
	Data    Dataset `json:"data"`
	Request Request `json:"request,omitempty"`
}

// Summary is the compact semantic acknowledgement returned to a model or log.
// It deliberately contains no rendered chart text.
type Summary struct {
	Kind    Kind `json:"kind"`
	Series  int  `json:"series"`
	Points  int  `json:"points"`
	Panels  int  `json:"panels"`
	Table   bool `json:"table"`
	Missing int  `json:"missing,omitempty"`
}

// Errors returned by the engine.
var (
	// ErrNoData reports a dataset with no series to present.
	ErrNoData = errors.New("chart: no data")
	// ErrNoValues reports a dataset whose selected series have no measured
	// points. The truthful rendering is an exact table with an explicit note,
	// so this is returned only when even that is impossible.
	ErrNoValues = errors.New("chart: no measured values")
	// ErrDuplicateSeries reports two series with the same name. Selection by
	// name would be ambiguous.
	ErrDuplicateSeries = errors.New("chart: duplicate series name")
	// ErrUnknownSeries reports a request naming a series the dataset lacks.
	ErrUnknownSeries = errors.New("chart: unknown series")
	// ErrUnknownKind reports an unknown chart kind.
	ErrUnknownKind = errors.New("chart: unknown kind")
	// ErrUnknownScale reports an unknown axis scale.
	ErrUnknownScale = errors.New("chart: unknown scale")
	// ErrInvalidUnit reports an unrecognized measurement unit.
	ErrInvalidUnit = errors.New("chart: invalid unit")
	// ErrInvalidAggregation reports an unrecognized aggregation.
	ErrInvalidAggregation = errors.New("chart: invalid aggregation")
	// ErrMisaligned reports series with different point counts or labels.
	// Rendering them row-by-row would silently associate unrelated values.
	ErrMisaligned = errors.New("chart: series are not aligned")
	// ErrInvalidValue reports NaN or infinite data.
	ErrInvalidValue = errors.New("chart: non-finite value")
	// ErrLogNonPositive reports a log scale with a zero or negative value.
	ErrLogNonPositive = errors.New("chart: log scale requires positive values")
	// ErrInvalidWidth reports a non-positive or nonsensical cell budget.
	ErrInvalidWidth = errors.New("chart: invalid width")
)
