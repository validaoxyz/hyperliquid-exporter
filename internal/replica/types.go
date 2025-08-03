// Package replica provides parsing and monitoring for Hyperliquid replica_cmds files
package replica

import (
	"encoding/json"
	"time"
)

// ReplicaBlock represents a single block from replica_cmds
type ReplicaBlock struct {
	ABCIBlock struct {
		Time                string          `json:"time"`
		Round               int64           `json:"round"`
		ParentRound         int64           `json:"parent_round"`
		Proposer            string          `json:"proposer"`
		SignedActionBundles json.RawMessage `json:"signed_action_bundles"`
		Hardfork            HardforkInfo    `json:"hardfork"`
	} `json:"abci_block"`
	Resps json.RawMessage `json:"resps,omitempty"`
}

// resets the ReplicaBlock for reuse
func (r *ReplicaBlock) Reset() {
	r.ABCIBlock.Time = ""
	r.ABCIBlock.Round = 0
	r.ABCIBlock.ParentRound = 0
	r.ABCIBlock.Proposer = ""
	r.ABCIBlock.SignedActionBundles = nil
	r.ABCIBlock.Hardfork = HardforkInfo{}
	r.Resps = nil
}

// contains protocol version information
type HardforkInfo struct {
	Version int64 `json:"version"`
	Round   int64 `json:"round"`
}

// represents a bundle of signed actions
type ActionBundle struct {
	Broadcaster      string         `json:"broadcaster"`
	BroadcasterNonce int64          `json:"broadcaster_nonce"`
	SignedActions    []SignedAction `json:"signed_actions"`
}

// represents a single signed action
type SignedAction struct {
	Signature Signature `json:"signature"`
	Action    Action    `json:"action"`
	Nonce     int64     `json:"nonce"`
}

// contains the cryptographic signature
type Signature struct {
	R string `json:"r"`
	S string `json:"s"`
	V int    `json:"v"`
}

// represents a blockchain action
type Action struct {
	Type string          `json:"type"`
	Data json.RawMessage `json:"-"` // Type-specific data
}

// common action types
const (
	ActionTypeOrder             = "order"
	ActionTypeCancel            = "cancel"
	ActionTypeCancelByCloid     = "cancelByCloid"
	ActionTypeBatchModify       = "batchModify"
	ActionTypeScheduleCancel    = "scheduleCancel"
	ActionTypeUpdateLeverage    = "updateLeverage"
	ActionTypeEvmRawTx          = "evmRawTx"
	ActionTypeModify            = "modify"
	ActionTypeApproveAgent      = "approveAgent"
	ActionTypeSpotSend          = "spotSend"
	ActionTypeUsdTransfer       = "usdClassTransfer"
	ActionTypeSetReferrer       = "setReferrer"
	ActionTypeClaimRewards      = "claimRewards"
	ActionTypeApproveBuilderFee = "approveBuilderFee"
	ActionTypeTokenDelegate     = "tokenDelegate"
	ActionTypeTwapOrder         = "twapOrder"
	ActionTypeEvmUserModify     = "evmUserModify"
	ActionTypeVoteAppHash       = "voteAppHash"
	ActionTypeOther             = "other"
)

// contains aggregated metrics for a block
type BlockMetrics struct {
	Height          int64
	Round           int64
	Time            time.Time
	Proposer        string
	TotalActions    int
	ActionCounts    map[string]int
	TotalOperations int            // new: total individual operations
	OperationCounts map[string]int // new: operations by type
	ProcessDuration time.Duration
}

// represents a parsed action bundle
type ParsedBundle struct {
	Broadcaster string
	Actions     []ParsedAction
}

// represents a parsed action with type information
type ParsedAction struct {
	Type      string
	Signature Signature
	Nonce     int64
	// add type-specific fields as needed
}
