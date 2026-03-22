package store

import (
	"context"
	"sort"
	"strconv"
	"strings"
	"sync"
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
	mu sync.RWMutex

	eventCh chan storeEvent

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
	linkHistory  map[string]*linkSampleRing
	prevTotalCPU uint64

	vrfUsedIfByNS        map[uint64]map[int]struct{}
	vrfUsedIfCompactByNS map[uint64]map[int]struct{}
	vrfUsedIfCompactHold map[uint64]map[int]time.Time
	bridgePortUsedByNS   map[uint64]map[int]struct{}

	lastRuntime  time.Time
	metaRevision uint64

	nsReloadRefreshedAt map[uint64]time.Time
	nsReloadDirty       map[uint64]nlReloadMask
	nsReloadDueAt       map[uint64]time.Time
	nsReloadPending     map[uint64]bool
}

func New() *Store {
	selfNamespaceID, err := readNamespaceID("/proc/self/ns/net")
	if err != nil {
		debuglog.Errorf("store.New read self netns failed: %v", err)
	}

	return &Store{
		eventCh:              make(chan storeEvent, 1024),
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
		linkHistory:          map[string]*linkSampleRing{},
		vrfUsedIfByNS:        map[uint64]map[int]struct{}{},
		vrfUsedIfCompactByNS: map[uint64]map[int]struct{}{},
		vrfUsedIfCompactHold: map[uint64]map[int]time.Time{},
		bridgePortUsedByNS:   map[uint64]map[int]struct{}{},
		nsReloadRefreshedAt:  map[uint64]time.Time{},
		nsReloadDirty:        map[uint64]nlReloadMask{},
		nsReloadDueAt:        map[uint64]time.Time{},
		nsReloadPending:      map[uint64]bool{},
	}
}

func (s *Store) SelfNamespaceID() uint64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.selfNamespaceID
}

func (s *Store) Run(ctx context.Context, send func(any)) {
	s.runLoop(ctx, send)
}

func (s *Store) RequestFetchLatest(at time.Time) {
	s.enqueueEvent(storeEvent{kind: storeEventFetchLatest, at: at})
}

func (s *Store) ReloadAll(now time.Time) error {
	debuglog.Tracef("store.ReloadAll")
	s.reloadRuntime(now)
	s.reloadInterfaces(now)
	s.reloadNeighAndFDB(now)
	s.reloadRoutes(now)
	return nil
}

func (s *Store) ReloadInterfaces() error {
	debuglog.Tracef("store.ReloadInterfaces")
	if err := s.syncNamespaces(); err != nil {
		return err
	}
	s.reloadInterfaces(time.Now())
	return nil
}

func (s *Store) ReloadNamespaceInterfaces(namespaceID uint64, now time.Time) error {
	debuglog.Tracef("store.ReloadNamespaceInterfaces namespace=%d", namespaceID)
	state := s.namespaceState(namespaceID)
	if state == nil {
		debuglog.Tracef("store.ReloadNamespaceInterfaces namespace=%d skipped: namespace not synced yet", namespaceID)
		return nil
	}
	s.reloadNamespaceInterfaces(state, now)
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
	s.reloadNamespaceInterfaces(state, now)
	s.rebuildInterfaces()
	return nil
}

func (s *Store) RuntimeRefreshDue(now time.Time) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.lastRuntime.IsZero() || now.Sub(s.lastRuntime) >= constants.RuntimeRefreshInterval
}

func (s *Store) metaRevisionLocked() uint64 {
	return s.metaRevision
}

func (s *Store) bumpMetaRevisionLocked() {
	s.metaRevision++
}

func (s *Store) reloadInterfaces(now time.Time) {
	for _, state := range s.namespaceStates() {
		s.reloadNamespaceInterfaces(state, now)
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
	changed = false
	active = false

	metaChanged, metaActive = advanceMeta(s.neighMeta, s.neigh, now)
	if metaChanged {
		debuglog.Tracef("store.Advance neigh meta changed")
	}
	changed = changed || metaChanged
	active = active || metaActive
	refChanged = refChanged || metaChanged

	metaChanged, metaActive = advanceMeta(s.fdbMeta, s.fdb, now)
	if metaChanged {
		debuglog.Tracef("store.Advance fdb meta changed")
	}
	changed = changed || metaChanged
	active = active || metaActive
	refChanged = refChanged || metaChanged

	metaChanged, metaActive = advanceMeta(s.routeMeta, s.routes, now)
	if metaChanged {
		debuglog.Tracef("store.Advance route meta changed")
	}
	changed = changed || metaChanged
	active = active || metaActive
	refChanged = refChanged || metaChanged

	// Do not track process meta changes as they are too noisy
	metaChanged, metaActive = advanceMeta(s.processMeta, s.processRecords, now)
	// if metaChanged {
	// 	debuglog.Tracef("store.Advance process meta changed")
	// }
	// changed = changed || metaChanged
	// active = active || metaActive

	metaChanged, metaActive = advanceMeta(s.linkMeta, s.linkRecords, now)
	if metaChanged {
		debuglog.Tracef("store.Advance link meta changed")
	}
	changed = changed || metaChanged
	active = active || metaActive

	if refChanged {
		s.rebuildReferenceMaps()
	}
	if changed {
		s.bumpMetaRevisionLocked()
	}

	return changed, active
}

func (s *Store) HasActiveFades() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return hasActiveMeta(s.neighMeta) ||
		hasActiveMeta(s.fdbMeta) ||
		hasActiveMeta(s.routeMeta) ||
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

func (s *Store) reloadNamespaceInterfaces(state *namespaceState, now time.Time) {
	if state == nil {
		return
	}
	if state.handle == nil {
		s.ifacesByNS[state.info.ID] = nil
		s.links[state.info.ID] = nil
		var changed bool
		s.linkRecords, s.linkMeta, changed = reconcileNamespace(s.linkRecords, s.linkMeta, nil, linkKey, linkFingerprint, state.info.ID, now)
		if changed {
			s.bumpMetaRevisionLocked()
		}
		return
	}

	raw, err := getInterfaceList(state.info, int(state.nsHandle), now, s.linkHistory)
	if err != nil {
		debuglog.Errorf("store.reloadNamespaceInterfaces namespace=%d failed: %v", state.info.ID, err)
		return
	}
	ifaces, links := parseInterfaceList(raw, state.info, s.linkHistory)
	s.ifacesByNS[state.info.ID] = ifaces
	s.links[state.info.ID] = links
	var changed bool
	s.linkRecords, s.linkMeta, changed = reconcileNamespace(s.linkRecords, s.linkMeta, links, linkKey, linkFingerprint, state.info.ID, now)
	if changed {
		debuglog.Tracef("store.reloadNamespaceInterfaces namespace=%d link meta changed", state.info.ID)
		s.bumpMetaRevisionLocked()
	}
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
	var linksRaw linkListRaw
	if state.handle != nil {
		rawLinks, err := getLinkListRaw(state.handle)
		if err != nil {
			debuglog.Errorf("store.reloadNamespaceNeighAndFDB links namespace=%d failed: %v", state.info.ID, err)
		} else {
			linksRaw = rawLinks
		}

		rawNeigh, err := getNeighList(state.handle, state.info)
		if err != nil {
			debuglog.Errorf("store.reloadNamespaceNeighAndFDB neigh namespace=%d failed: %v", state.info.ID, err)
		} else {
			neighList = parseNeighList(rawNeigh, linksRaw, state.info)
		}

		rawFDB, err := getFdbList(state.handle, state.info)
		if err != nil {
			debuglog.Errorf("store.reloadNamespaceNeighAndFDB fdb namespace=%d failed: %v", state.info.ID, err)
		} else {
			fdbList = parseFdbList(rawFDB, linksRaw, state.info)
		}
	}

	var changed bool
	s.neigh, s.neighMeta, changed = reconcileNamespace(s.neigh, s.neighMeta, neighList, neighKey, neighFingerprint, state.info.ID, now)
	if changed {
		debuglog.Tracef("store.reloadNamespaceNeighAndFDB namespace=%d neigh meta changed", state.info.ID)
		s.bumpMetaRevisionLocked()
	}
	s.fdb, s.fdbMeta, changed = reconcileNamespace(s.fdb, s.fdbMeta, fdbList, fdbKey, fdbFingerprint, state.info.ID, now)
	if changed {
		debuglog.Tracef("store.reloadNamespaceNeighAndFDB namespace=%d fdb meta changed", state.info.ID)
		s.bumpMetaRevisionLocked()
	}
}

func (s *Store) reloadNamespaceRoutes(state *namespaceState, now time.Time) {
	if state == nil {
		return
	}

	var routeList []types.RouteEntry
	var linksRaw linkListRaw
	if state.handle != nil {
		rawLinks, err := getLinkListRaw(state.handle)
		if err != nil {
			debuglog.Errorf("store.reloadNamespaceRoutes links namespace=%d failed: %v", state.info.ID, err)
		} else {
			linksRaw = rawLinks
		}

		raw, err := getRouteList(state.handle, state.info)
		if err != nil {
			debuglog.Errorf("store.reloadNamespaceRoutes namespace=%d failed: %v", state.info.ID, err)
		} else {
			routeList = parseRouteList(raw, linksRaw, state.info)
		}
	}
	var changed bool
	s.routes, s.routeMeta, changed = reconcileNamespace(s.routes, s.routeMeta, routeList, routeKey, routeFingerprint, state.info.ID, now)
	if changed {
		debuglog.Tracef("store.reloadNamespaceRoutes namespace=%d route meta changed", state.info.ID)
		s.bumpMetaRevisionLocked()
	}
}

func (s *Store) namespaceState(namespaceID uint64) *namespaceState {
	return s.namespacesByID[namespaceID]
}

func (s *Store) Namespaces() []types.NamespaceInfo {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]types.NamespaceInfo, len(s.namespaces))
	copy(out, s.namespaces)
	return out
}

func (s *Store) NamespaceInfo(id uint64) (types.NamespaceInfo, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	state := s.namespacesByID[id]
	if state == nil {
		return types.NamespaceInfo{}, false
	}
	return state.info, true
}

func (s *Store) Interfaces() []types.InterfaceInfo {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]types.InterfaceInfo, len(s.ifaces))
	copy(out, s.ifaces)
	return out
}

func (s *Store) IsVRFInterfaceReferenced(namespaceID uint64, ifIndex int, detailed bool) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
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
	s.mu.RLock()
	defer s.mu.RUnlock()
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
	s.mu.RLock()
	defer s.mu.RUnlock()
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
	s.mu.RLock()
	defer s.mu.RUnlock()
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
	s.mu.RLock()
	defer s.mu.RUnlock()
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
	s.mu.RLock()
	defer s.mu.RUnlock()
	rows := s.processes[nsID]
	out := make([]types.ProcessInfo, len(rows))
	copy(out, rows)
	return out
}

func (s *Store) NamespaceProcessRecords(nsID uint64, includeRemoved bool) []Record[types.ProcessInfo] {
	s.mu.RLock()
	defer s.mu.RUnlock()
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
	s.mu.RLock()
	defer s.mu.RUnlock()
	rows := s.links[nsID]
	out := make([]types.NamespaceLinkInfo, len(rows))
	copy(out, rows)
	return out
}

func (s *Store) NamespaceLinkRecords(nsID uint64, includeRemoved bool) []Record[types.NamespaceLinkInfo] {
	s.mu.RLock()
	defer s.mu.RUnlock()
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
