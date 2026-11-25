// The bots zone's data and its order (nav-rail.md, Bots); plain TypeScript so tests run it without a JSX transform.

/** A bot's state in the one vocabulary (OR-55): running, waiting for an answer, failed, stopped (paused) or asleep. */
export type RailBotState = 'working' | 'waiting' | 'failed' | 'stopped' | 'asleep';
/** One bot of the zone. at: the start of its active run, or of its last run (ms); pinned: pinned in Library. */
export type RailBot = { id: string; name: string; state: RailBotState; at?: number; pinned: boolean; href: string };
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
