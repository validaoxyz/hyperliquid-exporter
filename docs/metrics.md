# Hyperliquid Exporter metrics

This reference separates what the exporter observed from what an operator may infer. The inventory at the end is generated from the live metric declarations. Histograms appear once by family name; Prometheus also exposes their `_bucket`, `_sum`, and `_count` series.

## Reading the inventory

- `base` means the instrument is registered during normal exporter initialization. A label-bearing vector can still be absent until its source produces a value.
- `info-probe` means registration requires `--probe-info-endpoint`.
- `jailing` means registration occurs only after a valid validator jailing-config file is read.
- Counters are monotonic only for the exporter process lifetime. Use `rate()` or `increase()` and expect a reset after restart.
- Gauges represent current source values or retained last-good observations. Read their source-health and age companions before treating a value as current.
- A few deprecated gauges retain `_total` or `_count` in their names. Their inventory type is authoritative; never choose a query function from the suffix alone.
- The `Exporters` column is exact. Optional OTLP includes only families marked `OTel bridge`; Prometheus-only families are not sent through OTLP.

The inventory labels are the bounded union emitted by current production paths. Resource and scrape labels such as `job`, `instance`, and operator-added labels are not repeated in every row.

## Exporter lifecycle and source truth

`/livez` reports that the HTTP process is serving. `/readyz` and `hl_exporter_ready` report only that every registered worker launched. They do not attest that node files exist, parse successfully, remain fresh, or have published metrics. A worker can also exit after readiness became true.

Use these families together:

- `hl_exporter_monitor_registered`, `hl_exporter_monitor_running`, `hl_exporter_monitor_workers`, and `hl_exporter_monitor_exited_seconds` describe worker lifecycle.
- `hl_exporter_monitor_last_attempt_seconds`, `hl_exporter_monitor_last_valid_observation_seconds`, and `hl_exporter_monitor_last_publication_seconds` separate polling from accepted data and publication.
- `hl_exporter_source_enabled`, `present`, `read_ok`, `schema_ok`, `ever_observed`, source timestamp, last-valid age, and bounded error counters describe each fixed source.
- `hl_exporter_monitor_last_tick_seconds` is a deprecated compatibility alias for the last valid observation. It is not generic loop activity.

For tri-state source gauges, `-1` means not attempted or not applicable. A confirmed optional absence is not the same as an unreadable or malformed source. Failed and partial reads retain the last complete snapshot, qualified by health and age, instead of fabricating a healthy zero.

`hl_exporter_config_info{chain}` is immutable configured identity. `chain` is validated as `mainnet` or `testnet`; it is not an observation of the connected node and cannot detect a wrong-chain deployment by itself.

The metrics listener binds `:PORT` by default, which is a wildcard network bind. Treat it as an operator trust boundary. The server uses a 5-second read-header timeout, a 30-second scrape-handler deadline, at most five concurrent scrapes, a 35-second write timeout, and a 60-second idle timeout. The scrape cap does not block `/livez` or `/readyz`.

## HyperCore and replica records

Block-time sources can use the legacy single stream or the fast/slow pair. `state_type` appears only for the dual streams. A regressing height/time pair, malformed record, or overflowing apply duration is rejected without advancing the baseline.

Replica metrics require `--replica-metrics` and the node's replica command style. Counting units are intentionally separate:

- block records are completely validated top-level replica records;
- signed actions count validated actions within those records;
- operations count individual items inside supported action arrays;
- orders count individual order operations;
- action bundles and response outcomes have their own families.

`hl_core_last_processed_round` retains its compatibility name, but the new `hl_replica_last_processed_height` is the decoded top-level chain height. A malformed item rejects the record atomically. A genuinely valid empty block remains valid. Replica proposer labels are record provenance, not proof of a network-wide relay path.

The replica artifact's `hf` field is not interpreted. Current hardfork version comes only from the explicit visor-state `hardfork_version` field and its availability gauge.

## HyperEVM

Enable the stream with `--evm-metrics`.

- Gas used, gas limit, gas-used ratio availability, base fee, priority fee, transaction count, and transaction type are raw accepted-block observations. Gas limit is data, not a portable block class.
- The score-grade transaction shape is the fixed set `create`, `message`, and `unknown`. Only a schema-defined null destination is creation; the zero address is an ordinary destination.
- `--contract-metrics` is a compatibility flag for an address-only recipient diagnostic. It makes no contract assertion and performs no explorer lookup or name, symbol, token, or type enrichment. The process-sticky cap keeps at most N canonical addresses plus `address="other"`.
- Emitted transaction type labels are `Legacy`, `Eip1559`, `Eip2930`, and `other`. `other` is forward-compatible and is not a statement that the protocol has only four types.
- Receipt outcomes are `success`, `failed`, and `unknown`. `failed` means the source boolean was false; it does not necessarily mean EVM revert.
- Transaction/receipt mismatch compares array lengths only. The source does not prove receipt identity or ordering.
- System-transaction items are opaque array items. Precompile outcomes are only `ok` or `other`; no address or error text becomes a label.
- Receipt, system-item, and call wrappers are decoded lazily only when consumed. Disabled or unused siblings are not parsed.

The removed account-count metric has no replacement: no production-safe constant-cost source is known. Fee histogram buckets remain unchanged until representative mainnet and testnet distributions justify a migration.

## Validator API and consensus

Validator-summary stake values are raw `1e-8 HYPE` units. `hl_consensus_validators` is the number of rows in the latest complete `validatorSummaries` response, not a committee size. `active`, `jailed`, and `api_active_and_unjailed` are the source predicates; the last means exactly `isActive && !isJailed` and does not establish committee membership or next-proposer status.

Optional `--validator-rtt` diagnostics perform bounded outbound TCP connects to fresh API-active-and-unjailed targets that also have fresh local identity/IP evidence. The primary duration is seconds and is not protocol response latency. The old IP-labelled millisecond family is deprecated compatibility only.

Consensus event semantics are source-limited:

- peer vote observations are sampled when this node is the next leader; vote age is not a continuous committee-liveness denominator;
- heartbeat peer acknowledgements and local self-loop duration are separate;
- heartbeat durations are seconds on the observed build;
- disconnect evidence is status-reporter evidence in the consensus-round domain, not a causal diagnosis;
- `RoundCatchUp` direction remains upstream-opaque;
- accumulator counters add each accepted window's `delta`; `n` is the source-window observation count;
- registered and sent RPC accumulator values are outbound local work, not served requests;
- `hl_consensus_rpc_blocks_served_total` counts only the explicitly proven complete outbound-response branch.

Validator latency EMA is the upstream measured field used by the sampled node build. It is not an exporter-measured protocol RTT. A complete all-zero EMA snapshot is treated as initialization; mixed snapshots preserve genuine zeros.

## Network and peer evidence

TCP traffic rows expose the source's latest point-rate field. Its formal unit and socket-side ownership are unresolved. `In` and `Out` are source directions, not origin, child, or topology roles.

`hl_p2p_dominant_inbound_*` is a deterministic traffic heuristic over inbound candidates. It is not a parent, relay, block source, sync dependency, quality score, or causal signal. The implementation aggregates inbound ports, applies EWMA/hysteresis, clears stale or complete-zero snapshots, and exposes bounded tie/challenger evidence.

Kernel socket metrics use `service_port`, `service_side`, and `state`. `service_side` states whether the configured literal appeared on the local or remote side; it is not connection direction. The default bounded service-port vocabulary is `3001,3999,4001,4002,4003,4004`. Port 3001 is the info service and 3999 is the observed EVM-RPC bridge; this exporter does not assign a stable protocol role to 4004.

LZ4 metrics are latest paired-window observations. The old `_total` names are deprecated gauges, not cumulative counters. The byte/compression layer is unresolved, and the weighted ratio is a weighting of upstream-reported values rather than a derived compression ratio.

Explicit child topology comes only from complete fresh `child_peers status` snapshots. Aggregate child counts are always available after a valid snapshot. `--per-peer-metrics` adds at most 16 current canonical child identities plus connection/tenure diagnostics; it does not publish per-IP history.

The separate traffic-endpoint set is process-local and aggregate-only. Admission needs positive top-16 traffic in two consecutive fresh complete samples; the set has a 2048 cap and 24-hour TTL and resets on exporter restart. It is not a durable verified-peer registry.

Rate-limit retained counts and the deprecated alias count non-empty regular files only in the lexicographically newest source-date directory. `recent_files` and the nonempty-file mtime candidate scan the full fixed stream tree; future mtimes are excluded from time-derived gauges. These families remain file evidence: they do not identify an offender, event, current block, or active rate limiting.

## Host, process, and retained files

Process metrics are Linux/procfs-only. Selection validates the executable/cmdline and deterministically chooses the oldest eligible process. CPU time is cumulative for the selected process epoch and can reset when that process changes. IO counters accumulate only positive deltas during one `(pid,starttime)` epoch. Guard process-down or FD queries with the `process` source state.

Disk families keep logical apparent bytes and add unique allocated bytes deduplicated by `(device,inode)` within scope. Directory walks and `statfs` have separate health. A partial walk never replaces the last complete tree snapshot. Fixed filesystem path state reports only `present_nonempty`, `present_empty`, or `absent`; it is not node health or a canonical layout claim.

The legacy tmp totals include expected empty shell receipts. Use the receipt/material class families and successful scan state for current retained-material evidence. Child-stderr classification is a bounded read of retained artifacts; empty, unreadable, truncated, unknown, and explicit panic evidence remain distinct. A hardfork/upgrade classification is observed evidence, not a durable binary-upgrade guarantee.

Public-IP age is wall-clock age since the observed file mtime. It has no universal heartbeat or refresh cadence. Optional stream age describes a fixed source's last accepted record; it does not prove a downstream consumer stalled.

RocksDB gauges publish one complete parsed stats block per database and retain the last good snapshot through read/schema failures. They do not implement a general warning, corruption, or disk-full taxonomy.

## Runs, replay, snapshots, and persisted state

- `hl_node_observed_run_start_seconds` is the immutable timestamp parsed from the newest retained run directory name. Activity mtime is separate. Neither proves run end or duration.
- `hl_node_replay_last_seconds` is immutable parsed replay start. `hl_node_replay_last_activity_seconds` is retained marker-directory activity. Neither proves replay completion or duration.
- Snapshot sentinel count is a retained window of at most the two newest valid date directories, not cadence or capacity. Prefer height, lag, availability, and age when their source domains match.
- Persisted freeze/checkpoint replacements name the exact core/ABCI file provenance. They are not authoritative current HyperEVM head or current scheduled-freeze state.
- Scheduled freeze uses a value plus an availability gauge so null/unavailable is not confused with a real zero.

## Mempool

Mempool and split-client mempool streams publish only complete newline-delimited records. Current snapshots commit atomically. Parser, structured-error, prune, drop, and source-age families use bounded enums and counts only; hashes, accounts, payload text, and cross-stream dwell joins are intentionally absent.

`committed_tx_hashes` is a bounded post-commit dedup cache, not mempool depth or capacity. Oldest-uncommitted age comes only from a complete valid `Size stats` snapshot and remains qualified by independent size-source health.

## Query and cardinality rules

- Prefer the common source-state families over `absent()` for optional or lazy metrics.
- Never infer freshness from `/readyz` or the deprecated last-tick alias.
- Do not aggregate raw upstream `work_fraction` as a 0..1 duty cycle. Values above 1 are preserved.
- Do not use raw IP/address labels in alerts. They exist only on bounded diagnostics.
- Do not average per-node quantile gauges across instances as though they were histogram samples.
- Audit deprecated aliases before the next major release. The exact old-to-new map is in [UPGRADING.md](../UPGRADING.md).

## Generated metric inventory

Run `go generate ./internal/metrics` after changing a declaration. `go test ./internal/metrics -run 'Inventory|PrometheusMetadata'` checks the generated block against the declaration census, verifies HELP and static labels for native collectors, checks lazy profiles, and rejects retired high-risk labels.

<!-- BEGIN GENERATED METRIC INVENTORY -->
<!-- Generated by: go generate ./internal/metrics -->

### Current families (366)

| Metric | Type | Backend | Labels | Registration | HELP | Source |
|---|---|---|---|---|---|---|
| `hl_consensus_accepted_vote_observations_total` | Counter | Prometheus | - | base | Accepted vote observations since exporter start; peer votes are leadership-sampled and this counter is not a committee-coverage denominator. | `validator_consensus_prometheus.go` |
| `hl_consensus_active_stake` | Gauge | OTel bridge | - | base | Raw validatorSummaries stake summed over isActive rows, including jailed rows, in 1e-8 HYPE units | `instruments.go` |
| `hl_consensus_api_active_and_unjailed_stake` | Gauge | OTel bridge | - | base | Raw validatorSummaries stake summed over rows that are both API-active and not jailed, in 1e-8 HYPE units | `instruments.go` |
| `hl_consensus_api_active_and_unjailed_validators` | Gauge | OTel bridge | - | base | Number of validators both active and not jailed in the latest complete validatorSummaries response; not a committee count | `instruments.go` |
| `hl_consensus_block_direction_events_total` | Counter | Prometheus | `direction` | base | Block message events observed since exporter start by wire direction; duplicate out/in copies remain distinct here while block-level observations are deduplicated. | `validator_consensus_prometheus.go` |
| `hl_consensus_committed_blocks_total` | Counter | Prometheus | - | base | Accepted CommittedBlocks source-window delta accumulated since exporter start; source n is an observation count and is not exported here. | `prometheus_instruments.go` |
| `hl_consensus_committed_tx_bytes_total` | Counter | Prometheus | - | base | Accepted CommittedTxBytes source-window delta accumulated since exporter start; source n is an observation count and is not exported here. | `prometheus_instruments.go` |
| `hl_consensus_committed_txs_total` | Counter | Prometheus | - | base | Accepted CommittedTxs source-window delta accumulated since exporter start; source n is an observation count and is not exported here. | `prometheus_instruments.go` |
| `hl_consensus_current_round` | Gauge | OTel bridge | - | base | Latest local consensus round observed from a fixture-proven round-advance, status, or block event | `instruments.go` |
| `hl_consensus_dropped_txs_total` | Counter | Prometheus | - | base | Accepted DroppedTxs source-window delta accumulated since exporter start; no cause or severity is inferred. | `prometheus_instruments.go` |
| `hl_consensus_heartbeat_ack_join_total` | Counter | Prometheus | `kind`, `outcome` | base | Heartbeat acknowledgement joins since exporter start by fixed kind and outcome; random identifiers are never labels. | `validator_consensus_prometheus.go` |
| `hl_consensus_heartbeat_ack_observed` | Gauge | OTel bridge | `name`, `signer`, `validator` | base | Whether last_ack_duration was numeric in the latest complete status snapshot (1=numeric including zero, 0=explicit null) | `instruments.go` |
| `hl_consensus_heartbeat_peer_ack_delay_seconds` | Histogram | Prometheus | - | base | Delay in seconds for exact non-self heartbeat acknowledgement joins; local loopback is excluded. | `validator_consensus_prometheus.go` |
| `hl_consensus_heartbeat_peer_acks_total` | Counter | Prometheus | `name`, `signer`, `validator` | base | Exact non-self heartbeat acknowledgements joined since exporter start by canonical validator identity. | `validator_consensus_prometheus.go` |
| `hl_consensus_heartbeat_sent_total` | Counter | OTel bridge | `name`, `signer`, `validator` | base | Total heartbeats sent by validators (validator nodes only) | `instruments.go` |
| `hl_consensus_heartbeat_status` | Gauge | OTel bridge | `name`, `signer`, `status_type`, `validator` | base | Seconds-valued heartbeat fields from the latest complete status snapshot; status_type is since_last_success or last_ack_duration | `instruments.go` |
| `hl_consensus_inactive_stake` | Gauge | OTel bridge | - | base | Raw validatorSummaries stake summed over non-active rows, in 1e-8 HYPE units | `instruments.go` |
| `hl_consensus_jailed_stake` | Gauge | OTel bridge | - | base | Raw validatorSummaries stake summed over isJailed rows, in 1e-8 HYPE units | `instruments.go` |
| `hl_consensus_local_round` | Gauge | Prometheus | `field` | base | Latest fixture-proven local consensus-round value by fixed field; silence makes source age grow and does not imply jail or network lag. | `validator_consensus_prometheus.go` |
| `hl_consensus_local_round_lag` | Gauge | Prometheus | `field` | base | Nonnegative difference between same-domain local consensus round fields from fixture-proven events. | `validator_consensus_prometheus.go` |
| `hl_consensus_monitor_errors_total` | Counter | OTel bridge | `monitor_type` | base | Total errors encountered by consensus monitor | `instruments.go` |
| `hl_consensus_monitor_last_processed` | Gauge | OTel bridge | `monitor_type` | base | Unix timestamp of last processed line by monitor type | `instruments.go` |
| `hl_consensus_monitor_lines_processed_total` | Counter | OTel bridge | `monitor_type` | base | Total lines processed by consensus monitor | `instruments.go` |
| `hl_consensus_not_jailed_stake` | Gauge | OTel bridge | - | base | Raw validatorSummaries stake summed over non-jailed rows, in 1e-8 HYPE units | `instruments.go` |
| `hl_consensus_proposer_count_total` | Counter | OTel bridge | `name`, `signer`, `validator` | base | Total number of blocks proposed by each validator | `instruments.go` |
| `hl_consensus_qc_participation_percent` | Gauge | OTel bridge | `name`, `signer`, `validator` | base | QC signing participation rate per validator as percentage (validator nodes only) | `instruments.go` |
| `hl_consensus_qc_round_lag` | Gauge | OTel bridge | - | base | Nonnegative block round minus embedded QC round for the latest accepted Block carrying a QC; a single observed value, not an average | `instruments.go` |
| `hl_consensus_qc_signatures_total` | Counter | OTel bridge | `name`, `signer`, `validator` | base | Total QC signatures by each validator (validator nodes only) | `instruments.go` |
| `hl_consensus_qc_size` | Histogram | OTel bridge | - | base | Distribution of QC signer counts | `instruments.go` |
| `hl_consensus_round_advance_events_total` | Counter | Prometheus | `reason` | base | Local round-advance events since exporter start by bounded reason. | `validator_consensus_prometheus.go` |
| `hl_consensus_round_catchup_total` | Counter | Prometheus | - | base | Positive RoundCatchUp delta accumulated since the exporter started observing; upstream direction semantics are intentionally unspecified. | `prometheus_instruments.go` |
| `hl_consensus_round_qc_total` | Counter | Prometheus | - | base | Accepted RoundQc source-window delta accumulated since exporter start; no relationship to committed blocks is inferred. | `prometheus_instruments.go` |
| `hl_consensus_round_tc_total` | Counter | Prometheus | - | base | Accepted RoundTc source-window delta accumulated since exporter start; no cause or network-health conclusion is inferred. | `prometheus_instruments.go` |
| `hl_consensus_rounds_per_block` | Gauge | OTel bridge | - | base | Most recent positive difference between consecutive accepted consensus Block round values; a single observed value, not an average | `instruments.go` |
| `hl_consensus_rpc_blocks_served_total` | Counter | Prometheus | - | base | Blocks explicitly reported sent by complete query_peers=false Outbound response Ok.BlocksAndTxs results since exporter start. | `validator_consensus_prometheus.go` |
| `hl_consensus_rpc_events_total` | Counter | Prometheus | `content`, `direction`, `outcome`, `stage` | base | Consensus RPC lifecycle events since exporter start, classified only into fixed direction, stage, outcome, and content vocabularies. | `validator_consensus_prometheus.go` |
| `hl_consensus_rpc_parse_total` | Counter | Prometheus | `result` | base | Consensus RPC source records since exporter start by bounded parser result. | `validator_consensus_prometheus.go` |
| `hl_consensus_rpc_requests_registered_total` | Counter | Prometheus | - | base | Accepted RpcRequestsRegistered delta for outbound local accumulator work since exporter start; not a served-request count. | `prometheus_instruments.go` |
| `hl_consensus_rpc_requests_sent_total` | Counter | Prometheus | - | base | Accepted RpcRequestsSent delta for outbound local accumulator work since exporter start; no request-lifecycle stage is inferred. | `prometheus_instruments.go` |
| `hl_consensus_self_heartbeat_loop_duration_seconds` | Histogram | Prometheus | - | base | Duration in seconds of exact local heartbeat loopback joins, kept separate from peer latency. | `validator_consensus_prometheus.go` |
| `hl_consensus_status_api_active_and_unjailed_validators` | Gauge | Prometheus | `state` | base | Count from the latest complete status snapshot joined strictly to the latest API active-and-unjailed set; missing/null is never synthesized as zero. | `validator_consensus_prometheus.go` |
| `hl_consensus_status_field_reported` | Gauge | Prometheus | `field` | base | Whether a fixed nested field was present in the latest complete status snapshot (1=present, 0=omitted). | `validator_consensus_prometheus.go` |
| `hl_consensus_tc_blocks_total` | Counter | OTel bridge | `name`, `signer`, `validator` | base | Total blocks proposed with timeout certificates (Note: proposer of TC block is not who caused the timeout) | `instruments.go` |
| `hl_consensus_tc_participation_total` | Counter | OTel bridge | `name`, `signer`, `validator` | base | Total timeout votes sent by each validator (validator nodes only) | `instruments.go` |
| `hl_consensus_tc_size` | Histogram | OTel bridge | - | base | Distribution of TC timeout vote counts | `instruments.go` |
| `hl_consensus_total_stake` | Gauge | OTel bridge | - | base | Sum of raw validatorSummaries stake in 1e-8 HYPE units | `instruments.go` |
| `hl_consensus_validator_active_status` | Gauge | OTel bridge | `name`, `signer`, `validator` | base | Raw isActive from the latest complete validatorSummaries response (1=active, 0=inactive); not committee membership | `instruments.go` |
| `hl_consensus_validator_api_active_and_unjailed_status` | Gauge | OTel bridge | `name`, `signer`, `validator` | base | Whether the latest complete validatorSummaries row is API-active and not jailed (1=yes, 0=no); this is not committee membership | `instruments.go` |
| `hl_consensus_validator_commission_rate` | Gauge | Prometheus | `name`, `signer`, `validator` | base | Validator commission as a fraction (0.04 = 4%). | `prometheus_instruments.go` |
| `hl_consensus_validator_connectivity` | Gauge | OTel bridge | `name`, `reporter_name`, `reporter_signer`, `reporter_validator`, `signer`, `validator` | base | Value 0 for each subject and reporter relation in the latest complete disconnected_validators status snapshot; absence is not connected=1 | `instruments.go` |
| `hl_consensus_validator_disconnected_since_round` | Gauge | OTel bridge | `name`, `reporter_name`, `reporter_signer`, `reporter_validator`, `signer`, `validator` | base | Consensus round at which a status reporter began reporting a signer as disconnected, from the latest complete status snapshot | `instruments.go` |
| `hl_consensus_validator_jailed_local` | Gauge | Prometheus | `name`, `signer`, `validator` | base | 1 for each signer in current_jailed_validators from the latest successfully parsed local status row; unknown registry joins remain explicit and no cadence is assumed. | `prometheus_instruments.go` |
| `hl_consensus_validator_jailed_status` | Gauge | OTel bridge | `name`, `signer`, `validator` | base | Raw isJailed from the latest complete validatorSummaries response (1=jailed, 0=not jailed) | `instruments.go` |
| `hl_consensus_validator_latency_ema_seconds` | Gauge | OTel bridge | `name`, `signer`, `validator` | base | Exponential moving average of validator latency in seconds | `instruments.go` |
| `hl_consensus_validator_latency_ema_state` | Gauge | Prometheus | `state` | base | One-hot state of the latest EMA generation: measured, initializing, no_data, or invalid. | `validator_consensus_prometheus.go` |
| `hl_consensus_validator_latency_poll_max_seconds` | Gauge | Prometheus | `signer` | base | Largest complete raw latency sample observed for the signer during the latest exporter poll; withdrawn with the raw-latency snapshot. | `source_snapshot_prometheus.go` |
| `hl_consensus_validator_latency_round` | Gauge | OTel bridge | `name`, `signer`, `validator` | base | Last consensus round number for validator latency measurement | `instruments.go` |
| `hl_consensus_validator_latency_seconds` | Gauge | OTel bridge | `name`, `signer`, `validator` | base | Current latency for each validator in seconds | `instruments.go` |
| `hl_consensus_validator_predicted_apr` | Gauge | Prometheus | `name`, `period`, `signer`, `validator` | base | predictedApr from validatorSummaries stats, per period (day/week/month), as a fraction. | `prometheus_instruments.go` |
| `hl_consensus_validator_recent_blocks` | Gauge | Prometheus | `name`, `signer`, `validator` | base | Raw nRecentBlocks field from the latest complete validatorSummaries response; the API window is upstream-defined and zero has no inferred cause. | `prometheus_instruments.go` |
| `hl_consensus_validator_stake` | Gauge | OTel bridge | `name`, `signer`, `validator` | base | Raw validatorSummaries stake for each validator in 1e-8 HYPE units | `instruments.go` |
| `hl_consensus_validator_tcp_connect_attempts_total` | Counter | Prometheus | `name`, `outcome`, `signer`, `validator` | base | TCP connect probe attempts since exporter start by canonical validator identity and bounded outcome. | `validator_consensus_prometheus.go` |
| `hl_consensus_validator_tcp_connect_duration_seconds` | Gauge | Prometheus | `name`, `signer`, `validator` | base | Most recent successful TCP connect duration in seconds for a fresh API-active-and-unjailed validator target; not protocol RTT. | `validator_consensus_prometheus.go` |
| `hl_consensus_validator_tcp_connect_last_success_age_seconds` | Gauge | Prometheus | `name`, `signer`, `validator` | base | Seconds since the most recent successful TCP connect to a fresh API-active-and-unjailed validator target. | `validator_consensus_prometheus.go` |
| `hl_consensus_validator_tcp_connect_last_success_timestamp_seconds` | Gauge | Prometheus | `name`, `signer`, `validator` | base | Unix timestamp of the most recent successful TCP connect to a fresh API-active-and-unjailed validator target. | `validator_consensus_prometheus.go` |
| `hl_consensus_validator_unjailable_after_seconds` | Gauge | Prometheus | `name`, `signer`, `validator` | base | Positive unjailableAfter Unix timestamp (ms epoch normalized to seconds) reported for a jailed validator; zero/null sentinels are omitted. The countdown is (value - time()). | `prometheus_instruments.go` |
| `hl_consensus_validator_uptime_fraction` | Gauge | Prometheus | `name`, `period`, `signer`, `validator` | base | uptimeFraction from validatorSummaries stats, per period (day/week/month). | `prometheus_instruments.go` |
| `hl_consensus_validators` | Gauge | OTel bridge | - | base | Number of rows in the latest complete validatorSummaries response; not a committee count | `instruments.go` |
| `hl_consensus_vote_round` | Gauge | OTel bridge | `name`, `signer`, `validator` | base | Last observed vote round per validator; peer votes are leadership-sampled when this node is next proposer, not continuous coverage | `instruments.go` |
| `hl_consensus_vote_time_diff_seconds` | Gauge | OTel bridge | `name`, `signer`, `validator` | base | Age of the last observed vote per validator; peer observations are leadership-sampled and not a continuous liveness denominator | `instruments.go` |
| `hl_core_block_begin_wall_receipt_lag_seconds` | Histogram | Prometheus | `source_class` | base | Nonnegative exporter receipt lag since begin_block_wall_time, by block-time source class; this is local wall-clock observation lag, not network propagation time. | `validator_consensus_prometheus.go` |
| `hl_core_block_height` | Gauge | OTel bridge | - | base | Current block height of the chain | `instruments.go` |
| `hl_core_block_time_milliseconds` | Histogram | OTel bridge | `state_type` | base | Distribution of time between blocks in milliseconds | `instruments.go` |
| `hl_core_blocks_processed_total` | Counter | OTel bridge | - | base | Completely validated replica_cmds block records observed since exporter start | `instruments.go` |
| `hl_core_last_processed_round` | Gauge | OTel bridge | - | base | Last processed round in the Hyperliquid network | `instruments.go` |
| `hl_core_last_processed_time` | Gauge | OTel bridge | - | base | Last processed time in the Hyperliquid network | `instruments.go` |
| `hl_core_latest_block_time` | Gauge | OTel bridge | - | base | Latest block time | `instruments.go` |
| `hl_core_operations_per_block` | Histogram | OTel bridge | - | base | Distribution of individual operations per block in the Hyperliquid network | `instruments.go` |
| `hl_core_operations_total` | Counter | OTel bridge | `category`, `type` | base | Individual operations inside validated replica signed actions (array items for orders, cancels, and batch modifications) | `instruments.go` |
| `hl_core_orders_per_block` | Histogram | OTel bridge | - | base | Distribution of individual order operations per validated replica block | `instruments.go` |
| `hl_core_orders_total` | Counter | OTel bridge | - | base | Individual order operations inside validated replica order and twapOrder actions | `instruments.go` |
| `hl_core_round_advance` | Histogram | Prometheus | - | base | Distribution of positive round - parent_round values in accepted replica block records. | `prometheus_instruments.go` |
| `hl_core_tx_per_block` | Histogram | OTel bridge | - | base | Distribution of validated signed actions per replica block; compatibility name retains tx | `instruments.go` |
| `hl_core_tx_total` | Counter | OTel bridge | `type` | base | Validated signed actions observed in replica_cmds, by closed action type; compatibility name retains tx | `instruments.go` |
| `hl_evm_base_fee_gwei` | Gauge | OTel bridge | - | base | Base fee per gas (Gwei) from latest EVM block header | `instruments.go` |
| `hl_evm_base_fee_gwei_distribution` | Histogram | OTel bridge | - | base | Distribution of EVM base fee (Gwei) | `instruments.go` |
| `hl_evm_block_height` | Gauge | OTel bridge | - | base | Current block height of the EVM chain | `instruments.go` |
| `hl_evm_block_time_milliseconds` | Histogram | OTel bridge | - | base | Distribution of EVM block time in milliseconds | `instruments.go` |
| `hl_evm_gas_limit` | Gauge | OTel bridge | - | base | Gas limit in EVM transactions | `instruments.go` |
| `hl_evm_gas_used` | Gauge | OTel bridge | - | base | Gas used in EVM transactions | `instruments.go` |
| `hl_evm_gas_used_ratio_available` | Gauge | Prometheus | - | base | 1 when hl_evm_gas_util is available for the latest accepted block, 0 when its gas limit is zero or invalid. | `evm_prometheus.go` |
| `hl_evm_gas_util` | Gauge | OTel bridge | - | base | Gas utilization in EVM transactions | `instruments.go` |
| `hl_evm_latest_block_time` | Gauge | OTel bridge | - | base | Latest EVM block time | `instruments.go` |
| `hl_evm_max_priority_fee_gwei` | Gauge | OTel bridge | - | base | Maximum priority fee in EVM transactions | `instruments.go` |
| `hl_evm_max_priority_fee_gwei_distribution` | Histogram | OTel bridge | - | base | Distribution of EVM max priority fee (Gwei) | `instruments.go` |
| `hl_evm_parse_errors_total` | Counter | Prometheus | `reason`, `stage` | base | Rejected EVM stream records by bounded parser stage and reason. | `evm_prometheus.go` |
| `hl_evm_read_precompile_calls_total` | Counter | Prometheus | `outcome` | base | Structurally valid nested read-precompile calls by bounded result shape: ok when a non-null Ok member is present, otherwise other. | `evm_prometheus.go` |
| `hl_evm_receipts_total` | Counter | Prometheus | `outcome` | base | Accepted EVM user receipts by boolean execution outcome; false is failed, not necessarily reverted. | `evm_prometheus.go` |
| `hl_evm_recipient_tx_total` | Counter | Prometheus | `address` | base | Opt-in diagnostic count by canonical recipient address; addresses beyond the configured sticky cap use address="other". This does not assert that the recipient is a contract. | `evm_prometheus.go` |
| `hl_evm_system_transaction_items_total` | Counter | Prometheus | - | base | Opaque items observed in structurally valid system_txs arrays; no item semantics or outcomes are inferred. | `evm_prometheus.go` |
| `hl_evm_tx_per_block` | Histogram | OTel bridge | - | base | Distribution of EVM transactions per block | `instruments.go` |
| `hl_evm_tx_receipt_count_mismatches_total` | Counter | Prometheus | - | base | Blocks whose transaction-array and receipt-array lengths differ. This is count-only and does not assert receipt identity or order. | `evm_prometheus.go` |
| `hl_evm_tx_receipt_last_mismatch_height` | Gauge | Prometheus | - | base | Height of the most recent transaction/receipt array-count mismatch; zero means none observed by this exporter process. | `evm_prometheus.go` |
| `hl_evm_tx_shape_total` | Counter | Prometheus | `shape` | base | Accepted user transactions by closed destination shape: create, message, or unknown. | `evm_prometheus.go` |
| `hl_evm_tx_type_total` | Counter | OTel bridge | `type` | base | Total number of EVM transactions by type | `instruments.go` |
| `hl_exporter_build_info` | Gauge | Prometheus | `commit`, `go_version`, `version` | base | Build information for the running exporter; always 1. | `prometheus_instruments.go` |
| `hl_exporter_config_info` | Gauge | Prometheus | `chain` | base | Immutable exporter configuration identity. The chain label is configured and validated; it does not attest the chain observed from the node. | `config_api_metrics.go` |
| `hl_exporter_monitor_error_drops_total` | Counter | Prometheus | `monitor` | base | Monitor error reports dropped because that monitor's independent bounded error channel was full. | `prometheus_instruments.go` |
| `hl_exporter_monitor_errors_total` | Counter | Prometheus | `monitor` | base | Total reported errors for each monitor since exporter start. | `prometheus_instruments.go` |
| `hl_exporter_monitor_exited_seconds` | Gauge | Prometheus | `monitor` | base | Unix timestamp at which the final worker most recently exited; absent while running or before first exit. | `prometheus_instruments.go` |
| `hl_exporter_monitor_last_attempt_seconds` | Gauge | Prometheus | `monitor` | base | Unix timestamp of the most recent real poll, read, scan, or request attempt; absent before first attempt. | `prometheus_instruments.go` |
| `hl_exporter_monitor_last_publication_seconds` | Gauge | Prometheus | `monitor` | base | Unix timestamp of the most recent successful metric publication; absent before first publication. | `prometheus_instruments.go` |
| `hl_exporter_monitor_last_valid_observation_seconds` | Gauge | Prometheus | `monitor` | base | Unix timestamp of the most recent complete valid observation; absent before first valid observation. | `prometheus_instruments.go` |
| `hl_exporter_monitor_panics_total` | Counter | Prometheus | `monitor` | base | Total recovered panics for each monitor since exporter start. | `prometheus_instruments.go` |
| `hl_exporter_monitor_registered` | Gauge | Prometheus | `monitor` | base | Whether this monitor is part of the configured startup set (1=yes). | `prometheus_instruments.go` |
| `hl_exporter_monitor_running` | Gauge | Prometheus | `monitor` | base | Whether at least one worker for this logical monitor is currently running (1=yes, 0=no). | `prometheus_instruments.go` |
| `hl_exporter_monitor_started_seconds` | Gauge | Prometheus | `monitor` | base | Unix timestamp of the latest transition from zero to one active workers for each monitor. | `prometheus_instruments.go` |
| `hl_exporter_monitor_workers` | Gauge | Prometheus | `monitor` | base | Current number of active outer and inner workers for this logical monitor. | `prometheus_instruments.go` |
| `hl_exporter_ready` | Gauge | Prometheus | - | base | 1 once every configured monitor has started at least one worker; source availability and freshness are reported separately. | `prometheus_instruments.go` |
| `hl_exporter_source_enabled` | Gauge | Prometheus | `source` | base | Whether collection for this fixed source is enabled by configuration (1=yes, 0=no). | `source_state.go` |
| `hl_exporter_source_errors_total` | Counter | Prometheus | `source`, `stage` | base | Source failures since exporter start, partitioned by a fixed processing stage. | `source_state.go` |
| `hl_exporter_source_ever_observed` | Gauge | Prometheus | `source` | base | Whether this exporter process has received at least one complete valid observation from the source (1=yes, 0=no). | `source_state.go` |
| `hl_exporter_source_invalid_updates_total` | Counter | Prometheus | `kind` | base | Rejected source-state API updates with a non-allowlisted source or failure stage. | `source_state.go` |
| `hl_exporter_source_last_attempt_seconds` | Gauge | Prometheus | `source` | base | Exporter wall-clock Unix timestamp of the most recent attempt to inspect this source; absent before the first attempt. | `source_state.go` |
| `hl_exporter_source_last_publication_seconds` | Gauge | Prometheus | `source` | base | Exporter wall-clock Unix timestamp of the most recent successful metric publication from this source; absent before first publication. | `source_state.go` |
| `hl_exporter_source_last_valid_age_seconds` | Gauge | Prometheus | `source` | base | Seconds since exporter receipt of the most recent complete valid observation; absent before first observation and unaffected by future-dated source timestamps. | `source_state.go` |
| `hl_exporter_source_last_valid_observation_seconds` | Gauge | Prometheus | `source` | base | Exporter wall-clock Unix timestamp at which the most recent complete valid observation was received; absent before the first valid observation. | `source_state.go` |
| `hl_exporter_source_present` | Gauge | Prometheus | `source` | base | Latest confirmed presence state for this fixed source (1=present, 0=absent, -1=unknown/not checked). | `source_state.go` |
| `hl_exporter_source_read_ok` | Gauge | Prometheus | `source` | base | Latest read outcome for this fixed source (1=success, 0=failure, -1=not attempted/not applicable). | `source_state.go` |
| `hl_exporter_source_schema_ok` | Gauge | Prometheus | `source` | base | Latest schema/decode outcome for this fixed source (1=valid, 0=invalid, -1=not attempted/not applicable). | `source_state.go` |
| `hl_exporter_source_timestamp_seconds` | Gauge | Prometheus | `source` | base | Timestamp carried by the most recent complete valid source record when available; never used to compute exporter receipt freshness. | `source_state.go` |
| `hl_go_heap_idle_bytes` | Gauge | OTel bridge | - | base | Heap memory idle in bytes | `memory.go` |
| `hl_go_heap_inuse_bytes` | Gauge | OTel bridge | - | base | Heap memory in use in bytes | `memory.go` |
| `hl_go_heap_objects` | Gauge | OTel bridge | - | base | Number of allocated heap objects | `memory.go` |
| `hl_go_num_goroutines` | Gauge | OTel bridge | - | base | Number of goroutines | `memory.go` |
| `hl_go_sys_bytes` | Gauge | OTel bridge | - | base | Total memory obtained from OS in bytes | `memory.go` |
| `hl_info_endpoint_failures_total` | Counter | Prometheus | - | info-probe | Cumulative count of failed info-endpoint probes since exporter start. | `prometheus_instruments.go` |
| `hl_info_endpoint_last_success_seconds` | Gauge | Prometheus | - | info-probe | Unix timestamp of the last successful info-endpoint probe. | `prometheus_instruments.go` |
| `hl_info_endpoint_latency_seconds` | Histogram | Prometheus | - | info-probe | Latency of the active POST :3001/info {"type":"meta"} probe. | `prometheus_instruments.go` |
| `hl_info_endpoint_up` | Gauge | Prometheus | - | info-probe | 1 if the last POST :3001/info returned HTTP 200 with a non-empty body, 0 otherwise. Absent (not 0) when --probe-info-endpoint is off. | `prometheus_instruments.go` |
| `hl_info_exchange_status_delta_seconds` | Gauge | Prometheus | - | info-probe | local_wall_clock - exchangeStatus.time reported by the node's info endpoint, in seconds. Sustained growth means the node serves stale exchange state. | `prometheus_instruments.go` |
| `hl_info_exchange_status_last_success_age_seconds` | Gauge | Prometheus | - | info-probe | Seconds since the last successful exchangeStatus subprobe; retained and advanced through failures. | `info_probe_metrics.go` |
| `hl_info_exchange_status_last_success_seconds` | Gauge | Prometheus | - | info-probe | Unix timestamp of the last successful exchangeStatus subprobe. | `info_probe_metrics.go` |
| `hl_info_exchange_status_outcomes_total` | Counter | Prometheus | `outcome` | info-probe | ExchangeStatus subprobe outcomes since exporter start from a fixed vocabulary. | `info_probe_metrics.go` |
| `hl_info_exchange_status_up` | Gauge | Prometheus | - | info-probe | Whether the most recent independent exchangeStatus subprobe returned a valid timestamp (1=yes, 0=no). | `info_probe_metrics.go` |
| `hl_info_meta_last_success_age_seconds` | Gauge | Prometheus | - | info-probe | Seconds since the last successful meta subprobe; 0 before the first success is qualified by hl_exporter_source_ever_observed. | `info_probe_metrics.go` |
| `hl_info_meta_outcomes_total` | Counter | Prometheus | `outcome` | info-probe | Meta subprobe outcomes since exporter start from a fixed vocabulary. | `info_probe_metrics.go` |
| `hl_latency_bucket_guard_seconds` | Gauge | Prometheus | `quantile`, `step` | base | Latency stats for individual bucket_guard sub-steps (e.g. begin_block, distribute_funding). quantile ∈ {p50,p90,p95,max,mean,std_dev}. | `prometheus_instruments.go` |
| `hl_latency_bucket_guard_work_fraction` | Gauge | Prometheus | `step` | base | Raw upstream work_fraction value for each bucket_guard sub-step's latest sampling window; it is not constrained to the interval 0..1. | `prometheus_instruments.go` |
| `hl_latency_consensus_seconds` | Gauge | Prometheus | `quantile`, `step` | base | Validator-only per-step latency for the consensus state machine. The step label names which input the validator was processing (HandleStateInput::Block, TxCommit, etc.). quantile ∈ {p50,p90,p95,max,mean,std_dev}. | `prometheus_instruments.go` |
| `hl_latency_consensus_work_fraction` | Gauge | Prometheus | `step` | base | Raw upstream work_fraction value for each consensus step's latest sampling window; it is not constrained to the interval 0..1. | `prometheus_instruments.go` |
| `hl_latency_l1_task_seconds` | Gauge | Prometheus | `quantile`, `step` | base | Validator-only L1 block-apply phase latencies (BeginBlock / DeliverSignedActions / EndBlock / RecoverUsers). | `prometheus_instruments.go` |
| `hl_latency_l1_task_work_fraction` | Gauge | Prometheus | `step` | base | Raw upstream work_fraction value for each L1 phase's latest sampling window; it is not constrained to the interval 0..1. | `prometheus_instruments.go` |
| `hl_mempool_dropped_items_total` | Counter | Prometheus | `kind` | base | Items in validated mempool drop batches since exporter start (kind=blocks or transactions). | `mempool_prometheus.go` |
| `hl_mempool_events_total` | Counter | Prometheus | `event_type`, `status` | base | Valid mempool records observed since exporter start. add_tx and verify_block status is one of ok, err, other; other event types use not_applicable. Unknown tags use event_type="other". | `prometheus_instruments.go` |
| `hl_mempool_oldest_uncommitted_age_seconds` | Gauge | Prometheus | - | base | Age of the oldest current uncommitted transaction at the latest complete Size stats snapshot; absent before the first valid snapshot. | `mempool_prometheus.go` |
| `hl_mempool_parser_events_total` | Counter | Prometheus | `reason` | base | Mempool records requiring bounded parser/taxonomy handling; reasons are fixed and never contain payload text. | `mempool_prometheus.go` |
| `hl_mempool_rpc_prune_events_total` | Counter | Prometheus | - | base | Valid Pruned rpc request throttle records observed since exporter start. | `mempool_prometheus.go` |
| `hl_mempool_rpc_requests_pruned_total` | Counter | Prometheus | - | base | RPC-request entries pruned since exporter start, summed from the validated third payload element. | `mempool_prometheus.go` |
| `hl_mempool_size` | Gauge | Prometheus | `component` | base | Mempool component sizes from the 'Size stats' event payload. components: committed_tx_hashes, uncommitted_txs, blocks, rpc_requests. | `prometheus_instruments.go` |
| `hl_mempool_structured_errors_total` | Counter | Prometheus | `error_kind`, `operation` | base | Structured add/verify failures by fixed operation and allowlisted top-level error kind; nested payloads are ignored. | `mempool_prometheus.go` |
| `hl_mempool_txs_bytes_total` | Counter | Prometheus | - | base | Bytes read from complete JSONL records in data/mempool_txs. | `prometheus_instruments.go` |
| `hl_mempool_txs_latest_time` | Gauge | Prometheus | - | base | Source Unix timestamp from the latest valid split-client mempool transaction record; absent before first observation. | `prometheus_instruments.go` |
| `hl_mempool_txs_operations_per_record` | Histogram | Prometheus | - | base | Distribution of individual operation count per split-client mempool transaction record. | `prometheus_instruments.go` |
| `hl_mempool_txs_operations_total` | Counter | Prometheus | `type` | base | Individual operations observed in split-client mempool transactions, by allowlisted action type. orders/cancels/modifies count array elements; scalar actions count as 1. | `prometheus_instruments.go` |
| `hl_mempool_txs_order_operations_total` | Counter | Prometheus | `side`, `tif` | base | Order-like operations observed in split-client mempool transactions, by side and time-in-force. Includes order actions, modify actions, and batchModify entries. | `prometheus_instruments.go` |
| `hl_mempool_txs_parser_events_total` | Counter | Prometheus | `reason` | base | Split-client mempool records or fields requiring bounded parser/taxonomy handling; reasons never contain input text. | `mempool_prometheus.go` |
| `hl_mempool_txs_sample_age_seconds` | Gauge | Prometheus | - | base | Wall-clock age since exporter receipt of the latest valid split-client mempool transaction record; absent before first observation and advances through quiet/error periods. | `prometheus_instruments.go` |
| `hl_mempool_txs_seen_total` | Counter | Prometheus | - | base | Uncommitted mempool transaction records observed in data/mempool_txs. | `prometheus_instruments.go` |
| `hl_mempool_txs_signed_actions_per_record` | Histogram | Prometheus | - | base | Distribution of signed_actions array length per split-client mempool transaction record. | `prometheus_instruments.go` |
| `hl_mempool_txs_signed_actions_total` | Counter | Prometheus | `type` | base | Signed actions observed in split-client mempool transaction records, by allowlisted action type. New types are bucketed as type="other". | `prometheus_instruments.go` |
| `hl_metal_apply_duration_milliseconds` | Histogram | OTel bridge | `state_type` | base | Distribution of block apply durations in milliseconds | `instruments.go` |
| `hl_metal_parse_duration` | Gauge | OTel bridge | - | base | Duration of replica transaction parsing | `instruments.go` |
| `hl_node_bugs` | Gauge | Prometheus | `source` | base | Cumulative bug! event count reported by the source process generation. | `prometheus_instruments.go` |
| `hl_node_child_stderr_artifacts` | Gauge | Prometheus | `reason`, `state` | base | Retained visor child-stderr artifacts by bounded read state and bounded reason from the last complete directory scan; truncated and unreadable remain explicit evidence limits. | `host_prometheus.go` |
| `hl_node_child_stderr_last_artifact_timestamp_seconds` | Gauge | Prometheus | `reason`, `state` | base | Newest filesystem mtime among retained child-stderr artifacts for a bounded state and reason; absent when no matching artifact is retained. | `host_prometheus.go` |
| `hl_node_child_stderr_last_complete_timestamp_seconds` | Gauge | Prometheus | - | base | Exporter wall-clock Unix timestamp of the last complete child-stderr scan publication. | `host_prometheus.go` |
| `hl_node_child_stderr_scan_up` | Gauge | Prometheus | - | base | Whether the latest bounded visor_child_stderr directory scan completed successfully (1=yes, 0=no). | `host_prometheus.go` |
| `hl_node_crit_location` | Gauge | Prometheus | `file`, `line` | base | Per-source-location count from the matched hl-visor rich critical-message generation. Value is cumulative since the visor process started. | `prometheus_instruments.go` |
| `hl_node_crit_location_ignored` | Gauge | Prometheus | `file`, `line` | base | 1 if hl-node marks this crit location is_ignored (operator-suppressed via crit_msg_ignore.json), else 0. | `prometheus_instruments.go` |
| `hl_node_crit_location_last_seen_seconds` | Gauge | Prometheus | `file`, `line` | base | Unix timestamp at which the named source location most recently emitted a crit. | `prometheus_instruments.go` |
| `hl_node_crit_locations` | Gauge | Prometheus | `source` | base | Distinct source-code call sites (file:line) that have fired a bug or crit at least once for this process lifetime. | `prometheus_instruments.go` |
| `hl_node_critical_message_generation_match` | Gauge | Prometheus | `source` | base | Whether the latest valid rich projection exactly matches the committed daily generation by source, base time, counters, and location count. | `source_snapshot_prometheus.go` |
| `hl_node_critical_message_projection_available` | Gauge | Prometheus | `projection`, `source` | base | Whether the fixed critical-message projection is currently present, fresh, readable, valid, and generation-compatible (1=yes, 0=no). | `source_snapshot_prometheus.go` |
| `hl_node_critical_message_projection_parse_ok` | Gauge | Prometheus | `projection`, `source` | base | Latest parse state for the fixed critical-message projection (1=valid, 0=invalid, -1=not parsed because absent or unreadable). | `source_snapshot_prometheus.go` |
| `hl_node_critical_message_sample_timestamp_seconds` | Gauge | Prometheus | `source` | base | Source timestamp carried by the latest complete daily critical-message record. | `source_snapshot_prometheus.go` |
| `hl_node_critical_messages_base_time_seconds` | Gauge | Prometheus | `source` | base | Unix timestamp at which the current critical-message counters started accumulating (source process start). | `prometheus_instruments.go` |
| `hl_node_crits` | Gauge | Prometheus | `source` | base | Cumulative crit! event count reported by the source process generation. | `prometheus_instruments.go` |
| `hl_node_disk_allocated_bytes` | Gauge | Prometheus | - | base | Unique filesystem blocks allocated to entries in the last complete NODE_HOME walk, including directory metadata and deduplicated by device and inode; unlike apparent bytes, sparse extents and hardlinks are counted physically. | `host_prometheus.go` |
| `hl_node_disk_errors_total` | Counter | Prometheus | `stage` | base | Disk monitor failures since exporter start, partitioned by fixed stage. | `host_prometheus.go` |
| `hl_node_disk_free_bytes` | Gauge | Prometheus | - | base | Bytes available on the filesystem holding NODE_HOME (statfs Bavail*Bsize). | `prometheus_instruments.go` |
| `hl_node_disk_last_complete_age_seconds` | Gauge | Prometheus | - | base | Seconds since the last complete NODE_HOME walk publication at the latest monitor tick; zero before the first complete walk. | `host_prometheus.go` |
| `hl_node_disk_last_complete_timestamp_seconds` | Gauge | Prometheus | - | base | Exporter wall-clock Unix timestamp of the last complete NODE_HOME walk publication. | `host_prometheus.go` |
| `hl_node_disk_path_state` | Gauge | Prometheus | `path`, `state` | base | Filesystem-only one-hot state for a fixed NODE_HOME path from the last complete walk; state is present_nonempty, present_empty, or absent and does not attest node health. | `host_prometheus.go` |
| `hl_node_disk_statfs_up` | Gauge | Prometheus | - | base | Whether the latest independent filesystem-capacity stat for NODE_HOME succeeded (1=yes, 0=no). | `host_prometheus.go` |
| `hl_node_disk_subdir_allocated_bytes` | Gauge | Prometheus | `path` | base | Unique filesystem blocks allocated within each fixed NODE_HOME path in the last complete walk, deduplicated independently by device and inode within each path scope. | `host_prometheus.go` |
| `hl_node_disk_subdir_bytes` | Gauge | Prometheus | `subdir` | base | Bytes consumed by major NODE_HOME subdirectories (per a hard-coded allowlist that targets known hot paths). | `prometheus_instruments.go` |
| `hl_node_disk_total_bytes` | Gauge | Prometheus | - | base | Total bytes on the filesystem holding NODE_HOME (statfs Blocks*Bsize). | `prometheus_instruments.go` |
| `hl_node_disk_used_bytes` | Gauge | Prometheus | - | base | Total bytes consumed by the NODE_HOME tree (sum of regular file sizes). | `prometheus_instruments.go` |
| `hl_node_disk_walk_up` | Gauge | Prometheus | - | base | Whether the latest NODE_HOME walk and all required entry metadata reads completed successfully (1=yes, 0=no); statfs is independent. | `host_prometheus.go` |
| `hl_node_jailing_dry_run` | Gauge | Prometheus | - | jailing | 1 if heartbeat_jailing_config.json has dry_run=true (jail decisions are logged, not enforced), 0 if enforcement is live. | `prometheus_instruments.go` |
| `hl_node_jailing_threshold_seconds` | Gauge | Prometheus | - | jailing | latency_ema_jail_threshold from heartbeat_jailing_config.json: the heartbeat-ack EMA above which this node votes to jail a peer. Compare against hl_consensus_validator_latency_ema_seconds for per-peer headroom. | `prometheus_instruments.go` |
| `hl_node_log_bytes` | Gauge | Prometheus | `level`, `stream` | base | Size in bytes of the day's log file. | `prometheus_instruments.go` |
| `hl_node_log_lines` | Gauge | Prometheus | `level`, `stream` | base | Lines in the day's log file. Modeled as a gauge so the per-day file rotation cleanly resets the series at midnight. | `prometheus_instruments.go` |
| `hl_node_observed_run_last_activity_seconds` | Gauge | Prometheus | - | base | Unix mtime of the most recently modified retained replica_cmds run directory; this is filesystem activity, not run start or duration. | `session_prometheus.go` |
| `hl_node_observed_run_start_seconds` | Gauge | Prometheus | - | base | Unix timestamp of the newest retained hl-node run, parsed from the immutable data/replica_cmds/<run_timestamp>/ directory name. | `prometheus_instruments.go` |
| `hl_node_observed_runs` | Gauge | Prometheus | - | base | Current number of valid retained run-timestamp directories under data/replica_cmds/; pruning can decrease this gauge and it is not a lifetime counter. | `prometheus_instruments.go` |
| `hl_node_operator_config_age_seconds` | Gauge | Prometheus | `file` | base | Age (now - mtime) of operator-edited config files under file_mod_time_tracker/. | `prometheus_instruments.go` |
| `hl_node_operator_config_failed_load` | Gauge | Prometheus | `file` | base | Count of FAILED_LOAD sidecars for each fixed operator-config file; arbitrary filenames collapse to file=unknown. | `operator_config_metrics.go` |
| `hl_node_operator_config_failed_loads` | Gauge | Prometheus | - | base | Count of *_FAILED_LOAD sidecar files in file_mod_time_tracker/. Each one is a config the operator pushed but hl-node rejected — silent misconfiguration. | `prometheus_instruments.go` |
| `hl_node_operator_config_present` | Gauge | Prometheus | `file` | base | Presence of each fixed operator-config file (1=present, 0=absent, -1=stat failed). File labels are allowlisted. | `operator_config_metrics.go` |
| `hl_node_persisted_abci_height` | Gauge | Prometheus | `source_class` | base | Core/ABCI height read from the exact persisted checkpoint-height file for a fixed source class; not EVM block height or current execution head. | `validator_consensus_prometheus.go` |
| `hl_node_persisted_abci_height_gap` | Gauge | Prometheus | `comparison` | base | Difference between the fast and slow persisted core/ABCI checkpoint-height files when both are present in the same poll. | `validator_consensus_prometheus.go` |
| `hl_node_persisted_freeze_abci_height` | Gauge | Prometheus | `source` | base | Core/ABCI height read from the persisted freeze_abci_height file; persistence across process restarts does not make it a current scheduled freeze. | `validator_consensus_prometheus.go` |
| `hl_node_persisted_state_file_available` | Gauge | Prometheus | `file` | base | Whether a fixed persisted node-state file was present, readable, and held one nonnegative integer in the latest poll. | `validator_consensus_prometheus.go` |
| `hl_node_process_cpu_seconds_total` | Gauge | Prometheus | `process` | base | Cumulative CPU time consumed by the process (user+kernel), in seconds. | `prometheus_instruments.go` |
| `hl_node_process_eligible_matches` | Gauge | Prometheus | `process` | base | Processes whose comm and readable executable or argv0 identity matched the fixed process name in the latest complete procfs scan. | `host_prometheus.go` |
| `hl_node_process_io_total` | Counter | Prometheus | `operation`, `process` | base | Exporter-lifetime positive procfs IO deltas for the selected process; operation is read_bytes, write_bytes, read_syscalls, or write_syscalls and new process epochs establish a baseline. | `host_prometheus.go` |
| `hl_node_process_max_fds` | Gauge | Prometheus | `process` | base | Soft open-file descriptor limit for the selected process from procfs; zero when unavailable or unlimited. | `host_prometheus.go` |
| `hl_node_process_open_fds` | Gauge | Prometheus | `process` | base | Number of open file descriptors held by the process (count of /proc/PID/fd entries). | `prometheus_instruments.go` |
| `hl_node_process_open_fds_ratio` | Gauge | Prometheus | `process` | base | Selected process open file descriptors divided by its finite soft limit; zero when the limit is unavailable or unlimited. | `host_prometheus.go` |
| `hl_node_process_rss_bytes` | Gauge | Prometheus | `process` | base | Resident set size of the process, in bytes (/proc/PID/status VmRSS). | `prometheus_instruments.go` |
| `hl_node_process_start_time_seconds` | Gauge | Prometheus | `process` | base | Unix timestamp at which the process started (derived from /proc btime + stat starttime). | `prometheus_instruments.go` |
| `hl_node_process_threads` | Gauge | Prometheus | `process` | base | Number of OS threads in the process (/proc/PID/status Threads). | `prometheus_instruments.go` |
| `hl_node_process_up` | Gauge | Prometheus | `process` | base | 1 if the named hl-node process was found in /proc on the latest tick, 0 otherwise. | `prometheus_instruments.go` |
| `hl_node_process_virt_bytes` | Gauge | Prometheus | `process` | base | Virtual memory size of the process, in bytes (/proc/PID/status VmSize). | `prometheus_instruments.go` |
| `hl_node_public_ip_age_seconds` | Gauge | Prometheus | - | base | Wall-clock seconds since the observed mtime of last_known_public_ip.json; no universal rewrite cadence is implied. | `prometheus_instruments.go` |
| `hl_node_public_ip_changes_total` | Counter | Prometheus | - | base | Cumulative count of public-IP changes observed since the exporter started. | `prometheus_instruments.go` |
| `hl_node_public_ip_info` | Gauge | Prometheus | `ip` | base | 1 for the IP currently reported as the node's public address. | `prometheus_instruments.go` |
| `hl_node_rate_limited_last_nonempty_update_timestamp_seconds` | Gauge | Prometheus | `stream` | base | Maximum non-future mtime ever observed for a non-empty file anywhere in the fixed stream source tree; zero when none has been observed. | `network_prometheus.go` |
| `hl_node_rate_limited_last_success_timestamp_seconds` | Gauge | Prometheus | `stream` | base | Exporter wall-clock Unix timestamp of the latest complete scan for the fixed stream. | `network_prometheus.go` |
| `hl_node_rate_limited_nonempty_files_latest_date` | Gauge | Prometheus | `stream` | base | Non-empty regular files retained in the lexicographically newest source date directory; file evidence is not active rate limiting or an offender count. | `network_prometheus.go` |
| `hl_node_rate_limited_read_errors_total` | Counter | Prometheus | `stage`, `stream` | base | Rate-limit file-source failures since exporter start, partitioned by fixed stream and stage. | `network_prometheus.go` |
| `hl_node_rate_limited_recent_files` | Gauge | Prometheus | `stream` | base | Non-empty regular files modified within 120 seconds at the last successful scan; recent file evidence is not a currently blocked peer count. | `network_prometheus.go` |
| `hl_node_rate_limited_source_up` | Gauge | Prometheus | `stream` | base | Whether discovery and all required directory and file metadata reads completed in the latest attempt. | `network_prometheus.go` |
| `hl_node_replay_events` | Gauge | Prometheus | - | base | Current number of valid retained replay marker directories under data/node_logs/replay/; pruning can decrease this gauge and it is not a lifetime counter. | `prometheus_instruments.go` |
| `hl_node_replay_last_activity_seconds` | Gauge | Prometheus | - | base | Newest filesystem mtime among retained replay marker directories; this is activity and does not prove replay end or duration. | `session_prometheus.go` |
| `hl_node_replay_last_height` | Gauge | Prometheus | - | base | Block height at which the most recent replay event happened (parsed from the subdir name). | `prometheus_instruments.go` |
| `hl_node_replay_last_seconds` | Gauge | Prometheus | - | base | Immutable start timestamp of the newest retained replay marker, parsed from its <height>_<ISO timestamp> directory name. | `prometheus_instruments.go` |
| `hl_node_shell_exec_pending` | Gauge | Prometheus | - | base | Compatibility count of regular files retained under $NODE_HOME/tmp/shell_rs_out/, including expected empty receipts; use material-class metrics for stale payload evidence. | `prometheus_instruments.go` |
| `hl_node_snapshot_height_lag_available` | Gauge | Prometheus | - | base | Whether snapshot height lag could be joined to a current visor height on the latest complete status scan (1=yes, 0=no); qualify freshness with source health. | `session_prometheus.go` |
| `hl_node_snapshot_height_lag_blocks` | Gauge | Prometheus | - | base | Latest current visor height minus latest completed periodic ABCI snapshot height; absent until both heights are available and nonnegative. | `session_prometheus.go` |
| `hl_node_snapshot_last_age_seconds` | Gauge | Prometheus | - | base | Seconds since the most recent successful periodic snapshot (now - mtime of newest status sentinel). | `prometheus_instruments.go` |
| `hl_node_snapshot_last_height` | Gauge | Prometheus | - | base | Highest block height at which a periodic ABCI snapshot completed successfully. | `prometheus_instruments.go` |
| `hl_node_snapshot_sentinels_retained` | Gauge | Prometheus | - | base | Number of snapshot-completion sentinels in the retained scan window of at most the two newest valid date directories; this is not cadence or capacity headroom. | `prometheus_instruments.go` |
| `hl_node_stream_age_seconds` | Gauge | Prometheus | `stream` | base | Wall-clock age of the latest valid committed record observed in each fixed opt-in stream; no writer cadence, consumer, or stall is inferred. | `prometheus_instruments.go` |
| `hl_node_subsystem_latency_lifetime_mean_seconds` | Gauge | Prometheus | `subsystem` | base | Source-reported total_mean latency for the subsystem's current process generation. | `prometheus_instruments.go` |
| `hl_node_subsystem_latency_max_seconds` | Gauge | Prometheus | `subsystem` | base | Max per-sample latency for an internal hl-node subsystem. | `prometheus_instruments.go` |
| `hl_node_subsystem_latency_mean_seconds` | Gauge | Prometheus | `subsystem` | base | Mean per-sample latency for an internal hl-node subsystem. | `prometheus_instruments.go` |
| `hl_node_subsystem_latency_median_seconds` | Gauge | Prometheus | `subsystem` | base | Median per-sample latency for an internal hl-node subsystem. | `prometheus_instruments.go` |
| `hl_node_subsystem_latency_p90_seconds` | Gauge | Prometheus | `subsystem` | base | p90 per-sample latency for an internal hl-node subsystem. | `prometheus_instruments.go` |
| `hl_node_subsystem_latency_p95_seconds` | Gauge | Prometheus | `subsystem` | base | p95 per-sample latency for an internal hl-node subsystem. | `prometheus_instruments.go` |
| `hl_node_subsystem_latency_stddev_seconds` | Gauge | Prometheus | `subsystem` | base | Std-dev of per-sample latency for an internal hl-node subsystem. | `prometheus_instruments.go` |
| `hl_node_subsystem_samples_total` | Gauge | Prometheus | `subsystem` | base | Total number of samples behind the latest aggregated row (cumulative). | `prometheus_instruments.go` |
| `hl_node_subsystem_work_fraction` | Gauge | Prometheus | `subsystem` | base | Raw upstream work_fraction value for the subsystem's latest sampling window; it is not constrained to the interval 0..1. | `prometheus_instruments.go` |
| `hl_node_tmp_bytes` | Gauge | Prometheus | - | base | Apparent bytes across all files in the last complete $NODE_HOME/tmp scan; no cause is inferred from growth. | `prometheus_instruments.go` |
| `hl_node_tmp_bytes_by_class` | Gauge | Prometheus | `class` | base | Apparent file bytes in the last complete tmp scan by fixed receipt or material class. | `host_prometheus.go` |
| `hl_node_tmp_files` | Gauge | Prometheus | `class` | base | Files in the last complete tmp scan by fixed class: receipt is an empty regular file under shell_rs_out; material is every other file type or location. | `host_prometheus.go` |
| `hl_node_tmp_last_complete_timestamp_seconds` | Gauge | Prometheus | - | base | Exporter wall-clock Unix timestamp of the last complete tmp scan publication. | `host_prometheus.go` |
| `hl_node_tmp_material_stale_bytes` | Gauge | Prometheus | - | base | Apparent bytes in non-receipt tmp files older than 24 hours in the last complete scan. | `host_prometheus.go` |
| `hl_node_tmp_material_stale_files` | Gauge | Prometheus | - | base | Non-receipt tmp files with retained bytes older than 24 hours in the last complete scan. | `host_prometheus.go` |
| `hl_node_tmp_scan_up` | Gauge | Prometheus | - | base | Whether the latest tmp walk and all required entry metadata reads completed successfully (1=yes, 0=no). | `host_prometheus.go` |
| `hl_node_tmp_stale_files` | Gauge | Prometheus | - | base | Files older than 24 hours in the last complete $NODE_HOME/tmp scan, including empty shell_rs_out receipts and material files. | `prometheus_instruments.go` |
| `hl_node_visor_height_above_persisted_freeze` | Gauge | Prometheus | `comparison` | base | Nonnegative latest visor height minus the persisted freeze_abci_height file value when both are available; not proof of a current scheduled freeze. | `validator_consensus_prometheus.go` |
| `hl_p2p_child_connections` | Gauge | Prometheus | - | base | Sum of connection_count across explicit children in the newest complete fresh snapshot; this is not a peer count. | `network_prometheus.go` |
| `hl_p2p_child_identity_overflow` | Gauge | Prometheus | - | base | Current explicit children beyond the optional 16-identity publication cap. | `network_prometheus.go` |
| `hl_p2p_child_peer_connections` | Gauge | Prometheus | `ip` | base | Source connection_count for an optionally published current explicit child. | `network_prometheus.go` |
| `hl_p2p_child_peer_info` | Gauge | Prometheus | `ip`, `verified` | base | Optional bounded identity for a current explicit child from the latest fresh snapshot. | `network_prometheus.go` |
| `hl_p2p_child_peer_tenure_seconds` | Gauge | Prometheus | `ip` | base | Exporter-process-observed seconds of uninterrupted membership for an optionally published current explicit child. | `network_prometheus.go` |
| `hl_p2p_child_peers` | Gauge | Prometheus | `verified` | base | Explicit children in the newest complete fresh child_peers status snapshot, partitioned by verification status. | `network_prometheus.go` |
| `hl_p2p_child_snapshot_age_seconds` | Gauge | Prometheus | - | base | Seconds since exporter receipt of the latest complete accepted child snapshot; use snapshot_fresh to distinguish never seen. | `network_prometheus.go` |
| `hl_p2p_child_snapshot_errors_total` | Counter | Prometheus | `reason` | base | Rejected child-source observations since exporter start, partitioned by a fixed reason. | `network_prometheus.go` |
| `hl_p2p_child_snapshot_fresh` | Gauge | Prometheus | - | base | Whether the latest complete child snapshot was received no more than 90 seconds ago (1=yes, 0=no). | `network_prometheus.go` |
| `hl_p2p_child_snapshot_timestamp_seconds` | Gauge | Prometheus | - | base | Source timestamp of the latest complete accepted child snapshot; zero before the first accepted snapshot. | `network_prometheus.go` |
| `hl_p2p_child_source_up` | Gauge | Prometheus | - | base | Whether latest child-source discovery/read/scan succeeded, independent of snapshot freshness. | `network_prometheus.go` |
| `hl_p2p_dominant_inbound_challenger_ratio` | Gauge | Prometheus | - | base | Largest other EWMA divided by the selected candidate EWMA; zero when no challenger. | `network_prometheus.go` |
| `hl_p2p_dominant_inbound_endpoint_info` | Gauge | Prometheus | `ip` | base | Identity of the single selected dominant inbound traffic endpoint candidate; this is a traffic heuristic, not parentage, reachability, quality, or causality. | `network_prometheus.go` |
| `hl_p2p_dominant_inbound_ewma_value` | Gauge | Prometheus | - | base | EWMA of the unresolved-unit inbound traffic field for the selected dominant candidate. | `network_prometheus.go` |
| `hl_p2p_dominant_inbound_fresh` | Gauge | Prometheus | - | base | Whether the dominant-candidate state derives from a complete traffic snapshot received no more than 90 seconds ago. | `network_prometheus.go` |
| `hl_p2p_dominant_inbound_latest_value` | Gauge | Prometheus | - | base | Raw unresolved-unit inbound traffic value for the selected candidate in the newest complete snapshot; zero when absent. | `network_prometheus.go` |
| `hl_p2p_dominant_inbound_share_ratio` | Gauge | Prometheus | - | base | Selected candidate EWMA divided by the sum of positive inbound EWMAs; zero when undefined. | `network_prometheus.go` |
| `hl_p2p_dominant_inbound_switches_total` | Counter | Prometheus | - | base | Candidate A-to-B changes during continuously fresh epochs since exporter start; clear and recovery are not switches. | `network_prometheus.go` |
| `hl_p2p_dominant_inbound_tenure_seconds` | Gauge | Prometheus | - | base | Wall-clock seconds since the candidate was selected in the current uninterrupted fresh epoch. | `network_prometheus.go` |
| `hl_p2p_dominant_inbound_tie_count` | Gauge | Prometheus | - | base | Endpoints exactly tied for maximum inbound EWMA before lexicographic tie-breaking; zero without a candidate. | `network_prometheus.go` |
| `hl_p2p_gossip_events_total` | Counter | Prometheus | `event_type` | base | Newline-committed gossip-connection events observed after exporter start, by fixed event type; history found by a successful initial scan is skipped, a file first found after an initial discovery failure is read from its start, and the counter resets on exporter restart. | `prometheus_instruments.go` |
| `hl_p2p_gossip_last_read_success_timestamp_seconds` | Gauge | Prometheus | - | base | Exporter wall-clock Unix timestamp of the latest successful gossip-connection poll, independent of event arrival. | `network_prometheus.go` |
| `hl_p2p_gossip_parse_errors_total` | Counter | Prometheus | `reason` | base | Rejected complete gossip-connection records since exporter start, partitioned by a fixed reason. | `network_prometheus.go` |
| `hl_p2p_gossip_source_up` | Gauge | Prometheus | - | base | Whether the latest gossip-connection discovery/read/scan attempt succeeded; quiet input remains up. | `network_prometheus.go` |
| `hl_p2p_gossip_unknown_events_total` | Counter | Prometheus | - | base | Structurally valid gossip-connection events whose exact tag is outside the fixed vocabulary since exporter start. | `network_prometheus.go` |
| `hl_p2p_lz4_global_window_bytes` | Gauge | Prometheus | - | base | Source-provided global byte field from the latest complete paired LZ4 window; compression-layer semantics are unresolved. | `network_prometheus.go` |
| `hl_p2p_lz4_global_window_packets` | Gauge | Prometheus | - | base | Source-provided global packet field from the latest complete paired LZ4 window. | `network_prometheus.go` |
| `hl_p2p_lz4_global_window_ratio` | Gauge | Prometheus | - | base | Source-provided global ratio from the latest complete paired LZ4 window. | `network_prometheus.go` |
| `hl_p2p_lz4_sample_age_seconds` | Gauge | Prometheus | - | base | Seconds since exporter receipt of the latest complete paired LZ4 window; future source timestamps cannot make this negative. | `network_prometheus.go` |
| `hl_p2p_lz4_sample_timestamp_seconds` | Gauge | Prometheus | - | base | Source timestamp of the latest complete paired LZ4 window. | `network_prometheus.go` |
| `hl_p2p_lz4_source_up` | Gauge | Prometheus | - | base | Whether the latest LZ4 source discovery/read and complete pair selection succeeded (1=yes, 0=no). | `network_prometheus.go` |
| `hl_p2p_lz4_window_bytes` | Gauge | Prometheus | `direction`, `ip` | base | Latest paired-window upstream byte field summed across ports per endpoint; whether the field is compressed or uncompressed bytes is unresolved. | `network_prometheus.go` |
| `hl_p2p_lz4_window_bytes_by_service_port` | Gauge | Prometheus | `direction`, `service_port` | base | Latest paired-window upstream byte field summed by bounded source key port and direction; compression-layer semantics are unresolved. | `network_prometheus.go` |
| `hl_p2p_lz4_window_duration_seconds` | Gauge | Prometheus | - | base | Difference in seconds between consecutive committed paired-window source timestamps; zero before a prior window exists. | `network_prometheus.go` |
| `hl_p2p_lz4_window_packets` | Gauge | Prometheus | `direction`, `ip` | base | Latest paired-window upstream packet field summed across ports per endpoint. | `network_prometheus.go` |
| `hl_p2p_lz4_window_packets_by_service_port` | Gauge | Prometheus | `direction`, `service_port` | base | Latest paired-window upstream packet field summed by bounded source key port and direction. | `network_prometheus.go` |
| `hl_p2p_lz4_window_weighted_ratio` | Gauge | Prometheus | `direction`, `ip` | base | Byte-field-weighted mean of upstream-reported per-port ratios in the latest paired window; not a derived aggregate compression ratio. | `network_prometheus.go` |
| `hl_p2p_tcp_socket_connections` | Gauge | Prometheus | `service_port`, `service_side`, `state` | base | Kernel socket rows in the last fully successful combined TCP4/TCP6 snapshot, associated once with a tracked service port; service_side is the literal socket side carrying that port. | `network_prometheus.go` |
| `hl_p2p_tcp_socket_errors_total` | Counter | Prometheus | `source`, `stage` | base | Kernel TCP source failures since exporter start, partitioned by fixed source and stage. | `network_prometheus.go` |
| `hl_p2p_tcp_socket_last_success_timestamp_seconds` | Gauge | Prometheus | - | base | Exporter wall-clock Unix timestamp of the last fully successful combined TCP4/TCP6 snapshot commit. | `network_prometheus.go` |
| `hl_p2p_tcp_socket_source_up` | Gauge | Prometheus | `source` | base | Whether the latest attempt completely opened, scanned, and parsed the fixed kernel TCP source (1=yes, 0=no). | `network_prometheus.go` |
| `hl_p2p_tcp_traffic_by_service_port` | Gauge | Prometheus | `direction`, `service_port` | base | Latest upstream point-rate field summed by bounded source key port and direction; the formal unit and socket-side ownership are unresolved. | `network_prometheus.go` |
| `hl_p2p_tcp_traffic_errors_total` | Counter | Prometheus | `stage` | base | TCP traffic snapshot failures since exporter start, partitioned by a fixed stage. | `network_prometheus.go` |
| `hl_p2p_tcp_traffic_sample_timestamp_seconds` | Gauge | Prometheus | - | base | Timestamp carried by the most recently committed TCP traffic snapshot. | `network_prometheus.go` |
| `hl_p2p_tcp_traffic_source_up` | Gauge | Prometheus | - | base | Whether latest-file discovery/read and one complete newline-committed all-rows-valid traffic snapshot succeeded (1=yes, 0=no). | `network_prometheus.go` |
| `hl_p2p_traffic_endpoints_added_total` | Counter | Prometheus | - | base | Qualified traffic-endpoint additions since exporter start. | `network_prometheus.go` |
| `hl_p2p_traffic_endpoints_current` | Gauge | Prometheus | - | base | Unique canonical IPs with any positive value in the latest complete traffic snapshot; this is observation, not connectivity. | `network_prometheus.go` |
| `hl_p2p_traffic_endpoints_evicted_total` | Counter | Prometheus | `reason` | base | Qualified process-local traffic-endpoint evictions since exporter start, partitioned by fixed reason. | `network_prometheus.go` |
| `hl_p2p_traffic_endpoints_seen` | Gauge | Prometheus | `window` | base | Qualified process-local traffic endpoints refreshed within the fixed observation window. | `network_prometheus.go` |
| `hl_replica_action_bundles_total` | Counter | Prometheus | - | base | Validated signed-action bundles processed from newline-committed replica_cmds block records since exporter start. | `replica_prometheus.go` |
| `hl_replica_execution_outcomes_total` | Counter | Prometheus | `outcome` | base | Nested replica execution outcomes since exporter start using a closed fixture-backed vocabulary plus other. | `replica_prometheus.go` |
| `hl_replica_last_processed_height` | Gauge | Prometheus | - | base | Top-level chain height in the latest completely validated and published replica_cmds block record. | `replica_prometheus.go` |
| `hl_replica_multisig_inner_actions_total` | Counter | Prometheus | `category` | base | Validated outer multiSig actions since exporter start, classified by the inner action's closed operational category; missing or unknown inner actions are other. | `replica_prometheus.go` |
| `hl_replica_operations_total` | Counter | Prometheus | `action_type`, `category` | base | Individual operations inside validated replica signed actions since exporter start; an order/cancel/batch-modify array contributes its item count. | `replica_prometheus.go` |
| `hl_replica_orders_total` | Counter | Prometheus | - | base | Individual order operations inside validated order and twapOrder actions since exporter start; this is not a signed-action count. | `replica_prometheus.go` |
| `hl_replica_parser_events_total` | Counter | Prometheus | `reason`, `stage` | base | Replica records requiring bounded parser or schema-drift handling; stage and reason are fixed and never contain payload text. | `replica_prometheus.go` |
| `hl_replica_response_action_status_total` | Counter | Prometheus | `status` | base | Top-level replica action-response statuses since exporter start using the closed ok, err, and other vocabulary. | `replica_prometheus.go` |
| `hl_replica_response_count_relation_total` | Counter | Prometheus | `result` | base | Validated replica block records since exporter start by response-record count relative to signed-action count; no positional association is assumed. | `replica_prometheus.go` |
| `hl_replica_response_coverage_total` | Counter | Prometheus | `result` | base | Validated replica block records since exporter start by response payload coverage: available, unavailable, or malformed. | `replica_prometheus.go` |
| `hl_replica_response_records_total` | Counter | Prometheus | `result` | base | Replica response records observed since exporter start, classified as valid or malformed without retaining hashes, accounts, or free text. | `replica_prometheus.go` |
| `hl_replica_signed_actions_total` | Counter | Prometheus | `action_type` | base | Validated signed actions processed from replica_cmds since exporter start, by closed action type; unknown types are other. | `replica_prometheus.go` |
| `hl_rocksdb_block_cache_usage_bytes` | Gauge | Prometheus | `db` | base | Current block-cache usage in bytes, as reported by the RocksDB LOG's stats block. | `prometheus_instruments.go` |
| `hl_rocksdb_source_present` | Gauge | Prometheus | `db` | base | Whether the fixed RocksDB directory and LOG file are present in the latest poll (1=yes, 0=no). | `source_snapshot_prometheus.go` |
| `hl_rocksdb_sst_files` | Gauge | Prometheus | `db` | base | Count of .sst files for the named RocksDB (LSM tree size). Sustained growth without compaction = stuck. | `prometheus_instruments.go` |
| `hl_rocksdb_stats_last_valid_age_seconds` | Gauge | Prometheus | `db` | base | Seconds since the exporter most recently published a complete RocksDB snapshot; absent before first success. | `source_snapshot_prometheus.go` |
| `hl_rocksdb_stats_last_valid_observation_timestamp_seconds` | Gauge | Prometheus | `db` | base | Exporter wall-clock timestamp of the most recent complete RocksDB snapshot publication. | `source_snapshot_prometheus.go` |
| `hl_rocksdb_stats_parse_ok` | Gauge | Prometheus | `db` | base | Latest complete-stats parse state for the fixed RocksDB (1=valid, 0=invalid, -1=not parsed because absent or unreadable). | `source_snapshot_prometheus.go` |
| `hl_rocksdb_write_stalls_total` | Gauge | Prometheus | `db`, `reason` | base | Cumulative write-stall events from the RocksDB LOG file, by reason. Modeled as a gauge because the value resets on hl-node restart. | `prometheus_instruments.go` |
| `hl_software_up_to_date` | Gauge | OTel bridge | - | base | Software up to date status | `instruments.go` |
| `hl_software_version` | Gauge | OTel bridge | `commit`, `date` | base | Software version information | `instruments.go` |
| `hl_tcp_lz4_latency_seconds` | Gauge | Prometheus | `direction`, `port`, `quantile` | base | Per-direction-per-port lz4 latency stats. quantile ∈ {p50,p90,p95,max,mean,std_dev}. | `prometheus_instruments.go` |
| `hl_tcp_lz4_work_fraction` | Gauge | Prometheus | `direction`, `port` | base | Raw upstream work_fraction value for each lz4 direction/port's latest sampling window; it is not constrained to the interval 0..1. | `prometheus_instruments.go` |
| `hl_tokio_sample_age_seconds` | Gauge | Prometheus | - | base | Age of the newest tokio task sample. When this passes ~15m the hl_tokio_task_* gauges are withdrawn (stale source) until the feed resumes. | `prometheus_instruments.go` |
| `hl_tokio_task_dropped_total` | Gauge | Prometheus | `task` | base | Cumulative dropped (panicked or cancelled) task count. | `prometheus_instruments.go` |
| `hl_tokio_task_fast_polls_total` | Gauge | Prometheus | `task` | base | Cumulative fast polls since source start. | `prometheus_instruments.go` |
| `hl_tokio_task_idle_seconds_total` | Gauge | Prometheus | `task` | base | Cumulative idle time (task awaiting work) for the named Tokio task. | `prometheus_instruments.go` |
| `hl_tokio_task_long_delays_total` | Gauge | Prometheus | `task` | base | Cumulative long scheduling delays (task waited too long to be polled after becoming ready). | `prometheus_instruments.go` |
| `hl_tokio_task_poll_seconds_total` | Gauge | Prometheus | `task` | base | Cumulative poll time for the named Tokio task since the source process started. | `prometheus_instruments.go` |
| `hl_tokio_task_polls_total` | Gauge | Prometheus | `task` | base | Cumulative poll count for the named Tokio task. | `prometheus_instruments.go` |
| `hl_tokio_task_scheduled_seconds_total` | Gauge | Prometheus | `task` | base | Cumulative time the task spent waiting scheduled-but-not-polled since source start. Rising faster than poll time = runtime starvation. | `prometheus_instruments.go` |
| `hl_tokio_task_scheduled_total` | Gauge | Prometheus | `task` | base | Cumulative times the task was scheduled (woken) since the source process started. | `prometheus_instruments.go` |
| `hl_tokio_task_short_delays_total` | Gauge | Prometheus | `task` | base | Cumulative short scheduling delays since source start (complements long_delays). | `prometheus_instruments.go` |
| `hl_tokio_task_slow_polls_total` | Gauge | Prometheus | `task` | base | Cumulative slow polls (poll exceeded the runtime's slow-poll threshold). | `prometheus_instruments.go` |
| `hl_validator_api_cache_age_seconds` | Gauge | Prometheus | - | base | Wall-clock age in seconds of the last successful validatorSummaries network refresh; absent until the first successful refresh. | `validator_api_metrics.go` |
| `hl_validator_api_cache_stale` | Gauge | Prometheus | - | base | Whether the most recent validatorSummaries result was a stale cache fallback after a failed refresh (1=yes, 0=no). | `validator_api_metrics.go` |
| `hl_validator_api_last_success_seconds` | Gauge | Prometheus | - | base | Unix timestamp of the most recent complete valid validatorSummaries refresh; absent until the first successful refresh and never advanced by cache fallback. | `validator_api_metrics.go` |
| `hl_validator_api_outcomes_total` | Counter | Prometheus | `outcome` | base | Validator-summary resolver outcomes since exporter start from a fixed vocabulary. | `validator_api_metrics.go` |
| `hl_validator_api_unknown_periods_total` | Counter | Prometheus | - | base | Validator-summary stat rows dropped because period was not one of day, week, or month. | `validator_api_metrics.go` |
| `hl_validator_api_up` | Gauge | Prometheus | - | base | Whether the most recent validatorSummaries refresh produced a complete valid snapshot (1=yes, 0=no). Fresh-cache reads do not change this state. | `validator_api_metrics.go` |
| `hl_validator_connection_events_total` | Counter | Prometheus | `event`, `result`, `subsystem` | base | Sparse validator-subsystem connection events since exporter start using fixed event, result, and subsystem classes; no endpoint or session labels. | `validator_consensus_prometheus.go` |
| `hl_validator_connection_parse_total` | Counter | Prometheus | `result` | base | Validator-connection source records since exporter start by bounded parser result. | `validator_consensus_prometheus.go` |
| `hl_visor_blocks_applied` | Gauge | Prometheus | - | base | Nonnegative process-generation observation height - initial_height from the latest valid visor state; reset to 0 when either operand is unavailable. | `prometheus_instruments.go` |
| `hl_visor_consensus_ahead_of_wall_seconds` | Gauge | Prometheus | - | base | consensus_time - wall_clock_time for the latest visor sample, in seconds (positive = chain ahead of local wall). | `prometheus_instruments.go` |
| `hl_visor_hardfork_version` | Gauge | Prometheus | `source` | base | Hardfork version reported by the latest valid visor state; this is not artifact hf or binary selection. | `validator_consensus_prometheus.go` |
| `hl_visor_hardfork_version_available` | Gauge | Prometheus | - | base | Whether hardfork_version was present and nonnegative in the latest valid visor state. | `validator_consensus_prometheus.go` |
| `hl_visor_height` | Gauge | Prometheus | - | base | Latest height observed by the visor (block height the node has applied). | `prometheus_instruments.go` |
| `hl_visor_initial_height` | Gauge | Prometheus | - | base | initial_height from the latest valid visor state; this is scoped to that reported process generation. | `prometheus_instruments.go` |
| `hl_visor_last_observation_age_seconds` | Gauge | Prometheus | - | base | Age of the most recent visor sample read by the exporter, in seconds. | `prometheus_instruments.go` |
| `hl_visor_reference_lag_populated` | Gauge | Prometheus | - | base | 1 if the visor sample carried a reference_lag field, else 0 | `prometheus_instruments.go` |
| `hl_visor_reference_lag_seconds` | Gauge | Prometheus | - | base | Reference-node lag reported by the visor when available, in seconds. | `prometheus_instruments.go` |
| `hl_visor_scheduled_freeze_height_available` | Gauge | Prometheus | - | base | Whether scheduled_freeze_height was non-null in the latest valid visor state; the legacy height gauge is meaningful only while this is 1. | `validator_consensus_prometheus.go` |
| `hl_visor_scheduled_freeze_height_current` | Gauge | Prometheus | `source` | base | Explicitly scheduled core/ABCI freeze height from the latest valid visor state; the series is absent while scheduled_freeze_height is null. | `validator_consensus_prometheus.go` |

### Deprecated compatibility families (29)

These aliases preserve one transition release and are scheduled for removal in the next major release. Their replacement and semantic limits are in [UPGRADING.md](../UPGRADING.md).

| Metric | Type | Backend | Labels | Registration | HELP | Source |
|---|---|---|---|---|---|---|
| `hl_consensus_validator_rtt` | Gauge | OTel bridge | `ip`, `moniker`, `validator` | base | Deprecated compatibility family: most recent successful TCP connect duration to a validator endpoint in milliseconds; not protocol RTT | `instruments.go` |
| `hl_evm_db_checkpoint_height` | Gauge | Prometheus | `tier` | base | Deprecated misleading name: core/ABCI height read from hyperliquid_data/evm_db_hub_<tier>/cp_checkpoint_height; not EVM block height or current execution head. | `prometheus_instruments.go` |
| `hl_evm_db_checkpoint_lag_blocks` | Gauge | Prometheus | - | base | Deprecated misleading name: fast minus slow persisted core/ABCI cp_checkpoint_height files when both are readable; not an EVM execution lag. | `prometheus_instruments.go` |
| `hl_exporter_monitor_last_tick_seconds` | Gauge | Prometheus | `monitor` | base | Deprecated compatibility alias for the most recent valid observation reported through MarkMonitorTick; absent before one is observed. | `prometheus_instruments.go` |
| `hl_node_child_crashes` | Gauge | Prometheus | `reason` | base | Compatibility projection of retained material child-stderr artifacts by bounded evidence reason; a reason is not a guaranteed crash or upgrade cause. | `prometheus_instruments.go` |
| `hl_node_child_last_crash_seconds` | Gauge | Prometheus | `reason` | base | Compatibility timestamp of the newest retained material child-stderr artifact per bounded evidence reason. | `prometheus_instruments.go` |
| `hl_node_child_starts` | Gauge | Prometheus | - | base | Compatibility projection of artifacts retained in visor_child_stderr; includes readable empty start receipts and material artifacts, is prune-aware, and must not be rated. | `prometheus_instruments.go` |
| `hl_node_rate_limited_files` | Gauge | Prometheus | `stream` | base | Deprecated alias: non-empty regular files retained in the lexicographically newest rate_limited_ips date directory per fixed stream; evidence is not active rate limiting or an offender count. | `prometheus_instruments.go` |
| `hl_p2p_lz4_bytes_total` | Gauge | Prometheus | `direction`, `ip` | base | Deprecated latest-window gauge of the unresolved upstream byte field per bounded endpoint/direction; not cumulative despite the name. Do not use rate/increase. | `prometheus_instruments.go` |
| `hl_p2p_lz4_compression_ratio` | Gauge | Prometheus | `direction`, `ip` | base | Deprecated latest-window gauge: byte-field-weighted mean of upstream-reported per-port ratios for a bounded endpoint/direction; not a derived aggregate compression ratio. Do not use rate/increase. | `prometheus_instruments.go` |
| `hl_p2p_lz4_global_bytes_total` | Gauge | Prometheus | - | base | Deprecated latest-window gauge of the source-provided unresolved global byte field; not cumulative despite the name. Do not use rate/increase. | `prometheus_instruments.go` |
| `hl_p2p_lz4_global_packets_total` | Gauge | Prometheus | - | base | Deprecated latest-window gauge of the source-provided global packet field; not cumulative despite the name. Do not use rate/increase. | `prometheus_instruments.go` |
| `hl_p2p_lz4_global_ratio` | Gauge | Prometheus | - | base | Deprecated latest-window gauge of the source-provided global ratio; not a locally derived compression ratio. | `prometheus_instruments.go` |
| `hl_p2p_lz4_packets_total` | Gauge | Prometheus | `direction`, `ip` | base | Deprecated latest-window gauge of the upstream packet field per bounded endpoint/direction; not cumulative despite the name. Do not use rate/increase. | `prometheus_instruments.go` |
| `hl_p2p_non_val_connections` | Gauge | Prometheus | - | base | Deprecated alias: sum of source connection_count across explicit children in the latest fresh child_peers status snapshot; this is not a peer count. | `prometheus_instruments.go` |
| `hl_p2p_non_val_peer_connections` | Gauge | OTel bridge | `verified` | base | Deprecated compatibility gauge: explicit child identities in the latest fresh child_peers status snapshot by verification status; despite the name, values are peers, not connections | `instruments.go` |
| `hl_p2p_non_val_peers_total` | Gauge | OTel bridge | - | base | Deprecated compatibility gauge: explicit child identities in the latest fresh child_peers status snapshot; not all connected peers | `instruments.go` |
| `hl_p2p_peer_count` | Gauge | Prometheus | `direction` | base | Number of distinct canonical IP endpoint rows reported in the latest complete tcp_traffic sample per traffic direction, including zero-valued rows; this is observation, not connectivity. | `prometheus_instruments.go` |
| `hl_p2p_peer_traffic` | Gauge | Prometheus | `direction`, `ip` | base | Raw unresolved-unit tcp_traffic point value from the latest complete snapshot for each top-16 positive endpoint and traffic direction; ip="other" sums the remaining positive endpoints. This is not a byte counter, connection origin, or topology role. | `prometheus_instruments.go` |
| `hl_p2p_peers` | Gauge | Prometheus | - | base | Deprecated alias: unique canonical IPs with any positive value in the latest complete tcp_traffic snapshot; observation is not connectivity. | `prometheus_instruments.go` |
| `hl_p2p_peers_added_total` | Counter | Prometheus | - | base | Deprecated alias: qualified process-local traffic-endpoint admissions since exporter start after two consecutive eligible snapshots. | `prometheus_instruments.go` |
| `hl_p2p_peers_evicted_total` | Counter | Prometheus | - | base | Deprecated alias: qualified process-local traffic-endpoint evictions since exporter start, summed across TTL and capacity reasons. | `prometheus_instruments.go` |
| `hl_p2p_sample_age_seconds` | Gauge | Prometheus | - | base | Wall-clock seconds since exporter receipt of the latest complete valid tcp_traffic snapshot; advances without new records. | `prometheus_instruments.go` |
| `hl_p2p_tcp_connections` | Gauge | Prometheus | `port`, `state` | base | Deprecated aggregate of kernel TCP socket rows associated with a configured service port on either literal socket side, grouped by state; use hl_p2p_tcp_socket_connections. | `prometheus_instruments.go` |
| `hl_p2p_total_traffic` | Gauge | Prometheus | `direction` | base | Sum of the raw unresolved-unit tcp_traffic point field across all reported endpoint rows in the latest complete snapshot, per traffic direction. | `prometheus_instruments.go` |
| `hl_p2p_unique_peers_seen` | Gauge | Prometheus | `window` | base | Deprecated alias: qualified process-local traffic endpoints refreshed within the fixed window after two consecutive top-16 positive snapshots; observation is not connectivity. | `prometheus_instruments.go` |
| `hl_visor_blocks_above_freeze` | Gauge | Prometheus | - | base | Deprecated compatibility value: nonnegative latest visor height minus persisted hyperliquid_data/freeze_abci_height when both are readable; not process-generation progress. | `prometheus_instruments.go` |
| `hl_visor_freeze_abci_height` | Gauge | Prometheus | - | base | Deprecated compatibility value read from hyperliquid_data/freeze_abci_height; this persisted core/ABCI height can survive process restarts and is not a current scheduled freeze. | `prometheus_instruments.go` |
| `hl_visor_scheduled_freeze_height` | Gauge | Prometheus | - | base | Deprecated compatibility value for scheduled_freeze_height from the latest valid visor state; consult hl_visor_scheduled_freeze_height_available because 0 also represents unavailable. | `prometheus_instruments.go` |
<!-- END GENERATED METRIC INVENTORY -->
