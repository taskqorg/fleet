package main

import "os"

// inodeOf: no inode on Windows; the change key is mtime and size there.
func inodeOf(os.FileInfo) uint64 { return 0 }
