# Breaking Changes - Hyperliquid Exporter v2.0.0

This document provides a comprehensive list of all breaking changes in v2.0.0. 

## Metric Name Changes

### Core Metrics Renamed
| Old Metric Name | New Metric Name |
|----------------|-----------------|
| `hl_block_height` | `hl_core_block_height` |
| `hl_block_time_milliseconds` | `hl_core_block_time_milliseconds` |
| `hl_latest_block_time` | `hl_core_latest_block_time` |
| `hl_apply_duration` | `hl_metal_apply_duration` |
| `hl_apply_duration_milliseconds` | `hl_metal_apply_duration_milliseconds` |

### Consensus Metrics Renamed
| Old Metric Name | New Metric Name |
|----------------|-----------------|
| `hl_proposer_count_total` | `hl_consensus_proposer_count_total` |
| `hl_validator_count` | `hl_consensus_validator_count` |
| `hl_validator_jailed_status` | `hl_consensus_validator_jailed_status` |
| `hl_validator_stake` | `hl_consensus_validator_stake` |
| `hl_validator_active_status` | `hl_consensus_validator_active_status` |
| `hl_validator_rtt` | `hl_consensus_validator_rtt` |
| `hl_total_stake` | `hl_consensus_total_stake` |
| `hl_jailed_stake` | `hl_consensus_jailed_stake` |
| `hl_not_jailed_stake` | `hl_consensus_not_jailed_stake` |
| `hl_active_stake` | `hl_consensus_active_stake` |
| `hl_inactive_stake` | `hl_consensus_inactive_stake` |

## CLI Flag Changes

### Flags Removed
- `--enable-prom` - Prometheus endpoint is now always enabled
- `--disable-prom` - Prometheus endpoint is now always enabled

### Flags Renamed
- `--enable-otlp` → `--otlp`
- `--evm` → `--evm-metrics`

### Default Value Changes
- `--evm-metrics`: Default changed from `true` to `false`
- `--otlp-endpoint`: Default value removed (was "otel.hyperliquid.validao.xyz")

### New Required Parameters
- When using `--otlp`, the `--otlp-endpoint` is now required (no default)

## Grafana Dashboard Updates

All panels using old metric names must be updated:

1. **Block Height Panel**
   - Old: `hl_block_height`
   - New: `hl_core_block_height`

2. **Validator Stake Panel**
   - Old: `hl_validator_stake`
   - New: `hl_consensus_validator_stake`

3. **Block Time Panel**
   - Old: `histogram_quantile(0.95, hl_block_time_milliseconds)`
   - New: `histogram_quantile(0.95, hl_core_block_time_milliseconds)`

## Alert Rule Updates

Example alert rule migration:

```yaml
# OLD
- alert: ValidatorJailed
  expr: hl_validator_jailed_status == 1
  
# NEW
- alert: ValidatorJailed
  expr: hl_consensus_validator_jailed_status == 1
```

## New Metrics Available

### Consensus Monitoring
- `hl_consensus_vote_round` - Last vote round for each validator
- `hl_consensus_vote_time_diff_seconds` - Time since last vote
- `hl_consensus_current_round` - Current consensus round
- `hl_consensus_heartbeat_sent_total` - Heartbeats sent
- `hl_consensus_heartbeat_ack_received_total` - Heartbeat acknowledgments
- `hl_consensus_heartbeat_ack_delay_ms` - Heartbeat delays
- `hl_consensus_validator_connectivity` - Peer connectivity status
- `hl_consensus_heartbeat_status` - Heartbeat health
- `hl_consensus_qc_signatures_total` - QC signatures by validator
- `hl_consensus_qc_participation_rate` - QC participation percentage
- `hl_consensus_qc_size` - QC size distribution
- `hl_consensus_tc_blocks_total` - TC blocks by proposer
- `hl_consensus_tc_participation_total` - TC votes by validator
- `hl_consensus_tc_size` - TC size distribution
- `hl_consensus_blocks_after_timeout_ratio` - TC block ratio
- `hl_consensus_rounds_per_block` - Round efficiency
- `hl_consensus_qc_round_lag` - QC round lag
- `hl_consensus_monitor_last_processed` - Monitor health
- `hl_consensus_monitor_lines_processed_total` - Lines processed
- `hl_consensus_monitor_errors_total` - Monitor errors
- `hl_consensus_validator_latency_seconds` - Validator latency
- `hl_consensus_validator_latency_round` - Latency measurement round
- `hl_consensus_validator_latency_ema_seconds` - EMA latency
- `hl_timeout_rounds_total` - Timeout rounds by cause

### HyperCore Tx Processing
- `hl_core_tx_total` - Total transactions by type
- `hl_core_orders_total` - Total orders
- `hl_core_operations_total` - Individual operations
- `hl_core_blocks_processed` - Blocks processed
- `hl_core_rounds_processed` - Rounds processed
- `hl_core_tx_per_block` - Transaction distribution
- `hl_core_operations_per_block` - Operations distribution
- `hl_core_last_processed_round` - Last processed round
- `hl_core_last_processed_time` - Last processed time
- `hl_metal_parse_duration` - Parsing duration
- `hl_metal_last_processed_round` - Metal processing round
- `hl_metal_last_processed_time` - Metal processing time

### HyperEVM
- `hl_evm_latest_block_time` - EVM block timestamp
- `hl_evm_block_time_milliseconds` - EVM block time distribution
- `hl_evm_tx_type_total` - Transactions by type (0,1,2)
- `hl_evm_contract_create_total` - Contract creations
- `hl_evm_contract_tx_total` - Contract interactions
- `hl_evm_account_count` - Total accounts
- `hl_evm_tx_per_block` - Transaction distribution
- `hl_evm_base_fee_gwei` - Current base fee
- `hl_evm_base_fee_gwei_distribution` - Base fee distribution
- `hl_evm_max_priority_fee` - Priority fees
- `hl_evm_max_priority_fee_gwei_distribution` - Priority fee distribution
- `hl_evm_gas_used` - Gas consumption
- `hl_evm_gas_limit` - Gas limits
- `hl_evm_gas_util` - Gas utilization
- `hl_evm_gas_limit_distribution` - Gas limit distribution
- `hl_evm_high_gas_limit_blocks_total` - High gas blocks
- `hl_evm_last_high_gas_block_*` - Last high gas block info (4 metrics)
- `hl_evm_max_gas_limit_seen` - Maximum gas limit

### P2P metrics
- `hl_p2p_non_val_peer_connections` - Peer connections
- `hl_p2p_non_val_peers_total` - Total peers

### System Monitoring 
- `hl_go_heap_objects` - Heap objects
- `hl_go_heap_inuse_mb` - Heap usage
- `hl_go_heap_idle_mb` - Idle heap
- `hl_go_sys_mb` - System memory
- `hl_go_num_goroutines` - Goroutines

## Configuration Migration

### Old Configuration
```bash
./hl_exporter start --evm --enable-otlp
```

### New Configuration
```bash
./hl_exporter start --evm-metrics --otlp --otlp-endpoint "your-endpoint.com"
```

## Migration Checklist

- [ ] Update all Prometheus queries to use new metric names
- [ ] Update Grafana dashboards with new metric names
- [ ] Update alerting, recording rules with new metric names
- [ ] Update run flags