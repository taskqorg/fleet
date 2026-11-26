// Run with files on a pipeline page (app/src/views/pipeline-run.tsx): the input paths of the chosen files and the
// records of a result, kept apart from the view so they are checked without a browser.

export type Rec = Record<string, unknown>;

/** Input paths from file names: a name the server would refuse (a folder, `..`, `:` or `\`) is folded to a plain name, and
 * a repeated name gets `-2`, `-3` before its extension, so every file reaches the method. */
export function inputPaths(names: string[]): string[] {
  const seen = new Set<string>();
  return names.map((raw, i) => {
    const base = raw.replace(/[\\:\u0000]/g, '-').split('/').filter(s => s && s !== '.' && s !== '..').join('-') || `file-${i + 1}`;
    const dot = base.lastIndexOf('.');
    const [stem, ext] = dot > 0 ? [base.slice(0, dot), base.slice(dot)] : [base, ''];
    let name = base;
    for (let n = 2; seen.has(name); n++) name = `${stem}-${n}${ext}`;
    seen.add(name);
    return name;
  });
}

/** The records of a result: the first top-level list of objects (documents, items); null when it has none. */
export function resultRecords(out: unknown): Rec[] | null {
  if (!out || typeof out !== 'object' || Array.isArray(out)) return null;
  for (const v of Object.values(out as Rec)) {
    if (Array.isArray(v) && v.length > 0 && v.every(x => x && typeof x === 'object' && !Array.isArray(x))) return v as Rec[];
  }
  return null;
}

/** Keys that name a record, the first found leads the row (the row header): a result's keys come in any order, often sorted. */
const NAME_KEYS = ['file', 'path', 'name', 'title', 'id'];

/** The columns of records: keys holding plain values, in first-seen order, without a column that is empty in every
 * record; the key that names a record (NAME_KEYS) comes first, so the row header is the file, never a value such as a date. */
export function recordColumns(rows: Rec[]): string[] {
  const keys: string[] = [];
  for (const r of rows) for (const [k, v] of Object.entries(r)) if (!keys.includes(k) && (v == null || typeof v !== 'object')) keys.push(k);
  const filled = keys.filter(k => rows.some(r => r[k] != null && r[k] !== ''));
  const lead = NAME_KEYS.find(k => filled.includes(k));
  return lead ? [lead, ...filled.filter(k => k !== lead)] : filled;
}

/** A step's error as a person reads it: its message (a `{code, message}` object shows the message, not its JSON). */
export function stepErrorText(e: unknown): string | undefined {
  if (e == null || e === '') return undefined;
  if (typeof e === 'string') return e;
  const o = e as { message?: unknown; code?: unknown };
  return typeof o.message === 'string' && o.message ? o.message : typeof o.code === 'string' && o.code ? o.code : JSON.stringify(e);
}
