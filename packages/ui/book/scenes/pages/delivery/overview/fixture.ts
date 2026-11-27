import { dayStart, type StateOf } from '../../../../../src/index.ts';
import { NOW, min, hr } from '../../../../fixtures.ts';

type Stage = StateOf<'delivery'>;
const DAY = 24 * hr;

/* ---------- Fixture history: 9 recorded days shaped like the live delivery_history of 17 Nov 2026 ---------- */
// Merged per day 35, 150, 143, 131, 30, 18, 26, 34 and 7 so far today. The first two days have no queue times and a
// release that took hours (the backfill: about 31 h and 9 h medians); install times exist from the sixth day, and on
// that day for 9 of 18 pull requests only; the eighth day has three pull requests whose checks ran over 11 hours.
export type Hist = { n: number; title: string; merged: number; d: { pr_ci: number; queue?: number; release: number; install?: number; total?: number }; removals: number; flakes: number };
const MERGED = [35, 150, 143, 131, 30, 18, 26, 34, 7];
export const D0 = dayStart(NOW, -(MERGED.length - 1));
// Install times are recorded from noon of the sixth day: that day is measured for its afternoon only (9 of 18).
export const REC = D0 + 5 * DAY + 12 * hr;
const rnd = (() => { let s = 0x2545f491; return () => { s ^= s << 13; s ^= s >>> 17; s ^= s << 5; return ((s >>> 0) % 1e6) / 1e6; }; })();
const logn = (median: number, spread: number) => { const z = Math.sqrt(-2 * Math.log(rnd() || 1e-6)) * Math.cos(2 * Math.PI * rnd()); return median * Math.exp(spread * z); };
const TITLES = ['Delivery: day axis in the viewer zone', 'fix: lease renewal after host sleep', 'Gateway: drain before snapshot swap', 'Lists: row menu on long press', 'fix: interface history fixture',
  'Spotlight: jump by entity code', 'Hosts: limits card projection', 'fix: queue flake in browser tests', 'Conversations: archived group', 'Docs: delivery health metrics', 'fix: resource replacement throughput', 'Results: copy state chips'];
export const HIST: Hist[] = [];
{
  const ci = [20, 7.5, 10.4, 6.7, 2.4, 2.3, 7.8, 18, 4], q = [0, 0, 4.6, 5.2, 5.5, 6.6, 8.5, 9.2, 7], rel = [1850, 551, 2.8, 1.8, 0.9, 1, 0.9, 1.1, 1.2];
  let n = 612 - 9 - MERGED.reduce((a, b) => a + b, 0);
  MERGED.forEach((count, di) => {
    const start = D0 + di * DAY, span = di === MERGED.length - 1 ? Math.max(hr, NOW - start - 30 * min) : DAY;
    for (let k = 0; k < count; k++) {
      const merged = start + span * (k + 0.5) / count, outlier = di === 7 && k % 8 === 3;
      const d: Hist['d'] = { pr_ci: (outlier ? 690 : logn(ci[di], 0.9)) * min, release: logn(rel[di], 0.25) * min };
      if (di >= 2) d.queue = logn(q[di], 0.5) * min;
      if (di >= 6 || (di === 5 && merged >= REC)) { d.install = logn(1.1, 0.3) * min; d.total = d.pr_ci + d.queue! + d.release + d.install; }
      const removals = rnd() < 0.12 ? 1 + Math.floor(rnd() * 2) : 0;
      HIST.push({ n: ++n, title: TITLES[n % TITLES.length], merged, d, removals, flakes: removals && rnd() < 0.4 ? 1 : 0 });
    }
  });
}

/* ---------- Current pull requests: 9 in flight and the latest installed ---------- */
export type Pr = { n: number; title: string; stage: Stage; since: number; opened: number; runs: number; removals: number; release?: string; author: string; repo?: string };
const INSTALLED: Pr[] = HIST.slice(-26).reverse().map((h, i) => ({ n: h.n, title: h.title, stage: 'installed', since: h.merged + (h.d.release + (h.d.install ?? 0)), opened: h.merged - h.d.pr_ci - (h.d.queue ?? 0), runs: 1 + h.removals, removals: h.removals, release: `1.261117.${30 - i}`, author: i % 3 ? 'agent' : 'kirs' }));
export const PRS: Pr[] = [
  { n: 612, title: 'Delivery: pace by calendar day', stage: 'pr_failed', since: NOW - 25 * min, opened: NOW - 70 * min, runs: 0, removals: 0, author: 'agent' },
  { n: 611, title: 'Gateway: drain before snapshot swap', stage: 'mq_failed', since: NOW - 9 * min, opened: NOW - 48 * min, runs: 2, removals: 1, author: 'agent' },
  { n: 610, title: 'Version beside the person; one reliable update control', stage: 'mq_wait', since: NOW - 11 * min, opened: NOW - 95 * min, runs: 1, removals: 0, author: 'kirs' },
  { n: 609, title: 'Lists: row menu, selection mode, Pinned group', stage: 'pr_ci', since: NOW - 14 * min, opened: NOW - 16 * min, runs: 0, removals: 0, author: 'agent' },
  { n: 608, title: 'Hosts: limits card with projection', stage: 'pr_open', since: NOW - 2 * hr, opened: NOW - 5 * hr, runs: 1, removals: 1, author: 'agent' },
  { n: 607, title: 'fix: lease renewal after host sleep', stage: 'releasing', since: NOW - 3 * min, opened: NOW - 41 * min, runs: 1, removals: 0, author: 'agent' },
  { n: 606, title: 'Results: copy state chips', stage: 'installing', since: NOW - 4 * min, opened: NOW - 52 * min, runs: 1, removals: 0, release: '1.261117.31', author: 'kirs' },
  { n: 251, title: 'Catalogue: materials and direct entry', stage: 'pr_open', since: NOW - 2 * DAY, opened: NOW - 2 * DAY, runs: 0, removals: 0, author: 'kirs', repo: 'Amoenum/desiign' },
  { n: 250, title: 'Catalogue: frame step before the export', stage: 'pr_open', since: NOW - 8 * DAY, opened: NOW - 8 * DAY, runs: 0, removals: 0, author: 'kirs', repo: 'Amoenum/desiign' },
  ...INSTALLED,
];
