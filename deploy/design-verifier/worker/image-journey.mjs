// The operator supplies route and result data. Gestures and DOM observations ship with the verifier.
import { createHash } from 'node:crypto';

const flows = new Set(['image-open', 'image-fit', 'image-zoom', 'image-returned']);
const routes = {
  task: /^\/projects\/(?:[a-zA-Z0-9-]+\/)?tasks\/[a-zA-Z0-9-]+\/?(?:\?.*)?$/,
  conversation: /^\/conversations\/(?!new(?:[/?]|$))[a-zA-Z0-9-]+\/?(?:\?.*)?$/,
};
const fields = new Set(['flow', 'surface', 'start', 'target', 'recipe', 'sourceSha256',
  'responses', 'forbiddenOps', 'entry', 'resultTitle', 'resultCode', 'image', 'thumbnail']);
const mediaKey = value => typeof value === 'string' && /^[a-z0-9-]{1,60}$/.test(value);
export const isImageFlow = flow => flows.has(flow);

export function imageScenarioOK(s) {
  if (!isImageFlow(s.flow) || !Object.keys(s).every(key => fields.has(key)) ||
      !Object.hasOwn(routes, s.surface) || !routes[s.surface].test(s.start) || s.start !== s.target ||
      !['thumbnail', 'entity'].includes(s.entry) || typeof s.resultCode !== 'string' || !/^R-[A-Z0-9]+-[0-9]+$/.test(s.resultCode) ||
      typeof s.resultTitle !== 'string' || !s.resultTitle.trim() ||
      !mediaKey(s.image) || (s.thumbnail !== undefined && (!mediaKey(s.thumbnail) || s.thumbnail === s.image)) ||
      !Array.isArray(s.responses)) return false;
  const requires = op => s.responses.some(f => f?.op === op && f.required === true);
  return requires(s.surface === 'task' ? 'task_get' : 'session_view') &&
    requires('entity_refs') && (s.entry !== 'entity' || requires('entity_card'));
}

export function imageBytes(item) {
  if (!item || Object.keys(item).length !== 3 || typeof item.path !== 'string' ||
      !/^\/api\/results\/[a-zA-Z0-9_-]{1,80}$/.test(item.path) ||
      typeof item.png !== 'string' || typeof item.sha256 !== 'string' || !/^[a-f0-9]{64}$/.test(item.sha256))
    throw new Error('Invalid protected PNG fixture');
  const bytes = Buffer.from(item.png, 'base64');
  if (!bytes.subarray(0, 8).equals(Buffer.from('89504e470d0a1a0a', 'hex')) ||
      bytes.toString('base64') !== item.png || createHash('sha256').update(bytes).digest('hex') !== item.sha256)
    throw new Error('Protected PNG fixture integrity mismatch');
  return bytes;
}

export function validateImages(images) {
  if (!images || typeof images !== 'object' || Array.isArray(images)) throw new Error('Invalid protected image map');
  const paths = new Set();
  for (const [id, item] of Object.entries(images)) {
    if (!mediaKey(id)) throw new Error('Invalid protected image key');
    imageBytes(item);
    if (paths.has(item.path)) throw new Error('Duplicate protected image path');
    paths.add(item.path);
  }
}

export function resolveImages(s, images) {
  if (!Object.hasOwn(images, s.image) || (s.thumbnail !== undefined && !Object.hasOwn(images, s.thumbnail)))
    throw new Error('Missing protected image fixture');
  return { image: images[s.image], ...(s.thumbnail !== undefined && { thumbnail: images[s.thumbnail] }) };
}

// These fixed callbacks only observe DOM state. In particular no focus or scroll repair runs after Back.
const scrollPosition = el => {
  const offsets = [];
  for (let node = el.parentElement; node; node = node.parentElement) {
    offsets.push([node.scrollLeft, node.scrollTop]);
  }
  return offsets;
};
const focused = el => el.isConnected && el.ownerDocument.activeElement === el;
const viewportMatches = ({ width, height }) =>
  innerWidth === width && innerHeight === height && devicePixelRatio === 1 &&
  visualViewport?.width === width && visualViewport?.height === height && visualViewport?.scale === 1 &&
  visualViewport?.offsetLeft === 0 && visualViewport?.offsetTop === 0;
const pictureState = img => ({
  src: img.currentSrc, loaded: img.complete && img.naturalWidth > 0 && img.naturalHeight > 0,
  width: img.offsetWidth, height: img.offsetHeight, naturalWidth: img.naturalWidth,
  imageEdges: img.getBoundingClientRect().toJSON(),
});
// At DPR 1, an edge at x covers pixel centers on its inside half-open interval.
const pixelBoundary = edge => Math.ceil(edge - 0.5);
const covers = (outer, inner) => pixelBoundary(outer.left) <= pixelBoundary(inner.left) &&
  pixelBoundary(outer.top) <= pixelBoundary(inner.top) &&
  pixelBoundary(outer.right) >= pixelBoundary(inner.right) &&
  pixelBoundary(outer.bottom) >= pixelBoundary(inner.bottom);

async function observeOpen(page, opener, path) {
  if (!path) return;
  const openerPath = await opener.evaluate(el => {
    const indexes = [];
    for (let node = el; node !== document.documentElement; node = node.parentElement)
      indexes.unshift(Array.from(node.parentElement.children).indexOf(node));
    return indexes;
  });
  const cdp = await page.context().newCDPSession(page);
  try {
    const { frameTree } = await cdp.send('Page.getFrameTree');
    const { executionContextId } = await cdp.send('Page.createIsolatedWorld', { frameId: frameTree.frame.id, worldName: 'Fleet verifier' });
    // The original becomes available during the trusted click, before the app handles it.
    // The isolated world keeps the one-use loopback URL out of the app's globals.
    const { exceptionDetails } = await cdp.send('Runtime.evaluate', {
      contextId: executionContextId,
      expression: `(() => {
        const opener = ${JSON.stringify(openerPath)}.reduce((node, index) => node?.children[index], document.documentElement);
        if (!opener) throw new Error('Open control is absent');
        const mark = event => {
          if (!event.isTrusted || !event.composedPath().includes(opener)) return;
          window.removeEventListener('click', mark, true);
          const request = new XMLHttpRequest();
          request.open('POST', ${JSON.stringify(path)}, false);
          request.send();
        };
        window.addEventListener('click', mark, true);
      })()`,
    });
    if (exceptionDetails) throw new Error('Could not observe the trusted Open activation');
  } finally { await cdp.detach(); }
}

export async function imageJourney(page, scenario, frame, origin, settle, checkpoint, openSignal) {
  const { surface, flow, entry, resultCode, resultTitle } = scenario;
  const openLabel = 'Open ' + resultTitle;
  const button = (within, name) => within.getByRole('button', { name, exact: true });
  const task = surface === 'task';
  const details = page.getByRole('tab', { name: 'Details', exact: true, selected: true });
  const shell = task ? page.getByRole('tabpanel', { name: 'Details', exact: true }) : page;
  if (task) await page.getByRole('tab', { name: 'Details', exact: true }).click();
  const question = shell.locator(task ? '[data-ui="decision-block"]' : '[data-ui="question-card"]');
  const trigger = entry === 'thumbnail' ? button(question, scenario.thumbnail ? openLabel : 'Open image: ' + resultTitle) :
    question.locator('button[data-ui="entity-code"]').filter({ hasText: new RegExp('^' + resultCode + '$') });
  const dialog = page.getByRole('dialog', { name: 'Image viewer', exact: true });
  const stage = dialog.getByRole('region', { name: 'Image. Drag to pan; plus and minus to zoom.', exact: true });
  const picture = stage.getByRole('img', { name: resultTitle, exact: true });
  const back = button(dialog, 'Back to question');
  const zoomControls = button(dialog, 'Zoom controls');
  const zoomIn = button(dialog, 'Zoom in');
  const assert = (ok, reason) => { if (!ok) throw new Error(reason); };
  const checkViewport = async () => {
    assert(await page.evaluate(viewportMatches, frame), 'viewport changed');
    assert(page.url() === origin + scenario.start, 'image journey left its source route');
  };
  const waitFocused = async target => {
    const element = await target.elementHandle();
    try { await page.waitForFunction(focused, element); } finally { await element.dispose(); }
  };
  const decode = async (target, path) => {
    await target.waitFor({ state: 'visible' });
    const handle = await target.elementHandle();
    try {
      await page.waitForFunction(img => img.complete && img.naturalWidth > 0 && img.naturalHeight > 0, handle);
      assert(await handle.evaluate(img => img.currentSrc) === origin + path, 'unexpected fixture image');
      await handle.evaluate(img => img.decode());
    } finally { await handle.dispose(); }
  };
  const returning = flow === 'image-returned';

  checkpoint('image-source');
  await trigger.waitFor({ state: 'visible' });
  const loadThumbnail = async opener => {
    if (scenario.thumbnail) {
      await opener.scrollIntoViewIfNeeded();
      await decode(opener.locator('img'), scenario.thumbnail.path);
    }
  };
  if (entry === 'thumbnail') await loadThumbnail(trigger);
  await settle();
  await checkViewport();
  let source, offsets;
  try {
    if (returning) {
      await trigger.scrollIntoViewIfNeeded();
      if (!(await trigger.evaluate(scrollPosition)).some(([, top]) => top > 0)) {
        await trigger.hover();
        await page.mouse.wheel(0, 48);
        await settle();
        await trigger.scrollIntoViewIfNeeded();
      }
      // Trial confirms actionability and completes any Playwright scrolling before the baseline.
      await trigger.click({ trial: true });
      source = await trigger.elementHandle();
      offsets = await source.evaluate(scrollPosition);
      assert(offsets.some(([, top]) => top > 0), 'no displaced source scroll position');
    }
    let opener = trigger;
    if (entry === 'entity') {
      await trigger.click();
      opener = page.locator('[data-ui="entity-card"]').getByRole('link', { name: openLabel, exact: true });
      await loadThumbnail(opener);
    }
    // Finish actionability checks before the protected origin starts serving the original.
    if (!returning || entry === 'entity') await opener.click({ trial: true });
    await observeOpen(page, opener, openSignal);
    await opener.click();

    checkpoint('image-loaded');
    await decode(picture, scenario.image.path);
    await waitFocused(stage);
    await settle();
    await checkViewport();
    const readPicture = async () => {
      const state = await picture.evaluate(pictureState);
      assert(state.loaded && state.src === origin + scenario.image.path, 'unexpected viewer image');
      const stageEdges = await stage.evaluate(el => el.getBoundingClientRect().toJSON());
      const { left, top, right, bottom } = stageEdges;
      assert(left === 0 && top === 0 && right === frame.width && bottom === frame.height, 'viewer does not fill viewport');
      return { ...state, stageEdges };
    };
    const open = await readPicture();
    assert(covers(open.imageEdges, open.stageEdges), 'opening image does not cover viewport');

    if (returning) {
      checkpoint('image-returned');
      await back.click();
      await dialog.waitFor({ state: 'hidden' });
      await page.waitForFunction(focused, source);
      await settle();
      const confirm = async () => {
        await checkViewport();
        if (task) await details.waitFor({ state: 'visible' });
        assert(await source.evaluate(focused), 'source opener lost focus');
        assert(JSON.stringify(await source.evaluate(scrollPosition)) === JSON.stringify(offsets), 'source scroll changed');
        assert(await trigger.isVisible() && !await dialog.isVisible(), 'source did not return');
      };
      await confirm();
      return { confirm, evidence: { sourceScroll: offsets, focus: 'source opener' }, dispose: () => source.dispose() };
    }

    let actualWidth;
    if (flow !== 'image-open') {
      checkpoint(flow);
      await zoomControls.click();
      await waitFocused(zoomIn);
      if (flow === 'image-fit') {
        await button(dialog, 'Fit').click();
        await page.keyboard.press('Shift+Tab');
      } else {
        await button(dialog, 'Actual size').click();
        await settle();
        const actual = await readPicture();
        assert(actual.width === actual.naturalWidth, 'image did not reach actual size');
        actualWidth = actual.width;
        await page.keyboard.press('Shift+Tab');
        await page.keyboard.press('Shift+Tab');
        await waitFocused(zoomIn);
        await page.keyboard.press('Enter');
      }
      await waitFocused(zoomIn);
    }
    const confirm = async () => {
      await checkViewport();
      const { width, height, stageEdges, imageEdges } = await readPicture();
      if (flow === 'image-fit')
        assert(covers(stageEdges, imageEdges) && (width === frame.width || height === frame.height), 'image did not fit');
      else if (flow === 'image-zoom') assert(width > actualWidth, 'image did not enlarge');
      else assert(covers(imageEdges, stageEdges) && await zoomControls.isVisible(), 'compact viewer state changed');
      const focus = flow === 'image-open' ? stage : zoomIn;
      assert(await focus.evaluate(focused), 'viewer focus changed');
    };
    return { confirm, evidence: { focus: flow === 'image-open' ? 'image stage' : 'Zoom in' } };
  } catch (error) {
    await source?.dispose();
    throw error;
  }
}
