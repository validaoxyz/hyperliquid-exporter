package monitors

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/validaoxyz/hyperliquid-exporter/internal/config"
	"github.com/validaoxyz/hyperliquid-exporter/internal/logger"
	"github.com/validaoxyz/hyperliquid-exporter/internal/metrics"
	"github.com/validaoxyz/hyperliquid-exporter/internal/utils"
)

func StartProposalMonitor(ctx context.Context, cfg config.Config, errCh chan<- error) {
	metrics.RegisterSource(metrics.SourceProposal, !cfg.EnableReplicaMetrics)
	// Replica monitoring owns proposer counting when enabled. Exporter wiring
	// normally omits this monitor in that mode; keep the guard for direct users.
	if cfg.EnableReplicaMetrics {
		return
	}

	// Give the validator API monitor a short head start so proposer identities
	// are resolved against its first complete mapping generation where possible.
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
		rescanEvery: 2 * time.Second,
		eofSleep:    250 * time.Millisecond,
		resolve: func() (string, error) {
			metrics.MarkMonitorAttempt("proposal")
			metrics.MarkSourceAttempt(metrics.SourceProposal)
			path, err := utils.LatestReplicaFile(logsDir)
			if err == nil {
				return path, nil
			}
			if errors.Is(err, os.ErrNotExist) {
				metrics.MarkSourceAbsent(metrics.SourceProposal)
				return "", err
			}
			metrics.MarkSourceError(metrics.SourceProposal, metrics.SourceFailureDiscovery)
			ReportError(ctx, "proposal", errCh, fmt.Errorf("discover proposal stream: %w", err))
			return "", err
		},
		onSwitch: func(string) {
			metrics.MarkSourceReadOutcome(metrics.SourceProposal, true)
		},
		onIdle: func() {
			// EOF proves the open source is still readable. It is not a valid
			// proposal observation and must not advance observation/publication.
			metrics.MarkSourceReadOutcome(metrics.SourceProposal, true)
		},
		onLine: func(line string) {
			metrics.MarkMonitorAttempt("proposal")
			metrics.MarkSourceAttempt(metrics.SourceProposal)
			observation, proposal, err := parseProposalLine(line)
			if err != nil {
				metrics.MarkSourceError(metrics.SourceProposal, metrics.SourceFailureSchema)
				ReportError(ctx, "proposal", errCh, fmt.Errorf("parse proposal record: %w", err))
				return
			}
			if !proposal {
				return
			}
			metrics.IncrementProposerCounter(observation.proposer)
			logger.DebugComponent("consensus", "Proposer %s counter incremented", observation.proposer)
			metrics.MarkSourceValidObservation(metrics.SourceProposal, observation.sourceTime)
			metrics.MarkSourcePublication(metrics.SourceProposal)
			metrics.MarkMonitorValidObservation("proposal")
			metrics.MarkMonitorPublication("proposal")
		},
		onFailure: func(failure tailStreamFailure) {
			stage := metrics.SourceFailureRead
			switch failure {
			case tailStreamFailureOpen:
				stage = metrics.SourceFailureOpen
			case tailStreamFailureStat:
				stage = metrics.SourceFailureStat
			case tailStreamFailureRecord:
				stage = metrics.SourceFailureSchema
			}
			metrics.MarkSourceError(metrics.SourceProposal, stage)
			ReportError(ctx, "proposal", errCh, fmt.Errorf("proposal stream %s failure", failure))
		},
	})
}

type proposalObservation struct {
	proposer   string
	sourceTime time.Time
}

// parseProposalLine publishes only a structurally complete abci_block record.
// Plain-text diagnostics and unrelated JSON objects are valid stream noise and
// return proposal=false; malformed records that claim abci_block are errors.
func parseProposalLine(line string) (observation proposalObservation, proposal bool, err error) {
	trimmed := bytes.TrimSpace([]byte(line))
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return proposalObservation{}, false, nil
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &root); err != nil || root == nil {
		if err == nil {
			err = errors.New("proposal root must be an object")
		}
		return proposalObservation{}, false, err
	}
	abciRaw, exists := root["abci_block"]
	if !exists {
		return proposalObservation{}, false, nil
	}
	var abci map[string]json.RawMessage
	if err := json.Unmarshal(abciRaw, &abci); err != nil || abci == nil {
		return proposalObservation{}, true, errors.New("abci_block must be a non-null object")
	}
	proposerRaw, okProposer := abci["proposer"]
	timeRaw, okTime := abci["time"]
	if !okProposer || !okTime {
		return proposalObservation{}, true, errors.New("abci_block missing proposer or time")
	}
	var proposerID, timestamp string
	if err := unmarshalRequiredJSON(proposerRaw, &proposerID); err != nil || proposerID == "" {
		return proposalObservation{}, true, errors.New("abci_block proposer must be a non-empty string")
	}
	if err := unmarshalRequiredJSON(timeRaw, &timestamp); err != nil {
		return proposalObservation{}, true, errors.New("abci_block time must be a non-null string")
	}
	parsed, ok := parseVisorTime(timestamp)
	if !ok {
		return proposalObservation{}, true, errors.New("abci_block time is invalid")
	}
	return proposalObservation{proposer: proposerID, sourceTime: parsed}, true, nil
}
