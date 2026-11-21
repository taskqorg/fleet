// Semantic data tables (OR-25, modern-web-guidance responsive-table & sortable-table), tabs, and disclosures.
import type { ComponentChildren } from 'preact';
import { Icon } from './primitives.tsx';

/** Scrollable semantic table region; wide tables scroll sideways inside their own frame (OR-25). */
export function DataTable({ label, head, children, cls }: {
  label: string;
  head: ComponentChildren;
  children: ComponentChildren;
  cls?: string;
}) {
  return (
    <div class="table-frame" data-ui="data-table" tabIndex={0} role="region" aria-label={label}>
      <table class={cls ? `limit-table ${cls}` : 'limit-table'}>
        <thead>{head}</thead>
        <tbody>{children}</tbody>
      </table>
    </div>
  );
}

/** Sortable `<th scope="col">` header cell following modern-web-guidance sortable-table. */
export function SortColHead({ label, active, dir, numeric, onSort }: {
  label: string;
  active: boolean;
  dir: 'ascending' | 'descending';
  numeric?: boolean;
  onSort: () => void;
}) {
  return (
    <th scope="col" class={numeric ? 'num' : undefined} aria-sort={active ? dir : undefined}>
      <button type="button" class="sort-btn" onClick={onSort}>
        <span>{label}</span>
        {active && <Icon name={dir === 'ascending' ? 'arrow-up-narrow-wide' : 'arrow-down-wide-narrow'} />}
      </button>
    </th>
  );
}

/** Accessible single-row tablist following modern-web-guidance tabs (selection follows click/focus, arrow-key navigation). */
export function TabBar<T extends string>({ label, tabs, value, onChange, cls }: {
  label: string;
  tabs: readonly { id: T; label: string; count?: number }[];
  value: T;
  onChange: (id: T) => void;
  cls?: string;
}) {
  const move = (e: KeyboardEvent) => {
    const i = tabs.findIndex(t => t.id === value);
    if (i < 0) return;
    let next = -1;
    if (e.key === 'ArrowRight') next = (i + 1) % tabs.length;
    else if (e.key === 'ArrowLeft') next = (i - 1 + tabs.length) % tabs.length;
    else if (e.key === 'Home') next = 0;
    else if (e.key === 'End') next = tabs.length - 1;
    if (next < 0) return;
    e.preventDefault();
    const list = e.currentTarget as HTMLElement;
    const btn = list.querySelectorAll<HTMLButtonElement>('[role="tab"]')[next];
    btn?.focus();
    btn?.scrollIntoView({ block: 'nearest', inline: 'nearest' });
    onChange(tabs[next].id);
  };
  return (
    <div class={cls ? `tabs ${cls}` : 'tabs'} data-ui="tabs" role="tablist" aria-label={label} onKeyDown={move}>
      {tabs.map(t => (
        <button key={t.id} type="button" role="tab" aria-selected={value === t.id}
          tabIndex={value === t.id ? 0 : -1} onClick={() => onChange(t.id)}>
          <span>{t.label}</span>
          {t.count !== undefined && <span class="tab-n mono">{t.count}</span>}
        </button>
      ))}
    </div>
  );
}

/** Native disclosure widget (`details`/`summary`) for secondary or advanced content. */
export function Disclosure({ summary, summaryClass, open, children }: {
  summary: ComponentChildren;
  summaryClass?: string;
  open?: boolean;
  children: ComponentChildren;
}) {
  return (
    <details class="disclosure" data-ui="disclosure" open={open}>
      <summary class={summaryClass}>{summary}</summary>
      {children}
    </details>
  );
}
