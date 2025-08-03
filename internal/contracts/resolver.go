package contracts

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/validaoxyz/hyperliquid-exporter/internal/cache"
	"github.com/validaoxyz/hyperliquid-exporter/internal/logger"
)

type ContractInfo struct {
	Address string
	Name    string
	IsToken bool
	Type    string
	Symbol  string // only for tokens
}

// handles contract name resolution WITH caching
type Resolver struct {
	mu      sync.RWMutex
	cache   *cache.LRUCache
	client  *http.Client
	baseURL string

	// worker pool for async fetches
	fetchQueue chan string
	wg         sync.WaitGroup
	ctx        context.Context
	cancel     context.CancelFunc
}

// represents the API response for a token
type TokenResponse struct {
	Address string `json:"address"`
	Name    string `json:"name"`
	Symbol  string `json:"symbol"`
	Type    string `json:"type"`
}

// API response for a contract
type SmartContractResponse struct {
	Address struct {
		Hash string `json:"hash"`
		Name string `json:"name"`
	} `json:"address"`
}

// paginated token list response
type TokenListResponse struct {
	Items          []TokenResponse `json:"items"`
	NextPageParams *struct {
		ItemsCount int    `json:"items_count"`
		Hash       string `json:"hash"`
	} `json:"next_page_params"`
}

// paginated smart contract list response
type SmartContractListResponse struct {
	Items []struct {
		Address struct {
			Hash string `json:"hash"`
			Name string `json:"name"`
		} `json:"address"`
	} `json:"items"`
	NextPageParams *struct {
		ItemsCount       int    `json:"items_count"`
		Hash             string `json:"hash"`
		TransactionCount int    `json:"transaction_count"`
	} `json:"next_page_params"`
}

// creates a new contract resolver
func NewResolver() *Resolver {
	ctx, cancel := context.WithCancel(context.Background())
	r := &Resolver{
		cache: cache.NewLRUCache(5000, 24*time.Hour), // Cache up to 5000 contracts with 24h TTL
		client: &http.Client{
			Timeout: 10 * time.Second,
		},
		baseURL:    "https://www.hyperscan.com/api/v2",
		fetchQueue: make(chan string, 1000), // Buffer up to 1000 pending fetches
		ctx:        ctx,
		cancel:     cancel,
	}

	// start worker pool with limited concurrency
	const numWorkers = 5
	for i := 0; i < numWorkers; i++ {
		r.wg.Add(1)
		go r.fetchWorker()
	}

	logger.InfoComponent("contracts", "Started contract resolver with %d workers", numWorkers)

	return r
}

// preload only most common contracts and tokens at startup
func (r *Resolver) Initialize(ctx context.Context) error {
	logger.InfoComponent("contracts", "Initializing contract resolver with LRU cache (max 5000 entries)")

	// preload only the first page of tokens to have some initial data
	// rest will be loaded on demand
	tokenCount, err := r.preloadInitialTokens(ctx)
	if err != nil {
		logger.ErrorComponent("contracts", "Failed to preload initial tokens: %v", err)
		// continue anyway we can still fetch on-demand
	} else {
		logger.InfoComponent("contracts", "Pre-loaded %d initial tokens", tokenCount)
	}

	logger.InfoComponent("contracts", "Contract resolver initialized")
	return nil
}

// fetches only first page of tokens from API
func (r *Resolver) preloadInitialTokens(ctx context.Context) (int, error) {
	count := 0

	url := fmt.Sprintf("%s/tokens", r.baseURL)
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return count, fmt.Errorf("create request: %w", err)
	}

	resp, err := r.client.Do(req)
	if err != nil {
		return count, fmt.Errorf("fetch tokens: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return count, fmt.Errorf("unexpected status: %d", resp.StatusCode)
	}

	var tokenResp TokenListResponse
	if err := json.NewDecoder(resp.Body).Decode(&tokenResp); err != nil {
		return count, fmt.Errorf("decode response: %w", err)
	}

	// cache all tokens from first page
	for _, token := range tokenResp.Items {
		r.cacheToken(&token)
		count++
	}

	return count, nil
}

// fetch all tokens from API (not used atm, keeping for reference)
func (r *Resolver) preloadAllTokens(ctx context.Context) (int, error) {
	count := 0
	nextHash := ""

	for {
		select {
		case <-ctx.Done():
			return count, ctx.Err()
		default:
		}

		url := fmt.Sprintf("%s/tokens", r.baseURL)
		if nextHash != "" {
			url = fmt.Sprintf("%s?hash=%s&items_count=50", url, nextHash)
		}

		req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
		if err != nil {
			return count, fmt.Errorf("create request: %w", err)
		}

		resp, err := r.client.Do(req)
		if err != nil {
			return count, fmt.Errorf("fetch tokens: %w", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			return count, fmt.Errorf("unexpected status: %d", resp.StatusCode)
		}

		var tokenResp TokenListResponse
		if err := json.NewDecoder(resp.Body).Decode(&tokenResp); err != nil {
			return count, fmt.Errorf("decode response: %w", err)
		}

		// cache all tokens
		for _, token := range tokenResp.Items {
			r.cacheToken(&token)
			count++
		}

		// check if more pages
		if tokenResp.NextPageParams == nil || tokenResp.NextPageParams.Hash == "" {
			break
		}
		nextHash = tokenResp.NextPageParams.Hash

		// smol delay to avoid ratelimiting
		time.Sleep(100 * time.Millisecond)
	}

	return count, nil
}

// fetch all contracts from API
func (r *Resolver) preloadAllSmartContracts(ctx context.Context) (int, error) {
	count := 0
	nextHash := ""

	for {
		select {
		case <-ctx.Done():
			return count, ctx.Err()
		default:
		}

		url := fmt.Sprintf("%s/smart-contracts", r.baseURL)
		if nextHash != "" {
			url = fmt.Sprintf("%s?hash=%s&items_count=50", url, nextHash)
		}

		req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
		if err != nil {
			return count, fmt.Errorf("create request: %w", err)
		}

		resp, err := r.client.Do(req)
		if err != nil {
			return count, fmt.Errorf("fetch contracts: %w", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			return count, fmt.Errorf("unexpected status: %d", resp.StatusCode)
		}

		var contractResp SmartContractListResponse
		if err := json.NewDecoder(resp.Body).Decode(&contractResp); err != nil {
			return count, fmt.Errorf("decode response: %w", err)
		}

		// cache all contracts (skip if already cached as token)
		for _, contract := range contractResp.Items {
			addr := strings.ToLower(contract.Address.Hash)
			_, exists := r.cache.Get(addr)

			if !exists && contract.Address.Name != "" {
				r.cacheSmartContract(contract.Address.Hash, contract.Address.Name)
				count++
			}
		}

		// check if more pages
		if contractResp.NextPageParams == nil || contractResp.NextPageParams.Hash == "" {
			break
		}
		nextHash = contractResp.NextPageParams.Hash

		// smol delay to avoid ratelimit
		time.Sleep(100 * time.Millisecond)
	}

	return count, nil
}

// return contract info, fetching if necessary
func (r *Resolver) GetContractInfo(address string) *ContractInfo {
	addr := strings.ToLower(address)

	// check cache first
	if info, exists := r.cache.Get(addr); exists {
		if contractInfo, ok := info.(*ContractInfo); ok {
			return contractInfo
		}
	}

	// not in cache, queue for async fetch (non-blocking)
	select {
	case r.fetchQueue <- addr:
		// successfully queued
	default:
		// queue is full, skip fetch
		logger.DebugComponent("contracts", "Fetch queue full, skipping contract %s", addr)
	}

	// return unknown for now
	return &ContractInfo{
		Address: addr,
		Name:    "unknown",
		IsToken: false,
		Type:    "unknown",
		Symbol:  "",
	}
}

// fetch contract info and cache it
func (r *Resolver) fetchAndCacheContract(address string) {
	// try token API first
	if r.fetchTokenInfo(address) {
		return
	}

	// try contract API
	if r.fetchSmartContractInfo(address) {
		return
	}

	// cache as unknown
	r.cache.Set(address, &ContractInfo{
		Address: address,
		Name:    "unknown",
		IsToken: false,
		Type:    "unknown",
		Symbol:  "",
	})
}

// fetch token info
func (r *Resolver) fetchTokenInfo(address string) bool {
	url := fmt.Sprintf("%s/tokens/%s", r.baseURL, address)

	resp, err := r.client.Get(url)
	if err != nil {
		logger.DebugComponent("contracts", "Failed to fetch token %s: %v", address, err)
		return false
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return false
	}

	var token TokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&token); err != nil {
		logger.DebugComponent("contracts", "Failed to decode token response: %v", err)
		return false
	}

	r.cacheToken(&token)
	return true
}

// fetch contract info
func (r *Resolver) fetchSmartContractInfo(address string) bool {
	url := fmt.Sprintf("%s/smart-contracts/%s", r.baseURL, address)

	resp, err := r.client.Get(url)
	if err != nil {
		logger.DebugComponent("contracts", "Failed to fetch contract %s: %v", address, err)
		return false
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return false
	}

	var contract SmartContractResponse
	if err := json.NewDecoder(resp.Body).Decode(&contract); err != nil {
		logger.DebugComponent("contracts", "Failed to decode contract response: %v", err)
		return false
	}

	if contract.Address.Name != "" {
		r.cacheSmartContract(address, contract.Address.Name)
		return true
	}

	return false
}

// cache token
func (r *Resolver) cacheToken(token *TokenResponse) {
	addr := strings.ToLower(token.Address)

	r.cache.Set(addr, &ContractInfo{
		Address: addr,
		Name:    token.Name,
		IsToken: true,
		Type:    token.Type,
		Symbol:  token.Symbol,
	})
}

// cache contract
func (r *Resolver) cacheSmartContract(address, name string) {
	addr := strings.ToLower(address)

	r.cache.Set(addr, &ContractInfo{
		Address: addr,
		Name:    name,
		IsToken: false,
		Type:    "smart-contract",
		Symbol:  "",
	})
}

// returns current cache size
func (r *Resolver) GetCacheSize() int {
	return r.cache.Len()
}

// worker goroutine that processes fetch requests
func (r *Resolver) fetchWorker() {
	defer r.wg.Done()

	seen := make(map[string]time.Time)

	for {
		select {
		case <-r.ctx.Done():
			return
		case addr := <-r.fetchQueue:
			// dedup requests, skip if we've tried recently
			if lastTry, exists := seen[addr]; exists {
				if time.Since(lastTry) < 5*time.Minute {
					continue
				}
			}

			// check cache again in case another worker fetched it
			if _, exists := r.cache.Get(addr); exists {
				continue
			}

			seen[addr] = time.Now()
			r.fetchAndCacheContract(addr)

			// clean up old entries from seen map periodically
			if len(seen) > 1000 {
				now := time.Now()
				for k, v := range seen {
					if now.Sub(v) > 10*time.Minute {
						delete(seen, k)
					}
				}
			}
		}
	}
}

// gracefully shuts down resolver
func (r *Resolver) Shutdown() {
	logger.InfoComponent("contracts", "Shutting down contract resolver...")
	r.cancel()
	close(r.fetchQueue)
	r.wg.Wait()
	logger.InfoComponent("contracts", "Contract resolver shutdown complete")
}
