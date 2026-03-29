package store

import (
	"time"

	"github.com/msstnk/vxmon/internal/types"
	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netns"
)

type inventory struct {
	namespaces      []types.NamespaceInfo
	namespaceState  map[uint64]*namespaceState
	selfNamespaceID uint64
	topology        map[uint64]topologyState
}

type topologyState struct {
	ifaces map[string]types.InterfaceInfo
	neigh  map[string]types.NeighEntry
	fdb    map[string]types.FdbEntry
	routes map[string]types.RouteEntry
}

type runtimeState struct {
	processes    map[uint64][]types.ProcessInfo
	processPrev  map[string]processSample
	prevTotalCPU uint64
	lastRuntime  time.Time
}

type recordState struct {
	neighMeta      map[string]Meta
	fdbMeta        map[string]Meta
	routeMeta      map[string]Meta
	processMeta    map[string]Meta
	ifaceMeta      map[string]Meta
	processRecords map[string]types.ProcessInfo
}

type referenceState struct {
	vrfUsedIfByNS        map[uint64]map[int]struct{}
	vrfUsedIfCompactByNS map[uint64]map[int]struct{}
	vrfUsedIfCompactHold map[uint64]map[int]time.Time
	bridgePortUsedByNS   map[uint64]map[int]struct{}
}
type namespaceState struct {
	info       types.NamespaceInfo
	mountPoint string
	handle     *netlink.Handle
	nsHandle   netns.NsHandle
}

type nsReloadEntry struct {
	refreshedAt time.Time
	dirty       nlReloadMask
	dueAt       time.Time
	pending     bool
}

type reloadState struct {
	ns map[uint64]*nsReloadEntry
}
