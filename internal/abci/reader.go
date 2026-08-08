package abci

import (
	"bufio"
	"fmt"
	"os"

	"github.com/vmihailenco/msgpack/v5"
)

// provides reading of ABCI state files
type Reader struct {
	bufferSize int
}

// creates a new reader with specified buffer size
func NewReader(bufferSizeMB int) *Reader {
	return &Reader{
		bufferSize: bufferSizeMB * 1024 * 1024,
	}
}

// reads validator profiles from ABCI state.
//
// Schema notes:
//   - Profiles live under exchange.c_staking.validator_to_profile, NOT
//     exchange.consensus.validator_to_profile as they did pre-2026. We
//     still attempt the consensus path as a fallback for older hl-node
//     versions.
//   - The node_ip.Ip field may be a 4-byte msgpack-binary octet array
//     instead of a pre-formatted string. We accept both shapes.
//   - Profile map keys: commission_bps, delegations_disabled,
//     description, name, node_ip, signer. We only consume name + node_ip.
func (r *Reader) ReadValidatorProfiles(filePath string) ([]ValidatorProfile, error) {
	profiles, _, err := r.ReadValidatorProfilesWithSource(filePath)
	return profiles, err
}

// ReadValidatorProfilesWithSource is ReadValidatorProfiles plus the schema
// provenance: legacy is true when the profiles came from the pre-2026
// exchange.consensus.validator_to_profile fallback rather than the current
// exchange.c_staking path. Callers that don't care can use
// ReadValidatorProfiles.
func (r *Reader) ReadValidatorProfilesWithSource(filePath string) ([]ValidatorProfile, bool, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return nil, false, fmt.Errorf("open file: %w", err)
	}
	defer file.Close()

	reader := bufio.NewReaderSize(file, r.bufferSize)

	var data struct {
		Exchange struct {
			CStaking struct {
				ValidatorToProfile [][]interface{} `msgpack:"validator_to_profile"`
			} `msgpack:"c_staking"`
			Consensus struct {
				ValidatorToProfile [][]interface{} `msgpack:"validator_to_profile"`
			} `msgpack:"consensus"`
		} `msgpack:"exchange"`
	}

	decoder := msgpack.NewDecoder(reader)
	decoder.SetCustomStructTag("msgpack")

	if err := decoder.Decode(&data); err != nil {
		return nil, false, fmt.Errorf("decode: %w", err)
	}

	// Pick whichever location is populated. c_staking is the current path;
	// consensus is the pre-2026 (legacy) fallback.
	entries := data.Exchange.CStaking.ValidatorToProfile
	legacy := false
	if len(entries) == 0 {
		entries = data.Exchange.Consensus.ValidatorToProfile
		legacy = true
	}

	var profiles []ValidatorProfile

	for _, entry := range entries {
		if len(entry) != 2 {
			continue
		}

		address, ok := entry[0].(string)
		if !ok {
			continue
		}

		profileData, ok := entry[1].(map[string]interface{})
		if !ok {
			continue
		}

		ip := extractIP(profileData["node_ip"])

		var moniker string
		if nameValue, ok := profileData["name"].(string); ok {
			moniker = nameValue
		}

		if ip != "" && moniker != "" {
			profiles = append(profiles, ValidatorProfile{
				Address: address,
				Moniker: moniker,
				IP:      ip,
			})
		}
	}

	return profiles, legacy, nil
}

// extractIP pulls the IP string out of a profile's node_ip field.
//
// Two shapes are accepted:
//   - {"Ip": [a, b, c, d]} — current shape, 4-byte octet array
//   - {"Ip": "a.b.c.d"}    — pre-2026 hl-node releases
func extractIP(raw interface{}) string {
	m, ok := raw.(map[string]interface{})
	if !ok {
		return ""
	}
	v := m["Ip"]
	if s, ok := v.(string); ok {
		return s
	}
	// msgpack-bin decodes 4-byte octets as []byte
	if b, ok := v.([]byte); ok && len(b) == 4 {
		return fmt.Sprintf("%d.%d.%d.%d", b[0], b[1], b[2], b[3])
	}
	// some msgpack decoders surface arrays as []interface{} of numbers
	if arr, ok := v.([]interface{}); ok && len(arr) == 4 {
		var oct [4]int64
		for i, x := range arr {
			switch n := x.(type) {
			case int8:
				oct[i] = int64(n)
			case uint8:
				oct[i] = int64(n)
			case int16, int32, int64, uint16, uint32, uint64:
				oct[i] = toInt64(n)
			default:
				return ""
			}
		}
		return fmt.Sprintf("%d.%d.%d.%d", oct[0], oct[1], oct[2], oct[3])
	}
	return ""
}

func toInt64(v interface{}) int64 {
	switch n := v.(type) {
	case int8:
		return int64(n)
	case int16:
		return int64(n)
	case int32:
		return int64(n)
	case int64:
		return n
	case uint8:
		return int64(n)
	case uint16:
		return int64(n)
	case uint32:
		return int64(n)
	case uint64:
		return int64(n)
	}
	return 0
}
