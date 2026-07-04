package monitors

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/validaoxyz/hyperliquid-exporter/internal/config"
	hyperliquidapi "github.com/validaoxyz/hyperliquid-exporter/internal/hyperliquid-api"
	"github.com/validaoxyz/hyperliquid-exporter/internal/logger"
	"github.com/validaoxyz/hyperliquid-exporter/internal/metrics"
)

var hlResolver *hyperliquidapi.Resolver

func StartValidatorMonitor(ctx context.Context, cfg config.Config, errCh chan<- error) {
	// init HL resolver
	hlResolver = hyperliquidapi.NewResolver(cfg.Chain)

	goSafe("validator_api", func() {
		// run immediately on startup to populate mappings
		if err := updateValidatorMetrics(ctx, cfg); err != nil {
			logger.Error("Initial validator monitor update error: %v", err)
			errCh <- err
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
					errCh <- err
				}
			}
		}
	})
}

func updateValidatorMetrics(ctx context.Context, cfg config.Config) error {
	// use resolver to get val summaries
	summaries, err := hlResolver.GetValidatorSummaries(ctx, false)
	if err != nil {
		return err
	}

	totalStake := 0.0
	jailedStake := 0.0
	notJailedStake := 0.0
	activeStake := 0.0
	inactiveStake := 0.0

	snapshot := make([]metrics.ValidatorSummarySnapshot, 0, len(summaries))

	for _, summary := range summaries {
		// register signer->val mapping (lowercase for consistency)
		metrics.RegisterSignerMapping(strings.ToLower(summary.Signer), strings.ToLower(summary.Validator))

		// register the full validator addr for expansion
		metrics.RegisterFullAddress(strings.ToLower(summary.Validator))
		// also register the signer addr for expansion (consensus logs use signer addrss)
		metrics.RegisterFullAddress(strings.ToLower(summary.Signer))

		// register val info (signer and name) for consensus metrics
		metrics.RegisterValidatorInfo(strings.ToLower(summary.Validator), strings.ToLower(summary.Signer), summary.Name)

		snapshot = append(snapshot, metrics.ValidatorSummarySnapshot{
			Validator: summary.Validator,
			Signer:    summary.Signer,
			Name:      summary.Name,
			Stake:     summary.Stake,
			Jailed:    summary.IsJailed,
			Active:    summary.IsActive,
		})

		if summary.IsJailed {
			jailedStake += summary.Stake
		} else {
			notJailedStake += summary.Stake
		}
		if summary.IsActive {
			activeStake += summary.Stake
		} else {
			inactiveStake += summary.Stake
		}
		totalStake += summary.Stake
	}

	// reconcile the per-validator gauges as one snapshot so validators that
	// left the set are removed instead of freezing at their last value
	metrics.ReplaceValidatorSummaries(snapshot)
	reconcileValidatorExtras(summaries)

	// update aggregate metrics
	metrics.SetTotalStake(totalStake)
	metrics.SetJailedStake(jailedStake)
	metrics.SetNotJailedStake(notJailedStake)
	metrics.SetActiveStake(activeStake)
	metrics.SetInactiveStake(inactiveStake)
	metrics.SetValidatorCount(int64(len(summaries)))

	metrics.MarkMonitorTick("validator_api")
	return nil
}

// returns the HL resolver instance
func GetValidatorResolver() *hyperliquidapi.Resolver {
	return hlResolver
}

// summaryPeriods is the fixed period set validatorSummaries stats carry.
var summaryPeriods = []string{"day", "week", "month"}

// prevExtraLabels tracks the label sets currently published on the extras
// vectors so validators leaving the set (or unjailing) get their series
// deleted. Single-goroutine access from the API monitor.
var (
	prevExtraLabels = map[string][3]string{}
	prevUnjailable  = map[string][3]string{}
)

// reconcileValidatorExtras publishes the validatorSummaries facts beyond
// stake/status: recent blocks proposed, commission, the unjail timestamp
// while jailed, and per-period uptime/APR. These fields were previously
// decoded and discarded even though unjailableAfter is the number an
// operator actually needs mid-incident.
func reconcileValidatorExtras(summaries []hyperliquidapi.ValidatorSummary) {
	current := make(map[string][3]string, len(summaries))
	currentJailed := make(map[string][3]string)

	for _, summary := range summaries {
		labels := [3]string{summary.Validator, summary.Signer, summary.Name}
		current[summary.Validator] = labels

		metrics.HLConsensusValidatorRecentBlocks.
			WithLabelValues(labels[0], labels[1], labels[2]).Set(float64(summary.NRecentBlocks))

		if v, err := strconv.ParseFloat(summary.Commission, 64); err == nil {
			metrics.HLConsensusValidatorCommissionRate.
				WithLabelValues(labels[0], labels[1], labels[2]).Set(v)
		}

		if summary.IsJailed && summary.UnjailableAfter > 0 {
			currentJailed[summary.Validator] = labels
			// the API reports milliseconds since epoch
			metrics.HLConsensusValidatorUnjailableAfter.
				WithLabelValues(labels[0], labels[1], labels[2]).Set(float64(summary.UnjailableAfter) / 1000.0)
		}

		for _, ps := range parseSummaryStats(summary.Stats) {
			if ps.hasUptime {
				metrics.HLConsensusValidatorUptimeFraction.
					WithLabelValues(labels[0], labels[1], labels[2], ps.period).Set(ps.uptime)
			}
			if ps.hasApr {
				metrics.HLConsensusValidatorPredictedApr.
					WithLabelValues(labels[0], labels[1], labels[2], ps.period).Set(ps.apr)
			}
		}
	}

	for validator, labels := range prevExtraLabels {
		// drop old series when the validator left OR its label set changed
		// (a renamed moniker would otherwise leak the old-name series)
		if cur, ok := current[validator]; ok && cur == labels {
			continue
		}
		metrics.HLConsensusValidatorRecentBlocks.DeleteLabelValues(labels[0], labels[1], labels[2])
		metrics.HLConsensusValidatorCommissionRate.DeleteLabelValues(labels[0], labels[1], labels[2])
		for _, period := range summaryPeriods {
			metrics.HLConsensusValidatorUptimeFraction.DeleteLabelValues(labels[0], labels[1], labels[2], period)
			metrics.HLConsensusValidatorPredictedApr.DeleteLabelValues(labels[0], labels[1], labels[2], period)
		}
	}
	for validator, labels := range prevUnjailable {
		if cur, ok := currentJailed[validator]; !ok || cur != labels {
			metrics.HLConsensusValidatorUnjailableAfter.DeleteLabelValues(labels[0], labels[1], labels[2])
		}
	}
	prevExtraLabels = current
	prevUnjailable = currentJailed
}

type summaryPeriodStat struct {
	period            string
	uptime, apr       float64
	hasUptime, hasApr bool
}

func parseSummaryStats(raw [][]json.RawMessage) []summaryPeriodStat {
	out := make([]summaryPeriodStat, 0, len(raw))
	for _, pair := range raw {
		if len(pair) != 2 {
			continue
		}
		var period string
		if json.Unmarshal(pair[0], &period) != nil || period == "" {
			continue
		}
		var body struct {
			UptimeFraction string `json:"uptimeFraction"`
			PredictedApr   string `json:"predictedApr"`
		}
		if json.Unmarshal(pair[1], &body) != nil {
			continue
		}
		ps := summaryPeriodStat{period: period}
		if v, err := strconv.ParseFloat(body.UptimeFraction, 64); err == nil {
			ps.uptime, ps.hasUptime = v, true
		}
		if v, err := strconv.ParseFloat(body.PredictedApr, 64); err == nil {
			ps.apr, ps.hasApr = v, true
		}
		out = append(out, ps)
	}
	return out
}
