package metrics

import (
	"context"
	"fmt"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	otlpmetrichttp "go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/prometheus"
	"go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	semconv "go.opentelemetry.io/otel/semconv/v1.21.0"
)

// InitMetrics initializes the metrics system with the given configuration
func InitMetrics(ctx context.Context, cfg MetricsConfig) error {
	// Initialize node identity with values from config
	if err := InitializeNodeIdentity(cfg); err != nil {
		return fmt.Errorf("failed to initialize node identity: %w", err)
	}

	// Initialize the provider
	if err := InitProvider(ctx, cfg); err != nil {
		return fmt.Errorf("failed to initialize provider: %w", err)
	}

	if err := createInstruments(); err != nil {
		return fmt.Errorf("failed to initialize instruments: %w", err)
	}

	if cfg.EnablePrometheus {
		if err := StartPrometheusServer(ctx, 8086); err != nil {
			return fmt.Errorf("failed to start Prometheus server: %w", err)
		}
	}

	if err := RegisterCallbacks(); err != nil {
		return fmt.Errorf("failed to register callbacks: %w", err)
	}

	return nil
}

func sanitizeEndpoint(endpoint string) string {
	// Remove https:// or http:// if present
	if len(endpoint) > 8 && (endpoint[:8] == "https://" || endpoint[:7] == "http://") {
		if endpoint[:8] == "https://" {
			return endpoint[8:]
		}
		return endpoint[7:]
	}
	return endpoint
}

func InitProvider(ctx context.Context, cfg MetricsConfig) error {
	metricsMutex.RLock()
	serverIP := nodeIdentity.ServerIP
	isValidator := nodeIdentity.IsValidator
	validatorAddress := nodeIdentity.ValidatorAddress
	metricsMutex.RUnlock()

	res := resource.NewWithAttributes(
		semconv.SchemaURL,
		attribute.String("instance", cfg.Alias),
		attribute.String("job", fmt.Sprintf("hyperliquid-exporter/%s", cfg.Chain)),
		attribute.String("server_ip", serverIP),
		attribute.Bool("is_validator", isValidator),
		attribute.String("validator_address", validatorAddress),
	)

	var opts []sdkmetric.Option
	opts = append(opts, sdkmetric.WithResource(res))

	if cfg.EnablePrometheus {
		promExporter, err := prometheus.New(
			prometheus.WithoutScopeInfo(),
		)
		if err != nil {
			return fmt.Errorf("failed to create Prometheus exporter: %w", err)
		}
		opts = append(opts, sdkmetric.WithReader(promExporter))
	}

	// Initialize OTLP if enabled
	if cfg.EnableOTLP {
		options := []otlpmetrichttp.Option{
			otlpmetrichttp.WithEndpoint(sanitizeEndpoint(cfg.OTLPEndpoint)),
		}

		if cfg.OTLPInsecure {
			options = append(options, otlpmetrichttp.WithInsecure())
		}

		otlpExporter, err := otlpmetrichttp.New(ctx, options...)
		if err != nil {
			return fmt.Errorf("failed to create OTLP exporter: %w", err)
		}

		reader := sdkmetric.NewPeriodicReader(
			otlpExporter,
			sdkmetric.WithInterval(5*time.Second),
		)
		opts = append(opts, sdkmetric.WithReader(reader))
	}

	provider := sdkmetric.NewMeterProvider(opts...)

	otel.SetMeterProvider(provider)

	meter = otel.Meter(
		"hyperliquid-exporter",
		metric.WithInstrumentationVersion("0.1.0"),
	)

	return nil
}
