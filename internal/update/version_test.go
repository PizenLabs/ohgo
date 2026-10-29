package update

import "testing"

func TestUpdateAvailableNeverDowngrades(t *testing.T) {
	tests := []struct {
		name    string
		current string
		latest  string
		want    bool
		reason  DecisionReason
	}{
		{name: "newer stable", current: "v1.0.0", latest: "v1.1.0", want: true, reason: ReasonAvailable},
		{name: "same release", current: "v1.0.0", latest: "1.0.0", want: false, reason: ReasonNotNewer},
		{name: "older release", current: "v2.0.0", latest: "v1.9.9", want: false, reason: ReasonNotNewer},
		{name: "development build", current: "0.1.0-dev", latest: "v9.0.0", want: false, reason: ReasonDevelopment},
		{name: "git describe", current: "v1.0.0-3-gabc1234", latest: "v1.1.0", want: false, reason: ReasonDevelopment},
		{name: "dirty tree", current: "v1.0.0-dirty", latest: "v1.1.0", want: false, reason: ReasonDevelopment},
		{name: "unparseable current", current: "abc1234", latest: "v1.1.0", want: false, reason: ReasonDevelopment},
		{name: "malformed remote", current: "v1.0.0", latest: "latest", want: false, reason: ReasonMalformed},
		{name: "stable to prerelease", current: "v1.0.0", latest: "v1.1.0-rc.1", want: false, reason: ReasonPrerelease},
		{name: "prerelease to newer prerelease", current: "v1.0.0-rc.1", latest: "v1.0.0-rc.2", want: true, reason: ReasonAvailable},
		{name: "prerelease to stable", current: "v1.0.0-rc.2", latest: "v1.0.0", want: true, reason: ReasonAvailable},
		{name: "numeric prerelease ordering", current: "v1.0.0-rc.2", latest: "v1.0.0-rc.10", want: true, reason: ReasonAvailable},
		{name: "build metadata ignored", current: "v1.0.0+build.1", latest: "v1.0.0+build.2", want: false, reason: ReasonNotNewer},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, reason := UpdateAvailable(test.current, test.latest)
			if got != test.want || reason != test.reason {
				t.Fatalf("UpdateAvailable(%q, %q) = (%v, %q), want (%v, %q)",
					test.current, test.latest, got, reason, test.want, test.reason)
			}
		})
	}
}

func TestVersionCompare(t *testing.T) {
	tests := []struct {
		left, right string
		want        int
	}{
		{"1.0.0", "1.0.0", 0},
		{"1.10.0", "1.9.9", 1},
		{"1.0.0-alpha", "1.0.0-alpha.1", -1},
		{"1.0.0-alpha.1", "1.0.0-alpha.beta", -1},
		{"1.0.0-beta.11", "1.0.0-rc.1", -1},
		{"1.0.0-rc.1", "1.0.0", -1},
	}
	for _, test := range tests {
		left, ok := ParseVersion(test.left)
		if !ok {
			t.Fatalf("ParseVersion(%q) failed", test.left)
		}
		right, ok := ParseVersion(test.right)
		if !ok {
			t.Fatalf("ParseVersion(%q) failed", test.right)
		}
		if got := left.Compare(right); got != test.want {
			t.Fatalf("Compare(%q, %q) = %d, want %d", test.left, test.right, got, test.want)
		}
	}
}

func TestParseVersionRejectsDevelopmentSpellings(t *testing.T) {
	for _, raw := range []string{"", "dev", "v1", "v1.2", "v1.2.3.4", "v1.2.x", "v1.2.3-", "v1.2.3-rc..1"} {
		if version, ok := ParseVersion(raw); ok {
			t.Fatalf("ParseVersion(%q) = %v, want rejection", raw, version)
		}
	}
}

func TestIsDevelopmentRecognizesDescribeSuffix(t *testing.T) {
	for _, raw := range []string{"dev", "0.1.0-dev", "v1.2.3-4-gdeadbeef", "v1.2.3-dirty", "v1.2.3-4-gdeadbeef-dirty"} {
		if !IsDevelopment(raw) {
			t.Fatalf("IsDevelopment(%q) = false, want true", raw)
		}
	}
	for _, raw := range []string{"v1.2.3", "v1.2.3-rc.1", "1.2.3"} {
		if IsDevelopment(raw) {
			t.Fatalf("IsDevelopment(%q) = true, want false", raw)
		}
	}
}
