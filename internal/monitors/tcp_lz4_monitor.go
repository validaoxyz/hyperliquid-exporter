package monitors

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/validaoxyz/hyperliquid-exporter/internal/config"
	"github.com/validaoxyz/hyperliquid-exporter/internal/logger"
	"github.com/validaoxyz/hyperliquid-exporter/internal/metrics"
)

const (
	tcpLz4PollInterval = 60 * time.Second
	tcpLz4TopN         = 16
)

// lz4PeerSample is one peer row from the latest line.
type lz4PeerSample struct {
	direction string // "in" / "out"
	ip        string
	bytes     int64
	packets   int64
	ratio     float64
}

// StartTCPLz4Monitor watches $NODE_HOME/data/tcp_lz4_stats/<YYYYMMDD>.
//
// hl-node writes the file every 5 minutes (300 s sample windows), so a
// 60-second poll is more than enough; we only re-parse when the file's
// mtime changed.
//
// Each tick yields two records (microseconds apart on the same
// timestamp):
//   - a peer-level array:  [ts, [[[dir, ip, port], bytes, packets, ratio], ...]]
//   - a global aggregate:  [ts, [bytes, packets, ratio]]
//
// We take the most recent of each. Per-peer cardinality is capped at
// tcpLz4TopN per direction with an ip="other" rollup, mirroring the
// pattern in tcp_traffic_monitor.
func StartTCPLz4Monitor(ctx context.Context, cfg config.Config, errCh chan<- error) {
	path := filepath.Join(cfg.NodeHome, "data", "tcp_lz4_stats")
	if _, err := os.Stat(path); err != nil {
		logger.InfoComponent("tcp_lz4",
			"tcp_lz4_stats directory not present (%s); monitor idle", path)
		<-ctx.Done()
		return
	}

	logger.InfoComponent("tcp_lz4", "watching %s", path)

	prev := map[string]map[string]bool{
		"in":  {},
		"out": {},
	}
	var lastMtime time.Time

	ticker := time.NewTicker(tcpLz4PollInterval)
	defer ticker.Stop()

	tickLz4(path, prev, &lastMtime)
	metrics.MarkMonitorTick("tcp_lz4")

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			tickLz4(path, prev, &lastMtime)
			metrics.MarkMonitorTick("tcp_lz4")
		}
	}
}

func tickLz4(root string, prev map[string]map[string]bool, lastMtime *time.Time) {
	datePath, err := latestDateFile(root)
	if err != nil {
		return
	}
	info, err := os.Stat(datePath)
	if err != nil {
		return
	}
	if info.ModTime().Equal(*lastMtime) {
		return // file unchanged
	}
	data, err := os.ReadFile(datePath)
	if err != nil {
		return
	}

	// Walk lines from the bottom, classifying each as global or per-peer
	// until we've found one of each. The two records are written
	// microseconds apart on every sample, so finding both is the
	// expected case; we bound the search at maxLinesToScan as a sanity
	// guard against a pathologically large file from a long uptime.
	const maxLinesToScan = 64
	gotGlobal := false
	gotPeer := false
	end := len(data)
	scanned := 0
	for end > 0 && scanned < maxLinesToScan && (!gotGlobal || !gotPeer) {
		// Skip trailing newlines.
		for end > 0 && data[end-1] == '\n' {
			end--
		}
		if end == 0 {
			break
		}
		start := bytes.LastIndexByte(data[:end], '\n') + 1
		line := bytes.TrimSpace(data[start:end])
		end = start - 1
		scanned++
		if len(line) == 0 || line[0] != '[' {
			continue
		}
		if !gotGlobal {
			if b, p, r, ok := parseLz4GlobalLine(line); ok {
				metrics.HLP2PLz4GlobalBytes.Set(float64(b))
				metrics.HLP2PLz4GlobalPackets.Set(float64(p))
				metrics.HLP2PLz4GlobalRatio.Set(r)
				gotGlobal = true
				continue
			}
		}
		if !gotPeer {
			if peers, ok := parseLz4PeerLine(line); ok {
				publishLz4Peers(peers, prev)
				gotPeer = true
				continue
			}
		}
	}

	// On the rare edge where the tail contains only one record shape
	// (day-file rollover, or a torn last write), we leave the other
	// metric set at its previous values. That's strictly better than
	// zeroing — a stale-but-recent gauge gives an operator a
	// degraded-but-useful reading; a zeroed gauge looks like an
	// incident.

	*lastMtime = info.ModTime()
}

func publishLz4Peers(peers []lz4PeerSample, prev map[string]map[string]bool) {
	byDir := map[string][]lz4PeerSample{"in": {}, "out": {}}
	for _, p := range peers {
		byDir[p.direction] = append(byDir[p.direction], p)
	}

	for direction, list := range byDir {
		sort.Slice(list, func(a, b int) bool { return list[a].bytes > list[b].bytes })
		current := make(map[string]bool, tcpLz4TopN+1)
		var otherBytes, otherPackets int64
		var otherRatioWeight float64 // weighted by bytes
		var otherRatioBytesSum float64

		for i, p := range list {
			if i < tcpLz4TopN {
				metrics.HLP2PLz4BytesTotal.WithLabelValues(p.ip, direction).Set(float64(p.bytes))
				metrics.HLP2PLz4PacketsTotal.WithLabelValues(p.ip, direction).Set(float64(p.packets))
				metrics.HLP2PLz4CompressionRatio.WithLabelValues(p.ip, direction).Set(p.ratio)
				current[p.ip] = true
			} else {
				otherBytes += p.bytes
				otherPackets += p.packets
				otherRatioWeight += p.ratio * float64(p.bytes)
				otherRatioBytesSum += float64(p.bytes)
			}
		}
		if otherBytes > 0 {
			metrics.HLP2PLz4BytesTotal.WithLabelValues("other", direction).Set(float64(otherBytes))
			metrics.HLP2PLz4PacketsTotal.WithLabelValues("other", direction).Set(float64(otherPackets))
			if otherRatioBytesSum > 0 {
				metrics.HLP2PLz4CompressionRatio.WithLabelValues("other", direction).
					Set(otherRatioWeight / otherRatioBytesSum)
			}
			current["other"] = true
		}

		// Drop labels that fell out of the top-N.
		for ip := range prev[direction] {
			if !current[ip] {
				metrics.HLP2PLz4BytesTotal.DeleteLabelValues(ip, direction)
				metrics.HLP2PLz4PacketsTotal.DeleteLabelValues(ip, direction)
				metrics.HLP2PLz4CompressionRatio.DeleteLabelValues(ip, direction)
				delete(prev[direction], ip)
			}
		}
		for ip := range current {
			prev[direction][ip] = true
		}
	}
}

// parseLz4PeerLine returns the per-peer samples from a line of the form:
//
//	["<iso>", [ [["In"|"Out", "<ip>", <port>], <bytes>, <packets>, <ratio>], ... ]]
//
// Returns ok=false if the inner array entries don't all have the
// 4-element shape (this is what distinguishes a peer line from a global
// line, since the global line has a 3-element inner with no [dir,ip,port]
// key).
func parseLz4PeerLine(line []byte) ([]lz4PeerSample, bool) {
	var outer [2]json.RawMessage
	if err := json.Unmarshal(line, &outer); err != nil {
		return nil, false
	}
	var entries []json.RawMessage
	if err := json.Unmarshal(outer[1], &entries); err != nil || len(entries) == 0 {
		return nil, false
	}
	// Sniff the first entry — must be a 4-tuple starting with [dir, ip, port].
	var first []json.RawMessage
	if err := json.Unmarshal(entries[0], &first); err != nil || len(first) < 4 {
		return nil, false
	}
	var firstKey [3]json.RawMessage
	if err := json.Unmarshal(first[0], &firstKey); err != nil {
		return nil, false
	}

	out := make([]lz4PeerSample, 0, len(entries))
	for _, raw := range entries {
		var entry []json.RawMessage
		if err := json.Unmarshal(raw, &entry); err != nil || len(entry) < 4 {
			continue
		}
		var key [3]json.RawMessage
		if err := json.Unmarshal(entry[0], &key); err != nil {
			continue
		}
		var dir, ip string
		if err := json.Unmarshal(key[0], &dir); err != nil {
			continue
		}
		if err := json.Unmarshal(key[1], &ip); err != nil {
			continue
		}
		var bytesN, packetsN int64
		if err := json.Unmarshal(entry[1], &bytesN); err != nil {
			continue
		}
		if err := json.Unmarshal(entry[2], &packetsN); err != nil {
			continue
		}
		var ratio float64
		if err := json.Unmarshal(entry[3], &ratio); err != nil {
			continue
		}
		var direction string
		switch dir {
		case "In":
			direction = "in"
		case "Out":
			direction = "out"
		default:
			continue
		}
		out = append(out, lz4PeerSample{
			direction: direction,
			ip:        ip,
			bytes:     bytesN,
			packets:   packetsN,
			ratio:     ratio,
		})
	}
	if len(out) == 0 {
		return nil, false
	}
	return out, true
}

// parseLz4GlobalLine handles the aggregate row:
//
//	["<iso>", [<bytes>, <packets>, <ratio>]]
//
// Returns ok=false on any structural mismatch.
func parseLz4GlobalLine(line []byte) (int64, int64, float64, bool) {
	var outer [2]json.RawMessage
	if err := json.Unmarshal(line, &outer); err != nil {
		return 0, 0, 0, false
	}
	var inner []json.RawMessage
	if err := json.Unmarshal(outer[1], &inner); err != nil || len(inner) != 3 {
		return 0, 0, 0, false
	}
	// Must be 3 numbers; if the first element is itself an array, this is a
	// peer line, not a global aggregate.
	var b, p int64
	if err := json.Unmarshal(inner[0], &b); err != nil {
		return 0, 0, 0, false
	}
	if err := json.Unmarshal(inner[1], &p); err != nil {
		return 0, 0, 0, false
	}
	var r float64
	if err := json.Unmarshal(inner[2], &r); err != nil {
		return 0, 0, 0, false
	}
	return b, p, r, true
}
