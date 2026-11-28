package projectpublisher

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"

	"fleet/errtrack"
)

const apiOrigin = "https://api.github.com"

var permissions = map[string]string{
	"contents": "write", "pull_requests": "write", "statuses": "write", "checks": "read", "metadata": "read",
}

type installation struct {
	app, id int64
	key     *rsa.PrivateKey
	do      func(*http.Request) (*http.Response, error)
	mu      sync.Mutex
	cached  token
	retry   time.Time
	refresh chan struct{}
	reason  string
}

type token struct {
	value             string
	verified, expires time.Time
}

func githubClient() *http.Client {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.Proxy = nil
	return &http.Client{
		Transport:     t,
		Timeout:       10 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

func appKey(raw []byte) (*rsa.PrivateKey, error) {
	block, rest := pem.Decode(raw)
	if block != nil && len(bytes.TrimSpace(rest)) == 0 {
		if key, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
			return key, nil
		}
		if key, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
			if rsaKey, ok := key.(*rsa.PrivateKey); ok {
				return rsaKey, nil
			}
		}
	}
	return nil, errors.New("invalid GitHub App key")
}

func (i *installation) access(ctx context.Context) (token, error) {
	for {
		i.mu.Lock()
		now := time.Now()
		t := i.cached
		if now.Before(t.verified.Add(10*time.Minute)) && now.Add(2*time.Minute).Before(t.expires) {
			i.mu.Unlock()
			return t, nil
		}
		if now.Before(i.retry) {
			err := errors.New(i.reason)
			i.mu.Unlock()
			return token{}, err
		}
		wait := i.refresh
		if wait == nil {
			wait = make(chan struct{})
			i.refresh = wait
			errtrack.Go("publisher.refresh", func() { i.refreshToken(now, wait) })
		}
		i.mu.Unlock()
		select {
		case <-ctx.Done():
			return token{}, errors.New("publisher-installation-verification-pending")
		case <-wait:
		}
	}
}

func (i *installation) refreshToken(now time.Time, done chan struct{}) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	t, err := i.acquire(ctx, now)
	i.mu.Lock()
	defer i.mu.Unlock()
	i.cached = t
	i.retry = time.Time{}
	if err != nil {
		i.reason = err.Error()
		i.retry = time.Now().Add(30 * time.Second)
	}
	i.refresh = nil
	close(done)
}

func (i *installation) invalidate(value string) {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.cached.value == value {
		i.cached = token{}
	}
}

func (i *installation) jwt(now time.Time) (string, error) {
	encode := base64.RawURLEncoding.EncodeToString
	claims, err := json.Marshal(map[string]any{"iat": now.Add(-time.Minute).Unix(), "exp": now.Add(8 * time.Minute).Unix(), "iss": strconv.FormatInt(i.app, 10)})
	if err != nil {
		return "", err
	}
	payload := encode([]byte(`{"alg":"RS256","typ":"JWT"}`)) + "." + encode(claims)
	hash := sha256.Sum256([]byte(payload))
	signature, err := rsa.SignPKCS1v15(rand.Reader, i.key, crypto.SHA256, hash[:])
	return payload + "." + encode(signature), err
}

type repository struct {
	ID   int64  `json:"id"`
	Name string `json:"full_name"`
}

func (r repository) matches() bool { return r.ID == RepoID && r.Name == Repo }

func (i *installation) acquire(ctx context.Context, now time.Time) (token, error) {
	jwt, err := i.jwt(now)
	if err != nil {
		return token{}, errors.New("publisher-signature-unavailable")
	}
	var app struct {
		ID     int64  `json:"id"`
		Slug   string `json:"slug"`
		Public *bool  `json:"public"`
		Owner  struct {
			Login string `json:"login"`
		} `json:"owner"`
	}
	if i.api(ctx, jwt, http.MethodGet, "/app", nil, &app) != nil || app.ID != i.app || app.Slug != Slug ||
		app.Public == nil || *app.Public || app.Owner.Login != "taskqorg" {
		return token{}, errors.New("publisher-app-identity-unverified")
	}
	var binding struct {
		ID          int64             `json:"id"`
		App         int64             `json:"app_id"`
		Slug        string            `json:"app_slug"`
		Selection   string            `json:"repository_selection"`
		Suspended   *time.Time        `json:"suspended_at"`
		Permissions map[string]string `json:"permissions"`
	}
	if i.api(ctx, jwt, http.MethodGet, "/repos/"+Repo+"/installation", nil, &binding) != nil ||
		binding.ID != i.id || binding.App != i.app || binding.Slug != Slug ||
		binding.Selection != "selected" || binding.Suspended != nil || !maps.Equal(binding.Permissions, permissions) {
		return token{}, errors.New("publisher-installation-binding-unverified")
	}
	// Inventory the installation's full accessible set, then mint the operational token with an explicit repo ID.
	// Both tokens are held only in this process.
	var issued struct {
		Value       string            `json:"token"`
		Expires     time.Time         `json:"expires_at"`
		Permissions map[string]string `json:"permissions"`
	}
	path := fmt.Sprintf("/app/installations/%d/access_tokens", i.id)
	mint := func(repositoryIDs []int64) bool {
		body := map[string]any{"permissions": permissions}
		if len(repositoryIDs) != 0 {
			body["repository_ids"] = repositoryIDs
		}
		issued.Value, issued.Expires, issued.Permissions = "", time.Time{}, nil
		return i.api(ctx, jwt, http.MethodPost, path, body, &issued) == nil &&
			issued.Value != "" && issued.Expires.After(now.Add(2*time.Minute)) &&
			!issued.Expires.After(now.Add(time.Hour+time.Minute)) && maps.Equal(issued.Permissions, permissions)
	}
	if !mint(nil) {
		return token{}, errors.New("publisher-installation-token-unverified")
	}
	inventory := issued.Value
	// GitHub expires installation tokens automatically. Revoke the inventory token once the scope is read.
	defer func() { _ = i.api(ctx, inventory, http.MethodDelete, "/installation/token", nil, nil) }()
	var scope struct {
		Count        int64        `json:"total_count"`
		Repositories []repository `json:"repositories"`
	}
	if i.api(ctx, issued.Value, http.MethodGet, "/installation/repositories?per_page=2", nil, &scope) != nil ||
		scope.Count != 1 || len(scope.Repositories) != 1 || !scope.Repositories[0].matches() {
		return token{}, errors.New("publisher-repository-scope-unverified")
	}
	if !mint([]int64{RepoID}) {
		return token{}, errors.New("publisher-repository-token-unverified")
	}
	return token{value: issued.Value, verified: now.UTC(), expires: issued.Expires}, nil
}

func (i *installation) api(ctx context.Context, bearer, method, endpoint string, payload, out any) error {
	u, err := url.Parse(apiOrigin + endpoint)
	if err != nil || u.Scheme != "https" || u.Host != "api.github.com" || u.User != nil || u.Fragment != "" {
		return errors.New("publisher-provider-endpoint-refused")
	}
	var body io.Reader
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			return errors.New("publisher-provider-input-invalid")
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), body)
	if err != nil {
		return errors.New("publisher-provider-input-invalid")
	}
	req.Header.Set("Authorization", "Bearer "+bearer)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := i.do(req)
	if err != nil {
		return errors.New("publisher-provider-unavailable")
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("publisher-provider-http-%d", resp.StatusCode)
	}
	if out == nil {
		return nil
	}
	if json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(out) != nil {
		return errors.New("publisher-provider-reply-invalid")
	}
	return nil
}
