package store

import (
	"bufio"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/msstnk/vxmon/internal/types"
)

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

func (s *Store) reloadProcesses(now time.Time, scan procScanResult) {
	repPID := scan.repPID
	raw := collectProcessRaw(scan)

	for i := range s.inventory.namespaces {
		info := s.inventory.namespaces[i]
		info.SocketUsed = 0
		info.TCPInUse = 0
		info.UDPInUse = 0
		info.TCP6InUse = 0
		info.UDP6InUse = 0

		pid := repPID[info.ID]
		if pid != 0 {
			used, tcp4, udp4, tcp6, udp6, ok := readSockStats(pid)
			if ok {
				info.SocketUsed = used
				info.TCPInUse = tcp4
				info.UDPInUse = udp4
				info.TCP6InUse = tcp6
				info.UDP6InUse = udp6
			}
		}
		s.inventory.namespaces[i] = info
		if state := s.inventory.namespaceState[info.ID]; state != nil {
			state.info = info
		}
	}
	nextProcRows := s.updateProcessHistory(raw)
	s.runtimeState.processes = nextProcRows
	flat := make([]types.ProcessInfo, 0, len(raw.processes))
	for _, rows := range nextProcRows {
		flat = append(flat, rows...)
	}

	s.recordState.processRecords, s.recordState.processMeta, _ = reconcile(s.recordState.processRecords, s.recordState.processMeta, flat, processKey, processFingerprint, 0, now)
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
	var used, tcp4, udp4, tcp6, udp6 uint64
	ok4 := readNetSockStat(
		filepath.Join("/proc", strconv.Itoa(pid), "net/sockstat"),
		map[string]*uint64{"sockets:": &used, "TCP:": &tcp4, "UDP:": &udp4},
	)
	ok6 := readNetSockStat(
		filepath.Join("/proc", strconv.Itoa(pid), "net/sockstat6"),
		map[string]*uint64{"TCP6:": &tcp6, "UDP6:": &udp6},
	)
	if !ok4 && !ok6 {
		return 0, 0, 0, 0, 0, false
	}
	return used, tcp4, udp4, tcp6, udp6, true
}

func readNetSockStat(path string, targets map[string]*uint64) bool {
	file, err := os.Open(path)
	if err != nil {
		return false
	}
	defer file.Close()
	var found bool
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 3 {
			continue
		}
		if ptr, ok := targets[fields[0]]; ok {
			if v, err := strconv.ParseUint(fields[2], 10, 64); err == nil {
				*ptr = v
				found = true
			}
		}
	}
	return found
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
