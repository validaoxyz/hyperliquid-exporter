package metrics

import (
	"runtime"
	"sync"
	"time"

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
		Help: "initial_height from the latest valid visor state; this is scoped to that reported process generation.",
	})
	HLVisorBlocksApplied = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "hl_visor_blocks_applied",
		Help: "Nonnegative process-generation observation height - initial_height from the latest valid visor state; reset to 0 when either operand is unavailable.",
	})
	HLVisorScheduledFreezeHeight = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "hl_visor_scheduled_freeze_height",
		Help: "Deprecated compatibility value for scheduled_freeze_height from the latest valid visor state; consult hl_visor_scheduled_freeze_height_available because 0 also represents unavailable.",
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
	HLVisorReferenceLagPopulated = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "hl_visor_reference_lag_populated",
		Help: "1 if the visor sample carried a reference_lag field, else 0",
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
		Help: "Raw upstream work_fraction value for the subsystem's latest sampling window; it is not constrained to the interval 0..1.",
	}, subsystemLabels)
	HLNodeSubsystemSamplesTotal = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hl_node_subsystem_samples_total",
		Help: "Total number of samples behind the latest aggregated row (cumulative).",
	}, subsystemLabels)
)

// Critical-message metrics from $NODE_HOME/data/crit_msg_stats. The on-disk
// schema is a tuple [start_time, n_bugs, n_crits, n_locations]:
//   - n_bugs:      cumulative count of "bug!" events (most severe; crash on
//     bug if configured)
//   - n_crits:     cumulative count of "crit!" events (alert tier)
//   - n_locations: number of distinct (file, line) call sites that have
//     fired a bug or crit at least once
//
// Modeled as gauges so a source-process restart cleanly resets the series
// (the cumulative counters reset to 0 on restart) rather than looking like
// a Prometheus counter regression.
var (
	HLNodeBugsTotal = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hl_node_bugs",
		Help: "Cumulative bug! event count reported by the source process generation.",
	}, []string{"source"})
	HLNodeCritsTotal = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hl_node_crits",
		Help: "Cumulative crit! event count reported by the source process generation.",
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
// publishes the top-N positive endpoint values per traffic direction plus an
// "other" bucket. The upstream field is a point-rate-like value with unresolved
// formal unit and cadence; it is not a byte counter or a connection role.
var (
	HLP2PPeerTraffic = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hl_p2p_peer_traffic",
		Help: "Raw unresolved-unit tcp_traffic point value from the latest complete snapshot for each top-16 positive endpoint and traffic direction; ip=\"other\" sums the remaining positive endpoints. This is not a byte counter, connection origin, or topology role.",
	}, []string{"ip", "direction"})

	HLP2PTotalTraffic = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hl_p2p_total_traffic",
		Help: "Sum of the raw unresolved-unit tcp_traffic point field across all reported endpoint rows in the latest complete snapshot, per traffic direction.",
	}, []string{"direction"})

	HLP2PPeerCount = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hl_p2p_peer_count",
		Help: "Number of distinct canonical IP endpoint rows reported in the latest complete tcp_traffic sample per traffic direction, including zero-valued rows; this is observation, not connectivity.",
	}, []string{"direction"})

	HLP2PSampleAgeSeconds = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hl_p2p_sample_age_seconds",
		Help: "Wall-clock seconds since exporter receipt of the latest complete valid tcp_traffic snapshot; advances without new records.",
	}, []string{})
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
		Help: "Newline-committed gossip-connection events observed after exporter start, by fixed event type; history found by a successful initial scan is skipped, a file first found after an initial discovery failure is read from its start, and the counter resets on exporter restart.",
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
		Help: "Unix timestamp of the latest transition from zero to one active workers for each monitor.",
	}, []string{"monitor"})
	HLExporterMonitorLastTickSeconds = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hl_exporter_monitor_last_tick_seconds",
		Help: "Deprecated compatibility alias for the most recent valid observation reported through MarkMonitorTick; absent before one is observed.",
	}, []string{"monitor"})
	HLExporterMonitorRegistered = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hl_exporter_monitor_registered",
		Help: "Whether this monitor is part of the configured startup set (1=yes).",
	}, []string{"monitor"})
	HLExporterMonitorRunning = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hl_exporter_monitor_running",
		Help: "Whether at least one worker for this logical monitor is currently running (1=yes, 0=no).",
	}, []string{"monitor"})
	HLExporterMonitorWorkers = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hl_exporter_monitor_workers",
		Help: "Current number of active outer and inner workers for this logical monitor.",
	}, []string{"monitor"})
	HLExporterMonitorExitedSeconds = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hl_exporter_monitor_exited_seconds",
		Help: "Unix timestamp at which the final worker most recently exited; absent while running or before first exit.",
	}, []string{"monitor"})
	HLExporterMonitorLastAttemptSeconds = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hl_exporter_monitor_last_attempt_seconds",
		Help: "Unix timestamp of the most recent real poll, read, scan, or request attempt; absent before first attempt.",
	}, []string{"monitor"})
	HLExporterMonitorLastValidSeconds = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hl_exporter_monitor_last_valid_observation_seconds",
		Help: "Unix timestamp of the most recent complete valid observation; absent before first valid observation.",
	}, []string{"monitor"})
	HLExporterMonitorLastPublicationSeconds = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hl_exporter_monitor_last_publication_seconds",
		Help: "Unix timestamp of the most recent successful metric publication; absent before first publication.",
	}, []string{"monitor"})
	HLExporterMonitorPanicsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "hl_exporter_monitor_panics_total",
		Help: "Total recovered panics for each monitor since exporter start.",
	}, []string{"monitor"})
	HLExporterMonitorErrorsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "hl_exporter_monitor_errors_total",
		Help: "Total reported errors for each monitor since exporter start.",
	}, []string{"monitor"})
	HLExporterMonitorErrorDropsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "hl_exporter_monitor_error_drops_total",
		Help: "Monitor error reports dropped because that monitor's independent bounded error channel was full.",
	}, []string{"monitor"})
	HLExporterReady = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "hl_exporter_ready",
		Help: "1 once every configured monitor has started at least one worker; source availability and freshness are reported separately.",
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
	WithPrometheusSnapshotUpdate(publishMonitorHealthSnapshot)
}

func publishMonitorHealthSnapshot() {
	publishMonitorHealthSnapshotAt(time.Now())
}

func publishMonitorHealthSnapshotAt(now time.Time) {
	for _, s := range snapshotMonitors() {
		if s.Registered {
			HLExporterMonitorRegistered.WithLabelValues(s.Name).Set(1)
		} else {
			HLExporterMonitorRegistered.WithLabelValues(s.Name).Set(0)
		}
		if s.Running {
			HLExporterMonitorRunning.WithLabelValues(s.Name).Set(1)
		} else {
			HLExporterMonitorRunning.WithLabelValues(s.Name).Set(0)
		}
		HLExporterMonitorWorkers.WithLabelValues(s.Name).Set(float64(s.Workers))
		if s.StartedUnix > 0 {
			HLExporterMonitorStartedSeconds.WithLabelValues(s.Name).Set(float64(s.StartedUnix))
		} else {
			HLExporterMonitorStartedSeconds.DeleteLabelValues(s.Name)
		}
		if s.ExitedUnix > 0 {
			HLExporterMonitorExitedSeconds.WithLabelValues(s.Name).Set(float64(s.ExitedUnix))
		} else {
			HLExporterMonitorExitedSeconds.DeleteLabelValues(s.Name)
		}
		setOptionalMonitorTimestamp(HLExporterMonitorLastAttemptSeconds, s.Name, s.LastAttemptUnix)
		setOptionalMonitorTimestamp(HLExporterMonitorLastValidSeconds, s.Name, s.LastValidUnix)
		setOptionalMonitorTimestamp(HLExporterMonitorLastPublicationSeconds, s.Name, s.LastPublicationUnix)
		// Keep the legacy family as an exact alias of last-valid observation.
		setOptionalMonitorTimestamp(HLExporterMonitorLastTickSeconds, s.Name, s.LastValidUnix)
		// Panics/errors are real Counters now, driven by .Inc() at the
		// point each panic/error is recorded (IncMonitorPanic / IncMonitorError),
		// not mirrored from the atomic snapshot here.
	}
	if Ready() {
		HLExporterReady.Set(1)
	} else {
		HLExporterReady.Set(0)
	}
	publishSourceHealthAt(now.Unix())
	publishValidatorAPICacheAgeAt(now)
}

func setOptionalMonitorTimestamp(metric *prometheus.GaugeVec, monitor string, value int64) {
	if value > 0 {
		metric.WithLabelValues(monitor).Set(float64(value))
	} else {
		metric.DeleteLabelValues(monitor)
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
	HLNodeSnapshotLastHeight = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hl_node_snapshot_last_height",
		Help: "Highest block height at which a periodic ABCI snapshot completed successfully.",
	}, []string{})
	HLNodeSnapshotLastAgeSeconds = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hl_node_snapshot_last_age_seconds",
		Help: "Seconds since the most recent successful periodic snapshot (now - mtime of newest status sentinel).",
	}, []string{})
	HLNodeSnapshotKnown = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hl_node_snapshot_sentinels_retained",
		Help: "Number of snapshot-completion sentinels in the retained scan window of at most the two newest valid date directories; this is not cadence or capacity headroom.",
	}, []string{})
)

// Node-state single-file gauges (from node_state_monitor.go).
var (
	HLVisorFreezeAbciHeight = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "hl_visor_freeze_abci_height",
		Help: "Deprecated compatibility value read from hyperliquid_data/freeze_abci_height; this persisted core/ABCI height can survive process restarts and is not a current scheduled freeze.",
	})
	HLVisorBlocksAboveFreeze = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "hl_visor_blocks_above_freeze",
		Help: "Deprecated compatibility value: nonnegative latest visor height minus persisted hyperliquid_data/freeze_abci_height when both are readable; not process-generation progress.",
	})
	HLEVMDBCheckpointHeight = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hl_evm_db_checkpoint_height",
		Help: "Deprecated misleading name: core/ABCI height read from hyperliquid_data/evm_db_hub_<tier>/cp_checkpoint_height; not EVM block height or current execution head.",
	}, []string{"tier"}) // tier ∈ {"fast","slow"}
	HLEVMDBCheckpointLagBlocks = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "hl_evm_db_checkpoint_lag_blocks",
		Help: "Deprecated misleading name: fast minus slow persisted core/ABCI cp_checkpoint_height files when both are readable; not an EVM execution lag.",
	})
)

// =====================================================================
// Phase-B --probe-info-endpoint flag.
// =====================================================================

var ()

// Info-probe instruments register lazily via InitInfoProbeInstruments: with
// package-init registration, every node NOT running the probe exported
// hl_info_endpoint_up == 0 forever, tripping any `== 0` alert even though
// the probe is opt-in (--probe-info-endpoint).
var (
	HLInfoEndpointUp                 prometheus.Gauge
	HLInfoEndpointLatencySeconds     prometheus.Histogram
	HLInfoEndpointLastSuccessSeconds prometheus.Gauge
	HLInfoEndpointFailuresTotal      prometheus.Counter
	HLInfoExchangeStatusDeltaSeconds prometheus.Gauge

	infoProbeInstrumentsOnce    sync.Once
	exchangeDeltaInstrumentOnce sync.Once
)

// InitInfoProbeInstruments registers the probe family. Called from
// StartInfoProbeMonitor so the series exist only when probing is enabled.
func InitInfoProbeInstruments() {
	infoProbeInstrumentsOnce.Do(func() {
		HLInfoEndpointUp = promauto.NewGauge(prometheus.GaugeOpts{
			Name: "hl_info_endpoint_up",
			Help: "1 if the last POST :3001/info returned HTTP 200 with a non-empty body, 0 otherwise. Absent (not 0) when --probe-info-endpoint is off.",
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
	})
}

// InitExchangeStatusDeltaInstrument registers the exchangeStatus timestamp
// delta gauge on first successful parse, so nodes whose info endpoint
// doesn't serve the field never export a misleading 0.
func InitExchangeStatusDeltaInstrument() {
	exchangeDeltaInstrumentOnce.Do(func() {
		HLInfoExchangeStatusDeltaSeconds = promauto.NewGauge(prometheus.GaugeOpts{
			Name: "hl_info_exchange_status_delta_seconds",
			Help: "local_wall_clock - exchangeStatus.time reported by the node's info endpoint, in seconds. Sustained growth means the node serves stale exchange state.",
		})
	})
}

// =====================================================================
// Phase-C --extended-metrics flag.
// =====================================================================

// tcp_lz4 — peer-level + global lz4 compression stats from
// data/tcp_lz4_stats/<YYYYMMDD>.
var (
	HLP2PLz4CompressionRatio = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hl_p2p_lz4_compression_ratio",
		Help: "Deprecated latest-window gauge: byte-field-weighted mean of upstream-reported per-port ratios for a bounded endpoint/direction; not a derived aggregate compression ratio. Do not use rate/increase.",
	}, []string{"ip", "direction"})
	HLP2PLz4BytesTotal = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hl_p2p_lz4_bytes_total",
		Help: "Deprecated latest-window gauge of the unresolved upstream byte field per bounded endpoint/direction; not cumulative despite the name. Do not use rate/increase.",
	}, []string{"ip", "direction"})
	HLP2PLz4PacketsTotal = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hl_p2p_lz4_packets_total",
		Help: "Deprecated latest-window gauge of the upstream packet field per bounded endpoint/direction; not cumulative despite the name. Do not use rate/increase.",
	}, []string{"ip", "direction"})
	HLP2PLz4GlobalRatio = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "hl_p2p_lz4_global_ratio",
		Help: "Deprecated latest-window gauge of the source-provided global ratio; not a locally derived compression ratio.",
	})
	HLP2PLz4GlobalBytes = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "hl_p2p_lz4_global_bytes_total",
		Help: "Deprecated latest-window gauge of the source-provided unresolved global byte field; not cumulative despite the name. Do not use rate/increase.",
	})
	HLP2PLz4GlobalPackets = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "hl_p2p_lz4_global_packets_total",
		Help: "Deprecated latest-window gauge of the source-provided global packet field; not cumulative despite the name. Do not use rate/increase.",
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

// public_ip — observed file age + change detection on last_known_public_ip.json.
var (
	HLNodePublicIPInfo = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hl_node_public_ip_info",
		Help: "1 for the IP currently reported as the node's public address.",
	}, []string{"ip"})
	HLNodePublicIPAgeSeconds = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hl_node_public_ip_age_seconds",
		Help: "Wall-clock seconds since the observed mtime of last_known_public_ip.json; no universal rewrite cadence is implied.",
	}, []string{})
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
	// HLTokioSampleAgeSeconds ages the newest tokio sample. The feed can die
	// while the node stays healthy (observed live: a 26h gap); when it does,
	// the per-task gauges are withdrawn and only this age keeps climbing.
	HLTokioSampleAgeSeconds = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hl_tokio_sample_age_seconds",
		Help: "Age of the newest tokio task sample. When this passes ~15m the hl_tokio_task_* gauges are withdrawn (stale source) until the feed resumes.",
	}, []string{})
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
		Help: "Apparent bytes across all files in the last complete $NODE_HOME/tmp scan; no cause is inferred from growth.",
	})
	HLNodeTmpStaleFiles = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "hl_node_tmp_stale_files",
		Help: "Files older than 24 hours in the last complete $NODE_HOME/tmp scan, including empty shell_rs_out receipts and material files.",
	})
)

// =====================================================================
// Phase-D round-5 additions.
// =====================================================================

// Deprecated TCP compatibility aggregate, parsed from /proc/net/tcp{,6}.
var (
	HLP2PTCPConnections = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hl_p2p_tcp_connections",
		Help: "Deprecated aggregate of kernel TCP socket rows associated with a configured service port on either literal socket side, grouped by state; use hl_p2p_tcp_socket_connections.",
	}, []string{"port", "state"})
)

// Filesystem-observed restart tracking (always on).
var (
	HLNodeObservedRunsTotal = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hl_node_observed_runs",
		Help: "Current number of valid retained run-timestamp directories under data/replica_cmds/; pruning can decrease this gauge and it is not a lifetime counter.",
	}, []string{})
	HLNodeObservedRunStartSeconds = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hl_node_observed_run_start_seconds",
		Help: "Unix timestamp of the newest retained hl-node run, parsed from the immutable data/replica_cmds/<run_timestamp>/ directory name.",
	}, []string{})
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
		Help: "Latency stats for individual bucket_guard sub-steps (e.g. begin_block, distribute_funding). quantile ∈ {p50,p90,p95,max,mean,std_dev}.",
	}, []string{"step", "quantile"})
	HLLatencyBucketGuardWorkFraction = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hl_latency_bucket_guard_work_fraction",
		Help: "Raw upstream work_fraction value for each bucket_guard sub-step's latest sampling window; it is not constrained to the interval 0..1.",
	}, []string{"step"})
	HLTCPLz4Latency = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hl_tcp_lz4_latency_seconds",
		Help: "Per-direction-per-port lz4 latency stats. quantile ∈ {p50,p90,p95,max,mean,std_dev}.",
	}, []string{"direction", "port", "quantile"})
	HLTCPLz4WorkFraction = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hl_tcp_lz4_work_fraction",
		Help: "Raw upstream work_fraction value for each lz4 direction/port's latest sampling window; it is not constrained to the interval 0..1.",
	}, []string{"direction", "port"})
)

// Per-location crit_msg detail (--extended-metrics). Hard cap on
// cardinality at critLocationCap to defend against pathological growth.
var (
	HLNodeCritLocation = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hl_node_crit_location",
		Help: "Per-source-location count from the matched hl-visor rich critical-message generation. Value is cumulative since the visor process started.",
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
		Help: "Compatibility count of regular files retained under $NODE_HOME/tmp/shell_rs_out/, including expected empty receipts; use material-class metrics for stale payload evidence.",
	})
)

// =====================================================================
// Validator-only additions.
// =====================================================================

// Consensus accumulator counters from
// $NODE_HOME/data/accumulator_buckets/consensus/<bucket>/hourly/<date>/<hour>.
// Each bucket line is {"time","n","delta"} written per ~30s flush window:
// delta is the quantity accumulated in that window and n is the number of
// accumulation events in it (n == delta only for CommittedBlocks, where
// each block adds exactly 1). Neither field is cumulative, so the exporter
// sums deltas into true Prometheus counters. Values are cumulative since
// the exporter started observing; rate()/increase() are valid.
var (
	HLConsensusCommittedBlocks = promauto.NewCounter(prometheus.CounterOpts{
		Name: "hl_consensus_committed_blocks_total",
		Help: "Accepted CommittedBlocks source-window delta accumulated since exporter start; source n is an observation count and is not exported here.",
	})
	HLConsensusCommittedTxs = promauto.NewCounter(prometheus.CounterOpts{
		Name: "hl_consensus_committed_txs_total",
		Help: "Accepted CommittedTxs source-window delta accumulated since exporter start; source n is an observation count and is not exported here.",
	})
	HLConsensusCommittedTxBytes = promauto.NewCounter(prometheus.CounterOpts{
		Name: "hl_consensus_committed_tx_bytes_total",
		Help: "Accepted CommittedTxBytes source-window delta accumulated since exporter start; source n is an observation count and is not exported here.",
	})
	HLConsensusDroppedTxs = promauto.NewCounter(prometheus.CounterOpts{
		Name: "hl_consensus_dropped_txs_total",
		Help: "Accepted DroppedTxs source-window delta accumulated since exporter start; no cause or severity is inferred.",
	})
	HLConsensusRoundCatchup = promauto.NewCounter(prometheus.CounterOpts{
		Name: "hl_consensus_round_catchup_total",
		Help: "Positive RoundCatchUp delta accumulated since the exporter started observing; upstream direction semantics are intentionally unspecified.",
	})
	HLConsensusRoundQC = promauto.NewCounter(prometheus.CounterOpts{
		Name: "hl_consensus_round_qc_total",
		Help: "Accepted RoundQc source-window delta accumulated since exporter start; no relationship to committed blocks is inferred.",
	})
	HLConsensusRoundTC = promauto.NewCounter(prometheus.CounterOpts{
		Name: "hl_consensus_round_tc_total",
		Help: "Accepted RoundTc source-window delta accumulated since exporter start; no cause or network-health conclusion is inferred.",
	})
	HLConsensusRPCRequestsRegistered = promauto.NewCounter(prometheus.CounterOpts{
		Name: "hl_consensus_rpc_requests_registered_total",
		Help: "Accepted RpcRequestsRegistered delta for outbound local accumulator work since exporter start; not a served-request count.",
	})
	HLConsensusRPCRequestsSent = promauto.NewCounter(prometheus.CounterOpts{
		Name: "hl_consensus_rpc_requests_sent_total",
		Help: "Accepted RpcRequestsSent delta for outbound local accumulator work since exporter start; no request-lifecycle stage is inferred.",
	})
)

// Node-local jailed set parsed from the status stream's
// current_jailed_validators field (data/node_logs/status). Faster than the
// 5-minute validatorSummaries poll and works without internet access.
var HLConsensusValidatorJailedLocal = promauto.NewGaugeVec(prometheus.GaugeOpts{
	Name: "hl_consensus_validator_jailed_local",
	Help: "1 for each signer in current_jailed_validators from the latest successfully parsed local status row; unknown registry joins remain explicit and no cadence is assumed.",
}, []string{"validator", "signer", "name"})

// Replay-event tracking from $NODE_HOME/data/node_logs/replay/.
// Each subdir is named "<height>_<iso>" and is created when the node
// enters replay mode (recovering from a checkpoint). The count is the
// number of replay events the filesystem still retains.
var (
	HLNodeReplayEventsTotal = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hl_node_replay_events",
		Help: "Current number of valid retained replay marker directories under data/node_logs/replay/; pruning can decrease this gauge and it is not a lifetime counter.",
	}, []string{})
	HLNodeReplayLastSeconds = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hl_node_replay_last_seconds",
		Help: "Immutable start timestamp of the newest retained replay marker, parsed from its <height>_<ISO timestamp> directory name.",
	}, []string{})
	HLNodeReplayLastHeight = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hl_node_replay_last_height",
		Help: "Block height at which the most recent replay event happened (parsed from the subdir name).",
	}, []string{})
)

// Mempool monitor — from data/node_logs/mempool/hourly/<date>/<hour>.
// Per-event counter with bounded cardinality (allowlist of known event
// tags + 'other' rollup). add_tx and verify_block carry a closed status
// sub-label; for other events status is "not_applicable".
var (
	HLMempoolEventsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "hl_mempool_events_total",
		Help: "Valid mempool records observed since exporter start. add_tx and verify_block status is one of ok, err, other; other event types use not_applicable. Unknown tags use event_type=\"other\".",
	}, []string{"event_type", "status"})

	HLMempoolSize = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hl_mempool_size",
		Help: "Mempool component sizes from the 'Size stats' event payload. components: committed_tx_hashes, uncommitted_txs, blocks, rpc_requests.",
	}, []string{"component"})
)

// Split-client mempool transaction stream — from
// data/mempool_txs/hourly/<date>/<hour>. This directory appears only when
// split_client_blocks is enabled on this node and every upstream hop. The raw
// stream contains tx hashes, signatures, addresses, asset ids, prices, sizes,
// cloids, and order ids; the exporter deliberately emits only aggregate
// counters/histograms with bounded labels.
var (
	HLMempoolTxsSeenTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "hl_mempool_txs_seen_total",
		Help: "Uncommitted mempool transaction records observed in data/mempool_txs.",
	})
	HLMempoolTxsBytesTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "hl_mempool_txs_bytes_total",
		Help: "Bytes read from complete JSONL records in data/mempool_txs.",
	})
	HLMempoolTxsSignedActionsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "hl_mempool_txs_signed_actions_total",
		Help: "Signed actions observed in split-client mempool transaction records, by allowlisted action type. New types are bucketed as type=\"other\".",
	}, []string{"type"})
	HLMempoolTxsOperationsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "hl_mempool_txs_operations_total",
		Help: "Individual operations observed in split-client mempool transactions, by allowlisted action type. orders/cancels/modifies count array elements; scalar actions count as 1.",
	}, []string{"type"})
	HLMempoolTxsOrderOperationsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "hl_mempool_txs_order_operations_total",
		Help: "Order-like operations observed in split-client mempool transactions, by side and time-in-force. Includes order actions, modify actions, and batchModify entries.",
	}, []string{"side", "tif"})
	HLMempoolTxsSignedActionsPerRecord = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "hl_mempool_txs_signed_actions_per_record",
		Help:    "Distribution of signed_actions array length per split-client mempool transaction record.",
		Buckets: []float64{1, 2, 5, 10, 25, 50, 100, 250, 500, 1000, 2500, 5000, 10000},
	})
	HLMempoolTxsOperationsPerRecord = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "hl_mempool_txs_operations_per_record",
		Help:    "Distribution of individual operation count per split-client mempool transaction record.",
		Buckets: []float64{1, 2, 5, 10, 25, 50, 100, 250, 500, 1000, 2500, 5000, 10000, 25000},
	})
	HLMempoolTxsLatestTime = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hl_mempool_txs_latest_time",
		Help: "Source Unix timestamp from the latest valid split-client mempool transaction record; absent before first observation.",
	}, []string{})
	HLMempoolTxsSampleAgeSeconds = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hl_mempool_txs_sample_age_seconds",
		Help: "Wall-clock age since exporter receipt of the latest valid split-client mempool transaction record; absent before first observation and advances through quiet/error periods.",
	}, []string{})
)

// Operator-config FAILED_LOAD tripwire. Counts files under
// file_mod_time_tracker/ whose name ends with _FAILED_LOAD — these are
// sidecars hl-node writes when an operator-supplied config (e.g.
// firewall_ips.json) fails to load. Existence = the operator's intent
// is NOT being applied; sustained > 0 = silent misconfiguration.
var (
	HLNodeOperatorConfigFailedLoads = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hl_node_operator_config_failed_loads",
		Help: "Count of *_FAILED_LOAD sidecar files in file_mod_time_tracker/. Each one is a config the operator pushed but hl-node rejected — silent misconfiguration.",
	}, []string{})
)

// Per-step latency for the validator-only consensus + l1_task subsystems
// (--extended-metrics). Sourced from
// data/latency_summaries/{consensus,l1_task_latency}/<step>/<YYYYMMDD>.
// 19 consensus steps + 4 l1_task steps, × 7 quantile values.
var (
	HLLatencyConsensus = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hl_latency_consensus_seconds",
		Help: "Validator-only per-step latency for the consensus state machine. The step label names which input the validator was processing (HandleStateInput::Block, TxCommit, etc.). quantile ∈ {p50,p90,p95,max,mean,std_dev}.",
	}, []string{"step", "quantile"})

	HLLatencyConsensusWorkFraction = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hl_latency_consensus_work_fraction",
		Help: "Raw upstream work_fraction value for each consensus step's latest sampling window; it is not constrained to the interval 0..1.",
	}, []string{"step"})

	HLLatencyL1Task = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hl_latency_l1_task_seconds",
		Help: "Validator-only L1 block-apply phase latencies (BeginBlock / DeliverSignedActions / EndBlock / RecoverUsers).",
	}, []string{"step", "quantile"})

	HLLatencyL1TaskWorkFraction = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hl_latency_l1_task_work_fraction",
		Help: "Raw upstream work_fraction value for each L1 phase's latest sampling window; it is not constrained to the interval 0..1.",
	}, []string{"step"})
)

// Peer set: rolling-window unique-IP counts + cardinality stats.
// Always on; cheap (≤5 series total).
var (
	HLP2PUniquePeersSeen = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hl_p2p_unique_peers_seen",
		Help: "Deprecated alias: qualified process-local traffic endpoints refreshed within the fixed window after two consecutive top-16 positive snapshots; observation is not connectivity.",
	}, []string{"window"})
	HLP2PPeersTotal = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hl_p2p_peers",
		Help: "Deprecated alias: unique canonical IPs with any positive value in the latest complete tcp_traffic snapshot; observation is not connectivity.",
	}, []string{})
	HLP2PPeersAddedTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "hl_p2p_peers_added_total",
		Help: "Deprecated alias: qualified process-local traffic-endpoint admissions since exporter start after two consecutive eligible snapshots.",
	})
	HLP2PPeersEvictedTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "hl_p2p_peers_evicted_total",
		Help: "Deprecated alias: qualified process-local traffic-endpoint evictions since exporter start, summed across TTL and capacity reasons.",
	})
)

// Jailing-config gauges register on first successful read of
// heartbeat_jailing_config.json (validator-only file): registering at init
// would export threshold=0 on every other node, which reads as "jail at 0s".
var (
	HLNodeJailingThresholdSeconds *prometheus.GaugeVec
	HLNodeJailingDryRun           *prometheus.GaugeVec

	jailingInstrumentsOnce sync.Once
)

// InitJailingConfigInstruments registers the jailing-config pair; see above.
func InitJailingConfigInstruments() {
	jailingInstrumentsOnce.Do(func() {
		HLNodeJailingThresholdSeconds = promauto.NewGaugeVec(prometheus.GaugeOpts{
			Name: "hl_node_jailing_threshold_seconds",
			Help: "latency_ema_jail_threshold from heartbeat_jailing_config.json: the heartbeat-ack EMA above which this node votes to jail a peer. Compare against hl_consensus_validator_latency_ema_seconds for per-peer headroom.",
		}, []string{})
		HLNodeJailingDryRun = promauto.NewGaugeVec(prometheus.GaugeOpts{
			Name: "hl_node_jailing_dry_run",
			Help: "1 if heartbeat_jailing_config.json has dry_run=true (jail decisions are logged, not enforced), 0 if enforcement is live.",
		}, []string{})
	})
}

// Per-block consensus round advance from validated replica records.
var (
	HLCoreRoundAdvance = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "hl_core_round_advance",
		Help:    "Distribution of positive round - parent_round values in accepted replica block records.",
		Buckets: []float64{1, 2, 3, 4, 5, 10, 25, 50, 100},
	})
)

// Extra per-validator facts from the validatorSummaries poll. Vectors carry
// no series until populated, so they are safe to register unconditionally.
var (
	validatorInfoLabels = []string{"validator", "signer", "name"}

	HLConsensusValidatorRecentBlocks = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hl_consensus_validator_recent_blocks",
		Help: "Raw nRecentBlocks field from the latest complete validatorSummaries response; the API window is upstream-defined and zero has no inferred cause.",
	}, validatorInfoLabels)
	HLConsensusValidatorCommissionRate = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hl_consensus_validator_commission_rate",
		Help: "Validator commission as a fraction (0.04 = 4%).",
	}, validatorInfoLabels)
	HLConsensusValidatorUnjailableAfter = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hl_consensus_validator_unjailable_after_seconds",
		Help: "Positive unjailableAfter Unix timestamp (ms epoch normalized to seconds) reported for a jailed validator; zero/null sentinels are omitted. The countdown is (value - time()).",
	}, validatorInfoLabels)
	HLConsensusValidatorUptimeFraction = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hl_consensus_validator_uptime_fraction",
		Help: "uptimeFraction from validatorSummaries stats, per period (day/week/month).",
	}, []string{"validator", "signer", "name", "period"})
	HLConsensusValidatorPredictedApr = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hl_consensus_validator_predicted_apr",
		Help: "predictedApr from validatorSummaries stats, per period (day/week/month), as a fraction.",
	}, []string{"validator", "signer", "name", "period"})
)

// Tokio scheduling-pressure counters previously decoded and dropped.
// Same cumulative-since-source-start gauge semantics as the rest of the
// hl_tokio_task_* family.
var (
	HLTokioTaskScheduledTotal = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hl_tokio_task_scheduled_total",
		Help: "Cumulative times the task was scheduled (woken) since the source process started.",
	}, []string{"task"})
	HLTokioTaskScheduledSecondsTotal = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hl_tokio_task_scheduled_seconds_total",
		Help: "Cumulative time the task spent waiting scheduled-but-not-polled since source start. Rising faster than poll time = runtime starvation.",
	}, []string{"task"})
	HLTokioTaskFastPollsTotal = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hl_tokio_task_fast_polls_total",
		Help: "Cumulative fast polls since source start.",
	}, []string{"task"})
	HLTokioTaskShortDelaysTotal = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hl_tokio_task_short_delays_total",
		Help: "Cumulative short scheduling delays since source start (complements long_delays).",
	}, []string{"task"})
)

// Whether a crit location is suppressed by the operator's
// crit_msg_ignore.json. Alert rules should exclude ignored locations.
var HLNodeCritLocationIgnored = promauto.NewGaugeVec(prometheus.GaugeOpts{
	Name: "hl_node_crit_location_ignored",
	Help: "1 if hl-node marks this crit location is_ignored (operator-suppressed via crit_msg_ignore.json), else 0.",
}, []string{"file", "line"})

// Total TCP connection count across non-validator gossip peers (a peer can
// hold several connections; hl_p2p_non_val_peers_total counts peers).
var HLP2PNonValConnections = promauto.NewGauge(prometheus.GaugeOpts{
	Name: "hl_p2p_non_val_connections",
	Help: "Deprecated alias: sum of source connection_count across explicit children in the latest fresh child_peers status snapshot; this is not a peer count.",
})

// Lifetime mean latency per subsystem (total_mean in the summary rows) as
// a drift baseline for the windowed mean.
var HLNodeSubsystemLatencyLifetimeMean = promauto.NewGaugeVec(prometheus.GaugeOpts{
	Name: "hl_node_subsystem_latency_lifetime_mean_seconds",
	Help: "Source-reported total_mean latency for the subsystem's current process generation.",
}, subsystemLabels)

// Deprecated projection of retained non-empty files in the newest source-date
// directory. The file schema and any offender/event meaning are unproven.
var HLNodeRateLimitedFiles = promauto.NewGaugeVec(prometheus.GaugeOpts{
	Name: "hl_node_rate_limited_files",
	Help: "Deprecated alias: non-empty regular files retained in the lexicographically newest rate_limited_ips date directory per fixed stream; evidence is not active rate limiting or an offender count.",
}, []string{"stream"})

// Compatibility projections from retained data/visor_child_stderr artifacts.
// Empty, material, unreadable, truncated, and unknown evidence remain distinct
// in the replacement families; retained counts are prune-aware snapshots.
var (
	HLNodeChildStarts = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "hl_node_child_starts",
		Help: "Compatibility projection of artifacts retained in visor_child_stderr; includes readable empty start receipts and material artifacts, is prune-aware, and must not be rated.",
	})
	HLNodeChildCrashes = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hl_node_child_crashes",
		Help: "Compatibility projection of retained material child-stderr artifacts by bounded evidence reason; a reason is not a guaranteed crash or upgrade cause.",
	}, []string{"reason"})
	HLNodeChildLastCrashSeconds = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hl_node_child_last_crash_seconds",
		Help: "Compatibility timestamp of the newest retained material child-stderr artifact per bounded evidence reason.",
	}, []string{"reason"})
)

// Freshness of four fixed opt-in hl-node streams. Their payload semantics and
// producer cadence remain outside this metric contract.
var HLNodeStreamAgeSeconds = promauto.NewGaugeVec(prometheus.GaugeOpts{
	Name: "hl_node_stream_age_seconds",
	Help: "Wall-clock age of the latest valid committed record observed in each fixed opt-in stream; no writer cadence, consumer, or stall is inferred.",
}, []string{"stream"})
