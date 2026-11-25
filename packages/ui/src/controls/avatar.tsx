// Avatar and project chip: who a card, tile or row is about (controls/avatar.md, board masters `Bot avatar`, `Avatar`,
// `Project chip`).
import { For, Match, Show, Switch, createSignal, onCleanup } from 'solid-js';
import { type Harness, type Tone, harnesses } from '../registry.ts';
import { HarnessMark } from './primitives.tsx';

/** What an avatar shows: a bot's picture, its harness mark, or a letter (`P` for a project PM) in a tone. */
export type AvatarMark = { letter?: string; harness?: Harness; tone?: Tone; picture?: AvatarPicture };

/** A bot's picture: `src` the shown file (still, animated image, animated SVG or a muted video loop), `still` its still
 * frame for places that do not play it, `video` when `src` is a video, `glow` the activity light's colour (#RRGGBB). */
export type AvatarPicture = { src: string; still?: string; video?: boolean; glow?: string };

/** A bot's activity as its avatar shows it (owner pick A, 25 Nov): asleep dim, working full with a breathing glow,
 * waiting full with an amber dot, failed dim with a red dot, paused dimmest. */
export type AvatarState = 'asleep' | 'working' | 'waiting' | 'failed' | 'paused';

export type AvatarSize = 'sm' | 'md' | 'lg' | 'xl';

const reduceQuery = typeof matchMedia === 'function' ? matchMedia('(prefers-reduced-motion: reduce)') : undefined;
const [reduced, setReduced] = createSignal(reduceQuery?.matches ?? false);
reduceQuery?.addEventListener('change', (e) => setReduced(e.matches));

const px: Record<AvatarSize, number> = { sm: 16, md: 24, lg: 40, xl: 96 };

/** Decorative by default: the name next to it carries the meaning. With `label` it is named on its own. A picture plays
 * at lg and xl, on hover and while the bot works; elsewhere and under reduced motion it shows its still frame.
 * `tip` is the tooltip of an avatar shown without its name (the collapsed rail); the dot's ring takes the colour of
 * `--avatar-ring` (default `surface`), set by the surface the avatar sits on (`rail`, `float`). */
export function Avatar(p: AvatarMark & { size?: AvatarSize; label?: string; state?: AvatarState; tip?: string }) {
  const size = () => p.size ?? 'md';
  const mark = () => !p.picture && !!p.harness && p.harness in harnesses;
  const [hover, setHover] = createSignal(false);
  const plays = () => !reduced() && (size() === 'lg' || size() === 'xl' || hover() || p.state === 'working');
  const shown = () => (plays() || !p.picture!.still ? p.picture!.src : p.picture!.still);
  return (
    <span
      class={['avatar', size(), `t-${p.tone ?? 'running'}`, { mark: mark(), pic: !!p.picture }]}
      data-ui="avatar"
      data-state={p.state}
      data-tip={p.tip}
      style={p.picture?.glow ? { '--avatar-glow': p.picture.glow } : undefined}
      role={p.label ? 'img' : undefined}
      aria-label={p.label}
      aria-hidden={p.label ? undefined : 'true'}
      onPointerEnter={() => p.picture?.still && setHover(true)}
      onPointerLeave={() => setHover(false)}
    >
      <Switch fallback={<span class="letter">{(p.letter ?? '?').slice(0, 1)}</span>}>
        <Match when={p.picture?.video && plays()}>
          <AvatarVideo src={p.picture!.src} still={p.picture!.still} px={px[size()]} />
        </Match>
        <Match when={p.picture?.video && p.picture.still}>
          <img src={p.picture!.still} alt="" width={px[size()]} height={px[size()]} decoding="async" loading="lazy" />
        </Match>
        <Match when={p.picture?.video}>
          <video src={p.picture!.src} width={px[size()]} height={px[size()]} muted playsinline preload="metadata" />
        </Match>
        <Match when={p.picture}>
          <img src={shown()} alt="" width={px[size()]} height={px[size()]} decoding="async" loading="lazy" />
        </Match>
        <Match when={mark()}>
          <HarnessMark h={p.harness!} />
        </Match>
      </Switch>
      <Show when={p.state === 'waiting' || p.state === 'failed'}>
        <i class="avatar-dot" />
      </Show>
    </span>
  );
}

/** A muted loop that plays; reduced motion never mounts it (the still frame shows instead). */
function AvatarVideo(p: { src: string; still?: string; px: number }) {
  let v: HTMLVideoElement | undefined;
  onCleanup(() => v?.pause());
  return <video ref={v} src={p.src} poster={p.still} width={p.px} height={p.px} muted loop autoplay playsinline preload="metadata" />;
}

/** One avatar to choose among several (the avatar picker's results): its picture when ready, else a breathing placeholder,
 * and a caption. A radio group: arrows are the browser's, Enter or a click picks. */
export type AvatarChoiceItem = { picture?: AvatarPicture; letter?: string; caption: string };
export function AvatarChoices(p: { label: string; items: AvatarChoiceItem[]; picked: number; onPick: (i: number) => void }) {
  return (
    <div class="avatar-choices" role="radiogroup" aria-label={p.label} data-ui="avatar-choices">
      <For each={p.items}>
        {(c, i) => (
          <button type="button" role="radio" aria-checked={p.picked === i() ? 'true' : 'false'} class={['avatar-pick', { on: p.picked === i() }]}
            disabled={!c.picture} onClick={() => p.onPick(i())}>
            <Show when={c.picture} fallback={<span class="avatar xl pic avatar-making" aria-hidden="true" />}>
              {pic => <Avatar letter={c.letter} size="xl" picture={pic()} />}
            </Show>
            <span class="muted type-sm">{c.caption}</span>
          </button>
        )}
      </For>
    </div>
  );
}

/** The project a session belongs to, as a small pill. */
export function ProjectChip(p: { name: string }) {
  return <span class="pchip" data-ui="project-chip">{p.name}</span>;
}
