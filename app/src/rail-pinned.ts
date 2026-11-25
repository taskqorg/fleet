// The navigation's Pinned zone (docs/design-system/controls/nav-rail.md, Pinned). One pin per object: a conversation's pin
// is its Conversations pin (person setting rowPins.conversations, session ids), a bot's pin is its Library pin
// (rowPins.library, bot:<id>). The zone shows both, in the person's order (rowPins.nav, typed keys session:<id> and
// bot:<id>; keys it does not name yet go last). The switch is the person setting railPinned. Agents read and set all of it
// with person_settings_get and person_settings_set (rowPin, rowPins, railPinned).
import { createMemo } from 'solid-js';
import { life, railPinOrder, states, type AvatarState, type Harness, type RailPin, type RailPinnedProps, harnesses } from '../../packages/ui/src/index.ts';
import { timeOf } from '../../packages/ui/src/format.ts';
import type { BotListItem } from '../../contracts/generated/ops-bots.ts';
import type { SessionRow } from '../../contracts/generated/ops.ts';
import { DEFAULT_SESSION_SORT } from '../../packages/view-url/view-url.ts';
import { prefs, savePrefs, scopeIds, prefsRead } from './prefs.ts';
import { useOp } from './store.ts';
import { botAvatarState, botPicture } from './bot-view.ts';

/** The person-setting lists the zone reads: the two pin lists and its own order. */
export const PIN_LISTS = { conversations: 'conversations', bots: 'library', order: 'nav' } as const;
const SESSION = 'session:', BOT = 'bot:';
const ms = (t?: string | null) => timeOf(t, undefined);
const botWords: Record<AvatarState, string> = { working: 'working', waiting: 'waiting for an answer', failed: 'failed', paused: 'paused', asleep: 'asleep' };

/** A conversation's avatar state: working, waiting and failed show; idle and finished conversations show plain. */
function sessionState(s: SessionRow): AvatarState | undefined {
  const l = s.openQuestions > 0 ? 'waiting' : life('session', s.state);
  return l === 'working' || l === 'waiting' || l === 'failed' ? l : undefined;
}

/** The shell's Pinned-zone prop: read only while the zone is on and access is open. */
export function useRailPinned(open: () => boolean, go: (to: string) => void) {
  const on = () => prefs.value.railPinned ?? false;
  const list = (name: string) => prefs.value.rowPins[name] ?? [];
  const sessionIds = createMemo(() => list(PIN_LISTS.conversations));
  const botIds = createMemo(() => list(PIN_LISTS.bots).filter(id => id.startsWith(BOT)).map(id => id.slice(BOT.length)));
  const live = () => open() && on() && prefsRead.value;
  // Bots: their names, pictures and states; a bot's Home conversation pinned shows as the bot.
  const bots = useOp('bot_list', () => (live() && (botIds().length || sessionIds().length) ? scopeIds(false) : null));
  // Conversations: read by id, as the Conversations list reads its pinned rows (list_membership). An archived conversation
  // keeps its pin but leaves the zone until it is unarchived.
  const sessions = useOp('list_membership', () => (live() && sessionIds().length
    ? { ids: sessionIds().slice(0, 1000), lists: [{ name: 'sessions', filter: {}, group: 'none', sort: DEFAULT_SESSION_SORT }] }
    : null));
  const botById = createMemo(() => new Map((bots.data?.bots ?? []).map(b => [b.id, b])));
  const botByHome = createMemo(() => new Map((bots.data?.bots ?? []).filter(b => b.homeSessionId).map(b => [b.homeSessionId!, b])));
  const sessionById = createMemo(() => new Map(((sessions.data?.lists[0]?.rows ?? []) as SessionRow[]).map(s => [s.id, s])));

  const botPin = (key: string, b: BotListItem): RailPin => {
    const st = botAvatarState(b);
    return {
      key, name: b.name, word: botWords[st], state: st, asks: st === 'waiting', at: ms(b.lastRunAt),
      mark: { letter: b.name, tone: 'success', picture: botPicture(b) },
      href: b.homeSessionId ? '/conversations/' + encodeURIComponent(b.homeSessionId) : '/library/bots/' + encodeURIComponent(b.code || b.id),
    };
  };
  const pins = createMemo<RailPin[]>(() => {
    const keys = railPinOrder(list(PIN_LISTS.order), [...botIds().map(id => BOT + id), ...sessionIds().map(id => SESSION + id)]);
    return keys.flatMap((key): RailPin[] => {
      if (key.startsWith(BOT)) {
        const b = botById().get(key.slice(BOT.length));
        return b ? [botPin(key, b)] : [];
      }
      const id = key.slice(SESSION.length), s = sessionById().get(id);
      if (!s) return [];
      const home = botByHome().get(id);
      if (home) return [{ ...botPin(key, home), href: '/conversations/' + encodeURIComponent(id) }];
      const st = sessionState(s);
      const h = s.harness && s.harness in harnesses ? (s.harness as Harness) : undefined;
      return [{
        key, name: s.title ?? 'New conversation', href: '/conversations/' + encodeURIComponent(id), state: st, asks: s.openQuestions > 0,
        word: (states.session as Record<string, { label: string }>)[s.state]?.label.toLowerCase() ?? s.state, at: ms(s.activityAt),
        mark: h ? { harness: h, tone: 'neutral' } : { letter: s.title ?? 'C', tone: 'neutral' },
      }];
    });
  });

  const unpin = (key: string) => {
    const [list_, id] = key.startsWith(BOT) ? [PIN_LISTS.bots, key] : [PIN_LISTS.conversations, key.slice(SESSION.length)];
    savePrefs(s => ({ rowPins: { ...s.rowPins, [list_]: (s.rowPins[list_] ?? []).filter(x => x !== id) } }), { rowPin: { list: list_, id, pinned: false } });
  };
  const setOn = (v: boolean) => savePrefs(() => ({ railPinned: v }), { railPinned: v });
  const zone: RailPinnedProps = {
    get pins() { return pins(); },
    onOpen: p => go(p.href),
    onUnpin: p => unpin(p.key),
    onOrder: keys => savePrefs(s => ({ rowPins: { ...s.rowPins, [PIN_LISTS.order]: keys } }), { rowPins: { [PIN_LISTS.order]: keys } }),
    onHide: () => setOn(false),
  };
  return {
    get on() { return on(); },
    set: setOn,
    zone,
  };
}
