# Changelog

## [Unreleased] - breaking metrics cleanup

### Changed (BREAKING)

These rename, retype, drop, and relabel metrics. Historical series do NOT carry over - update dashboards, alerts, and recording rules. See [UPGRADING.md](UPGRADING.md) for the full old->new migration table.

#### Renamed metrics

| old | new |
|---|---|
| `hl_p2p_peers_total` | `hl_p2p_peers` |
| `hl_node_observed_runs_total` | `hl_node_observed_runs` |
| `hl_node_replay_events_total` | `hl_node_replay_events` |
| `hl_go_heap_inuse_mb` | `hl_go_heap_inuse_bytes` |
| `hl_go_heap_idle_mb` | `hl_go_heap_idle_bytes` |
| `hl_go_sys_mb` | `hl_go_sys_bytes` |
| `hl_evm_max_priority_fee` | `hl_evm_max_priority_fee_gwei` |
| `hl_consensus_qc_participation_rate` | `hl_consensus_qc_participation_percent` |

The three `hl_go_*_bytes` gauges now emit **raw bytes** (the previous `_mb` gauges divided by 1024^2 in `internal/metrics/memory.go`; that division is removed). Existing values are ~1,048,576x larger; the Grafana "bytes" unit auto-scales. The first three renames drop the `_total` suffix and leave the gauge-`_total` family entirely - they were never counters.

#### Gauge -> Counter (name unchanged)

These were gauges; they are now true Prometheus counters. They are in-process monotonic and reset only on **exporter** restart, so `rate()`/`increase()` are now valid (and were not before).

- `hl_exporter_monitor_panics_total`
- `hl_exporter_monitor_errors_total`
- `hl_p2p_peers_added_total`
- `hl_p2p_peers_evicted_total`

#### Dropped metric

- `hl_metal_apply_duration` (bare OTLP gauge) removed as redundant. Its histogram twin `hl_metal_apply_duration_milliseconds` is kept - use that.

#### Relabeled metric

- `hl_consensus_validator_stake`: label key `moniker` -> `name`, matching its sibling validator metrics (`active_status` / `jailed_status` / `proposer_count_total` all use `name`). The value source (`summary.Name`) is unchanged.

#### work_frac split out of `_seconds` metrics

The per-step latency gauges carried a `quantile="work_frac"` series - a unitless 0..1 duty-cycle wrongly living inside a `_seconds` metric. That series is removed from the `_seconds` metrics and moved to dedicated unitless gauges (same labels minus `quantile`):

| `_seconds` metric (work_frac removed) | new unitless gauge |
|---|---|
| `hl_latency_bucket_guard_seconds{step,quantile}` | `hl_latency_bucket_guard_work_fraction{step}` |
| `hl_latency_consensus_seconds{step,quantile}` | `hl_latency_consensus_work_fraction{step}` |
| `hl_latency_l1_task_seconds{step,quantile}` | `hl_latency_l1_task_work_fraction{step}` |
| `hl_tcp_lz4_latency_seconds{direction,port,quantile}` | `hl_tcp_lz4_work_fraction{direction,port}` |

`work_frac` is no longer a value of the `quantile` label; `quantile` in {p50, p90, p95, max, mean, std_dev}.

## [Unreleased]

### Fixed (metrics audit)

Non-breaking correctness pass. No metric renamed; no instrument type changed.

- `internal/monitors/validator_ip_monitor.go`: `hl_consensus_validator_rtt` now reports **milliseconds** (was raw microseconds, i.e. ~1000x larger numbers). NOTE: RTT values are now ~1000x smaller - adjust any RTT-based alert thresholds.
- `internal/monitors/validator_latency_monitor.go`: `hl_consensus_validator_latency_ema_seconds` no longer drops genuine latencies >=0.4s; the no-data filter now skips only the exact 0.4 sentinel (was `>= 0.4`), unfreezing the gauge that read a stuck ~400ms.
- `proposer_count`: the `name` label is now always set, so the series no longer disappears for proposers seen before their name resolves.
- tx/operations per-block histograms: corrected bucket boundaries so per-block counts land in meaningful buckets.
- Removed the fictional `--evm-block-type-metrics` usage line (no such flag exists).
- docs: added a gauge-vs-counter convention legend noting that several `_total`-suffixed metrics are gauges (reset to 0 on source/exporter restart) and must be queried with `delta()`/`changes()`, not `rate()`/`increase()`.
- `internal/monitors/node_state_monitor.go`: `hl_visor_blocks_above_freeze` now floors at 0 (was emitting a negative value when the freeze height led the visor height).
- `internal/monitors/visor_monitor.go`: tolerate the legacy `reference_lag` field (in addition to `reference_lag_seconds`); added companion gauge `hl_visor_reference_lag_populated` (1 if the sample carried a reference_lag field, else 0).
- `cmd/hl-exporter/vals.go`: `--peer-counter-url` flag makes the `/nodes` peer-counter snapshot endpoint configurable (default `http://127.0.0.1:19046/snapshot`). `--backfill` now derives each row's `legacy` flag from the ABCI state schema (`c_staking` vs the older `consensus` fallback) instead of hardcoding `true`.
- docs: documented the four OTLP-path consensus-monitor health metrics (`hl_timeout_rounds_total`, `hl_consensus_monitor_last_processed`, `hl_consensus_monitor_lines_processed_total`, `hl_consensus_monitor_errors_total`); reworded `hl_consensus_rounds_per_block` (single inter-block round delta, not a moving average); merged the duplicated `hl_consensus_validator_latency_seconds` row; clarified `hl_core_block_height` (fast-state-only) + `state_type` (dual-state only), network-wide stake/status source, `hl_software_up_to_date` UNSET-until-both-checks, and the LZ4 `_total` "since source start" qualifiers.

## [3.0.0] - 2026-05-26

Strict superset of v2.0.0. Same metric names, labels, semantics. Existing dashboards work without modification.

### Fixed

- `internal/metrics/prometheus.go`: replace hand-rolled timeout-via-goroutine in `/metrics` handler with `http.TimeoutHandler`. Stops the `superfluous response.WriteHeader` log spam and the half-written 503s under timeout.
- `internal/monitors/evm_monitor.go`: stop logging WARNING per block whose gas limit isn't exactly 2M or >=30M. Adds a `small` bucket (3M); silences `other`.
- `internal/exporter/runMonitor` + `internal/monitors/safego.go`: wrap every monitor goroutine and every inner goroutine in `recover()`. Panic in one monitor no longer freezes its metric for the process lifetime; per-monitor `hl_exporter_monitor_panics_total` counts recoveries.
- `internal/monitors/consensus_monitor.go`: `qcSignatures`, `tcVotes`, `validatorCache` were unbounded. 1h last-seen TTL on a 10-min housekeeping ticker; `validatorCache` switched to LRU.

### Added: monitors (always on)

| monitor | metrics | source |
|---|---|---|
| `visor` | `hl_visor_*` | `hyperliquid_data/visor_abci_state.json` + `data/visor_abci_states/hourly/` |
| `snapshot_status` | `hl_node_snapshot_last_height`, `hl_node_snapshot_last_age_seconds`, `hl_node_snapshot_known_count` | `data/periodic_abci_state_statuses/` |
| `node_state` | `hl_visor_freeze_abci_height`, `hl_visor_blocks_above_freeze`, `hl_evm_db_checkpoint_*` | `hyperliquid_data/freeze_abci_height`, `evm_db_hub_{fast,slow}/cp_checkpoint_height` |
| `subsystem_latency` | `hl_node_subsystem_latency_*`, `hl_node_subsystem_work_fraction`, `hl_node_subsystem_samples_total` | `data/latency_summaries/<subsystem>/<date>` (15-subsystem allowlist) |
| `crit_msg` | `hl_node_bugs`, `hl_node_crits`, `hl_node_crit_locations`, `hl_node_critical_messages_base_time_seconds` | `data/crit_msg_stats/{hl-node,hl-visor}/<date>` |
| `tcp_traffic` | `hl_p2p_peer_traffic`, `hl_p2p_total_traffic`, `hl_p2p_peer_count`, `hl_p2p_sample_age_seconds` | `data/tcp_traffic/hourly/` (top-16 per direction + `ip="other"`) |
| `parent_peer` | `hl_node_parent_peer_*` | shared tcp_traffic snapshot |
| `gossip_connections` | `hl_p2p_gossip_events_total{event_type}` | `data/node_logs/gossip_connections/` |
| `disk` | `hl_node_disk_used_bytes`, `hl_node_disk_free_bytes`, `hl_node_disk_total_bytes`, `hl_node_disk_subdir_bytes{subdir}` | NODE_HOME WalkDir + statfs + per-RocksDB subpaths |
| `process` | `hl_node_process_*` | `/proc/PID/{stat,status,fd,comm}` for hl-node + hl-visor (Linux only) |
| `tcp_connections` | `hl_p2p_tcp_connections{port,state}` | `/proc/net/tcp{,6}` (Linux only) |
| `replica_runs` | `hl_node_observed_runs_total`, `hl_node_observed_run_start_seconds` | `data/replica_cmds/<run_timestamp>/` dir count + newest mtime |
| `peer_set` | `hl_p2p_peers_total` (peers in latest sample), `hl_p2p_unique_peers_seen{window}` (5m/1h/24h rolling), `hl_p2p_peers_added_total`, `hl_p2p_peers_evicted_total` | in-memory LRU+TTL peer set fed by tcp_traffic (2048-cap, 24h TTL) |

### Added: monitors (opt-in flags)

| flag | monitor | metrics |
|---|---|---|
| `--probe-info-endpoint` | `info_probe` | `hl_info_endpoint_up`, `hl_info_endpoint_latency_seconds`, `hl_info_endpoint_last_success_seconds`, `hl_info_endpoint_failures_total` |
| `--extended-metrics` | `tcp_lz4` | `hl_p2p_lz4_*`, `hl_p2p_lz4_global_*` |
| `--extended-metrics` | `log_lines` | `hl_node_log_lines{stream,level}`, `hl_node_log_bytes{stream,level}` |
| `--extended-metrics` | `public_ip` | `hl_node_public_ip_info{ip}`, `hl_node_public_ip_age_seconds`, `hl_node_public_ip_changes_total` |
| `--extended-metrics` | `tokio_runtime` | `hl_tokio_task_*` (12-task allowlist) |
| `--extended-metrics` | `operator_config` | `hl_node_operator_config_age_seconds{file}` |
| `--extended-metrics` | `tmp_dir` | `hl_node_tmp_bytes`, `hl_node_tmp_stale_files`, `hl_node_shell_exec_pending` |
| `--extended-metrics` | `rocksdb` | `hl_rocksdb_sst_files{db}`, `hl_rocksdb_write_stalls_total{db,reason}`, `hl_rocksdb_block_cache_usage_bytes{db}` |
| `--extended-metrics` | `subsystem_steps` | `hl_latency_bucket_guard_seconds{step,quantile}`, `hl_tcp_lz4_latency_seconds{direction,port,quantile}` |
| `--extended-metrics` | `crit_locations` | `hl_node_crit_location{file,line}`, `hl_node_crit_location_last_seen_seconds` (32-location cap) |
| `--per-peer-metrics` | (extends `peer_set`) | `hl_p2p_peer_last_seen_seconds{ip}`, `hl_p2p_peer_first_seen_seconds{ip}` (LRU 2048 + 24h TTL) |

### Added: exporter self-observability

- `/livez`: always 200 while running.
- `/readyz`: 200 once every registered monitor goroutine has launched.
- `hl_exporter_build_info{version,commit,go_version}`: set via `-ldflags`.
- `hl_exporter_monitor_started_seconds{monitor}`
- `hl_exporter_monitor_last_tick_seconds{monitor}`: 0 if monitor hasn't seen data yet.
- `hl_exporter_monitor_panics_total{monitor}`
- `hl_exporter_monitor_errors_total{monitor}`
- `hl_exporter_ready`: mirrors `/readyz`.
- `hl_exporter version` subcommand.

### Added: flags

| flag | purpose |
|---|---|
| `--metrics-port N` | listener port (default 8086) |
| `--skip-version-check` | skip the local `hl-node --version` probe (containers) |
| `--skip-update-check` | skip the upstream binary download (containers) |
| `--probe-info-endpoint` | enable info-endpoint probe |
| `--info-endpoint-url <url>` | override probe URL (default `http://127.0.0.1:3001/info`) |
| `--extended-metrics` | enable extended monitor bundle |
| `--per-peer-metrics` | emit `hl_p2p_peer_{last,first}_seen_seconds{ip}` (LRU 2048 + 24h TTL) |

### Added: tests

Upstream had zero Go unit tests. Parser tests added for the new monitors:

- `tcp_traffic_monitor_test.go`: `parseTCPTrafficLine`, `lastFullLine` (torn writes / no-newline / only-newlines).
- `crit_msg_monitor_test.go`: `parseCritMsgLine`, `readLastCritMsg` torn-last recovery.
- `visor_monitor_test.go`: `latestHourlyFile` numeric hour sort, `parseVisorTime` shape coverage.
- `subsystem_latency_monitor_test.go`: `readLastSummary`.
- `gossip_connections_monitor_test.go`: `parseGossipConnectionLine` tag mapping + malformed input.
- `rocksdb_monitor_test.go`: `parseWriteStallLine` + `parseBlockCacheUsage` + `readRocksDBStats`.
- `crit_locations_monitor_test.go`: rich-form JSON shape.
- `tokio_runtime_monitor_test.go`: `parseTokioLine`.
- `node_state_monitor_test.go`: `readSingleInt`.
- `public_ip_monitor_test.go`: `parsePublicIP`.

`go test ./internal/monitors/` runs in under a second.

### Operator alert recipes

| symptom | metric expression |
|---|---|
| node crashed | `hl_node_process_up{process="hl-node"} == 0` for > 30s |
| visor crashed | `hl_node_process_up{process="hl-visor"} == 0` for > 30s |
| info endpoint down | `hl_info_endpoint_up == 0` for > 30s (requires `--probe-info-endpoint`) |
| bug! emitted | `hl_node_bugs{source="hl-node"} > 0` |
| crits accumulating | `hl_node_crits > 0 and changes(hl_node_crits[10m]) > 0` (gauge; crits reset to 0 on source restart, so don't use `rate()`) |
| disk filling | `hl_node_disk_free_bytes / hl_node_disk_total_bytes < 0.10` |
| not in sync | `hl_visor_last_observation_age_seconds > 60` |
| snapshot pipeline stalled | `hl_node_snapshot_last_age_seconds > 900` |
| EVM tier divergence | `hl_evm_db_checkpoint_lag_blocks > 100` for > 10m |
| max peers reached | `rate(hl_p2p_gossip_events_total{event_type="rejecting_gossip_stream_max_peers_reached"}[5m]) > 0` |
| begin-to-commit slow | `hl_node_subsystem_latency_p95_seconds{subsystem="node_fast_begin_block_to_commit"} > 0.1` for > 5m |
| peer churn elevated | `rate(hl_p2p_gossip_events_total{event_type="error_checking_connection"}[5m]) > 0.1` |
| public IP heartbeat dead | `hl_node_public_ip_age_seconds > 1800` (requires `--extended-metrics`) |
| lz4 compression collapsed | `hl_p2p_lz4_global_ratio > 0.85` for > 10m (requires `--extended-metrics`) |
| orphaned tmp files growing | `hl_node_tmp_stale_files > 100` (requires `--extended-metrics`) |
| node logged something | `hl_node_log_lines{level="error"} > 0` (requires `--extended-metrics`) |
| live peer count dropped | `hl_p2p_tcp_connections{port="4001",state="ESTABLISHED"} < 3` for > 5m |
| peer fanout collapsed | `hl_p2p_unique_peers_seen{window="24h"} < 50` |
| peer churn rate elevated | `delta(hl_p2p_peers_added_total[1h]) > 20` (in-process monotonic gauge; resets to 0 on exporter restart, so don't use `rate()`) |
| peer churn on gossip-2 | `hl_p2p_tcp_connections{port="4002",state="TIME_WAIT"} > 50` |
| RocksDB write-stalled | `delta(hl_rocksdb_write_stalls_total{reason=~"pending-compaction-bytes-stops|l0-file-count-limit-stops"}[5m]) > 0` (gauge; resets on source restart, so don't use `rate()`) (requires `--extended-metrics`) |
| begin_block tail-latency | `hl_latency_bucket_guard_seconds{step="begin_block",quantile="max"} > 1` for > 5m (requires `--extended-metrics`) |
| same crit hot-firing | `delta(hl_node_crit_location[5m]) > 0` (gauge; resets on source restart, so don't use `rate()`) (requires `--extended-metrics`) |
| visor shell-cleanup broken | `hl_node_shell_exec_pending > 5000` (requires `--extended-metrics`) |
| exporter monitor stuck | `time() - hl_exporter_monitor_last_tick_seconds > 600` (per monitor) |
| exporter panicking | `delta(hl_exporter_monitor_panics_total[5m]) > 0` (in-process monotonic gauge; resets to 0 on exporter restart, so don't use `rate()`) |

### Deferred

- Top-N tcp_traffic moving-window stabilization (parent_peer flaps).
- File-position persistence so monitors don't drop in-flight data on restart.
- Active TCP-probe peer latency.
- Outbound peer discovery beyond tcp_traffic.

## [2.0.0] - 2025-08-03

### Added

#### Consensus Monitoring
- Realtime consensus monitoring with 20+ new consensus metrics
- Validator connectivity tracking with heartbeats
- QC participation 
- TC tracking
- Validator latency measurements

#### HyperCore Tx and order metrics
- Moved to direct msgpack parsing (previously used binary)
- Monitor tps, orders per second
- See breakdown of order types

#### EVM
- Comprehensive gas metrics (base fee, priority fee, utilization)
- Per-contract transaction tracking with configurable limits
- High gas block detection and tracking
- EVM account growth monitoring

#### System Monitoring
- Go runtime memory metrics (heap, goroutines, system memory)
- P2P network peer connection tracking
- LRU caching system for improved performance
- Processing latency and throughput metrics

#### New CLI Flags
- `--replica-metrics` - Enable replica command transaction metrics
- `--contract-metrics` - Enable per-contract transaction metrics
- `--contract-metrics-limit` - Maximum contract labels to retain (default: 20)
- `--validator-rtt` - Enable validator RTT monitoring

### Changed

#### Metrics Organization (BREAKING CHANGES)
- All metrics reorganized with categorical prefixes:
  - `hl_core_*` - Core blockchain metrics
  - `hl_consensus_*` - Consensus-related metrics
  - `hl_metal_*` - Implementation-specific metrics
  - `hl_evm_*` - EVM chain metrics
- Total metrics increased from 20 to 82 (310% increase)

#### Complete List of Renamed Metrics
- `hl_block_height` -> `hl_core_block_height`
- `hl_block_time_milliseconds` -> `hl_core_block_time_milliseconds`
- `hl_latest_block_time` -> `hl_core_latest_block_time`
- `hl_apply_duration` -> `hl_metal_apply_duration`
- `hl_apply_duration_milliseconds` -> `hl_metal_apply_duration_milliseconds`
- `hl_proposer_count_total` -> `hl_consensus_proposer_count_total`
- `hl_validator_count` -> `hl_consensus_validator_count`
- `hl_validator_jailed_status` -> `hl_consensus_validator_jailed_status`
- `hl_validator_stake` -> `hl_consensus_validator_stake`
- `hl_validator_active_status` -> `hl_consensus_validator_active_status`
- `hl_validator_rtt` -> `hl_consensus_validator_rtt`
- `hl_total_stake` -> `hl_consensus_total_stake`
- `hl_jailed_stake` -> `hl_consensus_jailed_stake`
- `hl_not_jailed_stake` -> `hl_consensus_not_jailed_stake`
- `hl_active_stake` -> `hl_consensus_active_stake`
- `hl_inactive_stake` -> `hl_consensus_inactive_stake`

..with addition of many more brand new metrics

#### CLI Flags
- `--enable-otlp` renamed to `--otlp`
- `--evm` renamed to `--evm-metrics`
- `--otlp-endpoint` default value removed (now required when OTLP enabled)

### Removed
- `--enable-prom` flag (Prometheus now always enabled)
- `--disable-prom` flag (Prometheus now always enabled)
- `hl_evm_transactions_total` metric (replaced by `hl_evm_tx_type_total`)
