package metrics

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/validaoxyz/hyperliquid-exporter/internal/logger"
	"go.opentelemetry.io/otel/attribute"
	api "go.opentelemetry.io/otel/metric"
)

var (
	// shared context to avoid allocations
	sharedCtx = context.Background()

	// cache for commonly used attributes to reduce allocations
	attrCache   = make(map[string][]api.AddOption)
	attrCacheMu sync.RWMutex

	// preallocated attribute options for common action types
	cachedActionAttrs = make(map[string][]api.AddOption)
	cachedOpAttrs     = make(map[string][]api.AddOption)
	attrInitOnce      sync.Once
)

// initializes commonly used attributes
func initAttributeCache() {
	attrInitOnce.Do(func() {
		// pre create attributes for common action types to avoid allocations
		commonActionTypes := []string{
			"order", "cancel", "update", "batchModify", "transfer",
			"liquidate", "settle", "claim", "delegate", "withdraw",
			"deposit", "stake", "unstake", "vote", "other",
		}

		for _, actionType := range commonActionTypes {
			cachedActionAttrs[actionType] = []api.AddOption{
				api.WithAttributes(attribute.String("type", actionType)),
			}

			// pre create operation attributes for common categories
			for _, category := range []string{"order", "cancel", "update", "transfer", "other"} {
				key := actionType + ":" + category
				cachedOpAttrs[key] = []api.AddOption{
					api.WithAttributes(
						attribute.String("type", actionType),
						attribute.String("category", category),
					),
				}
			}
		}
	})
}

// returns cached attributes or creates new ones
func getOrCreateActionAttrs(actionType string) []api.AddOption {
	initAttributeCache()

	// try to get from preallocated cache first
	if attrs, ok := cachedActionAttrs[actionType]; ok {
		return attrs
	}

	// check dynamic cache
	attrCacheMu.RLock()
	if attrs, ok := attrCache[actionType]; ok {
		attrCacheMu.RUnlock()
		return attrs
	}
	attrCacheMu.RUnlock()

	// create new attributes and cache them
	attrs := []api.AddOption{
		api.WithAttributes(attribute.String("type", actionType)),
	}

	attrCacheMu.Lock()
	attrCache[actionType] = attrs
	attrCacheMu.Unlock()

	return attrs
}

// returns cached operation attributes or creates new
func getOrCreateOpAttrs(actionType, category string) []api.AddOption {
	initAttributeCache()

	key := actionType + ":" + category

	// try preallocated cache first
	if attrs, ok := cachedOpAttrs[key]; ok {
		return attrs
	}

	// check dynamic cache
	attrCacheMu.RLock()
	if attrs, ok := attrCache[key]; ok {
		attrCacheMu.RUnlock()
		return attrs
	}
	attrCacheMu.RUnlock()

	// create new attributes and cache them
	attrs := []api.AddOption{
		api.WithAttributes(
			attribute.String("type", actionType),
			attribute.String("category", category),
		),
	}

	attrCacheMu.Lock()
	attrCache[key] = attrs
	attrCacheMu.Unlock()

	return attrs
}

func IncrementProposerCounter(proposer string) {
	// the proposer field from replica_cmds contains the signer address
	signer := proposer
	validator := signer // default to signer if no mapping

	// look up the val addr for this signer
	if mapped, ok := GetValidatorForSigner(strings.ToLower(signer)); ok {
		validator = mapped
		logger.Debug("Mapped signer %s to validator %s", signer, validator)
	} else {
		logger.Debug("No mapping found for signer %s, using signer as validator", signer)
	}

	// get the val moniker
	name := GetValidatorName(validator)
	if name == "" {
		// if we can't find a name, try with the original case
		name = GetValidatorName(strings.ToLower(validator))
	}

	// now register metric atomically
	metricsMutex.Lock()
	defer metricsMutex.Unlock()

	ctx := context.Background()
	labels := []attribute.KeyValue{
		attribute.String("validator", validator),
		attribute.String("signer", signer),
	}

	// add name label if we found one
	if name != "" {
		labels = append(labels, attribute.String("name", name))
	}

	HLConsensusProposerCounter.Add(ctx, 1, api.WithAttributes(labels...))
}

func SetBlockHeight(height int64) {
	metricsMutex.Lock()
	defer metricsMutex.Unlock()
	currentValues[HLCoreBlockHeightGauge] = height
}

func RecordBlockTime(duration float64) {
	ctx := context.Background()
	commonLabels := getCommonLabels()

	if HLCoreBlockTimeHistogram != nil {
		HLCoreBlockTimeHistogram.Record(ctx, duration, api.WithAttributes(commonLabels...))
	}
}

func RecordBlockTimeWithLabel(duration float64, stateType string) {
	ctx := context.Background()
	commonLabels := getCommonLabels()

	// add state_type label to common labels
	labels := append(commonLabels, attribute.String("state_type", stateType))

	if HLCoreBlockTimeHistogram != nil {
		HLCoreBlockTimeHistogram.Record(ctx, duration, api.WithAttributes(labels...))
	}
}

func RecordApplyDuration(duration float64) {
	metricsMutex.Lock()
	defer metricsMutex.Unlock()
	ctx := context.Background()
	commonLabels := getCommonLabels()

	if HLMetalApplyDurationHistogram != nil {
		HLMetalApplyDurationHistogram.Record(ctx, duration, api.WithAttributes(commonLabels...))
	}

	currentValues[HLMetalApplyDurationGauge] = duration
}

func RecordApplyDurationWithLabel(duration float64, stateType string) {
	metricsMutex.Lock()
	defer metricsMutex.Unlock()
	ctx := context.Background()
	commonLabels := getCommonLabels()

	// add state_type label to common labels
	labels := append(commonLabels, attribute.String("state_type", stateType))

	if HLMetalApplyDurationHistogram != nil {
		HLMetalApplyDurationHistogram.Record(ctx, duration, api.WithAttributes(labels...))
	}

	// update gauge with label
	if _, exists := labeledValues[HLMetalApplyDurationGauge]; !exists {
		labeledValues[HLMetalApplyDurationGauge] = make(map[string]labeledValue)
	}
	labeledValues[HLMetalApplyDurationGauge][stateType] = labeledValue{
		value:  duration,
		labels: []attribute.KeyValue{attribute.String("state_type", stateType)},
	}
}

func SetValidatorStake(address, signer, moniker string, stake float64) {
	metricsMutex.Lock()
	defer metricsMutex.Unlock()
	if _, exists := labeledValues[HLConsensusValidatorStakeGauge]; !exists {
		labeledValues[HLConsensusValidatorStakeGauge] = make(map[string]labeledValue)
	}
	labeledValues[HLConsensusValidatorStakeGauge][address] = labeledValue{
		value: stake,
		labels: []attribute.KeyValue{
			attribute.String("validator", address),
			attribute.String("signer", signer),
			attribute.String("moniker", moniker),
		},
	}
}

func SetValidatorJailedStatus(validator, signer, name string, status float64) {
	metricsMutex.Lock()
	defer metricsMutex.Unlock()

	labels := append([]attribute.KeyValue{
		attribute.String("validator", validator),
		attribute.String("signer", signer),
		attribute.String("name", name),
	}, getCommonLabels()...)

	if _, exists := labeledValues[HLConsensusValidatorJailedStatus]; !exists {
		labeledValues[HLConsensusValidatorJailedStatus] = make(map[string]labeledValue)
	}

	labeledValues[HLConsensusValidatorJailedStatus][validator] = labeledValue{
		value:  status,
		labels: labels,
	}
}

func SetTotalStake(stake float64) {
	metricsMutex.Lock()
	defer metricsMutex.Unlock()
	currentValues[HLConsensusTotalStakeGauge] = stake
}

func SetJailedStake(stake float64) {
	metricsMutex.Lock()
	defer metricsMutex.Unlock()
	currentValues[HLConsensusJailedStakeGauge] = stake
}

func SetNotJailedStake(stake float64) {
	metricsMutex.Lock()
	defer metricsMutex.Unlock()
	currentValues[HLConsensusNotJailedStakeGauge] = stake
}

func SetValidatorCount(count int64) {
	metricsMutex.Lock()
	defer metricsMutex.Unlock()
	currentValues[HLConsensusValidatorCountGauge] = count
}

func SetSoftwareVersion(commit string, date string) {
	metricsMutex.Lock()
	defer metricsMutex.Unlock()
	if _, exists := labeledValues[HLSoftwareVersionInfo]; !exists {
		labeledValues[HLSoftwareVersionInfo] = make(map[string]labeledValue)
	}
	labeledValues[HLSoftwareVersionInfo]["current"] = labeledValue{
		value: 1,
		labels: []attribute.KeyValue{
			attribute.String("date", date),
			attribute.String("commit", commit),
		},
	}
}
func SetSoftwareUpToDate(upToDate bool) {
	metricsMutex.Lock()
	defer metricsMutex.Unlock()
	if upToDate {
		currentValues[HLSoftwareUpToDate] = int64(1)
	} else {
		currentValues[HLSoftwareUpToDate] = int64(0)
	}
}

func SetLatestBlockTime(timestamp int64) {
	metricsMutex.Lock()
	defer metricsMutex.Unlock()
	currentValues[HLCoreLatestBlockTimeGauge] = timestamp
}

func SetEVMBlockHeight(height int64) {
	metricsMutex.Lock()
	defer metricsMutex.Unlock()
	logger.Debug("Setting EVM block height to: %d", height)
	currentValues[HLEVMBlockHeightGauge] = height
}

func SetIsValidator(isValidator bool) {
	metricsMutex.Lock()
	defer metricsMutex.Unlock()
	nodeIdentity.IsValidator = isValidator
}

func SetValidatorAddress(address string) {
	metricsMutex.Lock()
	defer metricsMutex.Unlock()
	nodeIdentity.ValidatorAddress = address
}

func SetActiveStake(stake float64) {
	metricsMutex.Lock()
	defer metricsMutex.Unlock()
	currentValues[HLConsensusActiveStakeGauge] = stake
}

func SetInactiveStake(stake float64) {
	metricsMutex.Lock()
	defer metricsMutex.Unlock()
	currentValues[HLConsensusInactiveStakeGauge] = stake
}

func SetValidatorActiveStatus(validator, signer, name string, status float64) {
	metricsMutex.Lock()
	defer metricsMutex.Unlock()

	labels := append([]attribute.KeyValue{
		attribute.String("validator", validator),
		attribute.String("signer", signer),
		attribute.String("name", name),
	}, getCommonLabels()...)

	if _, exists := labeledValues[HLConsensusValidatorActiveStatus]; !exists {
		labeledValues[HLConsensusValidatorActiveStatus] = make(map[string]labeledValue)
	}

	labeledValues[HLConsensusValidatorActiveStatus][validator] = labeledValue{
		value:  status,
		labels: labels,
	}
}

func SetValidatorRTT(validator string, moniker string, ip string, latency float64) {
	metricsMutex.Lock()
	defer metricsMutex.Unlock()

	labels := append([]attribute.KeyValue{
		attribute.String("validator", validator),
		attribute.String("moniker", moniker),
		attribute.String("ip", ip),
	}, getCommonLabels()...)

	if _, exists := labeledValues[HLConsensusValidatorRTTGauge]; !exists {
		labeledValues[HLConsensusValidatorRTTGauge] = make(map[string]labeledValue)
	}

	labeledValues[HLConsensusValidatorRTTGauge][validator] = labeledValue{
		value:  latency,
		labels: labels,
	}
}

func RecordEVMBlockTime(duration float64) {
	ctx := context.Background()
	commonLabels := getCommonLabels()

	if HLEVMBlockTimeHistogram != nil {
		HLEVMBlockTimeHistogram.Record(ctx, duration, api.WithAttributes(commonLabels...))
	}
}

func SetEVMLatestBlockTime(timestamp int64) {
	metricsMutex.Lock()
	defer metricsMutex.Unlock()
	currentValues[HLEVMLatestBlockTimeGauge] = timestamp
}

func SetEVMBaseFeeGwei(fee float64, blockType ...string) {
	metricsMutex.Lock()
	defer metricsMutex.Unlock()

	// record in histogram (always, regardless of block type)
	ctx := context.Background()
	if HLEVMBaseFeeHistogram != nil {
		if len(blockType) == 0 {
			HLEVMBaseFeeHistogram.Record(ctx, fee)
		} else {
			HLEVMBaseFeeHistogram.Record(ctx, fee, api.WithAttributes(attribute.String("block_type", blockType[0])))
		}
	}

	// update gauge
	if len(blockType) == 0 {
		currentValues[HLEVMBaseFeeGauge] = fee
	} else {
		bt := blockType[0]
		if _, exists := labeledValues[HLEVMBaseFeeGauge]; !exists {
			labeledValues[HLEVMBaseFeeGauge] = make(map[string]labeledValue)
		}
		labeledValues[HLEVMBaseFeeGauge][bt] = labeledValue{
			value:  fee,
			labels: []attribute.KeyValue{attribute.String("block_type", bt)},
		}
	}
}

func SetEVMGasUsage(gasUsed, gasLimit int64, blockType ...string) {
	metricsMutex.Lock()
	defer metricsMutex.Unlock()

	// if no block type specified, update the global metrics (backward compatible)
	if len(blockType) == 0 {
		currentValues[HLEVMGasUsedGauge] = float64(gasUsed)
		currentValues[HLEVMGasLimitGauge] = float64(gasLimit)
		if gasLimit > 0 {
			util := float64(gasUsed) / float64(gasLimit)
			currentValues[HLEVMSGasUtilGauge] = util
		}
	} else {
		// update labeled metrics
		bt := blockType[0]

		// update gas used with label
		if _, exists := labeledValues[HLEVMGasUsedGauge]; !exists {
			labeledValues[HLEVMGasUsedGauge] = make(map[string]labeledValue)
		}
		labeledValues[HLEVMGasUsedGauge][bt] = labeledValue{
			value:  float64(gasUsed),
			labels: []attribute.KeyValue{attribute.String("block_type", bt)},
		}

		// update gas limit with label
		if _, exists := labeledValues[HLEVMGasLimitGauge]; !exists {
			labeledValues[HLEVMGasLimitGauge] = make(map[string]labeledValue)
		}
		labeledValues[HLEVMGasLimitGauge][bt] = labeledValue{
			value:  float64(gasLimit),
			labels: []attribute.KeyValue{attribute.String("block_type", bt)},
		}

		// update gas utilization with label
		if gasLimit > 0 {
			util := float64(gasUsed) / float64(gasLimit)
			if _, exists := labeledValues[HLEVMSGasUtilGauge]; !exists {
				labeledValues[HLEVMSGasUtilGauge] = make(map[string]labeledValue)
			}
			labeledValues[HLEVMSGasUtilGauge][bt] = labeledValue{
				value:  util,
				labels: []attribute.KeyValue{attribute.String("block_type", bt)},
			}
		}
	}
}

func RecordEVMTxPerBlock(count int, blockType ...string) {
	ctx := context.Background()
	if HLEVMTxPerBlockHistogram != nil {
		if len(blockType) == 0 {
			HLEVMTxPerBlockHistogram.Record(ctx, float64(count))
		} else {
			HLEVMTxPerBlockHistogram.Record(ctx, float64(count), api.WithAttributes(attribute.String("block_type", blockType[0])))
		}
	}
}

func IncrementEVMTxType(txType string, blockType ...string) {
	ctx := context.Background()
	if len(blockType) == 0 {
		HLEVMTxTypeCounter.Add(ctx, 1, api.WithAttributes(attribute.String("type", txType)))
	} else {
		HLEVMTxTypeCounter.Add(ctx, 1, api.WithAttributes(
			attribute.String("type", txType),
			attribute.String("block_type", blockType[0]),
		))
	}
}

func IncrementEVMContractCreations(blockType ...string) {
	ctx := context.Background()
	if len(blockType) == 0 {
		HLEVMContractCreateCounter.Add(ctx, 1)
	} else {
		HLEVMContractCreateCounter.Add(ctx, 1, api.WithAttributes(attribute.String("block_type", blockType[0])))
	}
}

func SetEVMMaxPriorityFeeGwei(fee float64, blockType ...string) {
	metricsMutex.Lock()
	defer metricsMutex.Unlock()

	// record in histogram (always regardless of block type)
	ctx := context.Background()
	if HLEVMPriorityFeeHistogram != nil {
		if len(blockType) == 0 {
			HLEVMPriorityFeeHistogram.Record(ctx, fee)
		} else {
			HLEVMPriorityFeeHistogram.Record(ctx, fee, api.WithAttributes(attribute.String("block_type", blockType[0])))
		}
	}

	// update gauge
	if len(blockType) == 0 {
		currentValues[HLEVMMaxPriorityFeeGauge] = fee
	} else {
		bt := blockType[0]
		if _, exists := labeledValues[HLEVMMaxPriorityFeeGauge]; !exists {
			labeledValues[HLEVMMaxPriorityFeeGauge] = make(map[string]labeledValue)
		}
		labeledValues[HLEVMMaxPriorityFeeGauge][bt] = labeledValue{
			value:  fee,
			labels: []attribute.KeyValue{attribute.String("block_type", bt)},
		}
	}
}

func IncrementEVMContractTx(address, name string, isToken bool, tokenType, symbol string, blockType ...string) {
	ctx := context.Background()
	labels := []attribute.KeyValue{
		attribute.String("contract_address", address),
		attribute.String("contract_name", name),
		attribute.Bool("is_token", isToken),
		attribute.String("type", tokenType),
	}

	// only add symbol label if it's not empty ( for tokens)
	if symbol != "" {
		labels = append(labels, attribute.String("symbol", symbol))
	}

	// add block type label if provided
	if len(blockType) > 0 {
		labels = append(labels, attribute.String("block_type", blockType[0]))
	}

	HLEVMContractTxCounter.Add(ctx, 1, api.WithAttributes(labels...))
}

func SetEVMAccountCount(cnt int64) {
	metricsMutex.Lock()
	defer metricsMutex.Unlock()
	currentValues[HLEVMAccountCountGauge] = cnt
}

func IncrementTimeoutRounds(suspect string) {
	metricsMutex.Lock()
	defer metricsMutex.Unlock()

	ctx := context.Background()
	labels := []attribute.KeyValue{
		attribute.String("suspect", suspect),
	}
	HLTimeoutRoundsCounter.Add(ctx, 1, api.WithAttributes(labels...))
}

// Core (l1) metrics setters (from replica data)

func IncCoreTxTotal(actionType string, count int64) {
	if HLCoreTxCounter == nil {
		return
	}
	attrs := getOrCreateActionAttrs(actionType)
	HLCoreTxCounter.Add(sharedCtx, count, attrs...)
}

func IncCoreOrdersTotal(count int64) {
	if HLCoreOrdersCounter == nil {
		return
	}
	HLCoreOrdersCounter.Add(sharedCtx, count)
}

func IncCoreOperationsTotal(actionType string, category string, count int64) {
	if HLCoreOperationsCounter == nil {
		return
	}
	attrs := getOrCreateOpAttrs(actionType, category)
	HLCoreOperationsCounter.Add(sharedCtx, count, attrs...)
}

func ObserveCoreOperationsPerBlock(count float64) {
	if HLCoreOperationsPerBlockHistogram != nil {
		HLCoreOperationsPerBlockHistogram.Record(sharedCtx, count)
	}
}

func IncCoreBlocksProcessed() {
	HLCoreBlocksProcessedCounter.Add(sharedCtx, 1)
}

func IncCoreRoundsProcessed() {
	HLCoreRoundsProcessedCounter.Add(sharedCtx, 1)
}

func ObserveCoreTxPerBlock(count float64) {
	if HLCoreTxPerBlockHistogram != nil {
		HLCoreTxPerBlockHistogram.Record(sharedCtx, count)
	}
}

func SetCoreLastProcessedRound(round float64) {
	metricsMutex.Lock()
	defer metricsMutex.Unlock()
	currentValues[HLCoreLastProcessedRound] = int64(round)
}

func SetCoreLastProcessedTime(timestamp float64) {
	metricsMutex.Lock()
	defer metricsMutex.Unlock()
	currentValues[HLCoreLastProcessedTime] = int64(timestamp)
}

// implementation specific metrics

func SetReplicaParseDuration(duration float64) {
	metricsMutex.Lock()
	defer metricsMutex.Unlock()
	currentValues[HLMetalParseDurationGauge] = duration
}

func SetReplicaLastProcessedRound(round float64) {
	metricsMutex.Lock()
	defer metricsMutex.Unlock()
	currentValues[HLMetalLastProcessedRound] = int64(round)
}

func SetReplicaLastProcessedTime(timestamp float64) {
	metricsMutex.Lock()
	defer metricsMutex.Unlock()
	currentValues[HLMetalLastProcessedTime] = int64(timestamp)
}

// high gas limit block tracking setters

func IncrementHighGasLimitBlocks(threshold string) {
	ctx := context.Background()
	HLEVMHighGasLimitBlocksCounter.Add(ctx, 1, api.WithAttributes(attribute.String("threshold", threshold)))
}

func RecordGasLimitDistribution(gasLimit float64) {
	ctx := context.Background()
	if HLEVMGasLimitHistogram != nil {
		HLEVMGasLimitHistogram.Record(ctx, gasLimit)
	}
}

func SetLastHighGasBlock(height, limit, used int64, timestamp time.Time) {
	metricsMutex.Lock()
	defer metricsMutex.Unlock()
	currentValues[HLEVMLastHighGasBlockHeight] = height
	currentValues[HLEVMLastHighGasBlockLimit] = limit
	currentValues[HLEVMLastHighGasBlockUsed] = used
	currentValues[HLEVMLastHighGasBlockTime] = timestamp.Unix()
}

func UpdateMaxGasLimit(gasLimit int64) {
	metricsMutex.Lock()
	defer metricsMutex.Unlock()

	// update max if this is higher than what we've seen
	if current, exists := currentValues[HLEVMMaxGasLimitSeen]; !exists || gasLimit > current.(int64) {
		currentValues[HLEVMMaxGasLimitSeen] = gasLimit
	}
}

// consensus monitoring setters

func SetValidatorLastVoteRound(validator string, round int64) {
	labels := getValidatorLabels(validator)

	// extract the actual validator address from the labels for map key
	var validatorAddr string
	for _, label := range labels {
		if label.Key == "validator" {
			validatorAddr = label.Value.AsString()
			break
		}
	}

	metricsMutex.Lock()
	defer metricsMutex.Unlock()

	if _, exists := labeledValues[HLConsensusVoteRoundGauge]; !exists {
		labeledValues[HLConsensusVoteRoundGauge] = make(map[string]labeledValue)
	}
	labeledValues[HLConsensusVoteRoundGauge][validatorAddr] = labeledValue{
		value:  float64(round),
		labels: labels,
	}
}

func SetValidatorVoteTimeDiff(validator string, seconds float64) {
	labels := getValidatorLabels(validator)

	// extract the actual validator address from the labels for map key
	var validatorAddr string
	for _, label := range labels {
		if label.Key == "validator" {
			validatorAddr = label.Value.AsString()
			break
		}
	}

	metricsMutex.Lock()
	defer metricsMutex.Unlock()

	if _, exists := labeledValues[HLConsensusVoteTimeDiffGauge]; !exists {
		labeledValues[HLConsensusVoteTimeDiffGauge] = make(map[string]labeledValue)
	}
	labeledValues[HLConsensusVoteTimeDiffGauge][validatorAddr] = labeledValue{
		value:  seconds,
		labels: labels,
	}
}

func SetCurrentConsensusRound(round int64) {
	metricsMutex.Lock()
	defer metricsMutex.Unlock()
	currentValues[HLConsensusCurrentRoundGauge] = round
}

// returns expanded validator, signer, and name labels for a validator or signer address
func getValidatorLabels(addressInput string) []attribute.KeyValue {
	// first expand the address if it's truncated
	addressInput = ExpandAddress(addressInput)

	// check if this is a signer address (0x-prefixed) that needs to be mapped to validator
	var validator, signer, name string
	var exists bool

	if strings.HasPrefix(strings.ToLower(addressInput), "0x") {
		// this is likely a signer address, try to map it to validator
		if mappedValidator, signerExists := GetValidatorForSigner(strings.ToLower(addressInput)); signerExists {
			// we found the validator for this signer
			validator = mappedValidator
			signer, name, exists = GetValidatorInfo(validator)
			if !exists {
				// fallback: we have the mapping but not the full info
				signer = addressInput
				name = "unknown"
			}
		} else {
			// no mapping found, use the signer address for all fields
			return []attribute.KeyValue{
				attribute.String("validator", addressInput),
				attribute.String("signer", addressInput),
				attribute.String("name", "unknown"),
			}
		}
	} else {
		validator = addressInput
		signer, name, exists = GetValidatorInfo(validator)
		if !exists {
			// if we don't have info, use validator address for all fields
			return []attribute.KeyValue{
				attribute.String("validator", validator),
				attribute.String("signer", validator),
				attribute.String("name", "unknown"),
			}
		}
	}

	return []attribute.KeyValue{
		attribute.String("validator", validator),
		attribute.String("signer", signer),
		attribute.String("name", name),
	}
}

func IncrementHeartbeatsSent(validator string) {
	ctx := context.Background()
	labels := getValidatorLabels(validator)
	HLConsensusHeartbeatSentCounter.Add(ctx, 1, api.WithAttributes(labels...))
}

func IncrementHeartbeatAcksReceived(fromValidator, toValidator string) {
	// get proper labels for both validators
	fromLabels := getValidatorLabels(fromValidator)
	toLabels := getValidatorLabels(toValidator)

	// extract the actual validator addresses from the labels
	var fromValidatorAddr, toValidatorAddr string
	var fromName, toName string
	for _, label := range fromLabels {
		if label.Key == "validator" {
			fromValidatorAddr = label.Value.AsString()
		} else if label.Key == "name" {
			fromName = label.Value.AsString()
		}
	}
	for _, label := range toLabels {
		if label.Key == "validator" {
			toValidatorAddr = label.Value.AsString()
		} else if label.Key == "name" {
			toName = label.Value.AsString()
		}
	}

	ctx := context.Background()
	HLConsensusHeartbeatAckCounter.Add(ctx, 1, api.WithAttributes(
		attribute.String("from_validator", fromValidatorAddr),
		attribute.String("to_validator", toValidatorAddr),
		attribute.String("from_name", fromName),
		attribute.String("to_name", toName),
	))
}

func RecordHeartbeatAckDelay(fromValidator, toValidator string, delayMs float64) {
	// expand truncated addresses if needed (even though not used in labels)
	fromValidator = ExpandAddress(fromValidator)
	toValidator = ExpandAddress(toValidator)

	ctx := context.Background()
	if HLConsensusHeartbeatDelayHist != nil {
		// record without labels to reduce cardinality
		// the histogram tracks the distribution of all heartbeat delays across the network
		HLConsensusHeartbeatDelayHist.Record(ctx, delayMs)
	}
}

func SetValidatorConnectivity(validator, peer string, connected float64) {
	// get proper labels for both addresses
	validatorLabels := getValidatorLabels(validator)
	peerLabels := getValidatorLabels(peer)

	// extract the actual validator addresses and names from the labels
	var validatorAddr, peerAddr string
	var validatorName, peerName string
	for _, label := range validatorLabels {
		if label.Key == "validator" {
			validatorAddr = label.Value.AsString()
		} else if label.Key == "name" {
			validatorName = label.Value.AsString()
		}
	}
	for _, label := range peerLabels {
		if label.Key == "validator" {
			peerAddr = label.Value.AsString()
		} else if label.Key == "name" {
			peerName = label.Value.AsString()
		}
	}

	metricsMutex.Lock()
	defer metricsMutex.Unlock()

	if _, exists := labeledValues[HLConsensusConnectivityGauge]; !exists {
		labeledValues[HLConsensusConnectivityGauge] = make(map[string]labeledValue)
	}
	// use combined key for validator-peer pairs
	key := validatorAddr + "_" + peerAddr
	labeledValues[HLConsensusConnectivityGauge][key] = labeledValue{
		value: connected,
		labels: []attribute.KeyValue{
			attribute.String("validator", validatorAddr),
			attribute.String("peer", peerAddr),
			attribute.String("validator_name", validatorName),
			attribute.String("peer_name", peerName),
		},
	}
}

func RemoveValidatorConnectivity(validator, peer string) {
	// get proper labels to extract the actual validator addresses
	validatorLabels := getValidatorLabels(validator)
	peerLabels := getValidatorLabels(peer)

	// extract the actual validator addresses from the labels
	var validatorAddr, peerAddr string
	for _, label := range validatorLabels {
		if label.Key == "validator" {
			validatorAddr = label.Value.AsString()
			break
		}
	}
	for _, label := range peerLabels {
		if label.Key == "validator" {
			peerAddr = label.Value.AsString()
			break
		}
	}

	metricsMutex.Lock()
	defer metricsMutex.Unlock()

	if _, exists := labeledValues[HLConsensusConnectivityGauge]; exists {
		// use combined key for validator-peer pairs
		key := validatorAddr + "_" + peerAddr
		delete(labeledValues[HLConsensusConnectivityGauge], key)
	}
}

func SetValidatorHeartbeatStatus(validator, statusType string, value float64) {
	// get proper labels for the validator
	labels := getValidatorLabels(validator)

	// extract the actual validator address from the labels
	var validatorAddr string
	for _, label := range labels {
		if label.Key == "validator" {
			validatorAddr = label.Value.AsString()
			break
		}
	}

	// add status_type to labels
	fullLabels := append(labels, attribute.String("status_type", statusType))

	metricsMutex.Lock()
	defer metricsMutex.Unlock()

	if _, exists := labeledValues[HLConsensusHeartbeatStatusGauge]; !exists {
		labeledValues[HLConsensusHeartbeatStatusGauge] = make(map[string]labeledValue)
	}
	// use combined key for validator-status pairs
	key := validatorAddr + "_" + statusType
	labeledValues[HLConsensusHeartbeatStatusGauge][key] = labeledValue{
		value:  value,
		labels: fullLabels,
	}
}

// QC and TC metric setters

func IncrementQCSignatures(validator string) {
	ctx := context.Background()
	// use getValidatorLabels to get proper expanded addresses and signer/validator mapping
	labels := getValidatorLabels(validator)
	HLConsensusQCSignaturesCounter.Add(ctx, 1, api.WithAttributes(labels...))
}

func SetQCParticipationRate(validator string, rate float64) {
	// use getValidatorLabels to get proper expanded addresses and signer/validator mapping
	labels := getValidatorLabels(validator)

	// extract the actual validator address from the labels for map key
	var validatorAddr string
	for _, label := range labels {
		if label.Key == "validator" {
			validatorAddr = label.Value.AsString()
			break
		}
	}

	metricsMutex.Lock()
	defer metricsMutex.Unlock()

	if _, exists := labeledValues[HLConsensusQCParticipationGauge]; !exists {
		labeledValues[HLConsensusQCParticipationGauge] = make(map[string]labeledValue)
	}
	labeledValues[HLConsensusQCParticipationGauge][validatorAddr] = labeledValue{
		value:  rate,
		labels: labels,
	}
}

func RecordQCSize(size float64) {
	ctx := context.Background()
	if HLConsensusQCSizeHist != nil {
		HLConsensusQCSizeHist.Record(ctx, size)
	}
}

func IncrementTCBlocks(proposer string) {
	ctx := context.Background()
	// use getValidatorLabels to get proper expanded addresses and signer/validator mapping
	labels := getValidatorLabels(proposer)
	HLConsensusTCBlocksCounter.Add(ctx, 1, api.WithAttributes(labels...))
}

func IncrementTCParticipation(validator string) {
	ctx := context.Background()
	// use getValidatorLabels to get proper expanded addresses and signer/validator mapping
	labels := getValidatorLabels(validator)
	HLConsensusTCParticipationCounter.Add(ctx, 1, api.WithAttributes(labels...))
}

func RecordTCSize(size float64) {
	ctx := context.Background()
	if HLConsensusTCSizeHist != nil {
		HLConsensusTCSizeHist.Record(ctx, size)
	}
}

func SetRoundsPerBlock(avg float64) {
	metricsMutex.Lock()
	defer metricsMutex.Unlock()
	currentValues[HLConsensusRoundsPerBlockGauge] = avg
}

func SetQCRoundLag(avg float64) {
	metricsMutex.Lock()
	defer metricsMutex.Unlock()
	currentValues[HLConsensusQCRoundLagGauge] = avg
}

// monitor health metrics
func SetConsensusMonitorLastProcessed(monitorType string, timestamp int64) {
	labels := []attribute.KeyValue{
		attribute.String("monitor_type", monitorType),
	}

	metricsMutex.Lock()
	defer metricsMutex.Unlock()

	if _, exists := labeledValues[HLConsensusMonitorLastProcessedGauge]; !exists {
		labeledValues[HLConsensusMonitorLastProcessedGauge] = make(map[string]labeledValue)
	}
	labeledValues[HLConsensusMonitorLastProcessedGauge][monitorType] = labeledValue{
		value:  float64(timestamp),
		labels: labels,
	}
}

func IncrementConsensusMonitorLines(monitorType string) {
	ctx := context.Background()
	labels := []attribute.KeyValue{
		attribute.String("monitor_type", monitorType),
	}
	HLConsensusMonitorLinesCounter.Add(ctx, 1, api.WithAttributes(labels...))
}

func IncrementConsensusMonitorErrors(monitorType string) {
	ctx := context.Background()
	labels := []attribute.KeyValue{
		attribute.String("monitor_type", monitorType),
	}
	HLConsensusMonitorErrorsCounter.Add(ctx, 1, api.WithAttributes(labels...))
}

// validator latency metric setters

func SetValidatorLatency(validator string, latency float64) {
	// use getValidatorLabels to get proper expanded addresses and signer/validator mapping
	labels := getValidatorLabels(validator)

	// extract the actual validator address from the labels for map key
	var validatorAddr string
	for _, label := range labels {
		if label.Key == "validator" {
			validatorAddr = label.Value.AsString()
			break
		}
	}

	metricsMutex.Lock()
	defer metricsMutex.Unlock()

	if _, exists := labeledValues[HLConsensusValidatorLatencyGauge]; !exists {
		labeledValues[HLConsensusValidatorLatencyGauge] = make(map[string]labeledValue)
	}
	labeledValues[HLConsensusValidatorLatencyGauge][validatorAddr] = labeledValue{
		value:  latency,
		labels: labels,
	}
}

func SetValidatorLatencyRound(validator string, round int64) {
	// use getValidatorLabels to get proper expanded addresses and signer/validator mapping
	labels := getValidatorLabels(validator)

	// extract the actual validator address from the labels for map key
	var validatorAddr string
	for _, label := range labels {
		if label.Key == "validator" {
			validatorAddr = label.Value.AsString()
			break
		}
	}

	metricsMutex.Lock()
	defer metricsMutex.Unlock()

	if _, exists := labeledValues[HLConsensusValidatorLatencyRoundGauge]; !exists {
		labeledValues[HLConsensusValidatorLatencyRoundGauge] = make(map[string]labeledValue)
	}
	labeledValues[HLConsensusValidatorLatencyRoundGauge][validatorAddr] = labeledValue{
		value:  float64(round),
		labels: labels,
	}
}

func SetValidatorLatencyEMA(validator string, ema float64) {
	// use getValidatorLabels to get proper expanded addresses and signer/validator mapping
	labels := getValidatorLabels(validator)

	// extract the actual validator address from the labels for map key
	var validatorAddr string
	for _, label := range labels {
		if label.Key == "validator" {
			validatorAddr = label.Value.AsString()
			break
		}
	}

	metricsMutex.Lock()
	defer metricsMutex.Unlock()

	if _, exists := labeledValues[HLConsensusValidatorLatencyEMAGauge]; !exists {
		labeledValues[HLConsensusValidatorLatencyEMAGauge] = make(map[string]labeledValue)
	}
	labeledValues[HLConsensusValidatorLatencyEMAGauge][validatorAddr] = labeledValue{
		value:  ema,
		labels: labels,
	}
}

// P2P metric setters (non-validator peers)

func SetP2PNonValPeerConnections(verified bool, count int64) {
	verifiedStr := "false"
	if verified {
		verifiedStr = "true"
	}

	labels := []attribute.KeyValue{
		attribute.String("verified", verifiedStr),
	}

	metricsMutex.Lock()
	defer metricsMutex.Unlock()

	if _, exists := labeledValues[HLP2PNonValPeerConnectionsGauge]; !exists {
		labeledValues[HLP2PNonValPeerConnectionsGauge] = make(map[string]labeledValue)
	}

	labeledValues[HLP2PNonValPeerConnectionsGauge][verifiedStr] = labeledValue{
		value:  float64(count),
		labels: labels,
	}
}

func SetP2PNonValPeersTotal(total int64) {
	metricsMutex.Lock()
	defer metricsMutex.Unlock()

	if _, exists := labeledValues[HLP2PNonValPeersTotalGauge]; !exists {
		labeledValues[HLP2PNonValPeersTotalGauge] = make(map[string]labeledValue)
	}

	// no labels for total peers metric
	labeledValues[HLP2PNonValPeersTotalGauge]["total"] = labeledValue{
		value:  float64(total),
		labels: []attribute.KeyValue{},
	}
}
