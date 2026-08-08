package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// Network metrics added by the 2026-08 network semantics audit.  Legacy
// families live in prometheus_instruments.go and are mirrored by the owning
// monitor for one release where the migration contract requires it.
var (
	HLP2PTCPSocketConnections = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hl_p2p_tcp_socket_connections",
		Help: "Kernel socket rows in the last fully successful combined TCP4/TCP6 snapshot, associated once with a tracked service port; service_side is the literal socket side carrying that port.",
	}, []string{"service_port", "service_side", "state"})
	HLP2PTCPSocketSourceUp = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hl_p2p_tcp_socket_source_up",
		Help: "Whether the latest attempt completely opened, scanned, and parsed the fixed kernel TCP source (1=yes, 0=no).",
	}, []string{"source"})
	HLP2PTCPSocketLastSuccessTimestampSeconds = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "hl_p2p_tcp_socket_last_success_timestamp_seconds",
		Help: "Exporter wall-clock Unix timestamp of the last fully successful combined TCP4/TCP6 snapshot commit.",
	})
	HLP2PTCPSocketErrorsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "hl_p2p_tcp_socket_errors_total",
		Help: "Kernel TCP source failures since exporter start, partitioned by fixed source and stage.",
	}, []string{"source", "stage"})

	HLP2PTCPTrafficByServicePort = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hl_p2p_tcp_traffic_by_service_port",
		Help: "Latest upstream point-rate field summed by bounded source key port and direction; the formal unit and socket-side ownership are unresolved.",
	}, []string{"service_port", "direction"})
	HLP2PTCPTrafficSourceUp = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "hl_p2p_tcp_traffic_source_up",
		Help: "Whether latest-file discovery/read and one complete newline-committed all-rows-valid traffic snapshot succeeded (1=yes, 0=no).",
	})
	HLP2PTCPTrafficSampleTimestampSeconds = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "hl_p2p_tcp_traffic_sample_timestamp_seconds",
		Help: "Timestamp carried by the most recently committed TCP traffic snapshot.",
	})
	HLP2PTCPTrafficErrorsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "hl_p2p_tcp_traffic_errors_total",
		Help: "TCP traffic snapshot failures since exporter start, partitioned by a fixed stage.",
	}, []string{"stage"})

	HLP2PLZ4WindowBytes = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hl_p2p_lz4_window_bytes",
		Help: "Latest paired-window upstream byte field summed across ports per endpoint; whether the field is compressed or uncompressed bytes is unresolved.",
	}, []string{"ip", "direction"})
	HLP2PLZ4WindowPackets = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hl_p2p_lz4_window_packets",
		Help: "Latest paired-window upstream packet field summed across ports per endpoint.",
	}, []string{"ip", "direction"})
	HLP2PLZ4WindowWeightedRatio = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hl_p2p_lz4_window_weighted_ratio",
		Help: "Byte-field-weighted mean of upstream-reported per-port ratios in the latest paired window; not a derived aggregate compression ratio.",
	}, []string{"ip", "direction"})
	HLP2PLZ4WindowBytesByServicePort = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hl_p2p_lz4_window_bytes_by_service_port",
		Help: "Latest paired-window upstream byte field summed by bounded source key port and direction; compression-layer semantics are unresolved.",
	}, []string{"service_port", "direction"})
	HLP2PLZ4WindowPacketsByServicePort = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hl_p2p_lz4_window_packets_by_service_port",
		Help: "Latest paired-window upstream packet field summed by bounded source key port and direction.",
	}, []string{"service_port", "direction"})
	HLP2PLZ4GlobalWindowBytes = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "hl_p2p_lz4_global_window_bytes",
		Help: "Source-provided global byte field from the latest complete paired LZ4 window; compression-layer semantics are unresolved.",
	})
	HLP2PLZ4GlobalWindowPackets = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "hl_p2p_lz4_global_window_packets",
		Help: "Source-provided global packet field from the latest complete paired LZ4 window.",
	})
	HLP2PLZ4GlobalWindowRatio = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "hl_p2p_lz4_global_window_ratio",
		Help: "Source-provided global ratio from the latest complete paired LZ4 window.",
	})
	HLP2PLZ4WindowDurationSeconds = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "hl_p2p_lz4_window_duration_seconds",
		Help: "Difference in seconds between consecutive committed paired-window source timestamps; zero before a prior window exists.",
	})
	HLP2PLZ4SampleTimestampSeconds = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "hl_p2p_lz4_sample_timestamp_seconds",
		Help: "Source timestamp of the latest complete paired LZ4 window.",
	})
	HLP2PLZ4SampleAgeSeconds = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "hl_p2p_lz4_sample_age_seconds",
		Help: "Seconds since exporter receipt of the latest complete paired LZ4 window; future source timestamps cannot make this negative.",
	})
	HLP2PLZ4SourceUp = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "hl_p2p_lz4_source_up",
		Help: "Whether the latest LZ4 source discovery/read and complete pair selection succeeded (1=yes, 0=no).",
	})

	HLP2PGossipUnknownEventsTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "hl_p2p_gossip_unknown_events_total",
		Help: "Structurally valid gossip-connection events whose exact tag is outside the fixed vocabulary since exporter start.",
	})
	HLP2PGossipParseErrorsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "hl_p2p_gossip_parse_errors_total",
		Help: "Rejected complete gossip-connection records since exporter start, partitioned by a fixed reason.",
	}, []string{"reason"})
	HLP2PGossipSourceUp = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "hl_p2p_gossip_source_up",
		Help: "Whether the latest gossip-connection discovery/read/scan attempt succeeded; quiet input remains up.",
	})
	HLP2PGossipLastReadSuccessTimestampSeconds = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "hl_p2p_gossip_last_read_success_timestamp_seconds",
		Help: "Exporter wall-clock Unix timestamp of the latest successful gossip-connection poll, independent of event arrival.",
	})

	HLP2PChildPeers = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hl_p2p_child_peers",
		Help: "Explicit children in the newest complete fresh child_peers status snapshot, partitioned by verification status.",
	}, []string{"verified"})
	HLP2PChildConnections = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "hl_p2p_child_connections",
		Help: "Sum of connection_count across explicit children in the newest complete fresh snapshot; this is not a peer count.",
	})
	HLP2PChildSnapshotTimestampSeconds = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "hl_p2p_child_snapshot_timestamp_seconds",
		Help: "Source timestamp of the latest complete accepted child snapshot; zero before the first accepted snapshot.",
	})
	HLP2PChildSnapshotAgeSeconds = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "hl_p2p_child_snapshot_age_seconds",
		Help: "Seconds since exporter receipt of the latest complete accepted child snapshot; use snapshot_fresh to distinguish never seen.",
	})
	HLP2PChildSnapshotFresh = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "hl_p2p_child_snapshot_fresh",
		Help: "Whether the latest complete child snapshot was received no more than 90 seconds ago (1=yes, 0=no).",
	})
	HLP2PChildSourceUp = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "hl_p2p_child_source_up",
		Help: "Whether latest child-source discovery/read/scan succeeded, independent of snapshot freshness.",
	})
	HLP2PChildSnapshotErrorsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "hl_p2p_child_snapshot_errors_total",
		Help: "Rejected child-source observations since exporter start, partitioned by a fixed reason.",
	}, []string{"reason"})
	HLP2PChildIdentityOverflow = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "hl_p2p_child_identity_overflow",
		Help: "Current explicit children beyond the optional 16-identity publication cap.",
	})
	HLP2PChildPeerInfo = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hl_p2p_child_peer_info",
		Help: "Optional bounded identity for a current explicit child from the latest fresh snapshot.",
	}, []string{"ip", "verified"})
	HLP2PChildPeerConnections = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hl_p2p_child_peer_connections",
		Help: "Source connection_count for an optionally published current explicit child.",
	}, []string{"ip"})
	HLP2PChildPeerTenureSeconds = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hl_p2p_child_peer_tenure_seconds",
		Help: "Exporter-process-observed seconds of uninterrupted membership for an optionally published current explicit child.",
	}, []string{"ip"})

	HLP2PDominantInboundEndpointInfo = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hl_p2p_dominant_inbound_endpoint_info",
		Help: "Identity of the single selected dominant inbound traffic endpoint candidate; this is a traffic heuristic, not parentage, reachability, quality, or causality.",
	}, []string{"ip"})
	HLP2PDominantInboundLatestValue = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "hl_p2p_dominant_inbound_latest_value",
		Help: "Raw unresolved-unit inbound traffic value for the selected candidate in the newest complete snapshot; zero when absent.",
	})
	HLP2PDominantInboundEWMAValue = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "hl_p2p_dominant_inbound_ewma_value",
		Help: "EWMA of the unresolved-unit inbound traffic field for the selected dominant candidate.",
	})
	HLP2PDominantInboundTenureSeconds = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "hl_p2p_dominant_inbound_tenure_seconds",
		Help: "Wall-clock seconds since the candidate was selected in the current uninterrupted fresh epoch.",
	})
	HLP2PDominantInboundSwitchesTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "hl_p2p_dominant_inbound_switches_total",
		Help: "Candidate A-to-B changes during continuously fresh epochs since exporter start; clear and recovery are not switches.",
	})
	HLP2PDominantInboundShareRatio = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "hl_p2p_dominant_inbound_share_ratio",
		Help: "Selected candidate EWMA divided by the sum of positive inbound EWMAs; zero when undefined.",
	})
	HLP2PDominantInboundChallengerRatio = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "hl_p2p_dominant_inbound_challenger_ratio",
		Help: "Largest other EWMA divided by the selected candidate EWMA; zero when no challenger.",
	})
	HLP2PDominantInboundTieCount = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "hl_p2p_dominant_inbound_tie_count",
		Help: "Endpoints exactly tied for maximum inbound EWMA before lexicographic tie-breaking; zero without a candidate.",
	})
	HLP2PDominantInboundFresh = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "hl_p2p_dominant_inbound_fresh",
		Help: "Whether the dominant-candidate state derives from a complete traffic snapshot received no more than 90 seconds ago.",
	})

	HLP2PTrafficEndpointsCurrent = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "hl_p2p_traffic_endpoints_current",
		Help: "Unique canonical IPs with any positive value in the latest complete traffic snapshot; this is observation, not connectivity.",
	})
	HLP2PTrafficEndpointsSeen = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hl_p2p_traffic_endpoints_seen",
		Help: "Qualified process-local traffic endpoints refreshed within the fixed observation window.",
	}, []string{"window"})
	HLP2PTrafficEndpointsAddedTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "hl_p2p_traffic_endpoints_added_total",
		Help: "Qualified traffic-endpoint additions since exporter start.",
	})
	HLP2PTrafficEndpointsEvictedTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "hl_p2p_traffic_endpoints_evicted_total",
		Help: "Qualified process-local traffic-endpoint evictions since exporter start, partitioned by fixed reason.",
	}, []string{"reason"})

	HLNodeRateLimitedNonemptyFilesLatestDate = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hl_node_rate_limited_nonempty_files_latest_date",
		Help: "Non-empty regular files retained in the lexicographically newest source date directory; file evidence is not active rate limiting or an offender count.",
	}, []string{"stream"})
	HLNodeRateLimitedRecentFiles = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hl_node_rate_limited_recent_files",
		Help: "Non-empty regular files modified within 120 seconds at the last successful scan; recent file evidence is not a currently blocked peer count.",
	}, []string{"stream"})
	HLNodeRateLimitedLastNonemptyUpdateTimestampSeconds = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hl_node_rate_limited_last_nonempty_update_timestamp_seconds",
		Help: "Maximum non-future mtime ever observed for a non-empty file anywhere in the fixed stream source tree; zero when none has been observed.",
	}, []string{"stream"})
	HLNodeRateLimitedSourceUp = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hl_node_rate_limited_source_up",
		Help: "Whether discovery and all required directory and file metadata reads completed in the latest attempt.",
	}, []string{"stream"})
	HLNodeRateLimitedLastSuccessTimestampSeconds = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hl_node_rate_limited_last_success_timestamp_seconds",
		Help: "Exporter wall-clock Unix timestamp of the latest complete scan for the fixed stream.",
	}, []string{"stream"})
	HLNodeRateLimitedReadErrorsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "hl_node_rate_limited_read_errors_total",
		Help: "Rate-limit file-source failures since exporter start, partitioned by fixed stream and stage.",
	}, []string{"stream", "stage"})
)
