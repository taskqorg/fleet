import { createMemo, createSignal, Show } from 'solid-js';
import {
  Button, CatalogueTree, Choice, DetailSection, Empty, ErrorBanner, KvRow, ListDetail, ShellActions, Skeleton, TextField,
  type CatalogueTreeNode,
} from '../../../packages/ui/src/index.ts';
import type { HumanAccessRow, PeopleUpdateRequest } from '../../../contracts/generated/ops.ts';
import { op, useAlive } from '../api.ts';
import { useLocation } from '../router.ts';
import { useOp } from '../store.ts';

const humanPath = (id: string) => '/humans/' + encodeURIComponent(id);
const roles = [{ id: 'operator', label: 'Operator' }, { id: 'architect', label: 'Architect' }];

export function Humans(props: { id?: string }) {
  const [, go] = useLocation();
  const me = useOp('me', {});
  const roster = useOp('people_list', () => me.data?.owner ? { manage: true } : null);
  const projects = useOp('project_list', () => me.data?.owner ? {} : null);
  const rows = () => roster.data?.roster ?? [];
  const title = (id: string) => String(projects.data?.projects.find(p => p.id === id)?.name ?? id);
  const selected = createMemo(() => rows().find(p => p.email === props.id));
  const nodes = createMemo<CatalogueTreeNode[]>(() => [false, true].map(machine => ({
    id: machine ? 'machines' : 'humans', label: machine ? 'Machine identities' : 'People',
    children: rows().filter(p => p.machine === machine).map(p => ({
      id: p.email, label: p.name, sub: p.owner ? 'Owner' : machine ? 'Service identity' : p.email,
    })),
  })));
  const close = () => go('/humans');
  const saved = (id?: string) => {
    roster.reload();
    if (!id) close();
    else go(humanPath(id));
  };
  return <Show when={me.data?.owner} fallback={
    <Show when={me.data || me.error} fallback={<Skeleton rows={4} />}>
      <Empty text="Only the Fleet owner can manage Humans." />
    </Show>
  }>
    <ShellActions>
      <Button icon="plus" iconOnly label="Add human" onClick={() => go('/humans/new')} />
    </ShellActions>
    <ListDetail
      detailKey={props.id}
      list={<>
        <Show when={roster.error}>{error => <ErrorBanner text={error()} onRetry={roster.reload} />}</Show>
        <Show when={roster.data} fallback={<Skeleton rows={5} />}>
          <CatalogueTree label="Humans and machine identities" fill nodes={nodes()} selectedId={props.id}
            onSelect={n => { if (!n.children) go(humanPath(n.id)); }} />
        </Show>
      </>}
      detail={props.id ? <section class="detail" data-ui="detail" tabindex={-1}>
        <header class="detail-h">
          <h2>{props.id === 'new' ? 'Add human' : selected()?.name ?? 'Human access'}</h2>
          <Button icon="x" iconOnly label="Close" onClick={close} />
        </header>
        <Show when={props.id === 'new'} fallback={
          <Show when={selected()} fallback={roster.data ? <Empty text="This identity is absent from the Fleet roster." /> : <Skeleton rows={4} />}>
            {person => <>
              <AccessFacts person={person()} projectTitle={title} />
              <Show when={person().owner || person().machine ? undefined : person().email} keyed>
                <HumanEditor person={person()} onSave={saved} />
              </Show>
            </>}
          </Show>
        }>
          <HumanEditor onSave={saved} />
        </Show>
      </section> : undefined}
      idle={<Empty text="Choose an identity to see its Fleet access." />}
    />
  </Show>;
}

function AccessFacts(props: { person: HumanAccessRow; projectTitle: (id: string) => string }) {
  const granted = () => props.person.grants.filter(g => g.kind === 'project');
  const all = () => props.person.owner || props.person.grants.some(g => g.kind === '*' || g.kind === 'project' && g.id === '*');
  return <DetailSection title="Effective access">
    <KvRow icon="shield" label="Identity" value={props.person.machine ? 'Cloudflare service identity' : props.person.email} />
    <KvRow icon="shield" label="Fleet admission" value="Listed in Fleet" />
    <Show when={!props.person.machine}>
      <KvRow icon="shield" label="Cloudflare admission" value="Unknown in Fleet" />
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

function HumanEditor(props: { person?: HumanAccessRow; onSave: (email?: string) => void }) {
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
      if (!alive.aborted) props.onSave(revoke || base ? undefined : reply.email);
    } catch (e) {
      if (!alive.aborted) setError(e instanceof Error ? e.message : String(e));
    } finally {
      if (!alive.aborted) setBusy(false);
    }
  }

  const changed = () => !!base && props.person?.version !== base.version;
  return <DetailSection title={base ? 'Edit Fleet access' : 'Sign-in and access'}>
    <Show when={!base}>
      <p>After adding the person in Fleet, admit the same sign-in email in the Cloudflare Access Fleet people policy.</p>
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
      <div class="actions">
        <Button kind="danger" label="Confirm revocation" busy={busy()}
          disabledReason={changed() ? 'Reopen this identity first' : undefined} onClick={() => void save(true)} />
        <Button label="Cancel" onClick={() => setConfirm(false)} />
      </div>
    </Show>
  </DetailSection>;
}
