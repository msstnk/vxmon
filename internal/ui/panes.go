package ui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// panes.go provides generic list and bordered pane rendering primitives.
type ListRow struct {
	Text  string
	Style lipgloss.Style
}

// RenderList is called from app.Model.View to render a viewport window with cursor prefix.
func RenderList(rows []ListRow, cursorRenderedIndex int, viewport int, visibleHeight int) string {
	start := viewport
	end := start + visibleHeight
	if end > len(rows) {
		end = len(rows)
	}
	var out []string
	for i := start; i < end; i++ {
		prefix := "  "
		if i == cursorRenderedIndex {
			prefix = "> "
		}
		line := prefix + rows[i].Text
		out = append(out, rows[i].Style.Render(line))
	}
	return strings.Join(out, "\n")
}

// RenderPane is called from app.Model.View to frame top and bottom pane content.
func RenderPane(title string, content string, width int, height int, active bool) string {
	borderColor := ColorPaneBorderInactive
	if active {
		borderColor = ColorPaneBorderActive
	}
	b := lipgloss.NormalBorder()
	padLen := width - 2 - lipgloss.Width(title) - 2
	if padLen < 0 {
		padLen = 0
	}
	topLine := b.TopLeft + b.Top + title + b.Top + strings.Repeat(b.Top, padLen) + b.TopRight
	topLine = lipgloss.NewStyle().Foreground(borderColor).Render(topLine)

	boxStyle := lipgloss.NewStyle().
		Border(b, false, true, true, true).
		BorderForeground(borderColor).
		Width(width - 2).
		Height(height - 2)

	return topLine + "\n" + boxStyle.Render(content)
}
