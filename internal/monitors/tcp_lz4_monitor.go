package monitors

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"net/netip"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/validaoxyz/hyperliquid-exporter/internal/config"
	"github.com/validaoxyz/hyperliquid-exporter/internal/logger"
	"github.com/validaoxyz/hyperliquid-exporter/internal/metrics"
)

const (
	tcpLz4PollInterval  = 60 * time.Second
	tcpLz4TopN          = 16
	tcpLz4PairTolerance = 5 * time.Second
	tcpLz4IdentityTTL   = 10 * time.Minute
	maxLz4LinesToScan   = 64
)

type lz4PeerSample struct {
	direction string
	ip        string
	port      uint16
	bytes     int64
	packets   int64
	ratio     float64
}

type lz4PeerRecord struct {
	timestamp time.Time
	peers     []lz4PeerSample
}

type lz4GlobalRecord struct {
	timestamp time.Time
	bytes     int64
	packets   int64
	ratio     float64
}

type lz4Pair struct {
	peer   lz4PeerRecord
	global lz4GlobalRecord
}

type lz4MonitorState struct {
	lastPairKey       string
	lastReceipt       time.Time
	previousSource    time.Time
	prevPublished     map[string]map[string]bool
	identitiesExpired bool
	servicePorts      []uint16
}

func newLz4MonitorState(configured ...[]uint16) *lz4MonitorState {
	ports := tcpListenPorts
	if len(configured) > 0 && len(configured[0]) > 0 {
		ports = configured[0]
	}
	ports, _ = validateTCPServicePorts(ports)
	return &lz4MonitorState{
		prevPublished: map[string]map[string]bool{"in": {}, "out": {}},
		servicePorts:  ports,
	}
}

func StartTCPLz4Monitor(ctx context.Context, cfg config.Config, errCh chan<- error) {
	root := filepath.Join(cfg.NodeHome, "data", "tcp_lz4_stats")
	metrics.RegisterSource(metrics.SourceTCPLZ4, true)
	logger.InfoComponent("tcp_lz4", "watching %s (late discovery enabled)", root)
	ports, err := tcpServicePortsForConfig(cfg)
	if err != nil {
		metrics.MarkSourceError(metrics.SourceTCPLZ4, metrics.SourceFailureSchema)
		logger.ErrorComponent("tcp_lz4", "invalid service-port configuration: %v", err)
		<-ctx.Done()
		return
	}
	state := newLz4MonitorState(ports)
	ticker := time.NewTicker(tcpLz4PollInterval)
	defer ticker.Stop()

	tickLz4(root, state)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			tickLz4(root, state)
		}
	}
}

func tickLz4(root string, state *lz4MonitorState) {
	metrics.MarkMonitorAttempt("tcp_lz4")
	metrics.MarkSourceAttempt(metrics.SourceTCPLZ4)
	now := time.Now()
	if !state.lastReceipt.IsZero() {
		metrics.WithPrometheusSnapshotUpdate(func() {
			metrics.HLP2PLZ4SampleAgeSeconds.Set(nonnegativeDuration(now.Sub(state.lastReceipt)).Seconds())
			if now.Sub(state.lastReceipt) > tcpLz4IdentityTTL && !state.identitiesExpired {
				clearLz4PeerSeries(state.prevPublished)
				state.identitiesExpired = true
			}
		})
	}

	datePath, err := latestDateFile(root)
	if err != nil {
		lz4Failure(metrics.SourceFailureDiscovery, err)
		return
	}
	data, err := os.ReadFile(datePath)
	if err != nil {
		lz4Failure(metrics.SourceFailureRead, err)
		return
	}
	pair, err := selectLatestLZ4Pair(data)
	if err != nil {
		lz4Failure(metrics.SourceFailureSchema, err)
		return
	}
	key := lz4PairKey(pair)
	if key == state.lastPairKey {
		markLz4SourceSuccess()
		return
	}
	sourceTime := pairTimestamp(pair)
	if !state.previousSource.IsZero() && sourceTime.Before(state.previousSource) {
		lz4Failure(metrics.SourceFailureSchema, errors.New("LZ4 source timestamp regressed"))
		return
	}

	receipt := time.Now()
	if err := commitLZ4Pair(pair, receipt, state); err != nil {
		lz4Failure(metrics.SourceFailureSchema, err)
		return
	}
	markLz4SourceSuccess()
	state.lastPairKey = key
	state.lastReceipt = receipt
	state.previousSource = sourceTime
	state.identitiesExpired = false
}

func markLz4SourceSuccess() {
	metrics.WithPrometheusSnapshotUpdate(publishLz4SourceSuccess)
}

func publishLz4SourceSuccess() {
	metrics.HLP2PLZ4SourceUp.Set(1)
	metrics.MarkSourceReadOutcome(metrics.SourceTCPLZ4, true)
	metrics.MarkSourceSchemaOutcome(metrics.SourceTCPLZ4, true)
}

func lz4Failure(stage metrics.SourceFailureStage, err error) {
	metrics.WithPrometheusSnapshotUpdate(func() {
		metrics.HLP2PLZ4SourceUp.Set(0)
		metrics.MarkSourceError(metrics.SourceTCPLZ4, stage)
		if stage == metrics.SourceFailureDiscovery && errors.Is(err, os.ErrNotExist) {
			metrics.MarkSourceAbsent(metrics.SourceTCPLZ4)
		}
		metrics.IncMonitorError("tcp_lz4")
	})
}

func pairTimestamp(pair lz4Pair) time.Time {
	if pair.global.timestamp.After(pair.peer.timestamp) {
		return pair.global.timestamp
	}
	return pair.peer.timestamp
}

// lz4PairKey identifies the complete semantic contents of one paired window,
// not only its timestamps. This lets a corrected/replaced committed record at
// the same source time reconcile stale values while keeping byte/order-only
// rewrites of an equivalent peer multiset idempotent.
func lz4PairKey(pair lz4Pair) string {
	peers := append([]lz4PeerSample(nil), pair.peer.peers...)
	sort.Slice(peers, func(i, j int) bool {
		a, b := peers[i], peers[j]
		switch {
		case a.direction != b.direction:
			return a.direction < b.direction
		case a.ip != b.ip:
			return a.ip < b.ip
		case a.port != b.port:
			return a.port < b.port
		case a.bytes != b.bytes:
			return a.bytes < b.bytes
		case a.packets != b.packets:
			return a.packets < b.packets
		default:
			return a.ratio < b.ratio
		}
	})

	var canonical strings.Builder
	appendLz4KeyField(&canonical, pair.peer.timestamp.Format(time.RFC3339Nano))
	for _, peer := range peers {
		appendLz4KeyField(&canonical, peer.direction)
		appendLz4KeyField(&canonical, peer.ip)
		appendLz4KeyField(&canonical, strconv.FormatUint(uint64(peer.port), 10))
		appendLz4KeyField(&canonical, strconv.FormatInt(peer.bytes, 10))
		appendLz4KeyField(&canonical, strconv.FormatInt(peer.packets, 10))
		appendLz4KeyField(&canonical, strconv.FormatFloat(peer.ratio, 'g', -1, 64))
	}
	appendLz4KeyField(&canonical, pair.global.timestamp.Format(time.RFC3339Nano))
	appendLz4KeyField(&canonical, strconv.FormatInt(pair.global.bytes, 10))
	appendLz4KeyField(&canonical, strconv.FormatInt(pair.global.packets, 10))
	appendLz4KeyField(&canonical, strconv.FormatFloat(pair.global.ratio, 'g', -1, 64))

	sum := sha256.Sum256([]byte(canonical.String()))
	return hex.EncodeToString(sum[:])
}

func appendLz4KeyField(out *strings.Builder, value string) {
	out.WriteString(strconv.Itoa(len(value)))
	out.WriteByte(':')
	out.WriteString(value)
}

func commitLZ4Pair(pair lz4Pair, receipt time.Time, state *lz4MonitorState) error {
	prepared, err := prepareLz4Peers(pair.peer.peers, state.servicePorts)
	if err != nil {
		return err
	}
	sourceTime := pairTimestamp(pair)
	duration := 0.0
	publishDuration := state.previousSource.IsZero()
	if !state.previousSource.IsZero() && sourceTime.After(state.previousSource) {
		duration = sourceTime.Sub(state.previousSource).Seconds()
		publishDuration = true
	}
	metrics.WithPrometheusSnapshotUpdate(func() {
		publishLz4SourceSuccess()
		publishLz4Peers(prepared, state.prevPublished, state.servicePorts)
		metrics.HLP2PLZ4GlobalWindowBytes.Set(float64(pair.global.bytes))
		metrics.HLP2PLZ4GlobalWindowPackets.Set(float64(pair.global.packets))
		metrics.HLP2PLZ4GlobalWindowRatio.Set(pair.global.ratio)
		metrics.HLP2PLz4GlobalBytes.Set(float64(pair.global.bytes)) // one-release gauge aliases
		metrics.HLP2PLz4GlobalPackets.Set(float64(pair.global.packets))
		metrics.HLP2PLz4GlobalRatio.Set(pair.global.ratio)
		// A same-source-time content replacement corrects the current window;
		// it must not erase the already observed inter-window duration.
		if publishDuration {
			metrics.HLP2PLZ4WindowDurationSeconds.Set(duration)
		}
		metrics.HLP2PLZ4SampleTimestampSeconds.Set(unixTimestampSeconds(sourceTime))
		metrics.HLP2PLZ4SampleAgeSeconds.Set(0)
		metrics.MarkSourceValidObservation(metrics.SourceTCPLZ4, sourceTime)
		metrics.MarkSourcePublication(metrics.SourceTCPLZ4)
		metrics.MarkMonitorValidObservation("tcp_lz4")
		metrics.MarkMonitorPublication("tcp_lz4")
	})
	_ = receipt // receipt is retained by the caller after the atomic commit boundary
	return nil
}

type lz4Aggregate struct {
	bytes       float64
	packets     float64
	ratioWeight float64
	ratioBytes  float64
}

type rankedLz4Aggregate struct {
	ip  string
	agg *lz4Aggregate
}

type preparedLz4Direction struct {
	top   []rankedLz4Aggregate
	other *lz4Aggregate
}

type preparedLz4Publication struct {
	directions map[string]preparedLz4Direction
	byPort     map[string]map[string]*lz4Aggregate
}

// prepareLz4Peers derives and validates the complete publishable generation
// before touching any metric. This keeps numeric/schema failures from partly
// reconciling the prior good snapshot.
func prepareLz4Peers(peers []lz4PeerSample, servicePorts []uint16) (preparedLz4Publication, error) {
	byEndpoint := map[string]map[string]*lz4Aggregate{"in": {}, "out": {}}
	byPort := map[string]map[string]*lz4Aggregate{"in": {}, "out": {}}
	byDirection := map[string]*lz4Aggregate{"in": {}, "out": {}}
	allowed := make(map[uint16]string, len(servicePorts))
	for _, port := range servicePorts {
		allowed[port] = strconv.FormatUint(uint64(port), 10)
	}
	for _, peer := range peers {
		if byEndpoint[peer.direction] == nil {
			return preparedLz4Publication{}, errors.New("invalid LZ4 aggregate direction")
		}
		agg := byEndpoint[peer.direction][peer.ip]
		if agg == nil {
			agg = &lz4Aggregate{}
			byEndpoint[peer.direction][peer.ip] = agg
		}
		if err := addLz4Sample(agg, peer); err != nil {
			return preparedLz4Publication{}, err
		}
		port := allowed[peer.port]
		if port == "" {
			port = "other"
		}
		portAgg := byPort[peer.direction][port]
		if portAgg == nil {
			portAgg = &lz4Aggregate{}
			byPort[peer.direction][port] = portAgg
		}
		if err := addLz4Sample(portAgg, peer); err != nil {
			return preparedLz4Publication{}, err
		}
		if err := addLz4Sample(byDirection[peer.direction], peer); err != nil {
			return preparedLz4Publication{}, err
		}
	}

	prepared := preparedLz4Publication{
		directions: make(map[string]preparedLz4Direction, 2),
		byPort:     byPort,
	}
	for _, direction := range []string{"in", "out"} {
		list := make([]rankedLz4Aggregate, 0, len(byEndpoint[direction]))
		for ip, agg := range byEndpoint[direction] {
			// All-zero rows contribute to bounded service/global aggregates but
			// never manufacture a raw-IP identity series.
			if agg.bytes == 0 && agg.packets == 0 {
				continue
			}
			list = append(list, rankedLz4Aggregate{ip: ip, agg: agg})
		}
		sort.Slice(list, func(i, j int) bool {
			if list[i].agg.bytes != list[j].agg.bytes {
				return list[i].agg.bytes > list[j].agg.bytes
			}
			return list[i].ip < list[j].ip
		})
		limit := len(list)
		if limit > tcpLz4TopN {
			limit = tcpLz4TopN
		}
		directionSnapshot := preparedLz4Direction{top: append([]rankedLz4Aggregate(nil), list[:limit]...)}
		if len(list) > tcpLz4TopN {
			other := &lz4Aggregate{}
			for _, item := range list[tcpLz4TopN:] {
				if err := mergeLz4Aggregate(other, item.agg); err != nil {
					return preparedLz4Publication{}, err
				}
			}
			directionSnapshot.other = other
		}
		prepared.directions[direction] = directionSnapshot
	}
	return prepared, nil
}

func publishLz4Peers(prepared preparedLz4Publication, prev map[string]map[string]bool, servicePorts []uint16) {
	for _, direction := range []string{"in", "out"} {
		snapshot := prepared.directions[direction]
		current := make(map[string]bool, tcpLz4TopN+1)
		for _, item := range snapshot.top {
			setLz4Endpoint(item.ip, direction, item.agg)
			current[item.ip] = true
		}
		if snapshot.other != nil {
			setLz4Endpoint("other", direction, snapshot.other)
			current["other"] = true
		}
		for ip := range prev[direction] {
			if !current[ip] {
				deleteLz4Endpoint(ip, direction)
				delete(prev[direction], ip)
			}
		}
		for ip := range current {
			prev[direction][ip] = true
		}

		for _, port := range servicePortLabels(servicePorts) {
			agg := prepared.byPort[direction][port]
			if agg == nil {
				agg = &lz4Aggregate{}
			}
			metrics.HLP2PLZ4WindowBytesByServicePort.WithLabelValues(port, direction).Set(float64(agg.bytes))
			metrics.HLP2PLZ4WindowPacketsByServicePort.WithLabelValues(port, direction).Set(float64(agg.packets))
		}
	}
}

func addLz4Sample(agg *lz4Aggregate, sample lz4PeerSample) error {
	bytesValue := float64(sample.bytes)
	packetsValue := float64(sample.packets)
	bytesTotal := agg.bytes + bytesValue
	packetsTotal := agg.packets + packetsValue
	if math.IsNaN(bytesTotal) || math.IsInf(bytesTotal, 0) || math.IsNaN(packetsTotal) || math.IsInf(packetsTotal, 0) {
		return errors.New("LZ4 byte or packet aggregate overflow")
	}
	agg.bytes = bytesTotal
	agg.packets = packetsTotal
	if sample.bytes > 0 {
		weight := sample.ratio * bytesValue
		weightTotal := agg.ratioWeight + weight
		ratioBytesTotal := agg.ratioBytes + bytesValue
		if math.IsNaN(weightTotal) || math.IsInf(weightTotal, 0) || math.IsNaN(ratioBytesTotal) || math.IsInf(ratioBytesTotal, 0) {
			return errors.New("LZ4 weighted-ratio aggregate overflow")
		}
		agg.ratioWeight = weightTotal
		agg.ratioBytes = ratioBytesTotal
	}
	return nil
}

func mergeLz4Aggregate(dst, src *lz4Aggregate) error {
	values := [4]float64{
		dst.bytes + src.bytes,
		dst.packets + src.packets,
		dst.ratioWeight + src.ratioWeight,
		dst.ratioBytes + src.ratioBytes,
	}
	for _, value := range values {
		if math.IsNaN(value) || math.IsInf(value, 0) {
			return errors.New("LZ4 overflow aggregate is non-finite")
		}
	}
	dst.bytes, dst.packets, dst.ratioWeight, dst.ratioBytes = values[0], values[1], values[2], values[3]
	return nil
}

func lz4Ratio(agg *lz4Aggregate) float64 {
	if agg.ratioBytes == 0 {
		return 0
	}
	return agg.ratioWeight / agg.ratioBytes
}

func setLz4Endpoint(ip, direction string, agg *lz4Aggregate) {
	ratio := lz4Ratio(agg)
	metrics.HLP2PLZ4WindowBytes.WithLabelValues(ip, direction).Set(agg.bytes)
	metrics.HLP2PLZ4WindowPackets.WithLabelValues(ip, direction).Set(agg.packets)
	metrics.HLP2PLZ4WindowWeightedRatio.WithLabelValues(ip, direction).Set(ratio)
	metrics.HLP2PLz4BytesTotal.WithLabelValues(ip, direction).Set(agg.bytes)
	metrics.HLP2PLz4PacketsTotal.WithLabelValues(ip, direction).Set(agg.packets)
	metrics.HLP2PLz4CompressionRatio.WithLabelValues(ip, direction).Set(ratio)
}

func deleteLz4Endpoint(ip, direction string) {
	metrics.HLP2PLZ4WindowBytes.DeleteLabelValues(ip, direction)
	metrics.HLP2PLZ4WindowPackets.DeleteLabelValues(ip, direction)
	metrics.HLP2PLZ4WindowWeightedRatio.DeleteLabelValues(ip, direction)
	metrics.HLP2PLz4BytesTotal.DeleteLabelValues(ip, direction)
	metrics.HLP2PLz4PacketsTotal.DeleteLabelValues(ip, direction)
	metrics.HLP2PLz4CompressionRatio.DeleteLabelValues(ip, direction)
}

func clearLz4PeerSeries(prev map[string]map[string]bool) {
	for direction, labels := range prev {
		for ip := range labels {
			deleteLz4Endpoint(ip, direction)
			delete(labels, ip)
		}
	}
}

func selectLatestLZ4Pair(data []byte) (lz4Pair, error) {
	lines := committedLinesReverse(data, maxLz4LinesToScan)
	if len(lines) == 0 {
		return lz4Pair{}, errors.New("no complete LZ4 record")
	}
	peers := make([]lz4PeerRecord, 0, 4)
	globals := make([]lz4GlobalRecord, 0, 4)
	for _, line := range lines {
		kind, timestamp, payload, err := decodeLz4Envelope(line)
		if err != nil {
			return lz4Pair{}, err
		}
		switch kind {
		case "peer":
			rows, err := parseLz4PeerPayload(payload)
			if err != nil {
				return lz4Pair{}, err
			}
			peers = append(peers, lz4PeerRecord{timestamp: timestamp, peers: rows})
		case "global":
			global, err := parseLz4GlobalPayload(payload)
			if err != nil {
				return lz4Pair{}, err
			}
			global.timestamp = timestamp
			globals = append(globals, global)
		}
	}
	if len(peers) == 0 || len(globals) == 0 {
		return lz4Pair{}, errors.New("latest scan lacks peer/global pair")
	}
	if pair, ok := newestCompatibleLZ4Pair(peers, globals); ok {
		return pair, nil
	}
	return lz4Pair{}, errors.New("latest peer/global records are from different windows")
}

func newestCompatibleLZ4Pair(peers []lz4PeerRecord, globals []lz4GlobalRecord) (lz4Pair, bool) {
	var best lz4Pair
	bestDelta := time.Duration(0)
	found := false
	for _, peer := range peers {
		for _, global := range globals {
			delta := peer.timestamp.Sub(global.timestamp)
			if delta < 0 {
				delta = -delta
			}
			if delta > tcpLz4PairTolerance {
				continue
			}
			candidate := lz4Pair{peer: peer, global: global}
			candidateTime := pairTimestamp(candidate)
			bestTime := pairTimestamp(best)
			if !found || candidateTime.After(bestTime) ||
				(candidateTime.Equal(bestTime) && delta < bestDelta) ||
				(candidateTime.Equal(bestTime) && delta == bestDelta && peer.timestamp.After(best.peer.timestamp)) {
				best = candidate
				bestDelta = delta
				found = true
			}
		}
	}
	return best, found
}

func committedLinesReverse(data []byte, limit int) [][]byte {
	boundary := bytes.LastIndexByte(data, '\n')
	if boundary < 0 {
		return nil
	}
	data = data[:boundary]
	out := make([][]byte, 0, limit)
	for len(data) > 0 && len(out) < limit {
		start := bytes.LastIndexByte(data, '\n') + 1
		line := bytes.TrimSpace(data[start:])
		if len(line) > 0 {
			out = append(out, append([]byte(nil), line...))
		}
		if start == 0 {
			break
		}
		data = data[:start-1]
	}
	return out
}

func decodeLz4Envelope(line []byte) (string, time.Time, json.RawMessage, error) {
	var outer []json.RawMessage
	if err := json.Unmarshal(line, &outer); err != nil || len(outer) != 2 {
		return "", time.Time{}, nil, errors.New("invalid LZ4 envelope")
	}
	var tsString string
	if err := unmarshalRequiredJSON(outer[0], &tsString); err != nil {
		return "", time.Time{}, nil, errors.New("invalid LZ4 timestamp")
	}
	timestamp, ok := parseVisorTime(tsString)
	if !ok {
		return "", time.Time{}, nil, errors.New("invalid LZ4 timestamp")
	}
	var entries []json.RawMessage
	if !rawJSONArray(outer[1]) {
		return "", time.Time{}, nil, errors.New("LZ4 payload must be an array")
	}
	if err := json.Unmarshal(outer[1], &entries); err != nil {
		return "", time.Time{}, nil, errors.New("invalid LZ4 payload")
	}
	if len(entries) == 0 {
		return "peer", timestamp, outer[1], nil
	}
	var number float64
	if len(entries) == 3 && unmarshalRequiredJSON(entries[0], &number) == nil {
		return "global", timestamp, outer[1], nil
	}
	return "peer", timestamp, outer[1], nil
}

func parseLz4PeerPayload(payload json.RawMessage) ([]lz4PeerSample, error) {
	var entries []json.RawMessage
	if !rawJSONArray(payload) {
		return nil, errors.New("LZ4 peer payload must be an array")
	}
	if err := json.Unmarshal(payload, &entries); err != nil {
		return nil, err
	}
	out := make([]lz4PeerSample, 0, len(entries))
	for _, raw := range entries {
		var entry []json.RawMessage
		if err := json.Unmarshal(raw, &entry); err != nil || len(entry) != 4 {
			return nil, errors.New("LZ4 peer row must have arity four")
		}
		var key []json.RawMessage
		if err := json.Unmarshal(entry[0], &key); err != nil || len(key) != 3 {
			return nil, errors.New("LZ4 peer key must have arity three")
		}
		var upstreamDirection, ipString string
		var port uint16
		var bytesN, packetsN int64
		var ratio float64
		if unmarshalRequiredJSON(key[0], &upstreamDirection) != nil || unmarshalRequiredJSON(key[1], &ipString) != nil || unmarshalRequiredJSON(key[2], &port) != nil || port == 0 ||
			unmarshalRequiredJSON(entry[1], &bytesN) != nil || bytesN < 0 || unmarshalRequiredJSON(entry[2], &packetsN) != nil || packetsN < 0 ||
			unmarshalRequiredJSON(entry[3], &ratio) != nil || math.IsNaN(ratio) || math.IsInf(ratio, 0) || ratio < 0 {
			return nil, errors.New("invalid LZ4 peer row")
		}
		ip, err := netip.ParseAddr(ipString)
		if err != nil || ip.Zone() != "" {
			return nil, errors.New("invalid LZ4 peer IP")
		}
		direction := ""
		switch upstreamDirection {
		case "In":
			direction = "in"
		case "Out":
			direction = "out"
		default:
			return nil, errors.New("invalid LZ4 direction")
		}
		out = append(out, lz4PeerSample{direction: direction, ip: ip.Unmap().String(), port: port, bytes: bytesN, packets: packetsN, ratio: ratio})
	}
	return out, nil
}

func parseLz4GlobalPayload(payload json.RawMessage) (lz4GlobalRecord, error) {
	var inner []json.RawMessage
	if err := json.Unmarshal(payload, &inner); err != nil || len(inner) != 3 {
		return lz4GlobalRecord{}, errors.New("invalid LZ4 global payload")
	}
	var out lz4GlobalRecord
	if unmarshalRequiredJSON(inner[0], &out.bytes) != nil || out.bytes < 0 || unmarshalRequiredJSON(inner[1], &out.packets) != nil || out.packets < 0 ||
		unmarshalRequiredJSON(inner[2], &out.ratio) != nil || math.IsNaN(out.ratio) || math.IsInf(out.ratio, 0) || out.ratio < 0 {
		return lz4GlobalRecord{}, errors.New("invalid LZ4 global values")
	}
	return out, nil
}

func parseLz4PeerLine(line []byte) ([]lz4PeerSample, bool) {
	kind, _, payload, err := decodeLz4Envelope(line)
	if err != nil || kind != "peer" {
		return nil, false
	}
	peers, err := parseLz4PeerPayload(payload)
	return peers, err == nil
}

func parseLz4GlobalLine(line []byte) (int64, int64, float64, bool) {
	kind, _, payload, err := decodeLz4Envelope(line)
	if err != nil || kind != "global" {
		return 0, 0, 0, false
	}
	global, err := parseLz4GlobalPayload(payload)
	return global.bytes, global.packets, global.ratio, err == nil
}
