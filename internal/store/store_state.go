package store

import (
	"time"

	"github.com/msstnk/vxmon/internal/types"
)

type inventory struct {
	namespaces      []types.NamespaceInfo
	namespacesByID  map[uint64]*namespaceState
	selfNamespaceID uint64
	ifaces          []types.InterfaceInfo
	topology        map[uint64]topologyState
}

type topologyState struct {
	ifaces []types.InterfaceInfo
	neigh  map[string]types.NeighEntry
	fdb    map[string]types.FdbEntry
	routes map[string]types.RouteEntry
}

type runtimeState struct {
	processes    map[uint64][]types.ProcessInfo
	links        map[uint64][]types.NamespaceLinkInfo
	processPrev  map[string]processSample
	linkHistory  map[string]*linkSampleRing
	prevTotalCPU uint64
	lastRuntime  time.Time
}

type recordState struct {
	neighMeta      map[string]Meta
	fdbMeta        map[string]Meta
	routeMeta      map[string]Meta
	processMeta    map[string]Meta
	linkMeta       map[string]Meta
	processRecords map[string]types.ProcessInfo
	linkRecords    map[string]types.NamespaceLinkInfo
}

type referenceState struct {
	vrfUsedIfByNS        map[uint64]map[int]struct{}
	vrfUsedIfCompactByNS map[uint64]map[int]struct{}
	vrfUsedIfCompactHold map[uint64]map[int]time.Time
	bridgePortUsedByNS   map[uint64]map[int]struct{}
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
