package update

import (
	"strings"
	"testing"
)

func TestEmbeddedChangelogIsLocalAndWellFormed(t *testing.T) {
	changelog, err := LoadChangelog()
	if err != nil {
		t.Fatalf("LoadChangelog: %v", err)
	}
	release, ok := changelog.Release("v0.0.3")
	if !ok {
		t.Fatal("the embedded changelog has no notes for v0.0.3")
	}
	if !strings.Contains(release.Title, "v0.0.3") {
		t.Errorf("title = %q, want the release named", release.Title)
	}
	if len(release.Changes) == 0 {
		t.Fatal("v0.0.3 has no changes")
	}
	seen := make(map[string]bool)
	for _, change := range release.Changes {
		if change.ID == "" || seen[change.ID] {
			t.Errorf("change id %q is empty or duplicated", change.ID)
		}
		seen[change.ID] = true
		switch change.Type {
		case ChangeAdded, ChangeImproved, ChangeFixed, ChangeChanged:
		default:
			t.Errorf("change %q has unsupported type %q", change.ID, change.Type)
		}
	}
}

func TestChangelogReleaseLookupIsSemantic(t *testing.T) {
	changelog := Changelog{Schema: 1, Releases: map[string]ReleaseNotes{
		"v1.2.3": {Title: "v1.2.3 notes", Changes: []Change{{ID: "a", Type: ChangeAdded, Title: "A"}}},
		"v1.3.0": {Title: "v1.3.0 notes", Changes: []Change{{ID: "b", Type: ChangeFixed, Title: "B"}}},
	}}
	for _, version := range []string{"v1.2.3", "1.2.3", " V1.2.3 "} {
		release, ok := changelog.Release(version)
		if !ok {
			t.Fatalf("Release(%q) missed the release", version)
		}
		if release.Title != "v1.2.3 notes" {
			t.Fatalf("Release(%q) = %q, want only v1.2.3", version, release.Title)
		}
	}
	if _, ok := changelog.Release("v9.9.9"); ok {
		t.Fatal("a missing release resolved to another release's notes")
	}
	if _, ok := changelog.Release(""); ok {
		t.Fatal("an empty release resolved notes")
	}
}

func TestParseChangelogRejectsMalformedDocuments(t *testing.T) {
	cases := map[string]string{
		"invalid json":  `{`,
		"wrong schema":  `{"schema":2,"releases":{}}`,
		"empty version": `{"schema":1,"releases":{"":{"changes":[]}}}`,
		"missing id":    `{"schema":1,"releases":{"v1.0.0":{"changes":[{"type":"added","title":"A"}]}}}`,
		"missing title": `{"schema":1,"releases":{"v1.0.0":{"changes":[{"id":"a","type":"added"}]}}}`,
		"unknown type":  `{"schema":1,"releases":{"v1.0.0":{"changes":[{"id":"a","type":"announced","title":"A"}]}}}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseChangelog([]byte(body)); err == nil {
				t.Fatalf("ParseChangelog accepted %s", body)
			}
		})
	}
}

func TestParseChangelogKeepsOptionalDescriptionOptional(t *testing.T) {
	changelog, err := ParseChangelog([]byte(`{
		"schema": 1,
		"releases": {
			"v1.0.0": {
				"title": "What's new",
				"changes": [
					{"id": "a", "type": "added", "title": "A"},
					{"id": "b", "type": "fixed", "title": "B", "description": "why"}
				]
			}
		}
	}`))
	if err != nil {
		t.Fatalf("ParseChangelog: %v", err)
	}
	release, ok := changelog.Release("v1.0.0")
	if !ok || len(release.Changes) != 2 {
		t.Fatalf("release = %+v, ok=%v", release, ok)
	}
}
