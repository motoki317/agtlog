package tui

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/help"
	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/table"
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
	filter            textinput.Model
	filtering         bool
	sort              sortMode
	agent             agentFilter
	screen            screen
	detail            *detailState
	table             table.Model
	width             int
	height            int
	help              help.Model
	keys              keyMap
	helpOpen          bool
	status            string
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

func newModelWithClock(sessions []*model.Session, registry *source.Registry, now func() time.Time) Model {
	filter := textinput.New()
	filter.Prompt = "/"
	filter.CharLimit = 128
	tableModel := table.New(
		table.WithColumns(listColumns(80)),
		table.WithFocused(true),
		table.WithHeight(16),
		table.WithWidth(80),
	)
	helpModel := help.New()
	helpModel.ShowAll = true
	helpModel.Width = 80
	styleSet := newStyles()
	tableModel.SetStyles(styleSet.table)
	m := Model{registry: registry, sessions: append([]*model.Session(nil), sessions...), filter: filter, table: tableModel, width: 80, height: 24, help: helpModel, keys: defaultKeys(), styles: styleSet, now: now, ctx: context.Background()}
	m.rebuildList()
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
		m.syncTable(m.selectedIdentity())
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
		m.table.SetWidth(m.width)
		m.table.SetHeight(max(1, m.height-2))
		m.table.SetRows(nil)
		m.table.SetColumns(listColumns(m.width))
		m.syncTable(selected)
		m.help.Width = m.width
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
		m.filtering = true
		focusCmd := m.filter.Focus()
		if len(key.Runes) == 1 {
			return m, focusCmd
		}
		selected := m.selectedIdentity()
		var inputCmd tea.Cmd
		m.filter, inputCmd = m.filter.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: key.Runes[1:]})
		m.applyFilter()
		m.syncTable(selected)
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
	if key, ok := msg.(tea.KeyMsg); ok && !m.filtering && key.String() == "enter" && len(m.visible) > 0 {
		index := min(m.table.Cursor(), len(m.visible)-1)
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
				m.filtering = false
				m.filter.Reset()
				m.filter.Blur()
				m.rebuildList()
				return m, nil
			case "enter":
				m.filtering = false
				m.filter.Blur()
				return m, nil
			}
		}
		var cmd tea.Cmd
		selected := m.selectedIdentity()
		m.filter, cmd = m.filter.Update(msg)
		m.applyFilter()
		m.syncTable(selected)
		return m, cmd
	}
	var cmd tea.Cmd
	m.table, cmd = m.table.Update(msg)
	return m, cmd
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
	for key, expanded := range previous.expanded {
		replacement.expanded[key] = expanded
	}
	replacement.rebuildKeeping(focusKey)
	replacement.viewport.SetYOffset(offset)
	m.detail = replacement
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
	m.syncTable(selected)
}

func (m Model) selectedIdentity() string {
	if len(m.visible) == 0 {
		return ""
	}
	index := max(0, min(m.table.Cursor(), len(m.visible)-1))
	return sessionIdentity(m.visible[index])
}

func (m *Model) syncTable(selected string) {
	rows := make([]table.Row, 0, len(m.visible))
	columnCount := len(m.table.Columns())
	cursor := 0
	for index, session := range m.visible {
		row := sessionRow(session, m.now(), m.styles)
		rows = append(rows, row[:min(len(row), columnCount)])
		if sessionIdentity(session) == selected {
			cursor = index
		}
	}
	m.table.SetRows(rows)
	m.table.SetCursor(cursor)
}

func (m *Model) applyFilter() {
	candidates := make([]*model.Session, 0, len(m.sessions))
	for _, session := range m.sessions {
		if (m.agent == agentClaude && session.Agent != model.AgentClaude) || (m.agent == agentCodex && session.Agent != model.AgentCodex) {
			continue
		}
		candidates = append(candidates, session)
	}
	query := strings.ToLower(strings.TrimSpace(m.filter.Value()))
	if query == "" {
		m.visible = candidates
		return
	}
	haystacks := make([]string, len(candidates))
	for index, session := range candidates {
		haystacks[index] = strings.ToLower(strings.Join([]string{string(session.Agent), session.Project, session.Title}, " "))
	}
	matches := fuzzy.FindNoSort(query, haystacks)
	m.visible = make([]*model.Session, 0, len(matches))
	for _, match := range matches {
		m.visible = append(m.visible, candidates[match.Index])
	}
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
	m.table.SetHeight(max(1, len(m.visible)+1))
	return m.listView()
}
