//go:build linux

package monitors

import (
	"bufio"
	"bytes"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/validaoxyz/hyperliquid-exporter/internal/metrics"
)

type tcpSocketKey struct {
	port  uint16
	side  string
	state string
}

type tcpSourceReadOutcome struct {
	label string
	stage string
	ok    bool
}

type tcpConnectionsAttempt struct {
	ports      []uint16
	enableTCP6 bool
	configErr  bool
	outcomes   []tcpSourceReadOutcome
	combined   map[tcpSocketKey]int
}

type tcpConnectionsPublishPhase string

const (
	tcpConnectionsPublishSources tcpConnectionsPublishPhase = "sources"
	tcpConnectionsPublishCounts  tcpConnectionsPublishPhase = "counts"
)

var procTCPSources = []struct {
	label string
	path  string
}{
	{label: "tcp4", path: "/proc/net/tcp"},
	{label: "tcp6", path: "/proc/net/tcp6"},
}

func activeProcTCPSources(enableTCP6 bool) []struct {
	label string
	path  string
} {
	active := make([]struct {
		label string
		path  string
	}, 0, len(procTCPSources))
	for _, source := range procTCPSources {
		if source.label == "tcp6" && !enableTCP6 {
			continue
		}
		active = append(active, source)
	}
	return active
}

// tickTCPConnections stages every enabled proc source and commits only after
// both TCP4 and TCP6 have been read in full. A failure leaves the last
// combined snapshot untouched instead of manufacturing a healthy zero.
func tickTCPConnections(configuredPorts []uint16, enableTCP6 bool) {
	attempt := collectTCPConnectionsAttempt(configuredPorts, enableTCP6)
	publishTCPConnectionsAttempt(attempt, time.Now())
}

func collectTCPConnectionsAttempt(configuredPorts []uint16, enableTCP6 bool) tcpConnectionsAttempt {
	ports, err := validateTCPServicePorts(configuredPorts)
	attempt := tcpConnectionsAttempt{
		ports:      ports,
		enableTCP6: enableTCP6,
		configErr:  err != nil,
	}
	if err != nil {
		return attempt
	}

	attempt.combined = preseedTCPSocketCounts(ports)
	for _, source := range activeProcTCPSources(enableTCP6) {
		part, stage, readErr := readTCPSource(source.path, ports, true)
		outcome := tcpSourceReadOutcome{label: source.label, stage: stage, ok: readErr == nil}
		attempt.outcomes = append(attempt.outcomes, outcome)
		if readErr != nil {
			continue
		}
		for key, value := range part {
			attempt.combined[key] += value
		}
	}
	return attempt
}

func publishTCPConnectionsAttempt(attempt tcpConnectionsAttempt, now time.Time) {
	publishTCPConnectionsAttemptWithCheckpoint(attempt, now, nil)
}

// publishTCPConnectionsAttemptWithCheckpoint keeps the callback inside the
// scrape barrier. Production passes nil; tests use it to prove a protected
// Gather cannot observe an intentionally paused half-publication.
func publishTCPConnectionsAttemptWithCheckpoint(
	attempt tcpConnectionsAttempt,
	now time.Time,
	checkpoint func(tcpConnectionsPublishPhase),
) {
	metrics.WithPrometheusSnapshotUpdate(func() {
		metrics.MarkMonitorAttempt("tcp_connections")
		metrics.MarkSourceAttempt(metrics.SourceTCPConnections)

		if attempt.configErr {
			metrics.HLP2PTCPSocketErrorsTotal.WithLabelValues("tcp4", "parse").Inc()
			metrics.HLP2PTCPSocketSourceUp.WithLabelValues("tcp4").Set(0)
			if attempt.enableTCP6 {
				metrics.HLP2PTCPSocketSourceUp.WithLabelValues("tcp6").Set(0)
			} else {
				metrics.HLP2PTCPSocketSourceUp.DeleteLabelValues("tcp6")
			}
			metrics.MarkSourceError(metrics.SourceTCPConnections, metrics.SourceFailureSchema)
			metrics.IncMonitorError("tcp_connections")
			return
		}

		failed := false
		failureStage := metrics.SourceFailureSchema
		failurePriority := 0
		for _, outcome := range attempt.outcomes {
			if outcome.ok {
				metrics.HLP2PTCPSocketSourceUp.WithLabelValues(outcome.label).Set(1)
				continue
			}
			failed = true
			metrics.HLP2PTCPSocketSourceUp.WithLabelValues(outcome.label).Set(0)
			metrics.HLP2PTCPSocketErrorsTotal.WithLabelValues(outcome.label, outcome.stage).Inc()
			stage, priority := tcpSourceFailureStage(outcome.stage)
			if priority > failurePriority {
				failureStage = stage
				failurePriority = priority
			}
		}
		if !attempt.enableTCP6 {
			// Disabled is intentionally absent, not a failed zero.
			metrics.HLP2PTCPSocketSourceUp.DeleteLabelValues("tcp6")
		}
		if checkpoint != nil {
			checkpoint(tcpConnectionsPublishSources)
		}
		if failed {
			metrics.MarkSourceError(metrics.SourceTCPConnections, failureStage)
			metrics.IncMonitorError("tcp_connections")
			return
		}

		for key, value := range attempt.combined {
			port := strconv.FormatUint(uint64(key.port), 10)
			metrics.HLP2PTCPSocketConnections.WithLabelValues(port, key.side, key.state).Set(float64(value))
		}
		if checkpoint != nil {
			checkpoint(tcpConnectionsPublishCounts)
		}
		// One-release aggregate compatibility alias. The new local-first
		// association guarantees the two sides are disjoint.
		for _, port := range attempt.ports {
			portLabel := strconv.FormatUint(uint64(port), 10)
			for _, state := range allTCPStateNames() {
				n := attempt.combined[tcpSocketKey{port: port, side: "local", state: state}] +
					attempt.combined[tcpSocketKey{port: port, side: "remote", state: state}]
				metrics.HLP2PTCPConnections.WithLabelValues(portLabel, state).Set(float64(n))
			}
		}
		metrics.HLP2PTCPSocketLastSuccessTimestampSeconds.Set(float64(now.Unix()))
		metrics.MarkSourceValidObservation(metrics.SourceTCPConnections, time.Time{})
		metrics.MarkSourcePublication(metrics.SourceTCPConnections)
		metrics.MarkMonitorValidObservation("tcp_connections")
		metrics.MarkMonitorPublication("tcp_connections")
	})
}

func tcpSourceFailureStage(stage string) (metrics.SourceFailureStage, int) {
	switch stage {
	case "open":
		return metrics.SourceFailureOpen, 2
	case "scan":
		return metrics.SourceFailureRead, 2
	default:
		return metrics.SourceFailureSchema, 1
	}
}

func allTCPStateNames() []string {
	names := make([]string, 0, len(tcpStateNames)+1)
	for _, name := range tcpStateNames {
		names = append(names, name)
	}
	sortStrings(names)
	return append(names, "UNKNOWN")
}

func sortStrings(values []string) {
	// This avoids exposing map iteration order in metric publication and tests.
	for i := 1; i < len(values); i++ {
		for j := i; j > 0 && values[j] < values[j-1]; j-- {
			values[j], values[j-1] = values[j-1], values[j]
		}
	}
}

func preseedTCPSocketCounts(ports []uint16) map[tcpSocketKey]int {
	out := make(map[tcpSocketKey]int, len(ports)*2*(len(tcpStateNames)+1))
	for _, port := range ports {
		for _, side := range []string{"local", "remote"} {
			for _, state := range allTCPStateNames() {
				out[tcpSocketKey{port: port, side: side, state: state}] = 0
			}
		}
	}
	return out
}

// readTCPSource parses one /proc/net/tcp{,6} snapshot. Every non-header row
// must be valid in strict mode. Each socket is associated exactly once:
// tracked local port first, otherwise tracked remote port.
func readTCPSource(path string, ports []uint16, strict bool) (map[tcpSocketKey]int, string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, "open", err
	}
	defer f.Close()

	tracked := make(map[uint16]struct{}, len(ports))
	for _, port := range ports {
		tracked[port] = struct{}{}
	}
	counts := preseedTCPSocketCounts(ports)
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	scanner.Split(scanCommittedProcLine)
	if !scanner.Scan() {
		if err := scanner.Err(); err != nil {
			return nil, "scan", err
		}
		return nil, "parse", errors.New("missing proc header")
	}
	if !validTCPProcHeader(scanner.Text()) {
		return nil, "parse", errors.New("invalid proc header")
	}
	lineNo := 1
	for scanner.Scan() {
		lineNo++
		if strings.TrimSpace(scanner.Text()) == "" {
			continue
		}
		key, relevant, err := parseTCPProcRow(scanner.Text(), tracked)
		if err != nil {
			if strict {
				return nil, "parse", fmt.Errorf("row %d: %w", lineNo, err)
			}
			continue
		}
		if relevant {
			counts[key]++
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, "scan", err
	}
	return counts, "", nil
}

func scanCommittedProcLine(data []byte, atEOF bool) (advance int, token []byte, err error) {
	if index := bytes.IndexByte(data, '\n'); index >= 0 {
		line := data[:index]
		if len(line) > 0 && line[len(line)-1] == '\r' {
			line = line[:len(line)-1]
		}
		return index + 1, line, nil
	}
	if atEOF {
		if len(data) == 0 {
			return 0, nil, nil
		}
		return 0, nil, io.ErrUnexpectedEOF
	}
	return 0, nil, nil
}

func parseTCPProcRow(line string, tracked map[uint16]struct{}) (tcpSocketKey, bool, error) {
	fields := strings.Fields(line)
	if len(fields) < 4 {
		return tcpSocketKey{}, false, errors.New("too few fields")
	}
	if !strings.HasSuffix(fields[0], ":") {
		return tcpSocketKey{}, false, errors.New("invalid row index")
	}
	if _, err := strconv.ParseUint(strings.TrimSuffix(fields[0], ":"), 10, 64); err != nil {
		return tcpSocketKey{}, false, errors.New("invalid row index")
	}
	local, err := parseProcPort(fields[1])
	if err != nil {
		return tcpSocketKey{}, false, fmt.Errorf("local address: %w", err)
	}
	remote, err := parseProcPort(fields[2])
	if err != nil {
		return tcpSocketKey{}, false, fmt.Errorf("remote address: %w", err)
	}
	state64, err := strconv.ParseUint(fields[3], 16, 8)
	if err != nil {
		return tcpSocketKey{}, false, fmt.Errorf("state: %w", err)
	}
	state := tcpStateNames[uint8(state64)]
	if state == "" {
		state = "UNKNOWN"
	}
	if _, ok := tracked[local]; ok {
		return tcpSocketKey{port: local, side: "local", state: state}, true, nil
	}
	if _, ok := tracked[remote]; ok {
		return tcpSocketKey{port: remote, side: "remote", state: state}, true, nil
	}
	return tcpSocketKey{}, false, nil
}

func parseProcPort(address string) (uint16, error) {
	if strings.Count(address, ":") != 1 {
		return 0, errors.New("invalid address separator")
	}
	host, portHex, _ := strings.Cut(address, ":")
	if (len(host) != 8 && len(host) != 32) || len(portHex) != 4 {
		return 0, errors.New("invalid address shape")
	}
	if _, err := hex.DecodeString(host); err != nil {
		return 0, errors.New("invalid address hex")
	}
	port, err := strconv.ParseUint(portHex, 16, 16)
	return uint16(port), err
}

func validTCPProcHeader(line string) bool {
	fields := strings.Fields(line)
	return len(fields) >= 4 &&
		fields[0] == "sl" &&
		fields[1] == "local_address" &&
		(fields[2] == "rem_address" || fields[2] == "remote_address") &&
		fields[3] == "st"
}

// readTCPFile is retained as a test/compatibility helper for the old local-only
// shape. Production publication uses strict readTCPSource above.
func readTCPFile(path string, counts map[uint16]map[string]int) {
	ports := make([]uint16, 0, len(counts))
	for port := range counts {
		ports = append(ports, port)
	}
	parsed, _, err := readTCPSource(path, ports, false)
	if err != nil {
		return
	}
	for key, value := range parsed {
		if key.side == "local" {
			counts[key.port][key.state] += value
		}
	}
}
