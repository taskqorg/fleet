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
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Gateway error codes that leave the provider-side outcome open when the gateway names no outcome of its own.
var gatewayUnknown = map[string]bool{"provider-body": true, "provider-timeout": true, "client-timeout": true, "invalid-response": true}
var providerCode = regexp.MustCompile(`^provider-\d+$`)

// GatewayTarget is where and as whom a call goes: the gateway URL, the bearer, extra headers (Cloudflare Access on
// https) and whether the tool may be repeated (it decides how an answer from the edge, not the gateway, reads).
type GatewayTarget struct {
	URL, Token string
	Header     map[string]string
	Idempotent bool
	// Stall and Answer override the client waits when longer (a connection's raised stallMs/answerMs); 0 keeps them.
	Stall, Answer time.Duration
}

// waits are the client waits for this target: the environment's, raised to the target's when those are longer.
func (t GatewayTarget) waits() (stall, answer time.Duration) {
	stall, answer = clientWaits()
	return max(stall, t.Stall), max(answer, t.Answer)
}

// clientWaits are the progress timeouts (docs/designs/fleet-connections.md §9 Client timeouts): stall, the longest
// time without progress in either direction (FLEET_STALL_MS, default 60 s); answer, the wait for the response headers
// after the last byte of a file call's request (FLEET_ANSWER_MS, default 180 s, above the gateway's own answerMs).
func clientWaits() (stall, answer time.Duration) {
	wait := func(name string, fallback time.Duration) time.Duration {
		if ms, err := strconv.ParseInt(os.Getenv(name), 10, 64); err == nil && ms > 0 {
			return time.Duration(ms) * time.Millisecond
		}
		return fallback
	}
	return wait("FLEET_STALL_MS", 60*time.Second), wait("FLEET_ANSWER_MS", 180*time.Second)
}

// gatewayClient: the default transport (it waits up to 1 s for 100 Continue, so a gateway refusal can arrive before an
// upload's body) without following redirects: an Access login redirect is an answer, not a place to send the call.
var gatewayClient = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

// watchdog cancels a call when no progress happened within the armed wait. Each progress re-arms it. Once the answer
// arrived the request body no longer arms it (the transport may still read it), and once stopped nothing does.
type watchdog struct {
	mu                sync.Mutex
	timer             *time.Timer
	fired             atomic.Bool
	answered, stopped bool
}

func newWatchdog(cancel context.CancelFunc, wait time.Duration) *watchdog {
	w := &watchdog{}
	w.timer = time.AfterFunc(wait, func() { w.fired.Store(true); cancel() })
	return w
}

// arm re-arms the wait; request marks progress of the request body.
func (w *watchdog) arm(wait time.Duration, request bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.stopped && !(request && w.answered) {
		w.timer.Reset(wait)
	}
}

// answer switches to the answer's stall wait.
func (w *watchdog) answer(stall time.Duration) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.answered = true
	if !w.stopped {
		w.timer.Reset(stall)
	}
}

func (w *watchdog) stop() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.stopped = true
	w.timer.Stop()
}

// bodyReader is a request body of want bytes. Each read re-arms the stall wait; it is done, and arms the answer wait,
// only once all want bytes were read (a file that shrank since its first pass never left whole). The answer wait
// counts from the last byte and, for a file, adds the time the body took: the gateway posts the staged file on before
// its own answer wait starts, and over loopback that takes no longer than the client's send did.
type bodyReader struct {
	r            io.Reader
	dog          *watchdog
	stall, after time.Duration
	file         bool
	begun        time.Time
	want, n      int64
	done         atomic.Bool
}

func (p *bodyReader) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	p.n += int64(n)
	if err == io.EOF && p.n == p.want {
		p.done.Store(true)
		wait := p.after
		if p.file {
			wait += time.Since(p.begun)
		}
		p.dog.arm(wait, true)
	} else {
		p.dog.arm(p.stall, true)
	}
	return n, err
}

// answerReader re-arms the stall wait with each read of the answer body.
type answerReader struct {
	r     io.Reader
	dog   *watchdog
	stall time.Duration
}

func (p *answerReader) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	p.dog.arm(p.stall, false)
	return n, err
}

// reader is the answer body with the stall wait re-armed on each read.
func (x *exchange) reader() io.Reader {
	return &answerReader{r: x.res.Body, dog: x.dog, stall: x.stall}
}

// exchange is one request to the gateway with its answer still open; line holds the outcome when the request failed.
type exchange struct {
	line   CallLine
	res    *http.Response
	dog    *watchdog
	stall  time.Duration
	cancel context.CancelFunc
	failed bool
}

func (x *exchange) fail(outcome CallOutcome, code string) {
	x.line.Outcome, x.line.ErrorCode, x.failed = outcome, &code, true
}

func (x *exchange) close() {
	if x.res != nil {
		x.res.Body.Close()
	}
	x.dog.stop()
	x.cancel()
}

// send makes the request. open makes the body afresh (the transport may resend a body that a stale kept-alive
// connection never took), length bytes long, counted by a progress reader, so a failure before its last byte is known
// to have reached nothing. file marks a file upload. wait is the header wait after the request's last byte; dial, TLS
// and the write run under the stall wait.
func send(ctx context.Context, t GatewayTarget, connection, tool string, open func() io.Reader, length int64, file bool, headers map[string]string, wait time.Duration) *exchange {
	stall, _ := t.waits()
	ctx, cancel := context.WithCancel(ctx)
	x := &exchange{line: CallLine{Tool: tool, Connection: connection, Outcome: CallOutcomeError}, stall: stall, cancel: cancel}
	x.dog = newWatchdog(cancel, stall)
	if t.URL == "" || t.Token == "" {
		x.fail(CallOutcomeError, "connection-unavailable")
		return x
	}
	var current atomic.Pointer[bodyReader] // the body last handed to the transport
	fresh := func() *bodyReader {
		p := &bodyReader{r: open(), dog: x.dog, stall: stall, after: wait, file: file, begun: time.Now(), want: length}
		current.Store(p)
		return p
	}
	var reader io.Reader = http.NoBody
	if length > 0 {
		reader = fresh()
	} else {
		x.dog.arm(wait, true)
	}
	req, err := http.NewRequestWithContext(ctx, "POST", strings.TrimRight(t.URL, "/")+"/v1/call/"+connection+"/"+tool, reader)
	if err != nil {
		x.fail(CallOutcomeError, "unreachable")
		return x
	}
	req.ContentLength = length
	if length > 0 {
		req.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(fresh()), nil }
	}
	for k, v := range t.Header {
		req.Header.Set(k, v)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	req.Header.Set("Authorization", "Bearer "+t.Token)
	if req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("User-Agent", "fleet-connection/1")
	res, err := gatewayClient.Do(req)
	sentAll := length == 0 || current.Load().done.Load()
	if err != nil {
		var timeout net.Error
		var op *net.OpError
		switch {
		case errors.As(err, &op) && op.Op == "dial":
			x.fail(CallOutcomeError, "unreachable") // not connected: nothing was sent
		case !sentAll && file:
			// The gateway sends an upload on only once it has every byte: a cut before the last one reached nothing.
			x.fail(CallOutcomeError, "upload-interrupted")
		case !sentAll:
			x.fail(CallOutcomeError, "unreachable") // the request never left whole, so the gateway cannot have run it
		case x.dog.fired.Load() || errors.Is(err, context.DeadlineExceeded) || errors.As(err, &timeout) && timeout.Timeout():
			x.fail(CallOutcomeUnknown, "client-timeout")
		case file:
			x.fail(CallOutcomeUnknown, "connection-lost")
		default:
			x.fail(CallOutcomeUnknown, "unreachable")
		}
		return x
	}
	x.res = res
	x.dog.answer(stall)
	x.line.CallId, x.line.Status = res.Header.Get("x-fleet-call-id"), int64(res.StatusCode)
	// A 5xx without the gateway's call id came from the edge (Cloudflare, Caddy): whether the gateway got and sent the
	// call is open, unless the tool may be repeated or the request never left whole.
	if res.StatusCode >= 500 && x.line.CallId == "" {
		text := readSome(x)
		x.line.Output = text
		outcome := CallOutcomeUnknown
		if t.Idempotent || !sentAll {
			outcome = CallOutcomeError
		}
		x.fail(outcome, fmt.Sprintf("edge-%d", res.StatusCode))
	}
	return x
}

// readSome is the first 2000 bytes of an answer as {"text": ...}.
func readSome(x *exchange) json.RawMessage {
	b, _ := io.ReadAll(io.LimitReader(x.reader(), 2000))
	return textOf(b)
}

// textOf is a non-JSON answer's first 2000 bytes as {"text": ...}.
func textOf(b []byte) json.RawMessage {
	text, _ := json.Marshal(map[string]string{"text": strings.ToValidUTF8(string(b[:min(len(b), 2000)]), "�")})
	return text
}

// answerJSON reads a JSON answer into the call line: ok, the gateway's refusal or the provider's status.
func (x *exchange) answerJSON() CallLine {
	body, err := io.ReadAll(x.reader())
	if err != nil {
		if x.dog.fired.Load() {
			x.fail(CallOutcomeUnknown, "client-timeout")
		} else {
			x.fail(CallOutcomeUnknown, "connection-lost")
		}
		return x.line
	}
	res := x.res
	ok := res.StatusCode/100 == 2
	if json.Valid(body) {
		x.line.Output = body
	} else {
		x.line.Output = textOf(body)
		if ok {
			x.fail(CallOutcomeUnknown, "invalid-response")
			return x.line
		}
	}
	if ok {
		x.line.Outcome = CallOutcomeOk
		return x.line
	}
	// The gateway names its own refusals in x-fleet-error; a provider reply passed through is http-<status>.
	code := res.Header.Get("x-fleet-error")
	if code == "" || providerCode.MatchString(code) {
		code = fmt.Sprintf("http-%d", res.StatusCode)
	}
	// The gateway's outcome header decides; without one (not the gateway's answer) the code does.
	outcome, named := CallOutcomeError, res.Header.Get("x-fleet-outcome")
	if named == "unknown" || named == "" && gatewayUnknown[code] {
		outcome = CallOutcomeUnknown
	}
	x.fail(outcome, code)
	return x.line
}

// GatewayCall is one JSON call through fleet-gateway (docs/designs/fleet-connections.md section 5), made by the
// coordinator (api steps) and by the executor's connection MCP server. It answers the call line with outcome
// ok|error|unknown, errorCode, the gateway's callId and status, and the provider's reply as output; it never fails.
func GatewayCall(ctx context.Context, t GatewayTarget, connection, tool string, args []byte) CallLine {
	stall, _ := t.waits()
	x := send(ctx, t, connection, tool, func() io.Reader { return bytes.NewReader(args) }, int64(len(args)), false, nil, stall)
	defer x.close()
	if x.failed {
		return x.line
	}
	return x.answerJSON()
}
