// Discover the newest stable host tool releases (docs/designs/fleet-host-updates.md, step 1) and write them to
// config/tool-pins.json. Only official release metadata is read: the Node.js dist index and SHASUMS256.txt, the
// GitHub releases of pnpm, gh, jq, ripgrep and python-build-standalone (asset digests), the GitLab releases of glab
// (checksums.txt), the npm registry entry of playwright-core (its sha512 integrity), and the Chrome for Testing Stable
// channel. Chrome for Testing publishes no digest, so its digest is the SHA-256 of the archive as downloaded from
// Google's bucket here. fleet-update verifies every payload against these digests before use.
import { createHash } from 'node:crypto';
import { readFile, writeFile } from 'node:fs/promises';

const path = new URL('../config/tool-pins.json', import.meta.url);
const pins = JSON.parse(await readFile(path));

async function get(url, headers = {}) {
  const response = await fetch(url, { headers, signal: AbortSignal.timeout(30000) });
  if (!response.ok) throw Error(`${url}: HTTP ${response.status}`);
  return response;
}
const github = repo => get(`https://api.github.com/repos/${repo}/releases/latest`, process.env.GH_TOKEN ? { authorization: `Bearer ${process.env.GH_TOKEN}` } : {}).then(r => r.json());

function version(value) {
  if (!/^\d+\.\d+\.\d+(\.\d+)?$/.test(value)) throw Error(`Invalid stable version: ${value}`);
  return value;
}

function digest(value) {
  if (typeof value !== 'string' || !/^[0-9a-f]{64}$/.test(value)) throw Error('Missing or invalid release digest');
  return value;
}

// A version the watch reverted (pins.skip, deploy/pins-revert.mjs) is never proposed again.
function keepNewer(current, next, name) {
  if (pins.skip?.[name]?.includes(next.version)) return current;
  if (!current) return next;
  const old = version(current.version).split('.').map(Number);
  const candidate = version(next.version).split('.').map(Number);
  const first = candidate.findIndex((part, i) => part !== old[i]);
  return first >= 0 && candidate[first] > old[first] ? next : current;
}

function githubAsset(release, name) {
  const asset = release.assets.find(a => a.name === name);
  if (!asset) throw Error(`Missing asset ${name} in ${release.tag_name}`);
  return { url: asset.browser_download_url, sha256: digest(asset.digest?.replace(/^sha256:/, '')) };
}

const platform = {
  'linux-amd64': {
    node: 'linux-x64', pnpm: 'linux-x64', gh: ['linux_amd64', 'tar.gz'], glab: 'linux_amd64', jq: 'linux-amd64',
    rg: 'x86_64-unknown-linux-musl', python: 'x86_64-unknown-linux-gnu', chromium: 'linux64',
  },
  'darwin-arm64': {
    node: 'darwin-arm64', pnpm: 'darwin-arm64', gh: ['macOS_arm64', 'zip'], glab: 'darwin_arm64', jq: 'macos-arm64',
    rg: 'aarch64-apple-darwin', python: 'aarch64-apple-darwin', chromium: 'mac-arm64',
  },
};

// sha256 of a download, for a vendor that publishes no digest (Chrome for Testing).
async function sha256Of(url) {
  const hash = createHash('sha256');
  for await (const chunk of (await get(url)).body) hash.update(chunk);
  return hash.digest('hex');
}

// Node.js: the newest release of the newest LTS line.
const index = await get('https://nodejs.org/dist/index.json').then(r => r.json());
const lts = index.find(r => r.lts);
const nodeVersion = version(lts.version.replace(/^v/, ''));
const nodeSums = await get(`https://nodejs.org/dist/v${nodeVersion}/SHASUMS256.txt`).then(r => r.text());
const sums = text => Object.fromEntries(text.trim().split('\n').map(line => line.trim().split(/\s+/).reverse()));

const [pnpm, gh, jq, rg, python] = await Promise.all([
  github('pnpm/pnpm'), github('cli/cli'), github('jqlang/jq'), github('BurntSushi/ripgrep'), github('astral-sh/python-build-standalone'),
]);
const pnpmVersion = version(pnpm.tag_name.replace(/^v/, ''));
const ghVersion = version(gh.tag_name.replace(/^v/, ''));
const jqVersion = version(jq.tag_name.replace(/^jq-/, ''));
const rgVersion = version(rg.tag_name);

// Python: the newest stable CPython of the newest python-build-standalone release (no alpha, beta or candidate).
const pythonBuild = python.tag_name;
const pythonVersion = python.assets
  .map(a => a.name.match(/^cpython-(\d+\.\d+\.\d+)\+\d+-x86_64-unknown-linux-gnu-install_only\.tar\.gz$/)?.[1])
  .filter(Boolean)
  .sort((a, b) => { const x = a.split('.').map(Number), y = b.split('.').map(Number); return x[0] - y[0] || x[1] - y[1] || x[2] - y[2]; })
  .at(-1);
version(pythonVersion ?? '');

// Playwright: the latest playwright-core on npm. The project browser loads only its module and connects over CDP.
const playwright = await get('https://registry.npmjs.org/playwright-core/latest').then(r => r.json());
const playwrightVersion = version(playwright.version);
const playwrightSha512 = Buffer.from(playwright.dist.integrity.replace(/^sha512-/, ''), 'base64').toString('hex');
if (!playwright.dist.integrity.startsWith('sha512-') || playwrightSha512.length !== 128) throw Error('Missing playwright-core integrity');

// Chromium: chrome-headless-shell of the current Chrome for Testing Stable (the one browser Fleet supports).
const stable = (await get('https://googlechromelabs.github.io/chrome-for-testing/last-known-good-versions-with-downloads.json').then(r => r.json())).channels.Stable;
const chromiumVersion = version(stable.version);

const glabReleases = await get('https://gitlab.com/api/v4/projects/gitlab-org%2Fcli/releases?per_page=5').then(r => r.json());
const glab = glabReleases.find(r => !r.upcoming_release && /^v\d+\.\d+\.\d+$/.test(r.tag_name));
const glabVersion = version(glab.tag_name.replace(/^v/, ''));
const glabLink = name => glab.assets.links.find(l => l.name === name)?.direct_asset_url;
const glabSums = sums(await get(glabLink('checksums.txt')).then(r => r.text()));

for (const [target, p] of Object.entries(platform)) {
  const current = (pins.targets[target] ??= {});
  const nodeFile = `node-v${nodeVersion}-${p.node}.tar.gz`;
  current.node = keepNewer(current.node, {
    version: nodeVersion, url: `https://nodejs.org/dist/v${nodeVersion}/${nodeFile}`,
    sha256: digest(sums(nodeSums)[nodeFile]), entry: `node-v${nodeVersion}-${p.node}/bin/node`,
  }, 'node');
  current.pnpm = keepNewer(current.pnpm, { version: pnpmVersion, ...githubAsset(pnpm, `pnpm-${p.pnpm}.tar.gz`), entry: 'pnpm' }, 'pnpm');
  const ghDir = `gh_${ghVersion}_${p.gh[0]}`;
  current.gh = keepNewer(current.gh, { version: ghVersion, ...githubAsset(gh, `${ghDir}.${p.gh[1]}`), entry: `${ghDir}/bin/gh` }, 'gh');
  const glabFile = `glab_${glabVersion}_${p.glab}.tar.gz`;
  const glabUrl = glabLink(glabFile);
  if (!glabUrl?.startsWith('https://gitlab.com/gitlab-org/cli/-/releases/')) throw Error(`Missing glab target ${target}`);
  current.glab = keepNewer(current.glab, { version: glabVersion, url: glabUrl, sha256: digest(glabSums[glabFile]), entry: 'bin/glab' }, 'glab');
  current.jq = keepNewer(current.jq, { version: jqVersion, ...githubAsset(jq, `jq-${p.jq}`) }, 'jq');
  const rgDir = `ripgrep-${rgVersion}-${p.rg}`;
  current.rg = keepNewer(current.rg, { version: rgVersion, ...githubAsset(rg, `${rgDir}.tar.gz`), entry: `${rgDir}/rg` }, 'rg');
  const pythonFile = `cpython-${pythonVersion}+${pythonBuild}-${p.python}-install_only.tar.gz`;
  current.python = keepNewer(current.python, { version: pythonVersion, ...githubAsset(python, pythonFile), entry: 'python/bin/python' }, 'python');
  current.playwright = keepNewer(current.playwright, {
    version: playwrightVersion, url: playwright.dist.tarball, sha512: playwrightSha512, entry: 'package/cli.js',
  }, 'playwright');
  const chromiumUrl = stable.downloads['chrome-headless-shell']?.find(d => d.platform === p.chromium)?.url;
  if (!chromiumUrl?.startsWith('https://storage.googleapis.com/chrome-for-testing-public/')) throw Error(`Missing chromium target ${target}`);
  const chromiumEntry = `chrome-headless-shell-${p.chromium}/chrome-headless-shell`;
  if (current.chromium?.version !== chromiumVersion && !pins.skip?.chromium?.includes(chromiumVersion)) {
    current.chromium = keepNewer(current.chromium, { version: chromiumVersion, url: chromiumUrl, sha256: await sha256Of(chromiumUrl), entry: chromiumEntry }, 'chromium');
  }
}

await writeFile(path, JSON.stringify(pins, null, 2) + '\n');
