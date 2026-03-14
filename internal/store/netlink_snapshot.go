package store

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"

	"vxmon/internal/types"
)

// netlink_snapshot.go reads kernel and sysfs state to build typed snapshots.
// getInterfaceInfo is called from Store.ReloadAll and Store.ReloadInterfaces.
func getInterfaceInfo() ([]types.InterfaceInfo, error) {
	links, err := netlink.LinkList()
	if err != nil {
		return nil, fmt.Errorf("netlink.LinkList() failed: %v", err)
	}

	indexToName := make(map[int]string, len(links))
	for _, link := range links {
		indexToName[link.Attrs().Index] = link.Attrs().Name
	}

	allRoutes, _ := netlink.RouteList(nil, netlink.FAMILY_ALL)
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
			IfName:   attrs.Name,
			IfType:   linkType,
			ParentID: attrs.ParentIndex,
			MasterID: attrs.MasterIndex,
			HWAddr:   attrs.HardwareAddr.String(),
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
				info.VxlanId = vxlan.VxlanId
			}
		}
		if linkType == "vrf" {
			if vrf, ok := link.(*netlink.Vrf); ok {
				info.TableID = vrf.Table
			}
		}
		if linkType == "bridge" {
			info.STPState = bridgeSTPState(attrs.Name)
		} else if attrs.MasterIndex > 0 {
			info.BridgePortState = bridgePortState(attrs.Name)
		}

		if info.TableID == 0 && attrs.Slave != nil && attrs.Slave.SlaveType() == "vrf" {
			if vrfSlave, ok := attrs.Slave.(*netlink.VrfSlave); ok {
				info.TableID = vrfSlave.Table
			}
		}

		if info.TableID == 0 {
			if t, ok := linkToTable[attrs.Index]; ok {
				info.TableID = uint32(t)
			}
		}

		results = append(results, info)
	}
	return results, nil
}

// bridgeSTPState is called from getInterfaceInfo for bridge links.
func bridgeSTPState(ifName string) string {
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

// bridgePortState is called from getInterfaceInfo for bridge slave links.
func bridgePortState(ifName string) string {
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

// readSysfsInt is called by bridgeSTPState and bridgePortState.
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

// getFdbList is called from Store.ReloadNeighAndFDB.
func getFdbList() ([]types.FdbEntry, error) {
	links, err := netlink.LinkList()
	if err != nil {
		return nil, err
	}

	vxlanVniMap := make(map[int]int, len(links))
	linkNameMap := make(map[int]string, len(links))
	masterIndexMap := make(map[int]int, len(links))

	for _, link := range links {
		attrs := link.Attrs()
		linkNameMap[attrs.Index] = attrs.Name
		masterIndexMap[attrs.Index] = attrs.MasterIndex
		if vxlan, ok := link.(*netlink.Vxlan); ok {
			vxlanVniMap[vxlan.Index] = vxlan.VxlanId
		}
	}

	fdbs, err := netlink.NeighList(0, unix.AF_BRIDGE)
	if err != nil {
		return nil, err
	}

	result := make([]types.FdbEntry, 0, len(fdbs))
	for _, fdb := range fdbs {
		portID := fdb.LinkIndex
		portName := linkNameMap[portID]
		masterIdx := masterIndexMap[portID]
		bridgeID := masterIdx
		bridgeName := linkNameMap[masterIdx]

		vni := 0
		if val, exists := vxlanVniMap[portID]; exists {
			vni = val
		}
		remoteVTEP := ""
		if fdb.IP != nil {
			remoteVTEP = fdb.IP.String()
		}

		result = append(result, types.FdbEntry{
			BridgeID:   bridgeID,
			BridgeName: bridgeName,
			VLANId:     fdb.Vlan,
			MacAddr:    fdb.HardwareAddr.String(),
			State:      fdb.State,
			VxlanId:    vni,
			RemoteVTEP: remoteVTEP,
			PortID:     portID,
			PortName:   portName,
		})
	}

	return result, nil
}

// getNeighList is called from Store.ReloadNeighAndFDB.
func getNeighList() ([]types.NeighEntry, error) {
	links, err := netlink.LinkList()
	if err != nil {
		return nil, err
	}

	linkNameMap := make(map[int]string, len(links))
	for _, link := range links {
		linkNameMap[link.Attrs().Index] = link.Attrs().Name
	}

	neighs, err := netlink.NeighList(0, unix.AF_UNSPEC)
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
			IP:           n.IP.String(),
			HardwareAddr: hwAddr,
			State:        n.State,
			InterfaceID:  n.LinkIndex,
			Interface:    linkNameMap[n.LinkIndex],
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

// getRouteList is called from Store.ReloadRoutes.
func getRouteList() ([]types.RouteEntry, error) {
	filter := &netlink.Route{Table: unix.RT_TABLE_UNSPEC}
	filterMask := netlink.RT_FILTER_TABLE

	routesV4, err := netlink.RouteListFiltered(netlink.FAMILY_V4, filter, filterMask)
	if err != nil {
		return nil, fmt.Errorf("failed to get IPv4 routes: %v", err)
	}
	routesV6, err := netlink.RouteListFiltered(netlink.FAMILY_V6, filter, filterMask)
	if err != nil {
		return nil, fmt.Errorf("failed to get IPv6 routes: %v", err)
	}

	links, _ := netlink.LinkList()
	linkNameMap := make(map[int]string)
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
					gw := ""
					if mp.Gw != nil {
						gw = mp.Gw.String()
					} else if mp.Via != nil {
						gwStr := mp.Via.String()
						if idx := strings.Index(gwStr, "Address: "); idx != -1 {
							gw = strings.TrimSpace(gwStr[idx+len("Address: "):])
						} else {
							gw = gwStr
						}
					}
					devName := linkNameMap[mp.LinkIndex]
					nexthops = append(nexthops, types.Nexthop{Gw: gw, Dev: devName})
				}
			} else {
				gw := ""
				if r.Gw != nil {
					gw = r.Gw.String()
				} else if r.Via != nil {
					gwStr := r.Via.String()
					if idx := strings.Index(gwStr, "Address: "); idx != -1 {
						gw = strings.TrimSpace(gwStr[idx+len("Address: "):])
					} else {
						gw = gwStr
					}
				}
				devName := linkNameMap[r.LinkIndex]
				nexthops = append(nexthops, types.Nexthop{Gw: gw, Dev: devName})
			}

			res = append(res, types.RouteEntry{
				Dst:      dst,
				Table:    uint32(r.Table),
				Type:     r.Type,
				Protocol: int(r.Protocol),
				Nexthops: nexthops,
			})
		}
	}

	processRoutes(routesV4, "0.0.0.0/0")
	processRoutes(routesV6, "::/0")
	return res, nil
}
