package tui

import (
	"context"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// Model-picker regression tests: keyboard, scrolling, search, refresh, and
// layout. Each drives the state machine or Update directly — no terminal.

// testModels builds a roster with provider identities, pricing flags, and
// context, mirroring what catalogSnapshot produces.
func testModels() []PickerItem {
	return []PickerItem{
		{Title: "OX Alpha (opencode-go/ox-alpha-free)", Detail: "free · remote", Value: "opencode-go/ox-alpha-free", Provider: "opencode-go", Name: "OX Alpha", Free: true, PriceKnown: true, Context: 128000},
		{Title: "Longcat (opencode-go/longcat-2.5-preview-free)", Detail: "remote", Value: "opencode-go/longcat-2.5-preview-free", Provider: "opencode-go", Name: "Longcat"},
		{Title: "DeepSeek V4 Flash (opencode-zen/deepseek-v4-flash-free)", Detail: "free · remote", Value: "opencode-zen/deepseek-v4-flash-free", Provider: "opencode-zen", Name: "DeepSeek V4 Flash", Free: true, PriceKnown: true, Context: 256000},
		{Title: "Qwen Plus (opencode-zen/qwen3.6-plus-free)", Detail: "remote", Value: "opencode-zen/qwen3.6-plus-free", Provider: "opencode-zen", Name: "Qwen Plus", Free: true, PriceKnown: true, Context: 128000},
		{Title: "Claude Sonnet (openrouter/anthropic/claude-sonnet-4-5)", Detail: "$3.00/$15.00 per 1M · remote · reasoning", Value: "openrouter/anthropic/claude-sonnet-4-5", Provider: "openrouter", Name: "Claude Sonnet", PriceKnown: true, Context: 1000000},
		{Title: "DeepSeek V4 (openrouter/deepseek/deepseek-v4-free)", Detail: "free · remote", Value: "openrouter/deepseek/deepseek-v4-free", Provider: "openrouter", Name: "DeepSeek V4", Free: true, PriceKnown: true, Context: 128000},
	}
}

// openModelsPicker opens a models picker with rows for tests.
func openModelsPicker(s *State, items []PickerItem) {
	s.Apply(PickerOpened{Kind: PickerModels, Title: "models", Items: items})
}

func keyMsg(t tea.KeyType) tea.KeyMsg { return tea.KeyMsg{Type: t} }

func TestPickerUpDownScrollsWindow(t *testing.T) {
	s := New()
	// Six models in a viewport that fits four result rows: every step
	// must keep the marker inside the scrolled window.
	s.Apply(TerminalResized{Width: 120, Height: 10})
	openModelsPicker(&s, testModels())
	rows := s.Picker.VisibleRows
	if rows <= 0 || rows >= len(testModels()) {
		t.Fatalf("VisibleRows = %d, want a scrolled window over %d rows", rows, len(testModels()))
	}
	// Walk past the window: the marker must stay inside it every step.
	for i := 0; i < len(testModels())*2; i++ {
		s.Apply(PickerMoved{Delta: 1})
		cursor, offset := s.Picker.Cursor, s.Picker.Offset
		if cursor < offset || cursor >= offset+rows {
			t.Fatalf("step %d: cursor %d outside window [%d,%d)", i, cursor, offset, offset+rows)
		}
	}
	// Walk back up: same invariant in reverse.
	for i := 0; i < len(testModels())*2; i++ {
		s.Apply(PickerMoved{Delta: -1})
		cursor, offset := s.Picker.Cursor, s.Picker.Offset
		if cursor < offset || cursor >= offset+rows {
			t.Fatalf("up %d: cursor %d outside window [%d,%d)", i, cursor, offset, offset+rows)
		}
	}
}

func TestPickerMarkerRendersInViewport(t *testing.T) {
	s := New()
	s.Apply(TerminalResized{Width: 120, Height: 24})
	openModelsPicker(&s, testModels())
	for i := 0; i < len(testModels()); i++ {
		s.Apply(PickerMoved{Delta: 1})
	}
	out := Render(s)
	lines := strings.Split(out, "\n")
	markers := 0
	for _, line := range lines {
		if strings.Contains(line, "> ") && !strings.Contains(line, "Search") {
			markers++
		}
	}
	if markers != 1 {
		t.Errorf("rendered %d selection markers, want exactly 1 in the viewport:\n%s", markers, out)
	}
}

func TestPickerPageHomeEnd(t *testing.T) {
	s := New()
	s.Apply(TerminalResized{Width: 120, Height: 24})
	var many []PickerItem
	for i := 0; i < 50; i++ {
		many = append(many, PickerItem{Title: "model", Value: "p/m" + strings.Repeat("x", i)})
	}
	openModelsPicker(&s, many)
	rows := s.Picker.VisibleRows
	s.Apply(PickerPaged{Delta: 1})
	if s.Picker.Cursor != rows {
		t.Errorf("page down cursor = %d, want one viewport (%d)", s.Picker.Cursor, rows)
	}
	s.Apply(PickerBottom{})
	if s.Picker.Cursor != 49 {
		t.Errorf("end cursor = %d, want the last row", s.Picker.Cursor)
	}
	// Paging past the end parks, never wraps.
	s.Apply(PickerPaged{Delta: 1})
	if s.Picker.Cursor != 49 {
		t.Errorf("page past end cursor = %d, want it parked at the last row", s.Picker.Cursor)
	}
	s.Apply(PickerTop{})
	if s.Picker.Cursor != 0 || s.Picker.Offset != 0 {
		t.Errorf("home cursor = %d offset = %d, want the first row", s.Picker.Cursor, s.Picker.Offset)
	}
	s.Apply(PickerPaged{Delta: -1})
	if s.Picker.Cursor != 0 {
		t.Errorf("page past start cursor = %d, want it parked at the first row", s.Picker.Cursor)
	}
}

func TestPickerFilterWhileScrolledClamps(t *testing.T) {
	s := New()
	s.Apply(TerminalResized{Width: 120, Height: 12})
	var many []PickerItem
	for i := 0; i < 50; i++ {
		provider := "openrouter"
		if i%2 == 0 {
			provider = "opencode-zen"
		}
		many = append(many, PickerItem{
			Title: "Model " + itoa(i), Value: "p/m" + itoa(i), Provider: provider,
		})
	}
	openModelsPicker(&s, many)
	s.Apply(PickerBottom{})
	if s.Picker.Offset == 0 {
		t.Fatal("precondition: bottom of a 50-row list must scroll")
	}
	s.Apply(PickerFilterChanged{Text: "openrouter"})
	n := len(s.Picker.Filtered())
	if n == 0 || n >= 50 {
		t.Fatalf("filtered = %d rows, want a narrowed non-empty set", n)
	}
	if s.Picker.Cursor >= n {
		t.Errorf("cursor = %d over %d rows: filtering under a bottom cursor must clamp", s.Picker.Cursor, n)
	}
	rows := s.Picker.VisibleRows
	if s.Picker.Cursor < s.Picker.Offset || (rows > 0 && s.Picker.Cursor >= s.Picker.Offset+rows) {
		t.Errorf("cursor = %d outside window [%d,%d): the highlight must stay visible",
			s.Picker.Cursor, s.Picker.Offset, s.Picker.Offset+rows)
	}
}

func TestPickerEmptyAndRestore(t *testing.T) {
	s := New()
	openModelsPicker(&s, testModels())
	s.Apply(PickerFilterChanged{Text: "zzz-no-such-model"})
	if len(s.Picker.Filtered()) != 0 {
		t.Fatalf("filtered = %v, want empty", s.Picker.Filtered())
	}
	if _, ok := s.Picker.Chosen(); ok {
		t.Error("an empty result set must not yield a choice")
	}
	if out := Render(s); !strings.Contains(out, "no matches") {
		t.Errorf("empty state missing from render: %q", out)
	}
	s.Apply(PickerFilterCleared{})
	if len(s.Picker.Filtered()) != len(testModels()) {
		t.Errorf("cleared = %d rows, want the full roster back", len(s.Picker.Filtered()))
	}
	if s.Picker.Cursor != 0 {
		t.Errorf("cursor = %d, want the top after clearing", s.Picker.Cursor)
	}
}

func TestPickerSpaceInSearch(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	p.state.Apply(PickerOpened{Kind: PickerModels, Title: "models", Items: testModels()})
	for _, msg := range []tea.KeyMsg{
		{Type: tea.KeyRunes, Runes: []rune("deepseek")},
		{Type: tea.KeySpace},
		{Type: tea.KeyRunes, Runes: []rune("free")},
	} {
		p.Update(msg)
	}
	if p.state.Picker.Filter != "deepseek free" {
		t.Fatalf("filter = %q, want the space preserved", p.state.Picker.Filter)
	}
	if len(p.state.Picker.Filtered()) == 0 {
		t.Error("multiword search matched nothing: tokens must AND across fields")
	}
	for _, item := range p.state.Picker.Filtered() {
		hay := strings.ToLower(item.Title + " " + item.Detail + " " + item.Value + " " + item.Name)
		if !strings.Contains(hay, "deepseek") || (!strings.Contains(hay, "free") && (!item.PriceKnown || !item.Free)) {
			t.Errorf("item %+v does not match both tokens", item)
		}
	}
}

func TestPickerSearchIsCaseInsensitiveAndPunctuated(t *testing.T) {
	s := New()
	openModelsPicker(&s, testModels())
	s.Apply(PickerFilterChanged{Text: "DEEPSEEK-V4"})
	if len(s.Picker.Filtered()) == 0 {
		t.Error("mixed-case hyphenated query matched nothing")
	}
	s.Apply(PickerFilterChanged{Text: "openrouter/anthropic"})
	if len(s.Picker.Filtered()) != 1 {
		t.Errorf("provider-qualified query matched %d rows, want 1", len(s.Picker.Filtered()))
	}
}

func TestPickerProviderPrefixComposes(t *testing.T) {
	s := New()
	openModelsPicker(&s, testModels())
	s.Apply(PickerFilterChanged{Text: "provider:openrouter free"})
	for _, item := range s.Picker.Filtered() {
		if item.Provider != "openrouter" {
			t.Errorf("item %+v escaped the provider pin", item)
		}
	}
	if scope := s.Picker.ProviderScope(); scope != "openrouter" {
		t.Errorf("scope = %q, want the pinned provider for the header", scope)
	}
}

func TestPickerFreeUsesVerifiedPricing(t *testing.T) {
	s := New()
	openModelsPicker(&s, testModels())
	s.Apply(PickerFilterChanged{Text: "free"})
	for _, item := range s.Picker.Filtered() {
		if item.PriceKnown && !item.Free {
			t.Errorf("priced non-free row %+v matched \"free\"", item)
		}
	}
	// The unpriced longcat row matches via the name fallback, not a claim.
	found := false
	for _, item := range s.Picker.Filtered() {
		if strings.Contains(item.Value, "longcat") {
			found = true
		}
	}
	if !found {
		t.Error("the unpriced -free row should match by name fallback")
	}
}

func TestPickerFilterCursorEditing(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	p.state.Apply(PickerOpened{Kind: PickerModels, Title: "models", Items: testModels()})
	p.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("ab")})
	p.Update(keyMsg(tea.KeyLeft))
	p.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("X")})
	if p.state.Picker.Filter != "aXb" {
		t.Errorf("filter = %q, want mid-text insertion", p.state.Picker.Filter)
	}
	p.Update(keyMsg(tea.KeyBackspace))
	if p.state.Picker.Filter != "ab" {
		t.Errorf("filter = %q, want backspace before the cursor", p.state.Picker.Filter)
	}
	p.Update(keyMsg(tea.KeyLeft))
	p.Update(keyMsg(tea.KeyLeft))
	p.Update(keyMsg(tea.KeyDelete))
	if p.state.Picker.Filter != "b" {
		t.Errorf("filter = %q, want delete under the cursor", p.state.Picker.Filter)
	}
}

func TestPickerEscClearsThenCloses(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	p.state.Apply(PickerOpened{Kind: PickerModels, Title: "models", Items: testModels()})
	p.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	p.Update(keyMsg(tea.KeyEsc))
	if p.state.Picker == nil {
		t.Fatal("first esc closed a non-empty query: it must clear first")
	}
	if p.state.Picker.Filter != "" {
		t.Errorf("filter = %q, want it cleared", p.state.Picker.Filter)
	}
	p.Update(keyMsg(tea.KeyEsc))
	if p.state.Picker != nil {
		t.Error("second esc must close the picker")
	}
}

func TestPickerCtrlCClosesImmediately(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	p.state.Apply(PickerOpened{Kind: PickerModels, Title: "models", Items: testModels()})
	p.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	p.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if p.state.Picker != nil {
		t.Error("ctrl+c must close without stopping at clear")
	}
}

func TestPickerCloseRestoresSlashDraft(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	p.state.Apply(ComposerChanged{Text: "/mod", Cursor: 4})
	// Dismiss the inline palette so Tab reaches the command & help center
	// (the inline palette owns Tab while it is visible).
	p.state.PaletteHide = true
	p.Update(keyMsg(tea.KeyTab))
	if p.state.HelpModal == nil {
		t.Fatal("no help center opened")
	}
	// First esc clears the carried filter; second closes and restores.
	p.Update(keyMsg(tea.KeyEsc))
	p.Update(keyMsg(tea.KeyEsc))
	if p.state.HelpModal != nil {
		t.Fatal("help center did not close")
	}
	if p.state.Input != "/mod" {
		t.Errorf("input = %q, want the typed slash text restored", p.state.Input)
	}
}

func TestPickerEnterSelectsExactID(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	var switched string
	p.SetModelSwitcher(func(id string) error { switched = id; return nil }, func() string { return switched })
	p.state.Apply(PickerOpened{Kind: PickerModels, Title: "models", Items: testModels()})
	p.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("openrouter/anthropic")})
	p.Update(keyMsg(tea.KeyEnter))
	if switched != "openrouter/anthropic/claude-sonnet-4-5" {
		t.Errorf("switched = %q, want the canonical provider-qualified id", switched)
	}
	if p.state.Picker != nil {
		t.Error("the picker must close after choosing")
	}
}

func TestPickerSetItemsPreservesSelection(t *testing.T) {
	s := New()
	openModelsPicker(&s, testModels())
	s.Apply(PickerFilterChanged{Text: "deepseek v4"})
	if _, ok := s.Picker.Chosen(); !ok {
		t.Skip("no rows to preserve")
	}
	before, _ := s.Picker.Chosen()
	s.Picker.SetItems(append(testModels(), PickerItem{Title: "New (openrouter/new/model)", Value: "openrouter/new/model", Provider: "openrouter", Name: "New"}))
	after, ok := s.Picker.Chosen()
	if !ok || after.Value != before.Value {
		t.Errorf("chosen = %+v, want the highlight to survive a refresh (%+v)", after, before)
	}
	// A refresh that drops the selected model clamps to a valid row.
	s.Picker.ClearFilter()
	kept := testModels()[:1]
	s.Picker.SetItems(kept)
	if _, ok := s.Picker.Chosen(); !ok {
		t.Error("no valid choice after the selected model vanished")
	}
}

func TestPickerStaleRefreshDropped(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	p.state.Apply(PickerOpened{Kind: PickerModels, Title: "models", Items: testModels()})
	p.modelSeq = 2
	p.Update(modelsRefreshedMsg{seq: 1, snapshot: CatalogSnapshot{Options: []ModelOption{{ID: "x/y"}}, Origin: "live"}})
	if len(p.state.Picker.Items) != len(testModels()) {
		t.Error("an older, slower refresh overwrote newer rows")
	}
}

func TestPickerFailedRefreshKeepsRows(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	p.state.Apply(PickerOpened{Kind: PickerModels, Title: "models", Items: testModels()})
	p.modelSeq = 3
	p.Update(modelsRefreshedMsg{seq: 3, errMsg: "offline"})
	if len(p.state.Picker.Items) != len(testModels()) {
		t.Error("a failed refresh erased valid cached rows")
	}
	if !strings.Contains(Render(p.state), "offline") {
		t.Error("the failure reason is not shown in the picker status")
	}
}

func TestPickerPartialRefreshAppliesLiveRows(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	p.state.Apply(PickerOpened{Kind: PickerModels, Title: "models", Items: testModels()})
	p.modelSeq = 4
	live := CatalogSnapshot{
		Options: []ModelOption{
			{ID: "openrouter/new/model", Provider: "openrouter", Title: "New", PriceKnown: true},
		},
		Origin: "live",
	}
	p.Update(modelsRefreshedMsg{seq: 4, snapshot: live, errMsg: "ollama: offline"})
	found := false
	for _, item := range p.state.Picker.Items {
		if item.Value == "openrouter/new/model" {
			found = true
		}
	}
	if !found {
		t.Error("live rows from a partial refresh were discarded with the failure")
	}
	if p.state.Picker.ErrMsg == "" {
		t.Error("the failed provider's reason must still show")
	}
}

func TestPickerRefreshGuard(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	called := 0
	p.SetCatalogHooks(CatalogHooks{
		Snapshot: func() CatalogSnapshot {
			return CatalogSnapshot{Options: []ModelOption{{ID: "a/1"}}, Origin: "cached", Stale: true}
		},
		Refresh: func(ctx context.Context, seq int) tea.Cmd {
			called++
			return func() tea.Msg { return modelsRefreshedMsg{seq: seq} }
		},
	})
	cmd := p.openModelPicker()
	if cmd == nil {
		t.Fatal("no refresh issued for a stale snapshot")
	}
	if called != 1 {
		t.Fatalf("refresh called %d times, want once", called)
	}
	// A second request while one is in flight must not fan out.
	p.Update(keyMsg(tea.KeyCtrlR))
	if called != 1 {
		t.Errorf("refresh called %d times, want no repeat while in flight", called)
	}
	// Delivering the result clears the in-flight flag and swaps rows.
	msg := cmd().(modelsRefreshedMsg)
	p.Update(msg)
	if p.state.Picker.Refreshing {
		t.Error("refreshing flag stuck after delivery")
	}
}

func TestPickerLayoutAtTerminalSizes(t *testing.T) {
	sizes := [][2]int{{120, 36}, {80, 24}, {60, 18}, {40, 10}}
	for _, size := range sizes {
		t.Run(itoa(size[0])+"x"+itoa(size[1]), func(t *testing.T) {
			s := New()
			s.Apply(TerminalResized{Width: size[0], Height: size[1]})
			many := append([]PickerItem(nil), testModels()...)
			for i := 0; i < 40; i++ {
				many = append(many, PickerItem{
					Title:    "Filler Model " + itoa(i) + " (openrouter/filler/model-" + itoa(i) + ")",
					Value:    "openrouter/filler/model-" + itoa(i),
					Provider: "openrouter", Name: "Filler Model " + itoa(i),
					PriceKnown: true, Context: 64000,
				})
			}
			openModelsPicker(&s, many)
			// Navigate deep: the marker must remain rendered in-viewport.
			for i := 0; i < 25; i++ {
				s.Apply(PickerMoved{Delta: 1})
			}
			out := Render(s)
			if !strings.Contains(out, "Search models") {
				t.Errorf("%dx%d: search input not visible", size[0], size[1])
			}
			if !strings.Contains(out, "result") {
				t.Errorf("%dx%d: result count missing", size[0], size[1])
			}
			markers := 0
			for _, line := range strings.Split(out, "\n") {
				// The marker is styled, so match the substring, not a
				// prefix: no other picker line contains "> ".
				if strings.Contains(line, "> ") {
					markers++
				}
				if CellWidth(line) > size[0] {
					t.Errorf("%dx%d: line overflows (%d cells): %q", size[0], size[1], CellWidth(line), line)
				}
			}
			if markers != 1 {
				t.Errorf("%dx%d: %d selection markers, want exactly 1", size[0], size[1], markers)
			}
			if !strings.Contains(out, "Enter select") || !strings.Contains(out, "Esc close") {
				t.Errorf("%dx%d: footer hints missing", size[0], size[1])
			}
		})
	}
}

// itoa formats small ints without importing strconv in tests.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}
