// Bot tile: a bot with no active run on Home, with Wake (controls/bot-tile.md, OR-50, board master `Bot tile`).
import { Show } from 'solid-js';
import { age } from '../format.ts';
import { Button, Ago } from './primitives.tsx';
import { Avatar, type AvatarMark } from './avatar.tsx';

/**
 * Asleep: avatar, name, purpose and last use, Wake. Waking: Wake is busy until the session's card appears.
 * Wake failed: a danger border, the reason in place of the purpose, and Retry. A click on the tile opens the bot's
 * session view; Wake opens nothing.
 */
export function BotTile(p: {
  name: string; about: string; avatar: AvatarMark; lastUsed?: number; clock?: number;
  state?: 'asleep' | 'waking' | 'failed'; reason?: string;
  onWake?: () => void; onOpen?: () => void;
}) {
  const state = () => p.state ?? 'asleep';
  const last = () => p.lastUsed !== undefined ? `, last used ${age(p.lastUsed, p.clock ?? Date.now()).long}` : '';
  const open = (e: KeyboardEvent | MouseEvent) => {
    if (e instanceof KeyboardEvent && (e.key !== 'Enter' || e.target !== e.currentTarget)) return;
    p.onOpen?.();
  };
  return (
    <div
      class={['bot-tile', state()]}
      data-ui="bot-tile"
      role="group"
      tabindex={0}
      aria-label={`${p.name}, ${state() === 'failed' ? `wake failed: ${p.reason ?? ''}` : state()}${last()}`}
      onClick={open}
      onKeyDown={open}
    >
      <Avatar {...p.avatar} />
      <span class="bot-tile-text">
        <b>{p.name}</b>
        <span class="bot-tile-about">
          <Show
            when={state() === 'asleep'}
            fallback={state() === 'waking' ? 'waking…' : `wake failed: ${p.reason ?? 'unknown reason'}`}
          >
            {p.about}<Show when={p.lastUsed !== undefined}> · <Ago t={p.lastUsed!} /></Show>
          </Show>
        </span>
      </span>
      <span class="bot-tile-act" onClick={e => e.stopPropagation()}>
        <Button
          label={state() === 'failed' ? 'Retry' : state() === 'waking' ? 'Waking' : 'Wake'}
          kind={state() === 'failed' ? 'danger' : 'default'}
          busy={state() === 'waking'}
          name={`${state() === 'failed' ? 'Retry waking' : 'Wake'} ${p.name}`}
          onClick={() => p.onWake?.()}
        />
      </span>
    </div>
  );
}
