package tui

import "github.com/charmbracelet/bubbles/key"

const (
	expandAllKey   = "E"
	collapseAllKey = "C"
	timeFormatKey  = "T"
	sortColumnKey  = "O"
	sortAgeKey     = "A"
	sortTitleKey   = "N"
)

type keyMap struct {
	SortColumn  key.Binding
	SortAge     key.Binding
	SortTitle   key.Binding
	ColumnLeft  key.Binding
	ColumnRight key.Binding
	Refresh     key.Binding
	Theme       key.Binding
	TimeFormat  key.Binding
	Help        key.Binding
	Quit        key.Binding
	Back        key.Binding
	Collapse    key.Binding
	Expand      key.Binding
}

func defaultKeys() keyMap {
	return keyMap{
		SortColumn:  key.NewBinding(key.WithKeys(sortColumnKey)),
		SortAge:     key.NewBinding(key.WithKeys(sortAgeKey)),
		SortTitle:   key.NewBinding(key.WithKeys(sortTitleKey)),
		ColumnLeft:  key.NewBinding(key.WithKeys("left")),
		ColumnRight: key.NewBinding(key.WithKeys("right")),
		Refresh:     key.NewBinding(key.WithKeys("r")),
		Theme:       key.NewBinding(key.WithKeys("t")),
		TimeFormat:  key.NewBinding(key.WithKeys(timeFormatKey)),
		Help:        key.NewBinding(key.WithKeys("?")),
		Quit:        key.NewBinding(key.WithKeys("q", "ctrl+c")),
		Back:        key.NewBinding(key.WithKeys("esc", "h")),
		Collapse:    key.NewBinding(key.WithKeys("left")),
		Expand:      key.NewBinding(key.WithKeys("right")),
	}
}
