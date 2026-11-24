// Primitive controls, badges, numbers, age clocks, harness marks, status states, copy, and search input.
import { createEffect, createMemo, createSignal, For, onSettled, Repeat, Show, untrack, type Accessor } from 'solid-js';
import type { JSX } from '@solidjs/web';
import { Button as SdkButton, Input, Menu, MenuContent, MenuItem, MenuTrigger } from '@ui-sdk/solid';
import { type IconName, type Family, type StateOf, type StateDef, type Tone, type Harness, type Stage, iconSvg, states, actions, harnesses, stages } from '../registry.ts';
import { type Measured, type Kind, type CostFigureLike, costMeasured, splitCostFigures, measured, age, exact } from '../format.ts';
import { isSubmitEnter } from '../keys.ts';

/* ---------- Icon and button ---------- */

/** Registry icon. With `label` it is meaningful (tooltip + accessible name); without, decorative. */
export function Icon(p: { name: IconName; label?: string }) {
  return (
    <span
      class="icon"
      role={p.label ? 'img' : undefined}
      aria-label={p.label}
      aria-hidden={p.label ? undefined : 'true'}
      innerHTML={iconSvg(p.name)}
    />
  );
}

type ButtonProps = {
  label: string; icon?: IconName; iconOnly?: boolean; kind?: 'primary' | 'default' | 'quiet' | 'attention' | 'danger';
  kbd?: string; rowLink?: boolean; busy?: boolean; disabledReason?: string; tip?: string; pressed?: boolean;
  /** A fuller accessible name than the visible label ("Wake Release manager"). */
  name?: string;
  onClick?: (event: MouseEvent) => void;
};
export function Button(p: ButtonProps) {
  const disabled = () => !!(p.busy || p.disabledReason);
  return (
    <SdkButton
      data-ui="button"
      class={['btn', p.kind ?? 'default', { 'icon-only': !!p.iconOnly, 'row-link': !!p.rowLink, busy: !!p.busy }]}
      disabled={disabled()}
      focusableWhenDisabled={true}
      aria-label={p.iconOnly ? p.label : p.name}
      data-tip={p.disabledReason ?? p.tip ?? (p.iconOnly ? p.label : undefined)}
      aria-pressed={p.pressed === undefined ? undefined : p.pressed ? 'true' : 'false'}
      onClick={e => {
        if (!disabled()) p.onClick?.(e);
      }}
    >
      <Show when={p.icon} keyed>{icon => <Icon name={icon} />}</Show>
      <Show when={!p.iconOnly}><span>{p.label}</span></Show>
      <Show when={p.kbd} keyed>{kbd => <kbd>{kbd}</kbd>}</Show>
    </SdkButton>
  );
}

/* ---------- State badge (OR-1, D7) ---------- */

type BadgeProps<F extends Family> = {
  family: F; state: StateOf<F>; reason?: string;
  /** Last contact time when the observation is unconfirmed. */
  lastSeen?: number; unconfirmed?: boolean; now?: number;
  /** Icon only, word in tooltip: for dense columns. */
  compact?: boolean;
};
export function StateBadge<F extends Family>(p: BadgeProps<F>) {
  const d = createMemo(() => states[p.family][p.state] as StateDef);
  const unconf = createMemo(() => p.unconfirmed || p.lastSeen !== undefined);
  const seen = createMemo(() => p.lastSeen !== undefined ? exact(p.lastSeen) : null);
  const name = createMemo(() => [d().label, unconf() && 'unconfirmed', seen() && `last seen ${seen()}`, p.reason].filter(Boolean).join(', '));
  return (
    <span class="state" data-ui="state-badge">
      <span
        class={['badge', `t-${d().tone}`, { unconf: unconf(), compact: !!p.compact }]}
        role="img"
        aria-label={name()}
        data-tip={p.compact || unconf() ? name() : undefined}
      >
        <Icon name={d().icon} />
        <Show when={!p.compact}><span>{d().label}</span></Show>
      </span>
      <Show when={(unconf() || p.reason) && !p.compact}>
        <span class={['reason', { unconf: unconf() }]} aria-hidden="true">
          <Show when={unconf()}>
            <>Unconfirmed<Show when={p.lastSeen !== undefined}> <> · seen <Ago t={p.lastSeen!} /></></Show></>
          </Show>
          {unconf() && p.reason ? ' · ' : ''}{p.reason}
        </span>
      </Show>
    </span>
  );
}

/* ---------- Numbers with basis, change tint (§2, OR-6, OR-7) ---------- */

type NumProps = {
  m: Measured; kind: Kind;
  /** What a rise means: tint green/red only when known (OR-6). */
  rise?: 'good' | 'bad' | 'neutral';
};
export function Num(p: NumProps) {
  const f = createMemo(() => measured(p.m, p.kind));
  let last = untrack(() => p.m.value);
  const [tint, setTint] = createSignal('', { ownedWrite: true });
  createEffect(() => p.m.value, val => {
    const before = last;
    last = val;
    if (before === null || val === null || before === val) return;
    const up = val > before;
    const rise = p.rise ?? 'neutral';
    setTint(rise === 'neutral' ? 'tint-neutral' : (up === (rise === 'good')) ? 'tint-good' : 'tint-bad');
    const t = setTimeout(() => setTint(''), 2000);
    return () => clearTimeout(t);
  });
  const tip = createMemo(() => p.m.basis === 'measured' && !p.m.partial ? undefined : f().long);
  return (
    <span
      data-ui="number"
      class={['num', `b-${p.m.basis}`, { null: p.m.value === null, partial: !!p.m.partial }, tint()]}
      data-tip={tip()}
      aria-label={f().long}
      tabindex={tip() ? 0 : undefined}
    >
      <Show when={p.m.basis === 'subscription'}><Icon name="rotate-cw" /></Show>
      {f().text}
    </span>
  );
}

export type TotalItem =
  | { label: string; m: Measured; kind: Kind; note?: string; cost?: never }
  | { label: string; cost: readonly CostFigureLike[]; note?: string; showUnavailable?: boolean; m?: never; kind?: never };

export function Totals(p: { items: TotalItem[]; compact?: boolean }) {
  return (
    <dl class={['totals', { compact: !!p.compact }]} data-ui="number">
      <For each={p.items}>
        {i => (
          <div>
            <dt>{i.label}</dt>
            <dd>
              {i.cost !== undefined ? <CostFigures cost={i.cost} showUnavailable={i.showUnavailable} empty="—" /> : <Num m={i.m!} kind={i.kind!} />}
            </dd>
            <Show when={i.note} keyed>{n => <dd class="note">{n}</dd>}</Show>
          </div>
        )}
      </For>
    </dl>
  );
}

/** Renders multi-currency CostFigure[] while keeping metered spend and subscription equivalents separate. */
export function CostFigures(p: { cost: readonly CostFigureLike[] | null | undefined; showUnavailable?: boolean; empty?: string }) {
  const split = createMemo(() => splitCostFigures(p.cost));
  const visMetered = createMemo(() => p.showUnavailable ? split().metered : split().metered.filter(c => c.value !== null && c.basis !== 'unavailable'));
  const subscription = createMemo(() => split().subscription);
  return (
    <Show
      when={visMetered().length > 0 || subscription().length > 0}
      fallback={<Show when={p.empty} keyed>{e => <span class="muted">{e}</span>}</Show>}
    >
      <span class="cost-figs" data-ui="number">
        <For each={visMetered()}>{c => <Num m={costMeasured(c)} kind="money" />}</For>
        <For each={subscription()}>{c => <Num m={costMeasured(c)} kind="money" />}</For>
      </span>
    </Show>
  );
}

/** Live clock accessor updating every `ms` while `active` (open layers whose ages count on; views that judge liveness by the clock). */
export function useTick(ms: number, active: boolean | Accessor<boolean> = true): Accessor<number> {
  const [now, setNow] = createSignal(Date.now(), { ownedWrite: true });
  const isActive = createMemo(() => typeof active === 'function' ? active() : active);
  createEffect(() => [ms, isActive()] as const, ([intervalMs, on]) => {
    if (!on) return;
    setNow(Date.now());
    const t = setInterval(() => setNow(Date.now()), intervalMs);
    return () => clearInterval(t);
  });
  return now;
}

const ageListeners = new Set<(now: number) => void>();
let ageTimer: ReturnType<typeof setInterval> | undefined;

function useAgeClock(t: Accessor<number>, alertAfter: Accessor<number | undefined>, offset: Accessor<number>): Accessor<number> {
  const [now, setNow] = createSignal(Date.now() + untrack(offset), { ownedWrite: true });
  createEffect(() => [t(), alertAfter(), offset()] as const, ([ts, after, off]) => {
    if (!Number.isFinite(ts)) return;
    const updateKey = (cur: number) => `${age(ts, cur).text}:${after !== undefined && cur - ts >= after * 1000}`;
    let previous = updateKey(Date.now() + off);
    const update = (clock: number) => {
      const nextNow = clock + off;
      const next = updateKey(nextNow);
      if (next === previous) return;
      previous = next;
      setNow(nextNow);
    };
    update(Date.now());
    ageListeners.add(update);
    ageTimer ??= setInterval(() => {
      const clock = Date.now();
      ageListeners.forEach(listener => listener(clock));
    }, 1000);
    return () => {
      ageListeners.delete(update);
      if (!ageListeners.size) {
        clearInterval(ageTimer);
        ageTimer = undefined;
      }
    };
  });
  return now;
}

type AgeProps = { t: number; clockOffsetMs?: number; alertAfter?: number; tone?: Tone; alertLabel?: string };

/** Compact live age with the original timestamp available on hover. */
export function Ago(p: AgeProps & { chip?: boolean }) {
  const now = useAgeClock(() => p.t, () => p.alertAfter, () => p.clockOffsetMs ?? 0);
  const cls = () => p.chip ? 'chip-age' : 'when';
  return (
    <Show
      when={Number.isFinite(p.t)}
      fallback={<span class={cls()} aria-label="Time unavailable">—</span>}
    >
      {(() => {
        const a = createMemo(() => age(p.t, now()));
        const alert = createMemo(() => p.alertAfter !== undefined && now() - p.t >= p.alertAfter * 1000);
        const alertLabel = () => p.alertLabel ?? 'needs a look';
        const tone = () => p.tone ?? 'danger';
        return (
          <time
            class={[cls(), alert() ? `t-${tone()} alert` : '']}
            data-ui={p.chip ? 'number' : undefined}
            datetime={new Date(p.t).toISOString()}
            data-tip={alert() ? `${alertLabel()} · ${exact(p.t)}` : exact(p.t)}
            aria-label={alert() ? `${a().long}, ${alertLabel()}` : a().long}
          >
            <Show when={alert()}><Icon name="triangle-alert" /></Show>
            {a().text}
          </time>
        );
      })()}
    </Show>
  );
}

export function AgeChip(props: AgeProps) {
  return <Ago t={props.t} clockOffsetMs={props.clockOffsetMs} alertAfter={props.alertAfter} tone={props.tone} alertLabel={props.alertLabel} chip />;
}

/* ---------- Harness mark, harness counts, stage chip (D11) ---------- */

/** Which harness runs it: the mark only, name in the tooltip (OR-1, OR-20). */
export function HarnessMark(p: { h: Harness }) {
  return (
    <span class="harness" data-ui="harness-counts" data-tip={harnesses[p.h].label} role="img" aria-label={harnesses[p.h].label}>
      <Icon name={harnesses[p.h].icon} />
    </span>
  );
}

/** Counts per harness as mark + number; zero counts are omitted. */
export function HarnessCounts(p: { counts: Partial<Record<Harness, number>>; label: string }) {
  const parts = createMemo(() => (Object.keys(harnesses) as Harness[]).filter(h => (p.counts[h] ?? 0) > 0));
  return (
    <span class="harness-counts" data-ui="harness-counts" role="group" aria-label={p.label}>
      <For each={parts()}>
        {h => (
          <span class="hc" data-tip={harnesses[h].label} aria-label={`${harnesses[h].label} ${p.counts[h]}`}>
            <Icon name={harnesses[h].icon} />
            <span class="num">{p.counts[h]}</span>
          </span>
        )}
      </For>
    </span>
  );
}

/** Delivery stage as a compact chip: icon, plus the short name only where the icon is not enough. */
export function StageChip(p: { stage: Stage }) {
  // A stage this build does not know (a newer server) shows a neutral chip with its own name instead of throwing
  // (Bugsink 787ca22f: "Cannot read properties of undefined (reading 'tone')").
  const d = createMemo((): StateDef => (stages[p.stage] as StateDef | undefined) ?? { label: String(p.stage), tone: 'neutral', icon: 'circle-dashed' });
  return (
    <span class={['badge', 'compact', `t-${d().tone}`]} data-ui="stage-chip" role="img" aria-label={d().label} data-tip={d().label}>
      <Icon name={d().icon} />
    </span>
  );
}

/* ---------- Empty, loading, error, stale (§8) ---------- */

export function Skeleton(p: { rows: number }) {
  return (
    <div class="skeleton" data-ui="status-states" aria-busy="true" aria-label="Loading">
      <Repeat count={p.rows}>{i => <div class="sk-row"><i style={{ width: `${55 + (i * 17) % 35}%` }} /><i /></div>}</Repeat>
    </div>
  );
}
export function Empty(p: { text: string; action?: JSX.Element }) {
  return <div class="empty" data-ui="status-states"><p>{p.text}</p>{p.action}</div>;
}
export function ErrorBanner(p: { text: string; onRetry?: () => void }) {
  return (
    <div class="err-banner" data-ui="status-states" role="alert">
      <Icon name="circle-alert" />
      <span>{p.text}</span>
      <span class="grow" />
      <Show when={p.onRetry} keyed>{retry => <Button label="Retry" icon={actions.retry} onClick={retry} />}</Show>
    </div>
  );
}
export function Stale(p: { at: number; now: number; onRefresh?: () => void }) {
  return (
    <span class="stale" data-ui="status-states">
      updated <Ago t={p.at} /> <Button label="Refresh" icon={actions.retry} iconOnly onClick={p.onRefresh} />
    </span>
  );
}

/* ---------- Offline marker (controls/offline-marker.md) ---------- */

/** Says whether shown data is current: offline, updating (stale but online) or signed out; hidden when current. */
export function OfflineMarker(p: { state: 'current' | 'offline' | 'updating' | 'signed-out'; since?: number; now: number }) {
  return (
    <Show when={p.state !== 'current'}>
      {(() => {
        const lastUpdate = createMemo(() => p.since ? exact(p.since) : null);
        const word = createMemo(() => p.state === 'offline' ? 'offline' : p.state === 'updating' ? 'updating' : 'signed out');
        const long = createMemo(() => p.state === 'signed-out' ? 'Session expired. Sign in again.' : `${word()}${lastUpdate() ? `, last updated ${lastUpdate()}` : ''}`);
        const tone = createMemo(() => p.state === 'signed-out' ? 'danger' : p.state === 'offline' ? 'attention' : 'neutral');
        return (
          <span class={['offline-marker', `t-${tone()}`]} data-ui="offline-marker" role="status" aria-label={long()}>
            <Icon name={p.state === 'updating' ? 'rotate-cw' : p.state === 'offline' ? 'circle-slash' : 'circle-alert'} />
            {word()}
            <Show when={p.since && p.state !== 'signed-out'}> <> · <Ago t={p.since!} /></></Show>
            <Show when={p.state === 'signed-out'}><a href={location.pathname}>Sign in again</a></Show>
          </span>
        );
      })()}
    </Show>
  );
}

/** Reserved two-line feedback region; updates never displace adjacent controls. */
export function StatusLine(p: { children?: JSX.Element }) {
  return <div role="status" style={{ 'min-height': '2lh' }}>{p.children}</div>;
}

/** Writes text to the clipboard; the caller shows a failure (status line with retry). */
export const copyText = (text: string) => navigator.clipboard.writeText(text);

/** A copy action: its icon turns into a check (a cross when the copy failed) for a moment and the result is announced;
 *  the button keeps its width (OR-23). */
export function CopyButton(p: { label: string; value: string; iconOnly?: boolean }) {
  const [done, setDone] = createSignal<boolean | null>(null);
  let timer: ReturnType<typeof setTimeout> | undefined;
  onSettled(() => () => clearTimeout(timer));
  const show = (ok: boolean) => {
    setDone(ok);
    clearTimeout(timer);
    timer = setTimeout(() => setDone(null), 1500);
  };
  return (
    <>
      <Button
        label={p.label}
        icon={done() === null ? actions.copy : done() ? 'check' : 'circle-x'}
        kind="quiet"
        iconOnly={p.iconOnly}
        onClick={() => copyText(p.value).then(() => show(true), () => show(false))}
      />
      <span class="sr-only" role="status">
        {done() === null ? '' : done() ? `${p.label.replace(/^Copy /, '')} copied` : 'Copy failed'}
      </span>
    </>
  );
}

/** Focuses an element once it is attached to the DOM. */
export const focusOnMount = (el: HTMLElement | null) => {
  if (el) queueMicrotask(() => { if (el.isConnected) el.focus(); });
};

/* ---------- Anchored native popovers (modern-web-guidance: viewport-aware-dropdowns) ---------- */

export type PopoverElement = HTMLElement & {
  showPopover(options?: { source?: HTMLElement }): void;
  hidePopover(): void;
};

/** Declarative popover attribute without TypeScript `any` (UI-ANY). */
export const popoverAttr = (kind: 'auto' | 'hint' | 'manual'): Record<string, string> => ({ popover: kind });

/**
 * Opens a native auto-dismiss popover tethered to `triggerRef` via `showPopover({ source })` and syncs browser
 * light-dismiss / Escape back to `onClose`. Guards re-clicking `triggerRef` so pointerdown light-dismiss does not
 * immediately re-open the popover on `click`.
 */
export function useAnchoredPopover(
  open: Accessor<boolean>,
  onClose: () => void,
  triggerRef: () => HTMLElement | null | undefined,
  popoverRef: () => HTMLElement | null | undefined,
) {
  let suppressClick = false;
  onSettled(() => {
    const trigger = triggerRef();
    if (!trigger) return;
    let clearTimer = 0;
    const onTriggerPointerDown = () => {
      const el = popoverRef() as PopoverElement | null | undefined;
      if (el?.matches(':popover-open')) {
        suppressClick = true;
        clearTimeout(clearTimer);
        clearTimer = window.setTimeout(() => { suppressClick = false; }, 400);
      }
    };
    const onTriggerClick = (e: MouseEvent) => {
      if (!suppressClick) return;
      suppressClick = false;
      clearTimeout(clearTimer);
      e.preventDefault();
      e.stopImmediatePropagation();
    };
    trigger.addEventListener('pointerdown', onTriggerPointerDown, true);
    trigger.addEventListener('click', onTriggerClick, true);
    return () => {
      clearTimeout(clearTimer);
      trigger.removeEventListener('pointerdown', onTriggerPointerDown, true);
      trigger.removeEventListener('click', onTriggerClick, true);
    };
  });
  createEffect(open, isOpen => {
    const el = popoverRef() as PopoverElement | null | undefined;
    const trigger = triggerRef();
    if (!isOpen || !el) return;
    const onCloseEvent = (e: Event) => {
      if ((e as Event & { newState?: string }).newState === 'closed') onClose();
    };
    const onEscape = (e: KeyboardEvent) => {
      if (e.key === 'Escape' && el.matches(':popover-open')) {
        e.preventDefault();
        onClose();
      }
    };
    el.addEventListener('beforetoggle', onCloseEvent);
    el.addEventListener('toggle', onCloseEvent);
    document.addEventListener('keydown', onEscape);
    if (!el.matches(':popover-open')) el.showPopover(trigger ? { source: trigger } : undefined);
    return () => {
      el.removeEventListener('beforetoggle', onCloseEvent);
      el.removeEventListener('toggle', onCloseEvent);
      document.removeEventListener('keydown', onEscape);
      if (el.matches(':popover-open')) el.hidePopover();
    };
  });
}

/* ---------- Split button (split-button.md) ---------- */

/** Default action plus a short menu of alternatives for the same place (OR-14 allows short action menus). */
export function SplitButton(p: {
  main: { label: string; icon?: IconName; onClick?: () => void };
  alternatives: { label: string; icon?: IconName; onClick?: () => void }[]; kind?: 'primary' | 'default';
}) {
  let wrap: HTMLSpanElement | undefined;
  return (
    <span ref={el => { wrap = el; }} class={['split', p.kind ?? 'default']} data-ui="split-button">
      <Button label={p.main.label} icon={p.main.icon} kind={p.kind ?? 'default'} onClick={p.main.onClick} />
      <Menu container={wrap?.closest<HTMLElement>('.th-dark, .th-light') ?? undefined}>
        <MenuTrigger
          class={['btn', p.kind ?? 'default', 'split-more']}
          aria-label={`More actions for ${p.main.label}`}
        >
          <Icon name="chevron-down" />
        </MenuTrigger>
        <MenuContent class="menu" side="bottom" align="end">
          <For each={p.alternatives}>
            {a => (
              <MenuItem
                render={itemProps => <button {...itemProps} type="button" />}
                onSelect={() => a.onClick?.()}
              >
                <Show when={a.icon} keyed>{icon => <Icon name={icon} />}</Show>
                {a.label}
              </MenuItem>
            )}
          </For>
        </MenuContent>
      </Menu>
    </span>
  );
}

/* ---------- Action menu: one icon button, a short left-aligned menu with icons (OR-13, OR-14) ---------- */

export function ActionMenu(p: {
  label: string;
  items: { label: string; icon: IconName; onClick: () => void; disabledReason?: string }[];
  disabledReason?: string;
}) {
  let wrap: HTMLSpanElement | undefined;
  return (
    <span ref={el => { wrap = el; }} class="split action-menu" data-ui="split-button">
      <Menu container={wrap?.closest<HTMLElement>('.th-dark, .th-light') ?? undefined}>
        <MenuTrigger
          class="btn default icon-only"
          disabled={!!p.disabledReason}
          focusableWhenDisabled={true}
          aria-label={p.label}
          data-tip={p.disabledReason ?? p.label}
        >
          <Icon name={actions.more} />
        </MenuTrigger>
        <MenuContent class="menu" side="bottom" align="end">
          <For each={p.items}>
            {a => (
              <MenuItem
                render={itemProps => <button {...itemProps} type="button" />}
                disabled={!!a.disabledReason}
                data-tip={a.disabledReason}
                onSelect={() => { if (!a.disabledReason) a.onClick(); }}
              >
                <Icon name={a.icon} />
                {a.label}
              </MenuItem>
            )}
          </For>
        </MenuContent>
      </Menu>
    </span>
  );
}

/**
 * Search as an icon (OR-31): the field shows only while focused or not empty. Filters are typed in it (OR-36), e.g.
 * `agent:claude host:mac`; the list's server reads the terms.
 */
export function SearchBox(p: {
  label: string; value: string; onInput: (v: string) => void; onEnter?: () => void;
  /** A picker whose job is typing keeps the field shown. */ always?: boolean;
}) {
  const [focused, setOpen] = createSignal(false);
  const open = createMemo(() => focused() || !!p.always);
  let grab = false;
  let closing: ReturnType<typeof setTimeout> | undefined;
  onSettled(() => () => clearTimeout(closing));
  return (
    <Show
      when={open() || !!p.value}
      fallback={<Button label={p.label} icon={actions.search} iconOnly kind="quiet" onClick={() => { grab = true; setOpen(true); }} />}
    >
      <label class="search" data-ui="search">
        <Icon name={actions.search} />
        <Input
          type="search"
          value={p.value}
          placeholder={p.label}
          aria-label={p.label}
          ref={el => {
            if (grab) {
              grab = false;
              queueMicrotask(() => { if (el.isConnected) el.focus(); });
            }
          }}
          onFocus={() => { clearTimeout(closing); setOpen(true); }}
          onBlur={() => { closing = setTimeout(() => setOpen(false), 250); }}
          onKeyDown={e => {
            if (isSubmitEnter(e)) p.onEnter?.();
            if (e.key === 'Escape') { p.onInput(''); e.currentTarget.blur(); }
          }}
          onValueChange={v => p.onInput(v)}
        />
      </label>
    </Show>
  );
}

/* ---------- Problem row ---------- */

export function ProblemRow(p: {
  title: string; facts: JSX.Element; severity: 'warning' | 'failure';
  actions: { label: string; icon?: IconName; primary?: boolean; onClick?: () => void }[];
  /** Repeats of the same problem, aggregated into this one row (OR-58). */
  count?: number;
  /** Opens the problem's details, on the right of the title line. */
  onDetails?: () => void;
}) {
  return (
    <div data-ui="problem-row" class={['problem', p.severity]} role="group" aria-label={`${p.severity === 'failure' ? 'Failure' : 'Warning'}: ${p.title}${(p.count ?? 1) > 1 ? `, ${p.count} times` : ''}`}>
      <Icon name={p.severity === 'failure' ? 'octagon-x' : 'triangle-alert'} />
      <b>{p.title}<Show when={(p.count ?? 1) > 1}> <span class="pr-n">×{p.count}</span></Show></b>
      <Show when={p.onDetails} keyed>{d => <Button label="Details" kind="quiet" onClick={d} />}</Show>
      <span class="facts" data-important="">{p.facts}</span>
      <span class="acts">
        <For each={p.actions}>
          {a => <Button label={a.label} icon={a.icon} kind={a.primary ? 'primary' : 'default'} onClick={a.onClick} />}
        </For>
      </span>
    </div>
  );
}

/* ---------- Detail sections (D11, C3) ---------- */

export function DetailSection(p: { title: string; children: JSX.Element }) {
  return <section class="dsec" data-ui="detail-section"><h3>{p.title}</h3>{p.children}</section>;
}
export function KvRow(p: { icon: IconName; label: string; meta?: JSX.Element; value?: JSX.Element }) {
  return (
    <div class="kv-row">
      <Icon name={p.icon} />
      <b>{p.label}</b>
      <Show when={p.meta} keyed>{m => <span class="muted">{m}</span>}</Show>
      <span class="grow" />
      {p.value}
    </div>
  );
}
