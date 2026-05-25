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
	EnableEVM        bool
	// PrometheusPort overrides the default 8086 listener. Useful for
	// side-by-side test runs and for sites with port conflicts.
	PrometheusPort int
}
