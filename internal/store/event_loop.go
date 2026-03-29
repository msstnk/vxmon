package store

import (
	"context"
	"time"

	"github.com/msstnk/vxmon/internal/constants"
	"github.com/msstnk/vxmon/internal/debuglog"
)

type nlReloadKind uint8

const (
	nlReloadInterfaces nlReloadKind = iota
	nlReloadNeighAndFDB
	nlReloadRoutes
)

type nlReloadMask uint8

const fullReloadMask nlReloadMask = (1 << nlReloadInterfaces) | (1 << nlReloadNeighAndFDB) | (1 << nlReloadRoutes)

type storeEventKind uint8

const (
	storeEventFetchLatest storeEventKind = iota
	storeEventNamespaceSync
	storeEventNamespaceSubscribed
	storeEventNeigh
	storeEventRoute
	storeEventLink
	storeEventFadeTick
	storeEventScheduledReload
)

type eventLoopMsg uint8

const (
	noupdate eventLoopMsg = iota
	updated
	periodicUpdate
)

type storeEvent struct {
	kind        storeEventKind
	namespaceID uint64
	at          time.Time
}

func (s *Store) enqueueEvent(ev storeEvent) {
	select {
	case s.eventCh <- ev:
	default:
		// Keep the latest view
		debuglog.Infof("store.enqueueEvent: event dropped kind=%v, namespaceID=%d, at=%v",
			ev.kind, ev.namespaceID, ev.at)
	}
}

func (s *Store) runLoop(ctx context.Context, send func(any)) {
	debuglog.Infof("store.runLoop start")
	go s.ListenNetlink(ctx, func(msg any) {
		switch x := msg.(type) {
		case NamespaceSyncMsg:
			s.enqueueEvent(storeEvent{kind: storeEventNamespaceSync, at: x.At})
		case NamespaceSubscribedMsg:
			s.enqueueEvent(storeEvent{kind: storeEventNamespaceSubscribed, namespaceID: x.NamespaceID, at: x.At})
		case NeighNLMsg:
			s.enqueueEvent(storeEvent{kind: storeEventNeigh, namespaceID: x.Namespace.ID, at: x.At})
		case RouteNLMsg:
			s.enqueueEvent(storeEvent{kind: storeEventRoute, namespaceID: x.Namespace.ID, at: x.At})
		case LinkNLMsg:
			s.enqueueEvent(storeEvent{kind: storeEventLink, namespaceID: x.Namespace.ID, at: x.At})
		}
	})

	fadeTicker := time.NewTicker(constants.AnimTickInterval)
	defer fadeTicker.Stop()
	s.RequestFetchLatest(time.Now())

	for {
		select {
		case <-ctx.Done():
			debuglog.Infof("store.runLoop stop")
			return
		case at := <-fadeTicker.C:
			s.enqueueEvent(storeEvent{kind: storeEventFadeTick, at: at})
		case ev := <-s.eventCh:
			msg := s.handleEvent(ev)
			switch msg {
			case updated:
				send(InventoryUpdatedMsg{At: time.Now()})
			case periodicUpdate:
				send(InventoryPeriodicUpdatedMsg{At: time.Now()})
			}
		}
	}
}

func (s *Store) handleEvent(ev storeEvent) eventLoopMsg {
	if ev.at.IsZero() {
		ev.at = time.Now()
	}
	switch ev.kind {
	case storeEventFetchLatest:
		return s.applyFetchLatest(ev.at)
	case storeEventNamespaceSync:
		return s.applyNamespaceSync(ev.at)
	case storeEventNamespaceSubscribed:
		return s.applyNamespaceSubscribedEvent(ev.namespaceID, ev.at)
	case storeEventNeigh:
		return s.applyNeighEvent(ev.namespaceID, ev.at)
	case storeEventRoute:
		return s.applyRouteEvent(ev.namespaceID, ev.at)
	case storeEventLink:
		return s.applyLinkEvent(ev.namespaceID, ev.at)
	case storeEventFadeTick:
		return s.applyFadeTick(ev.at)
	case storeEventScheduledReload:
		return s.applyScheduledReload(ev.at)
	default:
		return noupdate
	}
}

func (s *Store) applyFetchLatest(at time.Time) eventLoopMsg {
	return s.applyLockedUpdate(at, periodicUpdate, "store.applyFetchLatest failed", func() error {
		s.cancelScheduledNamespaceReloadLocked()
		if err := s.ReloadAll(at); err != nil {
			return err
		}
		s.markNamespaceReloadRefreshedAllLocked(at)
		return nil
	})
}

func (s *Store) applyNamespaceSync(at time.Time) eventLoopMsg {
	return s.applyLockedUpdate(at, noupdate, "store.applyNamespaceSync failed", func() error {
		return s.syncNamespaces()
	})
}

func (s *Store) applyNamespaceSubscribedEvent(namespaceID uint64, at time.Time) eventLoopMsg {
	return s.applyNamespaceReloadEvent(namespaceID, fullReloadMask, at)
}

func (s *Store) applyNamespaceReloadEvent(namespaceID uint64, mask nlReloadMask, at time.Time) eventLoopMsg {
	return s.applyLockedUpdate(at, noupdate, "", func() error {
		s.collectNamespaceReloadLocked(namespaceID, mask, at)
		s.applyDueNamespaceReloadLocked(at)
		return nil
	})
}

func (s *Store) applyNeighEvent(namespaceID uint64, at time.Time) eventLoopMsg {
	return s.applyNamespaceReloadEvent(namespaceID, nlReloadMaskForKind(nlReloadNeighAndFDB), at)
}

func (s *Store) applyRouteEvent(namespaceID uint64, at time.Time) eventLoopMsg {
	return s.applyNamespaceReloadEvent(namespaceID, nlReloadMaskForKind(nlReloadRoutes), at)
}

func (s *Store) applyLinkEvent(namespaceID uint64, at time.Time) eventLoopMsg {
	return s.applyNamespaceReloadEvent(namespaceID, fullReloadMask, at)
}

func (s *Store) applyFadeTick(at time.Time) eventLoopMsg {
	return s.applyLockedUpdate(at, noupdate, "", func() error {
		s.applyDueNamespaceReloadLocked(at)
		return nil
	})
}

func (s *Store) applyScheduledReload(at time.Time) eventLoopMsg {
	return s.applyLockedUpdate(at, noupdate, "store.applyScheduledReload sync failed", func() error {
		prev := make(map[uint64]struct{}, len(s.inventory.namespaceState))
		for id := range s.inventory.namespaceState {
			prev[id] = struct{}{}
		}
		if err := s.syncNamespaces(); err != nil {
			return err
		}
		newFound := false
		for id := range s.inventory.namespaceState {
			if _, existed := prev[id]; !existed {
				s.collectNamespaceReloadLocked(id, fullReloadMask, at)
				newFound = true
			}
		}
		if newFound {
			select {
			case s.nsResyncCh <- struct{}{}:
			default:
			}
		}
		s.applyDueNamespaceReloadLocked(at)
		return nil
	})
}

func (s *Store) applyLockedUpdate(at time.Time, onNoChange eventLoopMsg, errLog string, fn func() error) eventLoopMsg {
	s.mu.Lock()
	defer s.mu.Unlock()
	before := s.metaRevisionLocked()
	if fn != nil {
		if err := fn(); err != nil {
			if errLog != "" {
				debuglog.Errorf("%s: %v", errLog, err)
			}
			return noupdate
		}
	}
	_, _ = s.Advance(at)
	if s.metaRevisionLocked() != before {
		return updated
	}
	return onNoChange
}

// applyDueNamespaceReloadLocked processes all due namespace reloads.
// It releases s.mu during netlink I/O so UI reads are not blocked, then reacquires it.
// Callers must hold s.mu on entry; the lock is held again on return.
func (s *Store) applyDueNamespaceReloadLocked(now time.Time) {
	type work struct {
		namespaceID uint64
		dirty       nlReloadMask
		state       *namespaceState
	}
	var pending []work
	for namespaceID, e := range s.reloadState.ns {
		if !e.pending || e.dueAt.After(now) {
			continue
		}
		e.pending = false
		dirty := e.dirty
		e.dirty = 0
		e.dueAt = time.Time{}
		state := s.namespaceState(namespaceID)
		if dirty == 0 || state == nil {
			continue
		}
		pending = append(pending, work{namespaceID, dirty, state})
	}
	if len(pending) == 0 {
		return
	}

	// Release lock during netlink I/O to allow concurrent UI reads.
	s.mu.Unlock()
	type result struct {
		work
		links    linkListRaw
		linkOK   bool
		ifaces   interfaceInfoRaw
		neighFDB namespaceNeighFDBSnapshot
		routes   namespaceRouteSnapshot
	}
	results := make([]result, len(pending))
	for i, w := range pending {
		r := result{work: w}
		if w.dirty&(nlReloadMaskForKind(nlReloadNeighAndFDB)|nlReloadMaskForKind(nlReloadRoutes)) != 0 {
			links, err := getLinkListRaw(w.state.handle)
			if err == nil {
				r.links = links
				r.linkOK = true
			} else {
				debuglog.Errorf("store.applyDueNamespaceReloadLocked links namespace=%d failed: %v", w.state.info.ID, err)
			}
		}
		if w.dirty&nlReloadMaskForKind(nlReloadInterfaces) != 0 {
			r.ifaces, _ = s.collectNamespaceInterfaces(w.state)
		}
		if r.linkOK && w.dirty&nlReloadMaskForKind(nlReloadNeighAndFDB) != 0 {
			r.neighFDB, _ = s.collectNamespaceNeighAndFDB(w.state, r.links)
		}
		if r.linkOK && w.dirty&nlReloadMaskForKind(nlReloadRoutes) != 0 {
			r.routes, _ = s.collectNamespaceRoutes(w.state, r.links)
		}
		results[i] = r
	}
	s.mu.Lock()

	for _, r := range results {
		// Re-check in case the namespace was removed while we were collecting.
		state := s.namespaceState(r.namespaceID)
		if state == nil {
			continue
		}
		if r.dirty&nlReloadMaskForKind(nlReloadInterfaces) != 0 {
			s.applyNamespaceInterfaces(state, r.ifaces, now)
		}
		if r.dirty&nlReloadMaskForKind(nlReloadNeighAndFDB) != 0 {
			s.applyNamespaceNeighAndFDB(state, r.neighFDB, now)
		}
		if r.dirty&nlReloadMaskForKind(nlReloadRoutes) != 0 {
			s.applyNamespaceRoutes(state, r.routes, now)
		}
		if e := s.reloadState.ns[r.namespaceID]; e != nil {
			e.refreshedAt = now
		}
	}
	s.rebuildReferenceMaps()
}

func (s *Store) collectNamespaceReloadLocked(namespaceID uint64, mask nlReloadMask, at time.Time) {
	if mask == 0 {
		return
	}
	e := s.reloadState.ns[namespaceID]
	if e == nil {
		e = &nsReloadEntry{}
		s.reloadState.ns[namespaceID] = e
	}
	e.dirty |= mask
	dueAt := at.Add(constants.NLMsgAggregationTimer)
	if !e.refreshedAt.IsZero() && at.Sub(e.refreshedAt) < constants.NLMsgThrottleInterval {
		dueAt = e.refreshedAt.Add(constants.NLMsgThrottleInterval)
	}
	if !e.dueAt.IsZero() && e.dueAt.Before(dueAt) {
		dueAt = e.dueAt
	}
	e.dueAt = dueAt
	if !e.pending {
		e.pending = true
		wait := time.Until(dueAt)
		if wait < 0 {
			wait = 0
		}
		time.AfterFunc(wait, func() {
			s.enqueueEvent(storeEvent{
				kind:        storeEventScheduledReload,
				namespaceID: namespaceID,
				at:          dueAt.Add(time.Nanosecond),
			})
		})
	}
}

func (s *Store) markNamespaceReloadRefreshedAllLocked(at time.Time) {
	for _, ns := range s.inventory.namespaces {
		e := s.reloadState.ns[ns.ID]
		if e == nil {
			e = &nsReloadEntry{}
			s.reloadState.ns[ns.ID] = e
		}
		e.refreshedAt = at
	}
}

func (s *Store) cancelScheduledNamespaceReloadLocked() {
	s.reloadState.ns = map[uint64]*nsReloadEntry{}
}

func nlReloadMaskForKind(kind nlReloadKind) nlReloadMask {
	return 1 << kind
}
