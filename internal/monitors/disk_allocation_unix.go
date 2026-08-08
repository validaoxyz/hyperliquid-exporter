//go:build !windows

package monitors

import (
	"io/fs"
	"syscall"
)

func diskAllocatedBytesSupported() bool { return true }

// st_blocks is defined in 512-byte units independently of the filesystem
// block size. Device plus inode identifies one physical file within a scan.
func allocatedFileInfo(info fs.FileInfo) (device, inode uint64, bytes int64, ok bool) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Blocks < 0 {
		return 0, 0, 0, false
	}
	return uint64(stat.Dev), uint64(stat.Ino), stat.Blocks * 512, true
}
