// Lab (docs/designs/fleet-lab.md "/lab screen", docs/design-system/pages/laboratory.md): experiments list, use-case
// registry and examples awaiting approval on /lab; one experiment's matrix (rows examples, columns variants), spend against
// budgets, judge-to-owner agreement, rating queue and Promote on /lab/<experiment id>. Ratings and budget questions are
// answered through questions_answer like every question; rating outputs show blind (A, B, …) in the seeded order.
import { useEffect, useState } from 'preact/hooks';
import type { ComponentChildren } from 'preact';
import { useLocation } from 'wouter-preact';
import { StateBadge, Button, Disclosure, EntityCode, Num, Totals, DetailSection, KvRow, DecisionBlock, ProblemRow, RowCheck, AgeChip, VirtualList, Empty, OfflineMarker, Icon, ListDetail, DataTable, TabBar, Choice, TextField, catalogueRow, ROW2, percent, SettingField, StepList, msMeasured, type StepItem, type StateOf, type Basis, ShellStatus } from '../../../packages/ui/src/index.ts';
import { op, writeOnce, useAlive, type OpName, type Ops } from '../api.ts';
import { useOp } from '../store.ts';
import { LabCreate } from './lab-create.tsx';
import type { LabExperimentRecord, LabRating, LabScoreSummary, LabUseCaseEntry, ProjectAssessmentRef } from '../../../contracts/generated/ops.ts';

const ms = (t: string | number | null | undefined) => typeof t === 'number' ? t : Date.parse(t ?? '');
const message = (x: unknown) => x instanceof Error ? x.message : String(x);
const fixed = (x: number | null) => x === null ? '—' : x.toFixed(2);
/** A typed score's value: the answer given and the example's label. */
const typedValue = (v: unknown): { answer?: unknown; label?: unknown } => v && typeof v === 'object' ? v as { answer?: unknown; label?: unknown } : {};

// typedIcon: a typed score at a glance: right, wrong, escalated (middle band) or not scorable.
const typedIcon = (value: unknown) => { const v = typedValue(value); return v.answer === 'unscored' ? 'circle-alert' : v.answer === 'abstain' ? 'circle-pause' : v.answer === v.label ? 'circle-check' : 'circle-x'; };

export function Lab({ id }: { id?: string }) {
  const [, go] = useLocation();
  const [idleTab, setIdleTab] = useState<'questions' | 'registry'>('questions');
  const experiments = useOp('lab_experiments_list', {}), registry = useOp('lab_registry', {}), ratings = useOp('lab_ratings_list', { state: 'open' });
  const now = Date.now();
  if (id === 'new') return <LabCreate />;
  if (!experiments.data) return <Empty text="Loading…" />;
  const data = { experiments: experiments.data.experiments, useCases: registry.data?.useCases ?? [], ratings: ratings.data?.ratings ?? [] }, state = experiments.state;
  const approvals = data.ratings.filter(r => r.exampleArtifactId), open = data.ratings.filter(r => r.experimentId);
  const groupCount = new Set(data.experiments.map(e => e.labGroup || 'Ungrouped')).size;
  // Control column: what waits for the owner, then the experiments catalogue (its one scroller, OR-25).
  const list = <>
    <ShellStatus><OfflineMarker state={state} now={now} /></ShellStatus>
    <header class="page-h"><span class="grow" /><Button label="New experiment" icon="plus" iconOnly onClick={() => go('/lab/new')} /></header>
    {(approvals.length > 0 || open.length > 0) && <section class="panel"><header class="panel-h"><Icon name="bell" /><h2>Waiting for you</h2></header>
      {open.length > 0 && <div class="vl-row static"><span>{open.length} open ratings</span></div>}
      {approvals.map(r => <Rating key={r.id} r={r} now={now} />)}
    </section>}
    <section class="panel">
      {data.experiments.length ? <VirtualList items={data.experiments} getKey={e => e.id} rowHeight={catalogueRow()} follow="top" label="Experiments" columns="1.25rem minmax(0,1fr) auto auto"
        rowClass={e => e.id === id ? 'sel' : ''}
        render={e => <>
          <span class="lead"><StateBadge family="experiment" state={e.state as StateOf<'experiment'>} reason={e.stopReason ?? undefined} compact /></span>
          {/* One line: the series names the row, the pipeline, kind and variants follow as its secondary text (the full name was cut). */}
          <span class="tt"><a class="title row-link" href={`/lab/${e.id}`} onClick={ev => { ev.preventDefault(); go(`/lab/${e.id}`); }} data-tip={title(e)}>{e.labGroup || e.pipelineId}</a><span class="id sub">{subtitle(e)}</span></span>
          <span class="undecided" data-tip={undecided(e) ? 'Finished, waiting for a decision' : undefined}>{undecided(e) && <Icon name="bell" label="Undecided" />}</span>
          <AgeChip t={ms(e.createdAt)} />
        </>} />
        : <Empty text="No experiments yet. Choose New experiment to create a draft." />}
    </section>
  </>;
  const idle = <article class="detail" aria-label={idleTab === 'questions' ? 'Lab questions' : 'Use-case registry'}>
    <TabBar label="Lab views" value={idleTab} onChange={setIdleTab} tabs={[
      { id: 'questions', label: 'Questions', count: groupCount },
      { id: 'registry', label: 'Use-case registry', count: data.useCases.length },
    ]} />
    {idleTab === 'questions'
      ? <Questions experiments={data.experiments} onOpen={x => go(`/lab/${x}`)} />
      : <Registry useCases={data.useCases} head={<Undecided experiments={data.experiments} onOpen={x => go(`/lab/${x}`)} />} />}
  </article>;
  return <ListDetail list={list} detail={id ? <Experiment key={id} id={id} onClose={() => go('/lab')} /> : undefined} detailKey={id} idle={idle} />;
}

function Questions({ experiments, onOpen }: { experiments: LabExperimentRecord[]; onOpen: (id: string) => void }) {
  const groups = new Map<string, LabExperimentRecord[]>();
  for (const e of experiments) {
    const key = e.labGroup || 'Ungrouped';
    const items = groups.get(key);
    if (items) items.push(e);
    else groups.set(key, [e]);
  }
  const ordered = [...groups.entries()].sort((a, b) => Math.max(...b[1].map(e => ms(e.launchedAt ?? e.createdAt))) - Math.max(...a[1].map(e => ms(e.launchedAt ?? e.createdAt))));
  return <>
    <Undecided experiments={experiments} onOpen={onOpen} />
    {ordered.length === 0 && <Empty text="No experiment questions yet" />}
    {ordered.map(([group, items]) => {
      const sorted = items.toSorted((a, b) => ms(b.launchedAt ?? b.createdAt) - ms(a.launchedAt ?? a.createdAt));
      const latestGoal = sorted.find(e => e.goal)?.goal;
      const latestHyp = sorted.find(e => e.hypothesis)?.hypothesis;
      return <DetailSection key={group} title={group}>
        {latestGoal && <KvRow icon="circle-question-mark" label={latestGoal} meta={latestHyp ?? undefined} />}
        <DataTable label={`${group} experiments`} head={<tr><th>Experiment</th><th>State</th><th>Goal</th><th>Decision</th><th /></tr>}>
          {sorted.map(e => <tr key={e.id}>
            <td><EntityCode id={e.id} kind="experiment" /><div class="muted">{subtitle(e)}</div></td>
            <td><StateBadge family="experiment" state={e.state as StateOf<'experiment'>} reason={e.stopReason ?? undefined} compact /></td>
            <td>{e.goal ?? title(e)}</td>
            <td>{e.decision && e.decision !== 'pending' ? <><b>{e.decision}</b>{e.decisionReason && <div class="muted">{e.decisionReason}</div>}</> : <span class="muted">Undecided</span>}</td>
            <td><Button label="Open" kind="quiet" onClick={() => onOpen(e.id)} /></td>
          </tr>)}
        </DataTable>
      </DetailSection>;
    })}
  </>;
}

const title = (e: LabExperimentRecord) => `${e.pipelineId} · ${e.kind === 'ab' ? 'A/B' : e.kind} · ${e.variants.map(v => v.label).join(' vs ')}`;
const subtitle = (e: LabExperimentRecord) => `${e.kind === 'ab' ? 'A/B' : e.kind} · ${e.variants.length > 2 ? `${e.variants.length} variants` : e.variants.map(v => v.label).join(' vs ')}`;
/** A finished experiment nobody has decided yet (decision pending). */
const undecided = (e: LabExperimentRecord) => e.state === 'done' && e.decision === 'pending';

// What the Lab waits for, above the registry while no experiment is open (2026-11-20 decision): finished experiments
// without a decision, newest first.
function Undecided({ experiments, onOpen }: { experiments: LabExperimentRecord[]; onOpen: (id: string) => void }) {
  const waiting = experiments.filter(undecided).sort((a, b) => ms(b.launchedAt ?? b.createdAt) - ms(a.launchedAt ?? a.createdAt));
  if (!waiting.length) return null;
  const series = new Set(waiting.map(e => e.labGroup || e.pipelineId)).size;
  return <>
    <ProblemRow severity="warning" title={`${waiting.length} finished ${waiting.length === 1 ? 'experiment waits' : 'experiments wait'} for a decision`}
      facts={`in ${series} series`} actions={[{ label: 'Open the newest', primary: true, onClick: () => onOpen(waiting[0].id) }]} />
    <DetailSection title="Newest undecided">{waiting.slice(0, 5).map(e => <KvRow key={e.id} icon="chart-no-axes-column" label={e.goal ?? title(e)}
      meta={`${e.labGroup || e.pipelineId} · ${subtitle(e)}`} value={<Button label="Open" kind="quiet" onClick={() => onOpen(e.id)} />} />)}</DetailSection>
  </>;
}

// The use-case registry fills the work area while no experiment is open. It scrolls with the work area, so its rows are plain.
// ponytail: static rows, fine for tens of use cases; make it its own catalogue with a virtual list if the registry grows past a few hundred.
function Registry({ useCases, head }: { useCases: LabUseCaseEntry[]; head?: ComponentChildren }) {
  // Obsolete and duplicate entries collapse under their source; maps_to names what covers them.
  const replaced = (u: LabUseCaseEntry) => u.status === 'obsolete' || u.status === 'duplicate';
  const live = useCases.filter(u => !replaced(u)), gone = useCases.filter(replaced);
  const repos = [...new Set(gone.map(u => u.source?.repo ?? '?'))];
  const row = (u: LabUseCaseEntry) => <div key={u.id} role="row" class="vl-row static">
    <StateBadge family="source" state={u.source_status as StateOf<'source'>} reason={u.source ? `${u.source.repo}/${u.source.path}#${u.source.anchor}` : undefined} compact />
    <span class="tt"><span class="title">{u.goal}</span><span class="id">{u.id}{u.maps_to?.length ? ` → ${u.maps_to.join(', ')}` : ''}</span></span>
    <span class="muted">{u.status ?? 'current'}</span><span class="muted">{u.lab_fit}</span><span class="muted">{u.maturity}</span>
  </div>;
  return <>
    {head}
    <h2 class="detail-title">Use-case registry</h2>
    <div class="vl-wrap" style={{ '--cols': '1.25rem minmax(0,1fr) 6rem 5rem 9rem' }}>
      <div class="vl-head" role="row"><span /><span>Use case</span><span>Status</span><span>Fit</span><span>Maturity</span></div>
      {live.map(row)}
      {repos.map(repo => { const of = gone.filter(u => (u.source?.repo ?? '?') === repo);
        return <Disclosure key={repo} summaryClass="muted" summary={`${repo}: ${of.length} replaced`}>{of.map(row)}</Disclosure>; })}
    </div>
  </>;
}

// One rating or approval question with its blind outputs and keyboard answers.
function Rating({ r, now }: { r: LabRating; now: number }) {
  const [busy, setBusy] = useState(false);
  const opts = r.form === 'pick_best' ? r.items.map(i => ({ label: i.label, kbd: i.label.toLowerCase() }))
    : r.form === 'scale' ? [1, 2, 3, 4, 5].map(n => ({ label: String(n), kbd: String(n) }))
    : [{ label: 'Pass', kbd: 'p', primary: true }, { label: 'Fail', kbd: 'f' }];
  const alive = useAlive();
  const toAnswer = (a: string) => r.form === 'pick_best' ? (r.items.find(i => i.label === a.toUpperCase())?.trialId ?? a) : /^(pass|fail)\b/i.test(a) ? a.replace(/^(pass|fail)/i, m => m.toLowerCase()) : a;
  const shown = (a: string) => r.form === 'pick_best' ? (r.items.find(i => i.trialId === a)?.label ?? a) : a;
  const files = r.items.length > 0 && <div class="stack">{r.items.map(i => <div key={i.label}><b>{i.label}</b>{' '}
    {i.files.filter(f => !f.path.endsWith('/output.json') || i.files.length === 1).map(f => <a key={f.artifactId} class="id" href={`/api/results/${f.artifactId}${/\.(html?|svg)$/i.test(f.path) ? '/preview' : ''}`} target="_blank" rel="noopener">{f.path.split('/').slice(1).join('/')}</a>)}</div>)}</div>;
  if (r.state === 'withdrawn') return <div id={`rating-${r.id}`}><DetailSection title="Withdrawn rating"><KvRow icon="circle-slash" label={r.text} meta={r.withdrawal?.reason ?? 'No answer or score recorded'} />{files}</DetailSection></div>;
  return <div id={`rating-${r.id}`}><DecisionBlock question={r.text} askedBy="lab" addressedTo="you" since={now} now={now} waits={busy ? 'saving' : 'never blocks'} context={files || undefined} options={opts}
    answered={r.state === 'answered' ? { by: 'you', answer: shown(String(r.answer)), at: now, delivered: true } : undefined}
    onAnswer={a => { setBusy(true); writeOnce('questions_answer', { questionId: r.id, answer: toAnswer(a) }, () => {}, { signal: alive }).catch(() => { /* the rating stays open */ }).finally(() => setBusy(false)); }} /></div>;
}

const scoreText = (s: LabScoreSummary) => s.form === 'pick_best' ? `${s.wins ?? 0} of ${s.n} picks` : s.form === 'pass_fail' ? `${s.passed}/${s.n} pass` : s.mean == null ? '' : s.min === s.max ? s.mean.toFixed(2) : `${s.mean.toFixed(2)} (${s.min}–${s.max})`;
const evaluatorLabel = (k: string) => k.split(':')[0] === 'human' ? 'owner ' + k.split(':')[2] : k.split(':').slice(0, 2).join(' ');

function Experiment({ id, onClose }: { id: string; onClose: () => void }) {
  const exp = useOp('lab_experiment_get', { experimentId: id }, { every: 5000 }), rated = useOp('lab_ratings_list', { experimentId: id }, { every: 5000 });
  const e = exp.data, state = exp.state, ratings = rated.data?.ratings ?? [];
  const [savingRecord, setSavingRecord] = useState(0);
  const [recordDraft, setRecordDraft] = useState<Record<string, string>>({});
  const [recordErrors, setRecordErrors] = useState<Record<string, string>>({});
  const [savedRecord, setSavedRecord] = useState<Record<string, string>>({});
  const [decisionDraft, setDecisionDraft] = useState<string>();
  const [reasonDraft, setReasonDraft] = useState<string>();
  const [savingDecision, setSavingDecision] = useState(false);
  const [sel, setSel] = useState<string[]>([]);
  const [planTrial, setPlanTrial] = useState<string>();
  const [msg, setMsg] = useState<{ text: string; tone: 'warning' | 'failure' } | null>(null);
  const [patch, setPatch] = useState<Ops['lab_promote']['reply'] | null>(null);
  useEffect(() => { if (e && location.hash) document.getElementById(location.hash.slice(1))?.scrollIntoView(); }, [!!e]);
  const now = Date.now();
  if (!e) return <article class="detail" data-ui="detail" tabIndex={-1}><Empty text="Loading…" /></article>;
  const act = <K extends OpName>(name: K, body: Ops[K]['request']): Promise<Ops[K]['reply'] | undefined> => op(name, body).then(r => { const w = (r as { warnings?: string[] }).warnings; setMsg(w?.length ? { text: w.join('; '), tone: 'warning' } : null); return r; }).catch((x: unknown) => { setMsg({ text: message(x), tone: 'failure' }); return undefined; });
  const trial = (key: string) => e.trials.filter(t => `${t.cell.example}|${t.cell.variant}|${t.cell.repeat}` === key).sort((a, b) => (b.cell.attempt ?? 0) - (a.cell.attempt ?? 0))[0];
  const toggle = (t: string) => setSel(s => s.includes(t) ? s.filter(x => x !== t) : [...s, t]);
  const rate = (form: Ops['lab_rating_create']['request']['form']) => act('lab_rating_create', { experimentId: id, form, trialIds: sel }).then(r => { if (r?.id) { setSel([]); location.hash = 'rating-' + r.id; } });
  const budgetOf = (unit: string) => e.budgets.find(b => b.unit === unit);
  const note = (unit: string) => { const b = budgetOf(unit); return b ? `of ${b.limit} · ${b.action}${b.firedAt ? ' · reached' : ''}` : undefined; };
  const record = (field: string) => field === 'goal' ? e.goal : field === 'hypothesis' ? e.hypothesis : undefined;
  const cols = `minmax(8rem,14rem) ${e.variants.map(() => 'minmax(12rem,1fr)').join(' ')}`;
  const decisionOptions = [
    ...e.variants.map(v => ({ id: `adopt ${v.label ?? ''}`.trim(), label: `Adopt ${v.label ?? ''}` })),
    { id: 'keep baseline', label: 'Keep baseline' },
    { id: 'inconclusive', label: 'Inconclusive' },
  ];
  const currentDecision = decisionDraft ?? (e.decision && e.decision !== 'pending' ? e.decision : '');
  const currentReason = reasonDraft ?? e.decisionReason ?? '';
  const saveDecision = async () => {
    if (!currentDecision) return;
    setSavingDecision(true);
    try { await act('lab_experiment_decide', { experimentId: id, decision: currentDecision, reason: currentReason.trim() || undefined }); }
    finally { setSavingDecision(false); }
  };
  return <article class="detail" data-ui="detail" tabIndex={-1} aria-label={title(e)}>
    <header class="detail-h"><Button label="Close" icon="x" iconOnly onClick={onClose} /><StateBadge family="experiment" state={e.state as StateOf<'experiment'>} reason={e.stopReason ?? undefined} /><EntityCode id={e.id} kind="experiment" /><span class="grow" />
      {e.state === 'draft' && <Button label="Launch" icon="play" kind="primary" disabledReason={savingRecord ? 'Saving experiment record' : Object.values(recordErrors).some(Boolean) || Object.entries(recordDraft).some(([field, value]) => value !== String(savedRecord[field] ?? record(field) ?? '')) ? 'Save the experiment record successfully before launch' : !String(savedRecord.goal ?? e.goal ?? '').trim() || !String(savedRecord.hypothesis ?? e.hypothesis ?? '').trim() ? 'Save a goal and hypothesis before launch' : undefined} onClick={() => act('lab_experiment_action', { experimentId: id, action: 'launch' })} />}
      {e.state === 'paused' && <Button label="Resume" icon="play" kind="primary" onClick={() => act('lab_experiment_action', { experimentId: id, action: 'resume' })} />}
      {(e.state === 'running' || e.state === 'paused') && <Button label="Stop" icon="square" kind="danger" onClick={() => act('lab_experiment_action', { experimentId: id, action: 'stop' })} />}
      <OfflineMarker state={state} now={now} /></header>
    <h2 class="detail-title">{title(e)}</h2>
    {e.state === 'draft' && <DetailSection title="Experiment record">{(['goal', 'hypothesis'] as const).map(field => <SettingField key={field} label={field === 'goal' ? 'Goal' : 'Hypothesis'} help="Saved in the experiment record before explicit launch" value={recordDraft[field] ?? savedRecord[field] ?? record(field) ?? ''} now={now} status={recordErrors[field] ? {kind: 'apply_error', error: recordErrors[field]} : {kind: 'pending'}} onSave={async value => {
      setRecordDraft(r => ({...r, [field]: value})); setSavingRecord(n => n + 1);
      try { await op('lab_experiment_note', {experimentId: id, kind: 'amendment', field, value, text: `Set ${field} before launch`}); setSavedRecord(r => ({...r, [field]: value})); setRecordErrors(r => ({...r, [field]: ''})); setMsg(null); }
      catch (x) { setRecordErrors(r => ({...r, [field]: message(x)})); setMsg({text: message(x), tone: 'failure'}); }
      finally { setSavingRecord(n => n - 1); }
    }} />)}</DetailSection>}
    {(e.state !== 'draft' || (e.decision && e.decision !== 'pending')) && <DetailSection title="Question & decision">
      {e.goal && <KvRow icon="circle-question-mark" label="Goal" value={e.goal} />}
      {e.hypothesis && <KvRow icon="file-text" label="Hypothesis" value={e.hypothesis} />}
      {e.decisionRule && <KvRow icon="sliders-horizontal" label="Decision rule" value={e.decisionRule} />}
      {e.decision && e.decision !== 'pending' && <KvRow icon="check-check" label={e.decision}
        meta={[e.decidedBy && `by ${e.decidedBy}`, e.decidedAt].filter(Boolean).join(' · ') || undefined}
        value={e.decisionReason ?? undefined} />}
      {(e.state === 'done' || e.state === 'stopped') && <div class="stack">
        <Choice label="Experiment decision" options={decisionOptions} value={currentDecision} onChange={setDecisionDraft} />
        <TextField label="Rationale" value={currentReason} onChange={setReasonDraft} help="Why this outcome was chosen" />
        <div><Button label={savingDecision ? 'Saving decision…' : e.decision && e.decision !== 'pending' ? 'Update decision' : 'Record decision'} kind="primary" icon="check" disabledReason={!currentDecision ? 'Select a decision first' : savingDecision ? 'Saving decision' : undefined} onClick={saveDecision} /></div>
      </div>}
    </DetailSection>}
    {msg && <ProblemRow title={msg.tone === 'warning' ? 'Launched with warnings' : 'Action refused'} facts={msg.text} severity={msg.tone} actions={[]} />}
    <Totals items={[
      { label: 'Trials', m: { value: e.spend.trials.value, basis: 'measured' }, kind: 'count', note: note('trials') },
      { label: 'Wall time', m: { value: e.spend.wall_ms.value / 1000, basis: 'measured' }, kind: 'duration', note: note('wall_ms') },
      { label: 'API calls', m: { value: e.spend.calls.value, basis: 'measured' }, kind: 'count', note: note('calls') },
      { label: 'Credits', m: { value: e.spend.credits.value, basis: 'measured', partial: e.spend.credits.unmeasured ? `${e.spend.credits.unmeasured} calls without usage` : undefined }, kind: 'count', note: note('credits') },
    ]} />
    <section class="panel"><header class="panel-h"><Icon name="rows-3" /><h2>Matrix</h2><span class="muted">{e.examples.length} × {e.variants.length} × {e.repeats}</span><span class="grow" />
      {sel.length === 1 && <><Button label="Pass or fail" icon="check" onClick={() => rate('pass_fail')} /><Button label="Scale 1–5" icon="sliders-horizontal" onClick={() => rate('scale')} /></>}
      {sel.length >= 2 && <Button label={`Pick best of ${sel.length}`} icon="check-check" onClick={() => rate('pick_best')} />}</header>
      <div class="vl-wrap" style={{ '--cols': cols }}>
        <div class="vl-head" role="row"><span>Example</span>{e.variants.map(v => <span key={v.label}>{v.label}</span>)}</div>
        {e.examples.map(p => <div key={p.id} role="row" class="vl-row static lab-row">
          <span class="title">{p.id}<span class="id">{p.split}</span></span>
          {e.variants.map(v => { const c = e.cells.find(x => x.example === p.id && x.variant === v.label);
            return <div key={v.label} class="lab-cell">
              {Object.entries(c?.summary ?? {}).map(([k, s]) => <span key={k} class="muted" data-tip={k}>{evaluatorLabel(k)} <b>{scoreText(s)}</b></span>)}
              {(c?.trials ?? []).map(key => { const t = trial(key); if (!t) return null;
                const conf = [(t.confounders.hosts ?? []).join(', '), (t.confounders.harnesses ?? []).join(', '), (t.confounders.models ?? []).join(', '), 'web ' + String(t.confounders.webRoute)].filter(Boolean).join(' · ');
                return <span key={key} class="lab-trial">
                  <Button label="View trial plan" icon="network" iconOnly kind="quiet" onClick={() => setPlanTrial(t.id)} />
                  <StateBadge family="trial" state={(t.observation === 'unconfirmed' ? 'unknown' : t.state) as StateOf<'trial'>} reason={t.problem != null ? String(t.problem) : t.comparable ? undefined : 'left out of comparison'} compact />
                  {t.comparable && <RowCheck checked={sel.includes(t.id)} label={`Select repeat ${(t.cell.repeat ?? 0) + 1} for rating`} onToggle={() => toggle(t.id)} />}
                  <span class="muted" data-tip={conf}>r{(t.cell.repeat ?? 0) + 1}{t.cell.attempt ? `.${t.cell.attempt}` : ''}</span>
                  <Num m={{ value: t.durationMs == null ? null : t.durationMs / 1000, basis: t.durationMs == null ? 'unavailable' : 'measured', reason: 'running' }} kind="duration" />
                  <Num m={{ value: t.spend.credits ?? null, basis: 'measured', partial: t.spend.basis === 'partial' ? 'some calls without usage' : undefined }} kind="count" />
                  {t.recovery && <span data-tip={`Reuses ${(t.recovery.reusedStepIds ?? []).join(', ')}; original spend remains in source history`}><Button label="Source run" icon="network" kind="quiet" onClick={() => setPlanTrial(t.recovery?.sourceRunId)} /></span>}
                  {(t.cost ?? []).map((c, i) => <Num key={i} m={{ value: c.value, basis: c.basis as Basis, currency: c.currency ?? undefined, reason: c.value === null ? c.reason : `${c.prices?.length ?? 0} price${c.prices?.length === 1 ? '' : 's'}` }} kind="money" />)}
                  {t.scores.filter(s => s.trialIds?.length === 1).map(s => s.form === 'typed'
                    ? <span key={s.id} data-tip={`${evaluatorLabel(s.evaluator ?? '')}${s.rationale ? ": " + s.rationale : ""}`}><Icon name={typedIcon(s.value)} label={`${evaluatorLabel(s.evaluator ?? '')} ${String(typedValue(s.value).answer)}, label ${String(typedValue(s.value).label ?? 'none')}`} />{String(typedValue(s.value).answer)}</span>
                    : <span key={s.id} data-tip={`${evaluatorLabel(s.evaluator ?? '')}${s.rationale ? ': ' + s.rationale : ''}`}><Icon name={s.form === 'pass_fail' ? (s.value ? 'circle-check' : 'circle-x') : 'sliders-horizontal'} label={`${evaluatorLabel(s.evaluator ?? '')} ${String(s.value)}`} />{s.form === 'pass_fail' ? '' : String(s.form === 'metric' ? Number(s.value).toFixed(2) : s.value)}</span>)}
                  {t.state === 'running' ? <Button label="Cancel cell" icon="x" iconOnly kind="quiet" onClick={() => act('lab_cell_action', { experimentId: id, cell: key, action: 'cancel' })} />
                    : <Button label="Retry cell" icon="rotate-ccw" iconOnly kind="quiet" onClick={() => act('lab_cell_action', { experimentId: id, cell: key, action: 'retry' })} />}
                </span>; })}
            </div>; })}
        </div>)}
      </div>
    </section>
    {planTrial && <TrialPlan key={planTrial} id={planTrial} projectId={e.projectId} assessments={e.trials.find(t => t.id === planTrial)?.projectAssessments ?? []} onOpen={setPlanTrial} onClose={() => setPlanTrial(undefined)} />}
    <section class="panel"><header class="panel-h"><Icon name="sliders-horizontal" /><h2>Variants</h2></header>
      <DetailSection title="Spread across repeats">{e.variants.map(v => <KvRow key={v.label} icon="git-merge" label={v.proposalLabel ? `${v.proposalLabel} · ${v.label}` : v.label ?? ''}
        meta={<><span>{Object.entries(v.summary ?? {}).map(([k, s]) => `${evaluatorLabel(k)} ${scoreText(s)}`).join(' · ') || 'no scores yet'}</span><span> · Pipeline: <a href={`/pipelines/${v.pipelineId ?? e.pipelineId}`}>{v.pipelineId ?? e.pipelineId}</a></span><span class="id"> · Executable digest: {v.config_ref}</span>{v.catalogueConfigRef && <span class="id"> · Catalogue identity: {v.catalogueConfigRef}</span>}{v.catalogueRef && <span> · <a href={`/catalogue/${v.catalogueRef.id}`}>Catalogue r{v.catalogueRef.revision}</a></span>}</>}
        value={<Button label="Promote" icon="check-check" onClick={() => act('lab_promote', { experimentId: id, variant: v.label ?? '' }).then(r => setPatch(r ?? null))} />} />)}</DetailSection>
      {patch && <DetailSection title="Promote"><KvRow icon="check" label={`Recommendation set to ${patch.method.id ?? ''}`}
        meta={patch.catalogue ? <span class="mono">catalogue r{patch.catalogue.revision}</span> : 'method already in the catalogue'} />
        {patch.note && <KvRow icon="circle-alert" label={String(patch.note)} />}</DetailSection>}
      {(e.typed ?? []).map(t => <DetailSection key={`${t.variant}/${t.split}`} title={`${t.variant} · typed answers · ${t.split === 'tune' ? 'tune (bands are set here)' : 'held out (reported)'}`}>
        {t.stats.map(s => { const cov = percent(s.confident, s.n), esc = percent(100 - parseInt(cov.text, 10), 100);
          return <KvRow key={s.aggregate ? '\u0000all' : s.tag} icon={s.aggregate ? 'chart-no-axes-column' : 'file-text'} label={s.aggregate ? 'All languages' : s.tag}
            meta={`n ${s.n} · coverage ${cov.text} · escalated ${esc.text}`}
            value={s.selectiveAccuracy === null ? <span class="muted">no confident answer</span> : `accuracy ${percent(s.correct, s.confident).text}`} />; })}
      </DetailSection>)}
      {(e.comparison ?? []).map(c => <DetailSection key={c.variant} title={`${c.variant} vs ${c.baseline} · ${c.split === 'held_out' ? 'held out' : 'all examples'}`}>
        <KvRow icon="git-merge" label={c.result.mean === null ? 'No paired example' : `${c.evaluator.startsWith('typed:') ? 'Correct confident answers' : 'Mean'} difference ${c.result.mean >= 0 ? '+' : ''}${c.result.mean.toFixed(2)}`}
          meta={`${c.result.tooFew ? `n ${c.result.n} · too few examples for an interval` : `n ${c.result.n} · 95% interval ${fixed(c.result.low)} to ${fixed(c.result.high)}`}${c.evaluator.startsWith('typed:') ? ' · an escalation counts as not correct; see coverage and accuracy above' : ''}`}
          value={c.reason ? <span class="muted">{c.reason}</span> : undefined} />
      </DetailSection>)}
      {(e.strategies ?? []).length > 0 && <DetailSection title="Loop strategies"><VirtualList items={e.strategies} getKey={v => v.id ?? ''} rowHeight={ROW2} follow="top" label="Strategy runs" columns="1.25rem minmax(0,1fr) auto"
        render={v => <><span class="lead"><StateBadge family="execution" state={v.state as StateOf<'execution'>} compact /></span><span class="tt">Iteration {String(v.iteration)} · {v.output?.reason ?? (v.problem != null ? String(v.problem) : 'pending')}</span></>} /></DetailSection>}
      <DetailSection title="Decision gate">
        <KvRow icon="check-check" label="Judge agrees with you" meta={e.agreement.compared ? `${e.agreement.matched} of ${e.agreement.compared} (${e.agreement.percent}%)` : 'no trial rated by both yet'} />
        {(e.agreement.byVersion ?? []).length > 0 && <VirtualList items={e.agreement.byVersion ?? []} getKey={v => v.version ?? ''} rowHeight={ROW2} follow="top" label="Agreement by rubric version" columns="1.25rem minmax(0,1fr) auto"
          render={v => <><span class="lead"><Icon name="check-check" /></span><span class="tt">{v.version}</span><span>{v.compared ? `${v.matched} of ${v.compared} (${v.percent}%)` : 'no shared rating'}</span></>} />}
        {(e.pairwise ?? []).length > 0 && <VirtualList items={e.pairwise} getKey={v => v.group ?? ''} rowHeight={ROW2} follow="top" label="Pairwise order checks" columns="1.25rem minmax(0,1fr) auto"
          render={v => <><span class="lead"><Icon name={v.reason === 'agree' ? 'check-check' : 'circle-alert'} /></span><span class="tt">{v.group}</span><span>{v.reason}</span></>} />}
        <KvRow icon="clock" label="Priority background" meta={e.priority.note} />
      </DetailSection>
    </section>
    <section class="panel"><header class="panel-h"><Icon name="message-circle-question" /><h2>Ratings</h2><span class="muted">rated {ratings.filter(r => r.state === 'answered').length} of {ratings.filter(r => r.state !== 'withdrawn').length}</span></header>
      {ratings.length ? ratings.map(r => <Rating key={r.id} r={r} now={now} />) : <Empty text="Select comparable trials in the matrix to ask for a rating." />}
    </section>
  </article>;
}

// A scoped server read owns the plan; the matrix never reconstructs omitted work.
function TrialPlan({ id, projectId, assessments, onOpen, onClose }: { id: string; projectId: string; assessments: ProjectAssessmentRef[]; onOpen: (runId: string) => void; onClose: () => void }) {
  const { data: run, state, error } = useOp('pipeline_get', { runId: id, projectId }, { every: 5000 });
  const items: StepItem[] = (run?.plan.entries ?? []).map(entry => {
    const execution = entry.execution;
    const definitions = (run?.definition as { steps?: { id: string; kind: string }[] } | undefined)?.steps ?? [];
    const definition = definitions.find(step => step.id === entry.id);
    return { id: entry.id, kind: definition?.kind === 'api' ? 'tool' : definition?.kind === 'agent' ? 'agent' : 'program', executor: definition?.kind ?? '', reads: '', writes: '', activation: entry.activation, activationReason: entry.problem?.code ?? entry.decision?.reason,
      result: entry.id === run?.canonicalResult?.stepId,
      run: execution ? { state: execution.state, reason: execution.observation, note: entry.executionRef?.mode === 'reused' ? 'reused · original resources' : execution.observation === 'unconfirmed' ? 'unconfirmed' : undefined,
        time: msMeasured(execution.durationMs), cost: { value: null, basis: 'unavailable' as const }, error: execution.error ? JSON.stringify(execution.error) : undefined } : undefined };
  });
  return <DetailSection title="Trial plan">
    <KvRow icon="network" label="Trial" value={<><EntityCode id={id} kind="execution" /><Button label="Close trial plan" icon="x" iconOnly onClick={onClose} /></>} />
    <OfflineMarker state={state} now={Date.now()} />
    {error && <ProblemRow title="Trial plan unavailable" facts={error} severity="failure" actions={[]} />}
    {run && <KvRow icon="rows-3" label="Plan entries" meta={`${run.planSummary.planned} planned · ${run.planSummary.freshExecutions} fresh · ${run.planSummary.reusedExecutions} reused · ${run.planSummary.notSelected} not selected · ${run.planSummary.pending} pending · ${run.planSummary.blocked} blocked`} />}
    {run ? <StepList label="Trial activation and execution" items={items} /> : <Empty text="Plan unavailable" />}
    {(run?.plan.entries ?? []).filter(entry => entry.executionRef?.mode === 'reused').map(entry => <KvRow key={entry.id} icon="network" label={`${entry.id} · reused source`} value={<Button label="Open source run" icon="network" kind="quiet" onClick={() => onOpen(entry.executionRef!.sourceRunId!)} />} />)}
    {run?.canonicalResult && <KvRow icon="file-text" label="Canonical result" value={<a href={`/api/results/${run.canonicalResult.artifact.artifactId}`} target="_blank" rel="noopener">Open result</a>} />}
    {assessments.map(assessment => <KvRow key={`${assessment.checkId}:${assessment.evaluatorSha256}:${assessment.inputSha256}`} icon="file-text" label={`Project assessment · ${assessment.checkId}`} meta={assessment.schemaId} value={<a href={`/api/results/${assessment.artifactId}`} target="_blank" rel="noopener">Open assessment</a>} />)}
  </DetailSection>;
}
