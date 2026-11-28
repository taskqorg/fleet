package main

// A capture response is untrusted bytes until it is bound to this execution and
// staged in its private browser-capture directory. No VM path enters this code.
import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image/png"
	"io"
	"os"
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
func verifyAppCaptureSource(ctx context.Context, staged *os.Root, requested appCaptureRequest, manifest appCaptureManifest) error {
	source, err := staged.Open("source.tar")
	if err != nil {
		return errAppCapture
	}
	defer source.Close()
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
			if e != nil || int64(len(raw)) != header.Size || exactKeys(raw, "version", "producer", "commit", "target", "kind", "base", "baseTree", "tree", "archiveSha256", "runtime", "imageSha256", "fontSha256", "lockSha256", "node", "chromium", "playwright", "baseBundleSha256", "overlaySha256", "fixtureSha256", "fixture", "patchSha256", "cases", "scopeKey", "recipe", "recipeFile", "recipeSha256", "frames", "viewport", "theme", "width", "height", "file", "sha256", "bytes", "routes", "imageJourney", "focus", "sourceScroll") != nil || json.Unmarshal(raw, &replay) != nil {
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
	if len(allowed) != 0 || replay.Version != 1 || replay.Producer != manifest.Producer || replay.Kind != "fleet-normal-app" || replay.Base != requested.Base ||
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

// stageAppCapture admits a single bounded tar with handoff.json first and only
// the manifest's named files. It publishes the directory by one final rename.
func stageAppCapture(ctx context.Context, root string, requested appCaptureRequest, source io.Reader) (receipt appCaptureReceipt, err error) {
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0700 || owner(info) != os.Geteuid() {
		return receipt, errAppCapture
	}
	temp, err := os.MkdirTemp(root, ".app-capture-")
	if err != nil {
		return receipt, errAppCapture
	}
	originalDir, err := os.Lstat(temp)
	if err != nil || !originalDir.IsDir() || originalDir.Mode()&os.ModeSymlink != 0 {
		return receipt, errAppCapture
	}
	defer func() {
		if err != nil {
			current, statErr := os.Lstat(temp)
			if statErr == nil && os.SameFile(originalDir, current) {
				if removeErr := os.RemoveAll(temp); removeErr != nil {
					err = errors.New("private capture cleanup failed")
				}
			}
		}
	}()
	staged, err := os.OpenRoot(temp)
	if err != nil {
		return receipt, errAppCapture
	}
	defer staged.Close()
	opened, err := staged.Stat(".")
	if err != nil || !os.SameFile(originalDir, opened) {
		return receipt, errAppCapture
	}
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
	manifestFile, e := staged.OpenFile("handoff.json", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if e != nil {
		return receipt, errAppCapture
	}
	_, e = manifestFile.Write(raw)
	closeManifest := manifestFile.Close()
	if e != nil || closeManifest != nil {
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
		dst, e := staged.OpenFile(h.Name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if e != nil {
			return receipt, errAppCapture
		}
		digest := sha256.New()
		var first bytes.Buffer
		writer := io.MultiWriter(dst, digest)
		// At most a PNG header is kept in memory; image bodies are streamed.
		var written int64
		if want.width != 0 {
			head := make([]byte, 24)
			if _, e = io.ReadFull(tr, head); e == nil {
				_, _ = first.Write(head)
				_, e = writer.Write(head)
				written = int64(len(head))
			}
		}
		if e == nil {
			var n int64
			n, e = io.Copy(writer, tr)
			written += n
		}
		closeErr := dst.Close()
		if e != nil || closeErr != nil || written != h.Size || hex.EncodeToString(digest.Sum(nil)) != want.digest {
			return receipt, errAppCapture
		}
		if want.width != 0 {
			b := first.Bytes()
			if len(b) < 24 || !bytes.Equal(b[:8], []byte{137, 80, 78, 71, 13, 10, 26, 10}) || binary.BigEndian.Uint32(b[8:12]) != 13 || string(b[12:16]) != "IHDR" ||
				int(binary.BigEndian.Uint32(b[16:20])) != want.width || int(binary.BigEndian.Uint32(b[20:24])) != want.height {
				return receipt, errAppCapture
			}
			imageFile, openErr := staged.Open(h.Name)
			if openErr != nil {
				return receipt, errAppCapture
			}
			image, decodeErr := png.Decode(&captureContextReader{ctx: ctx, reader: imageFile})
			tail, tailErr := io.ReadAll(io.LimitReader(imageFile, 1))
			closeErr := imageFile.Close()
			if decodeErr != nil || tailErr != nil || closeErr != nil || len(tail) != 0 ||
				image.Bounds().Dx() != want.width || image.Bounds().Dy() != want.height {
				return receipt, errAppCapture
			}
		}
		if want.recipe != nil {
			raw, err := staged.ReadFile(h.Name)
			if err != nil || !validAppCaptureRecipe(raw, *want.recipe) {
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
	if verifyAppCaptureSource(ctx, staged, requested, manifest) != nil {
		return receipt, errAppCapture
	}
	if ctx.Err() != nil {
		return receipt, errAppCapture
	}
	currentDir, e := os.Lstat(temp)
	if e != nil || !os.SameFile(originalDir, currentDir) {
		return receipt, errAppCapture
	}
	final := temp + ".ready"
	if _, e = os.Lstat(final); !errors.Is(e, os.ErrNotExist) {
		return receipt, errAppCapture
	}
	if e = os.Rename(temp, final); e != nil {
		return receipt, errAppCapture
	}
	moved, e := os.Lstat(final)
	if e != nil || !os.SameFile(originalDir, moved) {
		return receipt, errAppCapture
	}
	if ctx.Err() != nil {
		current, statErr := os.Lstat(final)
		if statErr == nil && os.SameFile(originalDir, current) {
			_ = os.RemoveAll(final)
		}
		return receipt, errAppCapture
	}
	sum := sha256.Sum256(raw)
	return appCaptureReceipt{Directory: final, ManifestSHA256: hex.EncodeToString(sum[:]), Files: files}, nil
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
