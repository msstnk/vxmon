package store

import (
	"time"

	"github.com/vishvananda/netlink"
)

// messages.go defines netlink event messages sent into the Bubble Tea loop.

type ListenerNamespace struct {
	ID        uint64
	Path      string
	ShortName string
	IsRoot    bool
}

type NeighNLMsg struct {
	Namespace ListenerNamespace
	Update    netlink.NeighUpdate
	At        time.Time
}

type RouteNLMsg struct {
	Namespace ListenerNamespace
	Update    netlink.RouteUpdate
	At        time.Time
}

type LinkNLMsg struct {
	Namespace ListenerNamespace
	Update    netlink.LinkUpdate
	At        time.Time
}

type NamespaceSyncMsg struct {
	At time.Time
}

type NamespaceSubscribedMsg struct {
	NamespaceID uint64
	At          time.Time
}

// InventoryUpdatedMsg is emitted by the store loop after a full update cycle is applied.
type InventoryUpdatedMsg struct {
	At time.Time
}
type InventoryPeriodicUpdatedMsg struct {
	At time.Time
}
