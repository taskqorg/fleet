// Book demos of the Home blocks (board: docs/design-system/board/blocks.bl, Blocks · atoms and Blocks · Home).
import { createSignal, For } from 'solid-js';
import type { JSX } from '@solidjs/web';
import {
  StateDot, Avatar, ProjectChip, Button, QuestionCard, OpenQuestion, useAnswer, SessionCard, SessionCardSkeleton, BotTile, PulseLine, TargetPicker, Composer, QuietSummary, Ago,
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


// A HAL-like eye as an animated SVG (its glow breathes) and its still frame, for the bot avatar demo.
const EYE = (animated: boolean) => 'data:image/svg+xml,' + encodeURIComponent(
  `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 96 96" width="96" height="96"><defs><radialGradient id="g"><stop offset="0" stop-color="#fff6c0"/><stop offset=".12" stop-color="#ff3a1a"/><stop offset=".5" stop-color="#5a0804"/><stop offset="1" stop-color="#0b0b0c"/></radialGradient></defs>` +
  (animated ? '<style>#l{animation:b 4s ease-in-out infinite}@keyframes b{50%{opacity:.45}}</style>' : '') +
  `<circle cx="48" cy="48" r="47" fill="#202022" stroke="#6a6a6e" stroke-width="2"/><circle cx="48" cy="48" r="40" fill="#050505"/><circle id="l" cx="48" cy="48" r="34" fill="url(#g)"/></svg>`);
const HAL = { src: EYE(true), still: EYE(false), glow: '#ff3a1a' };
const STATES = ['asleep', 'working', 'waiting', 'failed', 'paused'] as const;

function BotAvatarDemo() {
  return <div class="col">
    <div class="brow">
      <Avatar letter="R" tone="success" size="sm" /><Avatar letter="R" tone="success" /><Avatar letter="R" tone="success" size="lg" /><Avatar letter="R" tone="success" size="xl" />
      <Avatar letter="H" picture={HAL} size="sm" /><Avatar letter="H" picture={HAL} /><Avatar letter="H" picture={HAL} size="lg" /><Avatar letter="H" picture={HAL} size="xl" />
    </div>
    <div class="brow"><For each={STATES}>{s => <span class="life-pair"><span class="muted type-sm">{s}</span><Avatar letter="R" tone="success" size="lg" state={s} /><Avatar letter="H" picture={HAL} size="lg" state={s} /><Avatar letter="H" picture={HAL} size="sm" state={s} /></span>}</For></div>
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

/** The open question over a fake write: the answer lands after a second; the second card is offline, the third refused. */
function OpenQuestionDemo() {
  const later = <T,>(v: T) => new Promise<T>(r => setTimeout(() => r(v), 1000));
  const ok = useAnswer({ online: () => true, answer: (_id, a) => later(a), addKey: () => later(null) });
  const off = useAnswer({ online: () => false, answer: (_id, a) => later(a), addKey: () => later(null) });
  const refused = useAnswer({ online: () => true, answer: () => Promise.reject(new Error('the question was withdrawn')), addKey: () => later(null) });
  return <div class="cards">
    <OpenQuestion q={{ id: 'b1', text: 'Keep the compact card or the wide card?', options: [{ label: 'Compact' }, { label: 'Wide' }] }} answers={ok} />
    <OpenQuestion q={{ id: 'b2', text: 'Accept the result of T-FLT-51?', kind: 'acceptance' }} answers={off} />
    <OpenQuestion q={{ id: 'b3', text: 'Retry the rollout on windows?', options: ['retry', 'skip'] }} answers={refused} />
    <OpenQuestion q={{ id: 'b4', text: 'The PM needs an OpenAI key to run the eval.', kind: 'secret', options: { connection: 'openai' } }} answers={ok} />
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

function QuietDemo() {
  return <div class="col">
    <QuietSummary span="Overnight" facts="12 tasks done, 2 pull requests merged, 0 failures" watcher="HAL0" onAsk={() => {}} onLive={() => {}} />
    <QuietSummary span="Today" facts="3 sessions done, 1 pull request merged" onLive={() => {}} />
    <QuietSummary loading />
  </div>;
}

export const HOME_BLOCKS: BookSection[] = [
  ['state-dot', 'State dot and word', 'state-badge', () => <StateDotDemo />, () => <>OR-55: one word, tone and dot per state. Working pulses, Stopped is hollow, Asleep is a dashed ring. Below: today's task, execution, bot-run and session states mapped to it (registry <span class="mono">lifeOf</span>).</>],
  ['avatar', 'Avatar, project chip, button kinds', 'session-card', () => <AtomsDemo />, () => <>A letter in a tone (P for a project PM) or the bot's harness mark; md and sm. The attention kind is a question's other choice.</>],
  ['bot-avatar', 'Bot avatar', 'avatar', () => <BotAvatarDemo />, () => <>The letter is the default; a picture (still, animated image or SVG, or a muted loop) replaces it. sm 16, md 24, lg 40, xl 96: lg and xl play, sm and md show the still frame until hover or while the bot works. Activity: asleep dim, working breathes its glow, waiting an amber dot, failed dim with a red dot, paused dimmest. Reduced motion: no breathing, still frames.</>],
  ['question-card', 'Question card', 'decision-block', () => <QuestionDemo />, () => <>OR-53: open (click a choice or type and press Enter), answering, answered, and the failed recovery question.</>],
  ['open-question', 'Open question', 'decision-block', () => <OpenQuestionDemo />, () => <>OR-53: the question card every screen answers with (Home, Conversations, Projects, the session view). Options as given (objects by label), an acceptance's two answers, a key request as a password field with Add key and decline; offline, outcome unknown and a refusal show under the card.</>],
  ['session-card', 'Session card', 'session-card', () => <SessionCardDemo />, () => <>OR-50: working with progress, waiting for you with its question, queued, failed with the recovery question, done (leaves Home), stale and loading. The whole card opens the session; the question acts in place.</>],
  ['bot-tile', 'Bot tile', 'bot-tile', () => <BotTileDemo />, () => <>Asleep, waking and wake failed. Wake on the first tile runs the sequence.</>],
  ['pulse-line', 'Pulse line and blocker chip', 'pulse-line', () => <PulseDemo />, () => <>Normal, blocked (two chips, then +K; a zero count is hidden) and stale.</>],
  ['quiet-summary', 'Quiet summary', 'quiet-summary', () => <QuietDemo />, () => <>OR-50: nothing waits and nothing failed. Since the last visit with a watcher, a first visit (today) without one, and loading.</>],
  ['target-picker', 'Composer target picker', 'composer', () => <TargetDemo />, () => <>Idle and typing. Click the chip: search, project PMs, bots with their state, then a new session; arrows move, Enter picks, Escape closes.</>],
];
