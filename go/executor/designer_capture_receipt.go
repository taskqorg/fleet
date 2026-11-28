package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"

	"fleet/contract"
)

type designerCaptureScope = contract.DesignCaptureScope
type designerCaptureRequest = contract.DesignCaptureJobRequest

type designerCaptureSource struct {
	SHA256        string `json:"sha256"`
	BaseTree      string `json:"baseTree"`
	Tree          string `json:"tree"`
	PatchSHA256   string `json:"patchSha256"`
	ArchiveSHA256 string `json:"archiveSha256"`
}

type designerCaptureRuntime = contract.DesignCaptureRuntime
type designerCaptureArchive = contract.DesignCaptureArchive

type designerCaptureReceipt struct {
	Version       int                    `json:"version"`
	Request       designerCaptureRequest `json:"request"`
	RequestSHA256 string                 `json:"requestSha256"`
	Producer      appCaptureProducer     `json:"producer"`
	Capture       designerCaptureArchive `json:"capture"`
	Source        designerCaptureSource  `json:"source"`
	Runtime       designerCaptureRuntime `json:"runtime"`
}

// A Fleet owner supplies these facts from the authorized terminal host job,
// confirmed teardown and stored artifact rows. They never come from tool args.
type designerCaptureStoredJob = contract.DesignCaptureStoredJob

func verifyDesignerCapture(ctx context.Context, active designerCaptureScope, job designerCaptureStoredJob, raw []byte,
	archive io.Reader, validateReplay func([]byte) error) (appCaptureReceipt, error) {
	if validateReplay == nil {
		return appCaptureReceipt{}, errAppCapture
	}
	return checkDesignerCapture(active, job, raw, func(request appCaptureRequest) (appCaptureReceipt, error) {
		return verifyAppCapture(ctx, request, archive, validateReplay, nil)
	})
}

func checkDesignerCapture(active designerCaptureScope, job designerCaptureStoredJob, raw []byte,
	verify func(appCaptureRequest) (appCaptureReceipt, error)) (appCaptureReceipt, error) {
	r := job.Request
	rt := job.Runtime
	if r.DesignCaptureScope != active || active.Project != "fleet" || r.Version != 1 ||
		!captureID.MatchString(active.Task) || !captureID.MatchString(r.Job) || r.Job == active.Execution ||
		!sceneSHA.MatchString(job.RequestSHA256) || len(raw) == 0 || len(raw) > 8192 ||
		!sceneSHA.MatchString(rt.ImageSHA256) || !sceneSHA.MatchString(rt.FontSHA256) || !sceneSHA.MatchString(rt.LockSHA256) ||
		rt.Node == "" || rt.Chromium == "" || rt.Playwright == "" {
		return appCaptureReceipt{}, errAppCapture
	}
	sum := sha256.Sum256(raw)
	if hex.EncodeToString(sum[:]) != job.ReceiptSHA256 {
		return appCaptureReceipt{}, errAppCapture
	}
	var measured designerCaptureReceipt
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if exactKeys(raw, "version", "request", "project", "task", "session", "execution", "job", "nonce", "base",
		"baseSha256", "overlaySha256", "fixtureSha256", "requestSha256", "producer", "commit", "target",
		"capture", "name", "bytes", "sha256", "source", "baseTree", "tree", "patchSha256", "archiveSha256",
		"runtime", "imageSha256", "fontSha256", "lockSha256", "node", "chromium", "playwright") != nil ||
		decoder.Decode(&measured) != nil || decoder.Decode(new(json.RawMessage)) != io.EOF ||
		measured.Version != 1 || measured.Request != r || measured.RequestSHA256 != job.RequestSHA256 ||
		measured.Producer != job.Producer || measured.Runtime != job.Runtime || measured.Capture != job.Capture ||
		job.Capture.Name != "capture.tar" || job.Capture.Bytes <= 0 || job.Capture.Bytes > maxCaptureWire {
		return appCaptureReceipt{}, errAppCapture
	}
	return verify(appCaptureRequest{
		Project: active.Project, Session: active.Session, Execution: active.Execution,
		Nonce: r.Nonce, Base: r.Base, BaseSHA256: r.BaseSHA256, OverlaySHA256: r.OverlaySHA256,
		FixtureSHA256: r.FixtureSHA256, CaptureSHA256: job.Capture.SHA256, measured: &measured,
	})
}
