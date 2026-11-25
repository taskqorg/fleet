// Step 10 of the host update procedure (docs/designs/fleet-host-updates.md): turn the coordinator's watch signals into
// a revert. The coordinator marks a landed proposal's head with the failed status pins-watch, description
// "revert <component> <version>: <why>". For each such mark on a proposal of this kind merged in the last 7 days whose
// version main still pins, put the pin back to the proposal's base version on every target and add the version to
// skip, so discovery never proposes it again. The result is proposed and qualified like any other pin change.
// Usage: node deploy/pins-revert.mjs <cli|tool> <pins file>; reads GitHub with GH_TOKEN.
import { readFile, writeFile } from 'node:fs/promises';

const [kind, file] = process.argv.slice(2);
const repo = process.env.GITHUB_REPOSITORY || 'taskqorg/fleet';
const headers = { accept: 'application/vnd.github+json', ...(process.env.GH_TOKEN ? { authorization: `Bearer ${process.env.GH_TOKEN}` } : {}) };
async function api(path, accept) {
  const r = await fetch(`https://api.github.com/repos/${repo}/${path}`, { headers: accept ? { ...headers, accept } : headers, signal: AbortSignal.timeout(30000) });
  if (!r.ok) throw Error(`${path}: HTTP ${r.status}`);
  return accept ? r.text() : r.json();
}

const pins = JSON.parse(await readFile(file));
const owner = repo.split('/')[0];
const pulls = await api(`pulls?state=closed&head=${owner}:automation/${kind}-pins&per_page=20`);
const week = Date.now() - 7 * 24 * 3600 * 1000;
let changed = false;
for (const pr of pulls) {
  if (!pr.merged_at || Date.parse(pr.merged_at) < week || pr.head.repo?.full_name !== repo) continue;
  const status = await api(`commits/${pr.head.sha}/status`);
  for (const s of status.statuses.filter(s => s.context === 'pins-watch' && s.state === 'failure')) {
    const m = /^revert ([a-z]+) (\d+\.\d+\.\d+):/.exec(s.description ?? '');
    if (!m) continue;
    const [, name, version] = m;
    const base = JSON.parse(await api(`contents/${file}?ref=${pr.base.sha}`, 'application/vnd.github.raw+json'));
    for (const [target, assets] of Object.entries(pins.targets)) {
      const previous = base.targets?.[target]?.[name];
      if (assets[name]?.version !== version || !previous) continue;
      assets[name] = previous;
      changed = true;
    }
    pins.skip ??= {};
    pins.skip[name] = [...new Set([...(pins.skip[name] ?? []), version])].sort();
    console.log(`revert ${kind}/${name} ${version}: ${s.description}`);
  }
}
if (changed) await writeFile(file, JSON.stringify(pins, null, 2) + '\n');
