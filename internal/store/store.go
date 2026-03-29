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

	eventCh    chan storeEvent
	nsResyncCh chan struct{}

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
		eventCh:    make(chan storeEvent, 1024),
		nsResyncCh: make(chan struct{}, 1),
		inventory: inventory{
			selfNamespaceID: selfNamespaceID,
			namespaceState:  map[uint64]*namespaceState{},
			topology:        map[uint64]topologyState{},
		},
		runtimeState: runtimeState{
			processes:   map[uint64][]types.ProcessInfo{},
			processPrev: map[string]processSample{},
		},
		recordState: recordState{
			neighMeta:      map[string]Meta{},
			fdbMeta:        map[string]Meta{},
			routeMeta:      map[string]Meta{},
			processRecords: map[string]types.ProcessInfo{},
			processMeta:    map[string]Meta{},
			ifaceMeta:      map[string]Meta{},
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
		ifaces: map[string]types.InterfaceInfo{},
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
	return s.inventory.namespaceState[namespaceID]
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
	state := s.inventory.namespaceState[id]
	if state == nil {
		return types.NamespaceInfo{}, false
	}
	return state.info, true
}

func (s *Store) InterfaceRecords(nsID uint64, includeRemoved bool, sortByRate bool) []Record[types.InterfaceInfo] {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return sortedTopologyRecords(s.inventory.topology, s.recordState.ifaceMeta, nsID, includeRemoved,
		func(t topologyState) map[string]types.InterfaceInfo { return t.ifaces },
		func(a, b Record[types.InterfaceInfo]) bool {
			if sortByRate {
				ai := a.Val.RxBps + a.Val.TxBps
				aj := b.Val.RxBps + b.Val.TxBps
				if ai != aj {
					return ai > aj
				}
				return a.Val.IfIndex < b.Val.IfIndex
			}
			return a.Val.IfIndex < b.Val.IfIndex
		})
}

func (s *Store) NeighRecords(nsID uint64, includeRemoved bool) []Record[types.NeighEntry] {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return sortedTopologyRecords(s.inventory.topology, s.recordState.neighMeta, nsID, includeRemoved,
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

func (s *Store) FDBRecords(nsID uint64, includeRemoved bool) []Record[types.FdbEntry] {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return sortedTopologyRecords(s.inventory.topology, s.recordState.fdbMeta, nsID, includeRemoved,
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

func (s *Store) RouteRecords(nsID uint64, includeRemoved bool) []Record[types.RouteEntry] {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return sortedTopologyRecords(s.inventory.topology, s.recordState.routeMeta, nsID, includeRemoved,
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
	nsID uint64,
	includeRemoved bool,
	pick func(topologyState) map[string]T,
	less func(a, b Record[T]) bool,
) []Record[T] {
	out := collectTopologyRecords(topology, metaMap, nsID, includeRemoved, pick)
	sort.Slice(out, func(i, j int) bool { return less(out[i], out[j]) })
	return out
}

func collectTopologyRecords[T any](
	topology map[uint64]topologyState,
	metaMap map[string]Meta,
	nsID uint64,
	includeRemoved bool,
	pick func(topologyState) map[string]T,
) []Record[T] {
	out := make([]Record[T], 0, len(metaMap))
	if nsID != 0 {
		t, ok := topology[nsID]
		if !ok {
			return nil
		}
		for key, val := range pick(t) {
			meta := metaMap[key]
			if !includeRemoved && meta.State == StateRemoved {
				continue
			}
			out = append(out, Record[T]{Key: key, Val: val, Meta: meta})
		}
		return out
	}
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
