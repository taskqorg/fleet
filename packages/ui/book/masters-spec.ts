// Types and helpers of the board masters page (masters.tsx).
import type { JSX } from '@solidjs/web';
import { states } from '../src/index.ts';

const LIFE_BY_ICON = Object.fromEntries(Object.entries(states.life).map(([k, d]) => [d.icon, k]));
/** A badge of another family (a setting's check, a run's preparing) shows the life state of its tone. */
const TONE_STATE: Record<string, string> = { success: 'done', danger: 'failed', attention: 'waiting', running: 'working' };
const iconOf = (el: Element) => /lucide-([a-z0-9-]+)/.exec(el.querySelector('svg')?.getAttribute('class') ?? '')?.[1] ?? '';

export type MasterSpec = {
  /** The master's name on board/masters.bl, the name screens use in inst(...). */
  name: string;
  /** Component properties: each axis is a prop of the control, with its values. */
  axes?: Record<string, readonly string[]>;
  /** Width of the box the control is drawn in (block controls fill it); without it the control hugs. */
  w?: number;
  /** CSS selector of the master inside what render draws; without it, the first element. */
  pick?: string;
  /** Names of the texts not marked by slot(), in document order. */
  texts?: readonly string[];
  render: (v: Record<string, string>) => JSX.Element;
};

/** A text the board names `name`; screens override it by that name. */
export const slot = (name: string, text: string) => `${name}${text}`;

/** Atoms drawn inside other masters, by their root's data-ui: the master and the variant the element shows. Read by
 *  scripts/board-extract.mjs in the page; null draws the element in place (a badge with its word). */
export const NEST: Record<string, { master: string; at: (el: Element) => Record<string, string> | null }> = {
  // An icon-only button and an avatar drawn as a harness mark or a picture are drawn in place: the masters show a label
  // and a letter.
  'button': { master: 'Button', at: el => el.classList.contains('icon-only') || el.querySelector('svg') ? null : { kind: ['primary', 'quiet', 'attention', 'danger'].find(k => el.classList.contains(k)) ?? 'default' } },
  'avatar': { master: 'Avatar', at: el => el.classList.contains('mark') || el.classList.contains('pic') ? null : { size: ['sm', 'lg', 'xl'].find(k => el.classList.contains(k)) ?? 'md' } },
  'state-dot': { master: 'State', at: el => ({ state: el.getAttribute('data-state') ?? 'working' }) },
  'state-badge': { master: 'State icon', at: el => {
    const b = el.querySelector('.badge.compact'); if (!b) return null;
    const tone = /\bt-([a-z]+)/.exec(b.getAttribute('class') ?? '')?.[1] ?? '';
    return { state: LIFE_BY_ICON[iconOf(el)] ?? TONE_STATE[tone] ?? 'queued' };
  } },
  'state-counters': { master: 'State counters', at: () => ({}) },
  'project-chip': { master: 'Project chip', at: () => ({}) },
  'blocker-chip': { master: 'Blocker chip', at: () => ({}) },
  'status-pill': { master: 'Status pill', at: el => ({ state: el.getAttribute('data-state') ?? 'working' }) },
  'sparkline': { master: 'Sparkline', at: el => ({ tone: el.classList.contains('alert') || el.querySelector('.alert') ? 'alert' : 'normal' }) },
  'gauge': { master: 'Gauge', at: el => ({ level: el.classList.contains('crit') ? 'danger' : el.classList.contains('warn') ? 'warn' : 'normal' }) },
  'question-card': { master: 'Question card', at: el => ({ state: el.classList.contains('answered') ? 'answered' : el.classList.contains('danger') ? 'failed' : 'open' }) },
  'plan-item': { master: 'Plan item', at: el => ({ state: el.classList.contains('done') ? 'done' : 'todo' }) },
  'timer-row': { master: 'Timer row', at: () => ({}) },
  'swarm-member': { master: 'Swarm member', at: () => ({}) },
  'target-picker': { master: 'Target chip', at: () => ({}) },
};
