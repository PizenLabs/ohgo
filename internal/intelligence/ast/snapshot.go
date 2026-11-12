package ast

import (
	"path"
	"sort"
	"strings"
)

// isGoFile reports whether the Go adapter claims a path.
func isGoFile(rel string) bool {
	return strings.EqualFold(path.Ext(rel), ".go")
}

// fileKindFor classifies a repository-relative path for inventory.
//
// The allowlist is deliberately small. A structural snapshot should name the
// module, build, container, and CI facts an agent actually reasons about, not
// every file format the repository happens to contain.
func fileKindFor(rel string) FileKind {
	if isGoFile(rel) {
		if strings.HasSuffix(rel, "_test.go") {
			return FileGoTest
		}
		return FileGoSource
	}
	base := path.Base(rel)
	switch base {
	case "go.mod", "go.work", "go.sum":
		return FileManifest
	case "Makefile", "makefile", "GNUmakefile", "Dockerfile", "Containerfile",
		".golangci.yml", ".golangci.yaml", ".goreleaser.yml", ".goreleaser.yaml",
		"Taskfile.yml", "Taskfile.yaml":
		return FileConfig
	}
	if strings.HasPrefix(base, "README") || strings.HasPrefix(base, "CONTRIBUTING") {
		return FileDocumentation
	}
	switch strings.ToLower(path.Ext(rel)) {
	case ".md", ".markdown", ".rst":
		return FileDocumentation
	}
	if strings.HasPrefix(rel, ".github/workflows/") {
		switch strings.ToLower(path.Ext(rel)) {
		case ".yml", ".yaml":
			return FileConfig
		}
	}
	return FileOther
}

// configFacts projects the allowlisted configuration records into inventory
// facts. The module path comes from the same go.mod read the graph resolved
// against; a second parser would be a second answer.
func configFacts(files map[string]fileRecord, module string) []ConfigInfo {
	out := make([]ConfigInfo, 0)
	for rel, rec := range files {
		if rec.kind != FileConfig && rec.kind != FileManifest {
			continue
		}
		fact := ConfigInfo{Path: rel, Kind: configKind(rel)}
		if rel == "go.mod" {
			fact.Value = module
		}
		out = append(out, fact)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// configKind names the configuration role without parsing its contents.
func configKind(rel string) string {
	base := path.Base(rel)
	switch {
	case base == "go.mod":
		return "module"
	case base == "go.work":
		return "workspace"
	case base == "go.sum":
		return "module_checksums"
	case base == "Makefile" || base == "makefile" || base == "GNUmakefile":
		return "make"
	case base == "Dockerfile" || base == "Containerfile":
		return "container"
	case strings.HasPrefix(rel, ".github/workflows/"):
		return "ci"
	default:
		return "config"
	}
}
