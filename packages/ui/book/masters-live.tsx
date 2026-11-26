// rename Host band: Text 68 -> problem
// rename Nav rail: Text 71 -> brand
// rename Nav rail: Text 72 -> user
// rename Run strip: Text 63 -> Text 58
// rename Run strip: Text 64 -> Text 59
// rename Run strip: Text 65 -> Text 60
// rename Run strip: Text 66 -> Text 61
// rename Run strip: Text 67 -> Text 62
// rename Bot avatar: Text 7 -> letter
// rename Bot avatar: Text 8 -> letter
// rename Bot avatar: Text 9 -> letter
// rename Bot avatar: Text 10 -> letter
// Board masters: the live blocks and the shell's navigation (masters.tsx). Values follow book/blocks.tsx and book/nav.ts.
import {
  Sparkline, Gauge, FigureTile, RunStrip, WorkerSlot, HostBand, LiveRow, StatusPill, NavRail, BottomBar, AppShell, Avatar,
  barTabs, type StripRun, type GaugeProps, type WorkerSlotProps, type NavItem,
} from '../src/index.ts';
import { NAV } from './nav.ts';
import { FocusDemo } from './focus-demo.tsx';
import { NOW, min, hr } from './fixtures.ts';
import { slot, type MasterSpec } from './masters-spec.ts';

const m = (value: number) => ({ value, basis: 'measured' as const });
const runs = (seed: number, failing = false): StripRun[] => {
  const out: StripRun[] = [];
  for (let i = 0; i < 22; i++) {
    const start = NOW - 24 * hr + (i * 65 + (seed * 13 + i * 7) % 20) * min;
    out.push({ start, end: start + (18 + (i * seed) % 30) * min, failed: !failing && i === 13 });
  }
  if (failing) for (let i = 0; i < 6; i++) out.push({ start: NOW - (7 * hr) + i * 12 * min, end: NOW - (7 * hr) + i * 12 * min + 4 * min, failed: true });
  if (failing) out.push({ start: NOW - 6 * hr, end: NOW - 40 * min, failed: true });
  return out;
};
const trend = [2, 3, 2, 4, 3, 3, 5, 4, 3, 5, 6, 4, 5, 6, 4, 4];
const failTrend = [null, null, null, null, null, null, null, 1, 2, 4, 3, 6];
/** The run strip's marks, the same five in every strip. */
const MARKS = ['Text 58', 'Text 59', 'Text 60', 'Text 61', 'Text 62'];

const gauge = (label: string, name: string, value: number): GaugeProps => ({ label: slot('label', label), name, value, warnAt: 80, critAt: 95 });
// Slot texts are named as the Worker slot master names them; the derived ones (elapsed time, tokens) by `texts`.
// A failing slot has no retry countdown: its "retry " and the countdown are two derived texts, which would take the
// names of a busy slot's time and tokens (texts are named in document order, one list for every variant).
const busy = (n: number, title: string, sub: string, started: number, tokens: number, color: 'd1' | 'd4' = 'd4'): WorkerSlotProps =>
  ({ slot: n, state: 'busy', title: slot('title', title), code: slot('sub', sub), started, tokens: m(tokens), color, now: NOW });
const failing: WorkerSlotProps = { slot: 3, state: 'failing', title: slot('title', 'Codex start failed ×22'), code: slot('sub', 'Lab FLT-6 · lab · codex'),
  cause: slot('tokens', 'browser tool missing'), now: NOW };
const SLOT_TEXTS = ['time', 'tokens', 'tokens unit'];

/** The book's rail sections with their labels named `label` (the Nav item and Tab item masters). */
const NAV_SLOTS: NavItem[] = NAV.map(i => ({ ...i, label: slot('label', i.label) }));
/** The rail head and user block as the app shell draws them (patterns.tsx, AppShell and UserBlock). */
const railHead = <><span class="brand-mark" role="img" aria-label="Fleet" /><b class="brand">{slot('brand', 'Fleet')}</b><span class="grow" />
  <a class="nav-count t-attention waiting" href="#/home">{slot('need', '2 need you')}</a></>;
const railFoot = <div class="rail-user"><div class="rail-user-row"><button type="button" class="rail-user-b"><span class="rail-av" aria-hidden="true">A</span>
  <span class="nm">{slot('user', 'Alex')}</span></button></div></div>;
/** Below 760 px the shell shows the bottom bar (layout.css, OR-60); the masters page is wider, so it is shown here. */
const PHONE = '.bm-phone .bottom-bar{display:flex;align-items:stretch;padding:var(--space-1) 0;border-top:1px solid var(--line);background:var(--rail)}';
/** The project picture and HAL0's eye of the hand master (board Assets). */
const PICTURE = { image: { src: '../board/Assets/bot-avatar-sample-rocket.png' }, eye: { src: '../board/Assets/bot-avatar-hal0-eye.png', glow: '#ff3a1a' } } as const;

export const MASTERS_LIVE: MasterSpec[] = [
  { name: 'Sparkline', axes: { tone: ['normal', 'alert'] },
    render: v => v.tone === 'alert'
      ? <Sparkline values={[1, 1, 1, 1, 1, 1.5, 1, 3, 5, 7, 9, 12]} label="Failed starts, last 12 hours" alertFrom={7} />
      : <Sparkline values={[4, 6, 5, 8, 7, 9, 6, 10, 8, 11, 9, 12]} label="Runs, last 12 hours" /> },
  { name: 'Gauge', axes: { level: ['normal', 'warn', 'danger'] }, texts: ['value'],
    render: v => <Gauge {...gauge('MEM', 'Memory', v.level === 'danger' ? 97 : v.level === 'warn' ? 84 : 57)} /> },
  { name: 'Figure tile', axes: { state: ['normal', 'alert'] }, w: 220, texts: ['value', 'of sep', 'of'],
    render: v => v.state === 'alert'
      ? <FigureTile label={slot('label', 'Failed starts 6h')} value={m(22)} sub={slot('sub', 'coding-tool-unready')} trend={failTrend} trendLabel="last 6 hours, 0 to 6" alert alertBars={5} />
      : <FigureTile label={slot('label', 'Agents working')} value={m(4)} of={8} sub={slot('sub', 'of 8 slots')} trend={trend} trendLabel="last 24 hours, 2 to 6" /> },
  { name: 'Run strip', axes: { state: ['normal', 'failing'] }, w: 600, texts: MARKS,
    render: v => <RunStrip runs={runs(v.state === 'failing' ? 2 : 1, v.state === 'failing')} now={NOW} /> },
  { name: 'Worker slot', axes: { state: ['busy', 'idle', 'failing'] }, w: 300, texts: SLOT_TEXTS,
    render: v => v.state === 'idle' ? <WorkerSlot slot={2} state="idle" />
      : v.state === 'failing' ? <WorkerSlot {...failing} />
      : <WorkerSlot {...busy(1, 'Fix receipt factory', 'T-FLT-51 · fleet · codex', NOW - 12 * min, 430_000)} /> },
  // Project counts are a number and a word each ("3", " running"): the number takes the hand name, the word `<name> unit`.
  { name: 'Live row', axes: { kind: ['project', 'delivery', 'runner', 'event', 'trouble'] }, w: 420, pick: '.lrow',
    texts: ['a', 'a unit', 'b', 'b unit', 'c', 'c unit'],
    render: v => <ul class="pane" style={{ margin: 0, padding: 0 }}>{
      v.kind === 'project' ? <LiveRow kind="project" name={slot('title', 'fleet')} running={3} queued={4} done={23} trend={trend} trendLabel="runs, last 24 hours" />
      : v.kind === 'delivery' ? <LiveRow kind="delivery" tone="running" code={slot('code', '#827')} title={slot('title', 'retain initiating person')} stage="merging" />
      : v.kind === 'runner' ? <LiveRow kind="runner" name={slot('title', 'ubuntu-latest')} fact={slot('fact', '3 jobs · 142 min today')} />
      : v.kind === 'trouble' ? <LiveRow kind="runner" name={slot('title', 'self-hosted pds18')} fact={slot('fact', 'offline 12m')} trouble />
      : <LiveRow kind="event" at={NOW - min} state="done" text={slot('title', 'T-FLT-43 merged · #828 rolling out')} />
    }</ul> },
  { name: 'Host band', axes: { state: ['normal', 'problem'] }, w: 1000,
    texts: [...SLOT_TEXTS, ...SLOT_TEXTS, 'title', ...MARKS],
    render: v => v.state === 'problem'
      ? <HostBand name={slot('name', 'pds18-native')} state="problem" facts={slot('sub', 'linux · 338 GB free')} gauges={[gauge('CPU', 'Processor', 62), gauge('MEM', 'Memory', 84), gauge('DISK', 'Disk', 41)]}
        day={slot('runs', '44 runs · 38 jobs · 5h 18m')} problem={slot('problem', 'Codex not ready: coding-tool-unready (browser). 22 failed starts since 18:40. Agent runs continue on claude.')}
        slots={[busy(1, 'Fix receipt factory', 'T-FLT-51 · fleet · codex', NOW - 12 * min, 430_000), busy(2, 'Remaining parent accounting', 'T-CXX-39 · cyprus.bz · claude', NOW - 34 * min, 1_200_000, 'd1'), failing, { slot: 4, state: 'idle' }]}
        strip={{ runs: runs(2, true), now: NOW }} />
      : <HostBand name={slot('name', 'owner-mac')} state="normal" facts={slot('sub', 'darwin · 318 GB free')} gauges={[gauge('CPU', 'Processor', 62), gauge('MEM', 'Memory', 57), gauge('DISK', 'Disk', 52)]}
        day={slot('runs', '38 runs · 5h 33m')}
        slots={[busy(1, 'Fix receipt factory', 'T-FLT-51 · fleet · codex', NOW - 12 * min, 430_000), busy(2, 'Design board review', 'O-FLT-596 · design · claude', NOW - 4 * min, 58_000, 'd1'), { slot: 3, state: 'idle' }]}
        strip={{ runs: runs(1), now: NOW }} /> },
  // The Live page's top bar in wall mode (app views/live.tsx): brand, status pill, focus and clock.
  { name: 'Status bar', axes: { state: ['working', 'problem'] }, w: 1400,
    render: v => <header class="live-bar">
      <b class="brand">{slot('mark', 'Fleet live')}</b>
      {v.state === 'problem' ? <StatusPill state="problem" text="pds18-native cannot start Codex" more={1} /> : <StatusPill state="working" />}
      <span class="grow" />
      <span class="live-focus muted">{slot('focus', 'focus: all projects')}</span>
      <time class="live-clock mono">{slot('clock', '22:47')}</time>
    </header> },
  // One item of the rail: the first (Home), current or not.
  { name: 'Nav item', axes: { state: ['default', 'current'] }, w: 196, pick: '.nav-i', texts: ['count'],
    render: v => <NavRail items={[{ ...NAV_SLOTS[0]!, count: { n: 2, tone: 'neutral', label: 'need you' } }, ...NAV_SLOTS.slice(1)]} current={v.state === 'current' ? 'home' : 'live'}
      onGo={() => {}} pinned={[]} onPins={() => {}} /> },
  // The current section is a property, so a screen of any section shows its own item current.
  { name: 'Nav rail', w: 220, texts: ['count', 'count', 'count', 'avatar'],
    axes: { current: ['home', 'live', 'history', 'conversations', 'projects', 'lab', 'library', 'system'] },
    render: v => <NavRail items={NAV_SLOTS} current={v.current} onGo={() => {}} pinned={[]} onPins={() => {}} head={railHead} foot={railFoot} /> },
  // One tab of the bottom bar: the first (Home), current or not.
  { name: 'Tab item', axes: { state: ['default', 'current'] }, pick: '.tab-i',
    render: v => <div class="bm-phone"><style>{PHONE}</style><BottomBar items={NAV_SLOTS} tabs={barTabs} current={v.state === 'current' ? 'home' : 'live'} onGo={() => {}} /></div> },
  { name: 'Bottom bar', w: 390, pick: '.bottom-bar', texts: ['count', 'count', 'more'], axes: { current: ['home', 'live', 'conversations', 'projects'] },
    render: v => <div class="bm-phone"><style>{PHONE}</style><BottomBar items={NAV_SLOTS} tabs={barTabs} current={v.current} onGo={() => {}} /></div> },
  // The shell header of the app shell; the rail beside it takes 220 of the 1440, so the header is 1220 wide.
  { name: 'Shell header', w: 1440, pick: '.shell-h', texts: ['focus_text'],
    render: () => <AppShell nav={NAV} current="home" onGo={() => {}} waiting={2} title={slot('title', 'Home')} focus={<FocusDemo />}>{null}</AppShell> },
  { name: 'Bot avatar', axes: { level: ['letter', 'image', 'eye'], size: ['sm', 'md', 'lg', 'xl'] }, texts: ['letter'],
    render: v => <Avatar letter="R" tone="success" size={v.size as 'sm'} picture={v.level === 'letter' ? undefined : PICTURE[v.level as 'image' | 'eye']} /> },
];
