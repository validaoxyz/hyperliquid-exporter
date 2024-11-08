# Changelog

### 🚨 Breaking Changes
- **[BREAKING]** Label names have changed:
  - `proposer` label renamed to `validator`
  - `address` label renamed to `validator`
  - Existing dashboards and alerts using these labels will need to be updated

### ✨ Added
- OpenTelemetry (OTLP) support enabling centralized metrics collection
  - Secure/insecure OTLP endpoint configuration
  - Chain (testnet/mainnet) identification
  - Node identity labeling for filtering on centralized collector
- 📊 New Metrics
  - EVM block height and transaction count monitoring
  - Active/inactive stake metrics for validator set analysis
  - Enhanced validator metrics with `signer` labels
- 🤖 Automatic Validator Detection
  - Self-identification of validator status
  - Automatic validator address detection
  - Dynamic label application based on node role

### 🔄 Changed
- Complete metrics system refactor to use OpenTelemetry
- Enhanced error handling and logging in monitors
- Improved metric documentation and examples
- More consistent label naming across all metrics

### 🗑️ Removed
- Direct Prometheus metric registration (now handled through OpenTelemetry)

### 📝 Notes
- Prometheus endpoint still available at `:8086/metrics` by default
- OTLP export is optional and disabled by default
- See updated README.md for new configuration options
