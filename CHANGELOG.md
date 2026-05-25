# Changelog

All notable changes to the Hyperliquid Exporter will be documented in this file.

## Unreleased — internal branch

Branched from upstream `main` at `ed02e16` (empty-block-dir SIGSEGV fix).
Targets node operators first, validators and data extractors second.

Verified live on a mainnet non-validator peer (24h/365d uptime, hl-node
@ commit 5f658a6, 2026-05-25). Side-by-side scrape latency matches the
production v2.0.0 build; zero error/warning lines in soak vs dozens
per minute on prod.

### Fixed

- **`internal/metrics/prometheus.go`** — replaced the hand-rolled
  timeout-via-goroutine in the `/metrics` handler with
  `http.TimeoutHandler`. The previous version raced two writers on
  the same `ResponseWriter`, producing the `superfluous
  response.WriteHeader` warning every scrape that exceeded the budget.
  Also adds `ReadHeaderTimeout` and a 5s server-shutdown deadline.
- **`internal/monitors/evm_monitor.go`** — stop emitting WARNING per
  block whose gas limit isn't exactly 2M or ≥30M. The intermediate
  3M tier is common, so the existing categorisation produced
  sustained log spam. Adds a "small" bucket; keeps "other" silent.
- **`internal/exporter/runMonitor`** wraps every monitor goroutine in
  `recover()`, logs the stack, and counts the panic. A bug in one
  monitor no longer leaves the metric permanently frozen.
- **`internal/monitors/safego.go`** — `goSafe` helper that does the
  same for the *inner* goroutines that nearly every monitor launches
  and returns from immediately (the outer wrapper alone wasn't enough
  protection). All long-running inner goroutines across consensus,
  evm, evm_account, proposal, validator_api, version, update_checker,
  validator_ip, validator_latency, validator_status, replica, and
  round_advance now run under panic recovery with their monitor name
  attributed to `hl_exporter_monitor_panics_total`.
- **`internal/monitors/consensus_monitor.go`** — `qcSignatures`,
  `tcVotes`, and `validatorCache` were unbounded maps growing with
  validator-set churn. Added last-seen tracking plus a 10-min
  housekeeping ticker that drops entries unseen for an hour;
  `validatorCache` is now an LRU.

### Added — new monitors

#### `visor` — node sync health
Reads `$NODE_HOME/hyperliquid_data/visor_abci_state.json` (live
snapshot) and falls back to `data/visor_abci_states/hourly/<date>/<hour>`
when the snapshot is absent. Exposes:

| metric | meaning |
| --- | --- |
| `hl_visor_height` | block height applied |
| `hl_visor_initial_height` | start point for the current visor process |
| `hl_visor_blocks_applied` | height − initial_height |
| `hl_visor_consensus_ahead_of_wall_seconds` | consensus_time − wall_clock_time (positive = chain ahead of local wall) |
| `hl_visor_reference_lag_seconds` | when the visor reports it |
| `hl_visor_scheduled_freeze_height` | 0 if not scheduled |
| `hl_visor_last_observation_age_seconds` | staleness of the exporter's sample, derived from the JSON's wall_clock_time |

This is the single most useful "am I in sync" signal for an operator.

#### `subsystem_latency` — per-subsystem latency profile
Reads `$NODE_HOME/data/latency_summaries/<subsystem>/<YYYYMMDD>`. 15
subsystems on the allowlist cover the block-production path and the
Tokio async runtime. Exposes
`hl_node_subsystem_latency_{mean,median,p90,p95,max,stddev}_seconds`,
`hl_node_subsystem_work_fraction`, `hl_node_subsystem_samples_total`,
each labelled by `subsystem`.

Live values on a peer node: `node_fast_begin_block_to_commit` p95
≈ 30 ms, `node_fast_block_duration` p95 ≈ 120 ms. Alert on sustained
drift here for "node is falling behind".

#### `crit_msg` — critical-message counters
Reads `$NODE_HOME/data/crit_msg_stats/{hl-node,hl-visor}/<YYYYMMDD>`.
Decoded against hl-node's `crit_msg.rs` schema (verified by binary
inspection):

| metric | meaning |
| --- | --- |
| `hl_node_bugs{source}` | cumulative `bug!` events (page-someone severity) |
| `hl_node_crits{source}` | cumulative `crit!` events |
| `hl_node_crit_locations{source}` | distinct (file, line) call sites that have fired a bug/crit |
| `hl_node_critical_messages_base_time_seconds{source}` | when the current cycle began (process start) |

mtime-based polling skips work when the file hasn't changed.

#### `tcp_traffic` — per-peer throughput
Reads `$NODE_HOME/data/tcp_traffic/hourly/<date>/<h>` every 30 s and
publishes the top-16 peers per direction plus an `ip="other"` rollup.
Per-IP cardinality stays bounded across peer churn because the monitor
remembers which `{ip,direction}` series it set last tick and
`Delete()`s any that drop out of the new top-N.

| metric | meaning |
| --- | --- |
| `hl_p2p_peer_traffic{ip,direction}` | per-peer raw throughput (top-N + "other") |
| `hl_p2p_total_traffic{direction}` | sum across all peers |
| `hl_p2p_peer_count{direction}` | distinct peers seen in the latest sample |
| `hl_p2p_sample_age_seconds` | age of the most recent sample read |

Other monitors can call `monitors.LatestTCPTrafficSnapshot()` to read
the same parsed sample without re-opening the file.

#### `parent_peer` — primary upstream detection
Inspired by dwellir-public's parent_peer_monitor. The inbound peer with
the largest byte volume is the node's effective "parent" (upstream data
source). Loss of the parent = node likely stops syncing. Consumes the
shared `tcp_traffic` snapshot, so no extra IO.

| metric | meaning |
| --- | --- |
| `hl_node_parent_peer_info{ip}` | 1 for the current parent peer (only one series active) |
| `hl_node_parent_peer_traffic` | inbound throughput attributed to the parent |
| `hl_node_parent_peer_tenure_seconds` | how long the current parent has held the role |
| `hl_node_parent_peer_switches_total` | counter, +1 each time the parent changes |

Logs a WARNING when the runner-up is within 10× of the top.

Caveat noted on a live mainnet peer: bandwidth per peer oscillates
wildly across 30s sampling windows, so the parent-of-last-sample can
flap. A moving-window variant is a candidate refinement.

#### `disk` — NODE_HOME consumption
60s tick. Walks the NODE_HOME tree once a minute and statfs'es the
host filesystem.

| metric | meaning |
| --- | --- |
| `hl_node_disk_used_bytes` | sum of regular-file sizes under NODE_HOME |
| `hl_node_disk_free_bytes` | statfs Bavail × Bsize |
| `hl_node_disk_total_bytes` | statfs Blocks × Bsize |
| `hl_node_disk_subdir_bytes{subdir}` | recursive size of hot subdirs |

Real numbers from a live peer at scrape time: replica_cmds = 154 GB,
periodic_abci_states = 44 GB, hyperliquid_data = 483 GB.

#### `process` — hl-node / hl-visor resource usage
Linux-only via `/proc`; build-tag-stubbed on other OSes. Looks up each
configured process by exact `/proc/PID/comm` match, walks fd/, parses
stat and status.

| metric | meaning |
| --- | --- |
| `hl_node_process_up{process}` | 1 if alive in /proc on this tick, 0 otherwise |
| `hl_node_process_start_time_seconds{process}` | derived from /proc btime + stat starttime |
| `hl_node_process_cpu_seconds_total{process}` | utime+stime / CLK_TCK |
| `hl_node_process_rss_bytes{process}` | VmRSS in bytes |
| `hl_node_process_virt_bytes{process}` | VmSize in bytes |
| `hl_node_process_threads{process}` | kernel-thread count |
| `hl_node_process_open_fds{process}` | count of /proc/PID/fd entries |

When the process disappears, gauges reset to 0 (rather than freeze)
so `hl_node_process_up == 0` alerting fires reliably.

#### `gossip_connections` — peer churn / capacity exhaustion
Tails `$NODE_HOME/data/node_logs/gossip_connections/hourly/<date>/<h>`.
Counts events by sanitized tag (verified_gossip_rpc,
handle_stream_connection, performing_checks_on_stream, got_tcp_greeting,
error_checking_connection, rejecting_gossip_stream_max_peers_reached,
other). The capacity-reached and error-checking rates are the
operator-actionable signals. Tracks file offset across ticks so
restarts don't replay history as a counter spike.

#### `snapshot_status` — periodic ABCI snapshot health (always on)
Watches `$NODE_HOME/data/periodic_abci_state_statuses/<date>/<height>`.
Each entry is an empty-file sentinel hl-node writes AFTER the
corresponding `.rmp` snapshot completes; the sentinel outlives the
`.rmp` itself, so this is the right source for "did the last
snapshot succeed". Emits `hl_node_snapshot_last_height`,
`hl_node_snapshot_last_age_seconds`, `hl_node_snapshot_known_count`.
Alert on age > 15 min = pipeline stalled.

#### `node_state` — freeze height + EVM checkpoint tier (always on)
Three single-file reads from `$NODE_HOME/hyperliquid_data/`. Emits
`hl_visor_freeze_abci_height`, `hl_visor_blocks_above_freeze`
(derived from the visor monitor's current height via an atomic),
`hl_evm_db_checkpoint_height{tier="fast"|"slow"}`, and
`hl_evm_db_checkpoint_lag_blocks` (sustained > 0 = EVM slow tier
falling behind).

#### `info_probe` — active HTTP liveness probe (--probe-info-endpoint)
First true liveness probe we have that doesn't depend on file
mtimes. POSTs `{"type":"meta"}` to `http://127.0.0.1:3001/info`
every 15s with a 5s timeout; counts 200 + non-empty body as up.
Emits `hl_info_endpoint_up`, `hl_info_endpoint_latency_seconds`
(histogram), `hl_info_endpoint_last_success_seconds`,
`hl_info_endpoint_failures_total`. URL configurable via
`--info-endpoint-url`. Off by default because some operators
disallow outbound HTTP from the exporter, even to localhost.

#### `tcp_lz4` — peer-level lz4 compression efficiency (--extended-metrics)
Reads `$NODE_HOME/data/tcp_lz4_stats/<date>`. Per-peer top-N with
"other" rollup + global aggregate. Compression ratio drifting
toward 1.0 = either attack traffic or a node-side regression.
Healthy steady state on a mainnet peer is ~0.62 (38% savings).

#### `log_lines` — error/warn line counters (--extended-metrics)
Counts `\n` in `$NODE_HOME/data/log/{infra,trade}/{error,warn}/<date>`.
Both files empty on a healthy node; any non-zero value is
operator-actionable. Modeled as a gauge so day-file rotation at
UTC midnight cleanly resets the series.

#### `public_ip` — IP drift + heartbeat (--extended-metrics)
hl-node rewrites `last_known_public_ip.json` every ~13 minutes as
a heartbeat. Emits `hl_node_public_ip_info{ip}`,
`hl_node_public_ip_age_seconds`, `hl_node_public_ip_changes_total`.
Alert on age > 30 min = heartbeat dead.

#### `tokio_runtime` — per-task Tokio metrics (--extended-metrics)
12-task allowlist bounds cardinality. Surfaces poll/idle/scheduled
durations, slow polls, long delays, dropped tasks. Watch for
slow-poll rates rising across MOST tasks (the
`nv_stream_forward_client_blocks` task naturally has many slow
polls — that's not the signal).

#### `operator_config` — config-file mtime ages (--extended-metrics)
mtime age of each file under `file_mod_time_tracker/`. Useful in
postmortems: "was a config touched right before this incident".

#### `tmp_dir` — orphaned tmp-file audit (--extended-metrics)
Total bytes + count of files older than 24h under `$NODE_HOME/tmp`.
The test peer carries ~2.5 GB of these from May/Jul 2025 crash
remnants; the alert catches it without ssh'ing in.

#### `tcp_connections` — kernel-truth peer connection count (always on)
Linux-only. Parses `/proc/net/tcp{,6}` every 15s and counts
connections per state on each hl-node listening port:
`hl_p2p_tcp_connections{port,state}`. The ESTABLISHED count on 4001
is the **true right-now peer count** (~6 on the test node) vs the
~260-peer file-snapshot view. TIME_WAIT pile-ups (33 observed on
4002) surface peer churn invisible from any other source. Cardinality
bounded at 4 ports × ~11 states.

#### `replica_runs` — filesystem-observable restart tracking (always on)
Counts `data/replica_cmds/<run_timestamp>/` directories — one per
hl-node startup — as `hl_node_observed_runs_total`. Emits the
newest mtime as `hl_node_observed_run_start_seconds`. Deliberate
redundancy with `/proc`-based process metrics: works in
containerized deploys where `/proc` is hidden, and serves as a
cross-check against `process_monitor`.

#### `rocksdb` — LSM tree health (--extended-metrics)
Tail-parses the last "DUMPING STATS" block from each RocksDB's LOG
file (db_hub/Rpc, evm_db_hub_fast, evm_db_hub_slow). Skips
db_hub/Evm and db_hub/Exchange because they are memtable-only on
the current chain version. Exposes:

| metric | meaning |
| --- | --- |
| `hl_rocksdb_sst_files{db}` | count of `.sst` files on disk |
| `hl_rocksdb_write_stalls_total{db,reason}` | canonical LSM "falling behind" counters (l0-file-count-limit-stops, memtable-limit-stops, pending-compaction-bytes-stops, …) |
| `hl_rocksdb_block_cache_usage_bytes{db}` | current LRU cache usage |

`pending-compaction-bytes-stops` growing = writes blocked while the
LSM catches up; the node will look sluggish before its disk does.

#### `subsystem_steps` — per-step latency under bucket_guard + tcp_lz4 (--extended-metrics)
The 19 bucket_guard sub-steps (begin_block, distribute_funding,
update_funding_rates, …) and the 4 tcp_lz4 sub-steps
(in_4001, out_4001, in_4002, out_4002) each carry their own latency
profile under `latency_summaries/<subsystem>/<step>/<date>`. The
existing subsystem_latency monitor only reads the parent subsystem;
this one walks one level deeper.

Captures tail-latency invisible from the rolled-up stats — e.g.
`begin_block` p95 may sit at 7.5ms while max occasionally spikes to
1.71s. Cardinality: 19×7 + 4×7 = 161 series.

`hl_latency_bucket_guard_seconds{step,quantile}` and
`hl_tcp_lz4_latency_seconds{direction,port,quantile}` with
quantile ∈ {p50, p90, p95, max, mean, std_dev, work_frac}.

#### `crit_locations` — per-source-location crit detail (--extended-metrics)
Parses `/tmp/crit_msg_latest_stats/hl-node.json`, the rich form
behind the on-disk crit_msg_stats tuple. Lets operators see WHICH
source code site is firing, not just the rolled-up count.

| metric | meaning |
| --- | --- |
| `hl_node_crit_location{file,line}` | crit count at that site |
| `hl_node_crit_location_last_seen_seconds{file,line}` | last activation timestamp |

`file` is the basename of the source path. Hard cap of 32 sites
(sorted by count desc), enforced as a safety net even though the
natural population is ~10.

A real example from the live node: `tcp/read.rs:35` is firing with
payload `tcp read bytes over limit @@ desc: "tcp greeting
193.176.31.213 gossip"` — an attempted-flood signal that no other
metric surfaces.

#### `shell_exec_pending` (--extended-metrics, extends tmp_dir)
`hl_node_shell_exec_pending` = file count under `$NODE_HOME/tmp/shell_rs_out/`.
Each visor shell-exec drops a (usually empty) sentinel here; sustained
growth means the visor's cleanup pass is broken. The test peer carries
1257 of these from past crashes.

#### Disk monitor — per-RocksDB rollups
`hl_node_disk_subdir_bytes` now reports six per-RocksDB rollups
under `hyperliquid_data/`: `db_hub/{Evm,Exchange,Rpc}`,
`evm_db_hub_{fast,slow}`, and `evm_db_hub_slow/checkpoint`. Lets
operators see which DB is bloating (the test peer's
`evm_db_hub_slow/checkpoint` was 391 GB out of a 395 GB slow DB —
99% of slow disk lives in one checkpoint dir).

### Added — exporter self-observability

- `/livez` — always 200 while the process is up
- `/readyz` — 200 once every registered monitor goroutine has launched
  (does NOT require real ticks, since some monitors legitimately no-op
  on non-validator nodes)
- `hl_exporter_build_info{version,commit,go_version}` — set via `-ldflags`
- `hl_exporter_monitor_started_seconds{monitor}` — goroutine launch time
- `hl_exporter_monitor_last_tick_seconds{monitor}` — last real
  processing cycle; 0 if the monitor has not yet seen data. Use
  `time() - hl_exporter_monitor_last_tick_seconds{monitor=...} > 600`
  for stall alerts.
- `hl_exporter_monitor_panics_total{monitor}` — recovered panics
- `hl_exporter_monitor_errors_total{monitor}` — errors reported
- `hl_exporter_ready` — mirrors `/readyz`

A `hl_exporter version` subcommand prints the same build info to stdout.

### Added — tests

Upstream had zero Go tests. Baseline parser unit tests for the new
monitors against real production data samples and the edge cases that
broke in development:

- `tcp_traffic_monitor_test.go` — `parseTCPTrafficLine`, `lastFullLine`
  across torn-trailing-write / no-newline / only-newlines.
- `crit_msg_monitor_test.go` — `parseCritMsgLine` + torn-last recovery
  in `readLastCritMsg`.
- `visor_monitor_test.go` — `latestHourlyFile` numeric hour sort
  (regression for the "10" < "2" lex bug found on a live node);
  `parseVisorTime` for each shape the visor emits.
- `subsystem_latency_monitor_test.go` — `readLastSummary` against real
  record + torn-last-line.
- `gossip_connections_monitor_test.go` — `parseGossipConnectionLine`
  tag mapping, "other" fallback, malformed inputs.

`go test ./internal/monitors/` runs in under a second.

### Added — flags

| flag | purpose |
| --- | --- |
| `--metrics-port N` | listener port for `/metrics` (default 8086) |
| `--skip-version-check` | skip the local `hl-node --version` probe (containers) |
| `--skip-update-check` | skip the upstream binary download (containers) |
| `--probe-info-endpoint` | enable active HTTP probe of `--serve-info` |
| `--info-endpoint-url <url>` | override probe URL (default http://127.0.0.1:3001/info) |
| `--extended-metrics` | enable the extended monitor set (tcp_lz4, log_lines, public_ip, tokio_runtime, operator_config, tmp_dir) |

### Operator semantics — at-a-glance

Critical alerts a node operator should set on this exporter:

| symptom | metric expression |
| --- | --- |
| node crashed | `hl_node_process_up{process="hl-node"} == 0` for > 30s |
| visor crashed | `hl_node_process_up{process="hl-visor"} == 0` for > 30s |
| info endpoint down | `hl_info_endpoint_up == 0` for > 30s (requires `--probe-info-endpoint`) |
| bug! emitted | `hl_node_bugs{source="hl-node"} > 0` |
| crits accumulating | `rate(hl_node_crits[5m]) > 0` for > 5m |
| disk filling | `hl_node_disk_free_bytes / hl_node_disk_total_bytes < 0.10` |
| not in sync | `hl_visor_last_observation_age_seconds > 60` |
| snapshot pipeline stalled | `hl_node_snapshot_last_age_seconds > 900` |
| EVM tier divergence | `hl_evm_db_checkpoint_lag_blocks > 100` for > 10m |
| max peers reached | `rate(hl_p2p_gossip_events_total{event_type="rejecting_gossip_stream_max_peers_reached"}[5m]) > 0` |
| fast state begin-to-commit slow | `hl_node_subsystem_latency_p95_seconds{subsystem="node_fast_begin_block_to_commit"} > 0.1` for > 5m |
| peer churn elevated | `rate(hl_p2p_gossip_events_total{event_type="error_checking_connection"}[5m]) > 0.1` |
| public IP heartbeat dead | `hl_node_public_ip_age_seconds > 1800` (requires `--extended-metrics`) |
| lz4 compression collapsed | `hl_p2p_lz4_global_ratio > 0.85` for > 10m (requires `--extended-metrics`) |
| orphaned tmp files growing | `hl_node_tmp_stale_files > 100` (requires `--extended-metrics`) |
| node logged something | `hl_node_log_lines{level="error"} > 0` (requires `--extended-metrics`) |
| live peer count dropped | `hl_p2p_tcp_connections{port="4001",state="ESTABLISHED"} < 3` for > 5m |
| peer churn on gossip-2 | `hl_p2p_tcp_connections{port="4002",state="TIME_WAIT"} > 50` |
| RocksDB write-stalled | `rate(hl_rocksdb_write_stalls_total{reason=~"pending-compaction-bytes-stops|l0-file-count-limit-stops"}[5m]) > 0` (requires `--extended-metrics`) |
| RocksDB cache exhausted | `hl_rocksdb_block_cache_usage_bytes / on() group_left() rocksdb_cache_capacity > 0.9` (capacity is a config constant — set as a recording rule) |
| begin_block tail-latency | `hl_latency_bucket_guard_seconds{step="begin_block",quantile="max"} > 1` for > 5m (requires `--extended-metrics`) |
| same crit hot-firing | `rate(hl_node_crit_location[5m]) > 0.5` (requires `--extended-metrics`) |
| visor shell-cleanup broken | `hl_node_shell_exec_pending > 5000` (requires `--extended-metrics`) |
| exporter monitor stuck | `time() - hl_exporter_monitor_last_tick_seconds > 600` (per monitor) |
| exporter panicking | `rate(hl_exporter_monitor_panics_total[5m]) > 0` |

### Deferred

- Top-N per-peer TCP traffic moving-window stabilization (parent_peer flaps)
- File-position persistence so monitors don't drop in-flight data on restart
- TCP-probe peer latency (dwellir has it; lower priority than passive obs)
- Outbound peer discovery beyond what tcp_traffic exposes
- Per-step `latency_buckets` granularity (the 19 bucket_guard steps and
  3 tcp_lz4 steps under `data/latency_buckets/` are not yet surfaced
  individually — `subsystem_latency_monitor` covers the parent subsystems)

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
- `hl_block_height` → `hl_core_block_height`
- `hl_block_time_milliseconds` → `hl_core_block_time_milliseconds`
- `hl_latest_block_time` → `hl_core_latest_block_time`
- `hl_apply_duration` → `hl_metal_apply_duration`
- `hl_apply_duration_milliseconds` → `hl_metal_apply_duration_milliseconds`
- `hl_proposer_count_total` → `hl_consensus_proposer_count_total`
- `hl_validator_count` → `hl_consensus_validator_count`
- `hl_validator_jailed_status` → `hl_consensus_validator_jailed_status`
- `hl_validator_stake` → `hl_consensus_validator_stake`
- `hl_validator_active_status` → `hl_consensus_validator_active_status`
- `hl_validator_rtt` → `hl_consensus_validator_rtt`
- `hl_total_stake` → `hl_consensus_total_stake`
- `hl_jailed_stake` → `hl_consensus_jailed_stake`
- `hl_not_jailed_stake` → `hl_consensus_not_jailed_stake`
- `hl_active_stake` → `hl_consensus_active_stake`
- `hl_inactive_stake` → `hl_consensus_inactive_stake`

..with addition of many more brand new metrics

#### CLI Flags
- `--enable-otlp` renamed to `--otlp`
- `--evm` renamed to `--evm-metrics`
- `--otlp-endpoint` default value removed (now required when OTLP enabled)

### Removed
- `--enable-prom` flag (Prometheus now always enabled)
- `--disable-prom` flag (Prometheus now always enabled)
- `hl_evm_transactions_total` metric (replaced by `hl_evm_tx_type_total`)
