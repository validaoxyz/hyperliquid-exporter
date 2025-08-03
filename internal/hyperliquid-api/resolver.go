package hyperliquidapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/validaoxyz/hyperliquid-exporter/internal/logger"
)

// handles all interactions with the Hyperliquid API
type Resolver struct {
	mu      sync.RWMutex
	client  *http.Client
	baseURL string
	chain   string

	// Caches
	validatorCache      *ValidatorCache
	lastValidatorUpdate time.Time
}

// stores validator summaries with update tracking
type ValidatorCache struct {
	summaries         []ValidatorSummary
	signerToValidator map[string]string            // lowercase signer -> lowercase validator
	validatorInfo     map[string]*ValidatorSummary // lowercase validator -> summary
	lastUpdate        time.Time
}

// validator info from API
type ValidatorSummary struct {
	Validator       string  `json:"validator"`
	Signer          string  `json:"signer"`
	Name            string  `json:"name"`
	Description     string  `json:"description"`
	NRecentBlocks   int     `json:"nRecentBlocks"`
	Stake           float64 `json:"stake"`
	IsJailed        bool    `json:"isJailed"`
	UnjailableAfter int64   `json:"unjailableAfter"`
	IsActive        bool    `json:"isActive"`
}

// request to the HL API
type APIRequest struct {
	Type string `json:"type"`
}

// creates a new HL API resolver
func NewResolver(chain string) *Resolver {
	var baseURL string
	if chain == "mainnet" {
		baseURL = "https://api.hyperliquid.xyz"
	} else {
		baseURL = "https://api.hyperliquid-testnet.xyz"
	}

	return &Resolver{
		client: &http.Client{
			Timeout: 30 * time.Second,
		},
		baseURL: baseURL,
		chain:   chain,
		validatorCache: &ValidatorCache{
			signerToValidator: make(map[string]string),
			validatorInfo:     make(map[string]*ValidatorSummary),
		},
	}
}

// returns val summaries, using cache if fresh
func (r *Resolver) GetValidatorSummaries(ctx context.Context, forceRefresh bool) ([]ValidatorSummary, error) {
	r.mu.RLock()
	cacheAge := time.Since(r.validatorCache.lastUpdate)
	cachedSummaries := r.validatorCache.summaries
	r.mu.RUnlock()

	// use cache if fresh (< 1 minute age) and not forcing refresh
	if !forceRefresh && cacheAge < 1*time.Minute && len(cachedSummaries) > 0 {
		logger.DebugComponent("consensus-api", "Using cached validator summaries (age: %v)", cacheAge)
		return cachedSummaries, nil
	}

	// fetch fresh data
	summaries, err := r.fetchValidatorSummaries(ctx)
	if err != nil {
		// if we have cached data and the fetch failed, return cached data
		if len(cachedSummaries) > 0 {
			logger.WarningComponent("consensus-api", "Failed to fetch validator summaries, using stale cache: %v", err)
			return cachedSummaries, nil
		}
		return nil, err
	}

	// update cache
	r.updateValidatorCache(summaries)

	return summaries, nil
}

// returns validator info by signer address
func (r *Resolver) GetValidatorBySigner(signer string) (*ValidatorSummary, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	validator, exists := r.validatorCache.signerToValidator[strings.ToLower(signer)]
	if !exists {
		return nil, false
	}

	info, exists := r.validatorCache.validatorInfo[validator]
	return info, exists
}

// returns val info by val address
func (r *Resolver) GetValidatorByAddress(validator string) (*ValidatorSummary, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	info, exists := r.validatorCache.validatorInfo[strings.ToLower(validator)]
	return info, exists
}

// returns complete signer to val mapping
func (r *Resolver) GetSignerToValidatorMapping() map[string]string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	// Return a copy to prevent external modifications
	mapping := make(map[string]string)
	for k, v := range r.validatorCache.signerToValidator {
		mapping[k] = v
	}
	return mapping
}

// makes actual API call
func (r *Resolver) fetchValidatorSummaries(ctx context.Context) ([]ValidatorSummary, error) {
	req := APIRequest{Type: "validatorSummaries"}

	var summaries []ValidatorSummary
	if err := r.makeAPICall(ctx, "/info", req, &summaries); err != nil {
		return nil, fmt.Errorf("failed to fetch validator summaries: %w", err)
	}

	return summaries, nil
}

// updates internal cache with new val data
func (r *Resolver) updateValidatorCache(summaries []ValidatorSummary) {
	r.mu.Lock()
	defer r.mu.Unlock()

	// clear and rebuild maps
	r.validatorCache.signerToValidator = make(map[string]string)
	r.validatorCache.validatorInfo = make(map[string]*ValidatorSummary)

	for i := range summaries {
		summary := &summaries[i]
		signerLower := strings.ToLower(summary.Signer)
		validatorLower := strings.ToLower(summary.Validator)

		r.validatorCache.signerToValidator[signerLower] = validatorLower
		r.validatorCache.validatorInfo[validatorLower] = summary
	}

	r.validatorCache.summaries = summaries
	r.validatorCache.lastUpdate = time.Now()
}

// generic method for API calls
func (r *Resolver) makeAPICall(ctx context.Context, endpoint string, request interface{}, response interface{}) error {
	payload, err := json.Marshal(request)
	if err != nil {
		return fmt.Errorf("failed to marshal request: %w", err)
	}

	url := r.baseURL + endpoint
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewBuffer(payload))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")

	// retry with exponential backoff
	var resp *http.Response
	var lastErr error

	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			backoff := time.Duration(attempt*attempt) * time.Second
			logger.DebugComponent("consensus-api", "Retrying API call after %v (attempt %d/3)", backoff, attempt+1)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(backoff):
			}
		}

		resp, err = r.client.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("request failed: %w", err)
			continue
		}

		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			lastErr = fmt.Errorf("API returned status %d: %s", resp.StatusCode, string(body))
			continue
		}

		// success,decode response
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return fmt.Errorf("failed to read response: %w", err)
		}

		if err := json.Unmarshal(body, response); err != nil {
			return fmt.Errorf("failed to unmarshal response: %w", err)
		}

		return nil
	}

	return fmt.Errorf("API call failed after 3 attempts: %w", lastErr)
}

// returns configured chain
func (r *Resolver) GetChain() string {
	return r.chain
}

// returns base API URL
func (r *Resolver) GetBaseURL() string {
	return r.baseURL
}
