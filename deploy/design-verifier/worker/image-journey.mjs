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
  visualViewport?.width === width && visualViewport?.height === height && visualViewport?.scale === 1;
const pictureState = img => ({
  src: img.currentSrc, loaded: img.complete && img.naturalWidth > 0 && img.naturalHeight > 0,
  width: img.offsetWidth, height: img.offsetHeight, naturalWidth: img.naturalWidth,
  stageWidth: img.parentElement.clientWidth, stageHeight: img.parentElement.clientHeight,
});

export async function imageJourney(page, scenario, frame, origin, settle, checkpoint) {
  const { surface, flow, entry, resultCode, resultTitle } = scenario;
  const openLabel = 'Open ' + resultTitle;
  const button = (within, name) => within.getByRole('button', { name, exact: true });
  const task = surface === 'task';
  const details = page.getByRole('tab', { name: 'Details', exact: true, selected: true });
  const shell = task ? page.getByRole('tabpanel', { name: 'Details', exact: true }) : page;
  if (task) await page.getByRole('tab', { name: 'Details', exact: true }).click();
  const question = shell.locator(task ? '[data-ui="decision-block"]' : '[data-ui="question-card"]');
  const trigger = entry === 'thumbnail' ? button(question, openLabel) :
    question.locator('button[data-ui="entity-code"]').filter({ hasText: new RegExp('^' + resultCode + '$') });
  const dialog = page.getByRole('dialog', { name: 'Image viewer', exact: true });
  const stage = dialog.getByRole('region', { name: 'Image. Drag to pan; plus and minus to zoom.', exact: true });
  const picture = stage.getByRole('img', { name: resultTitle, exact: true });
  const back = button(dialog, 'Back to question');
  const zoomControls = button(dialog, 'Zoom controls');
  const zoomIn = button(dialog, 'Zoom in');
  const assert = (ok, reason) => { if (!ok) throw new Error(reason); };
  const checkViewport = async () => assert(await page.evaluate(viewportMatches, frame), 'viewport changed');
  const waitFocused = async target => {
    const element = await target.elementHandle();
    try { await page.waitForFunction(focused, element); } finally { await element.dispose(); }
  };
  const returning = flow === 'image-returned';

  checkpoint('image-source');
  await trigger.waitFor({ state: 'visible' });
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
    await trigger.click();
    if (entry === 'entity') await page.locator('[data-ui="entity-card"]')
      .getByRole('link', { name: openLabel, exact: true }).click();

    checkpoint('image-loaded');
    await picture.waitFor({ state: 'visible' });
    const image = await picture.elementHandle();
    try {
      await page.waitForFunction(img => img.complete && img.naturalWidth > 0 && img.naturalHeight > 0, image);
      await image.evaluate(img => img.decode());
    } finally { await image.dispose(); }
    await waitFocused(stage);
    await settle();
    await checkViewport();
    const readPicture = async () => {
      const state = await picture.evaluate(pictureState);
      assert(state.loaded && state.src === origin + scenario.image.path, 'unexpected viewer image');
      assert(state.stageWidth === frame.width && state.stageHeight === frame.height, 'viewer does not fill viewport');
      return state;
    };
    const open = await readPicture();
    assert(open.width >= open.stageWidth && open.height >= open.stageHeight, 'opening image does not cover viewport');

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
      const { width, height, stageWidth, stageHeight } = await readPicture();
      if (flow === 'image-fit')
        assert(width <= stageWidth && height <= stageHeight && (width === stageWidth || height === stageHeight), 'image did not fit');
      else if (flow === 'image-zoom') assert(width > actualWidth, 'image did not enlarge');
      else assert(width >= stageWidth && height >= stageHeight && await zoomControls.isVisible(), 'compact viewer state changed');
      const focus = flow === 'image-open' ? stage : zoomIn;
      assert(await focus.evaluate(focused), 'viewer focus changed');
    };
    return { confirm, evidence: { focus: flow === 'image-open' ? 'image stage' : 'Zoom in' } };
  } catch (error) {
    await source?.dispose();
    throw error;
  }
}
