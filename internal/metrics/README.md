# Hyperliquid Exporter Metrics

## Block Metrics

### hl_block_height
- Type: Gauge
- Description: Current block height of the chain
- Usage: Track chain progress and sync status

### hl_latest_block_time
- Type: Gauge
- Description: Timestamp of the latest block
- Usage: Monitor block production and potential halts

### hl_apply_duration
- Type: Gauge
- Description: Duration of block application in milliseconds
- Usage: Monitor block processing performance

### hl_apply_duration_milliseconds
- Type: Histogram
- Description: Distribution of block apply durations in milliseconds
- Buckets: [0.1, 0.2, 0.5, 1, 2, 3, 5, 7, 10, 15, 20, 30, 50, 75, 100, 150, 160, 170, 180, 190, 200, 210, 220, 230, 240, 250]
- Usage: Analyze block processing performance patterns

### hl_block_time_milliseconds
- Type: Histogram
- Description: Distribution of time between blocks in milliseconds
- Buckets: [10, 20, 30, 40, 50, 60, 70, 80, 90, 100, 120, 140, 160, 180, 200, 220, 240, 260, 280, 300, 350, 400, 450, 500, 600, 700, 800, 900, 1000, 1500, 2000]
- Usage: Monitor block production consistency

### hl_proposer_count_total
- Type: Counter
- Description: Total number of blocks proposed by each validator
- Labels:
  - `validator`: validator address
- Usage: Track validator proposal activity

## EVM Metrics

### hl_evm_block_height
- Type: Gauge
- Description: Current EVM block height
- Usage: Track EVM chain sync status

### hl_evm_transactions_total
- Type: Counter
- Description: Total number of EVM transactions processed
- Usage: Monitor EVM transaction activity

## Software Status

### hl_software_version_info
- Type: Gauge
- Description: Information about the current software version running on the node
- Labels:
  - `version`: Software version
  - `commit_hash`: Git commit hash of the current version
- Usage: Track software versions across nodes

### hl_software_up_to_date
- Type: Gauge
- Description: Indicates if the node software is up to date (1) or needs updating (0)
- Usage: Monitor node software version status

## Stake Status

### hl_total_stake
- Type: Gauge
- Description: Total stake across all validators in the network
- Usage: Monitor total network security

### hl_active_stake
- Type: Gauge
- Description: Total stake of active validators in the network
- Usage: Monitor active stake in the network

### hl_inactive_stake
- Type: Gauge
- Description: Total stake of inactive validators in the network
- Usage: Monitor inactive stake in the network

### hl_jailed_stake
- Type: Gauge
- Description: Total stake of jailed validators
- Usage: Monitor impact of validator jailing

### hl_not_jailed_stake
- Type: Gauge
- Description: Total stake of not jailed validators
- Usage: Track active stake in the network

### hl_validator_stake
- Type: Gauge
- Description: Individual stake of each validator
- Labels: 
  - `validator`: validator address
  - `signer`: signer address
  - `moniker`: validator name
- Usage: Track individual validator stakes

### hl_validator_jailed_status
- Type: Gauge
- Description: Jailed status of each validator (1 if jailed, 0 if not jailed)
- Labels:
  - `validator`: validator address
  - `signer`: signer address
  - `name`: validator name
- Usage: Monitor validator jail status

### hl_validator_active_status
- Type: Gauge
- Description: Active status of each validator (1 if active, 0 if not active)
- Labels:
  - `validator`: validator address
  - `signer`: signer address
  - `name`: validator name
- Usage: Monitor validator active status

### hl_validator_count
- Type: Gauge
- Description: Total number of validators in the network
- Usage: Track validator set size

### hl_validator_rtt
- Type: Gauge
- Description: Round-trip time (RTT) to validator nodes in milliseconds
- Labels:
  - `validator`: validator address
  - `moniker`: validator name/moniker
  - `ip`: validator node IP address
- Usage: Monitor validator node connectivity and network latency
