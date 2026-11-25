package main

// Compressed replies (docs/designs/fleet-data-layer.md, "Bytes on the wire"): a JSON reply larger than one packet goes
// gzip-compressed when the client accepts it. Replies travel from the coordinator to Cloudflare's edge through the
// tunnel uncompressed otherwise (the edge compresses only for the browser), and on prod a reply of 14 kB gzip / 100 kB
// raw stalled after its first byte by 0.3-1.1 s now and then, while 8 kB replies never did (2026-11-26).

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
)

// gzipMin: replies below it go as they are (one packet; compressing them buys nothing).
const gzipMin = 1400

var gzipWriters = sync.Pool{New: func() any { w, _ := gzip.NewWriterLevel(nil, gzip.BestSpeed); return w }}

// writeJSONReply: out as JSON, gzip-compressed when it is larger than gzipMin and the request accepts gzip.
func writeJSONReply(w http.ResponseWriter, r *http.Request, out any) error {
	var body bytes.Buffer
	if err := json.NewEncoder(&body).Encode(out); err != nil {
		return err
	}
	h := w.Header()
	h.Set("Content-Type", "application/json; charset=utf-8")
	h.Add("Vary", "Accept-Encoding")
	if body.Len() < gzipMin || !acceptsGzip(r) {
		_, err := w.Write(body.Bytes())
		return err
	}
	h.Set("Content-Encoding", "gzip")
	h.Del("Content-Length")
	zw := gzipWriters.Get().(*gzip.Writer)
	defer gzipWriters.Put(zw)
	zw.Reset(w)
	if _, err := zw.Write(body.Bytes()); err != nil {
		return err
	}
	return zw.Close()
}

// acceptsGzip: the request's Accept-Encoding names gzip without q=0.
func acceptsGzip(r *http.Request) bool {
	for _, part := range strings.Split(r.Header.Get("Accept-Encoding"), ",") {
		name, params, _ := strings.Cut(strings.TrimSpace(part), ";")
		if strings.EqualFold(strings.TrimSpace(name), "gzip") {
			q := strings.ReplaceAll(strings.TrimSpace(params), " ", "")
			return q != "q=0" && q != "q=0.0" && q != "q=0.00" && q != "q=0.000"
		}
	}
	return false
}
