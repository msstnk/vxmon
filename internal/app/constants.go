package app

import "time"

const (
	appTitle          = "VxMon 0.1.0"
	clockTickInterval = time.Second
	animTickInterval  = 100 * time.Millisecond

	defaultVRFName    = "Default VRF"
	defaultVRFTableID = uint32(254)
	mainRouteTableID  = uint32(255)
)
