package store

import (
	"fmt"
	"time"

	"github.com/msstnk/vxmon/internal/constants"
)

type linkSample struct {
	rxBytes uint64
	txBytes uint64
}

type linkSampleRing struct {
	buffer []linkSample
	epochs []time.Time
	pos    int
	count  int
}

func newLinkSampleRing() *linkSampleRing {
	size := constants.LinkRateHistoryDepth
	return &linkSampleRing{
		buffer: make([]linkSample, size),
		epochs: make([]time.Time, size),
	}
}

func (r *linkSampleRing) push(sample linkSample, at time.Time) {
	r.buffer[r.pos] = sample
	r.epochs[r.pos] = at
	r.pos = (r.pos + 1) % len(r.buffer)
	if r.count < len(r.buffer) {
		r.count++
	}
}

func (r *linkSampleRing) newest() (linkSample, time.Time, bool) {
	if r.count == 0 {
		return linkSample{}, time.Time{}, false
	}
	idx := (r.pos - 1 + len(r.buffer)) % len(r.buffer)
	return r.buffer[idx], r.epochs[idx], true
}

func (r *linkSampleRing) averageBps(maxWindow time.Duration) (uint64, uint64) {
	if r.count < 2 {
		return 0, 0
	}
	newestSample, newestAt, ok := r.newest()
	if !ok {
		return 0, 0
	}

	var oldestSample linkSample
	var oldestAt time.Time
	found := false
	for i := 1; i < r.count; i++ {
		idx := (r.pos - 1 - i + len(r.buffer)) % len(r.buffer)
		at := r.epochs[idx]
		if at.IsZero() || !newestAt.After(at) {
			continue
		}
		elapsed := newestAt.Sub(at)
		if elapsed > maxWindow {
			break
		}
		oldestSample = r.buffer[idx]
		oldestAt = at
		found = true
	}
	if !found {
		return 0, 0
	}

	elapsed := newestAt.Sub(oldestAt).Seconds()
	if elapsed <= 0 {
		return 0, 0
	}

	var rxBps uint64
	if newestSample.rxBytes >= oldestSample.rxBytes {
		rxBps = uint64(float64(newestSample.rxBytes-oldestSample.rxBytes) * 8.0 / elapsed)
	}
	var txBps uint64
	if newestSample.txBytes >= oldestSample.txBytes {
		txBps = uint64(float64(newestSample.txBytes-oldestSample.txBytes) * 8.0 / elapsed)
	}
	return rxBps, txBps
}

func updateLinkHistory(raw interfaceInfoRaw, namespaceID uint64, at time.Time, history map[string]*linkSampleRing) {
	if history == nil {
		return
	}
	for _, link := range raw.links {
		attrs := link.Attrs()
		if attrs == nil || attrs.Statistics == nil {
			continue
		}
		key := linkSampleKey(namespaceID, attrs.Index)
		ring := history[key]
		if ring == nil {
			ring = newLinkSampleRing()
			history[key] = ring
		}
		ring.push(linkSample{rxBytes: attrs.Statistics.RxBytes, txBytes: attrs.Statistics.TxBytes}, at)
	}
}

func linkSampleKey(nsID uint64, ifIndex int) string {
	return fmt.Sprintf("%d|%d", nsID, ifIndex)
}
