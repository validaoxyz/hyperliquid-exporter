package monitors

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime/debug"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/validaoxyz/hyperliquid-exporter/internal/abci"
	"github.com/validaoxyz/hyperliquid-exporter/internal/config"
	"github.com/validaoxyz/hyperliquid-exporter/internal/logger"
	"github.com/validaoxyz/hyperliquid-exporter/internal/metrics"
	"github.com/validaoxyz/hyperliquid-exporter/internal/utils"
)

const (
	validatorProbeInterval      = 30 * time.Second
	validatorProbeWorkers       = 8
	validatorProbeTimeout       = 2 * time.Second
	validatorProfileFreshness   = 2 * time.Hour
	validatorAPITargetFreshness = 15 * time.Minute
	validatorProbeValueExpiry   = 10 * time.Minute
)

var (
	validatorIPMutex       sync.RWMutex
	validatorDataByAddress = make(map[string]validatorData)
	validatorProbeCycleMu  sync.Mutex
	validatorProbeMu       sync.Mutex
	validatorProbeStates   = make(map[string]*validatorProbeState)
	validatorDialContext   = func(ctx context.Context, network, address string) (net.Conn, error) {
		return (&net.Dialer{Timeout: validatorProbeTimeout}).DialContext(ctx, network, address)
	}
)

type validatorData struct {
	Moniker  string
	IP       string
	LastSeen time.Time
}

type validatorProbeTarget struct {
	identity metrics.ValidatorIdentity
	ip       string
	moniker  string
	stake    float64
}

type validatorProbeState struct {
	target      validatorProbeTarget
	lastSuccess time.Time
	duration    time.Duration
	nextAttempt time.Time
	failures    int
}

func getValidatorData(address string) (validatorData, bool) {
	validatorIPMutex.RLock()
	data, exists := validatorDataByAddress[strings.ToLower(address)]
	validatorIPMutex.RUnlock()
	return data, exists
}

func StartValidatorIPMonitor(ctx context.Context, cfg config.Config, errCh chan<- error) {
	metrics.RegisterSource(metrics.SourceValidatorIP, true)
	reader := abci.NewReader(8)
	goSafe("validator_ip", func() {
		stateDir := filepath.Join(cfg.NodeHome, "data/periodic_abci_states")
		if _, err := os.Stat(stateDir); err != nil {
			if os.IsNotExist(err) {
				metrics.MarkSourceAbsent(metrics.SourceValidatorIP)
			} else {
				metrics.MarkSourceError(metrics.SourceValidatorIP, metrics.SourceFailureStat)
			}
			reportValidatorIPError(ctx, errCh, fmt.Errorf("state directory unavailable: %w", err))
			return
		}

		var currentFile string
		if err := processLatestState(ctx, stateDir, &currentFile, reader); err != nil {
			reportValidatorIPError(ctx, errCh, err)
		}
		goSafe("validator_ip", func() { monitorValidatorTCPConnect(ctx, errCh) })

		ticker := time.NewTicker(time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := processLatestState(ctx, stateDir, &currentFile, reader); err != nil {
					reportValidatorIPError(ctx, errCh, err)
				}
			}
		}
	})
}

func reportValidatorIPError(ctx context.Context, errCh chan<- error, err error) {
	logger.ErrorComponent("consensus", "Validator IP monitor error: %v", err)
	ReportError(ctx, "validator_ip", errCh, err)
}

func processLatestState(ctx context.Context, stateDir string, currentFile *string, reader *abci.Reader) error {
	metrics.MarkMonitorAttempt("validator_ip")
	metrics.MarkSourceAttempt(metrics.SourceValidatorIP)
	latestFile, err := utils.LatestDateNumericFile(stateDir)
	if err != nil {
		metrics.MarkSourceError(metrics.SourceValidatorIP, metrics.SourceFailureDiscovery)
		return fmt.Errorf("find latest state file: %w", err)
	}
	if latestFile == *currentFile {
		metrics.MarkSourceAvailable(metrics.SourceValidatorIP)
		return nil
	}
	info, err := os.Stat(latestFile)
	if err != nil {
		metrics.MarkSourceError(metrics.SourceValidatorIP, metrics.SourceFailureStat)
		return fmt.Errorf("stat latest state file: %w", err)
	}
	sampleTime := info.ModTime()
	if sampleTime.IsZero() || sampleTime.After(time.Now().Add(time.Minute)) {
		metrics.MarkSourceError(metrics.SourceValidatorIP, metrics.SourceFailureSchema)
		return fmt.Errorf("latest state file has invalid modification time")
	}
	profiles, err := reader.ReadValidatorProfiles(latestFile)
	if err != nil {
		metrics.MarkSourceError(metrics.SourceValidatorIP, metrics.SourceFailureDecode)
		return fmt.Errorf("read validator profiles: %w", err)
	}
	staged := make(map[string]validatorData, len(profiles))
	for i, profile := range profiles {
		address := strings.ToLower(strings.TrimSpace(profile.Address))
		if !isFullHexAddress(address) || net.ParseIP(profile.IP) == nil || strings.TrimSpace(profile.Moniker) == "" {
			metrics.MarkSourceError(metrics.SourceValidatorIP, metrics.SourceFailureSchema)
			return fmt.Errorf("invalid validator profile row %d", i)
		}
		if _, duplicate := staged[address]; duplicate {
			metrics.MarkSourceError(metrics.SourceValidatorIP, metrics.SourceFailureSchema)
			return fmt.Errorf("duplicate validator profile row %d", i)
		}
		staged[address] = validatorData{Moniker: profile.Moniker, IP: profile.IP, LastSeen: sampleTime}
	}
	validatorIPMutex.Lock()
	validatorDataByAddress = staged
	validatorIPMutex.Unlock()
	for address := range staged {
		metrics.RegisterFullAddress(address)
	}
	*currentFile = latestFile
	metrics.MarkSourceValidObservation(metrics.SourceValidatorIP, sampleTime)
	metrics.MarkSourcePublication(metrics.SourceValidatorIP)
	metrics.MarkMonitorValidObservation("validator_ip")
	metrics.MarkMonitorPublication("validator_ip")
	return nil
}

func monitorValidatorTCPConnect(ctx context.Context, _ chan<- error) {
	runValidatorProbeCycle(ctx, time.Now())
	ticker := time.NewTicker(validatorProbeInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			runValidatorProbeCycle(ctx, now)
		}
	}
}

func runValidatorProbeCycle(ctx context.Context, now time.Time) bool {
	metrics.MarkMonitorAttempt("validator_ip")
	if !validatorProbeCycleMu.TryLock() {
		return false
	}
	defer validatorProbeCycleMu.Unlock()

	targets := currentValidatorProbeTargets(now, validatorSummaryLimit)
	current := make(map[string]validatorProbeTarget, len(targets))
	for _, target := range targets {
		current[target.identity.Validator] = target
	}

	validatorProbeMu.Lock()
	for validator, state := range validatorProbeStates {
		target, exists := current[validator]
		if exists && target.identity == state.target.identity && target.ip == state.target.ip && target.moniker == state.target.moniker {
			continue
		}
		metrics.DeleteTCPConnectTarget(state.target.identity)
		delete(validatorProbeStates, validator)
	}
	validatorProbeMu.Unlock()

	jobs := make(chan validatorProbeTarget)
	var wg sync.WaitGroup
	for i := 0; i < validatorProbeWorkers; i++ {
		startValidatorProbeWorker(ctx, jobs, now, &wg, probeValidatorTarget)
	}
	for _, target := range targets {
		validatorProbeMu.Lock()
		state := validatorProbeStates[target.identity.Validator]
		due := state == nil || !now.Before(state.nextAttempt)
		validatorProbeMu.Unlock()
		if due {
			select {
			case jobs <- target:
			case <-ctx.Done():
				close(jobs)
				wg.Wait()
				return true
			}
		}
	}
	close(jobs)
	wg.Wait()
	publishLegacyTCPConnectSnapshot(now)
	return true
}

func startValidatorProbeWorker(
	ctx context.Context,
	jobs <-chan validatorProbeTarget,
	now time.Time,
	wg *sync.WaitGroup,
	probe func(context.Context, validatorProbeTarget, time.Time),
) {
	wg.Add(1)
	goSafe("validator_ip", func() {
		defer wg.Done()
		for target := range jobs {
			runValidatorProbeSafely(ctx, target, now, probe)
		}
	})
}

func runValidatorProbeSafely(
	ctx context.Context,
	target validatorProbeTarget,
	now time.Time,
	probe func(context.Context, validatorProbeTarget, time.Time),
) {
	defer func() {
		if recovered := recover(); recovered != nil {
			metrics.IncMonitorPanic("validator_ip")
			logger.ErrorComponent("validator_ip", "validator probe PANIC recovered: %v\n%s", recovered, debug.Stack())
		}
	}()
	probe(ctx, target, now)
}

func currentValidatorProbeTargets(now time.Time, limit int) []validatorProbeTarget {
	eligible, apiUpdatedAt := metrics.GetAPIActiveAndUnjailedValidators()
	if apiUpdatedAt.IsZero() || now.Sub(apiUpdatedAt) > validatorAPITargetFreshness || apiUpdatedAt.After(now.Add(time.Minute)) {
		return nil
	}
	targets := make([]validatorProbeTarget, 0, len(eligible))
	for _, row := range eligible {
		profile, ok := getValidatorData(row.Validator)
		if !ok || profile.IP == "" || now.Sub(profile.LastSeen) > validatorProfileFreshness || profile.LastSeen.After(now.Add(time.Minute)) {
			continue
		}
		targets = append(targets, validatorProbeTarget{
			identity: metrics.ValidatorIdentity{Validator: row.Validator, Signer: row.Signer, Name: row.Name, Kind: "validator"},
			ip:       profile.IP, moniker: profile.Moniker, stake: row.Stake,
		})
	}
	sort.Slice(targets, func(i, j int) bool {
		if targets[i].stake == targets[j].stake {
			return targets[i].identity.Validator < targets[j].identity.Validator
		}
		return targets[i].stake > targets[j].stake
	})
	if len(targets) > limit {
		targets = targets[:limit]
	}
	return targets
}

func probeValidatorTarget(ctx context.Context, target validatorProbeTarget, now time.Time) {
	probeCtx, cancel := context.WithTimeout(ctx, validatorProbeTimeout)
	defer cancel()
	duration, outcome := connectToValidator(probeCtx, target.ip)
	labels := []string{target.identity.Validator, target.identity.Signer, target.identity.Name, outcome}
	metrics.HLConsensusValidatorTCPConnectOutcomes.WithLabelValues(labels...).Inc()
	metrics.MarkMonitorValidObservation("validator_ip")
	metrics.MarkMonitorPublication("validator_ip")

	validatorProbeMu.Lock()
	state := validatorProbeStates[target.identity.Validator]
	if state == nil {
		state = &validatorProbeState{}
		validatorProbeStates[target.identity.Validator] = state
	}
	state.target = target
	if outcome == "success" {
		state.lastSuccess = now
		state.duration = duration
		state.failures = 0
		state.nextAttempt = now.Add(validatorProbeInterval)
		metrics.SetTCPConnectSuccess(target.identity, duration, now)
	} else {
		state.failures++
		backoff := validatorProbeInterval * time.Duration(1<<min(state.failures, 4))
		if backoff > 5*time.Minute {
			backoff = 5 * time.Minute
		}
		state.nextAttempt = now.Add(backoff)
		if !state.lastSuccess.IsZero() {
			age := now.Sub(state.lastSuccess)
			metrics.SetTCPConnectSuccessAge(target.identity, age, age <= validatorProbeValueExpiry)
		}
	}
	validatorProbeMu.Unlock()
}

func connectToValidator(ctx context.Context, ip string) (time.Duration, string) {
	lastOutcome := "other"
	sawTimeout := false
	sawUnreachable := false
	sawRefused := false
	for port := 4000; port <= 4010; port++ {
		start := time.Now()
		conn, err := validatorDialContext(ctx, "tcp", net.JoinHostPort(ip, fmt.Sprintf("%d", port)))
		if err == nil {
			duration := time.Since(start)
			_ = conn.Close()
			return duration, "success"
		}
		lastOutcome = classifyConnectError(err)
		switch lastOutcome {
		case "timeout":
			sawTimeout = true
		case "unreachable":
			sawUnreachable = true
		case "refused":
			sawRefused = true
		}
		if ctx.Err() != nil {
			return 0, "timeout"
		}
	}
	if sawTimeout {
		return 0, "timeout"
	}
	if sawUnreachable {
		return 0, "unreachable"
	}
	if sawRefused {
		return 0, "refused"
	}
	return 0, lastOutcome
}

func classifyConnectError(err error) string {
	if err == nil {
		return "success"
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return "timeout"
	}
	if errors.Is(err, syscall.ECONNREFUSED) {
		return "refused"
	}
	if errors.Is(err, syscall.ENETUNREACH) || errors.Is(err, syscall.EHOSTUNREACH) {
		return "unreachable"
	}
	return "other"
}

func publishLegacyTCPConnectSnapshot(now time.Time) {
	validatorProbeMu.Lock()
	defer validatorProbeMu.Unlock()
	legacy := make([]metrics.ValidatorTCPConnectDurationSnapshot, 0, len(validatorProbeStates))
	for _, state := range validatorProbeStates {
		if state.lastSuccess.IsZero() {
			continue
		}
		age := now.Sub(state.lastSuccess)
		available := age >= 0 && age <= validatorProbeValueExpiry
		metrics.SetTCPConnectSuccessAge(state.target.identity, age, available)
		if available {
			legacy = append(legacy, metrics.ValidatorTCPConnectDurationSnapshot{
				Validator:    state.target.identity.Validator,
				Moniker:      state.target.moniker,
				IP:           state.target.ip,
				Milliseconds: state.duration.Seconds() * 1000,
			})
		}
	}
	metrics.ReplaceValidatorTCPConnectDurations(legacy)
}
