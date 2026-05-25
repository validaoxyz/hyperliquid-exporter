package monitors

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/validaoxyz/hyperliquid-exporter/internal/config"
	"github.com/validaoxyz/hyperliquid-exporter/internal/logger"
	"github.com/validaoxyz/hyperliquid-exporter/internal/metrics"
)

// tcpTrafficPollInterval matches the node's own ~30s sample cadence. There
// is no value in polling faster than the file changes.
const tcpTrafficPollInterval = 30 * time.Second

// tcpTrafficTopN bounds the published per-IP cardinality per direction.
// Everything below the cutoff is rolled into a single ip="other" bucket.
// 16 keeps the dashboard readable while still surfacing the top inbound
// + outbound peers that operators actually care about.
const tcpTrafficTopN = 16

// peerSample is one (direction, ip) → throughput row from the latest
// tcp_traffic line.
type peerSample struct {
	dir   string // "in" or "out"
	ip    string
	value float64
}

// LatestTCPTrafficSample exposes the most recent parsed tcp_traffic sample
// to other monitors (specifically parent_peer_monitor) so they don't each
// re-parse the file. The whole struct is replaced atomically under mu so
// readers get a consistent (ts, inbound, outbound) triple.
type LatestTCPTrafficSample struct {
	mu        sync.Mutex
	timestamp time.Time
	inbound   []peerSample // sorted desc by value
	outbound  []peerSample // sorted desc by value
}

var sharedTCPTraffic LatestTCPTrafficSample

// LatestTCPTrafficSnapshot returns the most recent tcp_traffic sample plus
// its source timestamp. Empty slices and a zero time are returned when no
// sample has been read yet. The returned slices alias the monitor's
// internal state; treat them as read-only.
func LatestTCPTrafficSnapshot() (ts time.Time, in []peerSample, out []peerSample) {
	sharedTCPTraffic.mu.Lock()
	defer sharedTCPTraffic.mu.Unlock()
	return sharedTCPTraffic.timestamp, sharedTCPTraffic.inbound, sharedTCPTraffic.outbound
}

// StartTCPTrafficMonitor watches $NODE_HOME/data/tcp_traffic/hourly. Each
// line of the latest hour-file is a snapshot of per-peer throughput. We
// only consume the last line per tick.
func StartTCPTrafficMonitor(ctx context.Context, cfg config.Config, errCh chan<- error) {
	root := filepath.Join(cfg.NodeHome, "data", "tcp_traffic", "hourly")

	if _, err := os.Stat(root); err != nil {
		logger.InfoComponent("tcp_traffic",
			"tcp_traffic directory not present (%s); monitor idle", root)
		<-ctx.Done()
		return
	}

	logger.InfoComponent("tcp_traffic", "watching %s", root)

	ticker := time.NewTicker(tcpTrafficPollInterval)
	defer ticker.Stop()

	// Track which IPs are currently in the gauge vec per direction so we
	// can Delete() series when an IP drops out of the top-N. Without this
	// the per-IP cardinality would creep upward over time.
	prevPublished := map[string]map[string]bool{
		"in":  {},
		"out": {},
	}

	tickTCPTraffic(root, prevPublished)
	metrics.MarkMonitorTick("tcp_traffic")

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			tickTCPTraffic(root, prevPublished)
			metrics.MarkMonitorTick("tcp_traffic")
		}
	}
}

func tickTCPTraffic(root string, prevPublished map[string]map[string]bool) {
	filePath, err := latestHourlyFile(root)
	if err != nil {
		return
	}
	data, err := os.ReadFile(filePath)
	if err != nil {
		return
	}
	line, ok := lastFullLine(data)
	if !ok {
		return
	}
	sampleTime, in, out, ok := parseTCPTrafficLine(line)
	if !ok {
		return
	}

	sort.Slice(in, func(a, b int) bool { return in[a].value > in[b].value })
	sort.Slice(out, func(a, b int) bool { return out[a].value > out[b].value })

	sharedTCPTraffic.mu.Lock()
	sharedTCPTraffic.timestamp = sampleTime
	sharedTCPTraffic.inbound = in
	sharedTCPTraffic.outbound = out
	sharedTCPTraffic.mu.Unlock()

	publishTCPDirection("in", in, prevPublished["in"])
	publishTCPDirection("out", out, prevPublished["out"])

	if !sampleTime.IsZero() {
		metrics.HLP2PSampleAgeSeconds.Set(time.Since(sampleTime).Seconds())
	}
}

func publishTCPDirection(direction string, samples []peerSample, prev map[string]bool) {
	current := make(map[string]bool, tcpTrafficTopN+1)

	var topSum, otherSum float64
	for i, s := range samples {
		if i < tcpTrafficTopN {
			metrics.HLP2PPeerTraffic.WithLabelValues(s.ip, direction).Set(s.value)
			current[s.ip] = true
			topSum += s.value
		} else {
			otherSum += s.value
		}
	}
	if otherSum > 0 {
		metrics.HLP2PPeerTraffic.WithLabelValues("other", direction).Set(otherSum)
		current["other"] = true
	}

	// Drop series for IPs that are no longer in the top-N.
	for ip := range prev {
		if !current[ip] {
			metrics.HLP2PPeerTraffic.DeleteLabelValues(ip, direction)
			delete(prev, ip)
		}
	}
	for ip := range current {
		prev[ip] = true
	}

	metrics.HLP2PTotalTraffic.WithLabelValues(direction).Set(topSum + otherSum)
	metrics.HLP2PPeerCount.WithLabelValues(direction).Set(float64(len(samples)))
}

// parseTCPTrafficLine consumes one record of the form
//
//	["<iso>", [[["In"|"Out", "<ip>", <port>], <bytes>], ...]]
//
// Returns inbound and outbound slices separately. Returns ok=false on any
// structural mismatch.
func parseTCPTrafficLine(line []byte) (time.Time, []peerSample, []peerSample, bool) {
	var outer [2]json.RawMessage
	if err := json.Unmarshal(line, &outer); err != nil {
		return time.Time{}, nil, nil, false
	}
	var tsStr string
	if err := json.Unmarshal(outer[0], &tsStr); err != nil {
		return time.Time{}, nil, nil, false
	}
	ts, _ := parseVisorTime(tsStr)

	var flows []json.RawMessage
	if err := json.Unmarshal(outer[1], &flows); err != nil {
		return ts, nil, nil, false
	}

	// Sum values per (direction, ip). The hl-node tcp_traffic file is
	// keyed by (direction, ip, port), so a single peer that holds both
	// the 4001 and 4002 connections shows up as two rows. For the
	// purposes of "which peer is delivering the most data" we want one
	// row per IP-direction.
	inAcc := map[string]float64{}
	outAcc := map[string]float64{}
	for _, flow := range flows {
		var pair [2]json.RawMessage
		if err := json.Unmarshal(flow, &pair); err != nil {
			continue
		}
		var key [3]json.RawMessage
		if err := json.Unmarshal(pair[0], &key); err != nil {
			continue
		}
		var dirStr, ip string
		if err := json.Unmarshal(key[0], &dirStr); err != nil {
			continue
		}
		if err := json.Unmarshal(key[1], &ip); err != nil {
			continue
		}
		var value float64
		if err := json.Unmarshal(pair[1], &value); err != nil {
			continue
		}
		switch dirStr {
		case "In":
			inAcc[ip] += value
		case "Out":
			outAcc[ip] += value
		}
	}

	in := make([]peerSample, 0, len(inAcc))
	for ip, v := range inAcc {
		in = append(in, peerSample{dir: "in", ip: ip, value: v})
	}
	out := make([]peerSample, 0, len(outAcc))
	for ip, v := range outAcc {
		out = append(out, peerSample{dir: "out", ip: ip, value: v})
	}
	return ts, in, out, true
}

// lastFullLine returns the last complete newline-terminated record in
// data, scanning backwards over any torn trailing write.
func lastFullLine(data []byte) ([]byte, bool) {
	end := len(data)
	for end > 0 && data[end-1] == '\n' {
		end--
	}
	if end == 0 {
		return nil, false
	}
	start := bytes.LastIndexByte(data[:end], '\n') + 1
	line := bytes.TrimSpace(data[start:end])
	if len(line) == 0 {
		return nil, false
	}
	return line, true
}
