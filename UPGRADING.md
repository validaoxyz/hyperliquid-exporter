# Upgrading Hyperliquid Exporter

## Upgrading to v4.0.6

This release is breaking. It removes unproven dimensions and dead families, adds explicit source health, and renames several metrics whose old names asserted more than the source proves. Audit dashboards, alerts, recording rules, and remote-write consumers before rollout.

Deprecated compatibility aliases are transition-only and scheduled for removal in the next major release. Their type and HELP in [the generated inventory](docs/metrics.md#generated-metric-inventory) are authoritative.

### Before upgrading

1. Export the metric names used by dashboards, alerts, and recording rules.
2. Compare them with the mapping below. Do not use a broad prefix regex as a substitute.
3. Deploy the new opt-in rules from `alerts/` separately from the binary.
4. Canary one exporter and verify `hl_exporter_source_*`, monitor lifecycle, and source-specific age before migrating queries.
5. Remove uses of raw IP/address labels from alerts.

### Current `start` flags

The table is checked against the executable FlagSet. Empty values can be resolved later from environment or configuration.

<!-- BEGIN CURRENT START FLAG INVENTORY -->
| Flag | Type | Default |
|---|---|---|
| `--alias` | string | `""` |
| `--chain` | string | `""` |
| `--contract-metrics` | bool | `false` |
| `--contract-metrics-limit` | int | `20` |
| `--disable-tcp6` | bool | `false` |
| `--evm-metrics` | bool | `false` |
| `--extended-metrics` | bool | `false` |
| `--info-endpoint-url` | string | `""` |
| `--log-level` | string | `"info"` |
| `--metrics-port` | int | `8086` |
| `--node-binary` | string | `""` |
| `--node-home` | string | `""` |
| `--otlp` | bool | `false` |
| `--otlp-endpoint` | string | `""` |
| `--otlp-insecure` | bool | `false` |
| `--per-peer-metrics` | bool | `false` |
| `--pprof` | bool | `false` |
| `--probe-info-endpoint` | bool | `false` |
| `--replica-metrics` | bool | `false` |
| `--skip-update-check` | bool | `false` |
| `--skip-version-check` | bool | `false` |
| `--tcp-service-ports` | string | `"3001,3999,4001,4002,4003,4004"` |
| `--validator-rtt` | bool | `false` |
<!-- END CURRENT START FLAG INVENTORY -->

There is no EVM block-type flag. `--contract-metrics` now means capped recipient-address diagnostics, not contract detection. `--per-peer-metrics` now means at most 16 current explicit child identities, not historical IP tracking. `--validator-rtt` retains its flag name for compatibility but enables TCP-connect diagnostics, not protocol RTT.

### Old to current metrics

| Old metric or query | Current target | Migration boundary |
|---|---|---|
| `hl_evm_base_fee_gwei{block_type}` and its distribution | Same family without that label | Breaking label removal. Accepted blocks refresh the current value, including zero. |
| `hl_evm_gas_used{block_type}`, `hl_evm_gas_limit{block_type}`, `hl_evm_gas_util{block_type}` | Same unlabeled families plus `hl_evm_gas_used_ratio_available` | Raw block fields only. Zero or invalid limit makes the ratio unavailable; no block class is inferred. |
| `hl_evm_max_priority_fee_gwei{block_type}` and its distribution | Same family without that label | Current gauge returns to zero on accepted no-fee blocks; the histogram observes only available fee inputs. |
| `hl_evm_tx_per_block{block_type}` | `hl_evm_tx_per_block` | Every accepted block is observed, including zero-transaction blocks. |
| `hl_evm_tx_type_total{type,block_type}` | `hl_evm_tx_type_total{type}` | Labels emitted now are `Legacy`, `Eip1559`, `Eip2930`, and `other`. |
| `hl_evm_contract_create_total` | `hl_evm_tx_shape_total{shape="create"}` | Shape is score-grade; no gas tier or address is attached. |
| `hl_evm_contract_tx_total{...}` | `hl_evm_tx_shape_total{shape="message"}` and optional `hl_evm_recipient_tx_total{address}` | Not one-to-one. Recipient diagnostics are address-only, process-capped, and have no contract/name/token/symbol/type enrichment. |
| `hl_evm_account_count` | none | No production-safe constant-cost source exists. Do not replace it with a scan or zero placeholder. |
| no old equivalent | receipt outcome, count mismatch, system-item, precompile, and parser families | Failed receipt means source `success=false`, not necessarily revert. Mismatch is array-length only; system items are opaque. |
| `hl_consensus_validator_count` | `hl_consensus_validators` | Latest complete validator-summary response row count, not committee size. |
| validator-summary stake families | Same names | Values are raw `1e-8 HYPE` units. Active and jailed are raw API predicates. |
| `hl_consensus_validator_rtt` | `hl_consensus_validator_tcp_connect_duration_seconds`, last-success/age, and attempts | Old family is a deprecated IP-labelled millisecond mirror. New data is TCP connect, not protocol latency. |
| `hl_consensus_heartbeat_ack_received_total` | `hl_consensus_heartbeat_peer_acks_total` | Old family is removed. Replacement excludes self loops and uses canonical bounded identity. |
| `hl_consensus_heartbeat_ack_delay_ms` | `hl_consensus_heartbeat_peer_ack_delay_seconds` and `hl_consensus_self_heartbeat_loop_duration_seconds` | Old family is removed. Peer and local loopback are separate. |
| vote round/age used as continuous liveness | Same names plus `hl_consensus_accepted_vote_observations_total` | Peer votes are leadership-sampled. There is no committee-coverage denominator. |
| accumulator names written without `_total` | Exact generated `_total` names | Each accepted window adds `delta`; `n` is the source-window observation count. `RoundCatchUp` direction is unknown. |
| RPC registered interpreted as served | `hl_consensus_rpc_events_total` and exact `hl_consensus_rpc_blocks_served_total` | Registered/sent accumulator values are outbound local work. Served blocks exist only for the proven response branch. |
| `hl_node_snapshot_known_count` | `hl_node_snapshot_sentinels_retained` | Retained count in at most two newest valid date directories; not cadence or capacity. |
| `hl_evm_db_checkpoint_height{tier}` | `hl_node_persisted_abci_height{source_class}` | Deprecated old name is persisted core/ABCI file evidence, not current HyperEVM head. |
| `hl_evm_db_checkpoint_lag_blocks` | `hl_node_persisted_abci_height_gap{comparison="fast_minus_slow"}` | File-value difference only; not current execution divergence. |
| `hl_visor_freeze_abci_height` | `hl_node_persisted_freeze_abci_height{source}` | Persisted file value; not necessarily current scheduled freeze. |
| `hl_visor_blocks_above_freeze` | `hl_node_visor_height_above_persisted_freeze{comparison}` | Nonnegative comparison only when both operands are available. |
| scheduled freeze with zero meaning none | current value plus `hl_visor_scheduled_freeze_height_available` | Null/unavailable is distinct from a real zero. |
| `hl_node_observed_run_start_seconds` described as mtime | Same name for parsed immutable start plus `hl_node_observed_run_last_activity_seconds` | Neither proves run end or duration. |
| `hl_node_replay_last_seconds` described as last activity | Same name for parsed immutable start plus `hl_node_replay_last_activity_seconds` | Marker activity does not prove replay completion or duration. |
| `hl_p2p_peer_count{direction}` | `hl_p2p_traffic_endpoints{direction}` | Latest complete traffic-snapshot rows; observation is not connectivity. |
| `hl_p2p_tcp_connections{port,state}` | `hl_p2p_tcp_socket_connections{service_port,service_side,state}` | Old alias sums local and remote literal-port sides for one transition release. |
| old LZ4 `_total` and ratio families | `hl_p2p_lz4_window_*` and `hl_p2p_lz4_global_window_*` | Old names are deprecated latest-window gauges, never cumulative counters. Byte/compression layer remains unresolved. |
| `hl_node_parent_peer_*` | `hl_p2p_dominant_inbound_*` | Breaking rename with no causal equivalence. The replacement is only a deterministic inbound-traffic candidate. |
| `hl_p2p_non_val_peer_connections{verified}` | `hl_p2p_child_peers{verified}` | Old one-release view counts explicit child rows, not sockets. |
| `hl_p2p_non_val_peers_total` | sum of `hl_p2p_child_peers` | Explicit fresh child snapshot only; not all connected peers. |
| `hl_p2p_non_val_connections` | `hl_p2p_child_connections` | Sum of source `connection_count`, separate from peer count. |
| old peer-set aggregate families | `hl_p2p_traffic_endpoints_current`, `_seen`, `_added_total`, `_evicted_total{reason}` | Process-local traffic-only set; two-fresh-positive admission, 2048 cap, 24-hour TTL, restart reset. |
| per-IP first/last-seen peer history | none | Removed. Optional current child identity is not history and is not a one-to-one replacement. |
| `hl_node_rate_limited_files{stream}` | retained, recent, mtime, scan, and failure families | Old alias mirrors retained nonempty-file evidence only. It is not an offender/event/current-block count. |
| child start/crash projections | `hl_node_child_stderr_artifacts{state,reason}` and scan/timestamp state | Bounded retained evidence only. Unknown, unreadable, truncated, and empty are not silently crashes. |
| generic tmp totals used for alerts | material/receipt class, material stale bytes/files, and scan state | Legacy total retains expected empty receipts. Alert only on material evidence after a complete scan. |
| `hl_node_disk_used_bytes` interpreted as physical | Keep as apparent bytes; add `hl_node_disk_allocated_bytes` | No silent reinterpretation. Allocated bytes deduplicate `(device,inode)` within scope. |
| `hl_core_tx_total` | `hl_replica_signed_actions_total{action_type}` | Compatibility name calls signed actions transactions. |
| `hl_core_operations_total` | `hl_replica_operations_total{action_type,category}` | Counts individual operations, including array items. |
| `hl_core_orders_total` | `hl_replica_orders_total` | Counts individual order operations. |
| `hl_core_blocks_processed_total` equated to action bundles | `hl_core_blocks_processed_total` remains block records; `hl_replica_action_bundles_total` is separate | No one-to-one migration between record and bundle units. |
| replica `hf` interpreted as current hardfork version | `hl_visor_hardfork_version` plus availability | Replica artifact `hf` is not interpreted. Only the visor state field is accepted. |
| `hl_exporter_monitor_last_tick_seconds` | `hl_exporter_monitor_last_valid_observation_seconds`, plus attempt/publication | Old name is a deprecated last-valid alias, not loop activity. |
| `/readyz` or `hl_exporter_ready` used for data health | monitor lifecycle plus common source state | Readiness remains worker-launch history and can stay true through later source/worker failure. |
| no old equivalent | `hl_exporter_config_info{chain}` | Validated configured chain only; not observed-node chain and not a wrong-chain detector. |

### Query changes

- Raw upstream `work_fraction` values are not clamped to 0..1 and are not a portable duty cycle.
- Do not call `rate()` or `increase()` on deprecated gauge-typed `_total` families. Check the generated inventory type.
- Do not use `absent()` as a generic optional-source failure test. Guard on source enabled/read/schema state.
- Do not infer parentage, committee membership, protocol RTT, active rate limiting, snapshot cadence, current HyperEVM checkpoint, current freeze, or consumer stall from similarly named evidence.
- Do not place raw IP/address labels in alert grouping or annotations.

### Archived v3 relabel recipe

The old v3 migration published a regex for selecting its then-new surface. Its mechanically repaired form is retained only to reproduce that historical match set:

<!-- BEGIN ARCHIVED V3 RELABEL REGEX -->
```regex
^(hl_visor_.*|hl_p2p_tcp_connections|hl_node_(snapshot_.*|process_.*|disk_.*|bugs|crits|crit_locations|observed_runs|observed_run_start_seconds|subsystem_.*|parent_peer_.*)|hl_evm_db_checkpoint_.*|hl_exporter_.*)$
```
<!-- END ARCHIVED V3 RELABEL REGEX -->

This does not reconstruct a supported current "v2 surface." Later families and breaking semantics are outside that pattern. Migrate current queries with the explicit table above.

### Rollout

1. Run `go test ./...` and validate generated docs before packaging.
2. Load only the alert profiles that match enabled/platform sources.
3. Canary one target and compare old aliases with replacements during the transition release.
4. Migrate recording rules before dashboards so query failures are visible early.
5. Remove deprecated aliases from consumers before the next major release.

## Archived release migrations

The sections below are historical context, not current operational guidance.

### v3.1

v3.1 removed duplicate/dead metrics, made the committed consensus accumulator families real exporter-lifetime counters, registered info-probe families only when the probe was enabled, and capped the then-current enriched EVM diagnostic. The current release supersedes the old enriched contract surface, block-tier guidance, directional catch-up interpretation, and generic `absent()` advice.

### v3.0

v3.0 renamed gauge families that incorrectly ended in `_total`, converted exporter panic/error and peer add/evict instruments to counters, moved `work_frac` out of seconds-labelled quantile gauges, added split-client mempool monitoring, and removed checked-in dashboards. Dashboard removal remains intentional.

Its historical `--per-peer-metrics` IP-history behavior, parent-peer interpretation, LZ4 compression claims, and alert recipes are superseded by the current source contracts.

### v2.0

v2.0 introduced the `hl_core_*`, `hl_consensus_*`, `hl_metal_*`, and `hl_evm_*` prefixes, renamed `--enable-otlp` to `--otlp` and `--evm` to `--evm-metrics`, and made Prometheus always on. These flag names remain historical facts; they are not accepted current aliases.
