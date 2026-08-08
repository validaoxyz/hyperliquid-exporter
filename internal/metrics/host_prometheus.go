package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// Host metrics added by the 2026-08 semantics audit. Legacy families remain
// in prometheus_instruments.go and are mirrored by the owning monitor where
// compatibility requires their historical meaning.
var (
	HLNodeDiskAllocatedBytes = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "hl_node_disk_allocated_bytes",
		Help: "Unique filesystem blocks allocated to entries in the last complete NODE_HOME walk, including directory metadata and deduplicated by device and inode; unlike apparent bytes, sparse extents and hardlinks are counted physically.",
	})
	HLNodeDiskSubdirAllocatedBytes = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hl_node_disk_subdir_allocated_bytes",
		Help: "Unique filesystem blocks allocated within each fixed NODE_HOME path in the last complete walk, deduplicated independently by device and inode within each path scope.",
	}, []string{"path"})
	HLNodeDiskWalkUp = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "hl_node_disk_walk_up",
		Help: "Whether the latest NODE_HOME walk and all required entry metadata reads completed successfully (1=yes, 0=no); statfs is independent.",
	})
	HLNodeDiskStatfsUp = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "hl_node_disk_statfs_up",
		Help: "Whether the latest independent filesystem-capacity stat for NODE_HOME succeeded (1=yes, 0=no).",
	})
	HLNodeDiskErrorsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "hl_node_disk_errors_total",
		Help: "Disk monitor failures since exporter start, partitioned by fixed stage.",
	}, []string{"stage"})
	HLNodeDiskLastCompleteTimestampSeconds = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "hl_node_disk_last_complete_timestamp_seconds",
		Help: "Exporter wall-clock Unix timestamp of the last complete NODE_HOME walk publication.",
	})
	HLNodeDiskLastCompleteAgeSeconds = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "hl_node_disk_last_complete_age_seconds",
		Help: "Seconds since the last complete NODE_HOME walk publication at the latest monitor tick; zero before the first complete walk.",
	})
	HLNodeDiskPathState = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hl_node_disk_path_state",
		Help: "Filesystem-only one-hot state for a fixed NODE_HOME path from the last complete walk; state is present_nonempty, present_empty, or absent and does not attest node health.",
	}, []string{"path", "state"})

	HLNodeTmpFiles = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hl_node_tmp_files",
		Help: "Files in the last complete tmp scan by fixed class: receipt is an empty regular file under shell_rs_out; material is every other file type or location.",
	}, []string{"class"})
	HLNodeTmpBytesByClass = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hl_node_tmp_bytes_by_class",
		Help: "Apparent file bytes in the last complete tmp scan by fixed receipt or material class.",
	}, []string{"class"})
	HLNodeTmpMaterialStaleFiles = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "hl_node_tmp_material_stale_files",
		Help: "Non-receipt tmp files with retained bytes older than 24 hours in the last complete scan.",
	})
	HLNodeTmpMaterialStaleBytes = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "hl_node_tmp_material_stale_bytes",
		Help: "Apparent bytes in non-receipt tmp files older than 24 hours in the last complete scan.",
	})
	HLNodeTmpScanUp = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "hl_node_tmp_scan_up",
		Help: "Whether the latest tmp walk and all required entry metadata reads completed successfully (1=yes, 0=no).",
	})
	HLNodeTmpLastCompleteTimestampSeconds = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "hl_node_tmp_last_complete_timestamp_seconds",
		Help: "Exporter wall-clock Unix timestamp of the last complete tmp scan publication.",
	})

	HLNodeChildStderrArtifacts = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hl_node_child_stderr_artifacts",
		Help: "Retained visor child-stderr artifacts by bounded read state and bounded reason from the last complete directory scan; truncated and unreadable remain explicit evidence limits.",
	}, []string{"state", "reason"})
	HLNodeChildStderrLastArtifactTimestampSeconds = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hl_node_child_stderr_last_artifact_timestamp_seconds",
		Help: "Newest filesystem mtime among retained child-stderr artifacts for a bounded state and reason; absent when no matching artifact is retained.",
	}, []string{"state", "reason"})
	HLNodeChildStderrScanUp = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "hl_node_child_stderr_scan_up",
		Help: "Whether the latest bounded visor_child_stderr directory scan completed successfully (1=yes, 0=no).",
	})
	HLNodeChildStderrLastCompleteTimestampSeconds = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "hl_node_child_stderr_last_complete_timestamp_seconds",
		Help: "Exporter wall-clock Unix timestamp of the last complete child-stderr scan publication.",
	})

	HLNodeProcessEligibleMatches = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hl_node_process_eligible_matches",
		Help: "Processes whose comm and readable executable or argv0 identity matched the fixed process name in the latest complete procfs scan.",
	}, []string{"process"})
	HLNodeProcessMaxFDs = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hl_node_process_max_fds",
		Help: "Soft open-file descriptor limit for the selected process from procfs; zero when unavailable or unlimited.",
	}, []string{"process"})
	HLNodeProcessOpenFDsRatio = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hl_node_process_open_fds_ratio",
		Help: "Selected process open file descriptors divided by its finite soft limit; zero when the limit is unavailable or unlimited.",
	}, []string{"process"})
	HLNodeProcessIOTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "hl_node_process_io_total",
		Help: "Exporter-lifetime positive procfs IO deltas for the selected process; operation is read_bytes, write_bytes, read_syscalls, or write_syscalls and new process epochs establish a baseline.",
	}, []string{"process", "operation"})
)
