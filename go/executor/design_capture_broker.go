package main

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"time"

	"fleet/contract"
)

const captureBrokerSocket = "/run/fleet-design-capture/broker.sock"
const captureReceiptLimit = 8192

var errCaptureBusy = errors.New("fixed Fleet design capture renderer is busy")
var errCaptureStageUnavailable = errors.New("the designer input directory no longer supports atomic capture publication")
var errCaptureExpired = errors.New("capture permit expired before renderer admission")

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
		job.OriginHost == "" || !job.Request.Valid() || job.Request.Job != execution || !job.Producer.Valid() ||
		job.Producer.Commit != job.Request.Base || job.InstallationDigest() == "" || permit.InstallationSHA256 != job.InstallationDigest() ||
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

// The launch spec supplies the very input directory given to artifact_get.
// Re-resolving the session root could select a different legacy workspace.
func (e *executor) captureStageInput(execution, session string) string {
	if e.db == nil || !captureID.MatchString(execution) {
		return ""
	}
	spec, ok := readWorkerSpec(workerDirFor(e.root, execution))
	if !ok || spec.Kind != "harness" || spec.Execution != execution || spec.DataDir != e.root ||
		!captureID.MatchString(spec.Session) || (session != "" && spec.Session != session) ||
		spec.InputDir != filepath.Join(spec.Dir, "input") || !filepath.IsAbs(spec.InputDir) || !e.insideDirs(spec.Dir) {
		return ""
	}
	var raw string
	if e.db.QueryRow("SELECT body FROM commands WHERE id=? AND terminal=0", spec.CommandId).Scan(&raw) != nil {
		return ""
	}
	cmd := commandOf(raw)
	sp, err := startPayload(cmd)
	if err != nil || str0(sp.ProjectId) != "fleet" {
		return ""
	}
	owner, err := idOf(cmd, "executionId")
	if err != nil || owner != execution {
		return ""
	}
	sid, err := idOf(cmd, "sessionId")
	if err != nil || sid != spec.Session {
		return ""
	}
	var profile string
	if json.Unmarshal(sp.AgentConfig["id"], &profile) != nil || profile != "codex-designer" {
		return ""
	}
	return spec.InputDir
}

func (e *executor) captureStageCapabilities(active []string, preflight func(string) bool) []string {
	var ready []string
	for _, execution := range active {
		if input := e.captureStageInput(execution, ""); input != "" && preflight(input) {
			ready = append(ready, contract.DesignCaptureStageCapability(execution))
		}
	}
	return ready
}

type captureUploadAuthority struct {
	URL        string                       `json:"url"`
	Token      string                       `json:"token"`
	Access     *[2]string                   `json:"access"`
	Stores     []string                     `json:"stores"`
	Execution  string                       `json:"execution"`
	OriginHost string                       `json:"originHost"`
	StageInput string                       `json:"stageInput,omitempty"`
	Permit     contract.DesignCapturePermit `json:"permit"`
}

func (a captureUploadAuthority) validStageBinding() bool {
	if a.OriginHost == contract.DesignCaptureHost {
		return filepath.IsAbs(a.StageInput)
	}
	return a.OriginHost != "" && a.StageInput == ""
}

func (a captureUploadAuthority) redemptionReady(preflight func(string) bool) bool {
	// A separate origin was checked by the coordinator on its own poll.
	// Its input directory stays on that executor.
	return a.validStageBinding() && (a.OriginHost != contract.DesignCaptureHost || preflight(a.StageInput))
}

// The supervisor passes host authority on a private inherited pipe. Neither
// the command line, job directory, guest, nor stdout carries it.
func captureBrokerCLI(args []string) error {
	if len(args) != 1 || !filepath.IsAbs(args[0]) || protectCaptureMemory() != nil {
		return errCaptureBroker
	}
	pipe := os.NewFile(3, "capture-authority")
	if pipe == nil {
		return errCaptureBroker
	}
	var auth captureUploadAuthority
	decoder := json.NewDecoder(io.LimitReader(pipe, 16384))
	decoder.DisallowUnknownFields()
	err := decoder.Decode(&auth)
	_ = pipe.Close()
	if err != nil || auth.URL == "" || auth.Token == "" || !captureID.MatchString(auth.Execution) || !auth.validStageBinding() {
		return errCaptureBroker
	}
	permit, err := json.Marshal(auth.Permit)
	if err != nil {
		return errCaptureBroker
	}
	client := newTransferClient(auth.URL, auth.Token, contract.DesignCaptureHost, auth.Access)
	client.accessHosts = auth.Stores
	auth.Token, auth.Access = "", nil
	jobDeadline := time.Now().Add(time.Duration(contract.DesignCaptureTimeoutMS) * time.Millisecond)
	conn, receipt, archive, err := awaitCaptureBroker(dialCaptureBroker, args[0], permit,
		time.Unix(auth.Permit.ExpiresAt, 0), jobDeadline, 2*time.Second, func() bool { return auth.redemptionReady(captureStageReady) })
	if err != nil {
		return err
	}
	defer conn.Close()
	return uploadCaptureResponse(client, auth.Execution, conn, receipt, archive, jobDeadline)
}

// Only explicit pre-admission busy is retried. Once the broker admits the job,
// any transport failure is terminal because the permit may have been spent.
func awaitCaptureBroker(dial func() (net.Conn, error), input string, permit []byte, expires, deadline time.Time, interval time.Duration, ready func() bool) (net.Conn, int64, int64, error) {
	until := minTime(expires, deadline)
	for time.Now().Before(until) {
		conn, err := dial()
		if err != nil {
			return nil, 0, 0, err
		}
		receipt, archive, err := captureBrokerResponse(conn, input, permit, until, deadline, ready)
		if err == nil {
			return conn, receipt, archive, nil
		}
		_ = conn.Close()
		if !errors.Is(err, errCaptureBusy) {
			return nil, 0, 0, err
		}
		time.Sleep(min(interval, time.Until(until)))
	}
	return nil, 0, 0, errCaptureExpired
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

func captureBrokerResponse(conn net.Conn, input string, permit []byte, until, deadline time.Time, ready func() bool) (int64, int64, error) {
	if err := conn.SetDeadline(deadline); err != nil {
		return 0, 0, err
	}
	if err := conn.SetReadDeadline(minTime(until, time.Now().Add(5*time.Second))); err != nil {
		return 0, 0, err
	}
	var admission [4]byte
	_, err := io.ReadFull(conn, admission[:])
	if !time.Now().Before(until) {
		return 0, 0, errCaptureExpired
	}
	if err != nil || string(admission[:]) != "FDCA" && string(admission[:]) != "FDB1" {
		return 0, 0, errCaptureBroker
	}
	if string(admission[:]) == "FDB1" {
		return 0, 0, errCaptureBusy
	}
	if err := conn.SetReadDeadline(deadline); err != nil {
		return 0, 0, err
	}
	root, err := os.OpenRoot(input)
	if err != nil {
		return 0, 0, err
	}
	defer root.Close()
	items := []struct {
		name string
		max  int64
	}{{"request.json", 8192}, {"base.bundle", 60 << 20}, {"overlay.json", 3 << 20}, {"fixture.json", 256 << 10}}
	if len(permit) == 0 || len(permit) > 1024 {
		return 0, 0, errCaptureBroker
	}
	// Check after broker admission and immediately before sending any permit.
	if !ready() {
		return 0, 0, errCaptureStageUnavailable
	}
	if !time.Now().Before(until) {
		return 0, 0, errCaptureExpired
	}
	if _, err := io.WriteString(conn, "FDC1"); err != nil {
		return 0, 0, err
	}
	if err := binary.Write(conn, binary.BigEndian, uint32(len(permit))); err != nil {
		return 0, 0, err
	}
	if _, err := conn.Write(permit); err != nil {
		return 0, 0, err
	}

	for _, item := range items {
		if err := sendCaptureInput(conn, root, item.name, item.max); err != nil {
			return 0, 0, err
		}
	}
	var header [12]byte
	if _, err := io.ReadFull(conn, header[:]); err != nil || string(header[:4]) != "FDR1" {
		return 0, 0, errCaptureBroker
	}
	receipt, archive := int64(binary.BigEndian.Uint32(header[4:8])), int64(binary.BigEndian.Uint32(header[8:12]))
	if receipt <= 0 || receipt > captureReceiptLimit || archive <= 0 || archive > maxCaptureWire {
		return 0, 0, errCaptureBroker
	}
	return receipt, archive, nil
}

func uploadCaptureResponse(client *transferClient, execution string, wire io.Reader, receipt, archive int64, deadline time.Time) error {
	return collectPrivateCapture(wire, receipt, archive, func(name, path string) error {
		// Same stable IDs as sessionless script artifacts, with no plaintext
		// output or spool file. Unsettled partial uploads cannot pass the job gate.
		sum := sha256.Sum256([]byte(execution + "\x00" + name))
		id := fmt.Sprintf("script-%x", sum[:16])
		body := transferJob{Path: path, ExecutionId: execution, Name: name, MediaType: mediaType(name), Required: true}
		return settlePrivateCaptureUpload(client, id, &body, deadline, time.Second)
	})
}

func settlePrivateCaptureUpload(client *transferClient, id string, body *transferJob, deadline time.Time, delay time.Duration) error {
	for time.Now().Before(deadline) {
		_, f := runUpload(client, nil, id, body)
		if f == nil {
			return nil
		}
		if !f.retry {
			break
		}
		time.Sleep(min(delay, time.Until(deadline)))
		delay = min(2*delay, 10*time.Second)
	}
	return errors.New("private capture artifact upload did not settle")
}

func collectPrivateCapture(wire io.Reader, receipt, archive int64, publish func(string, string) error) error {
	files := []struct {
		name string
		size int64
		file *os.File
	}{{"receipt.json", receipt, nil}, {"capture.tar", archive, nil}}
	defer func() {
		for _, row := range files {
			if row.file != nil {
				_ = row.file.Close()
			}
		}
	}()
	for i := range files {
		row := &files[i]
		var err error
		row.file, err = anonymousCaptureFile()
		if err != nil {
			return errCaptureBroker
		}
		if _, err := io.CopyN(row.file, wire, row.size); err != nil {
			return errCaptureBroker
		}
	}
	var extra [1]byte
	if n, err := io.ReadFull(wire, extra[:]); n != 0 || !errors.Is(err, io.EOF) {
		return errCaptureBroker
	}
	for _, row := range files {
		if err := publish(row.name, fmt.Sprintf("/proc/self/fd/%d", row.file.Fd())); err != nil {
			return err
		}
	}
	return nil
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
