package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
)

var (
	// Prometheus metrics

	// Proposal counts
	HLProposerCounter = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "hl_proposer_count_total",
			Help: "Count of proposals by proposer",
		},
		[]string{"proposer"},
	)

	// Block height
	HLBlockHeightGauge = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "hl_block_height",
			Help: "Block height from latest block time file",
		},
	)

	// Block apply duration
	HLApplyDurationGauge = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "hl_apply_duration",
			Help: "Apply duration from latest block time file",
		},
	)

	// Validator jailed status
	HLValidatorJailedStatus = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "hl_validator_jailed_status",
			Help: "Jailed status of validators",
		},
		[]string{"validator", "name"},
	)

	// Validator stake
	HLValidatorStakeGauge = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "hl_validator_stake",
			Help: "Stake of validators",
		},
		[]string{"validator", "name"},
	)

	// Total stake across all validators
	HLTotalStakeGauge = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "hl_total_stake",
			Help: "Total stake across all validators",
		},
	)

	// Total stake of jailed validators
	HLJailedStakeGauge = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "hl_jailed_stake",
			Help: "Total stake of jailed validators",
		},
	)

	// Total stake of not jailed validators
	HLNotJailedStakeGauge = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "hl_not_jailed_stake",
			Help: "Total stake of not jailed validators",
		},
	)

	// Total number of validators
	HLValidatorCountGauge = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "hl_validator_count",
			Help: "Total number of validators",
		},
	)

	// Software version information
	HLSoftwareVersionInfo = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "hl_software_version",
			Help: "Software version information",
		},
		[]string{"commit", "date"},
	)

	// Software up-to-date status
	HLSoftwareUpToDate = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "hl_software_up_to_date",
			Help: "Indicates if the current software is up to date (1) or not (0)",
		},
	)

	// Latest block time
	HLLatestBlockTimeGauge = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "hl_latest_block_time",
		Help: "The timestamp of the latest block",
	})
)

// RegisterMetrics registers all Prometheus metrics
func RegisterMetrics() {
	prometheus.MustRegister(HLProposerCounter)
	prometheus.MustRegister(HLBlockHeightGauge)
	prometheus.MustRegister(HLApplyDurationGauge)
	prometheus.MustRegister(HLValidatorJailedStatus)
	prometheus.MustRegister(HLValidatorStakeGauge)
	prometheus.MustRegister(HLTotalStakeGauge)
	prometheus.MustRegister(HLJailedStakeGauge)
	prometheus.MustRegister(HLNotJailedStakeGauge)
	prometheus.MustRegister(HLValidatorCountGauge)
	prometheus.MustRegister(HLSoftwareVersionInfo)
	prometheus.MustRegister(HLSoftwareUpToDate)
	prometheus.MustRegister(HLLatestBlockTimeGauge)
}
