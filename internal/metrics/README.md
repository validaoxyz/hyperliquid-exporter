
# Hyperliquid Exporter Metrics

Here's some information about each metric exposed by the Hyperliquid Exporter, along with sample Prometheus queries for analysis.

  

## Block Metrics

### hl_block_height
- Type: Gauge
- Description: The current block height of the Hyperliquid blockchain.
- Usage: This metric helps track the progression of the blockchain and can be used to monitor chain liveness.

#### Sample queries

```
#Block height increase rate (blocks per second) over the last hour

rate(hl_block_height[1h])
#Predict block height in 1 hour
hl_block_height + (rate(hl_block_height[6h]) 3600)
```

### hl_latest_block_time

- Type: Gauge
- Description: The Unix timestamp of the latest block.
- Usage: This metric is useful for monitoring chain liveness and calculating time-based statistics.

#### Sample queries:
```promql
# Time since the last block (in seconds)
time() - hl_latest_block_time

# Average block time over the last hour
rate(hl_latest_block_time[1h])

# Alert if no new block in 5 minutes
time() - hl_latest_block_time > 300
```

### hl_apply_duration_seconds

- Type: Histogram
- Description: Distribution of block apply durations in seconds.

Buckets:
Exponential buckets starting at 0.1ms (0.0001s), increasing by a factor of 1.5, for 20 buckets.

Additional buckets at: 0.1s, 0.15s, 0.2s, 0.3s, 0.5s, 1s, 2s, 5s, and 10s.

Usage: Provides insights into the performance of block application, which can help identify potential bottlenecks or performance issues.

#### Sample queries
```promql
# Median apply duration over the last 5 minutes
histogram_quantile(0.5, rate(hl_apply_duration_seconds_bucket[5m]))

# 90th percentile apply duration over the last 5 minutes
histogram_quantile(0.9, rate(hl_apply_duration_seconds_bucket[5m]))

# Percentage of apply durations under 10ms
sum(rate(hl_apply_duration_seconds_bucket{le="0.01"}[5m])) / sum(rate(hl_apply_duration_seconds_count[5m])) 100

# Average apply duration
rate(hl_apply_duration_seconds_sum[5m]) / rate(hl_apply_duration_seconds_count[5m])
```

### hl_proposer_count_total
- hl_proposer_count_total
- Type: Counter
- Description: The total number of blocks proposed by each validator address.
- Labels: `proposer` (validator address)

Usage: This metric helps monitor the distribution of block proposals among validators and can be used to detect potential issues with specific validators.

Sample Queries:

```promql
# Top 5 proposers by number of proposed blocks
topk(5, hl_proposer_count_total)

# Percentage of blocks proposed by each validator
hl_proposer_count_total / sum(hl_proposer_count_total) 100

# Proposal rate (blocks per minute) for each validator over the last hour
rate(hl_proposer_count_total[1h]) 60

# Alert if a validator hasn't proposed a block in the last 24 hours
changes(hl_proposer_count_total[24h]) == 0
```

### hl_software_version_info

- Type: Gauge
- Description: Information about the current software version running on the node.

Labels:
- `commit`: Git commit hash of the current version
- `date`: Build date of the current version

Usage: This metric helps track software versions across nodes and can be used to ensure all nodes are running the expected version.

Sample Queries:

```promql
# Check if all nodes are running the same version
count(count by (commit) (hl_software_version_info)) > 1

# List all unique versions currently running
count by (commit, date) (hl_software_version_info)
```

### Stake Status

#### hl_total_stake

-   Type: Gauge
-   Description: Total stake across all validators in the Hyperliquid network.
-   Usage: This metric provides an overview of the total stake in the network, which can be used to assess the overall security and economic value locked in the network.

##### Sample Queries
```promql
# Current total stake
hl_total_stake

# Percentage change in total stake over the last 24 hours
(hl_total_stake - hl_total_stake offset 24h) / hl_total_stake offset 24h * 100
```

####  hl_jailed_stake
-   Type: Gauge
-   Description: Total stake of jailed validators in the Hyperliquid network.
-   Usage: This metric helps monitor the amount of stake that is currently jailed, which can indicate network health and validator behavior.

##### Sample Queries
```promql
# Percentage of total stake that is jailed
hl_jailed_stake / hl_total_stake * 100

# Alert if jailed stake exceeds 10% of total stake
hl_jailed_stake / hl_total_stake > 0.1
```

#### hl_not_jailed_stake

-   Type: Gauge
-   Description: Total stake of not jailed validators in the Hyperliquid network.
-   Usage: This metric represents the active stake in the network, which is crucial for understanding the current voting power distribution.
Sample Queries:

```promql
# Ratio of not jailed stake to total stake
hl_not_jailed_stake / hl_total_stake

# Trend of not jailed stake over the last week
rate(hl_not_jailed_stake[1w])
```


#### hl_validator_stake

-   Type: Gauge
-   Description: Individual stake of each validator in the Hyperliquid network.
-   Labels: validator (validator address)
-   Usage: This metric allows for tracking the stake of individual validators, which is useful for monitoring validator performance and network decentralization.

##### Sample Queries
```
# Top 10 validators by stake
topk(10, hl_validator_stake)

# Validators with less than 1% of total stake
hl_validator_stake / ignoring(validator) group_left() hl_total_stake < 0.01

# Stake concentration: percentage of stake held by top 5 validators
sum(topk(5, hl_validator_stake)) / hl_total_stake * 100
```

### hl_validator_jailed_status

-   Type: Gauge
-   Description: Jailed status of each validator (1 if jailed, 0 if not jailed).
-   Labels: `validator` (validator address)
-   Usage: This metric helps monitor the jailed status of validators, which is crucial for understanding network health and validator behavior.

#### Sample Queries
```promql
# Count of jailed validators
sum(hl_validator_jailed_status == 1)

# List of jailed validators
hl_validator_jailed_status == 1

# Alert if a top 10 validator by stake is jailed
hl_validator_jailed_status{validator=~"$top_10_validators"} == 1
```



### hl_validator_count

-   Type: Gauge
-   Description: Total number of validators in the Hyperliquid network.
-   Usage: This metric provides insight into the size and decentralization of the validator set.

#### Sample Queries
```
# Current validator count
hl_validator_count

# Change in validator count over the last 24 hours
hl_validator_count - hl_validator_count offset 24h

# Alert if validator count drops below a threshold
hl_validator_count < 50
```

These additional metrics provide a comprehensive view of the stake distribution, validator status, and overall network composition in the Hyperliquid blockchain. They can be used to monitor network health, track validator performance, and ensure the network remains decentralized and secure.
