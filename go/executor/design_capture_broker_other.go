//go:build !linux

package main

import (
	"io"
	"net"
	"os"
)

func captureBrokerReady() bool             { return false }
func dialCaptureBroker() (net.Conn, error) { return nil, errCaptureBroker }

func protectCaptureMemory() error { return errAppCapture }

func anonymousCaptureFile() (*os.File, error) { return nil, errAppCapture }

func captureStageReady(string) bool { return false }

func publishCaptureArchive(int, string, string, io.Reader, int64) (string, error) {
	return "", errCaptureBroker
}
