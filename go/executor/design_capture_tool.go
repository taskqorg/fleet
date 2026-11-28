package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"time"

	"fleet/contract"
	"fleet/executor/supervise"
)

const captureStageKind = "design-capture-stage"

var captureRequestName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,79}$`)

type captureStageRequest struct {
	ExecutionID string `json:"executionId"`
	CommandID   string `json:"commandId"`
	RequestID   string `json:"requestId"`
}

func (q captureStageRequest) valid() bool {
	return captureID.MatchString(q.ExecutionID) && captureID.MatchString(q.CommandID) && captureRequestName.MatchString(q.RequestID)
}

func (q captureStageRequest) key() string { return q.CommandID + ":capture-stage:" + q.RequestID }

type capturePublication struct {
	Proof   contract.DesignCaptureReturn `json:"proof"`
	Capture appCaptureReceipt            `json:"capture"`
}

func captureToolText(result any) []byte {
	r := typed[struct {
		IsError bool `json:"isError"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}](result)
	if r.IsError || len(r.Content) != 1 || r.Content[0].Type != "text" {
		return nil
	}
	return []byte(r.Content[0].Text)
}

func captureToolReturn(result any) (contract.DesignCaptureReturn, bool) {
	var reply struct {
		State  string                        `json:"state"`
		Result *contract.DesignCaptureReturn `json:"result"`
	}
	if json.Unmarshal(captureToolText(result), &reply) != nil || reply.State != "succeeded" || reply.Result == nil ||
		reply.Result.ReceiptArtifact == "" || reply.Result.CaptureArtifact == "" {
		return contract.DesignCaptureReturn{}, false
	}
	return *reply.Result, true
}

func captureStageTool(result any, c *delegationContext, db *sql.DB, requestID string, recheck func() (any, error)) (any, error) {
	proof, ok := captureToolReturn(result)
	if !ok {
		return result, nil
	}
	execution, _ := c.execution()
	request := captureStageRequest{execution, str0(c.CommandId), requestID}
	if !request.valid() || str0(c.ProjectId) != "fleet" || proof.Job.Request.Execution != execution {
		return mcpText(M{"error": errAppCapture.Error()}, &yes), nil
	}
	key := request.key()
	if err := enqueue(db, key, captureStageKind, request); err != nil {
		return nil, dbe(err)
	}
	r, err := wait(db, "transfers", key, 8, false)
	if err != nil {
		return nil, err
	}
	if r == nil {
		return mcpText(M{"state": "transfer-pending"}, nil), nil
	}
	if r.Keys.Error.Set {
		return mcpText(r.Value, &yes), nil
	}
	verified, decodeErr := contractOf[capturePublication](r.Value)
	if decodeErr != nil || verified.Proof != proof || verified.Capture.Directory != "" ||
		!sceneSHA.MatchString(verified.Capture.ManifestSHA256) {
		return mcpText(M{"error": "Verification does not match the immutable Fleet archive"}, &yes), nil
	}
	observed, err := recheck()
	latest, ready := captureToolReturn(observed)
	if err != nil {
		return nil, err
	}
	if !ready || latest != proof {
		return mcpText(M{"error": "Capture authority changed before handoff"}, &yes), nil
	}
	return mcpText(M{"state": "verified", "archiveArtifactId": proof.CaptureArtifact,
		"archiveSha256": proof.Job.Capture.SHA256, "manifestSha256": verified.Capture.ManifestSHA256,
		"files": verified.Capture.Files}, nil), nil
}

func (c *transferClient) captureAuthority(request captureStageRequest) (contract.DesignCaptureReturn, *failure) {
	key := request.CommandID + ":agent:" + newID()
	args := contract.DesignCaptureRequest{Action: contract.DesignCaptureActionStage, RequestId: request.RequestID}
	raw, failure := c.post(contract.RouteAgentTools.Path, contract.AgentToolsRequest{
		HostId: c.host, CommandId: request.CommandID, ExecutionId: &request.ExecutionID, AgentToolKey: &key,
		Request: json.RawMessage(enc(M{"jsonrpc": "2.0", "id": 1, "method": "tools/call",
			"params": M{"name": "design_capture", "arguments": args}})),
	})
	if failure != nil {
		return contract.DesignCaptureReturn{}, failure
	}
	reply, err := readReply(string(raw))
	if err == nil && reply.Keys.Result.Set {
		if proof, ok := captureToolReturn(reply.Keys.Result.V); ok && proof.Job.Request.Valid() &&
			proof.Job.Request.Execution == request.ExecutionID && proof.Job.RequestSHA256 == proof.Job.Request.Digest() {
			return proof, nil
		}
	}
	return contract.DesignCaptureReturn{}, permanentf("Capture authorization or terminal result is unavailable")
}

type captureTransferPin struct {
	ID, Job, Name, SHA256 string
	Limit                 int64
}

func (c *transferClient) captureArtifact(request captureStageRequest, pin captureTransferPin) (io.ReadCloser, *failure) {
	r, f := transferReply(c, contract.RouteArtifactsGet, contract.HostArtifactGetRequest{
		HostId: c.host, ExecutionId: &request.ExecutionID, CommandId: &request.CommandID, ArtifactId: pin.ID,
	})
	if f != nil {
		return nil, f
	}
	if r.Id != pin.ID || str0(r.ExecutionId) != pin.Job || r.Name != pin.Name || r.Sha256 != pin.SHA256 ||
		r.State != "stored" || r.Url == nil || r.Size <= 0 || r.Size > pin.Limit {
		return nil, permanentf("Capture artifact identity or length differs")
	}
	return c.get(*r.Url)
}

func executeCaptureStage(request captureStageRequest,
	authorize func() (contract.DesignCaptureReturn, *failure),
	get func(captureStageRequest, captureTransferPin) (io.ReadCloser, *failure),
	validateReplay func([]byte) error,
) (capturePublication, *failure) {
	var none capturePublication
	proof, f := authorize()
	if f != nil {
		return none, f
	}
	if !captureID.MatchString(proof.Job.Request.Job) || proof.Job.Request.Execution != request.ExecutionID {
		return none, permanentf("Capture job belongs to another execution")
	}
	receipt, f := get(request, captureTransferPin{proof.ReceiptArtifact, proof.Job.Request.Job, "receipt.json", proof.Job.ReceiptSHA256, captureReceiptLimit})
	if f != nil {
		return none, f
	}
	raw, err := io.ReadAll(io.LimitReader(receipt, captureReceiptLimit+1))
	if err := errors.Join(err, receipt.Close()); err != nil {
		return none, retryf("Capture receipt transport was interrupted")
	}
	archive, f := get(request, captureTransferPin{proof.CaptureArtifact, proof.Job.Request.Job, "capture.tar", proof.Job.Capture.SHA256, proof.Job.Capture.Bytes})
	if f != nil {
		return none, f
	}
	stream := &captureBody{Reader: archive}
	verified, err := verifyDesignerCapture(context.Background(), proof.Job.Request.DesignCaptureScope, proof.Job, raw, stream, validateReplay)
	closeErr := archive.Close()
	if stream.interrupted(proof.Job.Capture.Bytes) || closeErr != nil {
		return none, retryf("Capture archive transport was interrupted")
	}
	if err != nil {
		return none, permanentf("Capture archive failed verification")
	}
	latest, f := authorize()
	if f != nil {
		return none, f
	}
	if latest != proof {
		return none, permanentf("Capture authorization changed during transfer")
	}
	// This is evidence, not a release of bytes. The subsequent artifact_get
	// is mediated again by the coordinator against the current execution lock.
	return capturePublication{Proof: proof, Capture: verified}, nil
}

type captureBody struct {
	io.Reader
	bytes int64
	err   error
}

func (r *captureBody) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	r.bytes += int64(n)
	if err != nil {
		r.err = err
	}
	return n, err
}

func (r *captureBody) interrupted(expected int64) bool {
	return r.err != nil && (!errors.Is(r.err, io.EOF) || r.bytes < expected)
}

// A fresh grant may complete despite later cancellation. Read and hash the
// granted bytes before creating any designer-visible file.
func downloadGrantedCapture(client *transferClient, view contract.ArtifactGetReply) (*os.File, *failure) {
	if view.Name != "capture.tar" || view.Size <= 0 || view.Size > maxCaptureWire ||
		!sceneSHA.MatchString(view.Sha256) || view.Url == nil || protectCaptureMemory() != nil {
		return nil, permanentf("Invalid capture download grant")
	}
	body, f := client.get(*view.Url)
	if f != nil {
		return nil, f
	}
	defer body.Close()
	file, err := anonymousCaptureFile()
	if err != nil {
		return nil, ioFailure(err)
	}
	hash := sha256.New()
	n, err := io.Copy(io.MultiWriter(file, hash), io.LimitReader(body, view.Size+1))
	if err != nil || n < view.Size {
		file.Close()
		return nil, retryf("Capture download was interrupted")
	}
	if n != view.Size || hex.EncodeToString(hash.Sum(nil)) != view.Sha256 {
		file.Close()
		return nil, permanentf("Capture download differs from verified archive")
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		file.Close()
		return nil, ioFailure(err)
	}
	return file, nil
}

// The signed producer and replay command share the one JS fixture grammar.
func signedCaptureReplayEntry() string {
	return filepath.Join(filepath.Dir(exe()), "deploy", "design-verifier", "designer.mjs")
}

func checkSignedCaptureReplay(raw []byte) error {
	entry := signedCaptureReplayEntry()
	cmd := &exec.Cmd{Path: "/usr/bin/node", Args: []string{"node", entry, "validate-replay"}, Env: []string{}, Stdin: bytes.NewReader(raw)}
	proc, err := supervise.Spawn(supervise.Spec{Cmd: cmd, Deadline: 10 * time.Second, KillLeftovers: true})
	if err != nil {
		return err
	}
	return proc.Wait()
}

// The ordinary artifact tool never accepts a command ID from its caller.
// Only verified archive evidence belonging to this exact start can set it.
func captureDownloadCommand(db *sql.DB, c *delegationContext, artifact string) string {
	execution, _ := c.execution()
	command := str0(c.CommandId)
	if !captureID.MatchString(execution) || !captureID.MatchString(command) || artifact == "" || str0(c.ProjectId) != "fleet" {
		return ""
	}
	var raw string
	err := db.QueryRow(`SELECT response FROM transfers WHERE kind=? AND state='done'
		AND json_extract(body,'$.commandId')=? AND json_extract(body,'$.executionId')=?
		AND json_extract(response,'$.proof.captureArtifact')=? LIMIT 1`,
		captureStageKind, command, execution, artifact).Scan(&raw)
	var result capturePublication
	if err != nil || json.Unmarshal([]byte(raw), &result) != nil || result.Proof.CaptureArtifact != artifact ||
		result.Proof.Job.Request.Execution != execution || !sceneSHA.MatchString(result.Capture.ManifestSHA256) ||
		result.Capture.Directory != "" {
		return ""
	}
	return command
}

func digestCaptureBytes(raw []byte) string {
	hash := sha256.Sum256(raw)
	return hex.EncodeToString(hash[:])
}
