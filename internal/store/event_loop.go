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

type storeEventKind uint8

const (
	storeEventFetchLatest storeEventKind = iota
	storeEventNamespaceSync
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
		// Keep the latest view; dropping stale events is preferable to blocking listeners.
	}
}

func (s *Store) runLoop(ctx context.Context, send func(any)) {
	debuglog.Infof("store.runLoop start")
	go s.ListenNetlink(ctx, func(msg any) {
		switch x := msg.(type) {
		case NamespaceSyncMsg:
			s.enqueueEvent(storeEvent{kind: storeEventNamespaceSync, at: x.At})
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
		s.mu.Lock()
		defer s.mu.Unlock()
		before := s.metaRevisionLocked()
		if err := s.ReloadAll(ev.at); err != nil {
			debuglog.Errorf("store.handleEvent fetch failed: %v", err)
			return noupdate
		}
		if s.metaRevisionLocked() != before {
			return updated
		}
		return periodicUpdate

	case storeEventNamespaceSync:
		s.mu.Lock()
		defer s.mu.Unlock()
		before := s.metaRevisionLocked()
		if err := s.syncNamespaces(); err != nil {
			debuglog.Errorf("store.handleEvent namespace sync failed: %v", err)
			return noupdate
		}
		if s.metaRevisionLocked() != before {
			return updated
		}
		return noupdate
	case storeEventNeigh:
		s.mu.Lock()
		defer s.mu.Unlock()
		before := s.metaRevisionLocked()
		s.scheduleNamespaceReloadLocked(nlReloadNeighAndFDB, ev.namespaceID, ev.at)
		s.scheduleNamespaceReloadLocked(nlReloadInterfaces, ev.namespaceID, ev.at)
		s.runScheduledNamespaceReloadLocked(ev.at)
		if s.metaRevisionLocked() != before {
			return updated
		}
		return noupdate
	case storeEventRoute:
		s.mu.Lock()
		defer s.mu.Unlock()
		before := s.metaRevisionLocked()
		s.scheduleNamespaceReloadLocked(nlReloadRoutes, ev.namespaceID, ev.at)
		s.scheduleNamespaceReloadLocked(nlReloadInterfaces, ev.namespaceID, ev.at)
		s.runScheduledNamespaceReloadLocked(ev.at)
		if s.metaRevisionLocked() != before {
			return updated
		}
		return noupdate
	case storeEventLink:
		s.mu.Lock()
		defer s.mu.Unlock()
		before := s.metaRevisionLocked()
		s.scheduleNamespaceReloadLocked(nlReloadInterfaces, ev.namespaceID, ev.at)
		s.runScheduledNamespaceReloadLocked(ev.at)
		if s.metaRevisionLocked() != before {
			return updated
		}
		return noupdate
	case storeEventFadeTick:
		s.mu.Lock()
		defer s.mu.Unlock()
		before := s.metaRevisionLocked()
		s.runScheduledNamespaceReloadLocked(ev.at)
		_, _ = s.Advance(ev.at)
		if s.metaRevisionLocked() != before {
			return periodicUpdate
		}
		return noupdate
	case storeEventScheduledReload:
		s.mu.Lock()
		defer s.mu.Unlock()
		before := s.metaRevisionLocked()
		s.runScheduledNamespaceReloadLocked(ev.at)
		if err := s.syncNamespaces(); err != nil {
			debuglog.Errorf("store.handleEvent scheduled namespace sync failed: %v", err)
			return noupdate
		}
		_, _ = s.Advance(ev.at)
		if s.metaRevisionLocked() != before {
			return updated
		}
		return noupdate
	default:
		return noupdate
	}
}

func (s *Store) runScheduledNamespaceReloadLocked(now time.Time) bool {
	changed := false
	for namespaceID := range s.nsReloadPending {
		dueAt := s.namespaceReloadDueAt(namespaceID)
		if dueAt.After(now) {
			continue
		}
		s.namespaceReloadSetPending(namespaceID, false)
		dirty := s.namespaceReloadTakeDirty(namespaceID)
		s.namespaceReloadClearDueAt(namespaceID)
		if dirty == 0 {
			continue
		}
		if dirty&nlReloadMaskForKind(nlReloadInterfaces) != 0 {
			s.applyNamespaceReloadLocked(nlReloadInterfaces, namespaceID, now)
			changed = true
		}
		if dirty&nlReloadMaskForKind(nlReloadNeighAndFDB) != 0 {
			s.applyNamespaceReloadLocked(nlReloadNeighAndFDB, namespaceID, now)
			changed = true
		}
		if dirty&nlReloadMaskForKind(nlReloadRoutes) != 0 {
			s.applyNamespaceReloadLocked(nlReloadRoutes, namespaceID, now)
			changed = true
		}
		s.namespaceReloadSetRefreshedAt(namespaceID, now)
	}
	return changed
}

func (s *Store) scheduleNamespaceReloadLocked(kind nlReloadKind, namespaceID uint64, at time.Time) {
	s.namespaceReloadAddDirty(namespaceID, kind)
	dueAt := at.Add(constants.NLMsgThrottleInterval)
	last := s.namespaceReloadRefreshedAt(namespaceID)
	if !last.IsZero() {
		minDueAt := last.Add(constants.NLMsgThrottleInterval)
		if minDueAt.After(dueAt) {
			dueAt = minDueAt
		}
	}
	s.namespaceReloadSetDueAt(namespaceID, dueAt)
	if !s.namespaceReloadPending(namespaceID) {
		s.namespaceReloadSetPending(namespaceID, true)
		time.AfterFunc(time.Until(dueAt), func() {
			s.eventCh <- storeEvent{
				kind:        storeEventScheduledReload,
				namespaceID: namespaceID,
				at:          dueAt.Add(time.Nanosecond),
			}
		})
	}
}

func (s *Store) applyNamespaceReloadLocked(kind nlReloadKind, namespaceID uint64, at time.Time) {
	switch kind {
	case nlReloadNeighAndFDB:
		_ = s.ReloadNamespaceNeighAndFDB(namespaceID, at)
	case nlReloadRoutes:
		_ = s.ReloadNamespaceRoutes(namespaceID, at)
	default:
		_ = s.ReloadNamespaceInterfaces(namespaceID, at)
	}
}

func nlReloadMaskForKind(kind nlReloadKind) nlReloadMask {
	return 1 << kind
}

func (s *Store) namespaceReloadRefreshedAt(namespaceID uint64) time.Time {
	return s.nsReloadRefreshedAt[namespaceID]
}

func (s *Store) namespaceReloadSetRefreshedAt(namespaceID uint64, at time.Time) {
	s.nsReloadRefreshedAt[namespaceID] = at
}

func (s *Store) namespaceReloadDueAt(namespaceID uint64) time.Time {
	return s.nsReloadDueAt[namespaceID]
}

func (s *Store) namespaceReloadPending(namespaceID uint64) bool {
	return s.nsReloadPending[namespaceID]
}

func (s *Store) namespaceReloadSetPending(namespaceID uint64, pending bool) {
	if pending {
		s.nsReloadPending[namespaceID] = true
		return
	}
	delete(s.nsReloadPending, namespaceID)
}

func (s *Store) namespaceReloadSetDueAt(namespaceID uint64, at time.Time) {
	s.nsReloadDueAt[namespaceID] = at
}

func (s *Store) namespaceReloadClearDueAt(namespaceID uint64) {
	delete(s.nsReloadDueAt, namespaceID)
}

func (s *Store) namespaceReloadAddDirty(namespaceID uint64, kind nlReloadKind) {
	s.nsReloadDirty[namespaceID] |= nlReloadMaskForKind(kind)
}

func (s *Store) namespaceReloadTakeDirty(namespaceID uint64) nlReloadMask {
	dirty := s.nsReloadDirty[namespaceID]
	delete(s.nsReloadDirty, namespaceID)
	return dirty
}
