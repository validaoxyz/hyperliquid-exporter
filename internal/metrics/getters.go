package metrics

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

// returns name/moniker for a validator address
func GetValidatorName(validatorAddr string) string {
	// is deprecated. names are resolved at data collection time
	// return empty to avoid lock contention during metrics collection
	return ""
}
