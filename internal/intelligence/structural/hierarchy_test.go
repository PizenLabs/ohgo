package structural

import (
	"reflect"
	"testing"

	"github.com/PizenLabs/ohgo/internal/intelligence/ast"
)

// TestClassifyPathSeparatesEvidenceClasses pins the path vocabulary and, more
// importantly, its evidence state: everything except a test flag is inferred,
// so a consumer cannot cite a filename as proof of authored code.
func TestClassifyPathSeparatesEvidenceClasses(t *testing.T) {
	cases := []struct {
		path string
		test bool
		want SourceClass
	}{
		{"internal/domain/order.go", false, ClassProduction},
		{"vendor/github.com/x/y.go", false, ClassVendor},
		{"web/node_modules/react/index.js", false, ClassVendor},
		{"proto/order.pb.go", false, ClassGenerated},
		{"zz_generated.deepcopy.go", false, ClassGenerated},
		{"src/app.min.js", false, ClassGenerated},
		{"docs/architecture.md", false, ClassDocumentation},
		{"README.md", false, ClassDocumentation},
		{"testdata/golden.json", false, ClassFixture},
		{"examples/basic/main.go", false, ClassExample},
		{"a/b_test.go", false, ClassTest},
		{"web/src/app.spec.ts", false, ClassTest},
		{"scripts/release.sh", false, ClassTooling},
		{"go.mod", false, ClassTooling},
		{"src/index.ts", false, ClassProduction},
		{"dist/app.js", false, ClassBuildArtifact},
	}
	for _, testCase := range cases {
		got, evidence := classifyPath(testCase.path, testCase.test)
		if got != testCase.want {
			t.Errorf("classifyPath(%q) = %s, want %s", testCase.path, got, testCase.want)
			continue
		}
		if testCase.test && evidence.State != StateObserved {
			t.Errorf("inventory test flag for %q is %s, want OBSERVED", testCase.path, evidence.State)
		}
		if !testCase.test && evidence.State != StateInferred {
			t.Errorf("path rule for %q is %s, want INFERRED", testCase.path, evidence.State)
		}
	}
}

func hierarchyFixture(t *testing.T) (*Snapshot, *Snapshot) {
	t.Helper()
	root := fixture(t, map[string]string{
		"go.mod":                   "module example.com/app\n\ngo 1.21\n",
		"api/api.go":               "package api\n\nfunc Handle() {}\n",
		"api/api_test.go":          "package api\n\nimport \"testing\"\n\nfunc TestHandle(t *testing.T) {}\n",
		"service/svc.go":           "package service\n\nfunc Serve() {}\n",
		"service/deep/nested/x.go": "package nested\n\nfunc X() {}\n",
		"docs/guide.md":            "# guide\n",
		"gen/zz_generated.go":      "package gen\n\nfunc Generated() {}\n",
		"examples/demo/main.go":    "package main\n\nfunc main() {}\n",
	})
	inventory := loadInventory(t, newIndex(t, root), true)
	full := Build(inventory, BuildOptions{Level: LevelFile})
	shallow := Build(inventory, BuildOptions{Level: LevelModule})
	return full, shallow
}

// TestHierarchyMaterialisesOnlyRequestedLevels pins progressive disclosure: a
// module-resolution request must not carry directory or file nodes.
func TestHierarchyMaterialisesOnlyRequestedLevels(t *testing.T) {
	full, shallow := hierarchyFixture(t)
	if len(shallow.AtLevel(LevelPackage)) != 0 {
		t.Fatalf("module-resolution snapshot carries %d package nodes", len(shallow.AtLevel(LevelPackage)))
	}
	if len(shallow.AtLevel(LevelFile)) != 0 {
		t.Fatalf("module-resolution snapshot carries %d file nodes", len(shallow.AtLevel(LevelFile)))
	}
	if len(full.AtLevel(LevelFile)) == 0 {
		t.Fatal("file-resolution snapshot carries no file nodes")
	}
	api := nodeByPath(t, full, "api")
	if api.Level != LevelModule || api.Kind == "" {
		t.Fatalf("api node = %+v", api)
	}
	if api.Files != 2 || api.TestFiles != 1 {
		t.Errorf("api counts = files %d tests %d, want 2/1", api.Files, api.TestFiles)
	}
	nested := nodeAt(t, full, "service/deep", LevelPackage)
	if nested.Parent != "dir:service" {
		t.Fatalf("nested node parent = %q", nested.Parent)
	}
	generated := nodeByPath(t, full, "gen/zz_generated.go")
	if generated.Class != ClassGenerated {
		t.Errorf("generated file class = %s", generated.Class)
	}
	if generated.Classification.State != StateInferred {
		t.Errorf("generated classification state = %s, want INFERRED", generated.Classification.State)
	}
	if generated.Level != LevelFile {
		t.Errorf("generated file level = %s", generated.Level)
	}
}

// TestHierarchyCountsEveryLevelForSummaries pins that counts aggregate even
// when the node itself is not materialised: a project summary at module
// resolution still knows the file total.
func TestHierarchyCountsEveryLevelForSummaries(t *testing.T) {
	_, shallow := hierarchyFixture(t)
	workspace := nodeAt(t, shallow, ".", LevelWorkspace)
	if workspace.Kind != NodeWorkspace {
		t.Fatalf("workspace kind = %s", workspace.Kind)
	}
	if workspace.Files != 8 {
		t.Errorf("workspace files = %d, want 8", workspace.Files)
	}
	modules := Summaries(shallow, LevelModule, 0)
	if len(modules) != 5 {
		t.Fatalf("modules = %+v", modules)
	}
	for _, module := range modules {
		if module.Evidence.State != StateObserved {
			t.Errorf("module %s evidence = %s, want OBSERVED", module.Path, module.Evidence.State)
		}
	}
	if deep := nodeByPath(t, shallow, "service"); deep.Files != 2 {
		t.Errorf("service files = %d, want 2", deep.Files)
	}
}

// TestHierarchyIsDeterministic pins identical output for identical input, the
// property the cache depends on.
func TestHierarchyIsDeterministic(t *testing.T) {
	full, _ := hierarchyFixture(t)
	root := fixture(t, map[string]string{
		"go.mod":                   "module example.com/app\n\ngo 1.21\n",
		"api/api.go":               "package api\n\nfunc Handle() {}\n",
		"api/api_test.go":          "package api\n\nimport \"testing\"\n\nfunc TestHandle(t *testing.T) {}\n",
		"service/svc.go":           "package service\n\nfunc Serve() {}\n",
		"service/deep/nested/x.go": "package nested\n\nfunc X() {}\n",
		"docs/guide.md":            "# guide\n",
		"gen/zz_generated.go":      "package gen\n\nfunc Generated() {}\n",
		"examples/demo/main.go":    "package main\n\nfunc main() {}\n",
	})
	again := Build(loadInventory(t, newIndex(t, root), true), BuildOptions{Level: LevelFile})
	if !reflect.DeepEqual(full.Nodes, again.Nodes) || !reflect.DeepEqual(full.Coverage, again.Coverage) {
		t.Fatal("hierarchy build is not deterministic")
	}
}

// TestHierarchyClampsFileMaterialisation pins the bound: a large repository is
// summarised at directory resolution and says so, instead of emitting tens of
// thousands of file rows.
func TestHierarchyClampsFileMaterialisation(t *testing.T) {
	root := fixture(t, map[string]string{
		"go.mod":  "module example.com/app\n\ngo 1.21\n",
		"a/a.go":  "package a\n\nfunc A() {}\n",
		"a/a2.go": "package a\n\nfunc A2() {}\n",
		"b/b.go":  "package b\n\nfunc B() {}\n",
		"b/b2.go": "package b\n\nfunc B2() {}\n",
	})
	inventory := loadInventory(t, newIndex(t, root), true)
	snapshot := Build(inventory, BuildOptions{Level: LevelFile, MaxFiles: 2})
	if snapshot.Level >= LevelFile {
		t.Fatalf("level = %s, want a directory resolution", snapshot.Level)
	}
	if snapshot.Coverage.Level != snapshot.Level {
		t.Errorf("coverage level %s disagrees with published level %s",
			snapshot.Coverage.Level, snapshot.Level)
	}
	if len(snapshot.AtLevel(LevelFile)) != 0 {
		t.Fatal("clamped snapshot materialised file nodes")
	}
	if !snapshot.Coverage.Truncated {
		t.Error("clamped build did not disclose truncation")
	}
	if !hasIssue(snapshot.Issues, "truncated_analysis") {
		t.Errorf("issues = %+v", snapshot.Issues)
	}
	module := nodeByPath(t, snapshot, "a")
	if module.Evidence.State != StatePartiallyObserved {
		t.Errorf("truncated container evidence = %s, want PARTIALLY_OBSERVED", module.Evidence.State)
	}
}

// TestEmptyRepositoryIsObservedNotUnknown pins the empty-project contract: zero
// files in a complete scope is an observation, not an analysis failure.
// TestSymbolLevelIsAnsweredByFocusNotByFakeNodes pins resolution honesty: a
// request for symbol resolution must not publish a level it did not materialise.
// Symbol and relationship detail comes from the bounded focal projection.
func TestSymbolLevelIsAnsweredByFocusNotByFakeNodes(t *testing.T) {
	root := fixture(t, map[string]string{
		"go.mod":     "module example.com/app\n\ngo 1.21\n",
		"api/api.go": "package api\n\nfunc Handle() {}\n",
	})
	inventory := loadInventory(t, newIndex(t, root), true)
	snapshot := Build(inventory, BuildOptions{Level: LevelSymbol})
	if snapshot.Level != LevelFile {
		t.Fatalf("level = %s, want file (the deepest materialised)", snapshot.Level)
	}
	if len(snapshot.AtLevel(LevelSymbol)) != 0 {
		t.Fatal("symbol-level nodes were invented")
	}
}

// TestNestedRepositoryBecomesAWorkspace pins multi-repository structure: an
// included nested project is a workspace, not a directory whose parent is the
// enclosing project.
func TestNestedRepositoryBecomesAWorkspace(t *testing.T) {
	inventory := ast.Inventory{Snapshot: ast.SnapshotInfo{
		Root: "/repo", Module: "example.com/app", SnapshotID: "nested",
		Files: 2, SourceFiles: 2, ParsedFiles: 2,
		ScopeComplete: true, InventoryComplete: true, StructuralComplete: true,
		NestedProjects: []ast.ProjectBoundary{{Path: "backend", Included: true}},
	}}
	inventory.Files = []ast.FileInfo{
		{Path: "main.go", Dir: ".", Language: "go", Kind: ast.FileGoSource, Parsed: true, Symbols: 1},
		{Path: "backend/svc.go", Dir: "backend", Language: "go", Kind: ast.FileGoSource, Parsed: true, Symbols: 1},
	}
	snapshot := Build(inventory, BuildOptions{Level: LevelFile})
	root := nodeAt(t, snapshot, ".", LevelWorkspace)
	nested := nodeAt(t, snapshot, "backend", LevelWorkspace)
	if nested.Parent != root.Parent {
		t.Errorf("nested workspace parent = %q, want project %q", nested.Parent, root.Parent)
	}
	file := nodeAt(t, snapshot, "backend/svc.go", LevelFile)
	if file.Parent != nested.ID {
		t.Fatalf("nested file parent = %q, want %q", file.Parent, nested.ID)
	}
	rootFile := nodeAt(t, snapshot, "main.go", LevelFile)
	if rootFile.Parent != root.ID {
		t.Fatalf("root file parent = %q, want %q", rootFile.Parent, root.ID)
	}
}

func TestEmptyRepositoryIsObservedNotUnknown(t *testing.T) {
	root := t.TempDir()
	snapshot := Build(loadInventory(t, newIndex(t, root), false), BuildOptions{Level: LevelFile})
	if len(snapshot.Nodes) != 2 {
		t.Fatalf("nodes = %+v", snapshot.Nodes)
	}
	if nodeAt(t, snapshot, ".", LevelProject).Kind != "project" {
		t.Errorf("missing project node: %+v", snapshot.Nodes)
	}
	if nodeAt(t, snapshot, ".", LevelWorkspace).Kind != NodeWorkspace {
		t.Errorf("missing workspace node: %+v", snapshot.Nodes)
	}
	if snapshot.Coverage.State != StateObserved {
		t.Errorf("coverage = %s, want OBSERVED", snapshot.Coverage.State)
	}
	if !hasIssue(snapshot.Issues, "empty_inventory") {
		t.Errorf("issues = %+v", snapshot.Issues)
	}
}
