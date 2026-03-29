package app

import (
	"fmt"
	"sort"
	"strconv"

	"github.com/charmbracelet/lipgloss"

	"github.com/msstnk/vxmon/internal/constants"
	"github.com/msstnk/vxmon/internal/helpers"
	"github.com/msstnk/vxmon/internal/store"
	"github.com/msstnk/vxmon/internal/types"
	"github.com/msstnk/vxmon/internal/ui"
)

type vrfItem struct {
	NamespaceID   uint64
	NamespaceName string
	NamespaceRoot bool
	Name          string
	Label         string
	TableID       uint32
	IfIndex       int
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

func buildBridgeItems(ns types.NamespaceInfo, ifaces []types.InterfaceInfo, st *store.Store) []bridgeItem {
	bridgeInfo := map[string]types.InterfaceInfo{}
	bound := map[string][]types.InterfaceInfo{}
	for _, it := range ifaces {
		if it.IfType == "bridge" {
			bridgeInfo[it.InterfaceName] = it
		}
	}
	for _, it := range ifaces {
		if it.MasterName == "" {
			continue
		}
		if _, ok := bridgeInfo[it.MasterName]; ok {
			if st.IsBridgePortReferenced(it.NamespaceID, it.IfIndex) {
				bound[it.MasterName] = append(bound[it.MasterName], it)
			}
		}
	}
	items := make([]bridgeItem, 0, len(bridgeInfo))
	for name, info := range bridgeInfo {
		devs := bound[name]
		sort.Slice(devs, func(i, j int) bool { return devs[i].IfIndex < devs[j].IfIndex })
		items = append(items, bridgeItem{
			NamespaceID:   ns.ID,
			NamespaceName: ns.ShortName,
			NamespaceRoot: ns.IsRoot,
			Label:         bridgeDisplayName(info),
			Info:          info,
			Devs:          devs,
		})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Info.IfIndex < items[j].Info.IfIndex })
	return items
}

func buildVRFItems(ns types.NamespaceInfo, ifaces []types.InterfaceInfo, st *store.Store, detailed bool) []vrfItem {
	vrfMasters := make(map[int]types.InterfaceInfo, len(ifaces))
	for _, it := range ifaces {
		if it.IfType == "vrf" {
			vrfMasters[it.IfIndex] = it
		}
	}

	bound := make(map[int][]types.InterfaceInfo, len(ifaces))
	var global []types.InterfaceInfo
	for _, it := range ifaces {
		if it.IfType == "vrf" {
			bound[it.IfIndex] = append(bound[it.IfIndex], it)
		}
		if !st.IsVRFInterfaceReferenced(it.NamespaceID, it.IfIndex, detailed) {
			continue
		}
		if it.MasterIndex != 0 {
			if _, ok := vrfMasters[it.MasterIndex]; ok {
				bound[it.MasterIndex] = append(bound[it.MasterIndex], it)
				continue
			}
		}
		global = append(global, it)
	}

	sort.Slice(global, func(i, j int) bool { return global[i].IfIndex < global[j].IfIndex })
	items := []vrfItem{{
		NamespaceID:   ns.ID,
		NamespaceName: ns.ShortName,
		NamespaceRoot: ns.IsRoot,
		Name:          constants.DefaultVRFName,
		Label:         vrfDisplayName(constants.DefaultVRFName, ns.ShortName, ns.IsRoot),
		TableID:       constants.DefaultVRFTableID,
		IfIndex:       0,
		Devs:          global,
	}}

	masterIndices := make([]int, 0, len(vrfMasters))
	for idx := range vrfMasters {
		masterIndices = append(masterIndices, idx)
	}
	sort.Ints(masterIndices)
	for _, idx := range masterIndices {
		master := vrfMasters[idx]
		devs := bound[idx]
		sort.Slice(devs, func(i, j int) bool { return devs[i].IfIndex < devs[j].IfIndex })
		items = append(items, vrfItem{
			NamespaceID:   ns.ID,
			NamespaceName: ns.ShortName,
			NamespaceRoot: ns.IsRoot,
			Name:          master.InterfaceName,
			Label:         vrfDisplayName(master.InterfaceName, ns.ShortName, ns.IsRoot),
			TableID:       master.TableID,
			IfIndex:       master.IfIndex,
			Devs:          devs,
		})
	}
	return items
}

func (m *Model) refreshTopItems() {
	nss := m.st.Namespaces()
	var bridges []bridgeItem
	var vrfs []vrfItem
	for _, ns := range nss {
		recs := m.st.InterfaceRecords(ns.ID, false, false)
		ifaces := make([]types.InterfaceInfo, len(recs))
		for i, r := range recs {
			ifaces[i] = r.Val
		}
		bridges = append(bridges, buildBridgeItems(ns, ifaces, m.st)...)
		vrfs = append(vrfs, buildVRFItems(ns, ifaces, m.st, m.detailed)...)
	}
	m.topItems = topItems{bridges: bridges, vrfs: vrfs, netns: nss}
}

func pick[T any](items []T, cursor int) T {
	if len(items) == 0 {
		var zero T
		return zero
	}
	return items[clamp(cursor, 0, len(items)-1)]
}

func bridgeIfFilter(items []bridgeItem, cursor, filterIdx int, mode TopMode) (ifName string, ok bool) {
	if mode != TopBridge || filterIdx < 0 {
		return "", false
	}
	bridge := pick(items, cursor)
	if bridge.Info.InterfaceName == "" {
		return "", false
	}
	if filterIdx >= len(bridge.Devs) {
		return "", false
	}
	return bridge.Devs[filterIdx].InterfaceName, true
}

// visibleChildRange returns [start, end) for rendering a child list within visibleTop rows.
// Keeps the selected filterIdx in view; -1 means no selection (show from start).
func visibleChildRange(total, filterIdx, visibleTop int) (int, int) {
	if visibleTop <= 1 {
		return 0, total
	}
	slots := visibleTop - 1
	if total <= slots {
		return 0, total
	}
	if filterIdx < 0 {
		return 0, slots
	}
	start := clamp(filterIdx, 0, total-1) - slots + 1
	if start < 0 {
		start = 0
	}
	end := start + slots
	if end > total {
		end = total
		start = max(0, end-slots)
	}
	return start, end
}

func bridgeVisibleChildRange(items []bridgeItem, cursor, filterIdx, visibleTop int, mode TopMode) (int, int) {
	bridge := pick(items, cursor)
	if bridge.Info.InterfaceName == "" {
		return 0, 0
	}
	if mode != TopBridge {
		return 0, len(bridge.Devs)
	}
	return visibleChildRange(len(bridge.Devs), filterIdx, visibleTop)
}

func vrfVisibleChildRange(items []vrfItem, cursor, filterIdx, visibleTop int, mode TopMode) (int, int) {
	if len(items) == 0 {
		return 0, 0
	}
	vrf := pick(items, cursor)
	if mode != TopVRF {
		return 0, len(vrf.Devs)
	}
	return visibleChildRange(len(vrf.Devs), filterIdx, visibleTop)
}

func vrfIfFilter(displayDevs []types.InterfaceInfo, filterIdx int, mode TopMode) (ifName string, ok bool) {
	if mode != TopVRF || filterIdx < 0 || filterIdx >= len(displayDevs) {
		return "", false
	}
	return displayDevs[filterIdx].InterfaceName, true
}

func (m *Model) buildTopRows(visibleTop int) (rows []ui.ListRow, cursorRenderedIndex int) {
	type displayRow struct {
		cols  []string
		style lipgloss.Style
	}
	toListRows := func(drs []displayRow) []ui.ListRow {
		tr := make([][]string, len(drs))
		for i := range drs {
			tr[i] = drs[i].cols
		}
		lines := ui.FormatRows(tr, m.width-6)
		rs := make([]ui.ListRow, len(lines))
		for i, line := range lines {
			rs[i] = ui.ListRow{Text: line, Style: drs[i].style}
		}
		return rs
	}
	base := lipgloss.NewStyle()
	child := lipgloss.NewStyle().Foreground(ui.ColorTopChild)
	childSelected := child.Foreground(ui.ColorTopChildSelected)
	childDim := child.Foreground(ui.ColorTopChildDimmed)
	now := m.fadeClock.UnixNano()

	switch m.topMode {
	case TopBridge:
		bridges := m.topItems.bridges
		cur := clamp(m.bridgeCursor, 0, len(bridges)-1)
		filterIf, filterOn := bridgeIfFilter(bridges, m.bridgeCursor, m.bridgeDevFilterIdx, m.topMode)
		displayRows := make([]displayRow, 0, len(bridges)+visibleTop)

		for i, b := range bridges {
			displayRows = append(displayRows, displayRow{
				cols: []string{
					b.Label,
					b.Info.Status + "/" + b.Info.OperState,
					"stp:" + helpers.BridgeSTPStateLabel(b.Info.STPState),
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
				stBase := child
				if filterOn {
					if d.InterfaceName == filterIf {
						stBase = childSelected
					} else {
						stBase = childDim
					}
				}
				vni := "-"
				if d.VxlanID > 0 {
					vni = strconv.Itoa(d.VxlanID)
				}
				displayRows = append(displayRows, displayRow{
					cols: []string{
						"  " + d.InterfaceName,
						d.Status + "/" + d.OperState,
						helpers.BridgePortStateLabel(d.BridgePortState),
						vni,
						d.MACAddr,
					},
					style: ui.FadeStyle(m.topParentMeta[bridgeChildKey(b, d)], now, stBase),
				})
			}
		}
		return toListRows(displayRows), cursorRenderedIndex

	case TopNETNS:
		items := m.topItems.netns
		cursorRenderedIndex = clamp(m.netnsCursor, 0, len(items)-1)
		drs := make([]displayRow, len(items))
		for i, item := range items {
			drs[i] = displayRow{
				cols: []string{
					namespaceListLabel(item),
					strconv.FormatUint(item.ID, 10),
					socketSummary(item),
				},
				style: ui.FadeStyle(m.topParentMeta[netnsParentKey(item)], now, base),
			}
		}
		return toListRows(drs), cursorRenderedIndex

	default:
		vrfs := m.topItems.vrfs
		cur := clamp(m.vrfCursor, 0, len(vrfs)-1)
		selected := pick(vrfs, m.vrfCursor)
		filterIf, filterOn := vrfIfFilter(selected.Devs, m.vrfDevFilterIdx, m.topMode)
		childStart, childEnd := vrfVisibleChildRange(vrfs, m.vrfCursor, m.vrfDevFilterIdx, visibleTop, m.topMode)
		displayRows := make([]displayRow, 0, len(vrfs)+visibleTop)

		for i, vrf := range vrfs {
			countText := "(L3 devs: " + strconv.Itoa(len(vrf.Devs)) + ")"
			if i == cur && filterOn {
				countText += " (filtered)"
			}
			displayRows = append(displayRows, displayRow{
				cols:  []string{vrf.Label, countText},
				style: ui.FadeStyle(m.topParentMeta[vrfParentKey(vrf)], now, base),
			})
			if i == cur {
				cursorRenderedIndex = len(displayRows) - 1
				for _, d := range selected.Devs[childStart:childEnd] {
					stBase := child
					if filterOn {
						if d.InterfaceName == filterIf {
							stBase = childSelected
						} else {
							stBase = childDim
						}
					}
					displayRows = append(displayRows, displayRow{
						cols: []string{
							"  " + d.InterfaceName,
							d.Status + "/" + d.OperState,
							d.IfType,
							d.MACAddr,
						},
						style: ui.FadeStyle(m.topParentMeta[vrfChildKey(vrf, d)], now, stBase),
					})
				}
			}
		}
		if len(vrfs) == 0 {
			cursorRenderedIndex = 0
		}
		return toListRows(displayRows), cursorRenderedIndex
	}
}
