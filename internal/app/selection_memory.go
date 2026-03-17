package app

func bottomModesForTop(mode TopMode) []BottomMode {
	switch mode {
	case TopVRF:
		return []BottomMode{BottomRoute, BottomNeigh}
	case TopNETNS:
		return []BottomMode{BottomLink, BottomProcess}
	default:
		return []BottomMode{BottomFDB}
	}
}

func isBottomModeAllowed(topMode TopMode, bottomMode BottomMode) bool {
	for _, mode := range bottomModesForTop(topMode) {
		if mode == bottomMode {
			return true
		}
	}
	return false
}

func (m *Model) rememberBottomMode() {
	if m.savedBottomModes == nil {
		m.savedBottomModes = make(map[TopMode]BottomMode, len(topModeCycle))
	}
	if isBottomModeAllowed(m.topMode, m.botMode) {
		m.savedBottomModes[m.topMode] = m.botMode
	}
}

func (m *Model) bottomModeForTop(mode TopMode) BottomMode {
	if isBottomModeAllowed(mode, m.savedBottomModes[mode]) {
		return m.savedBottomModes[mode]
	}
	return defaultBottomMode(mode)
}
