package store

import (
	"bufio"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/msstnk/vxmon/internal/types"
)

type processSample struct {
	cpuTicks uint64
}

type rawProcess struct {
	namespaceID uint64
	pid         int
	exe         string
	user        string
	cpuTicks    uint64
}

type procNamespaceRef struct {
	path string
	pid  int
}

type procScanResult struct {
	processes  []rawProcess
	repPID     map[uint64]int
	namespaces map[uint64]procNamespaceRef
	nameCache  map[string]string
}

func (s *Store) reloadRuntime(now time.Time) error {
	scan := scanProcfs(true)
	if err := s.syncNamespacesWithProcScan(&scan); err != nil {
		return err
	}

	s.reloadProcesses(now, scan)
	s.lastRuntime = now
	return nil
}

func (s *Store) reloadProcesses(now time.Time, scan procScanResult) {
	totalCPU := readTotalCPUTime()
	procs := scan.processes
	repPID := scan.repPID

	for i := range s.namespaces {
		s.namespaces[i].SocketUsed = 0
		s.namespaces[i].TCPInUse = 0
		s.namespaces[i].UDPInUse = 0
		s.namespaces[i].TCP6InUse = 0
		s.namespaces[i].UDP6InUse = 0
		if state := s.namespacesByID[s.namespaces[i].ID]; state != nil {
			state.info = s.namespaces[i]
		}

		pid := repPID[s.namespaces[i].ID]
		if pid == 0 {
			continue
		}
		used, tcp4, udp4, tcp6, udp6, ok := readSockStats(pid)
		if !ok {
			continue
		}
		s.namespaces[i].SocketUsed = used
		s.namespaces[i].TCPInUse = tcp4
		s.namespaces[i].UDPInUse = udp4
		s.namespaces[i].TCP6InUse = tcp6
		s.namespaces[i].UDP6InUse = udp6
		if state := s.namespacesByID[s.namespaces[i].ID]; state != nil {
			state.info = s.namespaces[i]
		}
	}

	var totalDelta uint64
	if s.prevTotalCPU > 0 && totalCPU >= s.prevTotalCPU {
		totalDelta = totalCPU - s.prevTotalCPU
	}
	s.prevTotalCPU = totalCPU

	nextProcRows := make(map[uint64][]types.ProcessInfo, len(s.namespaces))
	for _, proc := range procs {
		key := processSampleKey(proc.namespaceID, proc.pid)
		prev := s.processPrev[key]

		loadPct := 0.0
		if totalDelta > 0 && proc.cpuTicks >= prev.cpuTicks {
			loadPct = float64(proc.cpuTicks-prev.cpuTicks) / float64(totalDelta) * float64(runtime.NumCPU()) * 100.0
		}

		nextProcRows[proc.namespaceID] = append(nextProcRows[proc.namespaceID], types.ProcessInfo{
			NamespaceID: proc.namespaceID,
			PID:         proc.pid,
			Exe:         proc.exe,
			User:        proc.user,
			LoadPct:     loadPct,
		})
		s.processPrev[key] = processSample{
			cpuTicks: proc.cpuTicks,
		}
	}

	for nsID := range nextProcRows {
		sort.Slice(nextProcRows[nsID], func(i, j int) bool {
			if nextProcRows[nsID][i].LoadPct != nextProcRows[nsID][j].LoadPct {
				return nextProcRows[nsID][i].LoadPct > nextProcRows[nsID][j].LoadPct
			}
			return nextProcRows[nsID][i].PID < nextProcRows[nsID][j].PID
		})
	}
	s.processes = nextProcRows
	flat := make([]types.ProcessInfo, 0, len(procs))
	for _, rows := range nextProcRows {
		flat = append(flat, rows...)
	}

	s.processRecords, s.processMeta, _ = reconcile(s.processRecords, s.processMeta, flat, processKey, processFingerprint, now)
}

func scanProcfs(includeProcessDetails bool) procScanResult {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return procScanResult{
			repPID:     map[uint64]int{},
			namespaces: map[uint64]procNamespaceRef{},
			nameCache:  map[string]string{},
		}
	}

	scan := procScanResult{
		repPID:     make(map[uint64]int, len(entries)),
		namespaces: make(map[uint64]procNamespaceRef, len(entries)),
		nameCache:  map[string]string{},
	}
	if includeProcessDetails {
		scan.processes = make([]rawProcess, 0, len(entries))
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		pid, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}

		nsPath := filepath.Join("/proc", entry.Name(), "ns/net")
		nsID, err := readNamespaceID(nsPath)
		if err != nil {
			continue
		}
		if current, ok := scan.repPID[nsID]; !ok || pid < current {
			scan.repPID[nsID] = pid
		}
		if current, ok := scan.namespaces[nsID]; !ok || pid < current.pid {
			scan.namespaces[nsID] = procNamespaceRef{
				path: nsPath,
				pid:  pid,
			}
		}

		if !includeProcessDetails {
			continue
		}

		exe := readExeBase(pid)
		userName := readProcessUser(entry.Name(), scan.nameCache)
		cpuTicks, ok := readProcStat(pid)
		if !ok {
			continue
		}
		scan.processes = append(scan.processes, rawProcess{
			namespaceID: nsID,
			pid:         pid,
			exe:         exe,
			user:        userName,
			cpuTicks:    cpuTicks,
		})
	}

	return scan
}

func readExeBase(pid int) string {
	target, err := os.Readlink(filepath.Join("/proc", strconv.Itoa(pid), "exe"))
	if err != nil {
		return ""
	}
	return filepath.Base(target)
}

func readProcessUser(pid string, cache map[string]string) string {
	info, err := os.Stat(filepath.Join("/proc", pid))
	if err != nil {
		return ""
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return ""
	}
	uid := strconv.FormatUint(uint64(stat.Uid), 10)
	if name, ok := cache[uid]; ok {
		return name
	}
	u, err := user.LookupId(uid)
	if err != nil {
		cache[uid] = uid
		return uid
	}
	cache[uid] = u.Username
	return u.Username
}

func readProcStat(pid int) (uint64, bool) {
	data, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if err != nil {
		return 0, false
	}
	line := string(data)
	end := strings.LastIndexByte(line, ')')
	if end < 0 || end+2 >= len(line) {
		return 0, false
	}
	fields := strings.Fields(line[end+2:])
	if len(fields) <= 12 {
		return 0, false
	}
	utime, err1 := strconv.ParseUint(fields[11], 10, 64)
	stime, err2 := strconv.ParseUint(fields[12], 10, 64)
	if err1 != nil || err2 != nil {
		return 0, false
	}
	return utime + stime, true
}

func readSockStats(pid int) (uint64, uint64, uint64, uint64, uint64, bool) {
	used, tcp4, udp4, ok4 := readSockStatFile(filepath.Join("/proc", strconv.Itoa(pid), "net/sockstat"))
	tcp6, udp6, ok6 := readSockStat6File(filepath.Join("/proc", strconv.Itoa(pid), "net/sockstat6"))
	if !ok4 && !ok6 {
		return 0, 0, 0, 0, 0, false
	}
	return used, tcp4, udp4, tcp6, udp6, true
}

func readSockStatFile(path string) (uint64, uint64, uint64, bool) {
	file, err := os.Open(path)
	if err != nil {
		return 0, 0, 0, false
	}
	defer file.Close()

	var used uint64
	var tcp4 uint64
	var udp4 uint64
	var ok bool

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 3 {
			continue
		}
		switch fields[0] {
		case "sockets:":
			v, err := strconv.ParseUint(fields[2], 10, 64)
			if err == nil {
				used = v
				ok = true
			}
		case "TCP:":
			v, err := strconv.ParseUint(fields[2], 10, 64)
			if err == nil {
				tcp4 = v
			}
		case "UDP:":
			v, err := strconv.ParseUint(fields[2], 10, 64)
			if err == nil {
				udp4 = v
			}
		}
	}
	return used, tcp4, udp4, ok
}

func readSockStat6File(path string) (uint64, uint64, bool) {
	file, err := os.Open(path)
	if err != nil {
		return 0, 0, false
	}
	defer file.Close()

	var tcp6 uint64
	var udp6 uint64
	var ok bool

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 3 {
			continue
		}
		switch fields[0] {
		case "TCP6:":
			v, err := strconv.ParseUint(fields[2], 10, 64)
			if err == nil {
				tcp6 = v
				ok = true
			}
		case "UDP6:":
			v, err := strconv.ParseUint(fields[2], 10, 64)
			if err == nil {
				udp6 = v
				ok = true
			}
		}
	}
	return tcp6, udp6, ok
}

func readTotalCPUTime() uint64 {
	file, err := os.Open("/proc/stat")
	if err != nil {
		return 0
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	if !scanner.Scan() {
		return 0
	}
	fields := strings.Fields(scanner.Text())
	if len(fields) == 0 || fields[0] != "cpu" {
		return 0
	}
	var total uint64
	for _, field := range fields[1:] {
		v, err := strconv.ParseUint(field, 10, 64)
		if err != nil {
			continue
		}
		total += v
	}
	return total
}

func processSampleKey(nsID uint64, pid int) string {
	return strconv.FormatUint(nsID, 10) + "|" + strconv.Itoa(pid)
}
