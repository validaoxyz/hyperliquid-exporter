package metrics

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/prometheus/otlptranslator"
	"github.com/validaoxyz/hyperliquid-exporter/internal/config"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	otlpmetrichttp "go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/prometheus"
	"go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	semconv "go.opentelemetry.io/otel/semconv/v1.21.0"
)

const providerStartupCleanupTimeout = 5 * time.Second

// prometheusTranslationCompatibilityOption keeps the established escaping and
// counter-suffix behavior stable across OpenTelemetry bridge upgrades.
func prometheusTranslationCompatibilityOption() prometheus.Option {
	return prometheus.WithTranslationStrategy(otlptranslator.UnderscoreEscapingWithSuffixes)
}

// prometheusUnitCompatibilityOption prevents the newer bridge from appending
// custom instrument units to established metric family names. Instrument names
// already contain their intended unit where one belongs in the public name.
func prometheusUnitCompatibilityOption() prometheus.Option {
	//lint:ignore SA1019 The replacement translation strategies cannot preserve
	// both established unit-free names and Prometheus counter suffixes.
	return prometheus.WithoutUnits()
}

// InitMetrics initializes the metrics system and returns ownership of the SDK
// provider. The caller must stop metric producers before invoking Shutdown on
// the returned owner.
func InitMetrics(ctx context.Context, cfg MetricsConfig) (*ProviderOwner, error) {
	chain, err := config.NormalizeChain(cfg.Chain)
	if err != nil {
		return nil, fmt.Errorf("invalid configured chain: %w", err)
	}
	cfg.Chain = chain
	if err := SetConfiguredChain(chain); err != nil {
		return nil, fmt.Errorf("failed to initialize configured chain metric: %w", err)
	}

	// initialize node identity with values from config
	if err := InitializeNodeIdentity(cfg); err != nil {
		return nil, fmt.Errorf("failed to initialize node identity: %w", err)
	}

	// initialize the provider
	owner, err := InitProvider(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize provider: %w", err)
	}

	if err := createInstruments(); err != nil {
		return failMetricsInitialization(owner, fmt.Errorf("failed to initialize instruments: %w", err))
	}

	if cfg.EnablePrometheus {
		port := cfg.PrometheusPort
		if port == 0 {
			port = 8086
		}
		if err := StartPrometheusServer(ctx, port, cfg.EnablePprof); err != nil {
			return failMetricsInitialization(owner, fmt.Errorf("failed to start Prometheus server: %w", err))
		}
	}

	if err := RegisterCallbacks(); err != nil {
		return failMetricsInitialization(owner, fmt.Errorf("failed to register callbacks: %w", err))
	}

	return owner, nil
}

func failMetricsInitialization(owner *ProviderOwner, cause error) (*ProviderOwner, error) {
	cleanupCtx, cancel := context.WithTimeout(context.Background(), providerStartupCleanupTimeout)
	defer cancel()
	if err := owner.Shutdown(cleanupCtx); err != nil {
		cause = errors.Join(cause, fmt.Errorf("failed to shut down partially initialized provider: %w", err))
	}
	return nil, cause
}

func sanitizeEndpoint(endpoint string) string {
	if len(endpoint) > 8 && (endpoint[:8] == "https://" || endpoint[:7] == "http://") {
		if endpoint[:8] == "https://" {
			return endpoint[8:]
		}
		return endpoint[7:]
	}
	return endpoint
}

func InitProvider(ctx context.Context, cfg MetricsConfig) (*ProviderOwner, error) {
	chain, err := config.NormalizeChain(cfg.Chain)
	if err != nil {
		return nil, fmt.Errorf("invalid configured chain: %w", err)
	}
	cfg.Chain = chain

	metricsMutex.RLock()
	serverIP := nodeIdentity.ServerIP
	isValidator := nodeIdentity.IsValidator
	validatorAddress := nodeIdentity.ValidatorAddress
	metricsMutex.RUnlock()

	res := resource.NewWithAttributes(
		semconv.SchemaURL,
		attribute.String("instance", cfg.Alias),
		attribute.String("job", fmt.Sprintf("hyperliquid-exporter/%s", cfg.Chain)),
		attribute.String("chain", cfg.Chain),
		attribute.String("server_ip", serverIP),
		attribute.Bool("is_validator", isValidator),
		attribute.String("validator_address", validatorAddress),
	)

	var opts []sdkmetric.Option
	opts = append(opts, sdkmetric.WithResource(res))

	if cfg.EnablePrometheus {
		promExporter, err := prometheus.New(
			prometheus.WithoutScopeInfo(),
			prometheusTranslationCompatibilityOption(),
			prometheusUnitCompatibilityOption(),
		)
		if err != nil {
			return nil, fmt.Errorf("failed to create Prometheus exporter: %w", err)
		}
		opts = append(opts, sdkmetric.WithReader(promExporter))
	}

	// init OTLP if flag
	if cfg.EnableOTLP {
		options := []otlpmetrichttp.Option{
			otlpmetrichttp.WithEndpoint(sanitizeEndpoint(cfg.OTLPEndpoint)),
		}

		if cfg.OTLPInsecure {
			options = append(options, otlpmetrichttp.WithInsecure())
		}

		otlpExporter, err := otlpmetrichttp.New(ctx, options...)
		if err != nil {
			return nil, fmt.Errorf("failed to create OTLP exporter: %w", err)
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

	return newProviderOwner(provider.Shutdown), nil
}
