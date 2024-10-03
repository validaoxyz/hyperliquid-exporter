package monitors

import (
	"bytes"
	"os/exec"
	"strings"
	"time"

	"github.com/validaoxyz/hyperliquid-exporter/internal/config"
	"github.com/validaoxyz/hyperliquid-exporter/internal/logger"
	"github.com/validaoxyz/hyperliquid-exporter/internal/metrics"
)

var currentCommitHash string

// StartVersionMonitor starts monitoring the software version
func StartVersionMonitor(cfg config.Config) {
	go func() {
		for {
			updateVersionInfo(cfg)
			time.Sleep(60 * time.Second)
		}
	}()
}

func updateVersionInfo(cfg config.Config) {
	cmd := exec.Command(cfg.NodeBinary, "--version")
	var out bytes.Buffer
	cmd.Stdout = &out
	err := cmd.Run()
	if err != nil {
		logger.Error("Error running version command: %v", err)
		return
	}

	versionOutput := out.String()
	parts := strings.Split(versionOutput, "|")
	if len(parts) >= 3 {
		commitLine := parts[0]
		date := strings.TrimSpace(parts[1])

		commitParts := strings.Split(commitLine, " ")
		if len(commitParts) >= 2 {
			currentCommitHash = strings.TrimSpace(commitParts[1])
		}

		// Reset the metric before setting the new value
		metrics.HLSoftwareVersionInfo.Reset()
		metrics.HLSoftwareVersionInfo.WithLabelValues(currentCommitHash, date).Set(1)
		logger.Info("Updated software version: commit=%s, date=%s", currentCommitHash, date)
	} else {
		logger.Error("Unexpected version output format: %s", versionOutput)
	}
}
