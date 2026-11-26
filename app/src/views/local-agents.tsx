// Local agents on System › Access (docs/designs/fleet-connections.md#13-local-agents; board drafts/local-agents.bl):
// a person's own Claude Code, Codex and Antigravity on a Fleet host, connected by choice. The report reads
// local_agents: one row per machine and harness with its state, connections and last call; a row opens the
// connections with their last success and failure, Refresh and Disconnect. Connect picks a machine and harnesses and
// shows one command per harness (local_agent_connect); a new token's value is inside its command, shown once.
import { createMemo, createSignal, For, Show } from 'solid-js';
import { AgeChip, Button, Choice, CopyButton, DetailSection, ErrorBanner, KvRow, ReflowRow, ReflowTable, StateBadge } from '../../../packages/ui/src/index.ts';
import { op, type Ops } from '../api.ts';
import { useOp } from '../store.ts';

type Agent = Ops['local_agents']['reply']['agents'][number];
type Connected = Ops['local_agent_connect']['reply'];

const HARNESS: Record<string, string> = { claude: 'Claude Code', codex: 'Codex', antigravity: 'Antigravity' };
const HARNESSES = ['claude', 'codex', 'antigravity'];

/** The row's sentence: what works, what to do. */
function sentence(a: Agent): string {
  switch (a.state) {
    case 'refresh': return `${a.missing.join(', ')} ${a.missing.length === 1 ? 'is' : 'are'} new for you: refresh, then run the command`;
    case 'unused': return 'Connected; no call yet. Run the command on the machine, or open a new session';
    case 'disconnected': return 'Disconnected: its token is revoked';
    default: {
      const failed = a.connections.filter(c => c.failAt && (!c.okAt || c.failAt > c.okAt)).length;
      const answered = a.connections.filter(c => c.okAt).length;
      return failed ? `${failed} of ${a.connections.length} failed last time` : `${answered} of ${a.connections.length} connections answered`;
    }
  }
}

export function LocalAgents() {
  const read = useOp('local_agents', {});
  const [open, setOpen] = createSignal('');
  const [err, setErr] = createSignal('');
  const [busy, setBusy] = createSignal('');
  const [connecting, setConnecting] = createSignal(false);
  const [commands, setCommands] = createSignal<Connected[]>([]);
  const agents = createMemo(() => read.data?.agents ?? []);
  const counts = createMemo(() => {
    const by = (s: Agent['state']) => agents().filter(a => a.state === s).length;
    const parts = [[by('connected'), 'connected'], [by('refresh'), 'need a refresh'], [by('unused'), 'unused']] as const;
    return parts.filter(([n]) => n > 0).map(([n, w]) => `${n} ${w}`).join(', ');
  });
  const connect = (machine: string, harness: string, reissue = false) => {
    setBusy(machine + harness); setErr('');
    return op('local_agent_connect', { machine, harness, ...(reissue ? { reissue } : {}) }).then(r => {
      setBusy(''); read.reload();
      setCommands(cs => [...cs.filter(c => c.name !== r.name), r]);
      return r;
    }, e => { setBusy(''); setErr(String(e.message ?? e)); });
  };
  const disconnect = (a: Agent) => {
    setBusy(a.name); setErr('');
    void op('local_agent_disconnect', { machine: a.machine, harness: a.harness }).then(() => { setBusy(''); read.reload(); }, e => { setBusy(''); setErr(String(e.message ?? e)); });
  };
  return <DetailSection title="Local agents" count={agents().length || undefined}
    action={<Button label="Connect a local agent" icon="plus" kind="primary" onClick={() => setConnecting(v => !v)} />}>
    <div class="stack"><p class="muted">{counts() ? counts() + '. ' : ''}Your own Claude Code, Codex and Antigravity with Fleet tools. Fleet runs need none of this: each run gets its tools at dispatch.</p>
    <Show when={err()}>{e => <ErrorBanner text={e()} />}</Show>
    <Show when={connecting()}><ConnectFlow busy={busy()} onConnect={(m, h) => connect(m, h)} commands={commands()} /></Show>
    <Show when={agents().length}>
      <ReflowTable label="Local agents" head={['Agent', 'State', 'Connections', 'Last call']} cols="minmax(10rem, 1fr) minmax(12rem, 3fr) auto auto">
        <For each={agents()}>{a =>
          <ReflowRow name={`${HARNESS[a.harness] ?? a.harness} · ${a.machine}`} selected={open() === a.name} onOpen={() => setOpen(o => o === a.name ? '' : a.name)}
            cells={[
              { part: 'state', el: <span class="res-bar"><StateBadge family="localAgent" state={a.state} /> <span class="muted">{sentence(a)}</span></span> },
              { part: 'meta', el: a.state === 'refresh' ? `${a.connections.length} of ${a.connections.length + a.missing.length}` : `${a.connections.length} connections` },
              { part: 'time', el: a.lastOkAt ? <AgeChip t={a.lastOkAt} /> : undefined },
            ]}
            detail={<div class="stack access-detail">
              <span class="muted">Token {a.name}{a.connectedAt ? ' · connected ' : ''}<Show when={a.connectedAt}>{t => <AgeChip t={t()} />}</Show></span>
              <For each={a.connections}>{c =>
                <KvRow icon="shield" label={c.name} meta={c.okTool ?? c.failTool ?? 'no call yet'}
                  value={<span class="res-bar">
                    <Show when={c.okAt}>{t => <span class="t-success">ok <AgeChip t={t()} /></span>}</Show>
                    <Show when={c.failAt}>{t => <span class="t-danger">failed <AgeChip t={t()} />{c.failCode ? `: ${c.failCode}` : ''}</span>}</Show>
                  </span>} />}</For>
              <Show when={a.state !== 'disconnected'}>
                <span class="muted">Tools in a session: {a.connections.slice(0, 2).map(c => `mcp__${c.name}__…`).join(', ')}</span>
              </Show>
              <Show when={commands().find(c => c.name === a.name)}>{c => <Command c={c()} />}</Show>
              <div class="res-bar">
                <Button label={busy() === a.machine + a.harness ? 'Refreshing' : a.state === 'disconnected' ? 'Connect again' : 'Refresh'} icon="rotate-cw"
                  disabledReason={busy() ? 'Working' : undefined} onClick={() => void connect(a.machine, a.harness, a.state === 'disconnected')} />
                <Show when={a.state !== 'disconnected'}>
                  <Button label="Disconnect" icon="circle-slash" kind="quiet" disabledReason={busy() ? 'Working' : undefined} onClick={() => disconnect(a)} />
                </Show>
              </div>
            </div>} />}</For>
      </ReflowTable>
    </Show>
    </div>
  </DetailSection>;
}

/** One harness's command with Copy; it holds the token value only when it was just minted. */
function Command(props: { c: Connected }) {
  return <div class="stack">
    <span class="muted">Run on {props.c.machine} in a terminal{props.c.newToken ? '. The key is inside and shown once' : ''}:</span>
    <pre class="access-lines">{props.c.command}</pre>
    <div class="res-bar"><CopyButton label="Copy command" value={props.c.command} /></div>
  </div>;
}

function ConnectFlow(props: { busy: string; commands: Connected[]; onConnect: (machine: string, harness: string) => Promise<unknown> }) {
  const hosts = useOp('hosts_list', {});
  const [machine, setMachine] = createSignal('');
  const [harnesses, setHarnesses] = createSignal<string[]>(['claude', 'codex', 'antigravity']);
  const machines = createMemo(() => (hosts.data?.hosts ?? []).map(h => h.id));
  const pick = () => machine() || machines()[0] || '';
  const toggle = (h: string) => setHarnesses(hs => hs.includes(h) ? hs.filter(x => x !== h) : [...hs, h]);
  const go = async () => { for (const h of harnesses()) await props.onConnect(pick(), h); };
  return <div class="stack access-detail">
    <span class="muted">1. Machine: a Fleet host you work on</span>
    <Choice label="Machine" options={machines().map(id => ({ id, label: id }))} value={pick()} onChange={setMachine} />
    <span class="muted">2. Harnesses</span>
    <Choice label="Harnesses" options={HARNESSES.map(id => ({ id, label: HARNESS[id] }))} value={harnesses()} onChange={toggle} />
    <div class="res-bar">
      <Button label={props.busy ? 'Getting commands' : 'Get commands'} icon="terminal" kind="primary"
        disabledReason={!pick() ? 'No host' : !harnesses().length ? 'Pick a harness' : props.busy ? 'Working' : undefined} onClick={() => void go()} />
    </div>
    <span class="muted">3. Run each command on {pick() || 'the machine'}. It saves the key, adds Fleet tools to that harness and calls each connection once. Settings that are not Fleet tools stay as they are.</span>
    <For each={props.commands.filter(c => c.machine === pick())}>{c =>
      <div class="stack"><b>{HARNESS[c.harness] ?? c.harness}</b><Command c={c} /></div>}</For>
  </div>;
}
