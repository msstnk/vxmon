package constants

import (
	"time"

	"golang.org/x/sys/unix"
)

const (
	AppTitle                  = "vxmon 0.1.4"
	ClockTickInterval         = time.Second
	AnimTickInterval          = 100 * time.Millisecond
	NLMsgThrottleInterval     = 50 * time.Millisecond
	NamespaceResyncInterval   = 3 * time.Second
	RuntimeRefreshInterval    = 5 * time.Second
	LinkRateHistoryDepth      = 4
	LinkRateMaxSampleInterval = 10 * time.Second
	FadeDuration              = 2400 * time.Millisecond
	DefaultTopPanePercent     = 50
	MinTopPanePercent         = 30
	MaxTopPanePercent         = 60
	TopPanePercentStep        = 10
	VrfCompactReferenceHold   = 100 * time.Millisecond
	RootNamespaceLabel        = "Root Namespace"
	DefaultVRFName            = "Default VRF"
	DefaultVRFTableID         = unix.RT_TABLE_DEFAULT
	MainRouteTableID          = unix.RT_TABLE_MAIN
)
