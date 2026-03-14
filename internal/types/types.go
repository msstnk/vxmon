package types

// types.go defines shared data models used across store, app, and ui packages.
type InterfaceInfo struct {
	IfName          string
	IfType          string
	Status          string
	OperState       string
	STPState        string
	BridgePortState string
	TableID         uint32
	VxlanId         int
	VLANId          int
	ParentID        int
	MasterID        int
	ParentName      string
	MasterName      string
	HWAddr          string
}

type FdbEntry struct {
	BridgeID   int
	BridgeName string
	MacAddr    string
	IPAddr     string
	State      int
	VxlanId    int
	VLANId     int
	RemoteVTEP string
	PortID     int
	PortName   string
}

type NeighEntry struct {
	IP           string
	HardwareAddr string
	State        int
	InterfaceID  int
	Interface    string
}

type Nexthop struct {
	Gw  string
	Dev string
}

type RouteEntry struct {
	Dst      string
	Table    uint32
	Type     int
	Protocol int
	Nexthops []Nexthop
}
