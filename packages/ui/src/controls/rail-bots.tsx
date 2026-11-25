// The bots zone of the navigation (nav-rail.md, Bots): the person's pinned bots, the active ones and the recent ones, at the
// bottom of the rail above the user block, and the same bots first in the phone's More menu. Off by default; the switch and
// the recent count are the person setting railBots, stored on the server (the app saves them).
import { createSignal, For, Show } from 'solid-js';
import { Icon, Ago, focusOnMount, popoverAttr, useAnchoredPopover } from './primitives.tsx';

import { railBotRows, type RailBot, type RailBotState, type RailBotsProps } from './rail-bot-rows.ts';
export { railBotRows, type RailBot, type RailBotState, type RailBotsProps };

const tone: Record<RailBotState, string> = { working: 'running', waiting: 'attention', failed: 'danger', stopped: 'neutral', asleep: '' };
const words: Record<RailBotState, string> = { working: 'running', waiting: 'waiting for an answer', failed: 'failed', stopped: 'stopped', asleep: 'asleep' };

/** The bot's mark: its initial, with a state dot unless asleep (the bot avatar block replaces it once it lands). */
export function BotMark(p: { name: string; state: RailBotState }) {
  return (
    <span class={['bot-mark', tone[p.state] ? `dot t-${tone[p.state]}` : '']} aria-hidden="true">
      {p.name.slice(0, 1).toUpperCase()}
    </span>
  );
}

function BotAge(p: { b: RailBot }) {
  return (
    <span class={['bot-age', tone[p.b.state] ? `t-${tone[p.b.state]}` : '']}>
      <Show when={p.b.state === 'waiting'} fallback={<Show when={p.b.at !== undefined}><Ago t={p.b.at!} /></Show>}>asks</Show>
    </span>
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
    const row = (e.currentTarget as HTMLElement).closest<HTMLElement>('.nav-i')?.querySelector<HTMLElement>('a') ?? (e.currentTarget as HTMLElement);
    setAt({ key, source: row });
  };
  return { at, setAt, open, ref: (el: HTMLElement) => { pop = el; } };
}

/** The zone in the rail: the heading (Bots ›, to all bots), then one row per bot. Collapsed, rows keep their marks (OR-42). */
export function RailBots(p: RailBotsProps) {
  const menu = useMenu();
  const rows = () => railBotRows(p.bots, p.recent);
  const act = (f: () => void) => () => { menu.setAt(null); f(); };
  const ctx = (key: string) => (e: KeyboardEvent) => {
    if (e.key === 'ContextMenu' || (e.shiftKey && e.key === 'F10')) menu.open(key, e);
  };
  return (
    <div class="rail-bots" data-ui="rail-bots">
      <ul class="rail-g" aria-label="Bots">
        <li class="nav-i rail-bots-h" onContextMenu={e => e.preventDefault()} onPointerUp={e => { if (e.button === 2) menu.open('h', e); }}>
          <a href={p.allHref} aria-label="All bots" onClick={e => { e.preventDefault(); p.onAll(); }} onKeyDown={ctx('h')}>
            <span class="nav-ic" data-tip="All bots"><Icon name="bot" /></span>
            <span class="nm">Bots</span>
            <span class="nm bot-go"><Icon name="chevron-right" /></span>
          </a>
        </li>
        <For each={rows()}>
          {b => (
            <li class="nav-i" data-id={b.id} onContextMenu={e => e.preventDefault()} onPointerUp={e => { if (e.button === 2) menu.open(b.id, e); }}>
              <a
                href={b.href}
                aria-label={`${b.name}, ${words[b.state]}${b.pinned ? ', pinned' : ''}`}
                onClick={e => { e.preventDefault(); p.onOpen(b); }}
                onKeyDown={ctx(b.id)}
              >
                <span class="nav-ic" data-tip={b.name} data-tip-narrow><BotMark name={b.name} state={b.state} /></span>
                <span class="nm">{b.name}</span>
                <Show when={b.pinned}><span class="nm bot-pin"><Icon name="pin" /></span></Show>
                <span class="nm"><BotAge b={b} /></span>
              </a>
            </li>
          )}
        </For>
      </ul>
      <span class="menu bots-menu" {...popoverAttr('auto')} role="menu" aria-label="Bots menu" ref={menu.ref}>
        <Show when={menu.at()} keyed>
        {m => {
          const b = rows().find(x => x.id === m.key);
          // The bot left the zone (unpinned, past the count) while its menu was open: close it rather than show another menu.
          if (m.key !== 'h' && !b) queueMicrotask(() => menu.setAt(null));
          return (
            <>
              <Show when={m.key === 'h' ? null : b} fallback={m.key !== 'h' ? undefined : 
                <>
                  <button type="button" role="menuitem" ref={focusOnMount} onClick={act(p.onAll)}><Icon name="bot" />All bots</button>
                  <button type="button" role="menuitem" onClick={act(() => p.onRecent(p.recent + 1))}><Icon name="plus" />More recent bots ({p.recent})</button>
                  <Show when={p.recent > 0}>
                    <button type="button" role="menuitem" onClick={act(() => p.onRecent(p.recent - 1))}><Icon name="minus" />Fewer recent bots</button>
                  </Show>
                  <div class="menu-sep" />
                  <button type="button" role="menuitem" onClick={act(p.onHide)}><Icon name="x" />Hide bots in navigation</button>
                </>
              }>
                {bot => (
                  <>
                    <button type="button" role="menuitem" ref={focusOnMount} onClick={act(() => p.onPin(bot(), !bot().pinned))}>
                      <Icon name={bot().pinned ? 'pin-off' : 'pin'} />{bot().pinned ? 'Unpin' : 'Pin'}
                    </button>
                    <button type="button" role="menuitem" onClick={act(() => p.onLibrary(bot()))}><Icon name="rows-3" />Open in Library</button>
                  </>
                )}
              </Show>
            </>
          );
        }}
        </Show>
      </span>
    </div>
  );
}

/** The same bots first in the phone's More menu: the heading to all bots, then one row of marks (OR-60). */
export function RailBotsStrip(p: RailBotsProps & { onDone?: () => void }) {
  const rows = () => railBotRows(p.bots, p.recent);
  return (
    <div class="bots-strip" data-ui="rail-bots">
      <button type="button" role="menuitem" ref={focusOnMount} onClick={() => { p.onDone?.(); p.onAll(); }}>
        <Icon name="bot" />Bots<span class="grow" /><Icon name="chevron-right" />
      </button>
      <Show when={rows().length > 0}>
        <div class="bots-marks">
          <For each={rows()}>
            {b => (
              <button type="button" role="menuitem" class="bot-tile" aria-label={`${b.name}, ${words[b.state]}`} onClick={() => { p.onDone?.(); p.onOpen(b); }}>
                <BotMark name={b.name} state={b.state} />
                <span class="nm">{b.name}</span>
              </button>
            )}
          </For>
        </div>
      </Show>
      <div class="menu-sep" />
    </div>
  );
}
