package monitors

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"time"

	"github.com/validaoxyz/hyperliquid-exporter/internal/config"
	"github.com/validaoxyz/hyperliquid-exporter/internal/logger"
	"github.com/validaoxyz/hyperliquid-exporter/internal/metrics"
	"github.com/validaoxyz/hyperliquid-exporter/internal/utils"
)

func StartProposalMonitor(ctx context.Context, cfg config.Config, errCh chan<- error) {
	goSafe("proposal", func() {
		// skip if replica monitoring is enabled as it will handle proposer counting
		if cfg.EnableReplicaMetrics {
			// already logged in exporter.go, just return silently
			return
		}

		// wait a short time at startup to ensure signer mappings are populated
		// prevents early proposals from having incorrect validator labels
		select {
		case <-ctx.Done():
			return
		case <-time.After(5 * time.Second):
			logger.DebugComponent("consensus", "Initial startup delay complete, beginning proposal monitoring")
		}

		logger.InfoComponent("consensus", "Proposal monitor started - tracking block proposers")

		logsDir := filepath.Join(cfg.NodeHome, "data/replica_cmds")

		tailStream(ctx, tailStreamOpts{
			component:   "consensus",
			name:        "proposal stream",
			resolve:     func() (string, error) { return utils.LatestReplicaFile(logsDir) },
			rescanEvery: 2 * time.Second,
			eofSleep:    250 * time.Millisecond,
			onLine: func(line string) {
				if err := parseProposalLine(line); err != nil {
					logger.DebugComponent("consensus", "proposal parse skip: %v", err)
				}
			},
		})
	})
}

func parseProposalLine(line string) error {
	// quick sanity-skip: logs sometimes emit plain-text lines; ignore if line doesn't start with '[' or '{'
	if len(line) == 0 || (line[0] != '[' && line[0] != '{') {
		return nil
	}

	var data map[string]interface{}
	if err := json.Unmarshal([]byte(line), &data); err != nil {
		// skip malformed JSON lines silently
		return nil
	}

	abciBlock, ok := data["abci_block"].(map[string]interface{})
	if !ok {
		return fmt.Errorf("ABCI block not found in proposal line")
	}

	proposer, ok := abciBlock["proposer"].(string)
	if !ok {
		return fmt.Errorf("proposer not found in ABCI block")
	}

	// update OpenTelemetry metric
	metrics.IncrementProposerCounter(proposer)

	logger.DebugComponent("consensus", "Proposer %s counter incremented", proposer)
	return nil
}
