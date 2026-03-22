package store

import (
	"strconv"
	"strings"
	"time"
)

// reconcile.go applies snapshot diffs and state transitions for fade-aware records.
// reconcile is called from Store.ReloadNeighAndFDB and Store.ReloadRoutes.
func reconcile[T any](
	oldMap map[string]T,
	oldMeta map[string]Meta,
	newList []T,
	keyFn func(T) string,
	fpFn func(T) string,
	now time.Time,
) (map[string]T, map[string]Meta, bool) {
	return reconcileScoped(oldMap, oldMeta, newList, keyFn, fpFn, func(string) bool { return true }, now)
}

func reconcileNamespace[T any](
	oldMap map[string]T,
	oldMeta map[string]Meta,
	newList []T,
	keyFn func(T) string,
	fpFn func(T) string,
	namespaceID uint64,
	now time.Time,
) (map[string]T, map[string]Meta, bool) {
	prefix := strconv.FormatUint(namespaceID, 10) + "|"
	return reconcileScoped(oldMap, oldMeta, newList, keyFn, fpFn, func(key string) bool {
		return strings.HasPrefix(key, prefix)
	}, now)
}

func reconcileScoped[T any](
	oldMap map[string]T,
	oldMeta map[string]Meta,
	newList []T,
	keyFn func(T) string,
	fpFn func(T) string,
	shouldRemove func(string) bool,
	now time.Time,
) (map[string]T, map[string]Meta, bool) {
	changed := false
	newMap := make(map[string]T, len(newList))
	newFP := make(map[string]string, len(newList))
	for _, v := range newList {
		k := keyFn(v)
		newMap[k] = v
		newFP[k] = fpFn(v)
	}

	for k, v := range newMap {
		fp := newFP[k]
		prev, ok := oldMeta[k]
		if !ok {
			oldMeta[k] = Meta{State: StateAdded, ChangedAt: now, Fingerprint: fp}
			oldMap[k] = v
			changed = true
			continue
		}

		if prev.State == StateRemoved {
			oldMap[k] = v
			oldMeta[k] = Meta{State: StateAdded, ChangedAt: now, Fingerprint: fp}
			changed = true
			continue
		}

		if prev.Fingerprint != fp {
			oldMap[k] = v
			oldMeta[k] = Meta{State: StateUpdated, ChangedAt: now, Fingerprint: fp}
			changed = true
			continue
		}

		oldMap[k] = v
		prev.Fingerprint = fp
		oldMeta[k] = prev
	}

	for k := range oldMap {
		if !shouldRemove(k) {
			continue
		}
		if _, exists := newMap[k]; exists {
			continue
		}
		prev := oldMeta[k]
		if prev.State != StateRemoved {
			prev.State = StateRemoved
			prev.ChangedAt = now
			oldMeta[k] = prev
			changed = true
		}
	}
	return oldMap, oldMeta, changed
}
