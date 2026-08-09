package metrics

import (
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"
	api "go.opentelemetry.io/otel/metric"
)

type labeledValue struct {
	value          float64
	labels         []attribute.KeyValue
	identitySource string
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

// ValidatorInfo is the mutable API-derived identity attached to a validator.
// It is deliberately separate from the immutable OTel resource identity.
type ValidatorInfo struct {
	Signer string
	Name   string
	Stake  float64
	Active bool
	Jailed bool
}

// ValidatorIdentity is the result of resolving an observed identifier. Kind is
// explicit so an arbitrary 0x string is never silently relabelled as a
// validator address.
type ValidatorIdentity struct {
	Validator string
	Signer    string
	Name      string
	Kind      string // "validator" or "signer"
}

// validatorRegistry is one synchronized, replaceable identity generation.
// API polls replace both maps atomically. Legacy local status rows may add a
// proven signer mapping before the first API response, but can never fabricate
// validator metadata.
var validatorRegistry = struct {
	signerToValidator map[string]string
	validatorInfo     map[string]ValidatorInfo
	updatedAt         time.Time
}{
	signerToValidator: make(map[string]string),
	validatorInfo:     make(map[string]ValidatorInfo),
}

// ReplaceProvisionalSignerMappings installs one complete local-status mapping
// only before the first validator API generation. It exists solely for old
// node builds whose status rows carried [validator, signer] pairs. Once the
// API has published, local rows may not incrementally mutate that generation.
func ReplaceProvisionalSignerMappings(mappings map[string]string) {
	rebuilt := make(map[string]string, len(mappings))
	for signer, validator := range mappings {
		signer = strings.ToLower(strings.TrimSpace(signer))
		validator = strings.ToLower(strings.TrimSpace(validator))
		if signer != "" && validator != "" {
			rebuilt[signer] = validator
		}
	}
	metricsMutex.Lock()
	if validatorSnapshotGeneration == 0 {
		validatorRegistry.signerToValidator = rebuilt
		validatorRegistry.updatedAt = time.Time{}
	}
	metricsMutex.Unlock()
}

// get val addr for a signer
func GetValidatorForSigner(signer string) (string, bool) {
	signer = strings.ToLower(strings.TrimSpace(ExpandAddress(signer)))
	metricsMutex.RLock()
	validator, exists := validatorRegistry.signerToValidator[signer]
	metricsMutex.RUnlock()
	return validator, exists
}

// get signer and name for a val addr
func GetValidatorInfo(validator string) (signer string, name string, exists bool) {
	validator = strings.ToLower(strings.TrimSpace(ExpandAddress(validator)))
	metricsMutex.RLock()
	info, exists := validatorRegistry.validatorInfo[validator]
	metricsMutex.RUnlock()
	if !exists {
		return "", "", false
	}
	return info.Signer, info.Name, true
}

// ResolveValidatorIdentity resolves only identities proven by the current
// registry generation. Unknown inputs collapse to an explicit unknown series
// at the call site; they are not returned as if they were validators.
func ResolveValidatorIdentity(identifier string) (ValidatorIdentity, bool) {
	identifier = strings.ToLower(strings.TrimSpace(ExpandAddress(identifier)))
	if identifier == "" {
		return ValidatorIdentity{}, false
	}

	metricsMutex.RLock()
	defer metricsMutex.RUnlock()
	if validator, ok := validatorRegistry.signerToValidator[identifier]; ok {
		info := validatorRegistry.validatorInfo[validator]
		return ValidatorIdentity{
			Validator: validator,
			Signer:    identifier,
			Name:      normalizedValidatorName(info.Name),
			Kind:      "signer",
		}, true
	}
	if info, ok := validatorRegistry.validatorInfo[identifier]; ok {
		return ValidatorIdentity{
			Validator: identifier,
			Signer:    info.Signer,
			Name:      normalizedValidatorName(info.Name),
			Kind:      "validator",
		}, true
	}
	return ValidatorIdentity{}, false
}

// ResolveSignerSnapshot resolves one complete, source-typed signer generation
// under a single registry read lock. The map is keyed by the normalized input
// spelling so callers can resolve truncated wire identifiers without mixing
// two API generations in one status snapshot. An identifier absent from the
// current API generation remains explicitly a signer, while validator/name
// stay unknown; the status schema, not a 0x prefix, supplies that kind.
func ResolveSignerSnapshot(signers []string) map[string]ValidatorIdentity {
	type signerInput struct {
		key      string
		expanded string
	}
	inputs := make([]signerInput, 0, len(signers))
	for _, signer := range signers {
		key := strings.ToLower(strings.TrimSpace(signer))
		if key == "" {
			continue
		}
		inputs = append(inputs, signerInput{
			key:      key,
			expanded: strings.ToLower(strings.TrimSpace(ExpandAddress(key))),
		})
	}

	out := make(map[string]ValidatorIdentity, len(inputs))
	metricsMutex.RLock()
	defer metricsMutex.RUnlock()
	for _, input := range inputs {
		identity := ValidatorIdentity{
			Validator: "unknown",
			Signer:    input.expanded,
			Name:      "unknown",
			Kind:      "signer",
		}
		if validator, ok := validatorRegistry.signerToValidator[input.expanded]; ok {
			info := validatorRegistry.validatorInfo[validator]
			identity.Validator = validator
			identity.Name = normalizedValidatorName(info.Name)
		}
		out[input.key] = identity
	}
	return out
}

func normalizedValidatorName(name string) string {
	if strings.TrimSpace(name) == "" {
		return "unknown"
	}
	return name
}

// Note: there is deliberately no periodic truncation of labeledValues.
// A previous version capped every labeled family at 100 series and evicted
// by Go map iteration order (i.e. at random), which silently dropped
// per-validator series on networks with more than 100 validators. Series
// lifecycle is instead handled where the semantics are known: snapshot
// reconciles (ReplaceValidatorLatencyEMA, ReplaceValidatorSummaries) and
// per-monitor TTL housekeeping.
