// Query only the vendors' release metadata. The updater verifies every artifact before use.
import { readFile, writeFile } from 'node:fs/promises';

const path = new URL('../config/harness-pins.json', import.meta.url);
const pins = JSON.parse(await readFile(path));
const claude = 'https://downloads.claude.ai/claude-code-releases';
const agy = 'https://antigravity-cli-auto-updater-974169037036.us-central1.run.app/manifests';
const github = 'https://api.github.com/repos/openai/codex/releases/latest';

// A GitHub API read carries the workflow's token when there is one: a runner's address shares the keyless limit.
async function get(url) {
  const headers = url.startsWith('https://api.github.com/') && process.env.GH_TOKEN ? { authorization: `Bearer ${process.env.GH_TOKEN}` } : {};
  const response = await fetch(url, { headers, signal: AbortSignal.timeout(30000) });
  if (!response.ok) throw Error(`${url}: HTTP ${response.status}`);
  return response;
}

function version(value) {
  if (!/^\d+\.\d+\.\d+$/.test(value)) throw Error(`Invalid stable version: ${value}`);
  return value;
}

function digest(value, length) {
  if (typeof value !== 'string' || value.length !== length || !/^[0-9a-f]+$/.test(value)) {
    throw Error('Missing or invalid release digest');
  }
  return value;
}

// A version the watch reverted (pins.skip, deploy/pins-revert.mjs) is never proposed again.
function keepNewer(current, next, name) {
  if (pins.skip?.[name]?.includes(next.version)) return current;
  const old = version(current.version).split('.').map(Number);
  const candidate = version(next.version).split('.').map(Number);
  const first = candidate.findIndex((part, i) => part !== old[i]);
  return first >= 0 && candidate[first] > old[first] ? next : current;
}

const [codexRelease, claudeVersion] = await Promise.all([
  get(github).then(r => r.json()),
  get(`${claude}/latest`).then(r => r.text()).then(v => version(v.trim())),
]);
const claudeRelease = await get(`${claude}/${claudeVersion}/manifest.json`).then(r => r.json());
if (claudeRelease.version !== claudeVersion) throw Error('Claude manifest version mismatch');
const codexVersion = version(codexRelease.tag_name.replace(/^rust-v/, ''));
const platform = {
  'linux-amd64': ['x86_64-unknown-linux-musl', 'linux-x64', 'linux_amd64'],
  'darwin-arm64': ['aarch64-apple-darwin', 'darwin-arm64', 'darwin_arm64'],
};
const antigravity = await Promise.all(Object.values(platform).map(([, , id]) => get(`${agy}/${id}.json`).then(r => r.json())));
for (const [index, [target, [triple, claudeTarget]]] of Object.entries(platform).entries()) {
  const current = pins.targets[target];
  const name = `codex-package-${triple}.tar.gz`;
  const asset = codexRelease.assets.find(asset => asset.name === name);
  const codexUrl = `https://github.com/openai/codex/releases/download/rust-v${codexVersion}/${name}`;
  if (asset?.browser_download_url !== codexUrl) throw Error(`Missing Codex target ${target}`);
  current.codex = keepNewer(current.codex, {
    version: codexVersion, url: codexUrl, sha256: digest(asset.digest?.replace(/^sha256:/, ''), 64), entry: 'bin/codex',
  }, 'codex');
  current.claude = keepNewer(current.claude, {
    version: claudeVersion, url: `${claude}/${claudeVersion}/${claudeTarget}/claude`,
    sha256: digest(claudeRelease.platforms[claudeTarget]?.checksum, 64),
  }, 'claude');
  // The vendor can roll out manifests separately. Keep both targets on the same release.
  if (antigravity[0].version === antigravity[1].version) {
    const a = antigravity[index];
    if (!a.url.startsWith('https://storage.googleapis.com/antigravity-public/antigravity-cli/')) {
      throw Error(`Unexpected Antigravity asset origin for ${target}`);
    }
    current.antigravity = keepNewer(current.antigravity, {
      version: a.version, url: a.url, sha512: digest(a.sha512, 128), entry: 'antigravity',
    }, 'antigravity');
  }
}

await writeFile(path, JSON.stringify(pins, null, 2) + '\n');
