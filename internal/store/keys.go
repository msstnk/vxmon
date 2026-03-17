package store

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/msstnk/vxmon/internal/types"
)

// keys.go generates stable record keys and fingerprints for reconcile().
func neighKey(n types.NeighEntry) string {
	return strconv.FormatUint(n.NamespaceID, 10) + "|" + fmt.Sprintf("%d|%d|%d|%s", n.InterfaceID, n.VLANID, n.VxlanID, n.IP)
}

func neighFingerprint(n types.NeighEntry) string {
	return fmt.Sprintf("%s|%d|%s|%d", n.MACAddr, n.State, n.InterfaceName, n.MasterID)
}

func fdbKey(f types.FdbEntry) string {
	return strconv.FormatUint(f.NamespaceID, 10) + "|" + fmt.Sprintf("%d|%d|%d|%s|%d|%s", f.BridgeID, f.PortID, f.VLANID, f.MACAddr, f.VxlanID, f.RemoteVTEP)
}

func fdbFingerprint(f types.FdbEntry) string {
	return fmt.Sprintf("%s|%s|%d|%s", f.BridgeName, f.PortName, f.State, f.NamespaceName)
}

func routeKey(r types.RouteEntry) string {
	return strconv.FormatUint(r.NamespaceID, 10) + "|" + fmt.Sprintf("%d|%s|%d|%d|%d|%d|%s", r.Table, r.Dst, r.Priority, r.Type, r.Protocol, r.Scope, r.Src)
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
	return strconv.FormatUint(p.NamespaceID, 10) + "|" + strconv.Itoa(p.PID)
}

func processFingerprint(p types.ProcessInfo) string {
	return p.Exe + "|" + p.User
}

func linkKey(l types.NamespaceLinkInfo) string {
	return strconv.FormatUint(l.NamespaceID, 10) + "|" + strconv.Itoa(l.InterfaceID)
}

func linkFingerprint(l types.NamespaceLinkInfo) string {
	return fmt.Sprintf("%s|%s|%d|%d", l.Name, l.Type, l.RxErrors, l.TxErrors)
}
