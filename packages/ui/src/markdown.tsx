// Renders parsed markdown (markdown.ts) as Solid nodes: no HTML string is ever injected; links only for http, https, mailto.
import { Dynamic, type JSX } from '@solidjs/web';
import { type MdBlock, type MdText, sliceText, SAFE_LINK as SAFE } from './markdown.ts';
export { parse } from './markdown.ts';

/** Render a literal fragment, with optional entity linking outside code and markdown links. */
export type TextHook = (text: MdText, key: number, linkEntities: boolean) => JSX.Element;

/** Inline spans: code first (its content is literal), then links, bold, italic, strike. */
function inline(source: MdText, text: TextHook, depth = 0, linkEntities = true): JSX.Element[] {
  if (depth >= 16) return [text(source, 0, false)]; // bound the stack for deeply nested untrusted input
  const out: JSX.Element[] = [];
  const re = /(`+)([\s\S]*?[^`])\1(?!`)|\[([^\]]+)\]\(([^)\s]+)(?:\s+"[^"]*")?\)|<(https?:\/\/[^>\s]+)>|\*\*([\s\S]+?)\*\*|__([\s\S]+?)__|~~([\s\S]+?)~~|\*([^*\s][\s\S]*?)\*|(?<![\w])_([^_\s][\s\S]*?)_(?![\w])|(\n)/dg;
  let last = 0, m: RegExpExecArray | null, k = 0;
  while ((m = re.exec(source.text))) {
    const indices = m.indices!;
    const group = (i: number) => sliceText(source, ...indices[i]!);
    const nested = (i: number, entities = linkEntities) => inline(group(i), text, depth + 1, entities);
    if (m.index > last) out.push(text(sliceText(source, last, m.index), k++, linkEntities));
    if (m[2] !== undefined) {
      const [start, end] = indices[2]!;
      const padded = /^ (.*) $/.test(m[2]);
      out.push(<code>{text(sliceText(source, start + Number(padded), end - Number(padded)), 0, false)}</code>);
    } else if (m[3] !== undefined) out.push(SAFE.test(m[4]) ? <a href={m[4]} target="_blank" rel="noopener noreferrer">{nested(3, false)}</a> : <span>{nested(3, false)}</span>);
    else if (m[5] !== undefined) out.push(<a href={m[5]} target="_blank" rel="noopener noreferrer">{text(group(5), 0, false)}</a>);
    else if (m[6] !== undefined || m[7] !== undefined) out.push(<strong>{nested(m[6] === undefined ? 7 : 6)}</strong>);
    else if (m[8] !== undefined) out.push(<s>{nested(8)}</s>);
    else if (m[9] !== undefined || m[10] !== undefined) out.push(<em>{nested(m[9] === undefined ? 10 : 9)}</em>);
    else if (m[11] !== undefined) out.push(<br />);
    last = re.lastIndex;
  }
  if (last < source.text.length) out.push(text(sliceText(source, last), k, linkEntities));
  return out;
}

/** Renders blocks; `code` renders fenced code (the caller adds its copy button). */
export function renderBlocks(blocks: MdBlock<MdText>[], code: (text: MdText, key: number) => JSX.Element, text: TextHook): JSX.Element[] {
  return blocks.map((b, k) => {
    switch (b.t) {
      case 'code': return code(b.text, k);
      case 'h': return <Dynamic component={`h${Math.min(6, b.level + 2)}` as 'h3'} class="md-h">{inline(b.text, text)}</Dynamic>;
      case 'p': return <p>{inline(b.text, text)}</p>;
      case 'hr': return <hr />;
      case 'quote': return <blockquote>{renderBlocks(b.blocks, code, text)}</blockquote>;
      case 'list': {
        const items = b.items.map(it => <li>{it.length === 1 && it[0].t === 'p' ? inline(it[0].text, text) : renderBlocks(it, code, text)}</li>);
        return b.ordered ? <ol start={b.start}>{items}</ol> : <ul>{items}</ul>;
      }
      case 'table': return (
        <div class="md-table">
          <table>
            <thead><tr>{b.head.map((c, j) => <th style={b.align[j] ? { 'text-align': b.align[j]! } : undefined}>{inline(c, text)}</th>)}</tr></thead>
            <tbody>{b.rows.map(r => <tr>{b.head.map((_, x) => <td style={b.align[x] ? { 'text-align': b.align[x]! } : undefined}>{r[x] && inline(r[x], text)}</td>)}</tr>)}</tbody>
          </table>
        </div>
      );
    }
  });
}
