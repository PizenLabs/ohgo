package agent

import "testing"

// The path classifier is the shared evidence-class boundary. Its categories
// must keep documentation, manifests, configuration, source, and generated
// content distinct.
func TestClassifyPath(t *testing.T) {
	cases := map[string]PathClass{
		"README.md":                   PathDocumentation,
		"docs/architecture.md":        PathDocumentation,
		"docs":                        PathDocumentation,
		"LICENSE":                     PathDocumentation,
		"go.mod":                      PathManifest,
		"package.json":                PathManifest,
		"docs/requirements.txt":       PathManifest,
		"docker-compose.yml":          PathConfiguration,
		"backend/docker-compose.yml":  PathConfiguration,
		"backend/docker/Dockerfile":   PathConfiguration,
		"backend/k8s":                 PathConfiguration,
		"main.go":                     PathSource,
		"internal/agent/loop.go":      PathSource,
		"vendor/lib/client.go":        PathGenerated,
		"api/service.pb.go":           PathGenerated,
		"web/app.min.js":              PathGenerated,
		"backend/internal":            PathUnknown,
		"config/settings.yaml":        PathConfiguration,
		"internal/agent/loop_test.go": PathSource,
	}
	for path, want := range cases {
		if got := ClassifyPath(path); got != want {
			t.Errorf("ClassifyPath(%q) = %s, want %s", path, got, want)
		}
	}
}

// The manifest language mapping is shared with the cognition provenance layer
// so a manifest is classified and interpreted by one owner.
func TestManifestLanguage(t *testing.T) {
	cases := map[string]string{
		"backend/go.mod":   "go",
		"package.json":     "javascript",
		"pyproject.toml":   "python",
		"cargo.toml":       "rust",
		"pom.xml":          "",
		"internal/main.go": "",
	}
	for path, want := range cases {
		if got := ManifestLanguage(path); got != want {
			t.Errorf("ManifestLanguage(%q) = %q, want %q", path, got, want)
		}
	}
}
