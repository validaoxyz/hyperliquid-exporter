//go:build !linux

package monitors

// Non-Linux stub. /proc-based process metrics are only useful on Linux,
// which is where production node operators run hl-node. On darwin (the
// development OS) and other systems the monitor stays idle.

func procFSAvailable() bool { return false }

func findProcess(_ string) (processInfo, bool) { return processInfo{}, false }
