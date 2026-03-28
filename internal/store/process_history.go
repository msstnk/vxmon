package store

import (
	"runtime"
	"sort"
	"strconv"

	"github.com/msstnk/vxmon/internal/types"
)

type processSample struct {
	cpuTicks uint64
}

type processRaw struct {
	totalCPU  uint64
	processes []rawProcess
}

func collectProcessRaw(scan procScanResult) processRaw {
	return processRaw{
		totalCPU:  readTotalCPUTime(),
		processes: scan.processes,
	}
}

func (s *Store) updateProcessHistory(raw processRaw) map[uint64][]types.ProcessInfo {
	var totalDelta uint64
	if s.runtimeState.prevTotalCPU > 0 && raw.totalCPU >= s.runtimeState.prevTotalCPU {
		totalDelta = raw.totalCPU - s.runtimeState.prevTotalCPU
	}
	s.runtimeState.prevTotalCPU = raw.totalCPU

	rows := make(map[uint64][]types.ProcessInfo, len(s.inventory.namespaces))
	for _, proc := range raw.processes {
		key := processSampleKey(proc.namespaceID, proc.pid)
		prev := s.runtimeState.processPrev[key]

		loadPct := 0.0
		if totalDelta > 0 && proc.cpuTicks >= prev.cpuTicks {
			loadPct = float64(proc.cpuTicks-prev.cpuTicks) / float64(totalDelta) * float64(runtime.NumCPU()) * 100.0
		}

		rows[proc.namespaceID] = append(rows[proc.namespaceID], types.ProcessInfo{
			NamespaceID: proc.namespaceID,
			PID:         proc.pid,
			Exe:         proc.exe,
			User:        proc.user,
			LoadPct:     loadPct,
		})
		s.runtimeState.processPrev[key] = processSample{cpuTicks: proc.cpuTicks}
	}

	for nsID := range rows {
		sort.Slice(rows[nsID], func(i, j int) bool {
			if rows[nsID][i].LoadPct != rows[nsID][j].LoadPct {
				return rows[nsID][i].LoadPct > rows[nsID][j].LoadPct
			}
			return rows[nsID][i].PID < rows[nsID][j].PID
		})
	}
	return rows
}

func processSampleKey(nsID uint64, pid int) string {
	return recordPrefix(nsID) + strconv.Itoa(pid)
}
