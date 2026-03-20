package store

import (
	"errors"
	"strconv"
	"strings"
	"syscall"

	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netlink/nl"
	"github.com/vishvananda/netns"
	"golang.org/x/sys/unix"

	"github.com/msstnk/vxmon/internal/debuglog"
	"github.com/msstnk/vxmon/internal/types"
)

func loadBridgeNetlinkStates(ns types.NamespaceInfo, nsHandle int) (map[int]int, map[int]int) {
	stpByIndex := map[int]int{}
	portByIndex := map[int]int{}
	if nsHandle < 0 {
		return stpByIndex, portByIndex
	}

	nsFD := netns.NsHandle(nsHandle)
	sock, err := nl.GetNetlinkSocketAt(nsFD, netns.None(), unix.NETLINK_ROUTE)
	if err != nil {
		debuglog.Tracef("store.loadBridgeNetlinkStates socket failed namespace=%d err=%v", ns.ID, err)
		return stpByIndex, portByIndex
	}
	sh := &nl.SocketHandle{Socket: sock}
	defer sh.Close()

	dump := func(family uint8, label string) error {
		req := nl.NewNetlinkRequest(unix.RTM_GETLINK, unix.NLM_F_DUMP)
		req.Sockets = map[int]*nl.SocketHandle{
			unix.NETLINK_ROUTE: sh,
		}
		msg := nl.NewIfInfomsg(int(family))
		req.AddData(msg)

		msgs, err := req.Execute(unix.NETLINK_ROUTE, unix.RTM_NEWLINK)
		if err != nil && !errors.Is(err, netlink.ErrDumpInterrupted) {
			return err
		}
		for _, m := range msgs {
			ans := nl.DeserializeIfInfomsg(m)
			index := int(ans.Index)
			attrs, err := nl.ParseRouteAttr(m[ans.Len():])
			if err != nil {
				continue
			}
			ifName := linkNameFromAttrs(attrs)
			for _, attr := range attrs {
				if attr.Attr.Type == unix.IFLA_LINKINFO {
					stp, ok := parseBridgeStpState(attr.Value)
					if ok {
						stpByIndex[index] = stp
					}
					continue
				}
				if attr.Attr.Type != unix.IFLA_PROTINFO|unix.NLA_F_NESTED {
					continue
				}
				port, ok := parseBridgePortState(attr.Value)
				if ok {
					portByIndex[index] = port
					debuglog.Tracef("store.loadBridgeNetlinkStates brport if=%s index=%d state=%d", ifName, index, port)
				}
			}
		}
		debuglog.Tracef("store.loadBridgeNetlinkStates dump=%s stp=%d brport=%d", label, len(stpByIndex), len(portByIndex))
		return nil
	}

	if err := dump(unix.AF_UNSPEC, "af_unspec"); err != nil {
		debuglog.Tracef("store.loadBridgeNetlinkStates dump failed namespace=%d err=%v", ns.ID, err)
		return stpByIndex, portByIndex
	}
	if len(portByIndex) == 0 {
		if err := dump(unix.AF_BRIDGE, "af_bridge"); err != nil {
			debuglog.Tracef("store.loadBridgeNetlinkStates dump failed namespace=%d err=%v", ns.ID, err)
			return stpByIndex, portByIndex
		}
	}

	return stpByIndex, portByIndex
}

func parseBridgeStpState(b []byte) (int, bool) {
	native := nl.NativeEndian()
	attrs, err := nl.ParseRouteAttr(b)
	if err != nil {
		return 0, false
	}
	for _, attr := range attrs {
		if attr.Attr.Type == unix.IFLA_INFO_DATA {
			info, err := nl.ParseRouteAttr(attr.Value)
			if err != nil {
				return 0, false
			}
			for _, item := range info {
				if item.Attr.Type != nl.IFLA_BR_STP_STATE {
					continue
				}
				if len(item.Value) >= 4 {
					return int(native.Uint32(item.Value[0:4])), true
				}
				if len(item.Value) >= 1 {
					return int(item.Value[0]), true
				}
				return 0, false
			}
		}
	}
	return 0, false
}

func parseBridgePortState(b []byte) (int, bool) {
	native := nl.NativeEndian()
	attrs, err := nl.ParseRouteAttr(b)
	if err != nil {
		return 0, false
	}
	if debuglog.Enabled(debuglog.LevelTrace) {
		types := make([]string, 0, len(attrs))
		for _, attr := range attrs {
			types = append(types, strconv.Itoa(int(attr.Attr.Type)))
		}
		debuglog.Tracef("store.bridgePortState netlink attrs=%s", strings.Join(types, ","))
	}
	for _, attr := range attrs {
		if attr.Attr.Type != nl.IFLA_BRPORT_STATE {
			continue
		}
		debuglog.Tracef("store.bridgePortState netlink raw len=%d val=%x", len(attr.Value), attr.Value)

		if len(attr.Value) >= 4 {
			return int(native.Uint32(attr.Value[0:4])), true
		}
		if len(attr.Value) >= 1 {
			return int(attr.Value[0]), true
		}
		return 0, false
	}
	return 0, false
}

func linkNameFromAttrs(attrs []syscall.NetlinkRouteAttr) string {
	for _, attr := range attrs {
		if attr.Attr.Type != unix.IFLA_IFNAME {
			continue
		}
		return strings.TrimRight(string(attr.Value), "\x00")
	}
	return ""
}
