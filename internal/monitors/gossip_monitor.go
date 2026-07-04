package monitors

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/validaoxyz/hyperliquid-exporter/internal/config"
	"github.com/validaoxyz/hyperliquid-exporter/internal/logger"
	"github.com/validaoxyz/hyperliquid-exporter/internal/metrics"
)

const (
	gossipPollInterval = 30 * time.Second
)

// gossipTailBytes bounds how much of the hour-file each tick re-reads.
// Only the newest "child_peers status" line matters and those arrive about
// once a minute, so half a MiB of tail is plenty.
const gossipTailBytes = 512 * 1024

type GossipMonitor struct {
	config    *config.Config
	gossipDir string

	// size gate: skip the tick when the hour-file didn't grow
	lastPath string
	lastSize int64
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
		} else {
			metrics.MarkMonitorTick("gossip")
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
			} else {
				metrics.MarkMonitorTick("gossip")
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

	info, err := file.Stat()
	if err != nil {
		return fmt.Errorf("failed to stat gossip file: %w", err)
	}

	// the hour-file only grows; skip the tick when nothing was appended
	if filePath == m.lastPath && info.Size() == m.lastSize {
		return nil
	}

	// only the newest "child_peers status" line matters, so reading the
	// tail is enough and avoids re-parsing the whole hour every 30s
	seeked := false
	if info.Size() > gossipTailBytes {
		if _, err := file.Seek(info.Size()-gossipTailBytes, io.SeekStart); err != nil {
			return fmt.Errorf("failed to seek gossip file: %w", err)
		}
		seeked = true
	}

	// track the latest peer status
	var verifiedCount, unverifiedCount, connectionCount int64
	var lastUpdateTime time.Time

	// read line by line
	scanner := bufio.NewScanner(file)
	// child_peers lines list every connected peer and can pass 64 KiB
	scanner.Buffer(make([]byte, 256*1024), 4*1024*1024)
	skipFirst := seeked
	for scanner.Scan() {
		if skipFirst {
			// the first line after a mid-file seek is almost surely partial
			skipFirst = false
			continue
		}
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
		connections := int64(0)

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
			connections += int64(status.ConnectionCount)
		}

		// update counts with latest data
		verifiedCount = verified
		unverifiedCount = unverified
		connectionCount = connections
		lastUpdateTime = entryTime
	}

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("scanner error: %w", err)
	}

	m.lastPath, m.lastSize = filePath, info.Size()

	if !lastUpdateTime.IsZero() {
		metrics.SetP2PNonValPeerConnections(true, verifiedCount)
		metrics.SetP2PNonValPeerConnections(false, unverifiedCount)
		metrics.SetP2PNonValPeersTotal(verifiedCount + unverifiedCount)
		metrics.HLP2PNonValConnections.Set(float64(connectionCount))

		logger.DebugComponent("gossip", "Updated non-validator peer metrics: verified=%d, unverified=%d, total=%d",
			verifiedCount, unverifiedCount, verifiedCount+unverifiedCount)
	}

	return nil
}

// returns the path to the latest gossip log file
func (m *GossipMonitor) getLatestGossipLogFile() (string, error) {
	return latestHourlyFile(m.gossipDir)
}
