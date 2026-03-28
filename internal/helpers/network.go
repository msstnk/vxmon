package helpers

import (
	"fmt"
	"net/netip"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

// IsMulticastIP reports whether the input string looks like a multicast IP.
func IsMulticastIP(s string) bool {
	addr, err := netip.ParseAddr(strings.TrimSpace(s))
	if err != nil {
		return strings.HasPrefix(s, "ff") || strings.HasPrefix(s, "224.")
	}
	return addr.IsMulticast()
}

func IsLinkLocalIP(s string) bool {
	addr, err := netip.ParseAddr(strings.TrimSpace(s))
	if err != nil {
		return strings.HasPrefix(s, "fe80::") || strings.HasPrefix(s, "169.254.")
	}
	return addr.IsLinkLocalUnicast()
}

func IpFamilyOrderFromAddrStr(s string) int {
	if strings.Contains(s, ":") {
		return 1
	}
	return 0
}

func RouteFamilyOrder(dst string, gw string) int {
	if strings.Contains(dst, ":") {
		return 1
	}
	if strings.Contains(dst, ".") {
		return 0
	}
	if strings.Contains(gw, ":") {
		return 1
	}
	return 0
}

func FormatBps(v uint64) string {
	if v == 0 {
		return ""
	}
	if v >= 1_000_000_000 {
		return fmt.Sprintf("%.1f Gbps", float64(v)/1_000_000_000.0)
	}
	if v >= 1_000_000 {
		return fmt.Sprintf("%.1f Mbps", float64(v)/1_000_000.0)
	}
	if v >= 1_000 {
		return fmt.Sprintf("%.1f Kbps", float64(v)/1_000.0)
	}
	return fmt.Sprintf("%d bps", v)
}

func FormatNeighState(state int) string {
	switch state {
	case 0x00:
		return "NONE"
	case unix.NUD_INCOMPLETE:
		return "INCOMPLETE"
	case unix.NUD_REACHABLE:
		return "REACHABLE"
	case unix.NUD_STALE:
		return "STALE"
	case unix.NUD_DELAY:
		return "DELAY"
	case unix.NUD_PROBE:
		return "PROBE"
	case unix.NUD_FAILED:
		return "FAILED"
	case unix.NUD_NOARP:
		return "NOARP"
	case unix.NUD_PERMANENT:
		return "PERMANENT"
	default:
		return fmt.Sprintf("0x%02x", state)
	}
}

func BridgeSTPStateLabel(n int) string {
	if n < 0 {
		return "-"
	}
	if n == 1 {
		return "enabled"
	}
	return "disabled"
}

func BridgePortStateLabel(n int) string {
	if n < 0 {
		return "-"
	}
	switch n {
	case 0:
		return "disabled"
	case 1:
		return "listening"
	case 2:
		return "learning"
	case 3:
		return "forwarding"
	case 4:
		return "blocking"
	default:
		return strconv.Itoa(n)
	}
}
