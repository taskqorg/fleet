// Fleet connections in a dev session (docs/designs/fleet-connections.md#4-per-run-tokens, owner connection tokens):
// one host's personal token names the connections, and its harness gets one `fleet-executor mcp connection <name>`
// MCP server per connection. This script is the one way to add a connection to a host's dev sessions (fleet-postgres,
// a new provider): it sets the token's connections (connections_token_set: the token keeps its value, so no file
// changes), stores the answered specs next to the token (<token>.spec) and registers the MCP servers with the harness's
// own CLI.
//
// Usage: node scripts/dev-connections.ts <token> <claude|codex> [connection ...]
//   connections are added to the token's current ones; with none, the harness is brought up to the token's list.
//   Claude Code: every server is registered again (user scope), so specs stay current. Codex: only missing servers are
//   added, so the per-tool approval settings of existing ones stay; remove one with `codex mcp remove <name>` to renew it.
// Needs the site access of AGENTS.md (~/.config/ufobe-agents/site-access.env), read here and never printed.
import { execFileSync } from 'node:child_process';
import { existsSync, readFileSync, writeFileSync } from 'node:fs';
import { homedir } from 'node:os';
import { join } from 'node:path';

const ORIGIN = 'https://fleet.ufobe.com';
const [token, harness, ...add] = process.argv.slice(2);
if (!token || !/^[a-z0-9][a-z0-9-]{0,62}$/.test(token) || (harness !== 'claude' && harness !== 'codex')) {
  console.error('usage: node scripts/dev-connections.ts <token> <claude|codex> [connection ...]');
  process.exit(2);
}
const tokens = join(homedir(), '.config', 'fleet', 'tokens');
const tokenFile = join(tokens, token);
if (!existsSync(tokenFile)) {
  console.error(`${tokenFile} is missing: mint the host's token first (connections_token_create)`);
  process.exit(2);
}
const executor = process.env.FLEET_EXECUTOR ?? join(homedir(), 'releases', 'current', 'fleet-executor');

/** One operation as site-agents; the Access values stay inside this process. */
async function op<T>(name: string, body: unknown): Promise<T> {
  const env = Object.fromEntries(readFileSync(join(homedir(), '.config', 'ufobe-agents', 'site-access.env'), 'utf8')
    .split('\n').map(l => l.replace(/^export\s+/, '').match(/^([A-Z_]+)=["']?([^"'\n]*)["']?$/)).filter(m => m !== null).map(m => [m[1], m[2]]));
  const r = await fetch(`${ORIGIN}/api/ops/${name}`, {
    method: 'POST', body: JSON.stringify(body),
    headers: { 'Content-Type': 'application/json', Origin: ORIGIN, 'CF-Access-Client-Id': env.CF_ACCESS_CLIENT_ID ?? '', 'CF-Access-Client-Secret': env.CF_ACCESS_CLIENT_SECRET ?? '' },
  });
  const reply = await r.json() as T & { message?: string };
  if (!r.ok) throw new Error(`${name}: ${r.status} ${reply.message ?? ''}`);
  return reply;
}

type Spec = { name: string } & Record<string, unknown>;
const list = await op<{ tokens: { name: string; connections: string[]; revokedAt?: unknown }[] }>('connections_token_list', {});
const current = list.tokens.find(t => t.name === token && !t.revokedAt);
if (!current) throw new Error(`no live token ${token}`);
const wanted = [...new Set([...current.connections, ...add])];
const set = await op<{ connections: Spec[]; refused?: string[] }>('connections_token_set', { name: token, connections: wanted });
writeFileSync(`${tokenFile}.spec`, JSON.stringify({ name: token, connections: set.connections }), { mode: 0o600 });
if (set.refused?.length) console.log(`refused: ${set.refused.join(', ')}`);

const env = { FLEET_GATEWAY_URL: ORIGIN, FLEET_TOKEN_FILE: tokenFile };
const run = (cmd: string, args: string[], quiet = false) => {
  try { execFileSync(cmd, args, { stdio: quiet ? 'ignore' : ['ignore', 'ignore', 'inherit'] }); return true; } catch { return false; }
};
for (const spec of set.connections) {
  const args = ['mcp', 'connection', spec.name, JSON.stringify(spec)];
  if (harness === 'claude') {
    const json = JSON.stringify({ type: 'stdio', command: executor, args, env });
    // Add first: an existing server is removed only to be replaced, so a refused add never loses one.
    let ok = run('claude', ['mcp', 'add-json', '-s', 'user', spec.name, json], true);
    if (!ok && run('claude', ['mcp', 'get', spec.name], true)) {
      ok = run('claude', ['mcp', 'remove', '-s', 'user', spec.name], true) && run('claude', ['mcp', 'add-json', '-s', 'user', spec.name, json]);
    }
    console.log(`${spec.name}: ${ok ? 'registered' : 'FAILED'} in Claude Code (user scope)`);
  } else if (run('codex', ['mcp', 'get', spec.name], true)) {
    console.log(`${spec.name}: kept in Codex`);
  } else {
    const ok = run('codex', ['mcp', 'add', spec.name, ...Object.entries(env).flatMap(([k, v]) => ['--env', `${k}=${v}`]), '--', executor, ...args]);
    console.log(`${spec.name}: ${ok ? 'added' : 'FAILED'} in Codex`);
  }
}
console.log(`token ${token}: ${set.connections.map(s => s.name).join(', ')}; new sessions get these tools`);
