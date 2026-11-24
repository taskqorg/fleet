import { createMemo, createSignal, Show } from 'solid-js';
import {
  Button, CatalogueTree, DetailSection, Empty, ErrorBanner, KvRow, ListDetail, ShellActions, Skeleton, TextField,
  type CatalogueTreeNode,
} from '../../../packages/ui/src/index.ts';
import type { LabPersonalDraft } from '../../../contracts/generated/ops.ts';
import { op, useAlive } from '../api.ts';
import { useLocation } from '../router.ts';
import { useOp } from '../store.ts';

export function PersonalLab(props: { id?: string; path?: string }) {
  const [, go] = useLocation();
  const me = useOp('me', {});
  const architect = () => me.data?.roles.includes('architect');
  const lab = useOp('lab_personal_list', () => architect() || me.data?.personalLab ? {} : null, { every: 5000 });
  const rows = () => lab.data?.drafts ?? [];
  const selected = createMemo(() => rows().find(row => row.id === props.id));
  const nodes = createMemo<CatalogueTreeNode[]>(() => rows().map(row => ({
    id: row.id, label: row.variant,
    sub: row.promotedMethod ? 'Promoted' : row.pipelineRevision !== undefined ? 'Submitted for review' : 'Private draft',
    icon: 'chart-no-axes-column',
  })));
  const path = () => props.path ?? '/personal-lab';
  const open = (id?: string) => go(id ? path() + '/' + encodeURIComponent(id) : path());
  return <Show when={me.data} fallback={<Skeleton rows={4} />}>
    <Show when={architect() || me.data?.personalLab} fallback={<Empty text="Personal Lab access is required." />}>
      <ShellActions>
        <Show when={me.data?.personalLab}>
          <Button label="New draft" icon="plus" iconOnly onClick={() => open('new')} />
        </Show>
        <Show when={architect()}>
          <Button label="Shared Lab" icon="chart-no-axes-column" iconOnly onClick={() => go('/lab')} />
        </Show>
      </ShellActions>
      <ListDetail detailKey={props.id} list={<>
        <Show when={lab.error}>{error => <ErrorBanner text={error()} onRetry={lab.reload} />}</Show>
        <Show when={lab.data} fallback={<Skeleton rows={4} />}>
          <CatalogueTree label="Personal Lab drafts" fill nodes={nodes()} selectedId={props.id} onSelect={node => open(node.id)}
            emptyText="No drafts or submissions available." />
        </Show>
      </>} detail={props.id ? <section class="detail" data-ui="detail" tabindex={-1}>
        <header class="detail-h">
          <h2>{selected()?.variant ?? (props.id === 'new' ? 'New personal draft' : 'Personal Lab draft')}</h2>
          <Button label="Close" icon="x" iconOnly onClick={() => open()} />
        </header>
        <Show when={props.id === 'new' && me.data?.personalLab} fallback={
          <Show when={selected()?.id} keyed fallback={lab.data ? <Empty text="Draft unavailable." /> : <Skeleton rows={4} />}>
            <PersonalDraft row={selected()} edit={!!me.data?.personalLab && selected()?.author === me.data?.email}
              architect={!!architect()} done={id => { lab.reload(); open(id); }} />
          </Show>
        }>
          <PersonalDraft edit architect={!!architect()} done={id => { lab.reload(); open(id); }} />
        </Show>
      </section> : undefined} idle={<Empty text="Choose a draft or submission." />} />
    </Show>
  </Show>;
}

function PersonalDraft(props: {
  row?: LabPersonalDraft; edit: boolean; architect: boolean; done: (id?: string) => void;
}) {
  const id = props.row?.id ?? crypto.randomUUID();
  const [variant, setVariant] = createSignal(props.row?.variant ?? '');
  const [project, setProject] = createSignal(props.row?.project ?? '');
  const [pipeline, setPipeline] = createSignal(props.row?.pipeline ?? '');
  const [confirm, setConfirm] = createSignal<'submit' | 'promote' | 'withdraw'>();
  const [busy, setBusy] = createSignal(false);
  const [error, setError] = createSignal('');
  const alive = useAlive();
  const submitted = () => props.row?.pipelineRevision !== undefined;
  const promoted = () => props.row?.promotedMethod;

  async function act(action: 'save' | 'submit' | 'promote' | 'withdraw') {
    if (busy()) return;
    setError('');
    setBusy(true);
    try {
      if (action === 'withdraw') await op('lab_personal_withdraw', { id }, { signal: alive });
      else if (action === 'promote') await op('lab_personal_promote', {
        id, pipelineRevision: props.row!.pipelineRevision!, confirm: true,
      }, { signal: alive });
      else await op('lab_personal_save', { id, variant: variant(), project: project(), pipeline: pipeline(), submit: action === 'submit' }, { signal: alive });
      if (!alive.aborted) {
        setConfirm(undefined);
        props.done(action === 'withdraw' ? undefined : id);
      }
    } catch (e) {
      if (!alive.aborted) setError(e instanceof Error ? e.message : String(e));
    } finally {
      if (!alive.aborted) setBusy(false);
    }
  }

  return <DetailSection title={submitted() ? 'Submitted configuration' : 'Private draft'}>
    <Show when={props.edit && !submitted()} fallback={<>
      <KvRow icon="folder-open" label="Destination project" value={props.row?.project} />
      <KvRow icon="network" label="Shared pipeline" value={props.row?.pipeline} />
      <KvRow icon="history" label="Reviewed revision" value={props.row?.pipelineRevision} />
      <Show when={promoted()}>{method => <KvRow icon="check" label="Promoted method" value={method()} />}</Show>
    </>}>
      <TextField label="Variant name" value={variant()} onChange={setVariant} />
      <TextField label="Destination project ID" value={project()} onChange={setProject} />
      <TextField label="Shared pipeline ID" value={pipeline()} onChange={setPipeline} />
      <div class="actions">
        <Button kind="primary" label="Save private draft" busy={busy()} onClick={() => void act('save')} />
        <Button label="Submit for review" onClick={() => setConfirm('submit')} />
      </div>
    </Show>
    <Show when={submitted() && !promoted()}>
      <Show when={props.architect} fallback={<p>An architect must review and promote this variant.</p>}>
        <Button kind="primary" label="Promote to shared Fleet" onClick={() => setConfirm('promote')} />
      </Show>
    </Show>
    <Show when={props.edit && props.row && !promoted()}>
      <Button label="Withdraw draft" kind="danger" onClick={() => setConfirm('withdraw')} />
    </Show>
    <Show when={confirm()} keyed>{action => <div>
      <p>{action === 'submit' ? 'Share the variant name, pipeline revision and destination with architects for review?' :
        action === 'promote' ? 'Publish this configuration and set it as the shared project recommendation?' :
        'Withdraw this unpublished draft?'}</p>
      <div class="actions">
        <Button label={action === 'submit' ? 'Confirm submission' : action === 'promote' ? 'Confirm promotion' : 'Confirm withdrawal'}
          busy={busy()} onClick={() => void act(action)} />
        <Button label="Cancel" onClick={() => setConfirm(undefined)} />
      </div>
    </div>}</Show>
    <Show when={error()}>{message => <ErrorBanner text={message()} />}</Show>
  </DetailSection>;
}
