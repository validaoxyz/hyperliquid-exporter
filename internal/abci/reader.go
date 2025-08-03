package abci

import (
	"bufio"
	"fmt"
	"os"
	"sync"

	"github.com/vmihailenco/msgpack/v5"
)

// provides reading of ABCI state files
type Reader struct {
	bufferSize int
	cache      *Cache
	mu         sync.RWMutex
}

// creates a new reader with specified buffer size
func NewReader(bufferSizeMB int) *Reader {
	return &Reader{
		bufferSize: bufferSizeMB * 1024 * 1024,
		cache:      NewCache(),
	}
}

// extracts context information from an ABCI state file
func (r *Reader) ReadContext(filePath string) (*ContextInfo, error) {
	// file info for cache validation
	fileInfo, err := os.Stat(filePath)
	if err != nil {
		return nil, fmt.Errorf("stat file: %w", err)
	}

	// cache first
	if cached := r.cache.Get(filePath, fileInfo.ModTime()); cached != nil {
		return cached, nil
	}

	// Open file
	file, err := os.Open(filePath)
	if err != nil {
		return nil, fmt.Errorf("open file: %w", err)
	}
	defer file.Close()

	// buffered reader for efficiency
	reader := bufio.NewReaderSize(file, r.bufferSize)

	// minimal structure for context only
	var data struct {
		Exchange struct {
			Context struct {
				Height     int64  `msgpack:"height"`
				TxIndex    int64  `msgpack:"tx_index"`
				Time       string `msgpack:"time"`
				NextOid    int64  `msgpack:"next_oid"`
				NextLid    int64  `msgpack:"next_lid"`
				NextTwapId int64  `msgpack:"next_twap_id"`
				Hardfork   struct {
					Version int64 `msgpack:"version"`
				} `msgpack:"hardfork"`
			} `msgpack:"context"`
		} `msgpack:"exchange"`
	}

	// decoder and decode
	decoder := msgpack.NewDecoder(reader)
	decoder.SetCustomStructTag("msgpack")

	if err := decoder.Decode(&data); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}

	// Create context info
	ctx := &ContextInfo{
		Height:          data.Exchange.Context.Height,
		TxIndex:         data.Exchange.Context.TxIndex,
		Time:            data.Exchange.Context.Time,
		NextOid:         data.Exchange.Context.NextOid,
		NextLid:         data.Exchange.Context.NextLid,
		NextTwapId:      data.Exchange.Context.NextTwapId,
		HardforkVersion: data.Exchange.Context.Hardfork.Version,
	}

	// Cache result
	r.cache.Set(filePath, ctx, fileInfo.ModTime())

	return ctx, nil
}

// read context and EVM account count
func (r *Reader) ReadContextWithAccounts(filePath string) (*ContextInfo, int64, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return nil, 0, fmt.Errorf("open file: %w", err)
	}
	defer file.Close()

	reader := bufio.NewReaderSize(file, r.bufferSize)

	// structure including EVM accounts
	var data struct {
		Exchange struct {
			Context struct {
				Height     int64  `msgpack:"height"`
				TxIndex    int64  `msgpack:"tx_index"`
				Time       string `msgpack:"time"`
				NextOid    int64  `msgpack:"next_oid"`
				NextLid    int64  `msgpack:"next_lid"`
				NextTwapId int64  `msgpack:"next_twap_id"`
				Hardfork   struct {
					Version int64 `msgpack:"version"`
				} `msgpack:"hardfork"`
			} `msgpack:"context"`
			HyperEvm struct {
				State2 struct {
					EvmDb struct {
						InMemory struct {
							Accounts []interface{} `msgpack:"accounts"`
						} `msgpack:"InMemory"`
					} `msgpack:"evm_db"`
				} `msgpack:"state2"`
			} `msgpack:"hyper_evm"`
		} `msgpack:"exchange"`
	}

	decoder := msgpack.NewDecoder(reader)
	decoder.SetCustomStructTag("msgpack")

	if err := decoder.Decode(&data); err != nil {
		return nil, 0, fmt.Errorf("decode: %w", err)
	}

	ctx := &ContextInfo{
		Height:          data.Exchange.Context.Height,
		TxIndex:         data.Exchange.Context.TxIndex,
		Time:            data.Exchange.Context.Time,
		NextOid:         data.Exchange.Context.NextOid,
		NextLid:         data.Exchange.Context.NextLid,
		NextTwapId:      data.Exchange.Context.NextTwapId,
		HardforkVersion: data.Exchange.Context.Hardfork.Version,
	}

	accountCount := int64(len(data.Exchange.HyperEvm.State2.EvmDb.InMemory.Accounts))

	return ctx, accountCount, nil
}

// reads validator profiles from ABCI state
func (r *Reader) ReadValidatorProfiles(filePath string) ([]ValidatorProfile, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return nil, fmt.Errorf("open file: %w", err)
	}
	defer file.Close()

	reader := bufio.NewReaderSize(file, r.bufferSize)

	// Strct for validator profiles
	var data struct {
		Exchange struct {
			Consensus struct {
				ValidatorToProfile [][]interface{} `msgpack:"validator_to_profile"`
			} `msgpack:"consensus"`
		} `msgpack:"exchange"`
	}

	decoder := msgpack.NewDecoder(reader)
	decoder.SetCustomStructTag("msgpack")

	if err := decoder.Decode(&data); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}

	var profiles []ValidatorProfile

	// Parse val profiles
	for _, entry := range data.Exchange.Consensus.ValidatorToProfile {
		if len(entry) != 2 {
			continue
		}

		// 1st element is val address
		address, ok := entry[0].(string)
		if !ok {
			continue
		}

		// 2nd is profile map
		profileData, ok := entry[1].(map[string]interface{})
		if !ok {
			continue
		}

		// extract node IP
		var ip string
		if nodeIPData, ok := profileData["node_ip"].(map[string]interface{}); ok {
			if ipValue, ok := nodeIPData["Ip"].(string); ok {
				ip = ipValue
			}
		}

		// extract name (moniker)
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

	return profiles, nil
}
