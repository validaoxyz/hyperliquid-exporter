package monitors

import (
	"bufio"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/validaoxyz/hyperliquid-exporter/internal/config"
	"github.com/validaoxyz/hyperliquid-exporter/internal/metrics"
	"github.com/validaoxyz/hyperliquid-exporter/internal/utils"
)

// StartProposalMonitor starts monitoring proposal logs
func StartProposalMonitor(cfg config.Config) {
	go func() {
		logsDir := filepath.Join(cfg.NodeHome, "hl/data/replica_cmds")
		var latestFile string
		for {
			var err error
			latestFile, err = utils.GetLatestFile(logsDir)
			if err != nil {
				log.Printf("Error finding latest log file: %v", err)
				time.Sleep(10 * time.Second)
				continue
			}

			processProposalFile(latestFile)
			time.Sleep(10 * time.Second)
		}
	}()
}

func processProposalFile(filePath string) {
	file, err := os.Open(filePath)
	if err != nil {
		log.Printf("Error opening proposal file %s: %v", filePath, err)
		return
	}
	defer file.Close()

	reader := bufio.NewReader(file)
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			time.Sleep(1 * time.Second)
			continue
		}
		parseProposalLine(line)
	}
}

func parseProposalLine(line string) {
	var data map[string]interface{}
	err := json.Unmarshal([]byte(line), &data)
	if err != nil {
		log.Printf("Error parsing proposal line: %v", err)
		return
	}

	abciBlock, ok := data["abci_block"].(map[string]interface{})
	if ok {
		proposer, ok := abciBlock["proposer"].(string)
		if ok {
			metrics.HLProposerCounter.WithLabelValues(proposer).Inc()
			log.Printf("Proposer %s counter incremented.", proposer)
		}
	}
}
