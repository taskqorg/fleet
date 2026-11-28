package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"

	"fleet/contract"
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
	expected := filepath.Join(str0(c.TempDir), "design-originals-"+proof.Job.Request.Job)
	if decodeErr != nil || verified.Proof != proof || verified.Capture.Directory != expected || !checkCapturePublication(verified.Capture) {
		return mcpText(M{"error": "Staged originals no longer match the verified Fleet archive"}, &yes), nil
	}
	observed, err := recheck()
	latest, ready := captureToolReturn(observed)
	if err != nil {
		return nil, err
	}
	if !ready || latest != proof {
		return mcpText(M{"error": "Capture authority changed before handoff"}, &yes), nil
	}
	return mcpText(M{"state": "staged", "capture": verified.Capture}, nil), nil
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

func (c *transferClient) captureArtifact(execution string, pin captureTransferPin) (io.ReadCloser, *failure) {
	r, f := transferReply(c, contract.RouteArtifactsGet, contract.HostArtifactGetRequest{
		HostId: c.host, ExecutionId: &execution, ArtifactId: pin.ID,
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

func executeCaptureStage(root string, request captureStageRequest,
	authorize func() (contract.DesignCaptureReturn, *failure),
	get func(string, captureTransferPin) (io.ReadCloser, *failure),
) (capturePublication, *failure) {
	var none capturePublication
	proof, f := authorize()
	if f != nil {
		return none, f
	}
	if !captureID.MatchString(proof.Job.Request.Job) || proof.Job.Request.Execution != request.ExecutionID {
		return none, permanentf("Capture job belongs to another execution")
	}

	// Executor-owned sibling of the agent's TMPDIR and artifact_save roots.
	// The transfer lane is serial. A retry can safely replace private scratch.
	private := capturePrivateDir(root, request.ExecutionID)
	if err := os.MkdirAll(private, 0700); err != nil {
		return none, ioFailure(err)
	}
	final := filepath.Join(runTempDir(root, request.ExecutionID), "design-originals-"+proof.Job.Request.Job)
	marker := filepath.Join(private, "publication.json")
	if _, err := os.Lstat(final); err == nil {
		saved, err := os.ReadFile(marker)
		var proven capturePublication
		if err != nil || json.Unmarshal(saved, &proven) != nil || proven.Proof != proof || proven.Capture.Directory != final || !checkCapturePublication(proven.Capture) {
			return none, permanentf("Existing staged files do not match the verified capture")
		}
		return proven, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return none, ioFailure(err)
	}

	scratch := filepath.Join(private, "verify")
	if err := os.RemoveAll(scratch); err != nil {
		return none, ioFailure(err)
	}
	if err := os.Mkdir(scratch, 0700); err != nil {
		return none, ioFailure(err)
	}
	defer os.RemoveAll(scratch)

	receipt, f := get(request.ExecutionID, captureTransferPin{proof.ReceiptArtifact, proof.Job.Request.Job, "receipt.json", proof.Job.ReceiptSHA256, captureReceiptLimit})
	if f != nil {
		return none, f
	}
	raw, err := io.ReadAll(io.LimitReader(receipt, captureReceiptLimit+1))
	if err := errors.Join(err, receipt.Close()); err != nil {
		return none, ioFailure(err)
	}
	archive, f := get(request.ExecutionID, captureTransferPin{proof.CaptureArtifact, proof.Job.Request.Job, "capture.tar", proof.Job.Capture.SHA256, proof.Job.Capture.Bytes})
	if f != nil {
		return none, f
	}
	verified, err := stageDesignerCapture(context.Background(), scratch, proof.Job.Request.DesignCaptureScope, proof.Job, raw, archive)
	if err := errors.Join(err, archive.Close()); err != nil {
		return none, permanentf("Capture archive failed verification")
	}
	// Persist the exact proven handoff before publication, so losing the reply
	// or restarting between rename and settlement cannot manufacture a replay.
	publish := capturePublication{Proof: proof, Capture: verified}
	publish.Capture.Directory = final
	if err := saveCapturePublication(private, marker, publish); err != nil {
		return none, ioFailure(err)
	}
	published := false
	defer func() {
		if !published {
			_ = os.Remove(marker)
		}
	}()
	latest, f := authorize()
	if f != nil {
		return none, f
	}
	if latest != proof {
		return none, permanentf("Capture authorization changed during transfer")
	}
	if err := publishCaptureDirectory(verified.Directory, final); err != nil {
		return none, ioFailure(err)
	}
	published = true
	return publish, nil
}

func capturePrivateDir(root, execution string) string {
	return filepath.Join(root, "design-capture-staging", execution)
}

func saveCapturePublication(dir, marker string, publication capturePublication) error {
	f, err := os.CreateTemp(dir, "publication-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	writeErr := json.NewEncoder(f).Encode(publication)
	if err := errors.Join(writeErr, f.Sync(), f.Close()); err != nil {
		return err
	}
	if err := os.Rename(f.Name(), marker); err != nil {
		return err
	}
	root, err := os.Open(dir)
	if err != nil {
		return err
	}
	return errors.Join(root.Sync(), root.Close())
}

func checkCapturePublication(staged appCaptureReceipt) bool {
	if !sceneSHA.MatchString(staged.ManifestSHA256) || len(staged.Files) < 7 || len(staged.Files) > 2+maxCaptureCases*5 {
		return false
	}
	st, err := os.Lstat(staged.Directory)
	if err != nil || !st.IsDir() || st.Mode().Perm() != 0700 || owner(st) != os.Geteuid() {
		return false
	}
	manifest, err := openInside(staged.Directory, []string{"handoff.json"})
	if err != nil {
		return false
	}
	raw, err := io.ReadAll(io.LimitReader(manifest, maxCaptureManifest+1))
	if errors.Join(err, manifest.Close()) != nil || len(raw) > maxCaptureManifest || digestCaptureBytes(raw) != staged.ManifestSHA256 {
		return false
	}
	var handoff appCaptureManifest
	if json.Unmarshal(raw, &handoff) != nil || len(handoff.Cases) == 0 || len(handoff.Cases) > maxCaptureCases {
		return false
	}
	expected := map[string]string{"handoff.json": staged.ManifestSHA256, "source.tar": handoff.SourceSHA256}
	for _, c := range handoff.Cases {
		expected[c.RecipeFile] = c.RecipeSHA256
		for _, frame := range c.Frames {
			expected[frame.File] = frame.SHA256
		}
	}
	entries, err := os.ReadDir(staged.Directory)
	if err != nil || len(entries) != len(expected) || len(staged.Files) != len(expected) {
		return false
	}
	for _, entry := range entries {
		name := entry.Name()
		want := expected[name]
		if !sceneSHA.MatchString(want) || !slices.Contains(staged.Files, name) || !checkCaptureFile(staged.Directory, name, want) {
			return false
		}
	}
	return true
}

func checkCaptureFile(directory, name, want string) bool {
	f, err := openInside(directory, []string{name})
	if err != nil {
		return false
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() || st.Size() < 1 || st.Size() > maxCaptureSource {
		return false
	}
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(f, st.Size()+1))
	return err == nil && n == st.Size() && hex.EncodeToString(h.Sum(nil)) == want
}

func digestCaptureBytes(raw []byte) string {
	hash := sha256.Sum256(raw)
	return hex.EncodeToString(hash[:])
}
