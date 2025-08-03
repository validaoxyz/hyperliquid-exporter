# Grafana Dashboard

This directory contains a pre-built Grafana dashboard for monitoring Hyperliquid blockchain metrics.

## Quick Import

1. Open your Grafana instance
2. Click the **+** (Plus) icon in the left sidebar → **Import**
3. Upload `grafana.json` or paste its contents
4. Select your Prometheus data source
5. Click **Import**

## Dashboard Features

The dashboard provides comprehensive monitoring for:

- **Core Blockchain**: Block height, transaction rates, validator activity
- **EVM Chain**: Gas usage, transaction types, contract activity  
- **Consensus**: Validator status, stake distribution, network health
- **Performance**: Block processing times, system metrics

## Requirements

- **Grafana**: v8.0+ recommended
- **Prometheus**: Scraping the exporter at `/metrics`
- **Exporter**: Running with `--enable-replica-metrics` for complete data

## Recording Rules

For optimal dashboard performance, install the Prometheus recording rules:

```yaml
# prometheus.yml
rule_files:
  - "recording_rules_hyperliquid_rates.yml"
```

This pre-calculates smoothed rate metrics for better visualization of batch-updated data.