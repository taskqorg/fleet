// Package projectpublisher carries Fleet's repository-scoped GitHub App operations over a local Unix socket.
package projectpublisher

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"fleet/contract"
)

const (
	Repo       = "taskqorg/fleet"
	Host       = "github.com"
	RepoID     = 1450484391
	Mode       = "github_app"
	Slug       = "taskqorg-fleet-publisher"
	Actor      = Slug + "[bot]"
	Executor   = "pds18-native"
	Socket     = "/run/fleet-project-publisher/publisher.sock"
	Capability = "publishing-app:github:github.com:taskqorg/fleet"
)

func Required(ref contract.RepoRef) bool {
	return ref.Provider == "github" && ref.Host == Host && strings.EqualFold(ref.Slug, Repo)
}

type Readiness struct {
	Mode           string     `json:"mode"`
	Actor          string     `json:"actor"`
	Repo           string     `json:"repo"`
	RepositoryID   int64      `json:"repositoryId"`
	AppID          int64      `json:"appId,omitempty"`
	InstallationID int64      `json:"installationId,omitempty"`
	Ready          bool       `json:"ready"`
	Reason         string     `json:"reason,omitempty"`
	VerifiedAt     *time.Time `json:"verifiedAt,omitempty"`
	ExpiresAt      *time.Time `json:"expiresAt,omitempty"`
}

func Identity() Readiness {
	return Readiness{Mode: Mode, Actor: Actor, Repo: Repo, RepositoryID: RepoID}
}

type Operation struct {
	Action      string
	Branch      string
	Number      int64
	Base        string
	Head        string
	Title       string
	Body        string
	Lease       string
	MergeMethod string
	Description string
	TargetURL   string
}

type Reply struct {
	Error  string          `json:"error,omitempty"`
	Ticket string          `json:"ticket,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
}

var local = &http.Client{
	Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", Socket)
	}},
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

func Probe(ctx context.Context) Readiness {
	v := Identity()
	if err := localCall(ctx, http.MethodGet, "/readiness", "", nil, &v); err != nil {
		v.Reason = "publisher-service-unavailable"
	}
	return v
}

func Authorize(ctx context.Context, op Operation) (Reply, error) {
	b, err := json.Marshal(op)
	if err != nil {
		return Reply{}, err
	}
	return call(ctx, "/operation", "", bytes.NewReader(b))
}

func Push(ctx context.Context, ticket string, pack io.Reader) (Reply, error) {
	return call(ctx, "/push", ticket, pack)
}

func call(ctx context.Context, endpoint, ticket string, body io.Reader) (Reply, error) {
	var v Reply
	err := localCall(ctx, http.MethodPost, endpoint, ticket, body, &v)
	if err == nil && v.Error != "" {
		err = errors.New(v.Error)
	}
	return v, err
}

func localCall(ctx context.Context, method, endpoint, ticket string, body io.Reader, v any) error {
	req, err := http.NewRequestWithContext(ctx, method, "http://publisher"+endpoint, body)
	if err != nil {
		return err
	}
	if ticket != "" {
		req.Header.Set("X-Fleet-Publish-Ticket", ticket)
	}
	resp, err := local.Do(req)
	if err != nil {
		return errors.New("publisher-service-unavailable")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK || json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(v) != nil {
		return errors.New("publisher-service-reply-unavailable")
	}
	return nil
}
