// Offline qualification of proposed component pins against the current ones (docs/designs/fleet-host-updates.md, step 3).
// Usage: node deploy/qualify-pins.mjs <cli|tool> <current pins> <candidate pins> <target> <result.json>
//
// For the target of this machine, installs both pin sets as fleet-update does (download, digest, unpack with links,
// executable), then runs each component the way Fleet runs it, with a fresh home and no account. Agent CLIs: --version,
// the protocol handshake with Fleet's fixed flags, a session with a required stdio MCP server, and the structured
// output stream for every configured model (config/harness-models.json). Host tools: --version and one real use each.
// A check the current pins pass and the candidate fails is a regression; the candidate's install and version checks
// must pass on their own. Checks that need a signed-in account fail the same way for both sets and so do not count.
// The CLI flags mirror go/executor/harness_command.go and go/executor/accounts.go (probeModels); keep them in step.
import { spawn, execFileSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import { mkdtemp, mkdir, readFile, writeFile, chmod, rename, rm, stat } from 'node:fs/promises';
import { createWriteStream } from 'node:fs';
import { tmpdir } from 'node:os';
import { join, dirname } from 'node:path';
import { Readable } from 'node:stream';
import { pipeline } from 'node:stream/promises';
import { createInterface } from 'node:readline';
import { fileURLToPath } from 'node:url';

const [kind, currentPath, candidatePath, target, outPath] = process.argv.slice(2);
if (!outPath || !['cli', 'tool'].includes(kind)) throw Error('usage: qualify-pins.mjs <cli|tool> <current> <candidate> <target> <result.json>');
const here = dirname(fileURLToPath(import.meta.url));
const models = JSON.parse(await readFile(join(here, '../config/harness-models.json')));
const fixture = join(here, 'harness-smoke-mcp.mjs');
const node = process.execPath;
const wait = 60000;

// codexIsolation (go/executor/harness_command.go).
const codexIsolation = [
  '-c', 'project_doc_max_bytes=0', '-c', 'skills.include_instructions=false', '-c', 'mcp_servers={}',
  '--disable', 'memories', '--disable', 'hooks', '--enable', 'skip_host_skill_discovery',
  '-c', 'forced_login_method="chatgpt"', '-c', 'approval_policy="never"', '-c', 'sandbox_mode="workspace-write"',
  '-c', 'sandbox_workspace_write.network_access=false',
  '--disable', 'apps', '--disable', 'plugins', '--disable', 'browser_use', '--disable', 'computer_use', '--disable', 'image_generation',
];
// harness.WireMCP for codex: one required server with its tool approved.
const codexMCP = [
  '-c', `mcp_servers.fleet_smoke.command=${JSON.stringify(node)}`, '-c', `mcp_servers.fleet_smoke.args=${JSON.stringify([fixture])}`,
  '-c', 'mcp_servers.fleet_smoke.required=true', '-c', 'mcp_servers.fleet_smoke.enabled_tools=["echo"]',
  '-c', 'mcp_servers.fleet_smoke.tools.echo.approval_mode="approve"',
];

async function install(dir, name, asset) {
  const response = await fetch(asset.url, { signal: AbortSignal.timeout(300000) });
  if (!response.ok) throw Error(`download HTTP ${response.status}`);
  const payload = join(dir, 'download');
  const hash = createHash(asset.sha256 ? 'sha256' : 'sha512');
  const body = Readable.fromWeb(response.body);
  body.on('data', chunk => hash.update(chunk));
  await pipeline(body, createWriteStream(payload));
  const digest = hash.digest('hex');
  const want = (asset.sha256 || asset.sha512).toLowerCase();
  if (digest !== want) throw Error(`digest ${digest}, pinned ${want}`);
  const files = join(dir, 'files');
  await mkdir(files);
  if (asset.entry && asset.url.endsWith('.zip')) {
    execFileSync('unzip', ['-q', payload, '-d', files]);
  } else if (asset.entry) {
    execFileSync('tar', ['-xzf', payload, '-C', files]);
  } else {
    await chmod(payload, 0o755);
    await rename(payload, join(files, name));
  }
  const exe = join(files, asset.entry || name);
  const st = await stat(exe);
  if (!st.isFile() || !(st.mode & 0o111)) throw Error(`executable ${asset.entry || name} missing`);
  return exe;
}

// exchange: start the CLI, write the frames (a function of the replies so far may add more), read JSON lines until
// done(replies) or the time runs out. Answers the replies and the stderr tail.
function exchange(exe, args, env, frames, done) {
  return new Promise(resolve => {
    const child = spawn(exe, args, { env, cwd: env.HOME, stdio: ['pipe', 'pipe', 'pipe'], detached: true });
    const replies = [];
    let stderr = '';
    let finished = false;
    const finish = reason => {
      if (finished) return;
      finished = true;
      clearTimeout(timer);
      try { process.kill(-child.pid, 'SIGKILL'); } catch {}
      resolve({ replies, reason, stderr: stderr.slice(-600) });
    };
    const timer = setTimeout(() => finish('timeout'), wait);
    child.on('error', err => finish(`spawn: ${err.message}`));
    child.on('exit', code => setTimeout(() => finish(`exit ${code}`), 200));
    child.stderr.on('data', d => { stderr += d; });
    child.stdin.on('error', () => {});
    const write = list => list.forEach(f => child.stdin.write(JSON.stringify(f) + '\n'));
    createInterface({ input: child.stdout }).on('line', line => {
      let v;
      try { v = JSON.parse(line); } catch { return; }
      replies.push(v);
      if (typeof frames === 'function') write(frames(v));
      if (done(replies)) finish('done');
    });
    write(typeof frames === 'function' ? frames(null) : frames);
  });
}

async function home() {
  const dir = await mkdtemp(join(tmpdir(), 'fleet-smoke-'));
  await chmod(dir, 0o700);
  return dir;
}

function baseEnv(dir) {
  const env = { HOME: dir, PATH: process.env.PATH, LANG: process.env.LANG || 'C.UTF-8', TMPDIR: process.env.RUNNER_TEMP || tmpdir() };
  for (const k of ['USER', 'LOGNAME', 'SSL_CERT_FILE', 'SSL_CERT_DIR']) if (process.env[k]) env[k] = process.env[k];
  return env;
}

const byId = (replies, id) => replies.find(v => v.id === id);
const explain = (r, extra = '') => `${extra}${extra ? '; ' : ''}${r.reason}${r.stderr ? `; stderr: ${r.stderr.trim().split('\n').slice(-2).join(' | ')}` : ''}`;
const errorText = v => v?.error ? `error ${v.error.code}: ${String(v.error.message).slice(0, 200)}` : '';

async function versionCheck(exe, version) {
  const dir = await home();
  try {
    const out = execFileSync(exe, ['--version'], { env: { ...baseEnv(dir), DISABLE_AUTOUPDATER: '1' }, timeout: 30000, encoding: 'utf8' });
    return { ok: out.includes(version), detail: out.trim().split('\n')[0].slice(0, 200) };
  } catch (err) {
    return { ok: false, detail: String(err.message).slice(0, 300) };
  } finally {
    await rm(dir, { recursive: true, force: true });
  }
}

async function codexChecks(exe) {
  const checks = {};
  for (const m of models.codex) {
    const dir = await home();
    const codexHome = join(dir, 'codex');
    await mkdir(codexHome, { mode: 0o700 });
    const r = await exchange(exe, ['app-server', ...codexIsolation, ...codexMCP], { ...baseEnv(dir), CODEX_HOME: codexHome }, [
      { id: 1, method: 'initialize', params: { clientInfo: { name: 'fleet', version: '0.1.0' }, capabilities: { experimentalApi: true } } },
      { method: 'initialized' },
      { id: 2, method: 'thread/start', params: { cwd: dir, model: m.id, approvalPolicy: 'never', sandbox: 'workspace-write', developerInstructions: 'Fleet qualification smoke.', config: { 'sandbox_workspace_write.network_access': false } } },
    ], replies => byId(replies, 2));
    const init = byId(r.replies, 1);
    const thread = byId(r.replies, 2);
    if (!checks['app-server initialize']) {
      checks['app-server initialize'] = { ok: Boolean(init?.result?.userAgent), detail: init?.result?.userAgent || explain(r, errorText(init)) };
    }
    checks[`thread/start with required MCP (${m.id})`] = { ok: Boolean(thread?.result?.thread?.id), detail: thread?.result?.thread?.id ? 'thread started' : explain(r, errorText(thread)) };
    await rm(dir, { recursive: true, force: true });
  }
  return checks;
}

async function claudeChecks(exe) {
  const checks = {};
  for (const m of models.claude) {
    const dir = await home();
    const config = join(dir, 'claude');
    await mkdir(config, { mode: 0o700 });
    const args = ['-p', '--input-format', 'stream-json', '--output-format', 'stream-json', '--verbose', '--model', m.id,
      '--permission-mode', 'dontAsk', '--strict-mcp-config', '--setting-sources', '', '--system-prompt', 'Fleet qualification smoke.',
      '--tools', 'Read', '--add-dir', dir,
      '--mcp-config', JSON.stringify({ mcpServers: { fleet_smoke: { command: node, args: [fixture] } } }), '--allowedTools', 'Read,mcp__fleet_smoke__echo'];
    let asked = false;
    const r = await exchange(exe, args, { ...baseEnv(dir), CLAUDE_CONFIG_DIR: config, DISABLE_AUTOUPDATER: '1' }, v => {
      if (v === null) return [{ type: 'control_request', request_id: 'fleet-init', request: { subtype: 'initialize' } }];
      if (!asked && v.type === 'control_response') {
        asked = true;
        return [{ type: 'user', message: { role: 'user', content: 'Call the echo tool with text ok, then reply ok.' } }];
      }
      return [];
    }, replies => replies.some(v => v.type === 'result'));
    const control = r.replies.find(v => v.type === 'control_response');
    const init = r.replies.find(v => v.type === 'system' && v.subtype === 'init');
    const smoke = init?.mcp_servers?.find(s => s.name === 'fleet_smoke');
    const result = r.replies.find(v => v.type === 'result');
    if (!checks['stream-json initialize']) {
      checks['stream-json initialize'] = { ok: control?.response?.subtype === 'success', detail: control?.response?.subtype || explain(r) };
    }
    checks[`init event with MCP connected (${m.id})`] = {
      ok: smoke?.status === 'connected' && Boolean(init?.tools?.includes('mcp__fleet_smoke__echo')),
      detail: init ? `fleet_smoke ${smoke?.status ?? 'absent'}; model ${init.model ?? '?'}` : explain(r),
    };
    checks[`result event (${m.id})`] = { ok: Boolean(result) && !result.is_error, detail: result ? `${result.subtype}${result.is_error ? ' (error)' : ''}: ${String(result.result ?? '').slice(0, 160)}` : explain(r) };
    await rm(dir, { recursive: true, force: true });
  }
  return checks;
}

async function antigravityChecks(exe) {
  const dir = await home();
  const gemini = join(dir, 'gemini');
  await mkdir(gemini, { mode: 0o700 });
  const r = await exchange(exe, ['acp', '--gemini-dir', gemini, '--app-data-dir', 'antigravity-cli', '--include-default-customizations=false'], { ...baseEnv(dir), GEMINI_HOME: gemini }, [
    { jsonrpc: '2.0', id: 1, method: 'initialize', params: { protocolVersion: 1, clientCapabilities: {}, clientInfo: { name: 'fleet', version: '0.1.0' } } },
    { jsonrpc: '2.0', id: 2, method: 'session/new', params: { cwd: dir, mcpServers: [{ name: 'fleet_smoke', command: node, args: [fixture], env: [] }] } },
  ], replies => byId(replies, 2));
  await rm(dir, { recursive: true, force: true });
  const init = byId(r.replies, 1);
  const session = byId(r.replies, 2);
  const offered = JSON.stringify(session?.result ?? {});
  const checks = {
    'ACP initialize protocolVersion 1': { ok: init?.result?.protocolVersion === 1, detail: init?.result ? `protocolVersion ${init.result.protocolVersion}` : explain(r, errorText(init)) },
    'session/new with MCP': { ok: Boolean(session?.result?.sessionId), detail: session?.result?.sessionId ? 'session created' : explain(r, errorText(session)) },
  };
  for (const m of models.antigravity) {
    checks[`model offered (${m.id})`] = { ok: offered.includes(m.id), detail: session?.result ? (offered.includes(m.id) ? 'offered' : 'not offered') : 'no session' };
  }
  return checks;
}

// use: one real use of a host tool, with a fresh home and the tool's own directory first on PATH.
async function use(exe, args, check) {
  const dir = await home();
  try {
    const env = { ...baseEnv(dir), PATH: `${dirname(exe)}:${process.env.PATH}` };
    const out = execFileSync(args[0] === '' ? exe : join(dirname(exe), args[0]), args.slice(1), { env, cwd: dir, timeout: 60000, encoding: 'utf8' });
    return { ok: check(out), detail: out.trim().split('\n')[0].slice(0, 200) };
  } catch (err) {
    return { ok: false, detail: String(err.message).slice(0, 300) };
  } finally {
    await rm(dir, { recursive: true, force: true });
  }
}

const tools = {
  node: async exe => ({
    'runs a script': await use(exe, ['', '-e', 'console.log(JSON.stringify({ok: typeof fetch}))'], out => out.includes('"function"')),
    'npm through its link': await use(exe, ['npm', '--version'], out => /^\d+\.\d+\.\d+/.test(out.trim())),
  }),
  pnpm: async exe => ({ 'store path': await use(exe, ['', 'store', 'path'], out => out.trim().length > 0) }),
  gh: async exe => ({ 'help': await use(exe, ['', 'help'], out => out.includes('gh <command>')) }),
  glab: async exe => ({ 'help': await use(exe, ['', 'help'], out => /glab/i.test(out)) }),
};

const run = kind === 'cli' ? { codex: codexChecks, claude: claudeChecks, antigravity: antigravityChecks } : tools;

async function qualify(label, pins) {
  const out = {};
  for (const [name, asset] of Object.entries(pins.targets?.[target] ?? {})) {
    const dir = await mkdtemp(join(tmpdir(), `fleet-${label}-${name}-`));
    const entry = { version: asset.version, checks: {} };
    try {
      const exe = await install(dir, name, asset);
      entry.checks.install = { ok: true, detail: 'digest verified' };
      entry.checks.version = await versionCheck(exe, asset.version);
      Object.assign(entry.checks, await run[name](exe));
    } catch (err) {
      entry.checks.install = { ok: false, detail: String(err.message).slice(0, 300) };
    }
    await rm(dir, { recursive: true, force: true });
    out[name] = entry;
  }
  return out;
}

const current = await qualify('current', JSON.parse(await readFile(currentPath)));
const candidate = await qualify('candidate', JSON.parse(await readFile(candidatePath)));
const result = { kind, target, ok: true, harnesses: {} };
for (const name of Object.keys(candidate)) {
  const was = current[name] ?? { checks: {} };
  const now = candidate[name];
  const failures = [];
  for (const [check, c] of Object.entries(now.checks)) {
    const must = check === 'install' || check === 'version' || kind === 'tool';
    if (!c.ok && (must || was.checks[check]?.ok)) failures.push(`${check}: ${c.detail}`);
  }
  for (const [check, c] of Object.entries(was.checks)) {
    if (c.ok && !now.checks[check]) failures.push(`${check}: not reached`);
  }
  result.harnesses[name] = { from: was.version, to: now.version, failures, current: was.checks, candidate: now.checks };
  if (failures.length) result.ok = false;
}
await writeFile(outPath, JSON.stringify(result, null, 2) + '\n');

const lines = [`### ${kind} ${target}: ${result.ok ? 'qualified' : 'failed'}`, '', '| CLI | Check | Current | Candidate |', '|---|---|---|---|'];
for (const [name, h] of Object.entries(result.harnesses)) {
  const names = [...new Set([...Object.keys(h.current), ...Object.keys(h.candidate)])];
  for (const check of names) {
    const cell = c => (c ? `${c.ok ? 'pass' : 'fail'}: ${String(c.detail).replaceAll('|', '/').replaceAll('\n', ' ').slice(0, 120)}` : '-');
    lines.push(`| ${name} ${h.from} → ${h.to} | ${check} | ${cell(h.current[check])} | ${cell(h.candidate[check])} |`);
  }
}
console.log(lines.join('\n'));
if (process.env.GITHUB_STEP_SUMMARY) await writeFile(process.env.GITHUB_STEP_SUMMARY, lines.join('\n') + '\n', { flag: 'a' });
