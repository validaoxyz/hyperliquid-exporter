package monitors

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
	gossipPollInterval  = 30 * time.Second
	gossipTailBytes     = 512 * 1024
	childSnapshotMaxAge = 90 * time.Second
	childIdentityCap    = 16
)

type childPeer struct {
	ip          string
	verified    bool
	connections int64
}

type childSnapshot struct {
	sourceTime time.Time
	peers      []childPeer
}

type childParseError struct {
	reason string
	err    error
}

func (e *childParseError) Error() string { return e.reason + ": " + e.err.Error() }

type GossipMonitor struct {
	config        *config.Config
	gossipDir     string
	lastRecordKey string
	receivedAt    time.Time
	sourceTime    time.Time
	current       map[string]childPeer
	tenureStart   map[string]time.Time
	published     map[string]bool
	clearedStale  bool
}

// PeerStatus remains exported for compatibility with existing internal users.
type PeerStatus struct {
	Verified        bool  `json:"verified"`
	ConnectionCount int64 `json:"connection_count"`
}

func NewGossipMonitor(cfg *config.Config) *GossipMonitor {
	return &GossipMonitor{
		config:      cfg,
		gossipDir:   filepath.Join(cfg.NodeHome, "data", "node_logs", "gossip_rpc", "hourly"),
		current:     make(map[string]childPeer),
		tenureStart: make(map[string]time.Time),
		published:   make(map[string]bool),
	}
}

func StartGossipMonitor(ctx context.Context, cfg *config.Config, errCh chan<- error) {
	metrics.RegisterSource(metrics.SourceGossipChildren, true)
	m := NewGossipMonitor(cfg)
	metrics.HLP2PChildSnapshotTimestampSeconds.Set(0)
	metrics.HLP2PChildSnapshotAgeSeconds.Set(0)
	metrics.HLP2PChildSnapshotFresh.Set(0)
	logger.InfoComponent("gossip", "watching explicit child snapshots in %s (late discovery enabled)", m.gossipDir)
	m.monitorGossipLogs(ctx, errCh)
}

func (m *GossipMonitor) monitorGossipLogs(ctx context.Context, errCh chan<- error) {
	ticker := time.NewTicker(gossipPollInterval)
	defer ticker.Stop()
	m.tickWithClock(time.Now)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.tickWithClock(time.Now)
		}
	}
}

func (m *GossipMonitor) tick(now time.Time) {
	m.tickWithClock(func() time.Time { return now })
}

func (m *GossipMonitor) tickWithClock(now func() time.Time) {
	metrics.MarkMonitorAttempt("gossip")
	metrics.MarkSourceAttempt(metrics.SourceGossipChildren)
	m.publishFreshness(now())

	path, err := m.getLatestGossipLogFile()
	if err != nil {
		m.childFailure("read", metrics.SourceFailureDiscovery, err)
		return
	}
	snapshot, found, err := readLatestChildSnapshot(path)
	if err != nil {
		reason := "scan"
		var parseErr *childParseError
		if errors.As(err, &parseErr) {
			reason = parseErr.reason
		}
		stage := metrics.SourceFailureSchema
		if reason == "read" || reason == "scan" {
			stage = metrics.SourceFailureRead
		}
		m.childFailure(reason, stage, err)
		return
	}
	if !found {
		m.markChildSourceSuccess()
		return
	}
	key := childSnapshotKey(snapshot)
	if key == m.lastRecordKey {
		m.markChildSourceSuccess()
		return
	}
	m.lastRecordKey = key
	m.commitSnapshot(snapshot, now())
}

func (m *GossipMonitor) childFailure(reason string, stage metrics.SourceFailureStage, err error) {
	metrics.WithPrometheusSnapshotUpdate(func() {
		if stage == metrics.SourceFailureSchema {
			// The file source itself was readable/scannable; schema validity is
			// represented separately and must not impersonate an I/O outage.
			metrics.HLP2PChildSourceUp.Set(1)
		} else {
			metrics.HLP2PChildSourceUp.Set(0)
		}
		metrics.HLP2PChildSnapshotErrorsTotal.WithLabelValues(reason).Inc()
		metrics.MarkSourceError(metrics.SourceGossipChildren, stage)
		if stage == metrics.SourceFailureDiscovery && errors.Is(err, os.ErrNotExist) {
			metrics.MarkSourceAbsent(metrics.SourceGossipChildren)
		}
		metrics.IncMonitorError("gossip")
	})
}

func (m *GossipMonitor) commitSnapshot(snapshot childSnapshot, now time.Time) {
	metrics.WithPrometheusSnapshotUpdate(func() {
		metrics.HLP2PChildSourceUp.Set(1)
		metrics.MarkSourceReadOutcome(metrics.SourceGossipChildren, true)
		wasFresh := !m.receivedAt.IsZero() && now.Sub(m.receivedAt) <= childSnapshotMaxAge && !m.clearedStale
		previous := m.current
		next := make(map[string]childPeer, len(snapshot.peers))
		for _, peer := range snapshot.peers {
			next[peer.ip] = peer
			if !wasFresh {
				m.tenureStart[peer.ip] = now
			} else if _, existed := previous[peer.ip]; !existed {
				m.tenureStart[peer.ip] = now
			}
		}
		for ip := range m.tenureStart {
			if _, exists := next[ip]; !exists {
				delete(m.tenureStart, ip)
			}
		}
		m.current = next
		m.receivedAt = now
		m.sourceTime = snapshot.sourceTime
		m.clearedStale = false
		m.publishCurrent(now, m.config.EnablePerPeerMetrics)
		metrics.HLP2PChildSnapshotTimestampSeconds.Set(unixTimestampSeconds(snapshot.sourceTime))
		metrics.HLP2PChildSnapshotAgeSeconds.Set(0)
		metrics.HLP2PChildSnapshotFresh.Set(1)
		metrics.MarkSourceValidObservation(metrics.SourceGossipChildren, snapshot.sourceTime)
		metrics.MarkSourcePublication(metrics.SourceGossipChildren)
		metrics.MarkMonitorValidObservation("gossip")
		metrics.MarkMonitorPublication("gossip")
	})
}

func (m *GossipMonitor) markChildSourceSuccess() {
	metrics.WithPrometheusSnapshotUpdate(func() {
		metrics.HLP2PChildSourceUp.Set(1)
		metrics.MarkSourceReadOutcome(metrics.SourceGossipChildren, true)
	})
}

func (m *GossipMonitor) publishFreshness(now time.Time) {
	metrics.WithPrometheusSnapshotUpdate(func() {
		if m.receivedAt.IsZero() {
			metrics.HLP2PChildSnapshotAgeSeconds.Set(0)
			metrics.HLP2PChildSnapshotFresh.Set(0)
			return
		}
		age := nonnegativeDuration(now.Sub(m.receivedAt))
		metrics.HLP2PChildSnapshotAgeSeconds.Set(age.Seconds())
		if age <= childSnapshotMaxAge {
			metrics.HLP2PChildSnapshotFresh.Set(1)
			if !m.clearedStale {
				m.publishCurrent(now, m.config.EnablePerPeerMetrics)
			}
			return
		}
		metrics.HLP2PChildSnapshotFresh.Set(0)
		if !m.clearedStale {
			m.clearCurrentMetrics()
			m.current = make(map[string]childPeer)
			clear(m.tenureStart)
			m.clearedStale = true
		}
	})
}

func (m *GossipMonitor) publishCurrent(now time.Time, emitIdentity bool) {
	var verified, unverified, connections int64
	peers := make([]childPeer, 0, len(m.current))
	for _, peer := range m.current {
		peers = append(peers, peer)
		if peer.verified {
			verified++
		} else {
			unverified++
		}
		connections += int64(peer.connections)
	}
	metrics.HLP2PChildPeers.WithLabelValues("true").Set(float64(verified))
	metrics.HLP2PChildPeers.WithLabelValues("false").Set(float64(unverified))
	metrics.HLP2PChildConnections.Set(float64(connections))
	metrics.ReplaceP2PChildSnapshot(verified, unverified)
	metrics.HLP2PNonValConnections.Set(float64(connections))
	overflow := len(peers) - childIdentityCap
	if overflow < 0 {
		overflow = 0
	}
	metrics.HLP2PChildIdentityOverflow.Set(float64(overflow))

	if !emitIdentity {
		m.clearIdentityMetrics()
		return
	}
	sort.Slice(peers, func(i, j int) bool { return peers[i].ip < peers[j].ip })
	limit := len(peers)
	if limit > childIdentityCap {
		limit = childIdentityCap
	}
	currentLabels := make(map[string]bool, limit)
	for _, peer := range peers[:limit] {
		verifiedLabel := strconv.FormatBool(peer.verified)
		metrics.HLP2PChildPeerInfo.WithLabelValues(peer.ip, verifiedLabel).Set(1)
		metrics.HLP2PChildPeerConnections.WithLabelValues(peer.ip).Set(float64(peer.connections))
		metrics.HLP2PChildPeerTenureSeconds.WithLabelValues(peer.ip).Set(nonnegativeDuration(now.Sub(m.tenureStart[peer.ip])).Seconds())
		currentLabels[peer.ip] = peer.verified
	}
	for ip, oldVerified := range m.published {
		newVerified, exists := currentLabels[ip]
		if !exists || newVerified != oldVerified {
			metrics.HLP2PChildPeerInfo.DeleteLabelValues(ip, strconv.FormatBool(oldVerified))
		}
		if !exists {
			metrics.HLP2PChildPeerConnections.DeleteLabelValues(ip)
			metrics.HLP2PChildPeerTenureSeconds.DeleteLabelValues(ip)
		}
	}
	m.published = currentLabels
}

func (m *GossipMonitor) clearCurrentMetrics() {
	metrics.HLP2PChildPeers.WithLabelValues("true").Set(0)
	metrics.HLP2PChildPeers.WithLabelValues("false").Set(0)
	metrics.HLP2PChildConnections.Set(0)
	metrics.ReplaceP2PChildSnapshot(0, 0)
	metrics.HLP2PNonValConnections.Set(0)
	metrics.HLP2PChildIdentityOverflow.Set(0)
	m.clearIdentityMetrics()
}

func (m *GossipMonitor) clearIdentityMetrics() {
	for ip, verified := range m.published {
		metrics.HLP2PChildPeerInfo.DeleteLabelValues(ip, strconv.FormatBool(verified))
		metrics.HLP2PChildPeerConnections.DeleteLabelValues(ip)
		metrics.HLP2PChildPeerTenureSeconds.DeleteLabelValues(ip)
	}
	clear(m.published)
}

func readLatestChildSnapshot(path string) (childSnapshot, bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return childSnapshot{}, false, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return childSnapshot{}, false, err
	}
	start := info.Size() - gossipTailBytes
	if start < 0 {
		start = 0
	}
	if _, err := f.Seek(start, io.SeekStart); err != nil {
		return childSnapshot{}, false, err
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return childSnapshot{}, false, err
	}
	if start > 0 {
		if delimiter := bytes.IndexByte(data, '\n'); delimiter >= 0 {
			data = data[delimiter+1:]
		} else {
			return childSnapshot{}, false, nil
		}
	}
	lines := committedLinesReverse(data, 100000)
	for _, line := range lines {
		claimsChild := structurallyClaimsChildSnapshot(line)
		snapshot, isChild, err := parseChildSnapshotLine(line)
		if err != nil {
			if claimsChild {
				return childSnapshot{}, false, err
			}
			continue
		}
		if !isChild && claimsChild {
			return childSnapshot{}, false, &childParseError{reason: "shape", err: errors.New("malformed child snapshot envelope")}
		}
		if isChild {
			return snapshot, true, nil
		}
	}
	return childSnapshot{}, false, nil
}

// structurallyClaimsChildSnapshot recognizes only the event tag position. It
// deliberately ignores the same string in unrelated payloads so arbitrary log
// text cannot turn a non-child record into an authoritative schema failure.
func structurallyClaimsChildSnapshot(line []byte) bool {
	decoder := json.NewDecoder(bytes.NewReader(line))
	start, err := decoder.Token()
	if err != nil || start != json.Delim('[') || !decoder.More() {
		return false
	}
	var timestamp json.RawMessage
	if decoder.Decode(&timestamp) != nil || !decoder.More() {
		return false
	}
	second, err := decoder.Token()
	if err != nil {
		return false
	}
	if direct, ok := second.(string); ok {
		return direct == "child_peers status"
	}
	if second != json.Delim('[') || !decoder.More() {
		return false
	}
	tag, err := decoder.Token()
	return err == nil && tag == "child_peers status"
}

func parseChildSnapshotLine(line []byte) (childSnapshot, bool, error) {
	var outer []json.RawMessage
	if err := json.Unmarshal(line, &outer); err != nil {
		return childSnapshot{}, false, &childParseError{reason: "shape", err: err}
	}
	if len(outer) != 2 {
		return childSnapshot{}, false, nil
	}
	var inner []json.RawMessage
	if err := json.Unmarshal(outer[1], &inner); err != nil || len(inner) == 0 {
		return childSnapshot{}, false, nil
	}
	var event string
	if err := unmarshalRequiredJSON(inner[0], &event); err != nil || event != "child_peers status" {
		return childSnapshot{}, false, nil
	}
	if len(inner) != 2 {
		return childSnapshot{}, true, &childParseError{reason: "shape", err: errors.New("child event arity")}
	}
	var timestampString string
	if err := unmarshalRequiredJSON(outer[0], &timestampString); err != nil {
		return childSnapshot{}, true, &childParseError{reason: "timestamp", err: err}
	}
	timestamp, ok := parseVisorTime(timestampString)
	if !ok {
		return childSnapshot{}, true, &childParseError{reason: "timestamp", err: errors.New("invalid child timestamp")}
	}
	var rows []json.RawMessage
	if !rawJSONArray(inner[1]) {
		return childSnapshot{}, true, &childParseError{reason: "shape", err: errors.New("child peer list must be an array")}
	}
	if err := json.Unmarshal(inner[1], &rows); err != nil {
		return childSnapshot{}, true, &childParseError{reason: "shape", err: err}
	}
	peers := make([]childPeer, 0, len(rows))
	seen := make(map[string]struct{}, len(rows))
	var totalConnections int64
	for _, raw := range rows {
		peer, err := parseChildPeer(raw)
		if err != nil {
			return childSnapshot{}, true, err
		}
		if _, duplicate := seen[peer.ip]; duplicate {
			return childSnapshot{}, true, &childParseError{reason: "identity", err: errors.New("duplicate child IP")}
		}
		if peer.connections > math.MaxInt64-totalConnections {
			return childSnapshot{}, true, &childParseError{reason: "shape", err: errors.New("child connection_count aggregate overflow")}
		}
		totalConnections += peer.connections
		seen[peer.ip] = struct{}{}
		peers = append(peers, peer)
	}
	sort.Slice(peers, func(i, j int) bool { return peers[i].ip < peers[j].ip })
	return childSnapshot{sourceTime: timestamp, peers: peers}, true, nil
}

func parseChildPeer(raw json.RawMessage) (childPeer, error) {
	var pair []json.RawMessage
	if err := json.Unmarshal(raw, &pair); err != nil || len(pair) != 2 {
		return childPeer{}, &childParseError{reason: "shape", err: errors.New("child row arity")}
	}
	identity, err := parseChildIdentity(pair[0])
	if err != nil {
		return childPeer{}, &childParseError{reason: "identity", err: err}
	}
	ip, err := parseCanonicalChildIP(identity)
	if err != nil {
		return childPeer{}, &childParseError{reason: "identity", err: err}
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(pair[1], &fields); err != nil || fields == nil {
		return childPeer{}, &childParseError{reason: "shape", err: errors.New("child status object")}
	}
	verifiedRaw, okVerified := fields["verified"]
	connectionsRaw, okConnections := fields["connection_count"]
	if !okVerified || !okConnections {
		return childPeer{}, &childParseError{reason: "shape", err: errors.New("missing child status field")}
	}
	var verified bool
	var connections int64
	if unmarshalRequiredJSON(verifiedRaw, &verified) != nil || unmarshalRequiredJSON(connectionsRaw, &connections) != nil || connections < 0 {
		return childPeer{}, &childParseError{reason: "shape", err: errors.New("invalid child status field")}
	}
	return childPeer{ip: ip, verified: verified, connections: connections}, nil
}

func parseChildIdentity(raw json.RawMessage) (string, error) {
	var identity string
	if err := unmarshalRequiredJSON(raw, &identity); err == nil {
		return identity, nil
	}

	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil || len(object) != 1 {
		return "", errors.New("child identity must be a string or exact Ip object")
	}
	ip, ok := object["Ip"]
	if !ok || unmarshalRequiredJSON(ip, &identity) != nil || strings.TrimSpace(identity) == "" {
		return "", errors.New("invalid child Ip identity")
	}
	return identity, nil
}

func parseCanonicalChildIP(identity string) (string, error) {
	if ip, err := netip.ParseAddr(identity); err == nil {
		if ip.Zone() != "" {
			return "", fmt.Errorf("scoped child IP is not canonical")
		}
		return ip.Unmap().String(), nil
	}
	if endpoint, err := netip.ParseAddrPort(identity); err == nil {
		if endpoint.Addr().Zone() != "" {
			return "", fmt.Errorf("scoped child IP is not canonical")
		}
		return endpoint.Addr().Unmap().String(), nil
	}
	return "", fmt.Errorf("invalid child IP")
}

func childSnapshotKey(snapshot childSnapshot) string {
	buf := bytes.NewBufferString(snapshot.sourceTime.Format(time.RFC3339Nano))
	for _, peer := range snapshot.peers {
		fmt.Fprintf(buf, "|%s:%t:%d", peer.ip, peer.verified, peer.connections)
	}
	return buf.String()
}

func (m *GossipMonitor) processGossipFile(path string) error {
	snapshot, found, err := readLatestChildSnapshot(path)
	if err != nil || !found {
		return err
	}
	m.commitSnapshot(snapshot, time.Now())
	return nil
}

func (m *GossipMonitor) getLatestGossipLogFile() (string, error) {
	return latestHourlyFile(m.gossipDir)
}
