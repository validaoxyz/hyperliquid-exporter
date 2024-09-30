package monitors

import (
	"bytes"
	"log"
	"os/exec"
	"strings"
	"time"

	"github.com/validaoxyz/hyperliquid-exporter/internal/config"
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
		log.Printf("Error running version command: %v", err)
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

		metrics.HLSoftwareVersionInfo.WithLabelValues(currentCommitHash, date).Set(1)
		log.Printf("Updated software version: commit=%s, date=%s", currentCommitHash, date)
	} else {
		log.Printf("Unexpected version output format: %s", versionOutput)
	}
}
