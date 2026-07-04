package metrics

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/validaoxyz/hyperliquid-exporter/internal/logger"
)

// getPublicIP resolves this node's public IP for the server_ip OTLP
// resource attribute. hl-node already writes its detected public IP to
// $NODE_HOME/last_known_public_ip.json, so prefer that (no egress
// needed). Fall back to api.ipify.org with a short timeout for hosts
// where the file doesn't exist yet. Returns "" when neither works.
func getPublicIP(nodeHome string) string {
	if nodeHome != "" {
		raw, err := os.ReadFile(filepath.Join(nodeHome, "last_known_public_ip.json"))
		if err == nil {
			var ip string
			if json.Unmarshal(raw, &ip) == nil && ip != "" {
				return ip
			}
			if s := strings.Trim(strings.TrimSpace(string(raw)), "\""); s != "" {
				return s
			}
		}
	}

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get("https://api.ipify.org")
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	ip, err := io.ReadAll(io.LimitReader(resp.Body, 64))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(ip))
}

// InitializeNodeIdentity fills the identity attached to OTLP exports.
// The public IP is best-effort and must never block startup: server_ip
// is a cosmetic attribute and egress-restricted validators cannot reach
// ipify at all.
func InitializeNodeIdentity(cfg MetricsConfig) error {
	ip := getPublicIP(cfg.NodeHome)
	if ip == "" {
		logger.Warning("Could not determine public IP (no %s/last_known_public_ip.json, ipify unreachable); server_ip attribute will be empty", cfg.NodeHome)
	}

	metricsMutex.Lock()
	defer metricsMutex.Unlock()

	nodeIdentity = NodeIdentity{
		ServerIP:         ip,
		Alias:            cfg.Alias,
		Chain:            cfg.Chain,
		ValidatorAddress: cfg.ValidatorAddress,
		IsValidator:      cfg.IsValidator,
	}

	return nil
}
