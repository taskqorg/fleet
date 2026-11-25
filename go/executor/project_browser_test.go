package main

// Requirement: browser access is project-granted through normal MCP, never arbitrary origins.
// Risk: a missing binding or cross-origin navigation launches an unscoped host browser.
import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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
