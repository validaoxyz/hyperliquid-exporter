//go:build linux

package monitors

import (
	"bufio"
	"os"
	"strconv"
	"strings"

	"github.com/validaoxyz/hyperliquid-exporter/internal/metrics"
)

// tickTCPConnections reads /proc/net/tcp and /proc/net/tcp6, tallies
// each listening-port × state pair across both, and updates the
// hl_p2p_tcp_connections gauge. Any state not seen this tick is set to
// 0 explicitly so a transition out of (say) TIME_WAIT immediately
// reflects in the gauge rather than freezing at the last positive
// value.
func tickTCPConnections() {
	counts := make(map[uint16]map[string]int)
	for _, p := range tcpListenPorts {
		counts[p] = map[string]int{}
		// Pre-seed all known states with 0 so we publish a complete grid.
		for _, name := range tcpStateNames {
			counts[p][name] = 0
		}
	}

	for _, path := range []string{"/proc/net/tcp", "/proc/net/tcp6"} {
		readTCPFile(path, counts)
	}

	for port, byState := range counts {
		portLabel := strconv.FormatUint(uint64(port), 10)
		for state, n := range byState {
			metrics.HLP2PTCPConnections.WithLabelValues(portLabel, state).Set(float64(n))
		}
	}
}

// readTCPFile parses one of /proc/net/tcp{,6}. Format per line (after
// the header):
//
//	sl  local_address      rem_address    st tx_q rx_q tr tm->w retrnsmt uid timeout inode ...
//	0:  00000000:0FA1      00000000:0000  0A 00000000:00000000 ...
//
// `local_address` is `hex_ip:hex_port`. We tally counts[<localport>][<state>]
// only if localport is one of our tracked listening ports.
func readTCPFile(path string, counts map[uint16]map[string]int) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	headerSkipped := false
	for scanner.Scan() {
		if !headerSkipped {
			headerSkipped = true
			continue
		}
		line := scanner.Text()
		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}
		// local_address = "<ip>:<port>" in hex.
		colon := strings.LastIndexByte(fields[1], ':')
		if colon < 0 {
			continue
		}
		port64, err := strconv.ParseUint(fields[1][colon+1:], 16, 16)
		if err != nil {
			continue
		}
		port := uint16(port64)
		byState, ok := counts[port]
		if !ok {
			continue // not a port we care about
		}
		state64, err := strconv.ParseUint(fields[3], 16, 8)
		if err != nil {
			continue
		}
		stateName, known := tcpStateNames[uint8(state64)]
		if !known {
			stateName = "UNKNOWN"
		}
		byState[stateName]++
	}
}
