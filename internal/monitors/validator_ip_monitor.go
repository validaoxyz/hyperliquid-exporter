package monitors

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/validaoxyz/hyperliquid-exporter/internal/abci"
	"github.com/validaoxyz/hyperliquid-exporter/internal/cache"
	"github.com/validaoxyz/hyperliquid-exporter/internal/config"
	"github.com/validaoxyz/hyperliquid-exporter/internal/logger"
	"github.com/validaoxyz/hyperliquid-exporter/internal/metrics"
	"github.com/validaoxyz/hyperliquid-exporter/internal/utils"
)

var (
	validatorIPMutex sync.RWMutex
	// use LRU cache to store all validator data together
	validatorDataCache *cache.LRUCache
)

// validator data
type validatorData struct {
	Moniker  string
	IP       string
	Stake    float64
	LastSeen time.Time
}

type validatorWithStake struct {
	address string
	stake   float64
}

// helper functions for cache access
func getValidatorData(address string) (*validatorData, bool) {
	if data, exists := validatorDataCache.Get(address); exists {
		return data.(*validatorData), true
	}
	return nil, false
}

func setValidatorData(address string, data *validatorData) {
	validatorDataCache.Set(address, data)
}

func deleteValidatorData(address string) {
	validatorDataCache.Delete(address)
}

func StartValidatorIPMonitor(ctx context.Context, cfg config.Config, errCh chan<- error) {
	// init LRU cache for validator data
	validatorDataCache = cache.NewLRUCache(1000, 24*time.Hour)

	// create ABCI reader with 8MB buffer
	reader := abci.NewReader(8)

	go func() {
		stateDir := filepath.Join(cfg.NodeHome, "data/periodic_abci_states")

		// add directory check
		if _, err := os.Stat(stateDir); os.IsNotExist(err) {
			logger.ErrorComponent("consensus", "State directory does not exist: %s", stateDir)
			errCh <- fmt.Errorf("state directory does not exist: %s", stateDir)
			return
		}

		var currentFile string
		var lastAttempt time.Time

		// start the ping monitoring in a separate goroutine
		go monitorValidatorRTT(ctx, errCh)

		// initial attempt
		if err := processLatestState(ctx, stateDir, &currentFile, reader); err != nil {
			logger.ErrorComponent("consensus", "Initial state processing failed: %v", err)
			errCh <- err
		}
		lastAttempt = time.Now()

		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				// only retry if it's been at least an hour since last attempt
				if time.Since(lastAttempt) < time.Hour {
					continue
				}

				if err := processLatestState(ctx, stateDir, &currentFile, reader); err != nil {
					logger.ErrorComponent("consensus", "State processing failed: %v", err)
					errCh <- err
				}
				lastAttempt = time.Now()
			}
		}
	}()
}

func processLatestState(ctx context.Context, stateDir string, currentFile *string, reader *abci.Reader) error {
	latestFile, err := utils.GetLatestFile(stateDir)
	if err != nil {
		logger.ErrorComponent("consensus", "Error finding latest state file in dir %s: %v", stateDir, err)
		return fmt.Errorf("error finding latest state file: %w", err)
	}

	logger.InfoComponent("consensus", "Processing state file: %s", latestFile)

	if latestFile == *currentFile {
		return nil
	}

	// use native ABCI reader to extract validator profiles
	profiles, err := reader.ReadValidatorProfiles(latestFile)
	if err != nil {
		logger.ErrorComponent("consensus", "Error reading validator profiles: %v", err)
		return fmt.Errorf("error reading validator profiles: %w", err)
	}

	if len(profiles) == 0 {
		// no profiles found - this is normal in many cases
		return nil
	}

	validatorIPMutex.Lock()
	// keep track of current validators
	currentValidators := make(map[string]bool)
	now := time.Now()

	for _, profile := range profiles {
		// register the full validator address for expansion
		metrics.RegisterFullAddress(profile.Address)

		// update validator data in cache
		data := &validatorData{
			Moniker:  profile.Moniker,
			IP:       profile.IP,
			Stake:    0, // Will be updated separately
			LastSeen: now,
		}
		if existingData, exists := getValidatorData(profile.Address); exists {
			// preserve stake if it exists
			data.Stake = existingData.Stake
		}
		setValidatorData(profile.Address, data)
		currentValidators[profile.Address] = true
		logger.DebugComponent("consensus", "Found validator: %s, IP: %s, Name: %s", profile.Address, profile.IP, profile.Moniker)
	}

	// LRU cache will automatically handle cleanup based on size and TTL
	// no need for manual cleanup of old entries
	validatorIPMutex.Unlock()

	logger.InfoComponent("consensus", "Updated validator cache - Size: %d", validatorDataCache.Len())
	*currentFile = latestFile
	return nil
}

func monitorValidatorRTT(ctx context.Context, errCh chan<- error) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			for _, validator := range getTopValidators(50) {
				if data, exists := getValidatorData(validator); exists {
					go measureRTT(ctx, validator, data.IP)
				}
			}
		}
	}
}

func measureRTT(ctx context.Context, validator, ip string) {
	if ip == "" {
		return
	}

	moniker := ""
	if data, exists := getValidatorData(validator); exists {
		moniker = data.Moniker
	}

	success := false
	var latency float64

	for port := 4000; port <= 4010; port++ {
		start := time.Now()
		address := fmt.Sprintf("%s:%d", ip, port)

		conn, err := net.DialTimeout("tcp", address, 2*time.Second)
		if err == nil {
			latency = float64(time.Since(start).Microseconds())
			success = true
			conn.Close()
			break
		}
	}

	if success && latency > 0 && validator != "" && moniker != "" && ip != "" {
		metrics.SetValidatorRTT(validator, moniker, ip, latency)
	}
}

func UpdateValidatorStake(validator string, stake float64) {
	validatorIPMutex.Lock()
	defer validatorIPMutex.Unlock()

	if data, exists := getValidatorData(validator); exists {
		data.Stake = stake
		setValidatorData(validator, data)
	} else {
		// create new entry with just stake
		data := &validatorData{
			Stake:    stake,
			LastSeen: time.Now(),
		}
		setValidatorData(validator, data)
	}
}

func getTopValidators(n int) []string {
	validatorIPMutex.RLock()
	defer validatorIPMutex.RUnlock()

	stakes := metrics.GetValidatorStakes()
	validators := make([]validatorWithStake, 0, len(stakes))

	// only include validators that have IPs
	for addr, stake := range stakes {
		if data, exists := getValidatorData(addr); exists && data.IP != "" {
			validators = append(validators, validatorWithStake{addr, stake})
		}
	}

	// sort by stake in descending order
	sort.Slice(validators, func(i, j int) bool {
		return validators[i].stake > validators[j].stake
	})

	// get top N validators
	count := min(n, len(validators))
	topValidators := make([]string, count)
	for i := 0; i < count; i++ {
		topValidators[i] = validators[i].address
	}

	return topValidators
}
