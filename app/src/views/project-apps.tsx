// Project apps in the app (docs/designs/fleet-project-apps.md, Screens; board drafts/project-apps.bl PA1 and PA4, approved
// 2026-11-26): the project's own applications under Apps on its Setup facet, after Access. It reads project_apps, the
// operation agents call too, which the change stream rereads (kind project-app; an app's use moves its last-use time,
// so its row updates as it runs). A row opens its detail under it; Rotate key and Revoke (project_app_rotate,
// project_app_revoke) are the owner's and full-access people's, each confirming inline. The approval is the Needs-you
// question card the server writes (PA2). Key values never reach the browser.
import { createMemo, createSignal, For, Show } from 'solid-js';
import type { JSX } from '@solidjs/web';
import {
  AgeChip, Button, ErrorBanner, ReflowRow, ReflowTable, Skeleton, StateBadge, actor, day,
} from '../../../packages/ui/src/index.ts';
import { op } from '../api.ts';
import { appRowText, limitsText, pipelinesText, type ProjectApp } from '../project-apps-model.ts';
import { useLocation, useSearch } from '../router.ts';
import { useOp } from '../store.ts';

type App = ProjectApp;

const cap = (s: string) => s.charAt(0).toUpperCase() + s.slice(1);

/** The app's live key: the newest not retired (a rotation keeps the older one until the new key's first use). */
const liveKey = (a: App) => a.keys.filter(k => k.retiredAt == null).at(-1);

function KeyLine(props: { a: App }) {
  return <Show when={liveKey(props.a)} fallback={<span class="muted">{props.a.keys.length ? 'every key retired' : 'none yet'}</span>}>{k => <span>
    created {day(k().createdAt)} · {k().takenAt != null ? `taken ${day(k().takenAt!)}` : 'not taken yet'}
    <Show when={k().lastUsedAt}>{t => <> · last use <AgeChip t={t()} /></>}</Show>
  </span>}</Show>;
}

/** Who approved it and when (its first key is minted on approval), with the reason it was asked for. */
function Approval(props: { a: App }) {
  const reason = () => (props.a.reason ? ` · “${props.a.reason}”` : '');
  return <Show when={props.a.approvedBy} fallback={<span>asked by {actor(props.a.askedBy) || 'an agent'}{props.a.state === 'asked' ? ', waits in Needs you' : ''}{reason()}</span>}>
    {by => <span>by {actor(by())}{props.a.keys[0] ? `, ${day(props.a.keys[0].createdAt)}` : ''}{reason()}</span>}
  </Show>;
}

/** One app opened: pipelines, callbacks, limits, key, approval; Rotate key and Revoke for the owner, confirmed inline. */
function AppDetail(props: { a: App; manage: boolean; onChanged: () => void }) {
  const [confirm, setConfirm] = createSignal<'' | 'rotate' | 'revoke'>('');
  const [busy, setBusy] = createSignal(false);
  const [err, setErr] = createSignal('');
  const a = () => props.a;
  const canRotate = () => props.manage && a().state === 'active';
  const canRevoke = () => props.manage && (a().state === 'active' || a().state === 'asked');
  const act = (name: 'project_app_rotate' | 'project_app_revoke') => {
    setErr(''); setBusy(true); setConfirm('');
    void op(name, { appId: a().id }).then(() => { setBusy(false); props.onChanged(); }, e => { setBusy(false); setErr(String(e.message ?? e)); });
  };
  const line = (k: string, v: JSX.Element) => <div class="pw-line"><span class="pw-k">{k}</span><span class="pw-line-m">{v}</span></div>;
  return <div class="stack access-detail">
    <div class="pw-list">
      {line('Pipelines', <span class="mono">{pipelinesText(a())}</span>)}
      {line('Callbacks to', a().callbackOrigins.length ? <span class="mono">{a().callbackOrigins.join(', ')}</span> : <span class="muted">none</span>)}
      {line('Limits', <span>{cap(limitsText(a().limits))}</span>)}
      {line('Key', <KeyLine a={a()} />)}
      {line(a().approvedBy ? 'Approved' : 'Asked', <Approval a={a()} />)}
    </div>
    <Show when={err()}>{e => <ErrorBanner text={e()} />}</Show>
    <Show when={confirm()} fallback={<Show when={canRotate() || canRevoke()}><div class="res-bar">
      <Show when={canRotate()}><Button label="Rotate key" icon="rotate-cw" disabledReason={busy() ? 'Working' : undefined} onClick={() => setConfirm('rotate')} /></Show>
      <Show when={canRevoke()}><Button label={a().state === 'asked' ? 'Withdraw' : 'Revoke'} kind="danger" disabledReason={busy() ? 'Working' : undefined} onClick={() => setConfirm('revoke')} /></Show>
    </div></Show>}>
      <div class="res-bar" role="group" aria-label="Confirm">
        <span>{confirm() === 'rotate'
          ? `Mint a new key for ${a().name}? The current key keeps working until the new key's first use; the asker takes the new one with project_app_key_take.`
          : a().state === 'asked'
            ? `Withdraw the ask for ${a().name} and its approval card? The project can ask again.`
            : `Revoke ${a().name}? Every key stops at once and the app can no longer run ${props.a.pipelines.includes('*') ? 'any pipeline' : 'its pipelines'}; the project can ask again with project_app_ask.`}</span>
        <Button label={confirm() === 'rotate' ? 'Rotate key' : a().state === 'asked' ? 'Withdraw' : 'Revoke'} kind={confirm() === 'rotate' ? 'primary' : 'danger'}
          onClick={() => act(confirm() === 'rotate' ? 'project_app_rotate' : 'project_app_revoke')} />
        <Button label="Cancel" kind="quiet" onClick={() => setConfirm('')} />
      </div>
    </Show>
  </div>;
}

/** Setup › Apps: the project's apps, one row each; a row opens its detail under it (`?app=<id>`). */
export function ProjectApps(props: { projectId: string }) {
  const [, go] = useLocation();
  const search = useSearch();
  const read = useOp('project_apps', () => ({ projectId: props.projectId }));
  const me = useOp('me', {});
  const manage = () => !!(me.data?.owner || me.data?.managesPeople);
  // Rows are keyed by id: a reread keeps an open detail (a confirmation in flight).
  const byId = createMemo(() => new Map((read.data?.apps ?? []).map(a => [a.id, a])));
  const ids = createMemo(() => (read.data?.apps ?? []).toSorted((a, b) => b.createdAt - a.createdAt).map(a => a.id));
  const open = () => new URLSearchParams(search()).get('app') ?? '';
  const toggle = (id: string) => {
    const q = new URLSearchParams(search());
    if (open() === id) q.delete('app'); else q.set('app', id);
    go(`/projects/${encodeURIComponent(props.projectId)}?${q}`);
  };
  return <div class="stack" data-ui="project-apps">
    <Show when={read.error && !read.data}><ErrorBanner text={read.error ?? ''} onRetry={read.reload} /></Show>
    <Show when={read.data} fallback={<Show when={!read.error}><Skeleton rows={2} /></Show>}>
      <Show when={ids().length} fallback={<p class="muted">No apps yet.</p>}>
        <ReflowTable label="Project apps" head={['App', 'State', 'Pipelines, use over 30 days, limits', 'Last use']} cols="minmax(8rem, 1fr) auto minmax(10rem, 2fr) auto">
          <For each={ids()}>{id => <Show when={byId().get(id)}>{app =>
            <ReflowRow name={app().name} selected={open() === id} onOpen={() => toggle(id)}
              cells={[
                { part: 'state', el: <StateBadge family="projectApp" state={app().state} /> },
                { part: 'meta', el: appRowText(app()) },
                { part: 'time', el: app().lastUsedAt != null ? <span>used <AgeChip t={app().lastUsedAt!} /></span> : <AgeChip t={app().createdAt} /> },
              ]}
              detail={<AppDetail a={app()} manage={manage()} onChanged={read.reload} />} />
          }</Show>}</For>
        </ReflowTable>
      </Show>
      <p class="pw-muted-line">An agent or a person asks for an app with project_app_ask; you approve it in Needs you.</p>
    </Show>
  </div>;
}
