package store

import (
	"time"

	"github.com/msstnk/vxmon/internal/constants"
	"github.com/msstnk/vxmon/internal/debuglog"
)

func (s *Store) Advance(now time.Time) (changed bool, active bool) {
	var c, a bool
	refChanged := false

	c, a = advanceMeta(s.recordState.neighMeta, func(key string) {
		if nsID, ok := recordNamespaceID(key); ok {
			if t, exists := s.inventory.topology[nsID]; exists {
				delete(t.neigh, key)
				s.inventory.topology[nsID] = t
			}
		}
	}, now)
	if c {
		debuglog.Tracef("store.Advance neigh meta changed")
	}
	changed = changed || c
	active = active || a
	refChanged = refChanged || c

	c, a = advanceMeta(s.recordState.fdbMeta, func(key string) {
		if nsID, ok := recordNamespaceID(key); ok {
			if t, exists := s.inventory.topology[nsID]; exists {
				delete(t.fdb, key)
				s.inventory.topology[nsID] = t
			}
		}
	}, now)
	if c {
		debuglog.Tracef("store.Advance fdb meta changed")
	}
	changed = changed || c
	active = active || a
	refChanged = refChanged || c

	c, a = advanceMeta(s.recordState.routeMeta, func(key string) {
		if nsID, ok := recordNamespaceID(key); ok {
			if t, exists := s.inventory.topology[nsID]; exists {
				delete(t.routes, key)
				s.inventory.topology[nsID] = t
			}
		}
	}, now)
	if c {
		debuglog.Tracef("store.Advance route meta changed")
	}
	changed = changed || c
	active = active || a
	refChanged = refChanged || c

	// Process meta changes are too noisy to track.
	_, _ = advanceMeta(s.recordState.processMeta, func(key string) { delete(s.recordState.processRecords, key) }, now)

	c, a = advanceMeta(s.recordState.linkMeta, func(key string) { delete(s.recordState.linkRecords, key) }, now)
	if c {
		debuglog.Tracef("store.Advance link meta changed")
	}
	changed = changed || c
	active = active || a

	if refChanged {
		s.rebuildReferenceMaps()
	}
	if changed {
		s.bumpMetaRevisionLocked()
	}

	return changed, active
}

func advanceMeta(metaMap map[string]Meta, onRemoved func(string), now time.Time) (changed bool, active bool) {
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
			onRemoved(key)
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

func (s *Store) HasActiveFades() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return hasActiveMeta(s.recordState.neighMeta) ||
		hasActiveMeta(s.recordState.fdbMeta) ||
		hasActiveMeta(s.recordState.routeMeta) ||
		hasActiveMeta(s.recordState.linkMeta)
}
