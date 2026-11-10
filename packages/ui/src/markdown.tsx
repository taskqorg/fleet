// Renders parsed markdown (markdown.ts) as Preact nodes: no HTML string is ever injected; links only for http, https, mailto.
import type { ComponentChildren, VNode } from 'preact';
import { type MdBlock, SAFE_LINK as SAFE } from './markdown.ts';
export { parse } from './markdown.ts';

/** Inline spans: code first (its content is literal), then links, bold, italic, strike. */
export function inline(s: string, depth = 0): ComponentChildren[] {
  if (depth >= 16) return [s]; // nested emphasis past this stays text (untrusted input, bounded stack)
  const out: ComponentChildren[] = [];
  const re = /(`+)([\s\S]*?[^`])\1(?!`)|\[([^\]]+)\]\(([^)\s]+)(?:\s+"[^"]*")?\)|<(https?:\/\/[^>\s]+)>|\*\*([\s\S]+?)\*\*|__([\s\S]+?)__|~~([\s\S]+?)~~|\*([^*\s][\s\S]*?)\*|(?<![\w])_([^_\s][\s\S]*?)_(?![\w])|(\n)/g;
  let last = 0, m: RegExpExecArray | null, k = 0;
  while ((m = re.exec(s))) {
    if (m.index > last) out.push(s.slice(last, m.index));
    if (m[2] !== undefined) out.push(<code key={k++}>{m[2].replace(/^ (.*) $/, '$1')}</code>);
    else if (m[3] !== undefined) out.push(SAFE.test(m[4]) ? <a key={k++} href={m[4]} target="_blank" rel="noopener noreferrer">{inline(m[3], depth + 1)}</a> : <span key={k++}>{inline(m[3], depth + 1)}</span>);
    else if (m[5] !== undefined) out.push(<a key={k++} href={m[5]} target="_blank" rel="noopener noreferrer">{m[5]}</a>);
    else if (m[6] !== undefined || m[7] !== undefined) out.push(<strong key={k++}>{inline(m[6] ?? m[7], depth + 1)}</strong>);
    else if (m[8] !== undefined) out.push(<s key={k++}>{inline(m[8], depth + 1)}</s>);
    else if (m[9] !== undefined || m[10] !== undefined) out.push(<em key={k++}>{inline(m[9] ?? m[10], depth + 1)}</em>);
    else if (m[11] !== undefined) out.push(<br key={k++} />);
    last = re.lastIndex;
  }
  if (last < s.length) out.push(s.slice(last));
  return out;
}

/** Renders blocks; `code` renders fenced code (the caller adds its copy button). */
export function renderBlocks(blocks: MdBlock[], code: (text: string, lang: string, key: number) => VNode): ComponentChildren[] {
  return blocks.map((b, k) => {
    switch (b.t) {
      case 'code': return code(b.text, b.lang, k);
      case 'h': { const H = `h${Math.min(6, b.level + 2)}` as 'h3'; return <H key={k} class="md-h">{inline(b.text)}</H>; }
      case 'p': return <p key={k}>{inline(b.text)}</p>;
      case 'hr': return <hr key={k} />;
      case 'quote': return <blockquote key={k}>{renderBlocks(b.blocks, code)}</blockquote>;
      case 'list': { const items = b.items.map((it, j) => <li key={j}>{it.length === 1 && it[0].t === 'p' ? inline(it[0].text) : renderBlocks(it, code)}</li>);
        return b.ordered ? <ol key={k} start={b.start}>{items}</ol> : <ul key={k}>{items}</ul>; }
      case 'table': return <div key={k} class="md-table"><table><thead><tr>{b.head.map((c, j) => <th key={j} style={b.align[j] ? { textAlign: b.align[j]! } : undefined}>{inline(c)}</th>)}</tr></thead>
        <tbody>{b.rows.map((r, j) => <tr key={j}>{b.head.map((_, x) => <td key={x} style={b.align[x] ? { textAlign: b.align[x]! } : undefined}>{inline(r[x] ?? '')}</td>)}</tr>)}</tbody></table></div>;
    }
  });
}
