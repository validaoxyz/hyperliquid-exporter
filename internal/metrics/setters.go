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
	identity, ok := ResolveValidatorIdentity(proposer)
	if !ok {
		identity = ValidatorIdentity{Validator: "unknown", Signer: "unknown", Name: "unknown"}
		logger.Debug("No current identity mapping for proposer; recording bounded unknown series")
	}

	// now register metric atomically
	metricsMutex.Lock()
	defer metricsMutex.Unlock()

	ctx := context.Background()
	// always set the name label so the series shape stays stable; use the
	// "unknown" sentinel (as getValidatorLabels does) when we have no moniker
	labels := []attribute.KeyValue{
		attribute.String("validator", identity.Validator),
		attribute.String("signer", identity.Signer),
		attribute.String("name", identity.Name),
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
}

// ValidatorSummarySnapshot is one row of the validatorSummaries poll used
// to reconcile the per-validator stake/status gauges.
type ValidatorSummarySnapshot struct {
	Validator, Signer, Name string
	Stake                   float64
	Jailed, Active          bool
}

// ValidatorAggregateSnapshot is derived from exactly the same complete API
// generation as ValidatorSummarySnapshot. Keeping it explicit makes the
// atomic publication contract testable without taking a scrape in the middle
// of a writer transaction.
type ValidatorAggregateSnapshot struct {
	TotalStake             float64
	JailedStake            float64
	NotJailedStake         float64
	ActiveStake            float64
	InactiveStake          float64
	ValidatorCount         int64
	ActiveAndUnjailedStake float64
	ActiveAndUnjailedCount int64
}

var (
	validatorSnapshotGeneration uint64
	validatorSnapshotAggregates ValidatorAggregateSnapshot
)

// ReplaceValidatorSummaries reconciles the per-validator stake, jailed and
// active gauges to the latest validatorSummaries response, replacing each
// labeled map wholesale (same pattern as ReplaceValidatorLatencyEMA).
// Validators that leave the set disappear from the gauges instead of
// freezing at their last value forever.
func ReplaceValidatorSummaries(snap []ValidatorSummarySnapshot) {
	ReplaceValidatorSnapshot(snap)
}

// ReplaceValidatorSnapshot stages and commits the full signer/validator
// registry, all per-row status families, and every row-derived aggregate
// under one metrics lock. A callback therefore observes either the old
// generation or the new one, never a mixed count/stake view.
func ReplaceValidatorSnapshot(snap []ValidatorSummarySnapshot) {
	stake := make(map[string]labeledValue, len(snap))
	jailed := make(map[string]labeledValue, len(snap))
	active := make(map[string]labeledValue, len(snap))
	eligible := make(map[string]labeledValue, len(snap))
	signerToValidator := make(map[string]string, len(snap))
	validatorInfo := make(map[string]ValidatorInfo, len(snap))
	var aggregates ValidatorAggregateSnapshot
	for _, v := range snap {
		validator := strings.ToLower(strings.TrimSpace(v.Validator))
		signer := strings.ToLower(strings.TrimSpace(v.Signer))
		name := normalizedValidatorName(v.Name)
		labels := []attribute.KeyValue{
			attribute.String("validator", validator),
			attribute.String("signer", signer),
			attribute.String("name", name),
		}
		j, a, e := 0.0, 0.0, 0.0
		if v.Jailed {
			j = 1
			aggregates.JailedStake += v.Stake
		} else {
			aggregates.NotJailedStake += v.Stake
		}
		if v.Active {
			a = 1
			aggregates.ActiveStake += v.Stake
		} else {
			aggregates.InactiveStake += v.Stake
		}
		if v.Active && !v.Jailed {
			e = 1
			aggregates.ActiveAndUnjailedCount++
			aggregates.ActiveAndUnjailedStake += v.Stake
		}
		aggregates.TotalStake += v.Stake
		aggregates.ValidatorCount++
		stake[validator] = labeledValue{value: v.Stake, labels: labels}
		jailed[validator] = labeledValue{value: j, labels: labels}
		active[validator] = labeledValue{value: a, labels: labels}
		eligible[validator] = labeledValue{value: e, labels: labels}
		signerToValidator[signer] = validator
		validatorInfo[validator] = ValidatorInfo{
			Signer: signer,
			Name:   name,
			Stake:  v.Stake,
			Active: v.Active,
			Jailed: v.Jailed,
		}
	}

	metricsMutex.Lock()
	defer metricsMutex.Unlock()
	labeledValues[HLConsensusValidatorStakeGauge] = stake
	labeledValues[HLConsensusValidatorJailedStatus] = jailed
	labeledValues[HLConsensusValidatorActiveStatus] = active
	labeledValues[HLConsensusValidatorAPIActiveAndUnjailedStatus] = eligible
	currentValues[HLConsensusTotalStakeGauge] = aggregates.TotalStake
	currentValues[HLConsensusJailedStakeGauge] = aggregates.JailedStake
	currentValues[HLConsensusNotJailedStakeGauge] = aggregates.NotJailedStake
	currentValues[HLConsensusActiveStakeGauge] = aggregates.ActiveStake
	currentValues[HLConsensusInactiveStakeGauge] = aggregates.InactiveStake
	currentValues[HLConsensusValidatorCountGauge] = aggregates.ValidatorCount
	currentValues[HLConsensusAPIActiveAndUnjailedCountGauge] = aggregates.ActiveAndUnjailedCount
	currentValues[HLConsensusAPIActiveAndUnjailedStakeGauge] = aggregates.ActiveAndUnjailedStake
	validatorRegistry.signerToValidator = signerToValidator
	validatorRegistry.validatorInfo = validatorInfo
	validatorRegistry.updatedAt = time.Now()
	reconcileVoteIdentityValuesLocked(signerToValidator, validatorInfo)
	validatorSnapshotAggregates = aggregates
	validatorSnapshotGeneration++
}

func reconcileVoteIdentityValuesLocked(signerToValidator map[string]string, validatorInfo map[string]ValidatorInfo) {
	for _, instrument := range []api.Observable{HLConsensusVoteRoundGauge, HLConsensusVoteTimeDiffGauge} {
		old := labeledValues[instrument]
		if len(old) == 0 {
			continue
		}
		rebuilt := make(map[string]labeledValue, len(old))
		for _, value := range old {
			var signer, validator string
			identifier := strings.ToLower(strings.TrimSpace(ExpandAddress(value.identitySource)))
			if mapped, ok := signerToValidator[identifier]; ok {
				validator = mapped
				signer = identifier
			} else if _, ok := validatorInfo[identifier]; ok {
				validator = identifier
			}
			for _, label := range value.labels {
				switch string(label.Key) {
				case "signer":
					if signer == "" {
						signer = strings.ToLower(label.Value.AsString())
					}
				case "validator":
					if validator == "" {
						validator = strings.ToLower(label.Value.AsString())
					}
				}
			}
			if mapped, ok := signerToValidator[signer]; ok {
				validator = mapped
			} else if _, ok := validatorInfo[validator]; !ok {
				continue
			}
			info := validatorInfo[validator]
			value.labels = identityLabels(ValidatorIdentity{
				Validator: validator,
				Signer:    info.Signer,
				Name:      info.Name,
			})
			rebuilt[validator] = value
		}
		labeledValues[instrument] = rebuilt
	}
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

// ReplaceValidatorTCPConnectDurations reconciles the deprecated RTT-named
// compatibility family to the current successful TCP-connect snapshot. New
// code publishes the accurately named Prometheus family in parallel.
type ValidatorTCPConnectDurationSnapshot struct {
	Validator    string
	Moniker      string
	IP           string
	Milliseconds float64
}

func ReplaceValidatorTCPConnectDurations(snap []ValidatorTCPConnectDurationSnapshot) {
	rebuilt := make(map[string]labeledValue, len(snap))
	for _, row := range snap {
		if row.Validator == "" || row.IP == "" {
			continue
		}
		rebuilt[row.Validator] = labeledValue{
			value: row.Milliseconds,
			labels: append([]attribute.KeyValue{
				attribute.String("validator", row.Validator),
				attribute.String("moniker", row.Moniker),
				attribute.String("ip", row.IP),
			}, getCommonLabels()...),
		}
	}
	metricsMutex.Lock()
	labeledValues[HLConsensusValidatorRTTGauge] = rebuilt
	metricsMutex.Unlock()
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

func IncrementEVMTxType(txType string) {
	ctx := context.Background()
	HLEVMTxTypeCounter.Add(ctx, 1, api.WithAttributes(attribute.String("type", txType)))
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

func ObserveCoreOrdersPerBlock(count float64) {
	if HLCoreOrdersPerBlockHistogram != nil {
		HLCoreOrdersPerBlockHistogram.Record(sharedCtx, count)
	}
}

func IncCoreBlocksProcessed() {
	HLCoreBlocksProcessedCounter.Add(sharedCtx, 1)
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

// consensus monitoring setters

func SetValidatorLastVoteRound(validator string, round int64) {
	labels := getValidatorLabels(validator)
	identitySource := strings.ToLower(strings.TrimSpace(validator))

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
	key := validatorAddr
	if validatorAddr == "unknown" {
		key = "unknown"
	}
	labeledValues[HLConsensusVoteRoundGauge][key] = labeledValue{
		value:          float64(round),
		labels:         labels,
		identitySource: identitySource,
	}
}

// SetValidatorLastVoteTime records when a validator's vote was last
// observed. The stored value is the vote's unix timestamp; the scrape
// callback converts it to an age, so hl_consensus_vote_time_diff_seconds
// climbs while a validator is silent (the previous implementation stored a
// parse-time diff that froze at ~0 forever, useless for stall detection).
func SetValidatorLastVoteTime(validator string, voteTime time.Time) {
	labels := getValidatorLabels(validator)
	identitySource := strings.ToLower(strings.TrimSpace(validator))

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
	key := validatorAddr
	if validatorAddr == "unknown" {
		key = "unknown"
	}
	labeledValues[HLConsensusVoteTimeDiffGauge][key] = labeledValue{
		value:          float64(voteTime.Unix()),
		labels:         labels,
		identitySource: identitySource,
	}
}

// TrimVoteSeries drops vote round/age series whose last observed vote is
// older than maxAge. Departed validators otherwise keep a frozen round and
// an ever-climbing age series forever.
func TrimVoteSeries(maxAge time.Duration) {
	cutoff := float64(time.Now().Add(-maxAge).Unix())
	metricsMutex.Lock()
	defer metricsMutex.Unlock()
	ages := labeledValues[HLConsensusVoteTimeDiffGauge]
	rounds := labeledValues[HLConsensusVoteRoundGauge]
	for validator, v := range ages {
		if v.value < cutoff {
			delete(ages, validator)
			delete(rounds, validator)
		}
	}
}

func SetCurrentConsensusRound(round int64) {
	metricsMutex.Lock()
	defer metricsMutex.Unlock()
	currentValues[HLConsensusCurrentRoundGauge] = round
}

// returns expanded validator, signer, and name labels for a validator or signer address
func getValidatorLabels(addressInput string) []attribute.KeyValue {
	identity, ok := ResolveValidatorIdentity(addressInput)
	if !ok {
		identity = ValidatorIdentity{Validator: "unknown", Signer: "unknown", Name: "unknown"}
	}
	return []attribute.KeyValue{
		attribute.String("validator", identity.Validator),
		attribute.String("signer", identity.Signer),
		attribute.String("name", identity.Name),
	}
}

func IncrementHeartbeatsSent(validator string) {
	ctx := context.Background()
	labels := getValidatorLabels(validator)
	HLConsensusHeartbeatSentCounter.Add(ctx, 1, api.WithAttributes(labels...))
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

// ValidatorHeartbeatSnapshot is one fully validated status row. A nil
// LastAckDuration means the source carried explicit JSON null; AckFieldPresent
// false means the nested key was omitted.
type ValidatorHeartbeatSnapshot struct {
	Identity         ValidatorIdentity
	SinceLastSuccess *float64
	LastAckDuration  *float64
	AckFieldPresent  bool
}

// ValidatorDisconnectSnapshot is one subject/reporter relation from the
// latest complete status generation. SinceRound is in the consensus-round
// domain and is never converted into wall-clock time.
type ValidatorDisconnectSnapshot struct {
	Subject    ValidatorIdentity
	Reporter   ValidatorIdentity
	SinceRound int64
}

func identityLabels(identity ValidatorIdentity) []attribute.KeyValue {
	return []attribute.KeyValue{
		attribute.String("validator", identity.Validator),
		attribute.String("signer", identity.Signer),
		attribute.String("name", normalizedValidatorName(identity.Name)),
	}
}

// ReplaceConsensusStatusSnapshot atomically reconciles every OTel family
// owned by one complete status record. An omitted source field is represented
// by an empty slice and therefore withdraws the old generation; callers expose
// field presence separately rather than synthesizing healthy zeroes.
func ReplaceConsensusStatusSnapshot(heartbeats []ValidatorHeartbeatSnapshot, disconnected []ValidatorDisconnectSnapshot) {
	heartbeatValues := make(map[string]labeledValue, len(heartbeats)*2)
	ackObserved := make(map[string]labeledValue, len(heartbeats))
	connectivity := make(map[string]labeledValue, len(disconnected))
	disconnectRounds := make(map[string]labeledValue, len(disconnected))

	for _, hb := range heartbeats {
		base := identityLabels(hb.Identity)
		if hb.SinceLastSuccess != nil {
			labels := append(append([]attribute.KeyValue{}, base...), attribute.String("status_type", "since_last_success"))
			heartbeatValues[hb.Identity.Signer+"\x00since_last_success"] = labeledValue{value: *hb.SinceLastSuccess, labels: labels}
		}
		if hb.AckFieldPresent {
			observed := 0.0
			if hb.LastAckDuration != nil {
				observed = 1
				labels := append(append([]attribute.KeyValue{}, base...), attribute.String("status_type", "last_ack_duration"))
				heartbeatValues[hb.Identity.Signer+"\x00last_ack_duration"] = labeledValue{value: *hb.LastAckDuration, labels: labels}
			}
			ackObserved[hb.Identity.Signer] = labeledValue{value: observed, labels: base}
		}
	}

	for _, relation := range disconnected {
		key := relation.Subject.Signer + "\x00" + relation.Reporter.Signer
		labels := []attribute.KeyValue{
			attribute.String("validator", relation.Subject.Validator),
			attribute.String("signer", relation.Subject.Signer),
			attribute.String("name", normalizedValidatorName(relation.Subject.Name)),
			attribute.String("reporter_validator", relation.Reporter.Validator),
			attribute.String("reporter_signer", relation.Reporter.Signer),
			attribute.String("reporter_name", normalizedValidatorName(relation.Reporter.Name)),
		}
		connectivity[key] = labeledValue{value: 0, labels: labels}
		disconnectRounds[key] = labeledValue{value: float64(relation.SinceRound), labels: labels}
	}

	metricsMutex.Lock()
	labeledValues[HLConsensusHeartbeatStatusGauge] = heartbeatValues
	labeledValues[HLConsensusHeartbeatAckObservedGauge] = ackObserved
	labeledValues[HLConsensusConnectivityGauge] = connectivity
	labeledValues[HLConsensusDisconnectedSinceRoundGauge] = disconnectRounds
	metricsMutex.Unlock()
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

// ReplaceQCParticipationRates publishes exactly the identities represented by
// the current QC window. Historical signers are not seeded with synthetic
// zeroes and departed identities disappear from the next generation.
func ReplaceQCParticipationRates(rates map[string]float64) {
	rebuilt := make(map[string]labeledValue, len(rates))
	for identifier, rate := range rates {
		labels := getValidatorLabels(identifier)
		validator := "unknown"
		for _, label := range labels {
			if label.Key == "validator" {
				validator = label.Value.AsString()
				break
			}
		}
		rebuilt[validator] = labeledValue{value: rate, labels: labels}
	}
	metricsMutex.Lock()
	labeledValues[HLConsensusQCParticipationGauge] = rebuilt
	metricsMutex.Unlock()
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

// AddConsensusMonitorLines adds a batch of processed-line counts. The
// consensus stream runs thousands of lines between EOF pauses; batching
// keeps the per-line hot path free of metric calls.
func AddConsensusMonitorLines(monitorType string, n int64) {
	ctx := context.Background()
	labels := []attribute.KeyValue{
		attribute.String("monitor_type", monitorType),
	}
	HLConsensusMonitorLinesCounter.Add(ctx, n, api.WithAttributes(labels...))
}

// AddConsensusMonitorErrors adds a batch of error counts; see
// AddConsensusMonitorLines.
func AddConsensusMonitorErrors(monitorType string, n int64) {
	ctx := context.Background()
	labels := []attribute.KeyValue{
		attribute.String("monitor_type", monitorType),
	}
	HLConsensusMonitorErrorsCounter.Add(ctx, n, api.WithAttributes(labels...))
}

// validator latency metric setters

// ReplaceValidatorLatency reconciles the raw per-validator latency gauge to
// a full snapshot; same staleness rationale as ReplaceValidatorLatencyEMA
// (a validator whose latency files stop updating is dropped instead of
// freezing at its last sample).
func ReplaceValidatorLatency(latencyByValidator map[string]float64) {
	rebuilt := make(map[string]labeledValue, len(latencyByValidator))
	for validator, latency := range latencyByValidator {
		labels := getValidatorLabels(validator)
		var validatorAddr string
		for _, label := range labels {
			if label.Key == "validator" {
				validatorAddr = label.Value.AsString()
				break
			}
		}
		rebuilt[validatorAddr] = labeledValue{value: latency, labels: labels}
	}

	metricsMutex.Lock()
	defer metricsMutex.Unlock()
	labeledValues[HLConsensusValidatorLatencyGauge] = rebuilt
}

// ReplaceValidatorLatencyRound reconciles the latency-round gauge to a
// full snapshot; see ReplaceValidatorLatency.
func ReplaceValidatorLatencyRound(roundByValidator map[string]int64) {
	rebuilt := make(map[string]labeledValue, len(roundByValidator))
	for validator, round := range roundByValidator {
		labels := getValidatorLabels(validator)
		var validatorAddr string
		for _, label := range labels {
			if label.Key == "validator" {
				validatorAddr = label.Value.AsString()
				break
			}
		}
		rebuilt[validatorAddr] = labeledValue{value: float64(round), labels: labels}
	}

	metricsMutex.Lock()
	defer metricsMutex.Unlock()
	labeledValues[HLConsensusValidatorLatencyRoundGauge] = rebuilt
}

// ReplaceValidatorLatencyEMA reconciles the EMA gauge to a full snapshot.
//
// The EMA file's latest line is a complete snapshot of every validator at
// that timestamp, so we REBUILD labeledValues[HLConsensusValidatorLatencyEMAGauge]
// from scratch on each update rather than merging into the prior map. This is
// the staleness fix: an OTel ObservableGauge re-emits whatever is in the map
// every scrape, so a validator that drops out of the snapshot (or whose value
// has become the no-data sentinel and was filtered upstream) would otherwise
// keep its last value frozen forever. Rebuilding drops any validator absent
// from the snapshot. Labels are resolved per validator exactly as
// SetValidatorLatencyEMA does (getValidatorLabels + the "validator" label value
// as the map key).
func ReplaceValidatorLatencyEMA(emaByValidator map[string]float64) {
	// resolve labels for every validator in the snapshot before taking the lock
	rebuilt := make(map[string]labeledValue, len(emaByValidator))
	for validator, ema := range emaByValidator {
		labels := getValidatorLabels(validator)

		// extract the actual validator address from the labels for map key
		var validatorAddr string
		for _, label := range labels {
			if label.Key == "validator" {
				validatorAddr = label.Value.AsString()
				break
			}
		}

		rebuilt[validatorAddr] = labeledValue{
			value:  ema,
			labels: labels,
		}
	}

	metricsMutex.Lock()
	defer metricsMutex.Unlock()

	// replace the whole map so dropped-out / now-sentinel validators disappear
	labeledValues[HLConsensusValidatorLatencyEMAGauge] = rebuilt
}

// P2P metric setters (non-validator peers)

// ReplaceP2PChildSnapshot atomically replaces the complete OTLP compatibility
// view of one explicit child-peers snapshot. Despite the historical
// instrument name, the verified series count child identities, not sockets.
func ReplaceP2PChildSnapshot(verified, unverified int64) {
	byVerified := make(map[string]labeledValue, 2)
	for label, count := range map[string]int64{"true": verified, "false": unverified} {
		byVerified[label] = labeledValue{
			value: float64(count),
			labels: []attribute.KeyValue{
				attribute.String("verified", label),
			},
		}
	}
	total := map[string]labeledValue{
		"total": {
			value:  float64(verified + unverified),
			labels: []attribute.KeyValue{},
		},
	}

	metricsMutex.Lock()
	labeledValues[HLP2PNonValPeerConnectionsGauge] = byVerified
	labeledValues[HLP2PNonValPeersTotalGauge] = total
	metricsMutex.Unlock()
}

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
