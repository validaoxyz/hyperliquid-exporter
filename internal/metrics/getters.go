package metrics

import "time"

func IsValidator() bool {
	metricsMutex.Lock()
	defer metricsMutex.Unlock()
	return nodeIdentity.IsValidator
}

func GetValidatorStakes() map[string]float64 {
	metricsMutex.RLock()
	defer metricsMutex.RUnlock()

	stakes := make(map[string]float64)
	if values, exists := labeledValues[HLConsensusValidatorStakeGauge]; exists {
		for validator, value := range values {
			stakes[validator] = value.value
		}
	}
	return stakes
}

// EligibleValidator is one target from the latest complete API snapshot. The
// predicate is intentionally only API isActive && !isJailed; it must not be
// presented as current committee membership.
type EligibleValidator struct {
	Validator string
	Signer    string
	Name      string
	Stake     float64
}

func GetAPIActiveAndUnjailedValidators() ([]EligibleValidator, time.Time) {
	metricsMutex.RLock()
	defer metricsMutex.RUnlock()
	out := make([]EligibleValidator, 0, len(validatorRegistry.validatorInfo))
	for validator, info := range validatorRegistry.validatorInfo {
		if !info.Active || info.Jailed {
			continue
		}
		out = append(out, EligibleValidator{
			Validator: validator,
			Signer:    info.Signer,
			Name:      normalizedValidatorName(info.Name),
			Stake:     info.Stake,
		})
	}
	return out, validatorRegistry.updatedAt
}

func ValidatorSnapshotState() (uint64, ValidatorAggregateSnapshot) {
	metricsMutex.RLock()
	defer metricsMutex.RUnlock()
	return validatorSnapshotGeneration, validatorSnapshotAggregates
}

// returns name/moniker for a validator address
func GetValidatorName(validatorAddr string) string {
	// is deprecated. names are resolved at data collection time
	// return empty to avoid lock contention during metrics collection
	return ""
}
