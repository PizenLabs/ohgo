package diff

import (
	"container/list"
	"context"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// Focus names which pane owns the navigation keys.
type Focus int

const (
	// FocusTree is the changed-file sidebar.
	FocusTree Focus = iota
	// FocusDiff is the hunk viewport.
	FocusDiff
)

// Timing and bounds for the inspector.
const (
	// loadDebounce coalesces the burst of cursor moves a held j/k produces
	// into a single per-file fetch. Scrolling past ten files spawns one git
	// process, not ten.
	loadDebounce = 70 * time.Millisecond
	// fetchTimeout bounds one git invocation. A hung repository must surface
	// an error in the pane, never wedge the viewport.
	fetchTimeout = 20 * time.Second
	// cacheCapacity bounds the parsed-hunk cache. The working set is the files
	// a reader moves between, not the whole changeset, so a small LRU keeps a
	// directory-wide browse instant without retaining per-line spans for
	// every path.
	cacheCapacity = 48
)

// renderKind is the role of one line in the diff viewport's flattened list.
type renderKind int

const (
	// rowChunk is a hunk header line.
	rowChunk renderKind = iota
	// rowLine is one hunk body line.
	rowLine
	// rowGap is the "↕ N unchanged" marker standing in for elided context.
	rowGap
)

// renderRow references one renderable line of the current file.
//
// The viewport is a flat slice of these rather than a materialised string:
// building it is O(lines) of small structs, and only the window the terminal
// shows is ever styled. A 100k-line diff never produces a 100k-line ANSI blob.
type renderRow struct {
	kind  renderKind
	chunk int
	line  int
	gap   int
}

// messages produced by the async fetches and the debounce tick.
type (
	filesLoadedMsg struct {
		changes []FileChange
		err     error
	}
	diffLoadedMsg struct {
		path   string
		chunks []DiffChunk
		err    error
	}
	previewTickMsg struct{ path string }
)

// Model is the interactive diff inspector. It satisfies tea.Model, and the
// embedding program forwards messages to it while it is open.
type Model struct {
	fetcher Fetcher
	spec    Spec
	title   string

	roots []*FileNode
	rows  []Row

	cursor int
	focus  Focus

	filter      string
	filterFocus bool

	cache *diffCache

	currentPath string
	chunks      []DiffChunk
	renderRows  []renderRow
	diffScroll  int

	loadingFiles bool
	loadingDiff  bool
	errText      string
	pendingLoad  string

	width  int
	height int

	// light selects the light palette; the zero value is the dark default.
	light bool
	// styleSet is the resolved style bundle for the active palette. It is
	// rebuilt only on a theme change, so the render loop never constructs a
	// style.
	styleSet styles
	// totals are the tree wide file and line counts, computed once when the
	// file list loads rather than re-walked on every frame's header.
	totalFiles int
	totalAdd   int
	totalDel   int
}

// New builds an inspector over fetcher for the given revision spec. title is
// the header label; an empty title falls back to the spec's.
func New(fetcher Fetcher, spec Spec, title string) *Model {
	if title == "" {
		title = spec.Label
	}
	return &Model{
		fetcher:      fetcher,
		spec:         spec,
		title:        title,
		focus:        FocusTree,
		cache:        newDiffCache(cacheCapacity),
		loadingFiles: true,
		styleSet:     newStyles(colorSetFor(false)),
	}
}

// Init starts the initial, content-free fetch.
func (m *Model) Init() tea.Cmd {
	return m.loadFilesCmd()
}

// loadFilesCmd fetches the changed-file list and stats off the update
// goroutine: the first frame must not block on a subprocess.
func (m *Model) loadFilesCmd() tea.Cmd {
	fetcher, spec := m.fetcher, m.spec
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), fetchTimeout)
		defer cancel()
		changes, err := fetcher.ChangedFiles(ctx, spec)
		return filesLoadedMsg{changes: changes, err: err}
	}
}

// loadDiffCmd fetches and parses one file's hunks off the update goroutine.
func (m *Model) loadDiffCmd(path string) tea.Cmd {
	fetcher, spec := m.fetcher, m.spec
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), fetchTimeout)
		defer cancel()
		chunks, err := fetcher.FileDiff(ctx, spec, path)
		return diffLoadedMsg{path: path, chunks: chunks, err: err}
	}
}

// SetSize records the terminal budget the renderer slices against.
func (m *Model) SetSize(width, height int) {
	m.width, m.height = width, height
	m.clampDiffScroll()
}

// FilterActive reports whether the filter box holds text or owns the keyboard,
// so the owning program knows an Esc clears the filter before closing.
func (m *Model) FilterActive() bool { return m.filterFocus || m.filter != "" }

// ClearFilter drops the filter text and returns focus to the tree.
func (m *Model) ClearFilter() {
	m.filter = ""
	m.filterFocus = false
	m.rebuildRows()
}

// Update implements tea.Model.
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch e := msg.(type) {
	case tea.KeyMsg:
		return m, m.handleKey(e)

	case filesLoadedMsg:
		m.loadingFiles = false
		if e.err != nil {
			m.errText = e.err.Error()
			return m, nil
		}
		m.errText = ""
		m.roots = BuildTree(e.changes)
		m.rebuildRows()
		m.recountTotals()
		m.cursor = 0
		m.currentPath = ""
		m.chunks = nil
		m.renderRows = nil
		return m, m.requestPreview()

	case diffLoadedMsg:
		if e.path != m.currentPath {
			// A slower fetch for a file the reader has left must not replace
			// the one on screen.
			return m, nil
		}
		m.loadingDiff = false
		if e.err != nil {
			m.errText = e.err.Error()
			return m, nil
		}
		m.cache.put(e.path, e.chunks)
		m.install(e.path, e.chunks)
		return m, nil

	case previewTickMsg:
		if e.path != m.pendingLoad {
			return m, nil
		}
		return m, m.startLoad(e.path)
	}
	return m, nil
}

// handleKey routes a keypress to the focused pane or the filter box.
func (m *Model) handleKey(msg tea.KeyMsg) tea.Cmd {
	if m.filterFocus {
		return m.handleFilterKey(msg)
	}
	switch msg.String() {
	case "tab", "shift+tab":
		if m.focus == FocusTree {
			m.focus = FocusDiff
		} else {
			m.focus = FocusTree
		}
		return nil
	case "/", "ctrl+f":
		m.filterFocus = true
		return nil
	}
	if m.focus == FocusTree {
		return m.handleTreeKey(msg)
	}
	return m.handleDiffKey(msg)
}

// handleFilterKey edits the filter box. Every printable rune is text; only
// navigation and dismissal are bound, so a path fragment containing "j" or "q"
// never triggers a command.
func (m *Model) handleFilterKey(msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case "esc", "enter":
		m.filterFocus = false
		return m.requestPreview()
	case "backspace", "ctrl+h":
		if m.filter != "" {
			m.filter = m.filter[:trimLastRune(m.filter)]
		}
		m.rebuildRows()
		m.clampCursor()
		return m.requestPreview()
	case "ctrl+u":
		m.filter = ""
		m.rebuildRows()
		m.clampCursor()
		return m.requestPreview()
	case "down":
		m.filterFocus = false
		m.moveCursor(1)
		return m.requestPreview()
	case "up":
		m.filterFocus = false
		m.moveCursor(-1)
		return m.requestPreview()
	}
	if msg.Type == tea.KeyRunes || msg.Type == tea.KeySpace {
		text := string(msg.Runes)
		if msg.Type == tea.KeySpace {
			text = " "
		}
		m.filter += text
		m.rebuildRows()
		m.clampCursor()
		return m.requestPreview()
	}
	return nil
}

// handleTreeKey drives the sidebar.
func (m *Model) handleTreeKey(msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case "down", "j":
		m.moveCursor(1)
		return m.requestPreview()
	case "up", "k":
		m.moveCursor(-1)
		return m.requestPreview()
	case "pgdown", "ctrl+d":
		m.moveCursor(m.pageSize())
		return m.requestPreview()
	case "pgup", "ctrl+u":
		m.moveCursor(-m.pageSize())
		return m.requestPreview()
	case "home", "g":
		m.cursor = 0
		return m.requestPreview()
	case "end", "G":
		m.cursor = len(m.rows) - 1
		return m.requestPreview()
	case "enter", " ":
		return m.activate()
	}
	return nil
}

// handleDiffKey scrolls the hunk viewport.
func (m *Model) handleDiffKey(msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case "down", "j":
		m.scrollDiff(1)
	case "up", "k":
		m.scrollDiff(-1)
	case "pgdown", "ctrl+d":
		m.scrollDiff(m.pageSize())
	case "pgup", "ctrl+u":
		m.scrollDiff(-m.pageSize())
	case "home", "g":
		m.diffScroll = 0
	case "end", "G":
		m.diffScroll = m.maxDiffScroll()
	}
	return nil
}

// activate toggles a directory or opens a file's diff in the other pane.
func (m *Model) activate() tea.Cmd {
	row := m.selectedRow()
	if row == nil {
		return nil
	}
	if row.Node.IsDir {
		row.Node.Expanded = !row.Node.Expanded
		m.rebuildRows()
		m.clampCursor()
		return m.requestPreview()
	}
	m.focus = FocusDiff
	return m.startLoad(row.Node.Path)
}

// moveCursor shifts the sidebar selection, clamped at both ends.
func (m *Model) moveCursor(delta int) {
	m.cursor += delta
	if m.cursor < 0 {
		m.cursor = 0
	}
	if m.cursor >= len(m.rows) {
		m.cursor = len(m.rows) - 1
	}
	if m.cursor < 0 {
		m.cursor = 0
	}
}

// clampCursor keeps the selection inside the row list after a rebuild.
func (m *Model) clampCursor() {
	if m.cursor >= len(m.rows) {
		m.cursor = len(m.rows) - 1
	}
	if m.cursor < 0 {
		m.cursor = 0
	}
}

// pageSize is the rows a page key moves: the body height of the smaller pane.
func (m *Model) pageSize() int {
	size := m.bodyHeight() - 1
	if size < 1 {
		size = 10
	}
	return size
}

// requestPreview schedules a debounced load of the highlighted file's diff.
//
// It is a no-op for a directory or when the highlighted file is already shown,
// so merely re-selecting a row costs nothing.
func (m *Model) requestPreview() tea.Cmd {
	row := m.selectedRow()
	if row == nil || row.Node.IsDir {
		return nil
	}
	path := row.Node.Path
	if path == m.currentPath && m.chunks != nil {
		return nil
	}
	m.pendingLoad = path
	return tea.Tick(loadDebounce, func(time.Time) tea.Msg { return previewTickMsg{path: path} })
}

// startLoad installs a cached diff or kicks a fetch. The shown path is updated
// immediately so a stale in-flight result is recognised and dropped.
func (m *Model) startLoad(path string) tea.Cmd {
	if chunks, ok := m.cache.get(path); ok {
		m.install(path, chunks)
		return nil
	}
	if path == m.currentPath && m.chunks != nil {
		return nil
	}
	m.currentPath = path
	m.chunks = nil
	m.renderRows = nil
	m.diffScroll = 0
	m.loadingDiff = true
	m.errText = ""
	return m.loadDiffCmd(path)
}

// install makes chunks the displayed diff and rebuilds the flattened viewport.
func (m *Model) install(path string, chunks []DiffChunk) {
	m.currentPath = path
	m.chunks = chunks
	m.renderRows = buildRenderRows(chunks)
	m.diffScroll = 0
	m.loadingDiff = false
	m.errText = ""
}

// recountTotals caches the tree-wide file and line counts for the header.
func (m *Model) recountTotals() {
	m.totalFiles = countFiles(m.roots)
	m.totalAdd, m.totalDel = totalStats(m.roots)
}

// rebuildRows recomputes the visible sidebar rows for the current filter and
// expansion state.
func (m *Model) rebuildRows() {
	if m.filter == "" {
		m.rows = VisibleRows(m.roots)
		return
	}
	m.rows = filterRows(m.roots, strings.ToLower(m.filter))
}

// filterRows keeps files whose path contains query and the directories that
// lead to them, force-expanding every surviving directory.
func filterRows(nodes []*FileNode, query string) []Row {
	var rows []Row
	var walk func(nodes []*FileNode, depth int) bool
	walk = func(nodes []*FileNode, depth int) bool {
		matched := false
		for _, node := range nodes {
			if !node.IsDir {
				if strings.Contains(strings.ToLower(node.Path), query) {
					rows = append(rows, Row{Node: node, Depth: depth})
					matched = true
				}
				continue
			}
			mark := len(rows)
			rows = append(rows, Row{Node: node, Depth: depth})
			if walk(node.Children, depth+1) {
				matched = true
			} else {
				// No descendant matched: drop the directory row again.
				rows = rows[:mark]
			}
		}
		return matched
	}
	walk(nodes, 0)
	return rows
}

// selectedRow returns the highlighted row, or nil when the list is empty.
func (m *Model) selectedRow() *Row {
	if m.cursor < 0 || m.cursor >= len(m.rows) {
		return nil
	}
	return &m.rows[m.cursor]
}

// buildRenderRows flattens hunks into viewport rows, inserting a gap marker
// wherever the diff skipped unchanged lines between hunks (or before the
// first one).
func buildRenderRows(chunks []DiffChunk) []renderRow {
	var rows []renderRow
	prevOldEnd := 0
	for ci, chunk := range chunks {
		gap := chunk.OldStart - 1
		if ci > 0 {
			gap = chunk.OldStart - prevOldEnd
		}
		if gap > 0 {
			rows = append(rows, renderRow{kind: rowGap, gap: gap})
		}
		rows = append(rows, renderRow{kind: rowChunk, chunk: ci})
		for li := range chunk.Lines {
			rows = append(rows, renderRow{kind: rowLine, chunk: ci, line: li})
		}
		prevOldEnd = chunk.OldStart + chunk.OldLines
	}
	return rows
}

// scrollDiff shifts the hunk viewport, clamped to the content.
func (m *Model) scrollDiff(delta int) {
	m.diffScroll += delta
	m.clampDiffScroll()
}

// clampDiffScroll keeps the hunk viewport inside its content.
func (m *Model) clampDiffScroll() {
	if max := m.maxDiffScroll(); m.diffScroll > max {
		m.diffScroll = max
	}
	if m.diffScroll < 0 {
		m.diffScroll = 0
	}
}

// maxDiffScroll is the furthest the hunk viewport may scroll.
func (m *Model) maxDiffScroll() int {
	visible := m.bodyHeight() - 1 // the hunk pane spends one row on its stats header
	if visible < 1 {
		visible = 1
	}
	max := len(m.renderRows) - visible
	if max < 0 {
		return 0
	}
	return max
}

// bodyHeight is the rows available to the two panes: the frame minus the
// title row and the footer row. Unknown height renders everything instead of
// slicing, matching the rest of the workspace's no-size behaviour.
func (m *Model) bodyHeight() int {
	if m.height <= 0 {
		n := len(m.rows)
		if len(m.renderRows) > n {
			n = len(m.renderRows)
		}
		return n + 2
	}
	h := m.height - 2
	if h < 1 {
		h = 1
	}
	return h
}

// trimLastRune returns the byte length of s without its final rune, so
// backspace never splits a multi-byte character.
func trimLastRune(s string) int {
	if s == "" {
		return 0
	}
	for i := len(s) - 1; i > 0; i-- {
		if s[i]&0xC0 != 0x80 {
			return i
		}
	}
	return 0
}

// diffCache is a small LRU over parsed hunks keyed by path.
type diffCache struct {
	order *list.List
	items map[string]*list.Element
	max   int
}

// cacheEntry is one cached hunk list.
type cacheEntry struct {
	path   string
	chunks []DiffChunk
}

// newDiffCache builds a cache holding at most max entries.
func newDiffCache(max int) *diffCache {
	if max < 1 {
		max = 1
	}
	return &diffCache{order: list.New(), items: make(map[string]*list.Element, max), max: max}
}

// get returns the cached hunks for path and marks them most recently used.
func (c *diffCache) get(path string) ([]DiffChunk, bool) {
	element, ok := c.items[path]
	if !ok {
		return nil, false
	}
	c.order.MoveToFront(element)
	return element.Value.(*cacheEntry).chunks, true
}

// put stores hunks for path, evicting the least recently used entry past the
// capacity.
func (c *diffCache) put(path string, chunks []DiffChunk) {
	if element, ok := c.items[path]; ok {
		element.Value.(*cacheEntry).chunks = chunks
		c.order.MoveToFront(element)
		return
	}
	element := c.order.PushFront(&cacheEntry{path: path, chunks: chunks})
	c.items[path] = element
	for c.order.Len() > c.max {
		oldest := c.order.Back()
		if oldest == nil {
			break
		}
		c.order.Remove(oldest)
		delete(c.items, oldest.Value.(*cacheEntry).path)
	}
}
