package app

import (
	"fmt"
	"sort"
	"time"

	"github.com/charmbracelet/lipgloss"
	"golang.org/x/sys/unix"

	"vxmon/internal/types"
	"vxmon/internal/ui"
)

// top_view.go builds top-pane rows and selection context for Bridge/VRF modes.
type vrfItem struct {
	Name    string
	TableID uint32
	Devs    []types.InterfaceInfo
}

type bridgeItem struct {
	Info types.InterfaceInfo
	Devs []types.InterfaceInfo
}

// bridgeItems is called from top row builders and bridge-selection helpers.
func (m Model) bridgeItems() []bridgeItem {
	ifaces := m.st.Interfaces()
	bridgeNames := map[string]bool{}
	bridgeInfo := map[string]types.InterfaceInfo{}
	bound := map[string][]types.InterfaceInfo{}

	for _, it := range ifaces {
		if it.IfType == "bridge" {
			bridgeNames[it.IfName] = true
			bridgeInfo[it.IfName] = it
		}
	}
	for _, it := range ifaces {
		if it.MasterName == "" {
			continue
		}
		if bridgeNames[it.MasterName] {
			bound[it.MasterName] = append(bound[it.MasterName], it)
		}
	}

	var names []string
	for n := range bridgeNames {
		names = append(names, n)
	}
	sort.Strings(names)

	var items []bridgeItem
	for _, n := range names {
		devs := bound[n]
		sort.Slice(devs, func(i, j int) bool { return devs[i].IfName < devs[j].IfName })
		items = append(items, bridgeItem{
			Info: bridgeInfo[n],
			Devs: devs,
		})
	}
	return items
}

// vrfItems is called from top row builders and VRF-selection helpers.
func (m Model) vrfItems() []vrfItem {
	ifaces := m.st.Interfaces()

	vrfBound := map[string][]types.InterfaceInfo{}
	vrfNames := map[string]uint32{}

	for _, it := range ifaces {
		if it.IfType == "vrf" {
			vrfNames[it.IfName] = it.TableID
		}
	}
	for _, it := range ifaces {
		if it.MasterName != "" {
			if _, ok := vrfNames[it.MasterName]; ok {
				vrfBound[it.MasterName] = append(vrfBound[it.MasterName], it)
			}
		}
	}

	vrfMasterSet := map[string]bool{}
	for name := range vrfNames {
		vrfMasterSet[name] = true
	}
	var global []types.InterfaceInfo
	for _, it := range ifaces {
		if it.IfName == "lo" {
			continue
		}
		if it.IfType == "vrf" {
			continue
		}
		if it.MasterName == "" || !vrfMasterSet[it.MasterName] {
			global = append(global, it)
		}
	}
	sort.Slice(global, func(i, j int) bool { return global[i].IfName < global[j].IfName })

	items := []vrfItem{{Name: defaultVRFName, TableID: defaultVRFTableID, Devs: global}}
	var names []string
	for name := range vrfNames {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		devs := vrfBound[name]
		sort.Slice(devs, func(i, j int) bool { return devs[i].IfName < devs[j].IfName })
		items = append(items, vrfItem{Name: name, TableID: vrfNames[name], Devs: devs})
	}
	return items
}

// buildTopRows is called from refreshViewWithTopViewport to render the top pane list.
func (m Model) buildTopRows() (rows []ui.ListRow, cursorRenderedIndex int) {
	base := lipgloss.NewStyle()
	child := lipgloss.NewStyle().Foreground(ui.ColorTopChild)
	childSelected := child.Foreground(ui.ColorTopChildSelected)
	childDim := child.Foreground(ui.ColorTopChildDimmed)

	if m.topMode == TopBridge {
		bridges := m.bridgeItems()
		cur := clamp(m.bridgeCursor, 0, len(bridges)-1)
		filterIf, filterOn := m.selectedBridgeIfFilter()
		type displayRow struct {
			cols  []string
			style lipgloss.Style
		}
		displayRows := make([]displayRow, 0, len(bridges))
		cursorRenderedIndex = cur
		for i, b := range bridges {
			displayRows = append(displayRows, displayRow{
				cols: []string{
					b.Info.IfName,
					b.Info.Status,
					b.Info.STPState,
					"",
					b.Info.HWAddr,
				},
				style: base,
			})
			if i != cur {
				continue
			}
			cursorRenderedIndex = len(displayRows) - 1
			for _, d := range b.Devs {
				vni := "-"
				if d.VxlanId > 0 {
					vni = fmt.Sprintf("%d", d.VxlanId)
				}
				st := child
				if filterOn {
					if d.IfName == filterIf {
						st = childSelected
					} else {
						st = childDim
					}
				}
				displayRows = append(displayRows, displayRow{
					cols: []string{
						"  " + d.IfName,
						d.Status,
						d.BridgePortState,
						vni,
						d.HWAddr,
					},
					style: st,
				})
			}
		}
		tableRows := make([][]string, 0, len(displayRows))
		for _, row := range displayRows {
			tableRows = append(tableRows, row.cols)
		}
		lines := ui.FormatRows(tableRows, m.width-6)
		for i, line := range lines {
			rows = append(rows, ui.ListRow{Text: line, Style: displayRows[i].style})
		}
		return
	} else {
		vrfs := m.vrfItems()
		cur := clamp(m.vrfCursor, 0, len(vrfs)-1)
		filterIf, filterOn := m.selectedVrfIfFilter()
		idx := 0
		parentRows := make([][]string, 0, len(vrfs))
		parentStyles := make([]lipgloss.Style, 0, len(vrfs))
		var childRows [][]string
		var childStyles []lipgloss.Style
		for i, vrf := range vrfs {
			displayDevs := m.vrfDisplayDevs(vrf)
			cnt := len(displayDevs)
			countText := fmt.Sprintf("(L3 devs: %d)", cnt)
			if i == cur && filterOn {
				countText += " (filtered)"
			}
			parentRows = append(parentRows, []string{vrf.Name, countText})
			parentStyles = append(parentStyles, base)

			if i == cur {
				cursorRenderedIndex = idx
				for _, d := range displayDevs {
					st := child
					if filterOn {
						if d.IfName == filterIf {
							st = childSelected
						} else {
							st = childDim
						}
					}
					childRows = append(childRows, []string{
						"  " + d.IfName,
						d.Status,
						d.IfType,
						d.HWAddr,
					})
					childStyles = append(childStyles, st)
				}
			}
			idx = len(parentRows)
			if i == cur {
				idx += len(childRows)
			}
		}

		parentLines := ui.FormatRows(parentRows, m.width-6)
		childLines := ui.FormatRows(childRows, m.width-6)

		childLineIdx := 0
		for i := range parentLines {
			rows = append(rows, ui.ListRow{Text: parentLines[i], Style: parentStyles[i]})
			if i != cur {
				continue
			}
			for ; childLineIdx < len(childLines); childLineIdx++ {
				rows = append(rows, ui.ListRow{Text: childLines[childLineIdx], Style: childStyles[childLineIdx]})
			}
		}
		if len(vrfs) == 0 {
			cursorRenderedIndex = 0
		}
		return
	}
}

// selectedBridge is called from bottom_view.go to determine the active bridge context.
func (m Model) selectedBridge() string {
	bridges := m.bridgeItems()
	if len(bridges) == 0 {
		return ""
	}
	idx := clamp(m.bridgeCursor, 0, len(bridges)-1)
	return bridges[idx].Info.IfName
}

// selectedVRF is called from model and bottom/top builders to resolve current VRF context.
func (m Model) selectedVRF() vrfItem {
	vrfs := m.vrfItems()
	if len(vrfs) == 0 {
		return vrfItem{}
	}
	idx := clamp(m.vrfCursor, 0, len(vrfs)-1)
	return vrfs[idx]
}

// selectedBridgeIfFilter is called from top_view.go and bottom_view.go to apply bridge IF filter.
func (m Model) selectedBridgeIfFilter() (ifName string, ok bool) {
	if m.topMode != TopBridge {
		return "", false
	}
	if m.bridgeDevFilterIdx < 0 {
		return "", false
	}
	items := m.bridgeItems()
	if len(items) == 0 {
		return "", false
	}
	bridge := items[clamp(m.bridgeCursor, 0, len(items)-1)]
	if m.bridgeDevFilterIdx >= len(bridge.Devs) {
		return "", false
	}
	return bridge.Devs[m.bridgeDevFilterIdx].IfName, true
}

// unixNanoNow is called from bottom_view.go for fade interpolation timestamps.
func unixNanoNow(t time.Time) int64 {
	return t.UnixNano()
}

// vrfDisplayDevs is called from top/bottom model helpers to show only active VRF devices.
func (m Model) vrfDisplayDevs(vrf vrfItem) []types.InterfaceInfo {
	used := m.vrfUsedIfSet(vrf)
	var devs []types.InterfaceInfo
	for _, d := range vrf.Devs {
		if used[d.IfName] {
			devs = append(devs, d)
		}
	}
	return devs
}

// vrfUsedIfSet is called from vrfDisplayDevs to mark interfaces referenced by rows.
func (m Model) vrfUsedIfSet(vrf vrfItem) map[string]bool {
	used := map[string]bool{}
	ifSet := map[string]bool{}
	for _, d := range vrf.Devs {
		ifSet[d.IfName] = true
	}

	for _, n := range m.st.NeighRecords(true) {
		if !ifSet[n.Val.Interface] {
			continue
		}
		if !m.detailed && isMulticastIPStr(n.Val.IP) {
			continue
		}
		used[n.Val.Interface] = true
	}

	for _, rr := range m.st.RouteRecords(true) {
		r := rr.Val
		if r.Table != vrf.TableID {
			if vrf.TableID != defaultVRFTableID || r.Table != mainRouteTableID {
				continue
			}
		}
		if (!m.detailed && (r.Type == unix.RTN_MULTICAST || r.Type == unix.RTN_BROADCAST)) || (r.Type == unix.RTN_ANYCAST) {
			continue
		}
		for _, nh := range r.Nexthops {
			if nh.Dev == "" {
				continue
			}
			if ifSet[nh.Dev] {
				used[nh.Dev] = true
			}
		}
	}

	return used
}
