// Run with files on a pipeline page (docs/design-system/pages/projects.md, Pipeline page; board drafts/pipeline-run-files,
// owner OK 2026-11-26). The person drops one case's files; each uploads into the project's artifact store as it is added
// (uploads.ts); Start run calls pipeline_run with them as inputs and wait false, then the run is read live with
// pipeline_get: its steps (StepList), then its result: the summary its output.json carries and, when the result lists
// records (one per file), those records as a table (DataTable, OR-66).
import { For, Show, createEffect, createMemo, createSignal } from 'solid-js';
import {
  Ago, Button, DataTable, DetailSection, ErrorBanner, FilePicker, StepList, TableRow, duration, finishedStates, msMeasured, type StepItem,
} from '../../../packages/ui/src/index.ts';
import { op, type Ops } from '../api.ts';
import { useOp } from '../store.ts';
import { useAttachments } from '../uploads.ts';
import { inputPaths, recordColumns, resultRecords, type Rec } from '../pipeline-run-model.ts';

type Run = Ops['pipeline_get']['reply'];

const ENDED = new Set<string>(finishedStates.execution);
const resultUrl = (id: string) => `/api/results/${encodeURIComponent(id)}`;

/** A key or a value word as a reader says it: `legal_name` is "Legal name", `model_release` is "model release". */
const words = (s: string) => s.replace(/[_-]+/g, ' ').trim();
const heading = (k: string) => { const w = words(k); return w ? w[0].toUpperCase() + w.slice(1) : k; };
function cell(v: unknown): { text: string; tone?: string } {
  if (v == null || v === '') return { text: '—', tone: 'muted' };
  if (v === true) return { text: 'yes', tone: 't-success pw-tone' };
  if (v === false) return { text: 'no', tone: 't-danger pw-tone' };
  return { text: typeof v === 'string' && /^[a-z0-9]+(_[a-z0-9]+)+$/.test(v) ? words(v) : String(v) };
}

/** Run with files: the drop area, Start run, and the run started here, live. ref marks the block the head's Run focuses. */
export function PipelineRun(props: { projectId: string; pipelineId: string; method?: string | null; ref?: (el: HTMLElement) => void }) {
  const files = useAttachments(() => ({ projectId: props.projectId }));
  // A changed file list is a new request; the same files again (a retry) keep the requestId.
  const add = (fs: File[]) => { requestId = undefined; files.add(fs); };
  const remove = (id: string) => { requestId = undefined; files.remove(id); };
  const [runId, setRunId] = createSignal<string | null>(null);
  const [busy, setBusy] = createSignal(false);
  const [error, setError] = createSignal('');
  let requestId: string | undefined;
  const why = () => {
    const xs = files.items;
    if (!props.method) return 'This pipeline has no method to run';
    if (xs.length === 0) return 'Add files first';
    if (xs.some(x => x.state === 'failed')) return 'Remove the files that failed';
    return undefined;
  };
  const note = () => {
    const xs = files.items, up = xs.filter(x => x.state === 'uploading').length;
    if (!xs.length) return 'Files stay in the project\'s store; the run reads only them.';
    return `${xs.length} ${xs.length === 1 ? 'file' : 'files'}${up ? ` · ${up} uploading` : ''} · stored in the project`;
  };
  const start = async () => {
    if (why() || busy()) return;
    setBusy(true);
    setError('');
    try {
      // ready waits for uploads still running; its ids follow the order of the files still there, so names come after it.
      const ids = await files.ready();
      const names = inputPaths(files.items.map(x => x.name));
      // A retry after a lost reply asks with the same requestId and gets the run that already started.
      requestId ??= crypto.randomUUID();
      const run = await op('pipeline_run', {
        projectId: props.projectId, requestId, methodId: props.method!, wait: false,
        query: `Read ${names.length} ${names.length === 1 ? 'file' : 'files'}: ${names.join(', ')}`.slice(0, 2000),
        inputs: ids.map((artifactId, i) => ({ path: names[i], artifactId })),
      });
      setRunId(run.id);
      requestId = undefined;
      files.clear(ids);
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(false);
    }
  };
  return (
    <section class="pw-run" ref={el => props.ref?.(el)} tabindex={-1} aria-label="Run on files">
      <DetailSection title="Run on files">
        <FilePicker files={files.items} onFiles={add} onRemove={remove} prompt="Drop the files to read, or" />
        <div class="pw-run-foot">
          <p class="pw-muted-line">{note()}</p>
          <Button label="Start run" icon="play" kind="primary" busy={busy()} disabledReason={why()} onClick={() => void start()} />
        </div>
        <Show when={error()}>{e => <ErrorBanner text={`The run did not start: ${e()}`} onRetry={() => void start()} />}</Show>
      </DetailSection>
      <Show when={runId()} keyed>{id => <RunView runId={id} projectId={props.projectId} />}</Show>
    </section>
  );
}

/** One run started here, read live: its state line, its steps, then its result. */
function RunView(props: { runId: string; projectId: string }) {
  const run = useOp('pipeline_get', () => ({ runId: props.runId, projectId: props.projectId }));
  const ended = () => !!run.data && ENDED.has(run.data.state);
  const title = () => {
    const r = run.data;
    if (!r) return 'This run';
    const took = r.durationMs != null && ended() ? ` in ${duration(r.durationMs / 1000).text}` : '';
    return `This run · ${words(r.state)}${took}`;
  };
  return (
    <>
      <Show when={run.error}>{e => <ErrorBanner text={`The run did not load: ${e()}`} onRetry={run.reload} />}</Show>
      <Show when={run.data}>{r => (
        <>
          <DetailSection title={title()} action={r().startedAt ? <Ago t={r().startedAt!} /> : undefined}>
            <StepList label="Steps of this run" items={steps(r())} />
          </DetailSection>
          <Show when={r().state === 'succeeded' && r().canonicalResult?.artifact.artifactId} keyed>{id => <RunResult artifactId={id} />}</Show>
        </>
      )}</Show>
    </>
  );
}

/** The run's steps for StepList: each with what its definition says it does, its state and its time. */
function steps(r: Run): StepItem[] {
  const defs = (r.definition as { steps?: { id: string; kind?: string; description?: string }[] } | undefined)?.steps ?? [];
  return (r.plan?.entries ?? []).map(entry => {
    const def = defs.find(s => s.id === entry.id), x = entry.execution;
    return {
      id: entry.id, kind: def?.kind === 'agent' ? 'agent' : def?.kind === 'api' ? 'tool' : 'program', executor: def?.description ?? '', reads: '', writes: '',
      activation: entry.activation, activationReason: entry.problem?.message ?? entry.decision?.reason, result: entry.id === r.canonicalResult?.stepId,
      run: x ? { state: x.state, reason: x.waitDetail ?? x.observation, time: msMeasured(x.durationMs), cost: { value: null, basis: 'unavailable' as const },
        error: x.error ? (typeof x.error === 'string' ? x.error : JSON.stringify(x.error)) : undefined } : undefined,
    } satisfies StepItem;
  });
}

/** The run's result: its summary as the title (else "Result"), its records as a table, and the file itself. */
function RunResult(props: { artifactId: string }) {
  const [out, setOut] = createSignal<Rec | null>(null);
  const [error, setError] = createSignal('');
  createEffect(() => props.artifactId, id => {
    setOut(null);
    setError('');
    void fetch(resultUrl(id)).then(x => (x.ok ? x.json() : Promise.reject(new Error(`HTTP ${x.status}`))))
      .then((o: unknown) => setOut(o && typeof o === 'object' && !Array.isArray(o) ? o as Rec : {}), e => setError(e instanceof Error ? e.message : String(e)));
  });
  const summary = () => { const s = out()?.summary; return typeof s === 'string' && s.trim() ? s.trim() : null; };
  const rows = createMemo(() => resultRecords(out()));
  const cols = createMemo(() => recordColumns(rows() ?? []));
  return (
    <DetailSection title="Result" action={<a href={resultUrl(props.artifactId)} target="_blank" rel="noopener">Open output.json</a>}>
      <Show when={error()}>{e => <ErrorBanner text={`The result did not load: ${e()}`} />}</Show>
      <Show when={summary()}>{s => <div class="pw-run-sum"><b>{s()}</b></div>}</Show>
      <Show when={rows() && cols().length > 0}>
        <DataTable label="Result records" head={<tr><For each={cols()}>{c => <th scope="col">{heading(c)}</th>}</For></tr>}>
          <For each={rows()!}>{rec => (
            <TableRow>
              <For each={cols()}>{(c, i) => {
                const v = cell(rec[c]);
                return i() === 0 ? <th scope="row" class={['t-name', 'mono', v.tone]}>{v.text}</th> : <td class={v.tone}>{v.text}</td>;
              }}</For>
            </TableRow>
          )}</For>
        </DataTable>
      </Show>
    </DetailSection>
  );
}
