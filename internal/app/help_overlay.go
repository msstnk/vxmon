package app

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"vxmon/internal/ui"
)

// help_overlay.go renders the centered help modal shown on demand.
// withHelpOverlay is called from Model.View when help mode is active.
func (m Model) withHelpOverlay() string {
	lines := []string{
		"KEYBINDINGS",
		"",
		"Global:",
		"  q / Ctrl+C : Quit",
		"  Tab        : Switch focus (Top/Bottom)",
		"  Left/Right : Switch view/mode",
		"  Up/Down    : Move next/previous (VRF, Bridge, Route, etc)",
		"  d          : Toggle detailed view (show multicast, etc)",
		"  h / ?      : Toggle this help (any key to close)",
		"",
		"VRF View (Top):",
		"  . ,        : Select next/previous interface to filter",
		"Bridge View (Top):",
		"  . ,        : Select next/previous bridge port",
		"",
		"Route View legend:",
		"  B : BGP (proto 11/17/186)",
		"  S : Static",
		"  L : Kernel",
		"  C : Connected/Local",
		"  m : Multicast",
		"  b : Broadcast",
		"  = : ECMP",
	}
	content := strings.Join(lines, "\n")

	boxStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(ui.ColorHelpBorder).
		Foreground(ui.ColorHelpForeground).
		Background(ui.ColorHelpBackground).
		Padding(1, 2)

	box := boxStyle.Render(content)
	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, box)
}
