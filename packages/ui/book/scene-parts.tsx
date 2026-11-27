import { createSignal, For, onSettled, Show } from 'solid-js';
import { type JSX } from '@solidjs/web';
import { type IconName, KvRow } from '../src/index.ts';

const [hashLoc, setHashLoc] = createSignal(typeof location !== 'undefined' ? (location.hash.replace(/^#/, '') || '/') : '/');
export function useHashLocation() {
  onSettled(() => {
    const sync = () => setHashLoc(location.hash.replace(/^#/, '') || '/');
    addEventListener('hashchange', sync);
    return () => removeEventListener('hashchange', sync);
  });
  const go = (to: string) => {
    const path = to.startsWith('/') ? to : '/' + to;
    location.hash = path;
    setHashLoc(path);
  };
  return [hashLoc, go] as const;
}
/** A page's own tools under the shell header, which holds the section title (OR-43); none: no head. */
export function PageHead(props: { title: string; children?: JSX.Element }) {
  return <Show when={props.children} keyed>{ch => <header class="page-h" aria-label={props.title}><span class="grow" />{ch}</header>}</Show>;
}

export const Panel = (props: { title?: string; actions?: JSX.Element; children: JSX.Element; class?: string }) => (
  <section class={`panel ${props.class ?? ''}`}>
    <Show when={props.title} keyed>{t => <header class="panel-h"><h2>{t}</h2><span class="grow" />{props.actions}</header>}</Show>
    {props.children}
  </section>
);
export const m = (value: number | null, basis: 'measured' | 'estimated' | 'unavailable' = 'measured', reason?: string) => ({ value, basis, reason });
export const kvs = (rows: readonly [IconName, string, JSX.Element?, JSX.Element?][]) =>
  <For each={rows}>{([icon, label, meta, value]) => <KvRow icon={icon} label={label} meta={meta} value={value} />}</For>;
// A catalogue row built by hand: state, name with its second-line facts, one fact on the right.
export function CatRow(props: { id: string; sel: boolean; onOpen: () => void; state: JSX.Element; children?: JSX.Element; right?: JSX.Element }) {
  return <div role="row" class={`vl-row static${props.sel ? ' sel' : ''}`}>
    <span class="lead">{props.state}</span>
    <span class="tt"><a class="title row-link" href="#" onClick={e => { e.preventDefault(); props.onOpen(); }}>{props.id}</a>{props.children}</span>
    <span class="muted mono">{props.right}</span>
  </div>;
}
export const CAT_COLS = { '--cols': '1.25rem minmax(0,1fr) auto' };
