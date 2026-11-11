// Lab (docs/designs/fleet-lab.md "/lab screen", docs/design-system/pages/laboratory.md): experiments list, use-case
// registry and examples awaiting approval on /lab; one experiment's matrix (rows examples, columns variants), spend against
// budgets, judge-to-owner agreement, rating queue and Promote on /lab/<experiment id>. Ratings and budget questions are
// answered through questions_answer like every question; rating outputs show blind (A, B, …) in the seeded order.
import { useEffect, useState } from 'preact/hooks';
import { useLocation } from 'wouter-preact';
import { StateBadge, Button, Num, Totals, DetailSection, KvRow, DecisionBlock, ProblemRow, RowCheck, AgeChip, VirtualList, Empty, OfflineMarker, Icon, ListDetail, ROW2 } from '../../../packages/ui/src/index.ts';
import { op, type OpName, type Ops } from '../api.ts';
import { useDoc } from '../store.ts';

const LAB_KINDS = new Set(['experiment', 'score', 'question', 'execution']);
const touches = (c: { kind: string }) => LAB_KINDS.has(c.kind);
const ms = (t: any) => typeof t === 'number' ? t : Date.parse(t);

export function Lab({ id }: { id?: string }) {
  const [, go] = useLocation();
  const { data, state } = useDoc<any>('lab', async () => {
    const [x, r, p, ex] = await Promise.all([op('lab_experiments_list', {}), op('lab_registry', {}), op('lab_ratings_list', { state: 'open' }), op('lab_examples_list', { state: 'proposed' }).catch(() => ({ examples: [] }))]);
    return { experiments: x.experiments, useCases: r.useCases, ratings: p.ratings, proposed: ex.examples };
  }, touches);
  const now = Date.now();
  if (!data) return <><header class="page-h"><h1>Lab</h1></header><Empty text="Loading…" /></>;
  const approvals = data.ratings.filter((r: any) => r.exampleArtifactId), open = data.ratings.filter((r: any) => r.experimentId);
  // Control column: what waits for the owner, then the experiments catalogue (its one scroller, OR-25).
  const list = <>
    <header class="page-h"><h1>Lab</h1><span class="grow" /><OfflineMarker state={state} now={now} /></header>
    {(approvals.length > 0 || open.length > 0) && <section class="panel"><header class="panel-h"><Icon name="bell" /><h2>Waiting for you</h2></header>
      {open.length > 0 && <div class="vl-row static"><span>{open.length} open ratings</span></div>}
      {approvals.map((r: any) => <Rating key={r.id} r={r} now={now} />)}
    </section>}
    <section class="panel">
      {data.experiments.length ? <VirtualList items={data.experiments} getKey={(e: any) => e.id} rowHeight={ROW2} follow="top" label="Experiments" columns="1.25rem minmax(0,1fr) auto"
        rowClass={(e: any) => e.id === id ? 'sel' : ''}
        render={(e: any) => <>
          <span class="lead"><StateBadge family="experiment" state={e.state} reason={e.stopReason ?? undefined} compact /></span>
          <span class="tt"><a class="title row-link" href={`/lab/${e.id}`} onClick={ev => { ev.preventDefault(); go(`/lab/${e.id}`); }}>{title(e)}</a><span class="id">{e.id.slice(0, 8)}</span></span>
          <AgeChip t={ms(e.createdAt)} now={now} />
        </>} />
        : <Empty text="No experiments yet. Create one with POST /api/lab/experiments." />}
    </section>
  </>;
  return <ListDetail list={list} detail={id ? <Experiment key={id} id={id} onClose={() => go('/lab')} /> : undefined} detailKey={id}
    idle={<Registry useCases={data.useCases} />} />;
}

const title = (e: any) => `${e.pipelineId} · ${e.kind === 'ab' ? 'A/B' : e.kind} · ${e.variants.map((v: any) => v.label).join(' vs ')}`;

// The use-case registry fills the work area while no experiment is open. It scrolls with the work area, so its rows are plain.
// ponytail: static rows, fine for tens of use cases; make it its own catalogue with a virtual list if the registry grows past a few hundred.
function Registry({ useCases }: { useCases: any[] }) {
  return <article class="detail" aria-label="Use-case registry">
    <h2 class="detail-title">Use-case registry</h2>
    <div class="vl-wrap" style={{ '--cols': '1.25rem minmax(0,1fr) 5rem 9rem' }}>
      <div class="vl-head" role="row"><span /><span>Use case</span><span>Fit</span><span>Maturity</span></div>
      {useCases.map((u: any) => <div key={u.id} role="row" class="vl-row static">
        <StateBadge family="source" state={u.source_status} reason={u.source ? `${u.source.repo}/${u.source.path}#${u.source.anchor}` : undefined} compact />
        <span class="tt"><span class="title">{u.goal}</span><span class="id">{u.id}</span></span>
        <span class="muted">{u.lab_fit}</span><span class="muted">{u.maturity}</span>
      </div>)}
    </div>
  </article>;
}

// One rating or approval question with its blind outputs and keyboard answers.
function Rating({ r, now }: { r: any; now: number }) {
  const [busy, setBusy] = useState(false);
  const opts = r.form === 'pick_best' ? r.items.map((i: any) => ({ label: i.label, kbd: i.label.toLowerCase() }))
    : r.form === 'scale' ? [1, 2, 3, 4, 5].map(n => ({ label: String(n), kbd: String(n) }))
    : [{ label: 'Pass', kbd: 'p', primary: true }, { label: 'Fail', kbd: 'f' }];
  const toAnswer = (a: string) => r.form === 'pick_best' ? (r.items.find((i: any) => i.label === a.toUpperCase())?.trialId ?? a) : /^(pass|fail)\b/i.test(a) ? a.replace(/^(pass|fail)/i, m => m.toLowerCase()) : a;
  const shown = (a: string) => r.form === 'pick_best' ? (r.items.find((i: any) => i.trialId === a)?.label ?? a) : a;
  const files = r.items.length > 0 && <div class="stack">{r.items.map((i: any) => <div key={i.label}><b>{i.label}</b>{' '}
    {i.files.filter((f: any) => !f.path.endsWith('/output.json') || i.files.length === 1).map((f: any) => <a key={f.artifactId} class="id" href={`/api/results/${f.artifactId}${/\.(html?|svg)$/i.test(f.path) ? '/preview' : ''}`} target="_blank" rel="noopener">{f.path.split('/').slice(1).join('/')}</a>)}</div>)}</div>;
  return <div id={`rating-${r.id}`}><DecisionBlock question={r.text} askedBy="lab" addressedTo="you" since={now} now={now} waits={busy ? 'saving' : 'never blocks'} context={files || undefined} options={opts}
    answered={r.state === 'answered' ? { by: 'you', answer: shown(r.answer), at: now, delivered: true } : undefined}
    onAnswer={a => { setBusy(true); op('questions_answer', { questionId: r.id, answer: toAnswer(a) }).finally(() => setBusy(false)); }} /></div>;
}

const scoreText = (s: any) => s.form === 'pick_best' ? `${s.wins ?? 0} of ${s.n} picks` : s.form === 'pass_fail' ? `${s.passed}/${s.n} pass` : s.mean === undefined ? '' : s.min === s.max ? s.mean.toFixed(2) : `${s.mean.toFixed(2)} (${s.min}–${s.max})`;
const evaluatorLabel = (k: string) => k.split(':')[0] === 'human' ? 'owner ' + k.split(':')[2] : k.split(':').slice(0, 2).join(' ');

function Experiment({ id, onClose }: { id: string; onClose: () => void }) {
  const { data: e, state } = useDoc<any>(`lab:${id}`, async () => {
    const [x, r] = await Promise.all([op('lab_experiment_get', { experimentId: id }), op('lab_ratings_list', { experimentId: id })]);
    return { ...x, ratings: r.ratings };
  }, touches, 5000);
  const [sel, setSel] = useState<string[]>([]);
  const [msg, setMsg] = useState<{ text: string; tone: 'warning' | 'failure' } | null>(null);
  const [patch, setPatch] = useState<any>(null);
  useEffect(() => { if (e && location.hash) document.getElementById(location.hash.slice(1))?.scrollIntoView(); }, [!!e]);
  const now = Date.now();
  if (!e) return <article class="detail" data-ui="detail" tabIndex={-1}><Empty text="Loading…" /></article>;
  const act = <K extends OpName>(name: K, body: Ops[K]['request']) => op(name, body).then(r => { const w = (r as { warnings?: string[] }).warnings; setMsg(w?.length ? { text: w.join('; '), tone: 'warning' } : null); return r; }).catch((x: any) => setMsg({ text: x.message, tone: 'failure' }));
  const trial = (key: string) => e.trials.filter((t: any) => `${t.cell.example}|${t.cell.variant}|${t.cell.repeat}` === key).sort((a: any, b: any) => b.cell.attempt - a.cell.attempt)[0];
  const toggle = (t: string) => setSel(s => s.includes(t) ? s.filter(x => x !== t) : [...s, t]);
  const rate = (form: string) => act('lab_rating_create', { experimentId: id, form, trialIds: sel }).then((r: any) => { if (r?.id) { setSel([]); location.hash = 'rating-' + r.id; } });
  const budgetOf = (unit: string) => e.budgets.find((b: any) => b.unit === unit);
  const note = (unit: string) => { const b = budgetOf(unit); return b ? `of ${b.limit} · ${b.action}${b.firedAt ? ' · reached' : ''}` : undefined; };
  const cols = `minmax(8rem,14rem) ${e.variants.map(() => 'minmax(12rem,1fr)').join(' ')}`;
  return <article class="detail" data-ui="detail" tabIndex={-1} aria-label={title(e)}>
    <header class="detail-h"><Button label="Close" icon="x" iconOnly onClick={onClose} /><StateBadge family="experiment" state={e.state} reason={e.stopReason ?? undefined} /><span class="id">{e.id.slice(0, 8)}</span><span class="grow" />
      {e.state === 'draft' && <Button label="Launch" icon="play" kind="primary" onClick={() => act('lab_experiment_action', { experimentId: id, action: 'launch' })} />}
      {e.state === 'paused' && <Button label="Resume" icon="play" kind="primary" onClick={() => act('lab_experiment_action', { experimentId: id, action: 'resume' })} />}
      {(e.state === 'running' || e.state === 'paused') && <Button label="Stop" icon="square" kind="danger" onClick={() => act('lab_experiment_action', { experimentId: id, action: 'stop' })} />}
      <OfflineMarker state={state} now={now} /></header>
    <h2 class="detail-title">{title(e)}</h2>
    {msg && <ProblemRow title={msg.tone === 'warning' ? 'Launched with warnings' : 'Action refused'} facts={msg.text} severity={msg.tone} actions={[]} />}
    <Totals items={[
      { label: 'Trials', m: e.spend.trials, kind: 'count', note: note('trials') },
      { label: 'Wall time', m: { ...e.spend.wall_ms, value: e.spend.wall_ms.value / 1000 }, kind: 'duration', note: note('wall_ms') },
      { label: 'API calls', m: e.spend.calls, kind: 'count', note: note('calls') },
      { label: 'Credits', m: { value: e.spend.credits.value, basis: 'measured', partial: e.spend.credits.unmeasured ? `${e.spend.credits.unmeasured} calls without usage` : undefined }, kind: 'count', note: note('credits') },
    ]} />
    <section class="panel"><header class="panel-h"><Icon name="rows-3" /><h2>Matrix</h2><span class="muted">{e.examples.length} × {e.variants.length} × {e.repeats}</span><span class="grow" />
      {sel.length === 1 && <><Button label="Pass or fail" icon="check" onClick={() => rate('pass_fail')} /><Button label="Scale 1–5" icon="sliders-horizontal" onClick={() => rate('scale')} /></>}
      {sel.length >= 2 && <Button label={`Pick best of ${sel.length}`} icon="check-check" onClick={() => rate('pick_best')} />}</header>
      <div class="vl-wrap" style={{ '--cols': cols }}>
        <div class="vl-head" role="row"><span>Example</span>{e.variants.map((v: any) => <span key={v.label}>{v.label}</span>)}</div>
        {e.examples.map((p: any) => <div key={p.id} role="row" class="vl-row static lab-row">
          <span class="title">{p.id}<span class="id">{p.split}</span></span>
          {e.variants.map((v: any) => { const c = e.cells.find((x: any) => x.example === p.id && x.variant === v.label);
            return <div key={v.label} class="lab-cell">
              {Object.entries(c?.summary ?? {}).map(([k, s]: any) => <span key={k} class="muted" data-tip={k}>{evaluatorLabel(k)} <b>{scoreText(s)}</b></span>)}
              {(c?.trials ?? []).map((key: string) => { const t = trial(key); if (!t) return null;
                const conf = [t.confounders.hosts.join(', '), t.confounders.harnesses.join(', '), t.confounders.models.join(', '), 'web ' + t.confounders.webRoute].filter(Boolean).join(' · ');
                return <span key={key} class="lab-trial">
                  <StateBadge family="trial" state={t.observation === 'unconfirmed' ? 'unknown' : t.state} reason={t.problem ?? (t.comparable ? undefined : 'left out of comparison')} compact />
                  {t.comparable && <RowCheck checked={sel.includes(t.id)} label={`Select repeat ${t.cell.repeat + 1} for rating`} onToggle={() => toggle(t.id)} />}
                  <span class="muted" data-tip={conf}>r{t.cell.repeat + 1}{t.cell.attempt ? `.${t.cell.attempt}` : ''}</span>
                  <Num m={{ value: t.durationMs === null || t.durationMs === undefined ? null : t.durationMs / 1000, basis: t.durationMs == null ? 'unavailable' : 'measured', reason: 'running' }} kind="duration" />
                  <Num m={{ value: t.spend.credits, basis: 'measured', partial: t.spend.basis === 'partial' ? 'some calls without usage' : undefined }} kind="count" />
                  {(t.cost ?? []).map((c: any, i: number) => <Num key={i} m={{ value: c.value, basis: c.basis, currency: c.currency ?? undefined, reason: c.value === null ? c.reason : `price ${c.prices.map((p: any) => p.id.slice(0, 8)).join(', ')}` }} kind="money" />)}
                  {t.scores.filter((s: any) => s.trialIds.length === 1).map((s: any) => <span key={s.id} data-tip={`${evaluatorLabel(s.evaluator)}${s.rationale ? ': ' + s.rationale : ''}`}><Icon name={s.form === 'pass_fail' ? (s.value ? 'circle-check' : 'circle-x') : 'sliders-horizontal'} label={`${evaluatorLabel(s.evaluator)} ${String(s.value)}`} />{s.form === 'pass_fail' ? '' : String(s.form === 'metric' ? Number(s.value).toFixed(2) : s.value)}</span>)}
                  <a class="btn quiet icon-only" href={`/api/pipeline-runs/${t.id}`} target="_blank" rel="noopener" aria-label="Open the run" data-tip="Open the run"><Icon name="external-link" /></a>
                  {t.state === 'running' ? <Button label="Cancel cell" icon="x" iconOnly kind="quiet" onClick={() => act('lab_cell_action', { experimentId: id, cell: key, action: 'cancel' })} />
                    : <Button label="Retry cell" icon="rotate-ccw" iconOnly kind="quiet" onClick={() => act('lab_cell_action', { experimentId: id, cell: key, action: 'retry' })} />}
                </span>; })}
            </div>; })}
        </div>)}
      </div>
    </section>
    <section class="panel"><header class="panel-h"><Icon name="sliders-horizontal" /><h2>Variants</h2></header>
      <DetailSection title="Spread across repeats">{e.variants.map((v: any) => <KvRow key={v.label} icon="git-merge" label={v.label}
        meta={Object.entries(v.summary).map(([k, s]: any) => `${evaluatorLabel(k)} ${scoreText(s)}`).join(' · ') || 'no scores yet'}
        value={<Button label="Promote" icon="check-check" onClick={() => act('lab_promote', { experimentId: id, variant: v.label }).then(setPatch)} />} />)}</DetailSection>
      {patch && <DetailSection title="Promote">{patch.recommendation ? <KvRow icon="check" label={`Recommendation set to ${patch.method.id}`} />
        : <><KvRow icon="file-text" label="Commit this patch to config/catalogue.json, then promote again" /><pre class="lab-patch">{JSON.stringify(patch.cataloguePatch, null, 1)}</pre></>}</DetailSection>}
      <DetailSection title="Decision gate">
        <KvRow icon="check-check" label="Judge agrees with you" meta={e.agreement.compared ? `${e.agreement.matched} of ${e.agreement.compared} (${e.agreement.percent}%)` : 'no trial rated by both yet'} />
        <KvRow icon="clock" label="Priority background" meta={e.priority.note} />
      </DetailSection>
    </section>
    <section class="panel"><header class="panel-h"><Icon name="message-circle-question" /><h2>Ratings</h2><span class="muted">rated {e.ratings.filter((r: any) => r.state === 'answered').length} of {e.ratings.length}</span></header>
      {e.ratings.length ? e.ratings.map((r: any) => <Rating key={r.id} r={r} now={now} />) : <Empty text="Select comparable trials in the matrix to ask for a rating." />}
    </section>
  </article>;
}
