package monitors

import (
	"encoding/json"
	"time"
)

// ConsensusLogEntry represents a parsed consensus log line
type ConsensusLogEntry struct {
	Timestamp string          `json:"-"`
	Direction string          `json:"-"`
	Message   json.RawMessage `json:"-"`
}

// vote message in consensus logs
type VoteMessage struct {
	Vote struct {
		Validator string  `json:"validator"`
		Round     float64 `json:"round"`
	} `json:"vote"`
}

// block message in consensus logs
type BlockMessage struct {
	Round    float64         `json:"round"`
	Proposer string          `json:"proposer"`
	QC       json.RawMessage `json:"qc"`
	TC       json.RawMessage `json:"tc"`
}

// quorum certificate data
type QCData struct {
	Round     float64  `json:"round"`
	Signers   []string `json:"signers"`
	BlockHash string   `json:"block_hash"`
}

// timeout certificate data
type TCData struct {
	Timeouts []struct {
		Validator string `json:"validator"`
	} `json:"timeouts"`
}

// heartbeat message
type HeartbeatMessage struct {
	Validator string `json:"validator"`
	RandomID  uint64 `json:"random_id"`
	Round     uint64 `json:"round"`
}

// heartbeat acknowledgment
type HeartbeatAckMessage struct {
	Validator string `json:"validator"`
	RandomID  uint64 `json:"random_id"`
	Round     uint64 `json:"round"`
}

// parsed status log line
type StatusLogEntry struct {
	Timestamp              string          `json:"-"`
	DisconnectedValidators json.RawMessage `json:"disconnected_validators"`
	HeartbeatStatuses      json.RawMessage `json:"heartbeat_statuses"`
}

type optionalStatusFloat struct {
	Present bool
	Null    bool
	Value   float64
}

type statusHeartbeat struct {
	Signer           string
	SinceLastSuccess optionalStatusFloat
	LastAckDuration  optionalStatusFloat
}

type statusDisconnectedPair struct {
	SubjectSigner  string
	ReporterSigner string
	SinceRound     int64
}

type statusSnapshot struct {
	SourceTime              time.Time
	Round                   int64
	HeartbeatFieldPresent   bool
	Heartbeats              []statusHeartbeat
	DisconnectedPresent     bool
	Disconnected            []statusDisconnectedPair
	MissingHeartbeatPresent bool
	MissingHeartbeatSigners []string
}
