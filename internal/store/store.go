package store

import (
	"sort"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	"github.com/msstnk/vxmon/internal/constants"
	"github.com/msstnk/vxmon/internal/debuglog"
	"github.com/msstnk/vxmon/internal/helpers"
	"github.com/msstnk/vxmon/internal/types"
)

// store.go owns in-memory snapshots plus transient fade metadata for rendering.
type RecordState int

const (
	StateNone RecordState = iota
	StateAdded
	StateUpdated
	StateRemoved
)

type Meta struct {
	State       RecordState
	ChangedAt   time.Time
	Fingerprint string
}

type Record[T any] struct {
	Key  string
	Val  T
	Meta Meta
}

type Store struct {
	selfNamespaceID uint64

	namespaces     []types.NamespaceInfo
	namespacesByID map[uint64]*namespaceState

	ifaces     []types.InterfaceInfo
	ifacesByNS map[uint64][]types.InterfaceInfo

	neigh     map[string]types.NeighEntry
	neighMeta map[string]Meta

	fdb     map[string]types.FdbEntry
	fdbMeta map[string]Meta

	routes    map[string]types.RouteEntry
	routeMeta map[string]Meta

	processes map[uint64][]types.ProcessInfo
	links     map[uint64][]types.NamespaceLinkInfo

	processRecords map[string]types.ProcessInfo
	processMeta    map[string]Meta
	linkRecords    map[string]types.NamespaceLinkInfo
	linkMeta       map[string]Meta

	processPrev  map[string]processSample
	linkPrev     map[string]linkSample
	prevTotalCPU uint64

	vrfUsedIfByNS        map[uint64]map[int]struct{}
	vrfUsedIfCompactByNS map[uint64]map[int]struct{}
	vrfUsedIfCompactHold map[uint64]map[int]time.Time
	bridgePortUsedByNS   map[uint64]map[int]struct{}

	lastRuntime time.Time
}

func New() *Store {
	selfNamespaceID, err := readNamespaceID("/proc/self/ns/net")
	if err != nil {
		debuglog.Errorf("store.New read self netns failed: %v", err)
	}

	return &Store{
		selfNamespaceID:      selfNamespaceID,
		namespacesByID:       map[uint64]*namespaceState{},
		neigh:                map[string]types.NeighEntry{},
		neighMeta:            map[string]Meta{},
		fdb:                  map[string]types.FdbEntry{},
		fdbMeta:              map[string]Meta{},
		routes:               map[string]types.RouteEntry{},
		routeMeta:            map[string]Meta{},
		ifacesByNS:           map[uint64][]types.InterfaceInfo{},
		processes:            map[uint64][]types.ProcessInfo{},
		links:                map[uint64][]types.NamespaceLinkInfo{},
		processRecords:       map[string]types.ProcessInfo{},
		processMeta:          map[string]Meta{},
		linkRecords:          map[string]types.NamespaceLinkInfo{},
		linkMeta:             map[string]Meta{},
		processPrev:          map[string]processSample{},
		linkPrev:             map[string]linkSample{},
		vrfUsedIfByNS:        map[uint64]map[int]struct{}{},
		vrfUsedIfCompactByNS: map[uint64]map[int]struct{}{},
		vrfUsedIfCompactHold: map[uint64]map[int]time.Time{},
		bridgePortUsedByNS:   map[uint64]map[int]struct{}{},
	}
}

func (s *Store) SelfNamespaceID() uint64 {
	return s.selfNamespaceID
}

func (s *Store) ReloadAll(now time.Time) error {
	debuglog.Tracef("store.ReloadAll")
	if err := s.syncNamespaces(); err != nil {
		return err
	}
	s.reloadInterfaces()
	s.reloadNeighAndFDB(now)
	s.reloadRoutes(now)
	return s.reloadRuntime(now)
}

func (s *Store) ReloadInterfaces() error {
	debuglog.Tracef("store.ReloadInterfaces")
	if err := s.syncNamespaces(); err != nil {
		return err
	}
	s.reloadInterfaces()
	return nil
}

func (s *Store) ReloadNamespaceInterfaces(namespaceID uint64) error {
	debuglog.Tracef("store.ReloadNamespaceInterfaces namespace=%d", namespaceID)
	state := s.namespaceState(namespaceID)
	if state == nil {
		debuglog.Tracef("store.ReloadNamespaceInterfaces namespace=%d skipped: namespace not synced yet", namespaceID)
		return nil
	}
	s.reloadNamespaceInterfaces(state)
	s.rebuildInterfaces()
	return nil
}

func (s *Store) ReloadNeighAndFDB(now time.Time) error {
	debuglog.Tracef("store.ReloadNeighAndFDB")
	if err := s.syncNamespaces(); err != nil {
		return err
	}
	s.reloadNeighAndFDB(now)
	return nil
}

func (s *Store) ReloadNamespaceNeighAndFDB(namespaceID uint64, now time.Time) error {
	debuglog.Tracef("store.ReloadNamespaceNeighAndFDB namespace=%d", namespaceID)
	state := s.namespaceState(namespaceID)
	if state == nil {
		return nil
	}
	s.reloadNamespaceNeighAndFDB(state, now)
	s.rebuildReferenceMaps()
	return nil
}

func (s *Store) ReloadRoutes(now time.Time) error {
	debuglog.Tracef("store.ReloadRoutes")
	if err := s.syncNamespaces(); err != nil {
		return err
	}
	s.reloadRoutes(now)
	return nil
}

func (s *Store) ReloadNamespaceRoutes(namespaceID uint64, now time.Time) error {
	debuglog.Tracef("store.ReloadNamespaceRoutes namespace=%d", namespaceID)
	state := s.namespaceState(namespaceID)
	if state == nil {
		return nil
	}
	s.reloadNamespaceRoutes(state, now)
	s.rebuildReferenceMaps()
	return nil
}

func (s *Store) ReloadRuntime(now time.Time) error {
	debuglog.Tracef("store.ReloadRuntime")
	return s.reloadRuntime(now)
}

func (s *Store) ReloadNamespaceLinks(namespaceID uint64, now time.Time) error {
	debuglog.Tracef("store.ReloadNamespaceLinks namespace=%d", namespaceID)
	state := s.namespaceState(namespaceID)
	if state == nil {
		debuglog.Tracef("store.ReloadNamespaceLinks namespace=%d skipped: namespace not synced yet", namespaceID)
		return nil
	}
	s.reloadNamespaceLinks(state, now)
	return nil
}

func (s *Store) RuntimeRefreshDue(now time.Time) bool {
	return s.lastRuntime.IsZero() || now.Sub(s.lastRuntime) >= constants.RuntimeRefreshInterval
}

func (s *Store) reloadInterfaces() {
	for _, state := range s.namespaceStates() {
		s.reloadNamespaceInterfaces(state)
	}
	s.rebuildInterfaces()
}

func (s *Store) reloadNeighAndFDB(now time.Time) {
	for _, state := range s.namespaceStates() {
		s.reloadNamespaceNeighAndFDB(state, now)
	}
	s.rebuildReferenceMaps()
}

func (s *Store) reloadRoutes(now time.Time) {
	for _, state := range s.namespaceStates() {
		s.reloadNamespaceRoutes(state, now)
	}
	s.rebuildReferenceMaps()
}

func (s *Store) Advance(now time.Time) (changed bool, active bool) {
	var metaChanged bool
	var metaActive bool
	refChanged := false

	metaChanged, metaActive = advanceMeta(s.neighMeta, s.neigh, now)
	changed = changed || metaChanged
	active = active || metaActive
	refChanged = refChanged || metaChanged

	metaChanged, metaActive = advanceMeta(s.fdbMeta, s.fdb, now)
	changed = changed || metaChanged
	active = active || metaActive
	refChanged = refChanged || metaChanged

	metaChanged, metaActive = advanceMeta(s.routeMeta, s.routes, now)
	changed = changed || metaChanged
	active = active || metaActive
	refChanged = refChanged || metaChanged

	metaChanged, metaActive = advanceMeta(s.processMeta, s.processRecords, now)
	changed = changed || metaChanged
	active = active || metaActive

	metaChanged, metaActive = advanceMeta(s.linkMeta, s.linkRecords, now)
	changed = changed || metaChanged
	active = active || metaActive

	if refChanged {
		s.rebuildReferenceMaps()
	}

	return changed, active
}

func (s *Store) HasActiveFades() bool {
	return hasActiveMeta(s.neighMeta) ||
		hasActiveMeta(s.fdbMeta) ||
		hasActiveMeta(s.routeMeta) ||
		hasActiveMeta(s.processMeta) ||
		hasActiveMeta(s.linkMeta)
}

func advanceMeta[T any](metaMap map[string]Meta, records map[string]T, now time.Time) (changed bool, active bool) {
	for key, meta := range metaMap {
		if meta.State == StateNone {
			continue
		}
		if now.Sub(meta.ChangedAt) < constants.FadeDuration {
			active = true
			continue
		}
		if meta.State == StateRemoved {
			delete(metaMap, key)
			delete(records, key)
			changed = true
			continue
		}
		meta.State = StateNone
		metaMap[key] = meta
		changed = true
	}
	return changed, active
}

func hasActiveMeta(metaMap map[string]Meta) bool {
	for _, meta := range metaMap {
		if meta.State != StateNone {
			return true
		}
	}
	return false
}

func (s *Store) reloadNamespaceInterfaces(state *namespaceState) {
	if state == nil {
		return
	}
	if state.handle == nil {
		s.ifacesByNS[state.info.ID] = nil
		return
	}

	items, err := getInterfaceInfo(state.handle, state.info, int(state.nsHandle))
	if err != nil {
		debuglog.Errorf("store.reloadNamespaceInterfaces namespace=%d failed: %v", state.info.ID, err)
		return
	}
	s.ifacesByNS[state.info.ID] = items
}

func (s *Store) rebuildInterfaces() {
	total := 0
	for _, ns := range s.namespaces {
		total += len(s.ifacesByNS[ns.ID])
	}
	out := make([]types.InterfaceInfo, 0, total)
	for _, ns := range s.namespaces {
		out = append(out, s.ifacesByNS[ns.ID]...)
	}
	s.ifaces = out
	s.rebuildReferenceMaps()
}

func (s *Store) reloadNamespaceNeighAndFDB(state *namespaceState, now time.Time) {
	if state == nil {
		return
	}

	var neighList []types.NeighEntry
	var fdbList []types.FdbEntry
	if state.handle != nil {
		items, err := getNeighList(state.handle, state.info)
		if err != nil {
			debuglog.Errorf("store.reloadNamespaceNeighAndFDB neigh namespace=%d failed: %v", state.info.ID, err)
		} else {
			neighList = items
		}

		itemsFDB, err := getFdbList(state.handle, state.info)
		if err != nil {
			debuglog.Errorf("store.reloadNamespaceNeighAndFDB fdb namespace=%d failed: %v", state.info.ID, err)
		} else {
			fdbList = itemsFDB
		}
	}

	s.neigh, s.neighMeta = reconcileNamespace(s.neigh, s.neighMeta, neighList, neighKey, neighFingerprint, state.info.ID, now)
	s.fdb, s.fdbMeta = reconcileNamespace(s.fdb, s.fdbMeta, fdbList, fdbKey, fdbFingerprint, state.info.ID, now)
}

func (s *Store) reloadNamespaceRoutes(state *namespaceState, now time.Time) {
	if state == nil {
		return
	}

	var routeList []types.RouteEntry
	if state.handle != nil {
		items, err := getRouteList(state.handle, state.info)
		if err != nil {
			debuglog.Errorf("store.reloadNamespaceRoutes namespace=%d failed: %v", state.info.ID, err)
		} else {
			routeList = items
		}
	}
	s.routes, s.routeMeta = reconcileNamespace(s.routes, s.routeMeta, routeList, routeKey, routeFingerprint, state.info.ID, now)
}

func (s *Store) namespaceState(namespaceID uint64) *namespaceState {
	return s.namespacesByID[namespaceID]
}

func (s *Store) Namespaces() []types.NamespaceInfo {
	out := make([]types.NamespaceInfo, len(s.namespaces))
	copy(out, s.namespaces)
	return out
}

func (s *Store) NamespaceInfo(id uint64) (types.NamespaceInfo, bool) {
	state := s.namespacesByID[id]
	if state == nil {
		return types.NamespaceInfo{}, false
	}
	return state.info, true
}

func (s *Store) Interfaces() []types.InterfaceInfo {
	out := make([]types.InterfaceInfo, len(s.ifaces))
	copy(out, s.ifaces)
	return out
}

func (s *Store) IsVRFInterfaceReferenced(namespaceID uint64, ifIndex int, detailed bool) bool {
	if detailed {
		_, ok := s.vrfUsedIfByNS[namespaceID][ifIndex]
		return ok
	}
	if _, ok := s.vrfUsedIfCompactByNS[namespaceID][ifIndex]; ok {
		return true
	}
	expiry := s.vrfUsedIfCompactHold[namespaceID][ifIndex]
	if expiry.IsZero() {
		return false
	}
	return time.Now().Before(expiry)
}

func (s *Store) IsBridgePortReferenced(namespaceID uint64, ifIndex int) bool {
	_, ok := s.bridgePortUsedByNS[namespaceID][ifIndex]
	return ok
}

func (s *Store) rebuildReferenceMaps() {
	now := time.Now()
	vrfUsed := make(map[uint64]map[int]struct{}, len(s.ifacesByNS))
	vrfUsedCompact := make(map[uint64]map[int]struct{}, len(s.ifacesByNS))
	vrfUsedCompactHold := make(map[uint64]map[int]time.Time, len(s.ifacesByNS))
	bridgePortUsed := make(map[uint64]map[int]struct{}, len(s.ifacesByNS))

	ifIndexByName := make(map[uint64]map[string]int, len(s.ifacesByNS))
	for nsID, ifaces := range s.ifacesByNS {
		nameMap := make(map[string]int, len(ifaces))
		for _, iface := range ifaces {
			nameMap[iface.InterfaceName] = iface.InterfaceID
		}
		ifIndexByName[nsID] = nameMap
	}

	addRef := func(dst map[uint64]map[int]struct{}, nsID uint64, ifIndex int) {
		if ifIndex <= 0 {
			return
		}
		if dst[nsID] == nil {
			dst[nsID] = make(map[int]struct{})
		}
		dst[nsID][ifIndex] = struct{}{}
	}

	addCompactRef := func(nsID uint64, ifIndex int) {
		if ifIndex <= 0 {
			return
		}
		addRef(vrfUsedCompact, nsID, ifIndex)
	}

	for _, neigh := range s.neigh {
		addRef(vrfUsed, neigh.NamespaceID, neigh.InterfaceID)
		if !helpers.IsMulticastIP(neigh.IP) {
			addCompactRef(neigh.NamespaceID, neigh.InterfaceID)
		}
	}

	for _, route := range s.routes {
		skipCompact := route.Type == unix.RTN_ANYCAST ||
			route.Type == unix.RTN_MULTICAST ||
			route.Type == unix.RTN_BROADCAST
		nameMap := ifIndexByName[route.NamespaceID]
		for _, nh := range route.Nexthops {
			ifIndex := nameMap[nh.Dev]
			addRef(vrfUsed, route.NamespaceID, ifIndex)
			if !skipCompact {
				addCompactRef(route.NamespaceID, ifIndex)
			}
		}
	}

	for nsID, held := range s.vrfUsedIfCompactHold {
		for ifIndex, expiry := range held {
			if !expiry.After(now) {
				continue
			}
			if _, ok := vrfUsedCompact[nsID][ifIndex]; ok {
				continue
			}
			if vrfUsedCompactHold[nsID] == nil {
				vrfUsedCompactHold[nsID] = make(map[int]time.Time)
			}
			vrfUsedCompactHold[nsID][ifIndex] = expiry
		}
	}

	for nsID, prev := range s.vrfUsedIfCompactByNS {
		for ifIndex := range prev {
			if _, ok := vrfUsedCompact[nsID][ifIndex]; ok {
				continue
			}
			if vrfUsedCompactHold[nsID] == nil {
				vrfUsedCompactHold[nsID] = make(map[int]time.Time)
			}
			if expiry, ok := vrfUsedCompactHold[nsID][ifIndex]; ok && expiry.After(now) {
				continue
			}
			vrfUsedCompactHold[nsID][ifIndex] = now.Add(constants.VrfCompactReferenceHold)
		}
	}

	for _, fdb := range s.fdb {
		addRef(bridgePortUsed, fdb.NamespaceID, fdb.PortID)
	}

	s.vrfUsedIfByNS = vrfUsed
	s.vrfUsedIfCompactByNS = vrfUsedCompact
	s.vrfUsedIfCompactHold = vrfUsedCompactHold
	s.bridgePortUsedByNS = bridgePortUsed
}

func collectRecords[T any](
	records map[string]T,
	metaMap map[string]Meta,
	includeRemoved bool,
	match func(string, T) bool,
) []Record[T] {
	out := make([]Record[T], 0, len(records))
	for key, val := range records {
		if match != nil && !match(key, val) {
			continue
		}
		meta := metaMap[key]
		if !includeRemoved && meta.State == StateRemoved {
			continue
		}
		out = append(out, Record[T]{Key: key, Val: val, Meta: meta})
	}
	return out
}

func (s *Store) NeighRecords(includeRemoved bool) []Record[types.NeighEntry] {
	out := collectRecords(s.neigh, s.neighMeta, includeRemoved, nil)
	sort.Slice(out, func(i, j int) bool {
		if out[i].Val.NamespaceRoot != out[j].Val.NamespaceRoot {
			return out[i].Val.NamespaceRoot
		}
		if out[i].Val.NamespaceName != out[j].Val.NamespaceName {
			return out[i].Val.NamespaceName < out[j].Val.NamespaceName
		}
		if out[i].Val.InterfaceID != out[j].Val.InterfaceID {
			return out[i].Val.InterfaceID < out[j].Val.InterfaceID
		}
		return out[i].Val.IP < out[j].Val.IP
	})
	return out
}

func (s *Store) FDBRecords(includeRemoved bool) []Record[types.FdbEntry] {
	out := collectRecords(s.fdb, s.fdbMeta, includeRemoved, nil)
	sort.Slice(out, func(i, j int) bool {
		if out[i].Val.NamespaceRoot != out[j].Val.NamespaceRoot {
			return out[i].Val.NamespaceRoot
		}
		if out[i].Val.NamespaceName != out[j].Val.NamespaceName {
			return out[i].Val.NamespaceName < out[j].Val.NamespaceName
		}
		if out[i].Val.BridgeID != out[j].Val.BridgeID {
			return out[i].Val.BridgeID < out[j].Val.BridgeID
		}
		if out[i].Val.VLANID != out[j].Val.VLANID {
			return out[i].Val.VLANID < out[j].Val.VLANID
		}
		if out[i].Val.PortID != out[j].Val.PortID {
			return out[i].Val.PortID < out[j].Val.PortID
		}
		return out[i].Val.RemoteVTEP < out[j].Val.RemoteVTEP
	})
	return out
}

func (s *Store) RouteRecords(includeRemoved bool) []Record[types.RouteEntry] {
	out := collectRecords(s.routes, s.routeMeta, includeRemoved, nil)
	sort.Slice(out, func(i, j int) bool {
		if out[i].Val.NamespaceRoot != out[j].Val.NamespaceRoot {
			return out[i].Val.NamespaceRoot
		}
		if out[i].Val.NamespaceName != out[j].Val.NamespaceName {
			return out[i].Val.NamespaceName < out[j].Val.NamespaceName
		}
		if out[i].Val.Table != out[j].Val.Table {
			return out[i].Val.Table < out[j].Val.Table
		}
		return out[i].Val.Dst < out[j].Val.Dst
	})
	return out
}

func (s *Store) NamespaceProcesses(nsID uint64) []types.ProcessInfo {
	rows := s.processes[nsID]
	out := make([]types.ProcessInfo, len(rows))
	copy(out, rows)
	return out
}

func (s *Store) NamespaceProcessRecords(nsID uint64, includeRemoved bool) []Record[types.ProcessInfo] {
	prefix := strconv.FormatUint(nsID, 10) + "|"
	out := collectRecords(s.processRecords, s.processMeta, includeRemoved, func(key string, _ types.ProcessInfo) bool {
		return strings.HasPrefix(key, prefix)
	})
	sort.Slice(out, func(i, j int) bool {
		if out[i].Val.LoadPct != out[j].Val.LoadPct {
			return out[i].Val.LoadPct > out[j].Val.LoadPct
		}
		return out[i].Val.PID < out[j].Val.PID
	})
	return out
}

func (s *Store) NamespaceLinks(nsID uint64) []types.NamespaceLinkInfo {
	rows := s.links[nsID]
	out := make([]types.NamespaceLinkInfo, len(rows))
	copy(out, rows)
	return out
}

func (s *Store) NamespaceLinkRecords(nsID uint64, includeRemoved bool) []Record[types.NamespaceLinkInfo] {
	prefix := strconv.FormatUint(nsID, 10) + "|"
	out := collectRecords(s.linkRecords, s.linkMeta, includeRemoved, func(key string, _ types.NamespaceLinkInfo) bool {
		return strings.HasPrefix(key, prefix)
	})
	sort.Slice(out, func(i, j int) bool {
		ai := out[i].Val.RxBps + out[i].Val.TxBps
		aj := out[j].Val.RxBps + out[j].Val.TxBps
		if ai != aj {
			return ai > aj
		}
		return out[i].Val.InterfaceID < out[j].Val.InterfaceID
	})
	return out
}
