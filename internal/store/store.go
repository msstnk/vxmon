package store

import (
	"sort"
	"time"

	"vxmon/internal/types"
)

// store.go owns in-memory snapshots plus transient fade metadata for rendering.
// FadeDuration is consumed by Store.Advance and ui.FadeStyle to time transitions.
const FadeDuration = 2400 * time.Millisecond

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
	ifaces []types.InterfaceInfo

	neigh     map[string]types.NeighEntry
	neighMeta map[string]Meta

	fdb     map[string]types.FdbEntry
	fdbMeta map[string]Meta

	routes     map[string]types.RouteEntry
	routeMeta  map[string]Meta
	lastReload time.Time
}

// New is called from cmd/vxmon/main to construct the shared snapshot store.
func New() *Store {
	return &Store{
		neigh:     map[string]types.NeighEntry{},
		neighMeta: map[string]Meta{},
		fdb:       map[string]types.FdbEntry{},
		fdbMeta:   map[string]Meta{},
		routes:    map[string]types.RouteEntry{},
		routeMeta: map[string]Meta{},
	}
}

// ReloadAll is called from app.NewModel for the initial full snapshot.
func (s *Store) ReloadAll(now time.Time) error {
	ifaces, err := getInterfaceInfo()
	if err != nil {
		return err
	}
	s.ifaces = ifaces

	if err := s.ReloadNeighAndFDB(now); err != nil {
		return err
	}
	if err := s.ReloadRoutes(now); err != nil {
		return err
	}
	s.lastReload = now
	return nil
}

// ReloadInterfaces is called from app.Model.Update when link-related events arrive.
func (s *Store) ReloadInterfaces() error {
	ifaces, err := getInterfaceInfo()
	if err != nil {
		return err
	}
	s.ifaces = ifaces
	return nil
}

// ReloadNeighAndFDB is called from ReloadAll and from app.Model.Update on neigh events.
func (s *Store) ReloadNeighAndFDB(now time.Time) error {
	neighList, err := getNeighList()
	if err != nil {
		return err
	}
	fdbList, err := getFdbList()
	if err != nil {
		return err
	}

	s.neigh, s.neighMeta = reconcile(
		s.neigh, s.neighMeta, neighList,
		neighKey, neighFingerprint, now,
	)
	s.fdb, s.fdbMeta = reconcile(
		s.fdb, s.fdbMeta, fdbList,
		fdbKey, fdbFingerprint, now,
	)
	return nil
}

// ReloadRoutes is called from ReloadAll and from app.Model.Update on route events.
func (s *Store) ReloadRoutes(now time.Time) error {
	routeList, err := getRouteList()
	if err != nil {
		return err
	}
	s.routes, s.routeMeta = reconcile(
		s.routes, s.routeMeta, routeList,
		routeKey, routeFingerprint, now,
	)
	return nil
}

// Advance is called from app.Model.Update on animation ticks.
// It advances fade states, removes expired tombstones, and reports whether a repaint is needed.
func (s *Store) Advance(now time.Time) bool {
	needsRefresh := false
	changed, active := advanceMeta(s.neighMeta, s.neigh, now)
	if changed || active {
		needsRefresh = true
	}
	changed, active = advanceMeta(s.fdbMeta, s.fdb, now)
	if changed || active {
		needsRefresh = true
	}
	changed, active = advanceMeta(s.routeMeta, s.routes, now)
	if changed || active {
		needsRefresh = true
	}
	return needsRefresh
}

// advanceMeta is called only by Store.Advance for each record/meta map pair.
func advanceMeta[T any](metaMap map[string]Meta, records map[string]T, now time.Time) (changed bool, active bool) {
	for key, meta := range metaMap {
		if meta.State == StateNone {
			continue
		}
		if now.Sub(meta.ChangedAt) < FadeDuration {
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

// Interfaces is called from top/bottom row builders to read a defensive copy.
func (s *Store) Interfaces() []types.InterfaceInfo {
	out := make([]types.InterfaceInfo, len(s.ifaces))
	copy(out, s.ifaces)
	return out
}

// NeighRecords is called from top_view.go and bottom_view.go table builders.
func (s *Store) NeighRecords(includeRemoved bool) []Record[types.NeighEntry] {
	var out []Record[types.NeighEntry]
	for k, v := range s.neigh {
		meta := s.neighMeta[k]
		if !includeRemoved && meta.State == StateRemoved {
			continue
		}
		out = append(out, Record[types.NeighEntry]{Key: k, Val: v, Meta: meta})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Val.InterfaceID != out[j].Val.InterfaceID {
			return out[i].Val.InterfaceID < out[j].Val.InterfaceID
		}
		return out[i].Val.IP < out[j].Val.IP
	})
	return out
}

// FDBRecords is called from bottom_view.go table builders.
func (s *Store) FDBRecords(includeRemoved bool) []Record[types.FdbEntry] {
	var out []Record[types.FdbEntry]
	for k, v := range s.fdb {
		meta := s.fdbMeta[k]
		if !includeRemoved && meta.State == StateRemoved {
			continue
		}
		out = append(out, Record[types.FdbEntry]{Key: k, Val: v, Meta: meta})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Val.BridgeName != out[j].Val.BridgeName {
			return out[i].Val.BridgeName < out[j].Val.BridgeName
		}
		if out[i].Val.VLANId != out[j].Val.VLANId {
			return out[i].Val.VLANId < out[j].Val.VLANId
		}
		if out[i].Val.PortName != out[j].Val.PortName {
			return out[i].Val.PortName < out[j].Val.PortName
		}
		return out[i].Val.RemoteVTEP < out[j].Val.RemoteVTEP
	})
	return out
}

// RouteRecords is called from top_view.go and bottom_view.go table builders.
func (s *Store) RouteRecords(includeRemoved bool) []Record[types.RouteEntry] {
	var out []Record[types.RouteEntry]
	for k, v := range s.routes {
		meta := s.routeMeta[k]
		if !includeRemoved && meta.State == StateRemoved {
			continue
		}
		out = append(out, Record[types.RouteEntry]{Key: k, Val: v, Meta: meta})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Val.Table != out[j].Val.Table {
			return out[i].Val.Table < out[j].Val.Table
		}
		return out[i].Val.Dst < out[j].Val.Dst
	})
	return out
}
