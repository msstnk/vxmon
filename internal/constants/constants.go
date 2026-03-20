package constants

import "time"

const (
	AppTitle                = "vxmon 0.1.3"
	ClockTickInterval       = time.Second
	AnimTickInterval        = 100 * time.Millisecond
	NLMsgThrottleInterval   = 50 * time.Millisecond
	NamespaceResyncInterval = 3 * time.Second
	RuntimeRefreshInterval  = 5 * time.Second
	FadeDuration            = 2400 * time.Millisecond
	DefaultTopPanePercent   = 50
	MinTopPanePercent       = 30
	MaxTopPanePercent       = 60
	TopPanePercentStep      = 10

	RootNamespaceLabel      = "Root Namespace"
	DefaultVRFName          = "Default VRF"
	DefaultVRFTableID       = uint32(254)
	MainRouteTableID        = uint32(255)
	VrfCompactReferenceHold = 2000 * time.Millisecond
)
