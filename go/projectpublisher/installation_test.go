package projectpublisher

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"fmt"
	"io"
	"net/http"
	"os"

	"golang.org/x/net/html"
	"strings"
	"testing"
	"time"
)

const privateFixture = `<html><body><main><style>.footer { display: none; }</style><div class="container-sm"><div class="Box"><div></div><h3 class="f2 text-normal lh-condensed mb-2">` + Slug + `</h3><p class="tmp-mb-3 lh-condensed"><svg class="octicon-lock" aria-hidden="true"></svg>` + Slug + ` is a private GitHub App.</p><div><div><a href="https://docs.github.com/apps/building-integrations/setting-up-and-registering-github-apps/">Learn more about GitHub Apps</a></div></div></div></div></main></body></html>`

func TestPrivateVisibilityBeforeToken(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	validApp := `{"id":123,"slug":"` + Slug + `","owner":{"login":"taskqorg"}}`
	tests := []struct {
		name, app, landing string
		status             int
		allow              bool
	}{
		{"documented private banner", validApp, privateFixture, 200, true},
		{"public app", validApp, strings.Replace(privateFixture, "private GitHub App", "public GitHub App", 1), 200, false},
		{"internal app", validApp, strings.Replace(privateFixture, "private GitHub App", "internal GitHub App", 1), 200, false},
		{"description spoof", validApp, strings.Replace(privateFixture, "private GitHub App", "public GitHub App", 1) + `<div class="markdown-body">` + privateFixture + `</div>`, 200, false},
		{"hidden spoof", validApp, strings.Replace(privateFixture, `<p class="tmp-mb-3 lh-condensed">`, `<p hidden class="tmp-mb-3 lh-condensed">`, 1), 200, false},
		{"extra author description", validApp, strings.Replace(privateFixture, `<div></div><h3 class="f2 text-normal lh-condensed mb-2">`, `<div><div class="markdown-body">description</div></div><h3 class="f2 text-normal lh-condensed mb-2">`, 1), 200, false},
		{"nested style", validApp, strings.Replace(privateFixture, `<div></div><h3 class="f2 text-normal lh-condensed mb-2">`, `<div><style>p { display:none }</style></div><h3 class="f2 text-normal lh-condensed mb-2">`, 1), 200, false},
		{"hidden by class", validApp, strings.Replace(privateFixture, `<div></div><h3 class="f2 text-normal lh-condensed mb-2">`, `<div class="d-none"></div><h3 class="f2 text-normal lh-condensed mb-2">`, 1), 200, false},
		{"installation action", validApp, strings.Replace(privateFixture, `<div></div><h3 class="f2 text-normal lh-condensed mb-2">`, `<form></form><div></div><h3 class="f2 text-normal lh-condensed mb-2">`, 1), 200, false},
		{"ambiguous response", validApp, privateFixture, 404, false},
		{"redirect", validApp, privateFixture, 302, false},
		{"wrong owner", `{"id":123,"slug":"` + Slug + `","owner":{"login":"other"}}`, privateFixture, 200, false},
		{"explicit public", `{"id":123,"slug":"` + Slug + `","public":true,"owner":{"login":"taskqorg"}}`, privateFixture, 200, false},
		{"identity changed", validApp, privateFixture, 200, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mints, identityReads := 0, 0
			reply := func(status int, body string) *http.Response {
				return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": []string{"text/html; charset=utf-8"}}, Body: io.NopCloser(strings.NewReader(body))}
			}
			i := &installation{app: 123, id: 456, key: key, do: func(r *http.Request) (*http.Response, error) {
				if r.URL.Scheme != "https" {
					t.Fatal("scheme changed")
				}
				switch r.URL.Host + " " + r.Method + " " + r.URL.Path {
				case "api.github.com GET /app":
					identityReads++
					if tc.name == "identity changed" && identityReads > 1 {
						return reply(200, `{"id":124}`), nil
					}
					return reply(200, tc.app), nil
				case "github.com GET /apps/" + Slug:
					if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
						t.Fatal("sensitive header leaked")
					}
					return reply(tc.status, tc.landing), nil
				case "api.github.com GET " + repoPath + "/installation":
					return reply(200, `{"id":456,"app_id":123,"app_slug":"`+Slug+`","repository_selection":"selected","permissions":{"contents":"write","pull_requests":"write","statuses":"write","checks":"read","metadata":"read"}}`), nil
				case "api.github.com POST /app/installations/456/access_tokens":
					mints++
					return reply(200, fmt.Sprintf(`{"token":"t","expires_at":%q,"permissions":{"contents":"write","pull_requests":"write","statuses":"write","checks":"read","metadata":"read"}}`, time.Now().UTC().Add(45*time.Minute).Format(time.RFC3339))), nil
				case "api.github.com GET /installation/repositories":
					return reply(200, fmt.Sprintf(`{"total_count":1,"repositories":[{"id":%d,"full_name":%q}]}`, RepoID, Repo)), nil
				case "api.github.com DELETE /installation/token":
					return reply(204, ``), nil
				default:
					t.Fatalf("unexpected %s %s %s", r.URL.Host, r.Method, r.URL.Path)
					return nil, nil
				}
			}}
			_, err := i.acquire(context.Background(), time.Now())
			if (err == nil) != tc.allow {
				t.Fatalf("allow=%v, err=%v", tc.allow, err)
			}
			if !tc.allow && mints != 0 {
				t.Fatal("minted before verification")
			}
		})
	}
}

func TestBindingAndScopeStayFailClosed(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	for _, scenario := range []string{"wrong app", "wrong slug", "wrong installation", "all repositories", "wrong permissions", "extra repository", "wrong repo id"} {
		t.Run(scenario, func(t *testing.T) {
			mintCount := 0
			reply := func(status int, body string) *http.Response {
				return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body))}
			}
			i := &installation{app: 123, id: 456, key: key, do: func(r *http.Request) (*http.Response, error) {
				switch r.Method + " " + r.URL.Path {
				case "GET /app":
					id := 123
					slug := Slug
					if scenario == "wrong app" {
						id = 124
					}
					if scenario == "wrong slug" {
						slug = "other"
					}
					return reply(200, fmt.Sprintf(`{"id":%d,"slug":%q,"owner":{"login":"taskqorg"}}`, id, slug)), nil
				case "GET /apps/" + Slug:
					v := reply(200, privateFixture)
					v.Header = http.Header{"Content-Type": []string{"text/html"}}
					return v, nil
				case "GET " + repoPath + "/installation":
					id := 456
					selection := "selected"
					perms := `{"contents":"write","pull_requests":"write","statuses":"write","checks":"read","metadata":"read"}`
					if scenario == "wrong installation" {
						id = 457
					}
					if scenario == "all repositories" {
						selection = "all"
					}
					if scenario == "wrong permissions" {
						perms = `{"contents":"write"}`
					}
					return reply(200, fmt.Sprintf(`{"id":%d,"app_id":123,"app_slug":%q,"repository_selection":%q,"permissions":%s}`, id, Slug, selection, perms)), nil
				case "POST /app/installations/456/access_tokens":
					mintCount++
					return reply(200, fmt.Sprintf(`{"token":"t","expires_at":%q,"permissions":{"contents":"write","pull_requests":"write","statuses":"write","checks":"read","metadata":"read"}}`, time.Now().UTC().Add(45*time.Minute).Format(time.RFC3339))), nil
				case "GET /installation/repositories":
					count, id := 1, RepoID
					if scenario == "extra repository" {
						count = 2
					}
					if scenario == "wrong repo id" {
						id++
					}
					return reply(200, fmt.Sprintf(`{"total_count":%d,"repositories":[{"id":%d,"full_name":%q}]}`, count, id, Repo)), nil
				case "DELETE /installation/token":
					return reply(204, ``), nil
				default:
					t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
					return nil, nil
				}
			}}
			if _, err := i.acquire(context.Background(), time.Now()); err == nil {
				t.Fatal("ambiguous or overbroad identity was accepted")
			}
			if mintCount > 1 {
				t.Fatal("operational token minted despite invalid binding/scope")
			}
		})
	}
}

// The sanitized first-party landing shape includes the real badge and ancestor styling.
func TestPrivateLandingLiveShape(t *testing.T) {
	f, err := os.Open("testdata/github-private-landing.html")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	doc, err := html.Parse(f)
	if err != nil || !privateLanding(doc) {
		t.Fatalf("private notice not recognized: %v", err)
	}
}
