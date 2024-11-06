# Hyperliquid Exporter

A Go-based exporter that collects and exposes metrics for Hyperliquid node operators to Prometheus. This exporter monitors various aspects of a Hyperliquid node, including block height, proposer counts, block metrics, jailed validator statuses, software version information, stake distribution, and more.

A sample dashboard using these metrics can be found here: [ValiDAO Hyperliquid Testnet Monitor](https://hyperliquid-testnet-monitor.validao.xyz/public-dashboards/ff0fbe53299b4f95bb6e9651826b26e0)

You can import the sample grafana.json file provided in this repository to create your own Grafana dashboard using these metrics.

If you don't have a grafana+prom monitoring stack, you can find an easy-to-use one with a pre-loaded dashboard utilizing these metrics here: [validaoxyz/purrmetheus](https://github.com/validaoxyz/purrmetheus)

## Installation

### Building from Source

Clone the repository:

```bash
git clone https://github.com/validaoxyz/hyperliquid-exporter.git $HOME/hyperliquid-exporter
cd $HOME/hyperliquid-exporter
```

Build the exporter:

```bash
make build
```

The compiled binary will be placed in the `bin/` directory.

#### Using Docker

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

#### Install to System Directory

To install `hl_exporter` to `/usr/local/bin`:

```bash
make install
```

## Configuration

Create an `.env` file in the project's root directory with the required variables. 

To do so:
```bash
cp .env.sample .env
```
Open it with your text editor of choice, e.g.:
```bash
nano .env
```

## Running the Exporter

Ensure your `.env` file is properly configured.

Run the exporter:

```
hl_exporter start [options]
```
Or run it directly from the bin directory:

```
./bin/hl_exporter start [options]
```

The exporter will start a Prometheus HTTP server on port `8086` and begin exposing metrics.

To test it:
```bash
curl http://localhost:8086/metrics
```

### Systemd Service (Optional)

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

## Metrics Exposed
The following metrics are exposed by the exporter:
```
hl_proposer_count_total
hl_proposer_count_created
hl_block_height
hl_apply_duration
hl_validator_jailed_status
hl_validator_stake
hl_total_stake
hl_jailed_stake
hl_not_jailed_stake
hl_validator_count
hl_software_version_info
hl_software_up_to_date
hl_latest_block_time
hl_apply_duration_seconds
hl_block_time_milliseconds_bucket
```
To see an example of how to query these metrics in Grafana, see the example dashboard provided [in this repository](./grafana/). To understand what these metrics mean, see the [metrics documentation](./internal/metrics/).


## Customization

### Endpoint Configuration

The exporter fetches validator summaries from the Hyperliquid testnet API. If needed, you can modify the endpoint URL in the `validator_monitor.go` file within the `internal/monitors` directory.

### Logging Level

Adjust the logging level using the flag `--log-level` with values: `debug`, `info`, `warn`, `error`. Default is `debug`.

## Contributing

Contributions are greatly appreciated. Please submit a PR or open an issue on GitHub.

