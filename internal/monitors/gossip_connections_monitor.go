package monitors

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/validaoxyz/hyperliquid-exporter/internal/config"
	"github.com/validaoxyz/hyperliquid-exporter/internal/logger"
	"github.com/validaoxyz/hyperliquid-exporter/internal/metrics"
)

var gossipConnectionAllowlist = map[string]string{
	"verified gossip rpc":                                     "verified_gossip_rpc",
	"handle_stream_connection":                                "handle_stream_connection",
	"performing checks on stream":                             "performing_checks_on_stream",
	"got tcp greeting":                                        "got_tcp_greeting",
	"error checking connection":                               "error_checking_connection",
	"rejecting gossip stream because max peers reached":       "rejecting_gossip_stream_max_peers_reached",
	"closing gossip stream because no quorum yet":             "closing_gossip_stream_no_quorum_yet",
	"finished checks":                                         "finished_checks",
	"sending abci_state":                                      "sending_abci_state",
	"successfully sent abci_state":                            "successfully_sent_abci_state",
	"sending evm kvs":                                         "sending_evm_kvs",
	"dropping connection after sending abci state":            "dropping_connection_after_sending_abci_state",
	"marking node_ip as verified":                             "marking_node_ip_verified",
	"dropping connection":                                     "dropping_connection",
	"closing gossip stream because peer is already connected": "closing_gossip_stream_peer_already_connected",
}

const gossipConnectionsPollInterval = 10 * time.Second

type gossipConnectionParseResult struct {
	event      string
	known      bool
	reason     string
	sourceTime time.Time
}

func StartGossipConnectionsMonitor(ctx context.Context, cfg config.Config, errCh chan<- error) {
	root := filepath.Join(cfg.NodeHome, "data", "node_logs", "gossip_connections", "hourly")
	metrics.RegisterSource(metrics.SourceGossipConnections, true)
	logger.InfoComponent("gossip_connections", "watching %s (committed stream, no startup replay)", root)

	resolveOK := false
	resolve := func() (string, error) {
		path, err := latestHourlyFile(root)
		publishGossipConnectionResolution(&resolveOK, err)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				// A clean empty startup scan is semantically different from a
				// discovery failure: tailStream will read the first file later
				// created from byte zero instead of treating it as history.
				return "", nil
			}
			return "", err
		}
		return path, nil
	}

	tailStream(ctx, tailStreamOpts{
		component:   "gossip_connections",
		name:        "gossip connection events",
		resolve:     resolve,
		rescanEvery: gossipConnectionsPollInterval,
		eofSleep:    250 * time.Millisecond,
		bufSize:     1 << 20,
		onLine: func(line string) {
			result := parseGossipConnectionRecord([]byte(line))
			publishGossipConnectionLine(&resolveOK, result, time.Now())
		},
		onIdle: func() {
			publishGossipConnectionIdle(&resolveOK, time.Now())
		},
		onFailure: func(failure tailStreamFailure) {
			markGossipConnectionTailFailure(failure, &resolveOK)
		},
	})
}

func publishGossipConnectionResolution(resolveOK *bool, resolveErr error) {
	metrics.WithPrometheusSnapshotUpdate(func() {
		metrics.MarkMonitorAttempt("gossip_connections")
		metrics.MarkSourceAttempt(metrics.SourceGossipConnections)
		if resolveErr == nil {
			*resolveOK = true
			return
		}

		*resolveOK = false
		metrics.HLP2PGossipSourceUp.Set(0)
		if errors.Is(resolveErr, os.ErrNotExist) {
			metrics.MarkSourceAbsent(metrics.SourceGossipConnections)
			return
		}
		metrics.MarkSourceError(metrics.SourceGossipConnections, metrics.SourceFailureDiscovery)
		metrics.IncMonitorError("gossip_connections")
	})
}

func publishGossipConnectionLine(resolveOK *bool, result gossipConnectionParseResult, now time.Time) {
	metrics.WithPrometheusSnapshotUpdate(func() {
		// Reaching onLine proves that the selected file was opened and read,
		// even when the complete record fails its bounded schema.
		*resolveOK = true
		metrics.HLP2PGossipSourceUp.Set(1)
		metrics.HLP2PGossipLastReadSuccessTimestampSeconds.Set(float64(now.Unix()))
		if result.reason != "" {
			metrics.HLP2PGossipParseErrorsTotal.WithLabelValues(result.reason).Inc()
			metrics.MarkSourceReadOutcome(metrics.SourceGossipConnections, true)
			metrics.MarkSourceError(metrics.SourceGossipConnections, metrics.SourceFailureSchema)
			metrics.IncMonitorError("gossip_connections")
			return
		}

		metrics.HLP2PGossipEventsTotal.WithLabelValues(result.event).Inc()
		if !result.known {
			metrics.HLP2PGossipUnknownEventsTotal.Inc()
		}
		metrics.MarkSourceValidObservation(metrics.SourceGossipConnections, result.sourceTime)
		metrics.MarkSourcePublication(metrics.SourceGossipConnections)
		metrics.MarkMonitorValidObservation("gossip_connections")
		metrics.MarkMonitorPublication("gossip_connections")
	})
}

func publishGossipConnectionIdle(resolveOK *bool, now time.Time) {
	metrics.WithPrometheusSnapshotUpdate(func() {
		if !*resolveOK {
			return
		}
		metrics.HLP2PGossipSourceUp.Set(1)
		metrics.HLP2PGossipLastReadSuccessTimestampSeconds.Set(float64(now.Unix()))
		// A quiet successful poll proves presence/readability, not that a
		// previously rejected record suddenly became schema-valid.
		metrics.MarkSourceReadOutcome(metrics.SourceGossipConnections, true)
	})
}

func markGossipConnectionTailFailure(failure tailStreamFailure, resolveOK *bool) {
	metrics.WithPrometheusSnapshotUpdate(func() {
		if failure == tailStreamFailureRecord {
			// The file was opened and read successfully; only record framing
			// failed. Keep transport health up and invalidate schema separately.
			*resolveOK = true
			metrics.HLP2PGossipSourceUp.Set(1)
			metrics.HLP2PGossipLastReadSuccessTimestampSeconds.Set(float64(time.Now().Unix()))
			metrics.HLP2PGossipParseErrorsTotal.WithLabelValues("shape").Inc()
			metrics.MarkSourceReadOutcome(metrics.SourceGossipConnections, true)
			metrics.MarkSourceError(metrics.SourceGossipConnections, metrics.SourceFailureSchema)
		} else {
			*resolveOK = false
			metrics.HLP2PGossipSourceUp.Set(0)
			metrics.MarkSourceError(metrics.SourceGossipConnections, metrics.SourceFailureRead)
		}
		metrics.IncMonitorError("gossip_connections")
	})
}

func parseGossipConnectionRecord(line []byte) gossipConnectionParseResult {
	var outer []json.RawMessage
	if err := json.Unmarshal(line, &outer); err != nil {
		return gossipConnectionParseResult{reason: "json"}
	}
	if len(outer) != 2 {
		return gossipConnectionParseResult{reason: "shape"}
	}
	var timestampString string
	if err := unmarshalRequiredJSON(outer[0], &timestampString); err != nil {
		return gossipConnectionParseResult{reason: "timestamp"}
	}
	timestamp, ok := parseVisorTime(timestampString)
	if !ok {
		return gossipConnectionParseResult{reason: "timestamp"}
	}
	var inner []json.RawMessage
	if err := json.Unmarshal(outer[1], &inner); err != nil || len(inner) == 0 {
		return gossipConnectionParseResult{reason: "shape"}
	}
	var tag string
	if err := unmarshalRequiredJSON(inner[0], &tag); err != nil {
		return gossipConnectionParseResult{reason: "shape"}
	}
	event, known := gossipConnectionAllowlist[tag]
	if !known {
		return gossipConnectionParseResult{event: "other", known: false, sourceTime: timestamp}
	}
	if !validKnownGossipPayload(tag, inner) {
		return gossipConnectionParseResult{reason: "payload"}
	}
	return gossipConnectionParseResult{event: event, known: true, sourceTime: timestamp}
}

func validKnownGossipPayload(tag string, inner []json.RawMessage) bool {
	switch tag {
	case "handle_stream_connection", "performing checks on stream":
		if len(inner) != 3 {
			return false
		}
		var endpoint, stream string
		return unmarshalRequiredJSON(inner[1], &endpoint) == nil && unmarshalRequiredJSON(inner[2], &stream) == nil
	case "closing gossip stream because no quorum yet":
		// The post-2026-08-09 testnet build emits a nonempty string plus an
		// array, while retained generations use the IP-object/bool form. The
		// payload is validation-only and never becomes metric identity.
		return validGossipIPFlagPayload(inner, false) || validGossipStringArrayPayload(inner)
	case "finished checks", "sending abci_state", "successfully sent abci_state":
		return validGossipIPFlagPayload(inner, false)
	case "dropping connection after sending abci state":
		// Current mainnet can use a short string in the third field while
		// retained generations use a bool. The payload is validation-only;
		// neither value becomes a metric label.
		return validGossipIPBoolOrStringPayload(inner)
	case "sending evm kvs", "marking node_ip as verified",
		"closing gossip stream because peer is already connected":
		// Current mainnet emits these as object-only events while retained
		// generations also include a trailing bool. Both exact build-scoped
		// projections carry the same bounded event identity.
		return validGossipIPFlagPayload(inner, true)
	case "dropping connection":
		return len(inner) == 5
	case "got tcp greeting":
		if len(inner) == 2 {
			return rawExactIPObject(inner[1])
		}
		if len(inner) != 3 || !rawExactIPObject(inner[1]) {
			return false
		}
		var flag bool
		return unmarshalRequiredJSON(inner[2], &flag) == nil
	case "rejecting gossip stream because max peers reached":
		if len(inner) == 2 {
			return rawJSONObject(inner[1])
		}
		if len(inner) != 3 || !rawJSONArray(inner[2]) {
			return false
		}
		var message string
		return unmarshalRequiredJSON(inner[1], &message) == nil
	case "error checking connection":
		if len(inner) == 2 {
			return rawJSONObject(inner[1])
		}
		if len(inner) != 3 {
			return false
		}
		var endpoint, detail string
		return unmarshalRequiredJSON(inner[1], &endpoint) == nil && unmarshalRequiredJSON(inner[2], &detail) == nil
	default:
		return len(inner) == 2 && rawJSONObject(inner[1])
	}
}

func validGossipIPFlagPayload(inner []json.RawMessage, allowObjectOnly bool) bool {
	if len(inner) == 2 {
		return allowObjectOnly && rawExactIPObject(inner[1])
	}
	if len(inner) != 3 || !rawExactIPObject(inner[1]) {
		return false
	}
	var flag bool
	return unmarshalRequiredJSON(inner[2], &flag) == nil
}

func validGossipIPBoolOrStringPayload(inner []json.RawMessage) bool {
	if len(inner) != 3 || !rawExactIPObject(inner[1]) {
		return false
	}
	var flag bool
	if unmarshalRequiredJSON(inner[2], &flag) == nil {
		return true
	}
	var detail string
	return unmarshalRequiredJSON(inner[2], &detail) == nil && strings.TrimSpace(detail) != ""
}

func validGossipStringArrayPayload(inner []json.RawMessage) bool {
	if len(inner) != 3 || !rawJSONArray(inner[2]) {
		return false
	}
	var detail string
	return unmarshalRequiredJSON(inner[1], &detail) == nil && strings.TrimSpace(detail) != ""
}

func rawExactIPObject(raw json.RawMessage) bool {
	var object map[string]json.RawMessage
	if json.Unmarshal(raw, &object) != nil || len(object) != 1 {
		return false
	}
	var ip string
	return unmarshalRequiredJSON(object["Ip"], &ip) == nil && strings.TrimSpace(ip) != ""
}

func rawJSONObject(raw json.RawMessage) bool {
	var object map[string]json.RawMessage
	return json.Unmarshal(raw, &object) == nil && object != nil
}

func parseGossipConnectionLine(line []byte) (string, bool) {
	result := parseGossipConnectionRecord(line)
	return result.event, result.reason == ""
}
