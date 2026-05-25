# Hyperliquid Exporter

A Go-based exporter that collects and exposes metrics for Hyperliquid node operators. This exporter produces metrics for HyperCore, HyperEVM, and HyperBFT, covering block production, transaction flow, validator performance, stake distribution, EVM activity, and consensus events. For a full list of metrics, see [docs/metrics.md](docs/metrics.md).

## Quick Start

### Installation

```bash
git clone https://github.com/validaoxyz/hyperliquid-exporter.git $HOME/hyperliquid-exporter
cd $HOME/hyperliquid-exporter
make build
```

### Basic Usage

```bash
./bin/hl_exporter start --chain mainnet [OPTIONS]

OPTIONS:
  --replica-metrics          # Transaction metrics (requires node --replica-cmds-style)
  --evm-metrics             # EVM chain metrics
  --contract-metrics        # Per-contract transaction tracking
  --metrics-port N          # Prometheus port (default 8086)
  --probe-info-endpoint     # Active HTTP probe of --serve-info
  --extended-metrics        # Extra monitors (lz4, logs, RocksDB, etc.)
  --per-peer-metrics        # Per-IP peer first/last-seen gauges (LRU 2048, 24h TTL)
  --skip-version-check      # For containerized deployments
  --skip-update-check       # For containerized deployments
  --otlp                    # Enable OTLP export (requires --alias and --otlp-endpoint)
  --alias "validator-name"  # Node alias for OTLP
  --otlp-endpoint "url"     # OTLP endpoint URL
```

Example: `./bin/hl_exporter start --chain mainnet --replica-metrics --evm-metrics`.

By default, the exporter:
- Exposes Prometheus metrics on `:8086/metrics`, liveness on `/livez`, readiness on `/readyz`
- Looks for log files in `$HOME/hl` and binaries in `$HOME/`
- Uses `info` log level
- Disables OTLP export


## `vals` subcommand

`hl_exporter vals` extracts the full Hyperliquid validator set (IP, moniker, address) straight from the node's ABCI state files (`data/periodic_abci_states`), reusing the exporter's internal ABCI reader rather than scraping Prometheus - the metrics only surface the RTT-probed subset, not the complete validator IP list. It emits the `ip,moniker,address,vp` CSV the validao.xyz node-map pipeline consumes (`vp` is left empty, since the map renderer only uses `ip`).

Three modes:

```bash
# one-shot: write the CSV to stdout (or --out FILE)
hl_exporter vals --node-home /home/ubuntu/hl

# serve: HTTP server exposing the CSV at /vals/<chain> and a header-less
# IP-per-line list at /nodes/<chain>.txt on :8087, regenerated hourly
hl_exporter vals --serve --addr 0.0.0.0:8087 --node-home /home/ubuntu/hl

# backfill: one validator-count row per day from historical states (JSONL)
hl_exporter vals --backfill --since 2025-05-31 --out f.jsonl
```

In `--serve` mode, `/vals/<chain>` is the validator set from ABCI state, while `/nodes/<chain>.txt` is the "all nodes" view: this box's own 24h peer set, fetched from a local peer-counter snapshot endpoint (`peer_ips_24h`), configurable via `--peer-counter-url` (default `http://127.0.0.1:19046/snapshot`). The `/nodes` route depends on that peer-counter running on the box; if it's unreachable the cache keeps its last good value.

## Run with Systemd
To run the exporter as a systemd service:

Create a systemd service file:
```
echo "[Unit]
Description=HyperLiquid Prometheus Exporter
After=network.target

[Service]
# The working directory where the script is located
WorkingDirectory=$HOME/hyperliquid-exporter

# Command to execute the script
ExecStart=/usr/local/bin/hl_exporter start --chain $CHAIN [options]

# Restart the service if it crashes
Restart=always
RestartSec=10

# Run the service as the current user
User=$USER
Group=$USER

[Install]
WantedBy=multi-user.target" | sudo tee /etc/systemd/system/hyperliquid-exporter.service
```

## Run with Docker

Use Docker to run `hl_exporter` in a container:

1. Edit the `docker-compose.yml` to change the local hyperliquid home folder :
Example : Replace
`- <HYPERLIQUID LOCAL HOME>:/hl:ro`
by
`- /home/hyperliquid/hl:/hl:ro` 

2. Build the image and run the container :
```bash
docker compose up -d
```


## Grafana Dashboard

Grafana dashboards are in `grafana/`: `v3-all-metrics.json` (every metric), `v3-showcase.json` (overview), and `v3-validator-detail.json` (per-validator drill-down). Import the one you want into your Grafana instance; each is templated on `$job`/`$instance`.

## Documentation

- [Metrics Reference](docs/metrics.md) - All metrics with descriptions and labels
- [CHANGELOG](CHANGELOG.md)
- [UPGRADING](UPGRADING.md) - v2 -> v3 notes