// Thread transcript viewer, message/tool/question/result/boundary rows, decision block, markdown, and artifact viewer.
import { createEffect, createMemo, createSignal, For, onSettled, Show, untrack } from 'solid-js';
import type { JSX } from '@solidjs/web';
import { type IconName, type StateOf, type Harness, actions, harnesses } from '../registry.ts';
import { bytes, exact, duration, agentLabel, actor, fileLabel } from '../format.ts';
import type { Block as ThreadBlock, ResourceSummary, ThreadDeliveryStage } from '../../../../contracts/generated/ops.ts';
import { VirtualList, type VirtualListHandle } from '../virtual-list.tsx';
import { parse, renderBlocks, type TextHook } from '../markdown.tsx';
import { author, clipMessage, writeClip } from '../clip.ts';
import { isSubmitEnter } from '../keys.ts';
import tokens from '../../tokens.json';
import { Icon, Button, StateBadge, StageChip, Num, CostFigures, Ago, AgeChip, HarnessMark, Empty, ErrorBanner, copyText, SearchBox } from './primitives.tsx';
import { EntityCode, linkCodes } from './overlays.tsx';
import { TabBar } from './tables.tsx';

const ROW = parseInt(tokens.size.row);

/* ---------- Decision block (§6) ---------- */

type DecisionProps = {
  question: string; askedBy: string; addressedTo: string; since: number; now: number; waits?: JSX.Element;
  context?: JSX.Element; timeInRow?: boolean; technicalAnswer?: boolean; options: { label: string; kbd?: string; primary?: boolean }[];
  answered?: { by: string; answer: string; at: number; delivered: boolean };
  /** false: only the option buttons answer (a decision the server accepts by option only); default true. */
  typed?: boolean;
  /** A returned promise that rejects shows its reason under the answers. */
  onAnswer?: (answer: string) => void | Promise<unknown>;
  /** A key request: a write-only password field replaces the typed answer; the value goes to onSecret only, never to onAnswer. */
  onSecret?: (value: string) => Promise<unknown>;
};
export function DecisionBlock(p: DecisionProps) {
  const [text, setText] = createSignal('');
  const [failed, setFailed] = createSignal<string | null>(null);
  const send = (s: string) => {
    if (!s.trim()) return;
    setFailed(null);
    const r = p.onAnswer?.(s.trim());
    if (r) r.catch(e => setFailed(String((e as Error | null)?.message ?? e)));
  };
  // The field clears once the key is stored; a refusal shows its reason, never the value.
  const secret = () => {
    const v = text().trim();
    if (!v || !p.onSecret) return;
    setFailed(null);
    p.onSecret(v).then(() => setText(''), e => setFailed(String((e as Error | null)?.message ?? e)));
  };
  return (
    <Show
      when={p.answered}
      keyed
      fallback={
        <div
          class="decision"
          data-ui="decision-block"
          role="group"
          aria-label={p.question}
          onKeyDown={e => {
            if ((e.target as HTMLElement).tagName === 'INPUT') return;
            const o = p.options.find(opt => opt.kbd === e.key);
            if (o) send(o.label);
          }}
        >
          <div class="dq"><Icon name="bell" /><b>{p.question}</b></div>
          <div class="meta">
            {p.askedBy}{p.addressedTo === 'you' ? '' : ` · for ${p.addressedTo}`}
            <Show when={!p.timeInRow}> <> · <AgeChip t={p.since} /></></Show>
            <Show when={p.waits} keyed>{w => <> · {w}</>}</Show>
          </div>
          <Show when={p.context} keyed>{ctx => <div class="ctx">{ctx}</div>}</Show>
          <div class="answers">
            <For each={p.options}>
              {o => <Button label={o.label} kbd={o.kbd} kind={o.primary ? 'primary' : 'default'} onClick={() => send(o.label)} />}
            </For>
            <Show
              when={p.onSecret}
              fallback={
                <Show when={p.typed !== false}>
                  <input
                    value={text()}
                    placeholder="Answer"
                    aria-label="Typed answer"
                    onInput={e => setText(e.currentTarget.value)}
                    onKeyDown={e => { if (isSubmitEnter(e)) send(text()); }}
                  />
                </Show>
              }
            >
              <input
                type="password"
                autocomplete="off"
                spellcheck="false"
                value={text()}
                placeholder="Paste key"
                aria-label="Key value"
                onInput={e => setText(e.currentTarget.value)}
                onKeyDown={e => { if (isSubmitEnter(e)) { e.preventDefault(); secret(); } }}
              />
              <Button label="Add key" icon="shield" kind="primary" disabledReason={text().trim() ? undefined : 'paste the key first'} onClick={secret} />
            </Show>
          </div>
          <Show when={failed()} keyed>{err => <div class="error" role="alert">{err}</div>}</Show>
        </div>
      }
    >
      {a => (
        <Show
          when={p.timeInRow}
          fallback={
            <div class="decision answered" data-ui="decision-block" role="status">
              <Icon name={a.delivered ? 'check-check' : 'history'} />
              <span>{p.question} <b>{a.answer}</b> · {a.by} · <Ago t={a.at} />{a.delivered ? '' : ' · delivery pending'}</span>
            </div>
          }
        >
          <div class="decision answered thread-answer" data-ui="decision-block" role="status">
            <div class="dq"><Icon name="message-circle-question" label="Question" /><span>{p.question}</span></div>
            <div class="decision-reply">
              <span class="muted">Answer</span>{' '}
              {p.technicalAnswer ? <CodeBlock text={a.answer} /> : <b>{a.answer}</b>}
              {!a.delivered && ' · delivery pending'}
            </div>
          </div>
        </Show>
      )}
    </Show>
  );
}

/* ---------- Result viewer shell ---------- */

type ResultProps = {
  name: string; type: string; size: number; source: string; href?: string;
  copies: { where: string; state: StateOf<'copy'> }[];
  tabs: string[]; tab: string; onTab: (t: string) => void; children: JSX.Element;
};
export function ResultViewer(p: ResultProps) {
  return (
    <section class="rv" data-ui="result-viewer" aria-label={p.name}>
      <header class="rv-h">
        <Icon name="file-text" /><b class="nm">{p.name}</b>
        <span class="muted">{p.type} · {bytes(p.size).text} · {p.source}</span>
        <For each={p.copies}>{c => <StateBadge family="copy" state={c.state} reason={c.where} compact />}</For>
        <span class="grow" />
        <TabBar label={`${p.name} views`} tabs={p.tabs.map(t => ({ id: t, label: t }))} value={p.tab} onChange={p.onTab} />
        <Button label="Compare" icon="list" iconOnly kbd="c" />
        <Show
          when={p.href}
          keyed
          fallback={<Button label="Download" icon={actions.download} iconOnly />}
        >
          {href => <a class="btn default icon-only" data-ui="button" href={href} download={p.name} aria-label="Download" data-tip="Download"><Icon name={actions.download} /></a>}
        </Show>
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
export function ArtifactView(p: { a: ArtifactMeta; src: string }) {
  const kind = createMemo(() => viewOf(p.a.mediaType));
  const fmt = createMemo(() => kind() === 'text' ? renderOf(p.a.mediaType, p.a.name) : null);
  const [text, setText] = createSignal('', { ownedWrite: true });
  const [next, setNext] = createSignal(0, { ownedWrite: true });
  const [busy, setBusy] = createSignal(false, { ownedWrite: true });
  const [err, setErr] = createSignal<string | null>(null, { ownedWrite: true });
  const [mode, setMode] = createSignal<'preview' | 'source'>(untrack(() => fmt() ? modeOf(fmt()!) : 'source'), { ownedWrite: true });
  let decoder = new TextDecoder();

  const more = async () => {
    const a = untrack(() => p.a), src = untrack(() => p.src), n = untrack(next);
    if (untrack(busy) || n >= a.size) return;
    setBusy(true);
    try {
      const r = await fetch(src, { headers: { range: `bytes=${n}-${Math.min(a.size, n + PAGE_BYTES) - 1}` }, credentials: 'same-origin' });
      if (!r.ok) throw new Error(`${r.status}`);
      const b = new Uint8Array(await r.arrayBuffer());
      setText(t => t + decoder.decode(b, { stream: n + b.length < a.size })); /* one decoder per artifact: a character split across pages stays whole */
      setNext(cur => cur + b.length);
      setErr(null);
    } catch (e) {
      setErr(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(false);
    }
  };

  createEffect(() => p.a.id, () => {
    setText('');
    setNext(0);
    const f = untrack(fmt);
    setMode(f ? modeOf(f) : 'source');
    decoder = new TextDecoder();
  });

  createEffect(() => [p.a.id, next()] as const, ([, n]) => {
    if (untrack(kind) === 'text' && n === 0 && untrack(() => p.a.size) > 0) void more();
  });

  const flip = () => {
    const m = mode() === 'preview' ? 'source' : 'preview';
    setMode(m);
    try { sessionStorage.setItem('fleet.av.' + fmt(), m); } catch { /* session-only memory is optional */ }
  };

  const scroller = (body: JSX.Element) => (
    <div class="av-text" onScroll={e => { const el = e.currentTarget; if (el.scrollHeight - el.scrollTop - el.clientHeight < 400) void more(); }}>
      {body}
      <Show when={next() < p.a.size}>
        <div class="muted type-sm">{bytes(next()).text} of {bytes(p.a.size).text}{busy() ? ' · loading' : ''}</div>
      </Show>
    </div>
  );

  const rendered = () => {
    const f = fmt();
    if (f === 'html') return <div class="av-frame page" data-tip="Agent output running in a sandbox: not part of Fleet"><iframe src={`${p.src}/preview`} sandbox="allow-scripts allow-forms allow-modals" title={p.a.name} referrerpolicy="no-referrer" /></div>;
    if (f === 'svg') return <div class="av-frame"><img src={p.src} alt={p.a.name} /></div>;
    if (f === 'markdown') return scroller(<Markdown text={text()} />);
    if (f === 'json') return scroller(<pre><code>{next() < p.a.size ? text() : pretty(text())}</code></pre>);
    const csvRows = createMemo(() => {
      const all = parseCsv(text());
      const trimmed = next() < p.a.size ? all.slice(0, -1) : all; // the last row of a partial page may be cut
      return { head: trimmed[0] ?? [], rows: trimmed.slice(1) };
    });
    return scroller(
      <table class="av-table">
        <thead><tr><For each={csvRows().head}>{h => <th>{h}</th>}</For></tr></thead>
        <tbody><For each={csvRows().rows}>{r => <tr><For each={r}>{c => <td>{c}</td>}</For></tr>}</For></tbody>
      </table>,
    );
  };

  return (
    <div class="av" data-ui="artifact-view">
      <Show when={kind() === 'image'}>
        <div class="av-frame"><img src={p.src} alt={p.a.name} loading="lazy" /></div>
      </Show>
      <Show when={kind() === 'video'}>
        <div class="av-frame video"><video src={p.src} controls preload="metadata" /></div>
      </Show>
      <Show when={kind() === 'text'}>
        <div class="bar">
          <Show when={fmt()}>
            <Button label={mode() === 'preview' ? 'Source' : 'Preview'} icon={mode() === 'preview' ? 'type' : 'panels-top-left'} onClick={flip} />
          </Show>
          <Button
            label="Copy source"
            icon={actions.copy}
            iconOnly
            kind="quiet"
            onClick={() => (next() < p.a.size ? fetch(p.src, { credentials: 'same-origin' }).then(r => r.text()) : Promise.resolve(text())).then(copyText).catch(e => setErr((e as Error).message))}
          />
        </div>
        {fmt() && mode() === 'preview' ? rendered() : scroller(<pre><code>{text()}</code></pre>)}
      </Show>
      <Show when={kind() === 'download'}>
        <Empty text={`${p.a.mediaType} has no view here.`} />
      </Show>
      <Show when={err()} keyed>{e => <ErrorBanner text={`Did not load: ${e}`} onRetry={more} />}</Show>
    </div>
  );
}

/* ---------- Thread rows (transcript-viewer.md; docs/designs/fleet-conversations.md) ---------- */

type TextBounds = { from: number; to: number };
export type ThreadTextPoint = { key: string; offset: number };

let selectionHighlight: Highlight | undefined;

/** Built as nodes. Fenced code has a copy button, and D17 codes use the shared entity reference. */
export function Markdown(p: {
  text: string; onCopyFail?: (e: unknown) => void; selectable?: boolean; bounds?: TextBounds;
}) {
  let body: HTMLDivElement | undefined;
  const rendered = createMemo(() => {
    const sel = !!p.selectable;
    const literal: TextHook = (value, key, entities) => {
      if (!sel) return entities ? linkCodes(value.text, key) : value.text;
      return value.fragments.map(({ text, at }) => {
        const span = (t: string, start: number) => <span data-thread-offset={at + start}>{t}</span>;
        return entities ? linkCodes(text, at, span) : span(text, 0);
      });
    };
    return renderBlocks(
      parse(p.text, value => value),
      code => (
        <div class="code">
          <pre><code>{literal(code, 0, false)}</code></pre>
          <span class="code-copy">
            <Button label="Copy code" icon={actions.copy} iconOnly kind="quiet" onClick={() => copyText(code.text).catch(e => p.onCopyFail?.(e))} />
          </span>
        </div>
      ),
      literal,
    );
  });

  createEffect(
    () => [p.bounds?.from, p.bounds?.to, p.text, p.selectable, rendered()] as const,
    ([bFrom, bTo, txt, sel]) => {
      if (bFrom === undefined || bTo === undefined || !body || !sel) return;
      // The server indexes code points. DOM ranges use UTF-16.
      const characters = Array.from(txt);
      const from = characters.slice(0, bFrom).join('').length;
      const to = characters.slice(0, bTo).join('').length;
      const highlight = selectionHighlight ??= new Highlight();
      CSS.highlights.set('thread-selection', highlight);
      const ranges: Range[] = [];
      for (const span of body.querySelectorAll<HTMLElement>('[data-thread-offset]')) {
        const node = span.firstChild;
        if (!node) continue;
        const at = Number(span.dataset.threadOffset);
        const start = Math.max(0, from - at), end = Math.min(node.textContent!.length, to - at);
        if (start >= end) continue;
        const range = new Range();
        range.setStart(node, start);
        range.setEnd(node, end);
        highlight.add(range);
        ranges.push(range);
      }
      return () => ranges.forEach(range => highlight.delete(range));
    },
  );
  return <div class="md" ref={el => { body = el; }}>{rendered()}</div>;
}

type Mark = { harness?: string | null; model?: string | null; effort?: string | null };
/** Agent mark (D4): harness icon and the short label; the model id on hover. */
export function AgentMark(p: Mark) {
  return (
    <span class="agent-mark">
      <Show when={p.harness && p.harness in harnesses}>
        <HarnessMark h={p.harness as Harness} />
      </Show>
      <span class="lbl" data-tip={p.model ?? undefined}>
        {agentLabel(p.model, p.effort) || p.harness /* no model recorded: the harness still names the author */}
      </span>
    </span>
  );
}

function ThreadService(p: {
  at?: number | null; timing?: JSX.Element; action?: JSX.Element; meta?: JSX.Element; reveal?: boolean;
}) {
  return (
    <div class="th-service">
      <span class={p.reveal ? 'th-action th-copy' : 'th-action'}>{p.action}</span>
      <span class="th-time">
        <Show when={p.at != null}><Ago t={p.at!} /></Show>
        {p.timing}
      </span>
      <Show when={p.meta} keyed>{m => <span class="th-meta">{m}</span>}</Show>
    </div>
  );
}

/** An operator or agent message: a head line with who wrote it and when (OR-2), with a left-aligned metadata row below the content (OR-21).
 *  Copy writes the head line and the body as plain markdown and as chat-safe HTML (clip.ts, formats.md "Copy"). */
export function TextRow(p: {
  selectable?: boolean; bounds?: TextBounds; who: 'operator' | 'agent'; markdown: string; mark?: Mark; by?: string | null; at?: number | null; now?: number; onCopyFail?: (e: unknown) => void;
  /** The files the message carried; images show a thumbnail from src, every one opens in the result viewer. */
  attachments?: ArtifactMeta[]; onOpen?: (id: string) => void; src?: (id: string) => string;
  /** The one session view (OR-51, OR-54): no service row; the operator's words sit in a bubble on the right. */
  quiet?: boolean;
}) {
  const name = createMemo(() => author({ role: p.who, by: p.by, ...(p.mark ?? {}) }));
  return (
    <div class={['th-row th-text', p.who, { quiet: !!p.quiet }]} data-ui="thread-row">
      <div class="th-main">
        <Show when={p.markdown}>
          <Markdown text={p.markdown} onCopyFail={p.onCopyFail} selectable={p.selectable} bounds={p.bounds} />
        </Show>
        <Show when={p.attachments?.length}>
          <MessageFiles files={p.attachments!} onOpen={p.onOpen} src={p.src} />
        </Show>
      </div>
      <Show when={!p.quiet}>
        <ThreadService
          at={p.at}
          reveal
          meta={
            p.who === 'operator'
              ? <span class="who" data-tip={p.by ?? undefined}>{name()}</span>
              : <AgentMark harness={p.mark?.harness} model={p.mark?.model} effort={p.mark?.effort} />
          }
          action={
            <Button
              label="Copy message"
              icon={actions.copy}
              iconOnly
              kind="quiet"
              onClick={() => writeClip(clipMessage(name(), p.at, p.now ?? Date.now(), [p.markdown, ...(p.attachments ?? []).map(a => `Attachment: ${a.name}`)].filter(Boolean).join('\n\n'))).catch(e => p.onCopyFail?.(e))}
            />
          }
        />
      </Show>
    </div>
  );
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
export function MessageFiles(p: { files: ArtifactMeta[]; onOpen?: (id: string) => void; src?: (id: string) => string }) {
  const items = createMemo(() => namedFiles(p.files, 'Attachment'));
  return (
    <div class="msg-files" aria-label="Attachments">
      <For each={items()}>
        {({ file: a, name, position, total }) => {
          const image = createMemo(() => viewOf(a.mediaType) === 'image' && !!p.src);
          return (
            <a
              class={['att', { img: image() }]}
              href={`/results/${encodeURIComponent(a.id)}`}
              data-tip={`${a.name} · ${bytes(a.size).text}`}
              onClick={e => { if (p.onOpen && !e.metaKey && !e.ctrlKey) { e.preventDefault(); p.onOpen(a.id); } }}
            >
              <Show
                when={image()}
                fallback={
                  <>
                    <span class="att-thumb"><Icon name="file-text" /></span>
                    <span class="att-name">{name}</span>
                    <span class="att-size">{bytes(a.size).text}</span>
                  </>
                }
              >
                <img class="att-thumb" src={p.src!(a.id)} alt={a.name} loading="lazy" />
              </Show>
              <Show when={total > 1}>
                <span class="th-file-index muted" aria-label={`File ${position} of ${total}`}>{position}</span>
              </Show>
            </a>
          );
        }}
      </For>
    </div>
  );
}

const raw = (v: unknown) => typeof v === 'string' ? v : JSON.stringify(v, null, 1) ?? '';
const fenced = (v: unknown) => { const t = raw(v), ticks = '`'.repeat(Math.max(3, ...[...t.matchAll(/`+/g)].map(m => m[0].length + 1))); return ticks + '\n' + t + '\n' + ticks; };
/** Raw tool input or output: preformatted as is (never parsed as markdown), with its own copy. */
function CodeBlock(p: { text: string; onCopyFail?: (e: unknown) => void }) {
  return (
    <div class="md">
      <div class="code">
        <pre><code>{p.text}</code></pre>
        <span class="code-copy"><Button label="Copy code" icon={actions.copy} iconOnly kind="quiet" onClick={() => copyText(p.text).catch(e => p.onCopyFail?.(e))} /></span>
      </div>
    </div>
  );
}
/** A collapsed one-line row that opens downward on click; the clicked header stays where it is (OR-23). */
function Fold(p: {
  at?: number | null; timing?: JSX.Element; icon: IconName; head: JSX.Element; body: JSX.Element; open: boolean; onToggle: () => void; copy: string; onCopyFail?: (e: unknown) => void;
}) {
  return (
    <div class={['th-row th-fold', { open: p.open }]} data-ui="thread-row">
      <ThreadService
        at={p.timing ? undefined : p.at}
        timing={p.timing}
        reveal
        meta={
          <button type="button" class="th-head" aria-expanded={p.open ? 'true' : 'false'} onClick={() => p.onToggle()}>
            <span class={['chev', { open: p.open }]}><Icon name="chevron-down" /></span>
            <Icon name={p.icon} />
            {p.head}
          </button>
        }
        action={<Button label="Copy" icon={actions.copy} iconOnly kind="quiet" onClick={() => copyText(p.copy).catch(e => p.onCopyFail?.(e))} />}
      />
      <Show when={p.open}><div class="th-body">{p.body}</div></Show>
    </div>
  );
}
export function ToolRow(p: {
  name: string; input: unknown; output: unknown; state: string; startedAt?: number | null; endedAt?: number | null; open: boolean; onToggle: () => void; onCopyFail?: (e: unknown) => void;
}) {
  const secs = createMemo(() => p.startedAt != null && p.endedAt != null ? duration((p.endedAt - p.startedAt) / 1000) : null);
  const copy = createMemo(() => [`Tool ${p.name} (${p.state})`, p.input != null ? fenced(p.input) : '', p.output != null ? fenced(p.output) : ''].filter(Boolean).join('\n\n'));
  return (
    <Fold
      icon="terminal"
      at={p.startedAt}
      timing={<Show when={secs()} keyed>{s => <span data-tip={`Duration: ${s.long} · Started ${exact(p.startedAt!)}`}>Ran {s.text}</span>}</Show>}
      open={p.open}
      onToggle={p.onToggle}
      copy={copy()}
      onCopyFail={p.onCopyFail}
      head={<span class="th-line"><span class="mono ell" data-tip={p.name}>{p.name}</span><span class={['svc', { 't-danger': ['failed', 'unfinished'].includes(p.state) }]}>{p.state}</span></span>}
      body={
        <>
          <Show when={p.input != null}><CodeBlock text={raw(p.input)} onCopyFail={p.onCopyFail} /></Show>
          <Show when={p.output != null}><CodeBlock text={raw(p.output)} onCopyFail={p.onCopyFail} /></Show>
        </>
      }
    />
  );
}
export function ReasoningRow(p: {
  selectable?: boolean; bounds?: TextBounds; markdown: string; at?: number | null; open: boolean; onToggle: () => void; onCopyFail?: (e: unknown) => void;
}) {
  return (
    <Fold
      icon="bot"
      at={p.at}
      open={p.open}
      onToggle={p.onToggle}
      copy={p.markdown}
      onCopyFail={p.onCopyFail}
      head={<span class="th-line"><span>Reasoning</span><span class="svc ell">{p.markdown.split('\n')[0]}</span></span>}
      body={<Markdown text={p.markdown} onCopyFail={p.onCopyFail} selectable={p.selectable} bounds={p.bounds} />}
    />
  );
}
/** A question in the thread (DecisionBlock); answered, it collapses to one line with the answer and who gave it. */
export function QuestionRow(p: {
  text: string; options: unknown[]; state: string; answer?: string; answeredBy?: string; askedAt?: number | null; answeredAt?: number | null; now: number; onAnswer?: (a: string) => void; onSecret?: (value: string) => Promise<unknown>; technicalAnswer?: boolean;
}) {
  const answered = createMemo(() => p.state === 'answered' && p.answer !== undefined);
  return (
    <div class="th-row th-q" data-ui="thread-row">
      <div class="th-question">
        <DecisionBlock
          question={p.text}
          askedBy="agent"
          addressedTo="you"
          since={p.askedAt ?? p.now}
          now={p.now}
          timeInRow
          technicalAnswer={p.technicalAnswer}
          options={p.options.map((o, i) => ({ label: String(o), kbd: String(i + 1), primary: i === 0 }))}
          answered={answered() ? { by: actor(p.answeredBy) || 'operator', answer: p.answer!, at: p.answeredAt ?? p.now, delivered: true } : undefined}
          onAnswer={p.onAnswer}
          onSecret={p.onSecret}
        />
      </div>
      <ThreadService at={answered() ? p.answeredAt : p.askedAt} meta={answered() ? (actor(p.answeredBy) || 'Operator') : undefined} />
    </div>
  );
}
/** The connection a key request (a secret question) asks a key for: its options are {connection}. */
export const keyConnection = (options: unknown): string | undefined => {
  const c = options && !Array.isArray(options) && typeof options === 'object' ? (options as { connection?: unknown }).connection : undefined;
  return typeof c === 'string' ? c : undefined;
};
/** Results are listed one per line. Disambiguate matching short names without exposing full paths. */
export function ResultRow(p: { artifacts: ArtifactMeta[]; deliveryStage?: ThreadDeliveryStage; onOpen?: (id: string) => void }) {
  const items = createMemo(() => namedFiles(p.artifacts, 'Result'));
  const stage = () => p.deliveryStage === 'unknown' ? undefined : p.deliveryStage;
  return (
    <div class="th-row th-result" data-ui="thread-row">
      <span class="th-svc" aria-live="polite">
        <Show when={stage()}>{value => <StageChip stage={value()} />}</Show>
        <Show when={p.deliveryStage === 'unknown'}>Delivery stage unknown</Show>
      </span>
      <Show when={items().length > 0}>
        <ul class="th-links" aria-label="Results">
          <For each={items()}>
            {({ file: artifact, name, position, total }) => (
              <li class="th-file">
                <EntityCode id={artifact.id} kind="result" />
                <a
                  href={`/results/${encodeURIComponent(artifact.id)}`}
                  data-tip={`${artifact.name} · ${artifact.mediaType} · ${bytes(artifact.size).text}`}
                  onClick={e => { if (p.onOpen && !e.metaKey && !e.ctrlKey) { e.preventDefault(); p.onOpen(artifact.id); } }}
                >
                  {name}
                </a>
                <Show when={total > 1}>
                  <span class="th-file-index muted" aria-label={`File ${position} of ${total}`}>{position}</span>
                </Show>
              </li>
            )}
          </For>
        </ul>
      </Show>
    </div>
  );
}
/** An execution start: state, agent mark, duration chip, resource stats, and Stop while it runs (CUJ 5). */
export function BoundaryRow(p: {
  session?: string; executionId: string; state: StateOf<'execution'>; mark: Mark; startedAt?: number | null; endedAt?: number | null; now: number; resources?: ResourceSummary; onStop?: () => void; stopReason?: string;
}) {
  const active = createMemo(() => ['preparing', 'running', 'waiting'].includes(p.state));
  const secs = createMemo(() =>
    active() ? null : p.resources?.durations?.executionWallMs != null ? p.resources.durations.executionWallMs / 1000 : p.startedAt != null && p.endedAt != null ? (p.endedAt - p.startedAt) / 1000 : null,
  );
  const elapsedSecs = createMemo(() => p.resources?.durations?.elapsedMs != null ? p.resources.durations.elapsedMs / 1000 : null);
  const durTip = createMemo(() => {
    const s = secs(), el = elapsedSecs();
    if (s === null) return undefined;
    return [
      `Duration: ${duration(s).long}`,
      el != null && Math.abs(el - s) >= 1 ? `Elapsed: ${duration(el).long}` : '',
      p.startedAt != null ? `Started: ${exact(p.startedAt)}` : '',
    ].filter(Boolean).join(' · ');
  });
  return (
    <div class="th-row th-boundary" data-ui="thread-row">
      <ThreadService
        at={active() ? p.startedAt : undefined}
        timing={<Show when={secs() !== null}><span data-tip={durTip()}>Ran {duration(secs()!).text}</span></Show>}
        meta={
          <span class="th-boundary-info">
            <StateBadge family="execution" state={p.state} compact />
            <EntityCode id={p.executionId} kind="execution" label />
            <Show when={p.session} keyed>{sid => <EntityCode id={sid} kind="conversation" />}</Show>
            <AgentMark harness={p.mark.harness} model={p.mark.model} effort={p.mark.effort} />
            <span class="th-svc">
              <Show when={p.resources?.tokens?.total != null && (p.resources.tokens.basis !== 'unavailable' || p.resources.tokens.total > 0)}>
                <span class="hc" data-tip="Tokens">
                  <Num m={{ value: p.resources!.tokens!.total, basis: p.resources!.tokens!.basis === 'unavailable' ? 'measured' : p.resources!.tokens!.basis, partial: p.resources!.tokens!.basis === 'unavailable' ? 'incomplete telemetry' : undefined }} kind="count" />
                </span>
              </Show>
              <Show when={p.resources && p.resources.tools.totalCalls > 0}>
                <span class="hc" data-tip={`${p.resources!.tools.totalCalls} tool invocations${p.resources!.tools.failedCalls ? `, ${p.resources!.tools.failedCalls} failed` : ''}`}>
                  <Icon name="terminal" label="Tools" />
                  <Num m={{ value: p.resources!.tools.totalCalls, basis: p.resources!.tools.basis === 'unavailable' ? 'measured' : p.resources!.tools.basis, partial: p.resources!.tools.basis === 'unavailable' ? 'incomplete telemetry' : undefined }} kind="count" />
                </span>
              </Show>
              <Show when={p.resources && p.resources.tools.failedCalls > 0}>
                <span class="chip t-danger" data-tip={`${p.resources!.tools.failedCalls} failed tool calls`}>
                  <Icon name="octagon-x" label="Failed tools" />{p.resources!.tools.failedCalls}
                </span>
              </Show>
              <Show when={p.resources?.cost?.length}>
                <CostFigures cost={p.resources!.cost} />
              </Show>
            </span>
          </span>
        }
        action={<Show when={p.onStop} keyed>{stop => <Button label="Stop" icon="pause" iconOnly disabledReason={p.stopReason} onClick={stop} />}</Show>}
      />
    </div>
  );
}

/** A search field for one scope (a thread): Enter searches; an icon until used (OR-31). */
export function SearchField(p: { label: string; value: string; onInput: (v: string) => void; onSearch: () => void; always?: boolean }) {
  return <SearchBox label={p.label} value={p.value} onInput={p.onInput} onEnter={p.onSearch} always={p.always} />;
}
export type { ThreadBlock };

/**
 * A thread: one row per block in a measured VirtualList that follows the bottom, loads older rows upward (onStart) and
 * newer ones downward (onEnd), and jumps through `handle`. Open tool and reasoning rows are kept by key across the window.
 */
export function Thread(p: {
  blocks: ThreadBlock[]; label: string; now: number; marked?: string | null;
  /** Cross-message selection: rows in it are tinted; a click on a row (Shift extends) selects it, buttons and links excepted. */
  selected?: (key: string) => boolean; textBounds?: (key: string) => TextBounds | undefined;
  onTextRange?: (anchor: ThreadTextPoint, focus: ThreadTextPoint) => void;
  onRowClick?: (key: string, shift: boolean) => void; showSession?: boolean; onOpenResult?: (id: string) => void;
  /** The bytes URL of a result, for message image thumbnails. */ resultSrc?: (id: string) => string;
  handle?: { current: VirtualListHandle | null }; pending?: { count: number; onJump: () => void };
  onStart?: () => void; onEnd?: () => void; onStop?: (executionId: string) => void; stopReason?: string;
  onAnswer?: (questionId: string, answer: string) => void; onCopyFail?: (e: unknown) => void;
  /** A key request row (its options name a connection): the owner's value goes here, never to onAnswer. */
  onSecret?: (questionId: string, connection: string, value: string) => Promise<unknown>;
}) {
  const [open, setOpen] = createSignal<Set<string>>(new Set());
  const toggle = (k: string) => setOpen(o => { const n = new Set(o); if (n.has(k)) n.delete(k); else n.add(k); return n; });
  const markOf = (b: Mark): Mark => ({ harness: b.harness, model: b.model, effort: b.effort });
  let root: HTMLDivElement | undefined;
  const canSelectText = createMemo(() => !!p.onTextRange);

  const pointAt = (e: PointerEvent): ThreadTextPoint | undefined => {
    const caret = document.caretPositionFromPoint(e.clientX, e.clientY);
    if (caret?.offsetNode.nodeType !== Node.TEXT_NODE) return;
    const span = caret.offsetNode.parentElement;
    if (span?.dataset.threadOffset === undefined || !root?.contains(span)) return;
    const key = span.closest<HTMLElement>('.vl-row')?.dataset.key;
    const block = untrack(() => p.blocks).find(b => b.key === key);
    if (block?.kind !== 'text' && block?.kind !== 'reasoning') return;
    const source = block.plain || block.markdown || '';
    const offset = Number(span.dataset.threadOffset) + caret.offset;
    return { key: block.key, offset: Array.from(source.slice(0, offset)).length };
  };

  let gesture: { id: number; anchor: ThreadTextPoint; focus?: ThreadTextPoint } | null = null;
  createEffect(canSelectText, enabled => {
    if (!enabled) return;
    const update = (drag: NonNullable<typeof gesture>, focus?: ThreadTextPoint) => {
      if (!focus || !drag.focus && focus.key === drag.anchor.key) return;
      if (drag.focus?.key === focus.key && drag.focus.offset === focus.offset) return;
      drag.focus = focus;
      p.onTextRange?.(drag.anchor, focus);
    };
    const move = (e: PointerEvent) => {
      if (e.pointerId === gesture?.id) update(gesture, pointAt(e));
    };
    const cancel = () => {
      const drag = gesture;
      gesture = null;
      if (drag?.focus) {
        getSelection()?.removeAllRanges();
        p.onTextRange?.(drag.anchor, drag.anchor);
      }
    };
    const end = (e: PointerEvent) => {
      const drag = gesture;
      if (e.pointerId !== drag?.id) return;
      if (e.type === 'pointercancel') { cancel(); return; }
      update(drag, pointAt(e));
      gesture = null;
      if (!drag.focus || drag.focus.key === drag.anchor.key) return;
      getSelection()?.removeAllRanges();
      root?.querySelector<HTMLElement>('.vl')?.focus({ preventScroll: true });
    };
    document.addEventListener('pointermove', move);
    document.addEventListener('pointerup', end);
    document.addEventListener('pointercancel', end);
    window.addEventListener('blur', cancel);
    return () => {
      document.removeEventListener('pointermove', move);
      document.removeEventListener('pointerup', end);
      document.removeEventListener('pointercancel', end);
      window.removeEventListener('blur', cancel);
      gesture = null;
    };
  });

  const row = (b: ThreadBlock): JSX.Element => {
    // The current projection exposes its Markdown as plain source. Keep full-row copy available for other projections.
    const selectable = canSelectText() && (b.kind === 'text' || b.kind === 'reasoning') &&
      (!b.plain || b.plain === b.markdown);
    const bounds = p.textBounds?.(b.key);
    switch (b.kind) {
      case 'text': return <TextRow who={b.role === 'operator' ? 'operator' : 'agent'} markdown={b.markdown ?? ''} selectable={selectable} bounds={bounds} mark={markOf(b)} by={b.by} at={b.at} now={p.now} onCopyFail={p.onCopyFail} attachments={b.attachments} onOpen={p.onOpenResult} src={p.resultSrc} />;
      case 'tool': return <ToolRow name={b.name} input={b.input} output={b.output} state={b.state} startedAt={b.startedAt} endedAt={b.endedAt} open={open().has(b.key)} onToggle={() => toggle(b.key)} onCopyFail={p.onCopyFail} />;
      case 'reasoning': return <ReasoningRow markdown={b.markdown ?? ''} selectable={selectable} bounds={bounds} at={b.at} open={open().has(b.key)} onToggle={() => toggle(b.key)} onCopyFail={p.onCopyFail} />;
      case 'question': {
        const conn = keyConnection(b.options);
        return (
          <QuestionRow
            text={b.text}
            options={Array.isArray(b.options) ? b.options : conn ? ['decline'] : []}
            state={b.state}
            answer={b.answer === undefined || typeof b.answer === 'string' ? b.answer : JSON.stringify(b.answer)}
            answeredBy={b.answeredBy}
            askedAt={b.askedAt}
            answeredAt={b.answeredAt}
            now={p.now}
            technicalAnswer={!!conn || typeof b.answer === 'object'}
            onAnswer={p.onAnswer && (a => p.onAnswer!(b.questionId, a))}
            onSecret={conn && p.onSecret ? v => p.onSecret!(b.questionId, conn, v) : undefined}
          />
        );
      }
      case 'result': return <ResultRow artifacts={b.artifacts} deliveryStage={b.deliveryStage} onOpen={p.onOpenResult} />;
      case 'boundary': return (
        <BoundaryRow
          session={p.showSession ? b.sessionId ?? undefined : undefined}
          executionId={b.executionId}
          state={b.state}
          mark={markOf(b)}
          startedAt={b.startedAt}
          endedAt={b.endedAt}
          now={p.now}
          resources={b.resources}
          stopReason={p.stopReason}
          onStop={p.onStop && ['preparing', 'running', 'waiting'].includes(b.state) ? () => p.onStop!(b.executionId) : undefined}
        />
      );
      default: return null;
    }
  };

  return (
    <div
      class="transcript"
      data-ui="transcript-viewer"
      ref={el => { root = el; }}
      onPointerDown={e => {
        if (!p.onTextRange || e.button !== 0 || !e.isPrimary || e.pointerType === 'touch' || e.shiftKey || (e.target as Element).closest('button,input,textarea')) return;
        const anchor = pointAt(e);
        if (anchor) gesture = { id: e.pointerId, anchor };
      }}
      onDragStart={e => { if (gesture) e.preventDefault(); }}
    >
      <VirtualList
        class="tr-body"
        items={p.blocks}
        getKey={b => b.key}
        rowHeight={ROW}
        dynamic
        follow="bottom"
        label={p.label}
        handle={p.handle}
        pending={p.pending}
        onStart={p.onStart}
        onEnd={p.onEnd}
        rowClass={b => [b.key === p.marked ? 'marked' : '', p.selected?.(b.key) ? 'picked' : ''].join(' ')}
        render={b => (
          <div
            class="th-pick"
            style={{ display: 'contents' }}
            onClick={e => {
              if (!p.onRowClick || (e.target as HTMLElement).closest('button,a,input') || !(getSelection()?.isCollapsed ?? true) && !e.shiftKey) return;
              if (e.shiftKey) getSelection()?.removeAllRanges();
              (e.currentTarget.closest('.vl') as HTMLElement | null)?.focus({ preventScroll: true });
              p.onRowClick(b.key, e.shiftKey);
            }}
          >
            {row(b)}
          </div>
        )}
      />
    </div>
  );
}
