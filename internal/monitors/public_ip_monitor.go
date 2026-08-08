package monitors

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/validaoxyz/hyperliquid-exporter/internal/config"
	"github.com/validaoxyz/hyperliquid-exporter/internal/logger"
	"github.com/validaoxyz/hyperliquid-exporter/internal/metrics"
)

const publicIPPollInterval = 60 * time.Second

// StartPublicIPMonitor watches $NODE_HOME/last_known_public_ip.json.
// The file is a single JSON-encoded string, e.g. "203.0.113.51", and
// hl-node writes it when recording its public address. The mtime is an
// observed file timestamp only; no universal rewrite cadence is promised.
// Three signals:
//
//   - the literal IP, as a label on hl_node_public_ip_info{ip="..."} == 1
//   - the file's mtime age, as hl_node_public_ip_age_seconds
//   - a counter that ticks every time the content changes
func StartPublicIPMonitor(ctx context.Context, cfg config.Config, errCh chan<- error) {
	path := filepath.Join(cfg.NodeHome, "last_known_public_ip.json")
	logger.InfoComponent("public_ip", "watching %s", path)
	metrics.RegisterSource(metrics.SourcePublicIP, true)

	var currentIP string
	ticker := time.NewTicker(publicIPPollInterval)
	defer ticker.Stop()

	if err := tickPublicIP(path, &currentIP, time.Now()); err != nil {
		logger.DebugComponent("public_ip", "initial poll: %v", err)
	}

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := tickPublicIP(path, &currentIP, time.Now()); err != nil {
				logger.DebugComponent("public_ip", "poll: %v", err)
			}
		}
	}
}

func tickPublicIP(path string, currentIP *string, now time.Time) error {
	metrics.MarkMonitorAttempt("public_ip")
	metrics.MarkSourceAttempt(metrics.SourcePublicIP)
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			metrics.WithPrometheusSnapshotUpdate(func() {
				if *currentIP != "" {
					metrics.HLNodePublicIPInfo.DeleteLabelValues(*currentIP)
					*currentIP = ""
				}
				metrics.HLNodePublicIPAgeSeconds.DeleteLabelValues()
				metrics.MarkSourceAbsent(metrics.SourcePublicIP)
				metrics.MarkSourcePublication(metrics.SourcePublicIP)
				metrics.MarkMonitorPublication("public_ip")
			})
			return nil
		}
		metrics.WithPrometheusSnapshotUpdate(func() {
			metrics.MarkSourceError(metrics.SourcePublicIP, metrics.SourceFailureStat)
		})
		return fmt.Errorf("stat public-IP file: %w", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		metrics.WithPrometheusSnapshotUpdate(func() {
			metrics.MarkSourceError(metrics.SourcePublicIP, metrics.SourceFailureRead)
		})
		return fmt.Errorf("read public-IP file: %w", err)
	}
	ip, ok := parsePublicIP(data)
	if !ok || ip == "" {
		metrics.WithPrometheusSnapshotUpdate(func() {
			metrics.MarkSourceError(metrics.SourcePublicIP, metrics.SourceFailureSchema)
		})
		return fmt.Errorf("invalid public-IP file")
	}
	age := now.Sub(info.ModTime())
	if age < 0 {
		age = 0
	}
	oldIP := *currentIP
	metrics.WithPrometheusSnapshotUpdate(func() {
		metrics.HLNodePublicIPAgeSeconds.WithLabelValues().Set(age.Seconds())
		if ip != *currentIP {
			if *currentIP != "" {
				metrics.HLNodePublicIPInfo.DeleteLabelValues(*currentIP)
				metrics.HLNodePublicIPChangesTotal.Inc()
			}
			*currentIP = ip
			metrics.HLNodePublicIPInfo.WithLabelValues(ip).Set(1)
		}
		metrics.MarkSourceValidObservation(metrics.SourcePublicIP, info.ModTime())
		metrics.MarkSourcePublication(metrics.SourcePublicIP)
		metrics.MarkMonitorValidObservation("public_ip")
		metrics.MarkMonitorPublication("public_ip")
	})
	if oldIP != ip {
		if oldIP == "" {
			logger.InfoComponent("public_ip", "initial public IP: %s", ip)
		} else {
			logger.InfoComponent("public_ip", "public IP changed: %s -> %s", oldIP, ip)
		}
	}
	return nil
}

// parsePublicIP accepts either a JSON-encoded string (the canonical
// representation hl-node uses, e.g. `"203.0.113.51"`) or a bare string
// without quotes (defensive — the format could plausibly drift).
func parsePublicIP(data []byte) (string, bool) {
	s := strings.TrimSpace(string(data))
	if s == "" {
		return "", false
	}
	var ip string
	if err := json.Unmarshal([]byte(s), &ip); err == nil {
		if net.ParseIP(ip) == nil {
			return "", false
		}
		return ip, true
	}
	// Fall back: strip wrapping quotes if any, return whatever's left.
	s = strings.Trim(s, "\"'")
	if s == "" {
		return "", false
	}
	if net.ParseIP(s) == nil {
		return "", false
	}
	return s, true
}
