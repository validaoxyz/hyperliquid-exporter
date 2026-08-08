# Changelog

## [v4.0.0] - 2026-08-09

### Breaking

- Corrected EVM, validator, network, stream, process, disk, and snapshot metrics to match what their sources prove.
- Added explicit source presence, read, schema, and freshness reporting.
- Metric renames and removals require migration; see [UPGRADING.md](UPGRADING.md).

> Entries below describe the behavior of their dated releases. Current metric semantics and migrations are defined by the v4.0.0 section, [UPGRADING.md](UPGRADING.md), and the generated [metrics reference](docs/metrics.md).

## [v3.1.0] - 2026-07-04

### Fixed

- Consensus accumulator metrics published the wrong field. The on-disk lines are per-window samples: `delta` is the quantity, `n` is the event count in the window (they only coincide for CommittedBlocks). All nine `hl_consensus_committed_*` / `hl_consensus_round_*` / `hl_consensus_rpc_requests_*` metrics now sum deltas into real Prometheus counters. Values are cumulative since exporter start and `rate()` is valid. Previously `committed_tx_bytes` equaled `committed_blocks` and `dropped_txs` was unreliable.
- The status stream parser accepts the 2026-07 hl-node schema for `current_stakes` (`{"validator_to_stake": [...]}`) in addition to the legacy row array. New builds broke the parser on every status line.
- Status lines larger than 64 KiB no longer fail the last-line reader (buffer raised to 8 MiB; lines carry whole-validator-set maps).
- Removed the 30s metrics-registry sweep that truncated any labeled family to 100 series in map order. On networks with more than 100 validators it randomly dropped per-validator stake/jailed/latency series every 30 seconds.
- Removed the forced `runtime.GC()` every 30s and moved `ReadMemStats` (stop-the-world) off the scrape path onto a 30s cache.
- Startup no longer fails when `api.ipify.org` is unreachable. The public IP comes from `NODE_HOME/last_known_public_ip.json` first; ipify is a 5s-timeout fallback and failure only logs.
- `hl_software_up_to_date` compares the local hl-visor build against the latest published hl-visor. It used to compare the CDN visor against the local hl-node, which the visor swaps mid-release, pinning the metric at 0. The remote check now uses ETag conditional requests instead of downloading the full binary every 30 minutes, and the curl dependency is gone.
- `hl_consensus_proposer_count_total` resolved every moniker to "unknown" through a stubbed lookup.
- Per-validator gauges from validatorSummaries (stake, jailed, active) and the raw latency family reconcile as snapshots: validators leaving the set are removed instead of freezing at their last value.
- `hl_consensus_vote_time_diff_seconds` now emits the age of the last observed vote (climbs while a validator is silent). It used to store the exporter's parse lag once per vote, which sat near 0 forever. Vote series are trimmed after 24h without votes.
- Torn trailing lines are no longer half-consumed by the streaming monitors (mempool lost one event per file boundary; the shared tail reader now reassembles partials).
- `validator_latency` date-file selection uses UTC; on non-UTC hosts it read a nonexistent path around midnight.
- The gossip_rpc scanner survives >64 KiB peer-list lines.
- `hl_core_operations_total` / `hl_core_operations_per_block` were inflated 2-6x: the fast-path element counter only tracked array depth, so every comma inside an order/cancel object counted as a separator (one order = 6 "operations"). Elements are counted properly now. `hl_core_orders_total` counts order actions (documented); individual orders live in `hl_core_operations_total{type="order"}`.
- The contract resolver no longer closes its fetch queue on shutdown while the EVM stream can still enqueue (a send on a closed channel panics even under select/default, tripping panic alerts on routine restarts). Per-peer first/last-seen series are reconciled against the live peer set, so LRU-evicted peers can't leak series on nodes with more distinct peers than the cap. hl_node_snapshot_known_count counts the two newest date dirs (it sawtoothed at UTC midnight). gossip_connections no longer consumes torn trailing lines.

### Performance

The exporter burned ~195% CPU on a live validator; the causes are removed:

- Streaming monitors shared a loop that re-ran a full recursive directory walk on every 10ms EOF pause (the status goroutine walked a ~7,000-file tree ~100x/second to tail 31 lines/hour). All of them now use a shared tail-streamer with cheap directory-listing resolvers, 2-5s rescan gating, and proper file-handle rotation.
- Consensus per-line metric updates batch once per EOF pause instead of taking the global mutex several times per line; QC participation recalculation is gated to once per 2s instead of every block.
- The disk monitor walks NODE_HOME once (previously up to three overlapping walks) every 120s.
- EVM receipts are never decoded (nothing read them); gossip_rpc re-reads only the file tail instead of the whole hour.
- The version/update checkers stopped copying and downloading whole binaries every 30 minutes.

### Added

- `hl_node_jailing_threshold_seconds` and `hl_node_jailing_dry_run` from `heartbeat_jailing_config.json`: the number that decides jail votes. Headroom recipe: `hl_consensus_validator_latency_ema_seconds / on() group_left hl_node_jailing_threshold_seconds`.
- `hl_consensus_validator_jailed_local` from the status stream: node-local jailed set, ~2min cadence, no API dependency.
- `hl_consensus_validator_unjailable_after_seconds`, `_recent_blocks`, `_commission_rate`, `_uptime_fraction{period}`, `_predicted_apr{period}` from validatorSummaries fields that were previously discarded.
- `hl_node_child_starts`, `hl_node_child_crashes{reason}`, `hl_node_child_last_crash_seconds{reason}` from `visor_child_stderr`: crash taxonomy including `app_hash_mismatch` (consensus divergence) and `config_error`, which no other metric can see.
- `hl_core_round_advance` histogram (round - parent_round per block; >1 = timed-out rounds) and `hl_core_hardfork_version` from the replica stream.
- `hl_info_exchange_status_delta_seconds`: local clock minus the node's exchangeStatus time (staleness check the node README recommends).
- `hl_tokio_sample_age_seconds` plus withdrawal of `hl_tokio_task_*` when the source feed is stale (observed dead for 26h while the node ran).
- `hl_tokio_task_scheduled_total`, `_scheduled_seconds_total`, `_fast_polls_total`, `_short_delays_total`.
- `hl_node_rate_limited_files{stream}`: abuse tripwire from `data/rate_limited_ips`.
- `hl_node_crit_location_ignored{file,line}`, `hl_p2p_non_val_connections`, `hl_node_subsystem_latency_lifetime_mean_seconds`.
- `hl_node_stream_age_seconds{stream}`: freshness of opt-in data streams (fills, TWAP statuses, misc events, system/core writer actions). These exist to feed downstream consumers; nothing else notices when one silently stalls. `node_gossip_priority_config.json` joined the operator-config mtime allowlist.
- `--pprof` exposes /debug/pprof/ on the metrics listener (opt-in) so CPU questions get a profile instead of a guess.

### Changed

- Parent-peer selection runs on an EWMA of per-peer traffic with 1.2x switch hysteresis. The single-sample design flapped (peer values swing between ~1.0 and ~1e-6 across 30s windows) and logged an ambiguous-parent warning every 30 seconds.
- A failed metrics listener now exits the process instead of leaving the exporter running blind while every scrape fails.
- The mempool monitor starts new hour files at offset 0 (only the file found at startup seeks to EOF), so the head of each hour is no longer dropped. mempool_txs refreshes its sample-age gauge every tick so a dead stream shows a climbing age.

### Changed (BREAKING)

- Removed duplicates and dead weight: `hl_core_rounds_processed_total` (equal to `hl_core_blocks_processed_total`), `hl_metal_last_processed_round`/`_time` (aliases of `hl_core_last_processed_*`), `hl_evm_gas_limit_distribution` (two-bucket histogram), `hl_evm_max_gas_limit_seen`, `hl_evm_last_high_gas_block_{height,time,limit,used}`, `hl_evm_high_gas_limit_blocks_total`, `hl_evm_account_count` (constant 0 on current chain versions; EVM state lives in RethDB), `hl_timeout_rounds_total` (documented but never wired; use `hl_core_round_advance` buckets and `hl_consensus_round_tc`).
- `hl_consensus_heartbeat_ack_received_total` dropped the constant `from_validator`/`from_name` labels (always the local node).
- `hl_consensus_committed_*` family is typed Counter with since-exporter-start semantics (see Fixed).
- The info-probe family registers only when `--probe-info-endpoint` is on: `hl_info_endpoint_up` is now absent instead of a false-alarm 0 on nodes without the probe.
- `--contract-metrics-limit` now hard-caps `hl_evm_contract_tx_total` series (first N addresses keep their own series, the rest roll into `contract_address="other"`). Previously it only bounded a lookup cache while series grew without limit (observed: 7,140 series).

## [v3.0.0] - 2026-06-02

### Removed

- Removed the checked-in Grafana dashboard JSON files from the repository. Dashboards should be maintained outside the exporter release artifact.

### Added

- Added `hl_core_orders_per_block`, a replica-driven histogram for orders per block.
- Added the always-on `mempool_txs` monitor for split-client-blocks data under `data/mempool_txs/hourly/`, exposing aggregate `hl_mempool_txs_*` counters, histograms, and freshness gauges without raw order-flow labels.

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

#### Histogram buckets

`hl_core_operations_per_block` now uses wider bucket boundaries above 2000 so high-operation blocks no longer collapse directly into `+Inf`.
`hl_core_orders_per_block` now has finer bucket boundaries between 100 and 2000 orders per block so dashboard heatmaps do not collapse the common range into a few wide bands.

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

> **Superseded historical examples — do not copy for current releases.** Several expressions below rely on interpretations retired by the current metrics contract. Use the tested opt-in rules in [`alerts/`](alerts/) and the current mapping in [UPGRADING.md](UPGRADING.md).

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
