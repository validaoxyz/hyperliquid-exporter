package metrics

type MetricsConfig struct {
	EnablePrometheus bool
	EnableOTLP       bool
	OTLPEndpoint     string
	OTLPInsecure     bool
	Identity         string
	Chain            string
}
