package tui

import (
	"context"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// /model is a stale-while-revalidate surface: the cached snapshot must become
// a usable picker before any provider discovery or catalog fetch starts. These
// tests use a refresh command that blocks on a channel, so a regression that
// puts refresh work back on the open path hangs deterministically instead of
// depending on a timing sleep.

// refreshOptions is a small roster with stable ids for refresh tests.
func refreshOptions() []ModelOption {
	return []ModelOption{
		{ID: "a/1", Provider: "a", Title: "Alpha", Detail: "remote"},
		{ID: "b/2", Provider: "b", Title: "Beta", Detail: "remote"},
		{ID: "c/3", Provider: "c", Title: "Gamma", Detail: "remote"},
	}
}

// installRefreshControl wires a program that always renders from a cached
// snapshot and reports provider/role panes locally.
func installRefreshControl(p *Program, snapshot func() CatalogSnapshot, refresh func(context.Context, int) tea.Cmd) {
	p.SetCatalogHooks(CatalogHooks{Snapshot: snapshot, Refresh: refresh})
	p.SetModelControl(ModelControlHooks{
		Providers: func() []ProviderRow {
			return []ProviderRow{
				{ID: AllProviders, Name: "All providers", Count: 3},
				{ID: "a", Name: "A", Count: 1},
				{ID: "b", Name: "B", Count: 1},
				{ID: "c", Name: "C", Count: 1},
			}
		},
		Roles:      func() []RoleAssignment { return []RoleAssignment{{Role: "build", Model: "a/1"}} },
		ActiveRole: func() string { return "build" },
		Assign: func(update RoleAssignment) (RoleAssignment, []RoleAssignment, error) {
			return update, []RoleAssignment{{Role: "build", Model: update.Model}}, nil
		},
	})
}

func TestModelControlShowsCachedSnapshotBeforeBlockingRefresh(t *testing.T) {
	p := NewProgram(nil, "a/1", 0)
	cached := refreshOptions()
	started := make(chan struct{})
	release := make(chan struct{})
	installRefreshControl(p,
		func() CatalogSnapshot {
			return CatalogSnapshot{Options: cached, Origin: "cached", Stale: true}
		},
		func(_ context.Context, seq int) tea.Cmd {
			return func() tea.Msg {
				close(started)
				<-release
				return NewModelsRefreshedMsg(seq, CatalogSnapshot{Options: cached, Origin: "live"}, "")
			}
		},
	)

	cmd := p.openModelControl()
	if p.state.Plane == nil {
		t.Fatal("/model did not open the control plane")
	}
	if len(p.state.Plane.Models) != len(cached) {
		t.Fatalf("models = %d, want the cached snapshot rendered before refresh", len(p.state.Plane.Models))
	}
	if !p.state.Plane.Refreshing {
		t.Error("the subtle in-flight indicator is missing while cached rows are shown")
	}
	if cmd == nil {
		t.Fatal("no background refresh was issued after the picker opened")
	}

	// The pane is already usable: navigate before the refresh produces a
	// single byte. A blocking refresh must not freeze the UI.
	p.state.Apply(PlaneFocusSet{Focus: PlaneFocusModels})
	p.Update(tea.KeyMsg{Type: tea.KeyDown})
	selected, ok := p.state.Plane.ChosenModel()
	if !ok || selected.ID != "b/2" {
		t.Fatalf("chosen = %+v, want navigation to work while refreshing", selected)
	}

	done := make(chan tea.Msg, 1)
	go func() { done <- cmd() }()
	<-started
	close(release)
	p.Update(<-done)
	if p.state.Plane.Refreshing {
		t.Error("the in-flight indicator stuck after the refresh completed")
	}
}

func TestModelControlOpenKeepsTheRuntimeSelection(t *testing.T) {
	p := NewProgram(nil, "b/2", 0)
	switched := 0
	p.SetModelSwitcher(
		func(string) error { switched++; return nil },
		func() string { return "b/2" },
	)
	installRefreshControl(p,
		func() CatalogSnapshot {
			return CatalogSnapshot{Options: refreshOptions(), Origin: "cached", Stale: true}
		},
		func(context.Context, int) tea.Cmd { return nil },
	)

	p.openModelControl()
	if switched != 0 {
		t.Errorf("opening /model switched the model %d times, want none", switched)
	}
	if p.state.Status.Model != "b/2" {
		t.Errorf("status model = %q, want the runtime model untouched", p.state.Status.Model)
	}
	chosen, ok := p.state.Plane.ChosenModel()
	if !ok || chosen.ID != "b/2" {
		t.Errorf("highlighted = %+v, want the active runtime model", chosen)
	}
}

func TestModelControlRefreshReplacesStaleRowsAndKeepsSelection(t *testing.T) {
	p := NewProgram(nil, "a/1", 0)
	cached := refreshOptions()
	installRefreshControl(p,
		func() CatalogSnapshot { return CatalogSnapshot{Options: cached, Origin: "cached", Stale: true} },
		func(context.Context, int) tea.Cmd { return nil },
	)
	p.openModelControl()
	p.state.Apply(PlaneFocusSet{Focus: PlaneFocusModels})
	p.Update(tea.KeyMsg{Type: tea.KeyDown})
	before, ok := p.state.Plane.ChosenModel()
	if !ok || before.ID != "b/2" {
		t.Fatalf("chosen = %+v, want b/2 before the refresh", before)
	}

	fresh := []ModelOption{
		{ID: "a/1", Provider: "a", Title: "Alpha", Detail: "updated"},
		{ID: "b/2", Provider: "b", Title: "Beta", Detail: "remote"},
		{ID: "d/4", Provider: "d", Title: "Delta", Detail: "remote"},
	}
	p.Update(NewModelsRefreshedMsg(p.modelSeq, CatalogSnapshot{Options: fresh, Origin: "live"}, ""))

	if len(p.state.Plane.Models) != len(fresh) {
		t.Fatalf("models = %d, want the refreshed roster", len(p.state.Plane.Models))
	}
	foundNew := false
	for _, model := range p.state.Plane.Models {
		if model.ID == "d/4" {
			foundNew = true
		}
	}
	if !foundNew {
		t.Error("a newly discovered model did not replace the stale snapshot")
	}
	after, ok := p.state.Plane.ChosenModel()
	if !ok || after.ID != before.ID {
		t.Errorf("chosen = %+v, want the user's selection %q preserved", after, before.ID)
	}
	if p.state.Plane.Origin != "live" {
		t.Errorf("origin = %q, want the refreshed freshness", p.state.Plane.Origin)
	}
}

func TestModelControlUnchangedRefreshLeavesRowsUntouched(t *testing.T) {
	p := NewProgram(nil, "a/1", 0)
	cached := refreshOptions()
	installRefreshControl(p,
		func() CatalogSnapshot { return CatalogSnapshot{Options: cached, Origin: "cached", Stale: true} },
		func(context.Context, int) tea.Cmd { return nil },
	)
	p.openModelControl()
	p.state.Apply(PlaneFocusSet{Focus: PlaneFocusModels})
	p.Update(tea.KeyMsg{Type: tea.KeyDown})
	beforeCursor := p.state.Plane.ModelCursor
	beforeFirst := &p.state.Plane.Models[0]

	// Revalidation changes provenance, not facts: every row is now live, but
	// ids, details, and capabilities are identical. The list must not rebuild.
	revalidated := append([]ModelOption(nil), cached...)
	for i := range revalidated {
		revalidated[i].Origin = "live"
	}
	p.Update(NewModelsRefreshedMsg(p.modelSeq, CatalogSnapshot{Options: revalidated, Origin: "live"}, ""))

	if p.state.Plane.ModelCursor != beforeCursor {
		t.Errorf("cursor = %d, want %d: an unchanged roster must not move the user", p.state.Plane.ModelCursor, beforeCursor)
	}
	if &p.state.Plane.Models[0] != beforeFirst {
		t.Error("an unchanged roster rebuilt the list instead of leaving it untouched")
	}
}

func TestModelControlReusesAnInFlightRefresh(t *testing.T) {
	p := NewProgram(nil, "a/1", 0)
	cached := refreshOptions()
	calls := 0
	started := make(chan struct{})
	release := make(chan struct{})
	installRefreshControl(p,
		func() CatalogSnapshot { return CatalogSnapshot{Options: cached, Origin: "cached", Stale: true} },
		func(_ context.Context, seq int) tea.Cmd {
			calls++
			return func() tea.Msg {
				close(started)
				<-release
				return NewModelsRefreshedMsg(seq, CatalogSnapshot{Options: cached, Origin: "live"}, "")
			}
		},
	)

	first := p.openModelControl()
	if first == nil || calls != 1 {
		t.Fatalf("first open: cmd=%v calls=%d, want one refresh", first, calls)
	}
	// Reopening while the first refresh is still in flight must reuse it, not
	// spawn an uncontrolled second fetch.
	if cmd := p.openModelControl(); cmd != nil || calls != 1 {
		t.Fatalf("second open: cmd=%v calls=%d, want the in-flight refresh reused", cmd, calls)
	}
	if !p.state.Plane.Refreshing {
		t.Error("the reopened pane does not show the in-flight refresh")
	}

	done := make(chan tea.Msg, 1)
	go func() { done <- first() }()
	<-started
	close(release)
	p.Update(<-done)

	if cmd := p.openModelControl(); cmd == nil || calls != 2 {
		t.Fatalf("third open: cmd=%v calls=%d, want a fresh refresh after completion", cmd, calls)
	}
}

func TestModelPickerCacheMissOpensLoadingWithoutBlocking(t *testing.T) {
	p := NewProgram(nil, "a/1", 0)
	release := make(chan struct{})
	var started chan struct{}
	p.SetCatalogHooks(CatalogHooks{
		// A cache miss may carry no origin at all: the picker must still fall
		// back to its existing loading state instead of refusing to open.
		Snapshot: func() CatalogSnapshot { return CatalogSnapshot{Stale: true} },
		Refresh: func(_ context.Context, seq int) tea.Cmd {
			started = make(chan struct{})
			return func() tea.Msg {
				close(started)
				<-release
				return NewModelsRefreshedMsg(seq, CatalogSnapshot{
					Options: []ModelOption{{ID: "a/1", Provider: "a", Title: "Alpha"}},
					Origin:  "live",
				}, "")
			}
		},
	})

	cmd := p.openModelPicker()
	if p.state.Picker == nil {
		t.Fatal("a cache miss must still open a usable picker")
	}
	if !p.state.Picker.Loading {
		t.Error("the empty picker does not show the existing loading fallback")
	}
	if cmd == nil {
		t.Fatal("a cache miss did not request the asynchronous hydration")
	}
	done := make(chan tea.Msg, 1)
	go func() { done <- cmd() }()
	<-started
	// The picker is usable while the refresh is blocked: dismiss it without
	// waiting for the catalog.
	p.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if p.state.Picker != nil {
		t.Error("the loading picker could not be dismissed while refreshing")
	}
	close(release)
	<-done
}

func TestModelRefreshIsCancelledAtShutdown(t *testing.T) {
	p := NewProgram(nil, "a/1", 0)
	var refreshCtx context.Context
	installRefreshControl(p,
		func() CatalogSnapshot {
			return CatalogSnapshot{Options: refreshOptions(), Origin: "cached", Stale: true}
		},
		func(ctx context.Context, seq int) tea.Cmd {
			refreshCtx = ctx
			return func() tea.Msg {
				return NewModelsRefreshedMsg(seq, CatalogSnapshot{Options: refreshOptions(), Origin: "live"}, "")
			}
		},
	)
	p.openModelControl()
	if refreshCtx == nil {
		t.Fatal("no context reached the refresh hook")
	}
	p.closeDone()
	select {
	case <-refreshCtx.Done():
	default:
		t.Error("the program lifetime did not cancel the in-flight catalog refresh")
	}
}
