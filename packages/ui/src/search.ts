// The list search syntax in the browser (OR-36), shared by Spotlight providers and lists filtered on the client.
/** The field terms of the list search syntax (go/coordinator/search.go searchFields); keep the two lists equal. */
export const searchFields = ['agent', 'host', 'state', 'project'] as const;
type SearchFields = Partial<Record<(typeof searchFields)[number], string | null>>;
/**
 * The list search syntax for lists filtered in the browser, the same as the server's (OR-36): every word must be in text;
 * agent:, host:, project: must be in that field, state: equal to it; a term for a field the item lacks matches nothing.
 */
export function matchSearch(query: string, text: string, fields: SearchFields = {}): boolean {
  // Like the server's parseSearchQuery: a repeated field term keeps its last value.
  const t = text.toLowerCase(), terms = new Map<string, string>(), words: string[] = [];
  for (const w of query.toLowerCase().split(/\s+/).filter(Boolean)) {
    const [k, ...rest] = w.split(':'), v = rest.join(':');
    if (v && (searchFields as readonly string[]).includes(k)) terms.set(k, v); else words.push(w);
  }
  if (!words.every(w => t.includes(w))) return false;
  for (const [k, v] of terms) {
    const f = fields[k as keyof SearchFields]?.toLowerCase();
    if (f == null || (k === 'state' ? f !== v : !f.includes(v))) return false;
  }
  return true;
}
