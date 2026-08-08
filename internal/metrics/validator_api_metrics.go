package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

const (
	ValidatorAPIOutcomeRefreshSuccess = "refresh_success"
	ValidatorAPIOutcomeFreshCache     = "fresh_cache"
	ValidatorAPIOutcomeStaleFallback  = "stale_fallback"
	ValidatorAPIOutcomeError          = "error"
)

var (
	HLValidatorAPIUp = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "hl_validator_api_up",
		Help: "Whether the most recent validatorSummaries refresh produced a complete valid snapshot (1=yes, 0=no). Fresh-cache reads do not change this state.",
	})
	HLValidatorAPICacheStale = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "hl_validator_api_cache_stale",
		Help: "Whether the most recent validatorSummaries result was a stale cache fallback after a failed refresh (1=yes, 0=no).",
	})
	HLValidatorAPILastSuccessSeconds = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "hl_validator_api_last_success_seconds",
		Help: "Unix timestamp of the most recent complete valid validatorSummaries refresh; never advanced by cache fallback.",
	})
	HLValidatorAPICacheAgeSeconds = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "hl_validator_api_cache_age_seconds",
		Help: "Wall-clock age in seconds of the last successful validatorSummaries network refresh.",
	})
	HLValidatorAPIOutcomesTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "hl_validator_api_outcomes_total",
		Help: "Validator-summary resolver outcomes since exporter start from a fixed vocabulary.",
	}, []string{"outcome"})
	HLValidatorAPIUnknownPeriodsTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "hl_validator_api_unknown_periods_total",
		Help: "Validator-summary stat rows dropped because period was not one of day, week, or month.",
	})
)

func init() {
	for _, outcome := range []string{
		ValidatorAPIOutcomeRefreshSuccess,
		ValidatorAPIOutcomeFreshCache,
		ValidatorAPIOutcomeStaleFallback,
		ValidatorAPIOutcomeError,
	} {
		HLValidatorAPIOutcomesTotal.WithLabelValues(outcome)
	}
}
