// Package update owns OHGO's explicit self-update capability.
//
// The package is deliberately the only place that decides whether a newer
// release exists, whether it may replace this installation, and how the
// replacement happens. Normal invocations only read the local cache and, at
// most, refresh it in the background; installation is reachable exclusively
// through an explicit human action (`ohgo update`).
//
// The governing invariant is:
//
//	A failed, interrupted, unverifiable, unauthorized, or unsupported update
//	must never destroy the last known-good ohgo installation.
package update

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// describeSuffix matches the git-describe distance appended by the Makefile:
// "v1.2.3-4-gdeadbeef". It is not a semantic prerelease even though it follows
// a hyphen, because git's spelling uses hyphens while semver uses dots.
var describeSuffix = regexp.MustCompile(`(?i)-[0-9]+-g[0-9a-f]{7,}`)

// Version is a semantic release version as used by ohgo's build metadata.
//
// It is parsed rather than compared as a string: "1.10.0" is newer than
// "1.9.9", and a prerelease sorts before its base version. Raw preserves the
// exact spelling from the build or release tag for display.
type Version struct {
	Major int
	Minor int
	Patch int
	// Pre holds the dot-separated prerelease identifiers. Empty means a
	// stable release, which sorts after every prerelease of the same base.
	Pre []string
	// Raw is the original spelling, including a leading "v" when present.
	Raw string
}

// ParseVersion parses a release version such as "v1.2.3", "1.2.3-rc.1", or
// "v0.1.0". Build metadata after "+" is ignored: it participates in neither
// precedence nor identity. It returns ok=false for anything else, including
// the git-describe and development spellings the Makefile can produce.
func ParseVersion(raw string) (Version, bool) {
	text := strings.TrimSpace(raw)
	if text == "" {
		return Version{}, false
	}
	trimmed := strings.TrimPrefix(strings.TrimPrefix(text, "v"), "V")
	// Build metadata is not part of precedence. Discarding it before the
	// prerelease split keeps "1.2.3+meta" from being read as a prerelease.
	if i := strings.IndexByte(trimmed, '+'); i >= 0 {
		trimmed = trimmed[:i]
	}
	if trimmed == "" {
		return Version{}, false
	}
	core := trimmed
	var pre []string
	if i := strings.IndexByte(trimmed, '-'); i >= 0 {
		core = trimmed[:i]
		preText := trimmed[i+1:]
		if preText == "" {
			return Version{}, false
		}
		pre = strings.Split(preText, ".")
		for _, part := range pre {
			if part == "" || !validIdentifier(part) {
				return Version{}, false
			}
		}
	}
	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return Version{}, false
	}
	numbers := make([]int, 3)
	for i, part := range parts {
		if part == "" || !allDigits(part) {
			return Version{}, false
		}
		n, err := strconv.Atoi(part)
		if err != nil || n < 0 {
			return Version{}, false
		}
		numbers[i] = n
	}
	return Version{Major: numbers[0], Minor: numbers[1], Patch: numbers[2], Pre: pre, Raw: text}, true
}

// validIdentifier reports whether s is a non-empty semver prerelease
// identifier. Numeric identifiers with a leading zero are accepted because
// git-derived tags occasionally carry them; they compare numerically.
func validIdentifier(s string) bool {
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9':
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r == '-':
		default:
			return false
		}
	}
	return s != ""
}

func allDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return s != ""
}

// String returns the canonical form without a leading "v" and without build
// metadata, e.g. "1.2.3-rc.1".
func (v Version) String() string {
	base := fmt.Sprintf("%d.%d.%d", v.Major, v.Minor, v.Patch)
	if len(v.Pre) == 0 {
		return base
	}
	return base + "-" + strings.Join(v.Pre, ".")
}

// Display returns the version in the spelling used on user-facing surfaces,
// always prefixed with "v".
func (v Version) Display() string {
	return "v" + v.String()
}

// Equal reports whether two versions have identical precedence.
func (v Version) Equal(other Version) bool {
	return v.Compare(other) == 0
}

// Compare orders two versions using semantic-version precedence. It returns
// -1 when v sorts before other, 0 when they are equal, and 1 when v is newer.
func (v Version) Compare(other Version) int {
	if c := compareInt(v.Major, other.Major); c != 0 {
		return c
	}
	if c := compareInt(v.Minor, other.Minor); c != 0 {
		return c
	}
	if c := compareInt(v.Patch, other.Patch); c != 0 {
		return c
	}
	switch {
	case len(v.Pre) == 0 && len(other.Pre) == 0:
		return 0
	case len(v.Pre) == 0:
		return 1
	case len(other.Pre) == 0:
		return -1
	}
	for i := 0; i < len(v.Pre) && i < len(other.Pre); i++ {
		if c := compareIdentifier(v.Pre[i], other.Pre[i]); c != 0 {
			return c
		}
	}
	return compareInt(len(v.Pre), len(other.Pre))
}

func compareInt(a, b int) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}

// compareIdentifier implements semver's rule that numeric identifiers sort
// before alphanumeric ones, numerics compare as numbers, and everything else
// compares lexically.
func compareIdentifier(a, b string) int {
	aNum, bNum := allDigits(a), allDigits(b)
	switch {
	case aNum && bNum:
		ai, _ := strconv.Atoi(a)
		bi, _ := strconv.Atoi(b)
		return compareInt(ai, bi)
	case aNum:
		return -1
	case bNum:
		return 1
	default:
		return strings.Compare(a, b)
	}
}

// IsDevelopment reports whether raw identifies a build that cannot be ordered
// against a release.
//
// An unparseable version, the "dev" default, a git-describe suffix
// ("v0.1.0-3-gabc1234"), and a dirty marker all mean the same thing: there is
// no trustworthy current release to compare. Refusing to derive an update
// decision from them is what prevents a development build from being
// "upgraded" to an older stable release or compared as if it were a tag.
func IsDevelopment(raw string) bool {
	text := strings.ToLower(strings.TrimSpace(raw))
	if text == "" {
		return true
	}
	switch text {
	case "dev", "devel", "development", "unknown", "snapshot", "none":
		return true
	}
	for _, marker := range []string{"-dev", "-dirty", "+dirty", "-snapshot", "-unknown"} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	if describeSuffix.MatchString(text) {
		return true
	}
	parsed, ok := ParseVersion(raw)
	if !ok {
		return true
	}
	for _, part := range parsed.Pre {
		if strings.EqualFold(part, "dirty") {
			return true
		}
	}
	return false
}

// DecisionReason names why an update is or is not available. The vocabulary is
// closed so a caller cannot invent a policy at the presentation layer.
type DecisionReason string

const (
	// ReasonAvailable means latest is a newer stable-compatible release.
	ReasonAvailable DecisionReason = "available"
	// ReasonDevelopment means the current build cannot be ordered.
	ReasonDevelopment DecisionReason = "development build"
	// ReasonNotNewer means latest is equal to or older than current.
	ReasonNotNewer DecisionReason = "not newer"
	// ReasonPrerelease means moving from stable to prerelease would change
	// the release channel implicitly.
	ReasonPrerelease DecisionReason = "prerelease"
	// ReasonMalformed means one side could not be parsed.
	ReasonMalformed DecisionReason = "malformed version"
	// ReasonNotChecked means no local observation exists and no decision can
	// be made without a network check.
	ReasonNotChecked DecisionReason = "not checked"
	// ReasonUnsupported means this platform is outside the published release
	// matrix, so no artifact could be installed even if a release exists.
	ReasonUnsupported DecisionReason = "unsupported platform"
)

// UpdateAvailable decides whether latest is a legitimate update for current.
//
// The function never downgrades. It also refuses to move a stable build onto a
// prerelease: that is a channel change, and ohgo has no release-channel
// concept to authorize it. A development current build yields no decision at
// all rather than an arbitrary comparison.
func UpdateAvailable(current, latest string) (bool, DecisionReason) {
	if IsDevelopment(current) {
		return false, ReasonDevelopment
	}
	currentVersion, ok := ParseVersion(current)
	if !ok {
		return false, ReasonMalformed
	}
	latestVersion, ok := ParseVersion(latest)
	if !ok {
		return false, ReasonMalformed
	}
	if len(latestVersion.Pre) > 0 && len(currentVersion.Pre) == 0 {
		return false, ReasonPrerelease
	}
	if latestVersion.Compare(currentVersion) <= 0 {
		return false, ReasonNotNewer
	}
	return true, ReasonAvailable
}
