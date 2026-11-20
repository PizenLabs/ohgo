package ast

import (
	"reflect"
	"testing"
)

// Sibling Go modules have no single owner. The index must not choose one by
// directory order; it reports the ambiguity so a caller cannot mistake the
// missing cross-module edges for a complete dependency graph.
func TestSiblingGoModulesAreReportedAsAmbiguous(t *testing.T) {
	root := writeNestedModule(t, map[string]string{
		"a/go.mod": "module example.com/a\n\ngo 1.21\n",
		"a/a.go":   "package a\n\nfunc A() {}\n",
		"b/go.mod": "module example.com/b\n\ngo 1.21\n",
		"b/b.go":   "package b\n\nfunc B() {}\n",
	})
	idx := mustIndex(t, root)
	info := idx.Info()
	if info.Module != "" {
		t.Fatalf("module = %q, want no single module for sibling layouts", info.Module)
	}
	if !info.ModuleAmbiguous {
		t.Fatal("sibling modules were not reported as ambiguous")
	}
	if want := []string{"a", "b"}; !reflect.DeepEqual(info.ModuleCandidates, want) {
		t.Fatalf("candidates = %v, want %v", info.ModuleCandidates, want)
	}
}

// A single nested module remains unambiguous. The sibling rule must not
// regress the layout the prior fix already handles.
func TestSingleNestedModuleStaysUnambiguous(t *testing.T) {
	root := writeNestedModule(t, map[string]string{
		"backend/go.mod": "module example.com/app/backend\n\ngo 1.21\n",
		"backend/a.go":   "package backend\n\nfunc A() {}\n",
	})
	info := mustIndex(t, root).Info()
	if info.Module != "example.com/app/backend" || info.ModuleAmbiguous {
		t.Fatalf("module info = %+v, want the single nested module", info)
	}
}
