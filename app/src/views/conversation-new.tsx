// Start a conversation (docs/designs/fleet-conversations.md "Start a conversation"; docs/design-system/pages/conversations.md):
// /conversations/new in the work area. A project (focused and recent first, ignored left out; "No project" is one more
// choice), a preset of what we are doing now (session_presets), the composer, and the facts the start will use.
// Starting calls session_start with the preset, the access and the branch shown, the same call agents make. Files added to
// the composer (picker, paste, drop over the page) upload into the chosen project and go with the first message.
import { useEffect, useRef, useState } from 'preact/hooks';
import {
  Composer, DropZone, DetailSection, ErrorBanner, ProjectPick, PresetPicker, Skeleton, agentLabel, harnesses,
  type Harness, type IconName, type PickProject, type PresetOption, type StartFact, type ComposerChip } from '../../../packages/ui/src/index.ts';
import { op, online, writeOnce, sendingId, prepareSending, submissionKey, authorizedDraftScope, createDraftPersistence, unknownOutcome, AccessChanged, OpError, useAlive, type Ops } from '../api.ts';
import { useDoc } from '../store.ts';
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

export function NewConversation({ initialProject, initialRole, onStarted }: { initialProject: string | null; initialRole?: string | null; onStarted: (sessionId: string) => void }) {
  const alive = useAlive();
  const projects = useDoc<any>('conversation-projects', () => op('project_list', {}), c => c.kind === 'definition');
  // Recent: the projects of the latest conversations, newest first (sessions_list, latest activity first).
  const recent = useDoc<Ops['sessions_list']['reply']>('conversation-recent-projects', () => op('sessions_list', { group: 'none', sort: '-activity', limit: 50 }), () => false); // an ordering hint, read once
  const [project, setProject] = useState<string | null>(initialProject);
  const key = project ?? '-';
  const presets = useDoc<Ops['session_presets']['reply']>('session-presets:' + key, () => op('session_presets', project ? { projectId: project } : {}), c => c.kind === 'definition');
  const [picked, setPicked] = useState<Record<string, string>>({});
  const [override, setOverride] = useState<{ access?: 'read' | 'write'; branch?: boolean }>({});
  const [error, setError] = useState('');
  // Files belong to the project they upload into: another project starts without them.
  const files = useAttachments({ projectId: project });
  // The flipped access and branch belong to one preset: any change of the effective preset resets them.
  const chosenId = (presets.data?.presets ?? []).find(p => p.id === picked[key] && !p.unavailable)?.id ?? (presets.data?.presets ?? []).find(p => !p.unavailable)?.id;
  useEffect(() => { setOverride({}); setError(''); }, [key, chosenId]);

  // Order (OR-34): focused projects, then recent ones, then the rest by name; ignored projects are left out unless named.
  const { focus, ignored } = prefs.value;
  const all: PickProject[] = (projects.data?.projects ?? []).map((p: any) => ({ id: String(p.id), name: String(p.name ?? p.id) }));
  const seen = (recent.data?.sessions ?? []).map(s => (s as { projectId?: string | null }).projectId).filter(Boolean) as string[];
  const rank = (id: string) => focus.includes(id) ? focus.indexOf(id) : seen.includes(id) ? 1000 + seen.indexOf(id) : 100000;
  const ordered = all.filter(p => !ignored.includes(p.id) || p.id === initialProject).sort((a, b) => rank(a.id) - rank(b.id) || a.name.localeCompare(b.name));

  // Without ?in= the page starts on the first focused or recent project, once the projects are known.
  const [touched, setTouched] = useState(initialProject !== null);
  // Files added meanwhile belong to the shown project: then the page stays on it.
  useEffect(() => {
    if (touched || !projects.data || !recent.data) return;
    if (files.items.length) { setTouched(true); return; } // the shown project is the person's from now on
    if (ordered.length && (focus.length || seen.length)) { setProject(ordered[0].id); setTouched(true); }
  }, [touched, projects.data, recent.data, files.items.length]);
  const list: Preset[] = presets.data?.presets ?? [];
  // The picked preset while it can start; else the first one for the asked role (?role=pm), else the first that can start.
  const chosen = list.find(p => p.id === picked[key] && !p.unavailable) ?? list.find(p => !p.unavailable && !!initialRole && p.role === initialRole) ?? list.find(p => !p.unavailable);
  // Write work always has its own branch (session_start refuses write without one); read may go without.
  const access = override.access ?? chosen?.access ?? 'write', branch = access === 'write' || (override.branch ?? chosen?.branch ?? true);
  const options: PresetOption[] = list.map(p => ({ id: p.id, name: p.name, description: p.description, icon: p.source === 'system' ? ICONS[p.id] : undefined, facts: factsOf(p, !!project), unavailable: p.unavailable ?? undefined }));
  const name = project ? all.find(p => p.id === project)?.name ?? project : 'No project';
  const facts: StartFact[] = chosen ? [
    { key: 'project', icon: 'folder-open', text: name, tip: project ? 'Project' : 'Outside projects' },
    { key: 'agent', harness: isHarness(chosen.harness) ? chosen.harness : undefined, icon: 'bot', text: agentLabel(chosen.model, chosen.effort) || chosen.profileId || '', tip: `Profile ${chosen.profileId}${chosen.role ? `, the project's ${chosen.role} role` : ', a system profile'}` },
    { key: 'access', icon: 'shield', text: access === 'read' ? 'Code read only' : 'Can edit', tip: access === 'read' ? 'The agent reads the code and runs read-only tools; Fleet operations such as tasks and pipelines stay allowed. Click to let it edit' : 'The agent may edit files; click for code read only', onClick: () => setOverride(o => ({ ...o, access: access === 'read' ? 'write' : 'read' })) },
    ...(project ? [{ key: 'branch', icon: 'git-pull-request' as IconName, text: branch ? `${presets.data?.branchPrefix ?? 'fleet'}/new` : 'No branch',
      tip: access === 'write' ? `A new branch from ${presets.data?.base ?? 'the default branch'}; edits always get their own branch` : branch ? `A new branch from ${presets.data?.base ?? 'the default branch'}; click for no branch` : `A detached copy of ${presets.data?.base ?? 'the default branch'}, no branch; click for a new branch`,
      onClick: access === 'write' ? undefined : () => setOverride(o => ({ ...o, branch: !branch })) }] : []),
  ] : [];

  // One draft text for the page, so switching project or preset keeps it; a submission's identity stays per project and
  // preset (api.ts), so a retry never sends an earlier target's body.
  const page = { kind: 'project' as const, id: 'new', roleId: 'new' }, target = { kind: 'project' as const, id: key, roleId: chosen?.id ?? '' };
  const reason = !online.value ? 'offline' : !chosen ? (presets.error ? 'Presets did not load' : presets.data ? 'No preset can start here' : 'Loading presets') : undefined;
  // The settings a start would use now (read after an await, so a change during the upload wait is seen).
  const now = { project, chosen: chosen?.id, access, branch };
  const live = useRef(now); live.current = now;
  const settings = () => JSON.stringify(live.current);
  const send = async (text: string) => {
    const scope = authorizedDraftScope(target), shown = settings();
    // Files still uploading finish first; a failed one fails the start and stays on its chip.
    let attachments: string[];
    try { attachments = await files.ready(); } catch (e: any) { setError(`Attachment not uploaded: ${e?.message ?? e}`); throw e; }
    // The start uses what the page shows: settings changed while the files uploaded (a project change also drops its files)
    // are not started with the old ones. A file removed meanwhile just drops out, as in a conversation.
    if (settings() !== shown) {
      const e = Error('The settings or files changed while the files uploaded; check them and send again.'); setError(e.message); throw e;
    }
    const body = { ...(project ? { projectId: project, branch } : {}), preset: chosen!.id, access, text, ...(attachments.length ? { attachments } : {}) };
    const sendKey = submissionKey(body);
    try {
      setError('');
      const request = await prepareSending(scope, 'session_start', body);
      // A retry of an unknown send keeps its original body (api.ts); never start it with other settings than shown.
      if (request.access !== access || (project && request.branch !== branch)) {
        throw Object.assign(Error('This text was sent with other settings and its outcome is unknown; change the text to start anew.'), { keep: true });
      }
      const r = await writeOnce('session_start', request, () => setError('unknown, retrying'), { signal: alive });
      sendingId(scope, null, sendKey);
      files.clear(attachments);
      onStarted(r.sessionId);
    } catch (e: any) {
      // A kept unknown send stays fenced: its identity is cleared only by a definite answer.
      if (!e?.keep && !(e instanceof AccessChanged) && !unknownOutcome(e) && !(e instanceof OpError && e.message === 'aborted')) sendingId(scope, null, sendKey);
      setError(e.message); throw e;
    }
  };
  return <article class="detail new-conversation" data-ui="detail" tabIndex={-1} aria-label="New conversation"><DropZone onFiles={files.add} disabledReason={reason}>
    <DetailSection title="Project">
      {projects.data ? <ProjectPick projects={ordered} value={project} onChange={p => { setTouched(true); setProject(p); }} /> : <Skeleton rows={1} />}
    </DetailSection>
    <DetailSection title="What are we doing">
      {presets.error ? <ErrorBanner text={`Presets did not load: ${presets.error}`} /> : presets.data ? <PresetPicker presets={options} value={chosen?.id ?? null} onChange={id => setPicked(p => ({ ...p, [key]: id }))} /> : <Skeleton rows={2} />}
    </DetailSection>
    {error && <ErrorBanner text={error} />}
    <Composer draftPersistence={createDraftPersistence(authorizedDraftScope(page))} placeholder={project ? `Describe the work in ${name}` : 'Describe the work'} disabledReason={reason} onSend={send}
      onAttach={files.add} attachments={files.items} onRemoveAttachment={files.remove} chips={facts.map(chipOf)} chipsLabel="Start settings" />
  </DropZone></article>;
}
