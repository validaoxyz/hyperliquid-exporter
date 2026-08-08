package metrics

import (
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// SourceID is a compile-time bounded input identity. Never derive one from a
// path, peer, payload, or upstream error string: every value becomes a
// Prometheus label.
type SourceID string

const (
	SourceBlock                 SourceID = "block"
	SourceBlockFast             SourceID = "block_fast"
	SourceBlockSlow             SourceID = "block_slow"
	SourceBlockLegacy           SourceID = "block_legacy"
	SourceProposal              SourceID = "proposal"
	SourceValidatorAPI          SourceID = "validator_api"
	SourceValidatorStatus       SourceID = "validator_status"
	SourceValidatorIP           SourceID = "validator_ip"
	SourceVersion               SourceID = "version"
	SourceUpdate                SourceID = "update"
	SourceEVM                   SourceID = "evm"
	SourceConsensus             SourceID = "consensus"
	SourceConsensusStatus       SourceID = "consensus_status"
	SourceConsensusRoundAdvance SourceID = "consensus_round_advance"
	SourceConsensusLocalStatus  SourceID = "consensus_local_status"
	SourceConsensusExecution    SourceID = "consensus_execution_state"
	SourceConsensusRPC          SourceID = "consensus_rpc"
	SourceValidatorConnections  SourceID = "validator_connections"
	SourceValidatorLatency      SourceID = "validator_latency"
	SourceValidatorLatencyEMA   SourceID = "validator_latency_ema"
	SourceGossip                SourceID = "gossip"
	SourceVisor                 SourceID = "visor"
	SourceSubsystemLatency      SourceID = "subsystem_latency"
	SourceCriticalMessages      SourceID = "critical_messages"
	SourceTCPTraffic            SourceID = "tcp_traffic"
	SourceTCPLZ4                SourceID = "tcp_lz4"
	SourceGossipConnections     SourceID = "gossip_connections"
	SourceGossipChildren        SourceID = "gossip_children"
	SourceDisk                  SourceID = "disk"
	SourceProcess               SourceID = "process"
	SourceChildStderr           SourceID = "child_stderr"
	SourceSnapshotStatus        SourceID = "snapshot_status"
	SourceNodeState             SourceID = "node_state"
	SourceInfoMeta              SourceID = "info_meta"
	SourceInfoExchange          SourceID = "info_exchange"
	SourceLogLines              SourceID = "log_lines"
	SourcePublicIP              SourceID = "public_ip"
	SourceTokioRuntime          SourceID = "tokio_runtime"
	SourceOperatorConfig        SourceID = "operator_config"
	SourceTmpDir                SourceID = "tmp_dir"
	SourceTCPConnections        SourceID = "tcp_connections"
	SourceReplica               SourceID = "replica"
	SourceReplicaRuns           SourceID = "replica_runs"
	SourceReplay                SourceID = "replay"
	SourceRocksDB               SourceID = "rocksdb"
	SourceSubsystemSteps        SourceID = "subsystem_steps"
	SourceCriticalLocations     SourceID = "critical_locations"
	SourceAccumulatorConsensus  SourceID = "accumulator_consensus"
	SourceMempool               SourceID = "mempool"
	SourceMempoolSizeStats      SourceID = "mempool_size_stats"
	SourceMempoolTxs            SourceID = "mempool_txs"
	SourceOptionalFills         SourceID = "optional_fills"
	SourceOptionalTWAP          SourceID = "optional_twap"
	SourceOptionalMisc          SourceID = "optional_misc"
	SourceOptionalEvents        SourceID = "optional_events"
	SourceRateLimitABCI         SourceID = "rate_limit_abci_stream"
	SourceRateLimitBlocks       SourceID = "rate_limit_gossip_blocks"
	SourceRateLimitRequests     SourceID = "rate_limit_gossip_requests"
)

var sourceIDs = []SourceID{
	SourceBlock, SourceBlockFast, SourceBlockSlow, SourceBlockLegacy,
	SourceProposal, SourceValidatorAPI, SourceValidatorStatus,
	SourceValidatorIP, SourceVersion, SourceUpdate, SourceEVM, SourceConsensus,
	SourceConsensusStatus, SourceConsensusRoundAdvance, SourceConsensusLocalStatus,
	SourceConsensusExecution, SourceConsensusRPC, SourceValidatorConnections,
	SourceValidatorLatency, SourceValidatorLatencyEMA,
	SourceGossip, SourceVisor, SourceSubsystemLatency, SourceCriticalMessages,
	SourceTCPTraffic, SourceTCPLZ4, SourceGossipConnections, SourceGossipChildren,
	SourceDisk, SourceProcess, SourceChildStderr, SourceSnapshotStatus,
	SourceNodeState, SourceInfoMeta, SourceInfoExchange, SourceLogLines,
	SourcePublicIP, SourceTokioRuntime, SourceOperatorConfig, SourceTmpDir,
	SourceTCPConnections, SourceReplica, SourceReplicaRuns, SourceReplay,
	SourceRocksDB, SourceSubsystemSteps, SourceCriticalLocations,
	SourceAccumulatorConsensus, SourceMempool, SourceMempoolSizeStats,
	SourceMempoolTxs, SourceOptionalFills, SourceOptionalTWAP, SourceOptionalMisc,
	SourceOptionalEvents, SourceRateLimitABCI, SourceRateLimitBlocks,
	SourceRateLimitRequests,
}

// SourceFailureStage is a finite failure vocabulary. Payloads and error text
// belong in logs, never in the stage label.
type SourceFailureStage string

const (
	SourceFailureDiscovery SourceFailureStage = "discovery"
	SourceFailureStat      SourceFailureStage = "stat"
	SourceFailureOpen      SourceFailureStage = "open"
	SourceFailureRead      SourceFailureStage = "read"
	SourceFailureDecode    SourceFailureStage = "decode"
	SourceFailureSchema    SourceFailureStage = "schema"
	SourceFailureWalk      SourceFailureStage = "walk"
	SourceFailureRequest   SourceFailureStage = "request"
	SourceFailureStatus    SourceFailureStage = "status"
	SourceFailurePublish   SourceFailureStage = "publish"
)

var sourceFailureStages = map[SourceFailureStage]struct{}{
	SourceFailureDiscovery: {}, SourceFailureStat: {}, SourceFailureOpen: {},
	SourceFailureRead: {}, SourceFailureDecode: {}, SourceFailureSchema: {},
	SourceFailureWalk: {}, SourceFailureRequest: {}, SourceFailureStatus: {},
	SourceFailurePublish: {},
}

const sourceStateUnknown int64 = -1

type sourceState struct {
	mu                  sync.RWMutex
	registered          int64
	enabled             int64
	present             int64
	readOK              int64
	schemaOK            int64
	everObserved        int64
	lastAttemptUnix     int64
	lastValidUnix       int64
	lastPublicationUnix int64
	sourceTimeUnix      int64
}

var (
	sourcesMu sync.RWMutex
	sources   = newSourceStateMap()

	HLExporterSourceEnabled = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hl_exporter_source_enabled",
		Help: "Whether collection for this fixed source is enabled by configuration (1=yes, 0=no).",
	}, []string{"source"})
	HLExporterSourcePresent = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hl_exporter_source_present",
		Help: "Latest confirmed presence state for this fixed source (1=present, 0=absent, -1=unknown/not checked).",
	}, []string{"source"})
	HLExporterSourceReadOK = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hl_exporter_source_read_ok",
		Help: "Latest read outcome for this fixed source (1=success, 0=failure, -1=not attempted/not applicable).",
	}, []string{"source"})
	HLExporterSourceSchemaOK = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hl_exporter_source_schema_ok",
		Help: "Latest schema/decode outcome for this fixed source (1=valid, 0=invalid, -1=not attempted/not applicable).",
	}, []string{"source"})
	HLExporterSourceEverObserved = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hl_exporter_source_ever_observed",
		Help: "Whether this exporter process has received at least one complete valid observation from the source (1=yes, 0=no).",
	}, []string{"source"})
	HLExporterSourceLastAttemptSeconds = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hl_exporter_source_last_attempt_seconds",
		Help: "Exporter wall-clock Unix timestamp of the most recent attempt to inspect this source; absent before the first attempt.",
	}, []string{"source"})
	HLExporterSourceLastValidSeconds = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hl_exporter_source_last_valid_observation_seconds",
		Help: "Exporter wall-clock Unix timestamp at which the most recent complete valid observation was received; absent before the first valid observation.",
	}, []string{"source"})
	HLExporterSourceLastPublicationSeconds = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hl_exporter_source_last_publication_seconds",
		Help: "Exporter wall-clock Unix timestamp of the most recent successful metric publication from this source; absent before first publication.",
	}, []string{"source"})
	HLExporterSourceTimestampSeconds = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hl_exporter_source_timestamp_seconds",
		Help: "Timestamp carried by the most recent complete valid source record when available; never used to compute exporter receipt freshness.",
	}, []string{"source"})
	HLExporterSourceLastValidAgeSeconds = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hl_exporter_source_last_valid_age_seconds",
		Help: "Seconds since exporter receipt of the most recent complete valid observation; absent before first observation and unaffected by future-dated source timestamps.",
	}, []string{"source"})
	HLExporterSourceErrorsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "hl_exporter_source_errors_total",
		Help: "Source failures since exporter start, partitioned by a fixed processing stage.",
	}, []string{"source", "stage"})
	HLExporterSourceInvalidUpdatesTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "hl_exporter_source_invalid_updates_total",
		Help: "Rejected source-state API updates with a non-allowlisted source or failure stage.",
	}, []string{"kind"})
)

func newSourceStateMap() map[SourceID]*sourceState {
	out := make(map[SourceID]*sourceState, len(sourceIDs))
	for _, id := range sourceIDs {
		out[id] = &sourceState{
			present:  sourceStateUnknown,
			readOK:   sourceStateUnknown,
			schemaOK: sourceStateUnknown,
		}
	}
	return out
}

func getSource(id SourceID) *sourceState {
	sourcesMu.RLock()
	state := sources[id]
	sourcesMu.RUnlock()
	if state == nil {
		HLExporterSourceInvalidUpdatesTotal.WithLabelValues("source").Inc()
	}
	return state
}

// RegisterSource activates one allowlisted source identity for exposition.
func RegisterSource(id SourceID, enabled bool) bool {
	state := getSource(id)
	if state == nil {
		return false
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	atomic.StoreInt64(&state.registered, 1)
	if enabled {
		atomic.StoreInt64(&state.enabled, 1)
	} else {
		atomic.StoreInt64(&state.enabled, 0)
		atomic.StoreInt64(&state.present, sourceStateUnknown)
		atomic.StoreInt64(&state.readOK, sourceStateUnknown)
		atomic.StoreInt64(&state.schemaOK, sourceStateUnknown)
	}
	return true
}

func MarkSourceAttempt(id SourceID) bool {
	return markSourceAttemptAt(id, time.Now().Unix())
}

func markSourceAttemptAt(id SourceID, now int64) bool {
	state := getSource(id)
	if state == nil {
		return false
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	atomic.StoreInt64(&state.lastAttemptUnix, now)
	return true
}

// MarkSourceAbsent records a confirmed absence. It does not erase the last
// valid receipt/publication timestamps; those remain useful outage context.
func MarkSourceAbsent(id SourceID) bool {
	state := getSource(id)
	if state == nil {
		return false
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	atomic.StoreInt64(&state.lastAttemptUnix, time.Now().Unix())
	atomic.StoreInt64(&state.present, 0)
	atomic.StoreInt64(&state.readOK, sourceStateUnknown)
	atomic.StoreInt64(&state.schemaOK, sourceStateUnknown)
	return true
}

// MarkSourceAvailable records a successful presence/read/schema check that
// yielded no data record (for example, a healthy empty snapshot directory).
func MarkSourceAvailable(id SourceID) bool {
	state := getSource(id)
	if state == nil {
		return false
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	atomic.StoreInt64(&state.lastAttemptUnix, time.Now().Unix())
	atomic.StoreInt64(&state.present, 1)
	atomic.StoreInt64(&state.readOK, 1)
	atomic.StoreInt64(&state.schemaOK, 1)
	return true
}

func MarkSourceReadOutcome(id SourceID, ok bool) bool {
	state := getSource(id)
	if state == nil {
		return false
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	atomic.StoreInt64(&state.lastAttemptUnix, time.Now().Unix())
	if ok {
		atomic.StoreInt64(&state.present, 1)
		atomic.StoreInt64(&state.readOK, 1)
		return true
	}
	atomic.StoreInt64(&state.readOK, 0)
	atomic.StoreInt64(&state.schemaOK, sourceStateUnknown)
	return true
}

func MarkSourceSchemaOutcome(id SourceID, ok bool) bool {
	state := getSource(id)
	if state == nil {
		return false
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	atomic.StoreInt64(&state.lastAttemptUnix, time.Now().Unix())
	atomic.StoreInt64(&state.present, 1)
	atomic.StoreInt64(&state.readOK, 1)
	if ok {
		atomic.StoreInt64(&state.schemaOK, 1)
	} else {
		atomic.StoreInt64(&state.schemaOK, 0)
	}
	return true
}

// MarkSourceValidObservation records both exporter receipt time and an
// optional timestamp carried by the source. Freshness is always computed from
// receipt time so a future-dated source record cannot look perpetually fresh.
func MarkSourceValidObservation(id SourceID, sourceTime time.Time) bool {
	return markSourceValidObservationAt(id, sourceTime, time.Now())
}

func markSourceValidObservationAt(id SourceID, sourceTime, observedAt time.Time) bool {
	state := getSource(id)
	if state == nil {
		return false
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	now := observedAt.Unix()
	atomic.StoreInt64(&state.lastAttemptUnix, now)
	atomic.StoreInt64(&state.lastValidUnix, now)
	atomic.StoreInt64(&state.everObserved, 1)
	atomic.StoreInt64(&state.present, 1)
	atomic.StoreInt64(&state.readOK, 1)
	atomic.StoreInt64(&state.schemaOK, 1)
	if !sourceTime.IsZero() {
		atomic.StoreInt64(&state.sourceTimeUnix, sourceTime.Unix())
	}
	return true
}

func MarkSourcePublication(id SourceID) bool {
	state := getSource(id)
	if state == nil {
		return false
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	atomic.StoreInt64(&state.lastPublicationUnix, time.Now().Unix())
	return true
}

func MarkSourceError(id SourceID, stage SourceFailureStage) bool {
	state := getSource(id)
	if state == nil {
		return false
	}
	if _, ok := sourceFailureStages[stage]; !ok {
		HLExporterSourceInvalidUpdatesTotal.WithLabelValues("stage").Inc()
		return false
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	atomic.StoreInt64(&state.lastAttemptUnix, time.Now().Unix())
	switch stage {
	case SourceFailureDiscovery, SourceFailureStat, SourceFailureOpen, SourceFailureRead, SourceFailureWalk, SourceFailureRequest, SourceFailureStatus:
		atomic.StoreInt64(&state.readOK, 0)
		atomic.StoreInt64(&state.schemaOK, sourceStateUnknown)
	case SourceFailureDecode, SourceFailureSchema:
		atomic.StoreInt64(&state.readOK, 1)
		atomic.StoreInt64(&state.schemaOK, 0)
	}
	HLExporterSourceErrorsTotal.WithLabelValues(string(id), string(stage)).Inc()
	return true
}

type SourceSnapshot struct {
	ID                  SourceID
	Registered          bool
	Enabled             bool
	Present             int64
	ReadOK              int64
	SchemaOK            int64
	EverObserved        bool
	LastAttemptUnix     int64
	LastValidUnix       int64
	LastPublicationUnix int64
	SourceTimeUnix      int64
}

func snapshotSources() []SourceSnapshot {
	sourcesMu.RLock()
	defer sourcesMu.RUnlock()
	out := make([]SourceSnapshot, 0, len(sources))
	for id, state := range sources {
		state.mu.RLock()
		if atomic.LoadInt64(&state.registered) == 0 {
			state.mu.RUnlock()
			continue
		}
		snapshot := SourceSnapshot{
			ID:                  id,
			Registered:          true,
			Enabled:             atomic.LoadInt64(&state.enabled) == 1,
			Present:             atomic.LoadInt64(&state.present),
			ReadOK:              atomic.LoadInt64(&state.readOK),
			SchemaOK:            atomic.LoadInt64(&state.schemaOK),
			EverObserved:        atomic.LoadInt64(&state.everObserved) == 1,
			LastAttemptUnix:     atomic.LoadInt64(&state.lastAttemptUnix),
			LastValidUnix:       atomic.LoadInt64(&state.lastValidUnix),
			LastPublicationUnix: atomic.LoadInt64(&state.lastPublicationUnix),
			SourceTimeUnix:      atomic.LoadInt64(&state.sourceTimeUnix),
		}
		state.mu.RUnlock()
		out = append(out, snapshot)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func publishSourceHealthAt(now int64) {
	for _, state := range snapshotSources() {
		id := string(state.ID)
		if state.Enabled {
			HLExporterSourceEnabled.WithLabelValues(id).Set(1)
		} else {
			HLExporterSourceEnabled.WithLabelValues(id).Set(0)
		}
		HLExporterSourcePresent.WithLabelValues(id).Set(float64(state.Present))
		HLExporterSourceReadOK.WithLabelValues(id).Set(float64(state.ReadOK))
		HLExporterSourceSchemaOK.WithLabelValues(id).Set(float64(state.SchemaOK))
		if state.EverObserved {
			HLExporterSourceEverObserved.WithLabelValues(id).Set(1)
		} else {
			HLExporterSourceEverObserved.WithLabelValues(id).Set(0)
		}
		setOptionalSourceTimestamp(HLExporterSourceLastAttemptSeconds, id, state.LastAttemptUnix)
		setOptionalSourceTimestamp(HLExporterSourceLastValidSeconds, id, state.LastValidUnix)
		setOptionalSourceTimestamp(HLExporterSourceLastPublicationSeconds, id, state.LastPublicationUnix)
		setOptionalSourceTimestamp(HLExporterSourceTimestampSeconds, id, state.SourceTimeUnix)
		if state.LastValidUnix > 0 {
			age := now - state.LastValidUnix
			if age < 0 {
				age = 0
			}
			HLExporterSourceLastValidAgeSeconds.WithLabelValues(id).Set(float64(age))
		} else {
			HLExporterSourceLastValidAgeSeconds.DeleteLabelValues(id)
		}
	}
}

func setOptionalSourceTimestamp(metric *prometheus.GaugeVec, id string, value int64) {
	if value > 0 {
		metric.WithLabelValues(id).Set(float64(value))
	} else {
		metric.DeleteLabelValues(id)
	}
}
