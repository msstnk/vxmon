package store

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"

	"github.com/msstnk/vxmon/internal/types"
)

// netlink_snapshot.go reads kernel and sysfs state to build typed snapshots.

func getInterfaceInfo(h *netlink.Handle, ns types.NamespaceInfo) ([]types.InterfaceInfo, error) {
	links, err := h.LinkList()
	if err != nil {
		return nil, fmt.Errorf("LinkList failed: %v", err)
	}

	indexToName := make(map[int]string, len(links))
	for _, link := range links {
		indexToName[link.Attrs().Index] = link.Attrs().Name
	}

	allRoutes, _ := h.RouteListFiltered(netlink.FAMILY_ALL, &netlink.Route{Table: unix.RT_TABLE_UNSPEC}, netlink.RT_FILTER_TABLE)
	linkToTable := make(map[int]int, len(allRoutes))
	for _, r := range allRoutes {
		if r.LinkIndex > 0 {
			linkToTable[r.LinkIndex] = r.Table
		}
	}

	results := make([]types.InterfaceInfo, 0, len(links))
	for _, link := range links {
		attrs := link.Attrs()
		linkType := link.Type()
		info := types.InterfaceInfo{
			NamespaceID:      ns.ID,
			NamespaceName:    ns.ShortName,
			NamespaceDisplay: ns.DisplayName,
			NamespaceRoot:    ns.IsRoot,
			InterfaceID:      attrs.Index,
			InterfaceName:    attrs.Name,
			IfType:           linkType,
			ParentID:         attrs.ParentIndex,
			MasterID:         attrs.MasterIndex,
			MACAddr:          attrs.HardwareAddr.String(),
		}

		info.Status = "down"
		if attrs.Flags&unix.IFF_UP != 0 {
			info.Status = "up"
		}
		info.OperState = attrs.OperState.String()

		if parentName, ok := indexToName[attrs.ParentIndex]; ok {
			info.ParentName = parentName
		}
		if masterName, ok := indexToName[attrs.MasterIndex]; ok {
			info.MasterName = masterName
		}

		if linkType == "vxlan" {
			if vxlan, ok := link.(*netlink.Vxlan); ok {
				info.VxlanID = vxlan.VxlanId
			}
		}
		if linkType == "vrf" {
			if vrf, ok := link.(*netlink.Vrf); ok {
				info.TableID = vrf.Table
			}
		}
		if linkType == "bridge" {
			info.STPState = bridgeSTPState(ns, attrs.Name)
		} else if attrs.MasterIndex > 0 {
			info.BridgePortState = bridgePortState(ns, attrs.Name)
		}
		if info.TableID == 0 && attrs.Slave != nil && attrs.Slave.SlaveType() == "vrf" {
			if vrfSlave, ok := attrs.Slave.(*netlink.VrfSlave); ok {
				info.TableID = vrfSlave.Table
			}
		}
		if info.TableID == 0 {
			if tableID, ok := linkToTable[attrs.Index]; ok {
				info.TableID = uint32(tableID)
			}
		}
		results = append(results, info)
	}

	sort.Slice(results, func(i, j int) bool {
		return results[i].InterfaceID < results[j].InterfaceID
	})
	return results, nil
}

func bridgeSTPState(ns types.NamespaceInfo, ifName string) string {
	if !ns.IsRoot {
		return "-"
	}
	path := filepath.Join("/sys/class/net", ifName, "bridge", "stp_state")
	n, ok := readSysfsInt(path)
	if !ok {
		return "-"
	}
	if n == 1 {
		return "enabled"
	}
	return "disabled"
}

func bridgePortState(ns types.NamespaceInfo, ifName string) string {
	if !ns.IsRoot {
		return "-"
	}
	path := filepath.Join("/sys/class/net", ifName, "brport", "state")
	n, ok := readSysfsInt(path)
	if !ok {
		return "-"
	}
	switch n {
	case 0:
		return "disabled"
	case 1:
		return "listening"
	case 2:
		return "learning"
	case 3:
		return "forwarding"
	case 4:
		return "blocking"
	default:
		return strconv.Itoa(n)
	}
}

func readSysfsInt(path string) (int, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		return 0, false
	}
	return n, true
}

func getFdbList(h *netlink.Handle, ns types.NamespaceInfo) ([]types.FdbEntry, error) {
	links, err := h.LinkList()
	if err != nil {
		return nil, err
	}

	vxlanVniMap := make(map[int]int, len(links))
	linkNameMap := make(map[int]string, len(links))
	masterIndexMap := make(map[int]int, len(links))
	linkTypeMap := make(map[int]string, len(links))

	for _, link := range links {
		attrs := link.Attrs()
		linkNameMap[attrs.Index] = attrs.Name
		masterIndexMap[attrs.Index] = attrs.MasterIndex
		linkTypeMap[attrs.Index] = link.Type()
		if vxlan, ok := link.(*netlink.Vxlan); ok {
			vxlanVniMap[vxlan.Index] = vxlan.VxlanId
		}
	}

	fdbs, err := h.NeighList(0, unix.AF_BRIDGE)
	if err != nil {
		return nil, err
	}

	result := make([]types.FdbEntry, 0, len(fdbs))
	for _, fdb := range fdbs {
		portID := fdb.LinkIndex
		masterIdx := masterIndexMap[portID]
		bridgeID := masterIdx
		bridgeName := linkNameMap[masterIdx]
		if bridgeID == 0 && linkTypeMap[portID] == "bridge" {
			bridgeID = portID
			bridgeName = linkNameMap[portID]
		}

		vni := 0
		if val, exists := vxlanVniMap[portID]; exists {
			vni = val
		}
		remoteVTEP := ""
		if fdb.IP != nil {
			remoteVTEP = fdb.IP.String()
		}

		result = append(result, types.FdbEntry{
			NamespaceID:      ns.ID,
			NamespaceName:    ns.ShortName,
			NamespaceDisplay: ns.DisplayName,
			NamespaceRoot:    ns.IsRoot,
			BridgeID:         bridgeID,
			BridgeName:       bridgeName,
			VLANID:           fdb.Vlan,
			MACAddr:          fdb.HardwareAddr.String(),
			State:            fdb.State,
			VxlanID:          vni,
			RemoteVTEP:       remoteVTEP,
			PortID:           portID,
			PortName:         linkNameMap[portID],
		})
	}
	return result, nil
}

func getNeighList(h *netlink.Handle, ns types.NamespaceInfo) ([]types.NeighEntry, error) {
	links, err := h.LinkList()
	if err != nil {
		return nil, err
	}

	linkNameMap := make(map[int]string, len(links))
	for _, link := range links {
		linkNameMap[link.Attrs().Index] = link.Attrs().Name
	}

	neighs, err := h.NeighList(0, unix.AF_UNSPEC)
	if err != nil {
		return nil, err
	}

	result := make([]types.NeighEntry, 0, len(neighs))
	for _, n := range neighs {
		if n.IP == nil {
			continue
		}
		hwAddr := ""
		if n.HardwareAddr != nil {
			hwAddr = n.HardwareAddr.String()
		}
		result = append(result, types.NeighEntry{
			NamespaceID:      ns.ID,
			NamespaceName:    ns.ShortName,
			NamespaceDisplay: ns.DisplayName,
			NamespaceRoot:    ns.IsRoot,
			IP:               n.IP.String(),
			MACAddr:          hwAddr,
			State:            n.State,
			InterfaceID:      n.LinkIndex,
			InterfaceName:    linkNameMap[n.LinkIndex],
			VLANID:           n.Vlan,
			VxlanID:          n.VNI,
			MasterID:         n.MasterIndex,
		})
	}

	sort.Slice(result, func(i, j int) bool {
		if result[i].InterfaceID != result[j].InterfaceID {
			return result[i].InterfaceID < result[j].InterfaceID
		}
		return result[i].IP < result[j].IP
	})
	return result, nil
}

func getRouteList(h *netlink.Handle, ns types.NamespaceInfo) ([]types.RouteEntry, error) {
	filter := &netlink.Route{Table: unix.RT_TABLE_UNSPEC}
	filterMask := netlink.RT_FILTER_TABLE

	routesV4, err := h.RouteListFiltered(netlink.FAMILY_V4, filter, filterMask)
	if err != nil {
		return nil, fmt.Errorf("failed to get IPv4 routes: %v", err)
	}
	routesV6, err := h.RouteListFiltered(netlink.FAMILY_V6, filter, filterMask)
	if err != nil {
		return nil, fmt.Errorf("failed to get IPv6 routes: %v", err)
	}

	links, _ := h.LinkList()
	linkNameMap := make(map[int]string, len(links))
	for _, l := range links {
		linkNameMap[l.Attrs().Index] = l.Attrs().Name
	}

	var res []types.RouteEntry
	processRoutes := func(routeList []netlink.Route, defaultDst string) {
		for _, r := range routeList {
			dst := defaultDst
			if r.Dst != nil {
				dst = r.Dst.String()
			}

			var nexthops []types.Nexthop
			if len(r.MultiPath) > 0 {
				for _, mp := range r.MultiPath {
					gw := routeGatewayString(mp.Gw, mp.Via)
					nexthops = append(nexthops, types.Nexthop{Gw: gw, Dev: linkNameMap[mp.LinkIndex]})
				}
			} else {
				gw := routeGatewayString(r.Gw, r.Via)
				nexthops = append(nexthops, types.Nexthop{Gw: gw, Dev: linkNameMap[r.LinkIndex]})
			}

			res = append(res, types.RouteEntry{
				NamespaceID:      ns.ID,
				NamespaceName:    ns.ShortName,
				NamespaceDisplay: ns.DisplayName,
				NamespaceRoot:    ns.IsRoot,
				Dst:              dst,
				Src:              routeIPString(r.Src),
				Table:            uint32(r.Table),
				Priority:         r.Priority,
				Scope:            int(r.Scope),
				Type:             r.Type,
				Protocol:         int(r.Protocol),
				Nexthops:         nexthops,
			})
		}
	}

	processRoutes(routesV4, "0.0.0.0/0")
	processRoutes(routesV6, "::/0")
	return res, nil
}

func routeGatewayString(gw interface{}, via interface{}) string {
	if gw != nil {
		gwStr := fmt.Sprint(gw)
		if gwStr != "" && gwStr != "<nil>" {
			return gwStr
		}
	}

	if via == nil {
		return ""
	}
	viaStr := fmt.Sprint(via)
	if idx := strings.Index(viaStr, "Address: "); idx != -1 {
		return strings.TrimSpace(viaStr[idx+len("Address: "):])
	}
	return viaStr
}

func routeIPString(ip net.IP) string {
	if len(ip) == 0 {
		return ""
	}
	return ip.String()
}
