package main

import (
	"strings"
	"sync"
)

var lawTemplate struct {
	once      sync.Once
	name, err string
	sock, bin string
	port      int
}

func short(err error) string {
	if err == nil {
		return "ok"
	}
	s := strings.ReplaceAll(err.Error(), "\n", " | ")
	if len(s) > 160 {
		s = s[:160] + "..."
	}
	return s
}
