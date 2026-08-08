package metrics

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// Bounded source-specific state that cannot be represented by the common
// source registry alone (which intentionally has one fixed label per source).
var (
	HLConsensusValidatorLatencyPollMax = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hl_consensus_validator_latency_poll_max_seconds",
		Help: "Largest complete raw latency sample observed for the signer during the latest exporter poll; withdrawn with the raw-latency snapshot.",
	}, []string{"signer"})

	HLCriticalMessageSampleTimestamp = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hl_node_critical_message_sample_timestamp_seconds",
		Help: "Source timestamp carried by the latest complete daily critical-message record.",
	}, []string{"source"})
	HLCriticalMessageProjectionAvailable = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hl_node_critical_message_projection_available",
		Help: "Whether the fixed critical-message projection is currently present, fresh, readable, valid, and generation-compatible (1=yes, 0=no).",
	}, []string{"source", "projection"})
	HLCriticalMessageProjectionParseOK = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hl_node_critical_message_projection_parse_ok",
		Help: "Latest parse state for the fixed critical-message projection (1=valid, 0=invalid, -1=not parsed because absent or unreadable).",
	}, []string{"source", "projection"})
	HLCriticalMessageGenerationMatch = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hl_node_critical_message_generation_match",
		Help: "Whether the latest valid rich projection exactly matches the committed daily generation by source, base time, counters, and location count.",
	}, []string{"source"})

	HLRocksDBSourcePresent = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hl_rocksdb_source_present",
		Help: "Whether the fixed RocksDB directory and LOG file are present in the latest poll (1=yes, 0=no).",
	}, []string{"db"})
	HLRocksDBStatsParseOK = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hl_rocksdb_stats_parse_ok",
		Help: "Latest complete-stats parse state for the fixed RocksDB (1=valid, 0=invalid, -1=not parsed because absent or unreadable).",
	}, []string{"db"})
	HLRocksDBStatsLastValidTimestamp = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hl_rocksdb_stats_last_valid_observation_timestamp_seconds",
		Help: "Exporter wall-clock timestamp of the most recent complete RocksDB snapshot publication.",
	}, []string{"db"})
	HLRocksDBStatsLastValidAge = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hl_rocksdb_stats_last_valid_age_seconds",
		Help: "Seconds since the exporter most recently published a complete RocksDB snapshot; absent before first success.",
	}, []string{"db"})
)

func SetCriticalMessageProjectionState(source, projection string, available bool, parseState int) {
	if available {
		HLCriticalMessageProjectionAvailable.WithLabelValues(source, projection).Set(1)
	} else {
		HLCriticalMessageProjectionAvailable.WithLabelValues(source, projection).Set(0)
	}
	HLCriticalMessageProjectionParseOK.WithLabelValues(source, projection).Set(float64(parseState))
}

func SetRocksDBLastValidAge(db string, lastValid, now time.Time) {
	if lastValid.IsZero() {
		HLRocksDBStatsLastValidAge.DeleteLabelValues(db)
		return
	}
	age := now.Sub(lastValid)
	if age < 0 {
		age = 0
	}
	HLRocksDBStatsLastValidAge.WithLabelValues(db).Set(age.Seconds())
}
