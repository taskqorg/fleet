// The navigation's bots zone (docs/design-system/controls/nav-rail.md, Bots): the person's switch and recent count are the
// person setting railBots; the pins are the Library pins (rowPins.library, bot:<id>); the bots come from bot_list.
import { createMemo } from 'solid-js';
import type { RailBot, RailBotsProps } from '../../packages/ui/src/index.ts';
import { timeOf } from '../../packages/ui/src/format.ts';
import type { BotListItem } from '../../contracts/generated/ops-bots.ts';
import { prefs, savePrefs, scopeIds, prefsRead } from './prefs.ts';
import { useOp } from './store.ts';
import { botAvatarState, botPicture } from './bot-view.ts';

const LIBRARY_PINS = 'library';
const libraryPath = (b: BotListItem) => '/library/bots/' + encodeURIComponent(b.code || b.id);
const ms = (t?: string | null) => timeOf(t, undefined);

/** The shell's bots prop: read only while the zone is on and access is open. */
export function useRailBots(open: () => boolean, go: (to: string) => void) {
  const on = () => prefs.value.railBots?.on ?? false;
  const list = useOp('bot_list', () => (open() && on() && prefsRead.value ? scopeIds(false) : null));
  const pins = () => prefs.value.rowPins[LIBRARY_PINS] ?? [];
  const byId = createMemo(() => new Map((list.data?.bots ?? []).map(b => [b.id, b])));
  const bots = createMemo<RailBot[]>(() => {
    const all = list.data?.bots ?? [];
    const pinned = pins().filter(id => id.startsWith('bot:')).map(id => byId().get(id.slice(4))).filter((b): b is BotListItem => !!b);
    const pinnedIds = new Set(pinned.map(b => b.id));
    const row = (b: BotListItem, isPinned: boolean): RailBot => ({ id: b.id, name: b.name, state: botAvatarState(b), picture: botPicture(b), at: ms(b.lastRunAt), pinned: isPinned, href: b.homeSessionId ? '/conversations/' + encodeURIComponent(b.homeSessionId) : libraryPath(b) });
    return [...pinned.map(b => row(b, true)), ...all.filter(b => !pinnedIds.has(b.id)).map(b => row(b, false))];
  });
  const setRail = (next: { on: boolean; recent: number }) => savePrefs(() => ({ railBots: next }), { railBots: next });
  const zone: RailBotsProps = {
    get bots() { return bots(); },
    get recent() { return prefs.value.railBots?.recent ?? 0; },
    allHref: '/library/bots',
    onOpen: b => go(b.href),
    onAll: () => go('/library/bots'),
    onLibrary: b => { const x = byId().get(b.id); go(x ? libraryPath(x) : '/library/bots'); },
    onPin: (b, pinned) => {
      const id = 'bot:' + b.id;
      savePrefs(s => { const xs = (s.rowPins[LIBRARY_PINS] ?? []).filter(x => x !== id); return { rowPins: { ...s.rowPins, [LIBRARY_PINS]: pinned ? [...xs, id] : xs } }; }, { rowPin: { list: LIBRARY_PINS, id, pinned } });
    },
    onRecent: n => setRail({ on: true, recent: Math.max(0, n) }),
    onHide: () => setRail({ on: false, recent: prefs.value.railBots?.recent ?? 0 }),
  };
  return {
    get on() { return on(); },
    set: (v: boolean) => setRail({ on: v, recent: prefs.value.railBots?.recent ?? 0 }),
    zone,
  };
}
