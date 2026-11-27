package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"fleet/contract"
)

const (
	damBrowserOrigin = "https://revilio.com"
	damBrowserToken  = `invented-dam-"token\fixture`
)

func damBrowserGrant(credential string) M {
	return M{
		"origin": damBrowserOrigin, "accessFile": credential,
		"headers": M{"X-DAM-Verification-Token": "DAM_VERIFICATION_TOKEN", "X-DAM-Aux": "DAM_AUX_TOKEN"},
		"readinessProbe": M{"path": "/api/ready", "method": "GET", "status": 200,
			"identityField": "identity", "expectedIdentity": "dam-agents"},
	}
}

// Requirement: a generic production grant needs an exact HTTPS origin, a complete probe,
// and a private credential reference. Risk: partial or mis-scoped grants fall back to access.
func TestProjectBrowserProductionGrant(t *testing.T) {
	d := t.TempDir()
	credential := filepath.Join(d, "dam.env")
	noErr(t, os.WriteFile(credential, []byte("DAM_VERIFICATION_TOKEN=invented-value\nDAM_AUX_TOKEN=invented-other\n"), 0o600))
	binding := filepath.Join(d, "browser-bindings.json")
	t.Setenv("FLEET_BROWSER_BINDINGS", binding)
	check := func(raw M) error {
		t.Helper()
		noErr(t, os.WriteFile(binding, []byte(enc(M{"dam": M{"production": raw}})), 0o600))
		_, local, err := localBrowserAccess("dam")
		if !local {
			t.Fatal("configured production treated as absent")
		}
		return err
	}
	linked := filepath.Join(d, "linked.env")
	noErr(t, os.Symlink(credential, linked))
	public := filepath.Join(d, "public.env")
	noErr(t, os.WriteFile(public, nil, 0o644))
	noErr(t, os.Chmod(public, 0o644))
	readOnly := filepath.Join(d, "readonly.env")
	noErr(t, os.WriteFile(readOnly, nil, 0o400))
	if err := check(damBrowserGrant(credential)); err != nil {
		t.Fatal(err)
	}
	for _, probe := range []M{
		{"path": "/ready", "method": "HEAD", "status": 204},
		{"path": "/ready", "method": "GET", "identityField": "service", "expectedIdentity": "dam-agents"},
	} {
		grant := damBrowserGrant(credential)
		grant["readinessProbe"] = probe
		noErr(t, check(grant))
	}
	for _, change := range []struct {
		name, key string
		value     any
	}{
		{"insecure origin", "origin", "http://revilio.com"},
		{"Fleet origin", "origin", contract.ProductionBrowserOrigin},
		{"path on origin", "origin", damBrowserOrigin + "/"},
		{"origin query", "origin", damBrowserOrigin + "?"},
		{"origin fragment", "origin", damBrowserOrigin + "#"},
		{"user info", "origin", "https://user@revilio.com"},
		{"noncanonical port", "origin", damBrowserOrigin + ":443"},
		{"oversized port", "origin", damBrowserOrigin + ":65536"},
		{"relative credentials", "accessFile", "dam.env"},
		{"public credentials", "accessFile", public},
		{"read-only generic credentials", "accessFile", readOnly},
		{"symlink credentials", "accessFile", linked},
		{"directory credentials", "accessFile", d},
		{"missing credentials", "accessFile", filepath.Join(d, "missing.env")},
		{"missing headers", "headers", nil},
		{"duplicate headers", "headers", M{"X-DAM-Token": "TOKEN", "x-dam-token": "OTHER"}},
		{"invalid header name", "headers", M{"X-DAM:Token": "TOKEN"}},
		{"invalid env name", "headers", M{"X-DAM-Token": "TOKEN=value"}},
		{"cookie header", "headers", M{"Cookie": "TOKEN"}},
		{"host header", "headers", M{"Host": "TOKEN"}},
		{"missing probe", "readinessProbe", nil},
		{"network path", "readinessProbe", M{"path": "//foreign.invalid", "method": "GET", "status": 200}},
		{"backslash path", "readinessProbe", M{"path": `/\foreign.invalid`, "method": "GET", "status": 200}},
		{"forbidden method", "readinessProbe", M{"path": "/ready", "method": "TRACE", "status": 200}},
		{"invalid status", "readinessProbe", M{"path": "/ready", "method": "GET", "status": 700}},
		{"missing identity expectation", "readinessProbe", M{"path": "/ready", "method": "GET", "status": 200, "identityField": "identity"}},
	} {
		t.Run(change.name, func(t *testing.T) {
			grant := damBrowserGrant(credential)
			grant[change.key] = change.value
			if err := check(grant); err == nil {
				t.Fatal("invalid grant accepted")
			}
		})
	}
}

// Requirement: production readiness and the MCP browser share the same origin fence and
// redaction. Risk: redirect hops or reflected secrets escape the adapter. The fixture drives
// the adapter's CDP request handler and runs the real in-page probe with simulated responses.
func TestProjectBrowserProductionAdapter(t *testing.T) {
	d := t.TempDir()
	t.Setenv("TMPDIR", d)
	// CI's shared tool cache can be group-writable. Provide the private launcher
	// that production preparation requires while using the installed test Node.
	node, err := exec.LookPath("node")
	noErr(t, err)
	quotedNode := "'" + strings.ReplaceAll(node, "'", "'\\''") + "'"
	noErr(t, os.WriteFile(filepath.Join(d, "node"), []byte("#!/bin/sh\nexec "+quotedNode+" \"$@\"\n"), 0o700))
	t.Setenv("PATH", d+string(os.PathListSeparator)+os.Getenv("PATH"))
	binding := filepath.Join(d, "browser-bindings.json")
	credential := filepath.Join(d, "authority.env")
	module := filepath.Join(d, "fixture.cjs")
	t.Setenv("FLEET_BROWSER_BINDINGS", binding)
	t.Setenv("FLEET_BROWSER_EXECUTABLE", module)
	t.Setenv("FLEET_PLAYWRIGHT_MODULE", module)
	noErr(t, os.WriteFile(credential, []byte("export DAM_VERIFICATION_TOKEN='"+damBrowserToken+"'\n"+
		"DAM_AUX_TOKEN=invented-dam-secondary\nCF_ACCESS_CLIENT_ID=invented-fleet-id\nCF_ACCESS_CLIENT_SECRET=invented-fleet-secret\n"), 0o600))
	noErr(t, os.WriteFile(module, []byte(browserProductionFixture), 0o700))
	preview := "http://127.0.0.1:4173"
	for _, variant := range []struct {
		project string
		grant   M
	}{
		{"fleet", M{"origin": "https://fleet.ufobe.com", "accessFile": credential}},
		{"dam", damBrowserGrant(credential)},
	} {
		t.Run(variant.project, func(t *testing.T) {
			noErr(t, os.WriteFile(binding, []byte(enc(M{variant.project: M{
				"previewOrigins": []string{preview}, "production": variant.grant,
			}})), 0o600))
			if !slices.Contains(executorStores(), credential) {
				t.Fatal("browser credential reference is missing from the upload denylist")
			}
			payload := M{"projectId": variant.project}
			access := contract.BrowserAccessReply{ClientId: "invented-fleet-id", ClientSecret: "invented-fleet-secret"}
			var cfg M
			if variant.project == "dam" {
				host := &executor{} // The host-local grant requires no coordinator connection.
				var authority *browserAuthority
				var err error
				cfg, authority, err = host.prepareBrowser(payload, d)
				noErr(t, err)
				if authority == nil {
					t.Fatal("host-local grant did not prepare production")
				}
				access = authority.Access
			} else {
				fleet, err := newBrowserAuthority(payload, access)
				noErr(t, err)
				cfg = fleet.Config
			}
			if strings.Contains(enc(cfg), credential) || strings.Contains(enc(cfg), "invented-") {
				t.Fatal("credential reference or value exposed in browser config")
			}
			cfg["timeoutMs"] = 1000
			command := func(check bool) *exec.Cmd {
				t.Helper()
				pipe, err := browserPipe(access)
				noErr(t, err)
				t.Cleanup(func() { pipe.Close() })
				c := browserCommand(cfg, d, check)
				c.ExtraFiles = []*os.File{pipe}
				return c
			}
			c := command(true)
			if out, err := c.CombinedOutput(); err != nil || len(out) != 0 {
				t.Fatalf("readiness = %v, output length = %d", err, len(out))
			}
			if variant.project != "dam" {
				return
			}

			c = command(false)
			input := []string{
				enc(M{"id": 1, "method": "tools/call", "params": M{"name": "navigate", "arguments": M{"url": damBrowserOrigin}}}),
				enc(M{"id": 2, "method": "tools/call", "params": M{"name": "navigate", "arguments": M{"url": "https://revilio.com.evil.invalid"}}}),
				enc(M{"id": 3, "method": "tools/call", "params": M{"name": "navigate", "arguments": M{"url": damBrowserOrigin + "/protocol-loss"}}}),
			}
			c.Stdin = strings.NewReader(strings.Join(input, "\n"))
			out, err := c.CombinedOutput()
			noErr(t, err)
			lines := strings.Split(strings.TrimSpace(string(out)), "\n")
			if len(lines) != len(input) {
				t.Fatalf("expected %d MCP replies, got %d", len(input), len(lines))
			}
			reply, ok := decM(lines[0])["result"].(M)
			if !ok {
				t.Fatal("production navigation did not return a result")
			}
			content, ok := reply["content"].([]any)
			if !ok || len(content) != 1 {
				t.Fatal("production navigation did not return text")
			}
			result, _ := content[0].(M)
			text, _ := str(result["text"])
			decoded := decM(text)
			if decoded["title"] != "[redacted]" || decoded["text"] != "[redacted] [redacted] [redacted]" ||
				decM(lines[1])["error"] == nil || decM(lines[2])["error"] == nil || strings.Contains(string(out), "invented-") {
				t.Fatal("origin denial, fail-closed transport loss, or MCP secret redaction failed")
			}

			cfg["badIdentity"] = true
			c = command(true)
			if out, err := c.CombinedOutput(); err == nil || len(out) != 0 {
				t.Fatalf("mismatched readiness identity = %v, output length = %d", err, len(out))
			}
			delete(cfg, "badIdentity")
			cfg["badStatus"] = true
			c = command(true)
			if out, err := c.CombinedOutput(); err == nil || len(out) != 0 {
				t.Fatalf("mismatched readiness status = %v, output length = %d", err, len(out))
			}

			// Credential rotation is verified again on each preparation.
			rotated := filepath.Join(d, "rotated.env")
			noErr(t, os.Rename(credential, rotated))
			noErr(t, os.Symlink(rotated, credential))
			if _, local, err := localBrowserAccess("dam"); !local || err == nil {
				t.Fatal("rotated symlink was accepted")
			}
			noErr(t, os.Remove(credential))
			noErr(t, os.WriteFile(credential, []byte("DAM_AUX_TOKEN=invented-dam-secondary\n"), 0o600))
			if _, local, err := localBrowserAccess("dam"); !local || err == nil {
				t.Fatal("missing credential was accepted")
			}

			// With no local binding, the executor asks for the bound project's managed authority.
			t.Setenv("FLEET_BROWSER_BINDINGS", "")
			coordinator := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request contract.BrowserAccessRequest
				if r.URL.Path != contract.RouteBrowserAccess.Path ||
					json.NewDecoder(r.Body).Decode(&request) != nil || request.ProjectId != "dam" {
					t.Error("browser access did not request the bound project")
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				_, _ = w.Write([]byte(enc(access)))
			}))
			defer coordinator.Close()
			host := &executor{url: coordinator.URL}
			managedCfg, managed, err := host.prepareBrowser(payload, d)
			if err != nil || managed == nil || enc(managedCfg["production"]) != enc(M{"origin": damBrowserOrigin}) {
				t.Fatalf("managed DAM production preparation failed: %v", err)
			}
		})
	}
	matches, err := filepath.Glob(filepath.Join(d, "fleet-browser-*"))
	noErr(t, err)
	if len(matches) > 0 {
		t.Fatal("browser profile survived a closed or failed operation")
	}
}

const browserProductionFixture = `
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const {EventEmitter} = require('node:events');
const cfg = JSON.parse(process.argv[1]);
const origin = cfg.production.origin;
assert.equal(process.argv.some(arg => arg.includes('invented-')), false);
assert.equal(Object.values(process.env).some(value => value.includes('invented-')), false);
const legacy = origin === 'https://fleet.ufobe.com';
const token = 'invented-dam-"token\\fixture';
const auxToken = 'invented-dam-secondary';
const cookie = 'invented-browser-cookie';
const expected = legacy
  ? {'cf-access-client-id':'invented-fleet-id','cf-access-client-secret':'invented-fleet-secret'}
  : {'x-dam-verification-token':token, 'x-dam-aux':auxToken};
let socket;
let requestID = 0;
const intercepted = new Map();
class Socket extends EventEmitter {
  static OPEN = 1;
  constructor() {
    super();
    socket = this;
    this.readyState = Socket.OPEN;
    queueMicrotask(() => this.emit('open'));
  }
  addEventListener(name, listener) { this.on(name, listener); }
  close() { assert.notEqual(this.readyState, Socket.OPEN); }
  disconnect() { this.readyState = 3; this.emit('close'); }
  deliver(message) { this.emit('message', {data:JSON.stringify(message)}); }
  send(raw) {
    const {id, sessionId, method, params} = JSON.parse(raw);
    queueMicrotask(() => {
      this.deliver({id, result:{}});
      if (method === 'Target.setAutoAttach' && !sessionId) this.deliver({
        method:'Target.attachedToTarget', params:{sessionId:'fixture',targetInfo:{type:'page',targetId:'page'}}
      });
      if (['Fetch.continueRequest','Fetch.failRequest','Fetch.continueResponse'].includes(method))
        intercepted.get(params.requestId)({method, params});
    });
  }
}
global.WebSocket = Socket;
async function request(url, headers={}, redirectedRequestId) {
  const requestId = String(++requestID);
  const response = new Promise(resolve => intercepted.set(requestId, resolve));
  socket.deliver({method:'Fetch.requestPaused',sessionId:'fixture',params:{
    requestId, redirectedRequestId, request:{url, headers}
  }});
  const {method,params} = await response;
  intercepted.delete(requestId);
  const values = Object.fromEntries((params.headers || []).map(({name,value})=>[name.toLowerCase(),value]));
  return {requestId,method,values};
}
async function authenticated(url, headers) {
  const {requestId,method,values} = await request(url, headers);
  assert.equal(method, 'Fetch.continueRequest');
  assert.deepEqual(values, expected);
  return requestId;
}
require('node:child_process').spawn = (_exe,args) => {
  const directory = args.find(x=>x.startsWith('--user-data-dir=')).slice('--user-data-dir='.length);
  fs.writeFileSync(path.join(directory,'DevToolsActivePort'),'1\n/devtools/browser/fixture');
  const child = new EventEmitter();
  child.exitCode = null;
  child.kill = () => {
    child.exitCode = 0;
    queueMicrotask(() => {
      child.emit('exit',0);
      setImmediate(() => socket.disconnect());
    });
  };
  return child;
};
const page = {
  url: () => origin + '/',
  setContent: async () => {},
  title: async () => process.argv[2] === 'check' ? 'Fleet readiness' : token,
  locator: () => ({innerText:async()=>token+' '+auxToken+' '+cookie}),
  goto: async url => {
    if (url === origin+'/protocol-loss') {
      socket.disconnect();
      return;
    }
    assert.equal(url, origin);
    const requestId = await authenticated(url, legacy
      ? {'cf-access-CLIENT-id':'stale'}
      : {'x-DAM-verification-token':'stale'});
    if (process.argv[2] === 'check') return;
    const denied = [origin.replace('https:', 'http:'), origin+':444', origin+'.evil.invalid', 'blob:'+origin+'/fixture'];
    for (const target of denied) {
      const hop = await request(target, expected, requestId);
      assert.equal(hop.method, 'Fetch.failRequest');
      assert.deepEqual(hop.values, {});
    }
    const preview = await request(cfg.previewOrigins[0], {
      'x-DAM-verification-token':token, 'X-DAM-Aux':auxToken, 'X-Reflected':token, Cookie:token,
      Accept:'text/html'
    }, requestId);
    assert.equal(preview.method,'Fetch.continueRequest');
    assert.deepEqual(preview.values, {accept:'text/html'});
    const response = new Promise(resolve => intercepted.set(requestId, resolve));
    socket.deliver({method:'Fetch.requestPaused',sessionId:'fixture',params:{
      requestId,request:{url},responseStatusCode:200,responseHeaders:[
        {name:'X-DAM-Verification-Token',value:token},
        {name:'Set-Cookie',value:'verification='+auxToken+'; Secure'},
        {name:'Set-Cookie',value:'session='+cookie+'; Secure'},
        {name:'Content-Type',value:'text/html'}
      ]
    }});
    const stripped = await response;
    intercepted.delete(requestId);
    assert.equal(stripped.method,'Fetch.continueResponse');
    assert.deepEqual(stripped.params.responseHeaders,[
      {name:'Set-Cookie',value:'session='+cookie+'; Secure'},{name:'Content-Type',value:'text/html'}
    ]);
  },
  evaluate: async (fn,args) => {
    global.location = {origin};
    global.fetch = async (url,options) => {
      assert.equal(url.href, origin+(legacy?'/api/ops/me':'/api/ready'));
      assert.equal(options.method, legacy?'POST':'GET');
      assert.equal(options.mode, legacy?undefined:'same-origin');
      assert.equal(options.redirect, legacy?undefined:'error');
      if (legacy) assert.deepEqual(options.body, '{}');
      await authenticated(url.href);
      const status = cfg.badStatus ? 201 : 200;
      return {url:url.href, status, ok:true,
        json:async()=>legacy?{email:'site-agents'}:{identity:cfg.badIdentity?'wrong':'dam-agents'}};
    };
    return fn(args);
  }
};
const context = {pages:()=>[page],cookies:async()=>[{value:cookie}],routeWebSocket:async()=>{},close:async()=>{}};
module.exports = {chromium:{connectOverCDP:async()=>({contexts:()=>[context]})}};
`
