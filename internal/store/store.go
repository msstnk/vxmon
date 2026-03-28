package store

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/msstnk/vxmon/internal/constants"
	"github.com/msstnk/vxmon/internal/debuglog"
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

	inventory      inventory
	runtimeState   runtimeState
	recordState    recordState
	referenceState referenceState
	reloadState    reloadState

	metaRevision uint64
}

func New() *Store {
	selfNamespaceID, err := readNamespaceID("/proc/self/ns/net")
	if err != nil {
		debuglog.Errorf("store.New read self netns failed: %v", err)
	}

	return &Store{
		eventCh: make(chan storeEvent, 1024),
		inventory: inventory{
			selfNamespaceID: selfNamespaceID,
			namespacesByID:  map[uint64]*namespaceState{},
			topology:        map[uint64]topologyState{},
		},
		runtimeState: runtimeState{
			processes:   map[uint64][]types.ProcessInfo{},
			links:       map[uint64][]types.NamespaceLinkInfo{},
			processPrev: map[string]processSample{},
			linkHistory: map[string]*linkSampleRing{},
		},
		recordState: recordState{
			neighMeta:      map[string]Meta{},
			fdbMeta:        map[string]Meta{},
			routeMeta:      map[string]Meta{},
			processRecords: map[string]types.ProcessInfo{},
			processMeta:    map[string]Meta{},
			linkRecords:    map[string]types.NamespaceLinkInfo{},
			linkMeta:       map[string]Meta{},
		},
		referenceState: referenceState{
			vrfUsedIfByNS:        map[uint64]map[int]struct{}{},
			vrfUsedIfCompactByNS: map[uint64]map[int]struct{}{},
			vrfUsedIfCompactHold: map[uint64]map[int]time.Time{},
			bridgePortUsedByNS:   map[uint64]map[int]struct{}{},
		},
		reloadState: reloadState{
			ns: map[uint64]*nsReloadEntry{},
		},
	}
}

func newTopologyState() topologyState {
	return topologyState{
		neigh:  map[string]types.NeighEntry{},
		fdb:    map[string]types.FdbEntry{},
		routes: map[string]types.RouteEntry{},
	}
}

func (s *Store) topologyState(namespaceID uint64) topologyState {
	t, ok := s.inventory.topology[namespaceID]
	if !ok {
		return newTopologyState()
	}
	return t
}

func (s *Store) Run(ctx context.Context, send func(any)) {
	s.runLoop(ctx, send)
}

func (s *Store) RequestFetchLatest(at time.Time) {
	s.enqueueEvent(storeEvent{kind: storeEventFetchLatest, at: at})
}

func (s *Store) RuntimeRefreshDue(now time.Time) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.runtimeState.lastRuntime.IsZero() || now.Sub(s.runtimeState.lastRuntime) >= constants.RuntimeRefreshInterval
}

func (s *Store) metaRevisionLocked() uint64 {
	return s.metaRevision
}

func (s *Store) bumpMetaRevisionLocked() {
	s.metaRevision++
}

func (s *Store) namespaceState(namespaceID uint64) *namespaceState {
	return s.inventory.namespacesByID[namespaceID]
}

func (s *Store) Namespaces() []types.NamespaceInfo {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]types.NamespaceInfo, len(s.inventory.namespaces))
	copy(out, s.inventory.namespaces)
	return out
}

func (s *Store) NamespaceInfo(id uint64) (types.NamespaceInfo, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	state := s.inventory.namespacesByID[id]
	if state == nil {
		return types.NamespaceInfo{}, false
	}
	return state.info, true
}

func (s *Store) Interfaces() []types.InterfaceInfo {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]types.InterfaceInfo, len(s.inventory.ifaces))
	copy(out, s.inventory.ifaces)
	return out
}

func (s *Store) NeighRecords(includeRemoved bool) []Record[types.NeighEntry] {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return sortedTopologyRecords(s.inventory.topology, s.recordState.neighMeta, includeRemoved,
		func(t topologyState) map[string]types.NeighEntry { return t.neigh },
		func(a, b Record[types.NeighEntry]) bool {
			if a.Val.NamespaceRoot != b.Val.NamespaceRoot {
				return a.Val.NamespaceRoot
			}
			if a.Val.NamespaceName != b.Val.NamespaceName {
				return a.Val.NamespaceName < b.Val.NamespaceName
			}
			if a.Val.IfIndex != b.Val.IfIndex {
				return a.Val.IfIndex < b.Val.IfIndex
			}
			return a.Val.IP < b.Val.IP
		})
}

func (s *Store) FDBRecords(includeRemoved bool) []Record[types.FdbEntry] {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return sortedTopologyRecords(s.inventory.topology, s.recordState.fdbMeta, includeRemoved,
		func(t topologyState) map[string]types.FdbEntry { return t.fdb },
		func(a, b Record[types.FdbEntry]) bool {
			if a.Val.NamespaceRoot != b.Val.NamespaceRoot {
				return a.Val.NamespaceRoot
			}
			if a.Val.NamespaceName != b.Val.NamespaceName {
				return a.Val.NamespaceName < b.Val.NamespaceName
			}
			if a.Val.BridgeID != b.Val.BridgeID {
				return a.Val.BridgeID < b.Val.BridgeID
			}
			if a.Val.VLANID != b.Val.VLANID {
				return a.Val.VLANID < b.Val.VLANID
			}
			if a.Val.PortID != b.Val.PortID {
				return a.Val.PortID < b.Val.PortID
			}
			return a.Val.RemoteVTEP < b.Val.RemoteVTEP
		})
}

func (s *Store) RouteRecords(includeRemoved bool) []Record[types.RouteEntry] {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return sortedTopologyRecords(s.inventory.topology, s.recordState.routeMeta, includeRemoved,
		func(t topologyState) map[string]types.RouteEntry { return t.routes },
		func(a, b Record[types.RouteEntry]) bool {
			if a.Val.NamespaceRoot != b.Val.NamespaceRoot {
				return a.Val.NamespaceRoot
			}
			if a.Val.NamespaceName != b.Val.NamespaceName {
				return a.Val.NamespaceName < b.Val.NamespaceName
			}
			if a.Val.Table != b.Val.Table {
				return a.Val.Table < b.Val.Table
			}
			return a.Val.Dst < b.Val.Dst
		})
}

func (s *Store) NamespaceProcessRecords(nsID uint64, includeRemoved bool) []Record[types.ProcessInfo] {
	s.mu.RLock()
	defer s.mu.RUnlock()
	prefix := recordPrefix(nsID)
	out := collectRecords(s.recordState.processRecords, s.recordState.processMeta, includeRemoved, func(key string, _ types.ProcessInfo) bool {
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
	rows := s.runtimeState.links[nsID]
	out := make([]types.NamespaceLinkInfo, len(rows))
	copy(out, rows)
	return out
}

func (s *Store) NamespaceLinkRecords(nsID uint64, includeRemoved bool) []Record[types.NamespaceLinkInfo] {
	s.mu.RLock()
	defer s.mu.RUnlock()
	prefix := recordPrefix(nsID)
	out := collectRecords(s.recordState.linkRecords, s.recordState.linkMeta, includeRemoved, func(key string, _ types.NamespaceLinkInfo) bool {
		return strings.HasPrefix(key, prefix)
	})
	sort.Slice(out, func(i, j int) bool {
		ai := out[i].Val.RxBps + out[i].Val.TxBps
		aj := out[j].Val.RxBps + out[j].Val.TxBps
		if ai != aj {
			return ai > aj
		}
		return out[i].Val.IfIndex < out[j].Val.IfIndex
	})
	return out
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

func sortedTopologyRecords[T any](
	topology map[uint64]topologyState,
	metaMap map[string]Meta,
	includeRemoved bool,
	pick func(topologyState) map[string]T,
	less func(a, b Record[T]) bool,
) []Record[T] {
	out := collectTopologyRecords(topology, metaMap, includeRemoved, pick)
	sort.Slice(out, func(i, j int) bool { return less(out[i], out[j]) })
	return out
}

func collectTopologyRecords[T any](
	topology map[uint64]topologyState,
	metaMap map[string]Meta,
	includeRemoved bool,
	pick func(topologyState) map[string]T,
) []Record[T] {
	out := make([]Record[T], 0, len(metaMap))
	for _, t := range topology {
		rows := pick(t)
		for key, val := range rows {
			meta := metaMap[key]
			if !includeRemoved && meta.State == StateRemoved {
				continue
			}
			out = append(out, Record[T]{Key: key, Val: val, Meta: meta})
		}
	}
	return out
}
