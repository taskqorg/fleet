// Start a conversation (docs/designs/fleet-conversations.md "Start a conversation"; docs/design-system/pages/conversations.md):
// /conversations/new in the work area. A project (focused and recent first, ignored left out; "No project" is one more
// choice), a preset of what we are doing now (session_presets), the composer, and the facts the start will use.
// Starting calls session_start with the preset, the access and the branch shown, the same call agents make. Files added to
// the composer (picker, paste, drop over the page) upload into the chosen project and go with the first message.
import { Show, createEffect, createMemo, createSignal, untrack } from 'solid-js';
import {
  Composer, DropZone, DetailSection, ErrorBanner, ProjectPick, PresetPicker, Skeleton, Button, agentLabel, harnesses,
  type Harness, type IconName, type PickProject, type PresetOption, type StartFact, type ComposerChip,
} from '../../../packages/ui/src/index.ts';
import { online, writeOnce, sendingId, prepareSending, submissionKey, authorizedDraftScope, createDraftPersistence, unknownOutcome, AccessChanged, OpError, useAlive, type Ops } from '../api.ts';
import { useOp } from '../store.ts';
import { prefs } from '../prefs.ts';
import { useAttachments } from '../uploads.ts';

type Preset = Ops['session_presets']['reply']['presets'][number];
// The system presets' icons (OR-1); a project's own presets show their name only.
const ICONS: Record<string, IconName> = { discuss: 'messages-square', design: 'panels-top-left', fix: 'terminal', review: 'check-check', investigate: 'search' };
const isHarness = (h: string | null | undefined): h is Harness => !!h && h in harnesses;

/** What a preset sets, in words: agent, access, branch, starter (never a raw id alone). */
function factsOf(p: Preset, inProject: boolean): string[] {
  const agent = [isHarness(p.harness) ? harnesses[p.harness].label : p.harness, agentLabel(p.model, p.effort)].filter(Boolean).join(' ');
  return [agent || p.profileId || '', p.access === 'read' ? 'Code read only' : 'Can edit', inProject ? (p.branch ? 'New branch' : 'No branch') : '', p.starter ? 'Starter instruction' : ''].filter(Boolean);
}

/** A start fact as a chip in the composer's bottom row, where a conversation shows its profile and permissions. */
const chipOf = (f: StartFact): ComposerChip => ({ key: f.key, icon: f.icon, harness: f.harness, label: f.text, tip: f.tip, onClick: f.onClick });

export function NewConversation(props: { initialProject: string | null; initialRole?: string | null; onClose: () => void; onStarted: (sessionId: string) => void }) {
  const alive = useAlive();
  const projects = useOp('project_list', {});
  // Recent: the projects of the latest conversations, newest first (sessions_list, latest activity first).
  const recent = useOp('sessions_list', { group: 'none', sort: '-activity', limit: 50 }); // an ordering hint: sessions_list declares no changes, so it is read once
  const [project, setProject] = createSignal<string | null>(untrack(() => props.initialProject), { ownedWrite: true });
  const key = () => project() ?? '-';
  const presets = useOp('session_presets', () => {
    const p = project();
    return p ? { projectId: p } : {};
  });
  const [picked, setPicked] = createSignal<Record<string, string>>({});
  const [override, setOverride] = createSignal<{ access?: 'read' | 'write'; branch?: boolean }>({}, { ownedWrite: true });
  const [error, setError] = createSignal('', { ownedWrite: true });
  // Files belong to the project they upload into: another project starts without them.
  const files = useAttachments(() => ({ projectId: project() }));
  // The flipped access and branch belong to one preset: any change of the effective preset resets them.
  const chosenId = createMemo(() => {
    const xs = presets.data?.presets ?? [];
    const pk = picked()[key()];
    return xs.find(p => p.id === pk && !p.unavailable)?.id ?? xs.find(p => !p.unavailable)?.id;
  });
  createEffect(
    () => [key(), chosenId()] as const,
    () => { setOverride({}); setError(''); },
    { defer: true },
  );

  // Order (OR-34): focused projects, then recent ones, then the rest by name; ignored projects are left out unless named.
  const all = createMemo<PickProject[]>(() => (projects.data?.projects ?? []).map(p => ({ id: String(p.id), name: String(p.name ?? p.id) })));
  const seen = createMemo(() => (recent.data?.sessions ?? []).map(s => s.projectId).filter((p): p is string => !!p));
  const ordered = createMemo(() => {
    const { focus, ignored } = prefs.value;
    const sn = seen(), initP = props.initialProject;
    const rank = (id: string) => (focus.includes(id) ? focus.indexOf(id) : sn.includes(id) ? 1000 + sn.indexOf(id) : 100000);
    return all().filter(p => !ignored.includes(p.id) || p.id === initP).sort((a, b) => rank(a.id) - rank(b.id) || a.name.localeCompare(b.name));
  });

  // Without ?in= the page starts on the first focused or recent project, once the projects are known.
  const [touched, setTouched] = createSignal(untrack(() => props.initialProject !== null), { ownedWrite: true });
  // Files added meanwhile belong to the shown project: then the page stays on it.
  createEffect(
    () => [touched(), projects.data, recent.data, files.items.length] as const,
    ([t, pData, rData, fLen]) => {
      if (t || !pData || !rData) return;
      if (fLen) { setTouched(true); return; } // the shown project is the person's from now on
      const { focus } = prefs.value;
      const ord = untrack(ordered), sn = untrack(seen);
      if (ord.length && (focus.length || sn.length)) { setProject(ord[0].id); setTouched(true); }
    },
  );

  const list = () => presets.data?.presets ?? [];
  // The picked preset while it can start; else the first one for the asked role (?role=pm), else the first that can start.
  const chosen = createMemo(() => {
    const xs = list(), pk = picked()[key()], role = props.initialRole;
    return xs.find(p => p.id === pk && !p.unavailable) ?? xs.find(p => !p.unavailable && !!role && p.role === role) ?? xs.find(p => !p.unavailable);
  });
  // Write work always has its own branch (session_start refuses write without one); read may go without.
  const access = () => override().access ?? chosen()?.access ?? 'write';
  const branch = () => access() === 'write' || (override().branch ?? chosen()?.branch ?? true);
  const options = createMemo<PresetOption[]>(() => {
    const inP = !!project();
    return list().map(p => ({ id: p.id, name: p.name, description: p.description, icon: p.source === 'system' ? ICONS[p.id] : undefined, facts: factsOf(p, inP), unavailable: p.unavailable ?? undefined }));
  });
  const name = createMemo(() => {
    const p = project();
    return p ? all().find(x => x.id === p)?.name ?? p : 'No project';
  });
  const facts = createMemo<StartFact[]>(() => {
    const ch = chosen();
    if (!ch) return [];
    const p = project(), acc = access(), br = branch(), prData = presets.data;
    return [
      { key: 'project', icon: 'folder-open', text: name(), tip: p ? 'Project' : 'Outside projects' },
      { key: 'agent', harness: isHarness(ch.harness) ? ch.harness : undefined, icon: 'bot', text: agentLabel(ch.model, ch.effort) || ch.profileId || '', tip: `Profile ${ch.profileId}${ch.role ? `, the project's ${ch.role} role` : ', a system profile'}` },
      { key: 'access', icon: 'shield', text: acc === 'read' ? 'Code read only' : 'Can edit', tip: acc === 'read' ? 'The agent reads the code and runs read-only tools; Fleet operations such as tasks and pipelines stay allowed. Click to let it edit' : 'The agent may edit files; click for code read only', onClick: () => setOverride(o => ({ ...o, access: acc === 'read' ? 'write' : 'read' })) },
      ...(p ? [{
        key: 'branch',
        icon: 'git-pull-request' as IconName,
        text: br ? `${prData?.branchPrefix ?? 'fleet'}/new` : 'No branch',
        tip: acc === 'write' ? `A new branch from ${prData?.base ?? 'the default branch'}; edits always get their own branch` : br ? `A new branch from ${prData?.base ?? 'the default branch'}; click for no branch` : `A detached copy of ${prData?.base ?? 'the default branch'}, no branch; click for a new branch`,
        onClick: acc === 'write' ? undefined : () => setOverride(o => ({ ...o, branch: !br })),
      }] : []),
    ];
  });

  // One draft text for the page, so switching project or preset keeps it; a submission's identity stays per project and
  // preset (api.ts), so a retry never sends an earlier target's body.
  const page = { kind: 'project' as const, id: 'new', roleId: 'new' };
  const reason = () => (!online.value ? 'offline' : !chosen() ? (presets.error ? 'Presets did not load' : presets.data ? 'No preset can start here' : 'Loading presets') : undefined);
  // The settings a start would use now (read after an await, so a change during the upload wait is seen).
  const settings = () => untrack(() => JSON.stringify({ project: project(), chosen: chosen()?.id, access: access(), branch: branch() }));
  const send = async (text: string) => {
    const p = untrack(project), ch = untrack(chosen), acc = untrack(access), br = untrack(branch), k = untrack(key);
    const scope = authorizedDraftScope({ kind: 'project' as const, id: k, roleId: ch?.id ?? '' }), shown = settings();
    // Files still uploading finish first; a failed one fails the start and stays on its chip.
    let attachments: string[];
    try { attachments = await files.ready(); } catch (e) { setError(`Attachment not uploaded: ${e instanceof Error ? e.message : String(e)}`); throw e; }
    // The start uses what the page shows: settings changed while the files uploaded (a project change also drops its files)
    // are not started with the old ones. A file removed meanwhile just drops out, as in a conversation.
    if (settings() !== shown) {
      const e = Error('The settings or files changed while the files uploaded; check them and send again.'); setError(e.message); throw e;
    }
    const body = { ...(p ? { projectId: p, branch: br } : {}), preset: ch!.id, access: acc, text, ...(attachments.length ? { attachments } : {}) };
    const sendKey = submissionKey(body);
    try {
      setError('');
      const request = await prepareSending(scope, 'session_start', body);
      // A retry of an unknown send keeps its original body (api.ts); never start it with other settings than shown.
      if (request.access !== acc || (p && request.branch !== br)) {
        throw Object.assign(Error('This text was sent with other settings and its outcome is unknown; change the text to start anew.'), { keep: true });
      }
      const r = await writeOnce('session_start', request, () => setError('unknown, retrying'), { signal: alive });
      sendingId(scope, null, sendKey);
      files.clear(attachments);
      props.onStarted(r.sessionId);
    } catch (e) {
      // A kept unknown send stays fenced: its identity is cleared only by a definite answer.
      if (!(e instanceof Error && 'keep' in e && e.keep) && !(e instanceof AccessChanged) && !unknownOutcome(e) && !(e instanceof OpError && e.message === 'aborted')) sendingId(scope, null, sendKey);
      setError(e instanceof Error ? e.message : String(e)); throw e;
    }
  };
  const draftPersistence = createMemo(() => createDraftPersistence(authorizedDraftScope(page)));

  return (
    <article class="detail new-conversation" data-ui="detail" tabindex={-1} aria-label="New conversation">
      <DropZone onFiles={files.add} disabledReason={reason()}>
        <header class="detail-h">
          <Button label="Close" icon="x" iconOnly onClick={props.onClose} />
          <h2 class="detail-title">New conversation</h2>
        </header>
        <DetailSection title="Project">
          <Show when={projects.data} fallback={<Skeleton rows={1} />}>
            <ProjectPick projects={ordered()} value={project()} onChange={p => { setTouched(true); setProject(p); }} />
          </Show>
        </DetailSection>
        <DetailSection title="What are we doing">
          <Show
            when={!presets.error}
            fallback={<ErrorBanner text={`Presets did not load: ${presets.error}`} />}
          >
            <Show when={presets.data} fallback={<Skeleton rows={2} />}>
              <PresetPicker presets={options()} value={chosen()?.id ?? null} onChange={id => setPicked(p => ({ ...p, [key()]: id }))} />
            </Show>
          </Show>
        </DetailSection>
        <Show when={error()}>{err => <ErrorBanner text={err()} />}</Show>
        <Composer
          draftPersistence={draftPersistence()}
          placeholder={project() ? `Describe the work in ${name()}` : 'Describe the work'}
          disabledReason={reason()}
          onSend={send}
          onAttach={files.add}
          attachments={files.items}
          onRemoveAttachment={files.remove}
          chips={facts().map(chipOf)}
          chipsLabel="Start settings"
        />
      </DropZone>
    </article>
  );
}
