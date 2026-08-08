# Hyperliquid node metrics audit

**Audit date:** 2026-08-08
**Our baseline:** `validaoxyz/hyperliquid-exporter` at `c7920554db5805fee5c419ab19b04ba27ea17fa8`
**Comparison baseline:** `dwellir-public/hyperliquid-exporter` at [`b41c7bde61615c17e587619dcd1e2620df8bda29`](https://github.com/dwellir-public/hyperliquid-exporter/commit/b41c7bde61615c17e587619dcd1e2620df8bda29)
**Live evidence:** one ValiDAO Hyperliquid testnet validator, read-only inspection of `/home/ubuntu/hl` and its running exporter
**Mutation boundary:** report only. No exporter or validator code, configuration, data, process, or service was changed.

## Executive verdict

The exporter has broad source coverage and several good foundations, but it is not yet safe to treat every emitted metric as an accurate statement about the node. The highest-risk problems are semantic, not syntactic: values are parsed successfully and exposed under plausible names while meaning something materially different from their HELP text.

The four findings that should block calling the current metric set fully trustworthy are:

1. **Every normal consensus block is counted as a timeout-certificate block.** The log carries `"tc":null`; `json.RawMessage("null")` has length four, so `len(block.TC) > 0` passes, unmarshalling succeeds into an empty struct, and TC counters/histograms advance. On the live exporter, QC and TC histogram counts were both approximately 47.3 million even though a recent 32 MiB log slice had 4,260/4,260 blocks with `tc:null` and zero TC objects. See `internal/monitors/consensus_monitor.go:507-542`.
2. **Disk usage overcounts checkpoint hardlinks by roughly 6.6 times in the inspected `hyperliquid_data` namespace.** The walker adds `info.Size()` for every pathname and never deduplicates `(device,inode)`. That namespace summed to approximately 106.9 GB path-wise versus 16.27 GB in unique-inode apparent bytes. This is not the whole node footprint: `/home/ubuntu/hl/data` separately occupied about 119.50 GB. See `internal/monitors/disk_monitor.go:97-140`.
3. **An omitted chain silently selects the testnet API.** `--chain` is required only when OTLP is enabled, while every non-`mainnet` string—including `""`—selects `api.hyperliquid-testnet.xyz`. A Prometheus-only mainnet exporter can therefore publish testnet stake, jail, validator, proposer, and update data without failing. See `cmd/hl-exporter/main.go:128-143` and `internal/hyperliquid-api/resolver.go:59-78`.
4. **Exporter readiness and monitor ticks do not prove fresh data.** Readiness means only that every currently registered goroutine was launched; it does not track exit or successful source reads. Several monitors mark a tick after a no-op, source failure, or parse-free poll, and some stale series are never reconciled. The live exporter reported ready while `validator_latency` and `mempool_txs` had no tick, `validator_status` had a tick but no start record, and empty latency/Tokio source trees still had old published values.

Those are not isolated edge cases. The same audit found rotation loss in the generic stream tailer, unbounded stale API cache success, restart-zero values presented as validator latency, biased EVM distributions, incomplete action semantics, misleading socket and peer identities, and status populations that confuse jailed validators with active network failures. The exporter does correctly filter the exact `0.4`-second EMA no-data sentinel; that behavior should be retained.

There is also a strong positive result: substantial parts are already correct and should be retained. In particular, block/apply unit conversions, consensus-wall sign, heartbeat milliseconds, QC percentage units, accumulator `delta` handling, top-N TCP/LZ4 reconciliation, current array-based operation counting, bounded mempool-txs action/TIF labels, and our shared TCP snapshot plus EWMA/hysteresis parent candidate are sound foundations.

### Recommended disposition

| Priority | Decision | Scope |
|---|---|---|
| P0 | Correct before relying on alerts | TC-null handling; chain fail-closed; hardlink-aware disk bytes; monitor/source health and stale-series withdrawal |
| P1 | Correct next | tailing/rotation contract; validator active-set and heartbeat semantics; validator latency restart-zero/freshness semantics; EVM zero observations and contract classification; API retry/cache freshness; replica action/operation definitions |
| P1 | Add | home-validator safety, active-unjailed counts/weight, source ages, fast-head freshness/progress, scheduled freeze/hardfork, explicit verified-child state, RocksDB backlog/error classes |
| P2 | Improve | histogram buckets, TCP/LZ4 naming, dominant-inbound confidence, bounded protocol funnels, snapshot/checkpoint freshness, replay/session lifecycle |
| Reject | Do not copy from Dwellir | guessed multi-port “peer reachability,” heterogeneous `direction`, unproven traffic-volume counters, peer-attributed parent quality, raw incoming IP labels, global 200-series cleanup |

## Scope, evidence, and confidence

This audit followed source-to-metric paths rather than comparing names alone:

```text
live file or API
  -> observed schema / cadence / retention / reset behavior
  -> resolver and tailing behavior
  -> parser and state update
  -> OTel / Prometheus instrument type and labels
  -> scrape lifecycle
  -> defensible operator query and alert meaning
```

Evidence consisted of:

- The complete exporter source and metric documentation at the baseline commit.
- A read-only filesystem inventory and bounded statistical scans of retained live testnet logs.
- A live Prometheus scrape: 196 `hl_*` TYPE families and 4,983 `hl_*` samples at the observation point.
- Cross-source joins among status, stakes, heartbeat, latency, consensus, block times, replica sessions, replay markers, child stderr, snapshots, EVM, TCP, and RocksDB.
- The Dwellir source, history, tests, docs, and merged peer PRs at its stated baseline.
- Fresh local checks: `go test -count=1 ./...`, `go test -race -count=1 ./...`, and `go vet ./...` all passed.

Passing tests establish that the exercised build, tests, and race-instrumented paths completed cleanly. They do not prove the unexercised startup graph race-free or cover the live semantic failures in this report.

### Evidence language

- **Proven** means the exact source bytes and the exact code path establish the claim.
- **Strongly supported** means multiple live sources agree, but Hyperliquid does not publish a formal schema contract.
- **Inference** means a useful hypothesis that must not be encoded as a hard metric meaning until independently validated.

This is one testnet validator snapshot, not a guarantee that every mainnet build or future node version emits the same optional sources. Every source-dependent metric therefore needs explicit schema/version and freshness observability.

### Independent-model checkpoint status

A receipt-bound Verify campaign was attempted over 31 critical source targets.

- GPT-5.6 Sol at max completed two accepted shards with the exact requested pin.
- GPT-5.6 Sol Pro at max completed two accepted shards with the exact requested pin.
- Fable 5 and Opus 5 both ran at exact max and exact pins after a quota reauthentication. Their substantive responses were not accepted as Debate artifacts: each appended its required `model_ran` report after JSONL findings, while the semantic adapter required every line to be a finding. Smaller disjoint shards reproduced the incompatibility.

The campaign was therefore closed as **partial**, not reported as a successful Debate. No claim enters this report solely because Fable or Opus said it; the overlapping claims included here were independently reverified against source and live data.

## Live `hl/` source inventory

At the node-home level, `/home/ubuntu/hl/data` occupied about 119.50 GB at inspection. The separate `hyperliquid_data` checkpoint namespace was heavily hardlinked; `/home/ubuntu/hl/tmp` occupied about 1.32 GB, almost entirely one stale approximately 1.3208 GB file dated 2025-07-20; and `file_mod_time_tracker` held operator configuration/failed-load metadata already covered by the extended operator-config monitor. These root-level trees must not be conflated with one another when interpreting disk totals.

### Top-level data sources

| Source under `/home/ubuntu/hl/data` | Observed shape and cadence | Retention/current state | Current exporter coverage | Useful metric contract |
|---|---|---|---|---|
| `accumulator_buckets/consensus/*/hourly` | JSONL `{time,n,delta}`; busy buckets about every 30s, sparse buckets event-driven | Active | Good core handling | `n` is observations; `delta` is interval total. Accumulate `delta` once into process counters; expose source age |
| `crit_msg_stats` | Date-partitioned critical-message summaries | Active | Partial | bounded critical location/count; source age; clear absent locations |
| `dhs/EvmBlockNumbers`, `EvmBlocks`, `EvmTxs` | EVM index/integrity streams | Active | Not directly used | continuity, duplicate/reorg/integrity checks; lower priority than unified EVM stream |
| `evm_block_and_receipts/hourly` | `[timestamp, versioned block, receipts]`, about 1s | Active | Broad but semantically incomplete | head, freshness, gaps, tx/receipt parity, gas, success/failure, version tag, precompile/system activity |
| `node_fast_block_times` | JSONL height/block time/begin wall/apply duration | Active | Covered | primary consensus/apply health, height velocity, freshness, apply histogram |
| `node_slow_block_times` | Same schema as fast | Active | Covered | EVM/state/checkpoint path; classify 10k checkpoint episodes separately |
| `node_logs/*` | Hourly protocol event streams | Active; details below | Partial | protocol counters, explicit topology, status, parser/source health |
| `periodic_abci_state_statuses` | zero-byte completion/status markers | Active, retained across dates | Partial | latest completed height/age, expected completion gap; not raw retained count |
| `periodic_abci_states` | about 492 MB MessagePack snapshots, typically 10k heights | Active with pruning sawtooth | Partial | latest completed height/age/size and lag to current head; never deserialize each scrape |
| `replica_cmds` | session/day/10k-segment JSONL, one block per line | Active | Broad | blocks, bundles, signed actions, individual operations, bounded action taxonomy, responses, continuity/freshness |
| `tcp_lz4_stats` | about every 5m; peer rows plus global row | Active | Covered, naming needs work | interval bytes/messages and compression ratio; source age; top-N plus `other` |
| `tcp_traffic` | about every 30s; `[direction,ip,port] -> value` point rates | Active | Good aggregation/top-N | comparative point-rate gauges, direction/port, concentration, source age; unit remains upstream-contract sensitive |
| `validator_latency/<validator>/hourly` | `{time,round,latency}` raw observations | Active for 19 current validators | Covered with torn-line bug | valid measured latency/round only; freshness and observed state |
| `validator_latency_ema` | every about 10s, 96 validators | Active | Partly correct | retain exact ~0.4 no-data filtering; reject all-zero initialization; add observation state/age |
| `visor_abci_states` | about every 5s; height, hardfork, freeze, consensus/wall time | Active | Covered | height/freshness/velocity, consensus-wall, initial height, scheduled freeze, hardfork transition |
| `visor_child_stderr` | expiry/version/start path, file body optional | 3,427 retained files | Covered with classification errors | last true crash, bounded reason, occurrence time, current-child liveness correlation |
| `latency_summaries` | summary JSONL by subsystem/step | **Empty at inspection** | Old values can remain | sample timestamp/age and full reconciliation are mandatory before publishing |
| `latency_buckets` | optional detailed latency source | **Empty** | Optional | expose availability and age before values |
| `tokio_spawn_forever_metrics` | optional task runtime JSONL | **Empty** | Old values can remain if root/file disappears | task counters/gauges only while fresh; delete absent tasks and expose feed availability |
| `DroppedTxs` | sparse accumulator source | Directory present, no observations | Accumulator path exists | empty is consistent with no drop events; accumulate `delta` if observations appear |
| `rate_limited_ips` | three source subdirectories | `abci_stream`, `gossip_rpc_blocks`, and `gossip_rpc_requests` present with no files | Covered | aggregate bounded count/event rate; no raw-IP labels |
| `block_times`, `daily_evm_checkpoints` | legacy paths | Empty/absent | Compatibility paths exist | availability only; do not confuse absence with failure on newer layout |
| `data/log` | six legacy warn/error files under infra/trade/visor | Present, all zero-byte | Optional | availability/age only; empty is not a current error signal |

### `node_logs` substreams

| Stream | Retention seen | Directly observed event classes | Best use | Important trap |
|---|---:|---|---|---|
| `consensus` | about 7d | in/out Block, QC/TC material, votes, rounds, proposer/source | proposal counts, QC size/participation, round advance, true TC | `tc:null`; duplicate local out Block; source is proposer, not delivery parent |
| `consensus_rpc` | about 30d | incoming streams, requests/responses, catch-up/range/error events | bounded request/error/latency/bytes funnel | 30,947 endpoint IPs in 30d; raw endpoints are scanner/request sources, not a peer registry |
| `gossip_connections` | about 30d | stream handles, checks, greeting, verified states | aggregate connection/verification funnel | 517,031 handles from 15,222 endpoints, but only 5 greeted and 4 verified |
| `gossip_rpc` | about 30d | requests and explicit `child_peers status` snapshots | current verified child set/count/tenure; aggregate request rate | 2,738 request sources; only latest child snapshot is current truth |
| `mempool` | about 7d | add/verify/commit/drop/size events | bounded event outcomes and pressure | hourly switch must drain old file; absence/parse failures need age |
| `status` | about 30d | disconnected pairs, heartbeat statuses, stake/jail/heartbeat sets | node/status health and active-set joins | omission is not always zero; nested parse failures currently count as success |
| `validator_connections` | about 30d | connection checks/results | bounded validator connection diagnostics | do not promote arbitrary endpoints without canonical validator identity |
| `replay` | effectively permanent | start-height/time dirs, optional daily marker | restart/session/replay activity | marker mtime does not prove replay duration |

Empty legacy uppercase `Gossip_connections` and `Validator_connections` directories also existed; the active streams were the lowercase paths above. Most date-partitioned non-`node_logs` data families retained roughly 52 hours across three date partitions at inspection. Periodic state/checkpoint storage followed an approximately six-hour pruning sawtooth with newest-three protection. State snapshots were roughly 10,000 heights apart with about 770-second median cadence; completed slow checkpoints had about 680-second median cadence. These are observed retention/cadence facts, not stable protocol guarantees.

### Other operational trees

Four RocksDB databases were present: Exchange, RPC, fast EVM state, and slow EVM state. Current logs expose write stalls, L0 files/score, compaction bytes and amplification, cache occupancy, WAL/SST/manifest activity, and errors/warnings. Exchange and RPC each retained 999 rotated diagnostic logs. The slow EVM store also had expected hardlink/deletion-management warnings that must not be conflated with corruption or no-space failures.

Snapshot/checkpoint trees are heavily hardlinked and pruned. Directory count and logical pathname bytes are not physical backlog measures. Latest completed height, age, size, interval, and distance from current ABCI head are the useful signals.

## P0 correctness findings

### P0-1 — `tc:null` is counted as a timeout certificate

**Code path.** `ConsensusBlock.TC` is `json.RawMessage`. The parser tests only `len(block.TC) > 0`, then unmarshals into `TCData` and records size/counters (`internal/monitors/consensus_monitor.go:507-542`). JSON `null` is nonempty bytes and unmarshals without error into a zero-value struct. `IncrementTCBlocks` is also outside the decode-success branch, so malformed non-null TC JSON increments the block counter even when size and participation parsing fail.

**Live proof.** In a recent 32 MiB consensus slice, all 4,260 Block events had `tc:null`; none had a TC object. The live scrape nevertheless showed approximately:

- `hl_consensus_qc_size_count = 47,337,862`
- `hl_consensus_tc_size_count = 47,337,862`
- sum of `hl_consensus_tc_blocks_total` approximately equal to the same count
- `hl_consensus_tc_size_sum = 76,286`

The identical QC/TC observation counts are the signature of this bug.

**Required correction.** Treat missing and JSON null as no TC. Only record a TC when parsing yields a real object with the required fields; define whether an empty `timeouts` array is valid. Deduplicate Block events by a bounded `(round,hash)` key before updating block-level counters/histograms.

**Regression tests.** Table-test absent, `null`, `{}`, `{"timeouts":[]}`, malformed, and populated TC values. A normal `tc:null` block must not increment any TC metric. A duplicated in/out copy of the same round/hash must contribute once.

### P0-2 — path-size disk metrics count hardlinks repeatedly

**Code path.** `walkSizes` visits every non-directory path and adds `info.Size()` without checking file type, allocated blocks, device, inode, or link count (`internal/monitors/disk_monitor.go:105-140`).

**Live proof.** Checkpoint SSTs are hardlinked. Within `/home/ubuntu/hl/hyperliquid_data`, path-wise apparent sizes totaled approximately 106.9 GB, while counting each `(device,inode)` once yielded approximately 16.27 GB of unique-inode apparent bytes: about 6.6× inflation. This comparison is scoped to that namespace and is not physical allocated-byte truth; sparse allocation still requires `st_blocks`. The separate `/home/ubuntu/hl/data` tree occupied about 119.50 GB and is not part of the 16.27 GB figure.

**Required correction.** Decide and name two distinct contracts:

- Physical allocated bytes: prefer filesystem allocated blocks, deduplicated by `(device,inode)`.
- Logical namespace bytes: path-wise apparent size, explicitly named `logical` and not used for disk-exhaustion alerts.

Keep `statfs` free/total as the disk-capacity authority. If subdirectories overlap, document that per-subtree values are not additive or assign each inode to an explicit ownership bucket.

**Regression tests.** Build a fixture with a sparse file, hardlinks across tracked subdirs, symlinks, nested prefixes, unreadable entries, and a normal file. Assert physical and logical contracts separately.

### P0-3 — empty chain selects testnet silently

**Code path.** CLI validation requires `--chain` only inside the OTLP block (`cmd/hl-exporter/main.go:128-143`). `NewResolver` maps exactly `mainnet` to mainnet and everything else to testnet (`internal/hyperliquid-api/resolver.go:59-78`). The update path follows the same configuration risk.

**Impact.** A default Prometheus-only mainnet deployment can publish testnet network-wide validator data while local filesystem metrics remain mainnet, creating a plausible but internally cross-chain scrape.

**Required correction.** Require an explicit valid chain for every start, or derive it from a single trustworthy local source and fail on disagreement. Use an exhaustive switch returning an error for unknown/empty values. Export the selected chain as immutable target metadata.

**Regression tests.** Empty, typo, mixed case, mainnet, and testnet startup cases; Prometheus-only and OTLP modes; ensure no outbound API request is made before chain validation.

### P0-4 — liveness, successful processing, source freshness, and readiness are conflated

**Code path.** `Ready()` checks `registered && startedUnix != 0`, not running state or ticks (`internal/metrics/health.go:93-112`). Registration occurs incrementally as goroutines start. `RegisterMonitor` does not set `registered=true` if `getOrCreate` already inserted a state (`health.go:30-38,76-90`). `validator_status` bypasses `runMonitor` entirely (`internal/exporter/exporter.go:99-100`). A recovered panic exits the monitor without restart, while `startedUnix` remains set.

Many polling monitors mark a tick after a call that may have returned early. For example, subsystem latency unconditionally ticks after `tickSubsystemLatency`, even if the root is unreadable or no summary is published (`internal/monitors/subsystem_latency_monitor.go:104-114,118-142`). Subsystem steps do the same (`subsystem_steps_monitor.go:118-128`).

The streaming path has an even stronger false-positive: `tailStream` calls `onIdle` after every EOF pass, including passes with no new complete record (`internal/monitors/stream.go:149-175`). Consensus uses that callback to mark a successful monitor tick unconditionally (`consensus_monitor.go:233-270`). A completely frozen consensus file can therefore keep `last_tick` fresh indefinitely.

**Live proof.** Ready was 1 while launched/tick sets disagreed. The current source trees for subsystem latency and Tokio were empty, but old detailed metrics remained in the scrape. `work_fraction` values above one—up to about 3.0—were still exposed under docs promising a 0..1 duty cycle.

**Required correction.** Model these as separate states:

| State | Meaning |
|---|---|
| registered | monitor is part of this configured build/mode |
| running | owning goroutine is currently alive |
| last_run | loop attempted work |
| last_source_success | valid source bytes were read and parsed |
| last_publish | a current snapshot was reconciled |
| source_timestamp / source_age | upstream observation time and freshness |
| parse/io errors | monotonic failures by bounded reason |

Pre-register the complete configured monitor set before serving readiness. On goroutine exit set running=0 and record exit reason; either restart with bounded backoff or withdraw readiness for required monitors. Readiness should be a narrowly documented process/config signal, not proof that all optional streams contain data.

**Regression tests.** Monitor exits before first tick, exits after tick, idle optional source, missing required source, parse-only failures, directory appears after startup, directory disappears, and pre-existing unregistered state. Assert both `/readyz` and all health series.

## P1 interpretation and lifecycle findings

### P1-1 — generic tailing loses data at rotation and late source creation

The generic `tailStream` closes the old file as soon as a newer path resolves and clears `pending`, without first draining the old tail (`internal/monitors/stream.go:76-100`). It also leaves `firstRun=true` while no file exists; if the first file appears after exporter startup, it seeks that file to EOF and discards data written while the exporter was already running (`stream.go:119-132`).

Affected streams include block, proposal, EVM, replica, consensus, and status. Separate implementations repeat related failures:

- `gossip_connections` seeks every newly selected hourly file to EOF, losing events emitted before its next poll.
- mempool and mempool-txs switch without draining the old hourly tail. If their directory exists but contains no file at startup, the first file created later is also classified as startup history and sought to EOF, dropping its initial post-start records (`mempool_monitor.go:73-98`, `mempool_txs_monitor.go:136-159`).
- raw validator latency uses `bufio.Scanner`; a torn final fragment is returned as a token, fails JSON, and the subsequent seek position is committed, permanently skipping the completed line (`validator_latency_monitor.go:223-245`).

The accumulator monitor is the counterexample to follow: it starts existing files at EOF, treats a file created after startup as new, drains the prior file before rollover, and commits only through the last newline (`accumulator_consensus_monitor.go:74-85,114-155`).

**Required shared tailer contract:** committed complete lines only; persistent file identity and offset; drain-before-switch; truncation/replacement detection; directory-appeared-after-start distinction; bounded record size; explicit startup no-replay policy; source timestamp/age; parser error count; cancellation-safe reads.

### P1-2 — consensus duplicates slightly bias block-level metrics

In a 64 MiB sample there were 8,490 Block events but 8,473 unique rounds. All 17 duplicates were local `out` copies of an already observed `in` block. QC/TC histograms and the latest-100-event QC participation window count events, not unique blocks. At normal cadence the 100-event window represents roughly six seconds, not a durable hourly participation score.

Deduplicate block-level observations by round/hash, retain event-direction counters separately, and expose the participation window length in blocks plus age/time span.

### P1-3 — validator status failures masquerade as healthy state changes

The validator-status path can clear role/address on missing, stale, read, or parse failure and return nil; it can consume a torn final line; and it bypasses the standard monitor health wrapper. Jail and related labels can remain stale when the source fails. The initial OTel resource identity is captured before later runtime correction and cannot be mutated.

The semantics are internally inconsistent. `readValidatorStatus` declares the source stale after 12 hours, clears role/address, and returns nil; `GetValidatorStatus` accepts the same source for 24 hours (`internal/monitors/validator_status_monitor.go:130-165,285-361`). The 30-second loop marks a successful tick even after the nil-returning missing/stale/read/parse paths. This makes an observation failure look like a fresh non-validator decision.

Address enrichment is also unsafe and deterministically wrong during a modern-schema cold start. `GetValidatorStatus` runs before `PopulateSignerMappings` (`cmd/hl-exporter/main.go:148-155`); numeric stake rows carry no signer mapping, so it falls back to publishing the signer as the validator (`internal/monitors/validator_status_monitor.go:341-361`). The OTel resource snapshots that startup value permanently (`internal/metrics/init.go:61-75`). Separately, metric-label expansion presumes every `0x` input is a signer, including genuine validator addresses (`internal/metrics/setters.go:678-717`). Proposer-name lookup is literally hardcoded to return an empty string (`internal/metrics/getters.go:22-26`), so that path cannot emit a resolved proposer name.

**Required contract.** A source failure must not become a valid “not a validator” observation. Keep last-good value with explicit age until a bounded stale threshold, then withdraw/mark unknown. Distinguish validator address, signer address, and moniker. Resource identity is startup identity; mutable status belongs in gauges/info series.

### P1-4 — the API retry is not a real retry, and stale cache is returned as success indefinitely

`makeAPICall` constructs one request outside its three-attempt loop (`internal/hyperliquid-api/resolver.go:181-211`). The first `Do` consumes/closes the body; subsequent caller-level attempts reuse a drained body while retaining the original content length. Response closes are deferred inside the loop. A transient failure therefore frequently becomes one real request plus failed local retries.

On any fetch error, `GetValidatorSummaries` returns cached summaries with `nil` error whenever a cache exists, with no maximum age or exported failure/freshness signal (`resolver.go:81-108`). Optional fields that disappear or fail parsing can leave previous gauges frozen.

The optional-field reconciliation is incomplete even on a successful API response. An invalid or newly absent commission/uptime/APR value does not delete the previous series while the validator and its three identity labels remain unchanged. The parser accepts any nonempty stats-period string as a Prometheus label, but cleanup only enumerates `day`, `week`, and `month` (`internal/monitors/validator_api_monitor.go:117-222`); an unexpected period can therefore create an unbounded, permanently stale series.

**Required correction.** Rebuild request/body per attempt, close each response in its iteration, bound response bodies, and export API last-success, last-attempt, failure counter, cache age, and stale-serving state. Decide a maximum acceptable age for safety-critical jail/stake data.

### P1-5 — active validator, jail, and heartbeat populations are misread

The live `current_stakes` payload is an object containing `validator_to_stake` and `hard_validators`, not one flat active list. At inspection:

- 96 stake rows existed.
- 53 had positive stake.
- 18 were positive and unjailed, with total observed weight 6,951.
- 35 were positive but jailed, with total observed weight 757.
- The jail list contained 228 identities.
- All 77 validators listed as missing heartbeat were jailed.
- All 18 positive/unjailed validators were heartbeat-seen.
- The seen set had 19 identities, including one jailed/transitional identity.

Therefore “77 missing heartbeat” is not “77 active validators are failing.” It is largely a set-membership artifact. The adjacent `hard_validators=92` field is also not the active validator set; the defensible live active population was positive-stake intersected with unjailed: 18 validators at the inspection point.

Heartbeat series update only when values are positive (`internal/monitors/consensus_monitor.go:873-909`), so zero, null, and omission cannot clear old children. Connectivity publishes only disconnected pairs at value 0 and deletes on reconnect (`consensus_monitor.go:844-870`); absence means connected **or unknown/incomplete**, not a positive connectivity measurement.

Nested parsing failures are hidden from the outer status monitor. `processStatusLine` returns success after calling two void helpers; those helpers log and skip malformed nested arrays/items. A partially decoded `disconnected_validators` snapshot is then treated as complete and can delete prior disconnected pairs as “reconnected,” while the line advances the successful-line counters (`consensus_monitor.go:741-870`). Heartbeat items fail similarly but retain their previous positive values.

**Required joins and metrics:** active-unjailed count/weight; positive-jailed count/weight; home-validator jailed; home heartbeat observed and ack age; missing heartbeat intersected with active-unjailed; roster/status source completeness; reconciliation that deletes omitted validators after a complete snapshot.

`next_proposers` is a weighted 20-slot schedule, not 20 unique validators. The observed schedule had six unique validators with multiplicities 5, 5, 4, 4, 1, 1. Name the metric slot count/share, not unique proposer count.

### P1-6 — validator latency publishes restart initialization and lacks a freshness contract

Raw current latency had 19 real validators with p50 around 2.8 ms. The EMA source stream had 96 rows: normally 19 observed values and 77 values at approximately 0.4 seconds. No raw sample was exactly 0.4 seconds. Two restart windows emitted all 96 entries as zero for about three minutes.

Neither source value is a measured latency, but the current implementation treats them differently. It correctly filters only the exact `0.4` no-data sentinel (with a small floating-point epsilon) and retains real values above 400 ms (`internal/monitors/validator_latency_monitor.go:362-373`). It does **not** reject the all-zero initialization epoch, so every unseen validator can be published as a genuine zero-latency sample after a node restart. If the current-day EMA file later disappears, `processEMAFile` returns success without clearing or aging the prior snapshot (`validator_latency_monitor.go:273-300`). It also scans the entire growing daily file every 10 seconds merely to recover the last line (`validator_latency_monitor.go:288-293`).

**Required correction.** Preserve the exact `0.4` filter. Join EMA entries with raw observation/heartbeat state, reject or explicitly classify all-zero initialization epochs, and publish `observed`, source timestamp, and sample age. Clear or mark stale on source withdrawal. Tail the committed last record instead of rescanning the whole date file. Prefer raw latency for validator-quality statements.

### P1-7 — subsystem latency values can be ancient, and `work_frac` is not proven to be 0..1

The inspected `latency_summaries`, `latency_buckets`, and Tokio roots were empty while the live exporter still exposed hundreds of detailed series. Subsystem scanners use the newest named file and last parseable line but do not validate row time or mtime age, reconcile disappeared labels, or signal empty source. The Tokio monitor deletes on an existing stale file, but an empty root fails before deletion (`internal/monitors/tokio_runtime_monitor.go:125-146`).

Raw summary schema contains both `work_frac` and `bucket_work_frac` (`subsystem_latency_monitor.go:70-77`). Live values above 1 prove the current documentation “fraction of wall-clock, 0..1” is wrong for at least some emitted source/state. Do not clamp. Rename as an upstream-reported work value until its normalization window is proven; publish the raw field selected and source version.

### P1-8 — EVM distributions and identities are biased or stale

Live 24-hour evidence: 85,843 blocks, 41,842 transactions, and 53,407 zero-transaction blocks (62.2147%). Current code records `hl_evm_tx_per_block` only when `txCount > 0` (`internal/monitors/evm_monitor.go:315-329`), so the histogram is conditional on nonempty blocks rather than a distribution across blocks.

Other EVM issues:

- Outer timestamps are zone-less nanoseconds. `time.RFC3339Nano` rejects them, so every row falls back to the integer-second block header (`evm_monitor.go:135-145`). In one hour, 58 adjacent rows shared a header second despite positive high-resolution outer gaps. Block-time histograms are quantized and same-second intervals can disappear.
- Contract creation is normally `to:null`; the string assertion at `evm_monitor.go:349-360` misses it. Eighty such creations occurred in 24 hours.
- Every nonzero `to` is counted as a contract interaction when enabled, including EOAs. No code-presence proof exists.
- Max priority fee updates only when greater than zero (`evm_monitor.go:425-433`); blocks with zero/no EIP-1559 fee leave an old maximum frozen. It is the submitted maximum cap, not realized tip.
- Zero base fee similarly risks a frozen gauge.
- Receipts are deliberately not materialized (`evm_monitor.go:122-126`), so transaction success/failure and tx/receipt parity are unavailable despite the source containing them.
- Contract resolver initialization occurs for every EVM run even with contract metrics off and uses a hardcoded Hyperscan endpoint without chain input.
- An asynchronous unknown result can be cached by the EVM monitor for 24 hours, shadowing later resolution. Mutable name/type/symbol labels split counters across old and new label tuples.
- The contract limit bounds distinct addresses entering a local cache, not actual Prometheus series; block type and mutable metadata multiply series.

**Required correction.** Record zero observations; parse zone-less outer time explicitly as UTC; treat null `to` as creation; classify interaction only after code/contract proof or call it non-creation destination traffic; reset per-block gauges to zero; add receipt parity/outcome in a bounded form; make resolver chain-aware and opt-in; separate immutable address counter from mutable info metadata; document a true series budget.

### P1-9 — replica “transactions,” “orders,” and “operations” need stricter definitions

In a recent sample of 6,328 blocks there were 8,097 bundles, 28,617 signed actions, 15,205 order actions, and 204,668 individual orders—about 13.46 orders per order/TWAP action.

- `hl_core_tx_total` counts signed actions by type, not bundles or transactions (`internal/monitors/replica_monitor.go:161-168`).
- `hl_core_orders_total` and orders-per-block count order **actions**, while many readers will assume individual orders. Documentation partly corrects this, but names remain hazardous.
- The parser expands arrays only for order/TWAP, cancel/cancelByCloid, and batchModify (`internal/replica/parser.go:137-155`). MultiSig wrappers and bulk validator/oracle arrays count as one. Observed examples included 442 multiSig actions, 652 `validatorL1UpdateReferenceOracle` arrays of length 155, and 725 `perpDeploy setOracle` bulk actions.
- Malformed bundle pairs and bundle bodies are silently skipped (`parser.go:99-125`), allowing a malformed block to be counted as a valid zero-action block.
- Unknown raw action strings can become unbounded label values in the replica path.

The current array-element counter itself is correct; the old comma heuristic has been removed (`parser.go:162-173`). Preserve that fix.

**Required correction.** Publish distinct bundle, signed-action, and individual-operation families. Add schema-aware expansion for wrappers/bulk types, bounded `other`, parser/skipped-entry counters, and a completeness bit per block. Keep user/protocol response errors separate from node parser/execution failures.

### P1-10 — current block histograms hide meaningful tails

Over 24 hours, fast apply max was about 136.6 ms, while slow path reached about 4.93 s. A 152 s block-time gap occurred during recovery. Current core block-time buckets end at 2,000 ms and apply-duration buckets end at 250 ms (`internal/metrics/instruments.go:113-140`), so operationally important slow/recovery tails collapse into `+Inf`.

Slow-path 4–5 s sawteeth cluster immediately after 10,000-height checkpoint boundaries and recover over tens of blocks while fast-path p95 remains around 236 ms. This is not automatically a consensus stall.

Extend buckets through at least the observed recovery range, retain fast/slow labels, and add clustered episode metrics so one checkpoint sawtooth is not counted as dozens of incidents.

## Peer and network semantics

### What the live logs actually support

`tcp_traffic` is a 30-second point-rate stream keyed by traffic direction, IP, and port. Across two retained days it had 6,205 rows, 129 IPs, six ports, 94 endpoint-set variants, and heavy churn. Every endpoint/port relationship had both `In` and `Out`, proving those labels are byte-flow direction, not connection origin or parent/child role.

The current exporter’s top-16 plus `other` aggregation and per-IP multiport summation are good. Remaining problems:

- Any positive value, including ordinary approximately `1e-6` noise, is called active.
- `Snapshot()` omits its direction map in one path.
- The final valid-looking unterminated fragment can be treated as committed; Dwellir’s committed-newline reader is better.
- Malformed individual flow rows are silently skipped while the enclosing line still returns success; publication then reconciles/deletes against that reduced set, turning a corrupt partial parse into an apparently complete snapshot (`internal/monitors/tcp_traffic_monitor.go:150-197,228-264`).
- The upstream unit is strongly consistent with decimal MB/s when compared with five-minute LZ4 totals, but is not a published contract. Keep values comparative unless that contract is established.

`tcp_lz4_stats` contains five-minute interval bytes/messages and a compression ratio. Those interval totals reset each emission. Gauge families ending `_total` are therefore misleading unless the exporter explicitly accumulates them. Top-N reconciliation is otherwise sound.

### “Parent” is an inference, not a protocol identity

Our implementation is materially better than Dwellir’s parent inference: it reuses one shared TCP snapshot, aggregates ports per IP, smooths with EWMA, and requires 20% challenger dominance before switching (`internal/monitors/tcp_traffic_monitor.go:37-57,221-264`; `parent_peer_monitor.go:12-115`). The snapshot is not reliably newline-committed yet; that is the separate tailing defect above.

But the name still overclaims:

- The monitor runs on validators, where a single parent concept is not meaningful.
- Empty, missing, stale, and all-zero inputs do not clear the old candidate.
- Tenure advances only when a fresh sample arrives and freezes during source outage.
- Equal-value ties depend on Go map iteration.
- A dominant inbound rate does not prove which connection delivered a block.

Rename it to **dominant inbound/effective-upstream candidate**, gate it to node roles where it is meaningful, clear it after stale timeout, and expose top share, top/runner ratio, source age, and confidence. Never feed it into automatic peer scoring or pinning without a real causal delivery source.

### Socket “peer count” is not a peer count

The current socket monitor filters by local port. It includes listeners and accepted clients but misses outbound peer connections whose local side uses an ephemeral port. Worse, open/parse failures for `/proc/net/tcp{,6}` are ignored after a zero-valued grid is pre-seeded, so an unreadable source publishes healthy-looking zero sockets (`internal/monitors/tcp_connections_monitor_linux.go:20-54`). It should expose socket-state observations with explicit local/remote matching and unique endpoint counts, plus source-read success/age; an unreadable source is unknown, not zero.

### Validator “RTT” is TCP connect latency and can overlap heavily

Every five seconds the validator IP monitor starts up to 50 goroutines; each can try ports 4000–4010 with a two-second timeout (`internal/monitors/validator_ip_monitor.go:172-224`). Sustained failure permits about 250 overlapping probe goroutines. Success has no endpoint label, failures do not clear old success, and validators leaving the top 50 retain stale series.

Adopt Dwellir’s scheduler mechanics—single-flight cycles, global concurrency bound, context cancellation, overall deadline, backoff, and successful endpoint cache—but probe only validated endpoints and name the result `tcp_connect_latency`, not network/protocol RTT.

### Explicit child and verified-event sources

`gossip_rpc child_peers status` is the only inspected direct child relationship. It arrived about every 30 seconds, normally named one child, had at most two, and showed seven identities/128 set changes over 30 days. This supports latest-only current child count, verification state, connection count, and tenure with deterministic deletion of absent peers.

Raw incoming streams are not safe peer identities:

- 517,031 gossip handle events from 15,222 endpoints; only 5 reached greeting and 4 verified.
- 120,561 consensus-RPC incoming streams from 30,947 endpoints.
- 2,753 gossip-RPC requests from 2,738 sources.

A persistent 256-entry peer registry that admits those sources will be scanner-polluted immediately. Promote only canonical validator mappings, explicit current children, verified protocol identities, or sustained established traffic. Keep unverified attempts aggregate-only and never emit raw untrusted IPs as default Prometheus labels.

## Dwellir comparison: adopt mechanics, reject headline semantics

Dwellir’s recent peer work is useful prior art, not a trustworthy metric specification. Its merged peer PRs #3–#8 had no GitHub reviews or comments at audit time. The subsystem should not be copied wholesale.

### Peer architecture at a glance

| Area | Ours at the audited baseline | Dwellir at the audited baseline |
|---|---|---|
| TCP traffic | Latest shared sample; per-IP multiport aggregation; top-16 plus `other`; point-rate gauges | Incremental newline-committed tail; registers every row; per-IP additive counters; separate parent reader |
| Peer registry | Memory-only, cap 2,048/24h; fed by every positive TCP row; per-IP gauges opt-in | Persistent, cap 256/48h plus failure gate; multi-source identities, accumulated directions, and `WasParent` |
| Active probing | Validator-only top 50; sequential ports 4000–4010 per validator; cycles can overlap | Generic known-peer probes; 15 guessed ports; bounded single-flight/backoff scheduler |
| Parent inference | Shared multiport aggregate, EWMA, and hysteresis; no causal quality ledger | Second reader, last-sample largest single flow, no smoothing; node-global lag/degraded/block counters attributed to peer |
| Gossip | Latest child aggregates; bounded event types; event counters start at EOF | Per-IP child/request/stream/verification metrics; startup counter replay and batch-union child-state bug |

Our registry’s larger cap and traffic-only input make scanner pollution less immediate than Dwellir’s multi-source registry, but “any positive row is active” remains an incorrect identity/activity threshold in both designs.

Source anchors: ours—`internal/monitors/tcp_traffic_monitor.go:37-63,100-197,221-264`, `internal/peerset/peerset.go:54-116`, `validator_ip_monitor.go:172-224`, `parent_peer_monitor.go:16-115`, and the gossip monitors; Dwellir—`internal/monitors/outbound_peers_monitor.go:53-190`, `internal/peermon/peers.go:14-187,223-307`, `internal/peermon/monitor.go:131-204`, `internal/monitors/parent_peer_monitor.go:91-200`, `internal/monitors/parent_quality.go:54-167`, and its gossip monitors.

### Adopt or adapt

| Idea | Verdict | How to use it here |
|---|---|---|
| Newline-committed incremental reader | Adopt | Port its “only commit through newline” semantics into the shared tailer and TCP reader |
| Single-flight probe cycle | Adapt | Use for validator endpoint probes; one cycle at a time |
| Bounded concurrency, context cancellation, deadlines, backoff | Adapt | Preserve sub-millisecond latency; label exact endpoint and result age |
| Successful-port cache | Adapt | Only after the port came from validated protocol/traffic evidence |
| Generation-protected persistence save | Adapt | Combine with synchronous load-before-producers, schema/chain/node scope, validation, TTL-on-load, newest-first cap, mode 0600 |
| Latest child-state reconciliation | Adapt | Process only newest status snapshot and delete absent children |
| Runner-up comparison | Adapt | Export unitless top share and top/runner ratio; do not create peer quality scores |
| Multi-source observation registry | Adapt | Keep `observation_source`, traffic direction, explicit role, endpoint, verification, and source time separate |

### Reject

| Dwellir behavior | Why it is wrong |
|---|---|
| Discard the observed TCP port, then scan 3001, 3002, 443, 80, and 4000–4010; first listener means peer reachable | Throws away better endpoint evidence and measures arbitrary TCP service dialability, not gossip/peer health; NAT/firewall biased; up to 3,840 dials/cycle and 150 simultaneous fallback dials |
| Duplicate one probe result under accumulated `direction` labels | Conflates byte direction, request direction, child role, and unknown connection strings; RTT has no such direction |
| Add raw `tcp_traffic` samples into `traffic_volume_total` | Unit and integration interval are unproven; if source is a rate, summing raw values is dimensionally wrong |
| Elect parent from largest single IP/port in last sample | Fails multiport aggregation and flaps with 30-second oscillation |
| Attribute node-global lag, block count, and degraded seconds to inferred parent | Non-causal; local disk/CPU/chain stall and other peers contribute; EMAs carry across parent switches |
| Preserve failed parent-probe latency and store latency as integer milliseconds | A failed current-parent probe leaves old success frozen; a successful sub-millisecond connection is truncated to zero |
| Protect `WasParent` from eviction | Lets stale inferred identities squat in bounded state; does not preserve Prometheus history |
| Global 200-series cleanup with a 256-peer × direction design | Scheduler-dependent omissions and churn; conflicts with its own registry size |
| Read `tcp_traffic` a second time for parent | Creates inconsistent cuts and duplicate parsing; our shared snapshot is the correct architecture |
| Publish raw per-IP gossip metrics regardless of `--peer-latency` | The flag controls registry/probe/parent wiring, not the high-cardinality gossip labels operators may think it gates |

### Specific Dwellir defects to avoid

1. It registers every parsed TCP row before testing volume. A prior live non-validator note saw approximately 260 known entries per direction—mostly zero/near-zero—against its `PeerSet` cap of 256, enough to churn the LRU and defeat TTL. The validator inspected for this report saw 129 IPs over two days and 29 in the latest sample, so immediate cap churn is node-dependent; admitting zero/noise rows remains wrong regardless.
2. Parent is the last sample’s largest single flow, not a per-IP multiport aggregate; no smoothing/hysteresis exists.
3. Parent-quality EMAs explicitly survive peer switches, so the new peer inherits the old peer’s lag/degraded history.
4. Observable peer cleanup caps at 200, conflicting with 256 peers and multiple directions. Synchronous OTel counters bypass that map and instead face the SDK’s separate cardinality behavior.
5. Gossip event counters replay the current hour after exporter restart.
6. Child processing unions all status lines in a batch; a peer absent from the newest snapshot can remain connected.
7. Persistence load occurs asynchronously after producers start and can overwrite fresher registrations; it also lacks chain/node scoping and input validation. Its generation check does protect save from concurrent mutation, so the defect is startup ordering rather than an unqualified load/save race.
8. Its documentation diverges from code: it documents only ports 4000–4010 while the probe scans four additional service ports, calls source values GB despite the unproven unit, claims “all block data,” and describes switch warmup even though quality EMAs explicitly span parent changes.

## Correctness ledger for selected operator-critical metric groups

This table prioritizes alert-grade and semantically hazardous families; it is not a row-per-monitor inventory. The complete launched-family inventory is covered by the source tables above and `internal/exporter/exporter.go:63-259`.

| Group | Verdict | Correct query/meaning now | Required change |
|---|---|---|---|
| Fast/slow block height, apply duration, block interval | Mostly correct | Seconds-to-ms conversion is correct; distinguish fast consensus from slow checkpoint path | better buckets, per-source age/health, dedupe/continuity |
| Visor consensus-wall | Correct sign | positive means consensus ahead, negative behind | add freshness, height velocity, freeze/hardfork alerts |
| Accumulator consensus counters | Correct | exporter accumulates interval `delta`; `rate()` on resulting process counters is valid | add source age and optional `n` diagnostics |
| QC size/signatures | Mostly correct | event-derived QC data | dedupe blocks; describe 100-block window duration |
| TC size/participation | **Wrong** | currently counts `null` blocks; malformed non-null increments TC blocks | null/object validation, decode-success gating, and dedupe |
| API validator stake/jail/uptime/APR | Potentially cross-chain/stale | network-wide cache, not node-local | fail-closed chain, API age/failure, reconcile optional fields |
| Heartbeat status | Stale-prone | positive values only | complete-snapshot reconciliation, observed/unknown state, active-set joins |
| Connectivity | Misnamed | presence/value 0 means explicitly disconnected | rename disconnected relation; add snapshot completeness/age |
| Raw validator latency | Good source, torn-line risk | seconds are correct measured latency | committed-line offset and age |
| EMA validator latency | Partly correct | exact `0.4` unseen sentinel is filtered; restart zeros are published | reject initialization epochs; join observed state; source age/withdrawal |
| TCP traffic | Useful bounded comparative gauge, partial-parse risk | flow direction, not connection origin | threshold terminology, committed line, parse completeness, full snapshot, unit contract |
| TCP LZ4 | Good values, ambiguous names | five-minute interval values | use `_interval_*` gauges or accumulate; source age |
| Dominant parent candidate | Useful inference, overnamed | smoothed dominant inbound candidate | role gate, stale clear, confidence, deterministic tie |
| Validator RTT | Overnamed/stale | TCP connect latency to first open guessed port | scheduler, exact endpoint/result, clear stale/failures |
| Socket peers | Wrong identity/failure contract | local-port socket observations; source failure can look zero | local/remote classification, unique endpoint semantics, source success/age |
| EVM block/gas/tx types | Partial | nonzero-block-biased tx histogram; per-block gauges can freeze | zero observations, time parsing, continuity/freshness |
| EVM contracts | Incorrect classification/cardinality | all nonzero destinations when enabled | code proof, null creations, address counter + info metadata |
| Replica action/operation | Useful but incomplete taxonomy | action counts and selected array elements | distinguish bundles/actions/ops, wrappers, bounded labels, parser completeness |
| Mempool/mempool-txs | Mixed | mempool-txs action/TIF labels are bounded; ordinary outcome status is raw; committed-hash cache is not pending depth | drain/late-file handling, bounded status, semantic validation, component docs, source age/errors |
| Subsystem latency/steps | Unsafe when source vanishes | latest parseable retained row | source timestamp/age, clear disappeared series, correct work semantics |
| Tokio runtime | Good stale-file behavior, empty-root gap | cumulative-since-node-restart gauges | clear on empty/disappeared root; reconcile absent tasks |
| RocksDB | Useful subset | current cumulative stall/cache/SST data | L0/score/pending compaction, bounded errors, log age/rotation |
| Snapshot status | Count is misleading | currently newest date dirs only | latest success height/age/size/lag is primary |
| Child stderr | Overclassifies unknowns as panic | some known signatures | bounded explicit taxonomy, unknown separate, current-child correlation, clear old label |
| Replay/runs | Retained population, not rate | session/start evidence | 24h/7d starts and current-session markers; no invented duration |

## Secondary monitor-specific findings

These are lower priority than the P0/P1 items above, but they should be handled during the same subsystem changes rather than left as permanent ambiguity.

### Block monitor health is shared across fast and slow feeds

Fast and slow block streams publish under one logical monitor. Activity on slow can keep the monitor tick fresh while fast height—the primary consensus signal—has stopped, and vice versa. Export per-state source success, source age, last height, and continuity. The aggregate monitor can remain a process-level rollup, but it must not be the only health signal.

### Proposal and latency health registration are inconsistent

Proposal and validator-latency paths do not consistently produce ticks under normal idle/no-source cases; validator status produces state through a path that is not registered/started through `runMonitor`. The live launch/tick matrix exposed these mismatches. Health behavior should be generated by the shared runtime wrapper, not implemented ad hoc by each parser.

### One-time source discovery makes later log creation invisible

Several monitors decide at startup whether a source exists and never rediscover it. Mempool, mempool-txs, accumulator consensus, subsystem latency, subsystem steps, and Tokio park until cancellation when their initial root (or, for subsystem steps, specifically `bucket_guard`) is absent. Validator latency returns permanently when the raw-latency root is absent, which also prevents monitoring an EMA root that already exists. The block monitor selects fast/slow/legacy roots once and never starts a feed that appears later (`internal/monitors/mempool_monitor.go:59-66`, `mempool_txs_monitor.go:121-128`, `accumulator_consensus_monitor.go:63-70`, `subsystem_latency_monitor.go:89-97`, `subsystem_steps_monitor.go:99-110`, `tokio_runtime_monitor.go:93-100`, `validator_latency_monitor.go:72-87`, `block_monitor.go:34-68`).

This matters across upgrades, role changes, delayed mounts, and optional features enabled after exporter startup. Discovery must be periodic and separate from source freshness. An absent optional source may remain healthy-idle, but it must become active without an exporter restart and withdraw prior series if it later disappears.

### Configuration can silently select the wrong source or ignore a feature flag

`ReplicaDataDir` is derived from the environment/default `nodeHome` before `--node-home` overrides `Config.NodeHome`; the override does not recompute the replica path (`internal/config/config.go:113-164`). With replica metrics enabled, the exporter can therefore read a different tree while simultaneously disabling the proposal monitor because it assumes replica will supply proposal counting. Separately, `EVMBlockTypeMetrics` is initialized from its flag and then unconditionally overwritten with `EnableEVM`, so the block-type flag has no effect.

Resolve dependent paths only after final precedence is known, or accept an explicit replica path whose origin is exported. Preserve the feature flag independently and add configuration-precedence tests for environment, default, and CLI combinations.

### Lazy validator identity caches have an untested first-use race

`signerMap` and `validatorInfoCache` are pointers whose nil checks occur outside the mutex used by their initializer (`internal/metrics/types.go:47-113`). Concurrent first use by API and consensus/status paths can read and publish these pointers while another goroutine initializes them. The race-enabled suite passed because it does not exercise this startup graph. Initialize both caches eagerly before monitor concurrency or use `sync.Once`, then add a concurrent cold-start test.

### The global error fan-in can backpressure unrelated monitors

Every monitor gets a one-slot error channel, but one central `select` drains only one error and then sleeps 100 ms (`internal/exporter/exporter.go:21-61,274-405`). The process can therefore account for at most about ten errors per second across all monitors. Blocking producers can stall their own collection behind a full channel; nonblocking producers silently discard excess errors. A noisy monitor also delays visibility for unrelated monitors.

Errors should be bounded per monitor without blocking collection: aggregate counts at the producer, retain the latest bounded diagnostic separately, and rate-limit logging rather than the correctness counter/drain loop.

### OTLP shutdown does not flush the final telemetry window

`InitProvider` keeps the SDK meter provider in a local variable, installs it globally, and returns no shutdown handle (`internal/metrics/init.go:90-121`). Main waits for cancellation and logs graceful shutdown but never calls `ForceFlush` or `Shutdown` (`cmd/hl-exporter/main.go:173-181`). With the five-second periodic reader, the last interval can be lost on an otherwise graceful exit. Retain the provider and perform a bounded shutdown after collectors stop.

### Mempool parsing and label semantics need stricter contracts

The within-file committed-line readers are good, and mempool-txs action/TIF labels are bounded, but JSON `null` is syntactically accepted into zero-value Go targets. A mempool-txs record with a null payload can increment “seen” and observe zero actions/operations; a null `Size stats` value can become a valid zero because unmarshalling null into a `float64` succeeds (`internal/monitors/mempool_txs_monitor.go:231-278`, `mempool_monitor.go:157-219`). The ordinary mempool path also publishes raw `add_tx` and `verify_block` outcome strings directly as the `status` label, so that family is not bounded (`mempool_monitor.go:181-195`). Complete malformed records still advance the reader’s processed count and can refresh monitor health (`mempool_monitor.go:141-153`).

`committed_tx_hashes=100000` is a bounded committed-hash/deduplication cache at its cap, not 100,000 pending transactions and not direct mempool pressure or capacity. The live pressure fields are `uncommitted_txs`, `blocks`, and `rpc_requests`. Require expected object/number JSON kinds and mandatory fields before publishing, collapse outcome status into a reviewed allowlist plus `other`, and document each size component’s actual population.

### Disk logical-byte scans can also report partial success

Beyond the hardlink double-counting P0, `walkSizes` silently skips every unreadable entry and `d.Info` failure, discards the walk error, and publishes the partial total as a successful tick (`internal/monitors/disk_monitor.go:86-140`). Export walk completeness/error count and last-complete time. Filesystem free/total from `statfs` can remain independently valid even when logical attribution is incomplete.

### Peer history windows reset on exporter restart

Peer first/last seen and 5m/1h/24h windows are process-memory views. After restart they undercount history while their names look continuous. Either name them explicitly as `since_exporter_start`/`observed_window`, or persist only validated, scoped observations with source timestamps. Never reconstruct history by replaying current-hour logs into counters.

### Snapshot-status count is an implementation-window count

The current snapshot-status monitor counts only the newest two date directories, while documentation can be read as retained status count. Live status-marker count was about 271 across two days and the large snapshot-file count sawtooths with the pruner. Neither is a stable health KPI. Prefer latest completed height/time/size, current-head lag, expected interval, and missing completion.

### Replica run start uses mutable directory metadata

Replica session directories encode a start time in their name, but the monitor uses parent-directory mtime for a start signal. Directory mtime changes as children are added and is not the session start. Parse the encoded ISO component after validating the layout; expose file observation time separately.

### Child stderr is not a binary panic source

Among 3,427 retained child-stderr paths, the dominant nonempty classes were configuration (1,552), excessive block requests/sync (746), and newer-hardfork QC (370). Only a handful were distinct true panics such as app-hash mismatch, overflow, assertion, missing input, test panic, or home-validator-left-active-set. Two shell syntax/command-wrapper cases are currently liable to fall into a generic panic bucket. A partial first read can freeze classification, and an empty file can be the currently running child’s open stderr—not a clean completed run.

Use an explicit bounded taxonomy: configuration, sync/recovery, expected hardfork transition, app-hash/quorum, storage/input, assertion/overflow, test/simulated, wrapper/shell, unknown, and true panic. Track occurrence time and correlate with session liveness. Delete the prior `last_crash` label when a newer classification replaces it.

### Replay directories prove starts/activity, not duration

The 967 retained replay directories encode height/start time; 371 had a zero-byte daily marker and 596 were empty. Cross-source restarts aligned with replica session, replay directory, child stderr, visor initial height/hardfork, and all-zero latency EMA windows. This supports restart/session correlation. It does not prove that marker mtime minus directory time is replay duration. Export recent starts (24h/7d), current session start height/time, marker present, and last marker activity; keep duration unproven.

### Critical-location identity and cleanup are fragile

Critical-location scanning uses a fixed temporary path rather than deriving all paths from `NodeHome`, which is container/layout sensitive. Basename plus line can collide across source paths. When a fresh snapshot is empty, old labeled locations can remain. Use a stable bounded location identifier, source scope, complete-snapshot reconciliation, and no arbitrary error/message text labels.

### Optional stream and operator-state gauges need withdrawal

Optional stream, operator config, node-state, and RocksDB discoveries generally set values they see but do not always remove values when a source file/task/db disappears. Every snapshot owner needs a previous-label set and one of two explicit policies: complete-snapshot delete, or last-good-with-age followed by stale withdrawal.

### RocksDB counts need reason separation

All four current databases reported zero current write stalls. Slow EVM state nevertheless had numerous file-deletion enable/disable and “delete non-existing file” messages associated with checkpoint hardlink/deletion management. These should be a separate housekeeping warning class, not folded into corruption/fatal errors. High-value additions are L0 files, compaction score, pending compaction bytes, interval read/write/amplification, cache occupancy, WAL/SST/manifest bytes and ages, and bounded fatal/corruption/no-space/checksum/background-error counters.

### Info endpoint probe can report partial success

The active info probe performs more than one logical operation. Meta success can set the endpoint up while a later exchange-status request fails and leaves older detail values. Define endpoint transport availability separately from each response’s freshness/validity; close/bound bodies immediately and clear or age dependent fields on partial failure.

### Contract resolver lifecycle does unnecessary external work

`contracts.NewResolver()` is created for every `--evm` run even when contract metrics are disabled (`internal/monitors/evm_monitor.go:67-80`). Its endpoint is not selected from `cfg.Chain`, so testnet enrichment correctness is unproven. Initialize it only when contract enrichment is enabled, pass chain explicitly, and export enrichment availability/age without letting it affect core EVM metrics.

### Process metrics omit several resource-exhaustion signals

Existing CPU/memory/fd metrics are useful, but validator operations also need process read/write byte rates, open-FD headroom relative to limit, cgroup CPU throttled seconds/events, cgroup memory current/max/pressure/OOM, and possibly disk IO pressure. The hardcoded Linux clock-tick assumption is likely correct on the inspected host, but its comment should not equate userspace `USER_HZ` with the kernel build HZ.

### Stale-series lifecycle must include “source no longer enumerated”

It is not enough to remove labels only when a file is old. Empty roots, deleted files, renamed tasks, validators leaving the selected top set, optional fields disappearing, and a source switching from populated to empty are all lifecycle transitions. The owning monitor must reconcile them; a generic global LRU cannot infer domain truth.

## Missing metrics, prioritized

### P0 — validator safety and exporter truth

| Proposed semantic family | Source | Labels/bounds | Why it matters |
|---|---|---|---|
| home validator jailed/active/stake | current stakes + jail set + local identity | no high-cardinality label needed | direct alert on the operator’s validator |
| home heartbeat observed, since success, ack duration | status heartbeat joined to active set | status type allowlist | separates real validator failure from jailed roster noise |
| active-unjailed validator count and stake | stake/jail intersection | network/chain metadata only | correct denominator for network health |
| fast head height, age, velocity, continuity | fast block + visor | state type | primary node progress/freshness |
| scheduled freeze present/distance and hardfork version | visor state | bounded | pre-transition operational alerting |
| monitor running/exit and source success/age | every source | monitor/source allowlist | makes stale values observable |
| parser/io/schema failures | every parser | bounded reason/source | detects schema drift rather than freezing silently |

### P1 — consensus, topology, storage, and EVM integrity

| Proposed semantic family | Source/contract |
|---|---|
| true TC/QC/catch-up/dropped-tx/RPC rates | accumulator `delta`, accumulated exactly once |
| validator latency observed + valid latency + sample age | raw/EMA joined to heartbeat/observation; sentinels excluded |
| explicit verified child count/state/tenure | newest `gossip_rpc child_peers status` snapshot only |
| TCP aggregate by port/direction and top-peer concentration | point-rate gauges, top-N+other, no invented connection direction |
| consensus/gossip RPC errors and demand | bounded oversize, EOF, reset, timeout, notfound, catch-up response/range/failure/latency |
| gossip verification funnel | aggregate attempt -> check -> greeting -> verified; no raw IP labels |
| slow checkpoint-lag episodes | cluster contiguous slow-path deviations around 10k boundaries |
| snapshot/checkpoint latest height/age/size/lag | completion marker plus current head |
| RocksDB L0 files/score, pending compaction, stalls, fatal/corruption/no-space/checksum | current diagnostic log with bounded reason |
| EVM head age/velocity/gaps/duplicates/version | unified block/receipts stream |
| EVM tx/receipt parity and success/failure | bounded receipt decoding, no per-tx labels |
| heartbeat snapshot size/completeness/consistency | status snapshots; do not label raw heartbeat hashes |

### P2 — useful operational depth

- Five-minute LZ4 bytes/messages/ratio and expansion rate.
- Dominant inbound share and runner-up ratio.
- Proposal production share by validator, explicitly named proposal—not relay—metrics.
- Replica bundles, signed actions, individual operations, and validator duty actions.
- Replay/session/start lifecycle and restart correlation.
- Process I/O rates, file-descriptor headroom, cgroup CPU throttling, and cgroup memory pressure.

### P3 — lower-value or diagnostic-only

- Protocol volume by broad category.
- DHS integrity/reorg cross-checks.
- Temporary/checkpoint housekeeping by physical bytes and age.
- Opt-in per-verified-child detail. Never default raw scanner/request IP labels.

## Metric design contract for implementation

Every new or corrected family should answer these questions in code comments and docs:

1. **Population:** exactly which entities are included? Active-unjailed validators is not all stake rows.
2. **Observation:** what source field proves the value? Presence, omission, null, zero, and sentinel must be distinct.
3. **Time:** source timestamp, scrape timestamp, cadence, and stale threshold.
4. **Type:** monotonic counter, point gauge, interval-total gauge, histogram observation, or info relation.
5. **Restart:** exporter restart, node restart, source rotation, and retention behavior.
6. **Reconciliation:** how labels disappear when absent, stale, invalid, or no longer top-N.
7. **Cardinality:** hard bound after all label cross-products, not merely address-cache size.
8. **Query:** one canonical PromQL expression and its limits.

### Cardinality budget

| Entity | Default policy |
|---|---|
| validator roster | bounded by roster; reconcile complete snapshots |
| verified children | latest current set; small hard cap with aggregate overflow |
| traffic peers | top 16 per direction/port dimension plus `other`; hysteresis |
| unverified endpoints | aggregate counts only; no raw IP label |
| action/event/reason types | explicit allowlist plus `other` |
| contracts | opt-in; immutable address counter plus separate mutable info; document full label-product cap |
| arbitrary error text/hash/order/user/asset | never a label |

### Naming rules

- Use `_total` only for monotonic counters in the exporter’s metric contract.
- Name resettable upstream cumulative mirrors as `_current`/`_since_node_start`, or retain legacy names only with an explicit gauge type and query warning.
- Name five-minute LZ4 values `_interval_bytes`, `_interval_messages`, and `_compression_ratio` unless accumulated.
- Rename “parent” and “RTT” to the actual observation: dominant inbound candidate and TCP connect latency.
- Name connectivity as a disconnected relation if value 1 is never emitted.

## Implementation sequence — no code changes in this audit

### Phase 0 — semantic safety

1. Fix TC-null/object validation and block deduplication.
2. Require/validate chain before metrics or network calls.
3. Split disk metrics into statfs capacity, physical unique allocated bytes, and optional logical apparent bytes.
4. Pre-register monitors; add running/exit/source-success/source-age/parse-error states; withdraw stale series.
5. Correct docs/HELP immediately for any metric whose current meaning cannot change atomically.

### Phase 1 — one shared source reader contract

1. Extract the proven accumulator/Dwellir committed-line mechanics into a shared tailer.
2. Migrate generic streams, gossip connections, mempool, mempool-txs, and raw validator latency.
3. Add source identity, offset, timestamp, age, rotation, truncation, and parser telemetry.
4. Build golden fixtures from sanitized live shapes and schema-drift cases.

### Phase 2 — validator and consensus truth

1. Implement active-unjailed joins and home-validator metrics.
2. Reconcile heartbeat/connectivity snapshots and expose completeness.
3. Retain the exact `0.4` EMA filter, reject restart-zero epochs, and publish observed/age.
4. Fix API retry/cache age and address/signer identity.
5. Extend block histograms and classify checkpoint episodes.

### Phase 3 — EVM, replica, storage

1. Record EVM zero blocks/fees, parse outer time, detect null creations, and add head/receipt integrity.
2. Split contract address counters from metadata and make resolver chain-aware/opt-in.
3. Define bundles/actions/operations and bounded action schemas; expose parser completeness.
4. Add snapshot/checkpoint and RocksDB backlog/error metrics.

### Phase 4 — peer improvements

1. Add newest-only verified child state.
2. Rename/gate/clear dominant-inbound candidate and add concentration/confidence.
3. Replace overlapping RTT goroutines with bounded single-flight endpoint probes.
4. Add persistence only after chain/node scoping, synchronous load ordering, validation, TTL, and 0600 mode.
5. Keep unverified activity aggregate-only.

## Required regression test matrix

### Source reader invariants

- Existing file at exporter start does not replay historical counters.
- File created after exporter start is read from byte zero.
- Directory present but initially fileless follows the same post-start byte-zero rule.
- Old file is drained before hourly switch.
- Multiple rollovers during exporter downtime follow documented no-replay semantics.
- Torn final line is retained and emitted exactly once after completion.
- Valid-looking unterminated JSON is not committed.
- Truncation and same-path replacement reset safely.
- Oversized but permitted replica/EVM line parses; over-limit line reports a bounded error.
- Cancellation releases file/provider work and marks running=0.
- A monitored root created after exporter startup becomes active without restart; later withdrawal follows the declared stale policy.

### Consensus/status invariants

- `tc:null` contributes zero TC observations.
- Malformed, empty-object, empty-timeout, and populated TC cases update only their explicitly valid metrics.
- Duplicate `(round,hash)` block contributes once to block-level metrics.
- Latest-100 window contains unique blocks.
- Missing heartbeat count uses the active-unjailed denominator.
- Complete snapshot omission deletes old heartbeat/connectivity children.
- Partial/parse-failed snapshot never clears valid state or marks source success.
- Weighted proposer slots are not reported as unique validators.
- An all-zero EMA initialization epoch is not latency; exact `0.4` is filtered while real values above 0.4 seconds remain observable.
- The same status source cannot be simultaneously fresh under one path and stale under another.

### EVM/replica invariants

- Zero-tx block records zero in tx/block histogram.
- Zone-less nanosecond outer timestamp parses as UTC and preserves subsecond gaps.
- `to:null` counts as creation; EOA destination does not count as contract without proof.
- A zero-fee block resets current fee gauge and contributes zero to distribution.
- Tx/receipt mismatch increments an integrity metric.
- Malformed replica bundle/action increments parser/skipped counters and cannot become a complete zero-action block.
- Nested orders/cancels/modifies and selected wrapper/bulk arrays count their real elements.
- Unknown action types collapse to `other`.
- Null mempool payloads/values are rejected as semantically invalid rather than published as valid zero observations.
- Raw mempool outcomes collapse to a bounded status set, and `committed_tx_hashes` is never asserted to be pending depth.

### Peer/network invariants

- Multiport values aggregate per IP before ranking.
- `In`/`Out` remain traffic directions and never become connection origin.
- Zero/noise rows do not refresh “active” without a defined threshold.
- Top-N plus `other` exactly reconciles aggregate totals.
- One malformed TCP row invalidates the snapshot; it cannot delete labels as a complete reduced snapshot.
- Stable tie-break and hysteresis prevent flapping.
- Stale/empty/all-zero source clears dominant candidate after timeout.
- Validator role gate suppresses parent semantics.
- Explicit child processing uses only newest snapshot and deletes absent children.
- Raw incoming request/stream endpoints never enter persistent peer registry or labels.
- Probe cycle is single-flight, concurrency-bounded, deadline/cancellation-aware, endpoint-labeled, and clears stale success.
- Persistence loads before producers, validates schema/IP/time/chain/node, expires old entries, and never overwrites fresher memory.

### Health/cardinality invariants

- Every configured monitor is pre-registered before readiness can be true.
- Panic/return sets running=0 and records exit reason.
- `last_run` can advance without `last_source_success`; tests assert the distinction.
- A frozen streaming file does not refresh `last_source_success` or the monitor’s data-fresh tick.
- Disappeared source deletes owned series after the defined stale threshold.
- Label cross-products stay within explicit per-family budgets.
- No untrusted raw IP, hash, error text, user, order, or asset can become a default label.
- `--node-home` recomputes every dependent default path; `--evm-block-type-metrics` remains independent of `--evm`.
- Missing optional API fields and unexpected period labels cannot retain or create stale unbounded series.
- Concurrent cold first use of signer/validator caches is race-free and deterministic.
- Graceful OTLP shutdown flushes or explicitly reports failure within a bounded deadline.

## Confirmed non-findings

The audit explicitly rejected several tempting but incorrect criticisms:

- Block interval and apply-duration seconds-to-milliseconds conversions are correct.
- Heartbeat delay conversion to milliseconds is correct where applied.
- QC participation is intentionally 0–100 percent, not 0–1.
- Visor `consensus_time - wall_clock_time` sign is correct.
- Raw validator latency is correctly treated as seconds.
- Exact `0.4`-second EMA no-data values are correctly filtered without suppressing real latency above 400 ms.
- Current replica array operation counting is correct; the old comma heuristic is gone.
- The EVM wrapper does not itself hardcode the `Reth115` tag.
- Current and legacy status row shapes are supported.
- Numeric hour/height selection is correct.
- TCP raw values are documented as comparative rather than asserted bytes.
- TCP/LZ4 top-N plus `other` reconciliation is a good design.
- Accumulator `delta`, rollover drain, and torn-line handling are good.
- Mempool-txs has bounded action/TIF labels and correct array operation counting.
- Several upstream `_total` sources are intentionally gauges because they reset on node restart; the issue is naming/query clarity, not automatically the OTel instrument type.
- Every documented metric family is declared; `hl_core_blocks_processed_total` is the expected Prometheus translation of the OTel counter name.

## Empirical appendix

### Consensus and validator population

- Current status/stake rows: 96.
- Positive stake: 53.
- Positive and unjailed: 18.
- Positive and jailed: 35.
- Missing heartbeat: 77, all jailed.
- Seen heartbeat: 19, including one jailed/transitional validator.
- Current inbound Block events inspected: 47,293; source equaled proposer in every one.
- Recent duplicate Block events: 17 duplicates among 8,490 events / 8,473 unique rounds.

### Latency and block path

- Raw validator latency p50 about 2.88 ms, p95 about 10.60 ms, p99 about 19.91 ms.
- EMA source snapshots always contained 96 entries: either 19 observed plus 77 approximately 0.4-second sentinels, or 18 observed plus 78 sentinels; the exporter correctly filters the exact sentinel before publication.
- Two restart windows: all 96 EMA values zero for roughly three minutes.
- Fast block lag p50 about 159 ms, p95 about 236 ms, p99 about 261 ms.
- Slow block lag p50 about 161 ms, p95 about 241 ms, p99 about 267 ms.
- Slow path had 3,182 samples above four seconds in the multi-day paired scan, clustered around 10k checkpoint boundaries.

### TCP and protocol endpoints

- `tcp_traffic`: 6,205 rows over two days, 129 IPs, six ports, 94 endpoint-set variants.
- Latest: 29 IPs across 40 IP-port relationships.
- Every IP-port relationship carried both `In` and `Out` flow rows.
- Largest endpoint normally carried about 45% of row traffic; burst maximum approached 99.8%.
- Explicit child snapshots: normally one, max two, seven identities and 128 changes over 30 days.
- Gossip stream handles: 517,031 from 15,222 endpoints; only five greeting and four verified states.
- Consensus RPC incoming streams: 120,561 from 30,947 endpoints.
- Gossip RPC requests: 2,753 from 2,738 sources.

### EVM and replica

- EVM 24h: 85,843 blocks, 41,842 transactions, 53,407 zero-tx blocks (62.2147%), 80 `to:null` creations.
- Current-hour EVM sample had consecutive block numbers and approximately one-second cadence; receipts equaled transactions in the inspected sample.
- Replica recent sample: 6,328 blocks, 8,097 bundles, 28,617 signed actions, 15,205 order actions, 204,668 individual orders.
- Current maximum replica line: 63,825 bytes, close to the default 64 KiB scanner limit; the configured multi-megabyte path is necessary.

### Storage and lifecycle

- `hyperliquid_data` path-wise apparent bytes: about 106.9 GB.
- `hyperliquid_data` unique-inode apparent bytes: about 16.27 GB; this is not whole-node allocated storage.
- Separate `/home/ubuntu/hl/data` allocation: about 119.50 GB; `/home/ubuntu/hl/tmp`: about 1.32 GB.
- Current periodic state snapshot size: about 492 MB; usual height interval 10,000.
- Child stderr paths: 3,427; most nonempty entries were known configuration/sync/hardfork signatures, with only a small number of true distinct panics.
- Replay/session directories: 967 since 2025; retained population is not a restart rate.

## Final position

The exporter is already a strong breadth-oriented collector, but its next milestone should be **semantic trustworthiness**, not more metric count. First make every existing alert-grade metric answer “what population, what source, how fresh, what reset, and what cardinality?” Then add the highest-value missing metrics from sources that already exist.

For peer work specifically, keep our shared-snapshot aggregation and smoothing, borrow Dwellir’s tailer/scheduler/persistence mechanics selectively, and reject its identity and quality claims. The logs support verified-child state, bounded traffic concentration, protocol funnels, and TCP endpoint diagnostics. They do not support scanner-fed peer registries, guessed-port protocol reachability, or causal blocks-delivered/quality scores per inferred parent.
