//go:build !unix

package main

import "os"

// ponytail: no inode outside unix; mtime and size still catch an edit.
func inode(os.FileInfo) uint64 { return 0 }
