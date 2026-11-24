// Form inputs, settings, composer, drop zone, code editor, draft bar, and conversation start pickers.
import { createEffect, createMemo, createSignal, For, onSettled, Show, untrack } from 'solid-js';
import type { JSX } from '@solidjs/web';
import { Field, FieldDescription, FieldError, FieldLabel, Input, Textarea, Toggle } from '@ui-sdk/solid';
import { type IconName, type Harness, harnesses, actions } from '../registry.ts';
import { bytes, exact } from '../format.ts';
import { isSubmitEnter } from '../keys.ts';
import { Icon, Button, StateBadge, Ago, SearchBox, HarnessMark } from './primitives.tsx';

/** An unsaved form value; it never claims that a setting was applied (controls/text-field.md). */
export function TextField(p: {
  label: string; value: string; onChange: (value: string) => void; multiline?: boolean; type?: 'text' | 'number';
  help?: string; error?: string; disabledReason?: string;
}) {
  const note = createMemo(() => p.error || p.disabledReason || p.help);
  return (
    <Field class="text-field" data-ui="text-field" disabled={!!p.disabledReason} invalid={!!p.error}>
      <FieldLabel>{p.label}</FieldLabel>
      <Show
        when={p.multiline}
        fallback={<Input type={p.type ?? 'text'} value={p.value} onValueChange={v => p.onChange(v)} />}
      >
        <Textarea value={p.value} rows={3} onValueChange={v => p.onChange(v)} />
      </Show>
      <Show when={note()} keyed>
        {msg => (
          <Show
            when={p.error}
            keyed
            fallback={<FieldDescription class="muted" render={dp => <span {...dp} />}>{msg}</FieldDescription>}
          >
            {err => <FieldError class="t-danger" role="alert" render={ep => <span {...ep} />}>{err}</FieldError>}
          </Show>
        )}
      </Show>
    </Field>
  );
}

/* ---------- Setting field ---------- */

type SettingProps = {
  label: string; value: string; help: string; changed?: { by: string; at: number }; now: number;
  status: { kind: 'applied'; at: number } | { kind: 'pending' } | { kind: 'apply_error'; error: string };
  onSave?: (v: string) => void;
  /** A write-only value (a key): typed into a password field, never shown; the field starts empty. */
  secret?: boolean;
};
export function SettingField(p: SettingProps) {
  const [edit, setEdit] = createSignal<string | null>(null);
  const id = createMemo(() => `set-${p.label.replace(/\W+/g, '-')}-${p.value.replace(/\W+/g, '')}`);
  const submit = () => {
    const val = edit();
    if (val !== null) p.onSave?.(val);
    setEdit(null);
  };
  return (
    <div class="setting" data-ui="setting-field">
      <label for={id()}>{p.label}</label>
      <div class="val">
        <Show
          when={edit() !== null}
          fallback={
            <button type="button" id={id()} class="value" onClick={() => setEdit(p.value)}>
              {p.value || (p.secret ? 'Paste' : '—')}
            </button>
          }
        >
          <Input
            id={id()}
            value={edit() ?? ''}
            type={(p.secret ? 'password' : 'text') as 'text'}
            autocomplete={p.secret ? 'off' : undefined}
            onValueChange={v => setEdit(v)}
            onKeyDown={e => {
              if (isSubmitEnter(e)) submit();
              if (e.key === 'Escape') setEdit(null);
            }}
          />
          <Button label="Save" kind="primary" onClick={submit} />
          <Button label="Cancel" kind="quiet" onClick={() => setEdit(null)} />
        </Show>
        <StateBadge
          family="setting"
          state={p.status.kind}
          compact
          reason={p.status.kind === 'applied' ? exact(p.status.at) : p.status.kind === 'apply_error' ? p.status.error : undefined}
        />
        <Show when={p.status.kind === 'apply_error'}>
          <Button label="Retry" icon={actions.retry} iconOnly />
        </Show>
        <Show when={p.changed} keyed>
          {c => <span class="muted">{c.by} · <Ago t={c.at} /></span>}
        </Show>
      </div>
      <div class="help">{p.help}</div>
    </div>
  );
}

/* ---------- Composer (controls/composer.md) ---------- */

export type ComposerPersistence = { key: string; read(): string; write(text: string): void; clearIfUnchanged(text: string): void };
/** A file on its way to the message: uploading with progress 0..1, ready, or failed with the reason. */
export type ComposerAttachment = { id: string; name: string; size: number; mediaType: string; state: 'uploading' | 'ready' | 'failed'; progress?: number; error?: string; preview?: string };
/** A fact of the composer's target in its bottom row (agent profile, permissions); a click acts when onClick is given. */
export type ComposerChip = { key?: string; icon?: IconName; harness?: string | null; label: string; tip?: string; onClick?: () => void };
export type ComposerProps = {
  onSend: (text: string) => Promise<void> | void; disabledReason?: string; placeholder?: string; draftPersistence?: ComposerPersistence;
  /** Files from the "+" picker or a paste (images, files, and text longer than longPaste characters as a .txt file). */
  onAttach?: (files: File[]) => void; attachments?: ComposerAttachment[]; onRemoveAttachment?: (id: string) => void;
  chips?: ComposerChip[];
  /** The accessible name of the chips' group (the new page: "Start settings"). */
  chipsLabel?: string;
  /** While the agent runs: the one button of an empty composer is Stop. */
  onStop?: () => void; stopReason?: string;
  longPaste?: number;
};
/** Pasted text longer than this many characters becomes a .txt attachment (controls/composer.md). */
export const LONG_PASTE = 4000;

/** App-owned scoped persistence; a target change resets private input and busy state synchronously. */
export function Composer(props: ComposerProps) {
  return (
    <Show when={props.draftPersistence?.key ?? 'memory'} keyed>
      {_ => (
        <ScopedComposer
          onSend={props.onSend}
          disabledReason={props.disabledReason}
          placeholder={props.placeholder}
          draftPersistence={props.draftPersistence}
          onAttach={props.onAttach}
          attachments={props.attachments}
          onRemoveAttachment={props.onRemoveAttachment}
          chips={props.chips}
          chipsLabel={props.chipsLabel}
          onStop={props.onStop}
          stopReason={props.stopReason}
          longPaste={props.longPaste}
        />
      )}
    </Show>
  );
}

function ScopedComposer(p: ComposerProps) {
  const [text, setText] = createSignal('', { ownedWrite: true });
  const [busy, setBusy] = createSignal(false);
  const [warning, setWarning] = createSignal('', { ownedWrite: true });
  const currentKey = untrack(() => p.draftPersistence?.key);
  let currentText = '';
  let picker: HTMLInputElement | undefined;

  createEffect(() => p.draftPersistence?.key, () => {
    try {
      const value = untrack(() => p.draftPersistence?.read() ?? '');
      currentText = value;
      setText(value);
      setWarning('');
    } catch {
      setWarning('Draft storage unavailable; keep this page open.');
    }
  });

  const keep = (value: string) => {
    currentText = value;
    setText(value);
    try {
      p.draftPersistence?.write(value);
    } catch {
      setWarning('Draft storage unavailable; keep this page open.');
    }
  };

  const attachments = createMemo(() => p.attachments ?? []);
  const files = createMemo(() => attachments().length > 0);
  const failed = createMemo(() => attachments().some(a => a.state === 'failed'));
  const placeholder = () => p.placeholder ?? 'Message';
  const chipsLabel = () => p.chipsLabel ?? 'Agent and permissions';
  const longPaste = () => p.longPaste ?? LONG_PASTE;

  const send = async () => {
    const original = text(), value = original.trim(), adapter = p.draftPersistence, key = adapter?.key;
    if ((!value && !files()) || busy() || p.disabledReason || failed()) return;
    setBusy(true);
    try {
      // The app's onSend waits for uploads still running; the button stays busy until then.
      await p.onSend(value);
      // The captured adapter fences old generations; compare original, untrimmed input.
      adapter?.clearIfUnchanged(original);
      if (currentKey === key && currentText === original) {
        currentText = '';
        setText('');
      }
    } catch {
      /* caller shows send errors; authored text remains */
    } finally {
      if (currentKey === key) setBusy(false);
    }
  };

  const paste = (e: ClipboardEvent) => {
    if (!p.onAttach || !e.clipboardData) return;
    const pasted = [...e.clipboardData.files], t = e.clipboardData.getData('text/plain');
    // A rich copy (spreadsheet cells, a document paragraph) carries its text and a picture of it: the text wins.
    const picture = pasted.length > 0 && pasted.every(f => /^image\//.test(f.type)) && t.trim() !== '';
    if (pasted.length && !picture) { e.preventDefault(); p.onAttach(pasted); return; }
    if (t.length > longPaste()) {
      e.preventDefault();
      p.onAttach([new File([t], `pasted-${attachments().length + 1}.txt`, { type: 'text/plain' })]);
    }
  };

  const stop = createMemo(() => !!p.onStop && !text().trim() && !files() && !busy());

  return (
    <div class={['composer', { off: !!p.disabledReason }]} data-ui="composer">
      <Show when={files()}>
        <div class="composer-files" role="list" aria-label="Attachments">
          <For each={attachments()}>
            {a => (
              <span role="listitem" class={['att', { failed: a.state === 'failed' }]} data-tip={a.error ?? `${a.mediaType} · ${bytes(a.size).text}`}>
                <Show
                  when={a.preview && /^image\//.test(a.mediaType)}
                  fallback={<span class="att-thumb"><Icon name="file-text" /></span>}
                >
                  <img class="att-thumb" src={a.preview} alt="" />
                </Show>
                <span class="att-name">{a.name}</span>
                <span class="att-size">
                  {a.state === 'uploading' ? `${Math.round((a.progress ?? 0) * 100)}%` : a.state === 'failed' ? 'failed' : bytes(a.size).text}
                </span>
                <Show when={p.onRemoveAttachment} keyed>
                  {rem => (
                    <button type="button" class="att-x" aria-label={`Remove ${a.name}`} onClick={() => rem(a.id)}>
                      <Icon name="x" />
                    </button>
                  )}
                </Show>
                <Show when={a.state === 'uploading'}>
                  <span class="att-bar" style={{ width: `${Math.round((a.progress ?? 0) * 100)}%` }} />
                </Show>
              </span>
            )}
          </For>
        </div>
      </Show>
      <Textarea
        value={text()}
        placeholder={placeholder()}
        aria-label={placeholder()}
        rows={2}
        disabled={!!p.disabledReason}
        onValueChange={v => keep(v)}
        onPaste={paste}
        onKeyDown={e => { if (isSubmitEnter(e) && !e.shiftKey) { e.preventDefault(); void send(); } }}
      />
      <div class="composer-row">
        <Show when={p.onAttach}>
          <Button label="Attach files" icon="plus" iconOnly kind="quiet" disabledReason={p.disabledReason} onClick={() => picker?.click()} />
          <input
            ref={el => { picker = el; }}
            type="file"
            multiple
            hidden
            aria-hidden="true"
            tabindex={-1}
            onChange={e => {
              const t = e.currentTarget;
              if (t.files?.length) p.onAttach?.([...t.files]);
              t.value = '';
            }}
          />
        </Show>
        <Show when={(p.chips ?? []).length > 0}>
          <span class="composer-chips" role="group" aria-label={chipsLabel()}>
            <For each={p.chips ?? []}>
              {c => {
                const body = () => (
                  <>
                    {c.harness && c.harness in harnesses ? <HarnessMark h={c.harness as Harness} /> : c.icon ? <Icon name={c.icon} /> : null}
                    <span>{c.label}</span>
                  </>
                );
                return (
                  <Show
                    when={c.onClick}
                    keyed
                    fallback={<span class="composer-chip" data-fact={c.key} data-tip={c.tip}>{body()}</span>}
                  >
                    {click => (
                      <button type="button" class="composer-chip" data-fact={c.key} data-tip={c.tip} disabled={!!p.disabledReason} onClick={click}>
                        {body()}
                      </button>
                    )}
                  </Show>
                );
              }}
            </For>
          </span>
        </Show>
        <span class="grow" />
        <Show when={warning()} keyed>{w => <span role="status" class="muted">{w}</span>}</Show>
        {/* Stop stays in reach while a draft is in the box: a quiet Stop beside Send. */}
        <Show when={!!p.onStop && !stop()}>
          <Button label="Stop" icon="square" iconOnly kind="quiet" disabledReason={p.stopReason} onClick={p.onStop} />
        </Show>
        <Show
          when={stop()}
          fallback={
            <Button
              label="Send"
              icon="arrow-up"
              iconOnly
              kind="primary"
              busy={busy()}
              disabledReason={p.disabledReason ?? (failed() ? 'Remove the failed file' : undefined)}
              onClick={send}
            />
          }
        >
          <Button label="Stop" icon="square" iconOnly kind="primary" disabledReason={p.stopReason} onClick={p.onStop} />
        </Show>
      </div>
    </div>
  );
}

/**
 * Files dropped anywhere over its children (a conversation): a highlight covers the area while files are dragged over
 * it and moves nothing (OR-23); a drop hands the files to onFiles.
 */
export function DropZone(p: { onFiles: (files: File[]) => void; label?: string; disabledReason?: string; children: JSX.Element }) {
  const [over, setOver] = createSignal(false);
  let depth = 0;
  const hasFiles = (e: DragEvent) => !!e.dataTransfer && [...e.dataTransfer.types].includes('Files');
  return (
    <div
      class="drop-zone"
      data-ui="drop-zone"
      onDragEnter={e => { if (!hasFiles(e) || p.disabledReason) return; e.preventDefault(); depth++; setOver(true); }}
      // Disabled (offline), a drop is refused here, never left to the browser, which would open the file in the tab.
      onDragOver={e => { if (!hasFiles(e)) return; e.preventDefault(); e.dataTransfer!.dropEffect = p.disabledReason ? 'none' : 'copy'; }}
      onDragLeave={e => { if (!hasFiles(e)) return; depth = Math.max(0, depth - 1); if (!depth) setOver(false); }}
      onDrop={e => {
        if (!hasFiles(e)) return;
        e.preventDefault();
        if (p.disabledReason) return;
        depth = 0;
        setOver(false);
        const f = [...(e.dataTransfer?.files ?? [])];
        if (f.length) p.onFiles(f);
      }}
    >
      {p.children}
      <Show when={over()}>
        <div class="drop-over" aria-hidden="true"><Icon name="paperclip" /><span>{p.label ?? 'Drop files to attach'}</span></div>
      </Show>
    </div>
  );
}

/* ---------- Code editor: a definition's text, edited in place (controls/code-editor.md) ---------- */

/** A monospace text area for a definition file; problems are listed under it. The caller keeps the text (a draft). */
export function CodeEditor(p: {
  label: string; value: string; onChange: (text: string) => void; problems?: { path: string; message: string }[]; disabledReason?: string;
}) {
  const problems = createMemo(() => p.problems ?? []);
  return (
    <div class="code-editor" data-ui="code-editor">
      <Textarea
        class="mono"
        spellcheck="false"
        aria-label={p.label}
        value={p.value}
        disabled={!!p.disabledReason}
        onValueChange={v => p.onChange(v)}
        aria-invalid={problems().length > 0 ? 'true' : undefined}
      />
      <For each={problems()}>
        {prob => (
          <div class="problem-line t-danger" role="alert">
            <Icon name="circle-alert" />
            <Show when={prob.path} keyed>{path => <span class="mono">{path}</span>}</Show>
            {prob.message}
          </div>
        )}
      </For>
    </div>
  );
}

/** A unified diff, its added and removed lines tinted (controls/code-editor.md). */
export function DiffText(p: { text: string; label: string }) {
  const lines = createMemo(() => p.text.split('\n'));
  return (
    <pre class="diff block" data-ui="diff-text" aria-label={p.label}>
      <Show when={p.text !== ''} fallback={<span class="muted">No differences</span>}>
        <For each={lines()}>
          {l => (
            <span class={l.startsWith('+') && !l.startsWith('+++') ? 'add' : l.startsWith('-') && !l.startsWith('---') ? 'del' : undefined}>
              {l}{'\n'}
            </span>
          )}
        </For>
      </Show>
    </pre>
  );
}

/* ---------- Choice: one or several of a few fixed options (controls/choice.md) ---------- */

export function Choice(p: {
  label: string; options: { id: string; label: string; icon?: IconName }[]; value: string | string[]; onChange?: (id: string) => void;
}) {
  const isOn = (id: string) => Array.isArray(p.value) ? p.value.includes(id) : p.value === id;
  return (
    <span class="seg-group" data-ui="choice" role="group" aria-label={p.label}>
      <For each={p.options}>
        {o => (
          <Toggle
            class={['seg', { on: isOn(o.id) }]}
            pressed={isOn(o.id)}
            onPressedChange={() => p.onChange?.(o.id)}
          >
            <Show when={o.icon} keyed>{ic => <Icon name={ic} />}</Show>
            {o.label}
          </Toggle>
        )}
      </For>
    </span>
  );
}

/* ---------- Start a conversation: project pick, preset pick and start facts (controls/start-picker.md) ---------- */

export type PickProject = { id: string; name: string };
/**
 * One project out of many (OR-9): the first `n` as chips in the caller's order (focused, then recent), "No project" as one
 * more choice, and a search over all of them once there are more. A picked project outside the first `n` stays shown.
 */
export function ProjectPick(p: {
  projects: PickProject[]; value: string | null; onChange: (id: string | null) => void; n?: number; label?: string;
}) {
  const [q, setQ] = createSignal('');
  const n = () => p.n ?? 6;
  const needle = createMemo(() => q().trim().toLowerCase());
  const found = createMemo(() => {
    const t = needle();
    return t ? p.projects.filter(pr => `${pr.name} ${pr.id}`.toLowerCase().includes(t)) : p.projects.slice(0, n());
  });
  const picked = createMemo(() =>
    p.value !== null && !found().some(pr => pr.id === p.value) ? p.projects.find(pr => pr.id === p.value) : undefined,
  );
  const shown = createMemo(() => [...found().slice(0, needle() ? 12 : n()), ...(picked() && !needle() ? [picked()!] : [])]);
  const more = createMemo(() => (needle() ? found().length : p.projects.length) - shown().length);
  const chip = (id: string | null, name: string) => (
    <button type="button" class={['seg', { on: p.value === id }]} aria-pressed={p.value === id ? 'true' : 'false'} onClick={() => p.onChange(id)}>{name}</button>
  );
  return (
    <div class="project-pick" data-ui="project-pick" role="group" aria-label={p.label ?? 'Project'}>
      <For each={shown()}>{pr => chip(pr.id, pr.name)}</For>
      <Show when={!needle()}>{chip(null, 'No project')}</Show>
      <Show when={more() > 0}><span class="muted num" aria-label={`${more()} more projects: search to find them`}>+{more()}</span></Show>
      <Show when={p.projects.length > n()}><SearchBox label="Find project" value={q()} onInput={setQ} /></Show>
    </div>
  );
}

export type PresetOption = {
  id: string; name: string; description: string; icon?: IconName;
  /** What the preset sets, in words (agent, access, branch, starter), shown on the card. */ facts: string[];
  /** Why it cannot start here; the card stays visible and disabled. */ unavailable?: string;
};
/** What a new conversation does (fleet-conversations.md "Start a conversation"): one card per preset, its description and what it sets. */
export function PresetPicker(p: { presets: PresetOption[]; value: string | null; onChange: (id: string) => void; label?: string }) {
  return (
    <div class="preset-pick" data-ui="preset-pick" role="group" aria-label={p.label ?? 'Preset'}>
      <For each={p.presets}>
        {pr => (
          <button
            type="button"
            class={['preset-card', { on: p.value === pr.id }]}
            aria-pressed={p.value === pr.id ? 'true' : 'false'}
            aria-disabled={pr.unavailable ? 'true' : undefined}
            data-tip={pr.unavailable}
            onClick={() => { if (!pr.unavailable) p.onChange(pr.id); }}
          >
            <span class="pc-h"><Show when={pr.icon} keyed>{ic => <Icon name={ic} />}</Show><b>{pr.name}</b></span>
            <span class="pc-d">{pr.description}</span>
            <span class="pc-f">{pr.unavailable ?? pr.facts.join(' · ')}</span>
          </button>
        )}
      </For>
    </div>
  );
}

export type StartFact = { key: string; icon?: IconName; harness?: Harness; text: string; tip: string;
  /** A fact the person can flip here (access, branch): the chip is a button. */ onClick?: () => void };
/** The facts a start will use, as chips under the composer: project, agent, access, branch. */
export function StartFacts(p: { facts: StartFact[]; label?: string }) {
  return (
    <div class="start-facts" data-ui="start-facts" role="group" aria-label={p.label ?? 'Start settings'}>
      <For each={p.facts}>
        {f => {
          const inner = () => (
            <>
              {f.harness ? <HarnessMark h={f.harness} /> : f.icon ? <Icon name={f.icon} /> : null}
              <span>{f.text}</span>
            </>
          );
          return (
            <Show
              when={f.onClick}
              keyed
              fallback={<span class="chip start-fact" data-fact={f.key} data-tip={f.tip}>{inner()}</span>}
            >
              {click => (
                <button type="button" class="chip start-fact" data-fact={f.key} data-tip={f.tip} onClick={click}>
                  {inner()}
                </button>
              )}
            </Show>
          );
        }}
      </For>
    </div>
  );
}

/* ---------- Draft bar: a definition that differs from its saved revision (controls/draft-bar.md) ---------- */

export function DraftBar(p: {
  changes: number; base: string; note: string; actions: { label: string; icon?: IconName; primary?: boolean; onClick?: () => void }[];
}) {
  return (
    <div class="problem draft" data-ui="draft-bar" role="group" aria-label="Draft">
      <Icon name="file-plus-2" />
      <b>Draft</b>
      <span class="facts">{p.changes} {p.changes === 1 ? 'change' : 'changes'} against <span class="mono">{p.base}</span>. {p.note}</span>
      <span class="acts">
        <For each={p.actions}>
          {a => <Button label={a.label} icon={a.icon} kind={a.primary ? 'primary' : 'default'} onClick={a.onClick} />}
        </For>
      </span>
    </div>
  );
}

/** Semantic inline form wrapper for edit/rename sections in views. */
export function InlineForm(p: {
  cls?: string;
  onSubmit: (e: Event) => void;
  onKeyDown?: (e: KeyboardEvent) => void;
  children: JSX.Element;
}) {
  return <form class={p.cls} data-ui="inline-form" onSubmit={e => p.onSubmit(e)} onKeyDown={e => p.onKeyDown?.(e)}>{p.children}</form>;
}
