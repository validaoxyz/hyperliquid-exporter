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

	"github.com/validaoxyz/hyperliquid-exporter/internal/config"
	"github.com/validaoxyz/hyperliquid-exporter/internal/logger"
)

// handles all interactions with the Hyperliquid API
type Resolver struct {
	mu                         sync.RWMutex
	client                     *http.Client
	baseURL                    string
	chain                      string
	now                        func() time.Time
	wait                       func(context.Context, time.Duration) error
	validateValidatorSummaries func([]ValidatorSummary) error

	// Caches
	validatorCache *ValidatorCache
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
	Commission      string  `json:"commission"`
	// Stats holds [period, {uptimeFraction, predictedApr, nSamples}] pairs
	// for day/week/month.
	Stats [][]json.RawMessage `json:"stats"`
}

// UnmarshalJSON distinguishes required zero/false values from missing or null
// fields. encoding/json otherwise accepts null into primitive destinations and
// silently leaves the zero value, which can corrupt the active/jailed/stake
// generation while still passing downstream domain checks.
func (s *ValidatorSummary) UnmarshalJSON(data []byte) error {
	var wire struct {
		Validator       json.RawMessage     `json:"validator"`
		Signer          json.RawMessage     `json:"signer"`
		Name            string              `json:"name"`
		Description     string              `json:"description"`
		NRecentBlocks   json.RawMessage     `json:"nRecentBlocks"`
		Stake           json.RawMessage     `json:"stake"`
		IsJailed        json.RawMessage     `json:"isJailed"`
		UnjailableAfter json.RawMessage     `json:"unjailableAfter"`
		IsActive        json.RawMessage     `json:"isActive"`
		Commission      string              `json:"commission"`
		Stats           [][]json.RawMessage `json:"stats"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}

	var decoded ValidatorSummary
	required := []struct {
		name string
		raw  json.RawMessage
		dst  interface{}
	}{
		{"validator", wire.Validator, &decoded.Validator},
		{"signer", wire.Signer, &decoded.Signer},
		{"nRecentBlocks", wire.NRecentBlocks, &decoded.NRecentBlocks},
		{"stake", wire.Stake, &decoded.Stake},
		{"isJailed", wire.IsJailed, &decoded.IsJailed},
		{"unjailableAfter", wire.UnjailableAfter, &decoded.UnjailableAfter},
		{"isActive", wire.IsActive, &decoded.IsActive},
	}
	for _, field := range required {
		if err := unmarshalRequiredField(field.raw, field.dst); err != nil {
			return fmt.Errorf("invalid required validator summary field %s: %w", field.name, err)
		}
	}
	decoded.Name = wire.Name
	decoded.Description = wire.Description
	decoded.Commission = wire.Commission
	decoded.Stats = wire.Stats
	*s = decoded
	return nil
}

func unmarshalRequiredField(raw json.RawMessage, dst interface{}) error {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return fmt.Errorf("value is missing or null")
	}
	return json.Unmarshal(trimmed, dst)
}

// request to the HL API
type APIRequest struct {
	Type string `json:"type"`
}

type FailureStage string

const (
	FailureRequest FailureStage = "request"
	FailureStatus  FailureStage = "status"
	FailureRead    FailureStage = "read"
	FailureDecode  FailureStage = "decode"
	FailureSchema  FailureStage = "schema"
)

// CallError preserves a bounded failure stage for source-health metrics while
// retaining the underlying error for logs and errors.Is/errors.As.
type CallError struct {
	Stage FailureStage
	Err   error
}

func (e *CallError) Error() string { return e.Err.Error() }
func (e *CallError) Unwrap() error { return e.Err }

// ValidatorSummariesResult makes cache provenance explicit. A stale fallback
// always carries RefreshError and never impersonates a successful refresh.
type ValidatorSummariesResult struct {
	Summaries    []ValidatorSummary
	LastSuccess  time.Time
	FromCache    bool
	Stale        bool
	RefreshError error
}

const validatorCacheTTL = time.Minute

// creates a new HL API resolver
func NewResolver(chain string) (*Resolver, error) {
	chain, err := config.NormalizeChain(chain)
	if err != nil {
		return nil, err
	}

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
		now:     time.Now,
		wait: func(ctx context.Context, delay time.Duration) error {
			timer := time.NewTimer(delay)
			defer timer.Stop()
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-timer.C:
				return nil
			}
		},
		validatorCache: &ValidatorCache{
			signerToValidator: make(map[string]string),
			validatorInfo:     make(map[string]*ValidatorSummary),
		},
	}, nil
}

// returns val summaries, using cache if fresh
func (r *Resolver) GetValidatorSummaries(ctx context.Context, forceRefresh bool) (ValidatorSummariesResult, error) {
	r.mu.RLock()
	lastSuccess := r.validatorCache.lastUpdate
	cachedSummaries := cloneValidatorSummaries(r.validatorCache.summaries)
	validate := r.validateValidatorSummaries
	r.mu.RUnlock()

	// use cache if fresh (< 1 minute age) and not forcing refresh
	cacheAge := r.now().Sub(lastSuccess)
	if !forceRefresh && cacheAge < validatorCacheTTL && len(cachedSummaries) > 0 {
		logger.DebugComponent("consensus-api", "Using cached validator summaries (age: %v)", cacheAge)
		return ValidatorSummariesResult{
			Summaries:   cachedSummaries,
			LastSuccess: lastSuccess,
			FromCache:   true,
		}, nil
	}

	// fetch fresh data
	summaries, err := r.fetchValidatorSummaries(ctx)
	if err == nil && validate != nil {
		if validationErr := validate(summaries); validationErr != nil {
			err = &CallError{
				Stage: FailureSchema,
				Err:   fmt.Errorf("validator summaries schema validation failed: %w", validationErr),
			}
		}
	}
	if err != nil {
		// if we have cached data and the fetch failed, return cached data
		if len(cachedSummaries) > 0 {
			logger.WarningComponent("consensus-api", "Failed to fetch validator summaries, using stale cache: %v", err)
			return ValidatorSummariesResult{
				Summaries:    cachedSummaries,
				LastSuccess:  lastSuccess,
				FromCache:    true,
				Stale:        true,
				RefreshError: err,
			}, nil
		}
		return ValidatorSummariesResult{}, err
	}

	// update cache
	lastSuccess = r.updateValidatorCache(summaries)

	return ValidatorSummariesResult{
		Summaries:   cloneValidatorSummaries(summaries),
		LastSuccess: lastSuccess,
	}, nil
}

// SetValidatorSummariesValidator installs the complete-snapshot validator
// used before replacing the last-known-good cache. It must be set during
// resolver construction, before concurrent reads begin.
func (r *Resolver) SetValidatorSummariesValidator(validate func([]ValidatorSummary) error) {
	r.mu.Lock()
	r.validateValidatorSummaries = validate
	r.mu.Unlock()
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
	if !exists {
		return nil, false
	}
	copy := *info
	copy.Stats = cloneSummaryStats(info.Stats)
	return &copy, true
}

// returns val info by val address
func (r *Resolver) GetValidatorByAddress(validator string) (*ValidatorSummary, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	info, exists := r.validatorCache.validatorInfo[strings.ToLower(validator)]
	if !exists {
		return nil, false
	}
	copy := *info
	copy.Stats = cloneSummaryStats(info.Stats)
	return &copy, true
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
	if summaries == nil {
		return nil, &CallError{
			Stage: FailureSchema,
			Err:   fmt.Errorf("validator summaries response is null"),
		}
	}

	return summaries, nil
}

// updates internal cache with new val data
func (r *Resolver) updateValidatorCache(summaries []ValidatorSummary) time.Time {
	r.mu.Lock()
	defer r.mu.Unlock()

	cached := cloneValidatorSummaries(summaries)

	// clear and rebuild maps
	r.validatorCache.signerToValidator = make(map[string]string)
	r.validatorCache.validatorInfo = make(map[string]*ValidatorSummary)

	for i := range cached {
		summary := &cached[i]
		signerLower := strings.ToLower(summary.Signer)
		validatorLower := strings.ToLower(summary.Validator)

		r.validatorCache.signerToValidator[signerLower] = validatorLower
		r.validatorCache.validatorInfo[validatorLower] = summary
	}

	r.validatorCache.summaries = cached
	r.validatorCache.lastUpdate = r.now()
	return r.validatorCache.lastUpdate
}

func cloneValidatorSummaries(in []ValidatorSummary) []ValidatorSummary {
	out := make([]ValidatorSummary, len(in))
	for i := range in {
		out[i] = in[i]
		out[i].Stats = cloneSummaryStats(in[i].Stats)
	}
	return out
}

func cloneSummaryStats(in [][]json.RawMessage) [][]json.RawMessage {
	out := make([][]json.RawMessage, len(in))
	for i := range in {
		out[i] = make([]json.RawMessage, len(in[i]))
		for j := range in[i] {
			out[i][j] = append(json.RawMessage(nil), in[i][j]...)
		}
	}
	return out
}

// generic method for API calls
func (r *Resolver) makeAPICall(ctx context.Context, endpoint string, request interface{}, response interface{}) error {
	payload, err := json.Marshal(request)
	if err != nil {
		return &CallError{Stage: FailureRequest, Err: fmt.Errorf("failed to marshal request: %w", err)}
	}

	url := r.baseURL + endpoint

	// retry with exponential backoff
	var lastErr error

	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			backoff := time.Duration(attempt*attempt) * time.Second
			logger.DebugComponent("consensus-api", "Retrying API call after %v (attempt %d/3)", backoff, attempt+1)
			if err := r.wait(ctx, backoff); err != nil {
				return &CallError{Stage: FailureRequest, Err: err}
			}
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
		if err != nil {
			return &CallError{Stage: FailureRequest, Err: fmt.Errorf("failed to create request: %w", err)}
		}
		req.Header.Set("Content-Type", "application/json")

		resp, err := r.client.Do(req)
		if err != nil {
			lastErr = &CallError{Stage: FailureRequest, Err: fmt.Errorf("request failed: %w", err)}
			continue
		}

		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			lastErr = &CallError{
				Stage: FailureStatus,
				Err:   fmt.Errorf("API returned status %d: %s", resp.StatusCode, string(body)),
			}
			continue
		}

		// success,decode response
		body, err := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if err != nil {
			return &CallError{Stage: FailureRead, Err: fmt.Errorf("failed to read response: %w", err)}
		}

		if err := json.Unmarshal(body, response); err != nil {
			return &CallError{Stage: FailureDecode, Err: fmt.Errorf("failed to unmarshal response: %w", err)}
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
