package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	HLMempoolParserEventsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "hl_mempool_parser_events_total",
		Help: "Mempool records requiring bounded parser/taxonomy handling; reasons are fixed and never contain payload text.",
	}, []string{"reason"})
	HLMempoolStructuredErrorsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "hl_mempool_structured_errors_total",
		Help: "Structured add/verify failures by fixed operation and allowlisted top-level error kind; nested payloads are ignored.",
	}, []string{"operation", "error_kind"})
	HLMempoolPruneEventsTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "hl_mempool_rpc_prune_events_total",
		Help: "Valid Pruned rpc request throttle records observed since exporter start.",
	})
	HLMempoolPrunedItemsTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "hl_mempool_rpc_requests_pruned_total",
		Help: "RPC-request entries pruned since exporter start, summed from the validated third payload element.",
	})
	HLMempoolDroppedItemsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "hl_mempool_dropped_items_total",
		Help: "Items in validated mempool drop batches since exporter start (kind=blocks or transactions).",
	}, []string{"kind"})
	HLMempoolOldestUncommittedAgeSeconds = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hl_mempool_oldest_uncommitted_age_seconds",
		Help: "Age of the oldest current uncommitted transaction at the latest complete Size stats snapshot; absent before the first valid snapshot.",
	}, []string{})
	HLMempoolTxsParserEventsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "hl_mempool_txs_parser_events_total",
		Help: "Split-client mempool records or fields requiring bounded parser/taxonomy handling; reasons never contain input text.",
	}, []string{"reason"})
)
