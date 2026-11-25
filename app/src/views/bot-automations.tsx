import { For, Match, Show, Switch, createMemo, createSignal, untrack } from 'solid-js';
import {
  Ago, Button, Choice, DetailSection, Empty, ErrorBanner, KvRow, Skeleton, StateBadge, TextField, zonedTime,
} from '../../../packages/ui/src/index.ts';
import { useAlive, writeOnce, type Ops } from '../api.ts';
import { useOp } from '../store.ts';

// A bot's automations are its triggers (docs/designs/fleet-triggers.md): trigger_list {botId}, trigger_save.
type Automation = Ops['trigger_list']['reply']['triggers'][number];
/** The form's view of a trigger: manual, a daily schedule, or Needs you (a change check each pass). */
type Settings = { name: string; instructions: string; kind: 'manual' | 'schedule' | 'needs_you'; scheduleTime: string; scheduleZone: string; event: 'none' | 'all' | 'question' | 'execution'; enabled: boolean };
/** The trigger checks every pass on a change: the Needs you automation. */
const PASS_MS = 15_000;

function settingsOf(t: Automation): Settings {
  const s = t.settings, kinds = [...s.ifChanged].sort().join(',');
  const kind = s.when === 'daily' ? 'schedule' : s.when === 'manual' ? 'manual' : 'needs_you';
  return {
    name: s.name, instructions: s.text, kind, scheduleTime: s.dailyTime ?? '', scheduleZone: s.dailyZone ?? '', enabled: s.enabled,
    event: kind !== 'needs_you' ? 'none' : kinds === 'question' ? 'question' : kinds === 'execution' ? 'execution' : 'all',
  };
}

/** A trigger the form can show: daily, manual, or a change check on questions or execution problems (others are
 *  changed through the API, trigger_save). */
const shown = (t: Automation) => t.settings.when !== 'every' ||
  (t.settings.ifChanged.length > 0 && t.settings.ifChanged.every(k => k === 'question' || k === 'execution'));

function triggerOf(s: Settings): Ops['trigger_save']['request']['settings'] {
  const base = { name: s.name, text: s.instructions, enabled: s.enabled };
  if (s.kind === 'schedule') return { ...base, when: 'daily', dailyTime: s.scheduleTime, dailyZone: s.scheduleZone, ifChanged: [] };
  if (s.kind === 'manual') return { ...base, when: 'manual', ifChanged: [] };
  return { ...base, when: 'every', everyMs: PASS_MS, ifChanged: s.event === 'all' ? ['question', 'execution'] : [s.event] };
}

const triggerOptions = [
  { id: 'manual', label: 'Manual' },
  { id: 'schedule', label: 'Daily schedule' },
  { id: 'needs_you', label: 'Needs you' },
];
const eventOptions = [
  { id: 'all', label: 'Questions and problems' },
  { id: 'question', label: 'Questions' },
  { id: 'execution', label: 'Execution problems' },
];

export function BotAutomations(props: {
  bot: Ops['bot_get']['reply']['bot'];
  projectName: string;
  edit: string | null;
  onEdit: (id: string | null) => void;
  onHistory: (id: string) => void;
  onRun: () => void;
}) {
  const read = useOp('trigger_list', () => ({ botId: props.bot.id }), { every: 30_000 });
  const signal = useAlive();
  const [busy, setBusy] = createSignal<string | null>(null);
  const [error, setError] = createSignal('');
  const [note, setNote] = createSignal('');
  const selected = createMemo(() => read.data?.triggers.filter(shown).find(a => a.id === props.edit));

  const act = async (automation: Automation, action: 'enable' | 'pause' | 'run') => {
    if (busy()) return;
    setBusy(automation.id);
    setError('');
    setNote('');
    try {
      const pending = () => setNote('Confirming automation action…');
      if (action === 'run') {
        const result = await writeOnce('bot_run', {
          botId: props.bot.id, automationId: automation.id, requestId: crypto.randomUUID(),
        }, pending, { signal });
        setNote(result.admitted ? 'Run queued.' : `Wake recorded (${result.run.reason ?? result.run.state}).`);
        props.onRun();
      } else {
        await writeOnce('trigger_save', {
          id: automation.id, baseRevision: automation.revision, settings: { enabled: action === 'enable' },
        }, pending, { signal });
        setNote(action === 'enable' ? 'Automation enabled.' : 'Automation paused.');
      }
      read.reload();
    } catch (err) {
      if (!signal.aborted) {
        setError(err instanceof Error ? err.message : String(err));
        setNote('');
        read.reload();
      }
    } finally {
      if (!signal.aborted) setBusy(null);
    }
  };

  return (
    <div class="stack">
      <Show when={read.error}>{err => <ErrorBanner text={err()} onRetry={read.reload} />}</Show>
      <Show when={error()}>{err => <ErrorBanner text={err()} />}</Show>
      <Show when={note()}>{msg => <p role="status" class="muted">{msg()}</p>}</Show>
      <Show when={!read.data && !read.error}><Skeleton rows={4} /></Show>
      <Show when={read.data}>
        <Switch>
          <Match when={props.edit && read.data?.canManage}>
            <Show when={props.edit} keyed>{edit => (
              <Show when={edit === 'new' || selected()} fallback={
                <div class="stack">
                  <Empty text="Automation unavailable." />
                  <Button label="Automations" icon="arrow-left" iconOnly onClick={() => props.onEdit(null)} />
                </div>
              }>
                <AutomationEditor bot={props.bot} projectName={props.projectName}
                  item={edit === 'new' ? undefined : selected()}
                  onClose={() => props.onEdit(null)}
                  onSaved={() => {
                    read.reload();
                    props.onEdit(null);
                    setNote('Automation saved.');
                  }} />
              </Show>
            )}</Show>
          </Match>
          <Match when={true}>
            <Show when={read.data?.canManage}>
              <div class="row">
                <Button label="New automation" icon="plus" kind="primary" onClick={() => props.onEdit('new')} />
              </div>
            </Show>
            <Show when={read.data?.triggers.filter(shown).length} fallback={<Empty text="No automations yet." />}>
              <For each={read.data?.triggers.filter(shown)}>{automation => (<Show when={settingsOf(automation)} keyed>{set => (
                <DetailSection title={set.name}>
                  <KvRow icon="folder-open" label="Project" value={props.projectName} />
                  <KvRow icon="bell" label="Trigger" value={triggerOptions.find(t => t.id === set.kind)?.label} />
                  <Show when={set.kind === 'needs_you'}>
                    <KvRow icon="bell" label="Events" value={<span>{eventOptions.find(t => t.id === set.event)?.label}</span>} />
                  </Show>
                  <Show when={set.kind === 'schedule'}>
                    <KvRow icon="clock" label="Daily time" value={`${set.scheduleTime} · ${set.scheduleZone}`} />
                  </Show>
                  <KvRow icon="play" label="Status" value={
                    !set.enabled ? 'Paused' : props.bot.paused ? 'Bot paused' : 'Enabled'
                  } />
                  <Show when={set.kind === 'schedule' && !props.bot.paused && automation.nextAt} keyed>{next => (
                    <KvRow icon="clock" label="Next scheduled run" value={<time datetime={next}>{zonedTime(Date.parse(next), set.scheduleZone)}</time>} />
                  )}</Show>
                  <KvRow icon="clock" label="Last outcome" value={
                    <Show when={automation.lastRun} keyed fallback={<span class="muted">No runs yet</span>}>{run => (
                      <span class="row" style={{ 'flex-wrap': 'wrap', gap: 'var(--space-2)' }}>
                        <StateBadge family="botRun" state={run.state} reason={run.reason}
                          unconfirmed={run.observation === 'unconfirmed'} />
                        <Ago t={Date.parse(run.at)} />
                      </span>
                    )}</Show>
                  } />
                  <div class="row" style={{ 'flex-wrap': 'wrap', gap: 'var(--space-2)' }}>
                    <Button label="Run history" kind="quiet" onClick={() => props.onHistory(automation.id)} />
                    <Show when={read.data?.canManage}>
                      <Button label="Edit" onClick={() => props.onEdit(automation.id)} />
                      <Button label={automation.settings.enabled ? 'Pause automation' : 'Enable automation'}
                        tip={automation.settings.enabled ? 'Pause future automatic wakes' : 'Enable future automatic wakes'}
                        busy={busy() === automation.id} disabledReason={busy() ? 'An automation action is pending' : undefined}
                        onClick={() => void act(automation, automation.settings.enabled ? 'pause' : 'enable')} />
                      <Button label="Run now" icon="play" busy={busy() === automation.id}
                        disabledReason={busy() ? 'An automation action is pending' : undefined}
                        onClick={() => void act(automation, 'run')} />
                    </Show>
                  </div>
                </DetailSection>
              )}</Show>)}</For>
            </Show>
          </Match>
        </Switch>
      </Show>
    </div>
  );
}

function AutomationEditor(props: {
  bot: Ops['bot_get']['reply']['bot'];
  projectName: string;
  item?: Automation;
  onClose: () => void;
  onSaved: () => void;
}) {
  const initial = untrack(() => props.item);
  const id = initial?.id ?? crypto.randomUUID();
  const [settings, setSettings] = createSignal<Settings>(initial ? settingsOf(initial) : {
    name: '', instructions: '', kind: 'manual', scheduleTime: '', scheduleZone: '', event: 'none', enabled: true,
  });
  const [busy, setBusy] = createSignal(false);
  const [error, setError] = createSignal('');
  const [nameError, setNameError] = createSignal('');
  const [timeError, setTimeError] = createSignal('');
  const [zoneError, setZoneError] = createSignal('');
  const [note, setNote] = createSignal('');
  const signal = useAlive();
  const patch = (values: Partial<Settings>) => setSettings(s => ({ ...s, ...values }));
  const changeKind = (kind: Settings['kind']) => {
    setTimeError('');
    setZoneError('');
    patch({ kind, scheduleTime: '', scheduleZone: kind === 'schedule' ? 'Etc/UTC' : '', event: kind === 'needs_you' ? 'all' : 'none' });
  };

  const save = async () => {
    if (busy()) return;
    const s = settings();
    const name = s.name.trim();
    const scheduleTime = s.scheduleTime.trim();
    const scheduleZone = s.scheduleZone.trim();
    setError('');
    setNameError(name ? '' : 'Enter an automation name.');
    setTimeError(s.kind === 'schedule' && !/^([01]\d|2[0-3]):[0-5]\d$/.test(scheduleTime) ? 'Enter a time in HH:MM.' : '');
    setZoneError(s.kind === 'schedule' && !scheduleZone ? 'Enter an IANA time zone.' : '');
    if (nameError() || timeError() || zoneError()) return;

    setBusy(true);
    try {
      await writeOnce('trigger_save', {
        id, baseRevision: initial?.revision ?? 0, botId: props.bot.id,
        settings: triggerOf({ ...s, name, scheduleTime, scheduleZone }),
      }, () => setNote('Confirming save…'), { signal });
      if (!signal.aborted) props.onSaved();
    } catch (err) {
      if (!signal.aborted) {
        setError(err instanceof Error ? err.message : String(err));
        setNote('');
      }
    } finally {
      if (!signal.aborted) setBusy(false);
    }
  };

  return (
    <DetailSection title={initial ? `Edit ${initial.settings.name}` : 'New automation'}>
      <Show when={error()}>{err => <ErrorBanner text={err()} />}</Show>
      <KvRow icon="folder-open" label="Project" value={props.projectName} />
      <KvRow icon="bot" label="Profile and tools" value="Inherited from the bot" />
      <TextField label="Name" value={settings().name} error={nameError()}
        onChange={name => { patch({ name }); setNameError(''); }} />
      <TextField label="Instructions" multiline value={settings().instructions} onChange={instructions => patch({ instructions })} />
      <Choice label="Trigger type" value={settings().kind} options={triggerOptions}
        onChange={kind => changeKind(kind as Settings['kind'])} />
      <Show when={settings().kind === 'schedule'}>
        <TextField label="Daily wake time" value={settings().scheduleTime} error={timeError()} help="Local time in HH:MM."
          onChange={scheduleTime => { patch({ scheduleTime }); setTimeError(''); }} />
        <TextField label="Time zone" value={settings().scheduleZone} error={zoneError()} help="Use an IANA time zone, such as Europe/Paris."
          onChange={scheduleZone => { patch({ scheduleZone }); setZoneError(''); }} />
        <p class="muted">After downtime, Fleet catches up today’s eligible wake. A clock change can skip the chosen time.</p>
      </Show>
      <Show when={settings().kind === 'needs_you'}>
        <Choice label="Event filter" value={settings().event} options={eventOptions}
          onChange={event => patch({ event: event as Settings['event'] })} />
      </Show>
      <div class="row" style={{ gap: 'var(--space-2)' }}>
        <Button label={initial ? 'Save changes' : 'Create automation'} kind="primary" busy={busy()} onClick={() => void save()} />
        <Button label="Cancel" disabledReason={busy() ? 'Save pending' : undefined} onClick={props.onClose} />
      </div>
      <Show when={note()}>{msg => <p role="status" class="muted">{msg()}</p>}</Show>
    </DetailSection>
  );
}
