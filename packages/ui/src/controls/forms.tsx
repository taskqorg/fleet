// Form inputs, settings, composer, drop zone, code editor, draft bar, and conversation start pickers.
import type { ComponentChildren } from 'preact';
import { useEffect, useId, useRef, useState } from 'preact/hooks';
import { type IconName, type Harness, harnesses, actions } from '../registry.ts';
import { bytes, exact } from '../format.ts';
import { isSubmitEnter } from '../keys.ts';
import { Icon, Button, StateBadge, Ago, SearchBox, HarnessMark } from './primitives.tsx';

/** An unsaved form value; it never claims that a setting was applied (controls/text-field.md). */
export function TextField({ label, value, onChange, multiline, type = 'text', help, error, disabledReason }: {
  label: string; value: string; onChange: (value: string) => void; multiline?: boolean; type?: 'text' | 'number';
  help?: string; error?: string; disabledReason?: string;
}) {
  const id = useId(), note = error || disabledReason || help;
  const props = { id, value, disabled: !!disabledReason, 'aria-invalid': error ? true : undefined,
    'aria-describedby': note ? id + '-note' : undefined, onInput: (e: Event) => onChange((e.target as HTMLInputElement).value) };
  return <div class="text-field" data-ui="text-field"><label for={id}>{label}</label>
    {multiline ? <textarea {...props} rows={3} /> : type === 'number' ? <input {...props} type="number" /> : <input {...props} type="text" />}
    {note && <span id={id + '-note'} class={error ? 't-danger' : 'muted'} role={error ? 'alert' : undefined}>{note}</span>}
  </div>;
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
  const [edit, setEdit] = useState<string | null>(null);
  const id = `set-${p.label.replace(/\W+/g, '-')}-${p.value.replace(/\W+/g, '')}`;
  const s = p.status;
  return (
    <div class="setting" data-ui="setting-field">
      <label for={id}>{p.label}</label>
      <div class="val">
        {edit === null
          ? <button type="button" id={id} class="value" onClick={() => setEdit(p.value)}>{p.value || (p.secret ? 'Paste' : '—')}</button>
          : <><input id={id} value={edit} type={(p.secret ? 'password' : 'text') as 'text'} autocomplete={p.secret ? 'off' : undefined} onInput={e => setEdit((e.target as HTMLInputElement).value)}
              onKeyDown={e => { if (isSubmitEnter(e)) { p.onSave?.(edit); setEdit(null); } if (e.key === 'Escape') setEdit(null); }} />
            <Button label="Save" kind="primary" onClick={() => { p.onSave?.(edit); setEdit(null); }} /><Button label="Cancel" kind="quiet" onClick={() => setEdit(null)} /></>}
        <StateBadge family="setting" state={s.kind} compact reason={s.kind === 'applied' ? exact(s.at) : s.kind === 'apply_error' ? s.error : undefined} />
        {s.kind === 'apply_error' && <Button label="Retry" icon={actions.retry} iconOnly />}
        {p.changed && <span class="muted">{p.changed.by} · <Ago t={p.changed.at} /></span>}
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
export function Composer(props: ComposerProps) { return <ScopedComposer key={props.draftPersistence?.key ?? 'memory'} {...props} />; }
function ScopedComposer({ onSend, disabledReason, placeholder = 'Message', draftPersistence, onAttach, attachments = [], onRemoveAttachment, chips = [], chipsLabel = 'Agent and permissions', onStop, stopReason, longPaste = LONG_PASTE }: ComposerProps) {
  const [text, setText] = useState(''), [busy, setBusy] = useState(false), [warning, setWarning] = useState('');
  const current = useRef({ text, key: draftPersistence?.key }); current.current = { text, key: draftPersistence?.key };
  const picker = useRef<HTMLInputElement>(null);
  useEffect(() => { try { const value = draftPersistence?.read() ?? ''; current.current.text = value; setText(value); setWarning(''); } catch { setWarning('Draft storage unavailable; keep this page open.'); } }, [draftPersistence?.key]);
  const keep = (value: string) => { current.current.text = value; setText(value); try { draftPersistence?.write(value); } catch { setWarning('Draft storage unavailable; keep this page open.'); } };
  const files = attachments.length > 0, failed = attachments.some(a => a.state === 'failed');
  const send = async () => {
    const original = text, value = original.trim(), adapter = draftPersistence, key = adapter?.key;
    if ((!value && !files) || busy || disabledReason || failed) return;
    setBusy(true);
    try {
      // The app's onSend waits for uploads still running; the button stays busy until then.
      await onSend(value);
      // The captured adapter fences old generations; compare original, untrimmed input.
      adapter?.clearIfUnchanged(original);
      if (current.current.key === key && current.current.text === original) { current.current.text = ''; setText(''); }
    } catch { /* caller shows send errors; authored text remains */ }
    finally { if (current.current.key === key) setBusy(false); }
  };
  const paste = (e: ClipboardEvent) => {
    if (!onAttach || !e.clipboardData) return;
    const pasted = [...e.clipboardData.files], t = e.clipboardData.getData('text/plain');
    // A rich copy (spreadsheet cells, a document paragraph) carries its text and a picture of it: the text wins.
    const picture = pasted.length > 0 && pasted.every(f => /^image\//.test(f.type)) && t.trim() !== '';
    if (pasted.length && !picture) { e.preventDefault(); onAttach(pasted); return; }
    if (t.length > longPaste) { e.preventDefault(); onAttach([new File([t], `pasted-${attachments.length + 1}.txt`, { type: 'text/plain' })]); }
  };
  const stop = !!onStop && !text.trim() && !files && !busy;
  return <div class={`composer${disabledReason ? ' off' : ''}`} data-ui="composer">
    {files && <div class="composer-files" role="list" aria-label="Attachments">{attachments.map(a =>
      <span key={a.id} role="listitem" class={`att${a.state === 'failed' ? ' failed' : ''}`} data-tip={a.error ?? `${a.mediaType} · ${bytes(a.size).text}`}>
        {a.preview && /^image\//.test(a.mediaType) ? <img class="att-thumb" src={a.preview} alt="" /> : <span class="att-thumb"><Icon name="file-text" /></span>}
        <span class="att-name">{a.name}</span>
        <span class="att-size">{a.state === 'uploading' ? `${Math.round((a.progress ?? 0) * 100)}%` : a.state === 'failed' ? 'failed' : bytes(a.size).text}</span>
        {onRemoveAttachment && <button type="button" class="att-x" aria-label={`Remove ${a.name}`} onClick={() => onRemoveAttachment(a.id)}><Icon name="x" /></button>}
        {a.state === 'uploading' && <span class="att-bar" style={{ width: `${Math.round((a.progress ?? 0) * 100)}%` }} />}
      </span>)}</div>}
    <textarea value={text} placeholder={placeholder} aria-label={placeholder} rows={2} disabled={!!disabledReason}
      onInput={e => keep((e.target as HTMLTextAreaElement).value)} onPaste={paste}
      onKeyDown={e => { if (isSubmitEnter(e) && !e.shiftKey) { e.preventDefault(); void send(); } }} />
    <div class="composer-row">
      {onAttach && <><Button label="Attach files" icon="plus" iconOnly kind="quiet" disabledReason={disabledReason} onClick={() => picker.current?.click()} />
        <input ref={picker} type="file" multiple hidden aria-hidden="true" tabIndex={-1} onChange={e => { const t = e.target as HTMLInputElement; if (t.files?.length) onAttach([...t.files]); t.value = ''; }} /></>}
      {chips.length > 0 && <span class="composer-chips" role="group" aria-label={chipsLabel}>{chips.map(c => {
        const body = <>{c.harness && c.harness in harnesses ? <HarnessMark h={c.harness as Harness} /> : c.icon ? <Icon name={c.icon} /> : null}<span>{c.label}</span></>;
        return c.onClick ? <button key={c.key ?? c.label} type="button" class="composer-chip" data-fact={c.key} data-tip={c.tip} disabled={!!disabledReason} onClick={c.onClick}>{body}</button>
          : <span key={c.key ?? c.label} class="composer-chip" data-fact={c.key} data-tip={c.tip}>{body}</span>;
      })}</span>}
      <span class="grow" />
      {warning && <span role="status" class="muted">{warning}</span>}
      {/* Stop stays in reach while a draft is in the box: a quiet Stop beside Send. */}
      {!!onStop && !stop && <Button label="Stop" icon="square" iconOnly kind="quiet" disabledReason={stopReason} onClick={onStop} />}
      {stop ? <Button label="Stop" icon="square" iconOnly kind="primary" disabledReason={stopReason} onClick={onStop} />
        : <Button label="Send" icon="arrow-up" iconOnly kind="primary" busy={busy} disabledReason={disabledReason ?? (failed ? 'Remove the failed file' : undefined)} onClick={send} />}
    </div>
  </div>;
}

/**
 * Files dropped anywhere over its children (a conversation): a highlight covers the area while files are dragged over
 * it and moves nothing (OR-23); a drop hands the files to onFiles.
 */
export function DropZone({ onFiles, label = 'Drop files to attach', disabledReason, children }: { onFiles: (files: File[]) => void; label?: string; disabledReason?: string; children: ComponentChildren }) {
  const [over, setOver] = useState(false), depth = useRef(0);
  const hasFiles = (e: DragEvent) => !!e.dataTransfer && [...e.dataTransfer.types].includes('Files');
  return <div class="drop-zone" data-ui="drop-zone"
    onDragEnter={e => { if (!hasFiles(e) || disabledReason) return; e.preventDefault(); depth.current++; setOver(true); }}
    // Disabled (offline), a drop is refused here, never left to the browser, which would open the file in the tab.
    onDragOver={e => { if (!hasFiles(e)) return; e.preventDefault(); e.dataTransfer!.dropEffect = disabledReason ? 'none' : 'copy'; }}
    onDragLeave={e => { if (!hasFiles(e)) return; depth.current = Math.max(0, depth.current - 1); if (!depth.current) setOver(false); }}
    onDrop={e => { if (!hasFiles(e)) return; e.preventDefault(); if (disabledReason) return; depth.current = 0; setOver(false); const f = [...(e.dataTransfer?.files ?? [])]; if (f.length) onFiles(f); }}>
    {children}
    {over && <div class="drop-over" aria-hidden="true"><Icon name="paperclip" /><span>{label}</span></div>}
  </div>;
}

/* ---------- Code editor: a definition's text, edited in place (controls/code-editor.md) ---------- */

/** A monospace text area for a definition file; problems are listed under it. The caller keeps the text (a draft). */
export function CodeEditor({ label, value, onChange, problems = [], disabledReason }: {
  label: string; value: string; onChange: (text: string) => void; problems?: { path: string; message: string }[]; disabledReason?: string;
}) {
  return <div class="code-editor" data-ui="code-editor">
    <textarea class="mono" spellcheck={false} aria-label={label} value={value} disabled={!!disabledReason}
      onInput={e => onChange((e.target as HTMLTextAreaElement).value)} aria-invalid={problems.length > 0 || undefined} />
    {problems.map(p => <div key={p.path + p.message} class="problem-line t-danger" role="alert"><Icon name="circle-alert" />{p.path && <span class="mono">{p.path}</span>}{p.message}</div>)}
  </div>;
}

/** A unified diff, its added and removed lines tinted (controls/code-editor.md). */
export const DiffText = ({ text, label }: { text: string; label: string }) =>
  <pre class="diff block" data-ui="diff-text" aria-label={label}>{text === '' ? <span class="muted">No differences</span> : text.split('\n').map((l, i) =>
    <span key={i} class={l.startsWith('+') && !l.startsWith('+++') ? 'add' : l.startsWith('-') && !l.startsWith('---') ? 'del' : undefined}>{l}{'\n'}</span>)}</pre>;

/* ---------- Choice: one or several of a few fixed options (controls/choice.md) ---------- */

export function Choice({ label, options, value, onChange }: {
  label: string; options: { id: string; label: string; icon?: IconName }[]; value: string | string[]; onChange?: (id: string) => void;
}) {
  const on = (id: string) => Array.isArray(value) ? value.includes(id) : value === id;
  return <span class="seg-group" data-ui="choice" role="group" aria-label={label}>{options.map(o =>
    <button type="button" key={o.id} class={`seg${on(o.id) ? ' on' : ''}`} aria-pressed={on(o.id)} onClick={() => onChange?.(o.id)}>{o.icon && <Icon name={o.icon} />}{o.label}</button>)}</span>;
}

/* ---------- Start a conversation: project pick, preset pick and start facts (controls/start-picker.md) ---------- */

export type PickProject = { id: string; name: string };
/**
 * One project out of many (OR-9): the first `n` as chips in the caller's order (focused, then recent), "No project" as one
 * more choice, and a search over all of them once there are more. A picked project outside the first `n` stays shown.
 */
export function ProjectPick({ projects, value, onChange, n = 6, label = 'Project' }: {
  projects: PickProject[]; value: string | null; onChange: (id: string | null) => void; n?: number; label?: string;
}) {
  const [q, setQ] = useState(''), t = q.trim().toLowerCase();
  const found = t ? projects.filter(p => `${p.name} ${p.id}`.toLowerCase().includes(t)) : projects.slice(0, n);
  const picked = value !== null && !found.some(p => p.id === value) ? projects.find(p => p.id === value) : undefined;
  const shown = [...found.slice(0, t ? 12 : n), ...(picked && !t ? [picked] : [])];
  const more = (t ? found.length : projects.length) - shown.length;
  const chip = (id: string | null, name: string) => <button type="button" key={id ?? '-'} class={`seg${value === id ? ' on' : ''}`} aria-pressed={value === id} onClick={() => onChange(id)}>{name}</button>;
  return <div class="project-pick" data-ui="project-pick" role="group" aria-label={label}>
    {shown.map(p => chip(p.id, p.name))}
    {!t && chip(null, 'No project')}
    {more > 0 && <span class="muted num" aria-label={`${more} more projects: search to find them`}>+{more}</span>}
    {projects.length > n && <SearchBox label="Find project" value={q} onInput={setQ} />}
  </div>;
}

export type PresetOption = { id: string; name: string; description: string; icon?: IconName;
  /** What the preset sets, in words (agent, access, branch, starter), shown on the card. */ facts: string[];
  /** Why it cannot start here; the card stays visible and disabled. */ unavailable?: string };
/** What a new conversation does (fleet-conversations.md "Start a conversation"): one card per preset, its description and what it sets. */
export function PresetPicker({ presets, value, onChange, label = 'Preset' }: { presets: PresetOption[]; value: string | null; onChange: (id: string) => void; label?: string }) {
  return <div class="preset-pick" data-ui="preset-pick" role="group" aria-label={label}>{presets.map(p =>
    <button type="button" key={p.id} class={`preset-card${value === p.id ? ' on' : ''}`} aria-pressed={value === p.id} aria-disabled={!!p.unavailable || undefined}
      data-tip={p.unavailable} onClick={() => { if (!p.unavailable) onChange(p.id); }}>
      <span class="pc-h">{p.icon && <Icon name={p.icon} />}<b>{p.name}</b></span>
      <span class="pc-d">{p.description}</span>
      <span class="pc-f">{p.unavailable ?? p.facts.join(' · ')}</span>
    </button>)}</div>;
}

export type StartFact = { key: string; icon?: IconName; harness?: Harness; text: string; tip: string;
  /** A fact the person can flip here (access, branch): the chip is a button. */ onClick?: () => void };
/** The facts a start will use, as chips under the composer: project, agent, access, branch. */
export function StartFacts({ facts, label = 'Start settings' }: { facts: StartFact[]; label?: string }) {
  return <div class="start-facts" data-ui="start-facts" role="group" aria-label={label}>{facts.map(f => {
    const inner = <>{f.harness ? <HarnessMark h={f.harness} /> : f.icon && <Icon name={f.icon} />}<span>{f.text}</span></>;
    return f.onClick ? <button type="button" key={f.key} class="chip start-fact" data-fact={f.key} data-tip={f.tip} onClick={f.onClick}>{inner}</button>
      : <span key={f.key} class="chip start-fact" data-fact={f.key} data-tip={f.tip}>{inner}</span>;
  })}</div>;
}

/* ---------- Draft bar: a definition that differs from its saved revision (controls/draft-bar.md) ---------- */

export function DraftBar({ changes, base, note, actions: acts }: {
  changes: number; base: string; note: string; actions: { label: string; icon?: IconName; primary?: boolean; onClick?: () => void }[];
}) {
  return <div class="problem draft" data-ui="draft-bar" role="group" aria-label="Draft">
    <Icon name="file-plus-2" /><b>Draft</b><span class="facts">{changes} {changes === 1 ? 'change' : 'changes'} against <span class="mono">{base}</span>. {note}</span>
    <span class="acts">{acts.map(a => <Button key={a.label} label={a.label} icon={a.icon} kind={a.primary ? 'primary' : 'default'} onClick={a.onClick} />)}</span>
  </div>;
}
