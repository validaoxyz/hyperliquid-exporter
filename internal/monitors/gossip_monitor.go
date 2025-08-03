package monitors

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/validaoxyz/hyperliquid-exporter/internal/config"
	"github.com/validaoxyz/hyperliquid-exporter/internal/logger"
	"github.com/validaoxyz/hyperliquid-exporter/internal/metrics"
	"github.com/validaoxyz/hyperliquid-exporter/internal/utils"
)

const (
	gossipPollInterval = 30 * time.Second
)

type GossipMonitor struct {
	config    *config.Config
	gossipDir string
}

type PeerInfo struct {
	IP string `json:"Ip"`
}

type PeerStatus struct {
	Verified        bool `json:"verified"`
	ConnectionCount int  `json:"connection_count"`
}

func NewGossipMonitor(cfg *config.Config) *GossipMonitor {
	return &GossipMonitor{
		config:    cfg,
		gossipDir: filepath.Join(cfg.NodeHome, "data", "node_logs", "gossip_rpc", "hourly"),
	}
}

func StartGossipMonitor(ctx context.Context, cfg *config.Config, errCh chan<- error) {
	m := NewGossipMonitor(cfg)

	// Check if gossip directory exists
	if _, err := os.Stat(m.gossipDir); os.IsNotExist(err) {
		logger.InfoComponent("gossip", "Gossip RPC directory not found, monitoring disabled: %s", m.gossipDir)
		return
	}

	logger.InfoComponent("gossip", "Starting gossip monitor")
	m.monitorGossipLogs(ctx, errCh)
}

func (m *GossipMonitor) monitorGossipLogs(ctx context.Context, errCh chan<- error) {
	ticker := time.NewTicker(gossipPollInterval)
	defer ticker.Stop()

	var lastProcessedFile string

	// process immediately on startup
	filePath, err := m.getLatestGossipLogFile()
	if err == nil && filePath != "" {
		logger.InfoComponent("gossip", "First run: processing gossip file %s", filePath)
		if err := m.processGossipFile(filePath); err != nil {
			logger.ErrorComponent("gossip", "Initial processing error: %v", err)
		}
		lastProcessedFile = filePath
	}

	for {
		select {
		case <-ctx.Done():
			logger.InfoComponent("gossip", "Gossip monitor shutting down")
			return
		case <-ticker.C:
			filePath, err := m.getLatestGossipLogFile()
			if err != nil {
				logger.ErrorComponent("gossip", "Error getting latest gossip file: %v", err)
				continue
			}

			if filePath != lastProcessedFile && filePath != "" {
				logger.InfoComponent("gossip", "Switching to new gossip file: %s", filePath)
				lastProcessedFile = filePath
			}

			if err := m.processGossipFile(filePath); err != nil {
				logger.ErrorComponent("gossip", "Error processing gossip file: %v", err)
				select {
				case errCh <- fmt.Errorf("gossip monitor: %w", err):
				case <-ctx.Done():
					return
				}
			}
		}
	}
}

func (m *GossipMonitor) processGossipFile(filePath string) error {
	if filePath == "" {
		return fmt.Errorf("empty file path")
	}

	file, err := os.Open(filePath)
	if err != nil {
		return fmt.Errorf("failed to open gossip file: %w", err)
	}
	defer file.Close()

	// track the latest peer status
	var verifiedCount, unverifiedCount int64
	var lastUpdateTime time.Time

	// read line by line
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Bytes()

		// parse JSON array
		var entry []json.RawMessage
		if err := json.Unmarshal(line, &entry); err != nil {
			continue // skip malformed lines
		}

		if len(entry) != 2 {
			continue
		}

		// parse timestamp
		var timestamp string
		if err := json.Unmarshal(entry[0], &timestamp); err != nil {
			continue
		}

		entryTime, err := time.Parse("2006-01-02T15:04:05.999999999", timestamp)
		if err != nil {
			continue
		}

		var eventData []json.RawMessage
		if err := json.Unmarshal(entry[1], &eventData); err != nil {
			continue
		}

		if len(eventData) != 2 {
			continue
		}

		// check if it's a child_peers status event
		var eventType string
		if err := json.Unmarshal(eventData[0], &eventType); err != nil {
			continue
		}

		if eventType != "child_peers status" {
			continue
		}

		// parse peer list
		var peerList [][]json.RawMessage
		if err := json.Unmarshal(eventData[1], &peerList); err != nil {
			continue
		}

		// count peers by verification status
		verified := int64(0)
		unverified := int64(0)

		for _, peer := range peerList {
			if len(peer) != 2 {
				continue
			}

			// parse peer status
			var status PeerStatus
			if err := json.Unmarshal(peer[1], &status); err != nil {
				continue
			}

			if status.Verified {
				verified++
			} else {
				unverified++
			}
		}

		// update counts with latest data
		verifiedCount = verified
		unverifiedCount = unverified
		lastUpdateTime = entryTime
	}

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("scanner error: %w", err)
	}

	if !lastUpdateTime.IsZero() {
		metrics.SetP2PNonValPeerConnections(true, verifiedCount)
		metrics.SetP2PNonValPeerConnections(false, unverifiedCount)
		metrics.SetP2PNonValPeersTotal(verifiedCount + unverifiedCount)

		logger.DebugComponent("gossip", "Updated non-validator peer metrics: verified=%d, unverified=%d, total=%d",
			verifiedCount, unverifiedCount, verifiedCount+unverifiedCount)
	}

	return nil
}

// processes the latest gossip log file (kept for compatibility)
func (m *GossipMonitor) processLatestGossipFile() error {
	filePath, err := m.getLatestGossipLogFile()
	if err != nil {
		return fmt.Errorf("failed to get latest gossip file: %w", err)
	}
	return m.processGossipFile(filePath)
}

// returns the path to the latest gossip log file
func (m *GossipMonitor) getLatestGossipLogFile() (string, error) {
	// get today's directory
	today := time.Now().Format("20060102")
	todayDir := filepath.Join(m.gossipDir, today)

	// check if today's directory exists
	if _, err := os.Stat(todayDir); os.IsNotExist(err) {
		// fallback to getting the latest file in the gossip directory
		return utils.GetLatestFile(m.gossipDir)
	}

	// get latest file in today's directory
	return utils.GetLatestFile(todayDir)
}
