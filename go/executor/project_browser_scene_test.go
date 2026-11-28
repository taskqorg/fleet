package main

import (
	"encoding/json"
	"image/png"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// A build from another run, a path alias, or bytes replaced since the build must never reach the renderer.
func TestProjectBrowserSceneFiles(t *testing.T) {
	scope := browserSceneScope{Checkout: t.TempDir(), Execution: "current"}
	html := []byte("invented scene")
	dir := filepath.Join(scope.directory(), "valid")
	noErr(t, os.MkdirAll(dir, 0700))
	noErr(t, os.WriteFile(filepath.Join(dir, "recipe.json"), []byte("{}"), 0600))
	file := filepath.Join(dir, "preview.html")
	noErr(t, os.WriteFile(file, html, 0600))
	reply, err := scope.read("valid", sha(html))
	if err != nil || !reflect.DeepEqual(reply["html"], html) {
		t.Fatalf("scoped build rejected: %v", err)
	}
	reject := func(s browserSceneScope, name, hash string) {
		t.Helper()
		if _, err := s.read(name, hash); err == nil {
			t.Fatalf("unscoped or invalid scene accepted: %q", name)
		}
	}
	reject(browserSceneScope{Checkout: scope.Checkout, Execution: "previous"}, "valid", sha(html))
	reject(scope, "../current/valid", sha(html))
	reject(scope, "valid", sha([]byte("different HTML")))
	noErr(t, os.Symlink(dir, filepath.Join(scope.directory(), "alias")))
	reject(scope, "alias", sha(html))
	link := filepath.Join(scope.Checkout, "alias.html")
	noErr(t, os.Link(file, link))
	reject(scope, "valid", sha(html))
	noErr(t, os.Remove(file))
	noErr(t, os.Symlink(link, file))
	reject(scope, "valid", sha(html))
	noErr(t, os.Remove(file))
	noErr(t, os.WriteFile(file, html, 0600))
	noErr(t, os.Truncate(file, maxSceneHTML+1))
	reject(scope, "valid", sha(html))
}

// Run the owned browser adapter through MCP, without a scene HTTP server.
func TestProjectBrowserSceneMCP(t *testing.T) {
	executable := os.Getenv("FLEET_BROWSER_EXECUTABLE")
	module := os.Getenv("FLEET_PLAYWRIGHT_MODULE")
	if executable == "" && module == "" {
		t.Skip("requires pinned project-browser runtime")
	}
	o, _ := testMCPOwner(t)
	scope := browserSceneScope{Checkout: t.TempDir(), Execution: o.execution}
	dir := filepath.Join(scope.directory(), "fixture")
	noErr(t, os.MkdirAll(dir, 0700))
	const recipe = `{"version":1,"path":"/tasks"}`
	digest := sha([]byte("fleet:preview-recipe:v1\x00" + recipe))
	html := []byte(`<!doctype html><title>Private fixture</title><body data-preview-recipe="` + digest + `">
<button onclick="location.hash='/tasks/next';this.textContent='Opened'">Open</button>
</body>`)
	noErr(t, os.WriteFile(filepath.Join(dir, "recipe.json"), []byte(recipe[:len(recipe)-1]+`,"digest":"`+digest+`","html":"`+sha(html)+`"}`), 0600))
	noErr(t, os.WriteFile(filepath.Join(dir, "preview.html"), html, 0600))
	cfg := M{"captureDir": t.TempDir(), "module": module, "ownedExecutor": exe(),
		"privateScenes": true, "previewOrigins": []any{"http://127.0.0.1:4173"}, "timeoutMs": 10000}
	node, err := managedBrowserRuntime(cfg)
	noErr(t, err)
	o.browser = &browserAuthority{Config: cfg, Node: node, Scene: &scope}
	spec := projectBrowserServer(cfg, true)
	source, input, err := os.Pipe()
	noErr(t, err)
	output, sink, err := os.Pipe()
	noErr(t, err)
	defer output.Close()
	done := make(chan error, 1)
	go func() {
		err := mcpStdio(o.socket, spec, source, sink, scope.Checkout, "")
		sink.Close()
		done <- err
	}()
	defer func() {
		input.Close()
		noErr(t, <-done)
	}()
	encoder, decoder := json.NewEncoder(input), json.NewDecoder(output)
	id := 0
	call := func(name string, args M, wantError bool) M {
		t.Helper()
		id++
		noErr(t, encoder.Encode(M{"jsonrpc": "2.0", "id": id, "method": "tools/call", "params": M{"name": name, "arguments": args}}))
		var reply M
		noErr(t, decoder.Decode(&reply))
		if failed := reply["error"] != nil; failed != wantError {
			t.Fatalf("%s: unexpected reply %v", name, reply)
		}
		if wantError {
			return nil
		}
		return typedRaw[M]([]byte(jStr(jArr(reply, "result", "content")[0], "text")))
	}
	args := M{"name": "fixture", "recipeDigest": digest, "htmlSha256": sha(html)}
	call("open_scene", M{"name": "fixture", "recipeDigest": sha(html), "htmlSha256": sha(html)}, true)
	opened := call("open_scene", args, false)
	if jStr(opened, "recipeDigest") != digest {
		t.Fatal("opened recipe differs")
	}
	isolation := call("evaluate", M{"expression": `(async () => ({
		opaque: self.origin === 'null',
		networkDenied: await fetch('/another-document').then(() => false, () => true),
	}))()`}, false)
	if !jIsTrue(isolation, "result", "opaque") || !jIsTrue(isolation, "result", "networkDenied") {
		t.Fatalf("scene escaped isolation: %v", isolation)
	}
	call("click", M{"selector": "button"}, false)
	if got := jStr(call("read", M{}, false), "text"); got != "Opened" {
		t.Fatalf("scene interaction: %q", got)
	}
	call("emulate", M{"viewport": "mobile", "colorScheme": "dark"}, false)
	capture := call("screenshot", M{}, false)
	file, err := os.Open(jStr(capture, "path"))
	noErr(t, err)
	defer file.Close()
	frame, err := png.DecodeConfig(file)
	noErr(t, err)
	if frame.Width != 390 || frame.Height != 844 {
		t.Fatalf("wrong scene dimensions: %v", frame)
	}
	call("navigate", M{"url": "http://127.0.0.1:4173"}, true)
	call("navigate", M{"url": jStr(opened, "url")}, true)
}
