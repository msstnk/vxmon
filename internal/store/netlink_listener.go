package store

import (
	"context"
	"time"

	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netns"

	"github.com/msstnk/vxmon/internal/constants"
	"github.com/msstnk/vxmon/internal/debuglog"
)

// netlink_listener.go subscribes to kernel netlink streams for every discovered namespace.

type namespaceSubscription struct {
	id   uint64
	done chan struct{}
}

func ListenNetlink(ctx context.Context, selfNamespaceID uint64, send func(any)) {
	debuglog.Infof("store.ListenNetlink start")
	subs := map[uint64]*namespaceSubscription{}
	resync := func() {
		targets, err := discoverNamespaces(selfNamespaceID, nil)
		if err != nil {
			debuglog.Errorf("store.ListenNetlink discoverNamespaces failed: %v", err)
			return
		}

		current := make(map[uint64]discoveredNamespace, len(targets))
		for _, target := range targets {
			current[target.namespaceID] = target
		}

		changed := false
		newTargets := make([]discoveredNamespace, 0)
		for id, sub := range subs {
			if _, ok := current[id]; ok {
				continue
			}
			debuglog.Infof("store.ListenNetlink unsubscribe namespace=%d", id)
			close(sub.done)
			delete(subs, id)
			changed = true
		}

		for _, target := range targets {
			if _, ok := subs[target.namespaceID]; ok {
				continue
			}
			newTargets = append(newTargets, target)
			changed = true
		}

		if changed {
			send(NamespaceSyncMsg{At: time.Now()})
		}

		for _, target := range newTargets {
			done := make(chan struct{})
			if err := startNamespaceSubscription(ctx, target, done, send); err != nil {
				debuglog.Errorf("store.ListenNetlink subscribe namespace=%d failed: %v", target.namespaceID, err)
				continue
			}
			subs[target.namespaceID] = &namespaceSubscription{id: target.namespaceID, done: done}
			debuglog.Infof("store.ListenNetlink subscribe namespace=%d path=%s", target.namespaceID, target.mountPoint)
		}
	}

	resync()
	ticker := time.NewTicker(constants.NamespaceResyncInterval)
	defer ticker.Stop()
	defer func() {
		for _, sub := range subs {
			close(sub.done)
		}
	}()

	for {
		select {
		case <-ctx.Done():
			debuglog.Infof("store.ListenNetlink stop")
			return
		case <-ticker.C:
			resync()
		}
	}
}

func startNamespaceSubscription(ctx context.Context, target discoveredNamespace, done chan struct{}, send func(any)) error {
	debuglog.Tracef("store.startNamespaceSubscription namespace=%d path=%s", target.namespaceID, target.mountPoint)
	ns := netns.None()
	if !target.isCurrent {
		nsHandle, err := netns.GetFromPath(target.mountPoint)
		if err != nil {
			return err
		}
		ns = nsHandle
		defer nsHandle.Close()
	}

	neighCh := make(chan netlink.NeighUpdate)
	routeCh := make(chan netlink.RouteUpdate)
	linkCh := make(chan netlink.LinkUpdate)

	if err := netlink.NeighSubscribeAt(ns, neighCh, done); err != nil {
		return err
	}
	if err := netlink.RouteSubscribeAt(ns, routeCh, done); err != nil {
		return err
	}
	if err := netlink.LinkSubscribeAt(ns, linkCh, done); err != nil {
		return err
	}

	go func() {
		nsInfo := ListenerNamespace{
			ID:        target.namespaceID,
			Path:      target.mountPoint,
			ShortName: target.shortName,
			IsRoot:    target.isRoot,
		}
		for {
			select {
			case <-ctx.Done():
				return
			case <-done:
				return
			case u := <-neighCh:
				debuglog.Tracef("store.ListenNetlink neigh namespace=%d type=%d", target.namespaceID, u.Type)
				send(NeighNLMsg{Namespace: nsInfo, Update: u, At: time.Now()})
			case u := <-routeCh:
				debuglog.Tracef("store.ListenNetlink route namespace=%d type=%d", target.namespaceID, u.Type)
				send(RouteNLMsg{Namespace: nsInfo, Update: u, At: time.Now()})
			case u := <-linkCh:
				debuglog.Tracef("store.ListenNetlink link namespace=%d type=%d", target.namespaceID, u.Type)
				send(LinkNLMsg{Namespace: nsInfo, Update: u, At: time.Now()})
			}
		}
	}()

	return nil
}
