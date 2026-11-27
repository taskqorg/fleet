import { Show } from 'solid-js';
import type { JSX } from '@solidjs/web';
import { AppShell, Button, Empty } from '../src/index.ts';
import { NAV } from './nav.ts';
import { FocusDemo } from './focus-demo.tsx';
import { CardsProvider } from './entity-cards.tsx';
import { NOW } from './fixtures.ts';
import { useHashLocation } from './scene-parts.tsx';

const BOOK_VERSION = { current: '1.261115.6', tone: 'attention' as const, summary: '1 part needs a look', updateReady: true, newVersion: '1.261115.7', updateIn: 24, checkedAt: NOW - 12_000, onUpdate: () => {}, rows: [
  { key: 'tab', part: 'This tab', icon: 'panels-top-left' as const, version: '1.261115.5', state: 'older than the site', tone: 'running' as const },
  { key: 'ui', part: 'Site app', icon: 'house' as const, version: '1.261115.6', state: 'bundled', tone: 'neutral' as const },
  { key: 'coordinator', part: 'Coordinator', icon: 'network' as const, version: '1.261115.6', state: 'running', tone: 'success' as const, times: [{ label: 'started', at: NOW - 2 * 3600_000 }] },
  { key: 'gateway', part: 'Gateway', icon: 'shield' as const, version: '1.261115.6', state: 'ok', tone: 'success' as const, times: [{ label: 'checked', at: NOW - 40_000 }] },
  { key: 'host:program', part: 'program', icon: 'terminal' as const, version: null, state: 'offline', tone: 'neutral' as const, times: [{ label: 'last seen', at: NOW - 6 * 86400_000 }], open: () => {} },
  { key: 'host:mac', part: 'mac', icon: 'terminal' as const, version: '1.261115.6', state: 'current', tone: 'success' as const },
  { key: 'host:windows', part: 'windows', icon: 'terminal' as const, version: '1.261114.3', state: 'behind', tone: 'attention' as const, note: 'auto-update off', open: () => {} },
] };

export type SceneProps = { path: string; detail?: string };
export type SceneEntry = {
  route: string;
  states: string[];
  details?: boolean;
  view: (props: SceneProps) => JSX.Element;
};

export function SceneShell(props: { entries: SceneEntry[]; initial: string }) {
  const [loc, go] = useHashLocation();
  const path = () => loc() === '/' ? props.initial : loc();
  const section = () => path().split('/')[1];
  const route = () => props.entries.find(entry => entry.states.includes(path()))
    ?? props.entries.find(entry => entry.details && detail() && entry.route === '/' + section());
  const detail = () => /^\/[^/]+\/([^/]+)$/.exec(path())?.[1];
  return <CardsProvider><AppShell nav={NAV} current={section()} onGo={id => go('/' + id)} waiting={3}
    focus={<FocusDemo />} user={{ name: 'Alex Kirs', onLogout: () => go('/overview'), version: BOOK_VERSION }}>
    <Show when={route()} keyed fallback={<Empty text="This section has no preview." action={<Button label="Overview" icon="house" onClick={() => go(props.initial)} />} />}>
      {entry => <Show when={detail()} fallback={<entry.view path={path()} />}>
        {id => <entry.view path={path()} detail={id()} />}
      </Show>}
    </Show>
  </AppShell></CardsProvider>;
}
