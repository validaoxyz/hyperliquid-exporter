package replica

import (
	"encoding/json"
	"fmt"
	"sync"
	"time"
)

// parser handles parsing of replica_cmds files
type Parser struct {
	bufferSize int
	blockPool  sync.Pool
}

// new replica parser
func NewParser(bufferSizeMB int) *Parser {
	p := &Parser{
		bufferSize: bufferSizeMB * 1024 * 1024,
	}
	p.blockPool = sync.Pool{
		New: func() interface{} {
			return &ReplicaBlock{}
		},
	}
	return p
}

// get block from the pool and resets it
func (p *Parser) getBlock() *ReplicaBlock {
	block := p.blockPool.Get().(*ReplicaBlock)
	block.Reset()
	return block
}

// parses a replica_cmds file and returns block metrics
// parses blocks from a reader
// extracts metrics from a replica block
func (p *Parser) ExtractMetrics(block *ReplicaBlock) (*BlockMetrics, error) {
	start := time.Now()

	// parse time - try multiple formats
	var blockTime time.Time
	var err error

	// try RFC3339Nano first (with Z)
	blockTime, err = time.Parse(time.RFC3339Nano, block.ABCIBlock.Time)
	if err != nil {
		// try without timezone (assume UTC)
		blockTime, err = time.Parse("2006-01-02T15:04:05.999999999", block.ABCIBlock.Time)
		if err != nil {
			return nil, fmt.Errorf("parse time: %w", err)
		}
		// set to UTC since no timezone was specified
		blockTime = blockTime.UTC()
	}

	// count actions by type
	actionCounts := make(map[string]int)
	operationCounts := make(map[string]int)
	totalActions := 0
	totalOperations := 0

	// parse signed action bundles more efficiently
	if len(block.ABCIBlock.SignedActionBundles) > 0 {
		if err := p.parseActionBundles(block.ABCIBlock.SignedActionBundles, actionCounts, operationCounts, &totalActions, &totalOperations); err != nil {
			return nil, fmt.Errorf("parse action bundles: %w", err)
		}
	}

	// clear the raw JSON after parsing to free memory
	block.ABCIBlock.SignedActionBundles = nil

	// moved to parseActionBundles method

	return &BlockMetrics{
		Height:          0,
		Round:           block.ABCIBlock.Round,
		ParentRound:     block.ABCIBlock.ParentRound,
		HardforkVersion: block.ABCIBlock.Hardfork.Version,
		Time:            blockTime,
		Proposer:        block.ABCIBlock.Proposer,
		TotalActions:    totalActions,
		ActionCounts:    actionCounts,
		TotalOperations: totalOperations,
		OperationCounts: operationCounts,
		ProcessDuration: time.Since(start),
	}, nil
}

// parses action bundles more efficiently
func (p *Parser) parseActionBundles(bundlesJSON json.RawMessage, actionCounts, operationCounts map[string]int, totalActions, totalOperations *int) error {
	// parse into array of arrays to match the actual structure: [[hash, bundle], ...]
	var bundles []json.RawMessage
	if err := json.Unmarshal(bundlesJSON, &bundles); err != nil {
		return fmt.Errorf("unmarshal bundles: %w", err)
	}

	// process each bundle pair
	for _, bundlePair := range bundles {
		// each bundle is an array with 2 elements: [hash, bundleData]
		var pair []json.RawMessage
		if err := json.Unmarshal(bundlePair, &pair); err != nil {
			continue
		}

		if len(pair) < 2 {
			continue
		}

		// parse the second element which contains the bundle data
		var bundleData struct {
			SignedActions []struct {
				Action struct {
					Type     string          `json:"type"`
					Orders   json.RawMessage `json:"orders,omitempty"`
					Cancels  json.RawMessage `json:"cancels,omitempty"`
					Modifies json.RawMessage `json:"modifies,omitempty"`
				} `json:"action"`
			} `json:"signed_actions"`
		}

		if err := json.Unmarshal(pair[1], &bundleData); err != nil {
			continue
		}

		// process signed actions
		for _, sa := range bundleData.SignedActions {
			actionType := sa.Action.Type
			if actionType == "" {
				actionType = ActionTypeOther
			}

			actionCounts[actionType]++
			*totalActions++

			// count operations based on action type
			operationCount := 1
			switch actionType {
			case ActionTypeOrder, ActionTypeTwapOrder:
				if len(sa.Action.Orders) > 0 {
					operationCount = countJSONArrayElements(sa.Action.Orders)
				}
			case ActionTypeCancel, ActionTypeCancelByCloid:
				if len(sa.Action.Cancels) > 0 {
					operationCount = countJSONArrayElements(sa.Action.Cancels)
				}
			case ActionTypeBatchModify:
				if len(sa.Action.Modifies) > 0 {
					operationCount = countJSONArrayElements(sa.Action.Modifies)
				}
			}

			operationCounts[actionType] += operationCount
			*totalOperations += operationCount
		}
	}

	return nil
}

// counts elements in a JSON array without full parsing
func countJSONArrayElements(data json.RawMessage) int {
	// quick counting by looking for commas
	// this is approximate but much faster than parsing
	count := 1
	inString := false
	escaped := false
	depth := 0

	for _, b := range data {
		if escaped {
			escaped = false
			continue
		}

		switch b {
		case '"':
			if !escaped {
				inString = !inString
			}
		case '\\':
			if inString {
				escaped = true
			}
		case '[':
			if !inString {
				depth++
			}
		case ']':
			if !inString {
				depth--
			}
		case ',':
			if !inString && depth == 1 {
				count++
			}
		}
	}

	return count
}

// counts the number of operations within an action
// streams a replica_cmds file and calls the callback for each block
// parses a single line into a ReplicaBlock using the pool
func (p *Parser) ParseBlockFromLine(line []byte) (*ReplicaBlock, error) {
	block := p.getBlock()
	if err := json.Unmarshal(line, block); err != nil {
		p.blockPool.Put(block) // return to pool on error
		return nil, err
	}
	return block, nil
}

// returns a block to the pool after clearing it
func (p *Parser) ReturnBlock(block *ReplicaBlock) {
	block.Reset()
	p.blockPool.Put(block)
}
