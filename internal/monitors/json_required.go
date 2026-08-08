package monitors

import (
	"bytes"
	"encoding/json"
	"errors"
)

// unmarshalRequiredJSON rejects JSON null before decoding a required scalar or
// object field. encoding/json otherwise accepts null for primitive destinations
// and silently leaves their zero value, which can turn malformed log records
// into plausible zero observations.
func unmarshalRequiredJSON(raw json.RawMessage, dst any) error {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return errors.New("required JSON value is null")
	}
	return json.Unmarshal(trimmed, dst)
}
