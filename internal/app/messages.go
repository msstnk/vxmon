package app

import "time"

// messages.go defines internal Bubble Tea timer messages for the app model.
type clockTickMsg time.Time

type animTickMsg time.Time

type nlReloadKind uint8

const (
	nlReloadInterfaces nlReloadKind = iota
	nlReloadNeighAndFDB
	nlReloadRoutes
	nlReloadLinks
)

type nlReloadMask uint8

type nlReloadTickMsg struct {
	NamespaceID uint64
	At          time.Time
}
