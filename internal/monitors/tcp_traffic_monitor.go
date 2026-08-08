package monitors

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/netip"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/validaoxyz/hyperliquid-exporter/internal/config"
	"github.com/validaoxyz/hyperliquid-exporter/internal/logger"
	"github.com/validaoxyz/hyperliquid-exporter/internal/metrics"
	"github.com/validaoxyz/hyperliquid-exporter/internal/peerset"
)

const (
	tcpTrafficPollInterval = 30 * time.Second
	tcpTrafficTopN         = 16
	tcpAdmissionMaxGap     = 90 * time.Second
)

type peerSample struct {
	dir   string
	ip    string
	value float64
}

type tcpTrafficRecord struct {
	timestamp time.Time
	inbound   []peerSample
	outbound  []peerSample
	byPort    map[string]map[string]float64
}

type tcpTrafficParseError struct {
	stage string
	err   error
}

func (e *tcpTrafficParseError) Error() string { return e.stage + ": " + e.err.Error() }

type trafficAdmissionCandidate struct {
	streak int
}

type tcpTrafficMonitorState struct {
	prevPublished map[string]map[string]bool
	lastRecord    string
	lastReceipt   time.Time
	lastSource    time.Time
	admission     map[string]trafficAdmissionCandidate
	registry      *peerset.Set
	servicePorts  []uint16
}

func newTCPTrafficMonitorState(configured ...[]uint16) *tcpTrafficMonitorState {
	ports := tcpListenPorts
	if len(configured) > 0 && len(configured[0]) > 0 {
		ports = configured[0]
	}
	ports, _ = validateTCPServicePorts(ports)
	return &tcpTrafficMonitorState{
		prevPublished: map[string]map[string]bool{"in": {}, "out": {}},
		admission:     make(map[string]trafficAdmissionCandidate),
		registry:      peerset.Default(),
		servicePorts:  ports,
	}
}

type LatestTCPTrafficSample struct {
	mu         sync.RWMutex
	timestamp  time.Time
	receivedAt time.Time
	inbound    []peerSample
	outbound   []peerSample
}

var sharedTCPTraffic LatestTCPTrafficSample

// LatestTCPTrafficSnapshot is the compatibility accessor used by tests and
// older internal callers. The returned slices are copies.
func LatestTCPTrafficSnapshot() (ts time.Time, in []peerSample, out []peerSample) {
	ts, _, in, out = LatestTCPTrafficObservation()
	return ts, in, out
}

// LatestTCPTrafficObservation returns one coherent accepted snapshot and its
// exporter receipt time. Receipt time, never source time, governs freshness.
func LatestTCPTrafficObservation() (sourceTime, receivedAt time.Time, in, out []peerSample) {
	sharedTCPTraffic.mu.RLock()
	defer sharedTCPTraffic.mu.RUnlock()
	return sharedTCPTraffic.timestamp, sharedTCPTraffic.receivedAt,
		append([]peerSample(nil), sharedTCPTraffic.inbound...),
		append([]peerSample(nil), sharedTCPTraffic.outbound...)
}

func StartTCPTrafficMonitor(ctx context.Context, cfg config.Config, errCh chan<- error) {
	root := filepath.Join(cfg.NodeHome, "data", "tcp_traffic", "hourly")
	metrics.RegisterSource(metrics.SourceTCPTraffic, true)
	logger.InfoComponent("tcp_traffic", "watching %s (late discovery enabled)", root)

	ports, err := tcpServicePortsForConfig(cfg)
	if err != nil {
		metrics.MarkSourceError(metrics.SourceTCPTraffic, metrics.SourceFailureSchema)
		logger.ErrorComponent("tcp_traffic", "invalid service-port configuration: %v", err)
		<-ctx.Done()
		return
	}
	state := newTCPTrafficMonitorState(ports)
	ticker := time.NewTicker(tcpTrafficPollInterval)
	defer ticker.Stop()

	tickTCPTraffic(root, state)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			tickTCPTraffic(root, state)
		}
	}
}

func tickTCPTraffic(root string, state *tcpTrafficMonitorState) {
	metrics.MarkMonitorAttempt("tcp_traffic")
	metrics.MarkSourceAttempt(metrics.SourceTCPTraffic)
	now := time.Now()
	if !state.lastReceipt.IsZero() {
		metrics.HLP2PSampleAgeSeconds.Set(nonnegativeDuration(now.Sub(state.lastReceipt)).Seconds())
	}

	filePath, err := latestHourlyFile(root)
	if err != nil {
		trafficFailure("discover", err)
		return
	}
	data, err := os.ReadFile(filePath)
	if err != nil {
		trafficFailure("read", err)
		return
	}
	line, ok := lastFullLine(data)
	if !ok {
		trafficFailure("record", errors.New("no complete newline-committed record"))
		return
	}
	record, err := parseTCPTrafficRecordWithPorts(line, state.servicePorts)
	if err != nil {
		stage := "record"
		var parseErr *tcpTrafficParseError
		if errors.As(err, &parseErr) {
			stage = parseErr.stage
		}
		trafficFailure(stage, err)
		return
	}

	if string(line) == state.lastRecord {
		markTCPTrafficSourceSuccess()
		return
	}
	if !state.lastSource.IsZero() && !record.timestamp.After(state.lastSource) {
		trafficFailure("timestamp", errors.New("traffic source timestamp did not advance"))
		return
	}

	receipt := time.Now()
	state.lastRecord = string(line)
	commitTCPTraffic(record, receipt, state)
	state.lastReceipt = receipt
	state.lastSource = record.timestamp
}

func trafficFailure(stage string, err error) {
	metrics.WithPrometheusSnapshotUpdate(func() {
		metrics.HLP2PTCPTrafficSourceUp.Set(0)
		metrics.HLP2PTCPTrafficErrorsTotal.WithLabelValues(stage).Inc()
		if stage == "discover" {
			metrics.MarkSourceError(metrics.SourceTCPTraffic, metrics.SourceFailureDiscovery)
			if errors.Is(err, os.ErrNotExist) {
				metrics.MarkSourceAbsent(metrics.SourceTCPTraffic)
			}
		} else if stage == "read" {
			metrics.MarkSourceError(metrics.SourceTCPTraffic, metrics.SourceFailureRead)
		} else {
			metrics.MarkSourceError(metrics.SourceTCPTraffic, metrics.SourceFailureSchema)
		}
		metrics.IncMonitorError("tcp_traffic")
	})
}

func commitTCPTraffic(record tcpTrafficRecord, receipt time.Time, state *tcpTrafficMonitorState) {
	metrics.WithPrometheusSnapshotUpdate(func() {
		publishTCPTrafficSourceSuccess()
		sharedTCPTraffic.mu.Lock()
		sharedTCPTraffic.timestamp = record.timestamp
		sharedTCPTraffic.receivedAt = receipt
		sharedTCPTraffic.inbound = append([]peerSample(nil), record.inbound...)
		sharedTCPTraffic.outbound = append([]peerSample(nil), record.outbound...)
		sharedTCPTraffic.mu.Unlock()

		current := positiveEndpointSet(record.inbound, record.outbound)
		metrics.HLP2PTrafficEndpointsCurrent.Set(float64(len(current)))
		metrics.HLP2PPeersTotal.Set(float64(len(current))) // one-release alias
		advanceTrafficAdmissions(record, receipt, state)

		publishTCPDirection("in", record.inbound, state.prevPublished["in"])
		publishTCPDirection("out", record.outbound, state.prevPublished["out"])
		publishTrafficPorts(record.byPort, state.servicePorts)
		metrics.HLP2PTCPTrafficSampleTimestampSeconds.Set(unixTimestampSeconds(record.timestamp))
		metrics.HLP2PSampleAgeSeconds.Set(0)

		metrics.MarkSourceValidObservation(metrics.SourceTCPTraffic, record.timestamp)
		metrics.MarkSourcePublication(metrics.SourceTCPTraffic)
		metrics.MarkMonitorValidObservation("tcp_traffic")
		metrics.MarkMonitorPublication("tcp_traffic")
	})
}

func markTCPTrafficSourceSuccess() {
	metrics.WithPrometheusSnapshotUpdate(publishTCPTrafficSourceSuccess)
}

func publishTCPTrafficSourceSuccess() {
	metrics.HLP2PTCPTrafficSourceUp.Set(1)
	metrics.MarkSourceReadOutcome(metrics.SourceTCPTraffic, true)
	metrics.MarkSourceSchemaOutcome(metrics.SourceTCPTraffic, true)
}

func positiveEndpointSet(in, out []peerSample) map[string]struct{} {
	active := make(map[string]struct{}, len(in)+len(out))
	for _, list := range [][]peerSample{in, out} {
		for _, sample := range list {
			if sample.value > 0 {
				active[sample.ip] = struct{}{}
			}
		}
	}
	return active
}

func advanceTrafficAdmissions(record tcpTrafficRecord, receipt time.Time, state *tcpTrafficMonitorState) {
	if !state.lastReceipt.IsZero() && receipt.Sub(state.lastReceipt) > tcpAdmissionMaxGap {
		clear(state.admission)
	}
	eligible := make(map[string]struct{}, tcpTrafficTopN*2)
	for _, list := range [][]peerSample{record.inbound, record.outbound} {
		n := 0
		for _, sample := range list {
			if sample.value <= 0 {
				continue
			}
			if n >= tcpTrafficTopN {
				break
			}
			eligible[sample.ip] = struct{}{}
			n++
		}
	}
	for ip := range state.admission {
		if _, ok := eligible[ip]; !ok {
			delete(state.admission, ip)
		}
	}
	for ip := range eligible {
		candidate := state.admission[ip]
		candidate.streak++
		state.admission[ip] = candidate
		if candidate.streak >= 2 {
			state.registry.Register(ip, "", receipt)
		}
	}
}

func publishTCPDirection(direction string, samples []peerSample, prev map[string]bool) {
	current := make(map[string]bool, tcpTrafficTopN+1)
	var total, other float64
	n := 0
	for _, sample := range samples {
		total += sample.value
		if sample.value <= 0 {
			continue
		}
		if n < tcpTrafficTopN {
			metrics.HLP2PPeerTraffic.WithLabelValues(sample.ip, direction).Set(sample.value)
			current[sample.ip] = true
			n++
		} else {
			other += sample.value
		}
	}
	if other > 0 {
		metrics.HLP2PPeerTraffic.WithLabelValues("other", direction).Set(other)
		current["other"] = true
	}
	for ip := range prev {
		if !current[ip] {
			metrics.HLP2PPeerTraffic.DeleteLabelValues(ip, direction)
			delete(prev, ip)
		}
	}
	for ip := range current {
		prev[ip] = true
	}
	metrics.HLP2PTotalTraffic.WithLabelValues(direction).Set(total)
	metrics.HLP2PPeerCount.WithLabelValues(direction).Set(float64(len(samples)))
}

func publishTrafficPorts(byPort map[string]map[string]float64, servicePorts []uint16) {
	labels := servicePortLabels(servicePorts)
	for _, direction := range []string{"in", "out"} {
		for _, port := range labels {
			metrics.HLP2PTCPTrafficByServicePort.WithLabelValues(port, direction).Set(byPort[direction][port])
		}
	}
}

func servicePortLabels(servicePorts []uint16) []string {
	ports, _ := validateTCPServicePorts(servicePorts)
	out := make([]string, 0, len(ports)+1)
	for _, port := range ports {
		out = append(out, strconv.FormatUint(uint64(port), 10))
	}
	return append(out, "other")
}

func parseTCPTrafficLine(line []byte) (time.Time, []peerSample, []peerSample, bool) {
	record, err := parseTCPTrafficRecord(line)
	if err != nil {
		return time.Time{}, nil, nil, false
	}
	return record.timestamp, record.inbound, record.outbound, true
}

func parseTCPTrafficRecord(line []byte) (tcpTrafficRecord, error) {
	return parseTCPTrafficRecordWithPorts(line, tcpListenPorts)
}

func parseTCPTrafficRecordWithPorts(line []byte, servicePorts []uint16) (tcpTrafficRecord, error) {
	var outer []json.RawMessage
	if err := json.Unmarshal(line, &outer); err != nil || len(outer) != 2 {
		return tcpTrafficRecord{}, &tcpTrafficParseError{stage: "record", err: errors.New("outer record must have arity two")}
	}
	var tsString string
	if err := unmarshalRequiredJSON(outer[0], &tsString); err != nil {
		return tcpTrafficRecord{}, &tcpTrafficParseError{stage: "timestamp", err: err}
	}
	timestamp, ok := parseVisorTime(tsString)
	if !ok {
		return tcpTrafficRecord{}, &tcpTrafficParseError{stage: "timestamp", err: errors.New("invalid timestamp")}
	}
	var flows []json.RawMessage
	if !rawJSONArray(outer[1]) {
		return tcpTrafficRecord{}, &tcpTrafficParseError{stage: "record", err: errors.New("flow payload must be an array")}
	}
	if err := json.Unmarshal(outer[1], &flows); err != nil {
		return tcpTrafficRecord{}, &tcpTrafficParseError{stage: "record", err: err}
	}

	inAcc := make(map[string]float64)
	outAcc := make(map[string]float64)
	byPort := map[string]map[string]float64{"in": {}, "out": {}}
	allowedPorts := make(map[uint16]string, len(servicePorts))
	for _, port := range servicePorts {
		allowedPorts[port] = strconv.FormatUint(uint64(port), 10)
	}
	for _, raw := range flows {
		var pair []json.RawMessage
		if err := json.Unmarshal(raw, &pair); err != nil || len(pair) != 2 {
			return tcpTrafficRecord{}, &tcpTrafficParseError{stage: "row", err: errors.New("flow must have arity two")}
		}
		var key []json.RawMessage
		if err := json.Unmarshal(pair[0], &key); err != nil || len(key) != 3 {
			return tcpTrafficRecord{}, &tcpTrafficParseError{stage: "row", err: errors.New("flow key must have arity three")}
		}
		var upstreamDirection, ipString string
		var port uint16
		var value float64
		if err := unmarshalRequiredJSON(key[0], &upstreamDirection); err != nil {
			return tcpTrafficRecord{}, &tcpTrafficParseError{stage: "row", err: err}
		}
		if err := unmarshalRequiredJSON(key[1], &ipString); err != nil {
			return tcpTrafficRecord{}, &tcpTrafficParseError{stage: "row", err: err}
		}
		if err := unmarshalRequiredJSON(key[2], &port); err != nil || port == 0 {
			return tcpTrafficRecord{}, &tcpTrafficParseError{stage: "row", err: errors.New("invalid port")}
		}
		if err := unmarshalRequiredJSON(pair[1], &value); err != nil || math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
			return tcpTrafficRecord{}, &tcpTrafficParseError{stage: "row", err: errors.New("value must be finite and nonnegative")}
		}
		ip, err := netip.ParseAddr(ipString)
		if err != nil || ip.Zone() != "" {
			return tcpTrafficRecord{}, &tcpTrafficParseError{stage: "row", err: errors.New("invalid IP")}
		}
		ipString = ip.Unmap().String()
		direction := ""
		switch upstreamDirection {
		case "In":
			direction = "in"
		case "Out":
			direction = "out"
		default:
			return tcpTrafficRecord{}, &tcpTrafficParseError{stage: "row", err: errors.New("unknown direction")}
		}
		endpointAcc := inAcc
		if direction == "out" {
			endpointAcc = outAcc
		}
		if !addFiniteTrafficValue(endpointAcc, ipString, value) {
			return tcpTrafficRecord{}, &tcpTrafficParseError{stage: "row", err: errors.New("endpoint aggregate overflow")}
		}
		portLabel := allowedPorts[port]
		if portLabel == "" {
			portLabel = "other"
		}
		if !addFiniteTrafficValue(byPort[direction], portLabel, value) {
			return tcpTrafficRecord{}, &tcpTrafficParseError{stage: "row", err: errors.New("service-port aggregate overflow")}
		}
	}
	for _, values := range []map[string]float64{inAcc, outAcc} {
		total := 0.0
		for _, value := range values {
			total += value
			if math.IsInf(total, 0) || math.IsNaN(total) {
				return tcpTrafficRecord{}, &tcpTrafficParseError{stage: "row", err: errors.New("direction aggregate overflow")}
			}
		}
	}

	in := samplesFromAccumulator("in", inAcc)
	out := samplesFromAccumulator("out", outAcc)
	return tcpTrafficRecord{timestamp: timestamp, inbound: in, outbound: out, byPort: byPort}, nil
}

func addFiniteTrafficValue(values map[string]float64, key string, value float64) bool {
	next := values[key] + value
	if math.IsNaN(next) || math.IsInf(next, 0) {
		return false
	}
	values[key] = next
	return true
}

func rawJSONArray(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) > 0 && trimmed[0] == '['
}

func samplesFromAccumulator(direction string, values map[string]float64) []peerSample {
	out := make([]peerSample, 0, len(values))
	for ip, value := range values {
		out = append(out, peerSample{dir: direction, ip: ip, value: value})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].value != out[j].value {
			return out[i].value > out[j].value
		}
		return out[i].ip < out[j].ip
	})
	return out
}

// lastFullLine returns the newest non-empty newline-committed record. Any
// unterminated suffix is ignored and never treated as a snapshot boundary.
func lastFullLine(data []byte) ([]byte, bool) {
	boundary := bytes.LastIndexByte(data, '\n')
	if boundary < 0 {
		return nil, false
	}
	committed := data[:boundary]
	for len(committed) > 0 {
		end := len(committed)
		start := bytes.LastIndexByte(committed, '\n') + 1
		line := bytes.TrimSpace(committed[start:end])
		if len(line) > 0 {
			return line, true
		}
		if start == 0 {
			break
		}
		committed = committed[:start-1]
	}
	return nil, false
}

func nonnegativeDuration(d time.Duration) time.Duration {
	if d < 0 {
		return 0
	}
	return d
}

func unixTimestampSeconds(t time.Time) float64 {
	return float64(t.Unix()) + float64(t.Nanosecond())/1e9
}
