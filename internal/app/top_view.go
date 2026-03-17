package app

import (
	"fmt"
	"sort"

	"github.com/charmbracelet/lipgloss"

	"github.com/msstnk/vxmon/internal/constants"
	"github.com/msstnk/vxmon/internal/store"
	"github.com/msstnk/vxmon/internal/types"
	"github.com/msstnk/vxmon/internal/ui"
)

// top_view.go builds top-pane rows and selection context for Bridge/VRF/NETNS modes.
type vrfItem struct {
	NamespaceID   uint64
	NamespaceName string
	NamespaceRoot bool
	Name          string
	Label         string
	TableID       uint32
	InterfaceID   int
	Devs          []types.InterfaceInfo
}

type bridgeItem struct {
	NamespaceID   uint64
	NamespaceName string
	NamespaceRoot bool
	Label         string
	Info          types.InterfaceInfo
	Devs          []types.InterfaceInfo
}

type netnsItem = types.NamespaceInfo

type topItems struct {
	bridges []bridgeItem
	vrfs    []vrfItem
	netns   []netnsItem
}

func namespaceListLabel(ns types.NamespaceInfo) string {
	label := ns.DisplayName
	if ns.IsRoot {
		label = constants.RootNamespaceLabel
	}
	if ns.IsCurrent {
		return "*" + label
	}
	return label
}

func namespaceSuffix(shortName string, isRoot bool) string {
	if isRoot || shortName == "" {
		return ""
	}
	return shortName
}

func bridgeDisplayName(it types.InterfaceInfo) string {
	if it.NamespaceRoot {
		return it.InterfaceName
	}
	return it.InterfaceName + "@" + namespaceSuffix(it.NamespaceName, it.NamespaceRoot)
}

func vrfDisplayName(name string, shortName string, isRoot bool) string {
	if isRoot {
		return name
	}
	return name + "@" + namespaceSuffix(shortName, isRoot)
}

func socketSummary(ns types.NamespaceInfo) string {
	if ns.SocketUsed == 0 && ns.TCPInUse == 0 && ns.UDPInUse == 0 && ns.TCP6InUse == 0 && ns.UDP6InUse == 0 {
		return "-"
	}
	return fmt.Sprintf("%d (TCP: %d UDP: %d TCP6: %d UDP6: %d)", ns.SocketUsed, ns.TCPInUse, ns.UDPInUse, ns.TCP6InUse, ns.UDP6InUse)
}

func buildBridgeItems(ifaces []types.InterfaceInfo, st *store.Store) []bridgeItem {
	bridgeInfo := map[string]types.InterfaceInfo{}
	bound := map[string][]types.InterfaceInfo{}

	for _, it := range ifaces {
		if it.IfType == "bridge" {
			bridgeInfo[bridgeGroupKey(it.NamespaceID, it.InterfaceName)] = it
		}
	}
	for _, it := range ifaces {
		if it.MasterName == "" {
			continue
		}
		key := bridgeGroupKey(it.NamespaceID, it.MasterName)
		if _, ok := bridgeInfo[key]; ok {
			if st.IsBridgePortReferenced(it.NamespaceID, it.InterfaceID) {
				bound[key] = append(bound[key], it)
			}
		}
	}

	items := make([]bridgeItem, 0, len(bridgeInfo))
	for key, info := range bridgeInfo {
		devs := bound[key]
		sort.Slice(devs, func(i, j int) bool { return devs[i].InterfaceID < devs[j].InterfaceID })
		items = append(items, bridgeItem{
			NamespaceID:   info.NamespaceID,
			NamespaceName: info.NamespaceName,
			NamespaceRoot: info.NamespaceRoot,
			Label:         bridgeDisplayName(info),
			Info:          info,
			Devs:          devs,
		})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].NamespaceRoot != items[j].NamespaceRoot {
			return items[i].NamespaceRoot
		}
		if items[i].NamespaceName != items[j].NamespaceName {
			return items[i].NamespaceName < items[j].NamespaceName
		}
		return items[i].Info.InterfaceID < items[j].Info.InterfaceID
	})
	return items
}

func (m *Model) bridgeItems() []bridgeItem {
	return buildBridgeItems(m.st.Interfaces(), m.st)
}

func buildVRFItems(ifaces []types.InterfaceInfo, st *store.Store, detailed bool) []vrfItem {
	byNS := map[uint64][]types.InterfaceInfo{}
	nsInfo := map[uint64]types.InterfaceInfo{}
	for _, it := range ifaces {
		byNS[it.NamespaceID] = append(byNS[it.NamespaceID], it)
		if _, ok := nsInfo[it.NamespaceID]; !ok {
			nsInfo[it.NamespaceID] = it
		}
	}

	var nsIDs []uint64
	for nsID := range byNS {
		nsIDs = append(nsIDs, nsID)
	}
	sort.Slice(nsIDs, func(i, j int) bool {
		a := nsInfo[nsIDs[i]]
		b := nsInfo[nsIDs[j]]
		if a.NamespaceRoot != b.NamespaceRoot {
			return a.NamespaceRoot
		}
		return a.NamespaceName < b.NamespaceName
	})

	var items []vrfItem
	for _, nsID := range nsIDs {
		nsIfaces := byNS[nsID]
		ref := nsInfo[nsID]

		vrfMasters := map[string]types.InterfaceInfo{}
		bound := map[string][]types.InterfaceInfo{}
		for _, it := range nsIfaces {
			if it.IfType == "vrf" {
				vrfMasters[it.InterfaceName] = it
			}
		}
		for _, it := range nsIfaces {
			if it.MasterName == "" {
				continue
			}
			if _, ok := vrfMasters[it.MasterName]; ok {
				if st.IsVRFInterfaceReferenced(it.NamespaceID, it.InterfaceID, detailed) {
					bound[it.MasterName] = append(bound[it.MasterName], it)
				}
			}
		}

		vrfMasterSet := map[string]struct{}{}
		for name := range vrfMasters {
			vrfMasterSet[name] = struct{}{}
		}
		var global []types.InterfaceInfo
		for _, it := range nsIfaces {
			if !st.IsVRFInterfaceReferenced(it.NamespaceID, it.InterfaceID, detailed) {
				continue
			}
			if it.MasterName == "" {
				global = append(global, it)
				continue
			}
			if _, ok := vrfMasterSet[it.MasterName]; !ok {
				global = append(global, it)
			}
		}
		sort.Slice(global, func(i, j int) bool { return global[i].InterfaceID < global[j].InterfaceID })
		items = append(items, vrfItem{
			NamespaceID:   nsID,
			NamespaceName: ref.NamespaceName,
			NamespaceRoot: ref.NamespaceRoot,
			Name:          constants.DefaultVRFName,
			Label:         vrfDisplayName(constants.DefaultVRFName, ref.NamespaceName, ref.NamespaceRoot),
			TableID:       constants.DefaultVRFTableID,
			InterfaceID:   0,
			Devs:          global,
		})

		var masterNames []string
		for name := range vrfMasters {
			masterNames = append(masterNames, name)
		}
		sort.Slice(masterNames, func(i, j int) bool {
			return vrfMasters[masterNames[i]].InterfaceID < vrfMasters[masterNames[j]].InterfaceID
		})

		for _, name := range masterNames {
			devs := bound[name]
			sort.Slice(devs, func(i, j int) bool { return devs[i].InterfaceID < devs[j].InterfaceID })
			master := vrfMasters[name]
			items = append(items, vrfItem{
				NamespaceID:   nsID,
				NamespaceName: ref.NamespaceName,
				NamespaceRoot: ref.NamespaceRoot,
				Name:          name,
				Label:         vrfDisplayName(name, ref.NamespaceName, ref.NamespaceRoot),
				TableID:       master.TableID,
				InterfaceID:   master.InterfaceID,
				Devs:          devs,
			})
		}
	}
	return items
}

func (m *Model) vrfItems() []vrfItem {
	return buildVRFItems(m.st.Interfaces(), m.st, m.detailed)
}

func (m *Model) currentTopItems() topItems {
	ifaces := m.st.Interfaces()
	return topItems{
		bridges: buildBridgeItems(ifaces, m.st),
		vrfs:    buildVRFItems(ifaces, m.st, m.detailed),
		netns:   m.st.Namespaces(),
	}
}

func matchesVRFRouteTable(vrfTableID uint32, routeTableID uint32) bool {
	if routeTableID == vrfTableID {
		return true
	}
	return vrfTableID == constants.DefaultVRFTableID && routeTableID == constants.MainRouteTableID
}

func (m *Model) netnsItems() []netnsItem {
	return m.st.Namespaces()
}

func pickBridge(items []bridgeItem, cursor int) bridgeItem {
	return items[clamp(cursor, 0, len(items)-1)]
}

func pickVRF(items []vrfItem, cursor int) vrfItem {
	return items[clamp(cursor, 0, len(items)-1)]
}

func pickNETNS(items []netnsItem, cursor int) types.NamespaceInfo {
	return items[clamp(cursor, 0, len(items)-1)]
}

func bridgeIfFilter(items []bridgeItem, cursor int, filterIdx int, mode TopMode) (ifName string, ok bool) {
	if mode != TopBridge || filterIdx < 0 {
		return "", false
	}
	bridge := pickBridge(items, cursor)
	// Validate that the bridge is valid (non-empty) before accessing Devs
	if bridge.Info.InterfaceName == "" {
		return "", false
	}
	if filterIdx >= len(bridge.Devs) {
		return "", false
	}
	return bridge.Devs[filterIdx].InterfaceName, true
}

func bridgeVisibleChildRange(items []bridgeItem, cursor int, filterIdx int, visibleTop int, mode TopMode) (start int, end int) {
	bridge := pickBridge(items, cursor)
	if bridge.Info.InterfaceName == "" {
		return 0, 0
	}
	if mode != TopBridge || filterIdx < 0 || visibleTop <= 1 {
		return 0, len(bridge.Devs)
	}
	if len(bridge.Devs) <= visibleTop-1 {
		return 0, len(bridge.Devs)
	}

	selected := clamp(filterIdx, 0, len(bridge.Devs)-1)
	slots := visibleTop - 1
	start = selected - slots + 1
	if start < 0 {
		start = 0
	}
	end = start + slots
	if end > len(bridge.Devs) {
		end = len(bridge.Devs)
		start = max(0, end-slots)
	}
	return start, end
}

func vrfIfFilter(displayDevs []types.InterfaceInfo, filterIdx int, mode TopMode) (ifName string, ok bool) {
	if mode != TopVRF || filterIdx < 0 || filterIdx >= len(displayDevs) {
		return "", false
	}
	return displayDevs[filterIdx].InterfaceName, true
}

func (m *Model) buildTopRows(visibleTop int, data topItems) (rows []ui.ListRow, cursorRenderedIndex int) {
	base := lipgloss.NewStyle()
	child := lipgloss.NewStyle().Foreground(ui.ColorTopChild)
	childSelected := child.Foreground(ui.ColorTopChildSelected)
	childDim := child.Foreground(ui.ColorTopChildDimmed)
	now := m.fadeClock.UnixNano()

	switch m.topMode {
	case TopBridge:
		bridges := data.bridges
		cur := clamp(m.bridgeCursor, 0, len(bridges)-1)
		filterIf, filterOn := bridgeIfFilter(bridges, m.bridgeCursor, m.bridgeDevFilterIdx, m.topMode)
		type displayRow struct {
			cols  []string
			style lipgloss.Style
		}
		displayRows := make([]displayRow, 0, len(bridges))
		cursorRenderedIndex = cur
		for i, b := range bridges {
			displayRows = append(displayRows, displayRow{
				cols: []string{
					b.Label,
					b.Info.Status,
					fmt.Sprintf("stp:%s", b.Info.STPState),
					"",
					b.Info.MACAddr,
				},
				style: ui.FadeStyle(m.topParentMeta[bridgeParentKey(b)], now, base),
			})
			if i != cur {
				continue
			}
			cursorRenderedIndex = len(displayRows) - 1
			childStart, childEnd := bridgeVisibleChildRange(bridges, m.bridgeCursor, m.bridgeDevFilterIdx, visibleTop, m.topMode)
			for _, d := range b.Devs[childStart:childEnd] {
				vni := "-"
				if d.VxlanID > 0 {
					vni = fmt.Sprintf("%d", d.VxlanID)
				}
				baseStyle := child
				if filterOn {
					if d.InterfaceName == filterIf {
						baseStyle = childSelected
					} else {
						baseStyle = childDim
					}
				}
				st := ui.FadeStyle(m.topParentMeta[bridgeChildKey(b, d)], now, baseStyle)
				displayRows = append(displayRows, displayRow{
					cols: []string{
						"  " + d.InterfaceName,
						d.Status,
						d.BridgePortState,
						vni,
						d.MACAddr,
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
		return rows, cursorRenderedIndex

	case TopNETNS:
		items := data.netns
		cursorRenderedIndex = clamp(m.netnsCursor, 0, len(items)-1)
		tableRows := make([][]string, 0, len(items))
		for _, item := range items {
			tableRows = append(tableRows, []string{
				namespaceListLabel(item),
				fmt.Sprintf("%d", item.ID),
				socketSummary(item),
			})
		}
		lines := ui.FormatRows(tableRows, m.width-6)
		for i, line := range lines {
			rows = append(rows, ui.ListRow{
				Text:  line,
				Style: ui.FadeStyle(m.topParentMeta[netnsParentKey(items[i])], now, base),
			})
		}
		return rows, cursorRenderedIndex

	default:
		vrfs := data.vrfs
		cur := clamp(m.vrfCursor, 0, len(vrfs)-1)
		selected := pickVRF(vrfs, m.vrfCursor)
		selectedDisplayDevs := selected.Devs
		filterIf, filterOn := vrfIfFilter(selectedDisplayDevs, m.vrfDevFilterIdx, m.topMode)
		idx := 0
		parentRows := make([][]string, 0, len(vrfs))
		parentStyles := make([]lipgloss.Style, 0, len(vrfs))
		var childRows [][]string
		var childStyles []lipgloss.Style
		for i, vrf := range vrfs {
			displayDevs := selectedDisplayDevs
			if i != cur {
				displayDevs = vrf.Devs
			}
			cnt := len(displayDevs)
			countText := fmt.Sprintf("(L3 devs: %d)", cnt)
			if i == cur && filterOn {
				countText += " (filtered)"
			}
			parentRows = append(parentRows, []string{vrf.Label, countText})
			parentStyles = append(parentStyles, ui.FadeStyle(m.topParentMeta[vrfParentKey(vrf)], now, base))

			if i == cur {
				cursorRenderedIndex = idx
				for _, d := range displayDevs {
					baseStyle := child
					if filterOn {
						if d.InterfaceName == filterIf {
							baseStyle = childSelected
						} else {
							baseStyle = childDim
						}
					}
					st := ui.FadeStyle(m.topParentMeta[vrfChildKey(vrf, d)], now, baseStyle)
					childRows = append(childRows, []string{
						"  " + d.InterfaceName,
						d.Status,
						d.IfType,
						d.MACAddr,
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
		return rows, cursorRenderedIndex
	}
}

func (m *Model) selectedVRF() vrfItem {
	return pickVRF(m.vrfItems(), m.vrfCursor)
}
