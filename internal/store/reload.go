package store

import (
	"time"

	"github.com/msstnk/vxmon/internal/debuglog"
	"github.com/msstnk/vxmon/internal/types"
)

type namespaceNeighFDBSnapshot struct {
	neigh []types.NeighEntry
	fdb   []types.FdbEntry
}

type namespaceRouteSnapshot struct {
	routes []types.RouteEntry
}

func (s *Store) ReloadAll(now time.Time) error {
	debuglog.Tracef("store.ReloadAll")
	s.reloadRuntime(now)
	s.reloadInterfaces(now)
	s.reloadNeighAndFDB(now)
	s.reloadRoutes(now)
	s.rebuildReferenceMaps()
	return nil
}

func (s *Store) reloadRuntime(now time.Time) error {
	scan := scanProcfs(true)
	if err := s.syncNamespacesWithProcScan(&scan); err != nil {
		return err
	}

	s.reloadProcesses(now, scan)
	s.runtimeState.lastRuntime = now
	return nil
}

func (s *Store) reloadInterfaces(now time.Time) {
	reloadNamespaceRows(s, now, s.collectNamespaceInterfaces, s.applyNamespaceInterfaces)
}

func (s *Store) reloadNeighAndFDB(now time.Time) {
	for _, state := range s.namespaceStates() {
		if state == nil || state.handle == nil {
			s.applyNamespaceNeighAndFDB(state, namespaceNeighFDBSnapshot{}, now)
			continue
		}
		rawLinks, err := getLinkListRaw(state.handle)
		if err != nil {
			debuglog.Errorf("store.reloadNeighAndFDB links namespace=%d failed: %v", state.info.ID, err)
			s.applyNamespaceNeighAndFDB(state, namespaceNeighFDBSnapshot{}, now)
			continue
		}
		raw, _ := s.collectNamespaceNeighAndFDB(state, rawLinks)
		s.applyNamespaceNeighAndFDB(state, raw, now)
	}
}

func (s *Store) reloadRoutes(now time.Time) {
	for _, state := range s.namespaceStates() {
		if state == nil || state.handle == nil {
			s.applyNamespaceRoutes(state, namespaceRouteSnapshot{}, now)
			continue
		}
		rawLinks, err := getLinkListRaw(state.handle)
		if err != nil {
			debuglog.Errorf("store.reloadRoutes links namespace=%d failed: %v", state.info.ID, err)
			s.applyNamespaceRoutes(state, namespaceRouteSnapshot{}, now)
			continue
		}
		raw, _ := s.collectNamespaceRoutes(state, rawLinks)
		s.applyNamespaceRoutes(state, raw, now)
	}
}

func reloadNamespaceRows[T any](
	s *Store,
	now time.Time,
	collect func(*namespaceState) (T, error),
	apply func(*namespaceState, T, time.Time),
) {
	for _, state := range s.namespaceStates() {
		raw, _ := collect(state)
		apply(state, raw, now)
	}
}

func (s *Store) collectNamespaceInterfaces(state *namespaceState) (interfaceInfoRaw, error) {
	if state == nil || state.handle == nil {
		return interfaceInfoRaw{}, nil
	}
	raw, err := collectInterfaceRaw(state.info, int(state.nsHandle))
	if err != nil {
		debuglog.Errorf("store.collectNamespaceInterfaces namespace=%d failed: %v", state.info.ID, err)
		return interfaceInfoRaw{}, err
	}
	return raw, nil
}

func (s *Store) applyNamespaceInterfaces(state *namespaceState, raw interfaceInfoRaw, now time.Time) {
	if state == nil {
		return
	}
	id := state.info.ID
	t := s.topologyState(id)
	ifaces := parseInterfaceRaw(raw, state.info, t.ifaces, now)
	var c bool
	t.ifaces, s.recordState.ifaceMeta, c = reconcile(t.ifaces, s.recordState.ifaceMeta, ifaces, linkKey, linkFingerprint, id, now)
	s.inventory.topology[id] = t
	if c {
		s.bumpMetaRevisionLocked()
	}
}

func (s *Store) collectNamespaceNeighAndFDB(state *namespaceState, rawLinks linkListRaw) (namespaceNeighFDBSnapshot, error) {
	if state == nil || state.handle == nil {
		return namespaceNeighFDBSnapshot{}, nil
	}

	rawNeigh, err := getNeighList(state.handle, state.info)
	if err != nil {
		debuglog.Errorf("store.collectNamespaceNeighAndFDB neigh namespace=%d failed: %v", state.info.ID, err)
		return namespaceNeighFDBSnapshot{}, err
	}
	rawFDB, err := getFdbList(state.handle, state.info)
	if err != nil {
		debuglog.Errorf("store.collectNamespaceNeighAndFDB fdb namespace=%d failed: %v", state.info.ID, err)
		return namespaceNeighFDBSnapshot{}, err
	}

	return namespaceNeighFDBSnapshot{
		neigh: parseNeighList(rawNeigh, rawLinks, state.info),
		fdb:   parseFdbList(rawFDB, rawLinks, state.info),
	}, nil
}

func (s *Store) applyNamespaceNeighAndFDB(state *namespaceState, raw namespaceNeighFDBSnapshot, now time.Time) {
	if state == nil {
		return
	}
	id := state.info.ID
	t := s.topologyState(id)
	var c bool
	t.neigh, s.recordState.neighMeta, c = reconcile(t.neigh, s.recordState.neighMeta, raw.neigh, neighKey, neighFingerprint, id, now)
	if c {
		s.bumpMetaRevisionLocked()
	}
	t.fdb, s.recordState.fdbMeta, c = reconcile(t.fdb, s.recordState.fdbMeta, raw.fdb, fdbKey, fdbFingerprint, id, now)
	if c {
		s.bumpMetaRevisionLocked()
	}
	s.inventory.topology[id] = t
}

func (s *Store) collectNamespaceRoutes(state *namespaceState, rawLinks linkListRaw) (namespaceRouteSnapshot, error) {
	if state == nil || state.handle == nil {
		return namespaceRouteSnapshot{}, nil
	}

	rawRoute, err := getRouteList(state.handle, state.info)
	if err != nil {
		debuglog.Errorf("store.collectNamespaceRoutes namespace=%d failed: %v", state.info.ID, err)
		return namespaceRouteSnapshot{}, err
	}
	return namespaceRouteSnapshot{
		routes: parseRouteList(rawRoute, rawLinks, state.info),
	}, nil
}

func (s *Store) applyNamespaceRoutes(state *namespaceState, raw namespaceRouteSnapshot, now time.Time) {
	if state == nil {
		return
	}
	id := state.info.ID
	t := s.topologyState(id)
	var c bool
	t.routes, s.recordState.routeMeta, c = reconcile(t.routes, s.recordState.routeMeta, raw.routes, routeKey, routeFingerprint, id, now)
	if c {
		s.bumpMetaRevisionLocked()
	}
	s.inventory.topology[id] = t
}

