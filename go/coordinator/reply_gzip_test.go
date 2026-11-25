package main

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Requirement: a JSON reply larger than one packet goes gzip-compressed when the client accepts gzip, so the tunnel to
// the edge carries a seventh of the bytes; a small reply or a client without gzip gets plain JSON. Risk: a reply is
// compressed for a client that cannot read it, or a large one travels raw and stalls in the tunnel.
func TestWriteJSONReply(t *testing.T) {
	big := map[string]string{"x": strings.Repeat("fleet ", 2000)}
	for _, c := range []struct {
		accept string
		out    any
		gz     bool
	}{
		{"gzip, deflate, br", big, true},
		{"br;q=1, gzip;q=0.5", big, true},
		{"gzip;q=0", big, false},
		{"", big, false},
		{"gzip", map[string]int{"n": 1}, false},
	} {
		r := httptest.NewRequest("POST", "/api/ops/x", nil)
		if c.accept != "" {
			r.Header.Set("Accept-Encoding", c.accept)
		}
		w := httptest.NewRecorder()
		if err := writeJSONReply(w, r, c.out); err != nil {
			t.Fatal(err)
		}
		body := w.Body.Bytes()
		if got := w.Header().Get("Content-Encoding") == "gzip"; got != c.gz {
			t.Fatalf("%q: gzip %v, want %v", c.accept, got, c.gz)
		}
		if c.gz {
			zr, err := gzip.NewReader(bytes.NewReader(body))
			if err != nil {
				t.Fatal(err)
			}
			if body, err = io.ReadAll(zr); err != nil {
				t.Fatal(err)
			}
			if w.Body.Len()*5 > len(body) {
				t.Fatalf("%q: %d compressed bytes for %d", c.accept, w.Body.Len(), len(body))
			}
		}
		want, _ := json.Marshal(c.out)
		if string(bytes.TrimSpace(body)) != string(want) || w.Header().Get("Content-Type") != "application/json; charset=utf-8" || w.Code != http.StatusOK {
			t.Fatalf("%q: body or headers differ", c.accept)
		}
	}
}
