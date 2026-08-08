package monitors

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/validaoxyz/hyperliquid-exporter/internal/metrics"
)

type fakeProcessSpec struct {
	pid        int
	name       string
	executable string
	argv0      string
	start      uint64
	utime      uint64
	stime      uint64
	threads    int64
	virtBytes  int64
	rssPages   int64
	fds        int
	maxFDs     string
	io         *processIOValues
}

func TestFindProcessesAtSelectsOldestValidatedProcessDeterministically(t *testing.T) {
	root := t.TempDir()
	writeFakeProcBoot(t, root, 1_700_000_000)
	writeFakeProcess(t, root, fakeProcessSpec{
		pid: 2, name: "hl-node", executable: "/opt/hl-node", start: 300,
		utime: 120, stime: 80, threads: 8, virtBytes: 9000, rssPages: 2,
	})
	selectedIO := processIOValues{ReadBytes: 1000, WriteBytes: 2000, ReadSyscalls: 30, WriteSyscalls: 40}
	writeFakeProcess(t, root, fakeProcessSpec{
		pid: 10001, name: "hl-node", executable: "/opt/hl-node", start: 100,
		utime: 250, stime: 50, threads: 12, virtBytes: 12000, rssPages: 3,
		fds: 2, maxFDs: "100", io: &selectedIO,
	})
	// A matching comm with neither executable nor argv0 identity is ineligible.
	writeFakeProcess(t, root, fakeProcessSpec{
		pid: 7, name: "hl-node", executable: "/opt/not-hl-node", argv0: "/opt/not-hl-node", start: 50,
		threads: 1, virtBytes: 1, rssPages: 1,
	})
	// A readable matching argv0 remains eligible when the executable name does
	// not match (for wrapper/script launches).
	writeFakeProcess(t, root, fakeProcessSpec{
		pid: 3, name: "hl-node", executable: "/usr/bin/env", argv0: "/home/ubuntu/hl-node", start: 200,
		threads: 2, virtBytes: 2, rssPages: 1,
	})
	disappeared := filepath.Join(root, "4")
	if err := os.Mkdir(disappeared, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(disappeared, "comm"), []byte("hl-node\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/opt/hl-node", filepath.Join(disappeared, "exe")); err != nil {
		t.Fatal(err)
	}
	// Equal start ticks use numeric PID, never lexical directory order.
	writeFakeProcess(t, root, fakeProcessSpec{
		pid: 200, name: "hl-visor", executable: "/opt/hl-visor", start: 50,
		threads: 1, virtBytes: 1, rssPages: 1,
	})
	writeFakeProcess(t, root, fakeProcessSpec{
		pid: 100, name: "hl-visor", executable: "/opt/hl-visor", start: 50,
		threads: 1, virtBytes: 1, rssPages: 1,
	})

	selections, err := findProcessesAt(root, []string{"hl-node", "hl-visor"})
	if err != nil {
		t.Fatal(err)
	}
	node := selections["hl-node"]
	if !node.Found || node.Eligible != 3 || node.Info.PID != 10001 {
		t.Fatalf("node selection = %+v, want pid=10001 eligible=3", node)
	}
	if node.Info.StartTimeUnix != 1_700_000_001 {
		t.Fatalf("start time = %d, want 1700000001", node.Info.StartTimeUnix)
	}
	if node.Info.CPUSeconds != 3 {
		t.Fatalf("cpu seconds = %v, want 3", node.Info.CPUSeconds)
	}
	if node.Info.Threads != 12 || node.Info.VirtBytes != 12000 || node.Info.RSSBytes != 3*int64(os.Getpagesize()) {
		t.Fatalf("selected resource data = %+v", node.Info)
	}
	if node.Info.OpenFDs != 2 || node.Info.MaxFDs != 100 || !node.Info.IOValid || node.Info.IO != selectedIO {
		t.Fatalf("selected optional data = %+v", node.Info)
	}
	visor := selections["hl-visor"]
	if !visor.Found || visor.Eligible != 2 || visor.Info.PID != 100 {
		t.Fatalf("visor selection = %+v, want pid=100 eligible=2", visor)
	}
}

func TestFindProcessesAtFailsClosedOnIncompleteRoot(t *testing.T) {
	root := t.TempDir()
	if _, err := findProcessesAt(root, processNames); err == nil {
		t.Fatal("missing global proc stat was accepted")
	}
	writeFakeProcBoot(t, root, 1_700_000_000)
	if err := os.RemoveAll(root); err != nil {
		t.Fatal(err)
	}
	if _, err := findProcessesAt(root, processNames); err == nil {
		t.Fatal("missing proc root was accepted")
	}
}

func TestProcessMonitorStateUsesPositiveSameEpochDeltas(t *testing.T) {
	state := newProcessMonitorState()
	info := processInfo{
		PID: 10, StartTimeTicks: 100, IOValid: true,
		IO: processIOValues{ReadBytes: 100, WriteBytes: 200, ReadSyscalls: 10, WriteSyscalls: 20},
	}
	if delta, ok := state.observe("hl-node", info); !ok || delta != (processIOValues{}) {
		t.Fatalf("initial baseline = %+v, ok=%v", delta, ok)
	}

	info.IO = processIOValues{ReadBytes: 150, WriteBytes: 260, ReadSyscalls: 15, WriteSyscalls: 25}
	if delta, _ := state.observe("hl-node", info); delta != (processIOValues{ReadBytes: 50, WriteBytes: 60, ReadSyscalls: 5, WriteSyscalls: 5}) {
		t.Fatalf("same-epoch delta = %+v", delta)
	}

	// Two fields roll backwards while the others advance. Rollbacks contribute
	// zero and are rebased independently; positive operations still publish.
	info.IO = processIOValues{ReadBytes: 5, WriteBytes: 300, ReadSyscalls: 2, WriteSyscalls: 30}
	if delta, _ := state.observe("hl-node", info); delta != (processIOValues{WriteBytes: 40, WriteSyscalls: 5}) {
		t.Fatalf("rollback delta = %+v", delta)
	}
	info.IO = processIOValues{ReadBytes: 15, WriteBytes: 310, ReadSyscalls: 4, WriteSyscalls: 32}
	if delta, _ := state.observe("hl-node", info); delta != (processIOValues{ReadBytes: 10, WriteBytes: 10, ReadSyscalls: 2, WriteSyscalls: 2}) {
		t.Fatalf("post-rollback delta = %+v", delta)
	}

	// A transient unreadable io file neither emits nor discards the baseline.
	info.IOValid = false
	if delta, ok := state.observe("hl-node", info); ok || delta != (processIOValues{}) {
		t.Fatalf("unreadable io = %+v, ok=%v", delta, ok)
	}
	info.IOValid = true
	info.IO.ReadBytes = 25
	if delta, _ := state.observe("hl-node", info); delta.ReadBytes != 10 {
		t.Fatalf("delta across unreadable sample = %+v", delta)
	}

	info.StartTimeTicks++
	info.IO = processIOValues{ReadBytes: 5000, WriteBytes: 5000, ReadSyscalls: 5000, WriteSyscalls: 5000}
	if delta, _ := state.observe("hl-node", info); delta != (processIOValues{}) {
		t.Fatalf("new epoch emitted historical IO: %+v", delta)
	}
	state.reset("hl-node")
	info.IO.ReadBytes++
	if delta, _ := state.observe("hl-node", info); delta != (processIOValues{}) {
		t.Fatalf("post-absence baseline emitted IO: %+v", delta)
	}
}

func TestTickProcessesPublishesExhaustionAndResetAwareIO(t *testing.T) {
	state := newProcessMonitorState()
	current := processInfo{
		PID: 10, StartTimeTicks: 100, StartTimeUnix: 1_700_000_001,
		CPUSeconds: 3, RSSBytes: 4000, VirtBytes: 8000, Threads: 12,
		OpenFDs: 25, MaxFDs: 100, IOValid: true,
		IO: processIOValues{ReadBytes: 100, WriteBytes: 200, ReadSyscalls: 10, WriteSyscalls: 20},
	}
	readCounter := metrics.HLNodeProcessIOTotal.WithLabelValues("hl-node", processIOReadBytes)
	writeCounter := metrics.HLNodeProcessIOTotal.WithLabelValues("hl-node", processIOWriteBytes)
	readBefore := hostMetricValue(t, readCounter)
	writeBefore := hostMetricValue(t, writeCounter)
	scan := func([]string) (map[string]processSelection, error) {
		return map[string]processSelection{
			"hl-node":  {Info: current, Eligible: 2, Found: true},
			"hl-visor": {},
		}, nil
	}
	if !tickProcessesWith(state, scan) {
		t.Fatal("initial process tick failed")
	}
	if got := hostMetricValue(t, metrics.HLNodeProcessEligibleMatches.WithLabelValues("hl-node")); got != 2 {
		t.Fatalf("eligible matches = %v", got)
	}
	if got := hostMetricValue(t, metrics.HLNodeProcessMaxFDs.WithLabelValues("hl-node")); got != 100 {
		t.Fatalf("max fds = %v", got)
	}
	if got := hostMetricValue(t, metrics.HLNodeProcessOpenFDsRatio.WithLabelValues("hl-node")); got != 0.25 {
		t.Fatalf("fd ratio = %v", got)
	}
	if got := hostMetricValue(t, readCounter); got != readBefore {
		t.Fatalf("baseline emitted read IO: %v -> %v", readBefore, got)
	}

	current.IO.ReadBytes = 140
	current.IO.WriteBytes = 260
	if !tickProcessesWith(state, scan) {
		t.Fatal("delta process tick failed")
	}
	if got := hostMetricValue(t, readCounter) - readBefore; got != 40 {
		t.Fatalf("read delta counter = %v", got)
	}
	if got := hostMetricValue(t, writeCounter) - writeBefore; got != 60 {
		t.Fatalf("write delta counter = %v", got)
	}

	failingScan := func([]string) (map[string]processSelection, error) {
		return map[string]processSelection{
			"hl-node": {Info: processInfo{OpenFDs: 999, MaxFDs: 1000}, Eligible: 99, Found: true},
		}, errors.New("injected proc root failure")
	}
	if tickProcessesWith(state, failingScan) {
		t.Fatal("failed proc scan reported success")
	}
	if got := hostMetricValue(t, metrics.HLNodeProcessEligibleMatches.WithLabelValues("hl-node")); got != 2 {
		t.Fatalf("failed scan replaced eligible matches: %v", got)
	}
	if got := hostMetricValue(t, metrics.HLNodeProcessOpenFDsRatio.WithLabelValues("hl-node")); got != 0.25 {
		t.Fatalf("failed scan replaced fd ratio: %v", got)
	}

	missingScan := func([]string) (map[string]processSelection, error) {
		return map[string]processSelection{"hl-node": {}, "hl-visor": {}}, nil
	}
	if !tickProcessesWith(state, missingScan) {
		t.Fatal("complete missing-process scan failed")
	}
	if got := hostMetricValue(t, metrics.HLNodeProcessUp.WithLabelValues("hl-node")); got != 0 {
		t.Fatalf("missing process up = %v", got)
	}
	if got := hostMetricValue(t, metrics.HLNodeProcessMaxFDs.WithLabelValues("hl-node")); got != 0 {
		t.Fatalf("missing process max fds = %v", got)
	}
	current.IO.ReadBytes = 1000
	if !tickProcessesWith(state, scan) {
		t.Fatal("reappeared process tick failed")
	}
	if got := hostMetricValue(t, readCounter) - readBefore; got != 40 {
		t.Fatalf("reappeared process emitted historical IO: %v", got)
	}
}

func TestReadProcessOptionalFilesAreBoundedAndComplete(t *testing.T) {
	dir := t.TempDir()
	limits := filepath.Join(dir, "limits")
	if err := os.WriteFile(limits, []byte("Limit Soft Hard Units\nMax open files unlimited unlimited files\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if value, ok := readProcessMaxFDs(limits); !ok || value != 0 {
		t.Fatalf("unlimited max fds = %d, ok=%v", value, ok)
	}

	ioPath := filepath.Join(dir, "io")
	if err := os.WriteFile(ioPath, []byte("syscr: 1\nsyscw: 2\nread_bytes: 3\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok := readProcessIO(ioPath); ok {
		t.Fatal("partial proc io was accepted")
	}
	if err := os.WriteFile(ioPath, []byte("syscr: 1\nsyscw: 2\nread_bytes: 3\nwrite_bytes: 4\ncancelled_write_bytes: -9\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, ok := readProcessIO(ioPath); !ok || got != (processIOValues{ReadBytes: 3, WriteBytes: 4, ReadSyscalls: 1, WriteSyscalls: 2}) {
		t.Fatalf("proc io = %+v, ok=%v", got, ok)
	}
}

func writeFakeProcBoot(t *testing.T, root string, boot int64) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, "stat"), []byte(fmt.Sprintf("cpu 1 2 3 4\nbtime %d\n", boot)), 0o600); err != nil {
		t.Fatal(err)
	}
}

func writeFakeProcess(t *testing.T, root string, spec fakeProcessSpec) {
	t.Helper()
	dir := filepath.Join(root, strconv.Itoa(spec.pid))
	if err := os.MkdirAll(filepath.Join(dir, "fd"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "comm"), []byte(spec.name+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if spec.executable != "" {
		if err := os.Symlink(spec.executable, filepath.Join(dir, "exe")); err != nil {
			t.Fatal(err)
		}
	}
	if spec.argv0 != "" {
		if err := os.WriteFile(filepath.Join(dir, "cmdline"), append([]byte(spec.argv0), 0), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	tail := make([]string, 22)
	for i := range tail {
		tail[i] = "0"
	}
	tail[0] = "S"
	tail[11] = strconv.FormatUint(spec.utime, 10)
	tail[12] = strconv.FormatUint(spec.stime, 10)
	tail[17] = strconv.FormatInt(spec.threads, 10)
	tail[19] = strconv.FormatUint(spec.start, 10)
	tail[20] = strconv.FormatInt(spec.virtBytes, 10)
	tail[21] = strconv.FormatInt(spec.rssPages, 10)
	stat := fmt.Sprintf("%d (%s) %s\n", spec.pid, spec.name, strings.Join(tail, " "))
	if err := os.WriteFile(filepath.Join(dir, "stat"), []byte(stat), 0o600); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < spec.fds; i++ {
		if err := os.WriteFile(filepath.Join(dir, "fd", strconv.Itoa(i)), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if spec.maxFDs != "" {
		contents := "Limit Soft Hard Units\nMax open files " + spec.maxFDs + " " + spec.maxFDs + " files\n"
		if err := os.WriteFile(filepath.Join(dir, "limits"), []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if spec.io != nil {
		contents := fmt.Sprintf(
			"rchar: 0\nwchar: 0\nsyscr: %d\nsyscw: %d\nread_bytes: %d\nwrite_bytes: %d\ncancelled_write_bytes: 0\n",
			spec.io.ReadSyscalls, spec.io.WriteSyscalls, spec.io.ReadBytes, spec.io.WriteBytes,
		)
		if err := os.WriteFile(filepath.Join(dir, "io"), []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}
