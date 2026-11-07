package ui

import "github.com/charmbracelet/lipgloss"

var (
	colRed   = lipgloss.AdaptiveColor{Light: "#C62828", Dark: "#FF6B6B"}
	colGreen = lipgloss.AdaptiveColor{Light: "#2E7D32", Dark: "#6BCB77"}
	colAmber = lipgloss.AdaptiveColor{Light: "#B26A00", Dark: "#F2B84B"}
	colMuted = lipgloss.AdaptiveColor{Light: "#6B7280", Dark: "#7C8594"}
	colSelBg = lipgloss.AdaptiveColor{Light: "#E5E7EB", Dark: "#2A2F3A"}
	colRule  = lipgloss.AdaptiveColor{Light: "#C9CDD4", Dark: "#3A404C"}
)

// styles holds every lipgloss style the UI uses.
type styles struct {
	Title, Muted, Bold   lipgloss.Style
	Out, Unk, In         lipgloss.Style
	PillOut, PillUnk     lipgloss.Style
	PillIn, PillNeutral  lipgloss.Style
	ColHead, Divider     lipgloss.Style
	Warn, Note, Selected lipgloss.Style
	Empty                lipgloss.Style
	Row, Marker          lipgloss.Style
	Overlay, Prompt      lipgloss.Style
}

func newStyles() styles {
	pill := lipgloss.NewStyle().Bold(true)
	return styles{
		Title:       lipgloss.NewStyle().Bold(true),
		Muted:       lipgloss.NewStyle().Foreground(colMuted),
		Bold:        lipgloss.NewStyle().Bold(true),
		Out:         lipgloss.NewStyle().Foreground(colRed).Bold(true),
		Unk:         lipgloss.NewStyle().Foreground(colAmber),
		In:          lipgloss.NewStyle().Foreground(colGreen),
		PillOut:     pill.Foreground(colRed),
		PillUnk:     pill.Foreground(colAmber),
		PillIn:      pill.Foreground(colGreen),
		PillNeutral: lipgloss.NewStyle().Foreground(colMuted),
		ColHead:     lipgloss.NewStyle().Foreground(colMuted).Bold(true),
		Divider:     lipgloss.NewStyle().Foreground(colRule),
		Warn:        lipgloss.NewStyle().Foreground(colAmber),
		Note:        lipgloss.NewStyle().Foreground(colMuted).Italic(true),
		Selected:    lipgloss.NewStyle().Background(colSelBg),
		Empty:       lipgloss.NewStyle().Foreground(colMuted),
		Row:         lipgloss.NewStyle(),
		Marker:      lipgloss.NewStyle().Bold(true).Background(colSelBg),
		Overlay:     lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(colAmber).Padding(0, 1),
		Prompt:      lipgloss.NewStyle().Foreground(colAmber).Bold(true),
	}
}

// OnSelected adds the selected-row background to a cell style.
func (s styles) OnSelected(c lipgloss.Style) lipgloss.Style { return c.Background(colSelBg) }
