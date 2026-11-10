package main

// The S3 subset Fleet uses against Garage, signed with AWS Signature Version 4 by hand (fleet.md Stack: minio-go would add
// 19 modules). Same behaviour as packages/coordinator/s3.ts: region 'garage', path-style URLs, no checksums, Access headers
// and Accept-Encoding added after signing (the tunnel rewrites Accept-Encoding), 3 attempts with jittered backoff on
// 429/5xx, throttling codes and network errors, the clock offset learnt from Date after RequestTimeTooSkewed, and an idle
// timeout (FLEET_S3_IDLE_TIMEOUT_MS, default 60 s) instead of a total request cap.

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"html"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

type S3Error struct {
	Status int
	Code   string
	Msg    string
}

func (e *S3Error) Error() string { return e.Msg }

type S3Part struct {
	PartNumber int    `json:"partNumber"`
	ETag       string `json:"etag"`
}

func sha256hex(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

func hmacSHA(k []byte, s string) []byte {
	m := hmac.New(sha256.New, k)
	m.Write([]byte(s))
	return m.Sum(nil)
}

// s3enc: RFC 3986 encoding as SigV4 requires; with slash, '/' stays literal (an object key path).
func s3enc(s string, slash bool) string {
	var b strings.Builder
	for _, c := range []byte(s) {
		if c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.' || c == '~' || slash && c == '/' {
			b.WriteByte(c)
		} else {
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

// s3query: canonical query string, sorted by the encoded name.
func s3query(q map[string]string) string {
	parts := make([]string, 0, len(q))
	for k, v := range q {
		parts = append(parts, s3enc(k, false)+"="+s3enc(v, false))
	}
	sort.Strings(parts) // names are unique, so sorting k=v sorts by k ('=' sorts below every unreserved char)
	return strings.Join(parts, "&")
}

var spaces = regexp.MustCompile(`\s+`)

// sigv4 signs one request and returns the signature and the signed header list. headers must include host.
func sigv4(method, path string, q, headers map[string]string, payload, keyID, secret, region, date string) (sig, signed, scope string) {
	lower := map[string]string{}
	names := []string{}
	for k, v := range headers {
		k = strings.ToLower(k)
		lower[k] = v
		names = append(names, k)
	}
	sort.Strings(names)
	var canon strings.Builder
	for _, n := range names {
		canon.WriteString(n + ":" + spaces.ReplaceAllString(strings.TrimSpace(lower[n]), " ") + "\n")
	}
	signed = strings.Join(names, ";")
	creq := strings.Join([]string{method, path, s3query(q), canon.String(), signed, payload}, "\n")
	day := date[:8]
	scope = day + "/" + region + "/s3/aws4_request"
	key := hmacSHA(hmacSHA(hmacSHA(hmacSHA([]byte("AWS4"+secret), day), region), "s3"), "aws4_request")
	sig = hex.EncodeToString(hmacSHA(key, strings.Join([]string{"AWS4-HMAC-SHA256", date, scope, sha256hex([]byte(creq))}, "\n")))
	return
}

type S3 struct {
	base                  *url.URL
	host, prefix          string
	keyID, secret, region string
	extra                 func() map[string]string // Access headers, added after signing
	idle                  time.Duration
	offset                atomic.Int64 // server time minus local time, ms
	client                *http.Client
}

func newS3(endpoint, keyID, secret string, extra func() map[string]string) (*S3, error) {
	u, err := url.Parse(endpoint)
	if err != nil {
		return nil, err
	}
	idle := 60 * time.Second
	if ms, _ := strconv.Atoi(os.Getenv("FLEET_S3_IDLE_TIMEOUT_MS")); ms > 0 {
		idle = time.Duration(ms) * time.Millisecond
	}
	s := &S3{base: u, host: u.Host, prefix: strings.TrimSuffix(u.Path, "/"), keyID: keyID, secret: secret, region: "garage", extra: extra, idle: idle}
	d := &net.Dialer{Timeout: 30 * time.Second}
	tr := &http.Transport{Proxy: http.ProxyFromEnvironment, DialContext: (&idleDialer{d, idle}).dial, DisableCompression: true, ResponseHeaderTimeout: 0}
	// No redirects: a store reply must not send signed requests and Access headers elsewhere; a 3xx is an error.
	s.client = &http.Client{Transport: tr, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return s, nil
}

// idleDialer: every read or write on the connection pushes its deadline idle further out, so a long transfer keeps going but
// a half-open tunnel fails as a network error (and is retried).
type idleDialer struct {
	d    *net.Dialer
	idle time.Duration
}

func (i *idleDialer) dial(ctx context.Context, network, addr string) (net.Conn, error) {
	c, err := i.d.DialContext(ctx, network, addr)
	if err != nil {
		return nil, err
	}
	return &idleConn{c, i.idle}, nil
}

type idleConn struct {
	net.Conn
	idle time.Duration
}

func (c *idleConn) Read(b []byte) (int, error) {
	c.Conn.SetDeadline(time.Now().Add(c.idle))
	return c.Conn.Read(b)
}
func (c *idleConn) Write(b []byte) (int, error) {
	c.Conn.SetDeadline(time.Now().Add(c.idle))
	return c.Conn.Write(b)
}

func (s *S3) path(bucket, key string) string {
	p := s.prefix + "/" + s3enc(bucket, false)
	if key != "" {
		p += "/" + s3enc(key, true)
	}
	return p
}

func (s *S3) now() time.Time {
	return time.Now().Add(time.Duration(s.offset.Load()) * time.Millisecond)
}

func amzDate(t time.Time) string { return t.UTC().Format("20060102T150405Z") }

var xmlTag = map[string]*regexp.Regexp{}

func tag(xml, name string) (string, bool) {
	re, ok := xmlTag[name]
	if !ok {
		re = regexp.MustCompile("<" + name + ">([^<]*)</" + name + ">")
		xmlTag[name] = re
	}
	m := re.FindStringSubmatch(xml)
	if m == nil {
		return "", false
	}
	return html.UnescapeString(m[1]), true
}

func init() {
	for _, n := range []string{"Code", "Message", "UploadId", "ETag"} {
		tag("", n)
	}
}

var retryStatus = map[int]bool{429: true, 500: true, 502: true, 503: true, 504: true}
var retryCodes = map[string]bool{"SlowDown": true, "Throttling": true, "ThrottlingException": true, "RequestTimeout": true, "RequestTimeTooSkewed": true}

// once: one attempt; a non-2xx status is an *S3Error, the response body is the caller's to close.
func (s *S3) once(method, path string, q, headers map[string]string, body []byte) (*http.Response, error) {
	date := amzDate(s.now())
	payload := sha256hex(nil)
	if body != nil {
		payload = sha256hex(body)
	}
	h := map[string]string{}
	enc := ""
	for k, v := range headers {
		if strings.EqualFold(k, "accept-encoding") {
			enc = v
			continue
		}
		h[strings.ToLower(k)] = v
	}
	h["host"], h["x-amz-date"], h["x-amz-content-sha256"] = s.host, date, payload
	if body != nil {
		h["content-length"] = strconv.Itoa(len(body))
	}
	sig, signed, scope := sigv4(method, path, q, h, payload, s.keyID, s.secret, s.region, date)
	u := s.base.Scheme + "://" + s.host + path
	if qs := s3query(q); qs != "" {
		u += "?" + qs
	}
	req, err := http.NewRequest(method, u, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.ContentLength = int64(len(body))
	for k, v := range h {
		if k != "host" && k != "content-length" {
			req.Header.Set(k, v)
		}
	}
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential="+s.keyID+"/"+scope+", SignedHeaders="+signed+", Signature="+sig)
	if s.extra != nil {
		for k, v := range s.extra() {
			req.Header.Set(k, v)
		}
	}
	if enc != "" {
		req.Header.Set("Accept-Encoding", enc)
	}
	res, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	if res.StatusCode >= 200 && res.StatusCode < 300 {
		return res, nil
	}
	text := ""
	if method != "HEAD" {
		b, _ := io.ReadAll(res.Body)
		text = string(b)
	}
	res.Body.Close()
	code, ok := tag(text, "Code")
	if !ok {
		code = strconv.Itoa(res.StatusCode)
	}
	if code == "RequestTimeTooSkewed" {
		if t, err := http.ParseTime(res.Header.Get("Date")); err == nil {
			s.offset.Store(time.Until(t).Milliseconds())
		}
	}
	msg, ok := tag(text, "Message")
	if !ok {
		msg = fmt.Sprintf("%s %s: %d %s", method, path, res.StatusCode, code)
	}
	return nil, &S3Error{res.StatusCode, code, msg}
}

func (s *S3) send(method, path string, q, headers map[string]string, body []byte) (*http.Response, error) {
	for attempt := 0; ; attempt++ {
		res, err := s.once(method, path, q, headers, body)
		if err == nil {
			return res, nil
		}
		var se *S3Error
		retry := false
		if errors.As(err, &se) {
			retry = retryStatus[se.Status] || retryCodes[se.Code]
		} else {
			var ne net.Error
			retry = errors.As(err, &ne) || errors.Is(err, io.ErrUnexpectedEOF) || strings.Contains(err.Error(), "connection")
		}
		if !retry || attempt+1 >= 3 {
			return nil, err
		}
		time.Sleep(time.Duration(rand.Float64() * float64(min(20000, 100<<attempt)) * float64(time.Millisecond)))
	}
}

func (s *S3) text(method, path string, q, headers map[string]string, body []byte) (string, error) {
	res, err := s.send(method, path, q, headers, body)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	b, err := io.ReadAll(res.Body)
	if err != nil {
		return "", err
	}
	// A 200 response can still carry an error for Copy and CompleteMultipartUpload.
	if t := string(b); strings.Contains(t, "<Error>") {
		if code, ok := tag(t, "Code"); ok {
			msg, _ := tag(t, "Message")
			return "", &S3Error{200, code, msg}
		}
	}
	return string(b), nil
}

func drain(res *http.Response, err error) error {
	if err == nil {
		io.Copy(io.Discard, res.Body)
		res.Body.Close()
	}
	return err
}

func ctype(t string) map[string]string {
	if t == "" {
		return nil
	}
	return map[string]string{"content-type": t}
}

func (s *S3) Put(bucket, key string, body []byte, contentType string) error {
	return drain(s.send("PUT", s.path(bucket, key), nil, ctype(contentType), body))
}

// Head: size of the object, -1 when it does not exist. A proxy may compress text and drop Content-Length from HEAD: ask for
// identity, and when the length is still missing read it from the Content-Range of a one-byte GET.
func (s *S3) Head(bucket, key string) (int64, error) {
	p := s.path(bucket, key)
	res, err := s.send("HEAD", p, nil, map[string]string{"accept-encoding": "identity"}, nil)
	var se *S3Error
	if errors.As(err, &se) && se.Status == 404 {
		return -1, nil
	}
	if errors.As(err, &se) && se.Status == 416 {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	drain(res, nil)
	if n, err := strconv.ParseInt(res.Header.Get("Content-Length"), 10, 64); err == nil {
		return n, nil
	}
	g, err := s.send("GET", p, nil, map[string]string{"accept-encoding": "identity", "range": "bytes=0-0"}, nil)
	if errors.As(err, &se) && se.Status == 416 {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	drain(g, nil)
	cr := g.Header.Get("Content-Range")
	if i := strings.LastIndex(cr, "/"); i >= 0 {
		return strconv.ParseInt(cr[i+1:], 10, 64)
	}
	return strconv.ParseInt(g.Header.Get("Content-Length"), 10, 64)
}

// Get streams the object; rng is an inclusive byte range [start, end] or nil.
func (s *S3) Get(bucket, key string, rng []int64) (io.ReadCloser, error) {
	h := map[string]string{"accept-encoding": "identity"}
	if rng != nil {
		h["range"] = fmt.Sprintf("bytes=%d-%d", rng[0], rng[1])
	}
	res, err := s.send("GET", s.path(bucket, key), nil, h, nil)
	if err != nil {
		return nil, err
	}
	return res.Body, nil
}

func (s *S3) Delete(bucket, key string) error {
	return drain(s.send("DELETE", s.path(bucket, key), nil, nil, nil))
}

func (s *S3) Copy(bucket, key, source string) error {
	_, err := s.text("PUT", s.path(bucket, key), nil, map[string]string{"x-amz-copy-source": s3enc(source, true)}, nil)
	return err
}

func (s *S3) CreateMultipartUpload(bucket, key, contentType string) (string, error) {
	t, err := s.text("POST", s.path(bucket, key), map[string]string{"uploads": ""}, ctype(contentType), nil)
	if err != nil {
		return "", err
	}
	id, ok := tag(t, "UploadId")
	if !ok || id == "" {
		return "", &S3Error{200, "InvalidResponse", "CreateMultipartUpload returned no UploadId"}
	}
	return id, nil
}

func (s *S3) UploadPartCopy(bucket, key, uploadID string, n int, source, rng string) (string, error) {
	t, err := s.text("PUT", s.path(bucket, key), map[string]string{"partNumber": strconv.Itoa(n), "uploadId": uploadID},
		map[string]string{"x-amz-copy-source": s3enc(source, true), "x-amz-copy-source-range": rng}, nil)
	if err != nil {
		return "", err
	}
	etag, ok := tag(t, "ETag")
	if !ok {
		return "", &S3Error{200, "InvalidResponse", "UploadPartCopy returned no ETag"}
	}
	return etag, nil
}

func (s *S3) CompleteMultipartUpload(bucket, key, uploadID string, parts []S3Part) error {
	var b strings.Builder
	b.WriteString("<CompleteMultipartUpload>")
	for _, p := range parts {
		fmt.Fprintf(&b, "<Part><PartNumber>%d</PartNumber><ETag>%s</ETag></Part>", p.PartNumber, html.EscapeString(p.ETag))
	}
	b.WriteString("</CompleteMultipartUpload>")
	_, err := s.text("POST", s.path(bucket, key), map[string]string{"uploadId": uploadID}, map[string]string{"content-type": "application/xml"}, []byte(b.String()))
	return err
}

func (s *S3) AbortMultipartUpload(bucket, key, uploadID string) error {
	return drain(s.send("DELETE", s.path(bucket, key), map[string]string{"uploadId": uploadID}, nil, nil))
}

// Presign: a query-signed URL (host only, UNSIGNED-PAYLOAD) for executors; Access headers are theirs to add.
func (s *S3) Presign(method, bucket, key string, expires int, q map[string]string) string {
	p := s.path(bucket, key)
	date := amzDate(s.now())
	all := map[string]string{"X-Amz-Algorithm": "AWS4-HMAC-SHA256", "X-Amz-Credential": s.keyID + "/" + date[:8] + "/" + s.region + "/s3/aws4_request",
		"X-Amz-Date": date, "X-Amz-Expires": strconv.Itoa(expires), "X-Amz-SignedHeaders": "host"}
	for k, v := range q {
		all[k] = v
	}
	sig, _, _ := sigv4(method, p, all, map[string]string{"host": s.host}, "UNSIGNED-PAYLOAD", s.keyID, s.secret, s.region, date)
	all["X-Amz-Signature"] = sig
	return s.base.Scheme + "://" + s.host + p + "?" + s3query(all)
}
