//go:build !linux

package main

import "net"

func captureBrokerReady() bool             { return false }
func dialCaptureBroker() (net.Conn, error) { return nil, errCaptureBroker }

func publishCaptureDirectory(_, _ string) error { return errAppCapture }
