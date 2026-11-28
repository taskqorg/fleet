package main

// A capture response is untrusted bytes until it is bound to this execution and
// staged in its private browser-capture directory. No VM path enters this code.
import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image/png"
	"io"
	"path"
	"reflect"
	"regexp"
	"strings"

	"fleet/contract"
)

const maxCaptureManifest = 768 << 10
const maxCaptureSource = 64 << 20
const maxCapturePNG = 4 << 20
const maxCaptureRecipe = 768 << 10
const maxCaptureWire = 100 << 20
const maxCaptureCases = 8

var captureScope = regexp.MustCompile(`^[a-z0-9-]+/[a-z0-9-]+/[a-z0-9-]+:[a-z0-9-]+$`)
var captureCommit = regexp.MustCompile(`^[0-9a-f]{40}$`)
var captureID = regexp.MustCompile(`^[0-9a-f]{8}-([0-9a-f]{4}-){3}[0-9a-f]{12}$`)
var errAppCapture = errors.New("private app capture unavailable or invalid")

type appCaptureRequest struct {
	Project       string `json:"project"`
	Session       string `json:"session"`
	Execution     string `json:"execution"`
	Nonce         string `json:"nonce"`
	Base          string `json:"base"`
	BaseSHA256    string `json:"baseSha256"`
	OverlaySHA256 string `json:"overlaySha256"`
	FixtureSHA256 string `json:"fixtureSha256"`
	CaptureSHA256 string `json:"captureSha256"`
	measured      *designerCaptureReceipt
}
type appCaptureFrame struct {
	Viewport     string           `json:"viewport"`
	Theme        string           `json:"theme"`
	Width        int              `json:"width"`
	Height       int              `json:"height"`
	File         string           `json:"file"`
	SHA256       string           `json:"sha256"`
	Bytes        int64            `json:"bytes"`
	Routes       []string         `json:"routes"`
	ImageJourney *appCaptureImage `json:"imageJourney,omitempty"`
}

type appCaptureImage struct {
	Focus        string      `json:"focus"`
	SourceScroll [][]float64 `json:"sourceScroll,omitempty"`
}
type appCaptureCase struct {
	ScopeKey     string            `json:"scopeKey"`
	Recipe       string            `json:"recipe"`
	RecipeFile   string            `json:"recipeFile"`
	RecipeSHA256 string            `json:"recipeSha256"`
	Frames       []appCaptureFrame `json:"frames"`
}
type appCaptureProducer = contract.DesignCaptureProducer

type appCaptureManifest struct {
	Version          int                `json:"version"`
	Session          string             `json:"session"`
	Execution        string             `json:"execution"`
	Nonce            string             `json:"nonce"`
	Base             string             `json:"base"`
	Producer         appCaptureProducer `json:"producer"`
	BaseBundleSHA256 string             `json:"baseBundleSha256"`
	OverlaySHA256    string             `json:"overlaySha256"`
	Source           string             `json:"source"`
	SourceSHA256     string             `json:"sourceSha256"`
	Cases            []appCaptureCase   `json:"cases"`
}
type captureFile struct {
	digest        string
	limit         int64
	size          int64
	width, height int
	recipe        *appCaptureCase
}

type appCaptureReceipt struct {
	Directory      string   `json:"directory"`
	ManifestSHA256 string   `json:"manifestSha256"`
	Files          []string `json:"files"`
}

// requested and scope are supplied by the executor owner, not by the guest.
func captureFiles(m appCaptureManifest, requested appCaptureRequest) (map[string]captureFile, error) {
	if requested.Project != "fleet" || m.Version != 1 ||
		!captureID.MatchString(requested.Session) || !captureID.MatchString(requested.Execution) ||
		m.Session != requested.Session || m.Execution != requested.Execution ||
		!sceneSHA.MatchString(requested.Nonce) || !sceneSHA.MatchString(requested.CaptureSHA256) || m.Nonce != requested.Nonce ||
		!captureCommit.MatchString(requested.Base) || m.Base != requested.Base || m.Producer.Commit != requested.Base ||
		m.Producer.Target != "linux-amd64" || m.Producer.Version == "" || len(m.Producer.Version) >= 100 ||
		!sceneSHA.MatchString(requested.BaseSHA256) || m.BaseBundleSHA256 != requested.BaseSHA256 ||
		!sceneSHA.MatchString(requested.OverlaySHA256) || m.OverlaySHA256 != requested.OverlaySHA256 ||
		!sceneSHA.MatchString(requested.FixtureSHA256) ||
		m.Source != "source.tar" || !sceneSHA.MatchString(m.SourceSHA256) || len(m.Cases) < 1 || len(m.Cases) > maxCaptureCases {
		return nil, errAppCapture
	}
	if proof := requested.measured; proof != nil && (m.Producer != proof.Producer || m.SourceSHA256 != proof.Source.SHA256) {
		return nil, errAppCapture
	}
	files := map[string]captureFile{"source.tar": {digest: m.SourceSHA256, limit: maxCaptureSource}}
	scopes := map[string]bool{}
	matrix := []struct {
		viewport, theme string
		width, height   int
	}{
		{"desktop", "dark", 1440, 900}, {"mobile", "dark", 390, 844},
		{"desktop", "light", 1440, 900}, {"mobile", "light", 390, 844},
	}
	for i, c := range m.Cases {
		if !captureScope.MatchString(c.ScopeKey) || scopes[c.ScopeKey] || !sceneSHA.MatchString(c.Recipe) ||
			c.RecipeFile != fmt.Sprintf("case-%d-recipe.json", i+1) || !sceneSHA.MatchString(c.RecipeSHA256) || len(c.Frames) != len(matrix) {
			return nil, errAppCapture
		}
		scopes[c.ScopeKey] = true
		files[c.RecipeFile] = captureFile{digest: c.RecipeSHA256, limit: maxCaptureRecipe, recipe: &c}
		for j, frame := range c.Frames {
			cell := matrix[j]
			name := fmt.Sprintf("case-%d-%s-%s.png", i+1, cell.viewport, cell.theme)
			if frame.File != name || frame.Viewport != cell.viewport || frame.Theme != cell.theme ||
				frame.Width != cell.width || frame.Height != cell.height || !sceneSHA.MatchString(frame.SHA256) ||
				frame.Bytes < 33 || frame.Bytes > maxCapturePNG || len(frame.Routes) != 1 {
				return nil, errAppCapture
			}
			files[name] = captureFile{digest: frame.SHA256, limit: maxCapturePNG, size: frame.Bytes, width: cell.width, height: cell.height}
		}
	}
	return files, nil
}

// Verify the retained source names the exact base and overlay requested by the
// designer before publishing any paths. No archive member is extracted here.
func verifyAppCaptureSource(ctx context.Context, source io.Reader, requested appCaptureRequest, manifest appCaptureManifest, validateReplay func([]byte) error) error {
	tr := tar.NewReader(&captureContextReader{ctx: ctx, reader: source})
	allowed := map[string]int64{"base.bundle": 60 << 20, "overlay.json": 3 << 20, "overlay.patch": 2 << 20, "replay.json": maxCaptureManifest}
	hashes := map[string]string{}
	var replay struct {
		Version          int                    `json:"version"`
		Producer         appCaptureProducer     `json:"producer"`
		Kind             string                 `json:"kind"`
		Base             string                 `json:"base"`
		BaseTree         string                 `json:"baseTree"`
		Tree             string                 `json:"tree"`
		ArchiveSHA256    string                 `json:"archiveSha256"`
		AppSHA256        string                 `json:"appSha256"`
		Paths            []string               `json:"paths"`
		Runtime          designerCaptureRuntime `json:"runtime"`
		BaseBundleSHA256 string                 `json:"baseBundleSha256"`
		OverlaySHA256    string                 `json:"overlaySha256"`
		PatchSHA256      string                 `json:"patchSha256"`
		FixtureSHA256    string                 `json:"fixtureSha256"`
		Fixture          json.RawMessage        `json:"fixture"`
		Cases            []appCaptureCase       `json:"cases"`
	}
	for {
		header, e := tr.Next()
		if e == io.EOF {
			break
		}
		if e != nil || header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeRegA {
			return errAppCapture
		}
		limit, ok := allowed[header.Name]
		if !ok || header.Size < 0 || header.Size > limit || header.Name != "overlay.patch" && header.Size == 0 {
			return errAppCapture
		}
		if header.Name == "replay.json" {
			raw, e := io.ReadAll(io.LimitReader(tr, limit+1))
			if e != nil || int64(len(raw)) != header.Size || exactKeys(raw, "version", "producer", "commit", "target", "kind", "base", "baseTree", "tree", "archiveSha256", "appSha256", "paths", "runtime", "imageSha256", "fontSha256", "lockSha256", "node", "chromium", "playwright", "baseBundleSha256", "overlaySha256", "fixtureSha256", "fixture", "patchSha256", "cases", "scopeKey", "recipe", "recipeFile", "recipeSha256", "frames", "viewport", "theme", "width", "height", "file", "sha256", "bytes", "routes", "imageJourney", "focus", "sourceScroll") != nil || json.Unmarshal(raw, &replay) != nil || validateReplay != nil && validateReplay(raw) != nil {
				return errAppCapture
			}
		} else {
			sum := sha256.New()
			size, e := io.Copy(sum, tr)
			if e != nil || size != header.Size {
				return errAppCapture
			}
			hashes[header.Name] = hex.EncodeToString(sum.Sum(nil))
		}
		delete(allowed, header.Name)
	}
	tail, err := io.ReadAll(io.LimitReader(&captureContextReader{ctx: ctx, reader: source}, 10241))
	if err != nil || len(tail) > 10240 || len(bytes.Trim(tail, "\x00")) != 0 {
		return errAppCapture
	}
	if proof := requested.measured; proof != nil &&
		(!captureCommit.MatchString(replay.BaseTree) || !captureCommit.MatchString(replay.Tree) ||
			!sceneSHA.MatchString(replay.ArchiveSHA256) || replay.BaseTree != proof.Source.BaseTree ||
			replay.Tree != proof.Source.Tree || replay.ArchiveSHA256 != proof.Source.ArchiveSHA256 ||
			replay.PatchSHA256 != proof.Source.PatchSHA256 || replay.Runtime != proof.Runtime) {
		return errAppCapture
	}
	if len(allowed) != 0 || !sceneSHA.MatchString(replay.AppSHA256) || replay.Paths == nil || replay.Version != 1 || replay.Producer != manifest.Producer || replay.Kind != "fleet-normal-app" || replay.Base != requested.Base ||
		replay.BaseBundleSHA256 != requested.BaseSHA256 || hashes["base.bundle"] != requested.BaseSHA256 ||
		replay.OverlaySHA256 != requested.OverlaySHA256 || hashes["overlay.json"] != requested.OverlaySHA256 || replay.FixtureSHA256 != requested.FixtureSHA256 ||
		len(replay.Fixture) == 0 || !sceneSHA.MatchString(replay.PatchSHA256) ||
		hashes["overlay.patch"] != replay.PatchSHA256 || !reflect.DeepEqual(replay.Cases, manifest.Cases) ||
		func() string {
			sum := sha256.Sum256(append(append([]byte(nil), replay.Fixture...), '\n'))
			return hex.EncodeToString(sum[:])
		}() != requested.FixtureSHA256 {
		return errAppCapture
	}
	return nil
}

type captureContextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r *captureContextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	if len(p) > 64*1024 {
		p = p[:64*1024]
	}
	return r.reader.Read(p)
}

// Verify while streaming. The production mediator supplies no writer and
// never materializes an unverified member in a filesystem namespace.
func verifyAppCapture(ctx context.Context, requested appCaptureRequest, source io.Reader,
	validateReplay func([]byte) error, write func(string, []byte) error,
) (receipt appCaptureReceipt, err error) {
	archiveDigest := sha256.New()
	limited := &io.LimitedReader{R: io.TeeReader(&captureContextReader{ctx: ctx, reader: source}, archiveDigest), N: maxCaptureWire + 1}
	tr := tar.NewReader(limited)
	h, err := tr.Next()
	if err != nil || h.Name != "handoff.json" || h.Typeflag != tar.TypeReg && h.Typeflag != tar.TypeRegA || h.Size <= 0 || h.Size > maxCaptureManifest {
		return receipt, errAppCapture
	}
	raw, err := io.ReadAll(io.LimitReader(tr, maxCaptureManifest+1))
	if err != nil || int64(len(raw)) != h.Size || exactKeys(raw, "version", "session", "execution", "nonce", "base", "producer", "commit", "target", "baseBundleSha256", "overlaySha256", "source", "sourceSha256", "cases", "scopeKey", "recipe", "recipeFile", "recipeSha256", "frames", "viewport", "theme", "width", "height", "file", "sha256", "bytes", "routes", "imageJourney", "focus", "sourceScroll") != nil {
		return receipt, errAppCapture
	}
	var manifest appCaptureManifest
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&manifest) != nil || decoder.Decode(new(json.RawMessage)) != io.EOF {
		return receipt, errAppCapture
	}
	expected, err := captureFiles(manifest, requested)
	if err != nil {
		return receipt, errAppCapture
	}
	if write != nil && write("handoff.json", raw) != nil {
		return receipt, errAppCapture
	}
	files := []string{"handoff.json"}
	for {
		h, err = tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil || h.Typeflag != tar.TypeReg && h.Typeflag != tar.TypeRegA || strings.Contains(h.Name, "/") {
			return receipt, errAppCapture
		}
		want, ok := expected[h.Name]
		if !ok || h.Size <= 0 || h.Size > want.limit || want.width != 0 && h.Size != want.size {
			return receipt, errAppCapture
		}
		if h.Name == "source.tar" && write == nil {
			memberHash := sha256.New()
			if err := verifyAppCaptureSource(ctx, io.TeeReader(tr, memberHash), requested, manifest, validateReplay); err != nil ||
				hex.EncodeToString(memberHash.Sum(nil)) != want.digest {
				return receipt, errAppCapture
			}
		} else {
			data, err := io.ReadAll(tr)
			if err != nil || int64(len(data)) != h.Size || digestCaptureBytes(data) != want.digest {
				return receipt, errAppCapture
			}
			if h.Name == "source.tar" && verifyAppCaptureSource(ctx, bytes.NewReader(data), requested, manifest, validateReplay) != nil {
				return receipt, errAppCapture
			}
			if want.width != 0 {
				reader := bytes.NewReader(data)
				config, err := png.DecodeConfig(reader)
				if err != nil || config.Width != want.width || config.Height != want.height {
					return receipt, errAppCapture
				}
				reader.Reset(data)
				if _, err := png.Decode(reader); err != nil || reader.Len() != 0 {
					return receipt, errAppCapture
				}
			}
			if want.recipe != nil && !validAppCaptureRecipe(data, *want.recipe) {
				return receipt, errAppCapture
			}
			if write != nil && write(h.Name, data) != nil {
				return receipt, errAppCapture
			}
		}
		delete(expected, h.Name)
		files = append(files, h.Name)
	}
	if len(expected) != 0 || limited.N <= 0 {
		return receipt, errAppCapture
	}
	// Do not accept a second concatenated archive or trailing payload.
	tail, e := io.ReadAll(io.LimitReader(limited, 10241))
	if e != nil || len(tail) > 10240 || len(bytes.Trim(tail, "\x00")) != 0 || limited.N <= 0 {
		return receipt, errAppCapture
	}
	if hex.EncodeToString(archiveDigest.Sum(nil)) != requested.CaptureSHA256 {
		return receipt, errAppCapture
	}
	if proof := requested.measured; proof != nil && maxCaptureWire+1-limited.N != proof.Capture.Bytes {
		return receipt, errAppCapture
	}
	if ctx.Err() != nil {
		return receipt, errAppCapture
	}
	return appCaptureReceipt{ManifestSHA256: digestCaptureBytes(raw), Files: files}, nil
}

func validAppCaptureRecipe(raw []byte, c appCaptureCase) bool {
	var r struct {
		contract.PreviewRecipe
		Digest string `json:"digest"`
		HTML   string `json:"html"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if exactKeys(raw, "version", "scene", "state", "path", "landmark", "clock", "timeZone", "locale", "dpr",
		"reducedMotion", "matrix", "viewport", "theme", "width", "height", "css", "inputs", "sha256", "digest", "html") != nil ||
		decoder.Decode(&r) != nil || decoder.Decode(new(json.RawMessage)) != io.EOF || r.Version != 1 ||
		r.Scene+":"+r.State != c.ScopeKey || r.Digest != c.Recipe || r.PreviewRecipe.Digest() != c.Recipe ||
		!sceneSHA.MatchString(r.CSS) || !sceneSHA.MatchString(r.HTML) || r.Landmark == "" || r.Clock <= 0 ||
		r.TimeZone != "UTC" || r.Locale != "en-US" || r.DPR != 1 || !r.ReducedMotion || len(r.Inputs) == 0 ||
		len(r.Matrix) != len(c.Frames) || r.Path == "" || !strings.HasPrefix(r.Path, "/") || strings.HasPrefix(r.Path, "//") {
		return false
	}
	for i, m := range r.Matrix {
		f := c.Frames[i]
		if len(f.Routes) != 1 || f.Routes[0] != r.Path || m.Viewport != f.Viewport || m.Theme != f.Theme ||
			m.Width != f.Width || m.Height != f.Height {
			return false
		}
	}
	for i, input := range r.Inputs {
		if input.Path == "" || input.Path == "." || input.Path == ".." || strings.HasPrefix(input.Path, "../") ||
			strings.HasPrefix(input.Path, "/") || strings.Contains(input.Path, "\\") || path.Clean(input.Path) != input.Path ||
			!sceneSHA.MatchString(input.SHA256) || i > 0 && r.Inputs[i-1].Path >= input.Path {
			return false
		}
	}
	return true
}
