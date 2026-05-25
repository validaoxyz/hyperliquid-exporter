# internal/monitors

One file per monitor. Each exposes

```go
func StartXMonitor(ctx context.Context, cfg config.Config, errCh chan<- error)
```

and is launched from `internal/exporter/exporter.go` via `runMonitor("x", func() { monitors.StartXMonitor(monitorCtx, cfg, xErrCh) })`. The `runMonitor` wrapper registers the monitor name with the health tracker, marks it started, recovers from panics, and counts recoveries on `hl_exporter_monitor_panics_total{monitor="x"}`.

## Conventions

- **Inner goroutines** must use `goSafe("x", func() { ... })` from `safego.go`. The outer `runMonitor` wrapper's recover only covers the brief setup phase; `StartXMonitor` typically returns after launching the work loop, so the loop itself needs separate panic protection.

- **Mark progress** via `metrics.MarkMonitorTick("x")` after each successful processing cycle. The exporter's `/readyz` endpoint and the `hl_exporter_monitor_last_tick_seconds` gauge use this.

- **Idle path** when the data source is missing (e.g. `node_logs/consensus/` on a non-validator node): log once, block on `<-ctx.Done()`, return. The monitor stays registered but does no work.

- **Cardinality**: any per-IP / per-peer / per-validator / per-anything label needs an explicit ceiling. We use three patterns:
  - **Allowlist** (`tokio_runtime_monitor`, `subsystem_latency_monitor`): hardcoded set; new values dropped silently.
  - **Top-N + "other"** (`tcp_traffic_monitor`, `tcp_lz4_monitor`): publish top-N, sum the rest, `Delete()` series that fall out.
  - **TTL eviction** (`consensus_monitor`): track last-seen, periodic housekeeping drops stale entries.

- **Cumulative-since-source-start counters** from hl-node reset on the source process restart. Model them as Prometheus gauges, not counters, to avoid regression warnings in `rate()`. See `crit_msg_monitor`, `tokio_runtime_monitor`, `rocksdb_monitor`.

- **Stream files seek to EOF on startup**. Restart means data loss for in-flight records. Documented limitation.

## File-discovery helpers

- `latestHourlyFile(root string)` in `visor_monitor.go`: walks `<root>/<YYYYMMDD>/<hour>`, picks the newest date dir and the numerically-highest hour. Use this for any monitor reading hl-node's nested `hourly/` layout — naive lex sort puts hour `10` before hour `2`.
- `latestDateFile(dir string)` in `subsystem_latency_monitor.go`: picks the lex-max filename in a flat `<YYYYMMDD>` directory.

## Tests

Parser logic gets a unit test with real production data as a fixture (see `tcp_traffic_monitor_test.go`, `crit_msg_monitor_test.go`, `rocksdb_monitor_test.go`). Pure filesystem walkers (sum sizes, count entries) don't need unit tests; stdlib coverage is sufficient.

`go test ./internal/monitors/` runs in under a second.
