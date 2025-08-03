package monitors

import (
	"encoding/json"
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
	Round   float64  `json:"round"`
	Signers []string `json:"signers"`
}

// timeout certificate data
type TCData struct {
	Timeouts []struct {
		Validator string `json:"validator"`
	} `json:"timeouts"`
}

// heartbeat message
type HeartbeatMessage struct {
	Validator string  `json:"validator"`
	RandomID  float64 `json:"random_id"`
}

// heartbeat acknowledgment
type HeartbeatAckMessage struct {
	RandomID float64 `json:"random_id"`
}

// parsed status log line
type StatusLogEntry struct {
	Timestamp              string          `json:"-"`
	DisconnectedValidators json.RawMessage `json:"disconnected_validators"`
	HeartbeatStatuses      json.RawMessage `json:"heartbeat_statuses"`
}

// round advance event data
type RoundAdvancePayload struct {
	Reason  string `json:"reason"`
	Suspect string `json:"suspect"`
}
