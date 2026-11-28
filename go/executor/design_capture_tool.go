package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"

	"fleet/contract"
)

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

func getCaptureArtifact(execution string, db *sql.DB, dir, id string, limit int64) (*os.File, bool, error) {
	key := newID()
	if err := enqueue(db, key, "get", M{"executionId": execution, "artifactId": id, "inputDir": dir,
		"existingInputDir": true, "maxDownloadBytes": limit}); err != nil {
		return nil, false, err
	}
	r, err := wait(db, "transfers", key, 600, false)
	if err != nil {
		return nil, false, err
	}
	if r == nil {
		return nil, true, nil
	}
	if r.Keys.Error.Set {
		return nil, false, errAppCapture
	}
	reply := typed[struct {
		ArtifactID string `json:"artifactId"`
		Path       string `json:"path"`
	}](r.Value)
	parts := insideParts(dir, pathParts(reply.Path))
	if reply.ArtifactID != id || len(parts) != 1 {
		return nil, false, errAppCapture
	}
	file, err := openInside(dir, parts)
	if err != nil {
		return nil, false, errAppCapture
	}
	st, err := file.Stat()
	if err != nil || !st.Mode().IsRegular() || st.Size() <= 0 || st.Size() > limit {
		file.Close()
		return nil, false, errAppCapture
	}
	return file, false, nil
}

// Only a result from the authenticated delegation journal reaches staging.
// The caller cannot pass source paths, job IDs, hashes or artifact IDs here.
func stageCaptureTool(result any, c *delegationContext, db *sql.DB, recheck func() (any, error)) (any, error) {
	return stageCaptureWithTransfer(result, c, func(execution, dir, id string, limit int64) (*os.File, bool, error) {
		return getCaptureArtifact(execution, db, dir, id, limit)
	}, recheck)
}

func stageCaptureWithTransfer(result any, c *delegationContext, get func(execution, dir, id string, limit int64) (*os.File, bool, error), recheck func() (any, error)) (any, error) {
	proof, ok := captureToolReturn(result)
	if !ok {
		return result, nil
	}
	execution, _ := c.execution()
	if proof.Job.Request.Execution != execution || str0(c.ProjectId) != "fleet" || str0(c.TempDir) == "" {
		return mcpText(M{"error": errAppCapture.Error()}, &yes), nil
	}
	// The capture job is unique to this execution. Reserve its one staging
	// directory so overlapping and repeated calls cannot duplicate archives.
	if !captureID.MatchString(proof.Job.Request.Job) {
		return mcpText(M{"error": errAppCapture.Error()}, &yes), nil
	}
	root := filepath.Join(*c.TempDir, "design-originals-"+proof.Job.Request.Job)
	readyPath := filepath.Join(root, "originals")
	if err := os.Mkdir(root, 0700); err != nil {
		if errors.Is(err, os.ErrExist) {
			if st, statErr := os.Lstat(readyPath); statErr == nil && st.IsDir() && st.Mode().Perm() == 0700 && owner(st) == os.Geteuid() {
				return mcpText(M{"state": "already-staged", "directory": readyPath}, nil), nil
			}
			return mcpText(M{"state": "staging"}, nil), nil
		}
		return mcpText(M{"error": errAppCapture.Error()}, &yes), nil
	}
	exposed := false
	defer func() {
		if !exposed {
			_ = os.RemoveAll(root)
		}
	}()
	incoming := filepath.Join(root, "incoming")
	if err := os.Mkdir(incoming, 0o700); err != nil {
		return mcpText(M{"error": errAppCapture.Error()}, &yes), nil
	}
	receipt, pending, err := get(execution, incoming, proof.ReceiptArtifact, captureReceiptLimit)
	if err != nil {
		return mcpText(M{"error": errAppCapture.Error()}, &yes), nil
	}
	if pending {
		return mcpText(M{"state": "transfer-pending"}, nil), nil
	}
	body, err := io.ReadAll(io.LimitReader(receipt, captureReceiptLimit+1))
	if errors.Join(err, receipt.Close()) != nil {
		return mcpText(M{"error": errAppCapture.Error()}, &yes), nil
	}
	archive, pending, err := get(execution, incoming, proof.CaptureArtifact, maxCaptureWire)
	if err != nil {
		return mcpText(M{"error": errAppCapture.Error()}, &yes), nil
	}
	if pending {
		return mcpText(M{"state": "transfer-pending"}, nil), nil
	}
	staged, err := stageDesignerCapture(context.Background(), root, proof.Job.Request.DesignCaptureScope, proof.Job, body, archive)
	if errors.Join(err, archive.Close(), os.RemoveAll(incoming)) != nil {
		return mcpText(M{"error": errAppCapture.Error()}, &yes), nil
	}
	// Transfers can complete after cancellation or replacement. Revalidate
	// before publishing any paths back to the designer.
	observed, err := recheck()
	latest, ready := captureToolReturn(observed)
	if err != nil || !ready || latest != proof {
		return mcpText(M{"error": "Capture authorization changed during transfer"}, &yes), nil
	}
	if err := os.Rename(staged.Directory, readyPath); err != nil {
		return mcpText(M{"error": errAppCapture.Error()}, &yes), nil
	}
	staged.Directory = readyPath
	exposed = true
	return mcpText(M{"state": "staged", "capture": staged}, nil), nil
}
