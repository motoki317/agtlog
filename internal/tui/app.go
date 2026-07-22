package tui

import (
	"context"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/motoki317/agtlog/internal/model"
	"github.com/motoki317/agtlog/internal/source"
	"github.com/sahilm/fuzzy"
)

type detailScreen interface {
	update(tea.Msg) tea.Cmd
	scrollWheel(tea.MouseButton)
	view() string
	resize(width, height int)
	setWrap(bool)
}

type Model struct {
	registry          *source.Registry
	sessions          []*model.Session
	visible           []*model.Session
	visibleProjects   int
	visibleCost       model.Cost
	filter            textinput.Model
	filtering         bool
	sortState         sortState
	columnFocus       listColumnKind
	agent             agentFilter
	screen            screen
	detail            detailScreen
	detailStack       []detailScreen
	cursor            int
	listOffset        int
	filterSelection   string
	width             int
	height            int
	keys              keyMap
	helpOpen          bool
	status            string
	watchingRoots     int
	theme             Theme
	styles            styles
	absoluteTime      bool
	now               func() time.Time
	ctx               context.Context
	refreshGeneration uint64
	detailGeneration  uint64
}

type screen int

type ageTickMsg struct{}

const mouseWheelRows = 3

const (
	screenList screen = iota
	screenDetail
)

type agentFilter int

const (
	agentAll agentFilter = iota
	agentClaude
	agentCodex
)

func NewModel(sessions []*model.Session, registry *source.Registry) Model {
	return newModelWithClock(sessions, registry, time.Now)
}

func NewModelWithContext(ctx context.Context, sessions []*model.Session, registry *source.Registry) Model {
	m := newModelWithClock(sessions, registry, time.Now)
	m.ctx = ctx
	return m
}

func NewModelWithContextAndTheme(ctx context.Context, sessions []*model.Session, registry *source.Registry, theme Theme) Model {
	m := newModelWithClockAndTheme(sessions, registry, time.Now, theme)
	m.ctx = ctx
	return m
}

func newModelWithClock(sessions []*model.Session, registry *source.Registry, now func() time.Time) Model {
	theme, err := ResolveTheme("")
	if err != nil {
		theme = themes["default"]
	}
	return newModelWithClockAndTheme(sessions, registry, now, theme)
}

func newModelWithClockAndTheme(sessions []*model.Session, registry *source.Registry, now func() time.Time, theme Theme) Model {
	filter := textinput.New()
	filter.Prompt = "/"
	filter.CharLimit = 128
	styleSet := newStyles(theme)
	m := Model{registry: registry, sessions: append([]*model.Session(nil), sessions...), filter: filter, width: 80, height: 24, keys: defaultKeys(), theme: theme, styles: styleSet, now: now, ctx: context.Background()}
	m.rebuildList()
	return m
}

func (m Model) ThemeName() string { return m.theme.Name }

func (m Model) WithWatchingRoots(count int) Model {
	m.watchingRoots = max(0, count)
	return m
}

func (m Model) Init() tea.Cmd { return nextAgeTick() }

func nextAgeTick() tea.Cmd {
	return tea.Tick(time.Minute, func(time.Time) tea.Msg { return ageTickMsg{} })
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if mouse, ok := msg.(tea.MouseMsg); ok {
		if m.helpOpen || (!isMouseWheel(mouse) && !isLeftMouseRelease(mouse)) {
			return m, nil
		}
		if _, item := m.detail.(*itemView); item && isLeftMouseRelease(mouse) {
			return m, nil
		}
		if m.detail != nil && isMouseWheel(mouse) {
			m.detail = cloneDetailScreenForScroll(m.detail)
		} else if m.detail != nil {
			m.detail = cloneDetailScreen(m.detail)
		}
	} else if m.detail != nil {
		m.detail = cloneDetailScreen(m.detail)
	}
	if _, ok := msg.(ageTickMsg); ok {
		m.syncList(m.selectedIdentity())
		m.refreshDetailTimes()
		return m, nextAgeTick()
	}
	if refreshed, ok := msg.(refreshedMsg); ok {
		if refreshed.generation != m.refreshGeneration {
			return m, nil
		}
		if refreshed.err != nil {
			m.status = "refresh: " + terminalText(refreshed.err.Error(), 160)
			return m, nil
		}
		m.sessions = refreshed.sessions
		m.status = "refreshed"
		m.rebuildList()
		return m, nil
	}
	if loaded, ok := msg.(detailLoadedMsg); ok {
		if loaded.generation != m.detailGeneration {
			return m, nil
		}
		if root := m.detailRoot(); root != nil && sessionIdentity(root.session) == loaded.identity {
			if loaded.err != nil {
				root.err = loaded.err
				root.loading = false
				root.rebuild()
				return m, nil
			}
			m.replaceDetailTree(loaded.session)
		}
		return m, nil
	}
	if update, ok := msg.(source.SessionUpdate); ok {
		m.refreshGeneration++
		openIdentity := ""
		openPath := ""
		openChanged := false
		if root := m.detailRoot(); root != nil {
			openIdentity = sessionIdentity(root.session)
			openPath = root.session.Path
			for _, path := range update.RemovedPaths {
				openChanged = openChanged || path == openPath
			}
			for _, session := range update.Sessions {
				openChanged = openChanged || sessionIdentity(session) == openIdentity
			}
		}
		m.applySessionUpdate(update)
		if openIdentity != "" && openChanged {
			for _, session := range m.sessions {
				if sessionIdentity(session) != openIdentity {
					continue
				}
				if m.registry != nil {
					m.detailGeneration++
					return m, loadDetail(m.ctx, m.registry, session, m.detailGeneration)
				}
				m.replaceDetailTree(session)
				return m, nil
			}
			m.screen, m.detail = screenList, nil
			m.detailStack = nil
		}
		return m, nil
	}
	if size, ok := msg.(tea.WindowSizeMsg); ok {
		selected := m.selectedIdentity()
		m.width, m.height = max(1, size.Width), max(3, size.Height)
		m.columnFocus = snapColumnFocus(m.columnFocus, m.visibleListColumns(), sessionListColumnOrder)
		m.syncList(selected)
		if m.detail != nil {
			m.detail.resize(m.width, m.height)
		}
		for index, detail := range m.detailStack {
			m.detailStack[index] = cloneDetailScreen(detail)
			m.detailStack[index].resize(m.width, m.height)
		}
		return m, nil
	}
	if mouse, ok := msg.(tea.MouseMsg); ok {
		if m.screen == screenDetail {
			return m, m.updateDetailMouse(mouse)
		}
		if m.screen == screenList && isMouseWheel(mouse) {
			switch mouse.Button {
			case tea.MouseButtonWheelUp:
				m.listOffset = max(0, m.listOffset-mouseWheelRows)
			case tea.MouseButtonWheelDown:
				maxOffset := max(0, len(m.visible)-m.listRowCapacity())
				m.listOffset = min(m.listOffset+mouseWheelRows, maxOffset)
			}
		}
		if m.screen == screenList && !m.filtering && isLeftMouseRelease(mouse) && mouse.X > 0 && mouse.X < m.width-1 {
			if index, ok := m.rowAtY(mouse.Y); ok {
				alreadySelected := index == m.cursor
				m.cursor = index
				m.filterSelection = m.selectedIdentity()
				if alreadySelected {
					return m, m.openListSelection()
				}
			}
		}
		return m, nil
	}
	if keyMsg, ok := msg.(tea.KeyMsg); ok {
		if key.Matches(keyMsg, m.keys.Quit) && (!m.filtering || keyMsg.String() == "ctrl+c") {
			return m, tea.Quit
		}
		if m.helpOpen {
			if key.Matches(keyMsg, m.keys.Help, m.keys.Back) {
				m.helpOpen = false
			}
			return m, nil
		}
		if !m.filtering && key.Matches(keyMsg, m.keys.Help) {
			m.helpOpen = true
			return m, nil
		}
		if !m.filtering && key.Matches(keyMsg, m.keys.TimeFormat) {
			m.toggleTimeFormat()
			return m, nil
		}
	}
	if m.screen == screenDetail {
		if keyMsg, ok := msg.(tea.KeyMsg); ok && key.Matches(keyMsg, m.keys.Theme) {
			m.cycleTheme()
			return m, nil
		}
		if keyMsg, ok := msg.(tea.KeyMsg); ok && key.Matches(keyMsg, m.keys.Back) {
			if last := len(m.detailStack) - 1; last >= 0 {
				m.detail = m.detailStack[last]
				m.detailStack = m.detailStack[:last]
			} else {
				m.screen = screenList
				m.detail = nil
			}
			return m, nil
		}
		if keyMsg, ok := msg.(tea.KeyMsg); ok && key.Matches(keyMsg, m.keys.Collapse, m.keys.Expand) {
			if detail, ok := m.detail.(*detailState); ok && detail.tab == tabTimeline {
				if key.Matches(keyMsg, m.keys.Collapse) {
					detail.collapseFocused()
				} else {
					detail.expandFocused()
				}
				return m, nil
			}
		}
		if key, ok := msg.(tea.KeyMsg); ok && (key.String() == "enter" || key.String() == "l") {
			if m.activateDetailSelection() {
				return m, nil
			}
		}
		return m, m.detail.update(msg)
	}
	if keyMsg, ok := msg.(tea.KeyMsg); ok && !m.filtering && key.Matches(keyMsg, m.keys.Refresh) && m.registry != nil {
		m.status = "refreshing…"
		m.refreshGeneration++
		return m, refreshSessions(m.ctx, m.registry, m.refreshGeneration)
	}
	if key, ok := msg.(tea.KeyMsg); ok && !m.filtering && key.Type == tea.KeyRunes && len(key.Runes) > 0 && key.Runes[0] == '/' {
		if selected := m.selectedIdentity(); selected != "" {
			m.filterSelection = selected
		}
		m.filtering = true
		focusCmd := m.filter.Focus()
		if len(key.Runes) == 1 {
			return m, focusCmd
		}
		var inputCmd tea.Cmd
		m.filter, inputCmd = m.filter.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: key.Runes[1:]})
		m.applyFilter()
		m.syncList(m.filterSelection)
		return m, tea.Batch(focusCmd, inputCmd)
	}
	if keyMsg, ok := msg.(tea.KeyMsg); ok && !m.filtering && key.Matches(keyMsg, m.keys.SortColumn) {
		m.sortState = m.sortState.press(m.columnFocus)
		m.rebuildList()
		return m, nil
	}
	if keyMsg, ok := msg.(tea.KeyMsg); ok && !m.filtering && key.Matches(keyMsg, m.keys.ColumnLeft, m.keys.ColumnRight) {
		delta := -1
		if key.Matches(keyMsg, m.keys.ColumnRight) {
			delta = 1
		}
		m.columnFocus = moveColumnFocus(m.columnFocus, m.visibleListColumns(), sessionListColumnOrder, delta)
		return m, nil
	}
	if keyMsg, ok := msg.(tea.KeyMsg); ok && !m.filtering && key.Matches(keyMsg, m.keys.SortAge) {
		if columnVisible(columnAge, m.visibleListColumns()) {
			m.columnFocus = columnAge
		}
		m.sortState = m.sortState.press(columnAge)
		m.rebuildList()
		return m, nil
	}
	if keyMsg, ok := msg.(tea.KeyMsg); ok && !m.filtering && key.Matches(keyMsg, m.keys.SortTitle) {
		if columnVisible(columnTitle, m.visibleListColumns()) {
			m.columnFocus = columnTitle
		}
		m.sortState = m.sortState.press(columnTitle)
		m.rebuildList()
		return m, nil
	}
	if key, ok := msg.(tea.KeyMsg); ok && !m.filtering && key.String() == "a" {
		m.agent = (m.agent + 1) % 3
		m.rebuildList()
		return m, nil
	}
	if keyMsg, ok := msg.(tea.KeyMsg); ok && !m.filtering && key.Matches(keyMsg, m.keys.Theme) {
		m.cycleTheme()
		return m, nil
	}
	if key, ok := msg.(tea.KeyMsg); ok && !m.filtering && key.String() == "enter" {
		return m, m.openListSelection()
	}
	if m.filtering {
		if keyMsg, ok := msg.(tea.KeyMsg); ok {
			switch keyMsg.String() {
			case "esc":
				selected := m.filterSelection
				m.filtering = false
				m.filter.Reset()
				m.filter.Blur()
				m.applyFilter()
				m.syncList(selected)
				return m, nil
			case "enter":
				m.filtering = false
				m.filter.Blur()
				return m, nil
			}
		}
		var cmd tea.Cmd
		m.filter, cmd = m.filter.Update(msg)
		m.applyFilter()
		m.syncList(m.filterSelection)
		return m, cmd
	}
	if key, ok := msg.(tea.KeyMsg); ok {
		switch key.String() {
		case "j", "down":
			m.moveListSelection(1)
		case "k", "up":
			m.moveListSelection(-1)
		case "pgdown":
			m.moveListSelection(max(1, m.listRowCapacity()))
		case "pgup":
			m.moveListSelection(-max(1, m.listRowCapacity()))
		case "home", "g":
			m.cursor = 0
			m.ensureListSelectionVisible()
			m.filterSelection = m.selectedIdentity()
		case "end", "G":
			m.cursor = max(0, len(m.visible)-1)
			m.ensureListSelectionVisible()
			m.filterSelection = m.selectedIdentity()
		}
	}
	return m, nil
}

func isLeftMouseRelease(mouse tea.MouseMsg) bool {
	return mouse.Action == tea.MouseActionRelease && mouse.Button == tea.MouseButtonLeft
}

func isMouseWheel(mouse tea.MouseMsg) bool {
	return tea.MouseEvent(mouse).IsWheel()
}

func (m *Model) updateDetailMouse(mouse tea.MouseMsg) tea.Cmd {
	if isMouseWheel(mouse) {
		m.detail.scrollWheel(mouse.Button)
		return nil
	}
	detail, ok := m.detail.(*detailState)
	if !ok || !isLeftMouseRelease(mouse) || mouse.X <= 0 || mouse.X >= m.width-1 {
		return nil
	}
	index, ok := detail.rowAtY(mouse.Y)
	if !ok {
		return nil
	}
	detail.selectRow(index)
	if detail.selectedExpandable() {
		return detail.update(tea.KeyMsg{Type: tea.KeySpace})
	}
	return nil
}

func (m *Model) activateDetailSelection() bool {
	detail, ok := m.detail.(*detailState)
	if !ok {
		return false
	}
	if subagent := detail.focusedSubagent(); subagent != nil {
		wrap := detail.wrap
		crumbs := append([]string(nil), detail.crumbs...)
		if label := detailCrumbLabel(detail.session); label != "" {
			crumbs = append(crumbs, label)
		}
		if detail.tab == tabSubagents {
			crumbs = append(crumbs, subagentCrumbLabels(detail.session, subagent)...)
		}
		m.detailStack = append(m.detailStack, m.detail)
		child := newDetailStateBase(subagent, m.width, m.height, m.styles)
		child.now = m.now()
		child.absoluteTime = m.absoluteTime
		child.crumbs = crumbs
		child.defaultExpanded = detail.defaultExpanded
		child.wrap = wrap
		child.subagentSort = detail.subagentSort
		child.subagentColumnFocus = detail.subagentColumnFocus
		child.focus = -1
		child.resize(m.width, m.height)
		child.anchorBottom()
		m.detail = child
		return true
	}
	if event, exists := detail.focusedEvent(); exists {
		crumbs := append([]string(nil), detail.crumbs...)
		if label := detailCrumbLabel(detail.session); label != "" {
			crumbs = append(crumbs, label)
		}
		m.detailStack = append(m.detailStack, m.detail)
		item := newItemViewWithState(event, detail.session.Agent, crumbs, m.width, m.height, m.styles, m.now(), false, detail.wrap)
		item.focusKey = detail.focusables[detail.focus].key
		m.detail = item
		return true
	}
	return false
}

func (m *Model) openListSelection() tea.Cmd {
	if len(m.visible) == 0 {
		return nil
	}
	index := min(m.cursor, len(m.visible)-1)
	m.screen = screenDetail
	m.detailStack = nil
	detail := newDetailState(m.visible[index], m.width, m.height, m.styles)
	detail.now = m.now()
	detail.absoluteTime = m.absoluteTime
	detail.rebuild()
	detail.anchorBottom()
	m.detail = detail
	if m.registry == nil {
		return nil
	}
	detail.loading = true
	detail.rebuild()
	m.detailGeneration++
	return loadDetail(m.ctx, m.registry, m.visible[index], m.detailGeneration)
}

func detailCrumbLabel(session *model.Session) string {
	if title := firstLine(session.Title); title != "" {
		return ansi.Truncate(title, 48, "…")
	}
	return terminalText(session.ID, 48)
}

func subagentCrumbLabels(root, target *model.Session) []string {
	path := sessionPath(root, target)
	if len(path) < 2 {
		return nil
	}
	labels := make([]string, 0, len(path)-2)
	for _, session := range path[1 : len(path)-1] {
		if label := detailCrumbLabel(session); label != "" {
			labels = append(labels, label)
		}
	}
	return labels
}

func sessionPath(root, target *model.Session) []*model.Session {
	var path []*model.Session
	var find func(*model.Session) bool
	find = func(session *model.Session) bool {
		path = append(path, session)
		if session == target {
			return true
		}
		for _, child := range session.Subagents {
			if find(child) {
				return true
			}
		}
		path = path[:len(path)-1]
		return false
	}
	if !find(root) {
		return nil
	}
	return path
}

func detailBreadcrumbs(root, target *model.Session) []string {
	path := sessionPath(root, target)
	if len(path) == 0 {
		return nil
	}
	crumbs := make([]string, 0, len(path))
	if project := terminalText(root.Project, 96); project != "" {
		crumbs = append(crumbs, project)
	}
	for _, ancestor := range path[:len(path)-1] {
		if label := detailCrumbLabel(ancestor); label != "" {
			crumbs = append(crumbs, label)
		}
	}
	return crumbs
}

func (m Model) detailRoot() *detailState {
	for _, screen := range m.detailStack {
		if detail, ok := screen.(*detailState); ok {
			return detail
		}
	}
	detail, _ := m.detail.(*detailState)
	return detail
}

func cloneDetailScreen(screen detailScreen) detailScreen {
	switch screen := screen.(type) {
	case *detailState:
		return screen.clone()
	case *itemView:
		return screen.clone()
	default:
		return screen
	}
}

func cloneDetailScreenForScroll(screen detailScreen) detailScreen {
	switch screen := screen.(type) {
	case *detailState:
		copy := *screen
		return &copy
	case *itemView:
		copy := *screen
		return &copy
	default:
		return screen
	}
}

func (m *Model) replaceDetailTree(root *model.Session) {
	screens := append(append([]detailScreen(nil), m.detailStack...), m.detail)
	sessions := make(map[string]*model.Session)
	var indexSessions func(*model.Session)
	indexSessions = func(session *model.Session) {
		sessions[sessionIdentity(session)] = session
		for _, child := range session.Subagents {
			indexSessions(child)
		}
	}
	indexSessions(root)
	replacements := make([]detailScreen, 0, len(screens))
	for _, screen := range screens {
		state, ok := screen.(*detailState)
		if !ok {
			if item, ok := screen.(*itemView); ok && len(replacements) > 0 {
				parent, parentOK := replacements[len(replacements)-1].(*detailState)
				if !parentOK {
					continue
				}
				event, eventOK := parent.eventForKey(item.focusKey)
				if !eventOK || !sameItemEvent(item.event, event) {
					continue
				}
				crumbs := append([]string(nil), parent.crumbs...)
				if label := detailCrumbLabel(parent.session); label != "" {
					crumbs = append(crumbs, label)
				}
				replacement := newItemViewWithState(event, parent.session.Agent, crumbs, m.width, m.height, m.styles, m.now(), item.showRaw, item.wrap)
				replacement.focusKey = item.focusKey
				replacement.viewport.SetYOffset(item.viewport.YOffset)
				replacements = append(replacements, replacement)
			}
			continue
		}
		session := sessions[sessionIdentity(state.session)]
		if session == nil {
			break
		}
		replacements = append(replacements, m.replacementDetailState(state, session, root))
	}
	if len(replacements) == 0 {
		m.screen, m.detail, m.detailStack = screenList, nil, nil
		return
	}
	last := len(replacements) - 1
	m.detail = replacements[last]
	m.detailStack = replacements[:last]
}

func sameItemEvent(previous, replacement model.Event) bool {
	if previous.Kind != replacement.Kind {
		return false
	}
	if previous.CallID != "" || replacement.CallID != "" {
		return previous.CallID != "" && previous.CallID == replacement.CallID
	}
	if previous.AgentID != "" || replacement.AgentID != "" {
		return previous.AgentID != "" && previous.AgentID == replacement.AgentID
	}
	if !previous.Timestamp.IsZero() || !replacement.Timestamp.IsZero() {
		return !previous.Timestamp.IsZero() && previous.Timestamp.Equal(replacement.Timestamp)
	}
	return previous.Kind != model.EventToolCall || previous.ToolName == replacement.ToolName
}

func (m *Model) replacementDetailState(previous *detailState, session, root *model.Session) *detailState {
	offset := previous.viewport.YOffset
	pinned := previous.tab == tabTimeline && previous.pinnedToBottom()
	focusKey := ""
	if len(previous.focusables) > 0 {
		focusKey = previous.focusables[previous.focus].key
	}
	selectedSubagent := ""
	if previous.tab == tabSubagents {
		if session := previous.focusedSubagent(); session != nil {
			selectedSubagent = sessionIdentity(session)
		}
	}
	replacement := newDetailStateBase(session, m.width, m.height, m.styles)
	replacement.now = m.now()
	replacement.absoluteTime = m.absoluteTime
	replacement.wrap = previous.wrap
	replacement.defaultExpanded = previous.defaultExpanded
	replacement.crumbs = detailBreadcrumbs(root, session)
	replacement.tab = previous.tab
	replacement.subagentSort = previous.subagentSort
	replacement.subagentColumnFocus = previous.subagentColumnFocus
	replacement.subagentSelection = previous.subagentSelection
	replacement.focus = previous.focus
	if pinned {
		replacement.focus = -1
	}
	for key, expanded := range previous.expanded {
		replacement.expanded[key] = expanded
	}
	replacement.resize(m.width, m.height)
	if replacement.tab == tabSubagents {
		replacement.viewport.SetYOffset(offset)
	}
	if replacement.tab == tabSubagents && selectedSubagent != "" {
		for index, item := range replacement.subagents {
			if sessionIdentity(item.s) == selectedSubagent {
				replacement.subagentSelection = index
				replacement.selectedLine = subagentDetailLine(index)
				replacement.rebuildRendered()
				break
			}
		}
	} else if replacement.tab == tabTimeline && !pinned {
		for index, item := range replacement.focusables {
			if item.key == focusKey && index != replacement.focus {
				oldLine := replacement.selectedLine
				replacement.focus = index
				replacement.updateSelection(oldLine, item.line)
				break
			}
		}
	}
	if pinned {
		replacement.anchorBottom()
	} else if replacement.tab != tabSubagents {
		replacement.viewport.SetYOffset(offset)
	}
	return replacement
}

func (m *Model) cycleTheme() {
	m.theme = cycleTheme(m.theme)
	m.styles = newStyles(m.theme)
	if m.detail != nil {
		setDetailScreenStyles(m.detail, m.styles)
	}
	for index, detail := range m.detailStack {
		m.detailStack[index] = cloneDetailScreen(detail)
		setDetailScreenStyles(m.detailStack[index], m.styles)
	}
}

func (m *Model) toggleTimeFormat() {
	m.absoluteTime = !m.absoluteTime
	m.columnFocus = snapColumnFocus(m.columnFocus, m.visibleListColumns(), sessionListColumnOrder)
	m.refreshDetailTimes()
}

func (m *Model) refreshDetailTimes() {
	now := m.now()
	refresh := func(screen detailScreen) detailScreen {
		screen = cloneDetailScreen(screen)
		if detail, ok := screen.(*detailState); ok {
			detail.now = now
			detail.absoluteTime = m.absoluteTime
			detail.rebuildPreservingViewport()
		} else if item, ok := screen.(*itemView); ok {
			item.setNow(now)
		}
		return screen
	}
	if m.detail != nil {
		m.detail = refresh(m.detail)
	}
	for index, screen := range m.detailStack {
		m.detailStack[index] = refresh(screen)
	}
}

func setDetailScreenStyles(screen detailScreen, styleSet styles) {
	switch screen := screen.(type) {
	case *detailState:
		screen.styles = styleSet
		screen.rebuild()
	case *itemView:
		screen.styles = styleSet
		screen.rebuild()
	}
}

type refreshedMsg struct {
	generation uint64
	sessions   []*model.Session
	err        error
}

func refreshSessions(ctx context.Context, registry *source.Registry, generation uint64) tea.Cmd {
	return func() tea.Msg {
		sessions, err := registry.Discover(ctx)
		return refreshedMsg{generation: generation, sessions: sessions, err: err}
	}
}

func (m *Model) applySessionUpdate(update source.SessionUpdate) {
	removed := make(map[string]bool, len(update.RemovedPaths))
	for _, path := range update.RemovedPaths {
		removed[path] = true
	}
	kept := m.sessions[:0]
	for _, session := range m.sessions {
		if !removed[session.Path] {
			kept = append(kept, session)
		}
	}
	m.sessions = kept
	indices := make(map[string]int, len(m.sessions))
	for index, session := range m.sessions {
		indices[sessionIdentity(session)] = index
	}
	for _, session := range update.Sessions {
		identity := sessionIdentity(session)
		if index, exists := indices[identity]; exists {
			m.sessions[index] = session
		} else {
			indices[identity] = len(m.sessions)
			m.sessions = append(m.sessions, session)
		}
	}
	m.rebuildList()
}

type detailLoadedMsg struct {
	generation uint64
	identity   string
	session    *model.Session
	err        error
}

func loadDetail(ctx context.Context, registry *source.Registry, session *model.Session, generation uint64) tea.Cmd {
	identity := sessionIdentity(session)
	copy := cloneSession(session)
	return func() tea.Msg {
		err := registry.LoadDetail(ctx, copy)
		return detailLoadedMsg{generation: generation, identity: identity, session: copy, err: err}
	}
}

func cloneSession(session *model.Session) *model.Session {
	return cloneSessionGraph(session, make(map[*model.Session]*model.Session))
}

func cloneSessionGraph(session *model.Session, cloned map[*model.Session]*model.Session) *model.Session {
	if copy := cloned[session]; copy != nil {
		return copy
	}
	copy := *session
	cloned[session] = &copy
	copy.Models = append([]string(nil), session.Models...)
	copy.Usage = append([]model.Usage(nil), session.Usage...)
	copy.ModelCosts = make(map[string]float64, len(session.ModelCosts))
	for name, cost := range session.ModelCosts {
		copy.ModelCosts[name] = cost
	}
	copy.ModelCostBreakdowns = make(map[string]model.CostBreakdown, len(session.ModelCostBreakdowns))
	for name, breakdown := range session.ModelCostBreakdowns {
		copy.ModelCostBreakdowns[name] = breakdown.Clone()
	}
	copy.Cost.MissingPricingModels = append([]string(nil), session.Cost.MissingPricingModels...)
	copy.Events = append([]model.Event(nil), session.Events...)
	copy.Subagents = make([]*model.Session, len(session.Subagents))
	for index, subagent := range session.Subagents {
		copy.Subagents[index] = cloneSessionGraph(subagent, cloned)
	}
	for index := range copy.Events {
		if copy.Events[index].Subagent != nil {
			copy.Events[index].Subagent = cloneSessionGraph(copy.Events[index].Subagent, cloned)
		}
	}
	return &copy
}

func (m *Model) rebuildList() {
	selected := m.selectedIdentity()
	state := m.sortState
	if !state.active {
		state = sortState{kind: columnAge, desc: true, active: true}
	}
	sortSessions(m.sessions, state)
	m.applyFilter()
	m.syncList(selected)
}

func (m Model) selectedIdentity() string {
	if len(m.visible) == 0 {
		return ""
	}
	index := max(0, min(m.cursor, len(m.visible)-1))
	return sessionIdentity(m.visible[index])
}

func (m *Model) syncList(selected string) {
	cursor := min(m.cursor, max(0, len(m.visible)-1))
	for index, session := range m.visible {
		if sessionIdentity(session) == selected {
			cursor = index
			break
		}
	}
	m.cursor = cursor
	m.ensureListSelectionVisible()
	if !m.filtering {
		if current := m.selectedIdentity(); current != "" {
			m.filterSelection = current
		}
	}
}

func (m *Model) moveListSelection(delta int) {
	if len(m.visible) == 0 {
		m.cursor, m.listOffset = 0, 0
		return
	}
	m.cursor = max(0, min(m.cursor+delta, len(m.visible)-1))
	m.ensureListSelectionVisible()
	m.filterSelection = m.selectedIdentity()
}

func (m Model) listRowCapacity() int {
	return newListLayout(m.height, m.filtering).rowCapacity
}

func (m *Model) ensureListSelectionVisible() {
	capacity := m.listRowCapacity()
	if capacity <= 0 || len(m.visible) == 0 {
		m.listOffset = 0
		return
	}
	if m.cursor < m.listOffset {
		m.listOffset = m.cursor
	} else if m.cursor >= m.listOffset+capacity {
		m.listOffset = m.cursor - capacity + 1
	}
	m.listOffset = max(0, min(m.listOffset, max(0, len(m.visible)-capacity)))
}

func (m *Model) applyFilter() {
	candidates := make([]*model.Session, 0, len(m.sessions))
	for _, session := range m.sessions {
		if (m.agent == agentClaude && session.Agent != model.AgentClaude) || (m.agent == agentCodex && session.Agent != model.AgentCodex) {
			continue
		}
		candidates = append(candidates, session)
	}
	query := strings.ToLower(strings.TrimSpace(terminalText(m.filter.Value(), 128)))
	if query == "" {
		m.visible = candidates
		m.updateVisibleSummary()
		return
	}
	haystacks := make([]string, len(candidates))
	for index, session := range candidates {
		haystacks[index] = strings.ToLower(strings.Join([]string{
			terminalText(string(session.Agent), 32), terminalText(session.Project, 96), terminalText(session.Title, 160),
		}, " "))
	}
	matches := fuzzy.FindNoSort(query, haystacks)
	m.visible = make([]*model.Session, 0, len(matches))
	for _, match := range matches {
		m.visible = append(m.visible, candidates[match.Index])
	}
	m.updateVisibleSummary()
}

func (m *Model) updateVisibleSummary() {
	projects := make(map[string]bool)
	missing := make(map[string]bool)
	total := model.Cost{}
	for _, session := range m.visible {
		projects[session.Project] = true
		cost := session.TotalCost()
		total.USD += cost.USD
		total.Estimated = total.Estimated || cost.Estimated
		for _, name := range cost.MissingPricingModels {
			if !missing[name] {
				total.MissingPricingModels = append(total.MissingPricingModels, name)
				missing[name] = true
			}
		}
	}
	m.visibleProjects = len(projects)
	m.visibleCost = total
}

func (m Model) View() string {
	if m.helpOpen {
		return m.helpView()
	}
	if m.screen == screenDetail && m.detail != nil {
		return m.detail.view()
	}
	return m.listView()
}

func (m Model) StaticView() string {
	contextHeight := 3
	if m.filtering {
		contextHeight++
	}
	m.height = max(m.height, len(m.visible)+contextHeight+4)
	return m.listView()
}
