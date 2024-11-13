package metrics

import (
	"context"
	"fmt"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	api "go.opentelemetry.io/otel/metric"
)

var (
	meter metric.Meter
)

func getAllObservables() []api.Observable {
	return []api.Observable{
		HLBlockHeightGauge,
		HLApplyDurationGauge,
		HLValidatorJailedStatus,
		HLValidatorStakeGauge,
		HLTotalStakeGauge,
		HLJailedStakeGauge,
		HLNotJailedStakeGauge,
		HLValidatorCountGauge,
		HLSoftwareVersionInfo,
		HLSoftwareUpToDate,
		HLLatestBlockTimeGauge,
		// HLNodeInfo, // TODO
		HLEVMBlockHeightGauge,
		HLActiveStakeGauge,
		HLInactiveStakeGauge,
		HLValidatorActiveStatus,
		HLValidatorRTTGauge,
	}
}

func initInstruments() error {
	if err := createInstruments(); err != nil {
		return fmt.Errorf("failed to create instruments: %w", err)
	}

	callback, err := meter.RegisterCallback(
		func(ctx context.Context, o api.Observer) error {
			metricsMutex.RLock()
			defer metricsMutex.RUnlock()

			commonLabels := getCommonLabels()

			for instrument, value := range currentValues {
				switch v := value.(type) {
				case float64:
					o.ObserveFloat64(instrument.(api.Float64Observable), v, api.WithAttributes(commonLabels...))
				case int64:
					o.ObserveInt64(instrument.(api.Int64Observable), v, api.WithAttributes(commonLabels...))
				}
			}

			// Process labeled values (append common labels) (not currently populated)
			for instrument, values := range labeledValues {
				for _, v := range values {
					// Combine specific labels with common labels
					allLabels := append(v.labels, commonLabels...)
					switch obs := instrument.(type) {
					case api.Float64Observable:
						o.ObserveFloat64(obs, v.value, api.WithAttributes(allLabels...))
					case api.Int64Observable:
						o.ObserveInt64(obs, int64(v.value), api.WithAttributes(allLabels...))
					}
				}
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

// TODO
func getCommonLabels() []attribute.KeyValue {
	return []attribute.KeyValue{}
}
