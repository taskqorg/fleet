// The Pinned zone's data and its order (nav-rail.md, Pinned); plain TypeScript so tests run it without a JSX transform.
import type { AvatarMark, AvatarState } from './avatar.tsx';

/**
 * One pinned object in the zone. key: its typed id, `session:<id>` or `bot:<id>` (later `task:`, `project:`); mark and
 * state: its avatar (a bot's picture or letter, a conversation's harness); word: its state in words for the label and
 * tooltip; asks: it waits for the person's answer; at: its last activity (ms).
 */
export type RailPin = { key: string; name: string; href: string; mark: AvatarMark; state?: AvatarState; word: string; asks?: boolean; at?: number };
export type RailPinnedProps = {
  pins: RailPin[];
  onOpen: (p: RailPin) => void;
  onUnpin: (p: RailPin) => void;
  /** The whole new order of the zone's keys (drag to reorder). */ onOrder: (keys: string[]) => void;
  onHide: () => void;
};

/**
 * The zone's keys in order: the stored order first (keys no longer pinned drop out), then pinned keys the order does not
 * name yet, in the order given (an object pinned from a list or by an agent goes to the end).
 */
export function railPinOrder(order: readonly string[], keys: readonly string[]): string[] {
  const pinned = new Set(keys);
  const head = [...new Set(order)].filter(k => pinned.has(k));
  const seen = new Set(head);
  return [...head, ...keys.filter(k => !seen.has(k) && (seen.add(k), true))];
}
