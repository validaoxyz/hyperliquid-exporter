package metrics

import (
	"context"
	"fmt"
	"time"

	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	api "go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/sdk/metric"
)

func setupOTLPExporter(ctx context.Context, endpoint string) error {
	exporter, err := otlpmetrichttp.New(ctx, otlpmetrichttp.WithEndpoint(endpoint), otlpmetrichttp.WithInsecure())
	if err != nil {
		return fmt.Errorf("failed to create OTLP exporter: %w", err)
	}

	reader := metric.NewPeriodicReader(exporter, metric.WithInterval(15*time.Second))
	opts := []metric.Option{metric.WithReader(reader)}

	provider := metric.NewMeterProvider(opts...)
	meter = provider.Meter("hyperliquid-exporter", api.WithInstrumentationVersion("0.1.0"))

	return nil
}
