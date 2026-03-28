package store

import (
	"time"

	"golang.org/x/sys/unix"

	"github.com/msstnk/vxmon/internal/constants"
	"github.com/msstnk/vxmon/internal/debuglog"
	"github.com/msstnk/vxmon/internal/helpers"
)

func (s *Store) IsVRFInterfaceReferenced(namespaceID uint64, ifIndex int, detailed bool) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if detailed {
		_, ok := s.referenceState.vrfUsedIfByNS[namespaceID][ifIndex]
		return ok
	}
	if _, ok := s.referenceState.vrfUsedIfCompactByNS[namespaceID][ifIndex]; ok {
		return true
	}
	expiry := s.referenceState.vrfUsedIfCompactHold[namespaceID][ifIndex]
	if expiry.IsZero() {
		return false
	}
	return time.Now().Before(expiry)
}

func (s *Store) IsBridgePortReferenced(namespaceID uint64, ifIndex int) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.referenceState.bridgePortUsedByNS[namespaceID][ifIndex]
	return ok
}

func (s *Store) rebuildReferenceMaps() {
	debuglog.Tracef("store.rebuildReferenceMaps called")
	now := time.Now()
	ifIndexByName := buildIfIndexByName(s.inventory.topology)
	vrfUsed, vrfUsedCompact := buildVrfReferences(s.inventory.topology, ifIndexByName)
	vrfUsedCompactHold := buildCompactHold(now, vrfUsedCompact, s.referenceState.vrfUsedIfCompactByNS, s.referenceState.vrfUsedIfCompactHold)
	bridgePortUsed := buildBridgePortReferences(s.inventory.topology)
	changed := !equalRefSets(vrfUsed, s.referenceState.vrfUsedIfByNS) ||
		!equalRefSets(vrfUsedCompact, s.referenceState.vrfUsedIfCompactByNS) ||
		!equalRefSets(bridgePortUsed, s.referenceState.bridgePortUsedByNS)

	s.referenceState.vrfUsedIfByNS = vrfUsed
	s.referenceState.vrfUsedIfCompactByNS = vrfUsedCompact
	s.referenceState.vrfUsedIfCompactHold = vrfUsedCompactHold
	s.referenceState.bridgePortUsedByNS = bridgePortUsed
	if changed {
		s.bumpMetaRevisionLocked()
	}
}

func buildIfIndexByName(topology map[uint64]topologyState) map[uint64]map[string]int {
	out := make(map[uint64]map[string]int, len(topology))
	for nsID, t := range topology {
		nameMap := make(map[string]int, len(t.ifaces))
		for _, iface := range t.ifaces {
			nameMap[iface.InterfaceName] = iface.IfIndex
		}
		out[nsID] = nameMap
	}
	return out
}

func addRef(dst map[uint64]map[int]struct{}, nsID uint64, ifIndex int) {
	if ifIndex <= 0 {
		return
	}
	if dst[nsID] == nil {
		dst[nsID] = make(map[int]struct{})
	}
	dst[nsID][ifIndex] = struct{}{}
}

func buildVrfReferences(topology map[uint64]topologyState, ifIndexByName map[uint64]map[string]int) (map[uint64]map[int]struct{}, map[uint64]map[int]struct{}) {
	vrfUsed := make(map[uint64]map[int]struct{}, len(topology))
	vrfUsedCompact := make(map[uint64]map[int]struct{}, len(topology))

	for _, t := range topology {
		for _, neigh := range t.neigh {
			addRef(vrfUsed, neigh.NamespaceID, neigh.IfIndex)
			if !helpers.IsMulticastIP(neigh.IP) {
				addRef(vrfUsedCompact, neigh.NamespaceID, neigh.IfIndex)
			}
		}
	}

	for _, t := range topology {
		for _, route := range t.routes {
			skipCompact := route.Type == unix.RTN_ANYCAST ||
				route.Type == unix.RTN_MULTICAST ||
				route.Type == unix.RTN_BROADCAST
			nameMap := ifIndexByName[route.NamespaceID]
			for _, nh := range route.Nexthops {
				ifIndex := nameMap[nh.Dev]
				addRef(vrfUsed, route.NamespaceID, ifIndex)
				if !skipCompact {
					addRef(vrfUsedCompact, route.NamespaceID, ifIndex)
				}
			}
		}
	}

	return vrfUsed, vrfUsedCompact
}

func buildCompactHold(now time.Time, vrfUsedCompact map[uint64]map[int]struct{}, prevCompact map[uint64]map[int]struct{}, prevHold map[uint64]map[int]time.Time) map[uint64]map[int]time.Time {
	out := make(map[uint64]map[int]time.Time, len(vrfUsedCompact))
	for nsID, held := range prevHold {
		for ifIndex, expiry := range held {
			if !expiry.After(now) {
				continue
			}
			if _, ok := vrfUsedCompact[nsID][ifIndex]; ok {
				continue
			}
			if out[nsID] == nil {
				out[nsID] = make(map[int]time.Time)
			}
			out[nsID][ifIndex] = expiry
		}
	}

	for nsID, prev := range prevCompact {
		for ifIndex := range prev {
			if _, ok := vrfUsedCompact[nsID][ifIndex]; ok {
				continue
			}
			if out[nsID] == nil {
				out[nsID] = make(map[int]time.Time)
			}
			if expiry, ok := out[nsID][ifIndex]; ok && expiry.After(now) {
				continue
			}
			out[nsID][ifIndex] = now.Add(constants.VrfCompactReferenceHold)
		}
	}
	return out
}

func buildBridgePortReferences(topology map[uint64]topologyState) map[uint64]map[int]struct{} {
	out := make(map[uint64]map[int]struct{}, len(topology))
	for _, t := range topology {
		for _, fdb := range t.fdb {
			addRef(out, fdb.NamespaceID, fdb.PortID)
		}
	}
	return out
}

func equalRefSets(a, b map[uint64]map[int]struct{}) bool {
	if len(a) != len(b) {
		return false
	}
	for nsID, as := range a {
		bs := b[nsID]
		if len(as) != len(bs) {
			return false
		}
		for k := range as {
			if _, ok := bs[k]; !ok {
				return false
			}
		}
	}
	return true
}
