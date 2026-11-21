package chart

import "testing"

func TestFormatPreservesUnitsAndPrecision(t *testing.T) {
	cases := []struct {
		name  string
		value Value
		unit  Unit
		want  string
	}{
		{"milliseconds", Measured(43), UnitMilliseconds, "43 ms"},
		{"milliseconds promote", Measured(1290), UnitMilliseconds, "1.29 s"},
		{"milliseconds precision", Measured(0.43), UnitMilliseconds, "0.43 ms"},
		{"milliseconds negative", Measured(-1290), UnitMilliseconds, "-1.29 s"},
		{"microseconds promote", Measured(1500), UnitMicroseconds, "1.5 ms"},
		{"files exact", Measured(20000), UnitFiles, "20,000"},
		{"files exact million", Measured(1234567), UnitFiles, "1,234,567"},
		{"tokens integer", Measured(1200), UnitTokens, "1,200"},
		{"ratio", Measured(0.97), UnitRatio, "0.97"},
		{"percent", Measured(97), UnitPercent, "97%"},
		{"usd", Measured(1.2345), UnitUSD, "$1.23"},
		{"usd small", Measured(0.0123), UnitUSD, "$0.0123"},
		{"bytes", Measured(1536), UnitBytes, "1.5 KiB"},
		{"zero count", Measured(0), UnitCount, "0"},
		{"missing", Missing(), UnitMilliseconds, "—"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := FormatValue(tc.value, tc.unit); got != tc.want {
				t.Fatalf("FormatValue = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestFormatSignedMakesDirectionExplicit(t *testing.T) {
	if got := FormatSigned(370, UnitMilliseconds); got != "+370 ms" {
		t.Fatalf("positive delta = %q", got)
	}
	if got := FormatSigned(-11.83, UnitSeconds); got != "-11.83 s" {
		t.Fatalf("negative delta = %q", got)
	}
	if got := FormatSigned(-97, UnitPercent); got != "-97%" {
		t.Fatalf("percent delta = %q", got)
	}
}

func TestSanitizeLabelDropsTerminalControl(t *testing.T) {
	got := sanitizeLabel("\x1b[31mred\x1b[0m\tvalue\nnext")
	if got != "red value next" {
		t.Fatalf("sanitizeLabel = %q", got)
	}
}

func TestTurnsNoteUsesSingularAndPlural(t *testing.T) {
	if got := TurnsNote(1); got != "1 measured turn" {
		t.Fatalf("singular = %q", got)
	}
	if got := TurnsNote(3); got != "3 measured turns" {
		t.Fatalf("plural = %q", got)
	}
}

func TestSeriesTitleDoesNotRepeatAnAlreadyNamedUnit(t *testing.T) {
	if got := seriesTitle(Series{Name: "tokens", Unit: UnitTokens}); got != "tokens" {
		t.Fatalf("tokens title = %q", got)
	}
	if got := seriesTitle(Series{Name: "cold", Unit: UnitMilliseconds}); got != "cold (ms)" {
		t.Fatalf("duration title = %q", got)
	}
	if got := tableHeader(Series{Name: "docs", Unit: UnitFiles}); got != "docs (files)" {
		t.Fatalf("files header = %q", got)
	}
	if got := tableHeader(Series{Name: "tokens", Unit: UnitTokens}); got != "tokens" {
		t.Fatalf("token header = %q", got)
	}
}
