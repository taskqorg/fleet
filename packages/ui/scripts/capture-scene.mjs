export const captureMatrix = [
  { viewport: 'desktop', theme: 'dark', width: 1440, height: 900 },
  { viewport: 'mobile', theme: 'dark', width: 390, height: 844 },
  { viewport: 'desktop', theme: 'light', width: 1440, height: 900 },
  { viewport: 'mobile', theme: 'light', width: 390, height: 844 },
];

// This function is emitted by preview:scene for the managed tool runner. The shell only serves HTML.
export async function captureScene(tools, { url, recipe }) {
  async function call(tool, args) {
    const result = await tool(args);
    const content = result.content?.find(item => item.type === 'text')?.text;
    if (result.isError || !content) throw new Error(content || 'Managed tool did not return evidence');
    try { return JSON.parse(content); } catch { return content; }
  }
  const captures = [];
  const browser = name => tools['mcp__project_browser__' + name];

  try {
    await call(browser('close'), {});
    for (const frame of recipe.matrix) {
      const { viewport, theme, width, height } = frame;
      await call(browser('emulate'), { viewport, colorScheme: theme });
      await call(browser('navigate'), { url });
      const expression = `(async () => {
        await document.fonts.ready;
        await new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve)));
        const landmark = document.querySelector(${JSON.stringify(recipe.landmark)});
        const style = landmark && getComputedStyle(landmark);
        return {
          digest: document.body.dataset.previewRecipe, href: location.href,
          ready: document.readyState === 'complete' && landmark?.getClientRects().length > 0 && style.visibility === 'visible',
          width: innerWidth, height: innerHeight, dpr: devicePixelRatio,
          dark: matchMedia('(prefers-color-scheme: dark)').matches,
          scheme: style?.colorScheme,
          reducedMotion: matchMedia('(prefers-reduced-motion: reduce)').matches,
          clock: Date.now(), timeZone: Intl.DateTimeFormat().resolvedOptions().timeZone,
          locale: Intl.DateTimeFormat().resolvedOptions().locale,
          overflow: document.documentElement.scrollWidth > innerWidth,
          browser: navigator.userAgent,
        };
      })()`;
      const { result: observed } = await call(browser('evaluate'), { expression });
      if (!observed) throw new Error('Managed browser returned no render context');
      const scheme = observed.scheme?.split(' ') ?? [];
      if (observed.digest !== recipe.digest || observed.href !== url || !observed.ready ||
          observed.width !== width || observed.height !== height || observed.dpr !== recipe.dpr ||
          observed.dark !== (theme === 'dark') || !scheme.includes(theme) || !observed.reducedMotion ||
          observed.clock !== recipe.clock || observed.timeZone !== recipe.timeZone || observed.locale !== recipe.locale)
        throw new Error(`${viewport} ${theme}: unexpected page or render context ${JSON.stringify(observed)}`);
      const png = await call(browser('screenshot'), {});
      if (png.width !== width || png.height !== height || typeof png.path !== 'string')
        throw new Error(`${viewport} ${theme}: unexpected PNG dimensions or missing file`);
      captures.push({ ...frame, observed, path: png.path });
    }

    const frames = [];
    for (const { path, ...frame } of captures) {
      const saved = await call(tools.mcp__fleet__artifact_save, {
        path, name: `${recipe.scene.replaceAll('/', '-')}-${recipe.state}-${frame.viewport}-${frame.theme}.png`,
        media_type: 'image/png', required: true,
      });
      if (!saved.artifactId) throw new Error('PNG artifact ID is missing');
      frames.push({ ...frame, artifactId: saved.artifactId });
    }
    const retained = await call(tools.mcp__fleet__artifact_list, { media_type: 'image/png' });
    const stored = new Map(retained.artifacts.filter(item => item.state === 'stored').map(item => [item.id, item]));
    const originals = frames.map(frame => {
      const item = stored.get(frame.artifactId);
      if (!item || !/^[0-9a-f]{64}$/.test(item.sha256) || !item.size)
        throw new Error('PNG artifact storage was not confirmed');
      const { href, ...observed } = frame.observed;
      return { ...frame, observed, code: item.code, sha256: item.sha256, bytes: item.size };
    });
    return { recipe: recipe.digest, scene: recipe.scene, state: recipe.state, frames: originals };
  } finally {
    await call(browser('close'), {});
  }
}
