//go:build linux

package monitors

import (
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

// procFSAvailable reports whether /proc is mounted as procfs. On Linux this
// is essentially always true; the check protects against misconfigured
// containers that hide /proc.
func procFSAvailable() bool {
	_, err := os.Stat("/proc/self/stat")
	return err == nil
}

// clkTckHz is the kernel timer frequency, fixed at 100 Hz on every shipped
// Linux x86_64 kernel since the 2.6 days (configurable via CONFIG_HZ but
// not at runtime). We hardcode 100 rather than shelling out to `getconf
// CLK_TCK` or cgo'ing sysconf(_SC_CLK_TCK), both of which add a runtime
// dependency for a constant that's been stable for two decades. If you
// ever deploy this on a non-stock kernel (CONFIG_HZ_250 / _300 / _1000),
// CPU-time gauges will scale wrong; document it loud here so a future
// grep for "clkTckHz" when the numbers look funny.
const clkTckHz = 100.0

var (
	btimeOnce sync.Once
	btime     int64
)

// readBootTime returns the kernel boot epoch from /proc/stat's "btime" line.
// Cached after the first successful read since boot time is constant.
func readBootTime() int64 {
	btimeOnce.Do(func() {
		data, err := os.ReadFile("/proc/stat")
		if err != nil {
			return
		}
		for _, line := range strings.Split(string(data), "\n") {
			if strings.HasPrefix(line, "btime ") {
				if v, err := strconv.ParseInt(strings.TrimSpace(strings.TrimPrefix(line, "btime")), 10, 64); err == nil {
					btime = v
				}
				return
			}
		}
	})
	return btime
}

// findProcess walks /proc, parsing each comm entry, returning the first
// process whose /proc/PID/comm matches the supplied name. Returns
// ok=false if no match. Matching against `comm` (the binary basename
// truncated to 15 chars) is exact and avoids false positives from
// processes that happen to mention the name in their args.
func findProcess(name string) (processInfo, bool) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return processInfo{}, false
	}
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		commPath := filepath.Join("/proc", e.Name(), "comm")
		comm, err := os.ReadFile(commPath)
		if err != nil {
			continue
		}
		if strings.TrimSpace(string(comm)) != name {
			continue
		}
		info, ok := readProcessInfo(pid)
		if !ok {
			continue
		}
		return info, true
	}
	return processInfo{}, false
}

// readProcessInfo gathers the metric values from /proc/PID. Each subfield
// read is best-effort: failures degrade gracefully to zero rather than
// dropping the entire process from the metrics.
func readProcessInfo(pid int) (processInfo, bool) {
	dir := filepath.Join("/proc", strconv.Itoa(pid))

	info := processInfo{PID: pid}

	// /proc/PID/stat layout per proc(5). Fields are space-separated except
	// for field 2 (comm) which is bracketed and may contain spaces. Skip
	// past the bracketed comm and parse the rest as ordered tokens.
	statRaw, err := os.ReadFile(filepath.Join(dir, "stat"))
	if err != nil {
		return info, false
	}
	closeBracket := bytes.LastIndexByte(statRaw, ')')
	if closeBracket < 0 {
		return info, false
	}
	tail := strings.TrimSpace(string(statRaw[closeBracket+1:]))
	fields := strings.Fields(tail)
	// After (comm), field offsets shift: original field 3 (state) is now [0].
	//   utime     = original field 14 → tail index 11
	//   stime     = original field 15 → tail index 12
	//   starttime = original field 22 → tail index 19
	if len(fields) >= 20 {
		utime, _ := strconv.ParseInt(fields[11], 10, 64)
		stime, _ := strconv.ParseInt(fields[12], 10, 64)
		info.CPUSeconds = float64(utime+stime) / clkTckHz

		starttimeTicks, _ := strconv.ParseInt(fields[19], 10, 64)
		bt := readBootTime()
		if bt > 0 && clkTckHz > 0 {
			info.StartTimeUnix = bt + int64(float64(starttimeTicks)/clkTckHz)
		}
	}

	// /proc/PID/status for human-friendly memory + threads.
	statusRaw, err := os.ReadFile(filepath.Join(dir, "status"))
	if err == nil {
		for _, line := range strings.Split(string(statusRaw), "\n") {
			switch {
			case strings.HasPrefix(line, "VmRSS:"):
				info.RSSBytes = parseStatusKB(line)
			case strings.HasPrefix(line, "VmSize:"):
				info.VirtBytes = parseStatusKB(line)
			case strings.HasPrefix(line, "Threads:"):
				info.Threads = parseStatusInt(line)
			}
		}
	}

	// /proc/PID/fd is a directory containing one entry per open FD.
	if fdEntries, err := os.ReadDir(filepath.Join(dir, "fd")); err == nil {
		info.OpenFDs = int64(len(fdEntries))
	}

	return info, true
}

// parseStatusKB extracts the numeric prefix from a /proc/PID/status line
// like "VmRSS:    12345 kB" and returns the value in bytes.
func parseStatusKB(line string) int64 {
	fields := strings.Fields(line)
	if len(fields) < 2 {
		return 0
	}
	n, err := strconv.ParseInt(fields[1], 10, 64)
	if err != nil {
		return 0
	}
	return n * 1024
}

func parseStatusInt(line string) int64 {
	fields := strings.Fields(line)
	if len(fields) < 2 {
		return 0
	}
	n, err := strconv.ParseInt(fields[1], 10, 64)
	if err != nil {
		return 0
	}
	return n
}
