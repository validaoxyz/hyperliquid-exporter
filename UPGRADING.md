# UPGRADING — v2.0.0 to v3-internal

Quick-reference for what changed, what stayed the same, and what to
think about when rolling v3-internal into a place that's been running
v2.0.0.

## TL;DR

- **Backward-compatible.** Every v2.0.0 metric still exists with the
  same name, labels, and meaning. Existing dashboards and alert rules
  work without modification.
- **Default behavior unchanged.** Without any new flag, v3-internal
  exposes the v2.0.0 metric set plus a small number of always-on
  additions. It does NOT enable extended monitors, info probing, or
  any new flag-gated behavior by default.
- **64 new metric families** in the default-on set (v2 had 74, v3 has
  ~138 with extended flags enabled).
- **3 new CLI flags**, all optional. Existing systemd unit files
  continue to work unchanged.
- **No required upgrade order.** v3-internal can be deployed onto a
  v2.0.0 host with no node-side coordination.

## What didn't change

All these continue to work exactly as in v2.0.0:

- The default `:8086` listener (override with `--metrics-port`).
- Every `hl_core_*`, `hl_consensus_*`, `hl_metal_*`, `hl_evm_*`,
  `hl_software_*`, `hl_p2p_non_val_*`, `hl_go_*` metric. Names, labels,
  semantics, all unchanged.
- The OTLP export path (the v2 OTel instruments still feed it).
- The existing flags: `--chain`, `--otlp`, `--otlp-endpoint`,
  `--otlp-insecure`, `--alias`, `--node-home`, `--node-binary`,
  `--evm-metrics`, `--contract-metrics`, `--contract-metrics-limit`,
  `--replica-metrics`, `--validator-rtt`, `--log-level`.
- The systemd-unit-friendly behavior (signal handling, graceful
  shutdown).

## What's new — always on

These are added even without setting any new flag. They're cheap
(file reads, no heavy parsing) and broadly useful:

| family | meaning |
| --- | --- |
| `hl_visor_*` | sync-health from the visor JSON: height, blocks_applied, consensus_ahead_of_wall_seconds, reference_lag, freeze_height, blocks_above_freeze, last_observation_age_seconds |
| `hl_node_snapshot_*` | periodic ABCI snapshot completion sentinels: last_height, last_age_seconds, known_count |
| `hl_evm_db_checkpoint_*` | fast/slow EVM tier checkpoint heights + lag |
| `hl_node_subsystem_latency_*` | per-subsystem latency (15-subsystem allowlist) — mean/median/p90/p95/max/stddev/work_frac |
| `hl_node_bugs`, `hl_node_crits`, `hl_node_crit_locations` | crit_msg_stats decoded against hl-node's crit_msg.rs schema |
| `hl_p2p_peer_traffic{ip,direction}` | top-16 peer throughput + "other" rollup |
| `hl_p2p_total_traffic`, `hl_p2p_peer_count`, `hl_p2p_sample_age_seconds` | tcp_traffic aggregates |
| `hl_node_parent_peer_*` | upstream parent-peer detection (info, traffic, tenure_seconds, switches_total) |
| `hl_node_disk_*` | NODE_HOME byte size + statfs free/total + per-RocksDB subdir sizes |
| `hl_node_process_*` | hl-node + hl-visor /proc resource use (up, cpu, rss, threads, fds, start_time, virt) |
| `hl_p2p_gossip_events_total{event_type}` | gossip-connection events |
| `hl_p2p_tcp_connections{port,state}` | kernel-truth peer connection state from /proc/net/tcp |
| `hl_node_observed_runs_total`, `hl_node_observed_run_start_seconds` | filesystem-observable hl-node restart tracking |
| `hl_visor_freeze_abci_height`, `hl_visor_blocks_above_freeze` | replay floor + height delta |
| `hl_exporter_build_info`, `hl_exporter_monitor_*`, `hl_exporter_ready` | exporter self-observability |

Plus the new endpoints: **`/livez`** (always 200 while running) and
**`/readyz`** (200 once every registered monitor has launched).

## What's new — opt-in via flags

### `--probe-info-endpoint` (+ `--info-endpoint-url`)

Adds an active HTTP probe of the node's `--serve-info` endpoint
(POST `:3001/info` with `{"type":"meta"}`). This is the only true
live-process probe; everything else reads files which can lie under
clock skew or a write-stuck node.

Exposes `hl_info_endpoint_up`, `hl_info_endpoint_latency_seconds`
(histogram), `hl_info_endpoint_last_success_seconds`,
`hl_info_endpoint_failures_total`. Off by default because some
operators prohibit outbound HTTP from the exporter, even to localhost.

### `--extended-metrics`

Adds six additional monitors. None of them are expensive on their
own; the flag groups them so a deep-dashboard operator can opt in
without sprinkling per-feature flags:

| family | source |
| --- | --- |
| `hl_p2p_lz4_*` | per-peer + global lz4 compression efficiency |
| `hl_node_log_{lines,bytes}` | infra/trade error/warn day-files |
| `hl_node_public_ip_*` | last_known_public_ip.json heartbeat + drift |
| `hl_tokio_task_*` | 12-task Tokio runtime stats |
| `hl_node_operator_config_age_seconds` | file_mod_time_tracker mtimes |
| `hl_node_tmp_bytes`, `hl_node_tmp_stale_files`, `hl_node_shell_exec_pending` | `$NODE_HOME/tmp` audit |
| `hl_rocksdb_sst_files`, `hl_rocksdb_write_stalls_total`, `hl_rocksdb_block_cache_usage_bytes` | parsed from each RocksDB's LOG file |
| `hl_latency_bucket_guard_seconds`, `hl_tcp_lz4_latency_seconds` | per-step latency breakdowns under bucket_guard + tcp_lz4 |
| `hl_node_crit_location{file,line}` + `hl_node_crit_location_last_seen_seconds` | per-source-line crit detail from `/tmp/crit_msg_latest_stats/hl-node.json` |

### `--skip-version-check` and `--skip-update-check`

For container deployments where the `hl-node` binary isn't reachable
on the exporter's filesystem. Closes upstream issue #16. The
existing `version_monitor` and `update_checker` would otherwise
log errors every 30 minutes.

### `--metrics-port`

Default still 8086 (so no behavior change), but now configurable —
useful when scrape ports conflict or when running side-by-side test
deploys.

## Subtle differences worth checking

These will not break a v2 dashboard but the values may move slightly
or new label values may appear:

### EVM `block_type` label gained a `"small"` value

`hl_evm_*{block_type}` in v2 emitted `"standard"` for ≤2M and
`"high"` for ≥30M, with everything in between labeled `"other"` —
plus a WARNING log line per `"other"` block. 3M-gas-limit blocks are
now common, so v3-internal:

- Adds a `"small"` block_type value (exactly 3M)
- Keeps `"other"` silent for anything else in the middle
- No longer logs a WARNING per intermediate block

If any v2 alert filtered on `block_type="other"`, it will see less
traffic. Recommended: change the filter to
`block_type!~"standard|high"` to catch both `"small"` and `"other"`.

### Scrape behavior under timeout

The v2 `/metrics` handler raced two writers on the same
`ResponseWriter` when a scrape exceeded its 30s timeout — produced
`superfluous response.WriteHeader` log spam and could emit a
half-written response with a trailing 503. v3 uses
`http.TimeoutHandler` so timeouts return a clean 503 and never spam
the log. If a v2 alert was set on the `superfluous response` log
line, it will become silent — by design.

### Flag-gated metrics still appear (at zero) when their flag is off

The metrics added by `--probe-info-endpoint` and
`--extended-metrics` are auto-registered at startup, so they show up
on `/metrics` with their default zero value even when the relevant
flag wasn't passed. Example: `hl_info_endpoint_up 0` is exposed by
any v3 build, even without `--probe-info-endpoint` — it just never
updates from zero because the probe never runs.

This is normal Prometheus client-library behavior, not a bug, but
it means an alert written as

```
hl_info_endpoint_up == 0   # fires on EVERY exporter without --probe-info-endpoint
hl_p2p_lz4_global_ratio < 0.5   # fires on every exporter without --extended-metrics
```

…will produce permanent false positives on nodes where the flag
isn't enabled.

Two ways to guard:

1. Pair the alert with a presence check that proves the monitor
   actually ran:

   ```
   hl_info_endpoint_up == 0
     and on() hl_exporter_monitor_last_tick_seconds{monitor="info_probe"} > 0
   ```

2. Or scope the alert by the build's commit/version (use
   `hl_exporter_build_info`) so you only alert on the deployments
   you expect to have the flag enabled.

The same logic applies to every flag-gated family.

### Bounded label cardinality

v2's `consensus_monitor` accumulated three unbounded maps
(`qcSignatures`, `tcVotes`, `validatorCache`) that grew with
validator-set churn over months. v3 trims entries unseen for 1h on
a 10-min housekeeping ticker, and `validatorCache` is now an LRU.
The exposed metric set is the same; the underlying memory footprint
is now bounded.

## What if you only want the v2 surface?

Just don't pass any new flag. The default-on additions don't break
anything but they DO expose more series — if your Prometheus
ingestion has tight per-target time-series limits, you may want to
either:

- Use a relabel-drop in the scrape config:

  ```yaml
  scrape_configs:
    - job_name: hyperliquid
      static_configs:
        - targets: ['node:8086']
      metric_relabel_configs:
        - source_labels: [__name__]
          regex: 'hl_(visor|p2p_tcp_connections|node_(snapshot|process|disk|bugs|crits|crit_locations|observed_runs|observed_run_start|subsystem_|parent_peer)|evm_db_checkpoint_|exporter_)_.*'
          action: drop
  ```

  …drops the v3 additions and keeps the v2 surface.

- Or wait for a future build that supports `--legacy-v2-only`. Not
  there today; raise it if you need it.

## What if you want everything?

```sh
hl_exporter start \
  --chain mainnet \
  --replica-metrics --evm-metrics --contract-metrics \
  --probe-info-endpoint \
  --extended-metrics
```

…produces ~140 metric families on a non-validator peer. Scrape
latency on our test node is identical to v2 (~10ms). CPU is ~5–10%
higher than v2 due to the disk walks (subdir size tracking) and the
tcp_traffic dedupe; if that's tight, drop `--extended-metrics`.

## What if you're rolling onto an existing systemd unit?

The default unit on the ValiDAO test node was:

```ini
ExecStart=/usr/local/bin/hl_exporter start \
  --chain mainnet \
  --replica-metrics --evm-metrics --contract-metrics
```

v3-internal accepts this exact line — no edit required. If you want
the round-2-onward improvements, just add `--probe-info-endpoint
--extended-metrics` to the same line and `systemctl daemon-reload`
+ `restart`.

## Rollout sequence we recommend

1. Drop the v3-internal binary in place. Don't change any flags
   yet. Scrape works, all v2 dashboards continue to render, you
   get the always-on additions for free.
2. Add `--probe-info-endpoint`. Verify `hl_info_endpoint_up == 1`
   on the dashboard.
3. Add `--extended-metrics`. New families appear; existing ones
   unchanged.
4. Update alert rules to use the new operator-actionable signals
   from the CHANGELOG's "Operator semantics" table.

## What we INTENTIONALLY did NOT do

- Did not change any v2.0.0 metric's name, labels, or semantics.
- Did not change any existing flag default.
- Did not introduce a required new flag.
- Did not change the OTLP export schema.

The v3-internal branch is a strict superset of v2.0.0 in terms of
exposed metrics. If something looks like a regression, please flag
it — it would be a bug.
