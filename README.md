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
  --otlp                    # Enable OTLP export (requires --alias and --otlp-endpoint)
  --alias "validator-name"  # Node alias for OTLP
  --otlp-endpoint "url"     # OTLP endpoint URL
```

Example: `./bin/hl_exporter start --chain mainnet --replica-metrics --evm-metrics`.

By default, the exporter:
- Exposes Prometheus metrics on `:8086/metrics`
- Looks for log files in `$HOME/hl` and binaries in `$HOME/`
- Uses `info` log level
- Disables OTLP export


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

A Grafana dashboard is available at `grafana/dashboard.json`. Import it into your Grafana instance for visualization of all metrics.

## Documentation

- [Metrics Reference](docs/metrics.md) - All metrics with descriptions and labels