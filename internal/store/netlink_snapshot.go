package store

import (
	"errors"
	"fmt"
	"net"
	"sort"
	"strings"
	"time"

	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netlink/nl"
	"github.com/vishvananda/netns"
	"golang.org/x/sys/unix"

	"github.com/msstnk/vxmon/internal/constants"
	"github.com/msstnk/vxmon/internal/debuglog"
	"github.com/msstnk/vxmon/internal/types"
)

// netlink_snapshot.go reads kernel state to build typed snapshots.
type interfaceInfoRaw struct {
	links       []netlink.Link
	stpByIndex  map[int]int
	portByIndex map[int]int
	routes      []netlink.Route
}

type fdbRaw struct {
	fdbs []netlink.Neigh
}

type neighRaw struct {
	neighs []netlink.Neigh
}

type routeRaw struct {
	routesV4 []netlink.Route
	routesV6 []netlink.Route
}

type linkListRaw struct {
	links []netlink.Link
}

type linkSample struct {
	rxBytes uint64
	txBytes uint64
}

type linkSampleRing struct {
	buffer []linkSample
	epochs []time.Time
	pos    int
	count  int
}

func newLinkSampleRing(size int) *linkSampleRing {
	if size < 2 {
		size = 2
	}
	return &linkSampleRing{
		buffer: make([]linkSample, size),
		epochs: make([]time.Time, size),
	}
}

func (r *linkSampleRing) push(sample linkSample, at time.Time) {
	r.buffer[r.pos] = sample
	r.epochs[r.pos] = at
	r.pos = (r.pos + 1) % len(r.buffer)
	if r.count < len(r.buffer) {
		r.count++
	}
}

func (r *linkSampleRing) newest() (linkSample, time.Time, bool) {
	if r.count == 0 {
		return linkSample{}, time.Time{}, false
	}
	idx := (r.pos - 1 + len(r.buffer)) % len(r.buffer)
	return r.buffer[idx], r.epochs[idx], true
}

func (r *linkSampleRing) averageBps(maxWindow time.Duration) (uint64, uint64) {
	if r.count < 2 {
		return 0, 0
	}
	newestSample, newestAt, ok := r.newest()
	if !ok {
		return 0, 0
	}

	var oldestSample linkSample
	var oldestAt time.Time
	found := false
	for i := 1; i < r.count; i++ {
		idx := (r.pos - 1 - i + len(r.buffer)) % len(r.buffer)
		at := r.epochs[idx]
		if at.IsZero() || !newestAt.After(at) {
			continue
		}
		elapsed := newestAt.Sub(at)
		if elapsed > maxWindow {
			break
		}
		oldestSample = r.buffer[idx]
		oldestAt = at
		found = true
	}
	if !found {
		return 0, 0
	}

	elapsed := newestAt.Sub(oldestAt).Seconds()
	if elapsed <= 0 {
		return 0, 0
	}

	var rxBps uint64
	if newestSample.rxBytes >= oldestSample.rxBytes {
		rxBps = uint64(float64(newestSample.rxBytes-oldestSample.rxBytes) * 8.0 / elapsed)
	}
	var txBps uint64
	if newestSample.txBytes >= oldestSample.txBytes {
		txBps = uint64(float64(newestSample.txBytes-oldestSample.txBytes) * 8.0 / elapsed)
	}
	return rxBps, txBps
}

func getInterfaceList(ns types.NamespaceInfo, nsHandle int, now time.Time, history map[string]*linkSampleRing) (interfaceInfoRaw, error) {
	links, stpByIndex, portByIndex, err := getLinksAndBridgeStates(ns, nsHandle)
	if err != nil {
		return interfaceInfoRaw{}, fmt.Errorf("getLinksAndBridgeStates failed: %v", err)
	}

	if history != nil {
		for _, link := range links {
			attrs := link.Attrs()
			if attrs == nil || attrs.Statistics == nil {
				continue
			}
			key := linkSampleKey(ns.ID, attrs.Index)
			ring := history[key]
			if ring == nil {
				ring = newLinkSampleRing(constants.LinkRateHistoryDepth)
				history[key] = ring
			}
			ring.push(linkSample{rxBytes: attrs.Statistics.RxBytes, txBytes: attrs.Statistics.TxBytes}, now)
		}
	}

	return interfaceInfoRaw{
		links:       links,
		stpByIndex:  stpByIndex,
		portByIndex: portByIndex,
	}, nil
}

func parseInterfaceList(raw interfaceInfoRaw, ns types.NamespaceInfo, history map[string]*linkSampleRing) ([]types.InterfaceInfo, []types.NamespaceLinkInfo) {
	indexToName := make(map[int]string, len(raw.links))
	for _, link := range raw.links {
		indexToName[link.Attrs().Index] = link.Attrs().Name
	}

	linkToTable := make(map[int]int, len(raw.routes))
	for _, r := range raw.routes {
		if r.LinkIndex > 0 {
			linkToTable[r.LinkIndex] = r.Table
		}
	}

	results := make([]types.InterfaceInfo, 0, len(raw.links))
	links := make([]types.NamespaceLinkInfo, 0, len(raw.links))
	for _, link := range raw.links {
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
			STPState:         -1,
			BridgePortState:  -1,
		}

		info.Status = "down"
		if attrs.Flags&unix.IFF_UP != 0 {
			info.Status = "up"
		}
		if attrs.OperState == 0 { // IF_OPER_UNKNOWN
			info.OperState = "-"
		} else {
			info.OperState = attrs.OperState.String()
		}
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
			if stp, found := raw.stpByIndex[attrs.Index]; found {
				info.STPState = stp
			}
		} else if attrs.MasterIndex > 0 {
			if st, found := raw.portByIndex[attrs.Index]; found {
				info.BridgePortState = st
			}
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

		if attrs.Statistics == nil {
			continue
		}
		var rxBps uint64
		var txBps uint64
		if history != nil {
			if ring := history[linkSampleKey(ns.ID, attrs.Index)]; ring != nil {
				rxBps, txBps = ring.averageBps(constants.LinkRateMaxSampleInterval)
			}
		}
		links = append(links, types.NamespaceLinkInfo{
			NamespaceID: ns.ID,
			InterfaceID: attrs.Index,
			Name:        attrs.Name,
			Type:        linkType,
			RxBps:       rxBps,
			TxBps:       txBps,
			RxErrors:    attrs.Statistics.RxErrors,
			TxErrors:    attrs.Statistics.TxErrors,
		})
	}
	sort.Slice(results, func(i, j int) bool { return results[i].InterfaceID < results[j].InterfaceID })
	sort.Slice(links, func(i, j int) bool { return links[i].InterfaceID < links[j].InterfaceID })
	return results, links
}

func getLinkListRaw(h *netlink.Handle) (linkListRaw, error) {
	links, err := h.LinkList()
	if err != nil {
		return linkListRaw{}, err
	}
	return linkListRaw{links: links}, nil
}

func getFdbList(h *netlink.Handle, _ types.NamespaceInfo) (fdbRaw, error) {
	fdbs, err := h.NeighList(0, unix.AF_BRIDGE)
	if err != nil {
		return fdbRaw{}, err
	}
	return fdbRaw{fdbs: fdbs}, nil
}

func parseFdbList(raw fdbRaw, linksRaw linkListRaw, ns types.NamespaceInfo) []types.FdbEntry {
	vxlanVniMap := make(map[int]int, len(linksRaw.links))
	linkNameMap := make(map[int]string, len(linksRaw.links))
	masterIndexMap := make(map[int]int, len(linksRaw.links))
	linkTypeMap := make(map[int]string, len(linksRaw.links))

	for _, link := range linksRaw.links {
		attrs := link.Attrs()
		linkNameMap[attrs.Index] = attrs.Name
		masterIndexMap[attrs.Index] = attrs.MasterIndex
		linkTypeMap[attrs.Index] = link.Type()
		if vxlan, ok := link.(*netlink.Vxlan); ok {
			vxlanVniMap[vxlan.Index] = vxlan.VxlanId
		}
	}

	result := make([]types.FdbEntry, 0, len(raw.fdbs))
	for _, fdb := range raw.fdbs {
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
	return result
}

func getNeighList(h *netlink.Handle, _ types.NamespaceInfo) (neighRaw, error) {
	neighs, err := h.NeighList(0, unix.AF_UNSPEC)
	if err != nil {
		return neighRaw{}, err
	}
	return neighRaw{neighs: neighs}, nil
}

func parseNeighList(raw neighRaw, linksRaw linkListRaw, ns types.NamespaceInfo) []types.NeighEntry {
	linkNameMap := make(map[int]string, len(linksRaw.links))
	for _, link := range linksRaw.links {
		linkNameMap[link.Attrs().Index] = link.Attrs().Name
	}

	result := make([]types.NeighEntry, 0, len(raw.neighs))
	for _, n := range raw.neighs {
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
	return result
}

func getRouteList(h *netlink.Handle, _ types.NamespaceInfo) (routeRaw, error) {
	filter := &netlink.Route{Table: unix.RT_TABLE_UNSPEC}
	filterMask := netlink.RT_FILTER_TABLE

	routesV4, err := h.RouteListFiltered(netlink.FAMILY_V4, filter, filterMask)
	if err != nil {
		return routeRaw{}, fmt.Errorf("failed to get IPv4 routes: %v", err)
	}
	routesV6, err := h.RouteListFiltered(netlink.FAMILY_V6, filter, filterMask)
	if err != nil {
		return routeRaw{}, fmt.Errorf("failed to get IPv6 routes: %v", err)
	}

	return routeRaw{routesV4: routesV4, routesV6: routesV6}, nil
}

func parseRouteList(raw routeRaw, linksRaw linkListRaw, ns types.NamespaceInfo) []types.RouteEntry {
	linkNameMap := make(map[int]string, len(linksRaw.links))
	for _, l := range linksRaw.links {
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

	processRoutes(raw.routesV4, "0.0.0.0/0")
	processRoutes(raw.routesV6, "::/0")
	return res
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

func getLinksAndBridgeStates(ns types.NamespaceInfo, nsHandle int) ([]netlink.Link, map[int]int, map[int]int, error) {
	stpByIndex := map[int]int{}
	portByIndex := map[int]int{}
	var links []netlink.Link

	var sh *nl.SocketHandle
	if ns.IsCurrent {
		// Using netlink.Subscribe to get a socket without CAP_SYS_ADMIN in the root namespace.
		sock, err := nl.Subscribe(unix.NETLINK_ROUTE)
		if err != nil {
			debuglog.Tracef("store.getLinksAndBridgeStates nl.Subscribe failed: %v", err)
			return nil, nil, nil, err
		}
		sh = &nl.SocketHandle{Socket: sock}
	} else {
		sock, err := nl.GetNetlinkSocketAt(netns.NsHandle(nsHandle), netns.None(), unix.NETLINK_ROUTE)
		if err != nil {
			debuglog.Tracef("store.getLinksAndBridgeStates nl.GetNetlinkSocketAt failed: %v", err)
			return nil, nil, nil, err
		}
		sh = &nl.SocketHandle{Socket: sock}
	}
	defer sh.Close()

	dump := func(family uint8) error {
		req := nl.NewNetlinkRequest(unix.RTM_GETLINK, unix.NLM_F_DUMP)
		req.Sockets = map[int]*nl.SocketHandle{unix.NETLINK_ROUTE: sh}
		req.AddData(nl.NewIfInfomsg(int(family)))

		msgs, err := req.Execute(unix.NETLINK_ROUTE, unix.RTM_NEWLINK)
		if err != nil && !errors.Is(err, netlink.ErrDumpInterrupted) {
			debuglog.Tracef("store.getLinksAndBridgeStates Execute failed for family=%d: %v", family, err)
			return err
		}

		for _, m := range msgs {
			if family == unix.AF_UNSPEC {
				if link, err := netlink.LinkDeserialize(nil, m); err == nil && link != nil {
					links = append(links, link)
				}
			}

			ans := nl.DeserializeIfInfomsg(m)
			attrs, err := nl.ParseRouteAttrAsMap(m[ans.Len():])
			if err != nil {
				continue
			}

			index := int(ans.Index)
			if attr, ok := attrs[unix.IFLA_LINKINFO]; ok {
				if stp, ok := parseBridgeStpState(attr.Value); ok {
					stpByIndex[index] = stp
				}
			}

			protKey := uint16(unix.IFLA_PROTINFO | unix.NLA_F_NESTED)
			if attr, ok := attrs[protKey]; ok {
				if port, ok := parseBridgePortState(attr.Value); ok {
					portByIndex[index] = port
				}
			}
		}
		return nil
	}

	if err := dump(unix.AF_UNSPEC); err != nil {
		return nil, nil, nil, err
	}

	if len(portByIndex) == 0 {
		_ = dump(unix.AF_BRIDGE)
	}

	return links, stpByIndex, portByIndex, nil
}

func parseBridgeStpState(b []byte) (int, bool) {
	attrs, err := nl.ParseRouteAttrAsMap(b)
	if err != nil || attrs == nil {
		debuglog.Tracef("store.parseBridgeStpState ParseRouteAttrAsMap failed: %v", err)
		return 0, false
	}
	// IFLA_INFO_DATA -> IFLA_BR_STP_STATE
	if infoAttr, ok := attrs[unix.IFLA_INFO_DATA]; ok {
		infoItems, _ := nl.ParseRouteAttrAsMap(infoAttr.Value)
		if item, ok := infoItems[nl.IFLA_BR_STP_STATE]; ok {
			return decodeInt(item.Value)
		}
	}
	return 0, false
}

func parseBridgePortState(b []byte) (int, bool) {
	attrs, err := nl.ParseRouteAttrAsMap(b)
	if err != nil || attrs == nil {
		debuglog.Tracef("store.parseBridgePortState ParseRouteAttrAsMap failed: %v", err)
		return 0, false
	}
	// IFLA_BRPORT_STATE
	if attr, ok := attrs[nl.IFLA_BRPORT_STATE]; ok {
		return decodeInt(attr.Value)
	}
	return 0, false
}

func decodeInt(b []byte) (int, bool) {
	if len(b) >= 4 {
		return int(nl.NativeEndian().Uint32(b[:4])), true
	}
	if len(b) >= 1 {
		return int(b[0]), true
	}
	return 0, false
}

func linkSampleKey(nsID uint64, ifIndex int) string {
	return fmt.Sprintf("%d|%d", nsID, ifIndex)
}
