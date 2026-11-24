// Conversation context uses the same Watch, Bots and Swarm records as the agent and management surfaces.
import { For, Match, Show, Switch, createMemo, createSignal, onSettled } from 'solid-js';
import {
  Button, DetailSection, Disclosure, Empty, EntityCode, ErrorBanner, Icon, InlineForm, KvRow,
  Skeleton, StateBadge, TextField, exact, states, type StateOf,
} from '../../../packages/ui/src/index.ts';
import type { SessionGetReply, WatchRecord, WatchTimerAction } from '../../../contracts/generated/ops.ts';
import { online, op, unknownOutcome, useAlive } from '../api.ts';
import { useLocation } from '../router.ts';
import { useOp } from '../store.ts';
import { automationTrigger, automationStatus } from './bot-automations.tsx';
import { SwarmPanel, hasSwarmBoard, useSwarmBoard } from './swarm-panel.tsx';

export function ConversationContext(props: { id: string; session: SessionGetReply; onClose: () => void }) {
  let panel: HTMLElement | undefined;
  onSettled(() => panel?.focus({ preventScroll: true }));
  const [, go] = useLocation();
  const bots = useOp('bot_list', () => props.session.projectId ? { projectId: props.session.projectId } : null);
  const bot = createMemo(() => bots.error ? undefined : bots.data?.bots.find(b => b.homeSessionId === props.session.id));
  const automations = useOp('bot_automations', () => bot() ? { botId: bot()!.id } : null, { every: 30_000 });
  const managed = useOp('bot_tasks', () => bot() ? { botId: bot()!.id } : null, { quiet: 500 });
  const board = useSwarmBoard('session', () => props.session.projectId ? props.session.id : null);
  const plan = () => board.error ? undefined : board.data?.plan;
  const members = () => board.error ? [] : board.data?.roster ?? [];
  const steps = createMemo(() => plan()?.threads.flatMap(thread => thread.tasks) ?? []);
  const relatedIds = createMemo(() => {
    const ids = new Set<string>();
    if (props.session.taskId) ids.add(props.session.taskId);
    if (props.session.deliveryId) ids.add(props.session.deliveryId);
    for (const task of props.session.resources.spawnedTasks) ids.add(task.taskId);
    for (const step of steps()) if (step.taskId) ids.add(step.taskId);
    if (!managed.error) for (const task of managed.data?.tasks ?? []) ids.add(task.taskId);
    return [...ids];
  });
  const openBot = (tab: 'automations' | 'tasks') => {
    const b = bot();
    if (b) go('/bots/' + encodeURIComponent(b.id) + '?tab=' + tab);
  };

  return (
    <aside class="conversation-context stack" id={props.id} aria-label="Conversation context" tabindex={-1}
      ref={panel} onKeyDown={e => {
        if (e.key === 'Escape' && !e.defaultPrevented) {
          e.stopPropagation();
          props.onClose();
        }
      }}>
      <div class="row">
        <strong>Context</strong><span class="grow" />
        <Button label="Close context" icon="x" iconOnly kind="quiet" onClick={props.onClose} />
      </div>
      <ConversationWatch sessionId={props.session.id} />
      <DetailSection title="Automations">
        <Switch>
          <Match when={bots.error}><ErrorBanner text="Linked bot unavailable" onRetry={bots.reload} /></Match>
          <Match when={automations.error}><ErrorBanner text="Automations unavailable" onRetry={automations.reload} /></Match>
          <Match when={props.session.projectId && (!bots.data || (bot() && !automations.data))}><Skeleton rows={2} /></Match>
          <Match when={true}>
            <Show when={automations.data?.automations.length} fallback={<Empty text="No linked automations." />}>
              <ul class="context-items">
                <For each={automations.data?.automations}>{a => (
                  <li><strong>{a.settings.name}</strong>
                    <div class="muted">{automationTrigger(a.settings)} · {automationStatus(a.settings, bot()?.paused === true)}</div>
                  </li>
                )}</For>
              </ul>
            </Show>
          </Match>
        </Switch>
        <Show when={bot()}>
          <Button label={automations.data?.canManage ? 'Manage automations' : 'View automations'} kind="quiet" onClick={() => openBot('automations')} />
        </Show>
      </DetailSection>
      <DetailSection title="Plan">
        <Switch>
          <Match when={board.error}><ErrorBanner text="Plan unavailable" onRetry={board.reload} /></Match>
          <Match when={props.session.projectId && !board.data}><Skeleton rows={2} /></Match>
          <Match when={plan()}>
            <p><strong>{plan()!.title}</strong></p>
            <Show when={steps().length} fallback={<Empty text="No plan steps recorded." />}>
              <For each={plan()!.threads}>{thread => (
                <div>
                  <strong>{thread.title}</strong>
                  <ol class="context-items">
                    <For each={thread.tasks}>{step => (
                      <li><PlanStep status={step.status} /> {step.title}
                        <Show when={step.ownerMember}><div class="muted">{step.ownerMember}</div></Show>
                      </li>
                    )}</For>
                  </ol>
                </div>
              )}</For>
            </Show>
          </Match>
          <Match when={true}><Empty text="No structured plan recorded." /></Match>
        </Switch>
      </DetailSection>
      <DetailSection title="Related tasks">
        <Show when={managed.error}><ErrorBanner text="Bot tasks unavailable" onRetry={managed.reload} /></Show>
        <Show when={props.session.resources.hidden > 0}><p class="muted">Some related work is unavailable.</p></Show>
        <Show when={relatedIds().length > 0}>
          <ul class="context-items">
            <For each={relatedIds()}>{id => <RelatedTask id={id} />}</For>
          </ul>
        </Show>
        <Show when={!relatedIds().length && !managed.error && !board.error && !bots.error && !props.session.resources.hidden
          && (!props.session.projectId || (bots.data && board.data && (!bot() || managed.data)))}>
          <Empty text="No related tasks." />
        </Show>
        <Show when={managed.data?.hasMore && !managed.error}>
          <Button label="All bot tasks" kind="quiet" onClick={() => openBot('tasks')} />
        </Show>
      </DetailSection>
      <DetailSection title="Swarm">
        <Switch>
          <Match when={board.error}><ErrorBanner text="Swarm unavailable" onRetry={board.reload} /></Match>
          <Match when={props.session.projectId && !board.data}><Skeleton rows={2} /></Match>
          <Match when={hasSwarmBoard(board.data)}>
            <Show when={members().length} fallback={<Empty text="No Swarm members recorded." />}>
              <ul class="context-items">
                <For each={members()}>{member => {
                  const assigned = createMemo(() => steps().filter(step => step.ownerMember === member.memberName));
                  return (
                    <li>
                      <div class="row"><strong>{member.memberName}</strong><span class="muted">{member.role}</span>
                        <Show when={member.sessionId}>
                          <EntityCode kind="conversation" id={member.sessionId} code={member.sessionCode} />
                        </Show>
                      </div>
                      <div class="muted">
                        {member.state && Object.hasOwn(states.execution, member.state) ? states.execution[member.state as StateOf<'execution'>].label
                          : member.state === 'idle' ? 'Idle' : 'State unknown'}
                        {assigned().length ? ` · ${assigned().filter(step => step.status === 'done').length} of ${assigned().length} assigned steps done` : ''}
                      </div>
                      <Show when={assigned().length}><div>{assigned().map(step => step.title).join(', ')}</div></Show>
                    </li>
                  );
                }}</For>
              </ul>
            </Show>
            <Disclosure summary="Swarm board">
              <SwarmPanel scopeKind="session" scopeId={props.session.id} board={board} />
            </Disclosure>
          </Match>
          <Match when={true}><Empty text="No Swarm activity." /></Match>
        </Switch>
      </DetailSection>
    </aside>
  );
}

const planState = { pending: 'Pending', in_progress: 'In progress', blocked: 'Blocked', review: 'In review', done: 'Done', failed: 'Failed', cancelled: 'Cancelled' };

function PlanStep(props: { status?: keyof typeof planState }) {
  return <span class="muted">
    <Show when={props.status === 'done'}><Icon name="check" /> </Show>
    {props.status ? planState[props.status] : 'Pending'} ·
  </span>;
}

function RelatedTask(props: { id: string }) {
  const task = useOp('entity_card', () => ({ kind: 'task', id: props.id }));
  return <li>
    <Switch>
      <Match when={task.error}><ErrorBanner text="Related task unavailable" onRetry={task.reload} /></Match>
      <Match when={task.data}>
        <div class="row"><EntityCode kind="task" id={props.id} code={task.data!.code} /><strong>{task.data!.title}</strong></div>
        <Show when={task.data?.state && Object.hasOwn(states.task, task.data.state)}
          fallback={<span class="muted">State unknown</span>}>
          <StateBadge family="task" state={task.data!.state as StateOf<'task'>} />
        </Show>
      </Match>
      <Match when={true}><Skeleton rows={1} /></Match>
    </Switch>
  </li>;
}

function ConversationWatch(props: { sessionId: string }) {
  const read = useOp('watch_get', () => ({ sessionId: props.sessionId }));
  const signal = useAlive();
  const [form, setForm] = createSignal<{ action: 'edit' | 'cancel'; base: WatchRecord } | null>(null);
  const [seconds, setSeconds] = createSignal('');
  const [busy, setBusy] = createSignal(false);
  const [error, setError] = createSignal('');
  const [invalid, setInvalid] = createSignal('');
  const watch = () => read.data?.watch;
  const reason = () => !online.value ? 'Offline' : read.error ? 'Watch unavailable'
    : read.data?.canManageTimer !== true ? 'Read-only' : busy() ? 'Saving timer' : undefined;

  const change = async (action: WatchTimerAction, base: WatchRecord) => {
    if (reason() || base.timerMs == null) return;
    const input = seconds().trim();
    const timerMs = Math.round(Number(input) * 1000);
    if (action === 'edit' && (!/^(?:\d+(?:\.\d{0,3})?|\.\d{1,3})$/.test(input) || !Number.isSafeInteger(timerMs) || timerMs < 1)) {
      setInvalid('Enter a positive interval in seconds, to the nearest millisecond.');
      return;
    }
    setBusy(true);
    setError('');
    let saved = false;
    try {
      await op('watch_timer_set', {
        sessionId: props.sessionId, action, baseTimerMs: base.timerMs, basePaused: base.timerPaused === true,
        ...(action === 'edit' ? { timerMs } : {}),
      }, { signal });
      saved = true;
    } catch (e) {
      if (!signal.aborted) setError(unknownOutcome(e)
        ? 'Timer change unconfirmed. Check the saved timer before retrying.'
        : (e instanceof Error ? e.message : String(e)));
    } finally {
      if (!signal.aborted) await read.reload();
      if (!signal.aborted) {
        if (saved) setForm(null);
        setBusy(false);
      }
    }
  };
  const edit = (action: 'edit' | 'cancel', base: WatchRecord) => {
    setSeconds(String((base.timerMs ?? 0) / 1000));
    setInvalid('');
    setError('');
    setForm({ action, base });
  };
  const next = () => {
    const value = watch()?.nextWakeAt;
    return value == null || !Number.isFinite(new Date(value).getTime()) ? null : value;
  };

  return <DetailSection title="Watch">
    <Show when={error()}><ErrorBanner text={error()} /></Show>
    <Switch>
      <Match when={read.error}><ErrorBanner text="Watch unavailable" onRetry={read.reload} /></Match>
      <Match when={!read.data}><Skeleton rows={2} /></Match>
      <Match when={!watch()}><Empty text="No Watch configured." /></Match>
      <Match when={watch()}>
        <KvRow icon="bell" label="Events" value={watch()!.kinds.join(', ') || 'None'} />
        <KvRow icon="clock" label="Timer" value={watch()!.timerMs == null ? 'No timer'
          : watch()!.timerPaused ? 'Paused' : next() === null ? 'State unknown' : 'Scheduled'} />
        <Show when={watch()!.timerMs != null}>
          <KvRow icon="clock" label="Interval" value={
            watch()!.timerMs! % 60_000 === 0 ? `${watch()!.timerMs! / 60_000} min` : `${watch()!.timerMs! / 1000} s`
          } />
          <Show when={!watch()!.timerPaused}>
            <KvRow icon="clock" label="Next wake" value={next() === null ? 'Unknown'
              : <time datetime={new Date(next()!).toISOString()}>{exact(next()!)}</time>} />
          </Show>
          <Show when={reason() && !busy()}><p class="muted">{reason()}</p></Show>
          <Show when={!form()} fallback={
            <Show when={form()} keyed>{f => f.action === 'edit' ? (
              <InlineForm cls="stack" onSubmit={e => { e.preventDefault(); void change('edit', f.base); }}>
                <TextField label="Interval in seconds" inputMode="decimal" value={seconds()}
                  onChange={value => { setSeconds(value); setInvalid(''); }} error={invalid()} disabledReason={reason()} />
                <div class="row">
                  <Button label="Save interval" type="submit" kind="primary" busy={busy()} disabledReason={reason()} />
                  <Button label="Discard edit" kind="quiet" disabledReason={busy() ? 'Saving timer' : undefined} onClick={() => setForm(null)} />
                </div>
              </InlineForm>
            ) : (
              <div class="stack">
                <p>Cancel this timer? Event subscriptions stay active.</p>
                <div class="row">
                  <Button label="Yes, cancel timer" kind="danger" busy={busy()} disabledReason={reason()} onClick={() => void change('cancel', f.base)} />
                  <Button label="Keep timer" kind="quiet" disabledReason={busy() ? 'Saving timer' : undefined} onClick={() => setForm(null)} />
                </div>
              </div>
            )}</Show>
          }>
            <div class="row">
              <Button label="Edit timer" disabledReason={reason()} onClick={() => edit('edit', watch()!)} />
              <Button label={watch()!.timerPaused ? 'Resume timer' : 'Pause timer'} disabledReason={reason()}
                onClick={() => void change(watch()!.timerPaused ? 'resume' : 'pause', watch()!)} />
              <Button label="Cancel timer" kind="quiet" disabledReason={reason()} onClick={() => edit('cancel', watch()!)} />
            </div>
          </Show>
        </Show>
        <Show when={watch()!.lastError}><ErrorBanner text={`Last Watch error: ${watch()!.lastError}`} /></Show>
      </Match>
    </Switch>
  </DetailSection>;
}
