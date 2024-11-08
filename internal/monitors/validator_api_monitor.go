package monitors

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/validaoxyz/hyperliquid-exporter/internal/logger"
	"github.com/validaoxyz/hyperliquid-exporter/internal/metrics"
)

type ValidatorSummary struct {
	Validator string  `json:"validator"`
	Signer    string  `json:"signer"`
	Name      string  `json:"name"`
	Stake     float64 `json:"stake"`
	IsJailed  bool    `json:"isJailed"`
	IsActive  bool    `json:"isActive"`
}

func StartValidatorMonitor(ctx context.Context, errCh chan<- error) {
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := updateValidatorMetrics(ctx); err != nil {
					errCh <- fmt.Errorf("validator monitor error: %w", err)
				}
			}
		}
	}()
}

func updateValidatorMetrics(ctx context.Context) error {
	client := &http.Client{Timeout: 10 * time.Second}
	url := "https://api.hyperliquid-testnet.xyz/info"
	payload := []byte(`{"type": "validatorSummaries"}`)

	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewBuffer(payload))
	if err != nil {
		return fmt.Errorf("error creating request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("error making request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("error reading response body: %w", err)
	}

	var summaries []ValidatorSummary
	if err := json.Unmarshal(body, &summaries); err != nil {
		return fmt.Errorf("error parsing validator summaries: %w", err)
	}

	totalStake := 0.0
	jailedStake := 0.0
	notJailedStake := 0.0
	activeStake := 0.0
	inactiveStake := 0.0

	for _, summary := range summaries {
		// Update validator stake metric
		metrics.SetValidatorStake(summary.Validator, summary.Signer, summary.Name, summary.Stake)

		// Update validator jailed status
		jailedStatus := 0.0
		if summary.IsJailed {
			jailedStatus = 1.0
			jailedStake += summary.Stake
		} else {
			notJailedStake += summary.Stake
		}
		metrics.SetValidatorJailedStatus(summary.Validator, summary.Signer, summary.Name, jailedStatus)

		// Update active/inactive stake
		if summary.IsActive {
			activeStake += summary.Stake
		} else {
			inactiveStake += summary.Stake
		}

		// Set active status
		activeStatus := 0.0
		if summary.IsActive {
			activeStatus = 1.0
		}
		metrics.SetValidatorActiveStatus(summary.Validator, summary.Signer, summary.Name, activeStatus)

		totalStake += summary.Stake
	}

	// Update aggregate metrics
	metrics.SetTotalStake(totalStake)
	metrics.SetJailedStake(jailedStake)
	metrics.SetNotJailedStake(notJailedStake)
	metrics.SetActiveStake(activeStake)
	metrics.SetInactiveStake(inactiveStake)
	metrics.SetValidatorCount(int64(len(summaries)))

	logger.Info("Updated validator metrics: Total validators: %d", len(summaries))
	logger.Info("Total stake: %f, Jailed stake: %f, Not jailed stake: %f, Active stake: %f, Inactive stake: %f",
		totalStake, jailedStake, notJailedStake, activeStake, inactiveStake)

	return nil
}
