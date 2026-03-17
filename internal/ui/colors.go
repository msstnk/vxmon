package ui

import (
	"math"
	"strconv"

	"github.com/charmbracelet/lipgloss"

	"github.com/msstnk/vxmon/internal/constants"
	"github.com/msstnk/vxmon/internal/store"
)

const (
	gamma              = 2.2
	invGamma           = 1.0 / gamma
	maxColorValue8     = 255.0
	removedFaintCutoff = 0.65
)

type linearRGB struct{ R, G, B float64 }

var (
	linearFrom8LUT = buildLinearFrom8LUT()

	linFadeAddedStart   = hexToLinear(HexFadeAddedStart)
	linFadeUpdatedStart = hexToLinear(HexFadeUpdatedStart)
	linFadeRemovedStart = hexToLinear(HexFadeRemovedStart)
	linFadeNeutralEnd   = hexToLinear(HexFadeNeutralEnd)
	linFadeRemovedEnd   = hexToLinear(HexFadeRemovedEnd)
)

// FadeStyle is called from app/bottom_view when rendering animated row states.
func FadeStyle(meta store.Meta, nowUnixNano int64, base lipgloss.Style) lipgloss.Style {
	if meta.State == store.StateNone {
		return base
	}

	changed := meta.ChangedAt.UnixNano()
	if nowUnixNano < changed {
		nowUnixNano = changed
	}

	dur := constants.FadeDuration.Nanoseconds()
	elapsed := nowUnixNano - changed
	if elapsed >= dur {
		if meta.State == store.StateRemoved {
			return base.Foreground(lipgloss.Color(HexFadeRemovedEnd)).Faint(true)
		}
		return base
	}

	p := clamp01(float64(elapsed) / float64(dur))

	switch meta.State {
	case store.StateAdded, store.StateUpdated:
		start := linFadeAddedStart
		if meta.State == store.StateUpdated {
			start = linFadeUpdatedStart
		}
		end := styleForegroundLinear(base, linFadeNeutralEnd)
		return base.Foreground(lerpLinearHex(start, end, p))
	case store.StateRemoved:
		col := lerpLinearHex(linFadeRemovedStart, linFadeRemovedEnd, p)
		st := base.Foreground(col)
		if p > removedFaintCutoff {
			st = st.Faint(true)
		}
		return st
	default:
		return base
	}
}

func hexToLinear(s string) linearRGB {
	r, g, b, ok := parseHexColor(s)
	if !ok {
		return linearFrom8(255, 255, 255)
	}
	return linearFrom8(r, g, b)
}

func parseHexColor(s string) (r, g, b uint8, ok bool) {
	if len(s) != 7 || s[0] != '#' {
		return 0, 0, 0, false
	}

	u, err := strconv.ParseUint(s[1:], 16, 32)
	if err != nil {
		return 0, 0, 0, false
	}

	return uint8(u >> 16), uint8(u >> 8), uint8(u), true
}

func styleForegroundLinear(style lipgloss.Style, fallback linearRGB) linearRGB {
	fg := style.GetForeground()
	if fg == nil {
		return fallback
	}
	r, g, b, a := fg.RGBA()
	if a == 0 {
		return fallback
	}

	// color.Color returns premultiplied RGBA; convert back to non-premultiplied sRGB.
	fr := float64(r) / float64(a)
	fg2 := float64(g) / float64(a)
	fb := float64(b) / float64(a)

	return linearRGB{
		R: math.Pow(clamp01(fr), gamma),
		G: math.Pow(clamp01(fg2), gamma),
		B: math.Pow(clamp01(fb), gamma),
	}
}

func buildLinearFrom8LUT() [256]float64 {
	var lut [256]float64
	for i := range lut {
		lut[i] = math.Pow(float64(i)/maxColorValue8, gamma)
	}
	return lut
}

func linearFrom8(r, g, b uint8) linearRGB {
	return linearRGB{
		R: linearFrom8LUT[r],
		G: linearFrom8LUT[g],
		B: linearFrom8LUT[b],
	}
}

func lerpLinearHex(start, end linearRGB, t float64) lipgloss.Color {
	t = clamp01(t)
	r := toSRGB8(start.R + (end.R-start.R)*t)
	g := toSRGB8(start.G + (end.G-start.G)*t)
	b := toSRGB8(start.B + (end.B-start.B)*t)
	return lipgloss.Color(hexColor(r, g, b))
}

func toSRGB8(v float64) uint8 {
	v = clamp01(v)
	return uint8(math.Round(math.Pow(v, invGamma) * maxColorValue8))
}

func clamp01(v float64) float64 {
	return math.Max(0, math.Min(1, v))
}

func hexColor(r, g, b uint8) string {
	const digits = "0123456789abcdef"
	buf := [7]byte{'#', 0, 0, 0, 0, 0, 0}
	buf[1], buf[2] = digits[r>>4], digits[r&0x0f]
	buf[3], buf[4] = digits[g>>4], digits[g&0x0f]
	buf[5], buf[6] = digits[b>>4], digits[b&0x0f]
	return string(buf[:])
}
