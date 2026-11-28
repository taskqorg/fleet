package contract

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"regexp"
)

// DesignCaptureScript selects only the executor's fixed renderer transport.
// Coordinator-created script jobs carry an additional DesignCaptureCommand.
const DesignCaptureScript = "@fleet-design-capture/v1"
const DesignCaptureHost = "pds18-native"
const DesignCaptureCapability = "design-capture-broker-v2"
const DesignCaptureStageCapability = "design-capture-stage-linux-v1"
const DesignCaptureTimeoutMS = 900_000

type DesignCaptureScope struct {
	Project   string `json:"project"`
	Task      string `json:"task"`
	Session   string `json:"session"`
	Execution string `json:"execution"`
}

type DesignCaptureJobRequest struct {
	DesignCaptureScope
	Version       int    `json:"version"`
	Job           string `json:"job"`
	Nonce         string `json:"nonce"`
	Base          string `json:"base"`
	BaseSHA256    string `json:"baseSha256"`
	OverlaySHA256 string `json:"overlaySha256"`
	FixtureSHA256 string `json:"fixtureSha256"`
}

type DesignCaptureProducer struct {
	Commit  string `json:"commit"`
	Version string `json:"version"`
	Target  string `json:"target"`
}

type DesignCaptureRuntime struct {
	ImageSHA256 string `json:"imageSha256"`
	FontSHA256  string `json:"fontSha256"`
	LockSHA256  string `json:"lockSha256"`
	Node        string `json:"node"`
	Chromium    string `json:"chromium"`
	Playwright  string `json:"playwright"`
}

type DesignCaptureArchive struct {
	Name   string `json:"name"`
	Bytes  int64  `json:"bytes"`
	SHA256 string `json:"sha256"`
}

type DesignCaptureCommand struct {
	RequestID    string                  `json:"requestId"`
	Request      DesignCaptureJobRequest `json:"request"`
	Producer     DesignCaptureProducer   `json:"producer"`
	Runtime      DesignCaptureRuntime    `json:"runtime"`
	LaunchSHA256 string                  `json:"launchSha256"`
}

type DesignCaptureStoredJob struct {
	Request       DesignCaptureJobRequest `json:"request"`
	RequestSHA256 string                  `json:"requestSha256"`
	ReceiptSHA256 string                  `json:"receiptSha256"`
	Producer      DesignCaptureProducer   `json:"producer"`
	Runtime       DesignCaptureRuntime    `json:"runtime"`
	Capture       DesignCaptureArchive    `json:"capture"`
}

type DesignCaptureReturn struct {
	Job             DesignCaptureStoredJob `json:"job"`
	ReceiptArtifact string                 `json:"receiptArtifact"`
	CaptureArtifact string                 `json:"captureArtifact"`
}

var designCaptureID = regexp.MustCompile(`^[a-f0-9]{8}(?:-[a-f0-9]{4}){3}-[a-f0-9]{12}$`)
var designCaptureHash = regexp.MustCompile(`^[a-f0-9]{64}$`)
var designCaptureCommit = regexp.MustCompile(`^[a-f0-9]{40}$`)

func (r DesignCaptureJobRequest) Valid() bool {
	return r.Version == 1 && r.Project == "fleet" && designCaptureID.MatchString(r.Task) &&
		designCaptureID.MatchString(r.Session) && designCaptureID.MatchString(r.Execution) &&
		designCaptureID.MatchString(r.Job) && r.Job != r.Execution && designCaptureCommit.MatchString(r.Base) &&
		designCaptureHash.MatchString(r.Nonce) && designCaptureHash.MatchString(r.BaseSHA256) &&
		designCaptureHash.MatchString(r.OverlaySHA256) && designCaptureHash.MatchString(r.FixtureSHA256)
}

func (r DesignCaptureJobRequest) Bytes() []byte {
	b, _ := json.Marshal(r)
	return b
}

func (r DesignCaptureJobRequest) Digest() string {
	h := sha256.Sum256(r.Bytes())
	return hex.EncodeToString(h[:])
}

func (p DesignCaptureProducer) Valid() bool {
	return designCaptureCommit.MatchString(p.Commit) && p.Version != "" && len(p.Version) < 100 && p.Target == "linux-amd64"
}

func (r DesignCaptureRuntime) Valid() bool {
	return designCaptureHash.MatchString(r.ImageSHA256) && designCaptureHash.MatchString(r.FontSHA256) &&
		designCaptureHash.MatchString(r.LockSHA256) && r.Node != "" && r.Chromium != "" && r.Playwright != "" &&
		len(r.Node) < 100 && len(r.Chromium) < 100 && len(r.Playwright) < 100
}

// Only the host transport sends the permit to the broker. The guest reads the
// request and three immutable inputs from the subsequent, credential-free frame.
type DesignCapturePermit struct {
	ExpiresAt          int64  `json:"expiresAt"`
	InstallationSHA256 string `json:"installationSha256"`
	Signature          string `json:"signature"`
}

func (c DesignCaptureCommand) InstallationDigest() string {
	if !c.Producer.Valid() || !c.Runtime.Valid() || !designCaptureHash.MatchString(c.LaunchSHA256) {
		return ""
	}
	raw, _ := json.Marshal(struct {
		Producer     DesignCaptureProducer `json:"producer"`
		Runtime      DesignCaptureRuntime  `json:"runtime"`
		LaunchSHA256 string                `json:"launchSha256"`
	}{c.Producer, c.Runtime, c.LaunchSHA256})
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func (r DesignCaptureJobRequest) PermitMessage(expiresAt int64, installation string) []byte {
	b, _ := json.Marshal(struct {
		Job                string `json:"job"`
		Host               string `json:"host"`
		RequestSHA256      string `json:"requestSha256"`
		InstallationSHA256 string `json:"installationSha256"`
		ExpiresAt          int64  `json:"expiresAt"`
	}{r.Job, DesignCaptureHost, r.Digest(), installation, expiresAt})
	return append([]byte("fleet:design-capture-permit:v2\n"), b...)
}
