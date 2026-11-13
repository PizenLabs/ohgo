package workspace

import "testing"

// A technology destination is a semantic objective, never a file path. The
// ".js" spelling must not make "Next.js" a target the migration has to change
// by that name.
func TestMigrationTechnologyIsNotAPathTarget(t *testing.T) {
	for _, text := range []string{
		"ok, convert this project to Next.js help me",
		"convert this project to Next.js",
		"migrate the project to Node.js",
		"convert the project to React",
	} {
		t.Run(text, func(t *testing.T) {
			intent := BuildIntentContract(text)
			if intent.Kind != ObjectiveMutation {
				t.Fatalf("kind = %s, want mutation", intent.Kind)
			}
			for _, target := range intent.Targets {
				if target.Kind == TargetPath {
					t.Fatalf("technology became a path target: %+v", intent.Targets)
				}
			}
			var technology string
			for _, target := range intent.Targets {
				if target.Kind == TargetTechnology {
					technology = target.Value
				}
			}
			if technology == "" {
				t.Fatalf("no technology target in %+v", intent.Targets)
			}
			if intent.MutationScope.Kind != MutationScopeWorkspace {
				t.Fatalf("mutation scope = %+v, want workspace", intent.MutationScope)
			}
			if intent.Ambiguous {
				t.Fatalf("technology objective was reported ambiguous: %v", intent.Issues)
			}
		})
	}
}

// A literal path keeps its filesystem meaning and narrows the mutation scope.
func TestPathTargetStillNarrowsMutationScope(t *testing.T) {
	fileIntent := BuildIntentContract("update app/page.tsx")
	if fileIntent.MutationScope.Kind != MutationScopePaths {
		t.Fatalf("file scope = %+v, want paths", fileIntent.MutationScope)
	}
	if len(fileIntent.MutationScope.Files) != 1 || fileIntent.MutationScope.Files[0] != "app/page.tsx" {
		t.Fatalf("file scope = %+v, want app/page.tsx", fileIntent.MutationScope)
	}

	dirIntent := BuildIntentContract("add files under app/")
	if dirIntent.MutationScope.Kind != MutationScopePaths {
		t.Fatalf("directory scope = %+v, want paths", dirIntent.MutationScope)
	}
	if len(dirIntent.MutationScope.Directories) != 1 || dirIntent.MutationScope.Directories[0] != "app" {
		t.Fatalf("directory scope = %+v, want app", dirIntent.MutationScope)
	}
}

// A named file inside a repository migration is part of the workspace scope,
// not a boundary that makes every other migrated file unexpected.
func TestRepositoryMigrationKeepsWorkspaceScopeWithNamedFile(t *testing.T) {
	intent := BuildIntentContract("convert this project to Next.js and update package.json")
	if intent.MutationScope.Kind != MutationScopeWorkspace {
		t.Fatalf("migration scope = %+v, want workspace", intent.MutationScope)
	}
}

// An informational migration question may name the technology, but it must not
// acquire a mutable path target from the technology's spelling.
func TestInformationalMigrationKeepsSemanticTargetOnly(t *testing.T) {
	intent := BuildIntentContract("How to convert this project to Next.js?")
	if intent.Kind != ObjectiveInformational {
		t.Fatalf("kind = %s, want informational", intent.Kind)
	}
	for _, target := range intent.Targets {
		if target.Kind == TargetPath {
			t.Fatalf("question acquired a path target: %+v", intent.Targets)
		}
	}
}
