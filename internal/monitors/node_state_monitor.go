package monitors

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/validaoxyz/hyperliquid-exporter/internal/config"
	"github.com/validaoxyz/hyperliquid-exporter/internal/logger"
	"github.com/validaoxyz/hyperliquid-exporter/internal/metrics"
)

// nodeStatePollInterval — three single-file reads, none of which change
// often. 30s is plenty.
const nodeStatePollInterval = 30 * time.Second

// visorHeightForLag exposes the latest visor height to other monitors
// (specifically node_state_monitor, which derives blocks_above_freeze
// from it). The visor monitor calls SetLatestVisorHeight on each tick.
//
// Implemented as a plain int64 atomic rather than a channel/mutex because
// readers want the *most recent* value and dropped updates are fine.
var visorHeightForLag int64

// SetLatestVisorHeight is called by visor_monitor.tickVisor on each
// successful sample so node_state_monitor can compute
// blocks_above_freeze without re-reading the visor JSON itself.
func SetLatestVisorHeight(h int64) {
	atomic.StoreInt64(&visorHeightForLag, h)
}

func latestVisorHeight() int64 { return atomic.LoadInt64(&visorHeightForLag) }

// StartNodeStateMonitor publishes three tiny gauges sourced from single
// files under $NODE_HOME/hyperliquid_data:
//
//   - freeze_abci_height          → hl_visor_freeze_abci_height
//     plus hl_visor_blocks_above_freeze
//     when the visor monitor has reported
//     a current height.
//   - evm_db_hub_fast/cp_checkpoint_height → hl_evm_db_checkpoint_height{tier="fast"}
//   - evm_db_hub_slow/cp_checkpoint_height → hl_evm_db_checkpoint_height{tier="slow"}
//     plus hl_evm_db_checkpoint_lag_blocks
//     (fast - slow).
//
// Each file holds a single ASCII integer; the cost of reading all three
// per tick is negligible compared to any other monitor.
func StartNodeStateMonitor(ctx context.Context, cfg config.Config, errCh chan<- error) {
	freezePath := filepath.Join(cfg.NodeHome, "hyperliquid_data", "freeze_abci_height")
	fastPath := filepath.Join(cfg.NodeHome, "hyperliquid_data", "evm_db_hub_fast", "cp_checkpoint_height")
	slowPath := filepath.Join(cfg.NodeHome, "hyperliquid_data", "evm_db_hub_slow", "cp_checkpoint_height")

	logger.InfoComponent("node_state", "watching %s, %s, %s", freezePath, fastPath, slowPath)

	ticker := time.NewTicker(nodeStatePollInterval)
	defer ticker.Stop()

	tickNodeState(freezePath, fastPath, slowPath)
	metrics.MarkMonitorTick("node_state")

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			tickNodeState(freezePath, fastPath, slowPath)
			metrics.MarkMonitorTick("node_state")
		}
	}
}

func tickNodeState(freezePath, fastPath, slowPath string) {
	if freeze, ok := readSingleInt(freezePath); ok {
		metrics.HLVisorFreezeAbciHeight.Set(float64(freeze))
		// freeze is known here (readSingleInt parsed it). Set the gauge
		// unconditionally once the visor has reported a real height, so a
		// node below its replay floor (freeze > h, transient during replay)
		// reads a fresh 0 instead of silently holding a stale positive value
		// that mimics healthy progress. max(0, h-freeze) clamps the floor case.
		if h := latestVisorHeight(); h > 0 {
			above := h - freeze
			if above < 0 {
				above = 0
			}
			metrics.HLVisorBlocksAboveFreeze.Set(float64(above))
		}
	}
	fast, fastOK := readSingleInt(fastPath)
	slow, slowOK := readSingleInt(slowPath)
	if fastOK {
		metrics.HLEVMDBCheckpointHeight.WithLabelValues("fast").Set(float64(fast))
	}
	if slowOK {
		metrics.HLEVMDBCheckpointHeight.WithLabelValues("slow").Set(float64(slow))
	}
	if fastOK && slowOK {
		metrics.HLEVMDBCheckpointLagBlocks.Set(float64(fast - slow))
	}
}

// readSingleInt parses the entire file as a single ASCII integer. Whitespace
// (newlines, leading/trailing spaces) is trimmed. Returns ok=false on any
// IO or parse error — the caller silently skips that gauge for the tick.
func readSingleInt(path string) (int64, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	s := strings.TrimSpace(string(data))
	if s == "" {
		return 0, false
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, false
	}
	return n, true
}
