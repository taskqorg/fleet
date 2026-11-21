// Thread transcript viewer, message/tool/question/result/boundary rows, decision block, markdown, and artifact viewer.
import type { ComponentChildren } from 'preact';
import { useEffect, useRef, useState } from 'preact/hooks';
import { type IconName, type StateOf, type Harness, actions, harnesses } from '../registry.ts';
import { bytes, exact, duration, agentLabel, actor, fileLabel } from '../format.ts';
import type { Block as ThreadBlock, ResourceSummary } from '../../../../contracts/generated/ops.ts';
import { VirtualList, type VirtualListHandle } from '../virtual-list.tsx';
import { parse, renderBlocks } from '../markdown.tsx';
import { author, clipMessage, writeClip } from '../clip.ts';
import { isSubmitEnter } from '../keys.ts';
import tokens from '../../tokens.json';
import { Icon, Button, StateBadge, Num, CostFigures, Ago, AgeChip, HarnessMark, Empty, ErrorBanner, copyText, SearchBox } from './primitives.tsx';
import { EntityCode, linkCodes } from './overlays.tsx';
import { TabBar } from './tables.tsx';

const ROW = parseInt(tokens.size.row);

/* ---------- Decision block (§6) ---------- */

type DecisionProps = {
  question: string; askedBy: string; addressedTo: string; since: number; now: number; waits?: ComponentChildren;
  context?: ComponentChildren; timeInRow?: boolean; technicalAnswer?: boolean; options: { label: string; kbd?: string; primary?: boolean }[];
  answered?: { by: string; answer: string; at: number; delivered: boolean };
  onAnswer?: (answer: string) => void;
  /** A key request: a write-only password field replaces the typed answer; the value goes to onSecret only, never to onAnswer. */
  onSecret?: (value: string) => Promise<unknown>;
};
export function DecisionBlock(p: DecisionProps) {
  const [text, setText] = useState('');
  const [failed, setFailed] = useState<string | null>(null);
  if (p.answered) {
    const a = p.answered;
    if (p.timeInRow) return <div class="decision answered thread-answer" data-ui="decision-block" role="status">
      <div class="dq"><Icon name="message-circle-question" label="Question" /><span>{p.question}</span></div>
      <div class="decision-reply"><span class="muted">Answer</span> {p.technicalAnswer ? <CodeBlock text={a.answer} /> : <b>{a.answer}</b>}{!a.delivered && ' · delivery pending'}</div>
    </div>;
    return <div class="decision answered" data-ui="decision-block" role="status"><Icon name={a.delivered ? 'check-check' : 'history'} />
      <span>{p.question} <b>{a.answer}</b> · {a.by} · <Ago t={a.at} />{a.delivered ? '' : ' · delivery pending'}</span></div>;
  }
  const send = (s: string) => s.trim() && p.onAnswer?.(s.trim());
  // The field clears once the key is stored; a refusal shows its reason, never the value.
  const secret = () => { const v = text.trim(); if (!v || !p.onSecret) return; setFailed(null); p.onSecret(v).then(() => setText(''), e => setFailed(String(e?.message ?? e))); };
  return (
    <div class="decision" data-ui="decision-block" role="group" aria-label={p.question}
      onKeyDown={e => { if ((e.target as HTMLElement).tagName === 'INPUT') return; const o = p.options.find(o => o.kbd === e.key); if (o) send(o.label); }}>
      <div class="dq"><Icon name="bell" /><b>{p.question}</b></div>
      <div class="meta">{p.askedBy}{p.addressedTo === 'you' ? '' : ` · for ${p.addressedTo}`}{!p.timeInRow && <> · <AgeChip t={p.since} /></>}{p.waits && <> · {p.waits}</>}</div>
      {p.context && <div class="ctx">{p.context}</div>}
      <div class="answers">
        {p.options.map(o => <Button key={o.label} label={o.label} kbd={o.kbd} kind={o.primary ? 'primary' : 'default'} onClick={() => send(o.label)} />)}
        {p.onSecret ? <>
          <input type="password" autocomplete="off" spellcheck={false} value={text} placeholder="Paste key" aria-label="Key value"
            onInput={e => setText((e.target as HTMLInputElement).value)} onKeyDown={e => { if (isSubmitEnter(e)) { e.preventDefault(); secret(); } }} />
          <Button label="Add key" icon="shield" kind="primary" disabledReason={text.trim() ? undefined : 'paste the key first'} onClick={secret} />
        </> : <input value={text} placeholder="Answer" aria-label="Typed answer" onInput={e => setText((e.target as HTMLInputElement).value)}
          onKeyDown={e => { if (isSubmitEnter(e)) send(text); }} />}
      </div>
      {failed && <div class="error" role="alert">{failed}</div>}
    </div>
  );
}

/* ---------- Result viewer shell ---------- */

type ResultProps = {
  name: string; type: string; size: number; source: string; href?: string;
  copies: { where: string; state: StateOf<'copy'> }[];
  tabs: string[]; tab: string; onTab: (t: string) => void; children: ComponentChildren;
};
export function ResultViewer(p: ResultProps) {
  return (
    <section class="rv" data-ui="result-viewer" aria-label={p.name}>
      <header class="rv-h">
        <Icon name="file-text" /><b class="nm">{p.name}</b>
        <span class="muted">{p.type} · {bytes(p.size).text} · {p.source}</span>
        {p.copies.map(c => <StateBadge key={c.where} family="copy" state={c.state} reason={c.where} compact />)}
        <span class="grow" />
        <TabBar label={`${p.name} views`} tabs={p.tabs.map(t => ({ id: t, label: t }))} value={p.tab} onChange={p.onTab} />
        <Button label="Compare" icon="list" iconOnly kbd="c" />
        {p.href ? <a class="btn default icon-only" data-ui="button" href={p.href} download={p.name} aria-label="Download" data-tip="Download"><Icon name={actions.download} /></a> : <Button label="Download" icon={actions.download} iconOnly />}
        <Button label="Share" icon={actions.share} iconOnly />
      </header>
      <div class="rv-b" role="tabpanel">{p.children}</div>
    </section>
  );
}

/* ---------- Artifact view: the body of the result viewer (result-viewer.md) ---------- */

export type ArtifactMeta = { id: string; name: string; mediaType: string; size: number };
const TEXTUAL = /^(text\/|application\/(json|xml|x-ndjson|javascript|x-yaml|yaml|toml|sql|x-sh))/;
/** Which view a stored media type gets: image, video, text, or download only. HTML and SVG are text with a rendered preview. */
export const viewOf = (mediaType: string): 'image' | 'video' | 'text' | 'download' =>
  /^image\/(png|jpeg|gif|webp|avif|bmp)$/.test(mediaType) ? 'image' : /^video\//.test(mediaType) ? 'video' : TEXTUAL.test(mediaType) || mediaType === 'image/svg+xml' ? 'text' : 'download';
/** The rendered view a text format has, if any. YAML has none: its source is already its readable form. */
export const renderOf = (mediaType: string, name: string): 'html' | 'svg' | 'markdown' | 'json' | 'csv' | null =>
  /^text\/html/.test(mediaType) ? 'html' : mediaType === 'image/svg+xml' ? 'svg' : /^text\/markdown/.test(mediaType) || /\.md$/i.test(name) ? 'markdown'
    : /^application\/json/.test(mediaType) ? 'json' : /^text\/csv/.test(mediaType) ? 'csv' : null;
/** CSV rows (RFC 4180: quoted fields, doubled quotes, line breaks inside quotes). */
export function parseCsv(t: string): string[][] {
  const rows: string[][] = []; let row: string[] = [], f = '', q = false;
  for (let i = 0; i < t.length; i++) {
    const c = t[i];
    if (q) { if (c === '"') { if (t[i + 1] === '"') { f += '"'; i++; } else q = false; } else f += c; }
    else if (c === '"') q = true; else if (c === ',') { row.push(f); f = ''; }
    else if (c === '\n' || c === '\r') { if (c === '\r' && t[i + 1] === '\n') i++; row.push(f); rows.push(row); row = []; f = ''; }
    else f += c;
  }
  if (f || row.length) { row.push(f); rows.push(row); }
  return rows;
}
const pretty = (t: string) => { try { return JSON.stringify(JSON.parse(t), null, 2); } catch { return t; /* not complete yet, or not JSON: shown as is */ } };
// The chosen view per format lasts for the session (sessionStorage; a blocked store just falls back to the preview).
const modeOf = (f: string) => { try { return sessionStorage.getItem('fleet.av.' + f) === 'source' ? 'source' : 'preview'; } catch { return 'preview'; } };
const PAGE_BYTES = 65536;
/**
 * One stored artifact by media type: image and video in a fixed-ratio frame (no jump when they load, OR-23), text read in
 * Range pages as the reader scrolls (no cap). A format with a renderer opens rendered, with a Source/Preview switch:
 * HTML runs in a sandboxed frame without same-origin access (scripts on an opaque origin, no network), SVG as an image
 * (no scripts), Markdown as nodes, JSON indented, CSV as a table. Copy and download always take the source. Bytes come from `src`.
 */
export function ArtifactView({ a, src }: { a: ArtifactMeta; src: string }) {
  const kind = viewOf(a.mediaType), [text, setText] = useState(''), [next, setNext] = useState(0), [busy, setBusy] = useState(false), [err, setErr] = useState<string | null>(null);
  const fmt = kind === 'text' ? renderOf(a.mediaType, a.name) : null, [mode, setMode] = useState(() => fmt ? modeOf(fmt) : 'source'), decoder = useRef(new TextDecoder());
  const more = async () => {
    if (busy || next >= a.size) return; setBusy(true);
    try { const r = await fetch(src, { headers: { range: `bytes=${next}-${Math.min(a.size, next + PAGE_BYTES) - 1}` }, credentials: 'same-origin' });
      if (!r.ok) throw new Error(`${r.status}`); const b = new Uint8Array(await r.arrayBuffer());
      setText(t => t + decoder.current.decode(b, { stream: next + b.length < a.size })); /* one decoder per artifact: a character split across pages stays whole */ setNext(n => n + b.length); setErr(null); }
    catch (e) { setErr(e instanceof Error ? e.message : String(e)); } finally { setBusy(false); }
  };
  useEffect(() => { setText(''); setNext(0); setMode(fmt ? modeOf(fmt) : 'source'); decoder.current = new TextDecoder(); }, [a.id]);
  useEffect(() => { if (kind === 'text' && next === 0 && a.size > 0) more(); }, [a.id, next]);
  const flip = () => { const m = mode === 'preview' ? 'source' : 'preview'; setMode(m); try { sessionStorage.setItem('fleet.av.' + fmt, m); } catch { /* session-only memory is optional */ } };
  const scroller = (body: ComponentChildren) => <div class="av-text" onScroll={e => { const el = e.currentTarget; if (el.scrollHeight - el.scrollTop - el.clientHeight < 400) more(); }}>{body}
    {next < a.size && <div class="muted type-sm">{bytes(next).text} of {bytes(a.size).text}{busy ? ' · loading' : ''}</div>}</div>;
  const rendered = () => {
    if (fmt === 'html') return <div class="av-frame page" data-tip="Agent output running in a sandbox: not part of Fleet"><iframe src={`${src}/preview`} sandbox="allow-scripts allow-forms allow-modals" title={a.name} referrerpolicy="no-referrer" /></div>;
    if (fmt === 'svg') return <div class="av-frame"><img src={src} alt={a.name} /></div>;
    if (fmt === 'markdown') return scroller(<Markdown text={text} />);
    if (fmt === 'json') return scroller(<pre><code>{next < a.size ? text : pretty(text)}</code></pre>);
    const all = parseCsv(text), [head, ...rows] = next < a.size ? all.slice(0, -1) : all; // the last row of a partial page may be cut
    return scroller(<table class="av-table"><thead><tr>{(head ?? []).map((h, i) => <th key={i}>{h}</th>)}</tr></thead><tbody>{rows.map((r, i) => <tr key={i}>{r.map((c, j) => <td key={j}>{c}</td>)}</tr>)}</tbody></table>);
  };
  return <div class="av" data-ui="artifact-view">
    {kind === 'image' && <div class="av-frame"><img src={src} alt={a.name} loading="lazy" /></div>}
    {kind === 'video' && <div class="av-frame video"><video src={src} controls preload="metadata" /></div>}
    {kind === 'text' && <>
      <div class="bar">
        {fmt && <Button label={mode === 'preview' ? 'Source' : 'Preview'} icon={mode === 'preview' ? 'type' : 'panels-top-left'} onClick={flip} />}
        <Button label="Copy source" icon={actions.copy} iconOnly kind="quiet" onClick={() => (next < a.size ? fetch(src, { credentials: 'same-origin' }).then(r => r.text()) : Promise.resolve(text)).then(copyText).catch(e => setErr(e.message))} />
      </div>
      {fmt && mode === 'preview' ? rendered() : scroller(<pre><code>{text}</code></pre>)}
    </>}
    {kind === 'download' && <Empty text={`${a.mediaType} has no view here.`} />}
    {err && <ErrorBanner text={`Did not load: ${err}`} onRetry={more} />}
  </div>;
}

/* ---------- Thread rows (transcript-viewer.md; docs/designs/fleet-conversations.md) ---------- */

/** Markdown (markdown.tsx): built as nodes, never as HTML; each fenced code block has its own copy button; D17 codes in
 *  its text are entity references (linkCodes). */
export function Markdown({ text, onCopyFail }: { text: string; onCopyFail?: (e: unknown) => void }) {
  return <div class="md">{renderBlocks(parse(text), (code, _lang, k) => <div class="code" key={k}><pre><code>{code}</code></pre><span class="code-copy"><Button label="Copy code" icon={actions.copy} iconOnly kind="quiet" onClick={() => copyText(code).catch(e => onCopyFail?.(e))} /></span></div>, linkCodes)}</div>;
}

type Mark = { harness?: string | null; model?: string | null; effort?: string | null };
/** Agent mark (D4): harness icon and the short label; the model id on hover. */
export function AgentMark({ harness, model, effort }: Mark) {
  return <span class="agent-mark">{harness && harness in harnesses && <HarnessMark h={harness as Harness} />}<span class="lbl" data-tip={model ?? undefined}>{agentLabel(model, effort) || harness /* no model recorded: the harness still names the author */}</span></span>;
}

function ThreadService({ at, timing, action, meta, reveal }: {
  at?: number | null; timing?: ComponentChildren; action?: ComponentChildren; meta?: ComponentChildren; reveal?: boolean;
}) {
  return <div class="th-service">
    <span class={reveal ? 'th-action th-copy' : 'th-action'}>{action}</span>
    <span class="th-time">{at != null && <Ago t={at} />}{timing}</span>
    {meta && <span class="th-meta">{meta}</span>}
  </div>;
}

/** An operator or agent message: a head line with who wrote it and when (OR-2), with a left-aligned metadata row below the content (OR-21).
 *  Copy writes the head line and the body as plain markdown and as chat-safe HTML (clip.ts, formats.md "Copy"). */
export function TextRow({ who, markdown, mark, by, at, now, onCopyFail, attachments, onOpen, src }: { who: 'operator' | 'agent'; markdown: string; mark?: Mark; by?: string | null; at?: number | null; now?: number; onCopyFail?: (e: unknown) => void;
  /** The files the message carried; images show a thumbnail from src, every one opens in the result viewer. */
  attachments?: ArtifactMeta[]; onOpen?: (id: string) => void; src?: (id: string) => string }) {
  const t = now ?? Date.now(), name = author({ role: who, by, ...mark });
  return <div class={`th-row th-text ${who}`} data-ui="thread-row">
    <div class="th-main">
      {markdown && <Markdown text={markdown} onCopyFail={onCopyFail} />}
      {attachments?.length ? <MessageFiles files={attachments} onOpen={onOpen} src={src} /> : null}
    </div>
    <ThreadService at={at} reveal
      meta={who === 'operator' ? <span class="who" data-tip={by ?? undefined}>{name}</span> : <AgentMark {...(mark ?? {})} />}
      action={<Button label="Copy message" icon={actions.copy} iconOnly kind="quiet" onClick={() => writeClip(clipMessage(name, at, t, [markdown, ...(attachments ?? []).map(a => `Attachment: ${a.name}`)].filter(Boolean).join('\n\n'))).catch(e => onCopyFail?.(e))} />} />
  </div>;
}

function namedFiles(files: ArtifactMeta[], fallback: string) {
  const names = files.map(file => fileLabel(file.name) || fallback);
  const totals = new Map<string, number>();
  const positions = new Map<string, number>();
  for (const name of names) totals.set(name, (totals.get(name) ?? 0) + 1);
  return files.map((file, index) => {
    const name = names[index];
    const position = (positions.get(name) ?? 0) + 1;
    positions.set(name, position);
    return { file, name, position, total: totals.get(name)! };
  });
}

/** A message's files: an image as a fixed-size thumbnail, any other file as a chip with its name and size (controls/composer.md). */
export function MessageFiles({ files, onOpen, src }: { files: ArtifactMeta[]; onOpen?: (id: string) => void; src?: (id: string) => string }) {
  return <div class="msg-files" aria-label="Attachments">{namedFiles(files, 'Attachment').map(({file: a, name, position, total}) => {
    const image = viewOf(a.mediaType) === 'image' && src;
    return <a key={a.id} class={`att${image ? ' img' : ''}`} href={`/results/${encodeURIComponent(a.id)}`} data-tip={`${a.name} · ${bytes(a.size).text}`}
      onClick={e => { if (onOpen && !e.metaKey && !e.ctrlKey) { e.preventDefault(); onOpen(a.id); } }}>
      {image ? <img class="att-thumb" src={src!(a.id)} alt={a.name} loading="lazy" /> : <><span class="att-thumb"><Icon name="file-text" /></span><span class="att-name">{name}</span><span class="att-size">{bytes(a.size).text}</span></>}
      {total > 1 && <span class="th-file-index muted" aria-label={`File ${position} of ${total}`}>{position}</span>}
    </a>;
  })}</div>;
}

const raw = (v: unknown) => typeof v === 'string' ? v : JSON.stringify(v, null, 1) ?? '';
const fenced = (v: unknown) => { const t = raw(v), ticks = '`'.repeat(Math.max(3, ...[...t.matchAll(/`+/g)].map(m => m[0].length + 1))); return ticks + '\n' + t + '\n' + ticks; };
/** Raw tool input or output: preformatted as is (never parsed as markdown), with its own copy. */
function CodeBlock({ text, onCopyFail }: { text: string; onCopyFail?: (e: unknown) => void }) {
  return <div class="md"><div class="code"><pre><code>{text}</code></pre><span class="code-copy"><Button label="Copy code" icon={actions.copy} iconOnly kind="quiet" onClick={() => copyText(text).catch(e => onCopyFail?.(e))} /></span></div></div>;
}
/** A collapsed one-line row that opens downward on click; the clicked header stays where it is (OR-23). */
function Fold({ icon, head, body, at, timing, open, onToggle, copy, onCopyFail }: { at?: number | null; timing?: ComponentChildren; icon: IconName; head: ComponentChildren; body: ComponentChildren; open: boolean; onToggle: () => void; copy: string; onCopyFail?: (e: unknown) => void }) {
  return <div class={`th-row th-fold${open ? ' open' : ''}`} data-ui="thread-row">
    <ThreadService at={timing ? undefined : at} timing={timing} reveal
      meta={<button type="button" class="th-head" aria-expanded={open} onClick={onToggle}><span class={`chev${open ? ' open' : ''}`}><Icon name="chevron-down" /></span><Icon name={icon} />{head}</button>}
      action={<Button label="Copy" icon={actions.copy} iconOnly kind="quiet" onClick={() => copyText(copy).catch(e => onCopyFail?.(e))} />} />
    {open && <div class="th-body">{body}</div>}
  </div>;
}
export function ToolRow({ name, input, output, state, startedAt, endedAt, open, onToggle, onCopyFail }: { name: string; input: unknown; output: unknown; state: string; startedAt?: number | null; endedAt?: number | null; open: boolean; onToggle: () => void; onCopyFail?: (e: unknown) => void }) {
  const secs = startedAt != null && endedAt != null ? duration((endedAt - startedAt) / 1000) : null;
  const copy = [`Tool ${name} (${state})`, input != null ? fenced(input) : '', output != null ? fenced(output) : ''].filter(Boolean).join('\n\n');
  return <Fold icon="terminal" at={startedAt} timing={secs && <span data-tip={`Duration: ${secs.long} · Started ${exact(startedAt!)}`}>Ran {secs.text}</span>} open={open} onToggle={onToggle} copy={copy} onCopyFail={onCopyFail}
    head={<span class="th-line"><span class="mono ell" data-tip={name}>{name}</span><span class={`svc${['failed', 'unfinished'].includes(state) ? ' t-danger' : ''}`}>{state}</span></span>}
    body={<>{input != null && <CodeBlock text={raw(input)} onCopyFail={onCopyFail} />}{output != null && <CodeBlock text={raw(output)} onCopyFail={onCopyFail} />}</>} />;
}
export function ReasoningRow({ markdown, at, open, onToggle, onCopyFail }: { markdown: string; at?: number | null; open: boolean; onToggle: () => void; onCopyFail?: (e: unknown) => void }) {
  return <Fold icon="bot" at={at} open={open} onToggle={onToggle} copy={markdown} onCopyFail={onCopyFail}
    head={<span class="th-line"><span>Reasoning</span><span class="svc ell">{markdown.split('\n')[0]}</span></span>} body={<Markdown text={markdown} onCopyFail={onCopyFail} />} />;
}
/** A question in the thread (DecisionBlock); answered, it collapses to one line with the answer and who gave it. */
export function QuestionRow({ text, options, state, answer, answeredBy, askedAt, answeredAt, now, onAnswer, onSecret, technicalAnswer }: { text: string; options: unknown[]; state: string; answer?: string; answeredBy?: string; askedAt?: number | null; answeredAt?: number | null; now: number; onAnswer?: (a: string) => void; onSecret?: (value: string) => Promise<unknown>; technicalAnswer?: boolean }) {
  const answered = state === 'answered' && answer !== undefined;
  return <div class="th-row th-q" data-ui="thread-row">
    <div class="th-question"><DecisionBlock question={text} askedBy="agent" addressedTo="you" since={askedAt ?? now} now={now} timeInRow technicalAnswer={technicalAnswer}
      options={options.map((o, i) => ({ label: String(o), kbd: String(i + 1), primary: i === 0 }))}
      answered={answered ? { by: actor(answeredBy) || 'operator', answer, at: answeredAt ?? now, delivered: true } : undefined} onAnswer={onAnswer} onSecret={onSecret} /></div>
    <ThreadService at={answered ? answeredAt : askedAt} meta={answered && (actor(answeredBy) || "Operator")} />
  </div>;
}
/** The connection a key request (a secret question) asks a key for: its options are {connection}. */
export const keyConnection = (options: unknown): string | undefined => {
  const c = options && !Array.isArray(options) && typeof options === 'object' ? (options as { connection?: unknown }).connection : undefined;
  return typeof c === 'string' ? c : undefined;
};
/** Results are listed one per line. Disambiguate matching short names without exposing full paths. */
export function ResultRow({ artifacts, onOpen }: { artifacts: ArtifactMeta[]; onOpen?: (id: string) => void }) {
  return <div class="th-row th-result" data-ui="thread-row"><ul class="th-links" aria-label="Results">{namedFiles(artifacts, 'Result').map(({file: artifact, name, position, total}) => {
    return <li class="th-file" key={artifact.id}>
      <EntityCode id={artifact.id} kind="result" />
      <a href={`/results/${encodeURIComponent(artifact.id)}`} data-tip={`${artifact.name} · ${artifact.mediaType} · ${bytes(artifact.size).text}`}
        onClick={e => { if (onOpen && !e.metaKey && !e.ctrlKey) { e.preventDefault(); onOpen(artifact.id); } }}>{name}</a>
      {total > 1 && <span class="th-file-index muted" aria-label={`File ${position} of ${total}`}>{position}</span>}
    </li>;
  })}</ul></div>;
}
/** An execution start: state, agent mark, duration chip, resource stats, and Stop while it runs (CUJ 5). */
export function BoundaryRow({ session, executionId, state, mark, startedAt, endedAt, resources, onStop, stopReason }: { session?: string; executionId: string; state: StateOf<'execution'>; mark: Mark; startedAt?: number | null; endedAt?: number | null; now: number; resources?: ResourceSummary; onStop?: () => void; stopReason?: string }) {
  const active = ['preparing', 'running', 'waiting'].includes(state);
  const secs = active ? null : resources?.durations?.executionWallMs != null ? resources.durations.executionWallMs / 1000 : startedAt != null && endedAt != null ? (endedAt - startedAt) / 1000 : null;
  const elapsedSecs = resources?.durations?.elapsedMs != null ? resources.durations.elapsedMs / 1000 : null;
  const durTip = secs === null ? undefined : [
    `Duration: ${duration(secs).long}`,
    elapsedSecs != null && Math.abs(elapsedSecs - secs) >= 1 ? `Elapsed: ${duration(elapsedSecs).long}` : '',
    startedAt != null ? `Started: ${exact(startedAt)}` : '',
  ].filter(Boolean).join(' · ');
  return <div class="th-row th-boundary" data-ui="thread-row">
    <ThreadService at={active ? startedAt : undefined}
      timing={secs !== null && <span data-tip={durTip}>Ran {duration(secs).text}</span>}
      meta={<span class="th-boundary-info">
        <StateBadge family="execution" state={state} compact />
        <EntityCode id={executionId} kind="execution" label />{session && <EntityCode id={session} kind="conversation" />}
        <AgentMark {...mark} />
        <span class="th-svc">
          {resources?.tokens?.total != null && (resources.tokens.basis !== 'unavailable' || resources.tokens.total > 0) && <span class="hc" data-tip="Tokens"><Num m={{ value: resources.tokens.total, basis: resources.tokens.basis === 'unavailable' ? 'measured' : resources.tokens.basis, partial: resources.tokens.basis === 'unavailable' ? 'incomplete telemetry' : undefined }} kind="count" /></span>}
          {resources && resources.tools.totalCalls > 0 && <span class="hc" data-tip={`${resources.tools.totalCalls} tool invocations${resources.tools.failedCalls ? `, ${resources.tools.failedCalls} failed` : ''}`}><Icon name="terminal" label="Tools" /><Num m={{ value: resources.tools.totalCalls, basis: resources.tools.basis === 'unavailable' ? 'measured' : resources.tools.basis, partial: resources.tools.basis === 'unavailable' ? 'incomplete telemetry' : undefined }} kind="count" /></span>}
          {resources && resources.tools.failedCalls > 0 && <span class="chip t-danger" data-tip={`${resources.tools.failedCalls} failed tool calls`}><Icon name="octagon-x" label="Failed tools" />{resources.tools.failedCalls}</span>}
          {resources?.cost?.length ? <CostFigures cost={resources.cost} /> : null}
        </span>
      </span>}
      action={onStop && <Button label="Stop" icon="pause" iconOnly disabledReason={stopReason} onClick={onStop} />}
    />
  </div>;
}

/** A search field for one scope (a thread): Enter searches; an icon until used (OR-31). */
export const SearchField = ({ label, value, onInput, onSearch, always }: { label: string; value: string; onInput: (v: string) => void; onSearch: () => void; always?: boolean }) =>
  <SearchBox label={label} value={value} onInput={onInput} onEnter={onSearch} always={always} />;
export type { ThreadBlock };
/**
 * A thread: one row per block in a measured VirtualList that follows the bottom, loads older rows upward (onStart) and
 * newer ones downward (onEnd), and jumps through `handle`. Open tool and reasoning rows are kept by key across the window.
 */
export function Thread({ blocks, label, now, marked, selected, onRowClick, showSession, onOpenResult, resultSrc, handle, pending, onStart, onEnd, onStop, stopReason, onAnswer, onCopyFail, onSecret }: {
  blocks: ThreadBlock[]; label: string; now: number; marked?: string | null;
  /** Cross-message selection: rows in it are tinted; a click on a row (Shift extends) selects it, buttons and links excepted. */
  selected?: (key: string) => boolean; onRowClick?: (key: string, shift: boolean) => void; showSession?: boolean; onOpenResult?: (id: string) => void; /** The bytes URL of a result, for message image thumbnails. */ resultSrc?: (id: string) => string; handle?: { current: VirtualListHandle | null }; pending?: { count: number; onJump: () => void };
  onStart?: () => void; onEnd?: () => void; onStop?: (executionId: string) => void; stopReason?: string;
  onAnswer?: (questionId: string, answer: string) => void; onCopyFail?: (e: unknown) => void;
  /** A key request row (its options name a connection): the owner's value goes here, never to onAnswer. */
  onSecret?: (questionId: string, connection: string, value: string) => Promise<unknown>;
}) {
  const [open, setOpen] = useState<Set<string>>(new Set());
  const toggle = (k: string) => setOpen(o => { const n = new Set(o); n.has(k) ? n.delete(k) : n.add(k); return n; });
  const markOf = (b: Mark): Mark => ({ harness: b.harness, model: b.model, effort: b.effort });
  const row = (b: ThreadBlock) => {
    switch (b.kind) {
      case 'text': return <TextRow who={b.role === 'operator' ? 'operator' : 'agent'} markdown={b.markdown ?? ''} mark={markOf(b)} by={b.by} at={b.at} now={now} onCopyFail={onCopyFail} attachments={b.attachments} onOpen={onOpenResult} src={resultSrc} />;
      case 'tool': return <ToolRow name={b.name} input={b.input} output={b.output} state={b.state} startedAt={b.startedAt} endedAt={b.endedAt} open={open.has(b.key)} onToggle={() => toggle(b.key)} onCopyFail={onCopyFail} />;
      case 'reasoning': return <ReasoningRow markdown={b.markdown ?? ''} at={b.at} open={open.has(b.key)} onToggle={() => toggle(b.key)} onCopyFail={onCopyFail} />;
      case 'question': { const conn = keyConnection(b.options); return <QuestionRow text={b.text} options={Array.isArray(b.options) ? b.options : conn ? ['decline'] : []} state={b.state} answer={b.answer === undefined || typeof b.answer === 'string' ? b.answer : JSON.stringify(b.answer)} answeredBy={b.answeredBy} askedAt={b.askedAt} answeredAt={b.answeredAt} now={now} technicalAnswer={!!conn || typeof b.answer === 'object'} onAnswer={onAnswer && (a => onAnswer(b.questionId, a))} onSecret={conn && onSecret ? v => onSecret(b.questionId, conn, v) : undefined} />; }
      case 'result': return <ResultRow artifacts={b.artifacts} onOpen={onOpenResult} />;
      case 'boundary': return <BoundaryRow session={showSession ? b.sessionId ?? undefined : undefined} executionId={b.executionId} state={b.state} mark={markOf(b)} startedAt={b.startedAt} endedAt={b.endedAt} now={now} resources={b.resources} stopReason={stopReason}
        onStop={onStop && ['preparing', 'running', 'waiting'].includes(b.state) ? () => onStop(b.executionId) : undefined} />;
      default: return null;
    }
  };
  return <div class="transcript" data-ui="transcript-viewer">
    <VirtualList class="tr-body" items={blocks} getKey={b => b.key} rowHeight={ROW} dynamic follow="bottom" label={label} handle={handle} pending={pending} onStart={onStart} onEnd={onEnd}
      rowClass={b => [b.key === marked ? 'marked' : '', selected?.(b.key) ? 'picked' : ''].join(' ')}
      render={b => <div class="th-pick" style={{ display: 'contents' }} onClick={e => { if (!onRowClick || (e.target as HTMLElement).closest('button,a,input') || !(getSelection()?.isCollapsed ?? true) && !e.shiftKey) return; if (e.shiftKey) getSelection()?.removeAllRanges(); ((e.currentTarget as HTMLElement).closest('.vl') as HTMLElement | null)?.focus({ preventScroll: true }); onRowClick(b.key, e.shiftKey); }}>{row(b)}</div>} />
  </div>;
}
