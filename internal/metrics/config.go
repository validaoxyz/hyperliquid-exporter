package metrics

type MetricsConfig struct {
	EnablePrometheus bool
	EnableOTLP       bool
	OTLPEndpoint     string
	OTLPInsecure     bool
	Alias            string
	Chain            string
	NodeHome         string
	ValidatorAddress string
	IsValidator      bool
}
