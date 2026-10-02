package monitors

import (
	"encoding/json"
	"fmt"
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

func (h *HeartbeatMessage) UnmarshalJSON(data []byte) error {
	decoded, err := decodeHeartbeat(data)
	if err != nil {
		return err
	}
	*h = decoded
	return nil
}

func (h *HeartbeatAckMessage) UnmarshalJSON(data []byte) error {
	decoded, err := decodeHeartbeat(data)
	if err != nil {
		return err
	}
	*h = HeartbeatAckMessage(decoded)
	return nil
}

type heartbeatRound struct {
	value   uint64
	present bool
}

func (r *heartbeatRound) UnmarshalJSON(data []byte) error {
	if r.present {
		return fmt.Errorf("duplicate heartbeat round")
	}
	var value uint64
	if err := unmarshalRequiredJSON(data, &value); err != nil || value == 0 {
		return fmt.Errorf("invalid heartbeat round")
	}
	r.value, r.present = value, true
	return nil
}

// Testnet heartbeats use executed_round from October 2026. Validate each
// occurrence so duplicate keys cannot hide a malformed or conflicting value.
func decodeHeartbeat(data []byte) (HeartbeatMessage, error) {
	var wire struct {
		Validator     string         `json:"validator"`
		RandomID      uint64         `json:"random_id"`
		Round         heartbeatRound `json:"round"`
		ExecutedRound heartbeatRound `json:"executed_round"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return HeartbeatMessage{}, err
	}
	var round uint64
	for _, field := range []heartbeatRound{wire.Round, wire.ExecutedRound} {
		if !field.present {
			continue
		}
		if round != 0 && round != field.value {
			return HeartbeatMessage{}, fmt.Errorf("conflicting heartbeat rounds")
		}
		round = field.value
	}
	if round == 0 {
		return HeartbeatMessage{}, fmt.Errorf("missing heartbeat round")
	}
	return HeartbeatMessage{Validator: wire.Validator, RandomID: wire.RandomID, Round: round}, nil
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
