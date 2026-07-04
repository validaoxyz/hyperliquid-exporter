package metrics

import (
	"sync"
	"time"

	"github.com/validaoxyz/hyperliquid-exporter/internal/cache"
	"go.opentelemetry.io/otel/attribute"
	api "go.opentelemetry.io/otel/metric"
)

type labeledValue struct {
	value  float64
	labels []attribute.KeyValue
}

type NodeIdentity struct {
	ValidatorAddress string
	ServerIP         string
	Alias            string
	IsValidator      bool
	Chain            string
}

// global vars for metric state management
var (
	nodeIdentity  NodeIdentity
	metricsMutex  sync.RWMutex
	currentValues = make(map[api.Observable]interface{})
	labeledValues = make(map[api.Observable]map[string]labeledValue)
	callbacks     []api.Registration
)

// TODO commonLabels holds the common labels to be added to all metrics
var commonLabels []attribute.KeyValue

// signerMap maps signer addr -> val address
// using LRU cache
var signerMap *cache.LRUCache

// store additional info about a val
type ValidatorInfo struct {
	Signer string
	Name   string
}

// maps val addr -> val info
var validatorInfoCache *cache.LRUCache

// set val addr for a signer
func RegisterSignerMapping(signer, validator string) {
	if signerMap == nil {
		initSignerMap()
	}
	signerMap.Set(signer, validator)
}

// get val addr for a signer
func GetValidatorForSigner(signer string) (string, bool) {
	if signerMap == nil {
		initSignerMap()
	}
	val, exists := signerMap.Get(signer)
	if !exists {
		return "", false
	}
	return val.(string), true
}

// store val info (signer + name) for a val addr
func RegisterValidatorInfo(validator, signer, name string) {
	if validatorInfoCache == nil {
		initValidatorInfoCache()
	}
	validatorInfoCache.Set(validator, ValidatorInfo{
		Signer: signer,
		Name:   name,
	})
}

// get signer and name for a val addr
func GetValidatorInfo(validator string) (signer string, name string, exists bool) {
	if validatorInfoCache == nil {
		initValidatorInfoCache()
	}

	val, exists := validatorInfoCache.Get(validator)
	if !exists {
		return "", "", false
	}

	info := val.(ValidatorInfo)
	return info.Signer, info.Name, true
}

// init signer map with LRU cache
func initSignerMap() {
	metricsMutex.Lock()
	defer metricsMutex.Unlock()
	if signerMap == nil {
		// init with reasonable size for val count
		signerMap = cache.NewLRUCache(5000, 24*time.Hour)
	}
}

// init val info cache
func initValidatorInfoCache() {
	metricsMutex.Lock()
	defer metricsMutex.Unlock()
	if validatorInfoCache == nil {
		// init with reasonable size for val count
		validatorInfoCache = cache.NewLRUCache(5000, 24*time.Hour)
	}
}

// Note: there is deliberately no periodic truncation of labeledValues.
// A previous version capped every labeled family at 100 series and evicted
// by Go map iteration order (i.e. at random), which silently dropped
// per-validator series on networks with more than 100 validators. Series
// lifecycle is instead handled where the semantics are known: snapshot
// reconciles (ReplaceValidatorLatencyEMA, ReplaceValidatorSummaries) and
// per-monitor TTL housekeeping.
