package monitors

import (
	"bytes"
	"log"
	"os"
	"os/exec"
	"time"
        "strings"

	"github.com/validaoxyz/hyperliquid-exporter/internal/metrics"
)

// StartUpdateChecker starts checking for software updates
func StartUpdateChecker() {
	go func() {
		for {
			checkSoftwareUpdate()
			time.Sleep(300 * time.Second) // 5 minutes
		}
	}()
}

func checkSoftwareUpdate() {
	url := "https://binaries.hyperliquid.xyz/Testnet/hl-visor"
	localBinaryPath := "/tmp/hl-visor-latest"

	// Download the latest binary
	cmd := exec.Command("curl", "-sSL", "-o", localBinaryPath, url)
	err := cmd.Run()
	if err != nil {
		log.Printf("Error downloading latest binary: %v", err)
		return
	}

	err = os.Chmod(localBinaryPath, 0755)
	if err != nil {
		log.Printf("Error changing permissions of latest binary: %v", err)
		return
	}

	// Get version of the latest binary
	cmd = exec.Command(localBinaryPath, "--version")
	var out bytes.Buffer
	cmd.Stdout = &out
	err = cmd.Run()
	if err != nil {
		log.Printf("Error running latest binary version command: %v", err)
		return
	}

	latestVersionOutput := out.String()
	parts := strings.Split(latestVersionOutput, "|")
	if len(parts) >= 3 {
		commitLine := parts[0]
		latestCommitParts := strings.Split(commitLine, " ")
		if len(latestCommitParts) >= 2 {
			latestCommitHash := strings.TrimSpace(latestCommitParts[1])

			if currentCommitHash == latestCommitHash {
				metrics.HLSoftwareUpToDate.Set(1)
				log.Println("Software is up to date.")
			} else {
				metrics.HLSoftwareUpToDate.Set(0)
				log.Println("Software is NOT up to date.")
			}
		}
	} else {
		log.Printf("Unexpected latest version output format: %s", latestVersionOutput)
	}

	// Clean up
	err = os.Remove(localBinaryPath)
	if err != nil {
		log.Printf("Error removing temporary binary: %v", err)
	}
}
