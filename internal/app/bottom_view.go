package app

import (
	"fmt"
	"sort"
	"strings"

	"net/netip"
	"vxmon/internal/store"
	"vxmon/internal/types"
	"vxmon/internal/ui"

	"github.com/charmbracelet/lipgloss"
	"golang.org/x/sys/unix"
)

// bottom_view.go builds the bottom pane tables for FDB, Neigh, and Route modes.
// ipFamilyOrderFromAddrStr is called by buildBottom when sorting neighbor rows.
func ipFamilyOrderFromAddrStr(s string) int {
	if strings.Contains(s, ":") {
		return 1
	}
	return 0
}

// isMulticastIPStr is called by buildBottom and vrfUsedIfSet.
func isMulticastIPStr(s string) bool {
	addr, err := netip.ParseAddr(strings.TrimSpace(s))
	if err != nil {
		return strings.HasPrefix(s, "ff") || strings.HasPrefix(s, "FF") || strings.HasPrefix(s, "224.")
	}
	return addr.IsMulticast()
}

// routeFamilyOrder is called by buildBottom when sorting route rows by address family.
func routeFamilyOrder(dst string, gw string) int {
	if strings.Contains(dst, ":") {
		return 1
	}
	if strings.Contains(dst, ".") {
		return 0
	}
	if strings.Contains(gw, ":") {
		return 1
	}
	return 0
}

// buildBottom is called from refreshViewWithTopViewport to render the active bottom table.
func (m Model) buildBottom() (header string, rows []ui.ListRow, cursorIdx int) {
	base := lipgloss.NewStyle()
	base = base.Foreground(lipgloss.Color(ui.ColorBaseColor))
	now := unixNanoNow(m.fadeClock)

	switch {
	case m.topMode == TopBridge && m.botMode == BottomFDB:
		bridge := m.selectedBridge()
		if bridge == "" {
			return "", nil, 0
		}
		selectedPort, portFilterOn := m.selectedBridgeIfFilter()

		fdbs := m.st.FDBRecords(true)

		neighs := m.st.NeighRecords(false)
		macToIP := make(map[string]string, len(neighs))
		for _, n := range neighs {
			if n.Val.HardwareAddr != "" && n.Val.IP != "" {
				macToIP[n.Val.HardwareAddr] = n.Val.IP
			}
		}

		bridgeFdb := make([]store.Record[types.FdbEntry], 0, len(fdbs))
		for _, r := range fdbs {
			if r.Val.BridgeName == bridge {
				if portFilterOn && r.Val.PortName != selectedPort {
					continue
				}
				if !m.detailed && r.Val.VLANId == 0 && r.Val.VxlanId == 0 {
					continue
				}
				bridgeFdb = append(bridgeFdb, r)
			}
		}
		vniToVlan := map[int]int{}
		for _, r := range bridgeFdb {
			if r.Val.VLANId != 0 && r.Val.VxlanId != 0 {
				vniToVlan[r.Val.VxlanId] = r.Val.VLANId
			}
		}
		grouped := make(map[string][]store.Record[types.FdbEntry], len(bridgeFdb))
		for _, r := range bridgeFdb {
			f := r.Val
			if f.VLANId == 0 && f.VxlanId != 0 {
				if vlan, ok := vniToVlan[f.VxlanId]; ok {
					f.VLANId = vlan
				}
			}
			key := ""
			if f.MacAddr == "00:00:00:00:00:00" {
				key = fmt.Sprintf("%d-%s-%d-%s", f.VLANId, f.MacAddr, f.VxlanId, f.RemoteVTEP)
			} else {
				key = fmt.Sprintf("%d-%s-%d", f.VLANId, f.MacAddr, f.VxlanId)
			}

			r.Val = f
			grouped[key] = append(grouped[key], r)
		}
		final := make([]store.Record[types.FdbEntry], 0, len(grouped))
		for _, grp := range grouped {
			merged := grp[0]
			for _, rr := range grp {
				if merged.Val.RemoteVTEP == "" && rr.Val.RemoteVTEP != "" {
					merged.Val.RemoteVTEP = rr.Val.RemoteVTEP
				}
			}
			final = append(final, merged)
		}
		sort.Slice(final, func(i, j int) bool {
			// Sort by VLAN, MAC, Port, then Remote VTEP
			if final[i].Val.VLANId != final[j].Val.VLANId {
				return final[i].Val.VLANId < final[j].Val.VLANId
			}

			if final[i].Val.MacAddr != final[j].Val.MacAddr {
				if final[i].Val.MacAddr != final[j].Val.MacAddr {
					// Entries with BUM addresses at the bottom of the list
					if final[i].Val.MacAddr == "00:00:00:00:00:00" {
						return false
					}
					if final[j].Val.MacAddr == "00:00:00:00:00:00" {
						return true
					}
					return final[i].Val.MacAddr < final[j].Val.MacAddr
				}
			}
			if final[i].Val.PortName != final[j].Val.PortName {
				return final[i].Val.PortName < final[j].Val.PortName
			}
			return final[i].Val.RemoteVTEP < final[j].Val.RemoteVTEP
		})

		headers := []string{"VLAN", "MAC_ADDR", "NEIGH_IP", "PORT", "VNI", "REMOTE_VTEP"}
		tableRows := make([][]string, 0, len(final))
		styles := make([]lipgloss.Style, 0, len(final))
		for _, r := range final {
			neighIP := macToIP[r.Val.MacAddr]
			vxlan_vni := ""
			if r.Val.VxlanId != 0 {
				vxlan_vni = fmt.Sprintf("%d", r.Val.VxlanId)
			}

			tableRows = append(tableRows, []string{
				fmt.Sprintf("%d", r.Val.VLANId),
				r.Val.MacAddr,
				neighIP,
				r.Val.PortName,
				vxlan_vni,
				r.Val.RemoteVTEP,
			})
			st := ui.FadeStyle(r.Meta, now, base)
			if r.Val.MacAddr == "00:00:00:00:00:00" {
				st = st.Foreground(ui.ColorFdbBum)
			}
			styles = append(styles, st)
		}
		header, lines := ui.FormatTable(headers, tableRows, m.width-6)
		for i, ln := range lines {
			rows = append(rows, ui.ListRow{Text: ln, Style: styles[i]})
		}
		return header, rows, m.botCursor

	case m.topMode == TopVRF && m.botMode == BottomNeigh:
		vrf := m.selectedVRF()
		headers := []string{"IP_ADDR", "TYPE", "MAC_ADDR", "IF_NAME", "VNI", "REMOTE_VTEP", "STATE"}
		var tableRows [][]string
		var styles []lipgloss.Style

		neighs := m.st.NeighRecords(true)
		fdbs := m.st.FDBRecords(false)

		type fdbInfo struct {
			vni    int
			remote string
		}
		macInfo := make(map[string]fdbInfo, len(fdbs))
		for _, f := range fdbs {
			if f.Val.MacAddr == "" {
				continue
			}
			if _, ok := macInfo[f.Val.MacAddr]; !ok {
				macInfo[f.Val.MacAddr] = fdbInfo{vni: f.Val.VxlanId, remote: f.Val.RemoteVTEP}
			}
		}

		filterIf, filterOn := m.selectedVrfIfFilter()

		ifSet := make(map[string]bool, len(vrf.Devs))
		for _, d := range vrf.Devs {
			ifSet[d.IfName] = true
		}
		type neighRow struct {
			rec    store.Record[types.NeighEntry]
			vni    int
			remote string
			mcast  bool
		}
		var items []neighRow

		for _, n := range neighs {
			if !ifSet[n.Val.Interface] {
				continue
			}
			if filterOn && n.Val.Interface != filterIf {
				continue
			}

			mcast := isMulticastIPStr(n.Val.IP)
			if mcast && !m.detailed {
				continue
			}
			info := macInfo[n.Val.HardwareAddr]
			items = append(items, neighRow{rec: n, vni: info.vni, remote: info.remote, mcast: mcast})
		}

		sort.Slice(items, func(i, j int) bool {
			a, b := items[i], items[j]
			fa := ipFamilyOrderFromAddrStr(a.rec.Val.IP)
			fb := ipFamilyOrderFromAddrStr(b.rec.Val.IP)
			if fa != fb {
				return fa < fb
			}
			if a.rec.Val.HardwareAddr != b.rec.Val.HardwareAddr {
				return a.rec.Val.HardwareAddr < b.rec.Val.HardwareAddr
			}
			if a.rec.Val.Interface != b.rec.Val.Interface {
				return a.rec.Val.Interface < b.rec.Val.Interface
			}
			return a.rec.Val.IP < b.rec.Val.IP
		})

		for _, it := range items {
			typeStr := "UCAST"
			st := ui.FadeStyle(it.rec.Meta, now, base)
			if it.mcast {
				typeStr = "MCAST"
				st = st.Foreground(ui.ColorRouteMcast)
			}
			tableRows = append(tableRows, []string{
				it.rec.Val.IP,
				typeStr,
				it.rec.Val.HardwareAddr,
				it.rec.Val.Interface,
				fmt.Sprintf("%d", it.vni),
				it.remote,
				fmt.Sprintf("%d", it.rec.Val.State),
			})
			styles = append(styles, st)
		}
		header, lines := ui.FormatTable(headers, tableRows, m.width-6)
		for i, ln := range lines {
			rows = append(rows, ui.ListRow{Text: ln, Style: styles[i]})
		}
		return header, rows, m.botCursor

	case m.topMode == TopVRF && m.botMode == BottomRoute:
		vrf := m.selectedVRF()
		headers := []string{"    ", "DESTINATION", "GATEWAY", "DEVICE"}
		var tableRows [][]string
		var styles []lipgloss.Style

		routes := m.st.RouteRecords(true)
		filterIf, filterOn := m.selectedVrfIfFilter()

		type routeItem struct {
			rr     store.Record[types.RouteEntry]
			nhs    []types.Nexthop
			fam    int
			minDev string
		}
		var items []routeItem

		for _, rr := range routes {
			r := rr.Val
			if r.Table != vrf.TableID {
				if vrf.TableID == defaultVRFTableID && r.Table == mainRouteTableID {
					// In Default VRF, show routes from Main Table (255) as well
				} else {
					continue
				}

			}
			if (!m.detailed && (r.Type == unix.RTN_MULTICAST || r.Type == unix.RTN_BROADCAST)) || (r.Type == unix.RTN_ANYCAST) {
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
			minDev := nhs[0].Dev
			fam := routeFamilyOrder(r.Dst, nhs[0].Gw)
			items = append(items, routeItem{rr: rr, nhs: nhs, fam: fam, minDev: minDev})
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

		header, lines := ui.FormatTable(headers, tableRows, m.width-6)
		for i, ln := range lines {
			rows = append(rows, ui.ListRow{Text: ln, Style: styles[i]})
		}
		return header, rows, m.botCursor
	}

	return "", nil, 0
}
