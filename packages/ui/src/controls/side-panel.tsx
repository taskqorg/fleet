// Side panel (controls/side-panel.md, OR-41, OR-42): the one implementation of every column that opens and closes beside the
// page, the navigation rail on the left and the session's context panel on the right. Its toggle sits in the shell header at
// a fixed place, its width is resized by a divider, and open or closed and the width are person settings by panel key.
import { createContext, createEffect, createMemo, createSignal, onSettled, Show, useContext, type Accessor } from 'solid-js';
import { Portal, type JSX } from '@solidjs/web';
import { Button } from './primitives.tsx';

/**
 * Open state and widths of the side panels by key: person settings on the server in the app, own memory in the book. The
 * shell (AppShell) provides it; `slot` is the shell header's place for right-panel toggles, after search (OR-43).
 */
export type SidePanelStore = {
  isOpen: (key: string, byDefault: boolean) => boolean;
  setOpen: (key: string, open: boolean) => void;
  width: (key: string) => number | undefined;
  /** px; 0 resets to the default width. */ setWidth: (key: string, px: number) => void;
  slot: Accessor<HTMLElement | null>;
};

/** A store in memory, for a shell without person settings (book, mockups, tests). */
export function ownPanelStore(slot: Accessor<HTMLElement | null> = () => null): SidePanelStore {
  const [open, setOpenMap] = createSignal<Record<string, boolean>>({});
  const [widths, setWidths] = createSignal<Record<string, number>>({});
  return {
    isOpen: (k, d) => open()[k] ?? d,
    setOpen: (k, on) => setOpenMap(m => ({ ...m, [k]: on })),
    width: k => widths()[k],
    setWidth: (k, px) => setWidths(w => { const n = { ...w }; if (px) n[k] = px; else delete n[k]; return n; }),
    slot,
  };
}

export const SidePanels = createContext<SidePanelStore | undefined>(undefined);
/** The shell's side-panel store; outside a shell, one in memory. */
export function useSidePanels(): SidePanelStore {
  return useContext(SidePanels) ?? ownPanelStore();
}

/** A stored width inside its limits: a value an agent or another device wrote never breaks the layout. */
export const clampWidth = (px: number, lo: number, hi: number) => Math.round(Math.max(lo, Math.min(hi, px)));

/**
 * The toggle of a side panel: an icon button whose glyph shows the side and what a click does (panel-left-close while the
 * left panel is open, panel-right-open while the right one is closed). Its place is fixed by the shell header, so it never
 * moves when the panel opens or closes (OR-42).
 */
export function PanelToggle(p: { side: 'left' | 'right'; open: boolean; label: string; onToggle: () => void; class?: string }) {
  return (
    <span class={['panel-toggle', p.class]} data-ui="panel-toggle" data-side={p.side}>
      <Button label={p.label} icon={`panel-${p.side}-${p.open ? 'close' : 'open'}`} iconOnly onClick={() => p.onToggle()} />
    </span>
  );
}

/**
 * A resize divider on a panel's inner edge (OR-41): drag or the arrow keys resize the panel live, the end of a drag or the
 * key's release saves the width once, a double-click resets it. `side` is where the panel is: a right panel grows to the left.
 */
export function ResizeDivider(p: {
  label: string; setting: string; side: 'left' | 'right'; lo: number; hi: number; class?: string;
  /** The CSS variable that holds the width, set live on `host` while dragging. */ prop: string;
  host: (el: HTMLElement) => HTMLElement | null | undefined;
  /** The element whose width is resized. */ panel: (el: HTMLElement) => HTMLElement;
  /** An upper limit from the page (what CSS can show), below `hi`. */ cap?: (el: HTMLElement) => number;
}) {
  const store = useSidePanels();
  const sign = () => (p.side === 'left' ? 1 : -1);
  const clamp = (el: HTMLElement, w: number) => clampWidth(w, p.lo, Math.min(p.hi, p.cap?.(el) ?? p.hi));
  const width = (el: HTMLElement) => p.panel(el).getBoundingClientRect().width;
  const live = (el: HTMLElement, w: number) => p.host(el)?.style.setProperty(p.prop, w + 'px');
  return (
    <div
      class={['divider', `divider-${p.side}`, p.class]}
      data-ui="divider"
      role="separator"
      aria-orientation="vertical"
      aria-label={p.label}
      tabindex={0}
      onPointerDown={e => {
        if (e.button !== 0) return;
        const el = e.currentTarget, x0 = e.clientX, w0 = width(el), start = clamp(el, w0);
        let w = start;
        el.setPointerCapture(e.pointerId);
        e.preventDefault();
        // The drag moves the panel live; only its end is saved.
        el.onpointermove = m => { w = clamp(el, w0 + sign() * (m.clientX - x0)); live(el, w); };
        // A press without a move keeps the width; a cancelled drag keeps where it got to.
        el.onpointerup = el.onpointercancel = () => { el.onpointermove = el.onpointerup = el.onpointercancel = null; if (w !== start) store.setWidth(p.setting, w); };
      }}
      onDblClick={e => { p.host(e.currentTarget)?.style.removeProperty(p.prop); store.setWidth(p.setting, 0); }}
      // Arrow keys move it live (the arrow points where the divider goes); the release saves, so a held key writes once.
      onKeyDown={e => {
        if (e.key !== 'ArrowLeft' && e.key !== 'ArrowRight') return;
        e.preventDefault();
        const el = e.currentTarget;
        live(el, clamp(el, width(el) + sign() * (e.key === 'ArrowLeft' ? -16 : 16)));
      }}
      onKeyUp={e => {
        if (e.key !== 'ArrowLeft' && e.key !== 'ArrowRight') return;
        const el = e.currentTarget;
        store.setWidth(p.setting, clamp(el, width(el)));
      }}
    />
  );
}

/**
 * A right side panel beside the page (the session's context panel): a column at the stored width with a resize divider on
 * its left edge, open or closed by the person's setting `setting` (open by default), toggled from the shell header after search.
 * Where the page is too narrow for a column (CSS sets `--side-mode: sheet`, at phone width), the same toggle opens the panel
 * as a sheet from the bottom edge instead; the sheet is a passing view and leaves the stored state as it is.
 */
export function SidePanel(p: {
  /** The person-setting key (`session-context`): a lower-case slug. */ setting: string;
  /** What the panel is, for the toggle (`Show context`, `Hide context`) and the divider; `label` names the sheet. The
   *  contents name their own landmark. */ name: string; label: string;
  lo?: number; hi?: number; defaultOpen?: boolean;
  /** Before the column opens or closes: returns what to do once it has (the session keeps the row being read in place). */
  onLayout?: () => (() => void) | void;
  /** The panel's contents, in the column or the sheet; `close` closes the sheet. */
  children: (where: 'column' | 'sheet', close: () => void) => JSX.Element;
}) {
  const store = useSidePanels();
  const lo = () => p.lo ?? 200, hi = () => p.hi ?? 560;
  const open = createMemo(() => store.isOpen(p.setting, p.defaultOpen ?? true));
  const width = createMemo(() => { const w = store.width(p.setting); return w && w > 0 ? clampWidth(w, lo(), hi()) : undefined; });
  const [sheet, setSheet] = createSignal(false);
  // Column or sheet is CSS's choice (--side-mode on the column, also while it is hidden); it is read again when the page
  // around the panel changes size.
  const [mode, setMode] = createSignal<'column' | 'sheet'>('column', { ownedWrite: true });
  let column: HTMLDivElement | undefined;
  const readMode = () => { if (column) setMode(getComputedStyle(column).getPropertyValue('--side-mode').trim() === 'sheet' ? 'sheet' : 'column'); };
  onSettled(() => {
    readMode();
    const host = column?.parentElement;
    if (!host) return;
    const ro = new ResizeObserver(readMode);
    ro.observe(host);
    return () => ro.disconnect();
  });
  const toggle = () => {
    readMode();
    if (mode() === 'sheet') { setSheet(s => !s); return; }
    const after = p.onLayout?.();
    store.setOpen(p.setting, !open());
    if (after) requestAnimationFrame(() => requestAnimationFrame(after));
  };
  const shown = () => (mode() === 'sheet' ? sheet() : open());
  return (
    <>
      <Show when={store.slot()} keyed>
        {el => (
          <Portal mount={el}>
            <PanelToggle side="right" open={shown()} label={`${shown() ? 'Hide' : 'Show'} ${p.name}`} onToggle={toggle} />
          </Portal>
        )}
      </Show>
      <div
        ref={el => { column = el; }}
        class="side-panel"
        data-ui="side-panel"
        data-panel={p.setting}
        hidden={!open()}
        style={width() ? { '--size-side': width() + 'px' } : undefined}
      >
        <ResizeDivider
          label={`Resize ${p.name}`}
          setting={p.setting}
          side="right"
          lo={lo()}
          hi={hi()}
          prop="--size-side"
          host={el => el.parentElement}
          panel={el => el.parentElement!}
          cap={el => (el.parentElement?.parentElement?.getBoundingClientRect().width ?? Infinity) / 2}
        />
        <div class="side-body">{p.children('column', () => {})}</div>
      </div>
      <PanelSheet open={sheet()} label={p.label} onClose={() => setSheet(false)}>{p.children('sheet', () => setSheet(false))}</PanelSheet>
    </>
  );
}

/**
 * A side panel as a sheet at phone width (OR-60): a native modal <dialog> from the bottom edge. Escape, the Back gesture and a
 * tap outside close it; focus returns to the toggle that opened it.
 */
export function PanelSheet(p: { open: boolean; label: string; onClose: () => void; children: JSX.Element }) {
  let dialog: HTMLDialogElement | undefined;
  createEffect(() => p.open, open => {
    if (!dialog) return;
    if (open && !dialog.open) dialog.showModal();
    else if (!open && dialog.open) dialog.close();
  });
  return (
    <dialog ref={el => { dialog = el; }} class="side-sheet" data-ui="side-sheet" closedby="any" aria-label={p.label} onClose={() => p.onClose()}>
      {p.children}
    </dialog>
  );
}
