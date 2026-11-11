package contract

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// Gateway error codes that leave the provider-side outcome open: the call may have been charged.
var gatewayUnknown = map[string]bool{"provider-body": true, "provider-timeout": true, "client-timeout": true, "invalid-response": true}
var providerCode = regexp.MustCompile(`^provider-\d+$`)

// GatewayCall is one call through fleet-gateway (docs/designs/fleet-connections.md section 5), made by the coordinator
// (api steps) and by the executor's connection MCP server. It answers the call line with outcome ok|error|unknown,
// errorCode, the gateway's callId and status, and the provider's reply as output; it never fails. header carries extra
// request headers (Cloudflare Access on https).
func GatewayCall(ctx context.Context, url, token, connection, tool string, args []byte, header map[string]string) CallLine {
	line := CallLine{Tool: tool, Connection: connection, Outcome: CallOutcomeError}
	fail := func(outcome CallOutcome, code string) CallLine {
		line.Outcome, line.ErrorCode = outcome, &code
		return line
	}
	if url == "" || token == "" {
		return fail(CallOutcomeError, "connection-unavailable")
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "POST", strings.TrimRight(url, "/")+"/v1/call/"+connection+"/"+tool, bytes.NewReader(args))
	if err != nil {
		return fail(CallOutcomeError, "unreachable")
	}
	for k, v := range header {
		req.Header.Set(k, v)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "fleet-connection/1")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		var timeout net.Error
		var op *net.OpError
		switch {
		case errors.Is(err, context.DeadlineExceeded) || errors.As(err, &timeout) && timeout.Timeout():
			return fail(CallOutcomeUnknown, "client-timeout")
		case errors.As(err, &op) && op.Op == "dial":
			return fail(CallOutcomeError, "unreachable") // not connected: nothing was sent
		default:
			return fail(CallOutcomeUnknown, "unreachable")
		}
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		return fail(CallOutcomeUnknown, "connection-lost")
	}
	line.CallId, line.Status = res.Header.Get("x-fleet-call-id"), int64(res.StatusCode)
	ok := res.StatusCode/100 == 2
	if json.Valid(body) {
		line.Output = body
	} else {
		text, _ := json.Marshal(map[string]string{"text": strings.ToValidUTF8(string(body[:min(len(body), 2000)]), "�")})
		line.Output = text
		if ok {
			return fail(CallOutcomeUnknown, "invalid-response")
		}
	}
	if ok {
		line.Outcome = CallOutcomeOk
		return line
	}
	// The gateway names its own refusals in x-fleet-error; a provider reply passed through is http-<status>.
	code := res.Header.Get("x-fleet-error")
	if code == "" || providerCode.MatchString(code) {
		code = fmt.Sprintf("http-%d", res.StatusCode)
	}
	outcome := CallOutcomeError
	if res.Header.Get("x-fleet-outcome") == "unknown" || gatewayUnknown[code] {
		outcome = CallOutcomeUnknown
	}
	return fail(outcome, code)
}
