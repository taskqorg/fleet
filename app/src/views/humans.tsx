import { createMemo, createSignal, Show } from 'solid-js';
import type { JSX } from '@solidjs/web';
import {
  Button, CatalogueTree, Choice, DetailSection, Empty, ErrorBanner, KvRow, ListDetail, ShellActions, Skeleton, TextField,
  type CatalogueTreeNode,
} from '../../../packages/ui/src/index.ts';
import type { HumanAccessRow, PeopleAccessStatusReply, PeopleUpdateReply, PeopleUpdateRequest } from '../../../contracts/generated/ops.ts';
import { op, useAlive } from '../api.ts';
import { useLocation } from '../router.ts';
import { useOp } from '../store.ts';

const humanPath = (id: string) => '/humans/' + encodeURIComponent(id);
const roles = [{ id: 'operator', label: 'Operator' }, { id: 'architect', label: 'Architect' }];

export function Humans(props: { id?: string }) {
  const [, go] = useLocation();
  const me = useOp('me', {});
  const roster = useOp('people_list', () => me.data?.owner ? { manage: true } : null);
  const access = useOp('people_access_status', () => me.data?.owner ? {} : null);
  const projects = useOp('project_list', () => me.data?.owner ? {} : null);
  const [syncError, setSyncError] = createSignal<{ email: string; message: string }>();
  const [syncing, setSyncing] = createSignal(false);
  const alive = useAlive();
  const rows = () => roster.data?.roster ?? [];
  const title = (id: string) => String(projects.data?.projects.find(p => p.id === id)?.name ?? id);
  const selected = createMemo(() => rows().find(p => p.email === props.id));
  const selectedSyncError = createMemo(() => {
    const failure = syncError();
    return failure?.email === props.id ? failure?.message : undefined;
  });
  const status = () => access.error || access.state === 'offline' ? 'error' : access.data?.state;
  const admitted = createMemo(() => new Set(status() === 'active' ? access.data?.admitted : []));
  const missing = (email: string) => status() === 'active' && !admitted().has(email);
  const accessOnly = createMemo(() => {
    if (!roster.data || roster.error) return [];
    const emails = new Set(admitted());
    const failed = syncError();
    if (failed) emails.add(failed.email);
    for (const row of rows()) emails.delete(row.email);
    return [...emails];
  });
  const unlisted = () => props.id && accessOnly().includes(props.id);
  const nodes = createMemo<CatalogueTreeNode[]>(() => {
    const groups: CatalogueTreeNode[] = [false, true].map(machine => ({
      id: machine ? 'machines' : 'humans', label: machine ? 'Machine identities' : 'People',
      children: rows().filter(p => p.machine === machine).map(p => ({
        id: p.email, label: p.name,
        sub: p.owner ? 'Owner' : machine ? 'Service identity' : missing(p.email) ? 'Cloudflare email missing' : p.email,
      })),
    }));
    if (accessOnly().length) groups.push({
      id: 'cloudflare-only', label: 'Cloudflare only',
      children: accessOnly().map(email => ({ id: email, label: email })),
    });
    return groups;
  });
  const close = () => go('/humans');
  const saved = (reply: PeopleUpdateReply, stay: boolean) => {
    roster.reload();
    access.reload();
    if (reply.cloudflareSync?.state === 'error') {
      setSyncError({ email: reply.email, message: 'Fleet access saved. ' + (reply.cloudflareSync.error ?? 'Cloudflare sync failed.') });
      go(humanPath(reply.email));
    } else {
      if (syncError()?.email === reply.email) setSyncError(undefined);
      if (stay) go(humanPath(reply.email));
      else close();
    }
  };

  async function retry(email: string, baseVersion: string) {
    if (syncing()) return;
    setSyncing(true);
    try {
      const reply = await op('people_access_sync', { email, baseVersion }, { signal: alive });
      if (!alive.aborted) {
        if (reply.state === 'error') setSyncError({ email, message: reply.error ?? 'Cloudflare sync failed.' });
        else if (syncError()?.email === email) setSyncError(undefined);
        if (!baseVersion && reply.state === 'synced') close();
      }
    } catch (e) {
      if (!alive.aborted) setSyncError({ email, message: e instanceof Error ? e.message : String(e) });
    } finally {
      if (!alive.aborted) {
        setSyncing(false);
        roster.reload();
        access.reload();
      }
    }
  }

  const admission = (email: string) => {
    switch (status()) {
      case 'active': return admitted().has(email) ? 'Email included' : 'Email missing';
      case 'manual': return 'Managed in Cloudflare';
      case 'error': return 'Unknown';
      default: return 'Checking…';
    }
  };
  return <Show when={me.data?.owner} fallback={
    <Show when={me.data || me.error} fallback={<Skeleton rows={4} />}>
      <Empty text="Only the Fleet owner can manage Humans." />
    </Show>
  }>
    <ShellActions>
      <Button icon="plus" iconOnly label="Add human" onClick={() => go('/humans/new')} />
      <Show when={status() !== 'manual'}>
        <Button icon="rotate-ccw" iconOnly label="Check Cloudflare admission" onClick={access.reload} />
      </Show>
    </ShellActions>
    <ListDetail
      detailKey={props.id}
      list={<>
        <Show when={roster.error}>{error => <ErrorBanner text={error()} onRetry={roster.reload} />}</Show>
        <Show when={status() === 'error'}>
          <ErrorBanner text={access.error ?? access.data?.error ?? 'Cloudflare admission could not be checked.'} onRetry={access.reload} />
        </Show>
        <Show when={roster.data} fallback={<Skeleton rows={5} />}>
          <CatalogueTree label="Humans and machine identities" fill nodes={nodes()} selectedId={props.id}
            onSelect={n => { if (!n.children) go(humanPath(n.id)); }} />
        </Show>
      </>}
      detail={props.id ? <section class="detail" data-ui="detail" tabindex={-1}>
        <header class="detail-h">
          <h2>{props.id === 'new' ? 'Add human' : selected()?.name ?? (unlisted() ? props.id : 'Human access')}</h2>
          <Button icon="x" iconOnly label="Close" onClick={close} />
        </header>
        <Show when={selectedSyncError()}>
          {message => <ErrorBanner text={message()} />}
        </Show>
        <Show when={props.id === 'new'} fallback={
          <Show when={selected()} fallback={
            <Show when={unlisted()} fallback={roster.data ? <Empty text="This identity is absent from the Fleet roster." /> : <Skeleton rows={4} />}>
              <DetailSection title="Effective access">
                <KvRow icon="shield" label="Fleet admission" value="Absent from Fleet" />
                <CloudflareAdmission value={admission(props.id!)} />
                <Button kind="danger" label="Remove Cloudflare email entry" busy={syncing()}
                  onClick={() => void retry(props.id!, '')} />
              </DetailSection>
            </Show>
          }>
            {person => <>
              <AccessFacts person={person()} projectTitle={title} admission={admission(person().email)}>
                <Show when={!person().owner && !person().machine && (status() === 'error' ||
                  missing(person().email) || syncError()?.email === person().email)}>
                  <Button label="Retry Cloudflare sync" busy={syncing()} onClick={() => void retry(person().email, person().version)} />
                </Show>
              </AccessFacts>
              <Show when={person().owner || person().machine ? undefined : person().email} keyed>
                <HumanEditor person={person()} onSave={saved} accessState={status()} />
              </Show>
            </>}
          </Show>
        }>
          <HumanEditor onSave={saved} accessState={status()} />
        </Show>
      </section> : undefined}
      idle={<Empty text="Choose an identity to see its Fleet access." />}
    />
  </Show>;
}

function CloudflareAdmission(props: { value: string }) {
  return <div aria-live="polite">
    <KvRow icon="shield" label="Cloudflare admission" value={props.value} />
  </div>;
}

function AccessFacts(props: { person: HumanAccessRow; projectTitle: (id: string) => string; admission: string; children: JSX.Element }) {
  const granted = () => props.person.grants.filter(g => g.kind === 'project');
  const all = () => props.person.owner || props.person.grants.some(g => g.kind === '*' || g.kind === 'project' && g.id === '*');
  return <DetailSection title="Effective access">
    <KvRow icon="shield" label="Identity" value={props.person.machine ? 'Cloudflare service identity' : props.person.email} />
    <KvRow icon="shield" label="Fleet admission" value="Listed in Fleet" />
    <Show when={!props.person.machine}>
      <CloudflareAdmission value={props.admission} />
      {props.children}
    </Show>
    <KvRow icon="shield" label="Authority" value={props.person.owner ? 'Fleet owner' :
      props.person.grants.some(g => g.kind === '*') ? 'Full access grant' : props.person.roles.join(', ')} />
    <KvRow icon="folder-open" label="Effective project access" value={all() ? 'All projects' :
      [...new Set([...granted().map(g => g.id), ...props.person.initialProjects])].map(props.projectTitle).join(', ') || 'None'} />
    <KvRow icon="folder-open" label="Explicit project grants" value={granted().map(g => g.id === '*' ? 'All projects' : props.projectTitle(g.id)).join(', ') || 'None'} />
    <KvRow icon="folder-open" label="Initial project membership" value={props.person.initialProjects.map(props.projectTitle).join(', ') || 'None'} />
    <KvRow icon="chart-no-axes-column" label="Personal Lab" value={props.person.personalLab ? 'Granted' : 'No grant'} />
  </DetailSection>;
}

function HumanEditor(props: {
  person?: HumanAccessRow;
  onSave: (reply: PeopleUpdateReply, stay: boolean) => void;
  accessState?: PeopleAccessStatusReply['state'];
}) {
  const base = props.person;
  const [email, setEmail] = createSignal(base?.email ?? '');
  const [name, setName] = createSignal(base?.name ?? '');
  const [selectedRoles, setRoles] = createSignal(base?.roles ?? ['operator']);
  const [projects, setProjects] = createSignal(base?.grants.filter(g => g.kind === 'project').map(g => g.id).join(', ') ?? '');
  const [personalLab, setPersonalLab] = createSignal(base?.personalLab ?? false);
  const [confirm, setConfirm] = createSignal(false);
  const [busy, setBusy] = createSignal(false);
  const [error, setError] = createSignal('');
  const alive = useAlive();
  async function save(revoke: boolean) {
    if (busy()) return;
    setBusy(true);
    setError('');
    const subject = { email: email().trim(), baseVersion: base?.version ?? '' };
    const body: PeopleUpdateRequest = revoke ? { ...subject, revoke: true } : {
      ...subject, name: name().trim(), roles: selectedRoles(), projects: projects().split(',').map(id => id.trim()).filter(Boolean),
      personalLab: personalLab(),
    };
    try {
      const reply = await op('people_update', body, { signal: alive });
      if (!alive.aborted) props.onSave(reply, !revoke && !base);
    } catch (e) {
      if (!alive.aborted) setError(e instanceof Error ? e.message : String(e));
    } finally {
      if (!alive.aborted) setBusy(false);
    }
  }

  const changed = () => !!base && props.person?.version !== base.version;
  return <DetailSection title={base ? 'Edit Fleet access' : 'Sign-in and access'}>
    <Show when={!base}>
      <Show when={props.accessState === 'manual'}>
        <p>After saving, add the same sign-in email to fleet-people in the Cloudflare dashboard.</p>
      </Show>
      <Show when={props.accessState === 'active'}>
        <p>Fleet adds the sign-in email to the Cloudflare Access fleet-people group when you save.</p>
      </Show>
      <TextField label="Sign-in email" type="email" value={email()} onChange={setEmail} />
    </Show>
    <TextField label="Name" value={name()} onChange={setName} />
    <Choice label="Roles" options={roles} value={selectedRoles()} onChange={id =>
      setRoles(current => current.includes(id) ? current.filter(r => r !== id) : [...current, id])} />
    <TextField label="Explicit project grants" value={projects()} onChange={setProjects}
      help="Enter project IDs separated by commas. Architects can use * for all projects." />
    <Button label="Personal Lab" pressed={personalLab()} onClick={() => setPersonalLab(v => !v)} />
    <Show when={changed()}>
      <ErrorBanner text="Access changed. Close and reopen this identity to edit the current grants." />
    </Show>
    <Show when={error()}>{message => <ErrorBanner text={message()} />}</Show>
    <div class="actions">
      <Button kind="primary" label={base ? 'Save access' : 'Add human'} busy={busy()}
        disabledReason={changed() ? 'Reopen this identity to edit the current grants' : undefined} onClick={() => void save(false)} />
      <Show when={base && !confirm()}>
        <Button kind="danger" label="Revoke access" onClick={() => setConfirm(true)} />
      </Show>
    </div>
    <Show when={confirm()}>
      <p>Revoke Fleet sign-in and initial project memberships for {base?.name}?</p>
      <Show when={props.accessState === 'manual'} fallback={<p>Fleet will also try to remove the fleet-people email entry.</p>}>
        <p>Remove the email from fleet-people in the Cloudflare dashboard after revoking Fleet access.</p>
      </Show>
      <div class="actions">
        <Button kind="danger" label="Confirm revocation" busy={busy()}
          disabledReason={changed() ? 'Reopen this identity first' : undefined} onClick={() => void save(true)} />
        <Button label="Cancel" onClick={() => setConfirm(false)} />
      </div>
    </Show>
  </DetailSection>;
}
