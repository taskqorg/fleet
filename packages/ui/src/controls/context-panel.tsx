// Context panel beside a conversation: plan, timers, automations, swarm (controls/context-panel.md, OR-51, board masters
// `Context panel`, `Plan item`, `Timer row`, `Swarm member`). Part of the work area, never a floating layer (OR-27).
import { createMemo, createSignal, createUniqueId, For, Show } from 'solid-js';
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
export function TimerRow(p: { text: string; when: string; paused?: boolean; onPause?: () => void; onEdit?: () => void; onCancel?: () => void }) {
  const [confirm, setConfirm] = createSignal(false);
  return (
    <div class={['timer-row', { paused: !!p.paused }]} data-ui="timer-row">
      <span class="timer-text"><span class="timer-what">{p.text}</span><span class="timer-when">{p.when}</span></span>
      <Show
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
      </Show>
    </div>
  );
}

/** An agent of the session's swarm: avatar, role and target, state dot and word; it opens its own session view. */
export function SwarmMember(p: { name: string; avatar: AvatarMark; state: Life; href: string }) {
  return (
    <a class="swarm-member" data-ui="swarm-member" href={p.href}>
      <Avatar {...p.avatar} size="sm" />
      <span class="swarm-name">{p.name}</span>
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
export type Timer = { id: string; text: string; when: string; paused?: boolean };
export type Automation = { rule: string; when: string };
export type Member = { id: string; name: string; avatar: AvatarMark; state: Life; href: string };

/**
 * Sections in this order, each hidden when empty (OR-29): Plan (`Plan · 3 of 5`), Timers, Automations, Swarm
 * (`Swarm · 2 of 3 done`), then a quiet line that points to Details for model, tokens, cost and ids (OR-54).
 */
export function ContextPanel(p: {
  plan?: PlanStep[]; timers?: Timer[]; automations?: Automation[]; swarm?: Member[]; loading?: boolean;
  onHide?: () => void; onTimer?: (id: string, action: 'pause' | 'edit' | 'cancel') => void;
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
              {t => <TimerRow text={t.text} when={t.when} paused={t.paused} onPause={() => p.onTimer?.(t.id, 'pause')} onEdit={() => p.onTimer?.(t.id, 'edit')} onCancel={() => p.onTimer?.(t.id, 'cancel')} />}
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
            <For each={swarm()}>{m => <SwarmMember name={m.name} avatar={m.avatar} state={m.state} href={m.href} />}</For>
          </Part>
        </Show>
        <p class="ctx-details">Model, tokens, cost and ids: Details</p>
      </Show>
    </aside>
  );
}
