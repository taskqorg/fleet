// Semantic data tables (OR-25, modern-web-guidance responsive-table) and disclosures.
import type { ComponentChildren } from 'preact';

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
