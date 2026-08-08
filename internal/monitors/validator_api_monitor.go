package monitors

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/validaoxyz/hyperliquid-exporter/internal/config"
	hyperliquidapi "github.com/validaoxyz/hyperliquid-exporter/internal/hyperliquid-api"
	"github.com/validaoxyz/hyperliquid-exporter/internal/logger"
	"github.com/validaoxyz/hyperliquid-exporter/internal/metrics"
)

var hlResolver *hyperliquidapi.Resolver

const validatorSummaryLimit = 500

func StartValidatorMonitor(ctx context.Context, cfg config.Config, errCh chan<- error) {
	metrics.RegisterSource(metrics.SourceValidatorAPI, true)
	// init HL resolver
	resolver, err := hyperliquidapi.NewResolver(cfg.Chain)
	if err != nil {
		ReportError(ctx, "validator_api", errCh, fmt.Errorf("validator API resolver: %w", err))
		return
	}
	resolver.SetValidatorSummariesValidator(func(summaries []hyperliquidapi.ValidatorSummary) error {
		_, err := validateValidatorSummaries(summaries)
		return err
	})
	hlResolver = resolver

	goSafe("validator_api", func() {
		// run immediately on startup to populate mappings
		if err := updateValidatorMetrics(ctx, cfg); err != nil {
			logger.Error("Initial validator monitor update error: %v", err)
			ReportError(ctx, "validator_api", errCh, err)
		}

		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := updateValidatorMetrics(ctx, cfg); err != nil {
					logger.Error("Validator monitor error: %v", err)
					ReportError(ctx, "validator_api", errCh, err)
				}
			}
		}
	})
}

func updateValidatorMetrics(ctx context.Context, cfg config.Config) error {
	metrics.MarkMonitorAttempt("validator_api")
	metrics.MarkSourceAttempt(metrics.SourceValidatorAPI)
	// use resolver to get val summaries
	result, err := hlResolver.GetValidatorSummaries(ctx, false)
	if err != nil {
		metrics.WithPrometheusSnapshotUpdate(func() {
			metrics.HLValidatorAPIUp.Set(0)
			metrics.HLValidatorAPICacheStale.Set(0)
			metrics.HLValidatorAPIOutcomesTotal.WithLabelValues(metrics.ValidatorAPIOutcomeError).Inc()
			metrics.MarkSourceError(metrics.SourceValidatorAPI, validatorAPIFailureStage(err))
		})
		return err
	}
	if result.Stale {
		metrics.WithPrometheusSnapshotUpdate(func() {
			updateValidatorAPICacheAge(result.LastSuccess)
			metrics.HLValidatorAPIUp.Set(0)
			metrics.HLValidatorAPICacheStale.Set(1)
			metrics.HLValidatorAPIOutcomesTotal.WithLabelValues(metrics.ValidatorAPIOutcomeStaleFallback).Inc()
			metrics.MarkSourceError(metrics.SourceValidatorAPI, validatorAPIFailureStage(result.RefreshError))
		})
		return fmt.Errorf("validator API refresh failed; retained stale cache from %s: %w",
			result.LastSuccess.Format(time.RFC3339), result.RefreshError)
	}

	snapshot, err := validateValidatorSummaries(result.Summaries)
	if err != nil {
		metrics.WithPrometheusSnapshotUpdate(func() {
			metrics.HLValidatorAPICacheStale.Set(0)
			metrics.HLValidatorAPIUp.Set(0)
			metrics.HLValidatorAPIOutcomesTotal.WithLabelValues(metrics.ValidatorAPIOutcomeError).Inc()
			metrics.MarkSourceError(metrics.SourceValidatorAPI, metrics.SourceFailureSchema)
		})
		return err
	}
	if result.FromCache {
		metrics.WithPrometheusSnapshotUpdate(func() {
			metrics.HLValidatorAPICacheStale.Set(0)
			updateValidatorAPICacheAge(result.LastSuccess)
			metrics.HLValidatorAPIOutcomesTotal.WithLabelValues(metrics.ValidatorAPIOutcomeFreshCache).Inc()
		})
		return nil
	}
	commitValidatorAPISnapshot(result.Summaries, snapshot, result.LastSuccess)
	return nil
}

func commitValidatorAPISnapshot(summaries []hyperliquidapi.ValidatorSummary, snapshot []metrics.ValidatorSummarySnapshot, lastSuccess time.Time) {
	metrics.WithPrometheusSnapshotUpdate(func() {
		metrics.HLValidatorAPICacheStale.Set(0)
		updateValidatorAPICacheAge(lastSuccess)
		metrics.HLValidatorAPIUp.Set(1)
		metrics.HLValidatorAPIOutcomesTotal.WithLabelValues(metrics.ValidatorAPIOutcomeRefreshSuccess).Inc()
		if !lastSuccess.IsZero() {
			metrics.HLValidatorAPILastSuccessSeconds.Set(float64(lastSuccess.Unix()))
		}

		for _, summary := range summaries {
			metrics.RegisterFullAddress(strings.ToLower(summary.Validator))
			metrics.RegisterFullAddress(strings.ToLower(summary.Signer))
		}
		// The registry, row gauges, and all row-derived aggregates commit as one
		// generation. Register full identities first so truncated vote/QC
		// observations can reconcile in this same generation.
		metrics.ReplaceValidatorSnapshot(snapshot)
		reconcileValidatorExtras(summaries)
		metrics.MarkSourceValidObservation(metrics.SourceValidatorAPI, time.Time{})
		metrics.MarkSourcePublication(metrics.SourceValidatorAPI)
		metrics.MarkMonitorValidObservation("validator_api")
		metrics.MarkMonitorPublication("validator_api")
	})
}

func updateValidatorAPICacheAge(lastSuccess time.Time) {
	if lastSuccess.IsZero() {
		return
	}
	age := time.Since(lastSuccess).Seconds()
	if age < 0 {
		age = 0
	}
	metrics.HLValidatorAPICacheAgeSeconds.Set(age)
}

func validatorAPIFailureStage(err error) metrics.SourceFailureStage {
	var callErr *hyperliquidapi.CallError
	if !errors.As(err, &callErr) {
		return metrics.SourceFailureRequest
	}
	switch callErr.Stage {
	case hyperliquidapi.FailureStatus:
		return metrics.SourceFailureStatus
	case hyperliquidapi.FailureRead:
		return metrics.SourceFailureRead
	case hyperliquidapi.FailureDecode:
		return metrics.SourceFailureDecode
	case hyperliquidapi.FailureSchema:
		return metrics.SourceFailureSchema
	default:
		return metrics.SourceFailureRequest
	}
}

func validateValidatorSummaries(summaries []hyperliquidapi.ValidatorSummary) ([]metrics.ValidatorSummarySnapshot, error) {
	if summaries == nil {
		return nil, fmt.Errorf("validator summaries response is null")
	}
	if len(summaries) > validatorSummaryLimit {
		return nil, fmt.Errorf("validator summary count %d exceeds limit %d", len(summaries), validatorSummaryLimit)
	}
	validators := make(map[string]struct{}, len(summaries))
	signers := make(map[string]struct{}, len(summaries))
	out := make([]metrics.ValidatorSummarySnapshot, 0, len(summaries))
	for i, summary := range summaries {
		validator := strings.ToLower(strings.TrimSpace(summary.Validator))
		signer := strings.ToLower(strings.TrimSpace(summary.Signer))
		if !isFullHexAddress(validator) || !isFullHexAddress(signer) {
			return nil, fmt.Errorf("validator summary row %d has invalid identity", i)
		}
		if _, duplicate := validators[validator]; duplicate {
			return nil, fmt.Errorf("validator summary row %d duplicates validator", i)
		}
		if _, duplicate := signers[signer]; duplicate {
			return nil, fmt.Errorf("validator summary row %d duplicates signer", i)
		}
		if math.IsNaN(summary.Stake) || math.IsInf(summary.Stake, 0) || summary.Stake < 0 {
			return nil, fmt.Errorf("validator summary row %d has invalid stake", i)
		}
		if summary.UnjailableAfter < 0 || summary.NRecentBlocks < 0 {
			return nil, fmt.Errorf("validator summary row %d has invalid nonnegative field", i)
		}
		if _, err := parseSummaryStatsStrict(summary.Stats); err != nil {
			return nil, fmt.Errorf("validator summary row %d: %w", i, err)
		}
		validators[validator] = struct{}{}
		signers[signer] = struct{}{}
		out = append(out, metrics.ValidatorSummarySnapshot{
			Validator: validator,
			Signer:    signer,
			Name:      summary.Name,
			Stake:     summary.Stake,
			Jailed:    summary.IsJailed,
			Active:    summary.IsActive,
		})
	}
	return out, nil
}

func isFullHexAddress(s string) bool {
	if len(s) != 42 || !strings.HasPrefix(s, "0x") {
		return false
	}
	for _, c := range s[2:] {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}

// returns the HL resolver instance
func GetValidatorResolver() *hyperliquidapi.Resolver {
	return hlResolver
}

// summaryPeriods is the fixed period set validatorSummaries stats carry.
var summaryPeriods = []string{"day", "week", "month"}

var summaryPeriodIndex = map[string]int{"day": 0, "week": 1, "month": 2}

// prevExtraState tracks exactly which optional series were present in the
// last complete validator snapshot. Single-goroutine access from the API
// monitor.
var prevExtraState = map[string]validatorExtraState{}

type validatorExtraState struct {
	labels      [3]string
	commission  bool
	unjailable  bool
	uptime, apr [3]bool
}

// reconcileValidatorExtras publishes the validatorSummaries facts beyond
// stake/status: recent blocks proposed, commission, the unjail timestamp
// while jailed, and per-period uptime/APR. These fields were previously
// decoded and discarded even though unjailableAfter is the number an
// operator actually needs mid-incident.
func reconcileValidatorExtras(summaries []hyperliquidapi.ValidatorSummary) {
	current := make(map[string]validatorExtraState, len(summaries))

	for _, summary := range summaries {
		validator := strings.ToLower(strings.TrimSpace(summary.Validator))
		signer := strings.ToLower(strings.TrimSpace(summary.Signer))
		labels := [3]string{validator, signer, summary.Name}
		state := validatorExtraState{labels: labels}

		metrics.HLConsensusValidatorRecentBlocks.
			WithLabelValues(labels[0], labels[1], labels[2]).Set(float64(summary.NRecentBlocks))

		if v, ok := parseFiniteFloat(summary.Commission); ok {
			metrics.HLConsensusValidatorCommissionRate.
				WithLabelValues(labels[0], labels[1], labels[2]).Set(v)
			state.commission = true
		}

		if summary.IsJailed && summary.UnjailableAfter > 0 {
			state.unjailable = true
			// the API reports milliseconds since epoch
			metrics.HLConsensusValidatorUnjailableAfter.
				WithLabelValues(labels[0], labels[1], labels[2]).Set(float64(summary.UnjailableAfter) / 1000.0)
		}

		for _, ps := range parseSummaryStats(summary.Stats) {
			if ps.hasUptime {
				metrics.HLConsensusValidatorUptimeFraction.
					WithLabelValues(labels[0], labels[1], labels[2], ps.period).Set(ps.uptime)
				state.uptime[summaryPeriodIndex[ps.period]] = true
			}
			if ps.hasApr {
				metrics.HLConsensusValidatorPredictedApr.
					WithLabelValues(labels[0], labels[1], labels[2], ps.period).Set(ps.apr)
				state.apr[summaryPeriodIndex[ps.period]] = true
			}
		}
		current[validator] = state
	}

	for validator, previous := range prevExtraState {
		// drop old series when the validator left OR its label set changed
		// (a renamed moniker would otherwise leak the old-name series)
		cur, exists := current[validator]
		labels := previous.labels
		if !exists || cur.labels != labels {
			metrics.HLConsensusValidatorRecentBlocks.DeleteLabelValues(labels[0], labels[1], labels[2])
			metrics.HLConsensusValidatorCommissionRate.DeleteLabelValues(labels[0], labels[1], labels[2])
			metrics.HLConsensusValidatorUnjailableAfter.DeleteLabelValues(labels[0], labels[1], labels[2])
			for _, period := range summaryPeriods {
				metrics.HLConsensusValidatorUptimeFraction.DeleteLabelValues(labels[0], labels[1], labels[2], period)
				metrics.HLConsensusValidatorPredictedApr.DeleteLabelValues(labels[0], labels[1], labels[2], period)
			}
			continue
		}
		if previous.commission && !cur.commission {
			metrics.HLConsensusValidatorCommissionRate.DeleteLabelValues(labels[0], labels[1], labels[2])
		}
		if previous.unjailable && !cur.unjailable {
			metrics.HLConsensusValidatorUnjailableAfter.DeleteLabelValues(labels[0], labels[1], labels[2])
		}
		for i, period := range summaryPeriods {
			if previous.uptime[i] && !cur.uptime[i] {
				metrics.HLConsensusValidatorUptimeFraction.DeleteLabelValues(labels[0], labels[1], labels[2], period)
			}
			if previous.apr[i] && !cur.apr[i] {
				metrics.HLConsensusValidatorPredictedApr.DeleteLabelValues(labels[0], labels[1], labels[2], period)
			}
		}
	}
	prevExtraState = current
}

type summaryPeriodStat struct {
	period            string
	uptime, apr       float64
	hasUptime, hasApr bool
}

func parseSummaryStats(raw [][]json.RawMessage) []summaryPeriodStat {
	out, err := parseSummaryStatsStrict(raw)
	if err != nil {
		return nil
	}
	for _, pair := range raw {
		if len(pair) != 2 {
			continue
		}
		var period string
		if json.Unmarshal(pair[0], &period) == nil && period != "" {
			if _, ok := summaryPeriodIndex[period]; !ok {
				metrics.HLValidatorAPIUnknownPeriodsTotal.Inc()
			}
		}
	}
	return out
}

func parseSummaryStatsStrict(raw [][]json.RawMessage) ([]summaryPeriodStat, error) {
	out := make([]summaryPeriodStat, 0, len(raw))
	seen := make(map[string]struct{}, len(raw))
	for i, pair := range raw {
		if len(pair) != 2 {
			return nil, fmt.Errorf("stats pair %d has length %d", i, len(pair))
		}
		var period string
		if json.Unmarshal(pair[0], &period) != nil || period == "" {
			return nil, fmt.Errorf("stats pair %d has invalid period", i)
		}
		if _, ok := summaryPeriodIndex[period]; !ok {
			continue
		}
		if _, duplicate := seen[period]; duplicate {
			return nil, fmt.Errorf("stats pair %d duplicates period %s", i, period)
		}
		seen[period] = struct{}{}
		var body struct {
			UptimeFraction string `json:"uptimeFraction"`
			PredictedApr   string `json:"predictedApr"`
		}
		var bodyObject map[string]json.RawMessage
		if err := unmarshalRequiredJSON(pair[1], &bodyObject); err != nil || bodyObject == nil {
			return nil, fmt.Errorf("stats pair %d has invalid body", i)
		}
		if json.Unmarshal(pair[1], &body) != nil {
			return nil, fmt.Errorf("stats pair %d has invalid body", i)
		}
		ps := summaryPeriodStat{period: period}
		if body.UptimeFraction != "" {
			if v, ok := parseFiniteFloat(body.UptimeFraction); ok {
				ps.uptime, ps.hasUptime = v, true
			}
		}
		if body.PredictedApr != "" {
			if v, ok := parseFiniteFloat(body.PredictedApr); ok {
				ps.apr, ps.hasApr = v, true
			}
		}
		out = append(out, ps)
	}
	return out, nil
}

func parseFiniteFloat(raw string) (float64, bool) {
	v, err := strconv.ParseFloat(raw, 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, false
	}
	return v, true
}
