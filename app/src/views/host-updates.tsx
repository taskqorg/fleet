// System › Hosts › host: Updates (docs/design-system/pages/hosts.md, frame 2A of board/screens/host-updates.bl). Every change
// of the update result the host reported, newest first (host_update_history), loaded a page per scroll (OR-65). Each row
// says the state, the release, the components it brought and the reason, time on the right; consecutive switches that
// changed no component fold into one ×N row (OR-58), and an install that ended is shown by its end only.
import { createMemo, For, Show } from 'solid-js';
import { DetailSection, Empty, ErrorBanner, InfiniteList, LiveRow, Skeleton, timeOf, type Life } from '../../../packages/ui/src/index.ts';
import { useOpPages } from '../op-pages.ts';
import type { HostUpdateEntry } from '../../../contracts/generated/ops.ts';
import type { UpdateState } from '../../../contracts/generated/host-protocol.ts';

const LIFE: Record<UpdateState, Life> = { ok: 'done', held: 'stopped', pending: 'working', error: 'failed', refused: 'failed', failed: 'failed' };
const WORD: Partial<Record<UpdateState, string>> = { held: 'held', pending: 'installing', error: 'error', refused: 'refused', failed: 'rolled back' };

type Row = { e: HostUpdateEntry; n: number; oldest?: HostUpdateEntry };

/** The rows to show: a pending or held result followed by a later result of the same release is dropped; runs of switches
 *  that brought nothing fold into their newest with ×N and the oldest release of the run. */
export function updateRows(entries: readonly HostUpdateEntry[]): Row[] {
  const out: Row[] = [];
  entries.forEach((e, i) => {
    const newer = entries[i - 1];
    if ((e.state === 'pending' || e.state === 'held') && newer && newer.version === e.version) return;
    const last = out.at(-1);
    // Only a switch whose components are known and unchanged is quiet; unknown components say nothing.
    const quiet = (x: HostUpdateEntry) => x.state === 'ok' && x.components.length > 0 && x.brought.length === 0;
    if (last && quiet(e) && quiet(last.e)) {
      last.n++;
      last.oldest = e;
      return;
    }
    out.push({ e, n: 1 });
  });
  return out;
}

const brought = (e: HostUpdateEntry) => e.brought.map(c => `${c.component.split('/').pop()} ${c.version}`).join(', ');

export function HostUpdates(p: { hostId: string }) {
  const paged = useOpPages('host_update_history', () => ({ hostId: p.hostId }), {
    page: r => ({ rows: r.entries, next: r.nextCursor ?? null }), after: before => ({ before }), key: e => String(e.seq),
  });
  const rows = createMemo(() => updateRows(paged.pages.rows));
  return <DetailSection title="Updates">
    <Show when={paged.first.data || paged.first.error} fallback={<Skeleton rows={4} />}>
      <Show when={!paged.first.error || paged.first.data} fallback={<ErrorBanner text={`Updates did not load: ${paged.first.error}`} onRetry={() => paged.first.reload()} />}>
        <Show when={rows().length > 0} fallback={<Empty text="No update reported since the history started." />}>
          <InfiniteList pages={paged.pages} label="Updates" endLabel="Older updates">
            <ul class="live-list" data-ui="host-updates">
              <For each={rows()}>{r => {
                const text = () => r.n > 1
                  ? `×${r.n} releases, no component change · ${r.oldest!.version} to ${r.e.version}`
                  : `${r.e.version} ${brought(r.e) || (r.e.components.length ? 'no component change' : '')}`.trim();
                const why = () => [WORD[r.e.state], r.e.reason].filter(Boolean).join(' · ');
                return <LiveRow kind="event" state={LIFE[r.e.state]} at={timeOf(r.e.at, 0)} text={text()} trouble={LIFE[r.e.state] === 'failed'}
                  facts={<Show when={why()}><span class="lr-fact" data-tip={r.e.reason}>{why()}</span></Show>} />;
              }}</For>
            </ul>
          </InfiniteList>
        </Show>
      </Show>
    </Show>
  </DetailSection>;
}
