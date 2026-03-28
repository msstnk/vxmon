package app

import (
	"fmt"
	"strings"

	"github.com/msstnk/vxmon/internal/constants"
	"github.com/msstnk/vxmon/internal/ui"
	"golang.org/x/sys/unix"

	"github.com/charmbracelet/lipgloss"
)

func (m *Model) withHelpOverlay() string {
	lines := []string{
		"KEYBINDINGS",
		"",
		"Global:",
		"  q / Ctrl+C : Quit",
		"  Tab        : Switch focus (Top/Bottom)",
		"  Left/Right : Switch view/mode",
		"  Up/Down    : Move next/previous (VRF, Bridge, Route, etc)",
		"  PgDn/PgUp  : Move by one visible page",
		"  Home/End   : Move to first/last item",
		fmt.Sprintf("  t          : Change top pane height (%d-%d%%)", constants.MinTopPanePercent, constants.MaxTopPanePercent),
		"  d          : Toggle detailed view (show multicast, etc)",
		"  h / ?      : Toggle this help (any key to close)",
		"  . / ,      : Move to next/previous child item",
		"",
		"Route View legend:",
		fmt.Sprintf("  B : BGP (proto %d / %d (BIRD) /%d (Zebra))", unix.RTPROT_BGP, unix.RTPROT_BIRD, unix.RTPROT_ZEBRA),
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
