

# Hyperliquid Exporter

A Go-based exporter that collects and exposes metrics for Hyperliquid node operators to Prometheus. This exporter monitors various aspects of a Hyperliquid node, including block height, proposer counts, block metrics, jailed validator statuses, software version information, stake distribution, and more.

A sample dashboard using these metrics can be found here: [ValiDAO Hyperliquid Testnet Monitor](https://hyperliquid-testnet-monitor.validao.xyz/public-dashboards/ff0fbe53299b4f95bb6e9651826b26e0)

You can import the sample grafana.json file provided in this repository to create your own Grafana dashboard using these metrics.

## Requirements

- Go 1.19 or higher
- Prometheus server for scraping the metrics
- (Optional) Grafana for visualizing metrics

For the above last two requirements: We provide an easy-to-use prom+grafana stack with a pre-loaded dashboard here: [validaoxyz/hyperliquid-monitor](https://github.com/validaoxyz/hyperliquid-monitor)

## Installation

### Prerequisites

- If you wish to build from source: ensure you have Go installed on your system. Otherwise you can rely on the pre-built binaries provided in the releases section.

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

### Installing

#### Install to System Directory

To install `hl_exporter` to `/usr/local/bin`:

```bash
make install
```

## Configuration

Create an `.env` file in the project's root directory with the following content. 

To do so:
```bash
cp .env.sample .env
```
Then open it with your text editor of choice, e.g.:
```bash
nano .env
```
Make sure that you fill out all the relevant variables:
```bash
# The path to your node’s home directory.
NODE_HOME=/path/to/your/node/home
# The path to your hl-visor binary.
NODE_BINARY=/path/to/hl-visor
# Set to true if this node is a validator; otherwise, false.
IS_VALIDATOR=false
# Your validator’s address, if applicable.
VALIDATOR_ADDRESS=your_validator_address
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
```
To see an example of how to query these metrics in Grafana, see the example dashboard provided [in this repository](./grafana/). To understand what these metrics mean, see the [metrics documentation](./internal/metrics/).


## Customization

### Endpoint Configuration

The exporter fetches validator summaries from the Hyperliquid testnet API. If needed, you can modify the endpoint URL in the `validator_monitor.go` file within the `internal/monitors` directory.

### Logging Level

Adjust the logging level using the flag `--log-level` with values: `debug`, `info`, `warn`, `error`. Default is `debug`.

## Cleaning Up

To remove build artifacts:

```
make clean
```

## Contributing

We welcome contributions to continue building out this exporter by adding more metrics, improving performance, or fixing issues. To contribute:

- Fork the repository
- Create a feature branch
- Commit your changes
- Push to your branch
- Open a pull request