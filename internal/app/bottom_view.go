package app

import (
	"fmt"
	"sort"
	"strings"

	"net/netip"

	"github.com/charmbracelet/lipgloss"
	"golang.org/x/sys/unix"

	"github.com/msstnk/vxmon/internal/helpers"
	"github.com/msstnk/vxmon/internal/store"
	"github.com/msstnk/vxmon/internal/types"
	"github.com/msstnk/vxmon/internal/ui"
)

// bottom_view.go builds the bottom pane tables for FDB, Neigh, Route, Process, and Link modes.
func errorRows(text string) []ui.ListRow {
	style := lipgloss.NewStyle().Foreground(ui.ColorWarn).Bold(true)
	return []ui.ListRow{{Text: text, Style: style}}
}

func renderTableRows(headers []string, tableRows [][]string, styles []lipgloss.Style, width int) (string, []ui.ListRow) {
	return renderTableRowsWithSpecs(headers, tableRows, nil, styles, width)
}

func renderTableRowsWithSpecs(headers []string, tableRows [][]string, specs []ui.ColumnSpec, styles []lipgloss.Style, width int) (string, []ui.ListRow) {
	var (
		header string
		lines  []string
	)
	if len(specs) == 0 {
		header, lines = ui.FormatTable(headers, tableRows, width)
	} else {
		header, lines = ui.FormatTableWithSpecs(headers, tableRows, specs, width)
	}
	rows := make([]ui.ListRow, 0, len(lines))
	for i, line := range lines {
		rows = append(rows, ui.ListRow{Text: line, Style: styles[i]})
	}
	return header, rows
}

func (m *Model) buildBottom(data topItems) (header string, rows []ui.ListRow, cursorIdx int) {
	base := lipgloss.NewStyle().Foreground(lipgloss.Color(ui.ColorBaseColor))
	now := m.fadeClock.UnixNano()

	switch {
	case m.topMode == TopBridge && m.botMode == BottomFDB:
		bridge := pickBridge(data.bridges, m.bridgeCursor)
		if bridge.Info.InterfaceName == "" {
			return "", nil, 0
		}
		selectedPort, portFilterOn := bridgeIfFilter(data.bridges, m.bridgeCursor, m.bridgeDevFilterIdx, m.topMode)

		fdbs := m.st.FDBRecords(true)
		liveFdbs := m.st.FDBRecords(false)
		neighs := m.st.NeighRecords(false)
		neighByMAC := make(map[string][]types.NeighEntry, len(neighs))
		for _, n := range neighs {
			if n.Val.NamespaceID != bridge.NamespaceID {
				continue
			}
			if n.Val.MACAddr != "" && n.Val.IP != "" {
				neighByMAC[n.Val.MACAddr] = append(neighByMAC[n.Val.MACAddr], n.Val)
			}
		}

		allBridgeFdb := make([]store.Record[types.FdbEntry], 0, len(liveFdbs))
		final := make([]store.Record[types.FdbEntry], 0, len(fdbs))
		for _, r := range liveFdbs {
			if r.Val.NamespaceID != bridge.NamespaceID || r.Val.BridgeName != bridge.Info.InterfaceName {
				continue
			}
			allBridgeFdb = append(allBridgeFdb, r)
		}
		for _, r := range fdbs {
			if r.Val.NamespaceID != bridge.NamespaceID || r.Val.BridgeName != bridge.Info.InterfaceName {
				continue
			}
			if portFilterOn && r.Val.PortName != selectedPort {
				continue
			}
			if !m.detailed && r.Val.VLANID == 0 && r.Val.VxlanID == 0 {
				continue
			}
			final = append(final, r)
		}

		vniToVlans := make(map[int]map[int]struct{}, len(allBridgeFdb))
		for _, r := range allBridgeFdb {
			if r.Val.VLANID != 0 && r.Val.VxlanID != 0 {
				if _, ok := vniToVlans[r.Val.VxlanID]; !ok {
					vniToVlans[r.Val.VxlanID] = map[int]struct{}{}
				}
				vniToVlans[r.Val.VxlanID][r.Val.VLANID] = struct{}{}
			}
		}

		for i := range final {
			if final[i].Val.VLANID == 0 && final[i].Val.VxlanID != 0 {
				if vlans := vniToVlans[final[i].Val.VxlanID]; len(vlans) == 1 {
					for vlan := range vlans {
						final[i].Val.VLANID = vlan
					}
				}
			}
		}
		sort.Slice(final, func(i, j int) bool {
			if final[i].Val.VLANID != final[j].Val.VLANID {
				return final[i].Val.VLANID < final[j].Val.VLANID
			}
			if final[i].Val.MACAddr != final[j].Val.MACAddr {
				if final[i].Val.MACAddr == "00:00:00:00:00:00" {
					return false
				}
				if final[j].Val.MACAddr == "00:00:00:00:00:00" {
					return true
				}
				return final[i].Val.MACAddr < final[j].Val.MACAddr
			}
			if final[i].Val.PortID != final[j].Val.PortID {
				return final[i].Val.PortID < final[j].Val.PortID
			}
			return final[i].Val.RemoteVTEP < final[j].Val.RemoteVTEP
		})

		headers := []string{"VLAN", "MAC_ADDR", "NEIGH_IP", "PORT", "VNI", "REMOTE_VTEP"}
		type fdbDisplayRow struct {
			rec     store.Record[types.FdbEntry]
			neighIP string
			vni     string
		}
		displayRows := make([]fdbDisplayRow, 0, len(final))
		remotePresent := make(map[string]struct{}, len(final))
		for _, r := range final {
			vni := ""
			if r.Val.VxlanID != 0 {
				vni = fmt.Sprintf("%d", r.Val.VxlanID)
			}
			neighIP := ""
			if r.Val.MACAddr != "" && r.Val.MACAddr != "00:00:00:00:00:00" {
				ipSet := map[string]struct{}{}
				for _, n := range neighByMAC[r.Val.MACAddr] {
					if n.VLANID != 0 {
						if r.Val.VLANID == 0 || n.VLANID != r.Val.VLANID {
							continue
						}
					}
					if n.VxlanID != 0 {
						if r.Val.VxlanID == 0 || n.VxlanID != r.Val.VxlanID {
							continue
						}
					}
					ipSet[n.IP] = struct{}{}
				}
				if len(ipSet) > 0 {
					ips := make([]netip.Addr, 0, len(ipSet))
					for ipStr := range ipSet {
						if addr, err := netip.ParseAddr(ipStr); err == nil {
							ips = append(ips, addr)
						}
					}

					sort.Slice(ips, func(i, j int) bool {
						return ips[i].Less(ips[j])
					})

					if len(ips) == 1 {
						neighIP = ips[0].String()
					} else if len(ips) > 1 {
						neighIP = fmt.Sprintf("%s (+%d)", ips[0], len(ips)-1)
					}
				}
			}
			displayRows = append(displayRows, fdbDisplayRow{
				rec:     r,
				neighIP: neighIP,
				vni:     vni,
			})
			if r.Val.RemoteVTEP != "" {
				key := fmt.Sprintf("%s|%s|%d|%s|%d", r.Val.BridgeName, neighIP, r.Val.VLANID, r.Val.PortName, r.Val.VxlanID)
				remotePresent[key] = struct{}{}
			}
		}

		tableRows := make([][]string, 0, len(displayRows))
		styles := make([]lipgloss.Style, 0, len(displayRows))
		for _, row := range displayRows {
			r := row.rec
			if !m.detailed && r.Val.RemoteVTEP == "" {
				key := fmt.Sprintf("%s|%s|%d|%s|%d", r.Val.BridgeName, row.neighIP, r.Val.VLANID, r.Val.PortName, r.Val.VxlanID)
				if _, ok := remotePresent[key]; ok {
					continue
				}
			}
			tableRows = append(tableRows, []string{
				fmt.Sprintf("%d", r.Val.VLANID),
				r.Val.MACAddr,
				row.neighIP,
				r.Val.PortName,
				row.vni,
				r.Val.RemoteVTEP,
			})
			st := ui.FadeStyle(r.Meta, now, base)
			if r.Val.MACAddr == "00:00:00:00:00:00" {
				st = st.Foreground(ui.ColorFdbBum)
			}
			styles = append(styles, st)
		}
		header, rows = renderTableRows(headers, tableRows, styles, m.width-6)
		return header, rows, m.botCursor

	case m.topMode == TopVRF && m.botMode == BottomNeigh:
		vrf := pickVRF(data.vrfs, m.vrfCursor)
		headers := []string{"IP_ADDR", "TYPE", "MAC_ADDR", "IF_NAME", "VNI", "REMOTE_VTEP", "STATE"}
		var tableRows [][]string
		var styles []lipgloss.Style

		neighs := m.st.NeighRecords(true)
		fdbs := m.st.FDBRecords(false)
		type fdbInfo struct {
			vni    int
			remote string
		}
		macInfo := make(map[string][]types.FdbEntry, len(fdbs))
		for _, f := range fdbs {
			if f.Val.NamespaceID != vrf.NamespaceID || f.Val.MACAddr == "" {
				continue
			}
			macInfo[f.Val.MACAddr] = append(macInfo[f.Val.MACAddr], f.Val)
		}

		filterIf, filterOn := vrfIfFilter(vrf.Devs, m.vrfDevFilterIdx, m.topMode)
		ifSet := make(map[string]struct{}, len(vrf.Devs))
		for _, d := range vrf.Devs {
			ifSet[d.InterfaceName] = struct{}{}
		}

		type neighRow struct {
			rec    store.Record[types.NeighEntry]
			vni    int
			remote string
			mcast  bool
		}
		var items []neighRow
		for _, n := range neighs {
			if n.Val.NamespaceID != vrf.NamespaceID {
				continue
			}
			if _, ok := ifSet[n.Val.InterfaceName]; !ok {
				continue
			}
			if filterOn && n.Val.InterfaceName != filterIf {
				continue
			}
			mcast := helpers.IsMulticastIP(n.Val.IP)
			if mcast && !m.detailed {
				continue
			}
			infoSet := map[fdbInfo]struct{}{}
			for _, f := range macInfo[n.Val.MACAddr] {
				if n.Val.VLANID != 0 {
					if f.VLANID == 0 || f.VLANID != n.Val.VLANID {
						continue
					}
				}
				if n.Val.VxlanID != 0 {
					if f.VxlanID == 0 || f.VxlanID != n.Val.VxlanID {
						continue
					}
				}
				infoSet[fdbInfo{vni: f.VxlanID, remote: f.RemoteVTEP}] = struct{}{}
			}
			info := fdbInfo{}
			if len(infoSet) == 1 {
				for candidate := range infoSet {
					info = candidate
				}
			}
			items = append(items, neighRow{rec: n, vni: info.vni, remote: info.remote, mcast: mcast})
		}

		sort.Slice(items, func(i, j int) bool {
			a, b := items[i], items[j]
			fa := helpers.IpFamilyOrderFromAddrStr(a.rec.Val.IP)
			fb := helpers.IpFamilyOrderFromAddrStr(b.rec.Val.IP)
			if fa != fb {
				return fa < fb
			}
			if a.rec.Val.MACAddr != b.rec.Val.MACAddr {
				return a.rec.Val.MACAddr < b.rec.Val.MACAddr
			}
			if a.rec.Val.InterfaceName != b.rec.Val.InterfaceName {
				return a.rec.Val.InterfaceName < b.rec.Val.InterfaceName
			}
			return a.rec.Val.IP < b.rec.Val.IP
		})

		for _, it := range items {
			typeStr := "UCAST"
			vni := ""
			if it.vni != 0 {
				vni = fmt.Sprintf("%d", it.vni)
			}
			st := ui.FadeStyle(it.rec.Meta, now, base)
			if it.mcast {
				typeStr = "MCAST"
				st = st.Foreground(ui.ColorRouteMcast)
			}
			switch it.rec.Val.State {
			case unix.NUD_INCOMPLETE:
				st = st.Foreground(ui.ColorNeighIncomplete)
			case unix.NUD_FAILED:
				st = st.Foreground(ui.ColorNeighFailed)
			}
			tableRows = append(tableRows, []string{
				it.rec.Val.IP,
				typeStr,
				it.rec.Val.MACAddr,
				it.rec.Val.InterfaceName,
				vni,
				it.remote,
				helpers.FormatNeighState(it.rec.Val.State),
			})
			styles = append(styles, st)
		}
		header, rows = renderTableRows(headers, tableRows, styles, m.width-6)
		return header, rows, m.botCursor

	case m.topMode == TopVRF && m.botMode == BottomRoute:
		vrf := pickVRF(data.vrfs, m.vrfCursor)
		headers := []string{"    ", "DESTINATION", "GATEWAY", "DEVICE"}
		specs := []ui.ColumnSpec{
			{},
			{MinWidth: 19},
			{MinWidth: 16},
			{},
		}
		var tableRows [][]string
		var styles []lipgloss.Style

		routes := m.st.RouteRecords(true)
		filterIf, filterOn := vrfIfFilter(vrf.Devs, m.vrfDevFilterIdx, m.topMode)

		type routeItem struct {
			rr     store.Record[types.RouteEntry]
			nhs    []types.Nexthop
			fam    int
			minDev string
		}
		var items []routeItem

		for _, rr := range routes {
			r := rr.Val
			if r.NamespaceID != vrf.NamespaceID {
				continue
			}
			if !matchesVRFRouteTable(vrf.TableID, r.Table) {
				continue
			}
			if (!m.detailed && (r.Type == unix.RTN_MULTICAST || r.Type == unix.RTN_BROADCAST)) || r.Type == unix.RTN_ANYCAST {
				continue
			}

			var nhs []types.Nexthop
			for _, nh := range r.Nexthops {
				if filterOn && nh.Dev != filterIf {
					continue
				}
				nhs = append(nhs, nh)
			}
			if len(nhs) == 0 {
				continue
			}
			sort.Slice(nhs, func(i, j int) bool {
				if nhs[i].Dev != nhs[j].Dev {
					return nhs[i].Dev < nhs[j].Dev
				}
				return nhs[i].Gw < nhs[j].Gw
			})
			items = append(items, routeItem{
				rr:     rr,
				nhs:    nhs,
				fam:    helpers.RouteFamilyOrder(r.Dst, nhs[0].Gw),
				minDev: nhs[0].Dev,
			})
		}

		sort.Slice(items, func(i, j int) bool {
			a, b := items[i], items[j]
			if a.fam != b.fam {
				return a.fam < b.fam
			}
			if a.rr.Val.Dst != b.rr.Val.Dst {
				return a.rr.Val.Dst < b.rr.Val.Dst
			}
			return a.minDev < b.minDev
		})

		for _, it := range items {
			r := it.rr.Val
			isECMP := len(it.nhs) > 1
			prefix := "  "
			switch r.Type {
			case unix.RTN_MULTICAST:
				prefix = "m "
			case unix.RTN_BROADCAST:
				prefix = "b "
			case unix.RTN_LOCAL:
				prefix = "C "
			default:
				baseP := "  "
				switch r.Protocol {
				case unix.RTPROT_STATIC:
					baseP = "S "
				case unix.RTPROT_KERNEL:
					baseP = "L "
				case 11, 17, 186:
					baseP = "B "
				}
				if isECMP && baseP != "  " {
					prefix = strings.TrimSpace(baseP) + "="
				} else if isECMP {
					prefix = " ="
				} else {
					prefix = baseP
				}
			}
			st := ui.FadeStyle(it.rr.Meta, now, base)
			switch r.Type {
			case unix.RTN_LOCAL:
				st = st.Foreground(ui.ColorRouteMcast)
			case unix.RTN_BROADCAST, unix.RTN_MULTICAST:
				st = st.Foreground(ui.ColorRouteBcast)
			}
			for i, nh := range it.nhs {
				dstStr := r.Dst
				typeStr := prefix
				if i > 0 {
					dstStr = ""
					typeStr = "  "
				}
				tableRows = append(tableRows, []string{typeStr, dstStr, nh.Gw, nh.Dev})
				styles = append(styles, st)
			}
		}

		header, rows = renderTableRowsWithSpecs(headers, tableRows, specs, styles, m.width-6)
		return header, rows, m.botCursor

	case m.topMode == TopNETNS && m.botMode == BottomProcess:
		ns := pickNETNS(data.netns, m.netnsCursor)
		if ns.DisplayName == "" {
			return "", nil, 0
		}
		if ns.PermissionErr != "" {
			return "", errorRows("Error: " + ns.PermissionErr), 0
		}

		headers := []string{"PID", "COMMAND", "USER", "LOAD(%)"}
		specs := []ui.ColumnSpec{
			{},
			{MaxWidth: 40},
			{},
			{},
		}
		procs := m.st.NamespaceProcessRecords(ns.ID, true)
		tableRows := make([][]string, 0, len(procs))
		styles := make([]lipgloss.Style, 0, len(procs))
		for _, proc := range procs {
			load := ""
			if proc.Val.LoadPct > 0 {
				load = fmt.Sprintf("%.1f", proc.Val.LoadPct)
			}
			tableRows = append(tableRows, []string{
				fmt.Sprintf("%d", proc.Val.PID),
				proc.Val.Exe,
				proc.Val.User,
				load,
			})
			styles = append(styles, ui.FadeStyle(proc.Meta, now, base))
		}
		header, rows = renderTableRowsWithSpecs(headers, tableRows, specs, styles, m.width-6)
		return header, rows, m.botCursor

	case m.topMode == TopNETNS && m.botMode == BottomLink:
		ns := pickNETNS(data.netns, m.netnsCursor)
		if ns.DisplayName == "" {
			return "", nil, 0
		}
		if ns.PermissionErr != "" {
			return "", errorRows("Error: " + ns.PermissionErr), 0
		}

		headers := []string{"NAME", "TYPE", "RX", "TX", "RX-ERR", "TX-ERR"}
		specs := []ui.ColumnSpec{
			{},
			{},
			{MinWidth: 8},
			{MinWidth: 8},
			{},
			{},
		}
		links := m.st.NamespaceLinkRecords(ns.ID, true)
		tableRows := make([][]string, 0, len(links))
		styles := make([]lipgloss.Style, 0, len(links))
		for _, link := range links {
			tableRows = append(tableRows, []string{
				link.Val.Name,
				link.Val.Type,
				helpers.FormatBps(link.Val.RxBps),
				helpers.FormatBps(link.Val.TxBps),
				fmt.Sprintf("%d", link.Val.RxErrors),
				fmt.Sprintf("%d", link.Val.TxErrors),
			})
			styles = append(styles, ui.FadeStyle(link.Meta, now, base))
		}
		header, rows = renderTableRowsWithSpecs(headers, tableRows, specs, styles, m.width-6)
		return header, rows, m.botCursor
	}

	return "", nil, 0
}
