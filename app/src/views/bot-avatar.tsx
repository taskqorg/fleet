// A bot's avatar in Settings & Memory (controls/avatar.md, board screens/library.bl section 4): the letter, an uploaded
// file, or one made by the avatar-make pipeline (three candidates, pick one). Agents do the same with bot_avatar_set.
import { For, Match, Show, Switch, createEffect, createSignal } from 'solid-js';
import { Avatar, AvatarChoices, Button, Choice, DetailSection, ErrorBanner, FileButton, IdentityPicker, TextField, type AvatarPicture } from '../../../packages/ui/src/index.ts';
import { writeOnce, useAlive, type Ops } from '../api.ts';
import { useOp } from '../store.ts';
import { botAvatarState, botColor, botPicture } from '../bot-view.ts';
import { uploadFile } from '../uploads.ts';

type Bot = Ops['bot_get']['reply']['bot'];
type Source = 'letter' | 'upload' | 'make';
/** A made or uploaded avatar before it is set: the shown file, its still frame, its glow. */
type Candidate = { runId?: string; artifactId: string; stillArtifactId?: string; mediaType: string; glow?: string; state: 'running' | 'ready' | 'failed'; note?: string };

/** What avatar-make returns (scripts/lab/avatar.py result). */
type MadeAvatar = { avatar: { artifactId: string; mediaType: string }; still?: { artifactId: string }; glow?: string };
const result = (id: string) => `/api/results/${encodeURIComponent(id)}`;
const pictureOf = (c: Candidate): AvatarPicture => ({ src: result(c.artifactId), still: c.stillArtifactId ? result(c.stillArtifactId) : undefined, video: c.mediaType.startsWith('video/'), glow: c.glow });
const STILL_PX = 192;

/** The first frame of an image, animated image, SVG or video, as a PNG the lists show in place of the animation. */
async function stillFrame(file: File): Promise<File> {
  const url = URL.createObjectURL(file);
  try {
    const el: HTMLImageElement | HTMLVideoElement = file.type.startsWith('video/') ? document.createElement('video') : new Image();
    if (el instanceof HTMLVideoElement) {
      el.muted = true; el.preload = 'auto'; el.src = url;
      await new Promise((ok, no) => { el.onloadeddata = ok; el.onerror = no; });
    } else {
      el.src = url; await el.decode();
    }
    const w = el instanceof HTMLVideoElement ? el.videoWidth : el.naturalWidth || STILL_PX;
    const h = el instanceof HTMLVideoElement ? el.videoHeight : el.naturalHeight || STILL_PX;
    const side = Math.min(w, h);
    const canvas = new OffscreenCanvas(STILL_PX, STILL_PX);
    canvas.getContext('2d')!.drawImage(el, (w - side) / 2, (h - side) / 2, side, side, 0, 0, STILL_PX, STILL_PX);
    const blob = await canvas.convertToBlob({ type: 'image/png' });
    return new File([blob], (file.name.replace(/\.[^.]*$/, '') || 'avatar') + '-still.png', { type: 'image/png' });
  } finally {
    URL.revokeObjectURL(url);
  }
}

export function BotAvatarField(props: { bot: Bot; onSaved: (bot: Bot) => void }) {
  const signal = useAlive();
  const [source, setSource] = createSignal<Source>(props.bot.avatar ? 'upload' : 'letter');
  const [busy, setBusy] = createSignal(false);
  const [error, setError] = createSignal('');
  const [status, setStatus] = createSignal('');
  const [describe, setDescribe] = createSignal('');
  const [animate, setAnimate] = createSignal(true);
  const [reference, setReference] = createSignal<{ id: string; name: string }>();
  const [candidates, setCandidates] = createSignal<Candidate[]>([]);
  const [picked, setPicked] = createSignal(0);

  const current = () => botPicture(props.bot);
  const preview = () => {
    const c = candidates()[picked()];
    return c?.state === 'ready' ? pictureOf(c) : current();
  };
  const state = () => botAvatarState(props.bot);
  const project = () => ({ projectId: props.bot.projectId });

  const act = async (what: string, fn: () => Promise<void>) => {
    if (busy()) return;
    setBusy(true); setError(''); setStatus(what);
    try { await fn(); } catch (e) {
      if (!signal.aborted) { setStatus(''); setError(e instanceof Error ? e.message : String(e)); }
    } finally {
      if (!signal.aborted) setBusy(false);
    }
  };

  const set = (c: Pick<Candidate, 'artifactId' | 'stillArtifactId' | 'glow'>) => act('Setting the avatar…', async () => {
    const rep = await writeOnce('bot_avatar_set', { botId: props.bot.id, artifactId: c.artifactId, ...(c.stillArtifactId ? { stillArtifactId: c.stillArtifactId } : {}), ...(c.glow ? { glow: c.glow } : {}) }, () => {}, { signal });
    setCandidates([]); setStatus('Avatar set.'); props.onSaved(rep.bot);
  });
  const clear = () => act('Clearing the avatar…', async () => {
    const rep = await writeOnce('bot_avatar_clear', { botId: props.bot.id }, () => {}, { signal });
    setCandidates([]); setSource('letter'); setStatus('The bot shows its letter.'); props.onSaved(rep.bot);
  });
  const upload = (file: File) => act('Uploading…', async () => {
    const still = await stillFrame(file).catch(() => undefined);
    const artifactId = await uploadFile(file, project(), signal, () => {});
    const stillArtifactId = still ? await uploadFile(still, project(), signal, () => {}) : undefined;
    await set({ artifactId, stillArtifactId });
  });
  const attachReference = (file: File) => act('Uploading the reference…', async () => {
    setReference({ id: await uploadFile(file, project(), signal, () => {}), name: file.name });
    setStatus('');
  });

  /** One avatar-make run: started here; its RunWatch reads it (live) until it ends. */
  const update = (i: number, c: Partial<Candidate>) => setCandidates(cs => cs.map((x, k) => (k === i ? { ...x, ...c } : x)));
  const makeOne = async (i: number) => {
    const example: Record<string, unknown> = { description: describe(), animate: animate() };
    const ref = reference();
    if (ref) {
      const path = 'reference' + (/\.(png|jpe?g|webp)$/i.exec(ref.name)?.[0].toLowerCase() ?? '.png');
      example.reference = path;
      example.inputs = [{ path, artifactId: ref.id }];
    }
    try {
      const { runId } = await writeOnce('pipeline_run_create', { pipelineId: 'avatar-make', projectId: props.bot.projectId, requestId: crypto.randomUUID(), example }, () => {}, { signal });
      update(i, { runId });
    } catch (e) {
      if (!signal.aborted) update(i, { state: 'failed', note: e instanceof Error ? e.message : String(e) });
    }
  };
  const make = () => {
    setError(''); setStatus(''); setPicked(0);
    setCandidates([0, 1, 2].map(() => ({ artifactId: '', mediaType: '', state: 'running' as const })));
    void Promise.all([0, 1, 2].map(makeOne));
  };

  return (
    <DetailSection title="Avatar">
      <Show when={error()}>{err => <ErrorBanner text={err()} />}</Show>
      <div class="row" style={{ gap: 'var(--space-6)', 'align-items': 'flex-start', 'flex-wrap': 'wrap' }}>
        <div class="stack" style={{ 'align-items': 'center', gap: 'var(--space-3)' }}>
          <Avatar letter={props.bot.settings.name} tone="success" size="xl" picture={preview()} state={state()} color={botColor(props.bot)} />
          <div class="row" style={{ gap: 'var(--space-3)', 'align-items': 'center' }}>
            <Avatar letter={props.bot.settings.name} tone="success" size="sm" picture={preview()} color={botColor(props.bot)} />
            <Avatar letter={props.bot.settings.name} tone="success" size="md" picture={preview()} color={botColor(props.bot)} />
            <Avatar letter={props.bot.settings.name} tone="success" size="lg" picture={preview()} color={botColor(props.bot)} />
          </div>
          <span class="muted type-sm">as lists, tiles, header</span>
        </div>
        <div class="stack" style={{ flex: '1 1 var(--size-detail)', 'min-width': 0 }}>
          <p class="muted" style={{ margin: 0 }}>Shown on every row, tile and header of this bot. Without one the bot shows its letter.</p>
          <Choice label="Avatar source" value={source()} onChange={v => setSource(v as Source)}
            options={[{ id: 'letter', label: 'Letter' }, { id: 'upload', label: 'Upload file' }, { id: 'make', label: 'Make with pipeline' }]} />
          <Switch>
            <Match when={source() === 'letter'}>
              <Show when={props.bot.avatar} fallback={<p class="muted" style={{ margin: 0 }}>The bot shows the first letter of its name.</p>}>
                <Button label="Use the letter" busy={busy()} onClick={() => void clear()} />
              </Show>
            </Match>
            <Match when={source() === 'upload'}>
              <div class="row" style={{ gap: 'var(--space-2)', 'align-items': 'center', 'flex-wrap': 'wrap' }}>
                <FileButton label="Upload file…" icon="plus" accept="image/*,video/webm,video/mp4" busy={busy()} onFile={f => void upload(f)} />
                <Show when={props.bot.avatar}><Button label="Clear avatar" kind="quiet" busy={busy()} onClick={() => void clear()} /></Show>
              </div>
              <p class="muted type-sm" style={{ margin: 0 }}>Any picture or loop: PNG, JPEG, WebP, AVIF, GIF, APNG, SVG, WebM, MP4. Fleet keeps a still frame for lists.</p>
            </Match>
            <Match when={source() === 'make'}>
              <TextField label="Describe it" multiline value={describe()} onChange={setDescribe}
                help="What the avatar shows. A still picture comes first; Animate adds a loop over it." />
              <div class="row" style={{ gap: 'var(--space-2)', 'align-items': 'center', 'flex-wrap': 'wrap' }}>
                <FileButton label={reference() ? `Reference: ${reference()!.name}` : 'Attach reference…'} kind="quiet" accept="image/*" busy={busy()} onFile={f => void attachReference(f)} />
                <Show when={reference()}><Button label="Remove reference" icon="x" iconOnly kind="quiet" onClick={() => setReference(undefined)} /></Show>
                <span class="muted type-sm">optional · a picture to follow</span>
              </div>
              <Choice label="Animate" value={animate() ? 'loop' : 'still'} onChange={v => setAnimate(v === 'loop')}
                options={[{ id: 'still', label: 'Still' }, { id: 'loop', label: 'Animated' }]} />
              <Button label="Make avatar" kind="primary" busy={candidates().some(c => c.state === 'running')}
                disabledReason={describe().trim() || reference() ? undefined : 'Describe it or attach a reference'} onClick={make} />
              <Show when={candidates().length > 0}>
                <span class="muted type-sm">Pick one</span>
                <AvatarChoices label="Made avatars" picked={picked()} onPick={setPicked}
                  items={candidates().map(c => ({ letter: props.bot.settings.name, picture: c.state === 'ready' ? pictureOf(c) : undefined,
                    caption: c.state === 'ready' ? (c.stillArtifactId ? 'still + loop' : 'still') : c.state === 'running' ? 'making…' : (c.note ?? 'failed') }))} />
                <div class="row" style={{ gap: 'var(--space-2)', 'flex-wrap': 'wrap' }}>
                  <Button label="Use this" kind="primary" busy={busy()} disabledReason={candidates()[picked()]?.state === 'ready' ? undefined : 'Wait for a result'}
                    onClick={() => void set(candidates()[picked()]!)} />
                  <Button label="Run again" busy={candidates().some(c => c.state === 'running')} onClick={make} />
                </div>
              </Show>
            </Match>
          </Switch>
          <p role="status" class="muted type-sm" style={{ margin: 0 }}>{status()}</p>
        </div>
      </div>
      <For each={candidates()}>
        {(c, i) => (
          <Show when={c.state === 'running' && c.runId} keyed>
            {runId => <RunWatch runId={runId} projectId={props.bot.projectId} onDone={r => update(i(), r)} />}
          </Show>
        )}
      </For>
    </DetailSection>
  );
}

/** Reads one avatar-make run live (pipeline_get follows execution changes); when it ends, its result: the result step's
 * output {projectResult: {avatar, still, glow}} (scripts/lab/avatar.py result). Renders nothing. */
function RunWatch(props: { runId: string; projectId: string; onDone: (c: Partial<Candidate>) => void }) {
  const run = useOp('pipeline_get', () => ({ runId: props.runId, projectId: props.projectId }));
  let done = false;
  createEffect(
    () => run.data,
    r => {
      if (done || !r) return;
      if (r.state === 'failed' || r.state === 'cancelled') { done = true; props.onDone({ state: 'failed', note: 'The run ' + r.state }); return; }
      if (r.state !== 'succeeded') return;
      done = true;
      const id = r.canonicalResult?.artifact.artifactId;
      if (!id) { props.onDone({ state: 'failed', note: 'No picture came back' }); return; }
      void fetch(result(id)).then(x => x.json() as Promise<{ projectResult?: MadeAvatar }>).then(out => {
        const made = out.projectResult;
        if (!made?.avatar) { props.onDone({ state: 'failed', note: 'No picture came back' }); return; }
        props.onDone({ state: 'ready', artifactId: made.avatar.artifactId, mediaType: made.avatar.mediaType, glow: made.glow || undefined,
          stillArtifactId: made.still && made.still.artifactId !== made.avatar.artifactId ? made.still.artifactId : undefined });
      }, e => props.onDone({ state: 'failed', note: e instanceof Error ? e.message : String(e) }));
    },
  );
  return null;
}

/** The bot's identity colour (OR-72, controls/project-tile.md): Auto (the stored automatic colour) or one of the
 * eight, solid or hatched; saved at once with bot_identity_set. Agents call the same operation. */
export function BotIdentityField(props: { bot: Bot; onSaved: (bot: Bot) => void }) {
  const signal = useAlive();
  const [error, setError] = createSignal('');
  const save = async (color: string, pattern: string) => {
    setError('');
    try {
      const rep = await writeOnce('bot_identity_set', { botId: props.bot.id, color, pattern }, () => {}, { signal });
      if (!signal.aborted) props.onSaved(rep.bot);
    } catch (e) { if (!signal.aborted) setError(e instanceof Error ? e.message : String(e)); }
  };
  return (
    <DetailSection title="Colour">
      <Show when={error()}>{err => <ErrorBanner text={err()} />}</Show>
      <div class="row" style={{ gap: 'var(--space-4)', 'align-items': 'center', 'flex-wrap': 'wrap' }}>
        <Avatar letter={props.bot.settings.name} tone="success" size="lg" picture={botPicture(props.bot)} color={botColor(props.bot)} />
        <IdentityPicker code={props.bot.settings.name.slice(0, 1).toUpperCase()} auto={props.bot.autoColor ?? undefined}
          color={props.bot.color} pattern={props.bot.pattern}
          onColor={c => void save(c ?? '', props.bot.pattern ?? '')} onPattern={x => void save(props.bot.color ?? '', x ?? '')} />
      </div>
    </DetailSection>
  );
}
