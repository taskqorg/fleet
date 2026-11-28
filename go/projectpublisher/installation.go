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
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"fleet/errtrack"

	"golang.org/x/net/html"
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
	identity := func() bool {
		app.ID, app.Slug, app.Public, app.Owner.Login = 0, "", nil, ""
		return i.api(ctx, jwt, http.MethodGet, "/app", nil, &app) == nil && app.ID == i.app &&
			app.Slug == Slug && (app.Public == nil || !*app.Public) && app.Owner.Login == "taskqorg"
	}
	// Bracket the public probe with fresh authenticated identity checks so a
	// moved or renamed slug cannot establish privacy for the wrong App.
	if !identity() {
		return token{}, errors.New("publisher-app-identity-unverified")
	}
	if !i.privateApp(ctx) {
		return token{}, errors.New("publisher-app-visibility-unverified")
	}
	if !identity() {
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

// privateApp requires the first-party GitHub landing page's exact private-app
// banner, structurally separate from the App owner's description. A changed,
// hidden, duplicate or otherwise ambiguous page fails closed.
func (i *installation) privateApp(ctx context.Context) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://github.com/apps/"+Slug, nil)
	if err != nil {
		return false
	}
	req.Header.Set("Accept", "text/html")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	req.Header.Set("Cache-Control", "no-cache")
	resp, err := i.do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(strings.ToLower(resp.Header.Get("Content-Type")), "text/html") {
		return false
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20+1))
	if err != nil || len(body) > 2<<20 {
		return false
	}
	doc, err := html.Parse(bytes.NewReader(body))
	if err != nil {
		return false
	}
	return privateLanding(doc)
}

func privateLanding(doc *html.Node) bool {
	attr := func(n *html.Node, key string) string {
		for _, a := range n.Attr {
			if a.Key == key {
				return a.Val
			}
		}
		return ""
	}
	hasClass := func(n *html.Node, name string) bool { return slices.Contains(strings.Fields(attr(n, "class")), name) }
	visible := func(n *html.Node) bool {
		for p := n; p != nil; p = p.Parent {
			for _, a := range p.Attr {
				if a.Key == "hidden" || (a.Key == "style" && strings.TrimSpace(a.Val) != "word-wrap: break-word;") || (a.Key == "aria-hidden" && a.Val == "true") {
					return false
				}
			}
		}
		return true
	}
	children := func(n *html.Node, tag string) []*html.Node {
		var result []*html.Node
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			if c.Type == html.ElementNode && c.Data == tag {
				result = append(result, c)
			}
		}
		return result
	}
	directText := func(n *html.Node) string {
		var b strings.Builder
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			if c.Type == html.TextNode {
				b.WriteString(c.Data)
				b.WriteByte(' ')
			}
		}
		return strings.Join(strings.Fields(b.String()), " ")
	}
	var mains []*html.Node
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "main" {
			mains = append(mains, n)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	if len(mains) != 1 || !visible(mains[0]) {
		return false
	}
	mainElements := children(mains[0], "div")
	styles := children(mains[0], "style")
	if len(mainElements) != 1 || len(styles) != 1 || strings.TrimSpace(directText(styles[0])) != ".footer { display: none; }" {
		return false
	}
	for c := mains[0].FirstChild; c != nil; c = c.NextSibling {
		if c.Type == html.ElementNode && c != mainElements[0] && c != styles[0] {
			return false
		}
	}
	var linkCount int
	var bad bool
	var scan func(*html.Node)
	scan = func(n *html.Node) {
		if n.Type == html.ElementNode {
			switch n.Data {
			case "main", "div", "h3", "p", "svg", "path", "img":
			case "style":
				if n.Parent != mains[0] {
					bad = true
				}
			case "a":
				linkCount++
			default:
				bad = true
			}
			if hasClass(n, "markdown-body") {
				bad = true
			}
			for _, a := range n.Attr {
				if a.Key == "hidden" || (a.Key == "aria-hidden" && a.Val == "true" && n.Data != "svg") {
					bad = true
				}
				if a.Key == "style" && !(n.Data == "div" && hasClass(n, "CircleBadge") && a.Val == "background-color: #ffebda") {
					bad = true
				}
				if a.Key == "class" && (slices.Contains(strings.Fields(a.Val), "d-none") || slices.Contains(strings.Fields(a.Val), "visually-hidden") || slices.Contains(strings.Fields(a.Val), "sr-only")) {
					bad = true
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			scan(c)
		}
	}
	scan(mains[0])
	if bad || linkCount != 1 {
		return false
	}
	var matches int
	for _, container := range mainElements {
		if !hasClass(container, "container-sm") || !visible(container) {
			continue
		}
		boxes := children(container, "div")
		if len(boxes) != 1 {
			return false
		}
		for c := container.FirstChild; c != nil; c = c.NextSibling {
			if c.Type == html.ElementNode && c != boxes[0] {
				return false
			}
		}
		for _, box := range boxes {
			if !hasClass(box, "Box") || !visible(box) {
				continue
			}
			headings, paragraphs := children(box, "h3"), children(box, "p")
			if len(headings) != 1 || len(paragraphs) != 1 || attr(headings[0], "class") != "f2 text-normal lh-condensed mb-2" || attr(paragraphs[0], "class") != "tmp-mb-3 lh-condensed" || !visible(headings[0]) || !visible(paragraphs[0]) || directText(headings[0]) != Slug || directText(paragraphs[0]) != Slug+" is a private GitHub App." {
				continue
			}
			var elements []*html.Node
			for c := box.FirstChild; c != nil; c = c.NextSibling {
				if c.Type == html.ElementNode {
					elements = append(elements, c)
				}
			}
			if len(elements) != 4 || elements[0].Data != "div" || elements[1] != headings[0] || elements[2] != paragraphs[0] || elements[3].Data != "div" {
				return false
			}
			links := children(elements[3], "div")
			if len(links) != 1 {
				return false
			}
			anchors := children(links[0], "a")
			if len(anchors) != 1 || attr(anchors[0], "href") != "https://docs.github.com/apps/building-integrations/setting-up-and-registering-github-apps/" || directText(anchors[0]) != "Learn more about GitHub Apps" {
				return false
			}
			if len(children(links[0], "form")) != 0 || !visible(anchors[0]) {
				return false
			}
			for c := headings[0].FirstChild; c != nil; c = c.NextSibling {
				if c.Type == html.ElementNode {
					return false
				}
			}
			for c := anchors[0].FirstChild; c != nil; c = c.NextSibling {
				if c.Type == html.ElementNode {
					return false
				}
			}
			icons := children(paragraphs[0], "svg")
			if len(icons) != 1 || !hasClass(icons[0], "octicon-lock") || attr(icons[0], "aria-hidden") != "true" {
				continue
			}
			// No author-provided inline element may contribute or hide marker text.
			other := false
			for c := paragraphs[0].FirstChild; c != nil; c = c.NextSibling {
				if c.Type == html.ElementNode && c != icons[0] {
					other = true
				}
			}
			if !other {
				matches++
			}
		}
	}
	return matches == 1
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
