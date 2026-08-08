# Hyperliquid Exporter

A Prometheus exporter with optional OTLP for the families marked `OTel bridge` in the metrics reference. It reads bounded node files, streams, API responses, and Linux host state for HyperCore, HyperEVM, HyperBFT, network, and process metrics. See the [metrics reference](docs/metrics.md) for the generated inventory and interpretation limits.

## Install

```bash
git clone https://github.com/validaoxyz/hyperliquid-exporter.git "$HOME/hyperliquid-exporter"
cd "$HOME/hyperliquid-exporter"
make build
```

## Start

```bash
./bin/hl_exporter start --chain mainnet
```

Prometheus is always enabled. The default listener is `:8086`: metrics are at `/metrics`, liveness at `/livez`, and launch readiness at `/readyz`.

`/readyz` means every registered worker started. It does not mean node sources are present, readable, valid, fresh, or publishing. Use the `hl_exporter_monitor_*` and `hl_exporter_source_*` families for data health.

### Start options

The `Default` column is the executable FlagSet default. Empty path/URL values can be resolved later from environment or configuration as described in `-h`.

<!-- BEGIN START FLAG INVENTORY -->
| Flag | Type | Default | Purpose |
|---|---|---|---|
| `--alias` | string | `""` | Node alias; required for OTLP. |
| `--chain` | string | `""` | Required: `mainnet` or `testnet`. |
| `--contract-metrics` | bool | `false` | Enable capped canonical recipient-address diagnostics; no contract inference or enrichment. |
| `--contract-metrics-limit` | int | `20` | Keep at most N canonical recipient addresses, then use `address="other"`. |
| `--disable-tcp6` | bool | `false` | Disable `/proc/net/tcp6`; an unavailable enabled source is reported unhealthy. |
| `--evm-metrics` | bool | `false` | Enable HyperEVM stream metrics. |
| `--extended-metrics` | bool | `false` | Enable the extended file/host monitor bundle. |
| `--info-endpoint-url` | string | `""` | Probe URL; empty resolves to `http://127.0.0.1:3001/info`. |
| `--log-level` | string | `"info"` | `debug`, `info`, `warning`, or `error`. |
| `--metrics-port` | int | `8086` | Prometheus/health listener port. |
| `--node-binary` | string | `""` | Node binary override. |
| `--node-home` | string | `""` | Node home override; otherwise environment/default resolution applies. |
| `--otlp` | bool | `false` | Enable OTLP export. |
| `--otlp-endpoint` | string | `""` | OTLP endpoint; required with `--otlp`. |
| `--otlp-insecure` | bool | `false` | Use an insecure OTLP connection. |
| `--per-peer-metrics` | bool | `false` | Emit at most 16 current explicit child identities from fresh `child_peers` status. |
| `--pprof` | bool | `false` | Expose `/debug/pprof/` on the metrics listener. |
| `--probe-info-endpoint` | bool | `false` | Actively probe the node's `--serve-info` endpoint. |
| `--replica-metrics` | bool | `false` | Read validated replica block records, actions, operations, orders, responses, and parser outcomes. |
| `--skip-update-check` | bool | `false` | Skip the upstream visor update check. |
| `--skip-version-check` | bool | `false` | Skip the local `hl-node --version` probe. |
| `--tcp-service-ports` | string | `"3001,3999,4001,4002,4003,4004"` | Bounded service-port vocabulary, 1 to 16 entries. |
| `--validator-rtt` | bool | `false` | Enable outbound TCP-connect diagnostics for eligible validators; not protocol RTT. |
<!-- END START FLAG INVENTORY -->

Path precedence is explicit flag, environment, then fallback: node home uses `--node-home`, `NODE_HOME`, or `$HOME/hl`; the node binary uses `--node-binary`, `NODE_BINARY`, or `$BINARY_HOME/hl-node` (`$HOME/hl-node` when `BINARY_HOME` is unset). A local `.env` file can supply missing environment values.

Run `./bin/hl_exporter start -h` for executable help. Go renders flags with one dash in help; one- and two-dash forms are both accepted.

Useful profiles:

```bash
# Validated replica units and parser outcomes. The node must use its
# replica-cmds actions-and-responses mode.
./bin/hl_exporter start --chain mainnet --replica-metrics

# HyperEVM plus capped recipient-address diagnostics.
./bin/hl_exporter start --chain mainnet --evm-metrics \
  --contract-metrics --contract-metrics-limit 20

# Active info probe and bounded TCP-connect diagnostics.
./bin/hl_exporter start --chain mainnet --probe-info-endpoint --validator-rtt
```

The optional validator TCP-connect target set is not a complete validator-IP listing. It is limited to fresh API-active-and-unjailed validators with fresh local profile/IP evidence and the configured target cap.

## `vals` subcommand

`hl_exporter vals` reads the complete validator profile set from `data/periodic_abci_states` and emits `ip,moniker,address,vp` CSV. It does not use the bounded TCP-connect target set.

<!-- BEGIN VALS FLAG INVENTORY -->
| Flag | Type | Default | Purpose |
|---|---|---|---|
| `--addr` | string | `"0.0.0.0:8087"` | Listen address in serve mode. |
| `--backfill` | bool | `false` | Emit historical validator-count JSONL. |
| `--chain` | string | `"testnet"` | Route label used only in serve mode. |
| `--interval` | duration | `1h0m0s` | Serve-mode regeneration interval. |
| `--node-home` | string | environment-derived | `$NODE_HOME`, otherwise the current user's `~/hl`. |
| `--out` | string | `""` | Output file; empty writes stdout. |
| `--peer-counter-url` | string | `"http://127.0.0.1:19046/snapshot"` | Local peer-counter snapshot for `/nodes`. |
| `--serve` | bool | `false` | Serve CSV and flat-IP routes. |
| `--since` | string | `"2025-05-31"` | Backfill start date. |
| `--sleep` | duration | `2s` | Delay between backfill files. |
<!-- END VALS FLAG INVENTORY -->

```bash
# One-shot CSV to stdout, or add --out FILE.
./bin/hl_exporter vals --node-home /home/ubuntu/hl

# Serve /vals/<chain> and /nodes/<chain>.txt on :8087.
./bin/hl_exporter vals --serve --addr 0.0.0.0:8087 \
  --node-home /home/ubuntu/hl

# Historical validator-count rows as JSONL.
./bin/hl_exporter vals --backfill --since 2025-05-31 --out f.jsonl
```

In serve mode, `/vals/<chain>` comes from ABCI state. `/nodes/<chain>.txt` is a separate flat-IP view fetched from the local peer-counter snapshot endpoint. Configure it with `--peer-counter-url`; the default is `http://127.0.0.1:19046/snapshot`. A failed refresh retains the last good cached response.

Run `./bin/hl_exporter vals -h` for all 10 `vals` flags.

## systemd

The service needs read access to the node home. A minimal unit:

```ini
[Unit]
Description=Hyperliquid Prometheus Exporter
After=network.target

[Service]
WorkingDirectory=/opt/hyperliquid-exporter
ExecStart=/usr/local/bin/hl_exporter start --chain mainnet
Restart=always
RestartSec=10
User=hyperliquid
Group=hyperliquid

[Install]
WantedBy=multi-user.target
```

## Docker

Mount the Hyperliquid home read-only at the path configured for `--node-home`, then run:

```bash
docker compose up -d
```

Container deployments commonly use `--skip-version-check` when the node binary is not mounted and `--skip-update-check` when outbound update checks are disallowed.

## Documentation

- [Metrics reference](docs/metrics.md) — generated current inventory plus semantic boundaries
- [Upgrading](UPGRADING.md) — current breaking migration first; older migrations are archived
- [Changelog](CHANGELOG.md)
