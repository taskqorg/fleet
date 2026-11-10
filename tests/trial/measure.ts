// Trial measurement protocol (docs/designs/fleet-stack.md#trial-measurement-protocol). One script for the baseline and for
// both ports: the arm file tests/trial/arms/<arm>.json names its commands, everything else is the same.
//   node tests/trial/measure.ts typescript [--runs 5] [--worktrees 4]
// It works in fresh git worktrees of HEAD (commit the arm first), never in this checkout. It refuses nothing: a run that
// starts or ends on a busy machine is recorded and marked comparable=false. Results go to stdout as a table and to
// $TMPDIR/fleet-trial/<arm>-<time>.json.
import {execFileSync, spawn} from 'node:child_process';
import {appendFileSync, existsSync, mkdirSync, readFileSync, statSync, writeFileSync} from 'node:fs';
import {availableParallelism, cpus, tmpdir} from 'node:os';
import {join} from 'node:path';

const args = process.argv.slice(2);
const armName = args[0]; if (!armName) throw Error('usage: node tests/trial/measure.ts <arm> [--runs 5] [--worktrees 4]');
const opt = (k: string, d: number) => { const i = args.indexOf(k); return i < 0 ? d : Number(args[i + 1]); };
const RUNS = opt('--runs', 5), N = opt('--worktrees', 4); // trial run plan: 5 runs per arm, N = 4 worktrees
const root = new URL('../../', import.meta.url).pathname;
type Arm = {setup: string, clean: string, check: string, build: string, edit: string, server: string, artifact: string[], runtime?: string, lockfiles: string[], roots?: string[], disk: string[]};
const arm: Arm = JSON.parse(readFileSync(join(root, 'tests/trial/arms', armName + '.json'), 'utf8'));
const sh = (cmd: string, cwd: string) => execFileSync('sh', ['-c', cmd], {cwd, encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'], maxBuffer: 1 << 28});
const ms = (t: number) => Math.round(performance.now() - t);

// Machine load. Not comparable: a 1-minute load average above half the logical CPUs, or another process above one core.
const CPUS = availableParallelism();
function load() {
  const avg = sh('sysctl -n vm.loadavg 2>/dev/null || cat /proc/loadavg', root).replace(/[{}]/g, '').trim().split(/\s+/).slice(0, 3).map(Number);
  const own = new Set<number>([process.pid]);
  const heavy = sh('ps -A -o pid=,ppid=,%cpu=,comm=', root).split('\n').map(l => l.trim().match(/^(\d+)\s+(\d+)\s+([\d.]+)\s+(.*)$/)).filter(Boolean)
    .map(m => ({pid: Number(m![1]), ppid: Number(m![2]), cpu: Number(m![3]), comm: m![4].split('/').pop()!})).filter(p => p.cpu > 100 && !own.has(p.ppid) && !own.has(p.pid))
    .map(p => `${p.comm}:${Math.round(p.cpu)}%`);
  return {load1: avg[0], load5: avg[1], load15: avg[2], heavy, quiet: avg[0] <= CPUS / 2 && heavy.length === 0};
}

// Peak resident size of the largest single process a command starts (/usr/bin/time: -l on macOS, -v on Linux).
function timedRss(cmd: string, cwd: string) {
  const t = performance.now(), mac = process.platform === 'darwin';
  const r = spawnSyncText('/usr/bin/time', [mac ? '-l' : '-v', 'sh', '-c', cmd], cwd);
  if (r.code) throw Error(`${cmd} failed:\n${r.stderr.slice(-2000)}`);
  const m = mac ? r.stderr.match(/(\d+)\s+maximum resident set size/) : r.stderr.match(/Maximum resident set size \(kbytes\): (\d+)/);
  return {ms: ms(t), peakRssKb: m ? Math.round(Number(m[1]) / (mac ? 1024 : 1)) : null};
}
function spawnSyncText(cmd: string, a: string[], cwd: string) {
  try { const stderr = execFileSync(cmd, a, {cwd, encoding: 'utf8', stdio: ['ignore', 'ignore', 'pipe'], maxBuffer: 1 << 28}); return {code: 0, stderr: String(stderr ?? '')}; }
  catch (e: any) { return {code: e.status ?? 1, stderr: String(e.stderr ?? '')}; }
}

// The one-line edit: a comment line appended to the arm's edit file, removed afterwards.
const edit = (wt: string, n: number) => { const f = join(wt, arm.edit), old = readFileSync(f, 'utf8'); appendFileSync(f, `\n// trial edit ${n}\n`); return () => writeFileSync(f, old); };

// One outside-test run of the slice (tests/trial/run.ts) in a worktree; returns its own timing line.
function outside(wt: string): Promise<{ok: boolean, line: Record<string, string>}> {
  return new Promise(res => { let out = '';
    const p = spawn(process.execPath, [join(wt, 'tests/trial/run.ts')], {cwd: wt, env: {...process.env, FLEET_TRIAL_SERVER: arm.server}, stdio: ['ignore', 'pipe', 'pipe']});
    p.stdout.on('data', d => out += d); p.stderr.on('data', d => out += d);
    p.on('exit', code => { const m = out.match(/trial-run (.*)$/m); res({ok: code === 0, line: Object.fromEntries((m?.[1] ?? '').split(' ').filter(Boolean).map(kv => kv.split('=')))}); }); });
}
function runAsync(cmd: string, cwd: string) { return new Promise<boolean>(res => spawn('sh', ['-c', cmd], {cwd, stdio: 'ignore'}).on('exit', c => res(c === 0))); }

// Third-party packages: the whole lockfile, and the closure of the arm's roots (its coordinator dependencies) when given.
function modules(): Record<string, number> {
  const out: Record<string, number> = {};
  for (const f of arm.lockfiles) {
    const text = readFileSync(join(root, f), 'utf8');
    if (f.endsWith('pnpm-lock.yaml')) {
      const snap = text.slice(text.indexOf('\nsnapshots:\n'));
      const deps = new Map<string, string[]>();
      let cur = '';
      for (const line of snap.split('\n')) {
        const top = line.match(/^  '?(.+?@[^@(':]+)(\(.*\))?'?:$/); if (top) { cur = top[1]; deps.set(cur, []); continue; }
        const dep = line.match(/^      '?([^':]+)'?: '?([^'(\s]+)/); if (dep && cur) deps.get(cur)!.push(dep[1] + '@' + dep[2]);
      }
      out[f + ' (all)'] = deps.size;
      if (arm.roots) {
        const importer = text.slice(text.indexOf('\nimporters:\n'), text.indexOf('\npackages:\n'));
        const seen = new Set<string>(), stack = arm.roots.map(r => { const m = importer.match(new RegExp(`\\n      '?${r.replace(/[/.]/g, '\\$&')}'?:\\n        specifier: .*\\n        version: ([^(\\s]+)`)); if (!m) throw Error('root not in lockfile: ' + r); return r + '@' + m[1]; });
        while (stack.length) { const k = stack.pop()!; if (seen.has(k)) continue; seen.add(k); stack.push(...(deps.get(k) ?? [])); }
        out[f + ' (coordinator roots)'] = seen.size;
      }
    } else if (f.endsWith('Cargo.lock')) {
      const pkgs = text.split('[[package]]').slice(1).map(b => ({name: b.match(/name = "(.+)"/)![1], version: b.match(/version = "(.+)"/)![1], third: /\nsource = /.test(b), deps: [...b.matchAll(/^ "([^ "]+)/gm)].map(m => m[1])}));
      out[f + ' (all)'] = pkgs.filter(p => p.third).length;
      if (arm.roots) { const seen = new Set<string>(), stack = [...arm.roots];
        while (stack.length) { const n = stack.pop()!; if (seen.has(n)) continue; seen.add(n); stack.push(...(pkgs.find(p => p.name === n)?.deps ?? [])); }
        out[f + ' (coordinator roots)'] = pkgs.filter(p => p.third && seen.has(p.name)).length; }
    } else if (f.endsWith('go.sum')) {
      out[f + ' (all)'] = new Set(text.split('\n').filter(l => l && !/\/go\.mod /.test(l)).map(l => l.split(' ')[0])).size;
    } else throw Error('unknown lockfile kind ' + f);
  }
  return out;
}

const median = (xs: number[]) => { const s = [...xs].sort((a, b) => a - b); return s.length ? s[Math.floor((s.length - 1) / 2)] : NaN; };
const spread = (xs: number[]) => xs.length ? `${Math.min(...xs)}-${Math.max(...xs)}` : '';

// Worktrees: fresh checkouts of HEAD, set up once, removed at the end.
const base = join(tmpdir(), 'fleet-trial-worktrees');
mkdirSync(base, {recursive: true});
const head = sh('git rev-parse HEAD', root).trim();
const wts = Array.from({length: N}, (_, i) => join(base, `${armName}-${i}`));
for (const wt of wts) { if (existsSync(wt)) sh(`git worktree remove --force ${wt} || rm -rf ${wt}`, root); sh(`git worktree add --detach ${wt} ${head}`, root); }
const started = {at: new Date().toISOString(), ...load()};
console.error(`measure ${armName} head=${head.slice(0, 7)} runs=${RUNS} worktrees=${N} cpus=${CPUS} (${cpus()[0]?.model}) load=${started.load1}/${started.load5}/${started.load15} heavy=${started.heavy.join(',') || 'none'}`);
for (const wt of wts) sh(arm.setup, wt);
const w0 = wts[0];

const runs: any[] = [];
for (let r = 0; r < RUNS; r++) {
  const before = load();
  sh(arm.clean, w0);
  const cold = timedRss(arm.build, w0);
  const warm = timedRss(arm.build, w0);
  const undo = edit(w0, r); const rebuild = timedRss(arm.build, w0); undo();
  const test = await outside(w0);
  // N worktrees at once: the same one-line edit in each, then check and the outside tests; edit-to-green per worktree.
  const par = await Promise.all(wts.map(async (wt, i) => { const t = performance.now(); const back = edit(wt, r * 100 + i);
    const built = await runAsync(arm.check, wt); const t1 = ms(t); const green = built && (await outside(wt)).ok; back(); return {ok: green, checkMs: t1, ms: ms(t)}; }));
  const after = load();
  const run = {run: r + 1, comparable: before.quiet && after.quiet, loadStart: before, loadEnd: after, coldMs: cold.ms, coldPeakRssKb: cold.peakRssKb, warmMs: warm.ms, rebuildMs: rebuild.ms, rebuildPeakRssKb: rebuild.peakRssKb,
    outsideOk: test.ok, outsideTotalMs: Number(test.line.total_ms), outsideSetupMs: Number(test.line.setup_ms), outsideTestsMs: Number(test.line.tests_ms), serverPeakRssKb: Number(test.line.server_peak_rss_kb),
    parallelOk: par.every(p => p.ok), parallelEditToGreenMs: par.map(p => p.ms), parallelCheckMs: par.map(p => p.checkMs)};
  runs.push(run);
  console.error(`run ${r + 1}: comparable=${run.comparable} load ${before.load1}->${after.load1} cold=${cold.ms} warm=${warm.ms} rebuild=${rebuild.ms} outside=${run.outsideTotalMs}${test.ok ? '' : ' RED'} parallel=${run.parallelEditToGreenMs.join('/')}${run.parallelOk ? '' : ' RED'}`);
}
const size = Object.fromEntries(arm.artifact.map(a => [a, statSync(join(w0, a)).size]));
const runtime = arm.runtime ? {[arm.runtime]: statSync(sh(`command -v ${arm.runtime}`, w0).trim()).size} : {};
const disk = Number(sh(`du -sk ${arm.disk.filter(d => existsSync(join(w0, d))).join(' ')} | awk '{s+=$1} END {print s}'`, w0).trim());
const result = {arm: armName, head, cpus: CPUS, started, runs, sizeBytes: size, runtimeBytes: runtime, diskPerWorktreeKb: disk, modules: modules()};
for (const wt of wts) sh(`git worktree remove --force ${wt}`, root);

const ok = runs.filter(r => r.outsideOk && r.parallelOk);
const col = (k: string) => ok.map(r => r[k]).filter((x: any) => Number.isFinite(x));
const rows: [string, number[]][] = [['cold build ms', col('coldMs')], ['warm build ms', col('warmMs')], ['rebuild after one-line edit ms', col('rebuildMs')],
  ['compiler peak RSS KB (cold)', col('coldPeakRssKb')], ['outside tests incl. setup ms', col('outsideTotalMs')], ['outside tests only ms', col('outsideTestsMs')],
  ['server peak RSS KB', col('serverPeakRssKb')], [`edit-to-green, ${N} worktrees at once, ms (all worktrees)`, ok.flatMap(r => r.parallelEditToGreenMs)]];
console.log(`\n| ${armName} | median | spread |\n|---|---|---|`);
for (const [k, xs] of rows) console.log(`| ${k} | ${median(xs)} | ${spread(xs)} |`);
for (const [k, v] of Object.entries(size)) console.log(`| size ${k} bytes | ${v} | |`);
for (const [k, v] of Object.entries(runtime)) console.log(`| runtime ${k} bytes | ${v} | |`);
console.log(`| disk per worktree KB (${arm.disk.join(', ')}) | ${disk} | |`);
for (const [k, v] of Object.entries(result.modules)) console.log(`| third-party packages, ${k} | ${v} | |`);
console.log(`\nruns: ${runs.length}, green: ${ok.length}, comparable: ${runs.filter(r => r.comparable).length}; load at start ${started.load1}/${started.load5}/${started.load15} on ${CPUS} CPUs; heavy: ${started.heavy.join(', ') || 'none'}`);
const outDir = join(tmpdir(), 'fleet-trial'); mkdirSync(outDir, {recursive: true});
const file = join(outDir, `${armName}-${Date.now()}.json`); writeFileSync(file, JSON.stringify(result, null, 1)); console.log('results: ' + file);
