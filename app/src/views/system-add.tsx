// System › Add host and Add connection (docs/design-system/pages/system.md; canvas board/screens/system.bl states 6-8):
// work-area forms over the existing operations hosts_register and connections_create, as agents call them.
import { createSignal, Show } from 'solid-js';
import {
  Button, Choice, DetailHead, DetailSection, ErrorBanner, KvRow, ProblemRow, TextField, copyText,
} from '../../../packages/ui/src/index.ts';
import { op } from '../api.ts';
import { useOp } from '../store.ts';

const list = (v: string) => v.split(',').map(x => x.trim()).filter(Boolean);
const HOST_ID = /^[a-z0-9][a-z0-9-]{0,62}$/;
const HEX64 = /^[0-9a-f]{64}$/;

/** Add host: hosts_register with the host's own secret hash, or a minted secret shown here once. */
export function HostAdd(props: { onClose: () => void; onOpen: (path: string) => void }) {
  const [id, setId] = createSignal('');
  const [holder, setHolder] = createSignal('');
  const [hash, setHash] = createSignal('');
  const [roles, setRoles] = createSignal('');
  const [harnesses, setHarnesses] = createSignal('claude, codex');
  const [busy, setBusy] = createSignal(false);
  const [err, setErr] = createSignal('');
  // The minted secret lives only in this view: never stored, gone when the person leaves.
  const [minted, setMinted] = createSignal<{ id: string; secret: string } | null>(null);
  const idError = () => id() && !HOST_ID.test(id()) ? 'Lowercase letters, digits and dashes.' : undefined;
  const hashError = () => hash().trim() && !HEX64.test(hash().trim().toLowerCase()) ? 'Expected 64 hex characters.' : undefined;
  const submit = () => {
    if (!id() || idError() || hashError()) return;
    setBusy(true); setErr('');
    const hs = list(harnesses());
    void op('hosts_register', {
      id: id(),
      ...(holder().trim() ? { holder: holder().trim() } : {}),
      ...(hash().trim() ? { secretSha256: hash().trim().toLowerCase() } : {}),
      ...(list(roles()).length ? { roles: list(roles()) } : {}),
      ...(hs.length ? { harnesses: Object.fromEntries(hs.map(h => [h, { allowed: true }])) } : {}),
    }).then(r => {
      setBusy(false);
      if (r.secret) setMinted({ id: r.host.id, secret: r.secret });
      else props.onOpen(`/hosts/${encodeURIComponent(r.host.id)}`);
    }, e => { setBusy(false); setErr(String(e.message ?? e)); });
  };
  return <article class="detail" data-ui="detail" tabindex={-1} aria-label="New host">
    <Show when={minted()} fallback={<>
      <DetailHead crumb="system · host · new" title="New host" onClose={props.onClose}
        state={<span class="muted">The secret stays on the host: only its SHA-256 is sent. Fleet starts no work there until the first poll.</span>} />
      <Show when={err()}>{e => <ErrorBanner text={e()} />}</Show>
      <div class="stack sys-form">
        <TextField label="Host id" value={id()} onChange={setId} error={idError()} help="Lowercase letters, digits and dashes; it names the host everywhere." />
        <TextField label="Holder" value={holder()} onChange={setHolder} type="email" help="Empty: you. The host sees only projects its holder has a grant on." />
        <TextField label="Secret SHA-256" value={hash()} onChange={setHash} error={hashError()}
          help="On the host run scripts/host-secret.sh <file>; paste the hash it prints. Empty: Fleet makes one and shows it once." />
        <TextField label="Roles" value={roles()} onChange={setRoles} help="Optional: publication, inspector-for:<host>." />
        <TextField label="Harnesses" value={harnesses()} onChange={setHarnesses} help="Harnesses allowed to run here; change later on the host page." />
        <div class="res-bar">
          <Button label="Add host" kind="primary" onClick={submit} disabledReason={busy() ? 'Adding' : !id() || idError() || hashError() ? 'Enter a valid host id' : undefined} />
          <Button label="Cancel" kind="quiet" onClick={props.onClose} />
        </div>
      </div>
    </>}>{m => <>
      <DetailHead crumb="system · host" title={m().id} onClose={props.onClose}
        state={<span class="muted">registered · waiting for its first poll</span>} />
      <ProblemRow severity="warning" title="Copy the host secret now: Fleet shows it once"
        facts={<><span class="mono">{m().secret.slice(0, 8)}…</span> · write it on {m().id} to the file FLEET_HOST_SECRET_FILE names (0600); Fleet keeps only its SHA-256 · lost: rotate it on the host page</>}
        actions={[
          { label: 'Copy secret', primary: true, onClick: () => copyText(m().secret) },
          { label: 'I saved it', onClick: () => props.onOpen(`/hosts/${encodeURIComponent(m().id)}`) },
        ]} />
      <DetailSection title="Next, on the host">
        <KvRow icon="save" label="Secret file" meta="FLEET_HOST_SECRET_FILE=~/.fleet/host-secret, mode 0600" value="1" />
        <KvRow icon="terminal" label="Executor" meta="start fleet-executor; the host turns online on its first poll" value="2" />
        <KvRow icon="gauge" label="Accounts" meta="bind subscriptions; their models are probed at inspection (J5)" value="3" />
      </DetailSection>
    </>}</Show>
  </article>;
}

/** Add connection: connections_create; its key is added on the connection page, where the value goes to the secret file. */
export function ConnectionAdd(props: { onClose: () => void; onOpen: (path: string) => void }) {
  const conns = useOp('connections_list', {});
  const [name, setName] = createSignal('');
  const [provider, setProvider] = createSignal('');
  const [holder, setHolder] = createSignal('');
  const [comment, setComment] = createSignal('');
  const [busy, setBusy] = createSignal(false);
  const [err, setErr] = createSignal('');
  const providers = () => Object.keys(conns.data?.providers ?? {}).toSorted().map(p => ({ id: p, label: p }));
  const nameError = () => name() && !HOST_ID.test(name()) ? 'Lowercase letters, digits and dashes.' : undefined;
  const submit = () => {
    if (!name() || nameError() || !provider()) return;
    setBusy(true); setErr('');
    void op('connections_create', {
      name: name(), provider: provider(),
      ...(holder().trim() ? { holder: holder().trim() } : {}),
      ...(comment().trim() ? { comment: comment().trim() } : {}),
    }).then(() => { setBusy(false); void conns.reload(); props.onOpen(`/keys/${encodeURIComponent(name())}`); },
      e => { setBusy(false); setErr(String(e.message ?? e)); });
  };
  return <article class="detail" data-ui="detail" tabindex={-1} aria-label="New connection">
    <DetailHead crumb="system · connection · new" title="New connection" onClose={props.onClose}
      state={<span class="muted">An API key connection used through Fleet's gateway. No key value here: add it on the connection page next.</span>} />
    <Show when={err() || conns.error}>{e => <ErrorBanner text={e()} onRetry={conns.error ? conns.reload : undefined} />}</Show>
    <div class="stack sys-form">
      <TextField label="Name" value={name()} onChange={setName} error={nameError()} help="Tools and run tokens name the connection by this." />
      <div class="stack">
        <b>Provider</b>
        <Choice label="Provider" options={providers()} value={provider()} onChange={setProvider} />
        <span class="muted">From Fleet's provider catalogue.</span>
      </div>
      <TextField label="Holder" value={holder()} onChange={setHolder} type="email" help="Empty: you. Work directed by the holder may use it; grant projects and people after." />
      <TextField label="Comment" value={comment()} onChange={setComment} help="Optional." />
      <div class="res-bar">
        <Button label="Add connection" kind="primary" onClick={submit} disabledReason={busy() ? 'Adding' : !name() || nameError() || !provider() ? 'Enter a name and pick a provider' : undefined} />
        <Button label="Cancel" kind="quiet" onClick={props.onClose} />
      </div>
    </div>
  </article>;
}
