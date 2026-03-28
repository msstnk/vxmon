package store

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/msstnk/vxmon/internal/types"
)

// keys.go generates stable record keys and fingerprints for reconcile().
func recordPrefix(nsID uint64) string {
	return strconv.FormatUint(nsID, 10) + "|"
}

func recordNamespaceID(key string) (uint64, bool) {
	head, _, ok := strings.Cut(key, "|")
	if !ok {
		return 0, false
	}
	nsID, err := strconv.ParseUint(head, 10, 64)
	if err != nil {
		return 0, false
	}
	return nsID, true
}

func neighKey(n types.NeighEntry) string {
	return recordPrefix(n.NamespaceID) + fmt.Sprintf("%d|%d|%d|%s", n.IfIndex, n.VLANID, n.VxlanID, n.IP)
}

func neighFingerprint(n types.NeighEntry) string {
	return fmt.Sprintf("%s|%d|%s|%d", n.MACAddr, n.State, n.InterfaceName, n.MasterID)
}

func fdbKey(f types.FdbEntry) string {
	return recordPrefix(f.NamespaceID) + fmt.Sprintf("%d|%d|%d|%s|%d|%s", f.BridgeID, f.PortID, f.VLANID, f.MACAddr, f.VxlanID, f.RemoteVTEP)
}

func fdbFingerprint(f types.FdbEntry) string {
	return fmt.Sprintf("%s|%s|%d|%s", f.BridgeName, f.PortName, f.State, f.NamespaceName)
}

func routeKey(r types.RouteEntry) string {
	return recordPrefix(r.NamespaceID) + fmt.Sprintf("%d|%d|%s|%d|%d|%d|%d|%d|%s", r.Table, r.IfIndex, r.Dst, r.Prefix, r.Priority, r.Type, r.Protocol, r.Scope, r.Src)
}

func routeFingerprint(r types.RouteEntry) string {
	nh := make([]string, 0, len(r.Nexthops))
	for _, n := range r.Nexthops {
		nh = append(nh, n.Gw+"@"+n.Dev)
	}
	sort.Strings(nh)
	return strings.Join(nh, ";")
}

func processKey(p types.ProcessInfo) string {
	return recordPrefix(p.NamespaceID) + strconv.Itoa(p.PID)
}

func processFingerprint(p types.ProcessInfo) string {
	return p.Exe + "|" + strconv.Itoa(p.PID)
}

func linkKey(l types.NamespaceLinkInfo) string {
	return recordPrefix(l.NamespaceID) + strconv.Itoa(l.IfIndex)
}

func linkFingerprint(l types.NamespaceLinkInfo) string {
	return fmt.Sprintf("%s|%s|%d|%d", l.Name, l.Type, l.RxErrors, l.TxErrors)
}
