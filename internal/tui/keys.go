package tui

import "github.com/charmbracelet/bubbles/key"

type keyMap struct {
	MoveDown  key.Binding
	MoveUp    key.Binding
	Filter    key.Binding
	Sort      key.Binding
	Agent     key.Binding
	Open      key.Binding
	Refresh   key.Binding
	Theme     key.Binding
	Help      key.Binding
	Quit      key.Binding
	Back      key.Binding
	Toggle    key.Binding
	NextAgent key.Binding
	PrevAgent key.Binding
}

func defaultKeys() keyMap {
	return keyMap{
		MoveDown:  key.NewBinding(key.WithKeys("j", "down"), key.WithHelp("j/↓", "down")),
		MoveUp:    key.NewBinding(key.WithKeys("k", "up"), key.WithHelp("k/↑", "up")),
		Filter:    key.NewBinding(key.WithKeys("/"), key.WithHelp("/", "filter")),
		Sort:      key.NewBinding(key.WithKeys("s"), key.WithHelp("s", "sort")),
		Agent:     key.NewBinding(key.WithKeys("a"), key.WithHelp("a", "agent")),
		Open:      key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "open")),
		Refresh:   key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "refresh")),
		Theme:     key.NewBinding(key.WithKeys("t"), key.WithHelp("t", "theme")),
		Help:      key.NewBinding(key.WithKeys("?"), key.WithHelp("?", "help")),
		Quit:      key.NewBinding(key.WithKeys("q", "ctrl+c"), key.WithHelp("q", "quit")),
		Back:      key.NewBinding(key.WithKeys("esc", "h"), key.WithHelp("esc/h", "back")),
		Toggle:    key.NewBinding(key.WithKeys(" ", "enter"), key.WithHelp("space/enter", "expand")),
		NextAgent: key.NewBinding(key.WithKeys("J"), key.WithHelp("J", "next subagent")),
		PrevAgent: key.NewBinding(key.WithKeys("K"), key.WithHelp("K", "previous subagent")),
	}
}

func (k keyMap) ShortHelp() []key.Binding {
	return []key.Binding{k.Filter, k.Sort, k.Open, k.Help, k.Quit}
}

func (k keyMap) FullHelp() [][]key.Binding {
	return [][]key.Binding{
		{k.MoveDown, k.MoveUp, k.Filter, k.Sort, k.Agent},
		{k.Open, k.Refresh, k.Theme, k.Toggle, k.NextAgent, k.PrevAgent},
		{k.Back, k.Help, k.Quit},
	}
}
