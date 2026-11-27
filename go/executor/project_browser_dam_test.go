package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"fleet/contract"
)

// Exercise the required tools/list check used at native MCP startup without
// opening Chromium. The adapter must advertise every tool selected by the launch.
func TestProjectBrowserMCPInitialization(t *testing.T) {
	const dam = "https://revilio.com"
	const preview = "http://127.0.0.1:4173"
	dir := t.TempDir()
	module := filepath.Join(dir, "fixture.cjs")
	noErr(t, os.WriteFile(module, []byte("module.exports = {}"), 0600))
	binding := filepath.Join(dir, "binding.json")
	noErr(t, os.WriteFile(binding, []byte(enc(M{"dam": M{"previewOrigins": []string{preview}}})), 0600))
	t.Setenv("FLEET_BROWSER_EXECUTABLE", module)
	t.Setenv("FLEET_PLAYWRIGHT_MODULE", module)

	for _, tc := range []struct {
		name, origin string
		preview      bool
	}{
		{name: "restricted DAM", origin: dam},
		{name: "DAM with preview", origin: dam, preview: true},
		{name: "preview only", preview: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("FLEET_BROWSER_BINDINGS", "")
			if tc.preview {
				t.Setenv("FLEET_BROWSER_BINDINGS", binding)
			}
			cfg, err := browserConfig(M{"projectId": "dam"}, tc.origin)
			noErr(t, err)
			server := projectBrowserServer(cfg, tc.origin != "")
			selected := server["tools"].([]any)
			cmd := browserCommand(cfg, dir, false)
			if tc.origin != "" {
				pipe, err := browserPipe(M{"production": M{
					"origin":         dam,
					"headers":        []M{{"name": "X-DAM-Verification-Token", "value": "invented-test-token"}},
					"readinessProbe": M{"path": "/site/verification", "method": "GET", "status": 200},
				}})
				noErr(t, err)
				defer pipe.Close()
				cmd.ExtraFiles = []*os.File{pipe}
			}
			const startup = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05"}}
{"jsonrpc":"2.0","method":"notifications/initialized"}
{"jsonrpc":"2.0","id":2,"method":"tools/list"}`
			cmd.Stdin = strings.NewReader(startup)
			raw, err := cmd.Output()
			noErr(t, err)
			var output bytes.Buffer
			noErr(t, filterMCP(strings.NewReader(startup), &output, io.Discard, bytes.NewReader(raw), dir, selected, ""))
			replies := json.NewDecoder(&output)
			var init, listed M
			noErr(t, replies.Decode(&init))
			noErr(t, replies.Decode(&listed))
			if jStr(init, "result", "serverInfo", "name") != "project-browser" || listed["error"] != nil {
				t.Fatalf("MCP startup failed: initialize=%s tools/list=%s", enc(init), enc(listed))
			}
			names := mcpToolNames(jAt(listed, "result"))
			if tc.origin != "" && !tc.preview {
				if !slices.Equal(names, []any{"navigate", "assert", "close"}) {
					t.Fatalf("restricted DAM tools = %v, want navigate, assert, close", names)
				}
			} else if !slices.Contains(names, any("read")) || !slices.Contains(names, any("screenshot")) ||
				slices.Contains(names, any("assert")) != (tc.origin != "") {
				t.Fatalf("unexpected preview tools: %v", names)
			}
		})
	}
}

// Verify the MCP output and CDP policy together. A permissive route, response,
// credential or tool must fail against synthetic pages with private fields.
func TestProjectBrowserDAMVerification(t *testing.T) {
	const dam = "https://revilio.com"
	const preview = "http://127.0.0.1:4173"
	d := t.TempDir()
	module := filepath.Join(d, "fixture.cjs")
	noErr(t, os.WriteFile(module, []byte(browserProductionFixture+browserDAMFixture), 0600))
	binding := filepath.Join(d, "binding.json")
	noErr(t, os.WriteFile(binding, []byte(enc(M{"dam": M{"previewOrigins": []string{preview}}})), 0600))
	t.Setenv("FLEET_BROWSER_BINDINGS", binding)
	t.Setenv("FLEET_BROWSER_EXECUTABLE", module)
	t.Setenv("FLEET_PLAYWRIGHT_MODULE", module)
	for _, pair := range []struct{ project, origin string }{
		{"another", dam}, {"dam", damBrowserOrigin}, {"fleet", contract.ProductionBrowserOrigin},
	} {
		cfg, err := browserConfig(M{"projectId": pair.project}, pair.origin)
		noErr(t, err)
		if cfg["damVerification"] != nil {
			t.Fatal("DAM policy applied to another grant")
		}
	}
	cfg, err := browserConfig(M{"projectId": "dam"}, dam)
	noErr(t, err)
	cfg["timeoutMs"] = 3000
	cfg["captureDir"] = d
	pipe, err := browserPipe(M{"production": M{
		"origin": dam, "headers": []M{
			{"name": "X-DAM-Verification-Token", "value": damBrowserToken},
			{"name": "X-DAM-Aux", "value": "invented-dam-secondary"},
		},
		"readinessProbe": M{"path": "/site/verification", "method": "GET", "status": 200},
	}})
	noErr(t, err)
	defer pipe.Close()

	type step struct {
		name string
		args M
		want string
	}
	navigate := func(url string, want string) step { return step{"navigate", M{"url": url}, want} }
	check := func(name string, want string) step { return step{"assert", M{"check": name}, want} }
	const loaded = `{"loaded":true}`
	const denied = ""
	steps := []step{
		navigate(dam+"/site/verification", loaded),
		navigate(dam+"/review/index", loaded),
		check("reviewQueue", `{"cards":2,"checkCards":2,"matches":true}`),
		navigate(dam+"/review/view?id=101", loaded),
		check("reviewQueue", `{"cards":2,"checkCards":2,"matches":true}`),
		navigate(dam+"/model/list", loaded),
		check("modelsHeading", `{"visible":true}`),
		navigate(dam+"/video/view?id=101", loaded),
		check("sceneDossier", `{"visible":true}`),
		navigate(dam+"/project/view?id=101&verification_producer_id=201", loaded),
		check("missingBadge", `{"visible":true,"count":3}`),
		check("openChecklist", loaded),
		check("missingModels", `{"red":1,"yellow":2,"count":3}`),
		navigate(dam+"/project/view?id=102&verification_producer_id=201", loaded),
		check("missingBadge", `{"visible":false,"count":0}`),
		check("openChecklist", loaded),
		check("missingModels", `{"red":0,"yellow":0,"count":0}`),
		navigate(dam+"/video/view?id=101&verification_producer_id=201", loaded),
		check("participantPrompt", `{"visible":true}`),
		check("focusModels", `{"focused":true}`),
		navigate(dam+"/video/view?id=102&verification_producer_id=201", loaded),
		check("participantPrompt", `{"visible":false}`),
		{"close", M{}, `{"closed":true}`},
		navigate(dam+"/video/view?id=102&verification_producer_id=201", loaded),
		check("sceneDossier", denied),
	}
	for _, action := range []step{
		{"read", M{}, denied},
		{"evaluate", M{"expression": "document.documentElement.outerHTML"}, denied},
		{"screenshot", M{}, denied},
		{"emulate", M{"viewport": "desktop", "colorScheme": "light"}, denied},
		{"click", M{"selector": "a"}, denied},
		{"fill", M{"selector": "input", "value": "invented"}, denied},
		{"press", M{"key": "Enter"}, denied},
		{"drag", M{"source": "a", "target": "h1"}, denied},
		{"assert", M{"check": "modelsHeading", "selector": "body"}, denied},
		check("documentText", denied),
	} {
		steps = append(steps, navigate(dam+"/model/list", loaded), action)
	}
	for _, url := range []string{
		dam + "/uploads/invented.jpg", dam + "/media/invented.mp4", dam + "/video/documents-zip?id=101",
		dam + "/project/missing?id=101&verification_producer_id=201&format=csv",
		dam + "/project/view?id=101", dam + "/video/view?id=101&id=102",
		dam + "/video/view?id=101&%76erification_producer_id=201", dam + "/video/view?id=%31",
		dam + "/%6dodel/list", dam + "/uploads/%2e%2e/model/list", dam + "/model%252flist",
		dam + "/model/list/", dam + ":443/model/list", dam + ".invalid/model/list",
	} {
		steps = append(steps, navigate(url, denied))
	}
	steps = append(steps,
		navigate(preview+"/fixture", `{"url":"http://127.0.0.1:4173/fixture","title":"preview title","text":"preview body"}`),
		step{"evaluate", M{"expression": "21 * 2"}, `{"result":42,"url":"http://127.0.0.1:4173/fixture","title":"preview title","text":"preview body"}`},
	)
	var requests []string
	for i, step := range steps {
		requests = append(requests, enc(M{"id": i + 1, "method": "tools/call", "params": M{"name": step.name, "arguments": step.args}}))
	}
	c := browserCommand(cfg, d, false)
	c.ExtraFiles = []*os.File{pipe}
	c.Stdin = strings.NewReader(strings.Join(requests, "\n"))
	out, err := c.CombinedOutput()
	noErr(t, err)
	if strings.Contains(string(out), "invented-") {
		t.Fatal("private fixture values escaped")
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) != len(steps) {
		t.Fatalf("got %d MCP replies, want %d", len(lines), len(steps))
	}
	for i, step := range steps {
		reply := decM(lines[i])
		if step.want == denied {
			if reply["error"] == nil || reply["result"] != nil {
				t.Errorf("step %d %s: expected denial", i+1, step.name)
			}
			continue
		}
		result, _ := reply["result"].(M)
		content, _ := result["content"].([]any)
		if len(content) != 1 || enc(content[0]) != enc(M{"type": "text", "text": step.want}) {
			t.Errorf("step %d %s: unexpected MCP result %s", i+1, step.name, lines[i])
		}
	}
	captures, err := filepath.Glob(filepath.Join(d, "browser-capture-*"))
	noErr(t, err)
	if len(captures) != 0 {
		t.Fatal("restricted browser wrote a screenshot")
	}
}

const browserDAMFixture = `
const originalSend = Socket.prototype.send;
Socket.prototype.send = function(raw) {
  const command = JSON.parse(raw);
  if (command.method === 'Page.getFrameTree') {
    queueMicrotask(() => this.deliver({id:command.id,result:{frameTree:{frame:{id:'top'}}}}));
    return;
  }
  if (command.method === 'Browser.setDownloadBehavior') assert.equal(command.params.behavior, 'deny');
  originalSend.call(this,raw);
};
async function damPause(url, type, options = {}) {
  const {frameId='top', method='GET', requestHeaders={}, ...event} = options;
  const requestId = String(++requestID);
  const answered = new Promise(resolve => intercepted.set(requestId,resolve));
  socket.deliver({method:'Fetch.requestPaused',sessionId:'fixture',params:{
    requestId, frameId, resourceType:type, request:{url,method,headers:requestHeaders}, ...event
  }});
  const reply = await answered;
  intercepted.delete(requestId);
  return reply;
}
const deny = (reply) => assert.equal(reply.method,'Fetch.failRequest');
const allow = (reply, headers) => {
  assert.equal(reply.method,'Fetch.continueRequest');
  assert.deepEqual(Object.fromEntries(reply.params.headers.map(({name,value})=>[name.toLowerCase(),value])),headers);
};
const damHeaderFixture = {'X-DAM-Verification-Token':token, 'X-DAM-Aux':auxToken,
  Reflected:token, Cookie:cookie, Authorization:'invented-auth', Referer:origin+'/invented-private'};
const header = (name,value) => ({name,value});
const response = (url,type,mime,extra=[]) => damPause(url,type,{responseStatusCode:200,
  responseHeaders:[header('Content-Type',mime),...extra]});
let exercised, currentURL = 'about:blank', focused;
const previewURL = cfg.previewOrigins[0]+'/fixture';
page.url = () => currentURL;
page.goto = async url => {
  if (url === previewURL) {
    deny(await damPause(origin+'/model/list','Document'));
    allow(await damPause(url,'Document'),{});
  } else {
    allow(await damPause(url,'Document',{requestHeaders:damHeaderFixture}),expected);
    const mime = url.endsWith('/site/verification') ? 'text/plain' : 'text/html';
    const pageReply = await response(url,'Document',mime,[header('Set-Cookie','session='+cookie)]);
    assert.equal(pageReply.method,'Fetch.continueResponse');
    assert.deepEqual(pageReply.params.responseHeaders,[header('Content-Type',mime)]);
    if (!exercised) {
      const model = origin+'/model/list';
      const style = origin+'/css/site.css';
      const script = origin+'/assets/0a123b/js/select2.full.min.js';
      allow(await damPause(style,'Stylesheet',{requestHeaders:{...damHeaderFixture,Accept:'text/css'}}),{accept:'text/css'});
      allow(await damPause(script,'Script',{requestHeaders:damHeaderFixture}),{});
      assert.equal((await response(style,'Stylesheet','text/css')).method,'Fetch.continueResponse');
      assert.equal((await response(script,'Script','application/javascript')).method,'Fetch.continueResponse');
      for (const [target,type] of [
        [origin+'/uploads/invented.js','Script'],
        [origin+'/media/invented.mp4','Media'],
        [origin+'/static/file/invented.pdf','Document'],
        [origin+'/i.php?f=invented','Image'],
        [origin+'/assets/0a123b/%2Finvented.css','Stylesheet'],
        [origin+'/assets/0a123b/%252e%252e/invented.js','Script'],
        [origin+'/video/documents-zip?id=101','Document'],
        [origin+'/project/missing?id=101&verification_producer_id=201&format=csv','Document'],
        [style,'Image'], [model,'XHR'], [model,'Script'], [previewURL,'Document'],
        ['https://other.invalid/model/list','Document'],
      ]) deny(await damPause(target,type));
      deny(await damPause(model,'Document',{frameId:'child'}));
      deny(await damPause(model,'Document',{method:'POST'}));
      deny(await response(script,'Script','video/mp4'));
      deny(await response(model,'Document','application/zip'));
      deny(await response(model,'Document','text/html',[header('Content-Disposition','attachment')]));
      const redirect = (url,type,to) => damPause(url,type,{responseStatusCode:302,responseHeaders:[header('Location',to)]});
      const allowed = await redirect(model,'Document','/review/index');
      assert.equal(allowed.method,'Fetch.continueResponse');
      allow(await damPause(origin+'/review/index','Document',{redirectedRequestId:allowed.params.requestId}),expected);
      for (const to of ['/media/invented.mp4', '/uploads/%2e%2e/model/list','/%6dodel/list', '//other.invalid/',
        'https://other.invalid/model/list', previewURL, 'data:text/html,invented']) {
        deny(await redirect(model,'Document',to));
      }
      deny(await redirect(script,'Script','/media/invented.js'));
      deny(await damPause(origin+'/media/invented.mp4','Document',{redirectedRequestId:allowed.params.requestId}));
      exercised = true;
    }
  }
  currentURL = url;
  focused = false;
  return {ok:()=>true};
};
page.title = async () => currentURL === previewURL ? 'preview title' : 'invented-private-title';
page.evaluate = async () => currentURL === previewURL ? 42 : 'invented-private-dom';
page.getByRole = (role,options) => {
  assert.equal(role,'heading');
  assert.deepEqual(options,{name:'Models',exact:true});
  return {isVisible:async()=>true};
};
page.locator = (selector) => {
  const params = new URL(currentURL).searchParams;
  const two = params.get('id') === '102';
  switch (selector) {
  case 'body': return {innerText:async()=>currentURL === previewURL ? 'preview body' : 'invented-private-body'};
  case '.review-list > a':
  case '.review-list > a .dossier-dot[title="check"]': return {count:async()=>2};
  case '.dossier-panel': return {isVisible:async()=>true};
  case 'a.btn[href^="/project/missing?"]':
    return {isVisible:async()=>true,getAttribute:async()=>'/project/missing?'+params};
  case 'a.btn[href^="/project/missing?"] .badge':
    return {isVisible:async()=>!two,innerText:async()=>'3'};
  case 'table.table > tbody .dossier-dot.bg-danger': return {count:async()=>two?0:1};
  case 'table.table > tbody .dossier-dot.bg-warning': return {count:async()=>two?0:2};
  case '#scene-participant-prompt': return {isVisible:async()=>!two};
  case '#scene-participant-prompt button[aria-controls="scene-models"]':
    return {click:async()=>{focused = true;}};
  case '.select2-search__field[aria-labelledby="scene-models-label"]:focus':
    return {isVisible:async()=>focused};
  default: assert.fail('unexpected fixture selector');
  }
};
`
