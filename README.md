# Hyperliquid Exporter

A Go-based exporter that collects and exposes metrics for Hyperliquid node operators. This exporter monitors various aspects of a Hyperliquid node, including block metrics, proposer statistics, validator status and jailings, stake distribution, software version information, and EVM metrics

A sample dashboard using these metrics can be found here: [ValiDAO Hyperliquid Testnet Monitor](https://hyperliquid-testnet-monitor.validao.xyz/public-dashboards/ff0fbe53299b4f95bb6e9651826b26e0)

You can import the sample grafana.json file provided in this repository to create your own Grafana dashboard using these metrics.

If you don't have a grafana+prom monitoring stack, you can find an easy-to-use one with a pre-loaded dashboard utilizing these metrics here: [validaoxyz/purrmetheus](https://github.com/validaoxyz/purrmetheus)

## Quick Start

### Installation
```bash
git clone https://github.com/validaoxyz/hyperliquid-exporter.git $HOME/hyperliquid-exporter
cd $HOME/hyperliquid-exporter
make build
```

The compiled binary will be placed in the `bin/` directory.

### Default Configuration

By default, the exporter:
- Exposes Prometheus metrics on `:8086/metrics`
- Looks for log files in `$HOME/hl` and binaries in `$HOME/`
- Uses `info` log level
- Disables OTLP export

### Basic Usage
```bash
./bin/hyperliquid-exporter start
```

## Configuration Options

### Prometheus Metrics (Default)
- `--enable-prom`: Enable Prometheus endpoint (default: true)
- `--disable-prom`: Disable Prometheus endpoint (default: false)

### OpenTelemetry (OTLP) Export
- `--enable-otlp`: Enable OTLP export (default: false)
- `--otlp-endpoint`: OTLP endpoint (default: https://otel.hyperliquid.validao.xyz)
- `--otlp-insecure`: Use insecure connection for OTLP (default: false)
- `--identity`: Node identity (required when OTLP is enabled)
- `--chain`: Chain type ('mainnet' or 'testnet', required when OTLP is enabled)

### Node Configuration
- `--node-home`: Node home directory (default: $HOME/hl)
- `--node-binary`: Node binary path (default: $HOME/hl-node)

### Other Options
- `--log-level`: Log level (debug, info, warn, error) (default: info)

## Dashboards

### Sample Dashboard
A sample dashboard using these metrics can be found here: [ValiDAO Hyperliquid Testnet Monitor](https://hyperliquid-testnet-monitor.validao.xyz/public-dashboards/ff0fbe53299b4f95bb6e9651826b26e0)

You can import the `grafana/grafana.json` file provided in this repository to create your own Grafana dashboard.

### Ready-to-Use Monitoring Stack
If you don't have a Grafana + Prometheus monitoring stack, you can find an easy-to-use solution with a pre-loaded dashboard here: [validaoxyz/purrmetheus](https://github.com/validaoxyz/purrmetheus)

## Metrics Documentation

For detailed information about available metrics and sample queries, see the [metrics documentation](./internal/metrics/README.md).


## Run with Docker

Use Docker to run the hl_exporter in a container :

1. Edit the `docker-compose.yml` to change the local hyperliquid home folder :
Example : Replace
`- <HYPERLIQUID LOCAL HOME>:/hl:ro`
by
`- /home/hyperliquid/hl:/hl:ro` 

2. Build the image and run the container :
```bash
docker compose up -d
```

## Run with Systemd

To run the exporter as a systemd service:

Create a `systemd` service file:

```bash
echo "[Unit]
Description=HyperLiquid Prometheus Exporter
After=network.target

[Service]
# The working directory where the script is located
WorkingDirectory=$HOME/hyperliquid-exporter

# Command to execute the script
ExecStart=/usr/local/bin/hl_exporter start

# Restart the service if it crashes
Restart=always
RestartSec=10

# Run the service as the current user
User=$USER
Group=$USER

[Install]
WantedBy=multi-user.target" | sudo tee /etc/systemd/system/hyperliquid-exporter.service
```

Reload the systemd manager configuration and start the service:

```
sudo systemctl daemon-reload
sudo systemctl enable --now hyperliquid-exporter.service
```

## Contributing

Contributions are greatly appreciated. Please submit a PR or open an issue on GitHub.
