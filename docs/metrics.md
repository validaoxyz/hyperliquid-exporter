# Hyperliquid Exporter Metrics Reference

> **Legend - counters vs gauge-typed `_total`:** every metric typed Counter in the tables below is a real Prometheus counter and `rate()`/`increase()` are valid (OTel-path and promauto-path alike). The exceptions are the Gauge-typed families whose names end in `_total`: `hl_tokio_task_*_total`, `hl_p2p_lz4_*_total`, `hl_node_subsystem_samples_total`, `hl_rocksdb_write_stalls_total`, and `hl_node_process_cpu_seconds_total` mirror cumulative-since-source-restart values (reset when hl-node/hl-visor restarts; use `delta()`/`deriv()`), while `hl_node_observed_runs`, `hl_p2p_peers` and `hl_node_replay_events` are point-in-time counts that can decrease.

## HyperCore Metrics

| Metric | Type | Labels | Description | Requirements |
|--------|------|--------|-------------|--------------|
| `hl_core_block_height` | Gauge | - | Current block height. In dual-state mode this tracks the **fast** state only (slow state never updates it). | - |
| `hl_core_blocks_processed_total` | Counter | - | Total blocks processed | `--replica-metrics` |
| `hl_core_block_time_milliseconds` | Histogram | `state_type` | Time between blocks in milliseconds. `state_type` (`fast`/`slow`) is present only in dual-state mode; on the legacy single `block_times` path the metric is emitted without it. | - |
| `hl_core_latest_block_time` | Gauge | - | Unix timestamp of latest block | - |
| `hl_core_last_processed_round` | Gauge | - | Last processed consensus round | `--replica-metrics` |
| `hl_core_last_processed_time` | Gauge | - | Unix timestamp of last processed block | `--replica-metrics` |
| `hl_core_operations_per_block` | Histogram | - | Distribution of operations per block | `--replica-metrics` |
| `hl_core_operations_total` | Counter | `type`, `category` | Total individual operations by type and category | `--replica-metrics` |
| `hl_core_orders_per_block` | Histogram | - | Distribution of orders per block | `--replica-metrics` |
| `hl_core_orders_total` | Counter | - | Total orders placed | `--replica-metrics` |
| `hl_core_tx_per_block` | Histogram | - | Distribution of transactions per block | `--replica-metrics` |
| `hl_core_tx_total` | Counter | `type` | Total transactions/actions by type | `--replica-metrics` |
| `hl_core_round_advance` | Histogram | - | round - parent_round per block. 1 on a healthy chain; bucket counts above 1 are timed-out/skipped rounds. | `--replica-metrics` |
| `hl_core_hardfork_version` | Gauge | - | Hardfork version stamped on the latest block; steps up when a hardfork activates. | `--replica-metrics` |

Metrics marked with `--replica-metrics` also require hl-node to be running with `--replica-cmds-style actions-and-responses`.

## EVM Metrics

| Metric | Type | Labels | Description | Requirements |
|--------|------|--------|-------------|--------------|
| `hl_evm_base_fee_gwei` | Gauge | `block_type` | Current base fee in Gwei | - |
| `hl_evm_base_fee_gwei_distribution` | Histogram | `block_type` | Distribution of base fees (0-1000+ Gwei buckets) | - |
| `hl_evm_block_height` | Gauge | - | Current EVM block height | - |
| `hl_evm_block_time_milliseconds` | Histogram | - | Time between EVM blocks | - |
| `hl_evm_contract_create_total` | Counter | `block_type` | Total contract creations | - |
| `hl_evm_contract_tx_total` | Counter | `contract_address`, `contract_name`, `is_token`, `type`, `symbol`, `block_type` | Contract interactions by address. `--contract-metrics-limit` hard-caps the series: the first N distinct addresses keep their own series, later ones share `contract_address="other"`. `symbol` is present only for tokens. | `--contract-metrics` |
| `hl_evm_gas_limit` | Gauge | `block_type` | Gas limit per block | - |
| `hl_evm_gas_used` | Gauge | `block_type` | Gas used per block | - |
| `hl_evm_gas_util` | Gauge | `block_type` | Gas utilization fraction (0-1, gasUsed/gasLimit) | - |
| `hl_evm_latest_block_time` | Gauge | - | Unix timestamp of latest EVM block | - |
| `hl_evm_max_priority_fee_gwei` | Gauge | `block_type` | Current maximum priority fee (Gwei) | - |
| `hl_evm_max_priority_fee_gwei_distribution` | Histogram | `block_type` | Distribution of priority fees (0-1000+ Gwei buckets) | - |
| `hl_evm_tx_per_block` | Histogram | `block_type` | Distribution of transactions per block | - |
| `hl_evm_tx_type_total` | Counter | `type`, `block_type` | Transaction counts by type (Legacy, Eip1559, Eip2930, Eip4844) | - |


## Machine Metrics

| Metric | Type | Labels | Description | Requirements |
|--------|------|--------|-------------|--------------|
| `hl_metal_apply_duration_milliseconds` | Histogram | `state_type` | Distribution of block application durations | - |
| `hl_metal_parse_duration` | Gauge | - | Duration of replica transaction parsing in seconds | `--replica-metrics` |

## P2P Network Metrics

| Metric | Type | Labels | Description | Requirements |
|--------|------|--------|-------------|--------------|
| `hl_p2p_non_val_peer_connections` | Gauge | `verified` | Number of non-validator peer connections by verification status | gossip_rpc logs |
| `hl_p2p_non_val_peers_total` | Gauge | - | Total number of connected non-validator peers | gossip_rpc logs |
| `hl_p2p_non_val_connections` | Gauge | - | Sum of per-peer connection counts (a peer can hold several links) | gossip_rpc logs |

P2P metrics track non-validator peer connections only

## Software Version Metrics

| Metric | Type | Labels | Description | Requirements |
|--------|------|--------|-------------|--------------|
| `hl_software_up_to_date` | Gauge | - | 1 if the local hl-visor build matches the latest hl-visor on the CDN (set `BINARY_HOME` if the visor is not in `$HOME`). UNSET (absent, not 0) until both the local visor probe and the upstream ETag check have succeeded at least once. | - |
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
| `hl_consensus_validator_rtt` | Gauge | `validator`, `moniker`, `ip` | Validator response time in milliseconds. Note: the `ip` label exposes the validator IP map to anyone who can scrape `/metrics`. | `--validator-rtt` (plain opt-in flag; no auto-detection) |
| `hl_consensus_validator_stake` | Gauge | `validator`, `signer`, `name` | Stake amount per validator | - |
| `hl_consensus_validator_recent_blocks` | Gauge | `validator`, `signer`, `name` | nRecentBlocks from validatorSummaries; 0 on a jailed or stalled validator | - |
| `hl_consensus_validator_commission_rate` | Gauge | `validator`, `signer`, `name` | Commission as a fraction (0.04 = 4%) | - |
| `hl_consensus_validator_unjailable_after_seconds` | Gauge | `validator`, `signer`, `name` | Unix time after which a jailed validator may unjailSelf; series exists only while jailed. Countdown = value - time(). | - |
| `hl_consensus_validator_uptime_fraction` | Gauge | `validator`, `signer`, `name`, `period` | uptimeFraction per period (day/week/month) | - |
| `hl_consensus_validator_predicted_apr` | Gauge | `validator`, `signer`, `name`, `period` | predictedApr per period, as a fraction | - |

The above are available to all node types. Note that the stake/status series (`hl_consensus_*_stake`, `hl_consensus_total_stake`, `hl_consensus_validator_stake`, `hl_consensus_validator_count`, `hl_consensus_validator_active_status`, `hl_consensus_validator_jailed_status`, `hl_consensus_proposer_count_total`) are **network-wide**, fetched from `api.hyperliquid.xyz` (`validatorSummaries`) - so they are identical on every node and not node-local. The below metrics require access to consensus (running a validator).

| Metric | Type | Labels | Description | Requirements |
|--------|------|--------|-------------|--------------|
| `hl_consensus_current_round` | Gauge | - | Current consensus round from block messages | Validator node |
| `hl_consensus_heartbeat_ack_delay_ms` | Histogram | - | heartbeat acknowledgement delays | Validator node |
| `hl_consensus_heartbeat_ack_received_total` | Counter | `to_validator`, `to_name` | Heartbeat acks per acking peer. Only outgoing heartbeats are observed, so the origin is always the local validator (the old from_* labels were constants and were dropped). | Validator node |
| `hl_consensus_heartbeat_sent_total` | Counter | `validator`, `signer`, `name` | Total heartbeats sent by validators | Validator node |
| `hl_consensus_heartbeat_status` | Gauge | `validator`, `signer`, `name`, `status_type` | Heartbeat health metrics (status_type: since_last_success, last_ack_duration) | Validator node |
| `hl_consensus_validator_connectivity` | Gauge | `validator`, `peer`, `validator_name`, `peer_name` | 0 (or series present) = pair currently disconnected; the series is removed when the pair reconnects (value 1 is never emitted). Alert on `==0` / presence, not `==1`. | Validator node |
| `hl_consensus_vote_round` | Gauge | `validator`, `signer`, `name` | Last voting round for each validator | Validator node |
| `hl_consensus_vote_time_diff_seconds` | Gauge | `validator`, `signer`, `name` | Age of the last observed vote, computed at scrape time. Climbs while a validator is silent, so `> N` is a usable stall alert. Series are removed after 24h without votes. | Validator node |
| `hl_consensus_qc_participation_percent` | Gauge | `validator`, `signer`, `name` | Percentage of recent blocks where validator signed QC (100 blocks sliding) | Validator node |
| `hl_consensus_qc_signatures_total` | Counter | `validator`, `signer`, `name` | Cumulative QC signatures by each validator | Validator node |
| `hl_consensus_qc_size` | Histogram | - | Distribution of QC signer counts per block | Validator node |
| `hl_consensus_rounds_per_block` | Gauge | - | Round advance for the most recent block (single sample, ~1 on a healthy chain), not a moving average. Prefer `hl_core_round_advance` (histogram, replica-sourced) for rates. | Validator node |
| `hl_consensus_qc_round_lag` | Gauge | - | block round - QC round of the most recent block (single sample, 1 on a healthy chain; not an average) | Validator node |
| `hl_consensus_tc_blocks_total` | Counter | `validator`, `signer`, `name` | Total blocks proposed containing timeout certificates (`validator` is the proposer) | Validator node |
| `hl_consensus_tc_participation_total` | Counter | `validator`, `signer`, `name` | Total timeout votes sent by each validator | Validator node |
| `hl_consensus_tc_size` | Histogram | - | Distribution of timeout vote counts in TC blocks | Validator node |
| `hl_consensus_validator_latency_seconds` | Gauge | `validator`, `signer`, `name` | Per-validator raw latency (latest sample). Reconciled per tick: validators whose latency files go stale (>2h) drop out instead of freezing. | Validator node with latency files |
| `hl_consensus_validator_latency_round` | Gauge | `validator`, `signer`, `name` | Consensus round when latency was last measured | Validator node with latency monitoring |
| `hl_consensus_validator_latency_ema_seconds` | Gauge | `validator`, `signer`, `name` | Exponential moving average of heartbeat-ack latency, the quantity jail votes use. Headroom recipe: `hl_consensus_validator_latency_ema_seconds / on() group_left hl_node_jailing_threshold_seconds` (jail risk approaches 1.0). | Validator node with latency monitoring |
| `hl_consensus_validator_jailed_local` | Gauge | `validator` | 1 per validator the local node currently reports jailed (status stream, ~2min cadence, no API dependency); series removed on unjail | Validator node |

## Visor / Sync

| Metric | Type | Labels | Description | Requirements |
|--------|------|--------|-------------|--------------|
| `hl_visor_height` | Gauge | - | Latest applied block height | - |
| `hl_visor_initial_height` | Gauge | - | Height at current visor start | - |
| `hl_visor_blocks_applied` | Gauge | - | height − initial_height | - |
| `hl_visor_freeze_abci_height` | Gauge | - | Replay floor | - |
| `hl_visor_blocks_above_freeze` | Gauge | - | visor_height − freeze_height | - |
| `hl_visor_consensus_ahead_of_wall_seconds` | Gauge | - | consensus_time − wall_clock_time | - |
| `hl_visor_reference_lag_seconds` | Gauge | - | Reference-node lag when populated | - |
| `hl_visor_reference_lag_populated` | Gauge | - | 1 if the visor sample carried a `reference_lag` field, else 0 | - |
| `hl_visor_scheduled_freeze_height` | Gauge | - | Scheduled freeze height, 0 if none | - |
| `hl_visor_last_observation_age_seconds` | Gauge | - | Age of latest visor sample | - |
| `hl_node_snapshot_last_height` | Gauge | - | Last successful snapshot height | - |
| `hl_node_snapshot_last_age_seconds` | Gauge | - | Seconds since last snapshot | - |
| `hl_node_snapshot_known_count` | Gauge | - | Snapshot sentinels retained | - |
| `hl_evm_db_checkpoint_height` | Gauge | `tier` | EVM DB checkpoint per tier (fast/slow) | - |
| `hl_evm_db_checkpoint_lag_blocks` | Gauge | - | fast − slow checkpoint | - |
| `hl_node_observed_runs` | Gauge | - | hl-node runs observed via replica_cmds dirs. Point-in-time count of currently retained run dirs that DROPS when those dirs are pruned, so do not `rate()`/`increase()`. | - |
| `hl_node_observed_run_start_seconds` | Gauge | - | Newest run-dir mtime | - |

## Node Process

| Metric | Type | Labels | Description | Requirements |
|--------|------|--------|-------------|--------------|
| `hl_node_process_up` | Gauge | `process` | 1 if alive in /proc, 0 otherwise | Linux only |
| `hl_node_process_start_time_seconds` | Gauge | `process` | Process start | Linux only |
| `hl_node_process_cpu_seconds_total` | Gauge | `process` | Cumulative CPU time | Linux only |
| `hl_node_process_rss_bytes` | Gauge | `process` | VmRSS | Linux only |
| `hl_node_process_virt_bytes` | Gauge | `process` | VmSize | Linux only |
| `hl_node_process_threads` | Gauge | `process` | OS thread count | Linux only |
| `hl_node_process_open_fds` | Gauge | `process` | Open FD count | Linux only |

## Subsystem Latency

These expose node-precomputed per-node quantiles as gauges with no `_bucket` series - valid only per-instance; do NOT `avg()`/`quantile()` them across nodes or steps.

| Metric | Type | Labels | Description | Requirements |
|--------|------|--------|-------------|--------------|
| `hl_node_subsystem_latency_mean_seconds` | Gauge | `subsystem` | Windowed mean | - |
| `hl_node_subsystem_latency_median_seconds` | Gauge | `subsystem` | Windowed median | - |
| `hl_node_subsystem_latency_p90_seconds` | Gauge | `subsystem` | p90 | - |
| `hl_node_subsystem_latency_p95_seconds` | Gauge | `subsystem` | p95 | - |
| `hl_node_subsystem_latency_max_seconds` | Gauge | `subsystem` | Windowed max | - |
| `hl_node_subsystem_latency_stddev_seconds` | Gauge | `subsystem` | Std dev | - |
| `hl_node_subsystem_work_fraction` | Gauge | `subsystem` | Fraction of wall-clock doing work | - |
| `hl_node_subsystem_samples_total` | Gauge | `subsystem` | Cumulative samples | - |
| `hl_node_subsystem_latency_lifetime_mean_seconds` | Gauge | `subsystem` | Mean since source start; windowed mean drifting above it = degradation | - |

## Critical Messages

| Metric | Type | Labels | Description | Requirements |
|--------|------|--------|-------------|--------------|
| `hl_node_bugs` | Gauge | `source` | `bug!` events since source start | - |
| `hl_node_crits` | Gauge | `source` | `crit!` events since source start | - |
| `hl_node_crit_locations` | Gauge | `source` | Distinct (file, line) sites that fired | - |
| `hl_node_critical_messages_base_time_seconds` | Gauge | `source` | Cycle start (source process start) | - |

## P2P / Peer Traffic

| Metric | Type | Labels | Description | Requirements |
|--------|------|--------|-------------|--------------|
| `hl_p2p_peer_traffic` | Gauge | `ip`, `direction` | Per-peer raw hl-node tcp_traffic value (comparative only, not bytes; ~0..1 observed), top-16 + `ip="other"` | - |
| `hl_p2p_total_traffic` | Gauge | `direction` | Raw hl-node tcp_traffic value (comparative only, not bytes; ~0..1 observed), summed across peers | - |
| `hl_p2p_peer_count` | Gauge | `direction` | Distinct peers in sample | - |
| `hl_p2p_sample_age_seconds` | Gauge | - | Age of latest tcp_traffic sample | - |
| `hl_node_parent_peer_info` | Gauge | `ip` | 1 for current parent peer | - |
| `hl_node_parent_peer_traffic` | Gauge | - | Inbound raw hl-node tcp_traffic value from parent (comparative only, not bytes; ~0..1 observed) | - |
| `hl_node_parent_peer_tenure_seconds` | Gauge | - | Time current parent has held role | - |
| `hl_node_parent_peer_switches_total` | Counter | - | Cumulative parent changes | - |
| `hl_p2p_gossip_events_total` | Counter | `event_type` | Gossip events by tag | - |
| `hl_p2p_tcp_connections` | Gauge | `port`, `state` | TCP state counts per listening port | Linux only |

## Peer Set

**Which peer-count metric?** Several metrics count peers using different denominators:
- `hl_p2p_peers` - unique peer IPs with positive traffic in the latest tcp_traffic sample.
- `hl_p2p_peer_count{direction}` - peers in the latest sample per direction; INCLUDES idle peers at value=0, so `>=` `hl_p2p_peers`.
- `hl_p2p_unique_peers_seen{window}` - rolling-window unique peer IPs (5m/1h/24h).
- `hl_p2p_non_val_peers_total` - gossip non-validator peers.

| Metric | Type | Labels | Description | Requirements |
|--------|------|--------|-------------|--------------|
| `hl_p2p_unique_peers_seen` | Gauge | `window` | Distinct peer IPs seen in window (5m/1h/24h) | - |
| `hl_p2p_peers` | Gauge | - | Unique peer IPs with positive traffic in the latest tcp_traffic sample ("right now"). Point-in-time count (peers in the latest sample) that can DECREASE, so do not `rate()`/`increase()`. | - |
| `hl_p2p_peers_added_total` | Counter | - | Cumulative new peers since exporter start. In-process monotonic (resets only on exporter restart), so `rate()`/`increase()` are valid. | - |
| `hl_p2p_peers_evicted_total` | Counter | - | Cumulative evictions since exporter start. In-process monotonic (resets only on exporter restart), so `rate()`/`increase()` are valid. | - |
| `hl_p2p_peer_last_seen_seconds` | Gauge | `ip` | Last-seen Unix timestamp per known peer IP | `--per-peer-metrics` |
| `hl_p2p_peer_first_seen_seconds` | Gauge | `ip` | First-seen Unix timestamp per known peer IP | `--per-peer-metrics` |

## Disk

| Metric | Type | Labels | Description | Requirements |
|--------|------|--------|-------------|--------------|
| `hl_node_disk_used_bytes` | Gauge | - | NODE_HOME tree size | - |
| `hl_node_disk_free_bytes` | Gauge | - | Filesystem bytes available | - |
| `hl_node_disk_total_bytes` | Gauge | - | Filesystem total bytes | - |
| `hl_node_disk_subdir_bytes` | Gauge | `subdir` | Tracked NODE_HOME subdir sizes | - |

## Info Probe

| Metric | Type | Labels | Description | Requirements |
|--------|------|--------|-------------|--------------|
| `hl_info_endpoint_up` | Gauge | - | 1 if probe returned 200 + non-empty body | `--probe-info-endpoint` |
| `hl_info_endpoint_latency_seconds` | Histogram | - | Probe latency | `--probe-info-endpoint` |
| `hl_info_endpoint_last_success_seconds` | Gauge | - | Last successful probe | `--probe-info-endpoint` |
| `hl_info_endpoint_failures_total` | Counter | - | Cumulative probe failures | `--probe-info-endpoint` |
| `hl_info_exchange_status_delta_seconds` | Gauge | - | local_wall_clock - exchangeStatus.time from the node's own info endpoint. Sustained growth = the node serves stale exchange state. Registered on first successful parse. | `--probe-info-endpoint` |

The whole info-probe family registers only when `--probe-info-endpoint` is on: with the probe off the metrics are ABSENT, not 0.

## Extended: LZ4

| Metric | Type | Labels | Description | Requirements |
|--------|------|--------|-------------|--------------|
| `hl_p2p_lz4_compression_ratio` | Gauge | `ip`, `direction` | Per-peer ratio (compressed÷uncompressed) | `--extended-metrics` |
| `hl_p2p_lz4_bytes_total` | Gauge | `ip`, `direction` | Per-peer compressed bytes since source start | `--extended-metrics` |
| `hl_p2p_lz4_packets_total` | Gauge | `ip`, `direction` | Per-peer compressed packets since source start | `--extended-metrics` |
| `hl_p2p_lz4_global_ratio` | Gauge | - | Global ratio | `--extended-metrics` |
| `hl_p2p_lz4_global_bytes_total` | Gauge | - | Global compressed bytes since source start | `--extended-metrics` |
| `hl_p2p_lz4_global_packets_total` | Gauge | - | Global compressed packets since source start | `--extended-metrics` |
| `hl_tcp_lz4_latency_seconds` | Gauge | `direction`, `port`, `quantile` | Per-port lz4 latency. `quantile` ∈ {p50, p90, p95, max, mean, std_dev} (the `work_frac` duty-cycle moved to `hl_tcp_lz4_work_fraction`). | `--extended-metrics` |
| `hl_tcp_lz4_work_fraction` | Gauge | `direction`, `port` | Fraction of wall-clock time (0..1 duty cycle) the lz4 codec spent doing work per direction/port. Unitless; split out of `hl_tcp_lz4_latency_seconds`. | `--extended-metrics` |

## Extended: Per-step Latency

These expose node-precomputed per-node quantiles as gauges with no `_bucket` series - valid only per-instance; do NOT `avg()`/`quantile()` them across nodes or steps.

| Metric | Type | Labels | Description | Requirements |
|--------|------|--------|-------------|--------------|
| `hl_latency_bucket_guard_seconds` | Gauge | `step`, `quantile` | bucket_guard sub-step latency. `quantile` ∈ {p50, p90, p95, max, mean, std_dev} (the `work_frac` duty-cycle moved to `hl_latency_bucket_guard_work_fraction`). | `--extended-metrics` |
| `hl_latency_bucket_guard_work_fraction` | Gauge | `step` | Fraction of wall-clock time (0..1 duty cycle) each bucket_guard sub-step spent doing work. Unitless; split out of `hl_latency_bucket_guard_seconds`. | `--extended-metrics` |

## Extended: Operator Health

| Metric | Type | Labels | Description | Requirements |
|--------|------|--------|-------------|--------------|
| `hl_node_log_lines` | Gauge | `stream`, `level` | Lines in day's log file | `--extended-metrics` |
| `hl_node_log_bytes` | Gauge | `stream`, `level` | Day's log file size | `--extended-metrics` |
| `hl_node_public_ip_info` | Gauge | `ip` | 1 for current public IP | `--extended-metrics` |
| `hl_node_public_ip_age_seconds` | Gauge | - | IP heartbeat age | `--extended-metrics` |
| `hl_node_public_ip_changes_total` | Counter | - | Cumulative IP changes | `--extended-metrics` |
| `hl_node_operator_config_age_seconds` | Gauge | `file` | file_mod_time_tracker mtime age | `--extended-metrics` |
| `hl_node_tmp_bytes` | Gauge | - | `$NODE_HOME/tmp` bytes | `--extended-metrics` |
| `hl_node_tmp_stale_files` | Gauge | - | tmp files older than 24h | `--extended-metrics` |
| `hl_node_shell_exec_pending` | Gauge | - | shell_rs_out file count | `--extended-metrics` |
| `hl_node_jailing_threshold_seconds` | Gauge | - | latency_ema_jail_threshold from heartbeat_jailing_config.json: the EMA above which this node votes to jail a peer. Registered on first successful read (validator-only file). | `--extended-metrics`, validator |
| `hl_node_jailing_dry_run` | Gauge | - | 1 if jail decisions are logged but not enforced (dry_run), 0 if live | `--extended-metrics`, validator |

## Node Lifecycle

| Metric | Type | Labels | Description | Requirements |
|--------|------|--------|-------------|--------------|
| `hl_node_child_starts` | Gauge | - | hl-node child starts retained in visor_child_stderr (clean + crashed). Retained-history count; do not rate(). | - |
| `hl_node_child_crashes` | Gauge | `reason` | Retained crashes by reason: app_hash_mismatch (consensus divergence, page immediately), hardfork_upgrade, sync_overflow, config_error, network, panic | - |
| `hl_node_child_last_crash_seconds` | Gauge | `reason` | Unix time of the newest retained crash per reason. Alert shape: `time() - value < window`. | - |
| `hl_node_rate_limited_files` | Gauge | `stream` | Non-empty rate_limited_ips files in the newest date dir (abci_stream / gossip_rpc_blocks / gossip_rpc_requests). Zero on a healthy node; non-zero = actively rate-limiting peers. | - |

## Extended: Tokio Runtime

| Metric | Type | Labels | Description | Requirements |
|--------|------|--------|-------------|--------------|
| `hl_tokio_task_poll_seconds_total` | Gauge | `task` | Cumulative poll time | `--extended-metrics` |
| `hl_tokio_task_polls_total` | Gauge | `task` | Cumulative polls | `--extended-metrics` |
| `hl_tokio_task_slow_polls_total` | Gauge | `task` | Cumulative slow polls | `--extended-metrics` |
| `hl_tokio_task_long_delays_total` | Gauge | `task` | Cumulative long delays | `--extended-metrics` |
| `hl_tokio_task_idle_seconds_total` | Gauge | `task` | Cumulative idle time | `--extended-metrics` |
| `hl_tokio_task_dropped_total` | Gauge | `task` | Cumulative dropped tasks | `--extended-metrics` |
| `hl_tokio_task_scheduled_total` | Gauge | `task` | Cumulative schedules (wakes) | `--extended-metrics` |
| `hl_tokio_task_scheduled_seconds_total` | Gauge | `task` | Cumulative scheduled-but-not-polled time; rising faster than poll time = runtime starvation | `--extended-metrics` |
| `hl_tokio_task_fast_polls_total` | Gauge | `task` | Cumulative fast polls | `--extended-metrics` |
| `hl_tokio_task_short_delays_total` | Gauge | `task` | Cumulative short scheduling delays | `--extended-metrics` |
| `hl_tokio_sample_age_seconds` | Gauge | - | Age of the newest tokio sample. When it passes ~15m the per-task gauges above are withdrawn (the feed has been observed to die for a day while the node ran); alert on the age, not on task absence. | `--extended-metrics` |

## Extended: Crit Locations

| Metric | Type | Labels | Description | Requirements |
|--------|------|--------|-------------|--------------|
| `hl_node_crit_location` | Gauge | `file`, `line` | Per-site crit count, capped at 32 | `--extended-metrics` |
| `hl_node_crit_location_last_seen_seconds` | Gauge | `file`, `line` | Last activation timestamp | `--extended-metrics` |
| `hl_node_crit_location_ignored` | Gauge | `file`, `line` | 1 if the location is operator-suppressed via crit_msg_ignore.json; exclude these from alerts | `--extended-metrics` |

## Extended: RocksDB

| Metric | Type | Labels | Description | Requirements |
|--------|------|--------|-------------|--------------|
| `hl_rocksdb_sst_files` | Gauge | `db` | SST file count per RocksDB | `--extended-metrics` |
| `hl_rocksdb_write_stalls_total` | Gauge | `db`, `reason` | Cumulative write stalls by reason | `--extended-metrics` |
| `hl_rocksdb_block_cache_usage_bytes` | Gauge | `db` | Block cache usage | `--extended-metrics` |

## Validator (validator-only, always-on)

These auto-detect via filesystem: each monitor checks for its source directory at startup and idles silently on non-validator nodes (no flag needed).

| Metric | Type | Labels | Description | Requirements |
|--------|------|--------|-------------|--------------|
| `hl_consensus_committed_blocks` | Counter | - | Blocks committed since the exporter started observing; rate() valid. | Validator node |
| `hl_consensus_committed_txs` | Counter | - | Transactions committed since the exporter started observing. | Validator node |
| `hl_consensus_committed_tx_bytes` | Counter | - | Bytes of committed transactions since the exporter started observing. | Validator node |
| `hl_consensus_dropped_txs` | Counter | - | Dropped txs (mempool/consensus shedding load) since the exporter started observing. Any non-zero rate is operator-actionable. | Validator node |
| `hl_consensus_round_qc` | Counter | - | QC-decided rounds. Tracks committed_blocks on a healthy network. | Validator node |
| `hl_consensus_round_tc` | Counter | - | Timeout-certificate rounds (view changes). | Validator node |
| `hl_consensus_round_catchup` | Counter | - | Catch-up events (validator was behind and recovered). | Validator node |
| `hl_consensus_rpc_requests_registered` | Counter | - | Validator-RPC requests this validator served. | Validator node |
| `hl_consensus_rpc_requests_sent` | Counter | - | Validator-RPC requests this validator initiated. | Validator node |
| `hl_mempool_events_total` | Counter | `event_type`, `status` | Mempool log events by type (add_tx, verify_block, committed, dropping_blocks, size_stats, …). `status` carries the outcome for add_tx and verify_block; empty for others. | Validator node |
| `hl_mempool_size` | Gauge | `component` | Mempool depth from "Size stats" events. components: committed_tx_hashes, uncommitted_txs, blocks, rpc_requests. | Validator node |
| `hl_node_replay_events` | Gauge | - | Current count of retained replay-from-checkpoint dirs (decreases on pruning) under data/node_logs/replay/. Point-in-time count, so do not `rate()`/`increase()`. | Validator node |
| `hl_node_replay_last_seconds` | Gauge | - | Unix timestamp of the most recent replay event. | Validator node |
| `hl_node_replay_last_height` | Gauge | - | Block height of the most recent replay event. | Validator node |
| `hl_node_operator_config_failed_loads` | Gauge | - | Count of `*_FAILED_LOAD` sidecars in file_mod_time_tracker/. Non-zero = silent misconfiguration (operator pushed a config hl-node rejected). | `--extended-metrics` |

## Split-Client Mempool Transactions

These auto-detect `$NODE_HOME/data/mempool_txs/hourly/`, which appears when `split_client_blocks: true` is active and every upstream hop is forwarding split client blocks. The raw files contain uncommitted order-flow details; the exporter emits only aggregate counters and histograms with bounded labels.

| Metric | Type | Labels | Description | Requirements |
|--------|------|--------|-------------|--------------|
| `hl_mempool_txs_seen_total` | Counter | - | Uncommitted mempool transaction records observed in `data/mempool_txs`. | Node with `data/mempool_txs/hourly/` |
| `hl_mempool_txs_bytes_total` | Counter | - | Bytes read from complete JSONL records in `data/mempool_txs`. | Node with `data/mempool_txs/hourly/` |
| `hl_mempool_txs_signed_actions_total` | Counter | `type` | Signed actions observed in split-client mempool transaction records, by allowlisted action type. New action types are bucketed as `other`. | Node with `data/mempool_txs/hourly/` |
| `hl_mempool_txs_operations_total` | Counter | `type` | Individual operations observed in split-client mempool transactions. `orders`, `cancels`, and `modifies` count array elements; scalar actions count as 1. | Node with `data/mempool_txs/hourly/` |
| `hl_mempool_txs_order_operations_total` | Counter | `side`, `tif` | Order-like operations by side and time-in-force. Includes `order`, `modify`, and `batchModify` entries. | Node with `data/mempool_txs/hourly/` |
| `hl_mempool_txs_signed_actions_per_record` | Histogram | - | Distribution of `signed_actions` array length per split-client mempool transaction record. | Node with `data/mempool_txs/hourly/` |
| `hl_mempool_txs_operations_per_record` | Histogram | - | Distribution of individual operation count per split-client mempool transaction record. | Node with `data/mempool_txs/hourly/` |
| `hl_mempool_txs_latest_time` | Gauge | - | Unix timestamp from the latest split-client mempool transaction record processed. | Node with `data/mempool_txs/hourly/` |
| `hl_mempool_txs_sample_age_seconds` | Gauge | - | Wall-clock age of the latest record, refreshed every tick (keeps climbing when the stream goes quiet). | Node with `data/mempool_txs/hourly/` |

## Validator (validator-only, --extended-metrics)

Adds per-step latency breakdowns from the two validator-only sub-step subsystems.

These expose node-precomputed per-node quantiles as gauges with no `_bucket` series - valid only per-instance; do NOT `avg()`/`quantile()` them across nodes or steps.

| Metric | Type | Labels | Description | Requirements |
|--------|------|--------|-------------|--------------|
| `hl_latency_consensus_seconds` | Gauge | `step`, `quantile` | Consensus state-machine latency per input type. step ∈ {HandleStateInput::Block, HandleStateInput::Vote, …, TxCommit, WallClockBlockGap}. quantile ∈ {p50, p90, p95, max, mean, std_dev} (the `work_frac` duty-cycle moved to `hl_latency_consensus_work_fraction`). | Validator node + `--extended-metrics` |
| `hl_latency_consensus_work_fraction` | Gauge | `step` | Fraction of wall-clock time (0..1 duty cycle) each consensus state-machine step spent doing work. Unitless; split out of `hl_latency_consensus_seconds`. | Validator node + `--extended-metrics` |
| `hl_latency_l1_task_seconds` | Gauge | `step`, `quantile` | L1 block-apply phase latency. step ∈ {BeginBlock, DeliverSignedActions, EndBlock, RecoverUsers}. quantile ∈ {p50, p90, p95, max, mean, std_dev} (the `work_frac` duty-cycle moved to `hl_latency_l1_task_work_fraction`). | Validator node + `--extended-metrics` |
| `hl_latency_l1_task_work_fraction` | Gauge | `step` | Fraction of wall-clock time (0..1 duty cycle) each L1 block-apply phase spent doing work. Unitless; split out of `hl_latency_l1_task_seconds`. | Validator node + `--extended-metrics` |

## Go Runtime

| Metric | Type | Labels | Description | Requirements |
|--------|------|--------|-------------|--------------|
| `hl_go_heap_inuse_bytes` | Gauge | - | Heap in use (raw bytes) | - |
| `hl_go_heap_idle_bytes` | Gauge | - | Heap idle (raw bytes) | - |
| `hl_go_heap_objects` | Gauge | - | Live heap objects | - |
| `hl_go_sys_bytes` | Gauge | - | System memory acquired (raw bytes) | - |
| `hl_go_num_goroutines` | Gauge | - | Live goroutines | - |

## Exporter Self-observability

| Metric | Type | Labels | Description | Requirements |
|--------|------|--------|-------------|--------------|
| `hl_exporter_build_info` | Gauge | `version`, `commit`, `go_version` | Build info, always 1 | - |
| `hl_exporter_ready` | Gauge | - | Mirrors `/readyz` | - |
| `hl_exporter_monitor_started_seconds` | Gauge | `monitor` | Monitor goroutine launch time | - |
| `hl_exporter_monitor_last_tick_seconds` | Gauge | `monitor` | Last successful processing cycle | - |
| `hl_exporter_monitor_panics_total` | Counter | `monitor` | Recovered panics. In-process monotonic since exporter start (resets only on exporter restart), so `rate()`/`increase()` are valid. | - |
| `hl_exporter_monitor_errors_total` | Counter | `monitor` | Errors reported. In-process monotonic since exporter start (resets only on exporter restart), so `rate()`/`increase()` are valid. | - |

### Consensus monitor health (OTLP-path)

These are emitted via the OTLP/observable-instrument path (not the client_golang registry), so they appear on the Prometheus `/metrics` endpoint through the OTel->Prometheus bridge. The names below are exactly as exposed: the counters already carry `_total` (the bridge does not double-suffix), and `hl_consensus_monitor_last_processed` declares `WithUnit("timestamp")`, which is not in the bridge's unit-suffix table, so no `_seconds`/`_timestamp` suffix is appended.

| Metric | Type | Labels | Description | Requirements |
|--------|------|--------|-------------|--------------|
| `hl_consensus_monitor_last_processed` | Gauge | `monitor_type` | Unix timestamp of the last line processed, per monitor (`monitor_type` ∈ `consensus`/`status`), flushed once per EOF pause | - |
| `hl_consensus_monitor_lines_processed_total` | Counter | `monitor_type` | Lines processed by the consensus/status monitor | - |
| `hl_consensus_monitor_errors_total` | Counter | `monitor_type` | Errors encountered by the consensus/status monitor | - |

## HTTP endpoints

| Path | Status |
|------|--------|
| `/metrics` | Prometheus exposition |
| `/livez` | 200 while running |
| `/readyz` | 200 once every monitor has launched |
| `/health` | Legacy alias for `/livez` |

## Label Definitions

### Common Labels
- `validator`: Validator address
- `signer`: Signer address
- `name`: Human-readable validator name
- `type`: Transaction or operation type
- `category`: Operation category (for operations_total)
- `block_type`: EVM block type (standard / small / high / other); attached automatically whenever EVM metrics are enabled (no separate flag)

### v3 Labels
- `source`, `process`: `hl-node` or `hl-visor`
- `subsystem`: hl-node subsystem name
- `step`: sub-step within bucket_guard
- `quantile`: `p50` / `p90` / `p95` / `max` / `mean` / `std_dev` (the unitless `work_frac` duty-cycle is no longer a quantile value - it lives in the dedicated `*_work_fraction` gauges)
- `direction`: `in` / `out`
- `ip`: peer IP (or literal `other`)
- `port`, `state`: TCP port + state (`ESTABLISHED`, `TIME_WAIT`, …)
- `event_type`: gossip event tag
- `tier`: `fast` / `slow`
- `db`: RocksDB name
- `task`: Tokio task name
- `file`, `line`: source location
- `monitor`: monitor name
- `monitor_type`: consensus-monitor source stream (`consensus` / `status`)
- `subdir`: tracked NODE_HOME subdir
- `stream`, `level`: `infra`/`trade`/`visor`, `error`/`warn` (log lines); `stream` on `hl_node_rate_limited_files` is the rate-limiter stream
- `reason`: write-stall reason (RocksDB) or crash reason (`hl_node_child_crashes`)
- `period`: validatorSummaries stats window (`day`/`week`/`month`)
- `event_type`, `status`: mempool event tag + per-event outcome (add_tx/verify_block carry status)
- `component`: mempool capacity bucket - committed_tx_hashes / uncommitted_txs / blocks / rpc_requests
- `side`: split-client mempool order side (`buy`, `sell`, `unknown`)
- `tif`: split-client mempool order time-in-force (`Alo`, `Ioc`, `Gtc`, `FrontendMarket`, `unknown`, `other`)

## Metric Requirements

### Replica Metrics (`--replica-metrics`)
Require the flag AND hl-node running with `--replica-cmds-style actions-and-responses`:
- All `hl_core_tx_*`, `hl_core_operations_*`, `hl_core_orders_total`, `hl_core_orders_per_block`
- `hl_core_blocks_processed_total`, `hl_core_last_processed_*`, `hl_core_round_advance`, `hl_core_hardfork_version`
- `hl_metal_parse_duration`

### Validator Node Metrics
Validator node with consensus/status logs:
- All `hl_consensus_*` except basic info from api.hyperliquid.xyz/info

### `--probe-info-endpoint`
- All `hl_info_endpoint_*`

### `--extended-metrics`
Bundle: LZ4, per-step latency (bucket_guard + tcp_lz4, plus validator-only consensus + l1_task_latency), operator health, Tokio runtime, crit locations, RocksDB.

### Linux-only
- All `hl_node_process_*`
- `hl_p2p_tcp_connections`

### Flag-gated metric default values
The info-probe family (`hl_info_*`) registers only when `--probe-info-endpoint` is on and is ABSENT otherwise; alert with `absent()` semantics, not `== 0`. Plain gauges gated by `--extended-metrics` still appear at zero when the flag is off (standard `promauto` behavior); label vectors carry no series until populated. See [UPGRADING.md](../UPGRADING.md).
