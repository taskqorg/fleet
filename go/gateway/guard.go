package main

import (
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Cloudflare Access application of fleet.ufobe.com.
const (
	accessIssuer   = "https://graduz.cloudflareaccess.com"
	accessAudience = "352f4c75fdee7abac35e5ee8739ab622de4a368725eedac025c33b4e32df1ab0"
	keysLifetime   = 5 * time.Minute
	// An unknown kid refetches a set older than this (a rotation); younger sets refuse it from the cache.
	unknownRefetch = 30 * time.Second
)

// guard answers Caddy's forward_auth: a verified Access JWT gets 204 with the browser credential and the person's e-mail;
// anything else gets 403, and 503 when the signing keys cannot be fetched.
type guard struct {
	secret   string
	certsURL string
	client   *http.Client
	lock     sync.Mutex
	// The last fetched key set; an id it lacks stays unknown until the set is unknownRefetch old.
	keys    map[string]*rsa.PublicKey
	fetched time.Time
	// Closed when the running fetch ends; nil when none runs. The fetch itself runs outside lock.
	inflight chan struct{}
}

var (
	errKeysUnavailable = errors.New("signing keys unavailable")
	errUnknownKey      = errors.New("unknown signing key")
)

func newGuard(secret, certsURL string) *guard {
	return &guard{secret: secret, certsURL: certsURL, client: &http.Client{Timeout: 5 * time.Second}}
}

// key returns the signing key by id from a fresh key set, fetching the set when it is old or absent.
// One fetch runs at a time; other callers wait for its result instead of starting their own.
func (g *guard) key(id string) (*rsa.PublicKey, error) {
	for waited := false; ; waited = true {
		g.lock.Lock()
		if age := time.Since(g.fetched); g.keys != nil && age < keysLifetime {
			if k := g.keys[id]; k != nil || waited || age < unknownRefetch {
				g.lock.Unlock()
				if k == nil {
					return nil, errUnknownKey
				}
				return k, nil
			}
		}
		if waited {
			g.lock.Unlock()
			return nil, errKeysUnavailable
		}
		done := g.inflight
		if done == nil {
			done = make(chan struct{})
			g.inflight = done
			g.lock.Unlock()
			keys := g.fetch()
			g.lock.Lock()
			if keys != nil {
				g.keys, g.fetched = keys, time.Now()
			}
			g.inflight = nil
			close(done)
		}
		g.lock.Unlock()
		<-done
	}
}

// fetch reads the key set; nil when it cannot be fetched or read.
func (g *guard) fetch() map[string]*rsa.PublicKey {
	response, err := g.client.Get(g.certsURL)
	if err != nil {
		return nil
	}
	defer response.Body.Close()
	var set struct {
		Keys []struct{ Kid, Kty, N, E string } `json:"keys"`
	}
	if response.StatusCode != 200 || json.NewDecoder(response.Body).Decode(&set) != nil {
		return nil
	}
	keys := map[string]*rsa.PublicKey{}
	for _, k := range set.Keys {
		n, errN := base64.RawURLEncoding.DecodeString(k.N)
		e, errE := base64.RawURLEncoding.DecodeString(k.E)
		if k.Kty == "RSA" && errN == nil && errE == nil && len(e) <= 4 {
			keys[k.Kid] = &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: int(new(big.Int).SetBytes(e).Int64())}
		}
	}
	return keys
}

// verify checks an RS256 JWT for the Access issuer and audience and returns its identity: the e-mail claim of a person,
// or the common_name of a service token (no e-mail; people.json maps it to a person through accessCommonName).
func (g *guard) verify(jwt string) (string, error) {
	parts := strings.Split(jwt, ".")
	if len(parts) != 3 {
		return "", errors.New("malformed")
	}
	var header struct{ Alg, Kid string }
	if decode(parts[0], &header) != nil || header.Alg != "RS256" {
		return "", errors.New("algorithm refused")
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return "", err
	}
	key, err := g.key(header.Kid)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(key, crypto.SHA256, digest[:], signature); err != nil {
		return "", err
	}
	var claims struct {
		Iss, Sub, Email *string
		CommonName      *string `json:"common_name"`
		Exp, Iat, Nbf   *float64
		Aud             json.RawMessage
	}
	if decode(parts[1], &claims) != nil || claims.Exp == nil || claims.Iat == nil || claims.Sub == nil {
		return "", errors.New("required claim missing")
	}
	if claims.Email == nil {
		if claims.CommonName == nil || *claims.CommonName == "" || strings.Contains(*claims.CommonName, "@") {
			return "", errors.New("required claim missing")
		}
		claims.Email = claims.CommonName
	}
	now := float64(time.Now().Unix())
	var audiences []string
	if json.Unmarshal(claims.Aud, &audiences) != nil {
		var one string
		json.Unmarshal(claims.Aud, &one)
		audiences = []string{one}
	}
	switch {
	case claims.Iss == nil || *claims.Iss != accessIssuer:
		return "", errors.New("issuer")
	case !contains(audiences, accessAudience):
		return "", errors.New("audience")
	case *claims.Exp <= now, *claims.Iat > now, claims.Nbf != nil && *claims.Nbf > now:
		return "", errors.New("time")
	case *claims.Email == "" || len(*claims.Email) > 320:
		return "", errors.New("identity denied")
	}
	return *claims.Email, nil
}

func decode(part string, into any) error {
	data, err := base64.RawURLEncoding.DecodeString(part)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, into)
}

func (g *guard) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/verify" || r.Method != "GET" {
		w.WriteHeader(404)
		return
	}
	tokens := r.Header.Values("Cf-Access-Jwt-Assertion")
	if len(tokens) != 1 || tokens[0] == "" || len(tokens[0]) > 16384 {
		w.WriteHeader(403)
		return
	}
	email, err := g.verify(tokens[0])
	switch {
	case errors.Is(err, errKeysUnavailable):
		w.WriteHeader(503)
	case err != nil:
		w.WriteHeader(403)
	default:
		// Who the person is, for the coordinator (people.json decides what they may do; an unknown e-mail gets 403 there).
		w.Header().Set("Authorization", "Bearer "+g.secret)
		w.Header().Set("X-Fleet-Person", strings.ToLower(email))
		w.WriteHeader(204)
	}
}
