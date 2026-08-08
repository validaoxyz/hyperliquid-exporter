package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	HLNodeOperatorConfigPresent = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hl_node_operator_config_present",
		Help: "Presence of each fixed operator-config file (1=present, 0=absent, -1=stat failed). File labels are allowlisted.",
	}, []string{"file"})
	HLNodeOperatorConfigFailedLoad = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hl_node_operator_config_failed_load",
		Help: "Count of FAILED_LOAD sidecars for each fixed operator-config file; arbitrary filenames collapse to file=unknown.",
	}, []string{"file"})
)
