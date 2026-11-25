// Cursor pages of one operation (OR-65): the first page is an ordinary live read (useOp rereads it on change rows, so new
// rows arrive at the top); older pages are read once each through createPages, one in flight, aborted on unmount.
import { op, type Ops } from './api.ts';
import { useOp, canonical, type OpOptions, type OpRead } from './store.ts';
import { createPages, type Page, type Pages } from '../../packages/ui/src/index.ts';
import type { OpName } from './api.ts';

type Req<K extends OpName> = Ops[K]['request'];
type Rep<K extends OpName> = Ops[K]['reply'];

export function useOpPages<K extends OpName, R, C>(name: K, request: () => Req<K> | null, o: {
  page: (reply: Rep<K>) => Page<R, C>;
  /** The request fields that ask for the page after a cursor. */
  after: (cursor: C) => Partial<Req<K>>;
  key: (row: R) => string;
  opts?: OpOptions;
}): { pages: Pages<R>; first: OpRead<Rep<K>> } {
  const first = useOp(name, request, o.opts);
  const pages = createPages<R, C>({
    first: () => (first.data ? o.page(first.data) : null),
    fetch: async (cursor, signal) => o.page(await op(name, { ...request()!, ...o.after(cursor) } as Req<K>, { signal })),
    key: o.key,
    reset: () => { const r = request(); return r === null ? null : canonical(r); },
  });
  return { pages, first };
}
