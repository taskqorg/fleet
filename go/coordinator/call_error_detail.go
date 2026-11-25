// The provider's reply to a failed connection call, kept with the failure (pipeline step errors).
package main

import (
	"bytes"
	"encoding/json"
	"regexp"
	"unicode/utf8"
)

// callErrorDetailMax bounds the kept reply: enough for a provider's error object with its quota and retry details.
const callErrorDetailMax = 2048

// secretShapes: credential-like values a reply might echo (Google API keys, bearer and OpenAI-style tokens, key= and
// token= query values); each is replaced before the reply is kept. The gateway never sends the connection key itself.
var secretShapes = regexp.MustCompile(`AIza[0-9A-Za-z_\-]{20,}|(?i:bearer\s+)[0-9A-Za-z._\-]{8,}|\bsk-[0-9A-Za-z_\-]{8,}|(?i:\b(?:key|token|api_key|access_token)=)[^&"'\s]+`)

// callErrorDetail: a failed call's reply as compact text, credential-like values redacted, cut at callErrorDetailMax
// bytes on a character boundary; empty when there is no reply.
func callErrorDetail(output json.RawMessage) string {
	if len(output) == 0 {
		return ""
	}
	text := output
	var compact bytes.Buffer
	if json.Compact(&compact, output) == nil {
		text = compact.Bytes()
		var s string // a reply kept as text arrives as one JSON string
		if json.Unmarshal(text, &s) == nil {
			text = []byte(s)
		}
	}
	out := secretShapes.ReplaceAllString(string(text), "[redacted]")
	if len(out) > callErrorDetailMax {
		cut := callErrorDetailMax
		for cut > 0 && !utf8.RuneStart(out[cut]) {
			cut--
		}
		out = out[:cut] + "…"
	}
	return out
}
