//go:build linux

package monitors

import "os"

// procFSAvailable reports whether /proc is mounted as procfs. On Linux this
// is essentially always true; the check protects against misconfigured
// containers that hide /proc.
func procFSAvailable() bool {
	_, err := os.Stat("/proc/self/stat")
	return err == nil
}

func findProcesses(names []string) (map[string]processSelection, error) {
	return findProcessesAt("/proc", names)
}
