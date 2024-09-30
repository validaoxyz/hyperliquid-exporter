

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

- Ensure you have Go installed on your system. You can download it from the official website.
- git installed for version control.

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

To install `hl_exporter` to `/usr/local/bin` (may require sudo):

```bash
sudo make install
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
hl_exporter
```
Or run it directly from the bin directory:

```
./bin/hl_exporter
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
ExecStart=$HOME/hyperliquid-exporter/hl-python-venv/bin/python3 $HOME/hyperliquid-exporter/hl_exporter.py

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
```
# HELP proposer_count_total Number of proposals made by each proposer.
# TYPE proposer_count_total counter
proposer_count_total{proposer="full_validator_address"} <value>

# HELP hl_block_height Block height from latest block time file.
# TYPE hl_block_height gauge
hl_block_height <value>

# HELP hl_apply_duration Apply duration from latest block time file.
# TYPE hl_apply_duration gauge
hl_apply_duration <value>

# HELP validator_jailed_status Jailed status of validators
# TYPE validator_jailed_status gauge
validator_jailed_status{name="moniker",validator="full_validator_address"} <value>

# HELP hl_validator_stake Stake of validators
# TYPE hl_validator_stake gauge
hl_validator_stake{name="",validator="full_validator_address"} <value>

# HELP hl_total_stake Total stake across all validators
# TYPE hl_total_stake gauge
hl_total_stake <value>

# HELP hl_not_jailed_stake Total stake of not jailed validators
# TYPE hl_not_jailed_stake gauge
hl_not_jailed_stake <value>

# HELP hl_software_version_info Hyperliquid software version information.
# TYPE hl_software_version_info gauge
hl_software_version_info{commit="commit_hash", date="build_date"} <value>

# HELP hl_software_up_to_date Indicates whether the software is up to date.
# TYPE hl_software_up_to_date gauge
hl_software_up_to_date <0|1>
```



## Dashboard Visualization

You can use Grafana to visualize the metrics exported by this exporter. A sample `grafana.json` dashboard configuration is provided under `grafana/`.

To import the dashboard:

- Open your Grafana instance.
- Click on the Plus (+) icon on the left sidebar and select Import.
- Upload the `grafana.json` file or paste its JSON content.
- Select the Prometheus data source you are using to scrape the exporter.

## Customization

### Endpoint Configuration

The exporter fetches validator summaries from the Hyperliquid testnet API. If needed, you can modify the endpoint URL in the `validator_monitor.go` file within the `internal/monitors` directory.

### Logging Level

Adjust the logging level in the code if you need more or less verbosity. You can modify the log package settings in main.go or individual monitor files.

## Cleaning Up

To remove build artifacts:

```
make clean
```

## Contributing

We welcome contributions to enhance this exporter by adding more metrics, improving performance, or fixing issues. To contribute:

- Fork the Repository
- Create a Feature Branch
  ```bash
  git checkout -b feature/your-feature-name
  ```
- Commit Your Changes
  ```
  git commit -am 'Add new feature'
  ```
- Push to Your Branch
  ```
  git push origin feature/your-feature-name
  ```
- Open a Pull Request

## License

This project is licensed under the MIT License - see the LICENSE file for details.

## Acknowledgments

- Prometheus Go client library

- godotenv

