package main

// Requirement: browser access is project-granted through normal MCP, never arbitrary origins.
// Risk: a missing binding or cross-origin navigation launches an unscoped host browser.
import (
	"encoding/json"
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
	os.WriteFile(module, []byte(`module.exports={chromium:{launch(){throw Error('must not launch')}}}`), 0600)
	os.WriteFile(binding, []byte(`{"":{"previewOrigins":["http://127.0.0.1:4173"]},"project":{"previewOrigins":["http://127.0.0.1:4173"]}}`), 0600)
	t.Setenv("FLEET_BROWSER_BINDINGS", binding)
	t.Setenv("FLEET_PLAYWRIGHT_MODULE", module)
	t.Setenv("FLEET_BROWSER_EXECUTABLE", module)
	// Normal signed StartPayload carries identity separately; normalized settings have no id.
	payload := M{"projectId": "project", "project": M{"execution": M{"tools": []any{"browser"}}}}
	cfg, err := browserConfig(payload)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := browserConfig(M{"projectId": "other", "project": M{"id": "project"}}); err == nil {
		t.Fatal("foreign authoritative identity accepted")
	}
	if _, err := browserConfig(M{"project": M{"id": "project"}}); err == nil {
		t.Fatal("settings identity granted browser without authoritative projectId")
	}
	for _, id := range []any{nil, "", false, 42, []any{"project"}, M{"id": "project"}} {
		if _, err := browserConfig(M{"projectId": id, "project": M{"id": "project"}}); err == nil {
			t.Fatalf("invalid authoritative identity accepted: %T", id)
		}
	}
	c := browserCommand(cfg, d, false)
	c.Stdin = strings.NewReader("{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"tools/call\",\"params\":{\"name\":\"navigate\",\"arguments\":{\"url\":\"http://127.0.0.1:9999/admin\"}}}\n")
	out, err := c.Output()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), `"error"`) || strings.Contains(string(out), "must not launch") {
		t.Fatalf("unsafe result: %s", out)
	}
	os.WriteFile(binding, []byte(`{"project":{"previewOrigins":["http://127.0.0.1:4173/admin"]}}`), 0600)
	if _, err := browserConfig(payload); err == nil {
		t.Fatal("path accepted as origin")
	}
	os.WriteFile(binding, []byte(`{"project":{"production":{"origin":"https://other.invalid","accessFile":"/invented"}}}`), 0600)
	if _, err := browserConfig(payload); err == nil {
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
const {marker,failRm}=JSON.parse(process.argv[1]);
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
  constructor(){this.listeners={};queueMicrotask(()=>this.emit('open',{}));}
  addEventListener(name,fn){(this.listeners[name]??=[]).push(fn);}
  emit(name,event){for(const fn of this.listeners[name]??[])fn(event);}
  close(){}
  send(raw){
    const m=JSON.parse(raw);
    queueMicrotask(()=>this.emit('message',{data:JSON.stringify({id:m.id,result:{}})}));
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
const page={setContent:async()=>{},title:async()=>'Fleet readiness'};
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

	t.Run("propagates-persistent-rm-failure", func(t *testing.T) {
		cfg := M{"module": module, "executable": module, "previewOrigins": []any{"http://127.0.0.1:4173"}, "timeoutMs": 5000, "marker": marker, "failRm": true}
		c := browserCommand(cfg, d, true)
		if out, err := c.CombinedOutput(); err == nil {
			t.Fatalf("expected readiness check to fail when rm rejects: %s", out)
		}
	})
}

// Requirement: scoped MCP actions deliver real input to browser controls.
// Risk: synthetic events cannot capture the pointer or drive trusted keyboard gestures.
func TestProjectBrowserTrustedInput(t *testing.T) {
	const cookieValue = "invented-project-browser-cookie"
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
	preview := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/redirect" {
			http.Redirect(w, r, denied.URL, http.StatusFound)
			return
		}
		w.Header().Set("Content-Type", "text/html")
		http.SetCookie(w, &http.Cookie{Name: "fixture", Value: cookieValue, Path: "/"})
		_, _ = io.WriteString(w, `<!doctype html>
<title>Trusted input fixture</title>
<style>button { display: block; width: 140px; height: 60px; user-select: none; }</style>
<main><button id="a">A</button><button id="b">B</button></main>
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

	cfg := M{"module": module, "executable": executable, "previewOrigins": []string{preview.URL}, "timeoutMs": 5000}
	c := browserCommand(cfg, t.TempDir(), false)
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
		if strings.Contains(enc(reply), cookieValue) {
			t.Fatalf("%s: fixture cookie leaked in MCP reply", name)
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
	if !names["press"] || !names["drag"] {
		t.Fatalf("input tools missing: %v", names)
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
	evaluate := func(expression string, result any) {
		t.Helper()
		reply := call("evaluate", M{"expression": expression}, false)
		content := reply["content"].([]any)[0].(map[string]any)["text"].(string)
		if err := json.Unmarshal([]byte(content), result); err != nil {
			t.Fatal(err)
		}
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
	call("close", M{}, false)
}
