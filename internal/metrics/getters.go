package metrics

func IsValidator() bool {
	metricsMutex.Lock()
	defer metricsMutex.Unlock()
	return nodeIdentity.IsValidator
}
