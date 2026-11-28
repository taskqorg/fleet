package main

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"image"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"fleet/contract"
)

func captureTestSHA(b []byte) string { d := sha256.Sum256(b); return hex.EncodeToString(d[:]) }

var captureTestRuntime = designerCaptureRuntime{
	ImageSHA256: strings.Repeat("1", 64), FontSHA256: strings.Repeat("2", 64), LockSHA256: strings.Repeat("3", 64),
	Node: "synthetic-node", Chromium: "synthetic-chromium", Playwright: "synthetic-playwright",
}

func captureTestFixture(t *testing.T) (appCaptureRequest, appCaptureManifest, map[string][]byte) {
	t.Helper()
	req := appCaptureRequest{Project: "fleet", Session: "c1111111-1111-4111-8111-111111111111", Execution: "e1111111-1111-4111-8111-111111111111", Nonce: strings.Repeat("a", 64), Base: strings.Repeat("b", 40)}
	bundle, patch := []byte("synthetic-base"), []byte("synthetic-patch")
	overlay := []byte(`{"version":1,"base":"` + req.Base + `","files":[]}` + "\n")
	req.BaseSHA256 = captureTestSHA(bundle)
	req.OverlaySHA256 = captureTestSHA(overlay)
	req.FixtureSHA256 = captureTestSHA([]byte("{}\n"))
	files := map[string][]byte{}
	c := appCaptureCase{ScopeKey: "pages/tasks/app-shell:details", RecipeFile: "case-1-recipe.json"}
	for _, v := range []struct {
		viewport, theme string
		w, h            int
	}{{"desktop", "dark", 1440, 900}, {"mobile", "dark", 390, 844}, {"desktop", "light", 1440, 900}, {"mobile", "light", 390, 844}} {
		var encoded bytes.Buffer
		if err := png.Encode(&encoded, image.NewRGBA(image.Rect(0, 0, v.w, v.h))); err != nil {
			t.Fatal(err)
		}
		imageBytes := encoded.Bytes()
		name := "case-1-" + v.viewport + "-" + v.theme + ".png"
		files[name] = imageBytes
		c.Frames = append(c.Frames, appCaptureFrame{Viewport: v.viewport, Theme: v.theme, Width: v.w, Height: v.h, File: name, SHA256: captureTestSHA(imageBytes), Bytes: int64(len(imageBytes)), Routes: []string{"/projects/FLEET/tasks/T-FLEET-1"}})
	}
	recipe := contract.PreviewRecipe{Version: 1, Scene: "pages/tasks/app-shell", State: "details",
		Path: "/projects/FLEET/tasks/T-FLEET-1", Landmark: "main", Clock: 123, TimeZone: "UTC", Locale: "en-US", DPR: 1,
		ReducedMotion: true, CSS: strings.Repeat("1", 64), Inputs: []contract.PreviewRecipeInput{{Path: "app/main.ts", SHA256: strings.Repeat("6", 64)}}}
	for _, f := range c.Frames {
		recipe.Matrix = append(recipe.Matrix, contract.PreviewRecipeCell{Viewport: f.Viewport, Theme: f.Theme, Width: f.Width, Height: f.Height})
	}
	c.Recipe = recipe.Digest()
	files[c.RecipeFile], _ = json.Marshal(struct {
		contract.PreviewRecipe
		Digest string `json:"digest"`
		HTML   string `json:"html"`
	}{recipe, c.Recipe, strings.Repeat("2", 64)})
	c.RecipeSHA256 = captureTestSHA(files[c.RecipeFile])
	producer := appCaptureProducer{Commit: req.Base, Version: "invented-test-release", Target: "linux-amd64"}
	replay, _ := json.Marshal(map[string]any{
		"version": 1, "producer": producer, "kind": "fleet-normal-app", "base": req.Base,
		"baseTree": strings.Repeat("a", 40), "tree": strings.Repeat("b", 40), "archiveSha256": strings.Repeat("4", 64), "runtime": captureTestRuntime,
		"baseBundleSha256": req.BaseSHA256, "overlaySha256": req.OverlaySHA256, "patchSha256": captureTestSHA(patch),
		"fixtureSha256": req.FixtureSHA256, "fixture": map[string]any{}, "cases": []appCaptureCase{c},
	})
	files["source.tar"] = captureTestSource(t, map[string][]byte{"base.bundle": bundle, "overlay.json": overlay, "overlay.patch": patch, "replay.json": replay})
	manifest := appCaptureManifest{Version: 1, Session: req.Session, Execution: req.Execution, Nonce: req.Nonce, Base: req.Base, Producer: producer, BaseBundleSHA256: req.BaseSHA256, OverlaySHA256: req.OverlaySHA256, Source: "source.tar", SourceSHA256: captureTestSHA(files["source.tar"]), Cases: []appCaptureCase{c}}
	return req, manifest, files
}

func measuredCaptureFixture(t *testing.T) (designerCaptureScope, designerCaptureStoredJob, designerCaptureReceipt, []byte, int) {
	t.Helper()
	r, handoff, files := captureTestFixture(t)
	archive := captureTestTar(t, handoff, files, "")
	scope := designerCaptureScope{Project: r.Project, Task: "a1111111-1111-4111-8111-111111111111", Session: r.Session, Execution: r.Execution}
	request := designerCaptureRequest{
		DesignCaptureScope: scope, Version: 1, Job: "e2222222-2222-4222-8222-222222222222",
		Nonce: r.Nonce, Base: r.Base, BaseSHA256: r.BaseSHA256, OverlaySHA256: r.OverlaySHA256, FixtureSHA256: r.FixtureSHA256,
	}
	rawRequest, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	job := designerCaptureStoredJob{Request: request, RequestSHA256: captureTestSHA(rawRequest), Producer: handoff.Producer,
		Runtime: captureTestRuntime, Capture: designerCaptureArchive{Name: "capture.tar", Bytes: int64(len(archive)), SHA256: captureTestSHA(archive)}}
	receipt := designerCaptureReceipt{Version: 1, Request: request, RequestSHA256: job.RequestSHA256, Producer: job.Producer,
		Runtime: job.Runtime, Capture: job.Capture, Source: designerCaptureSource{SHA256: handoff.SourceSHA256,
			BaseTree: strings.Repeat("a", 40), Tree: strings.Repeat("b", 40), ArchiveSHA256: strings.Repeat("4", 64),
			PatchSHA256: captureTestSHA([]byte("synthetic-patch"))}}
	return scope, job, receipt, archive, len(files) + 1
}

func TestStageDesignerCaptureBindsStoredJobAndActiveDesigner(t *testing.T) {
	scope, job, receipt, archive, fileCount := measuredCaptureFixture(t)

	for _, tc := range []struct {
		name  string
		alter func(*designerCaptureScope, *designerCaptureStoredJob, *designerCaptureReceipt)
	}{
		{name: "matches"},
		{"wrong active execution", func(s *designerCaptureScope, _ *designerCaptureStoredJob, _ *designerCaptureReceipt) {
			s.Execution = s.Task
		}},
		{"wrong job in worker receipt", func(_ *designerCaptureScope, _ *designerCaptureStoredJob, p *designerCaptureReceipt) {
			p.Request.Job = p.Request.Task
		}},
		{"wrong runtime", func(_ *designerCaptureScope, _ *designerCaptureStoredJob, p *designerCaptureReceipt) {
			p.Runtime.Node = "different"
		}},
		{"wrong reconstructed tree", func(_ *designerCaptureScope, _ *designerCaptureStoredJob, p *designerCaptureReceipt) {
			p.Source.Tree = strings.Repeat("c", 40)
		}},
		{"wrong archive size", func(_ *designerCaptureScope, j *designerCaptureStoredJob, p *designerCaptureReceipt) {
			j.Capture.Bytes++
			p.Capture = j.Capture
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			active, stored, measured := scope, job, receipt
			if tc.alter != nil {
				tc.alter(&active, &stored, &measured)
			}
			raw, err := json.Marshal(measured)
			if err != nil {
				t.Fatal(err)
			}
			stored.ReceiptSHA256 = captureTestSHA(raw)
			root := t.TempDir()
			if err := os.Chmod(root, 0700); err != nil {
				t.Fatal(err)
			}
			staged, err := stageDesignerCapture(context.Background(), root, active, stored, raw, bytes.NewReader(archive))
			if tc.alter == nil {
				if err != nil || len(staged.Files) != fileCount {
					t.Fatalf("verified job did not stage originals: %v", err)
				}
			} else {
				if err == nil {
					t.Fatal("accepted mismatched job evidence")
				}
				if entries, err := os.ReadDir(root); err != nil || len(entries) != 0 {
					t.Fatalf("rejected job left staged originals: %v", err)
				}
			}
		})
	}
}

func captureTestSource(t *testing.T, members map[string][]byte) []byte {
	t.Helper()
	var source bytes.Buffer
	tw := tar.NewWriter(&source)
	for name, data := range members {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0600, Size: int64(len(data)), Format: tar.FormatUSTAR}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return source.Bytes()
}

func captureTestTar(t *testing.T, m appCaptureManifest, files map[string][]byte, extra string) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := tar.NewWriter(&buf)
	raw, _ := json.Marshal(m)
	add := func(name string, data []byte) {
		t.Helper()
		if err := w.WriteHeader(&tar.Header{Name: name, Mode: 0600, Size: int64(len(data)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	add("handoff.json", raw)
	for name, data := range files {
		add(name, data)
	}
	if extra != "" {
		add(extra, []byte("x"))
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}
func TestAppCaptureStagesOnlyBoundCompleteOutput(t *testing.T) {
	req, m, files := captureTestFixture(t)
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	raw := captureTestTar(t, m, files, "")
	req.CaptureSHA256 = captureTestSHA(raw)
	receipt, err := stageAppCapture(context.Background(), root, req, bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(receipt.Directory, ".ready") || len(receipt.Files) != len(files)+1 {
		t.Fatalf("bad receipt: %+v", receipt)
	}
	if got, err := os.ReadFile(filepath.Join(receipt.Directory, "source.tar")); err != nil || !bytes.Equal(got, files["source.tar"]) {
		t.Fatalf("source mismatch: %v", err)
	}
	for _, test := range []struct {
		name   string
		mutate func(*appCaptureRequest, *appCaptureManifest, map[string][]byte)
		extra  string
	}{
		{"wrong session", func(r *appCaptureRequest, _ *appCaptureManifest, _ map[string][]byte) {
			r.Session = "00000000-0000-0000-0000-000000000000"
		}, ""},
		{"wrong project", func(r *appCaptureRequest, _ *appCaptureManifest, _ map[string][]byte) { r.Project = "other" }, ""},
		{"wrong nonce", func(r *appCaptureRequest, _ *appCaptureManifest, _ map[string][]byte) {
			r.Nonce = strings.Repeat("0", 64)
		}, ""},
		{"forged replay case", func(_ *appCaptureRequest, m *appCaptureManifest, _ map[string][]byte) {
			m.Cases[0].Recipe = strings.Repeat("f", 64)
		}, ""},
		{"forged replay route", func(_ *appCaptureRequest, m *appCaptureManifest, _ map[string][]byte) {
			m.Cases[0].Frames[0].Routes[0] = "/home"
		}, ""},
		{"wrong fixture", func(r *appCaptureRequest, _ *appCaptureManifest, _ map[string][]byte) {
			r.FixtureSHA256 = strings.Repeat("0", 64)
		}, ""},
		{"source tampered", func(_ *appCaptureRequest, _ *appCaptureManifest, f map[string][]byte) {
			f["source.tar"] = []byte("wrong")
		}, ""},
		{"missing frame", func(_ *appCaptureRequest, _ *appCaptureManifest, f map[string][]byte) {
			delete(f, "case-1-mobile-light.png")
		}, ""},
		{"unexpected path", func(_ *appCaptureRequest, _ *appCaptureManifest, _ map[string][]byte) {}, "../escape"},
	} {
		t.Run(test.name, func(t *testing.T) {
			r, m, f := captureTestFixture(t)
			test.mutate(&r, &m, f)
			parent := t.TempDir()
			_ = os.Chmod(parent, 0700)
			raw := captureTestTar(t, m, f, test.extra)
			r.CaptureSHA256 = captureTestSHA(raw)
			_, err := stageAppCapture(context.Background(), parent, r, bytes.NewReader(raw))
			if err == nil {
				t.Fatal("expected rejection")
			}
			entries, _ := os.ReadDir(parent)
			if len(entries) != 0 {
				t.Fatalf("leftover staging: %v", entries)
			}
		})
	}
}

func TestAppCaptureRejectsMissingOrChangedOriginalOverlay(t *testing.T) {
	for _, changed := range []bool{false, true} {
		name := "missing"
		if changed {
			name = "changed"
		}
		t.Run(name, func(t *testing.T) {
			req, manifest, files := captureTestFixture(t)
			source := tar.NewReader(bytes.NewReader(files["source.tar"]))
			members := map[string][]byte{}
			for {
				header, err := source.Next()
				if err == io.EOF {
					break
				}
				if err != nil {
					t.Fatal(err)
				}
				data, err := io.ReadAll(source)
				if err != nil {
					t.Fatal(err)
				}
				members[header.Name] = data
			}
			if changed {
				members["overlay.json"] = []byte(`{"version":1,"files":[]}`)
			} else {
				delete(members, "overlay.json")
			}
			files["source.tar"] = captureTestSource(t, members)
			manifest.SourceSHA256 = captureTestSHA(files["source.tar"])
			raw := captureTestTar(t, manifest, files, "")
			req.CaptureSHA256 = captureTestSHA(raw)
			root := t.TempDir()
			if err := os.Chmod(root, 0700); err != nil {
				t.Fatal(err)
			}
			if _, err := stageAppCapture(context.Background(), root, req, bytes.NewReader(raw)); err == nil {
				t.Fatal("accepted a source archive without the requested original overlay")
			}
		})
	}
}

func TestAppCaptureRejectsCancellationAndWrongArchiveDigest(t *testing.T) {
	req, manifest, files := captureTestFixture(t)
	raw := captureTestTar(t, manifest, files, "")
	req.CaptureSHA256 = captureTestSHA(raw)
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := stageAppCapture(ctx, root, req, bytes.NewReader(raw)); err == nil {
		t.Fatal("cancelled capture accepted")
	}
	req.CaptureSHA256 = strings.Repeat("0", 64)
	if _, err := stageAppCapture(context.Background(), root, req, bytes.NewReader(raw)); err == nil {
		t.Fatal("wrong archive digest accepted")
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatalf("staging left files after failure: %v %v", entries, err)
	}
}

// Rerender-source and archive digests agree in both regressions. The bytes
// that used to pass config-only PNG and presence-only recipe checks differ.
func TestAppCaptureRejectsDigestBoundTruncatedPNGAndInvalidRecipe(t *testing.T) {
	for _, kind := range []string{"truncated PNG", "empty recipe"} {
		t.Run(kind, func(t *testing.T) {
			req, manifest, files := captureTestFixture(t)
			c := &manifest.Cases[0]
			if kind == "truncated PNG" {
				frame := &c.Frames[0]
				pngBytes := files[frame.File]
				files[frame.File] = append([]byte(nil), pngBytes[:len(pngBytes)-10]...)
				frame.Bytes = int64(len(files[frame.File]))
				frame.SHA256 = captureTestSHA(files[frame.File])
			} else {
				files[c.RecipeFile] = []byte("{}")
				c.RecipeSHA256 = captureTestSHA(files[c.RecipeFile])
			}
			source := map[string][]byte{}
			tr := tar.NewReader(bytes.NewReader(files["source.tar"]))
			for {
				header, err := tr.Next()
				if err == io.EOF {
					break
				}
				if err != nil {
					t.Fatal(err)
				}
				source[header.Name], err = io.ReadAll(tr)
				if err != nil {
					t.Fatal(err)
				}
			}
			var replay map[string]json.RawMessage
			if err := json.Unmarshal(source["replay.json"], &replay); err != nil {
				t.Fatal(err)
			}
			replay["cases"], _ = json.Marshal(manifest.Cases)
			source["replay.json"], _ = json.Marshal(replay)
			files["source.tar"] = captureTestSource(t, source)
			manifest.SourceSHA256 = captureTestSHA(files["source.tar"])
			wire := captureTestTar(t, manifest, files, "")
			req.CaptureSHA256 = captureTestSHA(wire)
			if _, err := stageAppCapture(context.Background(), privateCaptureTestRoot(t), req, bytes.NewReader(wire)); err == nil {
				t.Fatal("archive containing incomplete original was staged")
			}
		})
	}
}

func privateCaptureTestRoot(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	return dir
}
