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

// Requirement: production authority never follows a redirect or escapes the granted origin.
// Risk: Access headers or browser cookie values leak through requests or MCP results.
// Launch must also isolate the host credential store so Keychain cannot block CDP readiness.
func TestProjectBrowserAuthority(t *testing.T) {
	d := t.TempDir()
	module := filepath.Join(d, "mock.cjs")
	access := filepath.Join(d, "access.env")
	os.WriteFile(access, []byte("CF_ACCESS_CLIENT_ID=invented-id\nCF_ACCESS_CLIENT_SECRET=invented-secret\n"), 0600)
	os.WriteFile(module, []byte(`
const assert=require('node:assert/strict'),fs=require('node:fs'),path=require('node:path');
let socket,finish,closed=false,current='about:blank';
class Socket {
 constructor(){socket=this;this.listeners={};queueMicrotask(()=>this.emit('open',{}));}
 addEventListener(name,fn){(this.listeners[name]??=[]).push(fn);}
 emit(name,event){for(const fn of this.listeners[name]??[])fn(event);}
 message(message){queueMicrotask(()=>this.emit('message',{data:JSON.stringify(message)}));}
 close(){closed=true;}
 send(raw){const m=JSON.parse(raw);
 if(closed)throw Error('invented-secret invented-cookie');
 if(JSON.parse(process.argv[1]).omitResponse)return;this.message({id:m.id,result:{}});
 if(m.method==='Target.setAutoAttach'&&!m.sessionId)this.message({method:'Target.attachedToTarget',params:{sessionId:'page-session',targetInfo:{type:'page',targetId:'page-target'}}});
 if(m.method==='Fetch.continueRequest'){
 assert.equal(m.params.headers.find(h=>h.name==='CF-Access-Client-Secret').value,'invented-secret');
 this.message({sessionId:'page-session',method:'Fetch.requestPaused',params:{requestId:'redirect',request:{url:'https://other.invalid/redirect',headers:{}}}});
 }
 if(m.method==='Fetch.failRequest'){assert.equal(m.params.requestId,'redirect');finish();}
 }
}
global.WebSocket=Socket;
const page={url:()=>current,title:async()=>'invented-secret',locator:()=>({innerText:async()=>'invented-cookie'}),evaluate:()=>new Promise(()=>{}),goto:async url=>{current=url;await new Promise(resolve=>{finish=resolve;socket.message({sessionId:'page-session',method:'Fetch.requestPaused',params:{requestId:'sse',request:{url,headers:{'cf-access-client-secret':'untrusted-header',Accept:'text/event-stream'}}}});});}};
const events=require('node:events');
require('node:child_process').spawn=(_exe,args)=>{
assert.ok(args.includes('--password-store=basic'));assert.ok(args.includes('--use-mock-keychain'));
const dir=args.find(x=>x.startsWith('--user-data-dir=')).slice('--user-data-dir='.length);
fs.writeFileSync(path.join(dir,'DevToolsActivePort'),'1\n/devtools/browser/fixture');
const child=new events.EventEmitter();child.kill=()=>{queueMicrotask(()=>child.emit('exit',0));return true;};return child;
};
const context={pages:()=>[page],routeWebSocket:async()=>{},cookies:async()=>[{value:'invented-cookie'}],close:async()=>{socket.message({sessionId:'page-session',method:'Fetch.requestPaused',params:{requestId:'closing-sse',request:{url:current,headers:{Accept:'text/event-stream'}}}});throw Error('invented-secret invented-cookie');}};
module.exports={chromium:{connectOverCDP:async()=>({contexts:()=>[context]})}};
`), 0600)
	cfg := M{"module": module, "executable": module, "previewOrigins": []any{}, "production": M{"origin": "https://fleet.ufobe.com", "accessFile": access}}
	for _, scenario := range []string{"sse-close", "hanging-evaluate", "missing-response"} {
		t.Run(scenario, func(t *testing.T) {
			cfg["timeoutMs"] = 100
			cfg["omitResponse"] = scenario == "missing-response"
			c := browserCommand(cfg, d, false)
			requests := `{"id":1,"method":"tools/call","params":{"name":"navigate","arguments":{"url":"https://fleet.ufobe.com"}}}` + "\n"
			if scenario == "hanging-evaluate" {
				requests += `{"id":2,"method":"tools/call","params":{"name":"evaluate","arguments":{"expression":"new Promise(()=>{})"}}}` + "\n"
			}
			requests += `{"id":3,"method":"tools/call","params":{"name":"close"}}` + "\n"
			c.Stdin = strings.NewReader(requests)
			out, err := c.CombinedOutput()
			if err != nil {
				t.Fatalf("%v: %s", err, out)
			}
			if strings.Contains(string(out), "invented-secret") || strings.Contains(string(out), "invented-cookie") {
				t.Fatalf("authority exposed: %s", out)
			}
			if scenario == "sse-close" && !strings.Contains(string(out), "[redacted]") {
				t.Fatalf("missing safe result: %s", out)
			}
			if scenario != "sse-close" && !strings.Contains(string(out), `"error"`) {
				t.Fatalf("timeout not reported: %s", out)
			}
		})
	}
}
