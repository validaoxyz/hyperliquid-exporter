package metrics

import (
	"sync"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

const (
	InfoProbeOutcomeBuild   = "build"
	InfoProbeOutcomeRequest = "request"
	InfoProbeOutcomeStatus  = "status"
	InfoProbeOutcomeEmpty   = "empty"
	InfoProbeOutcomeDecode  = "decode"
	InfoProbeOutcomeSuccess = "success"
)

var (
	HLInfoMetaLastSuccessAgeSeconds    prometheus.Gauge
	HLInfoMetaOutcomesTotal            *prometheus.CounterVec
	HLInfoExchangeStatusUp             prometheus.Gauge
	HLInfoExchangeStatusLastSuccess    prometheus.Gauge
	HLInfoExchangeStatusLastSuccessAge prometheus.Gauge
	HLInfoExchangeStatusOutcomesTotal  *prometheus.CounterVec
	infoProbeStatusInstrumentsOnce     sync.Once
)

// InitInfoProbeStatusInstruments registers the independent meta and
// exchangeStatus state only when the opt-in info probe is enabled.
func InitInfoProbeStatusInstruments() {
	infoProbeStatusInstrumentsOnce.Do(func() {
		HLInfoMetaLastSuccessAgeSeconds = promauto.NewGauge(prometheus.GaugeOpts{
			Name: "hl_info_meta_last_success_age_seconds",
			Help: "Seconds since the last successful meta subprobe; 0 before the first success is qualified by hl_exporter_source_ever_observed.",
		})
		HLInfoMetaOutcomesTotal = promauto.NewCounterVec(prometheus.CounterOpts{
			Name: "hl_info_meta_outcomes_total",
			Help: "Meta subprobe outcomes since exporter start from a fixed vocabulary.",
		}, []string{"outcome"})
		HLInfoExchangeStatusUp = promauto.NewGauge(prometheus.GaugeOpts{
			Name: "hl_info_exchange_status_up",
			Help: "Whether the most recent independent exchangeStatus subprobe returned a valid timestamp (1=yes, 0=no).",
		})
		HLInfoExchangeStatusLastSuccess = promauto.NewGauge(prometheus.GaugeOpts{
			Name: "hl_info_exchange_status_last_success_seconds",
			Help: "Unix timestamp of the last successful exchangeStatus subprobe.",
		})
		HLInfoExchangeStatusLastSuccessAge = promauto.NewGauge(prometheus.GaugeOpts{
			Name: "hl_info_exchange_status_last_success_age_seconds",
			Help: "Seconds since the last successful exchangeStatus subprobe; retained and advanced through failures.",
		})
		HLInfoExchangeStatusOutcomesTotal = promauto.NewCounterVec(prometheus.CounterOpts{
			Name: "hl_info_exchange_status_outcomes_total",
			Help: "ExchangeStatus subprobe outcomes since exporter start from a fixed vocabulary.",
		}, []string{"outcome"})

		outcomes := []string{
			InfoProbeOutcomeBuild,
			InfoProbeOutcomeRequest,
			InfoProbeOutcomeStatus,
			InfoProbeOutcomeEmpty,
			InfoProbeOutcomeDecode,
			InfoProbeOutcomeSuccess,
		}
		for _, outcome := range outcomes {
			HLInfoMetaOutcomesTotal.WithLabelValues(outcome)
			HLInfoExchangeStatusOutcomesTotal.WithLabelValues(outcome)
		}
	})
}
