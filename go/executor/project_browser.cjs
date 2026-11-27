// Trusted MCP adapter: page operations stay inside the project origin grant.
const fs = require('node:fs');
const path = require('node:path');
const readline = require('node:readline');
const cfg = JSON.parse(process.argv[1]);
const {chromium} = require(cfg.module);
let browser, context, page, launched;
const timeout=cfg.timeoutMs || 60000;
function bounded(promise) {
 let timer;return Promise.race([promise,new Promise((_,reject)=>{timer=setTimeout(()=>reject(Error('browser-timeout-unknown-no-replay')),timeout);})]).finally(()=>clearTimeout(timer));
}
const previews = new Set(cfg.previewOrigins);
const allowed = new Set(previews);
if (cfg.production) allowed.add(cfg.production.origin);
const secrets = new Set();
let headers = {};
if (cfg.production) {
  const access = JSON.parse(fs.readFileSync(3, 'utf8'));
  fs.closeSync(3);
  for (const [key, header] of Object.entries({clientId:'CF-Access-Client-Id',clientSecret:'CF-Access-Client-Secret'})) {
    const value = access[key];
    if (typeof value !== 'string' || !value) throw Error('browser authority unavailable');
    headers[header] = value; secrets.add(value);
  }
}
function safe(value) {
  let text = String(value);
  for (const secret of secrets) text = text.split(secret).join('[redacted]');
  return text;
}
function permitted(url) { try { return allowed.has(new URL(url).origin); } catch { return false; } }
let control, primaryTarget, profileDir, initialResolve, initialReject, closing, nextCommand = 0;
let usedProduction = false;
const pending = new Map();
async function close() {
  if(closing)return closing;
  closing=(async()=>{
    const owned=context, child=launched, directory=profileDir;
    browser=context=page=launched=profileDir=undefined;
    if(control)control.close();control=undefined;
    for(const {reject} of pending.values())reject(Error('browser-closed'));
    pending.clear();
    let failed=false;
    try {if(owned)await bounded(owned.close());}catch {failed=true;}
    if(child && child.exitCode===null) {
      try {
        await bounded(new Promise((resolve,reject)=>{child.once('exit',resolve);child.once('error',()=>reject(Error('browser-close-failed')));child.kill('SIGTERM');}));
      }catch {failed=true;}
    }
    if(directory)await fs.promises.rm(directory,{recursive:true,force:true,maxRetries:20,retryDelay:100});
    if(failed)throw Error('browser-close-failed');
  })();
  try {await closing;}finally{closing=undefined;}
}
function targetSend(sessionId,method,params={}) {
  return new Promise((resolve,reject)=>{
    const id=++nextCommand;
    const timer=setTimeout(()=>{pending.delete(id);reject(Error('browser-timeout-unknown-no-replay'));failClosed();},timeout);
    pending.set(id,{resolve:value=>{clearTimeout(timer);resolve(value);},reject:error=>{clearTimeout(timer);reject(error);}});
    try {control.send(JSON.stringify({id,method,params,...(sessionId?{sessionId}:{})}));}
    catch {const waiter=pending.get(id);pending.delete(id);waiter.reject(Error('browser-protocol-failed'));}
  });
}
async function paused(sessionId,event) {
  const request=event.request;
  if(!permitted(request.url)) return targetSend(sessionId,'Fetch.failRequest',{requestId:event.requestId,errorReason:'BlockedByClient'});
  if(event.responseStatusCode || event.responseErrorReason){
    const responseHeaders=(event.responseHeaders ?? []).filter(({name,value})=>{
      const header=name.toLowerCase();
      return !header.startsWith('cf-access-') &&
        !(header==='set-cookie' && /^\s*CF_Authorization=/i.test(value));
    });
    if(responseHeaders.length===(event.responseHeaders ?? []).length)
      return targetSend(sessionId,'Fetch.continueResponse',{requestId:event.requestId});
    return targetSend(sessionId,'Fetch.continueResponse',{requestId:event.requestId,responseCode:event.responseStatusCode,responsePhrase:event.responseStatusText ?? "",responseHeaders});
  }
  const production=cfg.production && new URL(request.url).origin===cfg.production.origin;
  const requestHeaders=Object.entries(request.headers)
    .filter(([name])=>!name.toLowerCase().startsWith('cf-access-client-'))
    .map(([name,value])=>({name,value:String(value)}));
  if(production) {
    usedProduction = true;
    for(const [name,value] of Object.entries(headers))requestHeaders.push({name,value});
  }
  // CDP overrides only this request; redirected requests are paused and checked again.
  await targetSend(sessionId,'Fetch.continueRequest',{requestId:event.requestId,headers:requestHeaders,...(production?{interceptResponse:true}:{})});
}
async function attached(event) {
  const {sessionId,targetInfo}=event;
  if(targetInfo.type==='service_worker') {await targetSend(null,'Target.closeTarget',{targetId:targetInfo.targetId});return;}
  if(targetInfo.type==='page') {
    if(primaryTarget) {await targetSend(null,'Target.closeTarget',{targetId:targetInfo.targetId});return;}
    primaryTarget=targetInfo.targetId;
  }
  // Children (including out-of-process frames/workers) must be intercepted before resuming.
  await targetSend(sessionId,'Target.setAutoAttach',{autoAttach:true,waitForDebuggerOnStart:true,flatten:true});
  if(cfg.production)await targetSend(sessionId,'Network.setCacheDisabled',{cacheDisabled:true});
  await targetSend(sessionId,'Fetch.enable',{patterns:[{urlPattern:'*',requestStage:'Request'}]});
  await targetSend(sessionId,'Runtime.runIfWaitingForDebugger');
  if(targetInfo.targetId===primaryTarget)initialResolve();
}
async function targetMessage(event) {
  const message=JSON.parse(event.data);
  if('id' in message) {
    const waiter=pending.get(message.id);if(!waiter)return;
    pending.delete(message.id);
    if(message.error)waiter.reject(Error('browser-protocol-failed'));else waiter.resolve(message.result);
  } else if(message.method==='Fetch.requestPaused') await paused(message.sessionId,message.params);
  else if(message.method==='Target.attachedToTarget') await attached(message.params);
}
function failClosed() {if(closing)return;if(initialReject)initialReject(Error('browser-protocol-failed'));close().catch(()=>{});}
async function open() {
  if(browser)return;
  primaryTarget=undefined;
  usedProduction=false;
  profileDir=fs.mkdtempSync(require('node:path').join(require('node:os').tmpdir(),'fleet-browser-'));
  const owned=Boolean(process.env.FLEET_MCP_GENERATION);
  const executable=owned ? cfg.ownedExecutor : cfg.executable;
  if(!executable)throw Error('browser-owned-dependency-unready');
  // Match Playwright's credential-store isolation; macOS must not open the user's Keychain.
  launched=require('node:child_process').spawn(executable,['--headless','--password-store=basic','--use-mock-keychain','--no-first-run','--no-default-browser-check','--remote-debugging-port=0','--remote-debugging-address=127.0.0.1','--user-data-dir='+profileDir,'about:blank'],{stdio:'ignore',...(owned?{argv0:'fleet-browser-chromium'}:{})});
  launched.on('error',failClosed);
  const endpointFile=require('node:path').join(profileDir,'DevToolsActivePort');
  await bounded(new Promise((resolve,reject)=>{
    const watcher=fs.watch(profileDir,()=>{if(fs.existsSync(endpointFile)){watcher.close();resolve();}});
    launched.once('exit',()=>{watcher.close();reject(Error('browser-launch-failed'));});
    if(fs.existsSync(endpointFile)){watcher.close();resolve();}
  }));
  const [port,path]=fs.readFileSync(endpointFile,'utf8').trim().split('\n');
  browser=await bounded(chromium.connectOverCDP('http://127.0.0.1:'+port));
  context=browser.contexts()[0];
  control=new WebSocket('ws://127.0.0.1:'+port+path);
  await bounded(new Promise((resolve,reject)=>{control.addEventListener('open',resolve,{once:true});control.addEventListener('error',()=>reject(Error('browser-protocol-failed')),{once:true});}));
  control.addEventListener('message',event=>{targetMessage(event).catch(failClosed);});
  control.addEventListener('error',failClosed);
  const initialReady=new Promise((resolve,reject)=>{initialResolve=resolve;initialReject=reject;});
  initialReady.catch(()=>{});
  await targetSend(null,'Target.setAutoAttach',{autoAttach:true,waitForDebuggerOnStart:true,flatten:true});
  await bounded(initialReady);
  await targetSend(null,'Browser.setDownloadBehavior',{behavior:'deny'});
  await context.routeWebSocket('**/*',socket=>{try {socket.close().catch(failClosed);}catch {failClosed();}});
  page=context.pages()[0] || await context.newPage();
}
const tools = [
  {name:'navigate', description:'Open an explicitly granted project preview or production URL. Other origins, popups, downloads and WebSockets are blocked.', inputSchema:{type:'object',properties:{url:{type:'string'}},required:['url'],additionalProperties:false}},
  {name:'evaluate',description:'Evaluate JavaScript in the current granted page for DOM, manifest, image dimensions, display-mode and application assertions. Network remains origin-scoped; cookies and authority values are redacted from results.',inputSchema:{type:'object',properties:{expression:{type:'string'}},required:['expression'],additionalProperties:false}},
  {name:'read',description:'Read current page title, URL and rendered body text; no network inspection.',inputSchema:{type:'object',properties:{},additionalProperties:false}},
  {name:'click',description:'Click a unique Playwright locator selector on the current page.',inputSchema:{type:'object',properties:{selector:{type:'string'}},required:['selector'],additionalProperties:false}},
  {name:'fill',description:'Fill a unique input locator on the current page.',inputSchema:{type:'object',properties:{selector:{type:'string'},value:{type:'string'}},required:['selector','value'],additionalProperties:false}},
  {name:'press',description:'Send a trusted Playwright key or chord such as Escape, ArrowDown, Space, Enter or Shift+Tab. Optionally focus a unique locator first.',inputSchema:{type:'object',properties:{key:{type:'string'},selector:{type:'string'}},required:['key'],additionalProperties:false}},
  {name:'drag',description:'Perform a trusted pointer drag from a unique source locator to a unique target locator. Optional steps interpolate pointer movement (default 10).',inputSchema:{type:'object',properties:{source:{type:'string'},target:{type:'string'},steps:{type:'integer',minimum:1}},required:['source','target'],additionalProperties:false}},
  {name:'emulate',description:'Set Chromium layout and color scheme on the owned page, before or after navigating. Desktop is 1440x900, mobile is 390x844, both at device scale 1 with reduced motion.',inputSchema:{type:'object',properties:{viewport:{enum:['desktop','mobile']},colorScheme:{enum:['dark','light']}},required:['viewport','colorScheme'],additionalProperties:false}},
  {name:'screenshot',description:'Capture a granted loopback preview viewport or an optional unique Playwright locator as PNG. Use a fresh browser after production. Returns a run-private path and pixel dimensions for artifact_save.',inputSchema:{type:'object',properties:{selector:{type:'string',minLength:1}},additionalProperties:false}},
  {name:'close',description:'Close the owned browser and clean its temporary profile.',inputSchema:{type:'object',properties:{},additionalProperties:false}}
];
function requireScopedPage(current) {
  if (!current || current !== page || !permitted(current.url())) throw Error('page outside scope');
}
async function press(current, {key, selector}) {
  if (typeof key !== 'string' || !key || (selector !== undefined && (typeof selector !== 'string' || !selector))) throw Error('invalid press arguments');
  if (selector !== undefined) await current.locator(selector).focus({timeout});
  requireScopedPage(current);
  await current.keyboard.press(key);
}
async function drag(current, {source, target, steps = 10}) {
  if (typeof source !== 'string' || !source || typeof target !== 'string' || !target || !Number.isSafeInteger(steps) || steps < 1) throw Error('invalid drag arguments');
  const destination = current.locator(target);
  // Resolve a unique visible destination before pressing the mouse button.
  await destination.waitFor({state:'visible',timeout});
  requireScopedPage(current);
  await current.locator(source).dragTo(destination,{steps,timeout});
}
function textResult(value) {
  return {content:[{type:'text',text:JSON.stringify(value)}]};
}
async function emulate(args) {
  const {viewport, colorScheme} = args;
  if ((viewport !== 'desktop' && viewport !== 'mobile') ||
      (colorScheme !== 'dark' && colorScheme !== 'light') ||
      Object.keys(args).length !== 2) throw Error('invalid emulation arguments');
  await open();
  const current = page;
  if(current.url()!=='about:blank') requireScopedPage(current);
  const size = viewport === 'desktop' ? {width:1440,height:900} : {width:390,height:844};
  await current.setViewportSize(size);
  await current.emulateMedia({colorScheme,reducedMotion:'reduce'});
  if(current !== page || current.url()!=='about:blank') requireScopedPage(current);
  return textResult({...size,colorScheme,deviceScaleFactor:1});
}
function requirePreview(current) {
  requireScopedPage(current);
  const origin = new URL(current.url());
  if(usedProduction || !previews.has(origin.origin) || origin.protocol!=='http:' ||
      !['localhost','127.0.0.1'].includes(origin.hostname)) throw Error('preview capture denied');
}
function savePNG(png) {
  const root = cfg.captureDir;
  if(!root || !path.isAbsolute(root) || !fs.lstatSync(root).isDirectory()) throw Error('capture directory unavailable');
  if(png.length < 33 || png.subarray(0,8).toString('hex') !== '89504e470d0a1a0a' ||
      png.toString('ascii',12,16) !== 'IHDR') throw Error('invalid screenshot PNG');
  const width = png.readUInt32BE(16);
  const height = png.readUInt32BE(20);
  if(!width || !height) throw Error('invalid screenshot dimensions');
  const dir = fs.mkdtempSync(path.join(root,'browser-capture-'));
  const file = path.join(dir,'screenshot.png');
  try {
    fs.writeFileSync(file,png,{flag:'wx',mode:0o600});
  } catch(error) {
    fs.rmSync(dir,{recursive:true,force:true});
    throw error;
  }
  return textResult({path:file,width,height});
}
async function screenshot(current, args) {
  const {selector} = args;
  if(Object.keys(args).some(key=>key!=='selector') ||
      (selector !== undefined && (typeof selector !== 'string' || !selector.trim()))) throw Error('invalid screenshot arguments');
  requirePreview(current);
  let navigated = false;
  const onNavigation = frame => {if(frame === current.mainFrame()) navigated = true;};
  current.on('framenavigated',onNavigation);
  try {
    // Playwright waits for fonts in the captured frame and settles CSS/Web Animations for the PNG.
    const options = {type:'png',animations:'disabled',caret:'hide',scale:'css',timeout};
    const png = await (selector === undefined ? current.screenshot(options) : current.locator(selector).screenshot(options));
    requirePreview(current);
    if(navigated) throw Error('page navigated during capture');
    // Publish bytes only after the final scope check. No asynchronous write can outlive a timed-out call.
    return savePNG(png);
  } finally {
    current.off('framenavigated',onNavigation);
  }
}
async function call(req) {
  if(req.method==='initialize') return {protocolVersion:req.params.protocolVersion,capabilities:{tools:{}},serverInfo:{name:'project-browser',version:'1'},instructions:'Use these tools for browser checks. Shell Chromium is unsupported in the native sandbox. Granted origins: '+[...allowed].join(', ')};
  if(req.method==='tools/list') return {tools};
  if(req.method!=='tools/call') throw Error('unsupported method');
  const {name,arguments:a={}}=req.params;
  if(!tools.some(t=>t.name===name)) throw Error('unknown tool');
  if(name==='close') { await close(); return {content:[{type:'text',text:'Browser closed'}]}; }
  if(name==='emulate') return emulate(a);
  if(name==='screenshot') return screenshot(page,a);
  if(name==='navigate' && !permitted(a.url)) throw Error('origin denied');
  await open();
  for (const cookie of await context.cookies()) if(cookie.value) secrets.add(cookie.value);
  const current = page;
  if(name!=='navigate') requireScopedPage(current);
  let evaluated;
  if(name==='navigate') await current.goto(a.url,{waitUntil:'domcontentloaded'});
  else if(name==='evaluate') evaluated=await current.evaluate(a.expression);
  else if(name==='click') await current.locator(a.selector).click();
  else if(name==='fill') await current.locator(a.selector).fill(a.value);
  else if(name==='press') await press(current,a);
  else if(name==='drag') await drag(current,a);
  requireScopedPage(current);
  for (const cookie of await context.cookies()) if(cookie.value) secrets.add(cookie.value);
  const title = await current.title();
  const body = await current.locator('body').innerText();
  requireScopedPage(current);
  return {content:[{type:'text',text:safe(JSON.stringify({result:evaluated,url:current.url(),title,text:body}))}]};
}
(async()=>{
 if(process.argv[2]==='check') {try {await open();await page.setContent('<title>Fleet readiness</title>');if(await page.title()!=='Fleet readiness')throw Error();
    if(cfg.production) {
      await page.goto(cfg.production.origin,{waitUntil:'domcontentloaded'});
      const identity=await page.evaluate(async()=>{const r=await fetch('/api/ops/me',{method:'POST',headers:{'Content-Type':'application/json'},body:'{}'});return r.ok&&(await r.json()).email;});
      if(identity!=='site-agents')throw Error('browser identity unready');
    }} finally{await close();}return;}
 const input=readline.createInterface({input:process.stdin});
 try { for await(const line of input) {
   let req; try { req=JSON.parse(line); } catch {continue;}
   if(!('id' in req))continue;
   let reply={jsonrpc:'2.0',id:req.id};
   try { reply.result=await bounded(call(req)); } catch {try {await close();}catch {}reply.error={code:-32602,message:'Browser operation failed, denied or timed out; outcome unknown, no replay performed'};}
   process.stdout.write(JSON.stringify(reply)+'\n');
 }} finally{await close();}
})().catch(async()=>{try {await close();}catch {}process.exitCode=1;});
