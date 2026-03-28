package app

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/msstnk/vxmon/internal/constants"
	"github.com/msstnk/vxmon/internal/debuglog"
	"github.com/msstnk/vxmon/internal/store"
	"github.com/msstnk/vxmon/internal/ui"
)

type TopMode string

type BottomMode string

const (
	TopBridge TopMode = "bridge"
	TopVRF    TopMode = "vrf"
	TopNETNS  TopMode = "netns"

	BottomFDB     BottomMode = "fdb"
	BottomNeigh   BottomMode = "neigh"
	BottomRoute   BottomMode = "route"
	BottomProcess BottomMode = "process"
	BottomLink    BottomMode = "link"
)

var topModeCycle = []TopMode{TopBridge, TopVRF, TopNETNS}

type Model struct {
	st                 *store.Store
	requestFetchLatest func(time.Time)

	focusTop bool
	topMode  TopMode
	botMode  BottomMode

	bridgeCursor       int
	bridgeDevFilterIdx int
	vrfCursor          int
	vrfDevFilterIdx    int
	netnsCursor        int
	botCursor          int
	botViewport        int

	topViewport int
	topVisible  int

	topPanePercent int

	width  int
	height int

	clock     time.Time
	fadeClock time.Time

	fadeTickActive bool

	topRows          []ui.ListRow
	topCursorRowIdx  int
	bottomHeaderStr  string
	bottomRows       []ui.ListRow
	detailed         bool
	showHelp         bool
	helpDirty        bool
	savedBottomModes map[TopMode]BottomMode

	headerCache     string
	topPaneCache    string
	bottomPaneCache string
	baseCache       string

	topParentMeta  map[string]store.Meta
	topParentReady bool
}

func NewModel(st *store.Store, requestFetchLatest func(time.Time)) Model {
	now := time.Now()
	m := Model{
		st:                 st,
		requestFetchLatest: requestFetchLatest,
		focusTop:           true,
		topMode:            TopVRF,
		botMode:            BottomRoute,
		clock:              now,
		fadeClock:          now,
		bridgeDevFilterIdx: -1,
		vrfDevFilterIdx:    -1,
		topPanePercent:     constants.DefaultTopPanePercent,
		savedBottomModes:   make(map[TopMode]BottomMode, len(topModeCycle)),
		topParentMeta:      make(map[string]store.Meta),
	}
	m.refreshAll()
	m.fadeTickActive = st.HasActiveFades() || m.hasActiveTopParentFades()
	return m
}

func (m *Model) Init() tea.Cmd {
	cmds := []tea.Cmd{clockTickCmd()}
	cmds = append(cmds, m.requestFetchLatestCmd(time.Now()))
	if m.fadeTickActive {
		cmds = append(cmds, animTickCmd())
	}
	return batchCmds(cmds...)
}

func clockTickCmd() tea.Cmd {
	return tea.Tick(constants.ClockTickInterval, func(t time.Time) tea.Msg { return clockTickMsg(t) })
}

func animTickCmd() tea.Cmd {
	return tea.Tick(constants.AnimTickInterval, func(t time.Time) tea.Msg { return animTickMsg(t) })
}

func batchCmds(cmds ...tea.Cmd) tea.Cmd {
	filtered := make([]tea.Cmd, 0, len(cmds))
	for _, cmd := range cmds {
		if cmd != nil {
			filtered = append(filtered, cmd)
		}
	}
	switch len(filtered) {
	case 0:
		return nil
	case 1:
		return filtered[0]
	default:
		return tea.Batch(filtered...)
	}
}

func (m *Model) requestFetchLatestCmd(at time.Time) tea.Cmd {
	if m.requestFetchLatest == nil {
		return nil
	}
	return func() tea.Msg {
		m.requestFetchLatest(at)
		return nil
	}
}

func (m *Model) paneHeights() (topHeight int, bottomHeight int) {
	if m.height <= 1 {
		return 0, 0
	}
	paneHeight := m.height - 1
	topHeight = paneHeight * m.topPanePercent / 100
	if paneHeight > 1 {
		topHeight = clamp(topHeight, 1, paneHeight-1)
	} else {
		topHeight = paneHeight
	}
	bottomHeight = paneHeight - topHeight
	return topHeight, bottomHeight
}

func (m *Model) layout() (visibleTop int, visibleBottom int) {
	topHeight, bottomHeight := m.paneHeights()
	visibleTop = topHeight - 2
	visibleBottom = bottomHeight - 3
	if visibleTop < 0 {
		visibleTop = 0
	}
	if visibleBottom < 0 {
		visibleBottom = 0
	}
	return
}

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	debuglog.Tracef("app.Model.Update msg=%T", msg)
	switch x := msg.(type) {

	case animTickMsg:
		now := time.Time(x)
		m.fadeClock = now
		topChanged, topActive := m.advanceTopParentFades(now)
		storeActive := m.st.HasActiveFades()
		if storeActive || topChanged || topActive {
			if m.showHelp {
				m.helpDirty = true
			} else {
				if topChanged || topActive {
					m.refreshTopAndBottom()
				} else {
					m.rerenderBottomOnly()
				}
			}
		}
		if storeActive || topActive {
			m.fadeTickActive = true
			return m, animTickCmd()
		}
		m.fadeTickActive = false
		return m, nil

	case clockTickMsg:
		now := time.Time(x)
		m.clock = now
		var cmd tea.Cmd
		if m.st.RuntimeRefreshDue(now) {
			cmd = m.requestFetchLatestCmd(now)
		}
		if m.showHelp {
			m.helpDirty = true
		} else {
			m.refreshHeaderOnly()
		}
		return m, batchCmds(clockTickCmd(), cmd)

	case store.InventoryUpdatedMsg:
		if m.showHelp {
			m.helpDirty = true
		} else {
			m.refreshTopAndBottom()
		}
		return m, m.maybeStartFadeTick()
	case store.InventoryPeriodicUpdatedMsg:
		if m.showHelp {
			m.helpDirty = true
		} else if m.topMode == TopNETNS {
			m.refreshTopAndBottom()
		}
		return m, m.maybeStartFadeTick()
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
				topHeight, _ := m.paneHeights()
				if x.Y < 1+topHeight {
					m.focusTop = true
				} else {
					m.focusTop = false
				}
				if m.focusTop != prevFocusTop {
					m.rerenderPanes()
				}
			}
		}

	case tea.KeyMsg:
		visibleTop, visibleBottom := m.layout()
		if m.showHelp {
			m.showHelp = false
			if m.helpDirty {
				m.refreshAll()
				m.helpDirty = false
			}
			return m, nil
		}

		topDataChanged := false
		bottomDataChanged := false
		bottomRenderChanged := false
		paneRenderChanged := false

		switch x.String() {
		case "q", "Q", "ctrl+c":
			return m, tea.Quit
		case "h", "H", "?":
			m.showHelp = true
			m.helpDirty = false
			return m, nil
		case "tab":
			m.focusTop = !m.focusTop
			paneRenderChanged = true
		case "d", "D":
			m.detailed = !m.detailed
			topDataChanged = true
		case "t", "T":
			m.cycleTopPanePercent()
			topDataChanged = true
		case "left":
			if m.focusTop {
				m.rememberBottomMode()
				m.rotateTopMode(-1)
				topDataChanged = true
			} else {
				m.rotateBottomMode(-1)
				bottomDataChanged = true
			}
		case "right":
			if m.focusTop {
				m.rememberBottomMode()
				m.rotateTopMode(1)
				topDataChanged = true
			} else {
				m.rotateBottomMode(1)
				bottomDataChanged = true
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
				topDataChanged = true
			case TopBridge:
				m.moveBridgeDevFilter(delta)
				m.botCursor, m.botViewport = 0, 0
				topDataChanged = true
			}
		case "down", "up":
			delta := 1
			if x.String() == "up" {
				delta = -1
			}
			if m.focusTop {
				m.moveTopCursor(delta)
				topDataChanged = true
			} else {
				m.botCursor += delta
				bottomRenderChanged = true
			}
		case "pgdown", "pgup":
			delta := 1
			if x.String() == "pgup" {
				delta = -1
			}
			if m.clearTopFilter() {
				topDataChanged = true
			}
			if m.focusTop {
				m.moveTopPage(delta, visibleTop)
				topDataChanged = true
			} else {
				m.botCursor += delta * visibleBottom
				if !topDataChanged {
					bottomRenderChanged = true
				}
			}
		case "home", "end":
			last := x.String() == "end"
			if m.clearTopFilter() {
				topDataChanged = true
			}
			if m.focusTop {
				m.moveTopBoundary(last)
				topDataChanged = true
			} else {
				m.moveBottomBoundary(last, visibleBottom)
				if !topDataChanged {
					bottomRenderChanged = true
				}
			}
		}

		m.botCursor = clamp(m.botCursor, 0, len(m.bottomRows)-1)
		if m.botCursor < m.botViewport {
			m.botViewport = m.botCursor
		} else if m.botCursor >= m.botViewport+visibleBottom {
			m.botViewport = max(0, m.botCursor-visibleBottom+1)
		}

		m.rememberBottomMode()
		switch {
		case topDataChanged:
			m.refreshTopAndBottom()
		case bottomDataChanged:
			m.refreshBottomOnly()
		case bottomRenderChanged:
			m.rerenderBottomOnly()
		case paneRenderChanged:
			m.rerenderPanes()
		default:
			if visibleTop != m.topVisible {
				m.refreshTopAndBottom()
			}
		}
		return m, nil

	case tea.WindowSizeMsg:
		m.width = x.Width
		m.height = x.Height
		if m.showHelp {
			m.helpDirty = true
		} else {
			m.refreshAll()
		}
		return m, nil
	}

	return m, nil
}

func (m *Model) rotateTopMode(delta int) {
	idx := 0
	for i, mode := range topModeCycle {
		if mode == m.topMode {
			idx = i
			break
		}
	}
	idx = (idx + delta + len(topModeCycle)) % len(topModeCycle)
	m.topMode = topModeCycle[idx]
	m.botMode = m.bottomModeForTop(m.topMode)
	m.bridgeDevFilterIdx = -1
	m.vrfDevFilterIdx = -1
	m.botCursor, m.botViewport = 0, 0
	m.topViewport = 0
}

func (m *Model) cycleTopPanePercent() {
	next := m.topPanePercent + constants.TopPanePercentStep
	if next > constants.MaxTopPanePercent {
		next = constants.MinTopPanePercent
	}
	m.topPanePercent = next
}

func defaultBottomMode(mode TopMode) BottomMode {
	switch mode {
	case TopBridge:
		return BottomFDB
	case TopNETNS:
		return BottomLink
	default:
		return BottomRoute
	}
}

func (m *Model) rotateBottomMode(delta int) {
	options := bottomModesForTop(m.topMode)
	if len(options) <= 1 {
		return
	}
	idx := 0
	for i, mode := range options {
		if mode == m.botMode {
			idx = i
			break
		}
	}
	idx = (idx + delta + len(options)) % len(options)
	m.botMode = options[idx]
	m.botCursor, m.botViewport = 0, 0
}

func (m *Model) moveTopCursor(delta int) {
	switch m.topMode {
	case TopBridge:
		bridges := m.bridgeItems()
		m.bridgeCursor = clamp(m.bridgeCursor+delta, 0, len(bridges)-1)
		m.bridgeDevFilterIdx = -1
	case TopNETNS:
		netnsItems := m.st.Namespaces()
		m.netnsCursor = clamp(m.netnsCursor+delta, 0, len(netnsItems)-1)
	default:
		vrfs := m.vrfItems()
		m.vrfCursor = clamp(m.vrfCursor+delta, 0, len(vrfs)-1)
		m.vrfDevFilterIdx = -1
	}
	m.botCursor, m.botViewport = 0, 0
}

func (m *Model) moveTopPage(delta int, visibleTop int) {
	step := visibleTop
	if step < 1 {
		step = 1
	}
	m.moveTopCursor(delta * step)
}

func (m *Model) moveTopBoundary(last bool) {
	switch m.topMode {
	case TopBridge:
		bridges := m.bridgeItems()
		if last {
			m.bridgeCursor = max(0, len(bridges)-1)
		} else {
			m.bridgeCursor = 0
		}
		m.bridgeDevFilterIdx = -1
	case TopNETNS:
		items := m.st.Namespaces()
		if last {
			m.netnsCursor = max(0, len(items)-1)
		} else {
			m.netnsCursor = 0
		}
	default:
		vrfs := m.vrfItems()
		if last {
			m.vrfCursor = max(0, len(vrfs)-1)
		} else {
			m.vrfCursor = 0
		}
		m.vrfDevFilterIdx = -1
	}
	m.botCursor, m.botViewport = 0, 0
}

func (m *Model) moveBottomBoundary(last bool, visibleBottom int) {
	if last {
		m.botCursor = max(0, len(m.bottomRows)-1)
		m.botViewport = max(0, len(m.bottomRows)-visibleBottom)
		return
	}
	m.botCursor = 0
	m.botViewport = 0
}

func (m *Model) clearTopFilter() bool {
	changed := false
	if m.bridgeDevFilterIdx >= 0 {
		m.bridgeDevFilterIdx = -1
		changed = true
	}
	if m.vrfDevFilterIdx >= 0 {
		m.vrfDevFilterIdx = -1
		changed = true
	}
	return changed
}

func (m *Model) refreshAll() {
	m.rebuildHeaderCache()
	m.refreshTopAndBottom()
}

func (m *Model) refreshTopAndBottom() {
	visibleTop, visibleBottom := m.layout()
	data := m.currentTopItems()
	m.syncTopParentMeta(data)
	m.topVisible = visibleTop
	m.topRows, m.topCursorRowIdx = m.buildTopRows(visibleTop, data)
	if visibleTop > 0 {
		m.topViewport = m.adjustTopViewport(visibleTop, data)
	}
	if m.topViewport < 0 {
		m.topViewport = 0
	}
	m.bottomHeaderStr, m.bottomRows = m.buildBottom(data)
	m.botCursor = clamp(m.botCursor, 0, len(m.bottomRows)-1)
	m.renderTopPane(visibleTop)
	m.renderBottomPane(visibleBottom)
	m.rebuildBaseCache()
}

func (m *Model) refreshBottomOnly() {
	_, visibleBottom := m.layout()
	m.bottomHeaderStr, m.bottomRows = m.buildBottom(m.currentTopItems())
	m.botCursor = clamp(m.botCursor, 0, len(m.bottomRows)-1)
	m.renderBottomPane(visibleBottom)
	m.rebuildBaseCache()
}

func (m *Model) rerenderBottomOnly() {
	_, visibleBottom := m.layout()
	m.renderBottomPane(visibleBottom)
	m.rebuildBaseCache()
}

func (m *Model) rerenderPanes() {
	visibleTop, visibleBottom := m.layout()
	m.renderTopPane(visibleTop)
	m.renderBottomPane(visibleBottom)
	m.rebuildBaseCache()
}

func (m *Model) refreshHeaderOnly() {
	m.rebuildHeaderCache()
	m.rebuildBaseCache()
}

func (m *Model) rebuildHeaderCache() {
	timeStr := m.clock.Format("2006-01-02 15:04:05")
	leftTitle := constants.AppTitle
	rightStr := timeStr + "   "
	spaces := m.width - lipgloss.Width(leftTitle) - lipgloss.Width(rightStr)
	if spaces > 0 {
		m.headerCache = leftTitle + strings.Repeat(" ", spaces) + rightStr
		return
	}
	m.headerCache = leftTitle + "\n" + rightStr
}

func (m *Model) renderTopPane(visibleTop int) {
	if m.width <= 1 || m.height <= 1 {
		m.topPaneCache = ""
		return
	}
	topHeight, _ := m.paneHeights()
	topTitle := fmt.Sprintf("  %s  ", strings.ToUpper(string(m.topMode)))
	topContent := ui.RenderList(m.topRows, m.topCursorRowIdx, m.topViewport, visibleTop)
	m.topPaneCache = ui.RenderPane(topTitle, topContent, m.width, topHeight, m.focusTop)
}

func (m *Model) renderBottomPane(visibleBottom int) {
	if m.width <= 1 || m.height <= 1 {
		m.bottomPaneCache = ""
		return
	}
	_, bottomHeight := m.paneHeights()
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
	m.bottomPaneCache = ui.RenderPane(botTitle, botContent, m.width, bottomHeight, !m.focusTop)
}

func (m *Model) rebuildBaseCache() {
	m.baseCache = m.headerCache + "\n" + lipgloss.JoinVertical(lipgloss.Left, m.topPaneCache, m.bottomPaneCache)
}

func (m *Model) maybeStartFadeTick() tea.Cmd {
	if m.fadeTickActive || (!m.st.HasActiveFades() && !m.hasActiveTopParentFades()) {
		return nil
	}
	debuglog.Tracef("app.startFadeTick")
	m.fadeTickActive = true
	return animTickCmd()
}

func (m *Model) adjustTopViewport(visibleTop int, data topItems) int {
	if visibleTop <= 0 || len(m.topRows) == 0 {
		return 0
	}

	viewport := clamp(m.topViewport, 0, max(0, len(m.topRows)-visibleTop))
	if m.topMode == TopBridge || m.topMode == TopVRF {
		parentRow := m.topCursorRowIdx
		totalChildren, shownChildren := m.selectedTopChildCount(data, visibleTop)
		if shownChildren > 0 {
			// When all children cannot fit in one screen, pin parent to the top and clip children.
			if totalChildren > visibleTop-1 {
				return clamp(parentRow, 0, max(0, len(m.topRows)-visibleTop))
			}
			// Keep current viewport if parent + shown children are already fully visible.
			parentPos := parentRow - viewport
			if parentPos >= 0 && parentPos < visibleTop {
				rowsBelowParent := visibleTop - parentPos - 1
				if shownChildren <= rowsBelowParent {
					return viewport
				}
			}
			// Otherwise move parent upward just enough so all shown children are visible.
			maxParentPos := visibleTop - shownChildren - 1
			if maxParentPos < 0 {
				maxParentPos = 0
			}
			viewport = parentRow - maxParentPos
			return clamp(viewport, 0, max(0, len(m.topRows)-visibleTop))
		}
	}

	if _, hasChild := m.selectedTopSubFilterRenderedRow(data); hasChild {
		parentRow := m.topCursorRowIdx
		return clamp(parentRow, 0, max(0, len(m.topRows)-visibleTop))
	}

	selectedRow := m.topCursorRowIdx
	if selectedRow < viewport {
		viewport = selectedRow
	}
	if selectedRow >= viewport+visibleTop {
		viewport = selectedRow - visibleTop + 1
	}
	return clamp(viewport, 0, max(0, len(m.topRows)-visibleTop))
}

func (m *Model) selectedTopChildCount(data topItems, visibleTop int) (total int, shown int) {
	switch m.topMode {
	case TopBridge:
		bridge := pickBridge(data.bridges, m.bridgeCursor)
		if bridge.Info.InterfaceName == "" {
			return 0, 0
		}
		total = len(bridge.Devs)
		start, end := bridgeVisibleChildRange(data.bridges, m.bridgeCursor, m.bridgeDevFilterIdx, visibleTop, m.topMode)
		return total, max(0, end-start)
	case TopVRF:
		vrf := pickVRF(data.vrfs, m.vrfCursor)
		if vrf.Label == "" {
			return 0, 0
		}
		total = len(vrf.Devs)
		start, end := vrfVisibleChildRange(data.vrfs, m.vrfCursor, m.vrfDevFilterIdx, visibleTop, m.topMode)
		return total, max(0, end-start)
	default:
		return 0, 0
	}
}

func (m *Model) View() string {
	if m.height <= 15 {
		warn := lipgloss.NewStyle().Foreground(ui.ColorWarn).Bold(true)
		msg := warn.Render("Terminal height is too small (Minimum: 16)")
		return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, msg)
	}
	if m.showHelp {
		return m.withHelpOverlay()
	}
	return m.baseCache
}

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

func moveFilterIndex(current int, total int, delta int) int {
	if total == 0 {
		return -1
	}
	idx := current + delta
	if delta > 0 && idx >= total {
		return -1
	}
	if delta < 0 && idx < -1 {
		return -1
	}
	return idx
}

func (m *Model) moveVrfDevFilter(delta int) {
	displayDevs := pickVRF(m.vrfItems(), m.vrfCursor).Devs
	m.vrfDevFilterIdx = moveFilterIndex(m.vrfDevFilterIdx, len(displayDevs), delta)
}

func (m *Model) moveBridgeDevFilter(delta int) {
	bridge := pickBridge(m.bridgeItems(), m.bridgeCursor)
	if bridge.Info.InterfaceName == "" {
		m.bridgeDevFilterIdx = -1
		return
	}
	m.bridgeDevFilterIdx = moveFilterIndex(m.bridgeDevFilterIdx, len(bridge.Devs), delta)
}

func (m *Model) selectedVrfFilterRenderedRow(data topItems) (int, bool) {
	if m.topMode != TopVRF || m.vrfDevFilterIdx < 0 {
		return 0, false
	}
	vrf := pickVRF(data.vrfs, m.vrfCursor)
	displayDevs := vrf.Devs
	if m.vrfDevFilterIdx >= len(displayDevs) {
		return 0, false
	}
	childStart, childEnd := vrfVisibleChildRange(data.vrfs, m.vrfCursor, m.vrfDevFilterIdx, m.topVisible, m.topMode)
	if m.vrfDevFilterIdx < childStart || m.vrfDevFilterIdx >= childEnd {
		return 0, false
	}
	return m.topCursorRowIdx + 1 + (m.vrfDevFilterIdx - childStart), true
}

func (m *Model) selectedBridgeFilterRenderedRow(data topItems) (int, bool) {
	if m.topMode != TopBridge || m.bridgeDevFilterIdx < 0 {
		return 0, false
	}
	bridge := pickBridge(data.bridges, m.bridgeCursor)
	if bridge.Info.InterfaceName == "" {
		return 0, false
	}
	if m.bridgeDevFilterIdx >= len(bridge.Devs) {
		return 0, false
	}
	childStart, childEnd := bridgeVisibleChildRange(data.bridges, m.bridgeCursor, m.bridgeDevFilterIdx, m.topVisible, m.topMode)
	if m.bridgeDevFilterIdx < childStart || m.bridgeDevFilterIdx >= childEnd {
		return 0, false
	}
	return m.topCursorRowIdx + 1 + (m.bridgeDevFilterIdx - childStart), true
}

func (m *Model) selectedTopSubFilterRenderedRow(data topItems) (int, bool) {
	if m.topMode == TopVRF {
		return m.selectedVrfFilterRenderedRow(data)
	}
	if m.topMode == TopBridge {
		return m.selectedBridgeFilterRenderedRow(data)
	}
	return 0, false
}

func (m *Model) syncTopParentMeta(data topItems) {
	keys := m.currentTopFadeKeys(data)
	if !m.topParentReady {
		for key := range keys {
			m.topParentMeta[key] = store.Meta{}
		}
		m.topParentReady = true
		return
	}

	for key := range m.topParentMeta {
		if _, ok := keys[key]; !ok {
			delete(m.topParentMeta, key)
		}
	}
	for key := range keys {
		if _, ok := m.topParentMeta[key]; ok {
			continue
		}
		m.topParentMeta[key] = store.Meta{
			State:     store.StateAdded,
			ChangedAt: m.fadeClock,
		}
	}
}

func (m *Model) currentTopFadeKeys(data topItems) map[string]struct{} {
	total := len(data.netns)
	for _, item := range data.bridges {
		total += 1 + len(item.Devs)
	}
	for _, item := range data.vrfs {
		total += 1 + len(item.Devs)
	}
	keys := make(map[string]struct{}, total)
	for _, item := range data.bridges {
		keys[bridgeParentKey(item)] = struct{}{}
		for _, dev := range item.Devs {
			keys[bridgeChildKey(item, dev)] = struct{}{}
		}
	}
	for _, item := range data.vrfs {
		keys[vrfParentKey(item)] = struct{}{}
		for _, dev := range item.Devs {
			keys[vrfChildKey(item, dev)] = struct{}{}
		}
	}
	for _, item := range data.netns {
		keys[netnsParentKey(item)] = struct{}{}
	}
	return keys
}

func (m *Model) advanceTopParentFades(now time.Time) (changed bool, active bool) {
	for key, meta := range m.topParentMeta {
		if meta.State == store.StateNone {
			continue
		}
		if now.Sub(meta.ChangedAt) < constants.FadeDuration {
			active = true
			continue
		}
		meta.State = store.StateNone
		m.topParentMeta[key] = meta
		changed = true
	}
	return changed, active
}

func (m *Model) hasActiveTopParentFades() bool {
	for _, meta := range m.topParentMeta {
		if meta.State != store.StateNone {
			return true
		}
	}
	return false
}
