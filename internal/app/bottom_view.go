package app

import (
	"fmt"
	"net"
	"net/netip"
	"sort"
	"strconv"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"golang.org/x/sys/unix"

	"github.com/msstnk/vxmon/internal/constants"
	"github.com/msstnk/vxmon/internal/helpers"
	"github.com/msstnk/vxmon/internal/store"
	"github.com/msstnk/vxmon/internal/types"
	"github.com/msstnk/vxmon/internal/ui"
)

func errorRows(text string) []ui.ListRow {
	style := lipgloss.NewStyle().Foreground(ui.ColorWarn).Bold(true)
	return []ui.ListRow{{Text: text, Style: style}}
}

func resolveNeighIPs(mac string, candidates []types.NeighEntry, vlanID, vxlanID int) string {
	hw, err := net.ParseMAC(mac)
	if err != nil || helpers.IsUnspecified(hw) {
		return ""
	}
	ipSet := map[string]struct{}{}
	for _, n := range candidates {
		if n.VLANID != 0 {
			if vlanID == 0 || n.VLANID != vlanID {
				continue
			}
		}
		if n.VxlanID != 0 {
			if vxlanID == 0 || n.VxlanID != vxlanID {
				continue
			}
		}
		ipSet[n.IP] = struct{}{}
	}
	if len(ipSet) == 0 {
		return ""
	}
	ips := make([]netip.Addr, 0, len(ipSet))
	for ipStr := range ipSet {
		if addr, err := netip.ParseAddr(ipStr); err == nil {
			ips = append(ips, addr)
		}
	}
	sort.Slice(ips, func(i, j int) bool { return ips[i].Less(ips[j]) })
	if len(ips) == 1 {
		return ips[0].String()
	}
	return fmt.Sprintf("%s (+%d)", ips[0], len(ips)-1)
}

func routePrefix(rtype, proto int, isECMP bool) string {
	switch rtype {
	case unix.RTN_MULTICAST:
		return "m "
	case unix.RTN_BROADCAST:
		return "b "
	case unix.RTN_LOCAL:
		return "C "
	}
	base := "  "
	switch proto {
	case unix.RTPROT_STATIC:
		base = "S "
	case unix.RTPROT_KERNEL:
		base = "L "
	case unix.RTPROT_ZEBRA, unix.RTPROT_BIRD, unix.RTPROT_BGP:
		base = "B "
	}
	if isECMP {
		if base != "  " {
			return strings.TrimSpace(base) + "="
		}
		return " ="
	}
	return base
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

func (m *Model) buildBottom() (header string, rows []ui.ListRow) {
	base := lipgloss.NewStyle().Foreground(lipgloss.Color(ui.ColorBaseColor))
	now := m.fadeClock.UnixNano()

	switch {
	case m.topMode == TopBridge && m.botMode == BottomFDB:
		bridge := pick(m.topItems.bridges, m.bridgeCursor)
		if bridge.Info.InterfaceName == "" {
			return "", nil
		}

		selectedPort, portFilterOn := bridgeIfFilter(m.topItems.bridges, m.bridgeCursor, m.bridgeDevFilterIdx, m.topMode)

		fdbs := m.st.FDBRecords(m.selectedNsID, true)
		liveFdbs := m.st.FDBRecords(m.selectedNsID, false)
		neighs := m.st.NeighRecords(m.selectedNsID, false)

		neighByMAC := make(map[string][]types.NeighEntry)
		for _, n := range neighs {
			if n.Val.MACAddr != "" && n.Val.IP != "" {
				neighByMAC[n.Val.MACAddr] = append(neighByMAC[n.Val.MACAddr], n.Val)
			}
		}

		vniToVlans := make(map[int]map[int]struct{})
		for _, r := range liveFdbs {
			if r.Val.BridgeName == bridge.Info.InterfaceName && r.Val.VLANID != 0 && r.Val.VxlanID != 0 {
				if vniToVlans[r.Val.VxlanID] == nil {
					vniToVlans[r.Val.VxlanID] = make(map[int]struct{})
				}
				vniToVlans[r.Val.VxlanID][r.Val.VLANID] = struct{}{}
			}
		}

		type fdbEntry struct {
			rec     store.Record[types.FdbEntry]
			hw      net.HardwareAddr
			neighIP string
			unspec  bool
			bum     bool
		}

		final := make([]fdbEntry, 0, len(fdbs))
		for _, r := range fdbs {
			if r.Val.BridgeName != bridge.Info.InterfaceName {
				continue
			}
			if portFilterOn && r.Val.PortName != selectedPort {
				continue
			}
			if !m.detailed && r.Val.VLANID == 0 && r.Val.VxlanID == 0 {
				continue
			}

			hw, err := net.ParseMAC(r.Val.MACAddr)
			if err != nil {
				continue
			}

			if r.Val.VLANID == 0 && r.Val.VxlanID != 0 {
				if vlans := vniToVlans[r.Val.VxlanID]; len(vlans) == 1 {
					for v := range vlans {
						r.Val.VLANID = v
					}
				}
			}

			final = append(final, fdbEntry{
				rec:     r,
				hw:      hw,
				neighIP: resolveNeighIPs(r.Val.MACAddr, neighByMAC[r.Val.MACAddr], r.Val.VLANID, r.Val.VxlanID),
				unspec:  helpers.IsUnspecified(hw),
				bum:     helpers.IsBUM(hw),
			})
		}

		sort.Slice(final, func(i, j int) bool {
			if final[i].rec.Val.VLANID != final[j].rec.Val.VLANID {
				return final[i].rec.Val.VLANID < final[j].rec.Val.VLANID
			}

			if string(final[i].hw) != string(final[j].hw) {
				if final[i].unspec || final[j].unspec {
					return final[j].unspec
				}
				return final[i].rec.Val.MACAddr < final[j].rec.Val.MACAddr
			}
			if final[i].rec.Val.PortID != final[j].rec.Val.PortID {
				return final[i].rec.Val.PortID < final[j].rec.Val.PortID
			}
			return final[i].rec.Val.RemoteVTEP < final[j].rec.Val.RemoteVTEP
		})

		remotePresent := make(map[string]struct{})
		for _, f := range final {
			if f.rec.Val.RemoteVTEP != "" {
				key := fdbFlowKey(f.rec.Val.BridgeName, f.neighIP, f.rec.Val.PortName, f.rec.Val.VLANID, f.rec.Val.VxlanID)
				remotePresent[key] = struct{}{}
			}
		}

		headers := []string{"VLAN", "MAC_ADDR", "NEIGH_IP", "PORT", "VNI", "REMOTE_VTEP"}
		tableRows := make([][]string, 0, len(final))
		styles := make([]lipgloss.Style, 0, len(final))

		for _, f := range final {
			r := f.rec
			if !m.detailed && r.Val.RemoteVTEP == "" {
				key := fdbFlowKey(r.Val.BridgeName, f.neighIP, r.Val.PortName, r.Val.VLANID, r.Val.VxlanID)
				if _, ok := remotePresent[key]; ok {
					continue
				}
			}

			vniStr := ""
			vlanIDStr := ""
			if r.Val.VLANID != 0 {
				vlanIDStr = strconv.Itoa(r.Val.VLANID)
			}
			if r.Val.VxlanID != 0 {
				vniStr = strconv.Itoa(r.Val.VxlanID)

			}

			tableRows = append(tableRows, []string{
				vlanIDStr,
				r.Val.MACAddr,
				f.neighIP,
				r.Val.PortName,
				vniStr,
				r.Val.RemoteVTEP,
			})

			st := ui.FadeStyle(r.Meta, now, base)
			if f.bum {
				st = st.Foreground(ui.ColorFdbBum)
			}
			styles = append(styles, st)
		}

		return renderTableRowsWithSpecs(headers, tableRows, nil, styles, m.width-6)

	case m.topMode == TopVRF && m.botMode == BottomNeigh:
		vrf := pick(m.topItems.vrfs, m.vrfCursor)
		headers := []string{"IP_ADDR", "TYPE", "MAC_ADDR", "IF_NAME", "VNI", "REMOTE_VTEP", "STATE"}
		neighs := m.st.NeighRecords(m.selectedNsID, true)
		tableRows := make([][]string, 0, len(neighs))
		styles := make([]lipgloss.Style, 0, len(neighs))
		fdbs := m.st.FDBRecords(m.selectedNsID, false)
		type fdbInfo struct {
			vni    int
			remote string
		}
		macInfo := make(map[string][]types.FdbEntry, len(fdbs))
		for _, f := range fdbs {
			if f.Val.MACAddr == "" {
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
			family int
		}
		var items []neighRow
		for _, n := range neighs {
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
			items = append(items, neighRow{rec: n, vni: info.vni, remote: info.remote, mcast: mcast, family: helpers.IpFamilyOrderFromAddrStr(n.Val.IP)})
		}

		sort.Slice(items, func(i, j int) bool {
			a, b := items[i], items[j]
			if a.family != b.family {
				return a.family < b.family
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
		header, rows = renderTableRowsWithSpecs(headers, tableRows, nil, styles, m.width-6)
		return header, rows

	case m.topMode == TopVRF && m.botMode == BottomRoute:
		vrf := pick(m.topItems.vrfs, m.vrfCursor)
		headers := []string{"    ", "DESTINATION", "GATEWAY", "DEVICE"}
		specs := []ui.ColumnSpec{
			{},
			{MinWidth: 19},
			{MinWidth: 16},
			{},
		}
		var tableRows [][]string
		var styles []lipgloss.Style

		routes := m.st.RouteRecords(m.selectedNsID, true)
		filterIf, filterOn := vrfIfFilter(vrf.Devs, m.vrfDevFilterIdx, m.topMode)

		type routeItem struct {
			rr     store.Record[types.RouteEntry]
			nhs    []types.Nexthop
			family int
			minDev string
		}
		var items []routeItem

		for _, rr := range routes {
			r := rr.Val
			if !matchesVRFRouteTable(vrf.TableID, r.Table) {
				continue
			}
			if !m.detailed &&
				(r.Type == unix.RTN_MULTICAST ||
					r.Type == unix.RTN_BROADCAST ||
					r.Type == unix.RTN_ANYCAST ||
					(r.Protocol == unix.RTPROT_KERNEL && helpers.IsLinkLocalIP(r.Dst))) {
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
				family: helpers.RouteFamilyOrder(r.Dst, nhs[0].Gw),
				minDev: nhs[0].Dev,
			})
		}

		sort.Slice(items, func(i, j int) bool {
			a, b := items[i], items[j]
			// Always default routes to the top regardless of IP family
			if (a.rr.Val.Prefix == 0 || b.rr.Val.Prefix == 0) && a.rr.Val.Prefix != b.rr.Val.Prefix {
				return a.rr.Val.Prefix < b.rr.Val.Prefix
			}
			if a.family != b.family {
				return a.family < b.family
			}
			if a.rr.Val.Prefix != b.rr.Val.Prefix {
				return a.rr.Val.Prefix < b.rr.Val.Prefix
			}
			if a.rr.Val.IfIndex != b.rr.Val.IfIndex {
				return a.rr.Val.IfIndex < b.rr.Val.IfIndex
			}
			if a.rr.Val.Dst != b.rr.Val.Dst {
				return a.rr.Val.Dst < b.rr.Val.Dst
			}

			return a.minDev < b.minDev
		})

		for _, it := range items {
			r := it.rr.Val
			isECMP := len(it.nhs) > 1
			prefix := routePrefix(r.Type, r.Protocol, isECMP)
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
				tableRows = append(tableRows, []string{
					typeStr,
					dstStr,
					nh.Gw,
					nh.Dev,
				})
				styles = append(styles, st)
			}
		}

		header, rows = renderTableRowsWithSpecs(headers, tableRows, specs, styles, m.width-6)
		return header, rows

	case m.topMode == TopNETNS && m.botMode == BottomProcess:
		ns := pick(m.topItems.netns, m.netnsCursor)
		if ns.DisplayName == "" {
			return "", nil
		}
		if ns.PermissionErr != "" {
			return "", errorRows("Error: " + ns.PermissionErr)
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
		return header, rows

	case m.topMode == TopNETNS && m.botMode == BottomLink:
		ns := pick(m.topItems.netns, m.netnsCursor)
		if ns.DisplayName == "" {
			return "", nil
		}
		if ns.PermissionErr != "" {
			return "", errorRows("Error: " + ns.PermissionErr)
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
		links := m.st.InterfaceRecords(ns.ID, true, true)
		tableRows := make([][]string, 0, len(links))
		styles := make([]lipgloss.Style, 0, len(links))
		for _, link := range links {
			tableRows = append(tableRows, []string{
				link.Val.InterfaceName,
				link.Val.IfType,
				helpers.FormatBps(link.Val.RxBps),
				helpers.FormatBps(link.Val.TxBps),
				fmt.Sprintf("%d", link.Val.RxErrors),
				fmt.Sprintf("%d", link.Val.TxErrors),
			})
			styles = append(styles, ui.FadeStyle(link.Meta, now, base))
		}
		header, rows = renderTableRowsWithSpecs(headers, tableRows, specs, styles, m.width-6)
		return header, rows
	}

	return "", nil
}

func matchesVRFRouteTable(vrfTableID uint32, routeTableID uint32) bool {
	if routeTableID == vrfTableID {
		return true
	}
	return vrfTableID == constants.DefaultVRFTableID && routeTableID == constants.MainRouteTableID
}
