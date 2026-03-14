package app

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"vxmon/internal/store"
	"vxmon/internal/ui"
)

// model.go owns the Bubble Tea update loop, input handling, and viewport state.
type TopMode string

type BottomMode string

const (
	TopBridge   TopMode    = "bridge"
	TopVRF      TopMode    = "vrf"
	BottomFDB   BottomMode = "fdb"
	BottomNeigh BottomMode = "neigh"
	BottomRoute BottomMode = "route"
)

// Model is created in cmd/vxmon/main and then owned by Bubble Tea's single update loop.
type Model struct {
	st *store.Store

	focusTop bool
	topMode  TopMode
	botMode  BottomMode

	bridgeCursor       int
	bridgeDevFilterIdx int
	vrfCursor          int
	vrfDevFilterIdx    int
	botCursor          int
	botViewport        int

	topViewport int

	width  int
	height int

	clock     time.Time
	fadeClock time.Time

	topRows          []ui.ListRow
	topCursorRowIdx  int
	bottomHeaderStr  string
	bottomRows       []ui.ListRow
	bottomCursorIdx  int
	bottomTotalLines int
	detailed         bool
	showHelp         bool
	helpDirty        bool
}

// NewModel is called from cmd/vxmon/main to initialize UI state and the first snapshot.
func NewModel(st *store.Store) Model {
	m := Model{
		st:                 st,
		focusTop:           true,
		topMode:            TopBridge,
		botMode:            BottomFDB,
		clock:              time.Now(),
		fadeClock:          time.Now(),
		bridgeDevFilterIdx: -1,
		vrfDevFilterIdx:    -1,
	}
	_ = st.ReloadAll(time.Now())
	m.refreshView()
	return m
}

// Init is called by Bubble Tea once to register periodic timers.
func (m Model) Init() tea.Cmd {
	return tea.Batch(clockTickCmd(), animTickCmd())
}

// clockTickCmd is called from Init and Update to reschedule header clock updates.
func clockTickCmd() tea.Cmd {
	return tea.Tick(clockTickInterval, func(t time.Time) tea.Msg { return clockTickMsg(t) })
}

// animTickCmd is called from Init and Update to reschedule fade animation ticks.
func animTickCmd() tea.Cmd {
	return tea.Tick(animTickInterval, func(t time.Time) tea.Msg { return animTickMsg(t) })
}

// layout is called from Update, refreshView, and View to split pane heights.
func (m Model) layout() (visibleTop int, visibleBottom int) {
	paneHeight := m.height - 1
	half := paneHeight / 2
	visibleTop = half - 2
	visibleBottom = paneHeight - half - 3
	if visibleTop < 0 {
		visibleTop = 0
	}
	if visibleBottom < 0 {
		visibleBottom = 0
	}
	return
}

// Update is called by Bubble Tea for all events and drives store reloads and cursor state.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch x := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = x.Width
		m.height = x.Height
		if m.showHelp {
			m.helpDirty = true
		} else {
			m.refreshView()
		}
		return m, nil

	case animTickMsg:
		now := time.Time(x)
		m.fadeClock = now
		if m.st.Advance(now) {
			if m.showHelp {
				m.helpDirty = true
			} else {
				m.refreshView()
			}
		}
		return m, animTickCmd()

	case clockTickMsg:
		m.clock = time.Time(x)
		if m.showHelp {
			m.helpDirty = true
		} else {
			m.refreshView()
		}
		return m, clockTickCmd()

	case store.NeighNLMsg:
		_ = m.st.ReloadNeighAndFDB(x.At)
		_ = m.st.ReloadInterfaces()
		if m.showHelp {
			m.helpDirty = true
		} else {
			m.refreshView()
		}
		return m, nil

	case store.RouteNLMsg:
		_ = m.st.ReloadRoutes(x.At)
		_ = m.st.ReloadInterfaces()
		if m.showHelp {
			m.helpDirty = true
		} else {
			m.refreshView()
		}
		return m, nil

	case store.LinkNLMsg:
		_ = m.st.ReloadInterfaces()
		if m.showHelp {
			m.helpDirty = true
		} else {
			m.refreshView()
		}
		return m, nil

	case tea.MouseMsg:
		if m.showHelp {
			return m, nil
		}

		switch x.Action {
		case tea.MouseActionPress:
			switch x.Button {
			case tea.MouseButtonWheelDown:
				return m.Update(tea.KeyMsg{Type: tea.KeyDown, Runes: []rune("down")})
			case tea.MouseButtonWheelUp:
				return m.Update(tea.KeyMsg{Type: tea.KeyUp, Runes: []rune("up")})
			}
		case tea.MouseActionRelease:
			switch x.Button {
			case tea.MouseButtonLeft:
				prevFocusTop := m.focusTop
				if x.Y < m.height/2 {
					m.focusTop = true
				} else {
					m.focusTop = false
				}
				if m.focusTop != prevFocusTop {
					m.refreshView()
				}
			}
		}
	case tea.KeyMsg:
		visibleTop, visibleBottom := m.layout()
		if m.showHelp {
			m.showHelp = false
			if m.helpDirty {
				m.refreshView()
				m.helpDirty = false
			}
			return m, nil
		}
		switch x.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		case "h", "?":
			m.showHelp = true
			m.helpDirty = false
			return m, nil
		case "tab":
			m.focusTop = !m.focusTop
		case "d":
			m.detailed = !m.detailed

		case "left", "right":
			if m.focusTop {
				if m.topMode == TopBridge {
					m.topMode = TopVRF
					m.botMode = BottomRoute
				} else {
					m.topMode = TopBridge
					m.botMode = BottomFDB
				}
				m.bridgeCursor, m.vrfCursor = 0, 0
				m.bridgeDevFilterIdx = -1
				m.vrfDevFilterIdx = -1
				m.topViewport, m.botCursor, m.botViewport = 0, 0, 0

			} else {
				if m.topMode == TopVRF {
					if m.botMode == BottomNeigh {
						m.botMode = BottomRoute
					} else {
						m.botMode = BottomNeigh
					}
					m.botCursor, m.botViewport = 0, 0
				}
			}
		case ".", ",":
			delta := 1
			if x.String() == "," {
				delta = -1
			}

			switch m.topMode {
			case TopVRF:
				m.moveVrfDevFilter(delta)
				m.botCursor, m.botViewport = 0, 0
			case TopBridge:
				m.moveBridgeDevFilter(delta)
			}
		case "down", "up":
			delta := 1
			if x.String() == "up" {
				delta = -1
			}
			if m.focusTop {
				m.moveTopCursor(delta)
			} else {
				m.botCursor = clamp(m.botCursor+delta, 0, len(m.bottomRows)-1)

				if m.botCursor < m.botViewport {
					m.botViewport = m.botCursor
				}
			}
		}

		m.botCursor = clamp(m.botCursor, 0, len(m.bottomRows)-1)

		if m.botCursor < m.botViewport {
			m.botViewport = m.botCursor
		} else if m.botCursor >= m.botViewport+visibleBottom {
			m.botViewport = max(0, m.botCursor-visibleBottom+1)
		}

		m.refreshViewWithTopViewport(visibleTop)
		return m, nil
	}

	return m, nil
}

// moveTopCursor is called from Update when Up/Down is pressed in the top pane.
func (m *Model) moveTopCursor(delta int) {
	if m.topMode == TopBridge {
		bridges := m.bridgeItems()
		m.bridgeCursor = clamp(m.bridgeCursor+delta, 0, len(bridges)-1)
		if delta != 0 {
			m.bridgeDevFilterIdx = -1
		}
		return
	}
	vrfs := m.vrfItems()
	m.vrfCursor = clamp(m.vrfCursor+delta, 0, len(vrfs)-1)
	if delta != 0 {
		m.vrfDevFilterIdx = -1
	}
}

// refreshView is called from Update when full top viewport recalculation is needed.
func (m *Model) refreshView() {
	visibleTop, _ := m.layout()
	m.refreshViewWithTopViewport(visibleTop)
}

// refreshViewWithTopViewport is called from refreshView and Update after key navigation.
func (m *Model) refreshViewWithTopViewport(visibleTop int) {
	m.topRows, m.topCursorRowIdx = m.buildTopRows()
	m.topCursorRowIdx = clamp(m.topCursorRowIdx, 0, len(m.topRows)-1)
	if visibleTop > 0 {
		if filterRow, ok := m.selectedTopSubFilterRenderedRow(); ok {
			minStart := max(0, m.topCursorRowIdx-visibleTop+1)
			maxStart := m.topCursorRowIdx
			if filterRow-visibleTop+1 > minStart {
				minStart = filterRow - visibleTop + 1
			}
			if filterRow < maxStart {
				maxStart = filterRow
			}
			if minStart <= maxStart {
				if m.topViewport < minStart {
					m.topViewport = minStart
				}
				if m.topViewport > maxStart {
					m.topViewport = maxStart
				}
			} else {
				m.topViewport = clamp(m.topViewport, max(0, m.topCursorRowIdx-visibleTop+1), m.topCursorRowIdx)
			}
			if filterRow == m.topViewport+visibleTop-1 && filterRow < len(m.topRows)-1 && m.topViewport < maxStart {
				m.topViewport++
			}
		}
	}
	if m.topViewport < 0 {
		m.topViewport = 0
	}

	m.bottomHeaderStr, m.bottomRows, m.bottomCursorIdx = m.buildBottom()
	m.botCursor = clamp(m.botCursor, 0, len(m.bottomRows)-1)
}

// View is called by Bubble Tea to render the complete screen each frame.
func (m Model) View() string {
	if m.height <= 15 {
		warn := lipgloss.NewStyle().Foreground(ui.ColorWarn).Bold(true)
		msg := warn.Render("Terminal height is too small (Minimum: 16)")
		return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, msg)
	}

	timeStr := m.clock.Format("2006-01-02 15:04:05")
	leftTitle := appTitle
	rightStr := timeStr + "   "
	spaces := m.width - lipgloss.Width(leftTitle) - lipgloss.Width(rightStr)
	var headerView string
	if spaces > 0 {
		headerView = leftTitle + strings.Repeat(" ", spaces) + rightStr
	} else {
		headerView = leftTitle + "\n" + rightStr
	}

	paneHeight := m.height - 1
	half := paneHeight / 2
	visibleTop, visibleBottom := m.layout()

	topTitle := fmt.Sprintf("  %s  ", strings.ToUpper(string(m.topMode)))
	topContent := ui.RenderList(m.topRows, m.topCursorRowIdx, m.topViewport, visibleTop)
	topPane := ui.RenderPane(topTitle, topContent, m.width, half, m.focusTop)

	botTitle := fmt.Sprintf("  %s  ", strings.ToUpper(string(m.botMode)))
	botContent := m.bottomHeaderStr
	if botContent != "" {
		botContent = "  " + botContent
	}
	if len(m.bottomRows) > 0 {
		botLines := ui.RenderList(m.bottomRows, m.botCursor, m.botViewport, visibleBottom)
		if botContent != "" {
			botContent = botContent + "\n" + botLines
		} else {
			botContent = botLines
		}
	}
	botPane := ui.RenderPane(botTitle, botContent, m.width, paneHeight-half, !m.focusTop)

	base := headerView + "\n" + lipgloss.JoinVertical(lipgloss.Left, topPane, botPane)
	if m.showHelp {
		return m.withHelpOverlay()
	}
	return base

}

// clamp is a local helper called by cursor and viewport calculations in app/model and app/top_view.
func clamp(x, lo, hi int) int {
	if hi < lo {
		return lo
	}
	if x < lo {
		return lo
	}
	if x > hi {
		return hi
	}
	return x
}

// moveVrfDevFilter is called from Update when '.' or ',' is pressed in VRF mode.
func (m *Model) moveVrfDevFilter(delta int) {
	displayDevs := m.vrfDisplayDevs(m.selectedVRF())
	if len(displayDevs) == 0 {
		m.vrfDevFilterIdx = -1
		return
	}

	idx := m.vrfDevFilterIdx + delta

	if delta > 0 && idx >= len(displayDevs) {
		idx = -1
	} else if delta < 0 && idx < -1 {
		idx = -1
	}

	m.vrfDevFilterIdx = idx
}

// moveBridgeDevFilter is called from Update when '.' or ',' is pressed in bridge mode.
func (m *Model) moveBridgeDevFilter(delta int) {
	items := m.bridgeItems()
	if len(items) == 0 {
		m.bridgeDevFilterIdx = -1
		return
	}

	bridge := items[clamp(m.bridgeCursor, 0, len(items)-1)]
	if len(bridge.Devs) == 0 {
		m.bridgeDevFilterIdx = -1
		return
	}

	idx := m.bridgeDevFilterIdx + delta

	if delta > 0 && idx >= len(bridge.Devs) {
		idx = -1
	} else if delta < 0 && idx < -1 {
		idx = -1
	}

	m.bridgeDevFilterIdx = idx
}

// selectedVrfIfFilter is called from top_view.go and bottom_view.go to apply IF filtering.
func (m Model) selectedVrfIfFilter() (ifName string, ok bool) {
	if m.topMode != TopVRF {
		return "", false
	}
	if m.vrfDevFilterIdx < 0 {
		return "", false
	}
	vrf := m.selectedVRF()
	displayDevs := m.vrfDisplayDevs(vrf)
	if m.vrfDevFilterIdx >= len(displayDevs) {
		return "", false
	}
	return displayDevs[m.vrfDevFilterIdx].IfName, true
}

// selectedVrfFilterRenderedRow is called from refreshViewWithTopViewport to keep filter rows visible.
func (m Model) selectedVrfFilterRenderedRow() (int, bool) {
	if m.topMode != TopVRF || m.vrfDevFilterIdx < 0 {
		return 0, false
	}
	vrf := m.selectedVRF()
	displayDevs := m.vrfDisplayDevs(vrf)
	if m.vrfDevFilterIdx >= len(displayDevs) {
		return 0, false
	}
	return m.topCursorRowIdx + 1 + m.vrfDevFilterIdx, true
}

// selectedBridgeFilterRenderedRow is called from refreshViewWithTopViewport to keep filter rows visible.
func (m Model) selectedBridgeFilterRenderedRow() (int, bool) {
	if m.topMode != TopBridge || m.bridgeDevFilterIdx < 0 {
		return 0, false
	}
	items := m.bridgeItems()
	if len(items) == 0 {
		return 0, false
	}
	bridge := items[clamp(m.bridgeCursor, 0, len(items)-1)]
	if m.bridgeDevFilterIdx >= len(bridge.Devs) {
		return 0, false
	}
	return m.topCursorRowIdx + 1 + m.bridgeDevFilterIdx, true
}

// selectedTopSubFilterRenderedRow is called from refreshViewWithTopViewport for mode-specific row lookup.
func (m Model) selectedTopSubFilterRenderedRow() (int, bool) {
	if m.topMode == TopVRF {
		return m.selectedVrfFilterRenderedRow()
	}
	if m.topMode == TopBridge {
		return m.selectedBridgeFilterRenderedRow()
	}
	return 0, false
}
