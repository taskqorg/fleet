package main

import (
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"time"

	"fleet/contract"
)

const captureBrokerSocket = "/run/fleet-design-capture/broker.sock"
const captureReceiptLimit = 8192

var errCaptureBroker = errors.New("fixed Fleet design capture broker failed or unavailable")

func isDesignCaptureJob(c contract.Command) bool {
	return c.Script != nil && c.Script.Payload != nil && c.Script.Payload.Command == contract.DesignCaptureScript
}

func fixedCaptureCommand(c contract.Command, host, execution string) (contract.DesignCaptureCommand, contract.DesignCapturePermit, error) {
	var job contract.DesignCaptureCommand
	var permit contract.DesignCapturePermit
	if !isDesignCaptureJob(c) || host != contract.DesignCaptureHost || str0(c.Script.SessionId) != "" {
		return job, permit, errCaptureBroker
	}
	sp := c.Script.Payload
	if json.Unmarshal(sp.Rest["designCapture"], &job) != nil || json.Unmarshal(sp.Rest["designCapturePermit"], &permit) != nil ||
		!job.Request.Valid() || job.Request.Job != execution || !job.Producer.Valid() ||
		job.Producer.Commit != job.Request.Base || !job.Runtime.Valid() ||
		sp.Commit != nil || sp.Rest["workingDirectory"] != nil || sp.TimeoutMs == nil || *sp.TimeoutMs != contract.DesignCaptureTimeoutMS ||
		len(sp.Inputs) != 3 || sp.Inputs[0].Path != "base.bundle" || sp.Inputs[1].Path != "overlay.json" || sp.Inputs[2].Path != "fixture.json" {
		return job, permit, errCaptureBroker
	}
	sig, err := hex.DecodeString(permit.Signature)
	if err != nil || len(sig) != 64 || permit.ExpiresAt <= time.Now().Unix() {
		return job, permit, errCaptureBroker
	}
	return job, permit, nil
}

// captureBrokerCLI is a fixed host client. Its private input directory is
// prepared by the script runner. The broker gets a signed authorization frame
// followed by byte streams containing only the guest's four declared inputs.
func captureBrokerCLI(args []string) error {
	if len(args) != 2 || !filepath.IsAbs(args[0]) || !filepath.IsAbs(args[1]) {
		return errCaptureBroker
	}
	conn, err := dialCaptureBroker()
	if err != nil {
		return errCaptureBroker
	}
	defer conn.Close()
	return captureBrokerExchange(conn, args[0], args[1])
}

func captureBrokerExchange(conn net.Conn, input, output string) error {
	if err := conn.SetDeadline(time.Now().Add(time.Duration(contract.DesignCaptureTimeoutMS) * time.Millisecond)); err != nil {
		return err
	}
	root, err := os.OpenRoot(input)
	if err != nil {
		return err
	}
	defer root.Close()
	// The first frame ends at the broker. Remaining frames are the guest inputs.
	items := []struct {
		name string
		max  int64
	}{{"permit.json", 1024}, {"request.json", 8192}, {"base.bundle", 60 << 20}, {"overlay.json", 3 << 20}, {"fixture.json", 256 << 10}}
	if _, err := io.WriteString(conn, "FDC1"); err != nil {
		return err
	}
	for _, item := range items {
		if err := sendCaptureInput(conn, root, item.name, item.max); err != nil {
			return err
		}
	}
	var header [12]byte
	if _, err := io.ReadFull(conn, header[:]); err != nil || string(header[:4]) != "FDR1" {
		return errCaptureBroker
	}
	receipt, archive := int64(binary.BigEndian.Uint32(header[4:8])), int64(binary.BigEndian.Uint32(header[8:12]))
	if receipt <= 0 || receipt > captureReceiptLimit || archive <= 0 || archive > maxCaptureWire {
		return errCaptureBroker
	}
	return receiveCaptureOutputs(conn, output, receipt, archive)
}

func sendCaptureInput(conn io.Writer, root *os.Root, name string, max int64) error {
	st, err := root.Lstat(name)
	if err != nil || !st.Mode().IsRegular() || st.Size() <= 0 || st.Size() > max {
		return errCaptureBroker
	}
	f, err := root.Open(name)
	if err != nil {
		return err
	}
	defer f.Close()
	current, err := f.Stat()
	if err != nil || !os.SameFile(st, current) {
		return errCaptureBroker
	}
	if err := binary.Write(conn, binary.BigEndian, uint32(st.Size())); err != nil {
		return err
	}
	_, err = io.CopyN(conn, f, st.Size())
	return err
}

func receiveCaptureOutputs(conn io.Reader, output string, receipt, archive int64) (err error) {
	root, err := os.OpenRoot(output)
	if err != nil {
		return err
	}
	defer root.Close()
	files := []struct {
		name string
		size int64
	}{{"receipt.json", receipt}, {"capture.tar", archive}}
	created := []string{}
	defer func() {
		if err != nil {
			for _, name := range created {
				if cleanup := root.Remove(name); cleanup != nil {
					err = errors.Join(err, cleanup)
				}
			}
		}
	}()
	for _, file := range files {
		f, err := root.OpenFile(file.name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return err
		}
		created = append(created, file.name)
		_, copied := io.CopyN(f, conn, file.size)
		if err := errors.Join(copied, f.Close()); err != nil {
			return err
		}
	}
	var extra [1]byte
	if n, err := io.ReadFull(conn, extra[:]); n != 0 || !errors.Is(err, io.EOF) {
		return errCaptureBroker
	}
	return nil
}
