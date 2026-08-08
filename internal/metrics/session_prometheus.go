package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	HLNodeObservedRunLastActivitySeconds = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hl_node_observed_run_last_activity_seconds",
		Help: "Unix mtime of the most recently modified retained replica_cmds run directory; this is filesystem activity, not run start or duration.",
	}, []string{})
	HLNodeReplayLastActivitySeconds = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hl_node_replay_last_activity_seconds",
		Help: "Newest filesystem mtime among retained replay marker directories; this is activity and does not prove replay end or duration.",
	}, []string{})
	HLNodeSnapshotHeightLagBlocks = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hl_node_snapshot_height_lag_blocks",
		Help: "Latest current visor height minus latest completed periodic ABCI snapshot height; absent until both heights are available and nonnegative.",
	}, []string{})
	HLNodeSnapshotHeightLagAvailable = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hl_node_snapshot_height_lag_available",
		Help: "Whether snapshot height lag could be joined to a current visor height on the latest complete status scan (1=yes, 0=no); qualify freshness with source health.",
	}, []string{})
)
