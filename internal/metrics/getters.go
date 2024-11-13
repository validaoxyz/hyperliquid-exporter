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
	if values, exists := labeledValues[HLValidatorStakeGauge]; exists {
		for validator, value := range values {
			stakes[validator] = value.value
		}
	}
	return stakes
}
