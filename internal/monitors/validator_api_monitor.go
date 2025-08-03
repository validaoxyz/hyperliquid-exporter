package monitors

import (
	"context"
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

	go func() {
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
	}()
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
	mappingCount := 0

	for _, summary := range summaries {
		// register signer->val mapping (lowercase for consistency)
		metrics.RegisterSignerMapping(strings.ToLower(summary.Signer), strings.ToLower(summary.Validator))
		mappingCount++

		// register the full validator addr for expansion
		metrics.RegisterFullAddress(strings.ToLower(summary.Validator))
		// also register the signer addr for expansion (consensus logs use signer addrss)
		metrics.RegisterFullAddress(strings.ToLower(summary.Signer))

		// register val info (signer and name) for consensus metrics
		metrics.RegisterValidatorInfo(strings.ToLower(summary.Validator), strings.ToLower(summary.Signer), summary.Name)

		metrics.SetValidatorStake(summary.Validator, summary.Signer, summary.Name, summary.Stake)

		// update val jailed status
		jailedStatus := 0.0
		if summary.IsJailed {
			jailedStatus = 1.0
			jailedStake += summary.Stake
		} else {
			notJailedStake += summary.Stake
		}
		metrics.SetValidatorJailedStatus(summary.Validator, summary.Signer, summary.Name, jailedStatus)

		// update active/inactive stake
		if summary.IsActive {
			activeStake += summary.Stake
		} else {
			inactiveStake += summary.Stake
		}

		// set active status
		activeStatus := 0.0
		if summary.IsActive {
			activeStatus = 1.0
		}
		metrics.SetValidatorActiveStatus(summary.Validator, summary.Signer, summary.Name, activeStatus)

		totalStake += summary.Stake
	}

	// update aggregate metrics
	metrics.SetTotalStake(totalStake)
	metrics.SetJailedStake(jailedStake)
	metrics.SetNotJailedStake(notJailedStake)
	metrics.SetActiveStake(activeStake)
	metrics.SetInactiveStake(inactiveStake)
	metrics.SetValidatorCount(int64(len(summaries)))

	return nil
}

// returns the HL resolver instance
func GetValidatorResolver() *hyperliquidapi.Resolver {
	return hlResolver
}
