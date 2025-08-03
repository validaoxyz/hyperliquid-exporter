package metrics

import (
	"context"
	"fmt"

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
			for instrument, values := range labeledValues {
				for _, v := range values {
					allLabels := append(v.labels, commonLabels...)
					switch obs := instrument.(type) {
					case api.Float64Observable:
						o.ObserveFloat64(obs, v.value, api.WithAttributes(allLabels...))
					case api.Int64Observable:
						o.ObserveInt64(obs, int64(v.value), api.WithAttributes(allLabels...))
					}
				}
			}

			// collect memory stats
			if err := collectMemoryStats(ctx, o); err != nil {
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
