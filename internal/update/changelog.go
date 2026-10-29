package update

import (
	"embed"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
)

// changelogSchemaVersion is the only release-notes schema this build reads.
const changelogSchemaVersion = 1

// changelogFS embeds the release notes in the binary. What's New must never
// require a network request: release discovery may be remote, but the
// explanation of the release the user just installed is local content.
//
//go:embed changelog.json
var changelogFS embed.FS

// ChangeType classifies one release note. The vocabulary is closed so the
// presentation layer never invents a category.
type ChangeType string

const (
	ChangeAdded    ChangeType = "added"
	ChangeImproved ChangeType = "improved"
	ChangeFixed    ChangeType = "fixed"
	ChangeChanged  ChangeType = "changed"
)

// Change is one stable, human-readable release note.
type Change struct {
	// ID is stable across releases so a later announcement can point at the
	// same change without relying on its wording.
	ID          string     `json:"id"`
	Type        ChangeType `json:"type"`
	Title       string     `json:"title"`
	Description string     `json:"description,omitempty"`
}

// ReleaseNotes is the content for one released version.
type ReleaseNotes struct {
	Title   string   `json:"title"`
	Changes []Change `json:"changes"`
}

// Changelog is the embedded release-notes document.
type Changelog struct {
	Schema   int                     `json:"schema"`
	Releases map[string]ReleaseNotes `json:"releases"`
}

var (
	embeddedChangelog     Changelog
	embeddedChangelogErr  error
	embeddedChangelogOnce sync.Once
)

// LoadChangelog returns the release notes embedded in this binary. The result
// is parsed once and cached; it performs no file or network access.
func LoadChangelog() (Changelog, error) {
	embeddedChangelogOnce.Do(func() {
		data, err := changelogFS.ReadFile("changelog.json")
		if err != nil {
			embeddedChangelogErr = fmt.Errorf("read embedded changelog: %w", err)
			return
		}
		embeddedChangelog, embeddedChangelogErr = ParseChangelog(data)
	})
	return embeddedChangelog, embeddedChangelogErr
}

// ParseChangelog decodes and validates a release-notes document.
//
// Validation rejects an unknown schema and malformed change types, but keeps
// the document tolerant of extra fields and missing optional prose. A
// malformed changelog is reported as an error and never mutates update state.
func ParseChangelog(data []byte) (Changelog, error) {
	var changelog Changelog
	if err := json.Unmarshal(data, &changelog); err != nil {
		return Changelog{}, fmt.Errorf("parse changelog: %w", err)
	}
	if changelog.Schema != changelogSchemaVersion {
		return Changelog{}, fmt.Errorf("parse changelog: unsupported schema %d", changelog.Schema)
	}
	for version, release := range changelog.Releases {
		if strings.TrimSpace(version) == "" {
			return Changelog{}, fmt.Errorf("parse changelog: empty release version")
		}
		for i, change := range release.Changes {
			if strings.TrimSpace(change.ID) == "" {
				return Changelog{}, fmt.Errorf("parse changelog %s: change %d has no id", version, i)
			}
			if strings.TrimSpace(change.Title) == "" {
				return Changelog{}, fmt.Errorf("parse changelog %s: change %q has no title", version, change.ID)
			}
			switch change.Type {
			case ChangeAdded, ChangeImproved, ChangeFixed, ChangeChanged:
			default:
				return Changelog{}, fmt.Errorf("parse changelog %s: change %q has unsupported type %q", version, change.ID, change.Type)
			}
		}
	}
	return changelog, nil
}

// Release returns the notes for version. Releases are keyed by their release
// tag, so a caller may pass either "v1.2.3" or "1.2.3"; semantic equality keeps
// the two spellings from splitting the lookup.
func (c Changelog) Release(version string) (ReleaseNotes, bool) {
	raw := strings.TrimSpace(version)
	if raw == "" {
		return ReleaseNotes{}, false
	}
	if release, ok := c.Releases[raw]; ok {
		return release, true
	}
	want, ok := ParseVersion(raw)
	if !ok {
		return ReleaseNotes{}, false
	}
	for tag, release := range c.Releases {
		got, ok := ParseVersion(tag)
		if ok && got.Equal(want) {
			return release, true
		}
	}
	return ReleaseNotes{}, false
}
