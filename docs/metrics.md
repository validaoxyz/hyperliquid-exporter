# Hyperliquid Exporter Metrics Reference

## HyperCore Metrics

| Metric | Type | Labels | Description | Requirements |
|--------|------|--------|-------------|--------------|
| `hl_core_block_height` | Gauge | - | Current block height | - |
| `hl_core_blocks_processed_total` | Counter | - | Total blocks processed | `--replica-metrics` |
| `hl_core_block_time_milliseconds` | Histogram | `state_type` | Time between blocks in milliseconds | - |
| `hl_core_latest_block_time` | Gauge | - | Unix timestamp of latest block | - |
| `hl_core_last_processed_round` | Gauge | - | Last processed consensus round | `--replica-metrics` |
| `hl_core_last_processed_time` | Gauge | - | Unix timestamp of last processed block | `--replica-metrics` |
| `hl_core_operations_per_block` | Histogram | - | Distribution of operations per block | `--replica-metrics` |
| `hl_core_operations_total` | Counter | `type`, `category` | Total individual operations by type and category | `--replica-metrics` |
| `hl_core_orders_total` | Counter | - | Total orders placed | `--replica-metrics` |
| `hl_core_rounds_processed_total` | Counter | - | Total consensus rounds processed | `--replica-metrics` |
| `hl_core_tx_per_block` | Histogram | - | Distribution of transactions per block | `--replica-metrics` |
| `hl_core_tx_total` | Counter | `type` | Total transactions/actions by type | `--replica-metrics` |

Metrics marked with `--replica-metrics` also require hl-node to be running with `--replica-cmds-style actions-and-responses`.

## EVM Metrics

| Metric | Type | Labels | Description | Requirements |
|--------|------|--------|-------------|--------------|
| `hl_evm_account_count` | Gauge | - | Total number of EVM accounts | - |
| `hl_evm_base_fee_gwei` | Gauge | `block_type`* | Current base fee in Gwei | - |
| `hl_evm_base_fee_gwei_distribution` | Histogram | `block_type`* | Distribution of base fees (0-1000+ Gwei buckets) | - |
| `hl_evm_block_height` | Gauge | - | Current EVM block height | - |
| `hl_evm_block_time_milliseconds` | Histogram | - | Time between EVM blocks | - |
| `hl_evm_contract_create_total` | Counter | `block_type`* | Total contract creations | - |
| `hl_evm_contract_tx_total` | Counter | `contract_address`, `contract_name`, `is_token`, `type`, `symbol`, `block_type`* | Contract interactions by address | - |
| `hl_evm_gas_limit` | Gauge | `block_type`* | Gas limit per block | - |
| `hl_evm_gas_limit_distribution` | Histogram | - | Distribution of gas limits across blocks | - |
| `hl_evm_gas_used` | Gauge | `block_type`* | Gas used per block | - |
| `hl_evm_gas_util` | Gauge | `block_type`* | Gas utilization percentage | - |
| `hl_evm_high_gas_limit_blocks_total` | Counter | `threshold` | Count of high gas limit blocks by threshold | - |
| `hl_evm_last_high_gas_block_height` | Gauge | - | Height of last high gas block | - |
| `hl_evm_last_high_gas_block_limit` | Gauge | - | Gas limit of last high gas block | - |
| `hl_evm_last_high_gas_block_time` | Gauge | - | Unix timestamp of last high gas block | - |
| `hl_evm_last_high_gas_block_used` | Gauge | - | Gas used in last high gas block | - |
| `hl_evm_latest_block_time` | Gauge | - | Unix timestamp of latest EVM block | - |
| `hl_evm_max_gas_limit_seen` | Gauge | - | Maximum gas limit observed | - |
| `hl_evm_max_priority_fee` | Gauge | `block_type`* | Current maximum priority fee | - |
| `hl_evm_max_priority_fee_gwei_distribution` | Histogram | `block_type`* | Distribution of priority fees (0-1000+ Gwei buckets) | - |
| `hl_evm_tx_per_block` | Histogram | `block_type`* | Distribution of transactions per block | - |
| `hl_evm_tx_type_total` | Counter | `type`, `block_type`* | Transaction counts by type (Legacy, EIP-1559, etc.) | - |


## Machine Metrics

| Metric | Type | Labels | Description | Requirements |
|--------|------|--------|-------------|--------------|
| `hl_metal_apply_duration` | Gauge | `state_type` | Current block application duration | - |
| `hl_metal_apply_duration_milliseconds` | Histogram | `state_type` | Distribution of block application durations | - |
| `hl_metal_last_processed_round` | Gauge | - | Last round processed by replica monitor | `--replica-metrics` |
| `hl_metal_last_processed_time` | Gauge | - | Unix timestamp of last processing by replica monitor | `--replica-metrics` |
| `hl_metal_parse_duration` | Gauge | - | Duration of replica transaction parsing in seconds | `--replica-metrics` |

## P2P Network Metrics

| Metric | Type | Labels | Description | Requirements |
|--------|------|--------|-------------|--------------|
| `hl_p2p_non_val_peer_connections` | Gauge | `verified` | Number of non-validator peer connections by verification status | gossip_rpc logs |
| `hl_p2p_non_val_peers_total` | Gauge | - | Total number of connected non-validator peers | gossip_rpc logs |

P2P metrics track non-validator peer connections only

## Software Version Metrics

| Metric | Type | Labels | Description | Requirements |
|--------|------|--------|-------------|--------------|
| `hl_software_up_to_date` | Gauge | - | Whether software is up to date (0=outdated, 1=current) | - |
| `hl_software_version` | Gauge | `date`, `commit` | Software version info (always 1, version in labels) | - |


### Consensus metrics

### Basic Validator Metrics

| Metric | Type | Labels | Description | Requirements |
|--------|------|--------|-------------|--------------|
| `hl_consensus_active_stake` | Gauge | - | Total stake of active validators | - |
| `hl_consensus_inactive_stake` | Gauge | - | Total stake of inactive validators | - |
| `hl_consensus_jailed_stake` | Gauge | - | Total stake of jailed validators | - |
| `hl_consensus_not_jailed_stake` | Gauge | - | Total stake of non-jailed validators | - |
| `hl_consensus_proposer_count_total` | Counter | `validator`, `signer`, `name` | Blocks proposed per validator | - |
| `hl_consensus_total_stake` | Gauge | - | Total network stake | - |
| `hl_consensus_validator_active_status` | Gauge | `validator`, `signer`, `name` | Validator active status (0=inactive, 1=active) | - |
| `hl_consensus_validator_count` | Gauge | - | Total number of validators | - |
| `hl_consensus_validator_jailed_status` | Gauge | `validator`, `signer`, `name` | Validator jail status (0=not jailed, 1=jailed) | - |
| `hl_consensus_validator_rtt` | Gauge | `validator`, `moniker`, `ip` | Validator response time in milliseconds | Requires RTT monitoring enabled (auto-enabled on validator nodes) |
| `hl_consensus_validator_stake` | Gauge | `validator`, `signer`, `moniker` | Stake amount per validator | - |

The above are available to all node types, while the below metrics require access to consensus (running a validator)

| Metric | Type | Labels | Description | Requirements |
|--------|------|--------|-------------|--------------|
| `hl_consensus_current_round` | Gauge | - | Current consensus round from block messages | Validator node |
| `hl_consensus_heartbeat_ack_delay_ms` | Histogram | - | heartbeat acknowledgement delays | Validator node |
| `hl_consensus_heartbeat_ack_received_total` | Counter | `from_validator`, `to_validator`, `from_name`, `to_name` | Heartbeat acknowledgments between validator pairs | Validator node |
| `hl_consensus_heartbeat_sent_total` | Counter | `validator`, `signer`, `name` | Total heartbeats sent by validators | Validator node |
| `hl_consensus_heartbeat_status` | Gauge | `validator`, `signer`, `name`, `status_type` | Heartbeat health metrics (status_type: since_last_success, last_ack_duration) | Validator node |
| `hl_consensus_validator_connectivity` | Gauge | `validator`, `peer`, `validator_name`, `peer_name` | Real-time connectivity status (0=disconnected, 1=connected) | Validator node |
| `hl_consensus_vote_round` | Gauge | `validator`, `signer`, `name` | Last voting round for each validator | Validator node |
| `hl_consensus_vote_time_diff_seconds` | Gauge | `validator`, `signer`, `name` | Seconds since validator's last vote | Validator node |
| `hl_consensus_qc_participation_rate` | Gauge | `validator`, `signer`, `name` | Percentage of recent blocks where validator signed QC (100 blocks sliding) | Validator node |
| `hl_consensus_qc_signatures_total` | Counter | `validator`, `signer`, `name` | Cumulative QC signatures by each validator | Validator node |
| `hl_consensus_qc_size` | Histogram | - | Distribution of QC signer counts per block | Validator node |
| `hl_consensus_rounds_per_block` | Gauge | - | Average rounds needed to produce a block | Validator node |
| `hl_consensus_tc_blocks_total` | Counter | `proposer`, `signer`, `name` | Total blocks proposed containing timeout certificates | Validator node |
| `hl_consensus_tc_participation_total` | Counter | `validator`, `signer`, `name` | Total timeout votes sent by each validator | Validator node |
| `hl_consensus_tc_size` | Histogram | - | Distribution of timeout vote counts in TC blocks | Validator node |
| `hl_consensus_validator_latency_seconds` | Gauge | `validator`, `signer`, `name` | Current network latency to validator in seconds | Validator node with latency monitoring |
| `hl_consensus_validator_latency_round` | Gauge | `validator`, `signer`, `name` | Consensus round when latency was last measured | Validator node with latency monitoring |
| `hl_consensus_validator_latency_ema_seconds` | Gauge | `validator`, `signer`, `name` | Exponential moving average of validator latency | Validator node with latency monitoring |

## Label Definitions

### Common Labels
- `validator`: Validator address
- `signer`: Signer address
- `name`: Human-readable validator name
- `type`: Transaction or operation type
- `category`: Operation category (for operations_total)
- `block_type`: EVM block type (standard/high/other) - requires flag


## Metric Requirements

### Replica Metrics
Metrics that require `--replica-metrics` flag AND hl-node running with `--replica-cmds-style actions-and-responses`:
- All `hl_core_tx_*` metrics
- All `hl_core_operations_*` metrics
- `hl_core_orders_total`
- `hl_core_*_processed_*` metrics
- `hl_metal_parse_duration`
- `hl_metal_last_processed_*` metrics

### Validator Node Metrics
Metrics that require running on a validator node with access to consensus/status logs:
- All `hl_consensus_*` metrics except basic info obtained through api.hyperliquid.xyz/info