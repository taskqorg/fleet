// Turns tokens.json into CSS custom properties (with light-dark()) and checks contrast pairs (UR-42, WCAG 2.1 AA): text 4.5:1,
// UI parts (focus ring, status and chart marks) 3:1.
import { readFileSync } from 'node:fs';
export const tokens = JSON.parse(readFileSync(new URL('../tokens.json', import.meta.url)));

const vars = (o, p) => Object.entries(o).map(([k, v]) => `--${p ? p + '-' : ''}${k}:${v}`).join(';');
const colorVars = (light, dark) => Object.entries(light).map(([k, v]) => `--${k}:light-dark(${v},${dark[k] ?? v})`).join(';');
const STYLE_FILES = ['base.css', 'layout.css', 'controls.css', 'thread.css', 'tables.css', 'live.css', 'sections.css', 'overlays.css', 'home.css', 'session.css', 'lab.css'];

export function tokensCss(t = tokens) {
  const base = ['font', 'space', 'size', 'radius', 'elevation', 'motion'].map(g => vars(t[g], g)).join(';');
  return `@layer tokens{
:root{color-scheme:light dark;${base};${colorVars(t.color.light, t.color.dark)}}
:root[data-theme=light],.th-light{color-scheme:light}
:root[data-theme=dark],.th-dark{color-scheme:dark}
}`;
}

export function uiCss(extraCss = '') {
  const layers = '@layer tokens,base,layout,controls,overlays,utilities;\n';
  const parts = STYLE_FILES.map(f => readFileSync(new URL(`../src/styles/${f}`, import.meta.url), 'utf8')).join('\n');
  return layers + tokensCss() + '\n' + parts + (extraCss ? '\n' + extraCss : '');
}

const lum = h => { const c = [1, 3, 5].map(i => parseInt(h.slice(i, i + 2), 16) / 255).map(v => v <= .03928 ? v / 12.92 : ((v + .055) / 1.055) ** 2.4); return .2126 * c[0] + .7152 * c[1] + .0722 * c[2]; };
const ratio = (a, b) => { const [x, y] = [lum(a), lum(b)].sort((m, n) => n - m); return (x + .05) / (y + .05); };
/** The identity colours of projects and bots (OR-72, contracts/vocabulary.json identity): tokens id-<name>. */
export const IDENTITY = JSON.parse(readFileSync(new URL('../../../contracts/vocabulary.json', import.meta.url))).identity.colors;
// Text-on-background pairs the controls use.
export const PAIRS = [['fg', 'surface'], ['fg-muted', 'surface'], ['fg-muted', 'bg'], ['fg-muted', 'hover'], ['fg-muted', 'neutral-bg'], ['fg', 'selected'], ['accent', 'surface'], ['accent', 'bg'], ['on-accent', 'accent'],
  ['attention-fg', 'attention-bg'], ['attention-fg', 'attention-soft'], ['fg', 'attention-soft'], ['fg-muted', 'attention-soft'], ['danger-fg', 'danger-bg'], ['fg-muted', 'danger-bg'], ['fg', 'danger-bg'], ['danger-fg', 'surface'], ['danger-fg', 'attention-soft'], ['running-fg', 'running-bg'],
  ['success-fg', 'success-bg'], ['success-fg', 'surface'], ['neutral-fg', 'neutral-bg'], ['estimated-fg', 'surface'], ['fg', 'float'],
  ['data-1-ink', 'data-1'], ['data-2-ink', 'data-2'], ['data-3-ink', 'data-3'], ['data-4-ink', 'data-4'],
  // Identity colours (project tile code, bot letter): text on the page and on surfaces.
  ...IDENTITY.flatMap(c => [[`id-${c}`, 'bg'], [`id-${c}`, 'surface']])];
// UI parts on the surfaces they sit on (WCAG 1.4.11): the focus ring, status tones of icons and outlines, chart series.
export const UI_PAIRS = [['accent', 'surface'], ['accent', 'bg'], ['accent', 'float'], ['attention-fg', 'surface'], ['danger-fg', 'surface'], ['running-fg', 'surface'], ['success-fg', 'surface'],
  ['data-1', 'surface'], ['data-2', 'surface'], ['data-3', 'surface'], ['data-4', 'surface']];
export function contrastProblems(t = tokens) {
  const out = [];
  for (const [pairs, need, what] of [[PAIRS, 4.5, 'text'], [UI_PAIRS, 3, 'UI part']]) for (const th of ['light', 'dark']) for (const [f, b] of pairs) {
    const r = ratio(t.color[th][f], t.color[th][b]);
    if (r < need) out.push(`${th}: ${what} ${f} on ${b} = ${r.toFixed(2)}:1 (needs ${need}:1)`);
  }
  return out;
}
