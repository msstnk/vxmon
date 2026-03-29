package types

import (
	"time"

	"github.com/msstnk/vxmon/internal/constants"
)

func NewLinkSampleRing() *LinkSampleRing {
	size := constants.LinkRateHistoryDepth
	return &LinkSampleRing{
		buffer: make([]linkSample, size),
		epochs: make([]time.Time, size),
	}
}

func (r *LinkSampleRing) Push(rxBytes, txBytes uint64, at time.Time) {
	if r.count > 0 {
		lastIdx := (r.pos - 1 + len(r.buffer)) % len(r.buffer)
		if !r.epochs[lastIdx].IsZero() && at.Sub(r.epochs[lastIdx]) < constants.MinimumLinkSampleInterval {
			r.buffer[lastIdx] = linkSample{rxBytes: rxBytes, txBytes: txBytes}
			r.epochs[lastIdx] = at
			return
		}
	}
	r.buffer[r.pos] = linkSample{rxBytes: rxBytes, txBytes: txBytes}
	r.epochs[r.pos] = at
	r.pos = (r.pos + 1) % len(r.buffer)
	if r.count < len(r.buffer) {
		r.count++
	}
}

func (r *LinkSampleRing) newest() (linkSample, time.Time, bool) {
	if r.count == 0 {
		return linkSample{}, time.Time{}, false
	}
	idx := (r.pos - 1 + len(r.buffer)) % len(r.buffer)
	return r.buffer[idx], r.epochs[idx], true
}

func (r *LinkSampleRing) AverageBps(maxWindow time.Duration) (uint64, uint64) {
	newest, newestAt, ok := r.newest()
	if !ok || r.count < 2 {
		return 0, 0
	}

	var oldest linkSample
	var oldestAt time.Time
	found := false
	lastIdx := (r.pos - 1 + len(r.buffer)) % len(r.buffer)
	for i := 1; i < r.count; i++ {
		idx := (lastIdx - i + len(r.buffer)) % len(r.buffer)
		at := r.epochs[idx]
		if at.IsZero() || !newestAt.After(at) {
			continue
		}
		if newestAt.Sub(at) > maxWindow {
			break
		}
		oldest, oldestAt, found = r.buffer[idx], at, true
	}
	if !found {
		return 0, 0
	}
	elapsed := newestAt.Sub(oldestAt).Seconds()
	if elapsed <= 0 {
		return 0, 0
	}
	bps := func(n, o uint64) uint64 {
		if n < o {
			return 0
		}
		return uint64(float64(n-o) * 8.0 / elapsed)
	}
	return bps(newest.rxBytes, oldest.rxBytes), bps(newest.txBytes, oldest.txBytes)
}
