package metrics

import (
	"fmt"
	"io"
	"net/http"
)

func getPublicIP() (string, error) {
	resp, err := http.Get("https://api.ipify.org")
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	ip, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	return string(ip), nil
}

func InitializeNodeIdentity(cfg MetricsConfig) error {
	ip, err := getPublicIP()
	if err != nil {
		return fmt.Errorf("failed to get public IP: %w", err)
	}

	metricsMutex.Lock()
	defer metricsMutex.Unlock()

	nodeIdentity = NodeIdentity{
		ServerIP: ip,
		Identity: cfg.Identity,
		Chain:    cfg.Chain,
	}

	return nil
}
