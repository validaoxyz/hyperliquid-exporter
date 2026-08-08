package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// Replica metrics below make the counting unit explicit. They intentionally
// coexist with the older hl_core_* compatibility families for one release;
// the old families are still populated from the same normalized block.
var (
	HLReplicaActionBundlesTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "hl_replica_action_bundles_total",
		Help: "Validated signed-action bundles processed from newline-committed replica_cmds block records since exporter start.",
	})
	HLReplicaSignedActionsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "hl_replica_signed_actions_total",
		Help: "Validated signed actions processed from replica_cmds since exporter start, by closed action type; unknown types are other.",
	}, []string{"action_type"})
	HLReplicaOperationsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "hl_replica_operations_total",
		Help: "Individual operations inside validated replica signed actions since exporter start; an order/cancel/batch-modify array contributes its item count.",
	}, []string{"action_type", "category"})
	HLReplicaOrdersTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "hl_replica_orders_total",
		Help: "Individual order operations inside validated order and twapOrder actions since exporter start; this is not a signed-action count.",
	})
	HLReplicaLastProcessedHeight = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "hl_replica_last_processed_height",
		Help: "Top-level chain height in the latest completely validated and published replica_cmds block record.",
	})
	HLReplicaMultiSigInnerActionsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "hl_replica_multisig_inner_actions_total",
		Help: "Validated outer multiSig actions since exporter start, classified by the inner action's closed operational category; missing or unknown inner actions are other.",
	}, []string{"category"})
	HLReplicaParserEventsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "hl_replica_parser_events_total",
		Help: "Replica records requiring bounded parser or schema-drift handling; stage and reason are fixed and never contain payload text.",
	}, []string{"stage", "reason"})
	HLReplicaResponseRecordsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "hl_replica_response_records_total",
		Help: "Replica response records observed since exporter start, classified as valid or malformed without retaining hashes, accounts, or free text.",
	}, []string{"result"})
	HLReplicaResponseCoverageTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "hl_replica_response_coverage_total",
		Help: "Validated replica block records since exporter start by response payload coverage: available, unavailable, or malformed.",
	}, []string{"result"})
	HLReplicaResponseCountRelationTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "hl_replica_response_count_relation_total",
		Help: "Validated replica block records since exporter start by response-record count relative to signed-action count; no positional association is assumed.",
	}, []string{"result"})
	HLReplicaResponseActionStatusTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "hl_replica_response_action_status_total",
		Help: "Top-level replica action-response statuses since exporter start using the closed ok, err, and other vocabulary.",
	}, []string{"status"})
	HLReplicaExecutionOutcomesTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "hl_replica_execution_outcomes_total",
		Help: "Nested replica execution outcomes since exporter start using a closed fixture-backed vocabulary plus other.",
	}, []string{"outcome"})
)
