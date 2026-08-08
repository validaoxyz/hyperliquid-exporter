//go:build windows

package monitors

import "io/fs"

func diskAllocatedBytesSupported() bool { return false }

func allocatedFileInfo(fs.FileInfo) (device, inode uint64, bytes int64, ok bool) {
	return 0, 0, 0, false
}
