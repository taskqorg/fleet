// Semantic data tables (OR-25, modern-web-guidance responsive-table & sortable-table), tabs, and disclosures.
import { For, Show } from 'solid-js';
import type { JSX } from '@solidjs/web';
import { Tabs, TabsList, TabsTab } from '@ui-sdk/solid';
import { Icon } from './primitives.tsx';

/** Scrollable semantic table region; wide tables scroll sideways inside their own frame (OR-25). */
export function DataTable(p: {
  label: string;
  head: JSX.Element;
  children: JSX.Element;
  cls?: string;
}) {
  return (
    <div class="table-frame" data-ui="data-table" tabindex={0} role="region" aria-label={p.label}>
      <table class={['limit-table', p.cls]}>
        <thead>{p.head}</thead>
        <tbody>{p.children}</tbody>
      </table>
    </div>
  );
}

/** Sortable `<th scope="col">` header cell following modern-web-guidance sortable-table. */
export function SortColHead(p: {
  label: string;
  active: boolean;
  dir: 'ascending' | 'descending';
  numeric?: boolean;
  onSort: () => void;
}) {
  return (
    <th scope="col" class={p.numeric ? 'num' : undefined} aria-sort={p.active ? p.dir : undefined}>
      <button type="button" class="sort-btn" onClick={() => p.onSort()}>
        <span>{p.label}</span>
        <Show when={p.active}>
          <Icon name={p.dir === 'ascending' ? 'arrow-up-narrow-wide' : 'arrow-down-wide-narrow'} />
        </Show>
      </button>
    </th>
  );
}

/** Accessible single-row tablist powered by @ui-sdk/solid Tabs (selection follows click/focus, arrow-key navigation). */
export function TabBar<T extends string>(p: {
  label: string;
  tabs: readonly { id: T; label: string; count?: number }[];
  value: T;
  onChange: (id: T) => void;
  cls?: string;
}) {
  return (
    <Tabs
      style={{ display: 'contents' }}
      value={p.value}
      loop={true}
      onValueChange={v => {
        if (v !== null) p.onChange(v as T);
      }}
    >
      <TabsList class={['tabs', p.cls]} data-ui="tabs" aria-label={p.label}>
        <For each={p.tabs}>
          {t => (
            <TabsTab value={t.id}>
              <span>{t.label}</span>
              <Show when={t.count !== undefined}><span class="tab-n mono">{t.count}</span></Show>
            </TabsTab>
          )}
        </For>
      </TabsList>
    </Tabs>
  );
}

/** Native disclosure widget (`details`/`summary`) for secondary or advanced content. */
export function Disclosure(p: {
  summary: JSX.Element;
  summaryClass?: string;
  open?: boolean;
  children: JSX.Element;
}) {
  return (
    <details class="disclosure" data-ui="disclosure" open={p.open}>
      <summary class={p.summaryClass}>{p.summary}</summary>
      {p.children}
    </details>
  );
}
