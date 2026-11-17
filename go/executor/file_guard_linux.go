package main

import "golang.org/x/sys/unix"

// dirSearch opens a folder for lookups only: entering it needs search permission, not read (file_guard.go).
const dirSearch = unix.O_PATH
