package metrics

import (
	"context"
	"fmt"
	"time"

	api "go.opentelemetry.io/otel/metric"
)

// registers all observable instruments with a single callback
func RegisterCallbacks() error {
	// callback for metrics
	callback, err := meter.RegisterCallback(
		func(ctx context.Context, o api.Observer) error {
			metricsMutex.RLock()
			defer metricsMutex.RUnlock()

			commonLabels := getCommonLabels()

			//  regular values with common labels
			for instrument, value := range currentValues {
				switch v := value.(type) {
				case float64:
					o.ObserveFloat64(instrument.(api.Float64Observable), v, api.WithAttributes(commonLabels...))
				case int64:
					o.ObserveInt64(instrument.(api.Int64Observable), v, api.WithAttributes(commonLabels...))
				}
			}

			// labeled values
			now := float64(time.Now().Unix())
			for instrument, values := range labeledValues {
				// vote series store the last-vote unix timestamp; expose it
				// as an age so the value keeps climbing while a validator
				// is silent
				voteAge := instrument == api.Observable(HLConsensusVoteTimeDiffGauge)
				for _, v := range values {
					val := v.value
					if voteAge {
						val = now - v.value
					}
					allLabels := append(v.labels, commonLabels...)
					switch obs := instrument.(type) {
					case api.Float64Observable:
						o.ObserveFloat64(obs, val, api.WithAttributes(allLabels...))
					case api.Int64Observable:
						o.ObserveInt64(obs, int64(val), api.WithAttributes(allLabels...))
					}
				}
			}

			// collect memory stats (cached; no stop-the-world on scrape)
			if err := collectMemoryStats(o); err != nil {
				return err
			}

			return nil
		},
		getAllObservables()...,
	)
	if err != nil {
		return fmt.Errorf("failed to register callbacks: %w", err)
	}

	callbacks = append(callbacks, callback)
	return nil
}
