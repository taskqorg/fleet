// Project resource access in the app (docs/designs/fleet-project-access.md; board frames "System › Access: servers and
// project grants" in screens/system.bl and "Project › Access: grants and approval card" in screens/projects.bl, both
// approved 2026-11-26): the access servers on System › Access and a project's grants on its Access facet. Both read
// access_grants, the operation agents call too; Check and Revoke are access_server_check, access_grant_check and
// access_grant_revoke. Credentials never reach the browser: a grant carries its credential's fingerprint only.
import { createEffect, createMemo, createSignal, For, onCleanup, Show } from 'solid-js';
import {
  AgeChip, Button, DataTable, DetailSection, EntityCode, ErrorBanner, Empty, KvRow, Skeleton, StateBadge, TableName, TableRow, copyText,
} from '../../../packages/ui/src/index.ts';
import { op, type Ops } from '../api.ts';
import { useLocation, useSearch } from '../router.ts';
import { useOp } from '../store.ts';
import { toTarget } from '../routes.ts';

type Reply = Ops['access_grants']['reply'];
export type AccessServer = Reply['servers'][number];
export type AccessGrant = Reply['grants'][number];

// A grant the provisioner works on now; a revoke that failed (revoking with an error) waits for Retry revoke.
const busyGrant = (g: AccessGrant) => g.state === 'provisioning' || (g.state === 'revoking' && !g.error);
const ENDED = new Set<AccessGrant['state']>(['declined', 'withdrawn', 'revoked']);

/**
 * access_grants for a project (or every grant the caller sees). The operation has no change kind of its own, so the
 * read repeats every 3 s only while a grant is provisioning or revoking, or a check this page started has not
 * answered yet; otherwise it reads once and after each action.
 */
export function useAccessGrants(projectId: () => string | undefined) {
  const [waiting, setWaiting] = createSignal(false);
  const [every, setEvery] = createSignal(0, { ownedWrite: true });
  const read = useOp('access_grants', () => { const p = projectId(); return p ? { projectId: p } : {}; }, { every });
  createEffect(() => waiting() || !!read.data?.grants.some(busyGrant), busy => { setEvery(busy ? 3000 : 0); });
  return { read, setWaiting };
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
  return g.level === 'read' ? 'read ok · write refused' : 'read ok · write ok';
}

function GrantProbe(props: { g: AccessGrant }) {
  return <span class={props.g.probe && !props.g.probe.ok ? 't-danger' : 'muted'} data-tip={props.g.probe?.error}>
    {probeText(props.g)}<Show when={props.g.probe}>{p => <> · <AgeChip t={p().at} /></>}</Show>
  </span>;
}

/** The lines of a recipe or probe as the card shows them, one per line, in mono. */
function Lines(props: { lines?: string[] }) {
  return <Show when={props.lines?.length} fallback={<span class="muted">—</span>}>
    <pre class="access-lines">{(props.lines ?? []).join('\n')}</pre>
  </Show>;
}

/** One grant opened: scope, recipe, probe steps, error, credential fingerprint and the question, with Check and Revoke. */
function GrantDetail(props: { g: AccessGrant; onChanged: () => void; onWaiting: (on: boolean) => void }) {
  const [err, setErr] = createSignal('');
  const [confirm, setConfirm] = createSignal(false);
  const [busy, setBusy] = createSignal('');
  const me = useOp('me', {});
  // after: the probe time before this page's Check (null: no check waits); a newer probe is its answer.
  const [after, setAfter] = createSignal<number | null>(null);
  const g = () => props.g;
  const settle = () => { setAfter(null); setBusy(''); props.onWaiting(false); };
  let timer = 0;
  onCleanup(() => { clearTimeout(timer); if (after() !== null) props.onWaiting(false); });
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
  // A check probes in the background: the read repeats until a newer probe shows, or for 60 s.
  const check = () => {
    setErr(''); setBusy('check');
    const before = g().probe?.at ?? 0;
    void op('access_grant_check', { grant: g().id }).then(() => {
      setAfter(before); props.onWaiting(true); props.onChanged();
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
    <div><b>Probe</b> <GrantProbe g={g()} /><Show when={g().probe?.steps?.length}><Lines lines={g().probe?.steps} /></Show></div>
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
  const { read, setWaiting } = useAccessGrants(() => props.projectId);
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
        <DataTable label="Project access" cls="access-table" head={<tr>
          <th scope="col">Resource</th><th scope="col">Level</th><th scope="col">State</th><th scope="col">Last probe</th><th scope="col">Connection</th>
        </tr>}>
          <For each={ids()}>{id => <Show when={byId().get(id)}>{grant => <>
            <TableRow selected={open() === id} onOpen={() => toggle(id)}>
              <TableName label={grantResource(grant())} current={open() === id} onOpen={() => toggle(id)} />
              <td class="muted keep">{grant().level}</td>
              <td class="keep"><StateBadge family="accessGrant" state={grant().state} /></td>
              <td class="keep"><GrantProbe g={grant()} /></td>
              <td><Show when={grant().connection} fallback={<span class="muted">—</span>}>{c => <span class="mono">{c()}</span>}</Show></td>
            </TableRow>
            <Show when={open() === id}>
              <tr class="t-detail"><td colspan="5"><GrantDetail g={grant()} onChanged={read.reload} onWaiting={setWaiting} /></td></tr>
            </Show>
          </>}</Show>}</For>
        </DataTable>
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
  const { read } = useAccessGrants(() => undefined);
  const [checking, setChecking] = createSignal('');
  const [err, setErr] = createSignal('');
  const open = () => new URLSearchParams(search()).get('server') ?? '';
  const toggle = (id: string) => go(open() === id ? '/access' : `/access?server=${encodeURIComponent(id)}`);
  const check = (id: string) => {
    setChecking(id); setErr('');
    void op('access_server_check', { id }).then(() => { setChecking(''); read.reload(); }, e => { setChecking(''); setErr(String(e.message ?? e)); });
  };
  return <Show when={read.data?.servers.length}><DetailSection title="Servers">
    <Show when={err()}>{e => <ErrorBanner text={e()} />}</Show>
    <DataTable label="Servers" cls="access-table" head={<tr>
      <th scope="col">Server</th><th scope="col">Address</th><th scope="col">Via</th><th scope="col">State</th><th scope="col">Host key</th>
    </tr>}>
      <For each={read.data!.servers}>{s => {
        const st = createMemo(() => serverState(s));
        const grants = createMemo(() => (read.data?.grants ?? []).filter(g => g.server === s.id && !ENDED.has(g.state)));
        return <>
          <TableRow selected={open() === s.id} onOpen={() => toggle(s.id)}>
            <TableName label={s.id} current={open() === s.id} onOpen={() => toggle(s.id)} />
            <td class="muted keep">{s.address}</td>
            <td class="muted keep">{s.via ?? '—'}</td>
            <td><StateBadge family="accessServer" state={st().state} reason={st().reason} /></td>
            <td><HostKey line={s.hostKey} /></td>
          </TableRow>
          <Show when={open() === s.id}>
            <tr class="t-detail"><td colspan="5"><div class="stack access-detail">
              <KvRow icon="shield" label="Host key" meta="pinned" value={<HostKey line={s.hostKey} full />} />
              <KvRow icon="history" label="Last check" meta={s.checked ? (s.checked.ok ? `fleet-provision ${s.checked.version ?? ''}`.trim() : 'failed') : 'never checked'}
                value={<Show when={s.checked}>{c => <span class={c().ok ? 'muted' : 't-danger'}><AgeChip t={c().at} /></span>}</Show>} />
              <Show when={s.checked && !s.checked.ok && s.checked.error}>{e => <ErrorBanner text={e()} />}</Show>
              <Show when={s.checked?.steps?.length}><Lines lines={s.checked!.steps} /></Show>
              <Show when={!s.enrolledAt}>
                <span>Not enrolled: run <code>access_server_add</code> again to get the enroll command, run it once on {s.id} as root, then Check.</span>
              </Show>
              <div class="res-bar">
                <Button label={checking() === s.id ? 'Checking' : 'Check'} icon="rotate-cw" disabledReason={checking() ? 'Checking' : undefined} onClick={() => check(s.id)} />
              </div>
              <div>
                <b>Grants on {s.id}</b>
                <Show when={grants().length} fallback={<p class="muted">No live grants.</p>}>
                  <For each={grants()}>{g =>
                    <KvRow icon="shield" label={grantResource(g)} meta={`${g.projectId} · ${g.level}`}
                      value={<a href={toTarget(grantPath(g))} onClick={e => { if (!e.metaKey && !e.ctrlKey && !e.shiftKey) { e.preventDefault(); go(grantPath(g)); } }}>
                        <StateBadge family="accessGrant" state={g.state} /></a>} />}</For>
                </Show>
              </div>
            </div></td></tr>
          </Show>
        </>;
      }}</For>
    </DataTable>
  </DetailSection></Show>;
}

