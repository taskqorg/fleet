// Trusted MCP adapter: page operations stay inside the project origin grant.
const fs = require('node:fs');
const path = require('node:path');
const readline = require('node:readline');
const net = require('node:net');
const { createHash, randomUUID } = require('node:crypto');
const runFile = require('node:util').promisify(require('node:child_process').execFile);
const cfg = JSON.parse(process.argv[1]);
const {chromium} = require(cfg.module);
const damOrigin = cfg.damVerification && cfg.production?.origin === 'https://revilio.com'
  ? cfg.production.origin : undefined;
let browser, context, page, launched;
let activeScene;
const timeout=cfg.timeoutMs || 60000;
function bounded(promise, onTimeout) {
 let timer;return Promise.race([promise,new Promise((_,reject)=>{timer=setTimeout(()=>{
   onTimeout?.();
   reject(Error('browser-timeout-unknown-no-replay'));
 },timeout);})]).finally(()=>clearTimeout(timer));
}
const previews = new Set(cfg.previewOrigins);
const allowed = new Set(previews);
if (cfg.production) allowed.add(cfg.production.origin);
const secrets = new Set();
const authority = new Map();
let readinessProbe, legacyFleet;
function readAuthority(grant) {
  if (new URL(grant.origin).origin !== grant.origin) throw Error('browser production origin invalid');
  // The owning executor passes only the selected project's authority over private IPC.
  let access;
  try {
    access = JSON.parse(fs.readFileSync(3, 'utf8'));
  } finally {
    fs.closeSync(3);
  }
  legacyFleet = !access.production;
  if (!legacyFleet && access.production.origin !== grant.origin) throw Error('browser authority unavailable');
  const headers = legacyFleet
    ? [{name:'CF-Access-Client-Id',value:access.clientId},{name:'CF-Access-Client-Secret',value:access.clientSecret}]
    : access.production.headers;
  readinessProbe = legacyFleet
    ? {path:'/api/ops/me',method:'POST',identityField:'email',expectedIdentity:'site-agents'}
    : access.production.readinessProbe;
  if ((!grant.managed && !headers.length) || !readinessProbe) throw Error('browser authority unavailable');
  for (const {name,value} of headers) {
    if (typeof value !== 'string' || !value || /[\u0000-\u001f\u007f]/.test(value)) throw Error('browser authority unavailable');
    authority.set(name.toLowerCase(), {name,value});
    secrets.add(value);
  }
}
function redactedJSON(value) {
  const text = JSON.stringify(value);
  if (!secrets.size) return text;
  const escaped = [...secrets].sort((a,b)=>b.length-a.length).map(text => text.replace(/[.*+?^$(){}|[\]\\]/g, '\\$&'));
  const pattern = new RegExp(escaped.join('|'), 'g');
  // Redact values and object keys before JSON escaping can disguise a match. Preserve valid JSON.
  return text.replace(/"(?:[^"\\]|\\.)*"|-?\d[\d.eE+-]*|true|false|null/g, token => {
    const value = String(JSON.parse(token));
    const clean = value.replace(pattern, '[redacted]');
    return clean === value ? token : JSON.stringify(clean);
  });
}
function requestOrigin(url) {
  try {
    const parsed = new URL(url);
    if (parsed.protocol === 'https:' || parsed.protocol === 'http:') return parsed.origin;
  } catch {}
}
function permitted(url) { return allowed.has(requestOrigin(url)); }
function isSceneURL(raw) {
  return typeof raw === 'string' && activeScene?.documentURL === raw.split('#', 1)[0];
}

async function openScene(args, signal) {
  const {name, recipeDigest, htmlSha256} = args;
  const digest = value => typeof value === 'string' && /^[a-f0-9]{64}$/.test(value);
  if (!cfg.privateScenes || Object.keys(args).length !== 3 ||
      typeof name !== 'string' || !/^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$/.test(name) ||
      !digest(recipeDigest) || !digest(htmlSha256)) throw Error('invalid private scene arguments');
  const {stdout} = await runFile(cfg.ownedExecutor, ['mcp', 'project-browser', 'read-scene', name, htmlSha256],
    {maxBuffer: 8 << 20, signal});
  const bundle = JSON.parse(stdout);
  const {digest: storedDigest, html: storedHTML, ...recipe} = JSON.parse(bundle.recipe);
  const hash = value => createHash('sha256').update(value).digest('hex');
  const html = Buffer.from(bundle.html,'base64');
  if (recipe.version !== 1 || typeof recipe.path !== 'string' || !/^\/[a-z0-9_/-]*$/i.test(recipe.path) ||
      storedDigest !== recipeDigest || storedHTML !== htmlSha256 || hash(html) !== htmlSha256 ||
      hash('fleet:preview-recipe:v1\0' + JSON.stringify(recipe)) !== recipeDigest ||
      !html.includes('data-preview-recipe="' + recipeDigest + '"')) throw Error('private scene recipe or SHA mismatch');

  await close();
  signal.throwIfAborted();
  const scene = {documentURL:'https://fleet-scene.invalid/' + randomUUID()};
  activeScene = scene;
  productionContext = false;
  usedProduction = false;
  const isolated = await chromium.launch({
    executablePath:path.join(process.env.PATH.split(path.delimiter)[0],'fleet-scene-chromium'),
    chromiumSandbox:true, timeout,
  });
  if (activeScene !== scene) {
    await isolated.close();
    throw Error('private scene closed during launch');
  }
  browser = isolated;
  context = await browser.newContext({deviceScaleFactor:1, serviceWorkers:'block', acceptDownloads:false});
  const current = await context.newPage();
  page = current;
  await context.route('**/*', async route => {
    const request = route.request();
    if (request.url() !== scene.documentURL || request.method() !== 'GET' ||
        !request.isNavigationRequest() || request.frame() !== current.mainFrame()) return route.abort();
    // An opaque origin keeps storage and privileged APIs away from scene code. The kernel
    // network fence also covers APIs that do not use intercepted HTTP requests.
    return route.fulfill({body:html,headers:{
      'content-type':'text/html; charset=utf-8',
      'cache-control':'no-store',
      'content-security-policy':"sandbox allow-scripts; default-src 'none'; img-src data:; font-src data:; style-src 'unsafe-inline'; script-src 'unsafe-inline'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'",
      'x-content-type-options':'nosniff',
    }});
  });
  await context.routeWebSocket('**/*',socket=>socket.close());
  const url = scene.documentURL + '#' + recipe.path;
  await current.goto(url,{waitUntil:'domcontentloaded',timeout});
  requireScopedPage(current);
  if (await current.locator('body').getAttribute('data-preview-recipe',{timeout}) !== recipeDigest)
    throw Error('wrong private scene document');
  return textResult({url,recipeDigest,htmlSha256});
}
let damFrame;
const damPages = new Map([
  ['/site/verification', []],
  ['/review/index', ['project_id', 'model_id', 'person_id']],
  ['/review/view', ['id', 'project_id', 'model_id']],
  ['/model/list', []],
  ['/video/view', ['id', 'verification_producer_id']],
  ['/project/view', ['id', 'verification_producer_id']],
  ['/project/missing', ['id', 'verification_producer_id']],
]);
function damURL(raw) {
  if (!damOrigin || typeof raw !== 'string' || !raw.startsWith(damOrigin + '/')) return;
  const url = new URL(raw);
  // Admit only literal paths and queries, including before Chromium normalizes navigation input.
  if (raw !== damOrigin + url.pathname + url.search || /[%\\]/.test(raw)) return;
  return url;
}
function damPage(raw) {
  const url = damURL(raw);
  const keys = damPages.get(url?.pathname);
  if (!keys) return;
  const seen = new Set();
  if (url.search) {
    for (const field of url.search.slice(1).split('&')) {
      const match = /^([a-z_]+)=([1-9][0-9]*)$/.exec(field);
      if (!match || !keys.includes(match[1]) || seen.has(match[1])) return;
      seen.add(match[1]);
    }
  }
  if (keys[0] === 'id' && !seen.has('id')) return;
  if (url.pathname.startsWith('/project/') && !seen.has('verification_producer_id')) return;
  return url;
}
function damRequestKind(sessionId, event, raw = event.request.url) {
  if (event.request.method !== 'GET' || sessionId !== primarySession || event.frameId !== damFrame) return;
  const type = event.resourceType;
  if (type === 'Document' && damPage(raw)) return 'document';
  const url = damURL(raw);
  if (!url || url.search) return;
  const asset = /^\/assets\/[a-f0-9]+\/(?:[a-zA-Z0-9_-]+\/)*[a-zA-Z0-9_.-]+\.(css|js)$/.exec(url.pathname)?.[1];
  if (type === 'Stylesheet' && (asset === 'css' || url.pathname === '/css/site.css')) return 'style';
  if (type === 'Script' && (asset === 'js' || url.pathname === '/js/review.js')) return 'script';
}
function damResponseAllowed(kind, sessionId, event) {
  if (event.responseErrorReason) return false;
  const headers = event.responseHeaders ?? [];
  if (headers.some(({name,value}) => name.toLowerCase() === 'content-disposition' && !/^inline(?:\s*;|\s*$)/i.test(value))) return false;
  if ([301,302,303,307,308].includes(event.responseStatusCode)) {
    const locations = headers.filter(({name}) => name.toLowerCase() === 'location');
    if (locations.length !== 1) return false;
    const target = locations[0].value;
    const absolute = target.startsWith('/') ? damOrigin + target : target;
    return damRequestKind(sessionId, event, absolute) === kind;
  }
  const types = headers.filter(({name}) => name.toLowerCase() === 'content-type');
  if (types.length !== 1) return false;
  const mime = types[0].value.split(';')[0].trim().toLowerCase();
  if (kind === 'document') return mime === 'text/html' ||
    (mime === 'text/plain' && damPage(event.request.url)?.pathname === '/site/verification');
  if (kind === 'style') return mime === 'text/css';
  return ['text/javascript','application/javascript','application/x-javascript'].includes(mime);
}
let control, primaryTarget, primarySession, profileDir, initialResolve, initialReject, closing, nextCommand = 0;
let usedProduction = false;
let productionContext = false;
let generationCheck, generationTimer;

function ownerGeneration() {
  return new Promise((resolve, reject) => {
    const socket = net.createConnection(process.env.FLEET_MCP_OWNER);
    let response = '';
    socket.setTimeout(timeout, () => socket.destroy(Error('browser authority check timed out')));
    socket.on('connect', () => socket.write(JSON.stringify({
      action:'browser-generation', generation:process.env.FLEET_MCP_GENERATION
    })+'\n'));
    socket.on('data', chunk => { response += chunk; });
    socket.on('error', reject);
    socket.on('close', () => {
      try { resolve(JSON.parse(response).registered === true); }
      catch { resolve(false); }
    });
  });
}
async function requireGeneration() {
  if(!cfg.checkGeneration)return;
  const socket=control;
  generationCheck ??= ownerGeneration().catch(()=>false).finally(()=>{ generationCheck=undefined; });
  if(!await generationCheck) {
    if(productionContext && control===socket)failClosed();
    throw Error('browser authority changed');
  }
}
const pending = new Map();
async function close() {
  if(closing)return closing;
  closing=(async()=>{
    clearInterval(generationTimer);
    const owned=context, child=launched, directory=profileDir, socket=control;
    const isolated=activeScene && browser;
    activeScene=undefined;
    browser=context=page=launched=profileDir=undefined;
    for(const {reject} of pending.values())reject(Error('browser-closed'));
    pending.clear();
    // The owned dependency wrapper can exit before its Chromium group has stopped.
    const disconnected=socket && socket.readyState===WebSocket.OPEN
      ? new Promise(resolve=>socket.addEventListener('close',resolve,{once:true})) : undefined;
    let failed=false;
    try {
      try {if(owned)await bounded(owned.close());}catch {failed=true;}
      try {if(isolated)await bounded(isolated.close());}catch {failed=true;}
      if(child && child.exitCode===null && !child.signalCode) {
        try {
          await bounded(new Promise((resolve,reject)=>{child.once('exit',resolve);child.once('error',()=>reject(Error('browser-close-failed')));child.kill('SIGTERM');}));
        }catch {child.kill('SIGKILL');failed=true;}
      }
      if(disconnected)try {await bounded(disconnected);}catch {failed=true;}
    } finally {
      control=undefined;
      // Chromium resumes paused requests on detach. Do not detach a live interceptor.
      if(!disconnected)socket?.close();
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
  if(closing)return; // Leave requests paused until the owned browser exits.
  const request=event.request;
  const origin = requestOrigin(request.url);
  const production = origin === cfg.production?.origin;
  const blocked = () => targetSend(sessionId,'Fetch.failRequest',{requestId:event.requestId,errorReason:'BlockedByClient'});
  if(!allowed.has(origin) || ((cfg.production?.managed || damOrigin) && production !== productionContext))
    return blocked();
  let kind;
  if (damOrigin && production) {
    kind = damRequestKind(sessionId,event);
    if (!kind) return blocked();
  }
  const response = event.responseStatusCode || event.responseErrorReason;
  if (kind && response && !damResponseAllowed(kind,sessionId,event)) return blocked();
  const authenticated = production && (!kind || kind === 'document');
  const privateHeaders = [...authority.values()];
  const containsAuthority = (name,value) => privateHeaders.some(secret => name.includes(secret.value) || String(value).includes(secret.value));
  if(response) {
    const original = event.responseHeaders ?? [];
    const responseHeaders = original.filter(({name,value})=>{
      const lower = name.toLowerCase();
      return !lower.startsWith('cf-access-') && !authority.has(lower) && !containsAuthority(name,value) &&
        !(lower==='set-cookie' && (kind || /^\s*CF_Authorization=/i.test(value)));
    });
    if(responseHeaders.length===original.length)
      return targetSend(sessionId,'Fetch.continueResponse',{requestId:event.requestId});
    return targetSend(sessionId,'Fetch.continueResponse',{requestId:event.requestId,responseCode:event.responseStatusCode,...(event.responseStatusText?{responsePhrase:event.responseStatusText}:{}),responseHeaders});
  }
  const requestHeaders=Object.entries(request.headers)
    .filter(([name,value])=>{
      const lower = name.toLowerCase();
      if(lower.startsWith('cf-access-client-') || authority.has(lower)) return false;
      if (kind && ['authorization','cookie','referer','range'].includes(lower)) return false;
      return (production && !kind) || !containsAuthority(name,value);
    })
    .map(([name,value])=>({name,value:String(value)}));
  if(production) {
    const socket=control;
    await requireGeneration();
    if(closing || control!==socket)return;
    usedProduction = true;
    if(authenticated) requestHeaders.push(...privateHeaders);
  }
  // CDP overrides only this request; redirected requests are paused and checked again.
  await targetSend(sessionId,'Fetch.continueRequest',{requestId:event.requestId,headers:requestHeaders,...(production?{interceptResponse:true}:{})});
}
async function attached(event) {
  if(closing)return;
  const {sessionId,targetInfo}=event;
  if(targetInfo.type==='service_worker') {await targetSend(null,'Target.closeTarget',{targetId:targetInfo.targetId});return;}
  if(targetInfo.type==='page') {
    if(primaryTarget) {await targetSend(null,'Target.closeTarget',{targetId:targetInfo.targetId});return;}
    primaryTarget=targetInfo.targetId;
    primarySession=sessionId;
  }
  // Children (including out-of-process frames/workers) must be intercepted before resuming.
  await targetSend(sessionId,'Target.setAutoAttach',{autoAttach:true,waitForDebuggerOnStart:true,flatten:true});
  if (damOrigin && productionContext && targetInfo.targetId === primaryTarget) {
    const {frameTree} = await targetSend(sessionId,'Page.getFrameTree');
    damFrame = frameTree.frame.id;
  }
  if(cfg.production && ['page','iframe'].includes(targetInfo.type))await targetSend(sessionId,'Network.setCacheDisabled',{cacheDisabled:true});
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
function failClosed() {
  if(closing)return;
  if(initialReject)initialReject(Error('browser-protocol-failed'));
  // The origin fence may already be down on transport loss.
  launched?.kill('SIGKILL');
  close().catch(()=>{});
}
async function open(production = false) {
  if(closing)await closing;
  if(browser && (cfg.production?.managed || damOrigin) && production !== productionContext)await close();
  if(browser)return;
  productionContext=production;
  if(production && cfg.checkGeneration) {
    await requireGeneration();
    // Revoke an idle open context as well as one sending requests.
    generationTimer=setInterval(()=>{requireGeneration().catch(()=>{});},cfg.generationCheckMs);
  }
  primaryTarget=primarySession=undefined;
  damFrame=undefined;
  usedProduction=false;
  profileDir=fs.mkdtempSync(require('node:path').join(require('node:os').tmpdir(),'fleet-browser-'));
  const owned=Boolean(process.env.FLEET_MCP_GENERATION);
  const executable=owned ? cfg.ownedExecutor : cfg.executable;
  if(!executable)throw Error('browser-owned-dependency-unready');
  // Match Playwright's credential-store isolation; macOS must not open the user's Keychain.
  launched=require('node:child_process').spawn(executable,['--headless','--password-store=basic','--use-mock-keychain','--no-first-run','--no-default-browser-check','--remote-debugging-port=0','--remote-debugging-address=127.0.0.1','--user-data-dir='+profileDir,'about:blank'],{stdio:'ignore',...(owned?{argv0:'fleet-browser-chromium'}:{})});
  launched.on('error',failClosed);
  const endpointFile=path.join(profileDir,'DevToolsActivePort');
  let watcher;
  try {
    await bounded(new Promise((resolve,reject)=>{
      const check=()=>{if(fs.existsSync(endpointFile))resolve();};
      watcher=fs.watch(profileDir,check);
      watcher.once('error',()=>reject(Error('browser-launch-failed')));
      launched.once('exit',()=>reject(Error('browser-launch-failed')));
      launched.once('error',()=>reject(Error('browser-launch-failed')));
      check();
    }));
  } finally {
    watcher?.close();
  }
  const [port,socketPath]=fs.readFileSync(endpointFile,'utf8').trim().split('\n');
  browser=await bounded(chromium.connectOverCDP('http://127.0.0.1:'+port));
  context=browser.contexts()[0];
  const socket=control=new WebSocket('ws://127.0.0.1:'+port+socketPath);
  const failSocket=()=>{if(control===socket)failClosed();};
  await bounded(new Promise((resolve,reject)=>{socket.addEventListener('open',resolve,{once:true});socket.addEventListener('error',()=>reject(Error('browser-protocol-failed')),{once:true});}));
  socket.addEventListener('message',event=>{if(control===socket)targetMessage(event).catch(failSocket);});
  socket.addEventListener('error',failSocket);
  socket.addEventListener('close',failSocket);
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
const damChecks = new Map([
  ['reviewQueue', ['/review/index','/review/view']],
  ['modelsHeading', ['/model/list']],
  ['sceneDossier', ['/video/view']],
  ['missingBadge', ['/project/view']],
  ['openChecklist', ['/project/view']],
  ['missingModels', ['/project/missing']],
  ['participantPrompt', ['/video/view']],
  ['focusModels', ['/video/view']],
]);
const damTools = [
  {...tools[0], description:'Open a granted URL. DAM production navigation returns only a boolean.'},
  {name:'assert', description:'Run a fixed DAM verification check on the current page. Use reviewQueue for card counts, modelsHeading or sceneDossier for panel visibility, missingBadge then openChecklist and missingModels for the producer checklist, and participantPrompt or focusModels for the producer scene.',
    inputSchema:{type:'object',properties:{check:{enum:[...damChecks.keys()]}},required:['check'],additionalProperties:false}},
  tools.find(tool=>tool.name==='close'),
];
const projectTools = !damOrigin ? [...tools] : previews.size ? [damTools[0],...tools.slice(1),damTools[1]] : damTools;
if (cfg.privateScenes) projectTools.push({
  name:'open_scene',description:'Open a private built scene from the current run. Build with preview:scene --build-only under FLEET_SCENE_DIR, then pass the directory name and the two recipe hashes. Use read, evaluate, emulate and screenshot on the opened document. Close before navigating to a project URL.',
  inputSchema:{type:'object',properties:{name:{type:'string'},recipeDigest:{type:'string'},htmlSha256:{type:'string'}},
    required:['name','recipeDigest','htmlSha256'],additionalProperties:false},
});
const missingLink = 'a.btn[href^="/project/missing?"]';
const participantPrompt = '#scene-participant-prompt';
async function damNavigate(current, url) {
  const response = await current.goto(url,{waitUntil:'domcontentloaded',timeout});
  if (!response?.ok() || !damPage(current.url())) throw Error('DAM navigation failed');
  return {loaded:true};
}
async function damAssert(current, args) {
  const url = damPage(current.url());
  const routes = damChecks.get(args.check);
  if (Object.keys(args).length !== 1 || !routes?.includes(url?.pathname)) throw Error('DAM check denied');
  const producer = url.searchParams.has('verification_producer_id');
  switch (args.check) {
  case 'reviewQueue': {
    const cards = await current.locator('.review-list > a').count();
    const checkCards = await current.locator('.review-list > a .dossier-dot[title="check"]').count();
    return {cards,checkCards,matches:cards > 0 && cards === checkCards};
  }
  case 'modelsHeading':
    return {visible:await current.getByRole('heading',{name:'Models',exact:true}).isVisible()};
  case 'sceneDossier':
    if (producer) throw Error('DAM check denied');
    return {visible:await current.locator('.dossier-panel').isVisible()};
  case 'missingBadge': {
    if (!await current.locator(missingLink).isVisible()) throw Error('DAM checklist link unavailable');
    const badge = current.locator(missingLink+' .badge');
    if (!await badge.isVisible()) return {visible:false,count:0};
    const text = (await badge.innerText({timeout})).trim();
    const count = Number(text);
    if (!/^[1-9][0-9]*$/.test(text) || !Number.isSafeInteger(count)) throw Error('DAM checklist badge invalid');
    return {visible:true,count};
  }
  case 'openChecklist': {
    const link = await current.locator(missingLink).getAttribute('href',{timeout});
    const destination = damPage(damOrigin + link);
    if (destination?.pathname !== '/project/missing' ||
        ['id','verification_producer_id'].some(key=>destination.searchParams.get(key) !== url.searchParams.get(key)))
      throw Error('DAM checklist link denied');
    return damNavigate(current,destination.href);
  }
  case 'missingModels': {
    const red = await current.locator('table.table > tbody .dossier-dot.bg-danger').count();
    const yellow = await current.locator('table.table > tbody .dossier-dot.bg-warning').count();
    return {red,yellow,count:red+yellow};
  }
  case 'participantPrompt':
    if (!producer) throw Error('DAM check denied');
    return {visible:await current.locator(participantPrompt).isVisible()};
  case 'focusModels':
    if (!producer) throw Error('DAM check denied');
    await current.locator(participantPrompt+' button[aria-controls="scene-models"]').click({timeout});
    return {focused:await current.locator('.select2-search__field[aria-labelledby="scene-models-label"]:focus').isVisible()};
  }
}
function requireScopedPage(current) {
  if (!current || current !== page || !(activeScene ? isSceneURL(current.url()) : permitted(current.url()))) throw Error('page outside scope');
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
  if (activeScene) return;
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
async function call(req, signal) {
  if(req.method==='initialize') return {protocolVersion:req.params.protocolVersion,capabilities:{tools:{}},serverInfo:{name:'project-browser',version:'1'},instructions:'Use these tools for browser checks. Shell Chromium is unsupported in the native sandbox. Granted origins: '+[...allowed].join(', ')};
  if(req.method==='tools/list') return {tools:projectTools};
  if(req.method!=='tools/call') throw Error('unsupported method');
  const {name,arguments:a={}}=req.params;
  if(name==='open_scene') return openScene(a,signal);
  if(name==='close') {
    await close();
    if (damOrigin && (productionContext || !previews.size)) return textResult({closed:true});
    return {content:[{type:'text',text:'Browser closed'}]};
  }
  if(name==='navigate' && !(activeScene ? isSceneURL(a.url) : permitted(a.url))) throw Error('origin denied');
  const production = name === 'navigate' ? requestOrigin(a.url) === cfg.production?.origin : productionContext;
  if(production && productionContext && browser)await requireGeneration();
  if (damOrigin && production) {
    if (name !== 'navigate' && name !== 'assert') throw Error('DAM tool denied');
    if (name === 'navigate') {
      if (!damPage(a.url) || Object.keys(a).length !== 1) throw Error('DAM route denied');
      await open(true);
    }
    if (!page) throw Error('DAM page unavailable');
    const current = page;
    const url = current.url();
    const result = await (name === 'navigate' ? damNavigate(current,a.url) : damAssert(current,a));
    await requireGeneration();
    if (current !== page || !damPage(current.url()) ||
        (name === 'assert' && a.check !== 'openChecklist' && current.url() !== url))
      throw Error('DAM page changed');
    return textResult(result);
  }
  if(!tools.some(t=>t.name===name)) throw Error('unknown tool');
  if(name==='emulate') return emulate(a);
  if(name==='screenshot') return screenshot(page,a);
  await open(production);
  for (const cookie of await context.cookies()) if(cookie.value) secrets.add(cookie.value);
  const current = page;
  if(name!=='navigate') requireScopedPage(current);
  let evaluated;
  if(name==='navigate') {
    const response = await current.goto(a.url,{waitUntil:'domcontentloaded'});
    if(activeScene && !response) await current.reload({waitUntil:'domcontentloaded'});
  }
  else if(name==='evaluate') evaluated=await current.evaluate(a.expression);
  else if(name==='click') await current.locator(a.selector).click();
  else if(name==='fill') await current.locator(a.selector).fill(a.value);
  else if(name==='press') await press(current,a);
  else if(name==='drag') await drag(current,a);
  requireScopedPage(current);
  const title = await current.title();
  const body = await current.locator('body').innerText();
  if(production)await requireGeneration();
  for (const cookie of await context.cookies()) if(cookie.value) secrets.add(cookie.value);
  requireScopedPage(current);
  return {content:[{type:'text',text:redactedJSON({result:evaluated,url:current.url(),title,text:body})}]};
}
async function checkGenericProduction(grant) {
  const {path,method,status,identityField,expectedIdentity} = readinessProbe;
  const url = new URL(path, grant.origin);
  if (url.protocol !== 'https:' || url.origin !== grant.origin) throw Error('browser production origin denied');
  // A readiness request must not inherit the home page's access policy or CSP.
  const headers = new Headers();
  for (const {name,value} of authority.values()) headers.set(name,value);
  const options = {method,headers,mode:'same-origin',redirect:'error',cache:'no-store',signal:AbortSignal.timeout(timeout)};
  if (method === 'POST') {
    headers.set('Content-Type','application/json');
    options.body = '{}';
  }
  const response = await fetch(url, options);
  try {
    if (identityField && (response.status === 401 || response.status === 403)) return false;
    if (requestOrigin(response.url) !== grant.origin || (status ? response.status !== status : !response.ok)) return false;
    if (!identityField) return true;
    const body = await response.json();
    return body !== null && typeof body === 'object' && !Array.isArray(body) &&
      Object.hasOwn(body,identityField) && body[identityField] === expectedIdentity;
  } finally {
    if (!response.bodyUsed) await response.body?.cancel();
  }
}
async function probeInPage({origin, readinessProbe, legacyFleet, reportReason}) {
  const outcome = reason => reportReason ? reason : reason === 'verified';
  if (location.origin !== origin) return outcome('probe_failed');
  const {path,method,status,identityField,expectedIdentity} = readinessProbe;
  const url = new URL(path, origin);
  if (url.origin !== origin) return outcome('probe_failed');
  const options = legacyFleet ? {method} : {method,mode:'same-origin',redirect:'error',cache:'no-store'};
  if (method === 'POST') {
    options.headers = {'Content-Type':'application/json'};
    options.body = '{}';
  }
  const response = await fetch(url, options);
  if (new URL(response.url).origin !== origin) return outcome('probe_failed');
  if (identityField && (response.status === 401 || response.status === 403)) return outcome('auth_rejected');
  if (status ? response.status !== status : !response.ok) return outcome('unexpected_status');
  if (!identityField) return outcome('verified');
  const body = await response.json().catch(()=>null);
  const matched = body !== null && typeof body === 'object' && !Array.isArray(body) &&
    Object.hasOwn(body,identityField) && body[identityField] === expectedIdentity;
  return outcome(matched ? 'verified' : 'identity_mismatch');
}
async function checkProduction(grant) {
  if (!legacyFleet && !grant.managed) {
    if (!await checkGenericProduction(grant)) throw Error('browser production unready');
    return;
  }
  await page.goto(grant.origin,{waitUntil:'domcontentloaded'});
  if (requestOrigin(page.url()) !== grant.origin) throw Error('browser production origin denied');
  const args={origin:grant.origin,readinessProbe,legacyFleet,reportReason:grant.managed};
  let result;
  if(grant.managed) {
    // Use the browser's fetch from a world with its own builtins. Site scripts cannot replace the
    // probe's fetch, tune credentials, or supply a fabricated identity through the page's JS state.
    const {frameTree} = await targetSend(primarySession,'Page.getFrameTree');
    const {executionContextId} = await targetSend(primarySession,'Page.createIsolatedWorld',{
      frameId:frameTree.frame.id,worldName:'fleet-site-access'
    });
    const evaluated = await targetSend(primarySession,'Runtime.evaluate',{
      expression:'('+probeInPage.toString()+')('+JSON.stringify(args)+')',
      contextId:executionContextId,awaitPromise:true,returnByValue:true
    });
    result = evaluated.exceptionDetails ? 'probe_failed' : evaluated.result?.value;
  } else {
    result = await page.evaluate(probeInPage,args);
  }
  if (result !== (grant.managed ? 'verified' : true)) {
    if (grant.managed && ['auth_rejected','identity_mismatch','unexpected_status'].includes(result)) {
      process.stdout.write(result+'\n');
    }
    throw Error('browser production unready');
  }
}
(async()=>{
 if(cfg.production)readAuthority(cfg.production);
 if(process.argv[2]==='check') {try {await open(Boolean(cfg.production));await page.setContent('<title>Fleet readiness</title>');if(await page.title()!=='Fleet readiness')throw Error();
    if(cfg.production)await bounded(checkProduction(cfg.production));
  } finally{await close();}return;}
 const input=readline.createInterface({input:process.stdin});
 try { for await(const line of input) {
   let req; try { req=JSON.parse(line); } catch {continue;}
   if(!('id' in req))continue;
   let reply={jsonrpc:'2.0',id:req.id};
   const abort = new AbortController();
   try { reply.result=await bounded(call(req,abort.signal),()=>abort.abort()); } catch {try {await close();}catch {}reply.error={code:-32602,message:'Browser operation failed, denied or timed out; outcome unknown, no replay performed'};}
   process.stdout.write(JSON.stringify(reply)+'\n');
 }} finally{await close();}
})().catch(async()=>{try {await close();}catch {}process.exitCode=1;});
