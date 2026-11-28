import { createEffect } from 'solid-js';
import { Portal } from '@solidjs/web';
import Draft from './draft.tsx';

async function settle(frames = 2) {
  for (let frame = 0; frame < frames; frame++) {
    await new Promise<void>(resolve => requestAnimationFrame(() => resolve()));
  }
}

export default function QuestionImageScene(props: { path: string }) {
  let root!: HTMLDivElement;

  const control = (label: string) => {
    const button = [...root.querySelectorAll('button')].find(el =>
      el.getAttribute('aria-label') === label || el.textContent?.trim() === label,
    );
    if (!button) throw Error('Preview control is missing: ' + label);
    return button;
  };

  createEffect(() => props.path, path => {
    void (async () => {
    // Keep the draft's illustration and its own workspace. The adapter uses the
    // existing controls to set the state for the Fleet capture.
    await settle();
    if (matchMedia('(prefers-color-scheme: light)').matches) control('Light').click();
    if (path.includes('/task-')) control('Task details').click();
    await settle();
    const scroller = root.querySelector<HTMLElement>('.question-scroll')!;
    scroller.scrollTop = scroller.scrollHeight;
    const opener = control('Open image');
    opener.focus({ preventScroll: true });
    opener.click();
    await settle(4);

    if (path.endsWith('-fit') || path.endsWith('-zoom')) {
      control('Zoom controls').click();
      await settle();
      if (path.endsWith('-fit')) {
        control('Fit').click();
      } else {
        control('Actual size').click();
        await settle();
        control('Zoom in').click();
      }
    }
    if (path.endsWith('-returned')) control('Back to question').click();

    await settle(4);
    root.dataset.ready = '';
    })();
  });

  return (
    <Portal>
      <div class="question-image-scene" ref={root}>
        <Draft />
      </div>
    </Portal>
  );
}
