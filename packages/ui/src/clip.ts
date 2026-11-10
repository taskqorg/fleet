// Copying conversation messages (docs/design-system/formats.md "Copy"). One message or a selection goes to the clipboard twice:
// text/plain as light markdown, text/html with only the tags chat apps keep (b, i, code, pre, a, br), so a paste into Telegram,
// a doc or an editor keeps who wrote each message, when, and its code blocks.
import { agentLabel, stamp } from './format.ts';

/** Who wrote a message: the person (email local part), or the agent as harness, model and effort. Same as the coordinator's whoOf. */
export function author(b: { role?: string; by?: string | null; harness?: string | null; model?: string | null; effort?: string | null }): string {
  if (b.role === 'operator') return b.by ? b.by.split('@')[0] : 'You';
  return [b.harness, agentLabel(b.model).replace(/ · /g, ' '), b.effort].filter(Boolean).join(' ') || 'Agent';
}

/** The plain text of one message: a bold header line "who · 11 Nov 14:03", then the body unchanged. */
export const clipMessage = (who: string, at: number | null | undefined, now: number, markdown: string) =>
  `**${who}${at ? ' · ' + stamp(at, now) : ''}**\n${markdown}\n`;

const esc = (s: string) => s.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;');
const inline = (s: string) => esc(s)
  .replace(/`([^`\n]+)`/g, '<code>$1</code>')
  .replace(/\*\*([^*\n]+)\*\*/g, '<b>$1</b>')
  .replace(/(^|[^*\w])[*_]([^*_\n]+)[*_](?=[^*\w]|$)/g, '$1<i>$2</i>')
  .replace(/\[([^\]\n]+)\]\((https?:\/\/[^)\s]+)\)/g, '<a href="$2">$1</a>');

/** Light markdown to the HTML chat apps keep. Fenced code becomes <pre>; everything else is escaped, then b, i, code and links. */
export function clipHtml(markdown: string): string {
  const out: string[] = [];
  const parts = markdown.split(/^(`{3,})[^\n]*\n([\s\S]*?)\n\1[ \t]*$/m);
  // split with two groups: text, fence, code, text, fence, code, ...
  for (let i = 0; i < parts.length; i += 3) {
    // A <pre> is its own block: the line breaks right around it would show as empty lines.
    const t = parts[i].replace(i > 0 ? /^\n/ : /^$/, '').replace(i + 2 < parts.length ? /\n$/ : /^$/, '');
    if (t) out.push(t.split('\n').map(inline).join('<br>'));
    if (i + 2 < parts.length) out.push(`<pre>${esc(parts[i + 2])}</pre>`);
  }
  return out.join('').replace(/^(<br>)+|(<br>)+$/g, '');
}

/** Writes both formats. `text` may be a promise, so the write starts inside the user's gesture while an export loads. */
export function writeClip(text: string | Promise<string>): Promise<void> {
  const t = Promise.resolve(text);
  return navigator.clipboard.write([new ClipboardItem({
    'text/plain': t.then(s => new Blob([s], { type: 'text/plain' })),
    'text/html': t.then(s => new Blob([clipHtml(s)], { type: 'text/html' })),
  })]);
}
