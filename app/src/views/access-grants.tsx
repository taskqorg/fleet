// Project resource access in the app (docs/designs/fleet-project-access.md; board frames "System › Access: servers and
// project grants" in screens/system.bl and "Project › Access: grants and approval card" in screens/projects.bl, both
// approved 2026-11-26; "Project › Access · phone 390" and "System › Access · server not enrolled: enroll command",
// approved 2026-11-26): the access servers on System › Access and a project's grants on its Access facet. Both read
// access_grants, the operation agents call too, which the change stream rereads (kinds access-grant, access-server);
// Check and Revoke are access_server_check, access_grant_check and access_grant_revoke. Credentials never reach the
// browser: a grant carries its credential's fingerprint only. Both lists reflow to two-line rows when narrow
// (ReflowTable) and never scroll sideways.
import { createEffect, createMemo, createSignal, For, onCleanup, Show } from 'solid-js';
import {
  AgeChip, Button, DetailSection, EntityCode, ErrorBanner, Empty, KvRow, ReflowRow, ReflowTable, Skeleton, StateBadge, copyText,
} from '../../../packages/ui/src/index.ts';
import { op, type Ops } from '../api.ts';
import { useLocation, useSearch } from '../router.ts';
import { useOp } from '../store.ts';
import { toTarget } from '../routes.ts';

type Reply = Ops['access_grants']['reply'];
export type AccessServer = Reply['servers'][number];
export type AccessGrant = Reply['grants'][number];

const ENDED = new Set<AccessGrant['state']>(['declined', 'withdrawn', 'revoked']);

/** access_grants for a project, or every grant the caller sees; the change stream rereads it (no timer). */
export function useAccessGrants(projectId: () => string | undefined) {
  return useOp('access_grants', () => { const p = projectId(); return p ? { projectId: p } : {}; });
}

/** A server's state: failing once enrolled and its last check failed; enrolled with the provisioner's version; else not enrolled. */
export function serverState(s: AccessServer): { state: 'enrolled' | 'not-enrolled' | 'failing'; reason?: string } {
  if (s.enrolledAt && s.checked && !s.checked.ok) return { state: 'failing', reason: s.checked.error };
  if (s.enrolledAt) return { state: 'enrolled', reason: s.checked?.version ? 'v' + s.checked.version : undefined };
  return { state: 'not-enrolled' };
}

/** The SHA-256 fingerprint of an authorized-key line (`ssh-ed25519 AAAA…`), as ssh-keygen prints it. */
async function fingerprint(line: string): Promise<string> {
  const b64 = line.trim().split(/\s+/)[1] ?? '';
  const blob = Uint8Array.from(atob(b64), c => c.charCodeAt(0));
  const sum = new Uint8Array(await crypto.subtle.digest('SHA-256', blob));
  return 'SHA256:' + btoa(String.fromCharCode(...sum)).replace(/=+$/, '');
}

/** A server's pinned host key as its fingerprint: short in a row, whole in the detail with Copy. */
export function HostKey(props: { line?: string; full?: boolean }) {
  const [print, setPrint] = createSignal('');
  createEffect(() => props.line ?? '', line => {
    let live = true;
    if (!line) setPrint('');
    else void fingerprint(line).then(p => { if (live) setPrint(p); }, () => { if (live) setPrint('unreadable'); });
    return () => { live = false; };
  });
  const short = () => { const p = print(); return p.length > 16 ? `${p.slice(0, 10)}…${p.slice(-3)}` : p; };
  return <Show when={props.line} fallback={<span class="muted">—</span>}>
    <Show when={props.full} fallback={<span class="mono muted" data-tip={print()}>{short()}</span>}>
      <span class="mono">{print()}</span> <Button label="Copy host key fingerprint" icon="copy" iconOnly kind="quiet" onClick={() => void copyText(print())} />
    </Show>
  </Show>;
}

const KIND: Record<AccessGrant['kind'], string> = { mysql: 'MySQL', files: 'Files' };
const plural = (n: number, one: string) => `${n} ${one}${n === 1 ? '' : 's'}`;

/** A grant's resource: kind, what it covers (a database's every table, a tables count, a paths count) and its server. */
export function grantResource(g: AccessGrant): string {
  const t = g.scope.tables ?? [], p = g.scope.paths ?? [];
  const what = g.kind === 'mysql' ? (t.length === 1 && t[0] === '*' ? g.scope.database ?? 'every table' : plural(t.length, 'table')) : plural(p.length, 'path');
  return `${KIND[g.kind]} · ${what} · ${g.server}`;
}

/** The last probe in words: read works and, for read, write is refused; or that it failed. */
function probeText(g: AccessGrant): string {
  const p = g.probe;
  if (!p) return g.state === 'asked' ? 'waits for approval' : '—';
  if (!p.ok) return 'failed';
  return g.level === 'read' ? 'read ok, write refused' : 'read ok, write ok';
}

function GrantProbe(props: { g: AccessGrant; age?: boolean }) {
  return <span class={props.g.probe && !props.g.probe.ok ? 't-danger' : undefined} data-tip={props.g.probe?.error}>
    {probeText(props.g)}<Show when={props.age && props.g.probe}>{p => <> · <AgeChip t={p().at} /></>}</Show>
  </span>;
}

/** The lines of a recipe or probe as the card shows them, one per line, in mono. */
function Lines(props: { lines?: string[] }) {
  return <Show when={props.lines?.length} fallback={<span class="muted">—</span>}>
    <pre class="access-lines">{(props.lines ?? []).join('\n')}</pre>
  </Show>;
}

/** One grant opened: scope, recipe, probe steps, error, credential fingerprint and the question, with Check and Revoke. */
function GrantDetail(props: { g: AccessGrant; onChanged: () => void }) {
  const [err, setErr] = createSignal('');
  const [confirm, setConfirm] = createSignal(false);
  const [busy, setBusy] = createSignal('');
  const me = useOp('me', {});
  // after: the probe time before this page's Check (null: no check waits); a newer probe is its answer.
  const [after, setAfter] = createSignal<number | null>(null);
  const g = () => props.g;
  const settle = () => { setAfter(null); setBusy(''); };
  let timer = 0;
  onCleanup(() => clearTimeout(timer));
  createEffect(() => [after(), g().probe?.at ?? 0] as const, ([before, at]) => { if (before !== null && at > before) { clearTimeout(timer); settle(); } });
  const scope = createMemo(() => {
    const s = g().scope;
    return g().kind === 'mysql' ? `${s.database ?? '—'}: ${(s.tables ?? []).join(', ')}` : (s.paths ?? []).join(', ');
  });
  // Check probes a ready grant again; on a failed one the server provisions it again (access_grant_check).
  const canCheck = () => g().state === 'ready' || g().state === 'failed';
  const checkLabel = () => (g().state === 'failed' ? 'Provision again' : 'Check');
  // Revoke is the owner's (access_grant_revoke); a revoke that failed runs again.
  const canRevoke = () => !!me.data?.owner && !ENDED.has(g().state) && (g().state !== 'revoking' || !!g().error);
  const revokeLabel = () => (g().state === 'asked' ? 'Withdraw' : g().state === 'revoking' ? 'Retry revoke' : 'Revoke');
  // A check probes in the background; its probe arrives through the change stream. The label waits for it, 60 s at most.
  const check = () => {
    setErr(''); setBusy('check');
    const before = g().probe?.at ?? 0;
    void op('access_grant_check', { grant: g().id }).then(() => {
      setAfter(before); props.onChanged();
      timer = window.setTimeout(settle, 60_000);
    }, e => { setBusy(''); setErr(String(e.message ?? e)); });
  };
  const revoke = () => {
    setErr(''); setBusy('revoke'); setConfirm(false);
    void op('access_grant_revoke', { grant: g().id }).then(() => { setBusy(''); props.onChanged(); }, e => { setBusy(''); setErr(String(e.message ?? e)); });
  };
  return <div class="stack access-detail">
    <KvRow icon="shield" label="Scope" meta={`${g().level} · OS user ${g().user}`} value={<span class="mono">{scope()}</span>} />
    <Show when={g().reason}><KvRow icon="message-circle-question" label="Reason" value={<span>{g().reason}</span>} /></Show>
    <div><b>Recipe</b><Lines lines={g().recipe} /></div>
    <div><b>Probe</b> <GrantProbe g={g()} age /><Show when={g().probe?.steps?.length}><Lines lines={g().probe?.steps} /></Show></div>
    <Show when={g().error ?? g().probe?.error}>{e => <ErrorBanner text={e()} />}</Show>
    <Show when={g().credential}>{c => <KvRow icon="shield" label="Credential" meta="fingerprint" value={<span class="mono">{c()}</span>} />}</Show>
    <Show when={g().questionId}>{q => <KvRow icon="message-circle-question" label="Approval" meta={`asked by ${g().askedBy}`} value={<EntityCode kind="question" id={q()} label />} />}</Show>
    <Show when={err()}>{e => <ErrorBanner text={e()} />}</Show>
    <Show when={confirm()} fallback={<div class="res-bar">
      <Show when={canCheck()}><Button label={busy() === 'check' ? 'Checking' : checkLabel()} icon="rotate-cw" disabledReason={busy() ? 'Working' : undefined} onClick={check} /></Show>
      <Show when={canRevoke()}><Button label={revokeLabel()} kind="danger" disabledReason={busy() ? 'Working' : undefined} onClick={() => setConfirm(true)} /></Show>
    </div>}>
      <div class="res-bar">
        <span>{g().state === 'asked'
          ? 'Withdraw the ask and its approval card? The project can ask again.'
          : `Revoke ${grantResource(g())}? Fleet removes the database user, OS user and keys on ${g().server} and on PDS18; the project can ask again.`}</span>
        <Button label={revokeLabel()} kind="danger" onClick={revoke} />
        <Button label="Cancel" kind="quiet" onClick={() => setConfirm(false)} />
      </div>
    </Show>
  </div>;
}

/** Project › Access: the project's grants, one row each; a row opens its detail under it (`?grant=<id>`). */
export function ProjectAccess(props: { projectId: string; data?: string }) {
  const [, go] = useLocation();
  const search = useSearch();
  const read = useAccessGrants(() => props.projectId);
  // Rows are keyed by id: a reread that changes a grant keeps its open detail (a confirmation, a Check in flight).
  const byId = createMemo(() => new Map((read.data?.grants ?? []).map(g => [g.id, g])));
  const ids = createMemo(() => (read.data?.grants ?? []).toSorted((a, b) => b.createdAt - a.createdAt).map(g => g.id));
  const open = () => new URLSearchParams(search()).get('grant') ?? '';
  const toggle = (id: string) => {
    const q = new URLSearchParams(search());
    if (open() === id) q.delete('grant'); else q.set('grant', id);
    go(`/projects/${encodeURIComponent(props.projectId)}?${q}`);
  };
  return <article class="detail" data-ui="project-access" aria-label="Access">
    <Show when={props.data}>{d => <p class={d() === 'personal' ? 't-attention' : 'muted'}>{d()} data</p>}</Show>
    <Show when={read.error && !read.data}><ErrorBanner text={read.error ?? ''} onRetry={read.reload} /></Show>
    <Show when={read.data} fallback={<Show when={!read.error}><Skeleton rows={3} /></Show>}>
      <Show when={ids().length} fallback={<Empty text="This project has no access to servers yet. An agent asks for it with access_grant_ask; the owner approves it in Needs you." />}>
        <ReflowTable label="Project access" head={['Resource', 'Level', 'State', 'Last probe', 'Connection', 'When']}
          cols="minmax(8rem, 1fr) auto auto auto auto auto">
          <For each={ids()}>{id => <Show when={byId().get(id)}>{grant =>
            <ReflowRow name={grantResource(grant())} selected={open() === id} onOpen={() => toggle(id)}
              cells={[
                { part: 'meta', el: grant().level },
                { part: 'state', el: <StateBadge family="accessGrant" state={grant().state} /> },
                { part: 'meta', el: <GrantProbe g={grant()} /> },
                { part: 'meta', el: grant().connection ? <span class="mono">{grant().connection}</span> : undefined },
                { part: 'time', el: <AgeChip t={grant().probe?.at ?? grant().createdAt} /> },
              ]}
              detail={<GrantDetail g={grant()} onChanged={read.reload} />} />
          }</Show>}</For>
        </ReflowTable>
      </Show>
    </Show>
  </article>;
}

/** A grant opened on its project's Access facet (the path the views speak; toTarget gives the address bar's). */
const grantPath = (g: AccessGrant) => `/projects/${encodeURIComponent(g.projectId)}?facet=access&grant=${encodeURIComponent(g.id)}`;

/** System › Access, group Servers: the access servers; a row opens its detail under it (`?server=<id>`). */
export function AccessServers() {
  const [, go] = useLocation();
  const search = useSearch();
  const read = useAccessGrants(() => undefined);
  const [checking, setChecking] = createSignal('');
  const [err, setErr] = createSignal('');
  const open = () => new URLSearchParams(search()).get('server') ?? '';
  const toggle = (id: string) => go(open() === id ? '/access' : `/access?server=${encodeURIComponent(id)}`);
  const check = (id: string) => {
    setChecking(id); setErr('');
    void op('access_server_check', { id }).then(() => { setChecking(''); read.reload(); }, e => { setChecking(''); setErr(String(e.message ?? e)); });
  };
  const server = (id?: string) => read.data?.servers.find(x => x.id === id);
  const serverIds = createMemo(() => (read.data?.servers ?? []).map(x => x.id));
  return <Show when={read.data?.servers.length}><DetailSection title="Servers">
    <Show when={err()}>{e => <ErrorBanner text={e()} />}</Show>
    <ReflowTable label="Servers" head={['Server', 'Address', 'Via', 'State', 'Host key', 'Checked']} cols="minmax(6rem, 1fr) auto auto minmax(8rem, 2fr) auto auto">
      <For each={serverIds()}>{id => <Show when={server(id)}>{srv => {
        // Keyed by id: a reread keeps the open detail (the fingerprint, a selection in the command).
        const st = createMemo(() => serverState(srv()));
        const grants = createMemo(() => (read.data?.grants ?? []).filter(g => g.server === srv().id && !ENDED.has(g.state)));
        return <ReflowRow name={srv().id} selected={open() === srv().id} onOpen={() => toggle(srv().id)}
          cells={[
            { part: 'meta', el: srv().address },
            { part: 'meta', el: srv().via ? `via ${srv().via}` : undefined },
            { part: 'state', el: <StateBadge family="accessServer" state={st().state} reason={st().reason} /> },
            { part: 'meta', el: <HostKey line={srv().hostKey} /> },
            { part: 'time', el: srv().checked ? <span class={srv().checked?.ok ? undefined : 't-danger'}><AgeChip t={srv().checked!.at} /></span> : undefined },
          ]}
          detail={<div class="stack access-detail">
            <KvRow icon="shield" label="Host key" meta="pinned" value={<HostKey line={srv().hostKey} full />} />
            <KvRow icon="history" label="Last check" meta={srv().checked ? (srv().checked?.ok ? `fleet-provision ${srv().checked?.version ?? ''}`.trim() : 'failed') : 'never checked'}
              value={<Show when={srv().checked}>{c => <span class={c().ok ? 'muted' : 't-danger'}><AgeChip t={c().at} /></span>}</Show>} />
            <Show when={srv().checked && !srv().checked?.ok && srv().checked?.error}>{e => <ErrorBanner text={e()} />}</Show>
            <Show when={srv().checked?.steps?.length}><Lines lines={srv().checked!.steps} /></Show>
            <Show when={srv().enroll} fallback={<Show when={!srv().enrolledAt}><span class="muted">Not enrolled. The enroll command shows here to full-access people once Fleet's provisioner key exists (access_server_add makes it).</span></Show>}>{cmd =>
              <Enroll server={srv()} via={server(srv().via)} command={cmd()} />}</Show>
            <div class="res-bar">
              <Show when={srv().enroll}>{cmd => <Button label="Copy command" icon="copy" kind="primary" onClick={() => void copyText(cmd())} />}</Show>
              <Button label={checking() === srv().id ? 'Checking' : 'Check'} icon="rotate-cw" disabledReason={checking() ? 'Checking' : undefined} onClick={() => check(srv().id)} />
              <Show when={srv().enroll}>{cmd => <span class="muted">{Math.max(1, Math.round(cmd().length / 1024))} KB, one line</span>}</Show>
            </div>
            <div>
              <b>Grants on {srv().id}</b>
              <Show when={grants().length} fallback={<p class="muted">No live grants.</p>}>
                <For each={grants()}>{g =>
                  <KvRow icon="shield" label={grantResource(g)} meta={`${g.projectId} · ${g.level}`}
                    value={<a href={toTarget(grantPath(g))} onClick={e => { if (!e.metaKey && !e.ctrlKey && !e.shiftKey) { e.preventDefault(); go(grantPath(g)); } }}>
                      <StateBadge family="accessGrant" state={g.state} /></a>} />}</For>
              </Show>
            </div>
          </div>} />;
      }}</Show>}</For>
    </ReflowTable>
  </DetailSection></Show>;
}

/** host[:port] as ssh takes it: the host, and -p for a port. */
const sshTarget = (address: string) => {
  const m = /^(.*):(\d+)$/.exec(address);
  return m ? `-p ${m[2]} root@${m[1]}` : `root@${address}`;
};

/**
 * The enroll command for a server to enroll (access_grants answers it to full-access people): where to run it, then
 * the command in two lines with an ellipsis; Copy command copies it whole.
 */
function Enroll(props: { server: AccessServer; via?: AccessServer; command: string }) {
  const s = () => props.server;
  const lead = () => {
    const c = s().checked;
    if (!s().enrolledAt) return '';
    if (c && !c.ok) return `The last check failed; if fleet-provision is missing or too old on ${s().id}, run the command again. `;
    return `${s().id} runs fleet-provision ${c?.version ?? '?'}: run the command again to update it. `;
  };
  return <>
    <span>{lead()}Run once as root on {s().address}
      <Show when={props.via}>{v => <> (reach it through {v().id}: <code>ssh -J root@{v().address} {sshTarget(s().address)}</code>)</>}</Show>
      , then Check. It installs fleet-provision and lets Fleet's provisioner key run only its recipes.</span>
    <div class="access-lines" role="group" aria-label="Enroll command"><div class="clamp">{props.command}</div></div>
  </>;
}
