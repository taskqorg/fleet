// Lab draft creation uses the existing operation, never launch (laboratory.md, Create experiment).
import { useEffect, useMemo, useRef, useState } from 'preact/hooks';
import { useLocation, useSearch } from 'wouter-preact';
import {
  StatusLine, Button, Choice, CodeEditor, DetailSection, Disclosure, EntityPicker,
  ProblemRow, TextField, harnesses, type CatalogueTreeNode, type StateOf,
} from '../../../packages/ui/src/index.ts';
import { op, online, signedOut, OpError, type Ops } from '../api.ts';
import { useOp } from '../store.ts';
import { access, accessGeneration, isAccessCurrent } from '../access.ts';
import { onClear } from '../offline.ts';
import { buildUseCaseTree, matchingExamplesForUseCase, useCaseSlug, type RegistryUseCase } from './use-cases.ts';
import type { DefinitionListItem } from '../../../contracts/generated/ops.ts';

type Variant = { label: string; pipelineId: string; overrides: string };
type Evaluator = { kind: string; ref: string; form: string; overrides: string };
type Budget = { unit: string; limit: string; action: string };
type Draft = { useCaseId?: string; projectId: string; pipelineId: string; kind: string; examples: string[]; repeats: string;
  goal: string; hypothesis: string; decisionRule: string; labGroup: string; variants: Variant[]; evaluators: Evaluator[];
  primary: string; budgets: Budget[]; generator: string; params: string; max: string; k: string };
type Receipt = { draft: Draft; state: 'editing' | 'pending' | 'uncertain' | 'created'; id?: string; message?: string };
const PREFIX = 'fleet-lab-create:';
const blank = (): Draft => ({ useCaseId: '', projectId: '', pipelineId: '', kind: '', examples: [], repeats: '1', goal: '', hypothesis: '', decisionRule: '', labGroup: '',
  variants: [{ label: 'A', pipelineId: '', overrides: '{}' }], evaluators: [], primary: '', budgets: [], generator: 'list', params: '{"variants":[]}', max: '', k: '' });

const defaultEvaluatorForUseCase = (u?: RegistryUseCase | null): Evaluator => {
  const first = (u?.checks?.[0] ?? null) as { kind?: string; ref?: string; metric?: string; form?: string } | null;
  const kind = first?.kind || 'script';
  const ref = first?.ref || 'example.checks';
  const form = first?.form || first?.metric || 'metric';
  return { kind, ref, form, overrides: '{}' };
};

const defaultProseForUseCase = (u: Pick<RegistryUseCase, 'id' | 'goal'>) => ({
  goal: u.goal,
  hypothesis: `Comparing variants on ${u.id} will show measurable differences in quality, wall time, or cost.`,
  decisionRule: 'Adopt the highest-scoring variant on the primary evaluator; break ties by lower cost and median wall time.',
});

const prefillForUseCase = (useCaseId: string, u?: RegistryUseCase | null): Draft => {
  const ev = defaultEvaluatorForUseCase(u);
  const prose = u ? defaultProseForUseCase(u) : { goal: '', hypothesis: '', decisionRule: '' };
  return {
    ...blank(),
    useCaseId,
    kind: 'ab',
    labGroup: useCaseSlug(useCaseId),
    variants: [
      { label: 'A', pipelineId: '', overrides: '{}' },
      { label: 'B', pipelineId: '', overrides: '{}' },
    ],
    evaluators: [ev],
    primary: `${ev.kind}:${ev.ref}:${ev.form}`,
    ...prose,
  };
};

const parseParamObj = (raw: string): Record<string, unknown> => {
  try {
    const parsed = JSON.parse(raw);
    if (parsed && typeof parsed === 'object' && !Array.isArray(parsed)) return { ...parsed };
  } catch { /* fallback */ }
  return {};
};

const profileIdOfParams = (raw: string): string | null => {
  const p = parseParamObj(raw).profile;
  return typeof p === 'string' && p.trim() ? p.trim() : null;
};

const withProfileParam = (raw: string, agent: DefinitionListItem | null): string => {
  const obj = parseParamObj(raw);
  if (!agent) {
    delete obj.profile;
    delete obj.model;
  } else {
    obj.profile = agent.id;
    if (agent.agent?.model) obj.model = agent.agent.model;
    else delete obj.model;
  }
  return JSON.stringify(obj);
};

const compatibilityState = (c?: string): StateOf<'definition'> =>
  c === 'checked' ? 'ready' : c === 'failed' ? 'incompatible' : 'unchecked';

function buildAgentTree(agents: readonly DefinitionListItem[]): { nodes: CatalogueTreeNode[]; archivedNodes: CatalogueTreeNode[] } {
  const byCategory = new Map<string, DefinitionListItem[]>();
  const variantAgents: DefinitionListItem[] = [];
  for (const a of agents) {
    if (a.agent?.variantOf) {
      variantAgents.push(a);
      continue;
    }
    const cat = a.agent?.category || 'Agents';
    const list = byCategory.get(cat);
    if (list) list.push(a);
    else byCategory.set(cat, [a]);
  }
  const toLeaf = (a: DefinitionListItem): CatalogueTreeNode => {
    const h = a.agent?.harness;
    const hLabel = h && harnesses[h] ? harnesses[h].label : h;
    return {
      id: a.id,
      label: a.name,
      sub: [hLabel, a.agent?.model].filter(Boolean).join(' · ') || a.id,
      state: { family: 'definition', state: compatibilityState(a.compatibility) },
      searchText: [a.id, a.name, h, hLabel, a.agent?.model, a.agent?.category, a.agent?.purpose].filter(Boolean).join(' '),
    };
  };
  const nodes: CatalogueTreeNode[] = [...byCategory.entries()]
    .sort((a, b) => a[0].localeCompare(b[0]))
    .map(([cat, list]) => ({
      id: `agent-cat:${cat}`,
      label: cat,
      icon: 'bot',
      defaultCollapsed: false,
      children: list.toSorted((a, b) => a.name.localeCompare(b.name)).map(toLeaf),
    }));
  const archivedNodes: CatalogueTreeNode[] = variantAgents.length
    ? [{ id: 'agent-cat:variants', label: 'Lab variants', icon: 'git-merge', defaultCollapsed: false, children: variantAgents.map(toLeaf) }]
    : [];
  return { nodes, archivedNodes };
}

const object = (text: string, label: string): Record<string, unknown> => {
  let x: unknown; try { x = JSON.parse(text); } catch (e) { throw Error(`${label}: ${(e as Error).message}`); }
  if (!x || typeof x !== 'object' || Array.isArray(x)) throw Error(label + ' must be an object');
  return x as Record<string, unknown>;
};
const message = (e: unknown) => e instanceof Error ? e.message : String(e);
const nameOf = (e: Evaluator) => `${e.kind}:${e.ref}:${e.form}`;
function request(d: Draft): Ops['lab_experiment_create']['request'] {
  if (!d.projectId || !d.pipelineId || !d.kind || !d.examples.length) throw Error('Choose a project, pipeline, kind and approved examples.');
  const repeats = Number(d.repeats);
  if (!Number.isSafeInteger(repeats) || repeats < 1) throw Error('Repeats must be a positive integer.');
  if (!d.variants.length || (d.kind === 'single' && d.variants.length !== 1) || (d.kind === 'ab' && d.variants.length < 2)) throw Error('Single needs one variant; A/B needs at least two.');
  if (d.variants.some(v => !v.label.trim()) || new Set(d.variants.map(v => v.label)).size !== d.variants.length) throw Error('Variant labels must be nonempty and unique.');
  const evaluators = d.evaluators.map((e, i) => ({ kind: e.kind, ref: e.ref, form: e.form, ...(e.kind === 'judge' ? { overrides: object(e.overrides, `Judge ${i + 1} parameters`) } : {}) }));
  if (d.primary && !d.evaluators.some(e => nameOf(e) === d.primary)) throw Error('Choose a primary from the current evaluators.');
  const budgets = d.budgets.map(b => { const limit = Number(b.limit); if (!Number.isFinite(limit) || limit <= 0) throw Error('Budget limits must be positive.'); return { unit: b.unit, limit, action: b.action }; });
  const out: Ops['lab_experiment_create']['request'] = { projectId: d.projectId, pipelineId: d.pipelineId, kind: d.kind, examples: d.examples, repeats,
    goal: d.goal, hypothesis: d.hypothesis, decisionRule: d.decisionRule, labGroup: d.labGroup,
    variants: d.variants.map((v, i) => ({ label: v.label, ...(v.pipelineId ? { pipelineId: v.pipelineId } : {}), overrides: object(v.overrides, `Variant ${i + 1} parameters`) })), evaluators, budgets,
    ...(d.primary ? { primaryEvaluator: d.primary } : {}) };
  if (d.kind === 'loop') {
    if (!d.primary || d.primary.endsWith(':pick_best')) throw Error('Loop needs a scalar primary evaluator.');
    const positive = (x: string) => { const n = Number(x); if (!Number.isSafeInteger(n) || n < 1) throw Error('Loop stop values must be positive integers.'); return n; };
    out.strategy = { generator: { kind: d.generator as 'list' | 'grid' | 'propose', params: object(d.params, 'Generator parameters') }, allocator: { kind: 'full', params: {} },
      ...(d.max ? { max_iterations: positive(d.max) } : {}), ...(d.k ? { no_gain: { k: positive(d.k) } } : {}) };
  }
  return out;
}

export function LabCreate({ onClose }: { onClose?: () => void } = {}) {
  const [, go] = useLocation();
  const close = onClose ?? (() => go('/lab'));
  const search = useSearch();
  const requestedUseCase = new URLSearchParams(search).get('useCase')?.trim() ?? '';
  const prefillPending = useRef({ registry: false, project: false, pipeline: false, examples: false });
  const resetPrefill = (on: boolean) => {
    prefillPending.current = { registry: on, project: on, pipeline: on, examples: on };
  };
  const [receipt, setReceipt] = useState<Receipt>({ draft: blank(), state: 'editing' });
  const [key, setKey] = useState(''), [error, setError] = useState(''), [storageWarning, setStorageWarning] = useState('');
  const [proseKey, setProseKey] = useState(0), [proseOpen, setProseOpen] = useState(!requestedUseCase);
  const generation = useRef(accessGeneration()).current;
  const live = useRef(true), identity = useRef(''), current = useRef(receipt), busy = useRef(false);
  current.current = receipt;
  const seedDraft = (draft: Draft) => { if (!live.current || !isAccessCurrent(generation)) return; const next: Receipt = { draft, state: 'editing' }; current.current = next; setReceipt(next); };
  const persist = (next: Receipt) => { if (!live.current || !isAccessCurrent(generation)) return; current.current = next; if (live.current) setReceipt(next); if (identity.current) try { sessionStorage.setItem(identity.current, JSON.stringify(next)); } catch { if (live.current) setStorageWarning('Browser storage is unavailable. Keep this page open until creation is resolved.'); } };
  const clearAccess = () => {
    // Preserve authored input under its original actor; server receipts require fresh readback.
    if (identity.current) try { sessionStorage.setItem(identity.current, JSON.stringify({ draft: current.current.draft, state: current.current.state === 'editing' ? 'editing' : 'uncertain', message: current.current.state === 'editing' ? undefined : 'Previous creation outcome requires review; do not resend.' })); } catch { /* input still existed in this view */ }
    identity.current = ''; busy.current = true; resetPrefill(false); setKey('');
    current.current = { draft: blank(), state: 'editing' }; setReceipt(current.current);
    setError('Access changed. Reload before creating a draft.');
  };
  useEffect(() => {
    live.current = true;
    const cleared = clearAccess;
    onClear.add(cleared);
    (async () => {
      try {
        const actor = access.value.identity?.identityKey;
        if (!actor || !isAccessCurrent(generation)) return;
        const k = PREFIX + JSON.stringify([1, actor]); identity.current = k;
        let saved: Receipt | null = null; try { saved = JSON.parse(sessionStorage.getItem(k) ?? 'null'); } catch { /* start an empty draft */ }
        const canRestoreSaved = !!saved?.draft && typeof saved.draft.projectId === 'string' && typeof saved.draft.pipelineId === 'string' && typeof saved.draft.repeats === 'string' && Array.isArray(saved.draft.examples) && Array.isArray(saved.draft.variants) && Array.isArray(saved.draft.evaluators) && Array.isArray(saved.draft.budgets) && ['editing', 'pending', 'uncertain', 'created'].includes(saved.state) && (saved.state !== 'editing' || !requestedUseCase || saved.draft.useCaseId === requestedUseCase);
        if (canRestoreSaved && saved) {
          if (saved.state === 'pending') saved = { ...saved, state: 'uncertain', message: 'The previous request has no confirmed reply. Review experiments before starting another draft.' };
          if (saved.state === 'created') saved = saved.id ? { ...saved, message: 'Creation confirmed. Read the retained details to continue.' } : { ...saved, state: 'uncertain', message: 'The saved reply has no experiment identity. Review experiments.' };
          if (saved.draft.projectId) {
            try { await op('project_detail', { projectId: saved.draft.projectId }); }
            catch (e) { if (e instanceof OpError && [404, 503].includes(e.status)) await op('project_context', { projectId: saved.draft.projectId }); else throw e; }
          }
          if (!isAccessCurrent(generation) || !live.current) return;
          persist({ draft: saved.draft, state: saved.state === 'editing' ? 'editing' : 'uncertain', ...(saved.state !== 'editing' ? { message: 'Previous creation outcome requires review; do not resend.' } : {}) });
        } else if (requestedUseCase) {
          resetPrefill(true);
          seedDraft(prefillForUseCase(requestedUseCase));
        }
        busy.current = false; setKey(k); setError('');
      } catch (e) { if (live.current) setError(message(e)); }
    })();
    return () => { live.current = false; identity.current = ''; onClear.delete(cleared); };
  }, []);
  const d = receipt.draft;
  // The choices: read once this person's draft is restored (key), the pipelines of the chosen project.
  const projectList = useOp('project_list', key ? {} : null);
  const exampleList = useOp('lab_examples_list', key ? { state: 'approved' } : null);
  const registryList = useOp('lab_registry', key ? {} : null);
  const agentList = useOp('definition_list', key ? { kind: 'agent' } : null);
  const pipelineList = useOp('definition_list', key && d.projectId ? { kind: 'pipeline', projectId: d.projectId } : null);

  const projectNodes = useMemo<CatalogueTreeNode[]>(
    () => (key ? (projectList.data?.projects ?? []) : []).map(p => {
      const pid = String(p.id);
      const pname = String(p.name ?? p.id);
      return {
        id: pid,
        label: pname,
        sub: pname !== pid ? pid : undefined,
        icon: 'folder-open',
        searchText: `${pid} ${pname}`,
      };
    }),
    [key, projectList.data],
  );
  const exampleNodes = useMemo<CatalogueTreeNode[]>(
    () => (key ? (exampleList.data?.examples ?? []) : []).map(e => ({
      id: e.artifactId,
      label: e.name,
      sub: e.useCaseId ?? e.artifactId,
      icon: 'file-text',
      searchText: `${e.name} ${e.artifactId} ${e.useCaseId ?? ''}`,
    })),
    [key, exampleList.data],
  );
  const pipelineItems = useMemo(
    () => (key && d.projectId ? (pipelineList.data?.items ?? []).filter(p => p.revision > 0) : []),
    [key, d.projectId, pipelineList.data],
  );
  const pipelineNodes = useMemo<CatalogueTreeNode[]>(
    () => pipelineItems.map(p => ({
      id: p.id,
      label: p.name,
      sub: `${p.id} · r${p.revision}`,
      icon: 'network',
      state: { family: 'definition', state: p.draft ? 'draft' : 'ready' },
      searchText: `${p.id} ${p.name}`,
    })),
    [pipelineItems],
  );
  const primaryEvaluatorNodes = useMemo<CatalogueTreeNode[]>(
    () => d.evaluators.map(e => {
      const n = nameOf(e);
      return { id: n, label: n, icon: 'sliders-horizontal', searchText: n };
    }),
    [d.evaluators],
  );

  const useCases: RegistryUseCase[] = useMemo(() => registryList.data?.useCases ?? [], [registryList.data]);
  const agentItems = useMemo(() => agentList.data?.items ?? [], [agentList.data]);
  const { nodes: useCaseNodes, archivedNodes: archivedUseCaseNodes } = useMemo(
    () => buildUseCaseTree(useCases, { separateArchived: true, eligibleOnly: true }),
    [useCases],
  );
  const { nodes: agentNodes, archivedNodes: archivedAgentNodes } = useMemo(() => buildAgentTree(agentItems), [agentItems]);

  useEffect(() => {
    if (!key || busy.current || !live.current || !isAccessCurrent(generation)) return;
    if (current.current.state !== 'editing') return;
    const cur = current.current.draft;
    const targetUseCase = cur.useCaseId || requestedUseCase;
    if (!targetUseCase) return;
    const pending = prefillPending.current;
    if (!pending.registry && !pending.project && !pending.pipeline && !pending.examples) return;
    const patch: Partial<Draft> = {};
    if (pending.registry && registryList.data) {
      pending.registry = false;
      const u = registryList.data.useCases.find(x => x.id === targetUseCase);
      if (u && (!cur.goal || !cur.hypothesis || !cur.decisionRule)) {
        const prose = defaultProseForUseCase(u);
        const ev = defaultEvaluatorForUseCase(u);
        if (!cur.goal) patch.goal = prose.goal;
        if (!cur.hypothesis) patch.hypothesis = prose.hypothesis;
        if (!cur.decisionRule) patch.decisionRule = prose.decisionRule;
        patch.evaluators = [ev];
        patch.primary = `${ev.kind}:${ev.ref}:${ev.form}`;
      }
    }
    if (pending.project && projectList.data) {
      pending.project = false;
      if (!cur.projectId) {
        const ps = projectList.data.projects;
        const preferred = ps.find(p => String(p.id) === 'fleet') ? 'fleet' : (ps.length === 1 ? String(ps[0].id) : '');
        if (preferred) patch.projectId = preferred;
      }
    }
    if (pending.pipeline && pipelineList.data && cur.projectId) {
      pending.pipeline = false;
      if (!cur.pipelineId) {
        const pips = pipelineList.data.items.filter(p => p.revision > 0);
        const preferred = pips.find(p => p.id === 'coding-task')?.id ?? (pips.length === 1 ? pips[0].id : '');
        if (preferred) patch.pipelineId = preferred;
      }
    }
    if (pending.examples && exampleList.data) {
      pending.examples = false;
      if (cur.examples.length === 0) {
        const matched = matchingExamplesForUseCase(exampleList.data.examples, targetUseCase).map(e => e.artifactId);
        if (matched.length) patch.examples = matched;
      }
    }
    if (!Object.keys(patch).length) return;
    let hasSaved = false;
    try { hasSaved = !!identity.current && sessionStorage.getItem(identity.current) !== null; } catch { /* ignore */ }
    if (hasSaved) persist({ draft: { ...cur, ...patch }, state: 'editing' });
    else seedDraft({ ...cur, ...patch });
  }, [key, exampleList.data, registryList.data, projectList.data, pipelineList.data]);

  const readError = projectList.error ?? exampleList.error ?? pipelineList.error ?? registryList.error ?? agentList.error;
  const edit = (change: Partial<Draft>) => { if (busy.current || current.current.state !== 'editing') return; persist({ draft: { ...current.current.draft, ...change }, state: 'editing' }); setError(''); };
  const applyUseCase = (useCaseId: string | null) => {
    if (!useCaseId) {
      resetPrefill(false);
      edit({ useCaseId: '' });
      return;
    }
    const u = useCases.find(x => x.id === useCaseId) ?? null;
    const slug = useCaseSlug(useCaseId);
    const ev = defaultEvaluatorForUseCase(u);
    const prose = u ? defaultProseForUseCase(u) : { goal: d.goal, hypothesis: d.hypothesis, decisionRule: d.decisionRule };
    const matched = matchingExamplesForUseCase(exampleList.data?.examples ?? [], useCaseId).map(e => e.artifactId);
    const ps = projectList.data?.projects ?? [];
    const nextProjectId = d.projectId || (ps.find(p => String(p.id) === 'fleet') ? 'fleet' : (ps.length === 1 ? String(ps[0].id) : ''));
    const nextPipelineId = d.pipelineId || (nextProjectId === d.projectId ? (pipelineItems.find(p => p.id === 'coding-task')?.id ?? (pipelineItems.length === 1 ? pipelineItems[0].id : '')) : '');
    if (nextProjectId && !nextPipelineId) prefillPending.current.pipeline = true;
    const nextVariants = d.variants.length >= 2
      ? d.variants
      : [{ label: 'A', pipelineId: '', overrides: '{}' }, { label: 'B', pipelineId: '', overrides: '{}' }];
    edit({
      useCaseId,
      labGroup: slug,
      kind: 'ab',
      variants: nextVariants,
      evaluators: [ev],
      primary: `${ev.kind}:${ev.ref}:${ev.form}`,
      ...(matched.length || d.useCaseId ? { examples: matched } : {}),
      ...(nextProjectId ? { projectId: nextProjectId } : {}),
      ...(nextPipelineId ? { pipelineId: nextPipelineId } : {}),
      ...prose,
    });
    setProseOpen(false);
    setProseKey(n => n + 1);
  };

  const variants = (i: number, change: Partial<Variant>) => edit({ variants: current.current.draft.variants.map((v, j) => j === i ? { ...v, ...change } : v) });
  const evaluators = (i: number, change: Partial<Evaluator>) => edit({ evaluators: current.current.draft.evaluators.map((v, j) => j === i ? { ...v, ...change } : v) });
  const budgets = (i: number, change: Partial<Budget>) => edit({ budgets: current.current.draft.budgets.map((v, j) => j === i ? { ...v, ...change } : v) });
  const readback = async (id: string, draft: Draft, k: string) => {
    try {
      const e = await op('lab_experiment_get', { experimentId: id });
      if (e.id !== id || e.projectId !== draft.projectId || e.pipelineId !== draft.pipelineId || e.state !== 'draft') throw Error('Created experiment readback differs. Review its retained record.');
      if (!isAccessCurrent(generation) || identity.current !== k) return;
      try { sessionStorage.removeItem(k); } catch { /* memory still keeps the receipt */ }
      if (live.current) go('/lab/' + encodeURIComponent(id));
    } catch (e) { if (isAccessCurrent(generation) && identity.current === k) persist({ draft, state: 'created', id, message: `Creation confirmed; details unavailable: ${message(e)}` }); }
  };
  const create = async () => {
    if (!key || busy.current || current.current.state !== 'editing' || !online.value || signedOut.value) return;
    const draft = current.current.draft; let body: Ops['lab_experiment_create']['request'];
    try { body = request(draft); } catch (e) { setError(message(e)); return; }
    busy.current = true; const k = identity.current; persist({ draft, state: 'pending' }); setError('');
    try {
      const e = await op('lab_experiment_create', body);
      if (typeof e.id !== 'string' || !e.id) throw Error('The create reply did not identify the experiment.');
      if (!isAccessCurrent(generation) || identity.current !== k) return;
      persist({ draft, state: 'created', id: e.id }); await readback(e.id, draft, k);
    } catch (e) {
      if (!isAccessCurrent(generation) || identity.current !== k) return;
      if (e instanceof OpError && [400, 403, 404, 409].includes(e.status)) { persist({ draft, state: 'editing' }); setError(e.message); }
      else persist({ draft, state: 'uncertain', message: 'Creation could not be confirmed. Do not repeat this request; review experiments. ' + message(e) });
    } finally { busy.current = false; }
  };
  let invalid = ''; try { request(d); } catch (e) { invalid = message(e); }
  const disabled = !key ? 'Loading identity and access' : receipt.state !== 'editing' ? 'Resolve the previous creation first' : !online.value ? 'offline' : signedOut.value ? 'signed out' : undefined;
  const field = (label: string, key: 'goal' | 'hypothesis' | 'decisionRule' | 'labGroup' | 'repeats' | 'max' | 'k', multiline = false) => <TextField label={label} value={d[key]} onChange={v => edit({ [key]: v })} multiline={multiline} type={['repeats', 'max', 'k'].includes(key) ? 'number' : 'text'} disabledReason={disabled} />;
  const options = (xs: string[]) => xs.map(id => ({ id, label: id }));
  const advanced = (label: string, value: string, change: (value: string) => void, help: string) => {
    let error = ''; try { object(value, label); } catch (e) { error = (e as Error).message; }
    return <Disclosure open={error ? true : undefined} summary={label}><p class="muted">{help}</p>
      <CodeEditor label={label} value={value} onChange={change} problems={error ? [{ path: '', message: error }] : []} disabledReason={disabled} /></Disclosure>;
  };

  const distinctHarnessAgents = useMemo(() => {
    const seen = new Set<string>();
    const out: DefinitionListItem[] = [];
    const preferred = agentItems.toSorted((a, b) => {
      const aWorker = a.agent?.category === 'worker' ? 0 : 1;
      const bWorker = b.agent?.category === 'worker' ? 0 : 1;
      return aWorker - bWorker || Number(!!a.agent?.variantOf) - Number(!!b.agent?.variantOf) || a.id.localeCompare(b.id);
    });
    for (const a of preferred) {
      const h = a.agent?.harness;
      if (!h || seen.has(h)) continue;
      seen.add(h);
      out.push(a);
    }
    return out;
  }, [agentItems]);
  const hasCodingPipelinePair = pipelineItems.some(p => p.id === 'coding-task') && pipelineItems.some(p => p.id === 'coding-task-reviewed');
  const applyAgentPairPreset = (pair: readonly DefinitionListItem[]) => {
    edit({
      kind: 'ab',
      variants: pair.map(a => ({
        label: a.id,
        pipelineId: '',
        overrides: withProfileParam('{}', a),
      })),
    });
  };
  const applyPipelinePairPreset = () => {
    edit({
      kind: 'ab',
      pipelineId: d.pipelineId || 'coding-task',
      variants: [
        { label: '1-step', pipelineId: 'coding-task', overrides: '{}' },
        { label: '3-step-reviewed', pipelineId: 'coding-task-reviewed', overrides: '{}' },
      ],
    });
  };

  return <article class="detail" data-ui="detail" aria-label="Create experiment" tabIndex={-1}>
    <header class="detail-h">
      <Button label="Close" icon="x" iconOnly onClick={close} />
      <h2>New experiment</h2>
      <span class="grow" />
      <Button label="Create draft" icon="plus" kind="primary" busy={receipt.state === 'pending'} disabledReason={disabled || invalid || undefined} onClick={create} />
    </header>
    <StatusLine>{receipt.state === 'pending' ? 'Creating draft…' : invalid && !error ? invalid : ''}</StatusLine>
    {error && <ProblemRow title="Draft not created" facts={error} severity="failure" actions={[]} />}
    {!error && readError && <ProblemRow title="Choices did not load" facts={readError} severity="failure" actions={[]} />}
    {storageWarning && <ProblemRow title="Draft storage unavailable" facts={storageWarning} severity="warning" actions={[]} />}
    {receipt.message && <ProblemRow title={receipt.state === 'created' ? 'Creation confirmed' : 'Creation unconfirmed'} facts={receipt.message} severity="warning" actions={[]} />}
    {receipt.message && <div class="answers"><Button label="Review experiments" onClick={close} />{receipt.id && <Button label="Read details again" onClick={() => readback(receipt.id!, d, identity.current)} />}</div>}
    <fieldset class="draft-fields" disabled={!!disabled}>
      <DetailSection title="Setup">
        <EntityPicker
          mode="single"
          label="Use case"
          placeholder="Optional: pick a benchmark or real-work suite"
          searchPlaceholder="Search use cases"
          nodes={useCaseNodes}
          archivedNodes={archivedUseCaseNodes}
          value={d.useCaseId || null}
          loading={!!key && !registryList.data && !registryList.error}
          error={registryList.error}
          onRetry={registryList.reload}
          disabledReason={disabled}
          onChange={applyUseCase}
        />
        <EntityPicker
          mode="single"
          label="Project"
          placeholder="Choose project"
          searchPlaceholder="Search projects"
          nodes={projectNodes}
          value={d.projectId || null}
          loading={!!key && !projectList.data && !projectList.error}
          error={projectList.error}
          onRetry={projectList.reload}
          disabledReason={disabled}
          onChange={projectId => edit({ projectId: projectId ?? '' })}
        />
        <EntityPicker
          mode="single"
          label="Pipeline"
          placeholder="Choose pipeline"
          searchPlaceholder="Search pipelines"
          nodes={pipelineNodes}
          value={d.pipelineId || null}
          loading={!!key && !!d.projectId && !pipelineList.data && !pipelineList.error}
          error={pipelineList.error}
          onRetry={pipelineList.reload}
          disabledReason={disabled || (!d.projectId ? 'Choose a project first' : undefined)}
          onChange={pipelineId => edit({ pipelineId: pipelineId ?? '' })}
        />
        <Choice label="Experiment kind" options={options(['single', 'ab', 'loop'])} value={d.kind} onChange={v => edit({ kind: v })} />
        {field('Repeats', 'repeats')}
        <EntityPicker
          mode="multiple"
          label="Approved examples"
          placeholder="Choose approved examples"
          searchPlaceholder="Search approved examples"
          nodes={exampleNodes}
          value={d.examples}
          loading={!!key && !exampleList.data && !exampleList.error}
          error={exampleList.error}
          onRetry={exampleList.reload}
          disabledReason={disabled}
          onChange={examples => edit({ examples })}
        />
      </DetailSection>
      <DetailSection title="Record">
        <Disclosure
          key={proseKey}
          open={proseOpen ? true : undefined}
          summary={d.useCaseId ? 'Goal, hypothesis & decision rule (auto-filled)' : 'Goal, hypothesis & decision rule'}
        >
          {field('Goal', 'goal', true)}{field('Hypothesis', 'hypothesis', true)}{field('Decision rule', 'decisionRule', true)}{field('Group', 'labGroup')}
        </Disclosure>
      </DetailSection>
      <DetailSection title="Variants">
        {(distinctHarnessAgents.length >= 2 || hasCodingPipelinePair) && (
          <div class="ep-presets" role="group" aria-label="Quick A/B presets">
            {distinctHarnessAgents.length >= 2 && (
              <Button
                label={`${distinctHarnessAgents[0].name} vs ${distinctHarnessAgents[1].name}`}
                kind="quiet"
                icon="bot"
                disabledReason={disabled}
                onClick={() => applyAgentPairPreset([distinctHarnessAgents[0], distinctHarnessAgents[1]])}
              />
            )}
            {distinctHarnessAgents.length >= 3 && (
              <Button
                label={`${distinctHarnessAgents[0].name} vs ${distinctHarnessAgents[2].name}`}
                kind="quiet"
                icon="bot"
                disabledReason={disabled}
                onClick={() => applyAgentPairPreset([distinctHarnessAgents[0], distinctHarnessAgents[2]])}
              />
            )}
            {hasCodingPipelinePair && (
              <Button
                label="1-step vs 3-step reviewed"
                kind="quiet"
                icon="network"
                disabledReason={disabled}
                onClick={applyPipelinePairPreset}
              />
            )}
          </div>
        )}
        {d.variants.map((v, i) => (
          <section class="panel" key={i}>
            <TextField label={`Variant ${i + 1} label`} value={v.label} onChange={label => variants(i, { label })} disabledReason={disabled} />
            <EntityPicker
              mode="single"
              label={`Variant ${i + 1} agent / profile`}
              placeholder="Pipeline default"
              searchPlaceholder="Search agents and models"
              nodes={agentNodes}
              archivedNodes={archivedAgentNodes}
              value={profileIdOfParams(v.overrides)}
              loading={!!key && !agentList.data && !agentList.error}
              error={agentList.error}
              onRetry={agentList.reload}
              disabledReason={disabled}
              onChange={agentId => {
                const agent = agentId ? agentItems.find(a => a.id === agentId) ?? null : null;
                const nextLabel = (!v.label || v.label === 'A' || v.label === 'B') && agent ? agent.id : v.label;
                variants(i, { label: nextLabel, overrides: withProfileParam(v.overrides, agent) });
              }}
            />
            <EntityPicker
              mode="single"
              label={`Variant ${i + 1} pipeline`}
              placeholder="Base pipeline"
              searchPlaceholder="Search pipelines"
              nodes={pipelineNodes}
              value={v.pipelineId || null}
              loading={!!key && !!d.projectId && !pipelineList.data && !pipelineList.error}
              error={pipelineList.error}
              onRetry={pipelineList.reload}
              disabledReason={disabled || (!d.projectId ? 'Choose a project first' : undefined)}
              onChange={pipelineId => variants(i, { pipelineId: pipelineId ?? '' })}
            />
            {advanced(`Variant ${i + 1} changes from default`, v.overrides, overrides => variants(i, { overrides }), 'Optional model, skills, prompt, web_route, account, host, profile, arguments or bands. Server permissions still apply.')}
            <Button label={`Remove variant ${i + 1}`} icon="x" disabledReason={disabled} onClick={() => edit({ variants: d.variants.filter((_, j) => i !== j) })} />
          </section>
        ))}
        <Button label="Add variant" icon="plus" disabledReason={disabled} onClick={() => edit({ variants: [...d.variants, { label: '', pipelineId: '', overrides: '{}' }] })} />
      </DetailSection>
      <DetailSection title="Evaluators">
        {d.evaluators.map((e, i) => (
          <section class="panel" key={i}>
            <Choice label={`Evaluator ${i + 1} kind`} options={options(['script', 'judge', 'typed', 'human'])} value={e.kind} onChange={kind => evaluators(i, { kind, ref: kind === 'script' ? 'example.checks' : kind === 'typed' ? 'labels' : kind === 'human' ? 'owner' : '', form: kind === 'typed' ? 'typed' : 'pass_fail' })} />
            {e.kind === 'judge' && (
              <EntityPicker
                mode="single"
                label={`Judge ${i + 1} pipeline`}
                placeholder="Choose judge pipeline"
                searchPlaceholder="Search pipelines"
                nodes={pipelineNodes}
                value={e.ref || null}
                loading={!!key && !!d.projectId && !pipelineList.data && !pipelineList.error}
                error={pipelineList.error}
                onRetry={pipelineList.reload}
                disabledReason={disabled || (!d.projectId ? 'Choose a project first' : undefined)}
                onChange={ref => evaluators(i, { ref: ref ?? '' })}
              />
            )}
            <Choice label={`Evaluator ${i + 1} form`} options={options(e.kind === 'typed' ? ['typed'] : e.kind === 'script' ? ['pass_fail', 'metric'] : ['pass_fail', 'scale', 'pick_best'])} value={e.form} onChange={form => evaluators(i, { form })} />
            {e.kind === 'judge' && advanced(`Judge ${i + 1} parameters`, e.overrides, overrides => evaluators(i, { overrides }), 'Optional pipeline parameters; host, account and profile require the server’s owner authority.')}
            <Button label={`Remove evaluator ${i + 1}`} icon="x" disabledReason={disabled} onClick={() => edit({ evaluators: d.evaluators.filter((_, j) => i !== j) })} />
          </section>
        ))}
        <Button label="Add evaluator" icon="plus" disabledReason={disabled} onClick={() => edit({ evaluators: [...d.evaluators, { kind: 'script', ref: 'example.checks', form: 'pass_fail', overrides: '{}' }] })} />
        <EntityPicker
          mode="single"
          label="Primary evaluator"
          placeholder="None"
          searchPlaceholder="Search evaluators"
          nodes={primaryEvaluatorNodes}
          value={d.primary || null}
          disabledReason={disabled}
          onChange={primary => edit({ primary: primary ?? '' })}
        />
      </DetailSection>
      {d.kind === 'loop' && (
        <DetailSection title="Loop strategy">
          <Choice label="Generator" options={options(['list', 'grid', 'propose'])} value={d.generator} onChange={generator => edit({ generator })} />
          {advanced('Generator parameters', d.params, params => edit({ params }), 'List uses a variants array; grid uses parameter axes; propose uses the existing agent-strategy parameters. No strategy runs on create.')}
          <p>Allocator: full. Stop rules are optional and have no default ceiling.</p>
          {field('Maximum iterations (optional)', 'max')}{field('No gain iterations (optional)', 'k')}
        </DetailSection>
      )}
      <DetailSection title="Budgets">
        {d.budgets.length === 0 && <p>No experiment budget configured. Project, account and provider limits still apply; cost is not estimated here.</p>}
        {d.budgets.map((b, i) => (
          <section class="panel" key={i}>
            <Choice label={`Budget ${i + 1} unit`} options={options(['trials', 'wall_ms', 'calls', 'credits'])} value={b.unit} onChange={unit => budgets(i, { unit })} />
            <TextField label={`Budget ${i + 1} limit`} type="number" value={b.limit} onChange={limit => budgets(i, { limit })} disabledReason={disabled} />
            <Choice label={`Budget ${i + 1} action`} options={options(['warn', 'pause', 'stop'])} value={b.action} onChange={action => budgets(i, { action })} />
            <Button label={`Remove budget ${i + 1}`} icon="x" disabledReason={disabled} onClick={() => edit({ budgets: d.budgets.filter((_, j) => i !== j) })} />
          </section>
        ))}
        <Button label="Add budget" icon="plus" disabledReason={disabled} onClick={() => edit({ budgets: [...d.budgets, { unit: 'trials', limit: '', action: 'pause' }] })} />
      </DetailSection>
    </fieldset>
    <div class="answers">
      <Button label="Discard local draft" disabledReason={receipt.state === 'pending' ? 'Creation is pending' : undefined} onClick={() => { busy.current = false; resetPrefill(false); setProseOpen(true); setProseKey(n => n + 1); persist({ draft: blank(), state: 'editing' }); setError(''); }} />
    </div>
    <p class="muted">Closing or discarding a local draft does not cancel a request already sent. Launch remains a separate action.</p>
  </article>;
}
