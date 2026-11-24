// Target picker: who the Home composer talks to (controls/composer.md#target-picker, OR-50, board masters `Target chip`,
// `Picker row`, `Target picker`). The list floats under the chip as an informational layer (OR-14, OR-16).
import { createMemo, createSignal, createUniqueId, For, Show } from 'solid-js';
import { Popover, PopoverContent, PopoverTrigger } from '@ui-sdk/solid';
import type { Life } from '../registry.ts';
import { isSubmitEnter } from '../keys.ts';
import { Icon, focusOnMount } from './primitives.tsx';
import { Avatar, type AvatarMark } from './avatar.tsx';
import { StateDot } from './state-dot.tsx';

/** A project PM or a bot the person can talk to. */
export type Target = { id: string; kind: 'pm' | 'bot'; name: string; about: string; project?: string; avatar: AvatarMark; state: Life };

const NEW = '__new';

/**
 * The chip (avatar, name, caret) opens the picker: a search field, then project PMs, bots with their state, and
 * "New session with a preset…". Typing filters by name and project; arrow keys move, Enter picks, Escape closes.
 */
export function TargetPicker(p: { targets: Target[]; value: string; onPick: (id: string) => void; onNew: () => void }) {
  const [open, setOpen] = createSignal(false);
  const [q, setQ] = createSignal('');
  const [at, setAt] = createSignal(0);
  const id = createUniqueId();
  let wrap: HTMLSpanElement | undefined;
  const current = createMemo(() => p.targets.find(t => t.id === p.value));
  const shown = createMemo(() => {
    const needle = q().trim().toLowerCase();
    return p.targets.filter(t => !needle || `${t.name} ${t.project ?? ''} ${t.about}`.toLowerCase().includes(needle));
  });
  const groups = createMemo(() => [
    { title: 'Project PMs', items: shown().filter(t => t.kind === 'pm') },
    { title: 'Bots', items: shown().filter(t => t.kind === 'bot') },
  ].filter(g => g.items.length > 0));
  /** Rows in reading order; the last one starts a new session. */
  const order = createMemo(() => [...groups().flatMap(g => g.items.map(t => t.id)), NEW]);
  const active = createMemo(() => order()[Math.min(at(), order().length - 1)]);
  const pick = (key: string) => {
    setOpen(false);
    setQ('');
    setAt(0);
    if (key === NEW) p.onNew(); else p.onPick(key);
  };
  const keys = (e: KeyboardEvent) => {
    if (e.key === 'ArrowDown' || e.key === 'ArrowUp') {
      e.preventDefault();
      const n = order().length;
      setAt(i => (Math.min(i, n - 1) + (e.key === 'ArrowDown' ? 1 : n - 1)) % n);
    } else if (isSubmitEnter(e)) {
      e.preventDefault();
      pick(active());
    }
  };
  const optionId = (key: string) => `${id}-${key}`;
  return (
    <span ref={el => { wrap = el; }} class="target" data-ui="target-picker">
      <Popover open={open()} onOpenChange={setOpen} container={wrap}>
        <PopoverTrigger class="target-chip" aria-label={`Talk to: ${current()?.name ?? 'choose'}`}>
          <Show when={current()} keyed>{c => <Avatar {...c.avatar} size="sm" />}</Show>
          <span class="nm">{current()?.name ?? 'Choose'}</span>
          <Icon name="chevron-down" />
        </PopoverTrigger>
        <PopoverContent class="target-panel" side="bottom" align="start" aria-label="Talk to" render={cp => <div {...cp} role="group" aria-labelledby={undefined} />}>
          <input
            ref={focusOnMount}
            class="target-search"
            role="combobox"
            aria-expanded="true"
            aria-controls={`${id}-list`}
            aria-activedescendant={optionId(active())}
            aria-label="Search people, bots, projects"
            placeholder="Search people, bots, projects…"
            value={q()}
            onInput={e => { setQ(e.currentTarget.value); setAt(0); }}
            onKeyDown={keys}
          />
          <div id={`${id}-list`} role="listbox" aria-label="Targets">
            <For each={groups()}>
              {g => (
                <div role="group" aria-label={g.title}>
                  <div class="target-group" aria-hidden="true">{g.title}</div>
                  <For each={g.items}>
                    {t => (
                      <div
                        id={optionId(t.id)}
                        role="option"
                        aria-selected={active() === t.id ? 'true' : 'false'}
                        class={['pick-row', { on: active() === t.id }]}
                        data-kind={t.kind}
                        onPointerMove={() => setAt(order().indexOf(t.id))}
                        onClick={() => pick(t.id)}
                      >
                        <Avatar {...t.avatar} />
                        <span class="pick-text"><b>{t.name}</b><span>{t.about}</span></span>
                        <StateDot state={t.state} />
                      </div>
                    )}
                  </For>
                </div>
              )}
            </For>
            <div
              id={optionId(NEW)}
              role="option"
              aria-selected={active() === NEW ? 'true' : 'false'}
              class={['pick-row', 'new', { on: active() === NEW }]}
              onPointerMove={() => setAt(order().length - 1)}
              onClick={() => pick(NEW)}
            >
              <Icon name="plus" /><span class="pick-text"><span>New session with a preset…</span></span>
            </div>
          </div>
        </PopoverContent>
      </Popover>
    </span>
  );
}
