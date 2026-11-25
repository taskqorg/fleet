// The bots zone's data and its order (nav-rail.md, Bots); plain TypeScript so tests run it without a JSX transform.

/** A bot's activity as its avatar shows it (AvatarState in avatar.tsx): asleep, working, waiting for an answer, failed, paused. */
export type RailBotState = 'asleep' | 'working' | 'waiting' | 'failed' | 'paused';
/** A bot's picture for the Avatar block (AvatarPicture in avatar.tsx). */
export type RailBotPicture = { src: string; still?: string; video?: boolean; glow?: string };
/** One bot of the zone. at: the start of its last run (ms); pinned: pinned in Library; picture: its avatar, else its letter. */
export type RailBot = { id: string; name: string; state: RailBotState; at?: number; pinned: boolean; href: string; picture?: RailBotPicture };
export type RailBotsProps = {
  bots: RailBot[];
  /** How many bots that are neither pinned nor active the zone also shows. */ recent: number;
  allHref: string;
  onOpen: (b: RailBot) => void; onAll: () => void; onLibrary: (b: RailBot) => void;
  onPin: (b: RailBot, pinned: boolean) => void; onRecent: (n: number) => void; onHide: () => void;
};

/** The zone's rows in order: pinned (in pin order, as given), then active, then up to `recent` others by last run. */
export function railBotRows(bots: RailBot[], recent: number): RailBot[] {
  const active = (b: RailBot) => b.state === 'working' || b.state === 'waiting';
  const at = (b: RailBot) => b.at ?? -Infinity;
  const newest = (a: RailBot, b: RailBot) => (at(b) - at(a)) || 0; // two never-run bots tie (-Infinity - -Infinity is NaN)
  const rest = bots.filter(b => !b.pinned);
  return [
    ...bots.filter(b => b.pinned),
    ...rest.filter(active).sort(newest),
    ...rest.filter(b => !active(b)).sort(newest).slice(0, Math.max(0, recent)),
  ];
}
