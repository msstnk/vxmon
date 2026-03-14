package store

import (
	"fmt"
	"sort"
	"strings"

	"vxmon/internal/types"
)

// keys.go generates stable record keys and fingerprints for reconcile().
// neighKey is called from Store.ReloadNeighAndFDB via reconcile().
func neighKey(n types.NeighEntry) string {
	return fmt.Sprintf("%d|%s", n.InterfaceID, n.IP)
}

// neighFingerprint is called from Store.ReloadNeighAndFDB via reconcile().
func neighFingerprint(n types.NeighEntry) string {
	return fmt.Sprintf("%s|%d", n.HardwareAddr, n.State)
}

// fdbKey is called from Store.ReloadNeighAndFDB via reconcile().
func fdbKey(f types.FdbEntry) string {
	return fmt.Sprintf("%s|%d|%s|%d|%s", f.BridgeName, f.VLANId, f.MacAddr, f.VxlanId, f.RemoteVTEP)
}

// fdbFingerprint is called from Store.ReloadNeighAndFDB via reconcile().
func fdbFingerprint(f types.FdbEntry) string {
	return fmt.Sprintf("%s|%s|%d|%s", f.PortName, f.RemoteVTEP, f.State, f.IPAddr)
}

// routeKey is called from Store.ReloadRoutes via reconcile().
func routeKey(r types.RouteEntry) string {
	return fmt.Sprintf("%d|%s", r.Table, r.Dst)
}

// routeFingerprint is called from Store.ReloadRoutes via reconcile().
func routeFingerprint(r types.RouteEntry) string {
	nh := make([]string, 0, len(r.Nexthops))
	for _, n := range r.Nexthops {
		nh = append(nh, n.Gw+"@"+n.Dev)
	}
	sort.Strings(nh)
	return fmt.Sprintf("%d|%d|%s", r.Type, r.Protocol, strings.Join(nh, ";"))
}
