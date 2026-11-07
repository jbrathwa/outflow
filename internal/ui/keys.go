package ui

import "github.com/charmbracelet/bubbles/key"

// keyMap lists every binding the workload screen understands.
type keyMap struct {
	Up, Down         key.Binding
	Move             key.Binding // help-only: "↑/↓ move" in the short footer
	PageUp, PageDown key.Binding
	Top, Bottom      key.Binding
	DetailUp         key.Binding
	DetailDown       key.Binding
	Enter, Back      key.Binding
	Filter, Status   key.Binding
	Exception, Rerun key.Binding
	Help, Quit       key.Binding
}

func newKeyMap() keyMap {
	return keyMap{
		Up:         key.NewBinding(key.WithKeys("up", "k"), key.WithHelp("↑/k", "up")),
		Down:       key.NewBinding(key.WithKeys("down", "j"), key.WithHelp("↓/j", "down")),
		Move:       key.NewBinding(key.WithKeys("up", "down", "k", "j"), key.WithHelp("↑/↓", "move")),
		PageUp:     key.NewBinding(key.WithKeys("pgup"), key.WithHelp("pgup", "page up")),
		PageDown:   key.NewBinding(key.WithKeys("pgdown"), key.WithHelp("pgdn", "page down")),
		Top:        key.NewBinding(key.WithKeys("home", "g"), key.WithHelp("g", "top")),
		Bottom:     key.NewBinding(key.WithKeys("end", "G"), key.WithHelp("G", "bottom")),
		DetailUp:   key.NewBinding(key.WithKeys("ctrl+u"), key.WithHelp("ctrl+u", "scroll detail up")),
		DetailDown: key.NewBinding(key.WithKeys("ctrl+d"), key.WithHelp("ctrl+d", "scroll detail down")),
		Enter:      key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "detail")),
		Back:       key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "back")),
		Filter:     key.NewBinding(key.WithKeys("/"), key.WithHelp("/", "filter")),
		Status:     key.NewBinding(key.WithKeys("f"), key.WithHelp("f", "status")),
		Exception:  key.NewBinding(key.WithKeys("e"), key.WithHelp("e", "exception")),
		Rerun:      key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "rerun")),
		Help:       key.NewBinding(key.WithKeys("?"), key.WithHelp("?", "help")),
		Quit:       key.NewBinding(key.WithKeys("q", "ctrl+c"), key.WithHelp("q", "quit")),
	}
}

// ShortHelp is the footer hint line (help.KeyMap).
func (k keyMap) ShortHelp() []key.Binding {
	return []key.Binding{k.Move, k.Enter, k.Filter, k.Status, k.Exception, k.Rerun, k.Help, k.Quit}
}

// FullHelp is the `?` view, one column per group (help.KeyMap).
func (k keyMap) FullHelp() [][]key.Binding {
	return [][]key.Binding{
		{k.Up, k.Down, k.PageUp, k.PageDown, k.Top, k.Bottom},
		{k.Enter, k.Back, k.DetailUp, k.DetailDown},
		{k.Filter, k.Status, k.Exception, k.Rerun},
		{k.Help, k.Quit},
	}
}
