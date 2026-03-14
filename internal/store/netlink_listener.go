package store

import (
	"context"
	"time"

	"github.com/vishvananda/netlink"
)

// netlink_listener.go subscribes to kernel netlink streams and forwards typed messages.
// ListenNetlink is called from cmd/vxmon/main to push async updates into the UI loop.
func ListenNetlink(ctx context.Context, send func(any)) {
	neighCh := make(chan netlink.NeighUpdate)
	routeCh := make(chan netlink.RouteUpdate)
	linkCh := make(chan netlink.LinkUpdate)
	done := make(chan struct{})

	_ = netlink.NeighSubscribe(neighCh, done)
	_ = netlink.RouteSubscribe(routeCh, done)
	_ = netlink.LinkSubscribe(linkCh, done)

	for {
		select {
		case <-ctx.Done():
			close(done)
			return
		case u := <-neighCh:
			send(NeighNLMsg{Update: u, At: time.Now()})
		case u := <-routeCh:
			send(RouteNLMsg{Update: u, At: time.Now()})
		case u := <-linkCh:
			send(LinkNLMsg{Update: u, At: time.Now()})
		}
	}
}
