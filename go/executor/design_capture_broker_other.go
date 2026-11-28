//go:build !linux

package main

import (
	"net"
	"os"
)

func captureBrokerReady() bool             { return false }
func dialCaptureBroker() (net.Conn, error) { return nil, errCaptureBroker }

func protectCaptureMemory() error { return errAppCapture }

func anonymousCaptureFile() (*os.File, error) { return nil, errAppCapture }
