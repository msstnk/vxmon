package store

import (
	"bufio"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netns"
	"golang.org/x/sys/unix"

	"github.com/msstnk/vxmon/internal/debuglog"
	"github.com/msstnk/vxmon/internal/types"
)

type namespaceState struct {
	info       types.NamespaceInfo
	mountPoint string
	handle     *netlink.Handle
	nsHandle   netns.NsHandle
}

type discoveredNamespace struct {
	namespaceID uint64
	mountPoint  string
	displayName string
	shortName   string
	isRoot      bool
	isCurrent   bool
	sortKey     string
}

func discoverNamespaces(selfNamespaceID uint64, procScan *procScanResult) ([]discoveredNamespace, error) {
	if selfNamespaceID == 0 {
		var err error
		if selfNamespaceID, err = readNamespaceID("/proc/self/ns/net"); err != nil {
			return nil, err
		}
	}

	rootID, err := readNamespaceID("/proc/1/ns/net")
	if err != nil {
		if os.IsPermission(err) || errors.Is(err, unix.EPERM) || errors.Is(err, unix.EACCES) {
			rootID = selfNamespaceID
		} else {
			return nil, err
		}
	}

	if procScan == nil {
		scan := scanProcfs(false)
		procScan = &scan
	}

	byID := make(map[uint64]discoveredNamespace)

	if file, err := os.Open("/proc/self/mountinfo"); err == nil {
		scanner := bufio.NewScanner(file)
		for scanner.Scan() {
			fields := strings.Fields(scanner.Text())
			sepIdx := -1
			for i, f := range fields {
				if f == "-" {
					sepIdx = i
					break
				}
			}
			if sepIdx == -1 || len(fields) <= sepIdx+1 || fields[sepIdx+1] != "nsfs" {
				continue
			}

			if nsID, ok := parseNamespaceToken(fields[3]); ok {
				path := fields[4]
				byID[nsID] = discoveredNamespace{
					namespaceID: nsID,
					mountPoint:  path,
					displayName: path,
					shortName:   filepath.Base(path),
					isCurrent:   nsID == selfNamespaceID,
					sortKey:     path,
				}
			}
		}
		file.Close()
	}

	for nsID, ref := range procScan.namespaces {
		if _, exists := byID[nsID]; !exists {
			byID[nsID] = discoveredNamespace{
				namespaceID: nsID,
				mountPoint:  ref.path,
				displayName: ref.path,
				shortName:   ref.path,
				isCurrent:   nsID == selfNamespaceID,
				sortKey:     ref.path,
			}
		}
	}

	byID[rootID] = discoveredNamespace{
		namespaceID: rootID,
		mountPoint:  "/proc/1/ns/net",
		displayName: "/proc/1/ns/net",
		shortName:   "root",
		isRoot:      true,
		isCurrent:   rootID == selfNamespaceID,
	}

	if selfNamespaceID != rootID {
		byID[selfNamespaceID] = discoveredNamespace{
			namespaceID: selfNamespaceID,
			mountPoint:  "/proc/self/ns/net",
			displayName: "/proc/self/ns/net",
			shortName:   "self",
			isCurrent:   true,
			sortKey:     "/proc/self/ns/net",
		}
	} else {
		item := byID[rootID]
		item.mountPoint = "/proc/self/ns/net"
		item.displayName = "/proc/self/ns/net"
		byID[rootID] = item
	}

	items := make([]discoveredNamespace, 0, len(byID))
	for _, item := range byID {
		items = append(items, item)
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].isRoot != items[j].isRoot {
			return items[i].isRoot
		}
		return items[i].sortKey < items[j].sortKey
	})

	return items, nil
}

func readNamespaceID(path string) (uint64, error) {
	file, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer file.Close()

	var stat unix.Stat_t
	if err := unix.Fstat(int(file.Fd()), &stat); err != nil {
		return 0, err
	}
	if stat.Ino == 0 {
		return 0, errors.New("invalid namespace inode")
	}
	return stat.Ino, nil
}

func parseNamespaceToken(s string) (uint64, bool) {
	if !strings.HasPrefix(s, "net:[") || !strings.HasSuffix(s, "]") {
		return 0, false
	}
	num := strings.TrimSuffix(strings.TrimPrefix(s, "net:["), "]")
	v, err := strconv.ParseUint(num, 10, 64)
	if err != nil {
		return 0, false
	}
	return v, true
}

func permissionText(err error) string {
	if err == nil {
		return ""
	}
	if os.IsPermission(err) || errors.Is(err, unix.EPERM) || errors.Is(err, unix.EACCES) {
		return "no permission"
	}
	return err.Error()
}

func (s *Store) syncNamespaces() error {
	return s.syncNamespacesWithProcScan(nil)
}

func (s *Store) syncNamespacesWithProcScan(procScan *procScanResult) error {
	debuglog.Tracef("store.syncNamespaces")
	discovered, err := discoverNamespaces(s.inventory.selfNamespaceID, procScan)
	if err != nil {
		return err
	}

	nextStates := make(map[uint64]*namespaceState, len(discovered))
	nextList := make([]types.NamespaceInfo, 0, len(discovered))

	for _, item := range discovered {
		state, ok := s.inventory.namespacesByID[item.namespaceID]
		if ok && s.reuseNamespaceState(state, item) {
			nextStates[item.namespaceID] = state
			s.ensureNamespaceTopology(item.namespaceID)
			nextList = append(nextList, state.info)
			continue
		}
		if ok {
			s.closeNamespaceState(state)
		}
		state = s.openNamespaceState(item)
		nextStates[item.namespaceID] = state
		s.ensureNamespaceTopology(item.namespaceID)
		nextList = append(nextList, state.info)
	}
	s.closeRemovedNamespaceStates(nextStates)

	s.inventory.namespacesByID = nextStates
	s.inventory.namespaces = nextList
	s.pruneNamespaceCaches(nextStates)
	return nil
}

func (s *Store) ensureNamespaceTopology(namespaceID uint64) {
	if _, ok := s.inventory.topology[namespaceID]; !ok {
		s.inventory.topology[namespaceID] = newTopologyState()
	}
}

func (s *Store) reuseNamespaceState(state *namespaceState, item discoveredNamespace) bool {
	if state == nil {
		return false
	}
	if state.mountPoint != item.mountPoint || state.info.IsRoot != item.isRoot || state.info.IsCurrent != item.isCurrent {
		return false
	}
	state.info.DisplayName = item.displayName
	state.info.MountPoint = item.mountPoint
	state.info.ShortName = item.shortName
	state.info.IsCurrent = item.isCurrent
	if state.handle != nil {
		state.info.PermissionErr = ""
		return true
	}
	handle, nsHandle, handleErr := newNamespaceHandle(item)
	state.handle = handle
	state.nsHandle = nsHandle
	state.info.PermissionErr = permissionText(handleErr)
	return true
}

func (s *Store) openNamespaceState(item discoveredNamespace) *namespaceState {
	info := types.NamespaceInfo{
		ID:          item.namespaceID,
		MountPoint:  item.mountPoint,
		DisplayName: item.displayName,
		ShortName:   item.shortName,
		IsRoot:      item.isRoot,
		IsCurrent:   item.isCurrent,
	}
	handle, nsHandle, handleErr := newNamespaceHandle(item)
	info.PermissionErr = permissionText(handleErr)
	return &namespaceState{
		info:       info,
		mountPoint: item.mountPoint,
		handle:     handle,
		nsHandle:   nsHandle,
	}
}

func (s *Store) closeNamespaceState(state *namespaceState) {
	if state == nil {
		return
	}
	if state.handle != nil {
		state.handle.Close()
	}
	if state.nsHandle.IsOpen() {
		_ = state.nsHandle.Close()
	}
}

func (s *Store) closeRemovedNamespaceStates(nextStates map[uint64]*namespaceState) {
	for id, state := range s.inventory.namespacesByID {
		if _, ok := nextStates[id]; ok {
			continue
		}
		debuglog.Infof("store.syncNamespaces remove namespace=%d", id)
		s.closeNamespaceState(state)
	}
}

func newNamespaceHandle(item discoveredNamespace) (*netlink.Handle, netns.NsHandle, error) {
	if item.isCurrent {
		// unprivileged-safe behavior for the main handle:
		handle, err := netlink.NewHandleAt(netns.None())
		if err != nil {
			return nil, netns.None(), err
		}
		// Best-effort fd for bridge netlink dump path.
		nsHandle, err := netns.GetFromPath(item.mountPoint)
		if err != nil {
			return handle, netns.None(), nil
		}
		return handle, nsHandle, nil
	}

	nsHandle, err := netns.GetFromPath(item.mountPoint)
	if err != nil {
		return nil, netns.None(), err
	}
	handle, err := netlink.NewHandleAt(nsHandle)
	if err != nil {
		_ = nsHandle.Close()
		return nil, netns.None(), err
	}
	return handle, nsHandle, nil
}

func (s *Store) namespaceStates() []*namespaceState {
	out := make([]*namespaceState, 0, len(s.inventory.namespaces))
	for _, ns := range s.inventory.namespaces {
		state := s.inventory.namespacesByID[ns.ID]
		if state == nil {
			continue
		}
		out = append(out, state)
	}
	return out
}

func (s *Store) pruneNamespaceCaches(states map[uint64]*namespaceState) {
	metaChanged := false
	drop := func(keys []string, fn func(string)) {
		for _, key := range keys {
			fn(key)
			metaChanged = true
		}
	}
	if dropMissingNamespaceMap(s.inventory.topology, states) {
		metaChanged = true
	}
	dropMissingNamespaceMap(s.runtimeState.processes, states)
	dropMissingNamespaceMap(s.runtimeState.links, states)
	dropMissingNamespaceMap(s.referenceState.vrfUsedIfByNS, states)
	dropMissingNamespaceMap(s.referenceState.vrfUsedIfCompactByNS, states)
	dropMissingNamespaceMap(s.referenceState.vrfUsedIfCompactHold, states)
	dropMissingNamespaceMap(s.referenceState.bridgePortUsedByNS, states)
	dropMissingNamespaceMap(s.reloadState.ns, states)

	drop(staleRecordKeys(s.recordState.processRecords, states), func(key string) {
		delete(s.recordState.processRecords, key)
		delete(s.recordState.processMeta, key)
		delete(s.runtimeState.processPrev, key)
	})
	drop(staleRecordKeys(s.recordState.linkRecords, states), func(key string) {
		delete(s.recordState.linkRecords, key)
		delete(s.recordState.linkMeta, key)
		delete(s.runtimeState.linkHistory, key)
	})
	drop(staleRecordKeys(s.recordState.neighMeta, states), func(key string) {
		delete(s.recordState.neighMeta, key)
	})
	drop(staleRecordKeys(s.recordState.fdbMeta, states), func(key string) {
		delete(s.recordState.fdbMeta, key)
	})
	drop(staleRecordKeys(s.recordState.routeMeta, states), func(key string) {
		delete(s.recordState.routeMeta, key)
	})

	s.rebuildInterfaceIndexes()
	s.rebuildReferenceMaps()
	if metaChanged {
		debuglog.Tracef("store.pruneNamespaceCaches meta changed")
		s.bumpMetaRevisionLocked()
	}
}

func dropMissingNamespaceMap[T any](m map[uint64]T, states map[uint64]*namespaceState) bool {
	changed := false
	for nsID := range m {
		if _, ok := states[nsID]; ok {
			continue
		}
		delete(m, nsID)
		changed = true
	}
	return changed
}

func staleRecordKeys[T any](m map[string]T, states map[uint64]*namespaceState) []string {
	out := make([]string, 0)
	for key := range m {
		nsID, ok := recordNamespaceID(key)
		if !ok {
			continue
		}
		if _, exists := states[nsID]; exists {
			continue
		}
		out = append(out, key)
	}
	return out
}
