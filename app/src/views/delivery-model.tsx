// Delivery domain types, URL extra parsing, pace buckets, and stage/history helpers.
import {
  day, dayStart, slotStart, hourLabel, dateKey, duration, quantile, DELIVERY_ORDER,
  type StateOf, type DayCol, type DayMark, type Stage as ChipStage,
  timeOf,
} from '../../../packages/ui/src/index.ts';
import type { Ops } from '../api.ts';
import type { DeliveryStage } from '../../../contracts/generated/ops.ts';
import { DELIVERY_DONE } from '../system-views.ts';
import type { View } from '../../../packages/view-url/view-url.ts';

export type Stage = StateOf<'delivery'>;
/** The stage chip of a delivery stage (live row, task detail). */
export const CHIP_STAGE: Record<DeliveryStage, ChipStage> = { pr_open: 'pr_open', pr_ci: 'checks', mq_wait: 'merging', mq_ci: 'merging', pr_failed: 'checks_failed', mq_failed: 'checks_failed', releasing: 'deploying', installing: 'deploying', installed: 'merged', merged: 'merged' };
export type Status = Ops['publishing_status']['reply'];
export type PublishingCapability = Status['capabilities'][number];
export type Item = Status['items'][number];
export type History = Ops['delivery_history']['reply'];
export type HistoryPr = History['prs'][number];
export type Baseline = Ops['delivery_baseline']['reply'];
export type Regressions = Ops['delivery_regressions']['reply'];
/** A list row: the pull request, its focus id, its code (#number) and its group and order keys. */
export type Row = { id: string; code: string; sortKey: (string | number)[]; it: Item };
/** The norms are the fleet repository's history: other repositories' pull requests have none. */
export type Norms = { stage: (it: Pick<Item, 'stage' | 'repo' | 'release'>) => [number, number] | null; total: [number, number] | null };
/** The link's own keys: the pace range, the picked bucket, a release, and the project of a project's Delivery facet (routes.ts). */
export type DeliveryExtra = { range?: string; at?: string; release?: string; project?: string };
export type Bucket = DayCol & { start: number; end: number; today: boolean };

export const MIN = 6e4, DAY = 864e5;
export const FLEET = 'taskqorg/fleet';
export const ORDER = DELIVERY_ORDER;
export const DONE = DELIVERY_DONE as Stage[];
export const doneOnly = (v: View) => !!v.state?.length && v.state.every(s => DONE.includes(s as Stage));
// Each stage's norm is its P50 and P90 in delivery_baseline (the last 14 days): the key of the history duration it is.
// Open, Checks failed and Queue failed have none: people work there, and a failure needs a look at once.
// Waiting in queue is both "set to enter" and "in the queue, waiting for a runner": the queue's norm covers both.
// A UI-only release installs on no host: its Installing is the time until the site serves its UI (`ui_install`).
const NORM_KEY: Partial<Record<Stage, string>> = { pr_ci: 'pr_ci', mq_wait: 'queue', mq_ci: 'queue', releasing: 'release', installing: 'install' };
export const normKey = (it: Pick<Item, 'stage' | 'release'>) => it.stage === 'installing' && uiOnly(it.release) ? 'ui_install' : NORM_KEY[it.stage];
/** A UI-only release, ui-<backend>-v<UI version> (release.yml): the web app alone, delivered when the site serves it. */
// The shape go/coordinator/delivery_history.go uiReleaseTag matches; the server sends the tag only.
const UI_TAG = /^ui-\d+(?:\.\d+)*-v(\d+(?:\.\d+)*)$/;
export const uiOnly = (tag?: string | null) => !!tag && UI_TAG.test(tag);
/** A release as people name it: v1.2.3 is 1.2.3; ui-1.2.2-v1.2.3 is "UI 1.2.3" (its UI version; the backend stays 1.2.2). */
export const releaseLabel = (tag: string) => { const m = UI_TAG.exec(tag); return m ? `UI ${m[1]}` : tag.replace(/^v/, ''); };
export const RANGES = [{ id: '24h', label: '24 h' }, { id: '7d', label: '7 days' }, { id: '30d', label: '30 days' }];
/** Delivery times: a missing one is NaN, so it is never past a norm and never inside a window. */
export const ms = (s?: string | null) => timeOf(s, NaN);
export const fmt = (v: number) => duration(v / 1000).text;
/** The focus segment of a pull/merge request: repo path (~-joined) plus number (stable, readable, no slash). */
export const idOf = (it: { repo: string; number: number }) => `${it.repo.replaceAll('/', '~')}~${it.number}`;
export const providerLabel = (provider?: string | null, kind?: string | null) =>
  provider === 'gitlab' || kind === 'merge_request' ? 'GitLab MR' : 'GitHub PR';
// Current rows change with delivery-state:<project>, the recorded history with the other delivery kinds: the server's
// touches name the operation (contracts: changes). A history read answers 404 when no history is kept: known, unlike loading.
export const kept = <T,>(d: { data: T | null; status: number | null }) => d.status === 404 ? null : d.data;
export const missing = (d: { status: number | null }) => d.status === 404;
export const failed = (d: { error: string | null; status: number | null }) => d.status === 404 ? null : d.error;
export const q = quantile;
export const sec = (v: number | null | undefined) => ({ value: v == null ? null : v / 1000, basis: v == null ? 'unavailable' as const : 'measured' as const, reason: v == null ? 'no pull requests' : undefined });

/** The pace keys of a link: valid values other than the defaults. A link may carry anything (an old one has the text "undefined"). */
export function deliveryExtra(x: Record<string, string | undefined> | undefined): DeliveryExtra {
  const out: DeliveryExtra = {};
  if (x?.range && x.range !== '7d' && RANGES.some(r => r.id === x.range)) out.range = x.range;
  if (x?.at && /^\d{4}-\d{2}-\d{2}(T\d{2})?$/.test(x.at)) out.at = x.at;
  if (x?.release && (UI_TAG.test(x.release) || /^v[0-9]+(?:\.[0-9]+)*$/.test(x.release))) out.release = x.release;
  if (x?.project) out.project = x.project;
  return out;
}

/* ---------- Buckets: calendar days (or two-hour slots) in the viewer's zone ---------- */

/** 24 h: twelve two-hour slots of local time ending with the current one; 7 and 30 days: calendar days to today, from the first recorded day. */
export function bucketsOf(range: string, now: number, first?: number): Bucket[] {
  if (range === '24h') {
    return Array.from({ length: 12 }, (_, i) => { const s = slotStart(now, 2, i - 11), e = slotStart(now, 2, i - 10);
      return { key: dateKey(s, true), start: s, end: e, label: hourLabel(s), tip: `${day(s, now)} ${hourLabel(s)}–${hourLabel(e)}${e > now ? ', so far' : ''}`, today: e > now }; });
  }
  const out: Bucket[] = [];
  let s = dayStart(now, range === '30d' ? -29 : -6);
  if (first && first > s) s = dayStart(first);
  for (; s <= now; s = dayStart(s, 1)) { const t = s === dayStart(now); out.push({ key: dateKey(s), start: s, end: dayStart(s, 1), label: day(s, now), tip: t ? `${day(s, now)}, today so far` : day(s, now), today: t }); }
  return out;
}

/** Seconds in a stage after which its time is red: the stage's P90 over the last 14 days; none for stages without a norm. */
export const alertOf = (n: Norms, it: Item) => { const x = n.stage(it); return x ? x[1] / 1000 : undefined; };

/** Released before install times were recorded: its install is unknown, never measured. */
export const unrecorded = (x: HistoryPr, installsFrom: number | null) => !!installsFrom && !!x.released && ms(x.released) < installsFrom;
/** Due a measure: it has one, or it reached the hosts (installed), or recording had not begun. One not installed yet is
 *  on its way (or stuck before the hosts), not unmeasured; the rule reads the data only, never the clock. */
export const due = (x: HistoryPr, installsFrom: number | null) => typeof x.durations.total === 'number' || !!doneAt(x) || unrecorded(x, installsFrom);
/** When it was delivered: on the PDS18 coordinator, or for a UI-only release when the site served its UI. */
export const doneAt = (x: HistoryPr) => uiOnly(x.release) ? x.uiOnSite : x.installed?.coordinator;
/** A stage duration, a UI-only release's UI on site standing for the install it does not have. */
export const stageMs = (x: HistoryPr, k: string) => k === 'install' && uiOnly(x.release) ? x.durations.ui_install : x.durations[k];

/** A recorded (merged) pull request as a list row: its stage from how far it got. */
export function fromHistory(x: HistoryPr, installsFrom?: string): Item {
  // Released before installs were recorded: its install is unknown, so it is done (merged), not installing for weeks.
  const unrec = unrecorded(x, installsFrom ? ms(installsFrom) : null);
  const done = doneAt(x), stage: Stage = done ? 'installed' : unrec ? 'merged' : x.released ? 'installing' : 'releasing';
  return { projectId: 'fleet', repo: FLEET, number: x.number, title: x.title, url: x.url, stage, since: (done ?? x.released ?? x.merged) as string, opened: x.opened, merged: x.merged, release: x.release };
}

/**
 * Open to installed in a bucket: P50 and P90 of its pull requests measured through install; none measured is "not
 * measured", never 0. Only pull requests due a measure (`due`) count against "partly measured"; one not installed yet
 * is on its way.
 */
export function totalMark(prs: HistoryPr[], b: Bucket, installsFrom: number | null): DayMark {
  const xs = prs.map(x => x.durations.total).filter((v): v is number => typeof v === 'number');
  if (!prs.length) return { v: null, missing: 'nothing merged' };
  // Why none is measured, the most specific first: installed but its install time not matched, recording not begun, on its way.
  // An installed one without a measure: its hosts took a later release, or its install was not matched to this one.
  if (!xs.length) return { v: null, missing: prs.some(x => !!doneAt(x)) ? 'installed with a later release, or not matched to its own'
    : installsFrom && (b.end <= installsFrom || prs.some(x => unrecorded(x, installsFrom))) ? `install times recorded from ${day(installsFrom)} ${hourLabel(installsFrom)}`
    : 'not installed yet' };
  const n = prs.filter(x => due(x, installsFrom)).length;
  return { v: q(xs, 0.5), hi: q(xs, 0.9), some: xs.length < n ? `${xs.length} of ${n} measured` : undefined, partial: b.today ? 'so far' : undefined };
}
