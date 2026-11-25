// System › Accounts & limits: one subscription (docs/design-system/pages/system.md, /system/accounts/<id>) and the narrow
// limits read the System tree and readiness share. J5 (docs/designs/fleet-ia-and-journeys.md §3): where the account can
// run, per host, from the account's own inspections, so an account no host can start shows before work is routed to it.
import { createMemo, createSignal, For, onSettled, Show } from 'solid-js';
import {
  AgeChip, Button, DataTable, DetailHead, DetailSection, Empty, ErrorBanner, FillBar, HarnessMark, KvRow, Skeleton, StateBadge,
  countdown, exact, harnesses, type Harness,
} from '../../../packages/ui/src/index.ts';
import type { LimitPeriod, LimitSubscription } from '../../../contracts/generated/ops.ts';
import { useOp } from '../store.ts';
import { accountProfiles, compatProfiles, modelSkips, type CompatAccount, type ModelBinding } from '../compat.ts';

const HOUR = 36e5;
/** The profiles for J5 checks: profiles_list is an architect read, so others ask nothing and see no profile rows. */
export function useCompatProfiles() {
  const me = useOp('me', {});
  const list = useOp('profiles_list', () => (me.data && (me.data.owner || me.data.roles.includes('architect')) ? {} : null));
  return createMemo(() => compatProfiles(list.data?.profiles as Record<string, unknown>[] | undefined));
}


/** The limits read without chart history: the last hour only, so the tree and readiness stay light. The request changes
 *  once an hour, so every view in that hour shares one read. */
export function useAccountLimits() {
  const [hour, setHour] = createSignal(Math.floor(Date.now() / HOUR) * HOUR);
  onSettled(() => {
    const t = setInterval(() => setHour(Math.floor(Date.now() / HOUR) * HOUR), 60_000);
    return () => clearInterval(t);
  });
  return useOp('subscription_limits', () => ({ windowHours: 3, from: hour() - HOUR, to: hour() + HOUR }), { every: 60_000 });
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
  bindings?: (ModelBinding & { observedAt?: number })[]; checks?: Check[] };

/** A binding's probed models in words (J5): the list, or why it is unknown. */
export function modelsText(b: ModelBinding | undefined): string {
  if (!b?.models) return 'models not probed yet: the router does not skip on unknown';
  return b.models.length ? b.models.join(' · ') : 'offers no models';
}

/** Host readiness of an account: its latest inspection per host, else the binding's own readiness. */
export function accountHosts(a: Account | undefined): Check[] {
  if (!a) return [];
  const latest = new Map<string, Check>();
  for (const b of a.bindings ?? []) latest.set(b.hostId, { hostId: b.hostId, binding: b.binding, readiness: b.readiness ?? 'unknown', observedAt: b.observedAt });
  // The newer observation wins: a check older than the binding's own reading does not override it.
  for (const c of (a.checks ?? []).toSorted((x, y) => (x.observedAt ?? 0) - (y.observedAt ?? 0))) {
    const cur = latest.get(c.hostId);
    if (!cur || (c.observedAt ?? 0) >= (cur.observedAt ?? 0)) latest.set(c.hostId, c);
  }
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
  // J5: profiles of this harness and the hosts that serve their model; the router's model skips over the last day.
  const compatList = useCompatProfiles();
  const skipsOp = useOp('route_decisions', () => ({ subscriptionId: props.id }), { every: 60_000 });
  const binding = (host: string) => acct()?.bindings?.find(b => b.hostId === host);
  const profiles = createMemo(() => acct() ? accountProfiles(acct() as CompatAccount, compatList()) : []);
  const skips = createMemo(() => modelSkips(skipsOp.data?.decisions ?? [], props.id));
  const models = createMemo(() => {
    const by = new Map<string, { model: string; names: string[]; serving: string[]; refusing: string[]; of: number }>();
    for (const p of profiles()) {
      const g = by.get(p.profile.model!) ?? { model: p.profile.model!, names: [], serving: p.serving, refusing: p.refusing, of: p.of };
      g.names.push(p.profile.id);
      by.set(g.model, g);
    }
    return [...by.values()].toSorted((a, b) => b.refusing.length - a.refusing.length || b.names.length - a.names.length);
  });
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
                  meta={<span data-tip={c.binding ?? undefined}>{modelsText(binding(c.hostId))}</span>}
                  value={<span class={c.readiness === 'ready' ? '' : 't-danger'}>
                    {readinessText(c)}
                    <Show when={cli(c.hostId)}>{v => <span class="muted mono"> · {v().observed ?? '—'}</span>}</Show>
                  </span>} />
              )}</For>
            </Show>
          </DetailSection>
          <Show when={models().length}>
            {/* One row per model: many profiles share a model, and the hosts that serve it are the model's (J5). */}
            <DetailSection title={`Profiles on this account ${profiles().length}`}>
              <For each={models()}>{g => (
                <KvRow icon={g.refusing.length ? 'triangle-alert' : 'bot'} label={g.model}
                  meta={`${g.names.slice(0, 3).join(', ')}${g.names.length > 3 ? ` +${g.names.length - 3}` : ''}${g.refusing.length ? ` · ${g.serving.length ? `only ${g.serving.join(', ')} offers it` : 'no host offers it'}; ${g.refusing.join(', ')} skipped` : ''}`}
                  value={<span class={g.serving.length ? 'mono' : 'mono t-attention'}>{g.serving.length} of {g.of}</span>} />
              )}</For>
            </DetailSection>
          </Show>
          <Show when={skips().length}>
            <DetailSection title={`Router skips, 24 h ${skips().length}`}>
              <DataTable label="Router skips" head={<tr><th scope="col">Execution · reason</th><th scope="col" class="num">Skipped</th><th scope="col" class="num">Won</th><th scope="col" class="num">Age</th></tr>}>
                <For each={skips()}>{k => (
                  <tr>
                    <th scope="row" data-tip={k.executionId}>{k.reason.includes('modelNotFound') ? `${k.model}: rejected after start (modelNotFound)` : `${k.model} not offered (probe)`}</th>
                    <td class="num mono">{k.host}</td>
                    <td class="num mono">{k.won ?? '—'}</td>
                    <td class="num"><AgeChip t={Date.parse(k.at)} /></td>
                  </tr>
                )}</For>
              </DataTable>
            </DetailSection>
          </Show>
        </Show>
      </Show>
    </article>
  );
}
