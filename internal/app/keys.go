package app

import (
	"fmt"

	"github.com/msstnk/vxmon/internal/types"
)

func bridgeGroupKey(nsID uint64, name string) string {
	return fmt.Sprintf("%d|%s", nsID, name)
}

func bridgeParentKey(it bridgeItem) string {
	return "bridge|" + bridgeGroupKey(it.NamespaceID, it.Info.InterfaceName)
}

func bridgeChildKey(parent bridgeItem, it types.InterfaceInfo) string {
	return fmt.Sprintf("bridge-child|%d|%d|%d", parent.NamespaceID, parent.Info.IfIndex, it.IfIndex)
}

func vrfParentKey(it vrfItem) string {
	return fmt.Sprintf("vrf|%d|%s|%d", it.NamespaceID, it.Name, it.TableID)
}

func vrfChildKey(parent vrfItem, it types.InterfaceInfo) string {
	return fmt.Sprintf("vrf-child|%d|%d|%d", parent.NamespaceID, parent.IfIndex, it.IfIndex)
}

func netnsParentKey(it types.NamespaceInfo) string {
	return fmt.Sprintf("netns|%d", it.ID)
}
