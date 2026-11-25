import { createMemo, createSignal, For, Show } from 'solid-js';
import {
  AgeChip, Button, Choice, DetailSection, Empty, ErrorBanner, KvRow, ShellActions, Skeleton, TextField,
} from '../../../packages/ui/src/index.ts';
import type { HumanAccessRow, TelegramPeopleReply, TelegramPersonSetRequest, PeopleCloudflareMode, PeopleCloudflareState, PeopleUpdateReply, PeopleUpdateRequest } from '../../../contracts/generated/ops.ts';
import { op, unknownOutcome, useAlive } from '../api.ts';
import { useLocation } from '../router.ts';
import { useOp } from '../store.ts';

const humanPath = (id: string) => '/humans/' + encodeURIComponent(id);
const cloudflareLabels: Record<PeopleCloudflareState['status'], string> = {
  manual: 'Managed in Cloudflare',
  unknown: 'Could not verify the email rule',
  present: 'Email rule present in fleet-people',
  missing: 'Email rule missing from fleet-people',
  removed: 'Email rule removed',
  removal_required: 'Email rule still present in fleet-people',
  pending: 'Email rule change awaiting confirmation',
};
const roles = [{ id: 'operator', label: 'Operator' }, { id: 'architect', label: 'Architect' }];

export function Humans(props: { id?: string }) {
  const [, go] = useLocation();
  const me = useOp('me', {});
  const roster = useOp('people_list', () => me.data?.owner ? { manage: true } : null);
  const projects = useOp('project_list', () => me.data?.owner ? {} : null);
  const telegram = useOp('telegram_people', () => me.data?.owner ? {} : null);
  const [message, setMessage] = createSignal('');
  const rows = () => roster.data?.roster ?? [];
  const removals = () => roster.data?.cloudflareRemovals ?? [];
  const mode = () => roster.data?.cloudflareMode;
  const canRead = () => roster.state === 'current' && !roster.error;
  const state = (observed: PeopleCloudflareState | undefined): PeopleCloudflareState | undefined =>
    canRead() ? observed : { status: 'unknown' };
  const title = (id: string) => String(projects.data?.projects.find(p => p.id === id)?.name ?? id);
  const selected = createMemo(() => rows().find(p => p.email === props.id));
  const removal = createMemo(() => removals().find(p => p.email === props.id));
  const close = () => go('/humans');
  const saved = (reply: PeopleUpdateReply, revoke: boolean, editing: boolean) => {
    roster.reload();
    if (revoke && (!reply.cloudflare || reply.cloudflare.status === 'manual')) {
      setMessage(`Fleet access revoked for ${reply.email}. Remove its email rule in Cloudflare Access.`);
    } else {
      setMessage('');
    }
    if (reply.cloudflare?.status === 'pending') go(humanPath(reply.email));
    else if (revoke || editing) close();
    else go(humanPath(reply.email));
  };
  const synced = (removed: boolean) => {
    roster.reload();
    if (removed) close();
  };
  const canSync = () => canRead() && mode() === 'active';
  return <Show when={me.data?.owner} fallback={
    <Show when={me.data || me.error} fallback={<Skeleton rows={4} />}>
      <Empty text="Only the Fleet owner manages people and access." />
    </Show>
  }>
    <ShellActions>
      <Show when={mode() === 'active' && !roster.data?.cloudflareError}>
        <Button label="Refresh Cloudflare access" onClick={roster.reload} />
      </Show>
      <Button icon="plus" iconOnly label="Invite person" onClick={() => { setMessage(''); go('/humans/new'); }} />
    </ShellActions>
    <Show when={props.id} fallback={<PeopleIdle rows={rows()} telegram={telegram.data ?? undefined} removals={removals().length} mode={mode()} message={message()} error={roster.error ?? roster.data?.cloudflareError} onRetry={roster.reload} onOpen={id => go(humanPath(id))} />}>
      <section class="detail" data-ui="detail" tabindex={-1}>
        <header class="detail-h">
          <Button icon="x" iconOnly label="Close" onClick={close} />
          <h2 class="detail-title">{props.id === 'new' ? 'Add person' : selected()?.name ?? removal()?.email ?? 'Access'}</h2>
        </header>
        <div role="status"><Show when={message()}>{notice => <p>{notice()}</p>}</Show></div>
        <Show when={props.id === 'new'} fallback={
          <Show when={selected()} fallback={
            <Show when={removal()} fallback={roster.data ? <Empty text="This identity is absent from the Fleet roster." /> : <Skeleton rows={4} />}>
              {entry => <DetailSection title="Effective access">
                <KvRow icon="shield" label="Fleet admission" value="Revoked in Fleet" />
                <CloudflareRule email={entry().email} state={state(entry().cloudflare)} canSync={canSync()}
                  onSync={synced} removing />
                <Show when={mode() === 'manual'}><p>Remove this email rule in Cloudflare Access.</p></Show>
              </DetailSection>}
            </Show>
          }>
            {person => <>
              <Show when={person().email} keyed>
                <AccessFacts person={person()} projectTitle={title} state={state(person().cloudflare)} canSync={canSync()} onSync={synced} />
              </Show>
              <Show when={person().machine ? undefined : person().email} keyed>
                {email => <TelegramLink email={email} people={telegram.data ?? undefined} error={telegram.error ?? undefined} onChange={telegram.reload} />}
              </Show>
              <Show when={person().owner || person().machine ? undefined : person().email} keyed>
                <HumanEditor person={person()} mode={mode()} onSave={saved} onRefresh={roster.reload} />
              </Show>
            </>}
          </Show>
        }>
          <HumanEditor mode={mode()} onSave={saved} onRefresh={roster.reload} />
        </Show>
      </section>
    </Show>
  </Show>;
}

/** People idle (System › People): who has access, in one line each; a row opens the person. */
function PeopleIdle(props: {
  rows: readonly HumanAccessRow[]; telegram?: TelegramPeopleReply; removals: number; mode?: PeopleCloudflareMode; message: string;
  error?: string; onRetry: () => void; onOpen: (id: string) => void;
}) {
  const humans = () => props.rows.filter(p => !p.machine);
  const machines = () => props.rows.filter(p => p.machine);
  const access = (p: HumanAccessRow) => p.owner ? 'Owner' : p.grants.some(g => g.kind === '*' || g.id === '*') ? 'All projects'
    : `${p.roles.join(', ')} · ${p.grants.filter(g => g.kind === 'project').length || 'no'} projects`;
  return <article class="detail" data-ui="detail" aria-label="People">
    <Show when={props.error}>{error => <ErrorBanner text={error()} onRetry={props.onRetry} />}</Show>
    <div role="status"><Show when={props.message}>{notice => <p>{notice()}</p>}</Show></div>
    <DetailSection title="People">
      <For each={humans()}>{p => <KvRow icon="shield" label={p.name} meta={[p.email, telegramName(props.telegram, p.email)].filter(Boolean).join(' · ')} value={<Button label={access(p)} kind="quiet" onClick={() => props.onOpen(p.email)} />} />}</For>
    </DetailSection>
    <Show when={machines().length > 0}>
      <DetailSection title="Machine identities">
        <For each={machines()}>{p => <KvRow icon="bot" label={p.name} value={<Button label={access(p)} kind="quiet" onClick={() => props.onOpen(p.email)} />} />}</For>
      </DetailSection>
    </Show>
    <Show when={props.removals > 0}><KvRow icon="circle-slash" label="Cloudflare removals" value={String(props.removals)} /></Show>
    <KvRow icon="shield" label="Cloudflare Access" value={props.mode === 'active' ? 'Synced by Fleet' : props.mode === 'manual' ? 'Managed in Cloudflare' : '—'} />
  </article>;
}

const languages = [{ id: 'en', label: 'English' }, { id: 'ru', label: 'Русский' }];
const telegramOf = (people: TelegramPeopleReply | undefined, email: string) => people?.people.find(p => p.email === email);
/** The person's Telegram as the People list shows it: @username, else the user id; empty when not linked. */
function telegramName(people: TelegramPeopleReply | undefined, email: string): string {
  const tp = telegramOf(people, email);
  return tp ? tp.username ? '@' + tp.username : 'Telegram ' + tp.telegramUserId : '';
}

/** Telegram on the person card (telegram_people, telegram_person_set): the link and its language, or a pick among
 *  the Telegram users who wrote to the bot without a link, or a typed user id. The bot confirms a link in that chat. */
function TelegramLink(props: { email: string; people?: TelegramPeopleReply; error?: string; onChange: () => void }) {
  const alive = useAlive();
  const linked = () => telegramOf(props.people, props.email);
  const [id, setId] = createSignal('');
  const [language, setLanguage] = createSignal('en');
  const [busy, setBusy] = createSignal('');
  const [error, setError] = createSignal('');
  async function set(key: string, body: Omit<TelegramPersonSetRequest, 'email'>) {
    if (busy()) return;
    setBusy(key);
    setError('');
    try {
      await op('telegram_person_set', { email: props.email, ...body }, { signal: alive });
      if (alive.aborted) return;
      setId('');
      props.onChange();
    } catch (e) {
      if (alive.aborted) return;
      if (unknownOutcome(e)) props.onChange();
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      if (!alive.aborted) setBusy('');
    }
  }
  const typed = () => /^[1-9][0-9]{0,15}$/.test(id().trim()) ? Number(id().trim()) : 0;
  return <DetailSection title="Telegram">
    <Show when={props.error}>{message => <ErrorBanner text={message()} onRetry={props.onChange} />}</Show>
    <Show when={props.people || props.error} fallback={<Skeleton rows={2} />}>
      <Show when={linked()} fallback={<>
        <KvRow icon="link" label="Telegram" value="Not linked" />
        <Show when={(props.people?.unknown.length ?? 0) > 0}>
          <p class="muted">Wrote to the bot</p>
          <For each={props.people?.unknown}>{u => <KvRow icon="messages-square" label={u.name || 'Telegram user'}
            meta={[u.username && '@' + u.username, String(u.telegramUserId), `${u.messages} ${u.messages === 1 ? 'message' : 'messages'}`].filter(Boolean).join(' · ')}
            value={<>
              <AgeChip t={u.lastAt} />
              <Button label="Link" kind="quiet" busy={busy() === 'u' + u.telegramUserId}
                onClick={() => void set('u' + u.telegramUserId, { telegramUserId: u.telegramUserId, username: u.username, language: language() })} />
            </>} />}</For>
        </Show>
        <p class="muted">Only people who wrote to the bot are listed. The bot confirms the link in their chat.</p>
        <TextField label="Telegram user id" type="number" value={id()} onChange={setId} />
        <Choice label="Language" options={languages} value={language()} onChange={setLanguage} />
        <div class="actions">
          <Button kind="primary" label="Link" busy={busy() === 'id'} disabledReason={typed() ? undefined : 'Enter a Telegram user id'}
            onClick={() => void set('id', { telegramUserId: typed(), language: language() })} />
        </div>
      </>}>
        {tp => <>
          <KvRow icon="link" label="Telegram" value="Linked" />
          <KvRow icon="messages-square" label="Telegram user" meta={tp().username ? '@' + tp().username : undefined} value={String(tp().telegramUserId)} />
          <KvRow icon="messages-square" label="Language" value={<Choice label="Language" options={languages} value={tp().language || 'en'}
            onChange={lang => { if (lang !== tp().language) void set('lang', { language: lang }); }} />} />
          <KvRow icon="link" label="Linked" value={<AgeChip t={tp().linkedAt} />} />
          <div class="actions">
            <Button kind="danger" label="Unlink" busy={busy() === 'unlink'} onClick={() => void set('unlink', { unlink: true })} />
          </div>
        </>}
      </Show>
    </Show>
    <Show when={error()}>{message => <ErrorBanner text={message()} />}</Show>
  </DetailSection>;
}

function AccessFacts(props: {
  person: HumanAccessRow; projectTitle: (id: string) => string; state?: PeopleCloudflareState;
  canSync: boolean; onSync: (removed: boolean) => void;
}) {
  const granted = () => props.person.grants.filter(g => g.kind === 'project');
  const all = () => props.person.owner || props.person.grants.some(g => g.kind === '*' || g.kind === 'project' && g.id === '*');
  return <DetailSection title="Effective access">
    <KvRow icon="shield" label="Identity" value={props.person.machine ? 'Cloudflare service identity' : props.person.email} />
    <KvRow icon="shield" label="Fleet admission" value="Listed in Fleet" />
    <Show when={!props.person.machine}>
      <CloudflareRule email={props.person.email} state={props.state} canSync={props.canSync && !props.person.owner} onSync={props.onSync} />
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

function CloudflareRule(props: {
  email: string; state?: PeopleCloudflareState; canSync: boolean; removing?: boolean;
  onSync: (removed: boolean) => void;
}) {
  const alive = useAlive();
  const [busy, setBusy] = createSignal(false);
  const [error, setError] = createSignal('');
  async function sync() {
    if (busy()) return;
    setBusy(true);
    setError('');
    try {
      const result = await op('people_access_sync', { email: props.email }, { signal: alive });
      if (alive.aborted) return;
      if (result.cloudflare.status === 'pending') setError(result.cloudflare.message ?? 'Cloudflare sync is still pending.');
      props.onSync(result.cloudflare.status === 'removed');
    } catch (e) {
      if (!alive.aborted) setError(e instanceof Error ? e.message : String(e));
    } finally {
      if (!alive.aborted) setBusy(false);
    }
  }
  const status = () => props.state?.status ?? 'unknown';
  return <>
    <KvRow icon="shield" label="Cloudflare admission" value={cloudflareLabels[status()]} />
    <Show when={status() !== 'present' && (error() || props.state?.message)}>
      {message => <ErrorBanner text={message()} />}
    </Show>
    <Show when={props.canSync && status() !== 'present'}>
      <Button label={props.removing ? 'Retry Cloudflare removal' : 'Sync Cloudflare access'} busy={busy()} onClick={() => void sync()} />
    </Show>
  </>;
}

function HumanEditor(props: {
  person?: HumanAccessRow; mode?: PeopleCloudflareMode;
  onSave: (reply: PeopleUpdateReply, revoke: boolean, editing: boolean) => void;
  onRefresh: () => void;
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
      if (!alive.aborted) props.onSave(reply, revoke, !!base);
    } catch (e) {
      if (!alive.aborted) {
        if (unknownOutcome(e)) {
          props.onRefresh();
          setError('Fleet could not confirm the save. Check the refreshed roster before retrying.');
        } else {
          setError(e instanceof Error ? e.message : String(e));
        }
      }
    } finally {
      if (!alive.aborted) setBusy(false);
    }
  }

  const changed = () => !!base && props.person?.version !== base.version;
  return <DetailSection title={base ? 'Edit Fleet access' : 'Sign-in and access'}>
    <Show when={!base}>
      <p>{props.mode === 'active' ? 'Fleet also syncs the sign-in email to Cloudflare Access.' :
        props.mode === 'error' ? 'Fleet can save access. Cloudflare sync needs working coordinator settings.' :
        'After adding the person in Fleet, add the same email to the fleet-people group in Cloudflare Access.'}</p>
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
      <p>{props.mode === 'active' ? 'Fleet will also remove the direct email rule in Cloudflare Access.' :
        props.mode === 'error' ? 'Cloudflare removal will need a retry after the coordinator settings are fixed.' :
        'Remove the email rule in Cloudflare Access after revoking Fleet access.'}</p>
      <div class="actions">
        <Button kind="danger" label="Confirm revocation" busy={busy()}
          disabledReason={changed() ? 'Reopen this identity first' : undefined} onClick={() => void save(true)} />
        <Button label="Cancel" onClick={() => setConfirm(false)} />
      </div>
    </Show>
  </DetailSection>;
}
