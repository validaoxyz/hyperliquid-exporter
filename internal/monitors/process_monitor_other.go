//go:build !linux

package monitors

// Non-Linux stub. /proc-based process metrics are only useful on Linux,
// which is where production node operators run hl-node. On darwin (the
// development OS) and other systems the monitor stays idle.

func procFSAvailable() bool { return false }

func findProcesses(names []string) (map[string]processSelection, error) {
	selections := make(map[string]processSelection, len(names))
	for _, name := range names {
		selections[name] = processSelection{}
	}
	return selections, nil
}
