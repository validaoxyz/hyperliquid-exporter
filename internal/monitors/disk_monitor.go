package monitors

import (
	"context"
	"errors"
	"io/fs"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/validaoxyz/hyperliquid-exporter/internal/config"
	"github.com/validaoxyz/hyperliquid-exporter/internal/logger"
	"github.com/validaoxyz/hyperliquid-exporter/internal/metrics"
)

// diskPollInterval is intentionally slow: walking NODE_HOME visits hundreds
// of thousands of inodes on a long-lived node and a full disk doesn't change
// in seconds. statfs is cheap but the recursive size walk dominates.
const diskPollInterval = 120 * time.Second

type fsStats struct {
	Bavail uint64
	Blocks uint64
	Bsize  uint64
}

// trackedSubdirs is the allowlist of subpaths whose recursive byte usage
// the monitor publishes individually. These are the directories most
// likely to consume runaway disk on a Hyperliquid node. The list
// includes both broad rollups (hyperliquid_data, data/replica_cmds)
// and per-RocksDB subpaths so operators can tell *which* DB is
// bloating without ssh'ing in.
var trackedSubdirs = []string{
	"data/replica_cmds",
	"data/evm_block_and_receipts",
	"data/block_times",
	"data/node_fast_block_times",
	"data/node_slow_block_times",
	"data/node_logs",
	"data/latency_buckets",
	"data/latency_summaries",
	"data/periodic_abci_states",
	"data/visor_abci_states",
	"data/tcp_traffic",
	"data/dhs",
	"hyperliquid_data",
	"hyperliquid_data/db_hub/Evm",
	"hyperliquid_data/db_hub/Exchange",
	"hyperliquid_data/db_hub/Rpc",
	"hyperliquid_data/evm_db_hub_fast",
	"hyperliquid_data/evm_db_hub_fast/EvmState",
	"hyperliquid_data/evm_db_hub_slow",
	"hyperliquid_data/evm_db_hub_slow/EvmState",
	"hyperliquid_data/evm_db_hub_slow/checkpoint",
	"tmp",
}

const (
	diskPathPresentNonempty = "present_nonempty"
	diskPathPresentEmpty    = "present_empty"
	diskPathAbsent          = "absent"
)

var diskPathStates = []string{
	diskPathPresentNonempty,
	diskPathPresentEmpty,
	diskPathAbsent,
}

var diskLastCompleteUnix atomic.Int64

type diskFileID struct {
	device uint64
	inode  uint64
}

type diskSnapshot struct {
	apparentTotal   int64
	apparentByPath  map[string]int64
	allocatedTotal  int64
	allocatedByPath map[string]int64
	pathState       map[string]string
}

// StartDiskMonitor walks NODE_HOME once every two minutes and publishes its size
// alongside the host filesystem's free/total. Operators want to know
// "how much room do I have before this node breaks" without ssh'ing in;
// disk filling is one of the most common silent failure modes for
// blockchain nodes.
func StartDiskMonitor(ctx context.Context, cfg config.Config, errCh chan<- error) {
	metrics.RegisterSource(metrics.SourceDisk, true)

	logger.InfoComponent("disk", "starting disk monitor for %s (every %s)",
		cfg.NodeHome, diskPollInterval)

	ticker := time.NewTicker(diskPollInterval)
	defer ticker.Stop()

	tickDisk(cfg.NodeHome)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			tickDisk(cfg.NodeHome)
		}
	}
}

func tickDisk(nodeHome string) {
	tickDiskWith(nodeHome, statfs, walkSizes, time.Now)
}

type diskStatfsFunc func(string) (*fsStats, error)
type diskWalkFunc func(string, []string) (diskSnapshot, error)

func tickDiskWith(nodeHome string, stat diskStatfsFunc, walk diskWalkFunc, now func() time.Time) bool {
	metrics.MarkMonitorAttempt("disk")
	metrics.MarkSourceAttempt(metrics.SourceDisk)

	// Filesystem-level numbers come from statfs and don't require walking
	// the tree, so do them first and unconditionally — even if the tree
	// walk later fails or is slow, the operator still gets free/total.
	filesystem, statErr := stat(nodeHome)
	if statErr == nil && filesystem != nil {
		metrics.HLNodeDiskFreeBytes.Set(float64(filesystem.Bavail) * float64(filesystem.Bsize))
		metrics.HLNodeDiskTotalBytes.Set(float64(filesystem.Blocks) * float64(filesystem.Bsize))
		metrics.HLNodeDiskStatfsUp.Set(1)
	} else {
		if statErr == nil {
			statErr = errors.New("statfs returned no filesystem data")
		}
		metrics.HLNodeDiskStatfsUp.Set(0)
		metrics.HLNodeDiskErrorsTotal.WithLabelValues("statfs").Inc()
		logger.DebugComponent("disk", "statfs failed: %v", statErr)
	}

	snapshot, err := walk(nodeHome, trackedSubdirs)
	if err != nil {
		metrics.HLNodeDiskWalkUp.Set(0)
		metrics.HLNodeDiskErrorsTotal.WithLabelValues("walk").Inc()
		if errors.Is(err, fs.ErrNotExist) {
			metrics.MarkSourceAbsent(metrics.SourceDisk)
		} else {
			metrics.MarkSourceError(metrics.SourceDisk, metrics.SourceFailureWalk)
		}
		if last := diskLastCompleteUnix.Load(); last > 0 {
			age := now().Unix() - last
			if age < 0 {
				age = 0
			}
			metrics.HLNodeDiskLastCompleteAgeSeconds.Set(float64(age))
		}
		logger.DebugComponent("disk", "NODE_HOME walk incomplete; retaining last complete snapshot: %v", err)
		return false
	}

	metrics.HLNodeDiskUsedBytes.Set(float64(snapshot.apparentTotal))
	metrics.HLNodeDiskAllocatedBytes.Set(float64(snapshot.allocatedTotal))

	for _, sub := range trackedSubdirs {
		metrics.HLNodeDiskSubdirBytes.WithLabelValues(sub).Set(float64(snapshot.apparentByPath[sub]))
		metrics.HLNodeDiskSubdirAllocatedBytes.WithLabelValues(sub).Set(float64(snapshot.allocatedByPath[sub]))
		for _, state := range diskPathStates {
			value := 0.0
			if snapshot.pathState[sub] == state {
				value = 1
			}
			metrics.HLNodeDiskPathState.WithLabelValues(sub, state).Set(value)
		}
	}
	completedAt := now().Unix()
	diskLastCompleteUnix.Store(completedAt)
	metrics.HLNodeDiskWalkUp.Set(1)
	metrics.HLNodeDiskLastCompleteTimestampSeconds.Set(float64(completedAt))
	metrics.HLNodeDiskLastCompleteAgeSeconds.Set(0)
	metrics.MarkSourceValidObservation(metrics.SourceDisk, time.Time{})
	metrics.MarkSourcePublication(metrics.SourceDisk)
	metrics.MarkMonitorValidObservation("disk")
	metrics.MarkMonitorPublication("disk")
	return true
}

// walkSizes walks nodeHome once, attributing every regular file's size to
// the grand total and to each tracked subdir prefix it lives under.
// Prefixes nest (hyperliquid_data contains hyperliquid_data/db_hub/Evm), so
// a file adds to every matching bucket. The single pass replaces the
// previous walk-per-subdir approach, which re-traversed the biggest trees
// up to three times per tick. Any walk or metadata error rejects the staged
// snapshot so callers cannot publish a plausible-looking partial total.
func walkSizes(nodeHome string, subs []string) (diskSnapshot, error) {
	return walkSizesWith(nodeHome, subs, filepath.WalkDir)
}

type walkDirFunc func(string, fs.WalkDirFunc) error

func walkSizesWith(nodeHome string, subs []string, walk walkDirFunc) (diskSnapshot, error) {
	snapshot := diskSnapshot{
		apparentByPath:  make(map[string]int64, len(subs)),
		allocatedByPath: make(map[string]int64, len(subs)),
		pathState:       make(map[string]string, len(subs)),
	}
	paths := make([]string, len(subs))
	prefixes := make([]string, len(subs))
	allocatedSeen := make(map[diskFileID]struct{})
	allocatedSeenByPath := make(map[string]map[diskFileID]struct{}, len(subs))
	for i, sub := range subs {
		paths[i] = filepath.Join(nodeHome, filepath.FromSlash(sub))
		prefixes[i] = paths[i] + string(filepath.Separator)
		snapshot.pathState[sub] = diskPathAbsent
		allocatedSeenByPath[sub] = make(map[diskFileID]struct{})
	}

	err := walk(nodeHome, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		matching := make([]int, 0, 2)
		for i, prefix := range prefixes {
			if path == paths[i] {
				snapshot.pathState[subs[i]] = diskPathPresentEmpty
				matching = append(matching, i)
			}
			if strings.HasPrefix(path, prefix) {
				matching = append(matching, i)
				snapshot.pathState[subs[i]] = diskPathPresentNonempty
			}
		}

		info, err := d.Info()
		if err != nil {
			return err
		}

		// Physical allocation covers every filesystem entry, including directory
		// metadata, and is unique within each declared aggregation scope.
		if diskAllocatedBytesSupported() {
			device, inode, allocated, ok := allocatedFileInfo(info)
			if !ok {
				return errors.New("filesystem entry lacks allocation identity")
			}
			id := diskFileID{device: device, inode: inode}
			snapshot.allocatedTotal += addUniqueAllocated(allocatedSeen, id, allocated)
			for _, i := range matching {
				seen := allocatedSeenByPath[subs[i]]
				snapshot.allocatedByPath[subs[i]] += addUniqueAllocated(seen, id, allocated)
			}
		}

		if d.IsDir() {
			return nil
		}
		if info.Size() > 0 {
			for i := range paths {
				if path == paths[i] {
					snapshot.pathState[subs[i]] = diskPathPresentNonempty
				}
			}
		}
		size := info.Size()
		snapshot.apparentTotal += size
		for _, i := range matching {
			snapshot.apparentByPath[subs[i]] += size
		}
		return nil
	})
	if err != nil {
		return diskSnapshot{}, err
	}
	return snapshot, nil
}

func addUniqueAllocated(seen map[diskFileID]struct{}, id diskFileID, allocated int64) int64 {
	if _, exists := seen[id]; exists {
		return 0
	}
	seen[id] = struct{}{}
	return allocated
}
