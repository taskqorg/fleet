// Book demos of the Home blocks (board: docs/design-system/board/blocks.bl, Blocks · atoms and Blocks · Home).
import { createSignal, For } from 'solid-js';
import type { JSX } from '@solidjs/web';
import {
  StateDot, Avatar, ProjectChip, Button, QuestionCard, SessionCard, SessionCardSkeleton, BotTile, PulseLine, TargetPicker, Composer, Ago,
  states, lifeOf, type Life, type LifeFamily, type LifeRef, type Target,
} from '../src/index.ts';
import { NOW, min, hr } from './fixtures.ts';

type BookSection = [string, string, string | undefined, () => JSX.Element, (() => JSX.Element)?];

const LIVES = Object.keys(states.life) as Life[];

function StateDotDemo() {
  return <div class="col">
    <div class="brow"><For each={LIVES}>{s => <StateDot state={s} />}</For></div>
    <div class="life-map">
      <For each={Object.keys(lifeOf) as LifeFamily[]}>
        {f => <div><span class="muted type-sm mono">{f}</span><For each={Object.keys(lifeOf[f])}>{s => <span class="life-pair"><span class="muted type-sm">{s}</span><StateDot {...({ family: f, state: s } as LifeRef)} /></span>}</For></div>}
      </For>
    </div>
  </div>;
}

function AtomsDemo() {
  return <div class="brow">
    <Avatar letter="P" /><Avatar letter="P" size="sm" /><Avatar letter="H" tone="success" /><Avatar harness="claude" /><Avatar harness="codex" size="sm" />
    <ProjectChip name="fleet" />
    <Button label="Compact" kind="primary" /><Button label="Wake" /><Button label="Wide" kind="attention" /><Button label="Retry" kind="danger" />
  </div>;
}

const CHOICES = [{ label: 'Compact', primary: true }, { label: 'Wide' }];
function QuestionDemo() {
  const [answered, setAnswered] = createSignal<{ answer: string; at: number } | undefined>();
  return <div class="cards">
    <QuestionCard question="Keep the compact card or the wide card with progress?" choices={CHOICES} answered={answered()} onAnswer={a => setAnswered({ answer: a, at: Date.now() })} />
    <QuestionCard question="Keep the compact card or the wide card with progress?" choices={CHOICES} answering="Compact" />
    <QuestionCard question="Keep the compact card or the wide card with progress?" choices={CHOICES} answered={{ answer: 'Compact', at: NOW - 2 * min }} />
    <QuestionCard question="Retry the rollout on windows?" tone="danger" choices={[{ label: 'Retry', primary: true }, { label: 'Open' }]} />
  </div>;
}

const PM = { letter: 'P' } as const;
function SessionCardDemo() {
  return <div class="cards">
    <SessionCard name="PM · fleet" project="fleet" avatar={PM} state="working" now="Swarm of 3 on T-FLT-51 «Fix receipt factory»: review running."
      progress={{ step: 2, of: 3 }} meta={['step 2 of 3', <Ago t={NOW - 12 * min} />, 'pds18-native', 'timer 22:30']} />
    <SessionCard name="PM · design" project="design" avatar={{ letter: 'P', tone: 'attention' }} state="waiting" now="Showed the Home screen in three states; asks which card layout to keep."
      meta={[<Ago t={NOW} />, '4 screenshots']} question={<QuestionCard question="Keep the compact card or the wide card with progress?" choices={CHOICES} />} />
    <SessionCard name="PM · cyprus.bz" project="cyprus.bz" avatar={{ letter: 'P', tone: 'neutral' }} state="queued" now="Waits for a free slot." meta={['position 2', <Ago t={NOW - 4 * min} />]} />
    <SessionCard name="Release manager" project="fleet" avatar={{ harness: 'codex' }} state="failed" now="Rollout of PR 826 failed on windows." meta={[<Ago t={NOW - 9 * min} />, 'retry or ask']}
      question={<QuestionCard question="Retry the rollout on windows?" tone="danger" choices={[{ label: 'Retry', primary: true }, { label: 'Open' }]} />} />
    <SessionCard name="PM · design" project="design" avatar={{ letter: 'P', tone: 'success' }} state="done" now="Finished: Home canvas in 4 states." meta={['done', <Ago t={NOW - 2 * min} />]} />
    <SessionCard name="PM · fleet" project="fleet" avatar={PM} state="working" now="Rebuilding the session view blocks." meta={['step 1 of 4']} progress={{ step: 1, of: 4 }} staleSince={NOW - 18 * min} clock={NOW} />
    <SessionCardSkeleton />
  </div>;
}

function BotTileDemo() {
  const [state, setState] = createSignal<'asleep' | 'waking' | 'failed'>('asleep');
  return <div class="tiles">
    <BotTile name="Release manager" about="delivery, rollouts" lastUsed={NOW - 6 * hr} clock={NOW} avatar={{ harness: 'codex' }} state={state()}
      onWake={() => { setState('waking'); setTimeout(() => setState('failed'), 1500); }} />
    <BotTile name="Release manager" about="delivery, rollouts" avatar={{ harness: 'codex' }} state="waking" />
    <BotTile name="Release manager" about="delivery, rollouts" avatar={{ harness: 'codex' }} state="failed" reason="no free slot" />
  </div>;
}

function PulseDemo() {
  return <div class="col">
    <PulseLine working={4} queued={7} done={41} liveHref="#pulse-line" countHref={s => `#${s}`} />
    <PulseLine working={4} queued={0} done={41} liveHref="#pulse-line" countHref={s => `#${s}`}
      blockers={[{ cause: 'Codex cannot start on pds18-native', n: 22, href: '#pds18' }, { cause: 'windows host offline', n: 3, href: '#windows' }, { cause: 'GitHub rate limit', n: 2, href: '#github' }]} />
    <PulseLine working={4} queued={7} done={41} liveHref="#pulse-line" countHref={s => `#${s}`} staleSince={NOW - 18 * min} clock={NOW} />
  </div>;
}

const TARGETS: Target[] = [
  { id: 'pm-fleet', kind: 'pm', name: 'PM · fleet', about: 'fleet', project: 'fleet', avatar: PM, state: 'working' },
  { id: 'pm-cy', kind: 'pm', name: 'PM · cyprus.bz', about: 'cyprus.bz', project: 'cyprus.bz', avatar: { letter: 'P', tone: 'neutral' }, state: 'queued' },
  { id: 'hal', kind: 'bot', name: 'HAL0', about: 'watches Fleet tasks', avatar: { letter: 'H', tone: 'success' }, state: 'waiting' },
  { id: 'setup', kind: 'bot', name: 'Fleet setup', about: 'settings, hosts, accounts', avatar: { harness: 'claude' }, state: 'asleep' },
];
const typed = { key: 'book-typed', read: () => 'давай обсудим дизайн Home', write: () => {}, clearIfUnchanged: () => {} };
function TargetDemo() {
  const [to, setTo] = createSignal('pm-fleet');
  const picker = () => <TargetPicker targets={TARGETS} value={to()} onPick={setTo} onNew={() => {}} />;
  return <div class="col">
    <Composer placeholder="Talk to anyone…" onSend={() => {}} target={picker()} />
    <Composer placeholder="Talk to anyone…" onSend={() => {}} target={picker()} draftPersistence={typed} />
  </div>;
}

export const HOME_BLOCKS: BookSection[] = [
  ['state-dot', 'State dot and word', 'state-badge', () => <StateDotDemo />, () => <>OR-55: one word, tone and dot per state. Working pulses, Stopped is hollow, Asleep is a dashed ring. Below: today's task, execution, bot-run and session states mapped to it (registry <span class="mono">lifeOf</span>).</>],
  ['avatar', 'Avatar, project chip, button kinds', 'session-card', () => <AtomsDemo />, () => <>A letter in a tone (P for a project PM) or the bot's harness mark; md and sm. The attention kind is a question's other choice.</>],
  ['question-card', 'Question card', 'decision-block', () => <QuestionDemo />, () => <>OR-53: open (click a choice or type and press Enter), answering, answered, and the failed recovery question.</>],
  ['session-card', 'Session card', 'session-card', () => <SessionCardDemo />, () => <>OR-50: working with progress, waiting for you with its question, queued, failed with the recovery question, done (leaves Home), stale and loading. The whole card opens the session; the question acts in place.</>],
  ['bot-tile', 'Bot tile', 'bot-tile', () => <BotTileDemo />, () => <>Asleep, waking and wake failed. Wake on the first tile runs the sequence.</>],
  ['pulse-line', 'Pulse line and blocker chip', 'pulse-line', () => <PulseDemo />, () => <>Normal, blocked (two chips, then +K; a zero count is hidden) and stale.</>],
  ['target-picker', 'Composer target picker', 'composer', () => <TargetDemo />, () => <>Idle and typing. Click the chip: search, project PMs, bots with their state, then a new session; arrows move, Enter picks, Escape closes.</>],
];
