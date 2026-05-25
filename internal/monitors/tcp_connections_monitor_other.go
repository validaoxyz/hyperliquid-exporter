//go:build !linux

package monitors

// /proc/net/tcp is Linux-only. On other platforms the monitor stays
// idle (gated by procFSAvailable() in the platform-agnostic Start*).

func tickTCPConnections() {}
