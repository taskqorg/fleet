// The Access origin guard with disposable signing keys.
// Requirement: only a verified Access identity gets the browser credential (fleet-people-access). Risk: a forged,
// expired, foreign or unsigned token, or a client-supplied X-Fleet-Person, reaches the coordinator as a person.
package main

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func sign(t *testing.T, key *rsa.PrivateKey, alg string, claims map[string]any) string {
	enc := func(v any) string { data, _ := json.Marshal(v); return base64.RawURLEncoding.EncodeToString(data) }
	head := enc(map[string]string{"alg": alg, "kid": "k1", "typ": "JWT"}) + "." + enc(claims)
	digest := sha256.Sum256([]byte(head))
	signature, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	return head + "." + base64.RawURLEncoding.EncodeToString(signature)
}

func TestGuardAdmitsOnlyVerifiedAccessIdentity(t *testing.T) {
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	forged, _ := rsa.GenerateKey(rand.Reader, 2048)
	up := true
	certs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !up {
			w.WriteHeader(500)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{"kid": "k1", "kty": "RSA",
			"n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()), "e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes())}}})
	}))
	defer certs.Close()
	g := newGuard("invented-browser-secret", certs.URL)
	now := time.Now().Unix()
	claims := func(change map[string]any, drop string) map[string]any {
		c := map[string]any{"iss": accessIssuer, "aud": accessAudience, "email": "Owner@Example.invalid", "sub": "fixture", "iat": now, "exp": now + 60}
		for k, v := range change {
			c[k] = v
		}
		delete(c, drop)
		return c
	}
	check := func(headers map[string][]string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "/verify", nil)
		for k, vs := range headers {
			for _, v := range vs {
				r.Header.Add(k, v)
			}
		}
		w := httptest.NewRecorder()
		g.ServeHTTP(w, r)
		if w.Code != 204 && (w.Header().Get("Authorization") != "" || w.Header().Get("X-Fleet-Person") != "") {
			t.Fatal("credential on a refusal")
		}
		return w
	}
	status := func(token string) int { return check(map[string][]string{"Cf-Access-Jwt-Assertion": {token}}).Code }

	good := sign(t, key, "RS256", claims(nil, ""))
	w := check(map[string][]string{"Cf-Access-Jwt-Assertion": {good}, "X-Fleet-Person": {"forged@example.invalid"}})
	expect(t, []string{w.Header().Get("Authorization"), w.Header().Get("X-Fleet-Person")},
		[]string{"Bearer invented-browser-secret", "owner@example.invalid"}, "admitted")
	// Any verified e-mail passes the guard; the coordinator maps it to a person or answers 403.
	expect(t, status(sign(t, key, "RS256", claims(map[string]any{"email": "alina@example.invalid"}, ""))), 204, "other e-mail")
	for _, change := range []map[string]any{{"iss": "wrong"}, {"aud": "wrong"}, {"email": ""}, {"exp": 1}, {"iat": now + 3600}, {"email": 7}} {
		expect(t, status(sign(t, key, "RS256", claims(change, ""))), 403, "changed claim")
	}
	for _, drop := range []string{"exp", "iat", "sub", "email"} {
		expect(t, status(sign(t, key, "RS256", claims(nil, drop))), 403, "missing "+drop)
	}
	hs := strings.Split(sign(t, key, "HS256", claims(nil, "")), ".")
	for _, token := range []string{sign(t, forged, "RS256", claims(nil, "")), hs[0] + "." + hs[1] + ".x", "malformed", strings.Repeat("x", 16385)} {
		expect(t, status(token), 403, "bad token")
	}
	for _, headers := range []map[string][]string{{}, {"X-Dashq-Subject": {"forged"}}, {"Cf-Access-Jwt-Assertion": {good, good}}} {
		expect(t, check(headers).Code, 403, "bad header")
	}
	up = false
	g.keys = nil
	expect(t, status(good), 503, "key outage")
}

// Requirement: an unknown key id is refused from a key set younger than 30 s and refetches an older one
// (fleet-people-access). Risk: arbitrary kid values force one fetch each behind the guard's lock, or a rotated
// Access key locks the owner out until the whole set ages out.
func TestUnknownKeyIdRefetchesAtMostEvery30s(t *testing.T) {
	var hits atomic.Int32
	certs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		io.WriteString(w, `{"keys":[]}`)
	}))
	defer certs.Close()
	g := newGuard("invented-browser-secret", certs.URL)
	head := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","kid":"invented-unknown"}`))
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := g.verify(head + ".e30.AA"); err == nil {
				t.Error("unknown key admitted")
			}
		}()
	}
	wg.Wait()
	expect(t, hits.Load(), int32(1), "key-set fetches")
	g.fetched = time.Now().Add(-unknownRefetch - time.Second)
	g.verify(head + ".e30.AA")
	g.verify(head + ".e30.AA")
	expect(t, hits.Load(), int32(2), "fetches after 30 s")
}
