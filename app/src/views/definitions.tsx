// A Library definition (docs/designs/fleet-definition-editors.md, patterns/definition-editor.md): an agent, pipeline or
// catalogue entry in the coordinator's store, edited as a draft and saved as revisions in Library's work area.
import { createEffect, createMemo, createSignal, For, onSettled, Show, untrack } from 'solid-js';
import { useLocation } from '../router.ts';
import {
  DefinitionEditor, DiffRow, DraftBar, DetailSection, InheritedRows, ProblemRow, Disclosure, EntityCode, EntityPicker, KvRow, ProjectPick, RowCheck,
  StateBadge, Empty, SettingField, CodeEditor, CopyButton, DiffText, StepList, AgeChip, Choice, Button, DataTable,
  Totals, CostFigures, Num, Icon, duration, msMeasured, matchSearch, SearchField,
  type CostFigureLike, type IconName, type PickProject, type StateOf, type StepItem, type TotalItem,
} from '../../../packages/ui/src/index.ts';
import { op, writeOnce, useAlive, type Ops } from '../api.ts';
import { usePins } from '../prefs.ts';
import { useOp } from '../store.ts';
import { resourceTotals } from './resources.tsx';
import { buildUseCaseTree, isArchivedUseCase } from './use-cases.ts';
import type { DefinitionListItem } from '../../../contracts/generated/ops.ts';
import type { AntigravitySettings, Profile, ProfileMcpGrant } from '../../../contracts/generated/profile.ts';

type Kind = 'agent' | 'pipeline' | 'catalogue';
/** The head's word for a definition out of line with its repository file. */
const STATE_NOTE: Partial<Record<string, string>> = {
  'repository-changed': 'the repository has a newer version',
  'store-only': 'made in Fleet, not in the repository',
  ahead: 'saved in Fleet after the repository version',
  'repository-deleted': 'deleted in the repository',
};
type Get = Ops['definition_get']['reply'];
type Draft = { base: number; text: string; changes: number; stale: boolean; problems: { path: string; message: string }[] };
type Content = Ops['definition_save']['request']['content'];

export const SYSTEM_AGENT_DEFAULTS = {
  harness: 'codex',
  model: 'gpt-6.2-sol',
  effort: 'medium',
} as const;

export const defaultInstructionRole = (harness: string): string =>
  harness === 'claude' ? 'system' : harness === 'antigravity' || harness === 'acp' ? 'user' : 'developer';

export function baseAgentDefaults(
  parent?: { harness?: string; model?: string; effort?: string | null } | null,
  baseline?: { harness?: string; model?: string; effort?: string | null } | null,
  currentHarness?: string,
): { harness: string; model: string; effort: string } {
  const harness = parent?.harness ?? baseline?.harness ?? SYSTEM_AGENT_DEFAULTS.harness;
  const model = parent?.model ?? baseline?.model ?? SYSTEM_AGENT_DEFAULTS.model;
  const effort = parent
    ? (parent.effort ?? 'none')
    : (currentHarness && currentHarness !== harness
        ? 'none'
        : (baseline?.effort ?? SYSTEM_AGENT_DEFAULTS.effort));
  return { harness, model, effort };
}

/** The typed content of a definition's text: JSON.parse checks the syntax, the server the shape (draft problems). */
const contentOf = (kind: Kind, text: string) => ({ [kind]: JSON.parse(text) }) as Content;
const errorText = (e: unknown) => e instanceof Error ? e.message : String(e);
/** Project options for ProjectPick, preserving human names. */
const projectOptions = (r: Ops['project_list']['reply'] | null): PickProject[] =>
  (r?.projects ?? []).map(p => ({ id: String(p.id), name: String(p.name ?? p.id) }));

export function DefinitionDetail(props: {
  kind: Kind;
  id: string;
  agentById?: ReadonlyMap<string, DefinitionListItem>;
  onClose: () => void;
}) {
  const [tab, setTab] = createSignal(untrack(() => props.kind === 'catalogue' ? 'Methods' : 'Configuration'));
  const got = useOp('definition_get', () => ({ kind: props.kind, id: props.id }));
  const drafted = useOp('definition_draft_get', () => (got.data?.draft ? { kind: props.kind, id: props.id } : null));
  const [draft, setDraft] = createSignal<Draft | null>(null, { ownedWrite: true });
  const [text, setText] = createSignal('', { ownedWrite: true });
  const [message, setMessage] = createSignal('');
  const [failure, setError] = createSignal<string | null>(null);
  const [busy, setBusy] = createSignal(false);
  let timer = 0;
  let pending: (() => Promise<void>) | null = null;
  let seeded = '';
  onSettled(() => () => clearTimeout(timer));
  // The editor's text follows the server while nothing typed waits: the draft's bytes when it has one (not a re-encoding
  // of its typed content), else the saved revision's; a new source (saved, discarded, a draft begun) replaces it.
  createEffect(
    () => [got.data, drafted.data, drafted.status] as const,
    ([g, drData, drStatus]) => {
      if (!g || pending) return;
      const d = g.draft && drStatus !== 404 ? drData : null;
      if (g.draft && !d && drStatus !== 404) return; // its draft is still being read
      setDraft(d ? { base: d.draft.baseRevision, text: d.text, changes: d.draft.changes, stale: d.draft.stale, problems: d.problems ?? [] } : null);
      const source = d ? 'draft:' + d.draft.baseRevision : 'revision:' + g.head;
      if (seeded !== source) { seeded = source; setText(d ? d.text : g.text); }
    },
  );
  const load = () => { void got.reload(); void drafted.reload(); };
  const error = createMemo(() => failure() ?? got.error ?? (drafted.status === 404 ? null : drafted.error));
  // Typing keeps a draft: written after a pause, so the next execution still uses the saved revision.
  const flush = async () => { clearTimeout(timer); const f = pending; pending = null; if (f) await f(); };
  const edit = (t: string) => {
    setText(t);
    clearTimeout(timer);
    pending = async () => {
      const def = got.data;
      if (!def) return;
      const kind = props.kind, id = props.id;
      if (t === def.text) { await op('definition_draft_discard', { kind, id }).catch(() => {}); setDraft(null); return; }
      let content;
      try { content = contentOf(kind, t); } catch (e) { setDraft(d => ({ base: d?.base ?? def.head, text: t, changes: d?.changes ?? 0, stale: false, problems: [{ path: '', message: 'JSON: ' + errorText(e) }] })); return; }
      try {
        const r = await op('definition_draft_put', { kind, id, baseRevision: draft()?.base ?? def.head, content });
        seeded = 'draft:' + r.draft.baseRevision; // the text on screen is this draft
        setDraft({ base: r.draft.baseRevision, text: t, changes: r.draft.changes, stale: r.draft.stale, problems: r.problems });
      } catch (e) { setError(errorText(e)); }
    };
    timer = setTimeout(flush, 700) as unknown as number;
  };
  const run = async (f: () => Promise<unknown>) => { setBusy(true); try { await f(); setError(null); } catch (e) { setError(errorText(e)); } finally { setBusy(false); } };
  // Save from the draft's own base: the server refuses it (409) when someone else saved meanwhile or the repository file
  // changed, and the error says what to look at.
  const save = () => run(async () => {
    await flush();
    const def = got.data, d = draft();
    if (!def || !d) return;
    await op('definition_save', { kind: props.kind, id: props.id, baseRevision: d.base, content: contentOf(props.kind, text()), message: message().trim() || 'Edited in Fleet' });
    setMessage(''); load();
  });
  const discard = () => run(async () => { clearTimeout(timer); pending = null; await op('definition_draft_discard', { kind: props.kind, id: props.id }); load(); });
  const alive = useAlive();
  const runDraft = () => run(async () => { await flush(); const requestId = crypto.randomUUID(); await writeOnce('pipeline_run_create', { pipelineId: props.id, requestId, draft: true }, () => setError('unknown, retrying'), { signal: alive }); });

  // Take repository version shows its preview first; applying it replaces the store's head with the repository file.
  const [repoPreview, setRepoPreview] = createSignal<string | null>(null);
  const takeRepo = (preview: boolean) => run(async () => {
    const r = await op('definition_take_repository', { keys: [{ kind: props.kind, id: props.id }], preview, message: 'Take repository version from Library' });
    if (preview) setRepoPreview(r.items.map(i => `${i.kind}/${i.id}: ${i.status}${i.reason ? ` (${i.reason})` : ''}`).join(' · '));
    else { setRepoPreview(null); load(); }
  });
  const compatibility = (): StateOf<'definition'> => {
    const c = props.agentById?.get(props.id)?.compatibility;
    return c === 'checked' ? 'ready' : c === 'failed' ? 'incompatible' : 'unchecked';
  };

  const tabs = createMemo(() =>
    props.kind === 'agent' ? ['Configuration', 'Effective', 'Diff', 'Compatibility', 'Usage', 'History']
    : props.kind === 'catalogue' ? ['Methods', 'Configuration', 'Diff', 'History']
    : ['Configuration', 'Usage', 'Diff', 'History'],
  );

  return (
    <Show when={got.data} fallback={
      <article class="detail stack" data-ui="detail" tabindex={-1} aria-label={props.id}>
        <header class="detail-h"><Button label="Close" icon="x" iconOnly onClick={() => props.onClose()} /><span class="muted mono">{props.id}</span></header>
        <Show when={error()} fallback={<Empty text="Loading…" />}>{err => <div class="error" role="alert">{err()}</div>}</Show>
      </article>
    }>{def => (
      <DefinitionEditor label={props.id} crumb={`library · ${def().path} · r${def().head}`} title={def().id}
        state={<>
          <StateBadge family="definition" state={def().state === 'repository-changed' ? 'repository' : draft() ? 'draft' : props.kind === 'agent' ? compatibility() : 'ready'} />
          <Show when={STATE_NOTE[def().state]}>{n => <span class="muted">{n()}</span>}</Show>
        </>}
        actions={<Show when={def().state === 'repository-changed'}>
          <Button label="Take repository version" icon="download" busy={busy()} onClick={() => void takeRepo(true)} />
        </Show>}
        tabs={tabs()} tab={tab()} onTab={setTab} onClose={() => props.onClose()}>
        <Show when={error()}>{err => <div class="error" role="alert">{err()}</div>}</Show>
        <Show when={repoPreview()}>{facts => (
          <ProblemRow severity="warning" title="Preview: take the repository version" facts={facts()}
            actions={[
              { label: 'Take repository version', icon: 'download', primary: true, onClick: busy() ? undefined : () => void takeRepo(false) },
              { label: 'Dismiss', onClick: () => setRepoPreview(null) },
            ]} />
        )}</Show>
        <Show when={draft()}>{d => (
          <DraftBar changes={d().changes} base={`r${d().base}`} note={d().stale ? `The saved revision moved to r${def().head}: look at the diff before saving.` : 'New executions keep using the saved revision.'}
            actions={[
              ...(props.kind === 'pipeline' ? [{ label: 'Run', icon: 'play' as const, onClick: busy() || d().problems.length ? undefined : runDraft }] : []),
              { label: 'Diff', onClick: () => setTab('Diff') },
              { label: 'Discard', icon: 'x' as const, onClick: discard },
              { label: 'Save as commit', icon: 'save' as const, primary: true, onClick: busy() || d().problems.length ? undefined : save },
            ]} />
        )}</Show>
        <Show when={tab() === 'Configuration'}>
          <Show when={draft()}><SettingField label="Message" value={message()} help="Says what the revision changes; shown in History." now={Date.now()} status={{ kind: 'pending' }} onSave={setMessage} /></Show>
          <Show when={props.kind === 'pipeline'}><PipelineFacts text={text()} def={def()} /></Show>
          <Show when={props.kind === 'pipeline'}><Steps text={text()} /></Show>
          <Show when={props.kind === 'agent'}><AgentFacts text={text()} def={def()} agentById={props.agentById} /></Show>
          <Disclosure summary="Definition JSON" summaryClass="muted">
            <CodeEditor label={def().path} value={text()} onChange={edit} problems={draft()?.problems ?? []} />
          </Disclosure>
        </Show>
        <Show when={tab() === 'Methods' && props.kind === 'catalogue'}><CatalogueMethods /></Show>
        <Show when={tab() === 'Diff'}><DiffTab kind={props.kind} id={props.id} hasDraft={!!draft()} head={def().head} /></Show>
        <Show when={tab() === 'History'}><HistoryTab kind={props.kind} id={props.id} /></Show>
        <Show when={tab() === 'Usage' && (props.kind === 'agent' || props.kind === 'pipeline')}><UsageTab kind={props.kind as 'agent' | 'pipeline'} id={props.id} /></Show>
        <Show when={tab() === 'Effective'}><EffectiveTab id={props.id} draft={!!draft()} version={draft()?.text ?? ''} /></Show>
        <Show when={tab() === 'Compatibility'}><CompatibilityTab id={props.id} /></Show>
      </DefinitionEditor>
    )}</Show>
  );
}

// reads: what a step's arguments or prompt reference (${example.x}, ${steps.<id>.output…}).
type StepJson = {
  id?: string;
  kind?: string;
  profile?: string;
  connection?: string;
  tool?: string;
  status_tool?: string;
  recent_tool?: string;
  mode?: string;
  output?: Record<string, unknown>;
  command?: string;
  timeout_ms?: number;
  host?: string;
  when?: { source?: { step?: string; path?: string[] }; equals?: unknown };
};
type PipelineJson = { id?: string; version?: number; resultStep?: string; description?: string; steps?: unknown };
type DiffFieldRow = { key: string; icon: IconName; label: string; baseText: string; currentText: string; diff: boolean; draft?: boolean; note?: string };

const parseJsonObj = <T extends object>(raw: string): T | null => {
  try {
    const parsed: unknown = JSON.parse(raw);
    return parsed && typeof parsed === 'object' && !Array.isArray(parsed) ? (parsed as T) : null;
  } catch {
    return null;
  }
};

const stepArrayOf = (steps: unknown): StepJson[] =>
  Array.isArray(steps) ? (steps as unknown[]).filter((s): s is StepJson => !!s && typeof s === 'object') : [];

// Only what differs from the base default, as diff rows; the unchanged fields fold into "Inherited defaults (N)" (OR-45).
function DiffFromDefault(props: {
  fields: readonly DiffFieldRow[];
  base: string;
  emptyText: string;
  loadingText?: string;
}) {
  const diffs = createMemo(() => props.fields.filter(f => f.diff));
  const inherited = createMemo(() => props.fields.filter(f => !f.diff));
  return (
    <DetailSection title={`Differs from ${props.base} default${diffs().length ? ` ${diffs().length}` : ''}`}>
      <Show when={!props.loadingText} fallback={<Empty text={props.loadingText!} />}>
        <Show when={diffs().length > 0} fallback={<p class="muted">{props.emptyText}</p>}>
          <For each={diffs()}>{f => <DiffRow field={f.key} value={f.note ? `${f.currentText} · ${f.note}` : f.currentText} base={f.baseText} />}</For>
        </Show>
      </Show>
      <Show when={inherited().length > 0}>
        <InheritedRows count={inherited().length}>
          <For each={inherited()}>{f => <DiffRow field={f.key} value={f.currentText} base={f.baseText} />}</For>
        </InheritedRows>
      </Show>
    </DetailSection>
  );
}

const readsOf = (s: unknown) => {
  const refs = [...(JSON.stringify(s) ?? '').matchAll(/\$\{(example|steps\.[\w-]+)/g)].map(m => m[1].replace('steps.', ''));
  return [...new Set(refs)].join(', ') || 'nothing';
};

function Steps(props: { text: string }) {
  const items = createMemo<StepItem[] | null>(() => {
    const definition = parseJsonObj<PipelineJson>(props.text);
    if (!definition || !Array.isArray(definition.steps)) return null;
    const steps = stepArrayOf(definition.steps);
    const resultStep = definition.resultStep;
    return steps.map(s => ({ id: s.id ?? '', kind: s.kind === 'api' ? 'tool' : s.kind === 'agent' ? 'agent' : 'program', executor: s.kind === 'api' || s.kind === 'agent' || s.kind === 'script' ? s.profile ?? s.tool ?? s.command ?? '' : `${s.kind}: no longer run`, reads: readsOf(s), writes: 'output', result: s.id === resultStep, condition: s.when ? `${s.when.source?.step}.${(s.when.source?.path ?? []).join('.')} = ${JSON.stringify(s.when.equals)}` : undefined }));
  });
  return <Show when={items()}>{xs => <DetailSection title="Steps"><StepList items={xs()} label="Steps" /></DetailSection>}</Show>;
}

function PipelineFacts(props: { text: string; def: Get }) {
  const model = createMemo(() => {
    const p = parseJsonObj<PipelineJson>(props.text);
    if (!p) return null;
    const def = props.def;
    const steps = stepArrayOf(p.steps);
    const kinds = [...new Set(steps.map(s => s.kind).filter((k): k is string => !!k))];
    const profiles = [...new Set(steps.filter(s => s.kind === 'agent' && s.profile).map(s => s.profile!))];
    const connections = [...new Set(steps.filter(s => s.kind === 'api' && s.connection).map(s => s.connection!))];

    const version = typeof p.version === 'number' ? p.version : 2;
    const condSteps = steps.filter(s => !!s.when);
    const jobSteps = steps.filter(s => s.kind === 'api' && s.mode && s.mode !== 'sync');
    const outputSteps = steps.filter(s => s.kind === 'api' && s.output && typeof s.output === 'object' && Object.keys(s.output).length > 0);
    const hostSteps = steps.filter(s => s.kind === 'script' && !!s.host);
    const timeoutSteps = steps.filter(s => s.kind === 'script' && typeof s.timeout_ms === 'number');

    const fields: DiffFieldRow[] = [
      {
        key: 'version',
        icon: 'git-merge',
        label: 'Plan version',
        baseText: 'v2',
        currentText: version === 3 ? 'v3 (conditional)' : `v${version}`,
        diff: version !== 2,
      },
      {
        key: 'resultStep',
        icon: 'circle-check',
        label: 'Result step',
        baseText: 'last step',
        currentText: p.resultStep || 'last step',
        diff: !!p.resultStep,
      },
      {
        key: 'when',
        icon: 'sliders-horizontal',
        label: 'Step activation',
        baseText: 'unconditional',
        currentText: condSteps.length ? condSteps.map(s => s.id || '?').join(', ') : 'unconditional',
        diff: condSteps.length > 0,
      },
      {
        key: 'mode',
        icon: 'activity',
        label: 'API execution mode',
        baseText: 'sync',
        currentText: jobSteps.length ? jobSteps.map(s => `${s.id || '?'}: ${s.mode}`).join(', ') : 'sync',
        diff: jobSteps.length > 0,
      },
      {
        key: 'output',
        icon: 'file-text',
        label: 'API output mapping',
        baseText: 'raw reply',
        currentText: outputSteps.length ? outputSteps.map(s => `${s.id || '?'} (${Object.keys(s.output!).length})`).join(', ') : 'raw reply',
        diff: outputSteps.length > 0,
      },
      {
        key: 'host',
        icon: 'panels-top-left',
        label: 'Script host placement',
        baseText: 'auto',
        currentText: hostSteps.length ? hostSteps.map(s => `${s.id || '?'} → ${s.host}`).join(', ') : 'auto',
        diff: hostSteps.length > 0,
      },
      {
        key: 'timeout_ms',
        icon: 'clock',
        label: 'Script timeout',
        baseText: 'default',
        currentText: timeoutSteps.length ? timeoutSteps.map(s => `${s.id || '?'}: ${duration(s.timeout_ms! / 1000).text}`).join(', ') : 'default',
        diff: timeoutSteps.length > 0,
      },
    ];

    if (props.text !== def.text) {
      const saved = parseJsonObj<PipelineJson>(def.text);
      const savedSteps = stepArrayOf(saved?.steps);
      const savedById = new Map(savedSteps.map(s => [s.id ?? '', JSON.stringify(s)]));
      const curById = new Map(steps.map(s => [s.id ?? '', JSON.stringify(s)]));
      const changed = steps.filter(s => savedById.get(s.id ?? '') !== JSON.stringify(s)).length
        + savedSteps.filter(s => !curById.has(s.id ?? '')).length;
      if (changed > 0) {
        fields.unshift({
          key: 'draftSteps',
          icon: 'file-plus-2',
          label: 'Draft step edits',
          baseText: `r${def.head} (${savedSteps.length})`,
          currentText: `${steps.length} (${changed} changed)`,
          diff: true,
          draft: true,
        });
      }
    }

    return { p, def, steps, kinds, profiles, connections, fields };
  });

  return <Show when={model()}>{m => <>
    <DetailSection title="Identity">
      <KvRow
        icon="list-ordered"
        label="Steps"
        meta={<span class="mono">{m().steps.length}{m().kinds.length ? ` · ${m().kinds.join(', ')}` : ''}</span>}
      />
      <Show when={m().profiles.length > 0}><KvRow icon="bot" label="Agent profiles" meta={<span class="mono">{m().profiles.join(', ')}</span>} /></Show>
      <Show when={m().connections.length > 0}><KvRow icon="link" label="Connections" meta={<span class="mono">{m().connections.join(', ')}</span>} /></Show>
      <Show when={m().p.description}>{desc => <KvRow icon="info" label="Description" meta={desc()} />}</Show>
    </DetailSection>
    <DiffFromDefault fields={m().fields} base="pipeline" emptyText="Matches the standard plan and step defaults." />
  </>}</Show>;
}

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

function AgentFacts(props: { text: string; def: Get; agentById?: ReadonlyMap<string, DefinitionListItem> }) {
  const parsedProfile = createMemo<Partial<Profile> | null>(() => {
    try {
      const parsed: unknown = JSON.parse(props.text);
      return parsed && typeof parsed === 'object' && !Array.isArray(parsed) ? (parsed as Partial<Profile>) : null;
    } catch {
      return null; // keep null while JSON is invalid in draft
    }
  });

  const variantOf = createMemo(() => {
    const a = parsedProfile();
    return a && typeof a.variantOf === 'string' && a.variantOf && a.variantOf !== props.def.id ? a.variantOf : '';
  });
  const baseOp = useOp('definition_get', () => (variantOf() ? { kind: 'agent', id: variantOf() } : null));

  const model = createMemo(() => {
    const a = parsedProfile();
    if (!a) return null;
    const def = props.def;
    const vOf = variantOf();
    const baseProfile: Profile | null = vOf && baseOp.data?.content?.agent ? baseOp.data.content.agent : null;
    const listItem = props.agentById?.get(def.id);
    const baselineAgent = props.agentById?.get('baseline')?.agent;
    const roles = listItem?.agent?.roles ?? [];

    const curHarness = a.harness ?? baselineAgent?.harness ?? SYSTEM_AGENT_DEFAULTS.harness;
    const defaults = baseAgentDefaults(baseProfile, baselineAgent, curHarness);
    const baseHarness = defaults.harness;

    const fields: DiffFieldRow[] = [];

    fields.push({
      key: 'harness',
      icon: 'settings',
      label: 'Harness',
      baseText: baseHarness,
      currentText: curHarness,
      diff: curHarness !== baseHarness,
    });

    const curModel = typeof a.model === 'string' ? a.model : '';
    const baseModel = defaults.model;
    fields.push({
      key: 'model',
      icon: 'bot',
      label: 'Model',
      baseText: baseModel,
      currentText: curModel || 'unset',
      diff: !!curModel && curModel !== baseModel,
    });

    const curEffort = a.effort ?? (curHarness === SYSTEM_AGENT_DEFAULTS.harness ? defaults.effort : 'none');
    const baseEffort = defaults.effort;
    fields.push({
      key: 'effort',
      icon: 'gauge',
      label: 'Effort',
      baseText: baseEffort,
      currentText: curEffort,
      diff: curEffort !== baseEffort,
    });

    const curRole = a.instructionRole ?? defaultInstructionRole(curHarness);
    const baseRole = baseProfile ? (baseProfile.instructionRole ?? defaultInstructionRole(baseHarness)) : defaultInstructionRole(curHarness);
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
      currentText: fmtList(curConn),
      diff: !sameList(curConn, baseConn),
      note: curConn.length ? 'credentials stay in the connection' : undefined,
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

    const baseSourceLabel = baseProfile ? baseProfile.id : 'system';
    return { a, def, roles, fields, baseSourceLabel, loadingBase: !!vOf && !baseOp.data && !baseOp.error, vOf };
  });

  return <Show when={model()}>{m => <>
    <Show when={m().a.category || m().roles.length > 0 || m().a.variantOf || m().a.purpose || m().a.tags?.length}>
    <DetailSection title="Identity">
      <Show when={m().a.category || m().roles.length > 0}>
        <KvRow icon="layers-2" label="Role" meta={[m().a.category, ...m().roles].filter(Boolean).join(' · ')} />
      </Show>
      <Show when={m().a.variantOf}>{vo => (
        <KvRow
          icon="chart-no-axes-column"
          label="Variant of"
          meta={<span class="acts">
            <EntityCode code={vo()} id={vo()} kind="agent" cls="inline" />
            <Show when={m().a.experiment}>{exp => <EntityCode id={exp()} kind="experiment" label cls="inline" />}</Show>
          </span>}
        />
      )}</Show>
      <Show when={m().a.purpose}>{p => <KvRow icon="info" label="Purpose" meta={p()} />}</Show>
      <Show when={Array.isArray(m().a.tags) && m().a.tags!.length > 0}>
        <KvRow icon="layers-2" label="Tags" meta={<span class="tag-row"><For each={m().a.tags!}>{t => <span class="chip">{t}</span>}</For></span>} />
      </Show>
    </DetailSection>
    </Show>
    <DiffFromDefault
      fields={m().fields}
      base={m().baseSourceLabel}
      loadingText={m().loadingBase ? `Loading base profile ${m().vOf}…` : undefined}
      emptyText={`Matches the ${m().baseSourceLabel} defaults.`}
    />
  </>}</Show>;
}

function DiffTab(props: { kind: Kind; id: string; hasDraft: boolean; head: number }) {
  const diffOp = useOp('definition_diff', () => ({
    kind: props.kind,
    id: props.id,
    from: props.hasDraft ? { revision: props.head } : { repository: true },
    to: props.hasDraft ? { draft: true } : { revision: props.head },
  }));
  return <DetailSection title={props.hasDraft ? `Draft against r${props.head}` : `r${props.head} against the repository file`}>
    <Show when={!diffOp.error} fallback={<div class="error" role="alert">{diffOp.error}</div>}>
      <Show when={diffOp.data} fallback={<Empty text="Loading…" />}>{d => <DiffText label="Diff" text={d().unified} />}</Show>
    </Show>
  </DetailSection>;
}

function HistoryTab(props: { kind: Kind; id: string }) {
  const histOp = useOp('definition_history', () => ({ kind: props.kind, id: props.id }));
  return <DetailSection title="Revisions">
    <Show when={!histOp.error} fallback={<div class="error" role="alert">{histOp.error}</div>}>
      <Show when={histOp.data} fallback={<Empty text="Loading…" />}>{h => (
        <For each={h().revisions}>{r => (
          <KvRow
            icon={r.source === 'agent' ? 'bot' : r.source === 'repo' ? 'git-merge' : 'file-plus-2'}
            label={`r${r.revision} · ${r.message}`}
            meta={`${r.actor.person?.name ?? (r.actor.executionId ? 'agent' : 'Fleet')}${r.actor.kind === 'agent' && r.actor.directedBy ? ` for ${r.actor.directedBy.name}` : ''}`}
            value={<span class="acts"><CopyButton label={`sha256: ${r.contentSha}`} value={r.contentSha} iconOnly /><AgeChip t={Date.parse(r.at)} /></span>}
          />
        )}</For>
      )}</Show>
    </Show>
  </DetailSection>;
}

function EffectiveTab(props: { id: string; draft: boolean; version: string }) {
  const list = useOp('project_list', {});
  const projects = createMemo(() => projectOptions(list.data));
  const [chosen, setProject] = createSignal<string | null | undefined>(undefined);
  const project = createMemo(() => chosen() === undefined ? (projects()[0]?.id ?? '') : (chosen() ?? ''));
  const effective = useOp('agent_effective', () => (project() ? { id: props.id, projectId: project(), draft: props.draft } : null));
  const error = createMemo(() => list.error ?? effective.error);
  // The draft's text changed: read the effective settings again once typing pauses.
  createEffect(() => props.version, version => {
    if (!version) return;
    const t = setTimeout(() => void effective.reload(), 800);
    return () => clearTimeout(t);
  });
  return <>
    <Show when={projects().length > 0}>
      <DetailSection title="Project">
        <ProjectPick projects={projects()} value={project() || null} onChange={p => setProject(p)} />
      </DetailSection>
    </Show>
    <Show when={error()}>{err => <div class="error" role="alert">{err()}</div>}</Show>
    <Show when={!project() && projects().length > 0}><Empty text="Pick a project to preview effective layers." /></Show>
    <Show when={effective.data}>{e => <>
      <DetailSection title="Layers, in the order the agent receives them">
        <For each={e().layers}>{l => (
          <KvRow icon={l.name === 'role' ? 'bot' : l.name === 'project' ? 'folder-open' : 'file-plus-2'} label={l.name}
            meta={<span class="mono">{l.source}{l.revision ? ` · ${l.revision}` : ''}{l.fields.length ? ` · ${l.fields.join(', ')}` : ''}</span>} />
        )}</For>
      </DetailSection>
      <DetailSection title="Effective settings">
        <KvRow icon="settings" label="Harness" meta={<span class="mono">{e().result.harness ?? SYSTEM_AGENT_DEFAULTS.harness}</span>} />
        <KvRow icon="bot" label="Model" meta={<span class="mono">{e().result.model}{e().result.effort ? ` · ${e().result.effort}` : ''}</span>} />
      </DetailSection>
    </>}</Show>
  </>;
}

function CompatibilityTab(props: { id: string }) {
  const compatOp = useOp('agent_compatibility', () => ({ id: props.id }));
  return <DetailSection title="Combinations seen in 30 days">
    <Show when={!compatOp.error} fallback={<div class="error" role="alert">{compatOp.error}</div>}>
      <Show when={compatOp.data} fallback={<Empty text="Loading…" />}>{c => (
        <For each={c().rows}>{r => (
          <KvRow icon={r.state === 'checked' ? 'circle-check' : r.state === 'failed' ? 'circle-x' : 'circle-dashed'}
            label={`${r.harness}${r.harnessVersion ? ' ' + r.harnessVersion : ''} · ${r.model}${r.effort ? ' · ' + r.effort : ''}`}
            meta={<span class="mono">{r.state === 'unchecked' ? 'not checked' : r.state}{r.revision ? ` · r${r.revision}` : ''}{r.error ? ` · ${r.error}` : ''}</span>}
            value={r.evidence ? <AgeChip t={Date.parse(r.evidence.at)} /> : undefined} />
        )}</For>
      )}</Show>
    </Show>
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
  const list = useOp('project_list', {});
  const projects = createMemo(() => projectOptions(list.data));
  const [chosen, setProject] = createSignal<string | null | undefined>(undefined);
  const project = createMemo(() => chosen() === undefined ? (projects()[0]?.id ?? '') : (chosen() ?? ''));
  const [search, setSearch] = createSignal('');
  const registry = useOp('lab_registry', {});
  const useCases = createMemo(() => registry.data?.useCases ?? []);
  const [selectedUseCases, setSelectedUseCases] = createSignal<string[]>([]);
  const useCasePins = usePins('catalogue-use-cases');
  const [selected, setSelected] = createSignal<string[]>([]);
  const [baseline, setBaseline] = createSignal('');
  const [busy, setBusy] = createSignal(false);
  const [failure, setError] = createSignal<string | null>(null);
  const catalogue = useOp('catalogue', () => (project() ? { projectId: project() } : null));
  const error = createMemo(() => failure() ?? list.error ?? registry.error);
  // Evidence decides a recommendation: after a project is picked only an answer read since then counts, never the copy
  // held from an earlier visit. An answer for another project (a mismatch) is never shown as this project's evidence.
  let pickedProject: string | null = null;
  let pickedHeld: unknown = null;
  const c = createMemo(() => {
    const p = project();
    const candidate = catalogue.data;
    if (pickedProject !== p) { pickedProject = p; pickedHeld = candidate; }
    return candidate && candidate !== pickedHeld && candidate.projectId === p && !catalogue.error ? candidate : null;
  });
  const methods = createMemo(() => c()?.methods ?? []);

  const useCaseTree = createMemo(() =>
    buildUseCaseTree(useCases().filter(u => isArchivedUseCase(u) || u.lab_fit !== 'none'), {
      pinnedIds: useCasePins.ids,
      separateArchived: true,
      eligibleOnly: true,
    }),
  );

  const filteredMethods = createMemo(() => {
    const q = search();
    return methods().filter(m => {
      const mx = m as CatalogueMethodExt;
      return matchSearch(q, `${m.name} ${m.id} ${mx.capability ?? ''} ${mx.description ?? ''} ${mx.status ?? ''}`);
    });
  });

  const choose = (id: string) => {
    const curC = c();
    if (!curC || catalogue.restored || busy()) return;
    const curSel = selected();
    const next = curSel.includes(id) ? curSel.filter(x => x !== id) : [...curSel, id];
    setSelected(next);
    if (!next.includes(baseline())) setBaseline(next.includes(String(curC.recommendedMethod)) ? String(curC.recommendedMethod) : '');
  };
  const act = async (run: () => Promise<void>) => { setBusy(true); try { await run(); setError(null); } catch (x) { setError(errorText(x)); } finally { setBusy(false); } };
  return <>
    <DetailSection title="Project">
      <ProjectPick
        projects={projects()}
        value={project() || null}
        onChange={p => { setProject(p); setSelected([]); setBaseline(''); setSelectedUseCases([]); setError(null); }}
      />
    </DetailSection>
    <Show when={error()}>{err => <div class="error" role="alert">{err()}</div>}</Show>
    <Show when={catalogue.error}>{err => <div class="error" role="alert">Catalogue unavailable: {err()}. No current evidence loaded for {project()}.<Button label="Retry Catalogue" icon="rotate-cw" onClick={catalogue.reload} /></div>}</Show>
    <Show when={c()}>{cur => <p class="muted">Evidence project: {cur().projectId}{catalogue.restored ? ' · restored, read-only until current confirmation' : ''}</p>}</Show>
    <DetailSection title="Methods"><SearchField label="Find method" value={search()} onInput={setSearch} onSearch={() => {}} />
      <For each={filteredMethods()}>{m => {
        const mx = m as CatalogueMethodExt;
        const a = m.assessment;
        return <div>
          <KvRow icon="circle-check" label={`${m.name} · ${m.id}`}
            meta={<span class="tag-row">
              <span>{m.scope} · {m.pipeline}</span>
              {mx.capability && <span class="chip">{mx.capability}</span>}
              {mx.status && <span class={`badge t-${methodStatusTone(mx.status)}`}><Icon name={methodStatusIcon(mx.status)} /><span>{mx.status}</span></span>}
            </span>}
            value={<span class="acts">
              <RowCheck checked={selected().includes(m.id)} label={`Select ${m.name}`} onToggle={() => choose(m.id)} />
              <Button label="Recommend for project" icon="check" iconOnly busy={busy()} disabledReason={catalogue.restored ? 'Await current scoped Catalogue read' : undefined} onClick={() => act(async () => { await op('recommendation_set', { projectId: project(), methodId: m.id }); void catalogue.reload(); })} />
            </span>} />
          {mx.description && <p class="muted">{mx.description}</p>}
          <KvRow icon="chart-no-axes-column" label={a.scoreForm === 'typed' ? 'Comparison score' : 'Quality'} meta={a.score == null ? 'Unavailable' : `${a.score.toFixed(3)}${a.partial ? ' · partial/incomplete' : ''}`} value={`${a.basis} · ${a.eligibleCells ?? 0}/${a.expectedCells ?? 0} cells · ${a.examples ?? 0} ${a.examples === 1 ? 'example' : 'examples'}`} />
          <KvRow
            icon="clock"
            label="Time / cost"
            meta={a.wallMsMedian == null ? 'Time unavailable' : <><Num m={msMeasured(a.wallMsMedian, 'measured')} kind="duration" /> · {a.durationSamples == null ? 'pipeline usage window' : `${a.durationSamples} samples`}</>}
            value={a.costUsd == null ? 'Cost unavailable' : <Num m={{ value: a.costUsd, currency: 'USD', basis: costBasisOf(a.costBasis) }} kind="money" />}
          />
          <KvRow icon="folder-open" label="Recommended for" meta={m.recommendedFor.join(', ') || 'None'} />
          {a.state && <p class="muted">{a.state} · {a.stopReason ?? ''} · {a.failedCells ?? 0} failed · {a.excludedCells ?? 0} excluded</p>}
          {a.reason && <p>{a.reason}</p>}{a.exclusions && <p>{a.exclusions}</p>}
          {a.evaluator && <p class="muted">{a.variant} · {a.split} · {a.evaluator} · {a.evaluatorVersion}</p>}
          {a.experimentId && <KvRow icon="chart-no-axes-column" label="Assessment" value={<EntityCode id={a.experimentId} kind="experiment" />} />}
          {a.sourceExperimentId && <KvRow icon="chart-no-axes-column" label="Source" value={<EntityCode id={a.sourceExperimentId} kind="experiment" />} />}
          {a.ongoingExperimentId && <KvRow icon="chart-no-axes-column" label="Current" meta={a.ongoingState ? String(a.ongoingState) : undefined} value={<EntityCode id={a.ongoingExperimentId} kind="experiment" />} />}
        </div>;
      }}</For>
    </DetailSection>
    <DetailSection title="Compare methods in Lab">
      <Choice label="Baseline" value={baseline()} options={methods().filter(m => selected().includes(m.id)).map(m => ({ id: m.id, label: m.name }))} onChange={setBaseline} />
      <EntityPicker
        mode="multiple"
        label="Use cases"
        nodes={useCaseTree().nodes}
        archivedNodes={useCaseTree().archivedNodes}
        value={selectedUseCases()}
        onChange={setSelectedUseCases}
        onPin={node => useCasePins.toggle(node.id)}
        placeholder="Select use cases"
        searchPlaceholder="Find use case"
        loading={!registry.data && !registry.error}
        error={registry.error}
        onRetry={registry.reload}
        disabledReason={catalogue.error || catalogue.restored || !c() ? 'Await current scoped Catalogue read' : undefined}
      />
      <Button
        label={selectedUseCases().length > 1 ? `Create ${selectedUseCases().length} comparisons` : 'Create comparison'}
        icon="play"
        busy={busy()}
        disabledReason={catalogue.error || catalogue.restored || !c() ? 'Await current scoped Catalogue read' : !project() || selected().length < 2 || !baseline() || !selected().includes(baseline()) || selected().some(id => !methods().some(m => m.id === id)) || selectedUseCases().length === 0 ? 'Select two methods, a baseline and use case' : undefined}
        onClick={() => act(async () => {
          const curC = c(), curProj = project(), curSel = selected(), curBase = baseline(), curUcs = selectedUseCases();
          const results = await Promise.all(
            curUcs.map(uc =>
              op('lab_compare_methods', { projectId: curProj, capability: String(curC?.capability), methods: curSel, baseline: curBase, useCase: uc }),
            ),
          );
          if (results[0]?.experimentId) go(`/lab/${results[0].experimentId}`);
        })}
      />
    </DetailSection>
  </>;
}

function costBasisOf(b: string | null | undefined): CostFigureLike['basis'] {
  return b === 'measured' || b === 'estimated' || b === 'subscription' || b === 'unavailable' ? b : 'estimated';
}

function UsageTab(props: { kind: 'agent' | 'pipeline'; id: string }) {
  const [, go] = useLocation();
  const [days, setDays] = createSignal<'7' | '30'>('30');
  const usageOp = useOp('definition_usage', () => ({ kind: props.kind, id: props.id, days: Number(days()) }));
  const windowCost = (w: Ops['definition_usage']['reply']['total']): readonly CostFigureLike[] =>
    w.cost?.length ? w.cost : w.costUsd != null ? [{ value: w.costUsd, currency: 'USD', basis: costBasisOf(w.costBasis), ...(w.costBasis === 'tokens' ? { reason: 'tokens' } : {}) }] : [];
  const totalItems = createMemo<TotalItem[]>(() => {
    const u = usageOp.data;
    if (!u) return [];
    const totalCost = windowCost(u.total);
    const hasCost = totalCost.some(c => (c.value !== null && c.basis !== 'unavailable') || c.basis === 'subscription');
    const items: TotalItem[] = [
      { label: 'Runs', m: { value: u.total.runs, basis: 'measured' }, kind: 'count', note: u.total.runs > 0 ? `${u.total.succeeded} succeeded` : undefined },
      ...(u.total.resources ? resourceTotals(u.total.resources, { extraCost: totalCost, hideUnavailable: true }) : [
        ...(u.total.wallMsMedian != null ? [{ label: 'Median wall', m: msMeasured(u.total.wallMsMedian, 'measured'), kind: 'duration' as const }] : []),
        ...(u.total.tokens > 0 || u.total.runs === 0 ? [{ label: 'Tokens', m: { value: u.total.tokens, basis: 'measured' as const }, kind: 'count' as const }] : []),
        ...(hasCost ? [{ label: 'Cost', cost: totalCost, showUnavailable: false }] : []),
      ]),
    ];
    if (u.total.resources && u.total.wallMsMedian != null) {
      items.splice(1, 0, { label: 'Median wall', m: msMeasured(u.total.wallMsMedian, u.total.resources.durations.basis === 'unavailable' && u.total.wallMsMedian > 0 ? 'measured' : u.total.resources.durations.basis), kind: 'duration' });
    }
    return items;
  });
  const routeFor = (k: string) => k === 'agent' ? '/agents' : k === 'pipeline' ? '/pipelines' : '/catalogue';
  return <>
    <div class="res-bar">
      <Choice label="Usage window" value={days()} onChange={v => setDays(v as '7' | '30')} options={[{ id: '7', label: '7 days' }, { id: '30', label: '30 days' }]} />
      <span class="grow" />
      <Button label="Projects analytics" icon="external-link" kind="quiet" onClick={() => go('/projects')} />
    </div>
    <Show when={!usageOp.error} fallback={<DetailSection title="Usage"><div class="error" role="alert">{usageOp.error}</div></DetailSection>}>
      <Show when={usageOp.data} fallback={<DetailSection title="Usage"><Empty text="Loading…" /></DetailSection>}>{u => <>
        <DetailSection title={`Summary (${days()} days)`}><Totals items={totalItems()} /></DetailSection>
        <DetailSection title="Used by">
          <Show when={u().usedBy.length} fallback={<span class="muted">not referenced by other definitions</span>}>
            <For each={u().usedBy}>{x => (
              <KvRow
                icon={x.kind === 'pipeline' ? 'network' : x.kind === 'agent' ? 'bot' : 'rows-3'}
                label={x.kind}
                meta={x.kind === 'agent' || x.kind === 'pipeline' || x.kind === 'project'
                  ? <EntityCode code={x.id} id={x.id} kind={x.kind} cls="inline" />
                  : <a class="row-link" href={`${routeFor(x.kind)}/${encodeURIComponent(x.id)}`} onClick={e => { e.preventDefault(); go(`${routeFor(x.kind)}/${encodeURIComponent(x.id)}`); }}>{x.id}</a>}
              />
            )}</For>
          </Show>
        </DetailSection>
        <Show when={u().byRevision.length > 0}>
          <DetailSection title="By revision">
            <DataTable label="By revision" head={<tr><th scope="col">Revision</th><th scope="col">Runs</th><th scope="col">Succeeded</th><th scope="col">Median wall</th><th scope="col">Tokens</th><th scope="col">Cost</th></tr>}>
              <For each={u().byRevision}>{rv => {
                const w = rv.window;
                const cost = windowCost(w);
                const tokRes = w.resources?.tokens;
                const tokM = tokRes
                  ? (tokRes.basis === 'unavailable' ? (tokRes.total && tokRes.total > 0 ? { value: tokRes.total, basis: 'measured' as const, partial: 'incomplete telemetry' } : { value: null, basis: 'unavailable' as const }) : { value: tokRes.total, basis: tokRes.basis })
                  : (w.tokens === 0 && w.runs > 0 ? { value: null, basis: 'unavailable' as const } : { value: w.tokens, basis: 'measured' as const });
                const wallBasis = w.resources?.durations.basis === 'unavailable' && (w.wallMsMedian ?? 0) > 0 ? 'measured' : (w.resources?.durations.basis ?? 'measured');
                return <tr>
                  <th scope="row" class="mono">r{rv.revision}</th>
                  <td class="num"><Num m={{ value: w.runs, basis: 'measured' }} kind="count" /></td>
                  <td class="num"><Num m={{ value: w.succeeded, basis: 'measured' }} kind="count" /></td>
                  <td class="num">{w.wallMsMedian != null ? <Num m={msMeasured(w.wallMsMedian, wallBasis)} kind="duration" /> : <span class="muted">—</span>}</td>
                  <td class="num"><Num m={tokM} kind="count" /></td>
                  <td class="num"><CostFigures cost={cost} showUnavailable={false} empty="—" /></td>
                </tr>;
              }}</For>
            </DataTable>
          </DetailSection>
        </Show>
      </>}</Show>
    </Show>
  </>;
}
