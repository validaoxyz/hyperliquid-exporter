//go:build !windows

package monitors

import "syscall"

func statfs(path string) (*fsStats, error) {
	var s syscall.Statfs_t
	if err := syscall.Statfs(path, &s); err != nil {
		return nil, err
	}
	return &fsStats{
		Bavail: uint64(s.Bavail),
		Blocks: uint64(s.Blocks),
		Bsize:  uint64(s.Bsize),
	}, nil
}
