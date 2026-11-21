package chart

import (
	"fmt"
	"math"
	"strings"
)

// normalizeDataset returns a sanitized deep copy. It is the only place that
// repairs an omission the reader cannot see: when one series omits a label
// another series supplied at the same index, the shared label is copied across
// so a table row can never silently lose its name.
func normalizeDataset(data Dataset) (Dataset, error) {
	out := data
	out.Title = sanitizeLabel(data.Title)
	out.XLabel = sanitizeLabel(data.XLabel)
	out.Note = sanitizeLabel(data.Note)
	out.Finding = sanitizeLabel(data.Finding)
	out.Provenance = normalizeProvenance(data.Provenance)
	out.Series = make([]Series, len(data.Series))
	for i, series := range data.Series {
		copied := series
		copied.Name = sanitizeLabel(series.Name)
		copied.Provenance = normalizeProvenance(series.Provenance)
		copied.Points = make([]Point, len(series.Points))
		for j, point := range series.Points {
			copiedPoint := point
			copiedPoint.Label = sanitizeLabel(point.Label)
			copiedPoint.Note = sanitizeLabel(point.Note)
			copiedPoint.Provenance = normalizeProvenance(point.Provenance)
			copied.Points[j] = copiedPoint
		}
		out.Series[i] = copied
	}
	if len(out.Series) == 0 {
		return out, ErrNoData
	}
	// Share a label across aligned series when only one of them supplied it.
	points := len(out.Series[0].Points)
	for i := range out.Series {
		if len(out.Series[i].Points) != points {
			return out, fmt.Errorf("%w: %q has %d points, %q has %d",
				ErrMisaligned, out.Series[0].Name, points, out.Series[i].Name, len(out.Series[i].Points))
		}
	}
	for index := range points {
		label := ""
		for _, series := range out.Series {
			if candidate := series.Points[index].Label; candidate != "" {
				label = candidate
				break
			}
		}
		if label == "" {
			continue
		}
		for i := range out.Series {
			if out.Series[i].Points[index].Label == "" {
				out.Series[i].Points[index].Label = label
			}
		}
	}
	return out, nil
}

func normalizeProvenance(p Provenance) Provenance {
	p.ID = sanitizeLabel(p.ID)
	p.Source = sanitizeLabel(p.Source)
	p.State = sanitizeLabel(p.State)
	p.Freshness = sanitizeLabel(p.Freshness)
	p.Ref = sanitizeLabel(p.Ref)
	p.Note = sanitizeLabel(p.Note)
	return p
}

// ValidateDataset checks the structural contract a chart depends on. It does
// not decide a chart kind; that is the planner's job.
func ValidateDataset(data Dataset) error {
	if len(data.Series) == 0 {
		return ErrNoData
	}
	if !data.XUnit.Valid() {
		return fmt.Errorf("%w: %q", ErrInvalidUnit, data.XUnit)
	}
	seen := make(map[string]struct{}, len(data.Series))
	points := len(data.Series[0].Points)
	for _, series := range data.Series {
		name := strings.TrimSpace(series.Name)
		if name == "" {
			return fmt.Errorf("%w: a series has no name", ErrDuplicateSeries)
		}
		if _, ok := seen[name]; ok {
			return fmt.Errorf("%w: %q", ErrDuplicateSeries, name)
		}
		seen[name] = struct{}{}
		if !series.Unit.Valid() {
			return fmt.Errorf("%w: %q", ErrInvalidUnit, series.Unit)
		}
		if !series.Aggregation.Valid() {
			return fmt.Errorf("%w: %q", ErrInvalidAggregation, series.Aggregation)
		}
		if series.Scale != "" && !series.Scale.Valid() {
			return fmt.Errorf("%w: %q", ErrUnknownScale, series.Scale)
		}
		if len(series.Points) != points {
			return fmt.Errorf("%w: %q has %d points, expected %d",
				ErrMisaligned, name, len(series.Points), points)
		}
		for index, point := range series.Points {
			if point.Value.Present && !finite(point.Value.Number) {
				return fmt.Errorf("%w: %q point %d", ErrInvalidValue, name, index+1)
			}
			if point.Interval.Present {
				if !finite(point.Interval.Low) || !finite(point.Interval.High) {
					return fmt.Errorf("%w: %q point %d interval", ErrInvalidValue, name, index+1)
				}
				if point.Interval.Low > point.Interval.High {
					return fmt.Errorf("%w: %q point %d has an inverted interval",
						ErrInvalidValue, name, index+1)
				}
				if point.Value.Present && !point.Interval.Contains(point.Value.Number) {
					return fmt.Errorf("%w: %q point %d is outside its measured interval",
						ErrInvalidValue, name, index+1)
				}
			}
			if series.Scale == ScaleLog && point.Value.Present && point.Value.Number <= 0 {
				return fmt.Errorf("%w: %q point %d", ErrLogNonPositive, name, index+1)
			}
		}
	}
	if points == 0 {
		return fmt.Errorf("%w: every series is empty", ErrNoValues)
	}
	if err := validateAlignment(data); err != nil {
		return err
	}
	return nil
}

// validateAlignment checks labels and numeric positions across series. Two
// different labels at one row index mean the columns describe different
// observations; rendering them side by side would manufacture a comparison.
func validateAlignment(data Dataset) error {
	points := len(data.Series[0].Points)
	for index := range points {
		label := ""
		hasX := false
		x := 0.0
		for _, series := range data.Series {
			point := series.Points[index]
			if point.Label != "" {
				if label == "" {
					label = point.Label
				} else if point.Label != label {
					return fmt.Errorf("%w: row %d is %q in one series and %q in another",
						ErrMisaligned, index+1, label, point.Label)
				}
			}
			if point.HasX {
				if !isfiniteX(point.X) {
					return fmt.Errorf("%w: %q point %d has a non-finite position",
						ErrInvalidValue, series.Name, index+1)
				}
				if !hasX {
					hasX, x = true, point.X
				} else if math.Abs(point.X-x) > 1e-9 {
					return fmt.Errorf("%w: row %d has different numeric positions",
						ErrMisaligned, index+1)
				}
			}
		}
		if hasX && index > 0 {
			previous := 0.0
			found := false
			for _, series := range data.Series {
				point := series.Points[index-1]
				if point.HasX {
					previous, found = point.X, true
					break
				}
			}
			if found && x < previous {
				return fmt.Errorf("%w: numeric positions are not ordered at row %d",
					ErrMisaligned, index+1)
			}
		}
	}
	// Either every point has a numeric position or none does. A mixed
	// dimension cannot be drawn without silently choosing one.
	for _, series := range data.Series {
		for index, point := range series.Points {
			if point.HasX != data.Series[0].Points[index].HasX {
				return fmt.Errorf("%w: %q point %d mixes numeric and categorical positions",
					ErrMisaligned, series.Name, index+1)
			}
		}
	}
	return nil
}

// ValidateRequest checks a request's closed vocabulary. Unknown values are an
// error, never a silent fallback: a caller that asked for a histogram should
// be told this engine does not render one.
func ValidateRequest(req Request) error {
	if req.Kind != "" && !req.Kind.Valid() {
		return fmt.Errorf("%w: %q", ErrUnknownKind, req.Kind)
	}
	seen := make(map[string]struct{}, len(req.Series))
	for _, name := range req.Series {
		name = sanitizeLabel(name)
		if name == "" {
			return fmt.Errorf("%w: empty selection", ErrUnknownSeries)
		}
		if _, ok := seen[name]; ok {
			return fmt.Errorf("%w: %q selected twice", ErrUnknownSeries, name)
		}
		seen[name] = struct{}{}
	}
	return nil
}

// ValidateSpec checks the renderer's input. A malformed spec is a programming
// error; the renderer never guesses what was meant.
func ValidateSpec(spec ChartSpec) error {
	if !spec.Kind.Valid() {
		return fmt.Errorf("%w: %q", ErrUnknownKind, spec.Kind)
	}
	if spec.Width <= 0 {
		return fmt.Errorf("%w: %d", ErrInvalidWidth, spec.Width)
	}
	if len(spec.Series) == 0 {
		return ErrNoData
	}
	if err := ValidateDataset(Dataset{Series: spec.Series}); err != nil {
		return err
	}
	switch spec.Kind {
	case KindTable:
		if !spec.IncludeTable {
			return fmt.Errorf("%w: a table spec must include its exact table", ErrMisaligned)
		}
		if len(spec.Panels) != 0 {
			return fmt.Errorf("%w: a table spec has no panels", ErrMisaligned)
		}
	case KindBar, KindLine:
		if len(spec.Panels) != 1 {
			return fmt.Errorf("%w: %s needs exactly one panel", ErrMisaligned, spec.Kind)
		}
	case KindPanels:
		if len(spec.Panels) < 2 {
			return fmt.Errorf("%w: panels needs at least two panels", ErrMisaligned)
		}
	case KindChange:
		if len(spec.Series) != 2 || len(spec.Panels) != 0 {
			return fmt.Errorf("%w: change needs exactly two series", ErrMisaligned)
		}
		if spec.Series[0].Unit != spec.Series[1].Unit {
			return fmt.Errorf("%w: change series use different units", ErrMisaligned)
		}
		if spec.IncludeTable {
			return fmt.Errorf("%w: change grid is its own exact table", ErrMisaligned)
		}
	}
	for index, panel := range spec.Panels {
		if panel.Kind != KindBar && panel.Kind != KindLine {
			return fmt.Errorf("%w: panel %d kind %q", ErrUnknownKind, index+1, panel.Kind)
		}
		if len(panel.Series) == 0 {
			return fmt.Errorf("%w: panel %d has no series", ErrMisaligned, index+1)
		}
		if panel.Kind == KindBar && len(panel.Series) != 1 {
			return fmt.Errorf("%w: a bar panel needs exactly one series", ErrMisaligned)
		}
		if !panel.XScale.Valid() || !panel.YScale.Valid() {
			return fmt.Errorf("%w: panel %d axis scale", ErrUnknownScale, index+1)
		}
		for _, seriesIndex := range panel.Series {
			if seriesIndex < 0 || seriesIndex >= len(spec.Series) {
				return fmt.Errorf("%w: panel %d references series %d", ErrMisaligned, index+1, seriesIndex)
			}
		}
	}
	return nil
}

func finite(number float64) bool { return !math.IsNaN(number) && !math.IsInf(number, 0) }

// isfiniteX rejects NaN and infinities but accepts any finite coordinate.
func isfiniteX(number float64) bool { return finite(number) }

// selectedSeries returns the series a request selects, in dataset order. An
// empty selection selects everything. Unknown names are an error.
func selectedSeries(data Dataset, req Request) ([]Series, error) {
	if len(req.Series) == 0 {
		return append([]Series(nil), data.Series...), nil
	}
	byName := make(map[string]Series, len(data.Series))
	for _, series := range data.Series {
		byName[series.Name] = series
	}
	out := make([]Series, 0, len(req.Series))
	for _, name := range req.Series {
		name = sanitizeLabel(name)
		series, ok := byName[name]
		if !ok {
			return nil, fmt.Errorf("%w: %q", ErrUnknownSeries, name)
		}
		out = append(out, series)
	}
	if len(out) == 0 {
		return nil, ErrNoData
	}
	return out, nil
}
