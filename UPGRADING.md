# UPGRADING

## Breaking metrics cleanup (post-v3)

> **These changes are BREAKING.** Metrics are renamed, retyped, dropped, and relabeled. Prometheus treats a renamed/retyped metric as a brand-new series, so **historical data does NOT carry over** - old series stop at the upgrade and the new series start fresh. There is no automatic bridge. Update every dashboard panel, alerting rule, and recording rule that references the old names/labels before (or immediately after) upgrading.

### Renamed metrics

| old | new | what to update |
|---|---|---|
| `hl_p2p_peers_total` | `hl_p2p_peers` | Dashboards/alerts/recording rules referencing the metric. The `_total` suffix is gone (it was never a counter). |
| `hl_node_observed_runs_total` | `hl_node_observed_runs` | Dashboards/alerts/recording rules. `_total` suffix dropped; still a point-in-time gauge - keep using `delta()`/`changes()`, not `rate()`. |
| `hl_node_replay_events_total` | `hl_node_replay_events` | Dashboards/alerts/recording rules. `_total` suffix dropped; still a point-in-time gauge - keep using `delta()`/`changes()`, not `rate()`. |
| `hl_go_heap_inuse_mb` | `hl_go_heap_inuse_bytes` | Dashboards/alerts/recording rules. **Now emits raw bytes** (was MB) - drop any `* 1024 * 1024` math; set panel unit to "bytes" (auto-scales). Rescale byte-based thresholds (multiply old MB thresholds by 1024^2). |
| `hl_go_heap_idle_mb` | `hl_go_heap_idle_bytes` | Same as above - raw bytes, rescale thresholds and units. |
| `hl_go_sys_mb` | `hl_go_sys_bytes` | Same as above - raw bytes, rescale thresholds and units. |
| `hl_evm_max_priority_fee` | `hl_evm_max_priority_fee_gwei` | Dashboards/alerts/recording rules. Value/unit unchanged (still Gwei); only the name gained the `_gwei` suffix. The separate `hl_evm_max_priority_fee_gwei_distribution` histogram is **not** affected. |
| `hl_consensus_qc_participation_rate` | `hl_consensus_qc_participation_percent` | Dashboards/alerts/recording rules. Value unchanged (a 0-100 percentage); the new name reflects the unit. |

### Type changes: Gauge -> Counter (name unchanged)

| metric | what to update |
|---|---|
| `hl_exporter_monitor_panics_total` | Now a true Counter; in-process monotonic, resets only on exporter restart. `rate()`/`increase()` are now valid (and were not before). Existing `delta()` recipes still work. |
| `hl_exporter_monitor_errors_total` | Same - switch alerts to `rate()`/`increase()` if desired. |
| `hl_p2p_peers_added_total` | Same - `rate(hl_p2p_peers_added_total[...])` is now valid. |
| `hl_p2p_peers_evicted_total` | Same - `rate(hl_p2p_peers_evicted_total[...])` is now valid. |

### Dropped metric

| metric | what to update |
|---|---|
| `hl_metal_apply_duration` (bare gauge) | Removed as redundant. Repoint any panel/alert to the kept histogram `hl_metal_apply_duration_milliseconds` (e.g. `histogram_quantile(0.95, ..._milliseconds_bucket)`). |

### Relabeled metric

| metric | label change | what to update |
|---|---|---|
| `hl_consensus_validator_stake` | `moniker` -> `name` | Any query/legend/group-by using `{moniker=...}` or `by (moniker)` on this metric must switch to `name`. Value source (`summary.Name`) is unchanged; this aligns it with the sibling validator metrics that already use `name`. |

### work_frac split out of `_seconds` metrics

The per-step latency gauges carried a `quantile="work_frac"` series - a unitless 0..1 duty-cycle that did not belong in a `_seconds` metric. It now lives in dedicated unitless gauges; the `quantile` label no longer has a `work_frac` value.

| old series | new metric | what to update |
|---|---|---|
| `hl_latency_bucket_guard_seconds{quantile="work_frac"}` | `hl_latency_bucket_guard_work_fraction{step}` | Repoint any panel/alert filtering `quantile="work_frac"` to the new gauge (no `quantile` label on it). |
| `hl_latency_consensus_seconds{quantile="work_frac"}` | `hl_latency_consensus_work_fraction{step}` | Same. |
| `hl_latency_l1_task_seconds{quantile="work_frac"}` | `hl_latency_l1_task_work_fraction{step}` | Same. |
| `hl_tcp_lz4_latency_seconds{quantile="work_frac"}` | `hl_tcp_lz4_work_fraction{direction,port}` | Same. |

---

# UPGRADING: v2.0.0 to v3

## TL;DR

- Backward compatible. Every v2.0.0 metric kept with the same name, labels, and meaning.
- Default behavior unchanged. Without any new flag, v3 exposes the v2 set plus a handful of always-on additions.
- 3 new optional CLI flags.
- Existing systemd unit files work unchanged.

> **Note:** The "v2.0.0 to v3" section below describes the v3.0.0 release. The post-v3 *breaking metrics cleanup* documented at the top of this file supersedes the "no metric renamed" guarantees here for the names listed in those tables.

## Unchanged from v2.0.0

- `:8086/metrics` listener (override with `--metrics-port`).
- All `hl_core_*`, `hl_consensus_*`, `hl_metal_*`, `hl_evm_*`, `hl_software_*`, `hl_p2p_non_val_*`, `hl_go_*` metric names + labels.
- OTLP export.
- Flags: `--chain`, `--otlp`, `--otlp-endpoint`, `--otlp-insecure`, `--alias`, `--node-home`, `--node-binary`, `--evm-metrics`, `--contract-metrics`, `--contract-metrics-limit`, `--replica-metrics`, `--validator-rtt`, `--log-level`.

## New, always-on

| family | meaning |
| --- | --- |
| `hl_visor_*` | sync state (height, blocks_applied, consensus_ahead_of_wall_seconds, reference_lag, freeze_height, blocks_above_freeze, observation_age) |
| `hl_node_snapshot_*` | periodic ABCI snapshot completion sentinels |
| `hl_evm_db_checkpoint_*` | fast/slow EVM tier checkpoints + lag |
| `hl_node_subsystem_latency_*` | per-subsystem latency, 15-subsystem allowlist |
| `hl_node_bugs`, `hl_node_crits`, `hl_node_crit_locations` | crit_msg_stats decoded |
| `hl_p2p_peer_traffic{ip,direction}` | top-16 peers + `ip="other"` rollup |
| `hl_p2p_total_traffic`, `hl_p2p_peer_count`, `hl_p2p_sample_age_seconds` | tcp_traffic aggregates |
| `hl_node_parent_peer_*` | upstream peer detection |
| `hl_node_disk_*` | NODE_HOME size + statfs + per-RocksDB subdirs |
| `hl_node_process_*` | hl-node + hl-visor /proc resource use |
| `hl_p2p_gossip_events_total` | gossip-connection event tags |
| `hl_p2p_tcp_connections{port,state}` | kernel-truth from /proc/net/tcp |
| `hl_node_observed_runs_total`, `hl_node_observed_run_start_seconds` | filesystem restart tracking |
| `hl_visor_freeze_abci_height`, `hl_visor_blocks_above_freeze` | replay floor + delta |
| `hl_exporter_build_info`, `hl_exporter_monitor_*`, `hl_exporter_ready` | self-observability |

New endpoints: `/livez`, `/readyz`.

## New, opt-in flags

### `--probe-info-endpoint` (`--info-endpoint-url URL`)

Active HTTP probe of the node's `--serve-info` endpoint (POST `:3001/info {"type":"meta"}`).

Exposes `hl_info_endpoint_up`, `hl_info_endpoint_latency_seconds` (histogram), `hl_info_endpoint_last_success_seconds`, `hl_info_endpoint_failures_total`.

Off by default since some operators block outbound HTTP from the exporter.

### `--extended-metrics`

| family | source |
| --- | --- |
| `hl_p2p_lz4_*` | per-peer + global lz4 compression |
| `hl_node_log_{lines,bytes}` | infra/trade error/warn day-files |
| `hl_node_public_ip_*` | `last_known_public_ip.json` |
| `hl_tokio_task_*` | 12-task Tokio runtime stats |
| `hl_node_operator_config_age_seconds` | `file_mod_time_tracker` mtimes |
| `hl_node_tmp_bytes`, `hl_node_tmp_stale_files`, `hl_node_shell_exec_pending` | `$NODE_HOME/tmp` audit |
| `hl_rocksdb_sst_files`, `hl_rocksdb_write_stalls_total`, `hl_rocksdb_block_cache_usage_bytes` | RocksDB LOG |
| `hl_latency_bucket_guard_seconds`, `hl_tcp_lz4_latency_seconds` | per-step latency |
| `hl_node_crit_location{file,line}`, `hl_node_crit_location_last_seen_seconds` | crit detail from `/tmp/crit_msg_latest_stats/hl-node.json` |

### `--skip-version-check`, `--skip-update-check`

For container deployments where `hl-node` isn't on the exporter's filesystem. Closes upstream issue #16.

### `--metrics-port`

Default 8086. Useful for side-by-side test deploys and port conflicts.

### `--per-peer-metrics`

Emit `hl_p2p_peer_last_seen_seconds{ip}` and `hl_p2p_peer_first_seen_seconds{ip}` for every peer in the exporter's internal peer set. Set is LRU-capped at 2048 with a 24h TTL, so cardinality is bounded.

Off by default. The peer set itself (and the rolled-up `hl_p2p_unique_peers_seen{window}` gauges over 5m/1h/24h) is always on; only the per-IP labels gate on this flag.

## Behavior differences to know

### `hl_consensus_validator_rtt` now reports milliseconds

`hl_consensus_validator_rtt` now reports **milliseconds**. It previously emitted microseconds (raw numbers ~1000x larger). The metric name and instrument type are unchanged. Operators with RTT-based alerts or dashboards must rescale their thresholds (divide existing microsecond thresholds by 1000).

### EVM `block_type` label gained `"small"`

v2 emitted `"standard"` for <=2M and `"high"` for >=30M, with everything in between labeled `"other"` plus a WARNING log line per block. 3M-gas blocks are now common. v3:

- Adds `"small"` (exactly 3M)
- Keeps `"other"` silent
- No WARNING log

If a v2 alert filtered on `block_type="other"`, change to `block_type!~"standard|high"` to catch both `"small"` and `"other"`.

### Flag-gated metrics appear at zero when their flag is off

Metrics added by `--probe-info-endpoint` and `--extended-metrics` auto-register at startup, so they show up in `/metrics` with value 0 even when the flag is off. Example: `hl_info_endpoint_up 0` is exposed by any v3 build, even without `--probe-info-endpoint`.

This means

```
hl_info_endpoint_up == 0          # fires on every exporter without --probe-info-endpoint
hl_p2p_lz4_global_ratio < 0.5     # fires on every exporter without --extended-metrics
```

produces permanent false positives on nodes without the flag.

Guard the alert with a presence check:

```
hl_info_endpoint_up == 0
  and on() hl_exporter_monitor_last_tick_seconds{monitor="info_probe"} > 0
```

or scope by `hl_exporter_build_info` to nodes you expect to have the flag enabled.

## Keeping the v2 surface only

Don't pass any new flag. To also drop the always-on additions from your scrape, use a relabel:

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

## Everything on

```sh
hl_exporter start \
  --chain mainnet \
  --replica-metrics --evm-metrics --contract-metrics \
  --probe-info-endpoint \
  --extended-metrics
```

~140 metric families. Scrape latency identical to v2 (~10ms). CPU 5-10% higher than v2 from disk walks; drop `--extended-metrics` if tight.

## Rollout

1. Swap binary. Existing systemd unit works unchanged.
2. Add `--probe-info-endpoint`. Verify `hl_info_endpoint_up == 1`.
3. Add `--extended-metrics`.
4. Update alerts per the operator-semantics table in CHANGELOG.md.

## Not changed

- No v2 metric renamed, relabeled, or removed.
- No existing flag default changed.
- No required new flag.
- OTLP schema unchanged.
