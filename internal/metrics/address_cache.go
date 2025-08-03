package metrics

import (
	"regexp"
	"strings"
	"sync"
)

var (
	// stores mappings from truncated addresses to full addresses
	addressCache   = make(map[string]string)
	addressCacheMu sync.RWMutex

	// matches addresses in format "0x1234..5678"
	truncatedAddressPattern = regexp.MustCompile(`^0x[a-fA-F0-9]{4,6}\.\.[a-fA-F0-9]{4}$`)
)

// stores both full address and its truncated version in the cache
func RegisterFullAddress(fullAddress string) {
	if fullAddress == "" || len(fullAddress) < 42 { // ethereum addresses are 42 chars (0x + 40 hex)
		return
	}

	// normalize to lowercase
	fullAddress = strings.ToLower(fullAddress)

	// create truncated version: first 6 chars + ".." + last 4 chars
	truncated := truncateAddress(fullAddress)

	addressCacheMu.Lock()
	addressCache[truncated] = fullAddress
	addressCacheMu.Unlock()
}

// returns full address if input is truncated, otherwise returns input as is
func ExpandAddress(address string) string {
	if address == "" {
		return address
	}

	// normalize to lowercase
	address = strings.ToLower(address)

	// check if it's a truncated address
	if !IsAddressTruncated(address) {
		return address
	}

	addressCacheMu.RLock()
	fullAddress, exists := addressCache[address]
	addressCacheMu.RUnlock()

	if exists {
		return fullAddress
	}

	// return as is if no mapping found
	return address
}

// check if an address matches truncated pattern
func IsAddressTruncated(address string) bool {
	return truncatedAddressPattern.MatchString(address)
}

// creates truncated version of a full address
func truncateAddress(fullAddress string) string {
	if len(fullAddress) < 10 {
		return fullAddress
	}
	return fullAddress[:6] + ".." + fullAddress[len(fullAddress)-4:]
}

// return current size of address cache (for monitoring)
func GetAddressCacheSize() int {
	addressCacheMu.RLock()
	defer addressCacheMu.RUnlock()
	return len(addressCache)
}

// clears address cache (for testing)
func ClearAddressCache() {
	addressCacheMu.Lock()
	addressCache = make(map[string]string)
	addressCacheMu.Unlock()
}
