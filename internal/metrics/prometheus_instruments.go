package metrics

import (
	"runtime"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// prometheus_instruments.go declares Prometheus metric instruments using
// the prometheus client_golang library directly. These complement the
// OpenTelemetry instruments declared in instruments.go: both land in the
// same default registry that the OTel→Prometheus bridge reads, so they
// appear on the /metrics endpoint without extra wiring.
//
// Why two flavours: the OTel instruments existed in v2 and feed both the
// /metrics endpoint and the optional OTLP exporter. New gauges and
// counters added on top of v2 (visor, latency, crit_msg, tcp_traffic,
// parent_peer, disk, process, gossip_connections, and the exporter's own
// self-observability) use the simpler client_golang path because OTLP
// export isn't required for those operator-facing metrics. If a future
// instrument needs OTLP export too, move it to instruments.go.
//
// Naming follows the existing hl_<area>_<metric> convention.

// Visor / sync-state metrics (from internal/monitors/visor_monitor.go).
var (
	HLVisorHeight = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "hl_visor_height",
		Help: "Latest height observed by the visor (block height the node has applied).",
	})
	HLVisorInitialHeight = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "hl_visor_initial_height",
		Help: "Height at which the current visor instance started (sync starting point).",
	})
	HLVisorBlocksApplied = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "hl_visor_blocks_applied",
		Help: "Blocks the visor has applied since startup (height - initial_height).",
	})
	HLVisorScheduledFreezeHeight = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "hl_visor_scheduled_freeze_height",
		Help: "Height at which the visor is scheduled to freeze (0 if not scheduled).",
	})
	// Difference between the chain-consensus timestamp recorded for the
	// latest applied block and the local wall-clock time at the moment
	// the visor wrote the sample. Positive => chain's consensus clock is
	// ahead of this node's wall clock (typical, since blocks are timestamped
	// at decision time and observed after propagation). Negative =>
	// this node's wall clock is ahead of the chain (NTP drift or stale
	// data). Sustained large absolute values warrant investigation.
	HLVisorConsensusAheadOfWallSeconds = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "hl_visor_consensus_ahead_of_wall_seconds",
		Help: "consensus_time - wall_clock_time for the latest visor sample, in seconds (positive = chain ahead of local wall).",
	})
	HLVisorReferenceLagSeconds = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "hl_visor_reference_lag_seconds",
		Help: "Reference-node lag reported by the visor when available, in seconds.",
	})
	HLVisorLastObservationAge = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "hl_visor_last_observation_age_seconds",
		Help: "Age of the most recent visor sample read by the exporter, in seconds.",
	})
)

// Subsystem-latency metrics (from internal/monitors/subsystem_latency_monitor.go).
var (
	subsystemLabels = []string{"subsystem"}

	HLNodeSubsystemLatencyMean = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hl_node_subsystem_latency_mean_seconds",
		Help: "Mean per-sample latency for an internal hl-node subsystem.",
	}, subsystemLabels)
	HLNodeSubsystemLatencyMedian = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hl_node_subsystem_latency_median_seconds",
		Help: "Median per-sample latency for an internal hl-node subsystem.",
	}, subsystemLabels)
	HLNodeSubsystemLatencyP90 = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hl_node_subsystem_latency_p90_seconds",
		Help: "p90 per-sample latency for an internal hl-node subsystem.",
	}, subsystemLabels)
	HLNodeSubsystemLatencyP95 = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hl_node_subsystem_latency_p95_seconds",
		Help: "p95 per-sample latency for an internal hl-node subsystem.",
	}, subsystemLabels)
	HLNodeSubsystemLatencyMax = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hl_node_subsystem_latency_max_seconds",
		Help: "Max per-sample latency for an internal hl-node subsystem.",
	}, subsystemLabels)
	HLNodeSubsystemLatencyStdDev = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hl_node_subsystem_latency_stddev_seconds",
		Help: "Std-dev of per-sample latency for an internal hl-node subsystem.",
	}, subsystemLabels)
	HLNodeSubsystemWorkFrac = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hl_node_subsystem_work_fraction",
		Help: "Fraction of wall-clock time the subsystem spent doing work in the sample.",
	}, subsystemLabels)
	HLNodeSubsystemSamplesTotal = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hl_node_subsystem_samples_total",
		Help: "Total number of samples behind the latest aggregated row (cumulative).",
	}, subsystemLabels)
)

// Critical-message metrics from $NODE_HOME/data/crit_msg_stats. The on-disk
// schema is a tuple [start_time, n_bugs, n_crits, n_locations]:
//   - n_bugs:      cumulative count of "bug!" events (most severe; crash on
//                  bug if configured)
//   - n_crits:     cumulative count of "crit!" events (alert tier)
//   - n_locations: number of distinct (file, line) call sites that have
//                  fired a bug or crit at least once
//
// Modeled as gauges so a source-process restart cleanly resets the series
// (the cumulative counters reset to 0 on restart) rather than looking like
// a Prometheus counter regression.
var (
	HLNodeBugsTotal = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hl_node_bugs",
		Help: "Cumulative count of bug! events since the source process started. Any non-zero value is page-someone severity.",
	}, []string{"source"})
	HLNodeCritsTotal = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hl_node_crits",
		Help: "Cumulative count of crit! events since the source process started. Sustained growth = ongoing incident.",
	}, []string{"source"})
	HLNodeCritLocations = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hl_node_crit_locations",
		Help: "Distinct source-code call sites (file:line) that have fired a bug or crit at least once for this process lifetime.",
	}, []string{"source"})
	HLNodeCriticalMessagesBaseTime = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hl_node_critical_messages_base_time_seconds",
		Help: "Unix timestamp at which the current critical-message counters started accumulating (source process start).",
	}, []string{"source"})
)

// TCP-traffic metrics (from internal/monitors/tcp_traffic_monitor.go).
// Sourced from $NODE_HOME/data/tcp_traffic/hourly. The exporter only
// publishes the top-N peers per direction plus an "other" bucket, so
// label cardinality is bounded regardless of how many peers the node
// connects to. The raw values are per-30s-interval throughputs whose
// unit is not documented by hl-node; treat them as a comparative rate.
var (
	HLP2PPeerTraffic = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hl_p2p_peer_traffic",
		Help: "Per-peer TCP throughput from the latest tcp_traffic sample (unit: raw hl-node value, ~per-30s-interval rate). Labels: ip, direction (in/out). The literal label value ip=\"other\" sums all peers below the top-N cutoff.",
	}, []string{"ip", "direction"})

	HLP2PTotalTraffic = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hl_p2p_total_traffic",
		Help: "Sum of TCP throughput across all peers, per direction.",
	}, []string{"direction"})

	HLP2PPeerCount = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hl_p2p_peer_count",
		Help: "Number of distinct peers seen in the latest tcp_traffic sample, per direction.",
	}, []string{"direction"})

	HLP2PSampleAgeSeconds = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "hl_p2p_sample_age_seconds",
		Help: "Age of the most recent tcp_traffic sample read by the exporter, in seconds.",
	})
)

// Parent-peer metrics (from internal/monitors/parent_peer_monitor.go).
// For a non-validator node, the "parent peer" is the inbound peer
// delivering the most bytes — effectively the upstream source of block
// data. Losing it = the node likely stops syncing. Inspired by
// dwellir-public/hyperliquid-exporter.
var (
	HLNodeParentPeerInfo = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hl_node_parent_peer_info",
		Help: "1 for the IP currently identified as the node's parent peer. Only ever has one series active at a time.",
	}, []string{"ip"})

	HLNodeParentPeerTraffic = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "hl_node_parent_peer_traffic",
		Help: "Inbound throughput attributed to the current parent peer in the latest sample (same units as hl_p2p_peer_traffic).",
	})

	HLNodeParentPeerTenureSeconds = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "hl_node_parent_peer_tenure_seconds",
		Help: "Time the current parent peer has held the role since the exporter last observed a switch.",
	})

	HLNodeParentPeerSwitches = promauto.NewCounter(prometheus.CounterOpts{
		Name: "hl_node_parent_peer_switches_total",
		Help: "Number of times the parent peer has changed since the exporter started.",
	})
)

// hl-node / hl-visor process metrics (from internal/monitors/process_monitor.go).
// All labelled by `process` ∈ {"hl-node","hl-visor"} so a single dashboard
// shows both. Sourced from /proc on Linux; the monitor no-ops on other
// OSes.
var (
	HLNodeProcessUp = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hl_node_process_up",
		Help: "1 if the named hl-node process was found in /proc on the latest tick, 0 otherwise.",
	}, []string{"process"})
	HLNodeProcessStartTimeSeconds = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hl_node_process_start_time_seconds",
		Help: "Unix timestamp at which the process started (derived from /proc btime + stat starttime).",
	}, []string{"process"})
	HLNodeProcessCPUSecondsTotal = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hl_node_process_cpu_seconds_total",
		Help: "Cumulative CPU time consumed by the process (user+kernel), in seconds.",
	}, []string{"process"})
	HLNodeProcessRSSBytes = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hl_node_process_rss_bytes",
		Help: "Resident set size of the process, in bytes (/proc/PID/status VmRSS).",
	}, []string{"process"})
	HLNodeProcessVirtBytes = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hl_node_process_virt_bytes",
		Help: "Virtual memory size of the process, in bytes (/proc/PID/status VmSize).",
	}, []string{"process"})
	HLNodeProcessThreads = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hl_node_process_threads",
		Help: "Number of OS threads in the process (/proc/PID/status Threads).",
	}, []string{"process"})
	HLNodeProcessOpenFDs = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hl_node_process_open_fds",
		Help: "Number of open file descriptors held by the process (count of /proc/PID/fd entries).",
	}, []string{"process"})
)

// Gossip-connections metrics (from internal/monitors/gossip_connections_monitor.go).
// Each event_type is one tagged-union variant the node emits to
// node_logs/gossip_connections. The most operator-actionable are:
//   - "rejecting gossip stream because max peers reached" (capacity!)
//   - "error checking connection"                          (churn)
var (
	HLP2PGossipEventsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "hl_p2p_gossip_events_total",
		Help: "Cumulative count of gossip-connection events emitted by hl-node, by event type.",
	}, []string{"event_type"})
)

// Disk-usage metrics (from internal/monitors/disk_monitor.go). NODE_HOME
// can grow without bound; a full disk silently breaks the node.
var (
	HLNodeDiskUsedBytes = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "hl_node_disk_used_bytes",
		Help: "Total bytes consumed by the NODE_HOME tree (sum of regular file sizes).",
	})
	HLNodeDiskFreeBytes = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "hl_node_disk_free_bytes",
		Help: "Bytes available on the filesystem holding NODE_HOME (statfs Bavail*Bsize).",
	})
	HLNodeDiskTotalBytes = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "hl_node_disk_total_bytes",
		Help: "Total bytes on the filesystem holding NODE_HOME (statfs Blocks*Bsize).",
	})
	HLNodeDiskSubdirBytes = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hl_node_disk_subdir_bytes",
		Help: "Bytes consumed by major NODE_HOME subdirectories (per a hard-coded allowlist that targets known hot paths).",
	}, []string{"subdir"})
)

// Exporter self-observability (from internal/exporter and the monitor
// health module).
var (
	HLExporterBuildInfo = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hl_exporter_build_info",
		Help: "Build information for the running exporter; always 1.",
	}, []string{"version", "commit", "go_version"})

	HLExporterMonitorStartedSeconds = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hl_exporter_monitor_started_seconds",
		Help: "Unix timestamp at which each monitor's goroutine launched.",
	}, []string{"monitor"})
	HLExporterMonitorLastTickSeconds = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hl_exporter_monitor_last_tick_seconds",
		Help: "Unix timestamp of the most recent successful processing cycle per monitor (0 if the monitor has not yet processed real data).",
	}, []string{"monitor"})
	HLExporterMonitorPanicsTotal = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hl_exporter_monitor_panics_total",
		Help: "Total recovered panics for each monitor since exporter start.",
	}, []string{"monitor"})
	HLExporterMonitorErrorsTotal = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hl_exporter_monitor_errors_total",
		Help: "Total reported errors for each monitor since exporter start.",
	}, []string{"monitor"})
	HLExporterReady = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "hl_exporter_ready",
		Help: "1 once every registered monitor has produced at least one tick.",
	})
)

// SetBuildInfo records the build_info gauge with the values baked in at
// link time. Call once during startup.
func SetBuildInfo() {
	HLExporterBuildInfo.WithLabelValues(BuildVersion, BuildCommit, goVersion()).Set(1)
}

func goVersion() string {
	if BuildGoVersion != "" {
		return BuildGoVersion
	}
	return runtime.Version()
}

// PublishMonitorHealthSnapshot copies the in-memory monitor health state
// into the per-monitor gauges. Invoked periodically from a small goroutine
// so the values stay fresh between scrapes.
func PublishMonitorHealthSnapshot() {
	for _, s := range snapshotMonitors() {
		if s.StartedUnix > 0 {
			HLExporterMonitorStartedSeconds.WithLabelValues(s.Name).Set(float64(s.StartedUnix))
		}
		if s.LastTickUnix > 0 {
			HLExporterMonitorLastTickSeconds.WithLabelValues(s.Name).Set(float64(s.LastTickUnix))
		}
		HLExporterMonitorPanicsTotal.WithLabelValues(s.Name).Set(float64(s.Panics))
		HLExporterMonitorErrorsTotal.WithLabelValues(s.Name).Set(float64(s.Errors))
	}
	if Ready() {
		HLExporterReady.Set(1)
	} else {
		HLExporterReady.Set(0)
	}
}

// =====================================================================
// Phase-A always-on additions.
// =====================================================================

// Periodic-snapshot status (from internal/monitors/snapshot_status_monitor.go).
// hl-node writes one empty file at data/periodic_abci_state_statuses/
// <YYYYMMDD>/<height> *after* the corresponding .rmp snapshot completes;
// the empty file is a "snapshot succeeded" sentinel that outlives the
// .rmp itself.
var (
	HLNodeSnapshotLastHeight = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "hl_node_snapshot_last_height",
		Help: "Highest block height at which a periodic ABCI snapshot completed successfully.",
	})
	HLNodeSnapshotLastAgeSeconds = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "hl_node_snapshot_last_age_seconds",
		Help: "Seconds since the most recent successful periodic snapshot (now - mtime of newest status sentinel).",
	})
	HLNodeSnapshotKnown = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "hl_node_snapshot_known_count",
		Help: "Number of snapshot status sentinels retained on disk in the latest date directory.",
	})
)

// Node-state single-file gauges (from node_state_monitor.go).
var (
	HLVisorFreezeAbciHeight = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "hl_visor_freeze_abci_height",
		Help: "Replay floor written by hl-visor to hyperliquid_data/freeze_abci_height at process start.",
	})
	HLVisorBlocksAboveFreeze = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "hl_visor_blocks_above_freeze",
		Help: "hl_visor_height - hl_visor_freeze_abci_height (blocks applied since the current visor instance started).",
	})
	HLEVMDBCheckpointHeight = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hl_evm_db_checkpoint_height",
		Help: "Latest checkpoint height the named EVM DB tier has flushed to disk.",
	}, []string{"tier"}) // tier ∈ {"fast","slow"}
	HLEVMDBCheckpointLagBlocks = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "hl_evm_db_checkpoint_lag_blocks",
		Help: "hl_evm_db_checkpoint_height{tier=\"fast\"} - {tier=\"slow\"} (positive = slow tier behind fast).",
	})
)

// =====================================================================
// Phase-B --probe-info-endpoint flag.
// =====================================================================

var (
	HLInfoEndpointUp = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "hl_info_endpoint_up",
		Help: "1 if the last POST :3001/info returned HTTP 200 with a non-empty body, 0 otherwise.",
	})
	HLInfoEndpointLatencySeconds = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "hl_info_endpoint_latency_seconds",
		Help:    "Latency of the active POST :3001/info {\"type\":\"meta\"} probe.",
		Buckets: []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10},
	})
	HLInfoEndpointLastSuccessSeconds = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "hl_info_endpoint_last_success_seconds",
		Help: "Unix timestamp of the last successful info-endpoint probe.",
	})
	HLInfoEndpointFailuresTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "hl_info_endpoint_failures_total",
		Help: "Cumulative count of failed info-endpoint probes since exporter start.",
	})
)

// =====================================================================
// Phase-C --extended-metrics flag.
// =====================================================================

// tcp_lz4 — peer-level + global lz4 compression stats from
// data/tcp_lz4_stats/<YYYYMMDD>.
var (
	HLP2PLz4CompressionRatio = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hl_p2p_lz4_compression_ratio",
		Help: "lz4 compression ratio (bytes-after / bytes-before) for a peer in the latest sample. 1.0 = uncompressible. The literal ip=\"other\" sums all peers below the top-N cutoff.",
	}, []string{"ip", "direction"})
	HLP2PLz4BytesTotal = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hl_p2p_lz4_bytes_total",
		Help: "Cumulative compressed bytes since the source process started, per peer/direction.",
	}, []string{"ip", "direction"})
	HLP2PLz4PacketsTotal = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hl_p2p_lz4_packets_total",
		Help: "Cumulative compressed packets since the source process started, per peer/direction.",
	}, []string{"ip", "direction"})
	HLP2PLz4GlobalRatio = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "hl_p2p_lz4_global_ratio",
		Help: "lz4 compression ratio across all peers (global aggregate row).",
	})
	HLP2PLz4GlobalBytes = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "hl_p2p_lz4_global_bytes_total",
		Help: "Cumulative compressed bytes across all peers since the source process started.",
	})
	HLP2PLz4GlobalPackets = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "hl_p2p_lz4_global_packets_total",
		Help: "Cumulative compressed packets across all peers since the source process started.",
	})
)

// log_lines — line counts in data/log/{infra,trade}/{error,warn}/<YYYYMMDD>.
var (
	HLNodeLogLinesTotal = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hl_node_log_lines",
		Help: "Lines in the day's log file. Modeled as a gauge so the per-day file rotation cleanly resets the series at midnight.",
	}, []string{"stream", "level"})
	HLNodeLogBytes = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hl_node_log_bytes",
		Help: "Size in bytes of the day's log file.",
	}, []string{"stream", "level"})
)

// public_ip — heartbeat + change detection on last_known_public_ip.json.
var (
	HLNodePublicIPInfo = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hl_node_public_ip_info",
		Help: "1 for the IP currently reported as the node's public address.",
	}, []string{"ip"})
	HLNodePublicIPAgeSeconds = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "hl_node_public_ip_age_seconds",
		Help: "Age of last_known_public_ip.json (now - mtime); should refresh every ~13 min while hl-node runs.",
	})
	HLNodePublicIPChangesTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "hl_node_public_ip_changes_total",
		Help: "Cumulative count of public-IP changes observed since the exporter started.",
	})
)

// tokio_runtime — per-task Tokio metrics from
// data/tokio_spawn_forever_metrics/hourly/<date>/<hour>.
var (
	HLTokioTaskPollSecondsTotal = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hl_tokio_task_poll_seconds_total",
		Help: "Cumulative poll time for the named Tokio task since the source process started.",
	}, []string{"task"})
	HLTokioTaskPollsTotal = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hl_tokio_task_polls_total",
		Help: "Cumulative poll count for the named Tokio task.",
	}, []string{"task"})
	HLTokioTaskSlowPollsTotal = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hl_tokio_task_slow_polls_total",
		Help: "Cumulative slow polls (poll exceeded the runtime's slow-poll threshold).",
	}, []string{"task"})
	HLTokioTaskLongDelaysTotal = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hl_tokio_task_long_delays_total",
		Help: "Cumulative long scheduling delays (task waited too long to be polled after becoming ready).",
	}, []string{"task"})
	HLTokioTaskIdleSecondsTotal = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hl_tokio_task_idle_seconds_total",
		Help: "Cumulative idle time (task awaiting work) for the named Tokio task.",
	}, []string{"task"})
	HLTokioTaskDroppedTotal = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hl_tokio_task_dropped_total",
		Help: "Cumulative dropped (panicked or cancelled) task count.",
	}, []string{"task"})
)

// operator_config — mtime of file_mod_time_tracker/*.json files. Tells the
// operator when they last changed local node config.
var (
	HLNodeOperatorConfigAgeSeconds = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hl_node_operator_config_age_seconds",
		Help: "Age (now - mtime) of operator-edited config files under file_mod_time_tracker/.",
	}, []string{"file"})
)

// tmp_dir — size + stale-file count under ~/hl/tmp.
var (
	HLNodeTmpBytes = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "hl_node_tmp_bytes",
		Help: "Total size of files under $NODE_HOME/tmp. Persistent growth indicates orphaned writes from past crashes.",
	})
	HLNodeTmpStaleFiles = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "hl_node_tmp_stale_files",
		Help: "Number of files under $NODE_HOME/tmp older than 24h (orphaned write candidates).",
	})
)

// =====================================================================
// Phase-D round-5 additions.
// =====================================================================

// TCP connection state, parsed from /proc/net/tcp{,6}. Cardinality is
// bounded: 4 listening ports × ~5 states.
var (
	HLP2PTCPConnections = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hl_p2p_tcp_connections",
		Help: "Count of TCP connections on each hl-node listening port, grouped by socket state. Sourced from /proc/net/tcp{,6}.",
	}, []string{"port", "state"})
)

// Filesystem-observed restart tracking (always on).
var (
	HLNodeObservedRunsTotal = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "hl_node_observed_runs_total",
		Help: "Number of hl-node runs observed via the filesystem (count of run-timestamp directories under data/replica_cmds/).",
	})
	HLNodeObservedRunStartSeconds = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "hl_node_observed_run_start_seconds",
		Help: "Unix timestamp of the most recent run's start, derived from the newest data/replica_cmds/<run_timestamp>/ mtime.",
	})
)

// RocksDB LSM-tree stats (--extended-metrics).
var (
	HLRocksDBSSTFiles = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hl_rocksdb_sst_files",
		Help: "Count of .sst files for the named RocksDB (LSM tree size). Sustained growth without compaction = stuck.",
	}, []string{"db"})
	HLRocksDBWriteStallsTotal = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hl_rocksdb_write_stalls_total",
		Help: "Cumulative write-stall events from the RocksDB LOG file, by reason. Modeled as a gauge because the value resets on hl-node restart.",
	}, []string{"db", "reason"})
	HLRocksDBBlockCacheUsageBytes = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hl_rocksdb_block_cache_usage_bytes",
		Help: "Current block-cache usage in bytes, as reported by the RocksDB LOG's stats block.",
	}, []string{"db"})
)

// Per-step latency for bucket_guard + tcp_lz4 subsystems (--extended-metrics).
// Cardinality: 19 bucket_guard steps × 4 stats = 76, plus 4 tcp_lz4 steps × 4
// stats = 16. Bounded by the on-disk subdirectory count.
var (
	HLLatencyBucketGuard = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hl_latency_bucket_guard_seconds",
		Help: "Latency stats for individual bucket_guard sub-steps (e.g. begin_block, distribute_funding). quantile ∈ {p50,p90,p95,max,mean,std_dev,work_frac}.",
	}, []string{"step", "quantile"})
	HLTCPLz4Latency = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hl_tcp_lz4_latency_seconds",
		Help: "Per-direction-per-port lz4 latency stats. quantile ∈ {p50,p90,p95,max,mean,std_dev,work_frac}.",
	}, []string{"direction", "port", "quantile"})
)

// Per-location crit_msg detail (--extended-metrics). Hard cap on
// cardinality at critLocationCap to defend against pathological growth.
var (
	HLNodeCritLocation = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hl_node_crit_location",
		Help: "Per-source-location crit count from /tmp/crit_msg_latest_stats/hl-node.json. Value is the location's cumulative count since the source process started.",
	}, []string{"file", "line"})
	HLNodeCritLocationLastSeenSeconds = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hl_node_crit_location_last_seen_seconds",
		Help: "Unix timestamp at which the named source location most recently emitted a crit.",
	}, []string{"file", "line"})
)

// Shell-exec orphan tmp files (--extended-metrics, extends tmp_dir).
var (
	HLNodeShellExecPending = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "hl_node_shell_exec_pending",
		Help: "Count of files under $NODE_HOME/tmp/shell_rs_out/. Each visor shell-exec leaves one; sustained growth indicates the visor's cleanup pass is broken.",
	})
)
