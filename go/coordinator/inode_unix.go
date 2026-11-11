//go:build !windows

package main

import (
	"os"
	"syscall"
)

// inodeOf: the file's inode (0 when unknown), part of the people file change key.
func inodeOf(fi os.FileInfo) uint64 {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return uint64(st.Ino)
	}
	return 0
}
