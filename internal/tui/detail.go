package tui

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/motoki317/agtlog/internal/model"
)

type detailState struct {
	session             *model.Session
	crumbs              []string
	viewport            viewport.Model
	expanded            map[string]bool
	tabFocusKeys        [2]string
	defaultExpanded     bool
	focus               int
	focusables          []detailFocus
	width               int
	height              int
	now                 time.Time
	absoluteTime        bool
	loadStatus          detailLoadStatus
	loadGeneration      uint64
	loadRestore         *detailRestoreState
	previousTimeline    *model.Session
	err                 error
	styles              styles
	wrap                bool
	tab                 detailTab
	subagentTotal       int
	subagentSort        sortState
	subagentColumnFocus listColumnKind
	subagents           []flattenedSubagent
	lines               []detailLine
	rendered            []renderedRow
	renderedStarts      []int
	selectedLine        int
}

type renderedRow struct {
	detailIndex int
	text        string
	first       bool
}

type detailLayout struct {
	headerHeight        int
	timelineHeight      int
	keyBarHeight        int
	contentY            int
	contentHeight       int
	compact             bool
	compactPanelHeight  int
	compactHeaderHeight int
}

func newDetailLayout(height int) detailLayout {
	height = max(3, height)
	if height < 9 {
		keyBarHeight := 1
		if height == 3 {
			keyBarHeight = 0
		}
		panelHeight := height - keyBarHeight
		capacity := max(0, panelHeight-2)
		headerHeight := min(3, capacity)
		return detailLayout{
			compact: true, compactPanelHeight: panelHeight, compactHeaderHeight: headerHeight,
			keyBarHeight: keyBarHeight, contentY: 1 + headerHeight, contentHeight: capacity - headerHeight,
		}
	}
	headerHeight := 5
	timelineHeight := height - 6
	return detailLayout{
		headerHeight: headerHeight, timelineHeight: timelineHeight, keyBarHeight: 1,
		contentY: headerHeight + 1, contentHeight: timelineHeight - 2,
	}
}

func (d *detailState) rowAtY(y int) (int, bool) {
	layout := newDetailLayout(d.height)
	visibleRow := y - layout.contentY
	if visibleRow < 0 || visibleRow >= layout.contentHeight {
		return 0, false
	}
	renderedIndex := d.viewport.YOffset + visibleRow
	if renderedIndex < 0 || renderedIndex >= len(d.rendered) {
		return 0, false
	}
	detailIndex := d.rendered[renderedIndex].detailIndex
	focus := -1
	for index, item := range d.focusables {
		if item.line > detailIndex {
			break
		}
		if d.tab != tabOverview || item.line == detailIndex {
			focus = index
		} else {
			focus = -1
		}
	}
	return focus, focus >= 0
}

type detailFocus struct {
	key             string
	line            int
	expandable      bool
	subagent        bool
	subagentSession *model.Session
	event           model.Event
}

type detailLine struct {
	text            string
	label           string
	metrics         string
	key             string
	nowrap          bool
	expandable      bool
	subagent        bool
	subagentSession *model.Session
	subagentCost    string
	subagentHeader  bool
	role            detailRole
	agent           model.AgentKind
	event           model.Event
}

type detailRole int

type detailTab int

type detailLoadStatus int

type detailRestoreState struct {
	viewportOffset      int
	pinned              bool
	focusKey            string
	defaultExpanded     bool
	focus               int
	wrap                bool
	tab                 detailTab
	subagentSort        sortState
	subagentColumnFocus listColumnKind
	expanded            map[string]bool
	tabFocusKeys        [2]string
}

const (
	detailPreviewLineCap         = 40
	detailPreviewRuneCap         = 4096
	subagentTitleMinVisibleWidth = 20
	timelineBodyIndent           = "  "
)

const (
	detailRelativeTimeWidth = 4
	detailClockTimeWidth    = 8
	detailDatedTimeWidth    = 15
	detailTimeGapWidth      = 1
)

func newViewport(width, height int) viewport.Model {
	view := viewport.New(width, height)
	view.MouseWheelDelta = mouseWheelRows
	return view
}

func scrollViewport(view *viewport.Model, button tea.MouseButton) {
	switch button {
	case tea.MouseButtonWheelUp:
		view.ScrollUp(mouseWheelRows)
	case tea.MouseButtonWheelDown:
		view.ScrollDown(mouseWheelRows)
	}
}

const (
	tabTimeline detailTab = iota
	tabOverview
)

const (
	detailStatusLoaded detailLoadStatus = iota
	detailStatusLoading
	detailStatusFailed
)

const (
	detailRow detailRole = iota
	detailHeader
	detailAccent
	detailAssistant
	detailUserPrompt
	detailSystemPrompt
	detailTool
	detailSecondary
	detailWarning
	detailDiffAdd
	detailDiffRemove
	detailDiffContext
)

func newDetailState(session *model.Session, width, height int, styles styles) *detailState {
	state := newDetailStateBase(session, width, height, styles)
	state.focus = -1
	state.resize(width, height)
	state.anchorBottom()
	return state
}

func newDetailStateBase(session *model.Session, width, height int, styles styles) *detailState {
	state := &detailState{session: session, expanded: make(map[string]bool), defaultExpanded: true, loadStatus: detailStatusLoaded, styles: styles, wrap: true, subagentTotal: subagentCount(session)}
	if project := model.TerminalLine(session.Project, 96); project != "" {
		state.crumbs = []string{project}
	}
	state.viewport = newViewport(max(1, width-2), max(1, height-8))
	return state
}

func (d *detailState) isExpanded(key string) bool {
	if expanded, ok := d.expanded[key]; ok {
		return expanded
	}
	return d.defaultExpanded
}

func (d *detailState) clone() *detailState {
	copy := *d
	copy.expanded = make(map[string]bool, len(d.expanded))
	for key, expanded := range d.expanded {
		copy.expanded[key] = expanded
	}
	copy.focusables = append([]detailFocus(nil), d.focusables...)
	copy.crumbs = append([]string(nil), d.crumbs...)
	copy.lines = append([]detailLine(nil), d.lines...)
	copy.rendered = append([]renderedRow(nil), d.rendered...)
	copy.renderedStarts = append([]int(nil), d.renderedStarts...)
	return &copy
}

func (d *detailState) scrollWheel(button tea.MouseButton) {
	scrollViewport(&d.viewport, button)
}

func (d *detailState) markLoading(generation uint64, restore *detailRestoreState, previousTimeline *model.Session) {
	d.loadStatus = detailStatusLoading
	d.loadGeneration = generation
	d.loadRestore = restore
	d.previousTimeline = previousTimeline
	d.err = nil
}

func (d *detailState) markLoaded() {
	d.loadStatus = detailStatusLoaded
	d.loadGeneration = 0
	d.loadRestore = nil
	d.previousTimeline = nil
	d.err = nil
}

func (d *detailState) markLoadFailed(err error) {
	d.loadStatus = detailStatusFailed
	d.loadGeneration = 0
	d.loadRestore = nil
	d.previousTimeline = nil
	d.err = err
}

// timelineSession returns the session whose events the timeline shows, or nil
// when no rows exist yet.
func (d *detailState) timelineSession() *model.Session {
	switch d.loadStatus {
	case detailStatusLoaded:
		return d.session
	case detailStatusLoading:
		return d.previousTimeline
	default:
		return nil
	}
}

func (d *detailState) resize(width, height int) {
	pinned := len(d.rendered) > 0 && d.followingTail()
	anchorDetail, anchorOffset := -1, 0
	if d.tab == tabOverview {
		anchorDetail, anchorOffset = d.renderedViewportAnchor()
	}
	d.width, d.height = max(1, width), max(3, height)
	layout := newDetailLayout(d.height)
	d.viewport.Width = max(1, d.width-2)
	d.viewport.Height = max(1, layout.contentHeight)
	d.rebuild()
	if pinned {
		d.anchorBottom()
	} else if d.tab == tabOverview {
		d.restoreRenderedViewportAnchor(anchorDetail, anchorOffset)
	}
}

func (d *detailState) setWrap(wrap bool) {
	if d.wrap == wrap {
		return
	}
	d.wrap = wrap
	if d.tab == tabOverview {
		d.rebuildPreservingViewport()
		return
	}
	pinned := d.followingTail()
	d.rebuild()
	if pinned {
		d.anchorBottom()
	}
}

func (d *detailState) update(msg tea.Msg) tea.Cmd {
	key, ok := msg.(tea.KeyMsg)
	if !ok {
		var cmd tea.Cmd
		d.viewport, cmd = d.viewport.Update(msg)
		return cmd
	}
	if d.loadStatus == detailStatusFailed {
		return nil
	}
	switch key.String() {
	case sortColumnKey:
		if d.tab == tabOverview {
			d.sortSubagents(d.subagentColumnFocus)
		}
	case sortAgeKey:
		if d.tab == tabOverview {
			d.sortSubagents(columnAge)
		}
	case sortTitleKey:
		if d.tab == tabOverview {
			d.sortSubagents(columnTitle)
		}
	case "left", "right":
		if d.tab == tabOverview {
			delta := -1
			if key.String() == "right" {
				delta = 1
			}
			d.subagentColumnFocus = moveColumnFocus(d.subagentColumnFocus, d.visibleSubagentColumns(), subagentColumnOrder, delta)
			break
		}
		var cmd tea.Cmd
		d.viewport, cmd = d.viewport.Update(msg)
		return cmd
	case "tab", "shift+tab":
		d.switchTab()
	case "j", "down":
		d.moveFocus(1)
	case "k", "up":
		d.moveFocus(-1)
	case "g", "home":
		if len(d.focusables) > 0 {
			oldLine := d.selectedLine
			d.focus = 0
			d.updateSelection(oldLine, d.focusables[0].line)
		}
		d.viewport.GotoTop()
	case "G", "end":
		d.gotoBottom()
	case " ":
		if d.tab == tabTimeline {
			d.toggleFocused()
		}
	case expandAllKey:
		if d.tab == tabTimeline {
			d.setAllExpanded(true)
		}
	case collapseAllKey:
		if d.tab == tabTimeline {
			d.setAllExpanded(false)
		}
	case "w":
		d.setWrap(!d.wrap)
	default:
		var cmd tea.Cmd
		d.viewport, cmd = d.viewport.Update(msg)
		return cmd
	}
	return nil
}

// switchTab stores an empty Timeline focus key while the Timeline follows the
// tail. rebuild resolves an empty key to the newest event, so the Timeline
// resumes following on return, also after a live update replaces the state.
func (d *detailState) switchTab() {
	leaving := d.focusKey()
	if d.followingTail() {
		leaving = ""
	}
	d.tabFocusKeys[d.tab] = leaving
	d.tab = (d.tab + 1) % 2
	d.focusables = nil
	d.focus = -1
	d.viewport.SetYOffset(0)
	d.rebuild()
	if d.tab == tabTimeline && d.tabFocusKeys[tabTimeline] == "" {
		d.anchorBottom()
	}
}

func (d *detailState) pinnedToBottom() bool {
	return d.viewport.AtBottom() || d.viewport.YOffset == d.bottomAnchorOffset()
}

// followingTail reports whether the Timeline moves to newly appended events.
// The cursor can move within the last screenful without a scroll. A test of the
// viewport alone would then drag the cursor to the newest event while the
// reader reads an earlier one. G resumes following.
func (d *detailState) followingTail() bool {
	if d.tab != tabTimeline || !d.pinnedToBottom() {
		return false
	}
	return len(d.focusables) == 0 || d.focus == len(d.focusables)-1
}

func (d *detailState) bottomAnchorOffset() int {
	offset := max(0, len(d.rendered)-d.viewport.Height)
	for offset > 0 && !d.rendered[offset].first {
		offset--
	}
	return offset
}

func (d *detailState) snapTopToLineStart() {
	d.viewport.SetYOffset(d.bottomAnchorOffset())
}

func (d *detailState) anchorBottom() {
	d.viewport.GotoBottom()
	d.snapTopToLineStart()
}

func (d *detailState) gotoBottom() {
	if len(d.focusables) > 0 {
		oldLine := d.selectedLine
		d.focus = len(d.focusables) - 1
		d.updateSelection(oldLine, d.focusables[d.focus].line)
	}
	d.anchorBottom()
}

func (d *detailState) moveFocus(direction int) {
	if len(d.focusables) == 0 {
		if direction > 0 {
			d.viewport.ScrollDown(1)
		} else {
			d.viewport.ScrollUp(1)
		}
		return
	}
	next := d.focus + direction
	if next < 0 || next >= len(d.focusables) {
		return
	}
	oldLine := d.focusables[d.focus].line
	d.focus = next
	d.updateSelection(oldLine, d.focusables[d.focus].line)
}

// selectRow moves the cursor and never folds the clicked row. A fold under the
// pointer rewrites the rows that the reader aimed at, and the keyboard already
// folds.
func (d *detailState) selectRow(index int) {
	oldLine := d.selectedLine
	d.focus = index
	d.updateSelection(oldLine, d.focusables[index].line)
}

func (d *detailState) selectedExpandable() bool {
	return len(d.focusables) > 0 && d.focusables[d.focus].expandable
}

func (d *detailState) collapseFocused() {
	if !d.selectedExpandable() {
		return
	}
	item := d.focusables[d.focus]
	if !d.isExpanded(item.key) {
		return
	}
	d.expanded[item.key] = false
	d.rebuildKeeping(item.key)
}

func (d *detailState) expandFocused() {
	if !d.selectedExpandable() {
		return
	}
	item := d.focusables[d.focus]
	if d.isExpanded(item.key) {
		return
	}
	d.expanded[item.key] = true
	d.rebuildKeeping(item.key)
}

func (d *detailState) rebuildKeeping(key string) {
	d.rebuild()
	for index, item := range d.focusables {
		if item.key == key {
			oldLine := d.selectedLine
			d.focus = index
			d.updateSelection(oldLine, item.line)
			return
		}
	}
}

// setAllExpanded changes the default instead of writing an override per row, so
// rows that a live session appends later follow the last bulk choice
// (docs/ADR/20260723-bulk-fold-default.md).
func (d *detailState) setAllExpanded(expanded bool) {
	key := ""
	if len(d.focusables) > 0 {
		key = d.focusables[d.focus].key
	}
	d.expanded = make(map[string]bool)
	d.defaultExpanded = expanded
	d.rebuildKeeping(key)
}

func (d *detailState) focusKey() string {
	if d.focus >= 0 && d.focus < len(d.focusables) {
		return d.focusables[d.focus].key
	}
	return ""
}

func (d *detailState) toggleFocused() {
	if !d.selectedExpandable() {
		return
	}
	key := d.focusKey()
	d.expanded[key] = !d.isExpanded(key)
	d.rebuildKeeping(key)
}

func (d *detailState) rebuild() {
	// A resize and the T toggle both change which Subagents columns fit. The
	// toggle reaches the detail only through a rebuild, so the focus snaps here.
	d.subagentColumnFocus = snapColumnFocus(d.subagentColumnFocus, d.visibleSubagentColumns(), subagentColumnOrder)
	selected := d.focusKey()
	if selected == "" {
		selected = d.tabFocusKeys[d.tab]
	}
	var lines []detailLine
	if d.loadStatus == detailStatusFailed {
		d.subagents = nil
		message := "detail unavailable"
		if d.err != nil {
			message = model.TerminalLine(d.err.Error(), 512)
		}
		lines = []detailLine{{text: "detail error: " + message, role: detailWarning}}
	} else if d.tab == tabOverview {
		lines = d.overviewLines()
	} else if timeline := d.timelineSession(); timeline != nil {
		lines = d.sessionLines(timeline, sessionIdentity(d.session))
	} else {
		lines = []detailLine{{text: "Loading timeline…", role: detailSecondary}}
	}
	d.lines = lines
	d.focusables = d.focusables[:0]
	for index, line := range lines {
		if line.key != "" {
			d.focusables = append(d.focusables, detailFocus{key: line.key, line: index, expandable: line.expandable, subagent: line.subagent, subagentSession: line.subagentSession, event: line.event})
		}
	}
	found := false
	for index, item := range d.focusables {
		if item.key == selected {
			d.focus = index
			found = true
			break
		}
	}
	if !found && (selected == "" || d.focus < 0 || d.focus >= len(d.focusables)) {
		d.focus = max(0, len(d.focusables)-1)
		if d.tab == tabOverview {
			d.focus = 0
		}
	}
	d.selectedLine = -1
	if len(d.focusables) > 0 {
		d.selectedLine = d.focusables[d.focus].line
	}
	d.rebuildRendered()
}

func (d *detailState) updateSelection(oldLine, newLine int) {
	d.updateSelectionMarker(oldLine, false)
	d.selectedLine = newLine
	d.updateSelectionMarker(newLine, true)
	selectedRow := d.firstRenderedRow(newLine)
	if selectedRow < d.viewport.YOffset {
		d.viewport.SetYOffset(selectedRow)
	} else if selectedRow >= d.viewport.YOffset+d.viewport.Height {
		d.viewport.SetYOffset(selectedRow - d.viewport.Height + 1)
	}
}

func (d *detailState) updateSelectionMarker(detailIndex int, selected bool) {
	rowIndex := d.firstRenderedRow(detailIndex)
	if rowIndex < 0 {
		return
	}
	text := d.rendered[rowIndex].text
	if strings.HasPrefix(text, "› ") {
		text = strings.TrimPrefix(text, "› ")
	} else {
		text = strings.TrimPrefix(text, "  ")
	}
	prefix := "  "
	if selected {
		prefix = "› "
	}
	d.rendered[rowIndex].text = prefix + text
}

func (d *detailState) rebuildRendered() {
	d.rendered = d.rendered[:0]
	d.renderedStarts = d.renderedStarts[:0]
	content := make([]string, 0, len(d.lines))
	for detailIndex, line := range d.lines {
		d.renderedStarts = append(d.renderedStarts, len(d.rendered))
		markerWidth := min(2, d.viewport.Width)
		gutterWidth := min(d.timelineGutterWidth(), max(0, d.viewport.Width-markerWidth))
		bodyWidth := max(0, d.viewport.Width-markerWidth-gutterWidth)
		rows := []string{line.text}
		if line.metrics != "" {
			rows = []string{composeMetricRow(line.text, line.metrics, bodyWidth)}
		} else if d.wrap && !line.nowrap && bodyWidth > 0 && ansi.StringWidth(line.text) > bodyWidth {
			if d.tab == tabOverview {
				rows = wordWrapRows(line.text, bodyWidth)
			} else {
				rows = strings.Split(ansi.Hardwrap(line.text, bodyWidth, true), "\n")
			}
		}
		for rowIndex, row := range rows {
			first := rowIndex == 0
			marker := "  "
			if first && detailIndex == d.selectedLine {
				marker = "› "
			}
			plain := fitPlain(marker, markerWidth, false) + d.timelineGutter(line.event.Timestamp, first, gutterWidth) + fitPlain(row, bodyWidth, false)
			d.rendered = append(d.rendered, renderedRow{detailIndex: detailIndex, text: plain, first: first})
			content = append(content, plain)
		}
	}
	d.viewport.SetContent(strings.Join(content, "\n"))
	// SetContent clamps only an offset past the last row. Shrunk content otherwise
	// leaves blank rows below the bottom.
	d.viewport.SetYOffset(d.viewport.YOffset)
	selectedRow := d.firstRenderedRow(d.selectedLine)
	if selectedRow < 0 {
		return
	}
	if selectedRow < d.viewport.YOffset {
		d.viewport.SetYOffset(selectedRow)
	} else if selectedRow >= d.viewport.YOffset+d.viewport.Height {
		d.viewport.SetYOffset(selectedRow - d.viewport.Height + 1)
	}
}

func wordWrapRows(value string, width int) []string {
	var rows []string
	protected := strings.ReplaceAll(value, " × $", "\u00a0×\u00a0$")
	for _, row := range strings.Split(ansi.Wordwrap(protected, width, ""), "\n") {
		if ansi.StringWidth(row) <= width {
			rows = append(rows, strings.ReplaceAll(row, "\u00a0", " "))
			continue
		}
		for _, hardwrapped := range strings.Split(ansi.Hardwrap(row, width, true), "\n") {
			rows = append(rows, strings.ReplaceAll(hardwrapped, "\u00a0", " "))
		}
	}
	return rows
}

func (d *detailState) rebuildPreservingViewport() {
	pinned := d.followingTail()
	anchorDetail, anchorOffset := d.renderedViewportAnchor()
	d.rebuild()
	if pinned {
		d.anchorBottom()
		return
	}
	d.restoreRenderedViewportAnchor(anchorDetail, anchorOffset)
}

func (d *detailState) renderedViewportAnchor() (int, int) {
	if offset := d.viewport.YOffset; offset >= 0 && offset < len(d.rendered) {
		detailIndex := d.rendered[offset].detailIndex
		return detailIndex, offset - d.firstRenderedRow(detailIndex)
	}
	return -1, 0
}

func (d *detailState) restoreRenderedViewportAnchor(anchorDetail, anchorOffset int) {
	start := d.firstRenderedRow(anchorDetail)
	if start < 0 {
		d.viewport.SetYOffset(0)
		return
	}
	end := len(d.rendered)
	if anchorDetail+1 < len(d.renderedStarts) {
		end = d.renderedStarts[anchorDetail+1]
	}
	d.viewport.SetYOffset(start + min(anchorOffset, max(0, end-start-1)))
}

func (d *detailState) timelineGutterWidth() int {
	if d.tab != tabTimeline {
		return 0
	}
	width := detailRelativeTimeWidth
	if d.absoluteTime {
		width = detailClockTimeWidth
		if sessionSpansMultipleDates(d.session) {
			width = detailDatedTimeWidth
		}
	}
	return width + detailTimeGapWidth
}

func (d *detailState) timelineGutter(at time.Time, first bool, width int) string {
	if width <= 0 {
		return ""
	}
	stamp := ""
	if first && !at.IsZero() {
		if d.absoluteTime {
			stamp = formatDetailTime(at, sessionSpansMultipleDates(d.session))
		} else {
			stamp = formatAge(d.now, at)
		}
	}
	stampWidth := max(0, width-detailTimeGapWidth)
	return fitPlain(stamp, stampWidth, true) + strings.Repeat(" ", width-stampWidth)
}

func (d *detailState) firstRenderedRow(detailIndex int) int {
	if detailIndex < 0 || detailIndex >= len(d.renderedStarts) {
		return -1
	}
	return d.renderedStarts[detailIndex]
}

func (d *detailState) focusedSubagent() *model.Session {
	if len(d.focusables) == 0 {
		return nil
	}
	return d.focusables[d.focus].subagentSession
}

func (d *detailState) focusedEvent() (model.Event, bool) {
	if d.tab != tabTimeline || len(d.focusables) == 0 {
		return model.Event{}, false
	}
	focused := d.focusables[d.focus]
	if focused.subagent || focused.event.Kind == "" {
		return model.Event{}, false
	}
	return focused.event, true
}

func (d *detailState) eventForKey(key string) (model.Event, bool) {
	for _, focused := range d.focusables {
		if focused.key == key && !focused.subagent && focused.event.Kind != "" {
			return focused.event, true
		}
	}
	return model.Event{}, false
}

func (d *detailState) rowCounter() (current, total int) {
	if len(d.focusables) > 0 {
		return min(d.focus+1, len(d.focusables)), len(d.focusables)
	}
	return min(len(d.rendered), d.viewport.YOffset+1), len(d.rendered)
}

func (d *detailState) view() string {
	layout := newDetailLayout(d.height)
	if layout.compact {
		return d.compactView(layout)
	}
	header := d.header()
	timelineHeight := layout.timelineHeight
	visiblePlain := make([]string, 0, d.viewport.Height)
	for rowIndex := d.viewport.YOffset; rowIndex < len(d.rendered) && len(visiblePlain) < d.viewport.Height; rowIndex++ {
		visiblePlain = append(visiblePlain, d.rendered[rowIndex].text)
	}
	visible := make([]panelLine, len(visiblePlain))
	for index, plain := range visiblePlain {
		rowIndex := d.viewport.YOffset + index
		if rowIndex >= len(d.rendered) {
			visible[index] = panelLine{plain: plain, styled: d.styles.row.Render(plain)}
			continue
		}
		detailIndex := d.rendered[rowIndex].detailIndex
		visible[index] = panelLine{plain: plain, styled: d.styleLine(plain, d.lines[detailIndex], detailIndex == d.selectedLine, d.rendered[rowIndex].first)}
	}
	for len(visible) < layout.contentHeight {
		plain := strings.Repeat(" ", d.viewport.Width)
		visible = append(visible, panelLine{plain: plain, styled: d.styles.row.Render(plain)})
	}
	if len(visible) > layout.contentHeight {
		visible = visible[:layout.contentHeight]
	}
	hint := ""
	if len(d.rendered) > d.viewport.Height {
		current, total := d.rowCounter()
		hint = fmt.Sprintf("%d/%d", current, total)
	}
	timeline := renderPanelWithLabel(d.tabPanelLabel(), hint, visible, d.width, timelineHeight, d.styles)
	keyText := detailKeyText(d.width, d.styles.mono, d.tab, d.wrap)
	keyBar := d.styles.keyHint.Render(fitPlain(keyText, d.width, false))
	return strings.Join([]string{header, timeline, keyBar}, "\n")
}

func (d *detailState) compactView(layout detailLayout) string {
	capacity := max(0, layout.compactPanelHeight-2)
	headerLines := d.headerPanelLines()
	content := make([]panelLine, 0, capacity)
	for _, index := range []int{0, 2, 1} {
		if len(content) >= layout.compactHeaderHeight {
			break
		}
		content = append(content, headerLines[index])
	}
	for rowIndex := d.viewport.YOffset; len(content) < capacity && rowIndex < len(d.rendered); rowIndex++ {
		row := d.rendered[rowIndex]
		plain := fitPlain(row.text, max(0, d.width-2), false)
		content = append(content, panelLine{plain: plain, styled: d.styleLine(plain, d.lines[row.detailIndex], row.detailIndex == d.selectedLine, row.first)})
	}
	for len(content) < capacity {
		plain := strings.Repeat(" ", max(0, d.width-2))
		content = append(content, panelLine{plain: plain, styled: d.styles.row.Render(plain)})
	}
	panel := renderPanelWithLabel(d.compactPanelLabel(), "", content, d.width, layout.compactPanelHeight, d.styles)
	if layout.keyBarHeight == 0 {
		return panel
	}
	keyBar := d.styles.keyHint.Render(fitPlain(detailKeyText(d.width, d.styles.mono, d.tab, d.wrap), d.width, false))
	return panel + "\n" + keyBar
}

func (t detailTab) title() string {
	switch t {
	case tabOverview:
		return "Overview"
	default:
		return "Timeline"
	}
}

func (d *detailState) tabPanelLabel() panelLabel {
	label := d.tabLabel()
	plain := label.plain
	if ansi.StringWidth(plain) > max(1, d.width-5) {
		return d.activeTabLabel()
	}
	return label
}

func (d *detailState) compactPanelLabel() panelLabel {
	label := d.tabLabel()
	plain, styled := label.plain, label.styled
	if len(d.crumbs) > 0 {
		crumbs := model.TerminalLine(strings.Join(d.crumbs, " › "), 256)
		plain += " · " + crumbs
		styled += d.styles.title.Render(" · " + crumbs)
	}
	if ansi.StringWidth(plain) <= max(1, d.width-5) {
		return panelLabel{plain: plain, styled: styled}
	}
	return d.activeTabLabel()
}

func (d *detailState) tabLabel() panelLabel {
	labels := []string{"Timeline", "Overview"}
	styles := []lipgloss.Style{d.styles.muted, d.styles.muted}
	active := int(d.tab)
	labels[active] = "[" + labels[active] + "]"
	styles[active] = d.styles.title
	return panelLabel{plain: strings.Join(labels, "  "), styled: styles[0].Render(labels[0]) + d.styles.title.Render("  ") + styles[1].Render(labels[1])}
}

func (d *detailState) activeTabLabel() panelLabel {
	maxWidth := max(1, d.width-5)
	plain := "…"
	if maxWidth >= 2 {
		plain = "[" + ansi.Truncate(d.tab.title(), maxWidth-2, "…") + "]"
	}
	return panelLabel{plain: plain, styled: d.styles.title.Render(plain)}
}

func detailKeyText(width int, mono bool, tab detailTab, wrap bool) string {
	enterHint := "↵ inspect"
	if tab == tabOverview {
		enterHint = "↵ open"
	}
	wrapHint := "w wrap"
	if wrap {
		wrapHint = "w nowrap"
	}
	bulkHint := expandAllKey + "/" + collapseAllKey + " all"
	hints := []string{"j/k scroll"}
	if tab == tabTimeline {
		hints = append(hints, "←/→ fold", "space toggle", bulkHint, enterHint, "tab switch", wrapHint)
	} else {
		hints = append(hints, "←/→ column", "⇧"+sortColumnKey+" sort", enterHint, "tab switch", wrapHint)
	}
	hints = append(hints, timeFormatKey+" time")
	mouseHint := "mouse scroll/click"
	hints = append(hints, "esc back", mouseHint)
	if !mono {
		hints = append(hints, "t theme")
	}
	hints = append(hints, "? help", "q quit")
	return fitKeyHints(width, hints, []string{
		mouseHint, "t theme", "? help", "j/k scroll", "tab switch", wrapHint, bulkHint, "⇧" + sortColumnKey + " sort", "←/→ column", "q quit", "space toggle", "←/→ fold", timeFormatKey + " time", enterHint,
	})
}

func fitKeyHints(width int, hints, dropOrder []string) string {
	for _, drop := range dropOrder {
		if ansi.StringWidth(strings.Join(hints, "   ")) <= width {
			break
		}
		for index, hint := range hints {
			if hint == drop {
				hints = append(hints[:index], hints[index+1:]...)
				break
			}
		}
	}
	text := strings.Join(hints, "   ")
	if ansi.StringWidth(text) <= width {
		return text
	}
	if len(hints) == 1 && hints[0] == "esc back" && width >= ansi.StringWidth("esc") {
		return "esc"
	}
	return ""
}

func (d *detailState) header() string {
	return renderPanel(d.panelTitle("Session"), "", d.headerPanelLines(), d.width, 5, d.styles)
}

func (d *detailState) panelTitle(name string) string {
	title := name
	if len(d.crumbs) > 0 {
		title += " · " + strings.Join(d.crumbs, " › ")
	}
	return ansi.Truncate(model.TerminalLine(title, 256), max(1, d.width-5), "…")
}

// headerFieldSep is a spaced vertical bar, which reads as a column boundary. A
// middle dot blurs adjacent header fields together.
const headerFieldSep = " │ "

func (d *detailState) headerPanelLines() []panelLine {
	session := d.session
	totalCost := session.OwnedCost()
	ownCost := session.OwnedSelfCost()
	subagentCost := session.OwnedDescendantCost()
	innerWidth := max(0, d.width-2)
	line1 := firstLine(session.Title)

	line2Parts := make([]string, 0, 3)
	if agent := model.TerminalLine(string(session.Agent), 32); agent != "" {
		line2Parts = append(line2Parts, agent)
	}
	project, cwd := model.TerminalLine(session.Project, 96), model.TerminalLine(session.CWD, 256)
	workspaceIndex := -1
	if project != "" && cwd != "" {
		workspaceIndex = len(line2Parts)
		line2Parts = append(line2Parts, project+" ("+cwd+")")
	} else if project != "" {
		line2Parts = append(line2Parts, project)
	} else if cwd != "" {
		line2Parts = append(line2Parts, cwd)
	}
	if models := model.TerminalLine(detailModels(session), 256); models != "" {
		line2Parts = append(line2Parts, models)
	}
	if workspaceIndex >= 0 && ansi.StringWidth(strings.Join(line2Parts, headerFieldSep)) > innerWidth {
		line2Parts[workspaceIndex] = project
	}
	line2 := strings.Join(line2Parts, headerFieldSep)

	line3Parts := make([]string, 0, 3)
	if branch := model.TerminalLine(session.GitBranch, 96); branch != "" {
		line3Parts = append(line3Parts, "branch "+branch)
	}
	started, updated := formatHeaderTime(session.StartedAt), formatHeaderTime(session.UpdatedAt)
	if started != "" && updated != "" {
		line3Parts = append(line3Parts, started+"→"+updated)
	} else if started != "" {
		line3Parts = append(line3Parts, started)
	} else if updated != "" {
		line3Parts = append(line3Parts, updated)
	}
	detailedUsage := strings.Join([]string{
		"total " + formatCost(totalCost),
		"own " + formatCost(ownCost),
		"subagents " + formatCost(subagentCost),
	}, headerFieldSep)
	compactUsage := "total " + formatCost(totalCost)
	line3Parts = append(line3Parts, detailedUsage)
	if ansi.StringWidth(strings.Join(line3Parts, headerFieldSep)) > innerWidth {
		line3Parts[len(line3Parts)-1] = compactUsage
	}
	for len(line3Parts) > 1 && ansi.StringWidth(strings.Join(line3Parts, headerFieldSep)) > innerWidth {
		line3Parts = line3Parts[1:]
	}
	line3 := strings.Join(line3Parts, headerFieldSep)
	plainLines := []string{
		fitPlain(line1, innerWidth, false),
		fitPlain(line2, innerWidth, false),
		fitPlain(line3, innerWidth, false),
	}
	lines := []panelLine{
		{plain: plainLines[0], styled: d.styles.emphasis.Render(plainLines[0])},
		{plain: plainLines[1], styled: d.agentStyle(session.Agent).Render(plainLines[1])},
		{plain: plainLines[2], styled: d.styles.emphasis.Render(plainLines[2])},
	}
	return lines
}

func (d *detailState) styleLine(line string, detail detailLine, selected, first bool) string {
	gutterWidth := min(d.timelineGutterWidth(), max(0, ansi.StringWidth(line)-2))
	hasGutter := gutterWidth > 0 && ansi.StringWidth(line) == d.viewport.Width
	if selected && hasGutter && !d.styles.mono {
		marker := ansi.Cut(line, 0, 2)
		gutter := ansi.Cut(line, 2, 2+gutterWidth)
		body := ansi.Cut(line, 2+gutterWidth, ansi.StringWidth(line))
		mutedSelection := d.styles.selected.Foreground(d.styles.muted.GetForeground())
		return d.styles.selected.Render(marker) + mutedSelection.Render(gutter) + d.styles.selected.Render(body)
	}
	if selected {
		return d.styles.selected.Render(line)
	}
	if hasGutter {
		marker := ansi.Cut(line, 0, 2)
		gutter := ansi.Cut(line, 2, 2+gutterWidth)
		body := ansi.Cut(line, 2+gutterWidth, ansi.StringWidth(line))
		return d.styles.row.Render(marker) + d.styles.muted.Render(gutter) + d.styleLineBody(body, detail, first)
	}
	return d.styleLineBody(line, detail, first)
}

// styleLineBody colors only the label and mutes the metrics. The rest of the row
// keeps the role's base style, so a long timeline does not become one color.
func (d *detailState) styleLineBody(line string, detail detailLine, first bool) string {
	if detail.role == detailRow && detail.subagentSession != nil {
		return d.styleSubagentLine(line, detail)
	}
	if detail.subagentHeader {
		markerWidth := min(2, ansi.StringWidth(line))
		marker := ansi.Cut(line, 0, markerWidth)
		header := subagentHeader(d.visibleSubagentColumns(), d.subagentSort, d.subagentColumnFocus, d.styles)
		return d.styles.header.Render(marker) + header.styled
	}
	base := d.roleBaseStyle(detail.role)
	if !first {
		return base.Render(line)
	}
	var cells []styleCell
	if detail.label != "" {
		if start := strings.Index(line, detail.label); start >= 0 {
			cells = append(cells, styleCell{start: start, end: start + len(detail.label), style: d.labelStyle(detail)})
		}
	}
	if detail.metrics != "" {
		if start := strings.LastIndex(line, detail.metrics); start >= 0 {
			cells = append(cells, styleCell{start: start, end: start + len(detail.metrics), style: base.Foreground(d.styles.muted.GetForeground())})
		}
	}
	if len(cells) == 0 {
		return styleDetailRole(d.styles, detail.role, line)
	}
	sort.Slice(cells, func(i, j int) bool { return cells[i].start < cells[j].start })
	return renderStyleCells(line, base, cells)
}

type styleCell struct {
	start int
	end   int
	style lipgloss.Style
}

// renderStyleCells renders each cell with its own style and every gap with base.
// Cells hold byte offsets into line and must be sorted by start. A cell that
// overlaps an earlier cell or ends past the line is skipped.
func renderStyleCells(line string, base lipgloss.Style, cells []styleCell) string {
	var styled strings.Builder
	position := 0
	for _, item := range cells {
		if item.start < position || item.end > len(line) {
			continue
		}
		styled.WriteString(base.Render(line[position:item.start]))
		styled.WriteString(item.style.Render(line[item.start:item.end]))
		position = item.end
	}
	styled.WriteString(base.Render(line[position:]))
	return styled.String()
}

// roleBaseStyle gives tool and Task rows the plain row style instead of accent,
// so only their label carries color.
func (d *detailState) roleBaseStyle(role detailRole) lipgloss.Style {
	switch role {
	case detailHeader:
		return d.styles.header
	case detailUserPrompt:
		return d.styles.userPrompt
	case detailSystemPrompt:
		return d.styles.systemPrompt
	case detailSecondary:
		return d.styles.muted
	case detailWarning:
		return d.styles.warning
	case detailDiffAdd:
		return d.styles.diffAdd
	case detailDiffRemove:
		return d.styles.diffRemove
	case detailDiffContext:
		return d.styles.muted
	default:
		return d.styles.row
	}
}

// labelStyle reuses the key-hint and header hues for prompt and tool labels
// instead of adding two theme colors.
func (d *detailState) labelStyle(detail detailLine) lipgloss.Style {
	base := d.roleBaseStyle(detail.role)
	switch detail.role {
	case detailAssistant:
		return d.agentStyle(detail.agent)
	case detailUserPrompt:
		return base.Foreground(d.styles.keyHint.GetForeground()).Bold(true)
	case detailSystemPrompt:
		return base.Foreground(d.styles.muted.GetForeground())
	case detailTool:
		return base.Foreground(d.styles.header.GetForeground()).Bold(true)
	default:
		return d.styles.accent
	}
}

func styleDetailRole(styleSet styles, role detailRole, line string) string {
	switch role {
	case detailHeader:
		return styleSet.header.Render(line)
	case detailAccent, detailTool:
		return styleSet.accent.Render(line)
	case detailUserPrompt:
		return styleSet.userPrompt.Render(line)
	case detailSystemPrompt:
		return styleSet.systemPrompt.Render(line)
	case detailSecondary:
		return styleSet.muted.Render(line)
	case detailWarning:
		return styleSet.warning.Render(line)
	case detailDiffAdd:
		return styleSet.diffAdd.Render(line)
	case detailDiffRemove:
		return styleSet.diffRemove.Render(line)
	case detailDiffContext:
		return styleSet.muted.Render(line)
	}
	return styleSet.row.Render(line)
}

func (d *detailState) agentStyle(agent model.AgentKind) lipgloss.Style {
	if agent == model.AgentClaude {
		return d.styles.claude
	}
	if agent == model.AgentCodex {
		return d.styles.codex
	}
	return d.styles.row
}

func detailModels(session *model.Session) string {
	if len(session.Models) == 0 {
		return ""
	}
	missing, _ := modelCostMarkers(session.Cost)
	labels := make([]string, 0, len(session.Models))
	for _, name := range session.Models {
		label := shortModelName(name)
		if missing[name] {
			label += "!"
		}
		labels = append(labels, label)
	}
	return strings.Join(labels, ", ")
}

func formatHeaderTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.Format("Jan 02 15:04")
}

func formatDetailTime(value time.Time, includeDate bool) string {
	if value.IsZero() {
		return ""
	}
	if includeDate {
		return value.Format("Jan 2 15:04:05")
	}
	return value.Format("15:04:05")
}

func sessionSpansMultipleDates(session *model.Session) bool {
	if session == nil || session.StartedAt.IsZero() || session.UpdatedAt.IsZero() {
		return false
	}
	startYear, startMonth, startDay := session.StartedAt.Date()
	endYear, endMonth, endDay := session.UpdatedAt.Date()
	return startYear != endYear || startMonth != endMonth || startDay != endDay
}
