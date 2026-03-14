package store

import (
	"time"

	"github.com/vishvananda/netlink"
)

// messages.go defines netlink event messages sent into the Bubble Tea loop.

type NeighNLMsg struct {
	Update netlink.NeighUpdate
	At     time.Time
}

type RouteNLMsg struct {
	Update netlink.RouteUpdate
	At     time.Time
}

type LinkNLMsg struct {
	Update netlink.LinkUpdate
	At     time.Time
}
