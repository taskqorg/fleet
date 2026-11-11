// Lab draft creation uses the existing operation, never launch (laboratory.md, Create experiment).
import { useEffect, useRef, useState } from 'preact/hooks';
import { useLocation } from 'wouter-preact';
import { Button, Choice, CodeEditor, DetailSection, Empty, ListDetail, ProblemRow, RowCheck, SearchField, StateBadge, TextField, VirtualList, ROW2 } from '../../../packages/ui/src/index.ts';
import { op, online, signedOut, OpError, type Ops } from '../api.ts';
import { checkGrants, onClear } from '../offline.ts';

type Variant = { label: string; pipelineId: string; overrides: string };
type Evaluator = { kind: string; ref: string; form: string; overrides: string };
type Budget = { unit: string; limit: string; action: string };
type Draft = { projectId: string; pipelineId: string; kind: string; examples: string[]; repeats: string;
  goal: string; hypothesis: string; decisionRule: string; labGroup: string; variants: Variant[]; evaluators: Evaluator[];
  primary: string; budgets: Budget[]; generator: string; params: string; max: string; k: string };
type Receipt = { draft: Draft; state: 'editing' | 'pending' | 'uncertain' | 'created'; id?: string; message?: string };
type Option = { id: string; name: string };
type Picker = { label: string; options: Option[]; selected: string[]; multiple?: boolean; choose: (id: string) => void };
const PREFIX = 'fleet-lab-create:';
const blank = (): Draft => ({ projectId: '', pipelineId: '', kind: '', examples: [], repeats: '1', goal: '', hypothesis: '', decisionRule: '', labGroup: '',
  variants: [{ label: 'A', pipelineId: '', overrides: '{}' }], evaluators: [], primary: '', budgets: [], generator: 'list', params: '{"variants":[]}', max: '', k: '' });
const clearDrafts = () => { try { for (const key of Object.keys(sessionStorage)) if (key.startsWith(PREFIX)) sessionStorage.removeItem(key); } catch { /* storage unavailable */ } };
onClear.add(clearDrafts);
const object = (text: string, label: string): Record<string, unknown> => { const x = JSON.parse(text); if (!x || typeof x !== 'object' || Array.isArray(x)) throw Error(label + ' must be an object'); return x; };
const nameOf = (e: Evaluator) => `${e.kind}:${e.ref}:${e.form}`;
function request(d: Draft): Ops['lab_experiment_create']['request'] {
  if (!d.projectId || !d.pipelineId || !d.kind || !d.examples.length) throw Error('Choose a project, pipeline, kind and approved examples.');
  const repeats = Number(d.repeats);
  if (!Number.isSafeInteger(repeats) || repeats < 1) throw Error('Repeats must be a positive integer.');
  if (!d.variants.length || (d.kind === 'single' && d.variants.length !== 1) || (d.kind === 'ab' && d.variants.length < 2)) throw Error('Single needs one variant; A/B needs at least two.');
  if (d.variants.some(v => !v.label.trim()) || new Set(d.variants.map(v => v.label)).size !== d.variants.length) throw Error('Variant labels must be nonempty and unique.');
  const evaluators = d.evaluators.map(e => ({ kind: e.kind, ref: e.ref, form: e.form, ...(e.kind === 'judge' ? { overrides: object(e.overrides, 'Judge overrides') } : {}) }));
  if (d.primary && !d.evaluators.some(e => nameOf(e) === d.primary)) throw Error('Choose a primary from the current evaluators.');
  const budgets = d.budgets.map(b => { const limit = Number(b.limit); if (!Number.isFinite(limit) || limit <= 0) throw Error('Budget limits must be positive.'); return { unit: b.unit, limit, action: b.action }; });
  const out: Ops['lab_experiment_create']['request'] = { projectId: d.projectId, pipelineId: d.pipelineId, kind: d.kind, examples: d.examples, repeats,
    goal: d.goal, hypothesis: d.hypothesis, decisionRule: d.decisionRule, labGroup: d.labGroup,
    variants: d.variants.map(v => ({ label: v.label, ...(v.pipelineId ? { pipelineId: v.pipelineId } : {}), overrides: object(v.overrides, 'Variant overrides') })), evaluators, budgets,
    ...(d.primary ? { primaryEvaluator: d.primary } : {}) };
  if (d.kind === 'loop') {
    if (!d.primary || d.primary.endsWith(':pick_best')) throw Error('Loop needs a scalar primary evaluator.');
    const positive = (x: string) => { const n = Number(x); if (!Number.isSafeInteger(n) || n < 1) throw Error('Loop stop values must be positive integers.'); return n; };
    out.strategy = { generator: { kind: d.generator as 'list' | 'grid' | 'propose', params: object(d.params, 'Generator parameters') }, allocator: { kind: 'full', params: {} },
      ...(d.max ? { max_iterations: positive(d.max) } : {}), ...(d.k ? { no_gain: { k: positive(d.k) } } : {}) };
  }
  return out;
}

export function LabCreate() {
  const [, go] = useLocation();
  const [receipt, setReceipt] = useState<Receipt>({ draft: blank(), state: 'editing' });
  const [key, setKey] = useState(''), [error, setError] = useState(''), [storageWarning, setStorageWarning] = useState('');
  const [projects, setProjects] = useState<Option[]>([]), [pipelines, setPipelines] = useState<Option[]>([]), [examples, setExamples] = useState<Option[]>([]);
  const [picker, setPicker] = useState<Picker | null>(null), [query, setQuery] = useState('');
  const invoker = useRef<HTMLElement | null>(null);
  const live = useRef(true), identity = useRef(''), current = useRef(receipt), busy = useRef(false);
  current.current = receipt;
  const persist = (next: Receipt) => { current.current = next; if (live.current) setReceipt(next); if (identity.current) try { sessionStorage.setItem(identity.current, JSON.stringify(next)); } catch { if (live.current) setStorageWarning('Browser storage is unavailable. Keep this page open until creation is resolved.'); } };
  const clearAccess = () => {
    identity.current = ''; busy.current = true; setKey('');
    current.current = { draft: blank(), state: 'editing' }; setReceipt(current.current);
    setPicker(null); setQuery(''); setProjects([]); setPipelines([]); setExamples([]);
    setError('Access changed. Reload before creating a draft.');
  };
  useEffect(() => {
    live.current = true;
    const cleared = clearAccess;
    onClear.add(cleared);
    (async () => {
      try {
        const me = await op('me', {}); if (!me.email) throw Error('Sign in before creating an experiment.');
        await checkGrants(me.grantsVersion ?? ''); if (!live.current) return;
        try {
          const previous = JSON.parse(sessionStorage.getItem(PREFIX + 'actor') ?? 'null');
          if (previous && previous.actor !== me.email) clearDrafts();
          sessionStorage.setItem(PREFIX + 'actor', JSON.stringify({ actor: me.email }));
        } catch { setStorageWarning('Browser storage is unavailable. Keep this page open until creation is resolved.'); }
        const k = PREFIX + me.email + ':' + me.grantsVersion; identity.current = k;
        let saved: Receipt | null = null; try { saved = JSON.parse(sessionStorage.getItem(k) ?? 'null'); } catch { /* start an empty draft */ }
        if (saved?.draft && typeof saved.draft.projectId === 'string' && typeof saved.draft.pipelineId === 'string' && typeof saved.draft.repeats === 'string' && Array.isArray(saved.draft.examples) && Array.isArray(saved.draft.variants) && Array.isArray(saved.draft.evaluators) && Array.isArray(saved.draft.budgets) && ['editing', 'pending', 'uncertain', 'created'].includes(saved.state)) {
          if (saved.state === 'pending') saved = { ...saved, state: 'uncertain', message: 'The previous request has no confirmed reply. Review experiments before starting another draft.' };
          if (saved.state === 'created') saved = saved.id ? { ...saved, message: 'Creation confirmed. Read the retained details to continue.' } : { ...saved, state: 'uncertain', message: 'The saved reply has no experiment identity. Review experiments.' };
          persist(saved);
        }
        busy.current = false; setKey(k); setError('');
        const [p, e] = await Promise.all([op('project_list', {}), op('lab_examples_list', { state: 'approved' })]);
        if (!live.current || identity.current !== k) return;
        setProjects(p.projects.map(p => ({ id: String(p.id), name: String(p.name ?? p.id) })));
        setExamples(e.examples.map(e => ({ id: String(e.artifactId), name: String(e.name) })));
      } catch (e: any) { if (live.current) setError(e.message); }
    })();
    return () => { live.current = false; identity.current = ''; onClear.delete(cleared); };
  }, []);
  useEffect(() => {
    const verify = async () => { try { const me = await op('me', {}); const next = PREFIX + me.email + ':' + me.grantsVersion;
      if (identity.current && next !== identity.current) { clearDrafts(); clearAccess(); }
    } catch { /* ordinary signed-out handling clears the cache */ } };
    addEventListener('focus', verify); return () => removeEventListener('focus', verify);
  }, []);
  const d = receipt.draft;
  useEffect(() => { let active = true; const scope = identity.current, project = d.projectId; setPipelines([]); if (d.projectId) op('definition_list', { kind: 'pipeline', projectId: d.projectId }).then(x => {
    if (active && identity.current === scope && current.current.draft.projectId === project) setPipelines(x.items.filter(p => p.revision > 0).map(p => ({ id: p.id, name: `${p.name} · r${p.revision}` })));
  }).catch(e => { if (active && identity.current === scope) setError(e.message); }); return () => { active = false; }; }, [d.projectId]);
  const edit = (change: Partial<Draft>) => { if (busy.current || current.current.state !== 'editing') return; persist({ draft: { ...current.current.draft, ...change }, state: 'editing' }); setError(''); };
  const pick = (label: string, options: Option[], selected: string[], choose: (id: string) => void, multiple = false) => { invoker.current = document.activeElement as HTMLElement; setQuery(''); setPicker({ label, options, selected, choose, multiple }); };
  const closePicker = () => { setPicker(null); requestAnimationFrame(() => invoker.current?.focus()); };
  const choose = (id: string) => { if (!picker) return; picker.choose(id); if (!picker.multiple) closePicker(); else setPicker(previous => previous ? { ...previous, selected: previous.selected.includes(id) ? previous.selected.filter(x => x !== id) : [...previous.selected, id] } : null); };
  const variants = (i: number, change: Partial<Variant>) => edit({ variants: current.current.draft.variants.map((v, j) => j === i ? { ...v, ...change } : v) });
  const evaluators = (i: number, change: Partial<Evaluator>) => edit({ evaluators: current.current.draft.evaluators.map((v, j) => j === i ? { ...v, ...change } : v) });
  const budgets = (i: number, change: Partial<Budget>) => edit({ budgets: current.current.draft.budgets.map((v, j) => j === i ? { ...v, ...change } : v) });
  const readback = async (id: string, draft: Draft, k: string) => {
    try {
      const e = await op('lab_experiment_get', { experimentId: id });
      if (e.id !== id || e.projectId !== draft.projectId || e.pipelineId !== draft.pipelineId || e.state !== 'draft') throw Error('Created experiment readback differs. Review its retained record.');
      if (identity.current !== k) return;
      try { sessionStorage.removeItem(k); } catch { /* memory still keeps the receipt */ }
      if (live.current) go('/lab/' + encodeURIComponent(id));
    } catch (e: any) { if (identity.current === k) persist({ draft, state: 'created', id, message: `Creation confirmed; details unavailable: ${e.message}` }); }
  };
  const create = async () => {
    if (picker || !key || busy.current || current.current.state !== 'editing' || !online.value || signedOut.value) return;
    const draft = current.current.draft; let body: Ops['lab_experiment_create']['request'];
    try { body = request(draft); } catch (e: any) { setError(e.message); return; }
    busy.current = true; setPicker(null); const k = identity.current; persist({ draft, state: 'pending' }); setError('');
    try {
      const e = await op('lab_experiment_create', body);
      if (typeof e.id !== 'string' || !e.id) throw Error('The create reply did not identify the experiment.');
      if (identity.current !== k) return;
      persist({ draft, state: 'created', id: e.id }); await readback(e.id, draft, k);
    } catch (e: any) {
      if (identity.current !== k) return;
      if (e instanceof OpError && [400, 403, 404, 409].includes(e.status)) { persist({ draft, state: 'editing' }); setError(e.message); }
      else persist({ draft, state: 'uncertain', message: 'Creation could not be confirmed. Do not repeat this request; review experiments. ' + e.message });
    } finally { busy.current = false; }
  };
  let invalid = ''; try { request(d); } catch (e: any) { invalid = e.message; }
  const disabled = picker ? 'Finish choosing first' : !key ? 'Loading identity and access' : receipt.state !== 'editing' ? 'Resolve the previous creation first' : !online.value ? 'offline' : signedOut.value ? 'signed out' : undefined;
  const field = (label: string, key: 'goal' | 'hypothesis' | 'decisionRule' | 'labGroup' | 'repeats' | 'max' | 'k', multiline = false) => <TextField label={label} value={d[key]} onChange={v => edit({ [key]: v })} multiline={multiline} type={['repeats', 'max', 'k'].includes(key) ? 'number' : 'text'} disabledReason={disabled} />;
  const options = (xs: string[]) => xs.map(id => ({ id, label: id }));
  const available = !picker ? [] : picker.label === 'Projects' ? projects : picker.label === 'Approved examples' ? examples : picker.label === 'Primary evaluators' ? [{ id: '', name: 'None' }, ...d.evaluators.map(e => ({ id: nameOf(e), name: nameOf(e) }))] : picker.label === 'Variant pipelines' ? [{ id: '', name: 'Base pipeline' }, ...pipelines] : pipelines;
  useEffect(() => { if (picker) requestAnimationFrame(() => document.querySelector<HTMLInputElement>('.split-list .search input')?.focus()); }, [picker?.label]);
  const list = <><header class="page-h" data-ui={picker ? 'lab-picker' : undefined}><h1>{picker?.label ?? 'New experiment'}</h1><span class="grow" /><Button label={picker ? 'Done choosing' : 'Experiments'} icon="x" iconOnly onClick={() => picker ? closePicker() : go('/lab')} /></header>
    {picker ? <><SearchField label={`Search ${picker.label}`} value={query} onInput={setQuery} onSearch={() => {}} /><VirtualList items={available.filter(x => (x.name + x.id).toLowerCase().includes(query.toLowerCase()))} getKey={x => x.id} rowHeight={ROW2} follow="top" label={picker.label} columns="1.25rem minmax(0,1fr) auto"
      render={x => <><StateBadge family="source" state="current" compact /><span class="tt"><Button label={x.name} rowLink kind="quiet" onClick={() => choose(x.id)} /><span class="id">{x.id}</span></span>{picker.multiple && <RowCheck label={`Choose ${x.name}`} checked={picker.selected.includes(x.id)} onToggle={() => choose(x.id)} />}</>} />
      {available.length === 0 && <Empty text="No available choices. Check access and the required definitions or approved examples." />}</>
      : <section class="panel"><p>Create a draft, inspect it, then use its separate Launch control.</p><Button label="Choose project" onClick={() => pick('Projects', projects, [d.projectId], id => edit({ projectId: id }))} disabledReason={disabled} /></section>}</>;
  const detail = <article class="detail" data-ui="detail" aria-label="Create experiment" tabIndex={-1}><header class="detail-h"><h2>New experiment</h2><span class="grow" /><Button label="Create draft" icon="plus" kind="primary" busy={receipt.state === 'pending'} disabledReason={disabled || invalid || undefined} onClick={create} /></header>
    <div class="status-line" role="status">{receipt.state === 'pending' ? 'Creating draft…' : invalid && !error ? invalid : ''}</div>
    {error && <ProblemRow title="Draft not created" facts={error} severity="failure" actions={[]} />}
    {storageWarning && <ProblemRow title="Draft storage unavailable" facts={storageWarning} severity="warning" actions={[]} />}
    {receipt.message && <ProblemRow title={receipt.state === 'created' ? 'Creation confirmed' : 'Creation unconfirmed'} facts={receipt.message} severity="warning" actions={[]} />}
    {receipt.message && <div class="answers"><Button label="Review experiments" onClick={() => go('/lab')} />{receipt.id && <Button label="Read details again" onClick={() => readback(receipt.id!, d, identity.current)} />}</div>}
    <fieldset class="draft-fields" disabled={!!disabled}>
    <DetailSection title="Record">{field('Goal', 'goal', true)}{field('Hypothesis', 'hypothesis', true)}{field('Decision rule', 'decisionRule', true)}{field('Group', 'labGroup')}</DetailSection>
    <DetailSection title="Setup"><Button label={`Project: ${projects.find(p => p.id === d.projectId)?.name ?? (d.projectId || 'Choose')}`} disabledReason={disabled} onClick={() => pick('Projects', projects, [d.projectId], id => edit({ projectId: id }))} />
      <Button label={`Pipeline: ${d.pipelineId || 'Choose'}`} disabledReason={disabled || (!d.projectId ? 'Choose a project first' : undefined)} onClick={() => pick('Pipelines', pipelines, [d.pipelineId], id => edit({ pipelineId: id }))} />
      <Choice label="Experiment kind" options={options(['single', 'ab', 'loop'])} value={d.kind} onChange={v => edit({ kind: v })} />{field('Repeats', 'repeats')}
      <Button label={`Approved examples: ${d.examples.length} selected`} disabledReason={disabled} onClick={() => pick('Approved examples', examples, d.examples, id => { const xs = current.current.draft.examples; edit({ examples: xs.includes(id) ? xs.filter(x => x !== id) : [...xs, id] }); }, true)} /></DetailSection>
    <DetailSection title="Variants">{d.variants.map((v, i) => <section class="panel" key={i}><TextField label={`Variant ${i + 1} label`} value={v.label} onChange={label => variants(i, { label })} disabledReason={disabled} />
      <Button label={`Variant ${i + 1} pipeline: ${v.pipelineId || 'Base pipeline'}`} disabledReason={disabled} onClick={() => pick('Variant pipelines', [{ id: '', name: 'Base pipeline' }, ...pipelines], [v.pipelineId], id => variants(i, { pipelineId: id }))} />
      <CodeEditor label={`Variant ${i + 1} overrides`} value={v.overrides} onChange={overrides => variants(i, { overrides })} disabledReason={disabled} />
      <Button label={`Remove variant ${i + 1}`} icon="x" disabledReason={disabled} onClick={() => edit({ variants: d.variants.filter((_, j) => i !== j) })} /></section>)}
      <Button label="Add variant" icon="plus" disabledReason={disabled} onClick={() => edit({ variants: [...d.variants, { label: '', pipelineId: '', overrides: '{}' }] })} /></DetailSection>
    <DetailSection title="Evaluators">{d.evaluators.map((e, i) => <section class="panel" key={i}><Choice label={`Evaluator ${i + 1} kind`} options={options(['script', 'judge', 'typed', 'human'])} value={e.kind} onChange={kind => evaluators(i, { kind, ref: kind === 'script' ? 'example.checks' : kind === 'typed' ? 'labels' : kind === 'human' ? 'owner' : '', form: kind === 'typed' ? 'typed' : 'pass_fail' })} />
      {e.kind === 'judge' && <Button label={`Judge pipeline: ${e.ref || 'Choose'}`} disabledReason={disabled} onClick={() => pick('Judge pipelines', pipelines, [e.ref], id => evaluators(i, { ref: id }))} />}
      <Choice label={`Evaluator ${i + 1} form`} options={options(e.kind === 'typed' ? ['typed'] : e.kind === 'script' ? ['pass_fail', 'metric'] : ['pass_fail', 'scale', 'pick_best'])} value={e.form} onChange={form => evaluators(i, { form })} />
      {e.kind === 'judge' && <CodeEditor label={`Judge ${i + 1} overrides`} value={e.overrides} onChange={overrides => evaluators(i, { overrides })} disabledReason={disabled} />}
      <Button label={`Remove evaluator ${i + 1}`} icon="x" disabledReason={disabled} onClick={() => edit({ evaluators: d.evaluators.filter((_, j) => i !== j) })} /></section>)}
      <Button label="Add evaluator" icon="plus" disabledReason={disabled} onClick={() => edit({ evaluators: [...d.evaluators, { kind: 'script', ref: 'example.checks', form: 'pass_fail', overrides: '{}' }] })} />
      <Button label={`Primary evaluator: ${d.primary || 'None'}`} disabledReason={disabled} onClick={() => pick('Primary evaluators', [{ id: '', name: 'None' }, ...d.evaluators.map(e => ({ id: nameOf(e), name: nameOf(e) }))], [d.primary], primary => edit({ primary }))} /></DetailSection>
    {d.kind === 'loop' && <DetailSection title="Loop strategy"><Choice label="Generator" options={options(['list', 'grid', 'propose'])} value={d.generator} onChange={generator => edit({ generator })} />
      <CodeEditor label="Generator parameters" value={d.params} onChange={params => edit({ params })} disabledReason={disabled} /><p>Allocator: full. Stop rules are optional and have no default ceiling.</p>{field('Maximum iterations (optional)', 'max')}{field('No gain iterations (optional)', 'k')}</DetailSection>}
    <DetailSection title="Budgets">{d.budgets.length === 0 && <p>No experiment budget configured. Project, account and provider limits still apply; cost is not estimated here.</p>}{d.budgets.map((b, i) => <section class="panel" key={i}><Choice label={`Budget ${i + 1} unit`} options={options(['trials', 'wall_ms', 'calls', 'credits'])} value={b.unit} onChange={unit => budgets(i, { unit })} />
      <TextField label={`Budget ${i + 1} limit`} type="number" value={b.limit} onChange={limit => budgets(i, { limit })} disabledReason={disabled} /><Choice label={`Budget ${i + 1} action`} options={options(['warn', 'pause', 'stop'])} value={b.action} onChange={action => budgets(i, { action })} />
      <Button label={`Remove budget ${i + 1}`} icon="x" disabledReason={disabled} onClick={() => edit({ budgets: d.budgets.filter((_, j) => i !== j) })} /></section>)}
      <Button label="Add budget" icon="plus" disabledReason={disabled} onClick={() => edit({ budgets: [...d.budgets, { unit: 'trials', limit: '', action: 'pause' }] })} /></DetailSection>
    </fieldset>
    <Button label="Close" icon="x" onClick={() => go('/lab')} /><Button label="Discard local draft" disabledReason={receipt.state === 'pending' ? 'Creation is pending' : undefined} onClick={() => { busy.current = false; persist({ draft: blank(), state: 'editing' }); setError(''); }} />
    <p class="muted">Closing or discarding a local draft does not cancel a request already sent. Launch remains a separate action.</p>
  </article>;
  return <ListDetail list={list} detail={detail} detailKey="new" />;
}
