package chart

import (
	"errors"
	"testing"
)

func TestValidateDatasetRejectsMisalignedSeries(t *testing.T) {
	base := Dataset{Series: []Series{
		{Name: "a", Unit: UnitCount, Points: []Point{
			{Label: "one", Value: Measured(1)},
			{Label: "two", Value: Measured(2)},
		}},
		{Name: "b", Unit: UnitCount, Points: []Point{
			{Label: "one", Value: Measured(3)},
			{Label: "different", Value: Measured(4)},
		}},
	}}
	if err := ValidateDataset(base); !errors.Is(err, ErrMisaligned) {
		t.Fatalf("ValidateDataset = %v, want ErrMisaligned", err)
	}
}

func TestValidateDatasetRejectsDuplicateAndUnknownUnits(t *testing.T) {
	duplicate := Dataset{Series: []Series{
		{Name: "a", Unit: UnitCount, Points: []Point{{Label: "x", Value: Measured(1)}}},
		{Name: "a", Unit: UnitCount, Points: []Point{{Label: "x", Value: Measured(2)}}},
	}}
	if err := ValidateDataset(duplicate); !errors.Is(err, ErrDuplicateSeries) {
		t.Fatalf("duplicate = %v, want ErrDuplicateSeries", err)
	}
	bad := Dataset{Series: []Series{
		{Name: "a", Unit: Unit("furlongs"), Points: []Point{{Label: "x", Value: Measured(1)}}},
	}}
	if err := ValidateDataset(bad); !errors.Is(err, ErrInvalidUnit) {
		t.Fatalf("unit = %v, want ErrInvalidUnit", err)
	}
}

func TestDeclaredLogScaleRejectsNonPositive(t *testing.T) {
	data := Dataset{Series: []Series{
		{Name: "a", Unit: UnitMilliseconds, Scale: ScaleLog, Points: []Point{
			{Label: "x", Value: Measured(0)},
		}},
	}}
	if _, _, err := Build(PlanInput{Data: data}, 80); !errors.Is(err, ErrLogNonPositive) {
		t.Fatalf("Build = %v, want ErrLogNonPositive", err)
	}
}

func TestValidateRequestRejectsUnknownKindAndSeries(t *testing.T) {
	if err := ValidateRequest(Request{Kind: Kind("histogram")}); !errors.Is(err, ErrUnknownKind) {
		t.Fatalf("kind = %v, want ErrUnknownKind", err)
	}
	if err := ValidateRequest(Request{Series: []string{"a", "a"}}); !errors.Is(err, ErrUnknownSeries) {
		t.Fatalf("series = %v, want ErrUnknownSeries", err)
	}
}

func TestNormalizeFillsSharedLabelsWithoutInventingValues(t *testing.T) {
	data := Dataset{Series: []Series{
		{Name: "a", Unit: UnitCount, Points: []Point{
			{Label: "", Value: Measured(1)},
			{Label: "two", Value: Missing()},
		}},
		{Name: "b", Unit: UnitCount, Points: []Point{
			{Label: "one", Value: Measured(2)},
			{Label: "", Value: Measured(3)},
		}},
	}}
	spec, _ := mustBuild(t, PlanInput{Data: data}, 80)
	if spec.Series[0].Points[0].Label != "one" || spec.Series[1].Points[1].Label != "two" {
		t.Fatalf("labels were not shared: %+v", spec.Series)
	}
	if spec.Series[0].Points[1].Value.Present {
		t.Fatalf("missing value became present")
	}
}
