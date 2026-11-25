// The Pinned zone of the navigation (nav-rail.md, Pinned): the objects the person pinned (conversations of any kind and
// bots), at the bottom of the rail above the user block, and the same objects first in the phone's More menu. Off by
// default; the switch is the person setting railPinned, the pins and their order are person settings too (the app saves them).
import { createSignal, For, Show } from 'solid-js';
import { Icon, Ago, focusOnMount, popoverAttr, useAnchoredPopover } from './primitives.tsx';
import { Avatar } from './avatar.tsx';

import { railPinOrder, railPinMove, type RailPin, type RailPinnedProps } from './rail-pinned-rows.ts';
export { railPinOrder, railPinMove, type RailPin, type RailPinnedProps };

function PinAge(p: { p: RailPin }) {
  return (
    <Show when={!p.p.asks} fallback={<span class="pin-age t-attention">asks</span>}>
      <Show when={p.p.at !== undefined}><span class={['pin-age', { 't-running': p.p.state === 'working' }]}><Ago t={p.p.at!} /></span></Show>
    </Show>
  );
}

/**
 * The zone's context menu: one native auto popover tethered to the row it opened from (modern-web-guidance: context-menu,
 * viewport-aware-dropdowns), so the scrolling rail never clips it and it flips up at the bottom of the window; light dismiss
 * and Escape close it.
 */
function useMenu() {
  const [at, setAt] = createSignal<{ key: string; source: HTMLElement } | null>(null);
  let pop: HTMLElement | undefined;
  useAnchoredPopover(() => !!at(), () => setAt(null), () => at()?.source, () => pop);
  // Opened on pointerup, not contextmenu: the right button's release would otherwise light-dismiss the popover it opened.
  const open = (key: string, e: Event) => {
    e.preventDefault();
    const row = (e.currentTarget as HTMLElement).closest<HTMLElement>('.nav-i')?.querySelector<HTMLElement>('a, .pin-h') ?? (e.currentTarget as HTMLElement);
    setAt({ key, source: row });
  };
  return { at, setAt, open, ref: (el: HTMLElement) => { pop = el; } };
}

const label = (p: RailPin) => `${p.name}, ${p.asks ? 'waits for your answer' : p.word}`;

/** The zone in the rail: the heading (Pinned), then one row per pinned object. Collapsed, rows keep their marks (OR-42). */
export function RailPinned(p: RailPinnedProps) {
  const menu = useMenu();
  const keys = () => p.pins.map(x => x.key);
  const act = (f: () => void) => () => { menu.setAt(null); f(); };
  const ctx = (key: string) => (e: KeyboardEvent) => {
    if (e.key === 'ContextMenu' || (e.shiftKey && e.key === 'F10')) menu.open(key, e);
  };
  return (
    <div class="rail-pinned" data-ui="rail-pinned">
      <ul class="rail-g" aria-label="Pinned">
        <li class="nav-i rail-pinned-h" onContextMenu={e => e.preventDefault()} onPointerUp={e => { if (e.button === 2) menu.open('h', e); }}>
          <span class="pin-h" role="button" tabindex={0} aria-haspopup="menu" aria-label="Pinned menu" onKeyDown={e => { if (e.key === 'Enter' || e.key === ' ') menu.open('h', e); else ctx('h')(e); }} onClick={e => menu.open('h', e)}>
            <span class="nav-ic" data-tip="Pinned"><Icon name="pin" /></span>
            <span class="nm">Pinned</span>
          </span>
        </li>
        <For each={p.pins} fallback={<li class="nav-i rail-pinned-empty"><span class="nm">Pin a conversation or a bot: the pin in its head, or Pin in a row menu.</span></li>}>
          {x => (
            <li class="nav-i" data-key={x.key} onContextMenu={e => e.preventDefault()} onPointerUp={e => { if (e.button === 2) menu.open(x.key, e); }}>
              <a href={x.href} aria-label={label(x)} onClick={e => { e.preventDefault(); p.onOpen(x); }} onKeyDown={ctx(x.key)}>
                <span class="nav-ic" data-tip={label(x)} data-tip-narrow><Avatar {...x.mark} size="sm" state={x.state} /></span>
                <span class="nm">{x.name}</span>
                <span class="nm"><PinAge p={x} /></span>
              </a>
            </li>
          )}
        </For>
      </ul>
      <span class="menu pinned-menu" {...popoverAttr('auto')} role="menu" aria-label="Pinned menu" ref={menu.ref}>
        <Show when={menu.at()} keyed>
        {m => {
          const x = p.pins.find(y => y.key === m.key);
          // The object left the zone (unpinned elsewhere) while its menu was open: close it rather than show another menu.
          if (m.key !== 'h' && !x) queueMicrotask(() => menu.setAt(null));
          const i = () => keys().indexOf(m.key);
          return (
            <Show when={m.key === 'h' ? null : x} fallback={m.key !== 'h' ? undefined :
              <button type="button" role="menuitem" ref={focusOnMount} onClick={act(p.onHide)}><Icon name="pin-off" />Hide pinned in navigation</button>
            }>
              {pin => (
                <>
                  <button type="button" role="menuitem" ref={focusOnMount} onClick={act(() => p.onUnpin(pin()))}><Icon name="pin-off" />Unpin</button>
                  <button type="button" role="menuitem" onClick={act(() => p.onOpen(pin()))}><Icon name="external-link" />Open</button>
                  <Show when={i() > 0}>
                    <button type="button" role="menuitem" onClick={act(() => p.onOrder(railPinMove(keys(), pin().key, -1)))}><Icon name="arrow-up" />Move up</button>
                  </Show>
                  <Show when={i() >= 0 && i() < keys().length - 1}>
                    <button type="button" role="menuitem" onClick={act(() => p.onOrder(railPinMove(keys(), pin().key, 1)))}><Icon name="arrow-down" />Move down</button>
                  </Show>
                </>
              )}
            </Show>
          );
        }}
        </Show>
      </span>
    </div>
  );
}

/** The same objects first in the phone's More menu: the heading, then one row of marks (OR-60). */
export function RailPinnedStrip(p: RailPinnedProps & { onDone?: () => void }) {
  return (
    <div class="pinned-strip" data-ui="rail-pinned">
      <div class="pinned-strip-h"><Icon name="pin" />Pinned</div>
      <Show when={p.pins.length > 0} fallback={<p class="pinned-strip-empty">Pin a conversation or a bot: the pin in its head, or Pin in a row menu.</p>}>
        <div class="pinned-marks">
          <For each={p.pins}>
            {x => (
              <button type="button" role="menuitem" class="pin-tile" aria-label={label(x)} onClick={() => { p.onDone?.(); p.onOpen(x); }}>
                <Avatar {...x.mark} size="md" state={x.state} />
                <span class="nm">{x.name}</span>
              </button>
            )}
          </For>
        </div>
      </Show>
      <div class="menu-sep" />
    </div>
  );
}
