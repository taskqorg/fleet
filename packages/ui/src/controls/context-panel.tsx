// Context panel beside a conversation: plan, timers, automations, swarm, tasks (controls/context-panel.md, OR-51, board masters
// `Context panel`, `Plan item`, `Timer row`, `Swarm member`). Part of the work area, never a floating layer (OR-27).
import { createEffect, createMemo, createSignal, createUniqueId, For, Show } from 'solid-js';
import type { JSX } from '@solidjs/web';
import type { Life } from '../registry.ts';
import { Icon, Button, Skeleton } from './primitives.tsx';
import { Avatar, type AvatarMark } from './avatar.tsx';
import { StateDot } from './state-dot.tsx';

/** One step of the session's plan: a read-only check square and the step's words; done steps in the success tone. */
export function PlanItem(p: { text: string; done?: boolean }) {
  return (
    <li class={['plan-item', { done: !!p.done }]} data-ui="plan-item">
      <span class="plan-box" role="checkbox" aria-checked={p.done ? 'true' : 'false'} aria-readonly="true" aria-label={p.text}>
        <Show when={p.done}><Icon name="check" /></Show>
      </span>
      <span class="plan-text" aria-hidden="true">{p.text}</span>
    </li>
  );
}

/** A timer: what it does, when it fires (`in 42m · once`), and Pause, Edit and Cancel; Cancel confirms in place. */
export function TimerRow(p: { text: string; when: string; paused?: boolean; readOnly?: boolean; onPause?: () => void; onEdit?: () => void; onCancel?: () => void }) {
  const [confirm, setConfirm] = createSignal(false);
  return (
    <div class={['timer-row', { paused: !!p.paused }]} data-ui="timer-row">
      <span class="timer-text"><span class="timer-what">{p.text}</span><span class="timer-when">{p.when}</span></span>
      <Show when={!p.readOnly}><Show
        when={confirm()}
        fallback={
          <span class="timer-acts">
            <Button label={p.paused ? 'Resume timer' : 'Pause timer'} icon={p.paused ? 'play' : 'pause'} iconOnly kind="quiet" onClick={() => p.onPause?.()} />
            <Button label="Edit timer" icon="pencil" iconOnly kind="quiet" onClick={() => p.onEdit?.()} />
            <Button label="Cancel timer" icon="x" iconOnly kind="quiet" onClick={() => setConfirm(true)} />
          </span>
        }
      >
        <span class="timer-acts">
          <Button label="Cancel timer" kind="danger" onClick={() => { setConfirm(false); p.onCancel?.(); }} />
          <Button label="Keep" kind="quiet" onClick={() => setConfirm(false)} />
        </span>
      </Show></Show>
    </div>
  );
}

const inApp = (href: string, open?: (href: string) => void) => (e: MouseEvent) => {
  if (open && !e.metaKey && !e.ctrlKey && !e.shiftKey) { e.preventDefault(); open(href); }
};

/** An agent of the session's swarm: avatar, role and target, its plan steps done (`1 of 2 steps`), state dot and word; it
 *  opens its own session view. */
export function SwarmMember(p: { name: string; avatar: AvatarMark; state: Life; href: string; progress?: string; onOpen?: (href: string) => void }) {
  return (
    <a class="swarm-member" data-ui="swarm-member" href={p.href} onClick={inApp(p.href, p.onOpen)}>
      <Avatar {...p.avatar} size="sm" />
      <span class="swarm-name">{p.name}</span>
      <Show when={p.progress}><span class="swarm-progress">{p.progress}</span></Show>
      <StateDot state={p.state} />
    </a>
  );
}

/** A task related to the session: its code and title, state dot and word; it opens the task. */
export function TaskLink(p: { code?: string; title: string; state: Life; href: string; onOpen?: (href: string) => void }) {
  return (
    <a class="swarm-member ctx-task" data-ui="task-link" href={p.href} onClick={inApp(p.href, p.onOpen)}>
      <span class="swarm-name"><Show when={p.code}><span class="ctx-code">{p.code}</span> </Show>{p.title}</span>
      <StateDot state={p.state} />
    </a>
  );
}

function Part(p: { title: string; children: JSX.Element }) {
  const id = createUniqueId();
  return (
    <section class="ctx-part" aria-labelledby={id}>
      <h3 id={id}>{p.title}</h3>
      {p.children}
    </section>
  );
}

export type PlanStep = { text: string; done?: boolean };
export type Timer = { id: string; text: string; when: string; paused?: boolean; readOnly?: boolean };
export type Automation = { rule: string; when: string };
export type Member = { id: string; name: string; avatar: AvatarMark; state: Life; href: string; progress?: string };
export type RelatedTask = { id: string; code?: string; title: string; state: Life; href: string };

/**
 * Sections in this order, each hidden when empty (OR-29): Plan (`Plan · 3 of 5`), Timers, Automations, Swarm
 * (`Swarm · 2 of 3 done`), Tasks, then a quiet line that points to Details for model, tokens, cost and ids (OR-54).
 */
export function ContextPanel(p: {
  plan?: PlanStep[]; timers?: Timer[]; automations?: Automation[]; swarm?: Member[]; tasks?: RelatedTask[]; loading?: boolean;
  onHide?: () => void; onTimer?: (id: string, action: 'pause' | 'edit' | 'cancel') => void;
  /** Opens a swarm member's session or a task in the app (a plain click); without it the link loads the page. */
  onOpen?: (href: string) => void;
}) {
  const plan = createMemo(() => p.plan ?? []);
  const swarm = createMemo(() => p.swarm ?? []);
  return (
    <aside class="ctx-panel" data-ui="context-panel" aria-label="Session context">
      <div class="ctx-head">
        <b>Context</b>
        <Show when={p.onHide}><Button label="Hide context" icon="panel-right-close" iconOnly kind="quiet" onClick={() => p.onHide?.()} /></Show>
      </div>
      <Show when={!p.loading} fallback={<Skeleton rows={6} />}>
        <Show when={plan().length > 0}>
          <Part title={`Plan · ${plan().filter(s => s.done).length} of ${plan().length}`}>
            <ul class="plan"><For each={plan()}>{s => <PlanItem text={s.text} done={s.done} />}</For></ul>
          </Part>
        </Show>
        <Show when={(p.timers ?? []).length > 0}>
          <Part title="Timers">
            <For each={p.timers}>
              {t => <TimerRow text={t.text} when={t.when} paused={t.paused} readOnly={t.readOnly} onPause={() => p.onTimer?.(t.id, 'pause')} onEdit={() => p.onTimer?.(t.id, 'edit')} onCancel={() => p.onTimer?.(t.id, 'cancel')} />}
            </For>
          </Part>
        </Show>
        <Show when={(p.automations ?? []).length > 0}>
          <Part title="Automations">
            <For each={p.automations}>{a => <p class="ctx-line">{a.rule} · {a.when}</p>}</For>
          </Part>
        </Show>
        <Show when={swarm().length > 0}>
          <Part title={`Swarm · ${swarm().filter(m => m.state === 'done').length} of ${swarm().length} done`}>
            <For each={swarm()}>{m => <SwarmMember name={m.name} avatar={m.avatar} state={m.state} href={m.href} progress={m.progress} onOpen={p.onOpen} />}</For>
          </Part>
        </Show>
        <Show when={(p.tasks ?? []).length > 0}>
          <Part title="Tasks">
            <For each={p.tasks}>{t => <TaskLink code={t.code} title={t.title} state={t.state} href={t.href} onOpen={p.onOpen} />}</For>
          </Part>
        </Show>
        <p class="ctx-details">Model, tokens, cost and ids: Details</p>
      </Show>
    </aside>
  );
}

/**
 * The context panel as a sheet at phone width (OR-51, OR-60): a native modal <dialog> from the bottom edge. Escape, the
 * Back gesture and a tap outside close it; focus returns to the Context icon that opened it.
 */
export function ContextSheet(p: { open: boolean; onClose: () => void; children: JSX.Element }) {
  let dialog: HTMLDialogElement | undefined;
  createEffect(() => p.open, open => {
    if (!dialog) return;
    if (open && !dialog.open) dialog.showModal();
    else if (!open && dialog.open) dialog.close();
  });
  return (
    <dialog ref={el => { dialog = el; }} class="ctx-sheet" data-ui="context-sheet" closedby="any" aria-label="Session context" onClose={() => p.onClose()}>
      {p.children}
    </dialog>
  );
}
