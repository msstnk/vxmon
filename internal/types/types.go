package types

// types.go defines shared data models used across store, app, and ui packages.
type NamespaceInfo struct {
	ID            uint64
	MountPoint    string
	DisplayName   string
	ShortName     string
	IsRoot        bool
	IsCurrent     bool
	SocketUsed    uint64
	TCPInUse      uint64
	UDPInUse      uint64
	TCP6InUse     uint64
	UDP6InUse     uint64
	PermissionErr string
}

type InterfaceInfo struct {
	NamespaceID      uint64
	NamespaceName    string
	NamespaceDisplay string
	NamespaceRoot    bool
	IfIndex          int
	InterfaceName    string
	IfType           string
	Status           string
	OperState        string
	STPState         int
	BridgePortState  int
	TableID          uint32
	VxlanID          int
	VLANID           int
	ParentID         int
	MasterID         int
	ParentName       string
	MasterName       string
	MACAddr          string
}

type FdbEntry struct {
	NamespaceID      uint64
	NamespaceName    string
	NamespaceDisplay string
	NamespaceRoot    bool
	BridgeID         int
	BridgeName       string
	MACAddr          string
	State            int
	VxlanID          int
	VLANID           int
	RemoteVTEP       string
	PortID           int
	PortName         string
}

type NeighEntry struct {
	NamespaceID      uint64
	NamespaceName    string
	NamespaceDisplay string
	NamespaceRoot    bool
	IP               string
	MACAddr          string
	State            int
	IfIndex          int
	InterfaceName    string
	VLANID           int
	VxlanID          int
	MasterID         int
}

type Nexthop struct {
	Gw  string
	Dev string
}

type RouteEntry struct {
	NamespaceID      uint64
	NamespaceName    string
	NamespaceDisplay string
	NamespaceRoot    bool
	IfIndex          int
	Dst              string
	Prefix           int
	Src              string
	Table            uint32
	Priority         int
	Scope            int
	Type             int
	Protocol         int
	Nexthops         []Nexthop
}

type ProcessInfo struct {
	NamespaceID uint64
	PID         int
	Exe         string
	User        string
	LoadPct     float64
}

type NamespaceLinkInfo struct {
	NamespaceID uint64
	IfIndex     int
	Name        string
	Type        string
	RxBps       uint64
	TxBps       uint64
	RxErrors    uint64
	TxErrors    uint64
}
