package contract

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"io"
	"net/http"
	"strconv"
	"time"
)

const (
	UploadModeHeader   = "X-Fleet-Upload-Mode"
	UploadIDHeader     = "X-Fleet-Upload-Id"
	UploadSizeHeader   = "X-Fleet-Upload-Size"
	UploadOffsetHeader = "X-Fleet-Upload-Offset"

	// At the observed 3 Mbit/s, 4 MiB takes about 12 seconds per request.
	UploadChunkBytes int64 = 4 << 20
)

// resumableUpload keeps one ID across reconnects. Stage requests are safe to replay because they cannot call the
// provider and the gateway only appends at its current offset. An uncertain commit is returned for reconciliation.
func resumableUpload(ctx context.Context, t GatewayTarget, connection, tool string, headers map[string]string, u Upload) (CallLine, bool) {
	var nonce [16]byte
	rand.Read(nonce[:])
	headers[UploadIDHeader] = strconv.FormatInt(time.Now().Unix(), 10) + "-" + hex.EncodeToString(nonce[:])
	headers[UploadSizeHeader] = strconv.FormatInt(u.Size, 10)
	stall, answer := t.waits()

	request := func(mode string, offset, length int64) *exchange {
		headers[UploadModeHeader] = mode
		headers[UploadOffsetHeader] = strconv.FormatInt(offset, 10)
		headers["Content-Type"] = "application/json"
		delete(headers, "Expect")
		wait := stall
		if mode == "chunk" {
			headers["Content-Type"] = "application/octet-stream"
			headers["Expect"] = "100-continue"
		} else if mode == "commit" {
			wait = answer
		}
		target := t
		target.Idempotent = mode != "commit"
		open := func() io.Reader { return io.NewSectionReader(u.File, offset, length) }
		return send(ctx, target, connection, tool, open, length, mode == "chunk", headers, wait)
	}

	stage := func(mode string, offset, length int64) (int64, CallLine) {
		deadline := time.Now().Add(stall)
		for {
			x := request(mode, offset, length)
			if !x.failed && x.res.StatusCode == http.StatusNoContent {
				n, err := strconv.ParseInt(x.res.Header.Get(UploadOffsetHeader), 10, 64)
				if err == nil && n >= offset && n <= offset+length && (mode != "chunk" || n > offset) {
					x.close()
					x.line.Outcome = CallOutcomeOk
					return n, x.line
				}
				x.fail(CallOutcomeError, "upload-offset")
			}
			if !x.failed {
				x.answerJSON()
				if x.line.Outcome == CallOutcomeOk {
					x.fail(CallOutcomeError, "invalid-response")
				}
			}
			x.close()
			code := ""
			if x.line.ErrorCode != nil {
				code = *x.line.ErrorCode
			}
			if code != "draining" && code != "upload-busy" && code != "upload-interrupted" &&
				code != "unreachable" && code != "client-timeout" && code != "connection-lost" &&
				!(x.line.Status >= 500 && x.line.CallId == "") {
				return offset, x.line
			}
			if !retryUpload(ctx, deadline) {
				x.line.Outcome = CallOutcomeError // the provider is unreachable until the commit
				return offset, x.line
			}
		}
	}

	offset, line := stage("status", 0, 0)
	if line.Outcome != CallOutcomeOk {
		// Older gateways refuse the bodyless JSON probe before reading a file or contacting the provider.
		legacy := line.Status == 400 && line.ErrorCode != nil && *line.ErrorCode == "file-expected" && line.CallId != ""
		if !legacy {
			line.Outcome = CallOutcomeError
		}
		return line, legacy
	}
	for offset < u.Size {
		offset, line = stage("chunk", offset, min(UploadChunkBytes, u.Size-offset))
		if line.Outcome != CallOutcomeOk {
			// A failed stage cannot have reached the provider. Drop its bytes when the gateway is reachable.
			x := request("abort", 0, 0)
			x.close()
			line.Outcome = CallOutcomeError
			return line, false
		}
	}
	deadline := time.Now().Add(stall)
	for {
		x := request("commit", 0, 0)
		line := x.line
		if !x.failed {
			line = x.answerJSON()
		}
		x.close()
		if line.Outcome != CallOutcomeError || line.ErrorCode == nil ||
			(*line.ErrorCode != "draining" && *line.ErrorCode != "upload-busy") || !retryUpload(ctx, deadline) {
			return line, false
		}
	}
}

func retryUpload(ctx context.Context, deadline time.Time) bool {
	if ctx.Err() != nil || !time.Now().Before(deadline) {
		return false
	}
	timer := time.NewTimer(min(time.Second, time.Until(deadline)))
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return time.Now().Before(deadline)
	}
}
