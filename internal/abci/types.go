package abci

import "time"

// contains core chain context
type ContextInfo struct {
	Height          int64
	TxIndex         int64 // Transactions in current block (0-indexed)
	Time            string
	NextOid         int64 // Next Order ID (not transaction ID)
	NextLid         int64
	NextTwapId      int64
	HardforkVersion int64
}

// represents a point-in-time state
type StateSnapshot struct {
	Context     ContextInfo
	Timestamp   time.Time
	FilePath    string
	FileModTime time.Time
}

// contains validator info from ABCI state
type ValidatorProfile struct {
	Address string
	Moniker string
	IP      string
}
