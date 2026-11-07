package ui

import "github.com/charmbracelet/bubbles/key"

// keyMap lists every binding the workload screen understands.
type keyMap struct {
	Up, Down         key.Binding
	PageUp, PageDown key.Binding
	Top, Bottom      key.Binding
	DetailUp         key.Binding
	DetailDown       key.Binding
	Quit             key.Binding
}

func newKeyMap() keyMap {
	return keyMap{
		Up:         key.NewBinding(key.WithKeys("up", "k"), key.WithHelp("↑/k", "up")),
		Down:       key.NewBinding(key.WithKeys("down", "j"), key.WithHelp("↓/j", "down")),
		PageUp:     key.NewBinding(key.WithKeys("pgup"), key.WithHelp("pgup", "page up")),
		PageDown:   key.NewBinding(key.WithKeys("pgdown"), key.WithHelp("pgdn", "page down")),
		Top:        key.NewBinding(key.WithKeys("home", "g"), key.WithHelp("g", "top")),
		Bottom:     key.NewBinding(key.WithKeys("end", "G"), key.WithHelp("G", "bottom")),
		DetailUp:   key.NewBinding(key.WithKeys("ctrl+u"), key.WithHelp("ctrl+u", "scroll detail up")),
		DetailDown: key.NewBinding(key.WithKeys("ctrl+d"), key.WithHelp("ctrl+d", "scroll detail down")),
		Quit:       key.NewBinding(key.WithKeys("q", "ctrl+c"), key.WithHelp("q", "quit")),
	}
}

// shortHelp is the footer hint line.
func (k keyMap) shortHelp() []key.Binding {
	return []key.Binding{k.Up, k.Down, k.DetailDown, k.Quit}
}
