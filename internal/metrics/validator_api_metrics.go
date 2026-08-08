package metrics

import (
	"sync/atomic"
	"time"

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
	validatorAPILastSuccessUnix atomic.Int64

	HLValidatorAPIUp = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "hl_validator_api_up",
		Help: "Whether the most recent validatorSummaries refresh produced a complete valid snapshot (1=yes, 0=no). Fresh-cache reads do not change this state.",
	})
	HLValidatorAPICacheStale = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "hl_validator_api_cache_stale",
		Help: "Whether the most recent validatorSummaries result was a stale cache fallback after a failed refresh (1=yes, 0=no).",
	})
	HLValidatorAPILastSuccessSeconds = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hl_validator_api_last_success_seconds",
		Help: "Unix timestamp of the most recent complete valid validatorSummaries refresh; absent until the first successful refresh and never advanced by cache fallback.",
	}, []string{})
	HLValidatorAPICacheAgeSeconds = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hl_validator_api_cache_age_seconds",
		Help: "Wall-clock age in seconds of the last successful validatorSummaries network refresh; absent until the first successful refresh.",
	}, []string{})
	HLValidatorAPIOutcomesTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "hl_validator_api_outcomes_total",
		Help: "Validator-summary resolver outcomes since exporter start from a fixed vocabulary.",
	}, []string{"outcome"})
	HLValidatorAPIUnknownPeriodsTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "hl_validator_api_unknown_periods_total",
		Help: "Validator-summary stat rows dropped because period was not one of day, week, or month.",
	})
)

// SetValidatorAPILastSuccess records one successful network refresh. Cache
// reads and stale fallbacks must not advance this clock.
func SetValidatorAPILastSuccess(at time.Time) {
	if at.IsZero() {
		return
	}
	validatorAPILastSuccessUnix.Store(at.Unix())
	HLValidatorAPILastSuccessSeconds.WithLabelValues().Set(float64(at.Unix()))
	publishValidatorAPICacheAgeAt(time.Now())
}

func publishValidatorAPICacheAgeAt(now time.Time) {
	lastSuccess := validatorAPILastSuccessUnix.Load()
	if lastSuccess <= 0 {
		HLValidatorAPILastSuccessSeconds.DeleteLabelValues()
		HLValidatorAPICacheAgeSeconds.DeleteLabelValues()
		return
	}
	age := now.Unix() - lastSuccess
	if age < 0 {
		age = 0
	}
	HLValidatorAPICacheAgeSeconds.WithLabelValues().Set(float64(age))
}

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
