package ast

import (
	"context"
	"sort"
)

// Inventory bounds for whole-snapshot projections.
//
// Inventory is the in-process seam the analysis layer needs: one freshness
// check, one consistent generation, and no pagination round trips. It is not a
// model-facing API. A structural planner still decides how much of it may be
// summarised into a result.
const (
	// InventoryDefaultMaxFiles bounds one inventory projection.
	InventoryDefaultMaxFiles = 20000
	// InventoryMaxFiles is the hard ceiling. A larger repository is analysed
	// hierarchically, not materialised row by row.
	InventoryMaxFiles = 200000
)

// InventoryQuery selects one whole-snapshot inventory projection.
//
// Parse controls the activation boundary. False is the metadata-only shape an
// architecture overview needs: every file is named with its language, kind,
// size, and source classification, but no adapter runs, so declaration totals
// stay observations of a pending snapshot instead of facts. True is the
// targeted boundary: pending supported source is parsed first, which is what a
// planner chooses only when the task needs relationships.
type InventoryQuery struct {
	// Parse parses pending supported source before projecting.
	Parse bool
	// MaxFiles bounds the returned file rows. Zero uses the default.
	MaxFiles int
	// MaxPackages bounds the returned package rows. Zero uses MaxFiles as the
	// bound: a repository cannot have more package directories than files in
	// practice, and a second unrelated ceiling would be noise.
	MaxPackages int
}

// Inventory is one consistent projection of a published snapshot.
//
// FilesTruncated means the row list was cut by MaxFiles. It does not mean the
// snapshot itself is incomplete: the counts in Snapshot remain the whole-tree
// totals, and a consumer that needs repository-wide claims must not infer
// absence from a truncated row set.
type Inventory struct {
	Snapshot SnapshotInfo `json:"snapshot"`
	// Files are ordered by path.
	Files []FileInfo `json:"files"`
	// Packages are ordered by directory then package name.
	Packages []PackageInfo `json:"packages"`
	// Configs are the allowlisted manifest/configuration facts.
	Configs []ConfigInfo `json:"configs"`
	// TotalFiles is every indexed file, before MaxFiles.
	TotalFiles int `json:"total_files"`
	// TotalPackages is every package row, before the package bound.
	TotalPackages int `json:"total_packages"`
	// FilesTruncated and PackagesTruncated name the bound that applied.
	FilesTruncated    bool   `json:"files_truncated"`
	PackagesTruncated bool   `json:"packages_truncated"`
	Reason            string `json:"reason,omitempty"`
}

// Inventory projects the whole snapshot for in-process analysis.
//
// It performs exactly one freshness check and one optional parse pass. The
// returned rows all describe the same generation; that is the property five
// separate paginated queries cannot provide.
func (idx *Index) Inventory(ctx context.Context, query InventoryQuery) (Inventory, error) {
	if err := ctx.Err(); err != nil {
		return Inventory{}, err
	}
	if err := idx.ensureFreshGraph(ctx); err != nil {
		return Inventory{}, err
	}
	maxFiles := query.MaxFiles
	if maxFiles <= 0 {
		maxFiles = InventoryDefaultMaxFiles
	}
	if maxFiles > InventoryMaxFiles {
		maxFiles = InventoryMaxFiles
	}
	maxPackages := query.MaxPackages
	if maxPackages <= 0 {
		maxPackages = maxFiles
	}

	idx.mu.Lock()
	defer idx.mu.Unlock()
	if query.Parse {
		if err := idx.ensureParsedLockedCtx(ctx); err != nil {
			return Inventory{}, err
		}
	}
	if err := ctx.Err(); err != nil {
		return Inventory{}, err
	}

	result := Inventory{Snapshot: idx.infoLocked()}
	result.TotalFiles = len(idx.files)
	result.Files = make([]FileInfo, 0, len(idx.files))
	for rel, rec := range idx.files {
		result.Files = append(result.Files, idx.fileInfoLocked(rel, rec))
	}
	sort.SliceStable(result.Files, func(i, j int) bool {
		if result.Files[i].Path != result.Files[j].Path {
			return result.Files[i].Path < result.Files[j].Path
		}
		return result.Files[i].Kind < result.Files[j].Kind
	})
	if len(result.Files) > maxFiles {
		result.Files = result.Files[:maxFiles]
		result.FilesTruncated = true
		result.Reason = "file_limit"
	}

	if idx.graph != nil {
		for _, dir := range idx.graph.order {
			result.Packages = append(result.Packages, idx.packageInfoLocked(idx.graph.nodes[dir]))
		}
	}
	result.Packages = append(result.Packages, idx.languagePackagesLocked(PackageQuery{}, "", false)...)
	sort.SliceStable(result.Packages, func(i, j int) bool {
		if result.Packages[i].Dir != result.Packages[j].Dir {
			return result.Packages[i].Dir < result.Packages[j].Dir
		}
		if result.Packages[i].Language != result.Packages[j].Language {
			return result.Packages[i].Language < result.Packages[j].Language
		}
		return result.Packages[i].Name < result.Packages[j].Name
	})
	result.TotalPackages = len(result.Packages)
	if len(result.Packages) > maxPackages {
		result.Packages = result.Packages[:maxPackages]
		result.PackagesTruncated = true
		if result.Reason == "" {
			result.Reason = "package_limit"
		}
	}

	result.Configs = configFacts(idx.files, idx.modulePath)
	if len(result.Configs) > StructureMaxLimit {
		result.Configs = result.Configs[:StructureMaxLimit]
		if result.Reason == "" {
			result.Reason = "config_limit"
		}
	}
	return result, nil
}
