// System › Accounts & limits: one subscription (docs/design-system/pages/system.md, /system/accounts/<id>) and the narrow
// limits read the System tree and readiness share. J5 (docs/designs/fleet-ia-and-journeys.md §3): where the account can
// run, per host, from the account's own inspections, so an account no host can start shows before work is routed to it.
import { createMemo, For, Show } from 'solid-js';
import {
  AgeChip, Button, DetailHead, DetailSection, Empty, ErrorBanner, FillBar, HarnessMark, KvRow, Skeleton, StateBadge,
  countdown, exact, harnesses, type Harness,
} from '../../../packages/ui/src/index.ts';
import type { LimitPeriod, LimitSubscription } from '../../../contracts/generated/ops.ts';
import { useOp } from '../store.ts';

const HOUR = 36e5;

/** The limits read without chart history: the last hour only, so the tree and readiness stay light. The request changes
 *  once an hour, so every view in that hour shares one read. */
export function useAccountLimits() {
  const hour = Math.floor(Date.now() / HOUR) * HOUR;
  return useOp('subscription_limits', { windowHours: 3, from: hour - HOUR, to: hour + HOUR }, { every: 60_000 });
}

export const accountName = (s: LimitSubscription) => typeof s.name === 'string' && s.name ? s.name : s.id;
export const accountHarness = (h: string): Harness | undefined => h in harnesses ? h as Harness : undefined;

/** The period that limits the account first: the one with the least left. */
export function tightest(s: LimitSubscription): LimitPeriod | undefined {
  return s.periods.filter(p => p.left !== null).toSorted((a, b) => a.left! - b.left!)[0] ?? s.periods[0];
}

/** Near its limit: below its own Low mark, projected empty before the reset, or under 15% left. */
export function nearLimit(p: LimitPeriod | undefined): boolean {
  return !!p && (p.low || p.firstEmpty.state === 'at' || (p.left !== null && p.left < 15));
}

type Check = { hostId: string; binding?: string | null; readiness: string; reason?: string | null; observedAt?: number };
type Account = { id: string; name: string; harness?: string; holder?: string; intake?: string; plan?: string | null;
  bindings?: { hostId: string; binding: string; readiness?: string; observedAt?: number }[]; checks?: Check[] };

/** Host readiness of an account: its latest inspection per host, else the binding's own readiness. */
export function accountHosts(a: Account | undefined): Check[] {
  if (!a) return [];
  const latest = new Map<string, Check>();
  for (const b of a.bindings ?? []) latest.set(b.hostId, { hostId: b.hostId, binding: b.binding, readiness: b.readiness ?? 'unknown', observedAt: b.observedAt });
  for (const c of (a.checks ?? []).toSorted((x, y) => (x.observedAt ?? 0) - (y.observedAt ?? 0))) latest.set(c.hostId, c);
  return [...latest.values()];
}

/** Why a binding cannot start runs, in words; the reason code stays in the tooltip. */
export const readinessText = (c: Check) => c.readiness === 'ready' ? 'ready'
  : c.reason === 'binding-unprovisioned' ? 'login not set up on this host'
    : c.reason ? c.reason.replaceAll('-', ' ') : c.readiness;

export function AccountDetail(props: { id: string; onClose: () => void; onOpen: (path: string) => void }) {
  const limits = useAccountLimits();
  const accounts = useOp('account_list', {});
  const hostsOp = useOp('hosts_list', {}, { every: 5000 });
  const sub = createMemo(() => limits.data?.subscriptions.find(s => s.id === props.id));
  const acct = createMemo(() => (accounts.data?.accounts as Account[] | undefined)?.find(a => a.id === props.id));
  const now = () => limits.data?.serverNow ?? Date.now();
  const hosts = createMemo(() => accountHosts(acct()));
  const harness = () => accountHarness(sub()?.harness ?? acct()?.harness ?? '');
  const hostRow = (id: string) => hostsOp.data?.hosts.find(h => h.id === id);
  const cli = (id: string) => hostRow(id)?.harnessVersions?.find(v => v.harness === (sub()?.harness ?? acct()?.harness));
  return (
    <article class="detail" data-ui="detail" tabindex={-1} aria-label={sub() ? accountName(sub()!) : props.id}>
      <DetailHead crumb="system · account" onClose={props.onClose}
        title={<>{harness() && <HarnessMark h={harness()!} />} {sub() ? accountName(sub()!) : acct()?.name ?? props.id}</>}
        state={<Show when={sub()}>{s => <><StateBadge family="subscription" state={s().intake === 'paused' ? 'paused' : 'all'} compact /> <span class="muted">{String(s().holder ?? '')}</span></>}</Show>}
        actions={<Button label="Limits" icon="gauge" kind="quiet" onClick={() => props.onOpen('/hosts/limits')} />} />
      <Show when={limits.error ?? accounts.error}>{e => <ErrorBanner text={e()} onRetry={() => { void limits.reload(); void accounts.reload(); }} />}</Show>
      <Show when={limits.data || accounts.data} fallback={<Skeleton rows={5} />}>
        <Show when={sub() || acct()} fallback={<Empty text={`Account "${props.id}" not found.`} />}>
          <DetailSection title="Left">
            <Show when={sub()?.periods.length} fallback={<span class="muted">No reading yet: a run or a refresh reports one.</span>}>
              <For each={sub()!.periods}>{p => (
                <KvRow icon="gauge" label={p.period}
                  value={<span class="limit-cell">
                    {p.left !== null
                      ? <FillBar total={100} segments={[{ label: 'used', bytes: 100 - p.left }]} warnAt={sub()!.lowPct ?? undefined} unit="percent" />
                      : <span class="muted" data-tip={p.status ?? 'No reading'}>—</span>}
                    <Show when={p.resetsAt}>{r => <time data-tip={exact(r())} aria-label={`resets ${countdown(r(), now()).long}`}>{countdown(r(), now()).text}</time>}</Show>
                    {p.pace !== 'unknown' && <span class={`chip${p.pace === 'ahead' ? ' t-attention' : ''}`} data-tip="pace against the period's elapsed share">{p.pace}</span>}
                    <Show when={p.readingAt}>{r => <AgeChip t={r()} />}</Show>
                  </span>} />
              )}</For>
            </Show>
          </DetailSection>
          <DetailSection title="Runs on">
            <Show when={hosts().length} fallback={<span class="muted">No host has a login for this account.</span>}>
              <For each={hosts()}>{c => (
                <KvRow icon="panels-top-left" label={c.hostId}
                  meta={<span data-tip={c.reason ?? undefined}>{c.binding ?? ''}</span>}
                  value={<span class={c.readiness === 'ready' ? '' : 't-danger'}>
                    {readinessText(c)}
                    <Show when={cli(c.hostId)}>{v => <span class="muted mono"> · {v().observed ?? '—'}</span>}</Show>
                  </span>} />
              )}</For>
            </Show>
          </DetailSection>
        </Show>
      </Show>
    </article>
  );
}
