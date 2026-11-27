package main

// Requirement: browser access is project-granted through normal MCP, never arbitrary origins.
// Risk: a missing binding or cross-origin navigation launches an unscoped host browser.
import (
	"encoding/json"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

func TestProjectBrowserScope(t *testing.T) {
	d := t.TempDir()
	binding := filepath.Join(d, "bindings.json")
	module := filepath.Join(d, "mock.cjs")
	marker := filepath.Join(d, "spawned")
	noErr(t, os.WriteFile(module, []byte(`
require('node:child_process').spawn=()=>{
  require('node:fs').writeFileSync(JSON.parse(process.argv[1]).marker,'spawned');
  throw Error('unexpected launch');
};
module.exports={chromium:{}};
`), 0600))
	os.WriteFile(binding, []byte(`{"":{"previewOrigins":["http://127.0.0.1:4173"]},"project":{"previewOrigins":["http://127.0.0.1:4173"]}}`), 0600)
	t.Setenv("FLEET_BROWSER_BINDINGS", binding)
	t.Setenv("FLEET_PLAYWRIGHT_MODULE", module)
	t.Setenv("FLEET_BROWSER_EXECUTABLE", module)
	// Normal signed StartPayload carries identity separately; normalized settings have no id.
	payload := M{"projectId": "project", "project": M{"execution": M{"tools": []any{"browser"}}}}
	cfg, err := browserConfig(payload, false)
	if err != nil {
		t.Fatal(err)
	}
	cfg["marker"] = marker
	if _, err := browserConfig(M{"projectId": "other", "project": M{"id": "project"}}, false); err == nil {
		t.Fatal("foreign authoritative identity accepted")
	}
	if _, err := browserConfig(M{"project": M{"id": "project"}}, false); err == nil {
		t.Fatal("settings identity granted browser without authoritative projectId")
	}
	for _, id := range []any{nil, "", false, 42, []any{"project"}, M{"id": "project"}} {
		if _, err := browserConfig(M{"projectId": id, "project": M{"id": "project"}}, false); err == nil {
			t.Fatalf("invalid authoritative identity accepted: %T", id)
		}
	}
	c := browserCommand(cfg, d, false)
	c.Stdin = strings.NewReader(`
{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"navigate","arguments":{"url":"http://127.0.0.1:9999/admin"}}}
{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"screenshot"}}
{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"emulate","arguments":{"viewport":"20000x20000","colorScheme":"dark"}}}
`)
	out, err := c.Output()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(out), `"error"`) != 3 {
		t.Fatalf("unsafe result: %s", out)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("denied call attempted browser launch: %v", err)
	}
	os.WriteFile(binding, []byte(`{"project":{"previewOrigins":["http://127.0.0.1:4173/admin"]}}`), 0600)
	if _, err := browserConfig(payload, false); err == nil {
		t.Fatal("path accepted as origin")
	}
	os.WriteFile(binding, []byte(`{"project":{"production":{"origin":"https://other.invalid","accessFile":"/invented"}}}`), 0600)
	if _, err := browserConfig(payload, false); err == nil {
		t.Fatal("unauthorized production accepted")
	}
}

// Requirement: project browser close tolerates lingering Chromium sub-process files (ENOTEMPTY retry) and removes profileDir.
// Risk: synchronous unretried fs.rmSync fails readiness checks with ENOTEMPTY while browser helper processes are still exiting.
func TestProjectBrowserCloseRetry(t *testing.T) {
	d := t.TempDir()
	module := filepath.Join(d, "mock.cjs")
	marker := filepath.Join(d, "removed-dir.txt")
	if err := os.WriteFile(module, []byte(`
const assert=require('node:assert/strict'),fs=require('node:fs'),path=require('node:path'),events=require('node:events');
const {marker,failRm,production}=JSON.parse(process.argv[1]);
let socket, pending;
let spawnedDir='';
const origRm=fs.promises.rm.bind(fs.promises);
fs.rmSync=target=>{
  if(target===spawnedDir)throw Object.assign(Error('ENOTEMPTY: directory not empty, rmdir'),{code:'ENOTEMPTY'});
};
fs.promises.rm=async(target,opts)=>{
  assert.equal(target,spawnedDir);
  assert.equal(opts?.recursive,true);
  assert.equal(opts?.force,true);
  assert.ok((opts?.maxRetries??0)>=20,'expected maxRetries>=20');
  assert.ok((opts?.retryDelay??0)>=100,'expected retryDelay>=100');
  if(failRm){await origRm(target,opts);throw Object.assign(Error('ENOTEMPTY: persistent'),{code:'ENOTEMPTY'});}
  await new Promise(r=>setTimeout(r,25));
  await origRm(target,opts);
  assert.equal(fs.existsSync(target),false);
  fs.writeFileSync(marker,target);
};
class Socket {
  constructor(){socket=this;this.listeners={};queueMicrotask(()=>this.emit('open',{}));}
  addEventListener(name,fn){(this.listeners[name]??=[]).push(fn);}
  emit(name,event){for(const fn of this.listeners[name]??[])fn(event);}
  close(){}
  send(raw){
    const m=JSON.parse(raw);
    queueMicrotask(()=>this.emit('message',{data:JSON.stringify({id:m.id,result:{}})}));
    if(m.method==='Fetch.continueRequest'){
      const secret=m.params.headers.find(x=>x.name==='CF-Access-Client-Secret')?.value;
      assert.equal(secret,m.params.requestId==='prod'?'invented-secret':undefined);
      assert.equal(m.params.interceptResponse,m.params.requestId==='prod'?true:undefined);
      pending();
    }
    if(m.method==='Fetch.continueResponse'){
      assert.deepEqual(m.params.responseHeaders,[{name:'Content-Type',value:'text/event-stream'}]);
      pending();
    }
    if(m.method==='Target.setAutoAttach'&&!m.sessionId){
      queueMicrotask(()=>this.emit('message',{data:JSON.stringify({method:'Target.attachedToTarget',params:{sessionId:'page-session',targetInfo:{type:'page',targetId:'page-target'}}})}));
    }
  }
}
global.WebSocket=Socket;
require('node:child_process').spawn=(_exe,args)=>{
  spawnedDir=args.find(x=>x.startsWith('--user-data-dir=')).slice('--user-data-dir='.length);
  fs.mkdirSync(path.join(spawnedDir,'Default','Cache'),{recursive:true});
  fs.writeFileSync(path.join(spawnedDir,'Default','Cache','lock'),'lingering');
  fs.writeFileSync(path.join(spawnedDir,'DevToolsActivePort'),'1\n/devtools/browser/fixture');
  const child=new events.EventEmitter();
  child.exitCode=null;
  child.kill=()=>{child.exitCode=0;queueMicrotask(()=>child.emit('exit',0));return true;};
  return child;
};
const pause=params=>new Promise(resolve=>{
  pending=resolve;
  socket.emit('message',{data:JSON.stringify({method:'Fetch.requestPaused',sessionId:'page-session',params})});
});
const page={setContent:async()=>{},title:async()=>'Fleet readiness',evaluate:async()=>'site-agents',
  goto:async url=>{
    assert.equal(url,production.origin);
    await pause({requestId:'prod',request:{url,headers:{}}});
    await pause({requestId:'preview',request:{url:'http://127.0.0.1:4173',headers:{}}});
    await pause({requestId:'prod',request:{url},responseStatusCode:200,responseHeaders:[
      {name:'Content-Type',value:'text/event-stream'},
      {name:'Set-Cookie',value:'CF_Authorization=invented-cookie; HttpOnly'},
    ]});
  }
};
const context={pages:()=>[page],routeWebSocket:async()=>{},close:async()=>{}};
module.exports={chromium:{connectOverCDP:async()=>({contexts:()=>[context]})}};
`), 0600); err != nil {
		t.Fatal(err)
	}

	t.Run("retries-and-cleans-profile-dir", func(t *testing.T) {
		cfg := M{"module": module, "executable": module, "previewOrigins": []any{"http://127.0.0.1:4173"}, "timeoutMs": 5000, "marker": marker, "failRm": false}
		c := browserCommand(cfg, d, true)
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("browser readiness check failed: %v: %s", err, out)
		}
		raw, err := os.ReadFile(marker)
		if err != nil {
			t.Fatalf("expected removed-dir marker: %v", err)
		}
		removedDir := strings.TrimSpace(string(raw))
		if removedDir == "" {
			t.Fatal("empty removedDir recorded")
		}
		if _, err := os.Stat(removedDir); !os.IsNotExist(err) {
			t.Fatalf("profileDir %q still exists or unexpected stat error: %v", removedDir, err)
		}
	})

	t.Run("attaches-production-credentials-and-strips-the-access-cookie", func(t *testing.T) {
		cfg := M{"module": module, "executable": module, "previewOrigins": []any{"http://127.0.0.1:4173"}, "timeoutMs": 5000, "marker": marker,
			"production": M{"origin": "https://fleet.ufobe.com"}}
		c := browserCommand(cfg, d, true)
		pipe, err := browserPipe(M{"clientId": "invented-id", "clientSecret": "invented-secret"})
		if err != nil {
			t.Fatal(err)
		}
		defer pipe.Close()
		c.ExtraFiles = []*os.File{pipe}
		if err := c.Run(); err != nil {
			t.Fatal("production readiness fixture failed")
		}
	})

	t.Run("propagates-persistent-rm-failure", func(t *testing.T) {
		cfg := M{"module": module, "executable": module, "previewOrigins": []any{"http://127.0.0.1:4173"}, "timeoutMs": 5000, "marker": marker, "failRm": true}
		c := browserCommand(cfg, d, true)
		if out, err := c.CombinedOutput(); err == nil {
			t.Fatalf("expected readiness check to fail when rm rejects: %s", out)
		}
	})
}

// Requirements: real trusted input and Chromium preview capture through scoped MCP.
// Risks: synthetic input, scaled rather than reflowed layout, incorrect PNGs and export of authenticated pixels.
func TestProjectBrowserTrustedInput(t *testing.T) {
	const cookieValue = "invented-project-browser-cookie"
	const accessSecret = "invented-access-secret"
	module, executable := os.Getenv("FLEET_PLAYWRIGHT_MODULE"), os.Getenv("FLEET_BROWSER_EXECUTABLE")
	if module == "" && executable == "" {
		t.Skip("requires the project-browser Playwright module and Chromium executable")
	}
	if !isFile(module) || !isFile(executable) {
		t.Fatal("incomplete project-browser runtime")
	}
	var deniedRequests atomic.Int32
	denied := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		deniedRequests.Add(1)
	}))
	defer denied.Close()
	var authenticated atomic.Bool
	production := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("CF-Access-Client-Id") == "invented-access-id" &&
			r.Header.Get("CF-Access-Client-Secret") == accessSecret {
			authenticated.Store(true)
		}
		w.Header().Set("Content-Type", "text/html")
		_, _ = io.WriteString(w, "<title>Authenticated fixture</title><body>"+accessSecret+"</body>")
	}))
	defer production.Close()
	preview := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/redirect" {
			http.Redirect(w, r, denied.URL, http.StatusFound)
			return
		}
		w.Header().Set("Content-Type", "text/html")
		if r.URL.Path == "/with-production" {
			_, _ = io.WriteString(w, `<body><iframe src="`+production.URL+`"></iframe></body>`)
			return
		}
		http.SetCookie(w, &http.Cookie{Name: "fixture", Value: cookieValue, Path: "/"})
		_, _ = io.WriteString(w, `<!doctype html>
<title>Trusted input fixture</title>
<style>
  button { display: block; width: 140px; height: 60px; user-select: none; }
  #swatch { width: 200px; height: 40px; background: #fff; }
  @media (max-width: 500px) { #swatch { width: 100px; } }
  @media (prefers-color-scheme: dark) { #swatch { background: #000; } }
</style>
<main><button id="a">A</button><button id="b">B</button></main>
<div id="swatch"></div>
<output>`+cookieValue+`</output>
<script>
  const list = document.querySelector('main');
  const a = document.querySelector('#a');
  const b = document.querySelector('#b');
  const state = {down: false, captured: false, moves: 0, up: false, keys: [], cancelled: false};
  let grabbed = false;
  let moved = false;
  a.addEventListener('keydown', e => {
    state.keys.push([e.key, e.isTrusted, e.shiftKey]);
    e.preventDefault();
    if (e.key === ' ') grabbed = true;
    if (grabbed && e.key === 'ArrowDown') moved = true;
    if (e.key === 'Escape') {
      state.cancelled = grabbed && moved;
      grabbed = moved = false;
    }
    if (e.key === 'Enter' && grabbed && moved) list.append(a);
  });
  a.addEventListener('pointerdown', e => {
    state.down = e.isTrusted;
    a.setPointerCapture(e.pointerId);
  });
  a.addEventListener('gotpointercapture', e => { state.captured = e.isTrusted; });
  a.addEventListener('pointermove', e => {
    if (a.hasPointerCapture(e.pointerId) && e.isTrusted) state.moves++;
  });
  a.addEventListener('pointerup', e => {
    state.up = e.isTrusted && a.hasPointerCapture(e.pointerId);
    if (state.up && document.elementFromPoint(e.clientX, e.clientY) === b) list.append(a);
  });
</script>`)
	}))
	defer preview.Close()

	root := t.TempDir()
	captureDir := browserCaptureDir(root, "fixture")
	noErr(t, os.MkdirAll(captureDir, 0o700))
	// Use a second loopback origin and invented credentials to exercise managed Access.
	cfg := M{"module": module, "executable": executable, "previewOrigins": []string{preview.URL}, "timeoutMs": 5000,
		"captureDir": captureDir, "production": M{"origin": production.URL}}
	t.Setenv("TMPDIR", runTempDir(root, "fixture"))
	access, err := browserPipe(M{"clientId": "invented-access-id", "clientSecret": accessSecret})
	noErr(t, err)
	defer access.Close()
	c := browserCommand(cfg, root, false)
	c.ExtraFiles = []*os.File{access}
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdin, err := c.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := c.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	timer := time.AfterFunc(45*time.Second, func() { _ = syscall.Kill(-c.Process.Pid, syscall.SIGKILL) })
	defer func() {
		defer timer.Stop()
		_ = stdin.Close()
		if err := c.Wait(); err != nil {
			t.Errorf("project-browser process: %v", err)
		}
	}()

	encoder, decoder := json.NewEncoder(stdin), json.NewDecoder(stdout)
	requestID := 0
	request := func(method string, params M) (M, bool) {
		t.Helper()
		requestID++
		if err := encoder.Encode(M{"jsonrpc": "2.0", "id": requestID, "method": method, "params": params}); err != nil {
			t.Fatal(err)
		}
		var reply struct {
			ID     int
			Result M
			Error  *struct{ Message string }
		}
		if err := decoder.Decode(&reply); err != nil {
			t.Fatalf("%s: %v", method, err)
		}
		if reply.ID != requestID {
			t.Fatalf("unexpected response ID %d to request %d", reply.ID, requestID)
		}
		return reply.Result, reply.Error != nil
	}
	call := func(name string, args M, wantError bool) M {
		t.Helper()
		reply, failed := request("tools/call", M{"name": name, "arguments": args})
		if failed != wantError {
			t.Fatalf("%s: failed = %v, want %v", name, failed, wantError)
		}
		if rendered := enc(reply); strings.Contains(rendered, cookieValue) || strings.Contains(rendered, accessSecret) {
			t.Fatalf("%s: fixture credential leaked in MCP reply", name)
		}
		return reply
	}
	listing, failed := request("tools/list", nil)
	if failed {
		t.Fatal("tools/list failed")
	}
	names := map[string]bool{}
	for _, tool := range listing["tools"].([]any) {
		names[tool.(map[string]any)["name"].(string)] = true
	}
	if !names["press"] || !names["drag"] || !names["emulate"] || !names["screenshot"] {
		t.Fatalf("browser tools missing: %v", names)
	}
	dragArgs := M{"source": "#a", "target": "#b", "steps": 7}
	call("press", M{"key": "Escape"}, true)
	call("drag", dragArgs, true)
	call("navigate", M{"url": denied.URL}, true)
	call("navigate", M{"url": preview.URL + "/redirect"}, true)
	if got := deniedRequests.Load(); got != 0 {
		t.Fatalf("ungranted origin received %d requests", got)
	}
	call("navigate", M{"url": preview.URL}, false)
	call("press", M{"key": "Space", "selector": "#a"}, false)
	call("press", M{"key": "Shift+ArrowDown"}, false)
	call("press", M{"key": "Escape"}, false)
	call("press", M{"key": "Enter"}, false)
	decodeResult := func(reply M, result any) {
		t.Helper()
		content := reply["content"].([]any)[0].(map[string]any)["text"].(string)
		if err := json.Unmarshal([]byte(content), result); err != nil {
			t.Fatal(err)
		}
	}
	evaluate := func(expression string, result any) {
		t.Helper()
		decodeResult(call("evaluate", M{"expression": expression}, false), result)
	}
	var keyboard struct {
		Result struct {
			Keys      [][]any
			Cancelled bool
			Order     string
		}
	}
	evaluate("({keys: state.keys, cancelled: state.cancelled, order: list.textContent})", &keyboard)
	wantKeys := [][]any{{" ", true, false}, {"Shift", true, true}, {"ArrowDown", true, true}, {"Escape", true, false}, {"Enter", true, false}}
	if !reflect.DeepEqual(keyboard.Result.Keys, wantKeys) || !keyboard.Result.Cancelled || keyboard.Result.Order != "AB" {
		t.Fatalf("trusted keyboard cancellation failed: %+v", keyboard.Result)
	}

	call("drag", dragArgs, false)
	var pointer struct {
		Result struct {
			Down, Captured, Up bool
			Moves              int
			Order              string
		}
	}
	evaluate("({...state, order: list.textContent})", &pointer)
	if got := pointer.Result; !got.Down || !got.Captured || !got.Up || got.Moves != 7 || got.Order != "BA" {
		t.Fatalf("trusted pointer reorder failed: %+v", got)
	}
	call("drag", M{"source": "#b", "target": "button"}, true)
	call("drag", dragArgs, true) // The failed drag closed the scoped page.
	call("navigate", M{"url": preview.URL}, false)
	call("press", M{"key": "Space", "selector": "button"}, true)

	checkCapture := func(args M, width, height int) color.Color {
		t.Helper()
		var got struct {
			Path          string
			Width, Height int
		}
		decodeResult(call("screenshot", args, false), &got)
		parts := insideParts(runTempDir(root, "fixture"), pathParts(got.Path))
		if !strings.HasPrefix(got.Path, captureDir+string(filepath.Separator)) || len(parts) == 0 {
			t.Fatalf("screenshot outside capture root: %q", got.Path)
		}
		f, err := openInside(runTempDir(root, "fixture"), parts)
		noErr(t, err)
		defer f.Close()
		info, err := f.Stat()
		noErr(t, err)
		if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || info.Sys().(*syscall.Stat_t).Nlink != 1 {
			t.Fatalf("screenshot is not a private regular file with one link: %v", info.Mode())
		}
		img, err := png.Decode(f)
		noErr(t, err)
		if got.Width != width || got.Height != height || img.Bounds().Dx() != width || img.Bounds().Dy() != height {
			t.Fatalf("screenshot dimensions = %dx%d, PNG bounds = %v, want %dx%d", got.Width, got.Height, img.Bounds(), width, height)
		}
		return img.At(width/2, height/2)
	}
	call("emulate", M{"viewport": "desktop", "colorScheme": "dark"}, false)
	call("navigate", M{"url": preview.URL}, false)
	for _, size := range []struct {
		name                  string
		width, height, swatch int
	}{{"desktop", 1440, 900, 200}, {"mobile", 390, 844, 100}} {
		for _, scheme := range []string{"dark", "light"} {
			call("emulate", M{"viewport": size.name, "colorScheme": scheme}, false)
			var layout struct {
				Result struct {
					Width, Height, Scale, Swatch float64
					Dark, Reduced                bool
				}
			}
			evaluate(`({width:innerWidth,height:innerHeight,scale:devicePixelRatio,
				swatch:document.querySelector('#swatch').getBoundingClientRect().width,
				dark:matchMedia('(prefers-color-scheme: dark)').matches,
				reduced:matchMedia('(prefers-reduced-motion: reduce)').matches})`, &layout)
			if got := layout.Result; got.Width != float64(size.width) || got.Height != float64(size.height) ||
				got.Scale != 1 || got.Swatch != float64(size.swatch) || got.Dark != (scheme == "dark") || !got.Reduced {
				t.Fatalf("%s %s layout: %+v", size.name, scheme, got)
			}
			checkCapture(M{}, size.width, size.height)
			r, g, b, a := checkCapture(M{"selector": "#swatch"}, size.swatch, 40).RGBA()
			var shade uint32 = 0
			if scheme == "light" {
				shade = 0xffff
			}
			if r != shade || g != shade || b != shade || a != 0xffff {
				t.Fatalf("%s %s swatch PNG: rgba16 %x,%x,%x,%x", size.name, scheme, r, g, b, a)
			}
		}
	}
	call("screenshot", M{"selector": "button"}, true)
	call("screenshot", M{}, true) // The failed capture closed the scoped page.
	call("navigate", M{"url": preview.URL}, false)
	call("screenshot", M{"path": filepath.Join(root, "override.png")}, true)
	call("navigate", M{"url": preview.URL + "/with-production"}, false)
	call("evaluate", M{"expression": "document.readyState === 'complete' || new Promise(resolve => addEventListener('load', () => resolve(true), {once:true}))"}, false)
	if !authenticated.Load() {
		t.Fatal("production fixture did not receive its authentication headers")
	}
	call("screenshot", M{}, true) // A preview that embedded authenticated content cannot export pixels.
	call("navigate", M{"url": production.URL}, false)
	call("screenshot", M{}, true)
	call("navigate", M{"url": preview.URL}, false)
	call("screenshot", M{"selector": "#missing"}, true) // Deadline leaves no PNG.
	call("close", M{}, false)
	entries, err := os.ReadDir(runTempDir(root, "fixture"))
	noErr(t, err)
	if len(entries) != 1 || entries[0].Name() != filepath.Base(captureDir) {
		t.Fatalf("browser profile remains after close: %v", entries)
	}
	captures, err := os.ReadDir(captureDir)
	noErr(t, err)
	if len(captures) != 8 {
		t.Fatalf("expected 8 successful captures and no files from failures, got %d", len(captures))
	}
	cleanupRun(root, "fixture")
	if _, err := os.Stat(captureDir); !os.IsNotExist(err) {
		t.Fatalf("execution cleanup left the capture directory: %v", err)
	}
}

// A managed production-only execution requires no local binding file and ignores
// obsolete Access bindings. The credentials travel through a private descriptor.
func TestProjectBrowserManagedProduction(t *testing.T) {
	d := t.TempDir()
	module := filepath.Join(d, "playwright.cjs")
	if err := os.WriteFile(module, []byte(`
const assert=require('node:assert/strict');
const secret='invented-production-secret';
assert.equal(process.argv.some(arg=>arg.includes(secret)),false);
assert.equal(Object.values(process.env).some(value=>value.includes(secret)),false);
module.exports={chromium:{}};
`), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FLEET_BROWSER_BINDINGS", "")
	t.Setenv("FLEET_PLAYWRIGHT_MODULE", module)
	t.Setenv("FLEET_BROWSER_EXECUTABLE", module)
	cfg, err := browserConfig(M{"projectId": "fleet"}, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := browserConfig(M{"projectId": "another"}, true); err == nil {
		t.Fatal("managed production granted to another project")
	}
	pipe, err := browserPipe(M{"clientId": "invented-id", "clientSecret": "invented-production-secret"})
	if err != nil {
		t.Fatal(err)
	}
	defer pipe.Close()
	cmd := browserCommand(cfg, d, false)
	cmd.ExtraFiles = []*os.File{pipe}
	cmd.Stdin = strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26"}}` + "\n")
	output, err := cmd.Output()
	if err != nil || !strings.Contains(string(output), "https://fleet.ufobe.com") || strings.Contains(string(output), "invented-") {
		t.Fatal("managed browser did not advertise its scoped origin without credential output")
	}
}

// One in-flight refresh preserves current evidence. A first or overdue probe
// leaves eligibility and the unknown problem to the coordinator.
func TestProjectBrowserProductionObservationFreshness(t *testing.T) {
	for _, tc := range []struct {
		name         string
		last         time.Time
		ready        bool
		detail       string
		wantObserved bool
	}{
		{name: "first"},
		{name: "refreshing confirmed identity", last: time.Now().Add(-browserReadinessInterval - time.Second), ready: true, wantObserved: true},
		{name: "ungranted", last: time.Now(), wantObserved: true},
		{name: "unready", last: time.Now(), detail: "test probe failed", wantObserved: true},
		{name: "overdue", last: time.Now().Add(-browserReadinessInterval - outboxClient.Timeout - probeWait - time.Second), ready: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := &executor{browserHealth: productionBrowserHealth{busy: true, last: tc.last, ready: tc.ready, detail: tc.detail}}
			ready, problems := e.productionBrowserStatus()
			if (ready != nil) != tc.wantObserved || (ready != nil && *ready != tc.ready) {
				t.Fatal("unexpected production observation")
			}
			if got := len(problems) > 0; got != (tc.detail != "") {
				t.Fatalf("unexpected reported problems: %v", problems)
			}
		})
	}
}
