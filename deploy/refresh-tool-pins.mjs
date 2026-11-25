// Discover the newest stable host tool releases (docs/designs/fleet-host-updates.md, step 1) and write them to
// config/tool-pins.json. Only official release metadata is read: the Node.js dist index and SHASUMS256.txt, the
// GitHub releases of pnpm and gh (asset digests), and the GitLab releases of glab (checksums.txt). fleet-update
// verifies every payload against these digests before use.
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
  if (!/^\d+\.\d+\.\d+$/.test(value)) throw Error(`Invalid stable version: ${value}`);
  return value;
}

function digest(value) {
  if (typeof value !== 'string' || !/^[0-9a-f]{64}$/.test(value)) throw Error('Missing or invalid release digest');
  return value;
}

function keepNewer(current, next) {
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
  'linux-amd64': { node: 'linux-x64', pnpm: 'linux-x64', gh: ['linux_amd64', 'tar.gz'], glab: 'linux_amd64' },
  'darwin-arm64': { node: 'darwin-arm64', pnpm: 'darwin-arm64', gh: ['macOS_arm64', 'zip'], glab: 'darwin_arm64' },
};

// Node.js: the newest release of the newest LTS line.
const index = await get('https://nodejs.org/dist/index.json').then(r => r.json());
const lts = index.find(r => r.lts);
const nodeVersion = version(lts.version.replace(/^v/, ''));
const nodeSums = await get(`https://nodejs.org/dist/v${nodeVersion}/SHASUMS256.txt`).then(r => r.text());
const sums = text => Object.fromEntries(text.trim().split('\n').map(line => line.trim().split(/\s+/).reverse()));

const [pnpm, gh] = await Promise.all([github('pnpm/pnpm'), github('cli/cli')]);
const pnpmVersion = version(pnpm.tag_name.replace(/^v/, ''));
const ghVersion = version(gh.tag_name.replace(/^v/, ''));

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
  });
  current.pnpm = keepNewer(current.pnpm, { version: pnpmVersion, ...githubAsset(pnpm, `pnpm-${p.pnpm}.tar.gz`), entry: 'pnpm' });
  const ghDir = `gh_${ghVersion}_${p.gh[0]}`;
  current.gh = keepNewer(current.gh, { version: ghVersion, ...githubAsset(gh, `${ghDir}.${p.gh[1]}`), entry: `${ghDir}/bin/gh` });
  const glabFile = `glab_${glabVersion}_${p.glab}.tar.gz`;
  const glabUrl = glabLink(glabFile);
  if (!glabUrl?.startsWith('https://gitlab.com/gitlab-org/cli/-/releases/')) throw Error(`Missing glab target ${target}`);
  current.glab = keepNewer(current.glab, { version: glabVersion, url: glabUrl, sha256: digest(glabSums[glabFile]), entry: 'bin/glab' });
}

await writeFile(path, JSON.stringify(pins, null, 2) + '\n');
