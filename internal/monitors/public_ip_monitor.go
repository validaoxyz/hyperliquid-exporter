package monitors

import (
	"context"
	"encoding/json"
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
// hl-node rewrites it every ~13 minutes as a heartbeat (with the same
// value if nothing changed). Three signals:
//
//   - the literal IP, as a label on hl_node_public_ip_info{ip="..."} == 1
//   - the file's mtime age, as hl_node_public_ip_age_seconds
//   - a counter that ticks every time the content changes
func StartPublicIPMonitor(ctx context.Context, cfg config.Config, errCh chan<- error) {
	path := filepath.Join(cfg.NodeHome, "last_known_public_ip.json")
	if _, err := os.Stat(path); err != nil {
		logger.InfoComponent("public_ip",
			"last_known_public_ip.json not present (%s); monitor idle", path)
		<-ctx.Done()
		return
	}

	logger.InfoComponent("public_ip", "watching %s", path)

	var currentIP string
	ticker := time.NewTicker(publicIPPollInterval)
	defer ticker.Stop()

	tickPublicIP(path, &currentIP)
	metrics.MarkMonitorTick("public_ip")

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			tickPublicIP(path, &currentIP)
			metrics.MarkMonitorTick("public_ip")
		}
	}
}

func tickPublicIP(path string, currentIP *string) {
	info, err := os.Stat(path)
	if err != nil {
		return
	}
	metrics.HLNodePublicIPAgeSeconds.Set(time.Since(info.ModTime()).Seconds())

	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	ip, ok := parsePublicIP(data)
	if !ok || ip == "" {
		return
	}
	if ip == *currentIP {
		return
	}
	if *currentIP != "" {
		metrics.HLNodePublicIPInfo.DeleteLabelValues(*currentIP)
		metrics.HLNodePublicIPChangesTotal.Inc()
		logger.InfoComponent("public_ip", "public IP changed: %s -> %s", *currentIP, ip)
	} else {
		logger.InfoComponent("public_ip", "initial public IP: %s", ip)
	}
	*currentIP = ip
	metrics.HLNodePublicIPInfo.WithLabelValues(ip).Set(1)
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
		return ip, true
	}
	// Fall back: strip wrapping quotes if any, return whatever's left.
	s = strings.Trim(s, "\"'")
	if s == "" {
		return "", false
	}
	return s, true
}
