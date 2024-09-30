package monitors

import (
	"bytes"
	"encoding/json"
	"io/ioutil"
	"log"
	"net/http"
	"time"

	"github.com/validaoxyz/hyperliquid-exporter/internal/metrics"
)

// ValidatorSummary represents a validator summary
type ValidatorSummary struct {
	Validator string  `json:"validator"`
	Name      string  `json:"name"`
	Stake     float64 `json:"stake"`
	IsJailed  bool    `json:"isJailed"`
}

// StartValidatorMonitor starts monitoring validator data
func StartValidatorMonitor() {
	go func() {
		for {
			updateValidatorMetrics()
			time.Sleep(600 * time.Second) // 10 minutes
		}
	}()
}

func updateValidatorMetrics() {
	client := &http.Client{Timeout: 10 * time.Second}
	url := "https://api.hyperliquid-testnet.xyz/info"
	payload := []byte(`{"type": "validatorSummaries"}`)

	req, err := http.NewRequest("POST", url, bytes.NewBuffer(payload))
	if err != nil {
		log.Printf("Error creating request: %v", err)
		return
	}

	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		log.Printf("Error making request: %v", err)
		return
	}
	defer resp.Body.Close()

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		log.Printf("Error reading response body: %v", err)
		return
	}

	var summaries []ValidatorSummary
	err = json.Unmarshal(body, &summaries)
	if err != nil {
		log.Printf("Error parsing validator summaries: %v", err)
		return
	}

	totalStake := 0.0
	jailedStake := 0.0
	notJailedStake := 0.0

	for _, summary := range summaries {
		metrics.HLValidatorStakeGauge.WithLabelValues(summary.Validator, summary.Name).Set(summary.Stake)
		status := 0.0
		if summary.IsJailed {
			status = 1.0
			jailedStake += summary.Stake
		} else {
			notJailedStake += summary.Stake
		}
		totalStake += summary.Stake

		metrics.HLValidatorJailedStatus.WithLabelValues(summary.Validator, summary.Name).Set(status)
	}

	metrics.HLTotalStakeGauge.Set(totalStake)
	metrics.HLJailedStakeGauge.Set(jailedStake)
	metrics.HLNotJailedStakeGauge.Set(notJailedStake)
	metrics.HLValidatorCountGauge.Set(float64(len(summaries)))

	log.Printf("Updated validator metrics: Total validators: %d", len(summaries))
	log.Printf("Total stake: %f, Jailed stake: %f, Not jailed stake: %f", totalStake, jailedStake, notJailedStake)
}
