// Agents, Pipelines and Catalogue (docs/designs/fleet-definition-editors.md, patterns/definition-editor.md): definitions in
// the coordinator's store. The control column lists one kind; the work area edits one as a draft and saves revisions.
import { useEffect, useMemo, useRef, useState } from 'preact/hooks';
import { useLocation } from 'wouter-preact';
import {
  ListDetail, DefinitionEditor, DraftBar, DetailSection, Disclosure, EntityPicker, KvRow, StateBadge, Empty,
  SettingField, CodeEditor, DiffText, StepList, AgeChip, Choice, Button, DataTable, Totals, CostFigures, Num, Icon,
  msMeasured, matchSearch, SearchField,
  type CatalogueTreeNode, type CostFigureLike, type IconName, type StepItem, type TotalItem,
} from '../../../packages/ui/src/index.ts';
import { op, writeOnce, useAlive, type Ops } from '../api.ts';
import { usePins } from '../prefs.ts';
import { useOp } from '../store.ts';
import { resourceTotals } from './resources.tsx';
import { groupUseCases, isArchivedUseCase, type RegistryUseCase } from './use-cases.ts';
import type { AntigravitySettings, Profile, ProfileMcpGrant } from '../../../contracts/generated/profile.ts';

type Kind = 'agent' | 'pipeline' | 'catalogue';
type Get = Ops['definition_get']['reply'];
type Draft = { base: number; text: string; changes: number; stale: boolean; problems: { path: string; message: string }[] };
const SECTION: Record<Kind, { title: string; route: string }> = {
  agent: { title: 'Agents', route: '/agents' }, pipeline: { title: 'Pipelines', route: '/pipelines' }, catalogue: { title: 'Catalogue', route: '/catalogue' },
};
type Content = Ops['definition_save']['request']['content'];
/** The typed content of a definition's text: JSON.parse checks the syntax, the server the shape (draft problems). */
const contentOf = (kind: Kind, text: string) => ({ [kind]: JSON.parse(text) }) as Content;
const errorText = (e: unknown) => e instanceof Error ? e.message : String(e);
/** The project ids of project_list, for the project choices. */
const projectIds = (r: Ops['project_list']['reply'] | null) => (r?.projects ?? []).map(p => String(p.id));

export function Definitions({ kind, id }: { kind: Kind; id?: string }) {
  const [, go] = useLocation();
  const { title, route } = SECTION[kind];
  const { data } = useOp('definition_list', { kind });
  const items = data?.items ?? [];
  const list = <>
    <header class="page-h"><span class="muted mono" aria-label={`${items.length} ${title.toLowerCase()}`}>{items.length}</span></header>
    <section class="panel">
      <div class="vl-wrap" style={{ '--cols': '1.25rem minmax(0,1fr) auto' }}>
        {data && items.length === 0 && <Empty text={`No ${title.toLowerCase()} yet`} />}
        {!data && <Empty text="Loading…" />}
        {items.toSorted((a, b) => Number(!!b.draft) - Number(!!a.draft)).map(x =>
          <div key={x.id} role="row" class={`vl-row static${x.id === id ? ' sel' : ''}`} onClick={() => go(`${route}/${x.id}`)}>
            <span class="lead"><StateBadge family="definition" state={x.draft ? 'draft' : 'ready'} compact /></span>
            <span class="tt"><a class="title row-link" href={`${route}/${x.id}`} onClick={e => { e.preventDefault(); go(`${route}/${x.id}`); }}>{x.name}</a>
              {x.name !== x.id && <span class="id">{x.id}</span>}{x.agent && <span class="muted">{x.agent.harness} · {x.agent.model}</span>}
              {kind !== 'catalogue' && x.last30?.runs > 0 && <>
                <span class="hc" data-tip="Runs in last 30 days"><Icon name="activity" label="Runs" /><Num m={{ value: x.last30.runs, basis: 'measured' }} kind="count" /></span>
                {x.last30.cost?.length ? <CostFigures cost={x.last30.cost} /> : x.last30.costUsd != null && <Num m={{ value: x.last30.costUsd, currency: 'USD', basis: costBasisOf(x.last30.costBasis), ...(x.last30.costBasis === 'tokens' ? { reason: 'tokens' } : {}) }} kind="money" />}
              </>}</span>
            <span class="r mono" data-tip={x.state}>r{x.revision}{x.state !== 'synced' ? ` · ${x.state}` : ''}</span>
          </div>)}
      </div>
    </section>
  </>;
  return <ListDetail list={list} detailKey={id} detail={id && <DefinitionDetail key={`${kind}/${id}`} kind={kind} id={id} onClose={() => go(route)} />} />;
}

export function DefinitionDetail({ kind, id, onClose }: { kind: Kind; id: string; onClose: () => void }) {
  const [tab, setTab] = useState(kind === 'catalogue' ? 'Methods' : 'Configuration');
  const got = useOp('definition_get', { kind, id });
  const drafted = useOp('definition_draft_get', got.data?.draft ? { kind, id } : null);
  const def: Get | null = got.data;
  const [draft, setDraft] = useState<Draft | null>(null);
  const [text, setText] = useState('');
  const [message, setMessage] = useState('');
  const [failure, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const timer = useRef(0);
  const pending = useRef<(() => Promise<void>) | null>(null);
  const seeded = useRef('');
  useEffect(() => () => clearTimeout(timer.current), []);
  // The editor's text follows the server while nothing typed waits: the draft's bytes when it has one (not a re-encoding
  // of its typed content), else the saved revision's; a new source (saved, discarded, a draft begun) replaces it.
  useEffect(() => {
    const g = got.data; if (!g || pending.current) return;
    const d = g.draft && drafted.status !== 404 ? drafted.data : null;
    if (g.draft && !d && drafted.status !== 404) return; // its draft is still being read
    setDraft(d ? { base: d.draft.baseRevision, text: d.text, changes: d.draft.changes, stale: d.draft.stale, problems: d.problems ?? [] } : null);
    const source = d ? 'draft:' + d.draft.baseRevision : 'revision:' + g.head;
    if (seeded.current !== source) { seeded.current = source; setText(d ? d.text : g.text); }
  }, [got.data, drafted.data, drafted.status]);
  const load = () => { got.reload(); drafted.reload(); };
  const error = failure ?? got.error ?? (drafted.status === 404 ? null : drafted.error);
  // Typing keeps a draft: written after a pause, so the next execution still uses the saved revision.
  const flush = async () => { clearTimeout(timer.current); const f = pending.current; pending.current = null; if (f) await f(); };
  const edit = (t: string) => {
    setText(t);
    clearTimeout(timer.current);
    pending.current = async () => {
      if (!def) return;
      if (t === def.text) { await op('definition_draft_discard', { kind, id }).catch(() => {}); setDraft(null); return; }
      let content;
      try { content = contentOf(kind, t); } catch (e) { setDraft(d => ({ base: d?.base ?? def.head, text: t, changes: d?.changes ?? 0, stale: false, problems: [{ path: '', message: 'JSON: ' + errorText(e) }] })); return; }
      try {
        const r = await op('definition_draft_put', { kind, id, baseRevision: draft?.base ?? def.head, content });
        seeded.current = 'draft:' + r.draft.baseRevision; // the text on screen is this draft
        setDraft({ base: r.draft.baseRevision, text: t, changes: r.draft.changes, stale: r.draft.stale, problems: r.problems });
      } catch (e) { setError(errorText(e)); }
    };
    timer.current = setTimeout(flush, 700) as unknown as number;
  };
  const run = async (f: () => Promise<unknown>) => { setBusy(true); try { await f(); setError(null); } catch (e) { setError(errorText(e)); } finally { setBusy(false); } };
  // Save from the draft's own base: the server refuses it (409) when someone else saved meanwhile or the repository file
  // changed, and the error says what to look at.
  const save = () => run(async () => {
    await flush();
    if (!def || !draft) return;
    await op('definition_save', { kind, id, baseRevision: draft.base, content: contentOf(kind, text), message: message.trim() || 'Edited in Fleet' });
    setMessage(''); load();
  });
  const discard = () => run(async () => { clearTimeout(timer.current); pending.current = null; await op('definition_draft_discard', { kind, id }); load(); });
  const alive = useAlive();
  const runDraft = () => run(async () => { await flush(); const requestId = crypto.randomUUID(); await writeOnce('pipeline_run_create', { pipelineId: id, requestId, draft: true }, () => setError('unknown, retrying'), { signal: alive }); });
  if (!def) return <article class="detail" data-ui="detail" tabIndex={-1}>{error ? <div class="error" role="alert">{error}</div> : <Empty text="Loading…" />}</article>;
  const tabs = kind === 'agent' ? ['Configuration', 'Effective', 'Diff', 'Compatibility', 'Usage', 'History'] : kind === 'catalogue' ? ['Methods', 'Configuration', 'Diff', 'History'] : ['Configuration', 'Usage', 'Diff', 'History'];
  return <DefinitionEditor label={id} crumb={`shared · ${def.path} · r${def.head}${def.state !== 'synced' ? ` · ${def.state}` : ''}`} title={def.id}
    tabs={tabs} tab={tab} onTab={setTab} onClose={onClose}>
    {error && <div class="error" role="alert">{error}</div>}
    {draft && <DraftBar changes={draft.changes} base={`r${draft.base}`} note={draft.stale ? `The saved revision moved to r${def.head}: look at the diff before saving.` : 'New executions keep using the saved revision.'}
      actions={[
        ...(kind === 'pipeline' ? [{ label: 'Run', icon: 'play' as const, onClick: busy || draft.problems.length ? undefined : runDraft }] : []),
        { label: 'Diff', onClick: () => setTab('Diff') },
        { label: 'Discard', icon: 'x' as const, onClick: discard },
        { label: 'Save as commit', icon: 'save' as const, primary: true, onClick: busy || draft.problems.length ? undefined : save },
      ]} />}
    {tab === 'Configuration' && <>
      {draft && <SettingField label="Message" value={message} help="Says what the revision changes; shown in History." now={Date.now()} status={{ kind: 'pending' }} onSave={setMessage} />}
      {kind === 'pipeline' && <Steps text={text} />}
      {kind === 'agent' && <AgentFacts text={text} def={def} />}
      <DetailSection title="Definition">
        <CodeEditor label={def.path} value={text} onChange={edit} problems={draft?.problems ?? []} />
      </DetailSection>
    </>}
    {tab === 'Methods' && kind === 'catalogue' && <CatalogueMethods />}
    {tab === 'Diff' && <DiffTab kind={kind} id={id} hasDraft={!!draft} head={def.head} />}
    {tab === 'History' && <HistoryTab kind={kind} id={id} />}
    {tab === 'Usage' && (kind === 'agent' || kind === 'pipeline') && <UsageTab kind={kind} id={id} />}
    {tab === 'Effective' && <EffectiveTab id={id} draft={!!draft} version={draft?.text ?? ''} />}
    {tab === 'Compatibility' && <CompatibilityTab id={id} />}
  </DefinitionEditor>;
}

// reads: what a step's arguments or prompt reference (${example.x}, ${steps.<id>.output…}).
type StepJson = { id?: string; kind?: string; profile?: string; tool?: string; command?: string; when?: { source?: { step?: string; path?: string[] }; equals?: unknown } };
const readsOf = (s: unknown) => {
  const refs = [...(JSON.stringify(s) ?? '').matchAll(/\$\{(example|steps\.[\w-]+)/g)].map(m => m[1].replace('steps.', ''));
  return [...new Set(refs)].join(', ') || 'nothing';
};

function Steps({ text }: { text: string }) {
  let parsed: unknown, resultStep: string | undefined;
  try { const definition: { steps?: unknown; resultStep?: string } | null = JSON.parse(text); parsed = definition?.steps; resultStep = definition?.resultStep; } catch { return null; }
  if (!Array.isArray(parsed)) return null;
  const steps = (parsed as unknown[]).filter((s): s is StepJson => !!s && typeof s === 'object');
  const items: StepItem[] = steps.map(s => ({ id: s.id ?? '', kind: s.kind === 'api' ? 'tool' : s.kind === 'agent' ? 'agent' : 'program', executor: s.kind === 'api' || s.kind === 'agent' || s.kind === 'script' ? s.profile ?? s.tool ?? s.command ?? '' : `${s.kind}: no longer run`, reads: readsOf(s), writes: 'output', result: s.id === resultStep, condition: s.when ? `${s.when.source?.step}.${(s.when.source?.path ?? []).join('.')} = ${JSON.stringify(s.when.equals)}` : undefined }));
  return <DetailSection title="Steps"><StepList items={items} label="Steps" /></DetailSection>;
}

function AgentFacts({ text, def }: { text: string; def: Get }) {
  const [, go] = useLocation();
  let a: Partial<Profile> | null = null;
  try {
    const parsed: unknown = JSON.parse(text);
    if (parsed && typeof parsed === 'object' && !Array.isArray(parsed)) a = parsed as Partial<Profile>;
  } catch { /* keep null while JSON is invalid in draft */ }

  const variantOf = a && typeof a.variantOf === 'string' && a.variantOf && a.variantOf !== def.id ? a.variantOf : '';
  const baseOp = useOp('definition_get', variantOf ? { kind: 'agent', id: variantOf } : null);
  const listOp = useOp('definition_list', { kind: 'agent' });

  if (!a) return null;

  const baseProfile: Profile | null = variantOf && baseOp.data?.content?.agent ? baseOp.data.content.agent : null;
  const listItem = listOp.data?.items.find(x => x.id === def.id);
  const roles = listItem?.agent?.roles ?? [];

  const curHarness = a.harness ?? 'codex';
  const baseHarness = baseProfile ? (baseProfile.harness ?? 'codex') : 'codex';
  const defaultEffort = (h: string) => (h === 'codex' ? 'medium' : 'none');
  const defaultRole = (h: string) => (h === 'claude' ? 'system' : h === 'antigravity' || h === 'acp' ? 'user' : 'developer');

  const normList = (xs: unknown): string[] =>
    Array.isArray(xs) ? xs.filter((x): x is string => typeof x === 'string') : [];
  const sameList = (x: readonly string[], y: readonly string[]) =>
    x.length === y.length && x.every((v, i) => v === y[i]);
  const fmtList = (xs: readonly string[], empty = 'none') => (xs.length ? xs.join(', ') : empty);

  const fmtMcp = (xs: unknown): { text: string; key: string } => {
    const arr = Array.isArray(xs) ? (xs as ProfileMcpGrant[]) : [];
    return {
      text: arr.length ? arr.map(m => m.id).join(', ') : 'none',
      key: JSON.stringify(arr),
    };
  };

  const fmtSettings = (s: AntigravitySettings | undefined): { text: string; key: string } => {
    if (!s || typeof s !== 'object') return { text: 'default', key: '' };
    const parts: string[] = [];
    if (s.mode !== undefined) parts.push(`mode: ${s.mode}`);
    if (s.sandbox !== undefined) parts.push(`sandbox: ${s.sandbox}`);
    if (s.includeDefaultCustomizations !== undefined) parts.push(`includeDefaultCustomizations: ${s.includeDefaultCustomizations}`);
    return {
      text: parts.length ? parts.join(', ') : 'default',
      key: parts.join('|'),
    };
  };

  type FieldRow = { key: string; icon: IconName; label: string; baseText: string; currentText: string; diff: boolean };
  const fields: FieldRow[] = [];

  fields.push({
    key: 'harness',
    icon: 'settings',
    label: 'Harness',
    baseText: baseHarness,
    currentText: curHarness,
    diff: curHarness !== baseHarness,
  });

  const curModel = typeof a.model === 'string' ? a.model : '';
  const baseModel = baseProfile ? baseProfile.model : 'gpt-6.2-sol';
  fields.push({
    key: 'model',
    icon: 'bot',
    label: 'Model',
    baseText: baseModel,
    currentText: curModel || 'unset',
    diff: !!curModel && curModel !== baseModel,
  });

  const curEffort = a.effort ?? (curHarness === 'codex' ? 'medium' : 'none');
  const baseEffort = baseProfile ? (baseProfile.effort ?? 'none') : defaultEffort(curHarness);
  fields.push({
    key: 'effort',
    icon: 'gauge',
    label: 'Effort',
    baseText: baseEffort,
    currentText: curEffort,
    diff: curEffort !== baseEffort,
  });

  const curRole = a.instructionRole ?? defaultRole(curHarness);
  const baseRole = baseProfile ? (baseProfile.instructionRole ?? defaultRole(baseHarness)) : defaultRole(curHarness);
  fields.push({
    key: 'instructionRole',
    icon: 'messages-square',
    label: 'Instruction role',
    baseText: baseRole,
    currentText: curRole,
    diff: curRole !== baseRole,
  });

  const curTools = Array.isArray(a.tools) ? normList(a.tools) : null;
  const baseTools = baseProfile ? (Array.isArray(baseProfile.tools) ? normList(baseProfile.tools) : null) : null;
  const toolsDiff = (curTools === null) !== (baseTools === null) || (curTools !== null && baseTools !== null && !sameList(curTools, baseTools));
  fields.push({
    key: 'tools',
    icon: 'terminal',
    label: 'Native tools',
    baseText: baseTools === null ? 'all native' : fmtList(baseTools),
    currentText: curTools === null ? 'all native' : fmtList(curTools),
    diff: toolsDiff,
  });

  const curSet = fmtSettings(a.settings);
  const baseSet = fmtSettings(baseProfile?.settings);
  fields.push({
    key: 'settings',
    icon: 'sliders-horizontal',
    label: 'Harness settings',
    baseText: baseSet.text,
    currentText: curSet.text,
    diff: curSet.key !== baseSet.key,
  });

  const curSkills = normList(a.skills);
  const baseSkills = baseProfile ? normList(baseProfile.skills) : [];
  fields.push({
    key: 'skills',
    icon: 'file-text',
    label: 'Skills',
    baseText: fmtList(baseSkills),
    currentText: fmtList(curSkills),
    diff: !sameList(curSkills, baseSkills),
  });

  const curConn = normList(a.connections);
  const baseConn = baseProfile ? normList(baseProfile.connections) : [];
  fields.push({
    key: 'connections',
    icon: 'link',
    label: 'Connections',
    baseText: fmtList(baseConn),
    currentText: curConn.length ? `${fmtList(curConn)} · credentials stay in the connection` : 'none',
    diff: !sameList(curConn, baseConn),
  });

  const curMcp = fmtMcp(a.mcpServers);
  const baseMcp = fmtMcp(baseProfile?.mcpServers);
  fields.push({
    key: 'mcpServers',
    icon: 'network',
    label: 'MCP servers',
    baseText: baseMcp.text,
    currentText: curMcp.text,
    diff: curMcp.key !== baseMcp.key,
  });

  const curDel = normList(a.delegationProfiles);
  const baseDel = baseProfile ? normList(baseProfile.delegationProfiles) : [];
  fields.push({
    key: 'delegationProfiles',
    icon: 'bot',
    label: 'Delegation profiles',
    baseText: fmtList(baseDel),
    currentText: fmtList(curDel),
    diff: !sameList(curDel, baseDel),
  });

  const boolFields: { key: keyof Pick<Profile, 'fleetTools' | 'collaborationTools' | 'fleetOperations' | 'gitNetwork'>; label: string; icon: IconName; defVal: boolean }[] = [
    { key: 'fleetTools', label: 'Fleet tools', icon: 'settings', defVal: true },
    { key: 'collaborationTools', label: 'Collaboration tools', icon: 'messages-square', defVal: true },
    { key: 'fleetOperations', label: 'Fleet operations', icon: 'play', defVal: false },
    { key: 'gitNetwork', label: 'Git network', icon: 'git-merge', defVal: false },
  ];
  for (const bf of boolFields) {
    const curVal = a[bf.key] ?? bf.defVal;
    const baseVal = baseProfile ? (baseProfile[bf.key] ?? bf.defVal) : bf.defVal;
    fields.push({
      key: bf.key,
      icon: bf.icon,
      label: bf.label,
      baseText: String(baseVal),
      currentText: String(curVal),
      diff: curVal !== baseVal,
    });
  }

  const curCodeMode = a.codeMode ?? 'off';
  const baseCodeMode = baseProfile ? (baseProfile.codeMode ?? 'off') : 'off';
  fields.push({
    key: 'codeMode',
    icon: 'terminal',
    label: 'Code mode',
    baseText: baseCodeMode,
    currentText: curCodeMode,
    diff: curCodeMode !== baseCodeMode,
  });

  if (baseProfile) {
    const curInstr = typeof a.instructions === 'string' ? a.instructions : '';
    const instrDiff = curInstr !== baseProfile.instructions;
    fields.push({
      key: 'instructions',
      icon: 'file-text',
      label: 'Instructions',
      baseText: `${baseProfile.instructions.length} chars`,
      currentText: instrDiff ? `${curInstr.length} chars (custom)` : `inherited (${curInstr.length} chars)`,
      diff: instrDiff,
    });
  }

  const diffs = fields.filter(f => f.diff);
  const inherited = fields.filter(f => !f.diff);
  const baseSourceLabel = baseProfile ? baseProfile.id : 'system';
  const compatState = def.draft ? 'draft' : listItem?.compatibility === 'checked' ? 'ready' : listItem?.compatibility === 'failed' ? 'incompatible' : 'unchecked';

  return <>
    <DetailSection title="Identity">
      <KvRow
        icon="bot"
        label={a.id || def.id}
        meta={<span class="mono">{def.scope} · {def.state} · r{def.head}{def.draft ? ' · draft' : ''}</span>}
        value={<StateBadge family="definition" state={compatState} />}
      />
      {(a.category || roles.length > 0) && (
        <KvRow icon="layers-2" label="Role" meta={[a.category, ...roles].filter(Boolean).join(' · ')} />
      )}
      {a.variantOf && (
        <KvRow
          icon="chart-no-axes-column"
          label="Variant of"
          meta={<>
            <a class="row-link" href={`/agents/${encodeURIComponent(a.variantOf)}`} onClick={e => { e.preventDefault(); go(`/agents/${encodeURIComponent(a.variantOf!)}`); }}>{a.variantOf}</a>
            {a.experiment && <span class="mono"> · {a.experiment}</span>}
          </>}
        />
      )}
      {a.purpose && <KvRow icon="info" label="Purpose" meta={a.purpose} />}
      {Array.isArray(a.tags) && a.tags.length > 0 && (
        <KvRow icon="layers-2" label="Tags" meta={<span class="tag-row">{a.tags.map(t => <span key={t} class="chip">{t}</span>)}</span>} />
      )}
    </DetailSection>
    <DetailSection title="Differences from default">
      {variantOf && !baseOp.data && !baseOp.error ? (
        <Empty text={`Loading base profile ${variantOf}…`} />
      ) : diffs.length > 0 ? (
        diffs.map(f => (
          <KvRow
            key={f.key}
            icon={f.icon}
            label={f.label}
            meta={<span class="mono">{f.baseText} → {f.currentText}</span>}
            value={<span class="badge t-attention">override</span>}
          />
        ))
      ) : (
        <KvRow icon="circle-check" label="Matches base defaults" meta={`No overrides from ${baseSourceLabel}`} />
      )}
      {inherited.length > 0 && (
        <Disclosure summary={`Inherited defaults (${inherited.length})`} summaryClass="muted">
          {inherited.map(f => (
            <KvRow key={f.key} icon={f.icon} label={f.label} meta={<span class="mono">{f.currentText}</span>} />
          ))}
        </Disclosure>
      )}
    </DetailSection>
  </>;
}

function DiffTab({ kind, id, hasDraft, head }: { kind: Kind; id: string; hasDraft: boolean; head: number }) {
  const from = hasDraft ? { revision: head } : { repository: true }, to = hasDraft ? { draft: true } : { revision: head };
  const { data: d, error } = useOp('definition_diff', { kind, id, from, to });
  return <DetailSection title={hasDraft ? `Draft against r${head}` : `r${head} against the repository file`}>
    {error ? <div class="error" role="alert">{error}</div> : d ? <DiffText label="Diff" text={d.unified} /> : <Empty text="Loading…" />}
  </DetailSection>;
}

function HistoryTab({ kind, id }: { kind: Kind; id: string }) {
  const { data: h, error } = useOp('definition_history', { kind, id });
  if (error) return <DetailSection title="Revisions"><div class="error" role="alert">{error}</div></DetailSection>;
  return <DetailSection title="Revisions">
    {!h ? <Empty text="Loading…" /> : h.revisions.map(r => <KvRow key={r.revision} icon={r.source === 'agent' ? 'bot' : r.source === 'repo' ? 'git-merge' : 'file-plus-2'} label={`r${r.revision} · ${r.message}`}
      meta={<span class="mono">{r.actor.person?.name ?? (r.actor.executionId ? 'agent' : 'Fleet')}{r.actor.kind === 'agent' && r.actor.directedBy ? ` for ${r.actor.directedBy.name}` : ''} · {r.contentSha.slice(0, 7)}</span>}
      value={<AgeChip t={Date.parse(r.at)} />} />)}
  </DetailSection>;
}

function EffectiveTab({ id, draft, version }: { id: string; draft: boolean; version: string }) {
  const list = useOp('project_list', {}), projects = projectIds(list.data);
  const [chosen, setProject] = useState(''), project = chosen || projects[0] || '';
  const effective = useOp('agent_effective', project ? { id, projectId: project, draft } : null), e = effective.data;
  const error = list.error ?? effective.error;
  // The draft's text changed: read the effective settings again once typing pauses.
  const reread = useRef(effective.reload); reread.current = effective.reload;
  useEffect(() => { if (!version) return; const t = setTimeout(() => reread.current(), 800); return () => clearTimeout(t); }, [version]);
  return <>
    {projects.length > 0 && <DetailSection title="Project"><Choice label="Project" value={project} onChange={setProject} options={projects.map(p => ({ id: p, label: p }))} /></DetailSection>}
    {error && <div class="error" role="alert">{error}</div>}
    {e && <DetailSection title="Layers, in the order the agent receives them">
      {e.layers.map(l => <KvRow key={l.name} icon={l.name === 'role' ? 'bot' : l.name === 'project' ? 'folder-open' : 'file-plus-2'} label={l.name}
        meta={<span class="mono">{l.source}{l.revision ? ` · ${l.revision}` : ''}{l.fields.length ? ` · ${l.fields.join(', ')}` : ''}</span>} />)}
    </DetailSection>}
    {e && <DetailSection title="Effective settings">
      <KvRow icon="settings" label="Harness" meta={<span class="mono">{e.result.harness ?? 'codex'}</span>} />
      <KvRow icon="bot" label="Model" meta={<span class="mono">{e.result.model}{e.result.effort ? ` · ${e.result.effort}` : ''}</span>} />
    </DetailSection>}
  </>;
}

function CompatibilityTab({ id }: { id: string }) {
  const { data: c, error } = useOp('agent_compatibility', { id });
  return <DetailSection title="Combinations seen in 30 days">
    {error ? <div class="error" role="alert">{error}</div> : !c ? <Empty text="Loading…" /> : c.rows.map(r =>
      <KvRow key={`${r.harness}/${r.harnessVersion}/${r.model}/${r.effort}`} icon={r.state === 'checked' ? 'circle-check' : r.state === 'failed' ? 'circle-x' : 'circle-dashed'}
        label={`${r.harness}${r.harnessVersion ? ' ' + r.harnessVersion : ''} · ${r.model}${r.effort ? ' · ' + r.effort : ''}`}
        meta={<span class="mono">{r.state === 'unchecked' ? 'not checked' : r.state}{r.revision ? ` · r${r.revision}` : ''}{r.error ? ` · ${r.error}` : ''}</span>}
        value={r.evidence ? <AgeChip t={Date.parse(r.evidence.at)} /> : undefined} />)}
  </DetailSection>;
}


type CatalogueMethodExt = Ops['catalogue']['reply']['methods'][number] & {
  status?: string;
  capability?: string;
  description?: string;
};

const methodStatusTone = (s: string) =>
  s === 'recommended' ? 'success' : s === 'experimental' ? 'attention' : s === 'deprecated' ? 'danger' : 'neutral';

const methodStatusIcon = (s: string) =>
  s === 'recommended' ? 'circle-check' : s === 'experimental' ? 'circle-dashed' : s === 'deprecated' ? 'circle-slash' : 'info';

function CatalogueMethods() {
  const [, go] = useLocation();
  const list = useOp('project_list', {}), projects = projectIds(list.data);
  const [chosen, setProject] = useState(''), project = chosen || projects[0] || '';
  const [search, setSearch] = useState(''), [scopeSearch, setScopeSearch] = useState('');
  const registry = useOp('lab_registry', {}), useCases = registry.data?.useCases ?? [];
  const [selectedUseCases, setSelectedUseCases] = useState<string[]>([]);
  const useCasePins = usePins('catalogue-use-cases');
  const [selected, setSelected] = useState<string[]>([]), [baseline, setBaseline] = useState('');
  const [busy, setBusy] = useState(false), [failure, setError] = useState<string | null>(null);
  const catalogue = useOp('catalogue', project ? { projectId: project } : null), candidate = catalogue.data, catalogueError = catalogue.error, restored = catalogue.restored;
  const error = failure ?? list.error ?? registry.error;
  // Evidence decides a recommendation: after a project is picked only an answer read since then counts, never the copy
  // held from an earlier visit. An answer for another project (a mismatch) is never shown as this project's evidence.
  const picked = useRef<{ project: string | null; held: unknown }>({ project: null, held: null });
  if (picked.current.project !== project) picked.current = { project, held: candidate };
  const c = candidate && candidate !== picked.current.held && candidate.projectId === project && !catalogueError ? candidate : null;
  const methods = c?.methods ?? [];

  const { useCaseNodes, archivedUseCaseNodes } = useMemo(() => {
    const grouped = groupUseCases(useCases.filter(u => isArchivedUseCase(u) || u.lab_fit !== 'none'));
    const toNode = (u: RegistryUseCase): CatalogueTreeNode => {
      const goal = u.goal.trim();
      const shortGoal = goal.length > 48 ? `${goal.slice(0, 47).trimEnd()}…` : goal;
      return {
        id: u.id,
        label: u.id,
        sub: shortGoal || undefined,
        icon: 'chart-no-axes-column',
        pinned: useCasePins.has(u.id),
        disabled: u.lab_fit === 'none',
        disabledReason: u.lab_fit === 'none' ? 'Not eligible for Lab evaluation' : undefined,
        searchText: `${u.id} ${u.goal} ${u.status} ${(u.tags ?? []).join(' ')}`,
      };
    };
    const activeNodes: CatalogueTreeNode[] = grouped.collections.map(col => ({
      id: `group:${col.key}`,
      label: col.title,
      icon: col.key === 'benchmarks' ? 'chart-no-axes-column' : col.key === 'real-work' ? 'activity' : 'folder-open',
      children: col.items.map(toNode),
    }));
    const archived: CatalogueTreeNode[] = grouped.archived.length > 0
      ? [{
          id: 'group:archived',
          label: 'Archived',
          icon: 'archive',
          defaultCollapsed: false,
          children: grouped.archived.map(toNode),
        }]
      : [];
    return { useCaseNodes: activeNodes, archivedUseCaseNodes: archived };
  }, [useCases, useCasePins.ids]);

  const choose = (id: string) => { if (!c || restored || busy) return; const next = selected.includes(id) ? selected.filter(x => x !== id) : [...selected, id]; setSelected(next); if (!next.includes(baseline)) setBaseline(next.includes(String(c?.recommendedMethod)) ? String(c?.recommendedMethod) : ''); };
  const act = async (run: () => Promise<void>) => { setBusy(true); try { await run(); setError(null); } catch (x) {setError(errorText(x));} finally {setBusy(false);} };
  return <>
    <DetailSection title="Project"><SearchField label="Find project" value={scopeSearch} onInput={setScopeSearch} onSearch={() => {}} /><Choice label="Project" value={project} options={projects.filter(p => p.includes(scopeSearch)).map(p => ({id:p,label:p}))} onChange={p => {setProject(p);setSelected([]);setBaseline('');setSelectedUseCases([]);setError(null);}} /></DetailSection>
    {error && <div class="error" role="alert">{error}</div>}
    {catalogueError && <div class="error" role="alert">Catalogue unavailable: {catalogueError}. No current evidence loaded for {project}.<Button label="Retry Catalogue" icon="rotate-cw" onClick={catalogue.reload} /></div>}
    {c && <p class="muted">Evidence project: {c.projectId}{restored ? ' · restored, read-only until current confirmation' : ''}</p>}
    <DetailSection title="Methods"><SearchField label="Find method" value={search} onInput={setSearch} onSearch={() => {}} />
      {methods.filter(m => {
        const mx = m as CatalogueMethodExt;
        return matchSearch(search, `${m.name} ${m.id} ${mx.capability ?? ''} ${mx.description ?? ''} ${mx.status ?? ''}`);
      }).map(m => {
        const mx = m as CatalogueMethodExt;
        const a = m.assessment;
        return <div key={m.id}>
          <KvRow icon="circle-check" label={`${m.name} · ${m.id}`}
            meta={<span class="tag-row">
              <span>{m.scope} · {m.pipeline}</span>
              {mx.capability && <span class="chip">{mx.capability}</span>}
              {mx.status && <span class={`badge t-${methodStatusTone(mx.status)}`}><Icon name={methodStatusIcon(mx.status)} /><span>{mx.status}</span></span>}
            </span>}
            value={<><Choice label={`Select ${m.name}`} value={selected} options={[{id:m.id,label:selected.includes(m.id) ? 'Selected' : 'Select'}]} onChange={choose} /><Button label="Recommend for project" icon="check" iconOnly busy={busy} disabledReason={restored ? 'Await current scoped Catalogue read' : undefined} onClick={() => act(async () => {await op('recommendation_set', {projectId:project,methodId:m.id});catalogue.reload();})} /></>} />
          {mx.description && <p class="muted">{mx.description}</p>}
          <KvRow icon="chart-no-axes-column" label={a.scoreForm === 'typed' ? 'Comparison score' : 'Quality'} meta={a.score == null ? 'Unavailable' : `${a.score.toFixed(3)}${a.partial ? ' · partial/incomplete' : ''}`} value={`${a.basis} · ${a.eligibleCells ?? 0}/${a.expectedCells ?? 0} cells · ${a.examples ?? 0} ${a.examples === 1 ? 'example' : 'examples'}`} />
          <KvRow icon="clock" label="Time / cost" meta={a.wallMsMedian == null ? 'Time unavailable' : `${a.wallMsMedian} ms · ${a.durationSamples == null ? 'pipeline usage window' : `${a.durationSamples} samples`}`} value={`${a.costUsd == null ? 'Cost unavailable' : `$${a.costUsd} · ${a.costBasis}`}`} />
          <KvRow icon="folder-open" label="Recommended for" meta={m.recommendedFor.join(', ') || 'None'} />
          {a.state && <p class="muted">{a.state} · {a.stopReason ?? ''} · {a.failedCells ?? 0} failed · {a.excludedCells ?? 0} excluded</p>}
          {a.reason && <p>{a.reason}</p>}{a.exclusions && <p>{a.exclusions}</p>}
          {a.evaluator && <p class="muted">{a.variant} · {a.split} · {a.evaluator} · {a.evaluatorVersion}</p>}
          {a.experimentId && <a href={`/lab/${a.experimentId}`}>Assessment experiment</a>}
          {a.sourceExperimentId && <a href={`/lab/${a.sourceExperimentId}`}>Source experiment</a>}
          {a.ongoingExperimentId && <a href={`/lab/${a.ongoingExperimentId}`}>Current experiment · {a.ongoingState}</a>}
        </div>;
      })}
    </DetailSection>
    <DetailSection title="Compare methods in Lab">
      <Choice label="Baseline" value={baseline} options={methods.filter(m=>selected.includes(m.id)).map(m=>({id:m.id,label:m.name}))} onChange={setBaseline} />
      <EntityPicker
        mode="multiple"
        label="Use cases"
        nodes={useCaseNodes}
        archivedNodes={archivedUseCaseNodes}
        value={selectedUseCases}
        onChange={setSelectedUseCases}
        onPin={node => useCasePins.toggle(node.id)}
        placeholder="Select use cases"
        searchPlaceholder="Find use case"
        loading={!registry.data && !registry.error}
        error={registry.error}
        onRetry={registry.reload}
        disabledReason={catalogueError || restored || !c ? 'Await current scoped Catalogue read' : undefined}
      />
      <Button
        label={selectedUseCases.length > 1 ? `Create ${selectedUseCases.length} comparisons` : 'Create comparison'}
        icon="play"
        busy={busy}
        disabledReason={catalogueError || restored || !c ? 'Await current scoped Catalogue read' : !project || selected.length < 2 || !baseline || !selected.includes(baseline) || selected.some(id => !methods.some(m => m.id === id)) || selectedUseCases.length === 0 ? 'Select two methods, a baseline and use case' : undefined}
        onClick={() => act(async () => {
          let firstExperimentId = '';
          for (const uc of selectedUseCases) {
            const r = await op('lab_compare_methods', { projectId: project, capability: String(c?.capability), methods: selected, baseline, useCase: uc });
            if (!firstExperimentId) firstExperimentId = r.experimentId;
          }
          if (firstExperimentId) go(`/lab/${firstExperimentId}`);
        })}
      />
    </DetailSection>
  </>;
}

function costBasisOf(b: string | null | undefined): CostFigureLike['basis'] {
  return b === 'measured' || b === 'estimated' || b === 'subscription' || b === 'unavailable' ? b : 'estimated';
}

function UsageTab({ kind, id }: { kind: 'agent' | 'pipeline'; id: string }) {
  const [, go] = useLocation();
  const [days, setDays] = useState<'7' | '30'>('30');
  const { data: u, error } = useOp('definition_usage', { kind, id, days: Number(days) });
  if (error) return <DetailSection title="Usage"><div class="error" role="alert">{error}</div></DetailSection>;
  if (!u) return <DetailSection title="Usage"><Empty text="Loading…" /></DetailSection>;
  const windowCost = (w: Ops['definition_usage']['reply']['total']): readonly CostFigureLike[] =>
    w.cost?.length ? w.cost : w.costUsd != null ? [{ value: w.costUsd, currency: 'USD', basis: costBasisOf(w.costBasis), ...(w.costBasis === 'tokens' ? { reason: 'tokens' } : {}) }] : [];
  const totalCost = windowCost(u.total);
  const hasCost = totalCost.some(c => (c.value !== null && c.basis !== 'unavailable') || c.basis === 'subscription');
  const totalItems: TotalItem[] = [
    { label: 'Runs', m: { value: u.total.runs, basis: 'measured' }, kind: 'count', note: u.total.runs > 0 ? `${u.total.succeeded} succeeded` : undefined },
    ...(u.total.resources ? resourceTotals(u.total.resources, { extraCost: totalCost, hideUnavailable: true }) : [
      ...(u.total.wallMsMedian != null ? [{ label: 'Median wall', m: msMeasured(u.total.wallMsMedian, 'measured'), kind: 'duration' as const }] : []),
      ...(u.total.tokens > 0 || u.total.runs === 0 ? [{ label: 'Tokens', m: { value: u.total.tokens, basis: 'measured' as const }, kind: 'count' as const }] : []),
      ...(hasCost ? [{ label: 'Cost', cost: totalCost, showUnavailable: false }] : []),
    ]),
  ];
  if (u.total.resources && u.total.wallMsMedian != null) {
    totalItems.splice(1, 0, { label: 'Median wall', m: msMeasured(u.total.wallMsMedian, u.total.resources.durations.basis === 'unavailable' && u.total.wallMsMedian > 0 ? 'measured' : u.total.resources.durations.basis), kind: 'duration' });
  }
  const routeFor = (k: string) => k === 'agent' ? '/agents' : k === 'pipeline' ? '/pipelines' : '/catalogue';
  return <>
    <div class="res-bar">
      <Choice label="Usage window" value={days} onChange={v => setDays(v as '7' | '30')} options={[{ id: '7', label: '7 days' }, { id: '30', label: '30 days' }]} />
      <span class="grow" />
      <Button label="Projects analytics" icon="external-link" kind="quiet" onClick={() => go('/projects')} />
    </div>
    <DetailSection title={`Summary (${days} days)`}><Totals items={totalItems} /></DetailSection>
    <DetailSection title="Used by">
      {u.usedBy.length ? u.usedBy.map(x => <KvRow key={`${x.kind}:${x.id}`} icon={x.kind === 'pipeline' ? 'network' : x.kind === 'agent' ? 'bot' : 'rows-3'}
        label={x.kind} meta={<a class="row-link" href={`${routeFor(x.kind)}/${x.id}`} onClick={e => { e.preventDefault(); go(`${routeFor(x.kind)}/${x.id}`); }}>{x.id}</a>} />)
        : <span class="muted">not referenced by other definitions</span>}
    </DetailSection>
    {u.byRevision.length > 0 && <DetailSection title="By revision">
      <DataTable label="By revision" head={<tr><th scope="col">Revision</th><th scope="col">Runs</th><th scope="col">Succeeded</th><th scope="col">Median wall</th><th scope="col">Tokens</th><th scope="col">Cost</th></tr>}>
        {u.byRevision.map(rv => {
          const w = rv.window;
          const cost = windowCost(w);
          const tokRes = w.resources?.tokens;
          const tokM = tokRes
            ? (tokRes.basis === 'unavailable' ? (tokRes.total && tokRes.total > 0 ? { value: tokRes.total, basis: 'measured' as const, partial: 'incomplete telemetry' } : { value: null, basis: 'unavailable' as const }) : { value: tokRes.total, basis: tokRes.basis })
            : (w.tokens === 0 && w.runs > 0 ? { value: null, basis: 'unavailable' as const } : { value: w.tokens, basis: 'measured' as const });
          const wallBasis = w.resources?.durations.basis === 'unavailable' && (w.wallMsMedian ?? 0) > 0 ? 'measured' : (w.resources?.durations.basis ?? 'measured');
          return <tr key={rv.revision}>
            <th scope="row" class="mono">r{rv.revision}</th>
            <td class="num"><Num m={{ value: w.runs, basis: 'measured' }} kind="count" /></td>
            <td class="num"><Num m={{ value: w.succeeded, basis: 'measured' }} kind="count" /></td>
            <td class="num">{w.wallMsMedian != null ? <Num m={msMeasured(w.wallMsMedian, wallBasis)} kind="duration" /> : <span class="muted">—</span>}</td>
            <td class="num"><Num m={tokM} kind="count" /></td>
            <td class="num"><CostFigures cost={cost} showUnavailable={false} empty="—" /></td>
          </tr>;
        })}
      </DataTable>
    </DetailSection>}
  </>;
}
