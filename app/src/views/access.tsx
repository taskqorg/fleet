// System › Access (docs/design-system/pages/system.md "Access"; canvas board/screens/system.bl, frame "System › Access:
// status and onboarding (approved)"): every subscription login, model API key and service key in three groups, each with
// its kind, state in plain words, limit or credits, where it serves, the last check and one next-step button. The add
// flows (a key, a host login, a monthly limit) open in the same work area. Every fact and write is an operation agents
// call too (access_status, access_check, access_onboard, connections_key_add, account_login_handoff). A check is
// read-only and never starts a login.
import { createMemo, createSignal, For, Match, Show, Switch } from 'solid-js';
import {
  AgeChip, Button, Choice, DataTable, DetailHead, DetailSection, Empty, ErrorBanner, KvRow, Skeleton, StateBadge, TableRow, TextField,
  copyText,
} from '../../../packages/ui/src/index.ts';
import { op, type Ops } from '../api.ts';
import { useOp } from '../store.ts';

type Status = Ops['access_status']['reply'];
type Item = Status['items'][number];
type Group = Item['group'];

const GROUPS: { id: Group; label: string }[] = [
  { id: 'subscription', label: 'Subscriptions · login on a host' },
  { id: 'model-key', label: 'Model API keys · credits' },
  { id: 'service', label: 'Services' },
];
const PROVIDERS = [
  { id: 'anthropic', label: 'Anthropic' }, { id: 'openai', label: 'OpenAI' }, { id: 'google-ai', label: 'Google AI' },
  { id: 'tavily', label: 'Tavily' }, { id: 'meshy', label: 'Meshy' }, { id: 'tripo', label: 'Tripo' },
];

const needsYou = (s: Item['state']) => s === 'failing' || s === 'needs-login' || s === 'needs-key' || s === 'not-added';

type Flow =
  | { kind: 'key'; provider?: string; connection?: string }
  | { kind: 'login'; item: Item; hostId?: string; binding?: string }
  | { kind: 'limit'; item: Item };

export function Access(props: { onOpen: (path: string) => void }) {
  const status = useOp('access_status', {}, { every: 30_000 });
  const [flow, setFlow] = createSignal<Flow | null>(null);
  const [checking, setChecking] = createSignal<string | null>(null);
  const [err, setErr] = createSignal('');
  const items = createMemo(() => status.data?.items ?? []);
  const check = (it: Item) => {
    setChecking(it.id); setErr('');
    void op('access_check', { id: it.id }).then(() => { setChecking(null); status.reload(); }, e => { setChecking(null); setErr(String(e.message ?? e)); });
  };
  const checkAll = async () => {
    setErr('');
    for (const it of items()) {
      if (it.state === 'not-added' || it.state === 'needs-key') continue;
      setChecking(it.id);
      try { await op('access_check', { id: it.id }); } catch (e) { setErr(`${it.name}: ${String((e as Error).message ?? e)}`); }
    }
    setChecking(null); status.reload();
  };
  const next = (it: Item) => {
    const n = it.next;
    if (!n) return;
    switch (n.action) {
      case 'login': return setFlow({ kind: 'login', item: it, hostId: n.hostId, binding: n.binding });
      case 'add-key': case 'add-connection': return setFlow({ kind: 'key', provider: it.provider, connection: it.id.startsWith('connection:') ? it.name : undefined });
      case 'billing': return n.url ? void window.open(n.url, '_blank', 'noopener') : undefined;
      case 'limit': return setFlow({ kind: 'limit', item: it });
      default: return check(it);
    }
  };
  const open = (it: Item) => {
    const [kind, id] = [it.id.slice(0, it.id.indexOf(':')), it.id.slice(it.id.indexOf(':') + 1)];
    if (kind === 'account') props.onOpen(`/hosts/limits/${encodeURIComponent(id)}`);
    else if (kind === 'connection') props.onOpen(`/keys/${encodeURIComponent(id)}`);
    else setFlow({ kind: 'key', provider: it.provider });
  };

  return <article class="detail" data-ui="detail" tabindex={-1} aria-label="Access">
    <DetailHead crumb="system · access" title="Accounts & keys"
      state={<Show when={status.data} fallback={<span class="muted">Reading…</span>}>{s =>
        <span class="muted">{s().counts.works} work · <span class={s().counts.needYou ? 't-attention' : undefined}>{s().counts.needYou} need you</span> · {s().counts.notAdded} not added</span>}</Show>}
      actions={<>
        <Button label="Add access" icon="plus" kind="primary" onClick={() => setFlow({ kind: 'key' })} />
        <Button label="Check all" icon="rotate-cw" disabledReason={checking() ? 'Checking' : undefined} onClick={() => void checkAll()} />
      </>} />
    <Show when={err()}>{e => <ErrorBanner text={e()} />}</Show>
    <Show when={status.error && !status.data}><ErrorBanner text={status.error ?? ''} onRetry={status.reload} /></Show>
    <Show when={flow()}>{f => <Switch>
      <Match when={f().kind === 'key'}><KeyFlow flow={f() as Flow & { kind: 'key' }} onChanged={status.reload} onDone={() => { setFlow(null); status.reload(); }} /></Match>
      <Match when={f().kind === 'login'}><LoginFlow flow={f() as Flow & { kind: 'login' }} onDone={() => { setFlow(null); status.reload(); }} /></Match>
      <Match when={f().kind === 'limit'}><LimitFlow flow={f() as Flow & { kind: 'limit' }} onDone={() => { setFlow(null); status.reload(); }} /></Match>
    </Switch>}</Show>
    <Show when={status.data} fallback={<Skeleton rows={8} />}>
      <For each={GROUPS}>{g => {
        const rows = createMemo(() => items().filter(i => i.group === g.id));
        return <Show when={rows().length}>
          <DetailSection title={g.label}>
            <DataTable label={g.label} cls="access-table" head={<tr>
              <th>Account · kind</th><th>State</th><th>Limit · credits</th><th>Where · used by</th><th>Last check</th><th>Next step</th>
            </tr>}>
              <For each={rows()}>{it => <TableRow onOpen={() => open(it)}>
                <td><div>{it.name}<Show when={it.note}>{n => <span class="muted"> · {n()}</span>}</Show></div>
                  <div class="muted">{it.kind}{it.keys != null && it.keys > 0 ? ` · ${it.keys} key${it.keys > 1 ? 's' : ''}` : ''}</div></td>
                <td><StateBadge family="access" state={it.state} /><div class="muted" data-tip={it.stateText}>{it.state === 'not-added' ? '' : it.stateText}</div></td>
                <td class="muted">{it.limit?.text ?? '—'}</td>
                <td class="muted" data-tip={it.usedBy?.join(', ')}>{[it.where, it.usedBy?.length ? `${it.usedBy.length} profile${it.usedBy.length > 1 ? 's' : ''}` : ''].filter(Boolean).join(' · ') || '—'}</td>
                <td><Show when={it.lastCheck} fallback={<span class="muted">—</span>}>{c =>
                  <span class={c().ok ? 'muted' : 't-danger'} data-tip={c().text}><AgeChip t={c().at} /></span>}</Show></td>
                <td onClick={e => e.stopPropagation()}>
                  <Show when={it.next} fallback={<Show when={it.state !== 'not-added'}>
                    <Button label={checking() === it.id ? 'Checking' : 'Check'} kind="quiet" disabledReason={checking() ? 'Checking' : undefined} onClick={() => check(it)} />
                  </Show>}>{n =>
                    <Button label={n().label} kind={needsYou(it.state) ? 'primary' : 'quiet'} disabledReason={checking() === it.id ? 'Checking' : undefined} onClick={() => next(it)} />}
                  </Show>
                  <Show when={it.id.startsWith('connection:')}>
                    <Button label="Limit" icon="gauge" iconOnly kind="quiet" onClick={() => setFlow({ kind: 'limit', item: it })} />
                  </Show>
                </td>
              </TableRow>}</For>
            </DataTable>
          </DetailSection>
        </Show>;
      }}</For>
      <DetailSection title="Browser only (links)">
        <span class="muted">Flow · Gemini Notebook · Genie · Gemini in Chrome · Veo 3.1 (blocked: needs a Google Cloud sign-in, not a key).</span>
      </DetailSection>
      <Show when={!items().length}><Empty text="Fleet has no accounts or keys yet." /></Show>
    </Show>
  </article>;
}

/** Add an API key: provider, where the key comes from, paste once, Fleet checks at once and explains a failure. */
function KeyFlow(props: { flow: Flow & { kind: 'key' }; onDone: () => void; onChanged: () => void }) {
  const [provider, setProvider] = createSignal(props.flow.provider ?? 'anthropic');
  const [name, setName] = createSignal(props.flow.connection ?? '');
  const [guide, setGuide] = createSignal<Ops['access_onboard']['reply'] | null>(null);
  const [value, setValue] = createSignal('');
  const [busy, setBusy] = createSignal(false);
  const [err, setErr] = createSignal('');
  const [result, setResult] = createSignal<Item | null>(null);
  const prepare = () => {
    setBusy(true); setErr('');
    void op('access_onboard', { kind: 'key', provider: provider(), ...(name().trim() ? { connection: name().trim() } : {}) })
      .then(r => { setBusy(false); setGuide(r); if (r.created) props.onChanged(); }, e => { setBusy(false); setErr(String(e.message ?? e)); });
  };
  const add = () => {
    const g = guide(); const v = value().trim();
    if (!g?.connection || !v) return;
    setBusy(true); setErr('');
    // The value goes once to connections_key_add and is dropped here; only its fingerprint comes back.
    void op('connections_key_add', { connection: g.connection, value: v })
      .then(() => { setValue(''); return op('access_check', { id: `connection:${g.connection}` }); })
      .then(r => { setBusy(false); setResult(r.item); }, e => { setBusy(false); setValue(''); setErr(String(e.message ?? e)); });
  };
  return <DetailSection title="Add an API key">
    <div class="stack sys-form">
      <Show when={!guide()} fallback={<>
        <ol class="access-steps"><For each={guide()!.steps}>{s => <li>{s}</li>}</For></ol>
        <div class="res-bar"><Show when={guide()!.guide?.keyUrl}>{u => <Button label="Open the key page" icon="external-link" onClick={() => window.open(u(), '_blank', 'noopener')} />}</Show>
          <Show when={guide()!.guide?.billingUrl}>{u => <Button label="Open billing" icon="external-link" kind="quiet" onClick={() => window.open(u(), '_blank', 'noopener')} />}</Show></div>
        <Show when={guide()!.guide?.note}>{n => <span class="muted">{n()}</span>}</Show>
        <TextField label="Key" type="password" value={value()} onChange={setValue} help="Sent once to Fleet's key store, never shown again; Fleet keeps only its fingerprint." />
        <div class="res-bar">
          <Button label="Add and check" kind="primary" disabledReason={busy() ? 'Checking' : !value().trim() ? 'Paste a key' : undefined} onClick={add} />
          <Button label="Cancel" kind="quiet" onClick={props.onDone} />
        </div>
      </>}>
        <Choice label="Provider" options={PROVIDERS} value={provider()} onChange={setProvider} />
        <TextField label="Connection name" value={name()} onChange={setName} help={`Empty: ${provider()}. A second account of one provider takes its own name (openai-work).`} />
        <div class="res-bar"><Button label="Next" kind="primary" disabledReason={busy() ? 'Preparing' : undefined} onClick={prepare} />
          <Button label="Cancel" kind="quiet" onClick={props.onDone} /></div>
      </Show>
      <Show when={err()}>{e => <ErrorBanner text={e()} />}</Show>
      <Show when={result()}>{r => <>
        <div><StateBadge family="access" state={r().state} /> <span>{r().stateText}{r().lastCheck && !r().lastCheck!.ok ? `: ${r().lastCheck!.text}` : ''}</span></div>
        <Show when={r().state !== 'works' && r().guide?.billingUrl}>{u => <Button label="Open billing" icon="external-link" onClick={() => window.open(u(), '_blank', 'noopener')} />}</Show>
        <div class="res-bar"><Button label="Done" kind="primary" onClick={props.onDone} /></div>
      </>}</Show>
    </div>
  </DetailSection>;
}

/** Sign a subscription in on a host: Fleet starts the vendor login there only on Sign in, then shows the link and code
 * the host printed; the owner signs in in the browser and Fleet never sees the password. */
function LoginFlow(props: { flow: Flow & { kind: 'login' }; onDone: () => void }) {
  const it = () => props.flow.item;
  const accountId = () => it().id.slice('account:'.length);
  const [host, setHost] = createSignal(props.flow.hostId ?? it().hosts?.[0]?.hostId ?? '');
  const binding = () => props.flow.binding ?? it().hosts?.find(h => h.hostId === host())?.binding;
  // started: the host's previous prompt time at Sign in (0: none); a newer prompt is this login's (host clock only).
  const [started, setStarted] = createSignal<number | null>(null);
  const [err, setErr] = createSignal('');
  // While a login runs the host reports its link: reread every 3 s until it shows (access_status, the same read agents use).
  const live = useOp('access_status', () => (started() !== null ? {} : null), { every: 3000 });
  const prompt = createMemo(() => {
    const before = started();
    const h = live.data?.items.find(i => i.id === it().id)?.hosts?.find(x => x.hostId === host());
    return before !== null && h?.login && h.login.startedAt > before ? h.login : null;
  });
  const start = () => {
    setErr('');
    const before = it().hosts?.find(x => x.hostId === host())?.login?.startedAt ?? 0;
    void op('access_onboard', { kind: 'login', accountId: accountId(), hostId: host(), ...(binding() ? { binding: binding()! } : {}), start: true })
      .then(() => setStarted(before), e => setErr(String(e.message ?? e)));
  };
  const recheck = () => void op('access_check', { id: it().id }).then(props.onDone, props.onDone);
  return <DetailSection title={`Sign in ${it().name}`}>
    <div class="stack sys-form">
      <Show when={(it().hosts ?? []).length} fallback={<span class="muted">No host has a login place for this account yet. Add the account on a host first (System › Hosts).</span>}>
        <Choice label="Host" options={(it().hosts ?? []).map(h => ({ id: h.hostId, label: h.hostId }))} value={host()} onChange={v => { setHost(v); setStarted(null); }} />
      </Show>
      <span class="muted">Fleet starts {it().guide?.login ?? 'the login'} on {host() || 'the host'} only when you press Sign in. Sign in as {it().name}; Fleet never sees the password.</span>
      <Show when={err()}>{e => <ErrorBanner text={e()} />}</Show>
      <Show when={started() !== null} fallback={<div class="res-bar">
        <Button label={`Sign in on ${host() || 'host'}`} kind="primary" disabledReason={host() ? undefined : 'Pick a host'} onClick={start} />
        <Button label="Cancel" kind="quiet" onClick={props.onDone} /></div>}>
        <Show when={prompt()} fallback={<span class="t-running">Waiting for the host…</span>}>{p => <Switch>
          <Match when={p().state === 'waiting'}>
            <Show when={p().url}>{u => <KvRow icon="external-link" label="Open" value={<a href={u()} target="_blank" rel="noopener">{u()}</a>} />}</Show>
            <Show when={p().code}>{c => <KvRow icon="shield" label="Code" value={<code>{c()}</code>} meta={<Button label="Copy" icon="copy" iconOnly kind="quiet" onClick={() => copyText(c())} />} />}</Show>
            <span class="muted">The host waits up to 15 minutes for you to finish. Then press Check.</span>
          </Match>
          <Match when={p().state === 'signed-in'}><span class="t-success">The host's login ended without an error. Press Check.</span></Match>
          <Match when={p().state === 'failed'}><ErrorBanner text={p().text ?? 'The login did not start on the host.'} /></Match>
        </Switch>}</Show>
        <div class="res-bar"><Button label="Check" kind="primary" onClick={recheck} /><Button label="Close" kind="quiet" onClick={props.onDone} /></div>
      </Show>
    </div>
  </DetailSection>;
}

/** The monthly limit Fleet counts a connection's spend against (a warn budget). */
function LimitFlow(props: { flow: Flow & { kind: 'limit' }; onDone: () => void }) {
  const it = () => props.flow.item;
  const [amount, setAmount] = createSignal(String(it().limit?.budget?.limit ?? ''));
  const [unit, setUnit] = createSignal(it().limit?.budget?.unit ?? 'calls');
  const [err, setErr] = createSignal('');
  const save = () => {
    const n = Number(amount());
    if (!(n > 0)) return;
    void op('access_onboard', { kind: 'limit', connection: it().name, limit: { amount: n, unit: unit(), period: 'month' } })
      .then(props.onDone, e => setErr(String(e.message ?? e)));
  };
  return <DetailSection title={`Monthly limit · ${it().name}`}>
    <div class="stack sys-form">
      <span class="muted">Where the provider does not report credits for a key, Fleet counts this connection's calls against your limit and warns when it is reached.</span>
      <TextField label="Amount per month" type="number" value={amount()} onChange={setAmount} />
      <Choice label="Unit" options={[{ id: 'calls', label: 'calls' }, { id: 'tokens', label: 'tokens' }, { id: 'credits', label: 'credits' }]} value={unit()} onChange={setUnit} />
      <Show when={err()}>{e => <ErrorBanner text={e()} />}</Show>
      <div class="res-bar"><Button label="Save" kind="primary" disabledReason={Number(amount()) > 0 ? undefined : 'Enter an amount'} onClick={save} />
        <Button label="Cancel" kind="quiet" onClick={props.onDone} /></div>
    </div>
  </DetailSection>;
}
