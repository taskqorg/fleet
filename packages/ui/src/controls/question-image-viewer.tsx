// Full-screen image content shared by question surfaces and the design book.
import { createSignal, onCleanup, Show } from 'solid-js';
import { Button } from './primitives.tsx';

export function QuestionImageViewer(p: { src: string; title: string; onClose: () => void }) {
  const [failed, setFailed] = createSignal(false);
  const [controls, setControls] = createSignal(false);
  const [scale, setScale] = createSignal(1);
  const [fitWidth, setFitWidth] = createSignal(300);
  const [sourceWidth, setSourceWidth] = createSignal(0);
  const [offset, setOffset] = createSignal({ x: 0, y: 0 });
  let stage: HTMLDivElement | undefined;
  let picture: HTMLImageElement | undefined;
  let viewer: HTMLElement | undefined;
  let observer: ResizeObserver | undefined;
  let sizing: 'fill' | 'fit' | 'actual' | 'custom' = 'fill';
  const points = new Map<number, { x: number; y: number }>();
  let drag: { x: number; y: number; ox: number; oy: number } | null = null;
  let pinch: { distance: number; scale: number } | null = null;
  onCleanup(() => observer?.disconnect());

  // Stop at each edge; a small image remains centered instead of being lost offscreen.
  const pan = (x: number, y: number, atScale = scale(), fit = fitWidth()) => {
    if (!stage || !picture?.naturalWidth || !picture.naturalHeight) return;
    const width = fit * atScale;
    const height = width * picture.naturalHeight / picture.naturalWidth;
    const maxX = Math.max(0, (width - stage.clientWidth) / 2);
    const maxY = Math.max(0, (height - stage.clientHeight) / 2);
    setOffset({ x: Math.max(-maxX, Math.min(maxX, x)), y: Math.max(-maxY, Math.min(maxY, y)) });
  };
  const minimumScale = (fit = fitWidth(), width = sourceWidth()) => width ? Math.min(1, width / fit) : 1;
  const zoom = (value: number, mode: typeof sizing = 'custom') => {
    if (!Number.isFinite(value)) return;
    const next = Math.max(minimumScale(), value);
    sizing = mode;
    setScale(next);
    pan(offset().x, offset().y, next);
  };
  // Each button or key step adds a quarter of the fitted image width.
  const stepZoom = (direction: 1 | -1) => zoom(scale() + direction * 0.25);
  const measure = () => {
    if (!stage || !picture?.naturalWidth || !picture.naturalHeight) return;
    const ratio = picture.naturalWidth / picture.naturalHeight;
    const fit = Math.min(stage.clientWidth, stage.clientHeight * ratio);
    if (!fit) return;
    setSourceWidth(picture.naturalWidth);
    setFitWidth(fit);
    const next = sizing === 'fill' ? Math.max(stage.clientWidth, stage.clientHeight * ratio) / fit
      : sizing === 'actual' ? picture.naturalWidth / fit
      : Math.max(minimumScale(fit, picture.naturalWidth), scale());
    setScale(next);
    pan(offset().x, offset().y, next, fit);
  };
  const distance = () => {
    const a = [...points.values()];
    return Math.hypot(a[0].x - a[1].x, a[0].y - a[1].y);
  };
  const down = (e: PointerEvent) => {
    if (e.pointerType === 'mouse' && e.button !== 0) return;
    stage?.setPointerCapture(e.pointerId);
    points.set(e.pointerId, { x: e.clientX, y: e.clientY });
    if (points.size === 1) {
      const o = offset();
      drag = { x: e.clientX, y: e.clientY, ox: o.x, oy: o.y };
    } else if (points.size === 2) {
      pinch = { distance: distance(), scale: scale() };
      drag = null;
    }
  };
  const move = (e: PointerEvent) => {
    if (!points.has(e.pointerId)) return;
    points.set(e.pointerId, { x: e.clientX, y: e.clientY });
    if (points.size >= 2 && pinch && pinch.distance) zoom(pinch.scale * distance() / pinch.distance);
    else if (drag) pan(drag.ox + e.clientX - drag.x, drag.oy + e.clientY - drag.y);
  };
  const up = (e: PointerEvent) => { points.delete(e.pointerId); pinch = null; drag = null; };
  const changeControls = (open: boolean) => {
    setControls(open);
    requestAnimationFrame(() => viewer?.querySelector<HTMLButtonElement>(open ? 'button[aria-label="Zoom in"]' : 'button[aria-label="Zoom controls"]')?.focus({ preventScroll: true }));
  };
  const key = (e: KeyboardEvent) => {
    if (e.key === 'Tab') {
      const items = [...viewer?.querySelectorAll<HTMLElement>('button:not([disabled]):not([tabindex="-1"]), a[href], .qiv-stage') ?? []];
      const first = items[0], last = items.at(-1);
      if (e.shiftKey && e.target === first && last) { e.preventDefault(); last.focus(); }
      if (!e.shiftKey && e.target === last && first) { e.preventDefault(); first.focus(); }
    } else if (e.key === '+' || e.key === '=') { e.preventDefault(); stepZoom(1); }
    else if (e.key === '-') { e.preventDefault(); stepZoom(-1); }
    else if (e.key === '0') { e.preventDefault(); zoom(1, 'fit'); }
    else if (['ArrowLeft', 'ArrowRight', 'ArrowUp', 'ArrowDown'].includes(e.key)) {
      e.preventDefault();
      pan(offset().x + (e.key === 'ArrowLeft' ? 40 : e.key === 'ArrowRight' ? -40 : 0), offset().y + (e.key === 'ArrowUp' ? 40 : e.key === 'ArrowDown' ? -40 : 0));
    }
  };
  return <section class="qiv th-dark" ref={el => { viewer = el; }} onKeyDown={key}>
    <header class={['qiv-header', { compact: !controls() }]}>
      <Button label="Back to question" icon="arrow-left" iconOnly onClick={p.onClose} />
      <Show when={controls()}>
        <div class="qiv-title"><strong>{p.title}</strong></div>
        <div class="qiv-tools">
          <Button label="Zoom out" icon="minus" iconOnly onClick={() => stepZoom(-1)} disabledReason={scale() <= minimumScale() ? 'Minimum zoom' : undefined} />
          <span class="qiv-zoom" aria-live="polite">{sourceWidth() ? Math.round(fitWidth() * scale() * 100 / sourceWidth()) + '%' : ''}</span>
          <Button label="Zoom in" icon="plus" iconOnly onClick={() => stepZoom(1)} />
          <Button label="Fit" onClick={() => zoom(1, 'fit')} />
          <Button label="Actual size" onClick={() => zoom(sourceWidth() / fitWidth(), 'actual')} disabledReason={sourceWidth() ? '' : 'Image loading'} />
          <Button label="Hide controls" onClick={() => changeControls(false)} />
        </div>
      </Show>
      <Show when={!controls()}><Button label="Zoom controls" icon="maximize-2" iconOnly onClick={() => changeControls(true)} /></Show>
    </header>
    <div class="qiv-stage" tabindex="0" role="region" aria-label="Image. Drag to pan; plus and minus to zoom."
      ref={el => { stage = el; observer = new ResizeObserver(measure); observer.observe(el); }}
      onPointerDown={down} onPointerMove={move} onPointerUp={up} onPointerCancel={up}
      onWheel={e => { e.preventDefault(); zoom(scale() + (e.deltaY < 0 ? 0.15 : -0.15)); }}>
      <Show when={failed()}><div role="alert">Image could not be loaded. Return to the question to open the original.</div></Show>
      <img ref={el => { picture = el; }} onLoad={() => { setFailed(false); measure(); }} onError={() => setFailed(true)}
        draggable={false} src={p.src} alt={p.title} style={{ width: `${fitWidth() * scale()}px`, transform: `translate(${offset().x}px, ${offset().y}px)` }} />
    </div>
  </section>;
}
