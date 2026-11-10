// Markdown for agent replies (docs/designs/fleet-conversations.md, "Copy"; security NFR: no raw HTML). A small block and inline
// parser that builds Preact nodes directly, so text is always escaped by Preact and no HTML string is ever injected. Links are
// kept only for http, https and mailto. Covers what agents write: headings, paragraphs, lists (nested by indent), quotes,
// tables, rules, fenced code, inline code, bold, italic, strike and links. This file parses; markdown.tsx renders.
export type MdBlock =
  | { t: 'code'; lang: string; text: string }
  | { t: 'h'; level: number; text: string }
  | { t: 'p'; text: string }
  | { t: 'hr' }
  | { t: 'quote'; blocks: MdBlock[] }
  | { t: 'list'; ordered: boolean; start: number; items: MdBlock[][] }
  | { t: 'table'; head: string[]; align: ('left' | 'center' | 'right' | null)[]; rows: string[][] };

const FENCE = /^ {0,3}(`{3,}|~{3,})\s*([^`\s]*)[^`]*$/, HEAD = /^ {0,3}(#{1,6})\s+(.*?)\s*#*\s*$/, HR = /^ {0,3}([-*_])(\s*\1){2,}\s*$/;
const ITEM = /^( {0,3})([-*+]|(\d{1,9})[.)])(\s+|$)(.*)$/, QUOTE = /^ {0,3}> ?(.*)$/, SEP = /^\s*\|?\s*:?-+:?\s*(\|\s*:?-+:?\s*)*\|?\s*$/;
const cells = (l: string) => l.trim().replace(/^\|/, '').replace(/\|$/, '').split(/(?<!\\)\|/).map(c => c.trim().replace(/\\\|/g, '|'));

/** Block structure of a markdown text. */
// Nesting past this depth (quotes in quotes, lists in lists) stays paragraph text: agent output is untrusted, so a deeply nested
// reply must not exhaust the stack.
const MAX_DEPTH = 16;
export function parse(src: string, depth = 0): MdBlock[] {
  if (depth >= MAX_DEPTH) return src.trim() ? [{ t: 'p', text: src }] : [];
  const lines = src.replace(/\r\n?/g, '\n').split('\n'), out: MdBlock[] = [];
  let i = 0;
  const para: string[] = [];
  const flush = () => { if (para.length) { out.push({ t: 'p', text: para.join('\n') }); para.length = 0; } };
  while (i < lines.length) {
    const l = lines[i];
    let m: RegExpExecArray | null;
    if ((m = FENCE.exec(l))) {
      flush(); const end = new RegExp(`^ {0,3}\\${m[1][0]}{${m[1].length},}\\s*$`), body: string[] = []; i++;
      while (i < lines.length && !end.test(lines[i])) body.push(lines[i++]);
      i++; out.push({ t: 'code', lang: m[2], text: body.join('\n') }); continue;
    }
    if (!l.trim()) { flush(); i++; continue; }
    if ((m = HEAD.exec(l))) { flush(); out.push({ t: 'h', level: m[1].length, text: m[2] }); i++; continue; }
    if (HR.test(l) && !para.length) { flush(); out.push({ t: 'hr' }); i++; continue; }
    if (QUOTE.test(l)) {
      flush(); const body: string[] = [];
      while (i < lines.length && lines[i].trim() && (m = QUOTE.exec(lines[i]))) { body.push(m[1]); i++; }
      out.push({ t: 'quote', blocks: parse(body.join('\n'), depth + 1) }); continue;
    }
    if (l.includes('|') && i + 1 < lines.length && SEP.test(lines[i + 1]) && lines[i + 1].includes('-')) {
      flush(); const head = cells(l), align = cells(lines[i + 1]).map(c => c.startsWith(':') && c.endsWith(':') ? 'center' as const : c.endsWith(':') ? 'right' as const : c.startsWith(':') ? 'left' as const : null);
      const rows: string[][] = []; i += 2;
      while (i < lines.length && lines[i].trim() && lines[i].includes('|')) rows.push(cells(lines[i++]));
      out.push({ t: 'table', head, align, rows }); continue;
    }
    if ((m = ITEM.exec(l)) && (!para.length || m[2] === '-' || m[2] === '*' || m[2] === '+' || m[3] === '1')) {
      flush(); const ordered = m[3] !== undefined, indent = m[1].length, items: MdBlock[][] = [];
      let cur: string[] | null = null;
      const close = () => { if (cur) items.push(parse(cur.join('\n'), depth + 1)); };
      while (i < lines.length) {
        const x = lines[i], im = ITEM.exec(x);
        if (im && im[1].length <= indent + 1 && (im[3] !== undefined) === ordered) { close(); cur = [im[5]]; i++; continue; }
        if (!x.trim()) { if (i + 1 < lines.length && /^\s{2,}\S/.test(lines[i + 1]) && cur) { cur.push(''); i++; continue; } break; }
        if (/^\s{2,}/.test(x) && cur) { cur.push(x.replace(/^\s{2,4}/, '')); i++; continue; }
        if (cur && !im && !HEAD.test(x) && !FENCE.test(x) && !QUOTE.test(x)) { cur.push(x.trim()); i++; continue; }
        break;
      }
      close(); out.push({ t: 'list', ordered, start: ordered ? Number(m[3]) : 1, items }); continue;
    }
    para.push(l); i++;
  }
  flush();
  return out;
}

export const SAFE_LINK = /^(https?:|mailto:)/i;
