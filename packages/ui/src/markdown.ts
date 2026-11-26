// Markdown for agent replies (docs/designs/fleet-conversations.md, "Copy"; security NFR: no raw HTML).
// This file parses; markdown.tsx renders. Text fragments retain their source positions through block and inline parsing.
export type MdBlock<T = string> =
  | { t: 'code'; lang: string; text: T }
  | { t: 'h'; level: number; text: T }
  | { t: 'p'; text: T }
  | { t: 'hr' }
  | { t: 'quote'; blocks: MdBlock<T>[] }
  | { t: 'list'; ordered: boolean; start: number; items: MdBlock<T>[][] }
  | { t: 'table'; head: T[]; align: ('left' | 'center' | 'right' | null)[]; rows: T[][] };

/** A pull request named in text: its GitHub URL, or owner/repo#number (entity-card.md: agent messages). */
export const PR_REF = /\bhttps:\/\/github\.com\/([\w.-]+)\/([\w.-]+)\/pull\/([1-9][0-9]*)(?![\w/])|(?<![\w/.-])([\w.-]+)\/([\w.-]+)#([1-9][0-9]*)\b/g;
/** A link to a Fleet page of one entity (…/conversations/<uuid>, …/tasks/T-FLT-79): its reference, never the raw URL (OR-38). Group 1 a UUID, group 2 a code. */
export const APP_URL_REF = /\bhttps?:\/\/[^\s/]+\/(?:home|live|conversations|tasks|results|delivery|projects|lab|library|system|history)\/(?:[\w.-]+\/)*?(?:([0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12})|([TQCR]-[A-Z][A-Z0-9]{1,4}-[1-9][0-9]*))(?![\w/-])/g;
/** A UUID in agent text: a reference whose kind the server finds (OR-38: a UUID is never shown). */
export const UUID_REF = /\b[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}\b/g;

type Fragment = { text: string; at: number };
export type MdText = { text: string; fragments: Fragment[] };
const join = (fragments: Fragment[]): MdText => ({ text: fragments.map(f => f.text).join(''), fragments });

/** Slice visible text without losing its UTF-16 positions in the original Markdown. */
export function sliceText(source: MdText, from: number, to = source.text.length): MdText {
  let offset = 0;
  const fragments: Fragment[] = [];
  for (const f of source.fragments) {
    const start = Math.max(0, from - offset), end = Math.min(f.text.length, to - offset);
    if (start < end) fragments.push({ text: f.text.slice(start, end), at: f.at + start });
    offset += f.text.length;
    if (offset >= to) break;
  }
  return { text: source.text.slice(from, to), fragments };
}

type Line = Fragment & { newlineAt: number };
const content = (line: Line, start: number, length?: number): Line => ({
  ...line, at: line.at + start, text: line.text.slice(start, length === undefined ? undefined : start + length),
});
const trimLine = (line: Line) => content(line, line.text.length - line.text.trimStart().length, line.text.trim().length);
function fromLines(lines: Line[]): MdText {
  return join(lines.flatMap((line, i) => i + 1 < lines.length
    ? [line, { text: '\n', at: line.newlineAt }]
    : [line]));
}

const FENCE = /^ {0,3}(`{3,}|~{3,})\s*([^`\s]*)[^`]*$/, HEAD = /^ {0,3}(#{1,6})\s+(.*?)\s*#*\s*$/d, HR = /^ {0,3}([-*_])(\s*\1){2,}\s*$/;
const ITEM = /^( {0,3})([-*+]|(\d{1,9})[.)])(\s+|$)(.*)$/d, QUOTE = /^ {0,3}> ?(.*)$/d, SEP = /^\s*\|?\s*:?-+:?\s*(\|\s*:?-+:?\s*)*\|?\s*$/;
function cells(line: Line): MdText[] {
  line = trimLine(line);
  if (line.text.startsWith('|')) line = content(line, 1);
  if (line.text.endsWith('|')) line = content(line, 0, line.text.length - 1);
  let at = 0;
  return line.text.split(/(?<!\\)\|/).map(raw => {
    const cell = trimLine(content(line, at, raw.length)), fragments: Fragment[] = [];
    at += raw.length + 1;
    let start = 0;
    for (const m of cell.text.matchAll(/\\\|/g)) {
      fragments.push(content(cell, start, m.index - start));
      start = m.index + 1; // keep the pipe and skip its escape
    }
    fragments.push(content(cell, start));
    return join(fragments.filter(f => f.text));
  });
}

/** Block structure of a markdown text. Supply a projection for callers that need original source positions. */
export function parse(src: string): MdBlock[];
export function parse<T>(src: string, project: (text: MdText) => T): MdBlock<T>[];
export function parse<T>(src: string, project: (text: MdText) => T = s => s.text as T): MdBlock<T>[] {
  const parts = src.split(/(\r\n?|\n)/), lines: Line[] = [];
  for (let i = 0, at = 0; i < parts.length; i += 2) {
    const text = parts[i], newline = parts[i + 1] ?? '';
    lines.push({ text, at, newlineAt: at + text.length + Math.max(0, newline.length - 1) });
    at += text.length + newline.length;
  }
  return parseLines(lines, project, 0);
}

// A deeply nested untrusted reply must not exhaust the stack.
function parseLines<T>(lines: Line[], project: (text: MdText) => T, depth: number): MdBlock<T>[] {
  if (depth >= 16) {
    const text = fromLines(lines);
    return text.text.trim() ? [{ t: 'p', text: project(text) }] : [];
  }
  const out: MdBlock<T>[] = [], para: Line[] = [];
  let i = 0;
  const flush = () => { if (para.length) { out.push({ t: 'p', text: project(fromLines(para)) }); para.length = 0; } };
  while (i < lines.length) {
    const line = lines[i], l = line.text;
    let m: RegExpExecArray | null;
    if ((m = FENCE.exec(l))) {
      flush(); const end = new RegExp(`^ {0,3}\\${m[1][0]}{${m[1].length},}\\s*$`), body: Line[] = []; i++;
      while (i < lines.length && !end.test(lines[i].text)) body.push(lines[i++]);
      i++; out.push({ t: 'code', lang: m[2], text: project(fromLines(body)) }); continue;
    }
    if (!l.trim()) { flush(); i++; continue; }
    if ((m = HEAD.exec(l))) {
      flush(); out.push({ t: 'h', level: m[1].length, text: project(fromLines([content(line, m.indices![2]![0], m[2].length)])) }); i++; continue;
    }
    if (HR.test(l) && !para.length) { flush(); out.push({ t: 'hr' }); i++; continue; }
    if (QUOTE.test(l)) {
      flush(); const body: Line[] = [];
      while (i < lines.length && lines[i].text.trim() && (m = QUOTE.exec(lines[i].text))) { body.push(content(lines[i], m.indices![1]![0])); i++; }
      out.push({ t: 'quote', blocks: parseLines(body, project, depth + 1) }); continue;
    }
    const next = lines[i + 1];
    if (l.includes('|') && next && SEP.test(next.text) && next.text.includes('-')) {
      flush(); const head = cells(line).map(project);
      const align = cells(next).map(({ text }) => text.startsWith(':') && text.endsWith(':') ? 'center' as const : text.endsWith(':') ? 'right' as const : text.startsWith(':') ? 'left' as const : null);
      const rows: T[][] = []; i += 2;
      while (i < lines.length && lines[i].text.trim() && lines[i].text.includes('|')) rows.push(cells(lines[i++]).map(project));
      out.push({ t: 'table', head, align, rows }); continue;
    }
    if ((m = ITEM.exec(l)) && (!para.length || m[2] === '-' || m[2] === '*' || m[2] === '+' || m[3] === '1')) {
      flush(); const ordered = m[3] !== undefined, indent = m[1].length, items: MdBlock<T>[][] = [];
      let cur: Line[] | null = null;
      const close = () => { if (cur) items.push(parseLines(cur, project, depth + 1)); };
      while (i < lines.length) {
        const entry = lines[i], x = entry.text, im = ITEM.exec(x);
        if (im && im[1].length <= indent + 1 && (im[3] !== undefined) === ordered) { close(); cur = [content(entry, im.indices![5]![0])]; i++; continue; }
        if (!x.trim()) { if (i + 1 < lines.length && /^\s{2,}\S/.test(lines[i + 1].text) && cur) { cur.push(content(entry, 0, 0)); i++; continue; } break; }
        const padding = /^\s{2,4}/.exec(x);
        if (padding && cur) { cur.push(content(entry, padding[0].length)); i++; continue; }
        if (cur && !im && !HEAD.test(x) && !FENCE.test(x) && !QUOTE.test(x)) { cur.push(trimLine(entry)); i++; continue; }
        break;
      }
      close(); out.push({ t: 'list', ordered, start: ordered ? Number(m[3]) : 1, items }); continue;
    }
    para.push(line); i++;
  }
  flush();
  return out;
}

export const SAFE_LINK = /^(https?:|mailto:)/i;
