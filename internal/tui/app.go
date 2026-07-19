package tui

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/motoki317/agtlog/internal/model"
	"github.com/motoki317/agtlog/internal/source"
	"github.com/sahilm/fuzzy"
)

type Model struct {
	registry          *source.Registry
	sessions          []*model.Session
	visible           []*model.Session
	visibleProjects   int
	visibleCost       model.Cost
	filter            textinput.Model
	filtering         bool
	sort              sortMode
	agent             agentFilter
	screen            screen
	detail            *detailState
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
	now               func() time.Time
	ctx               context.Context
	refreshGeneration uint64
	detailGeneration  uint64
}

type screen int

type ageTickMsg struct{}

const (
	screenList screen = iota
	screenDetail
)

type sortMode int

const (
	sortAge sortMode = iota
	sortTokens
	sortCost
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
	if m.detail != nil {
		m.detail = m.detail.clone()
	}
	if _, ok := msg.(ageTickMsg); ok {
		m.syncList(m.selectedIdentity())
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
		if m.detail != nil && sessionIdentity(m.detail.session) == loaded.identity {
			if loaded.err != nil {
				m.detail.err = loaded.err
				m.detail.loading = false
				m.detail.rebuild()
				return m, nil
			}
			m.replaceDetail(loaded.session)
		}
		return m, nil
	}
	if update, ok := msg.(source.SessionUpdate); ok {
		m.refreshGeneration++
		openIdentity := ""
		openChanged := false
		if m.detail != nil {
			openIdentity = sessionIdentity(m.detail.session)
			for _, path := range update.RemovedPaths {
				openChanged = openChanged || path == m.detail.session.Path
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
				m.replaceDetail(session)
				return m, nil
			}
			m.screen, m.detail = screenList, nil
		}
		return m, nil
	}
	if size, ok := msg.(tea.WindowSizeMsg); ok {
		selected := m.selectedIdentity()
		m.width, m.height = max(1, size.Width), max(3, size.Height)
		m.syncList(selected)
		if m.detail != nil {
			m.detail.resize(m.width, m.height)
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
	}
	if m.screen == screenDetail {
		if keyMsg, ok := msg.(tea.KeyMsg); ok && key.Matches(keyMsg, m.keys.Theme) {
			m.cycleTheme()
			return m, nil
		}
		if key, ok := msg.(tea.KeyMsg); ok && (key.String() == "esc" || key.String() == "h") {
			m.screen = screenList
			m.detail = nil
			return m, nil
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
	if key, ok := msg.(tea.KeyMsg); ok && !m.filtering && key.String() == "s" {
		m.sort = (m.sort + 1) % 3
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
	if key, ok := msg.(tea.KeyMsg); ok && !m.filtering && key.String() == "enter" && len(m.visible) > 0 {
		index := min(m.cursor, len(m.visible)-1)
		m.screen = screenDetail
		m.detail = newDetailState(m.visible[index], m.width, m.height, m.styles)
		if m.registry != nil {
			m.detail.loading = true
			m.detail.rebuild()
			m.detailGeneration++
			return m, loadDetail(m.ctx, m.registry, m.visible[index], m.detailGeneration)
		}
		return m, nil
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

func (m *Model) replaceDetail(session *model.Session) {
	if m.detail == nil {
		m.detail = newDetailState(session, m.width, m.height, m.styles)
		return
	}
	previous := m.detail
	offset := previous.viewport.YOffset
	focusKey := ""
	if len(previous.focusables) > 0 {
		focusKey = previous.focusables[previous.focus].key
	}
	replacement := newDetailState(session, m.width, m.height, m.styles)
	replacement.wrap = previous.wrap
	for key, expanded := range previous.expanded {
		replacement.expanded[key] = expanded
	}
	replacement.rebuildKeeping(focusKey)
	replacement.viewport.SetYOffset(offset)
	m.detail = replacement
}

func (m *Model) cycleTheme() {
	m.theme = cycleTheme(m.theme)
	m.styles = newStyles(m.theme)
	if m.detail != nil {
		m.detail.styles = m.styles
		m.detail.rebuild()
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
	sort.SliceStable(m.sessions, func(i, j int) bool {
		left, right := m.sessions[i], m.sessions[j]
		switch m.sort {
		case sortTokens:
			return left.TotalUsage().TotalTokens() > right.TotalUsage().TotalTokens()
		case sortCost:
			return left.TotalCost().USD > right.TotalCost().USD
		default:
			return left.UpdatedAt.After(right.UpdatedAt)
		}
	})
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
