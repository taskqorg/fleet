// Requirement: shared Composer preserves newer authored input on late send and resets by adapter key.
// Risk: an old completion clears current text or shared controls access unscoped browser storage.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { build } from 'esbuild';
import { transform } from '@solidjs/compiler';
// @ts-expect-error ESM helper module
import { solidEsbuildPlugin } from '../../packages/ui/scripts/solid-esbuild.mjs';
import { chromium } from 'playwright';
test('mounted Composer: original-text clear, newer input, adapter reset and memory-only default', { skip: !process.env.FLEET_CONTROL_BROWSER }, async () => {
  const src = `
    import { createSignal, flush } from 'solid-js'; import { render } from '@solidjs/web'; import { Composer } from './packages/ui/src/index.ts';
    const w=window; w.record='restored'; w.key='one'; w.refuse=false;
    const adapter=()=>({key:w.key,read:()=>w.record,write:text=>{w.record=text},clearIfUnchanged:text=>{if(w.refuse)throw Error('stale');if(w.record===text)w.record=''}});
    const [dp, setDp] = createSignal(adapter());
    w.draw=()=>{ setDp(adapter()); flush(); };
    w.memory=()=>{ setDp(undefined); flush(); };
    render(()=><Composer draftPersistence={dp()} onSend={()=>new Promise((resolve,reject)=>{w.sent=resolve;w.failed=reject})}/>,document.body);
  `;
  const compiled = transform(src, { filename: 'fixture.tsx', generate: 'dom', moduleName: '@solidjs/web' });
  const result = await build({ stdin: { contents: compiled.code, resolveDir: process.cwd(), loader: 'ts' }, bundle: true, write: false, format: 'iife', conditions: ['solid'], plugins: [solidEsbuildPlugin()] });
  const browser = await chromium.launch({ headless: true }), page = await browser.newPage();
  try {
    await page.setContent('<body></body>'); await page.addScriptTag({ content: result.outputFiles[0].text });
    await page.waitForFunction(() => document.querySelector('textarea')?.value === 'restored');
    await page.getByPlaceholder('Message').fill('original'); await page.getByRole('button', { name: 'Send' }).click();
    await page.getByPlaceholder('Message').fill('newer'); await page.evaluate(() => (window as any).sent());
    assert.equal(await page.getByPlaceholder('Message').inputValue(), 'newer');
    assert.equal(await page.evaluate(() => (window as any).record), 'newer');
    await page.evaluate(() => { (window as any).key='two'; (window as any).record='new-generation'; (window as any).draw(); });
    await page.waitForFunction(() => document.querySelector('textarea')?.value === 'new-generation');
    await page.getByRole('button', { name: 'Send' }).click();
    await page.evaluate(() => { (window as any).refuse=true; (window as any).sent(); });
    assert.equal(await page.getByPlaceholder('Message').inputValue(), 'new-generation');
    await page.getByRole('button', { name: 'Send' }).click();
    await page.evaluate(() => { const w=window as any;w.oldFailed=w.failed;w.key='third';w.record='third scope';w.refuse=false;w.draw(); });
    await page.waitForFunction(() => document.querySelector('textarea')?.value === 'third scope');
    assert.equal(await page.getByRole('button', { name: 'Send' }).isEnabled(), true, 'new target is not blocked by an old pending send');
    await page.evaluate(() => (window as any).oldFailed(Error('old scope failed')));
    assert.equal(await page.getByRole('button', { name: 'Send' }).isEnabled(), true, 'old rejection cannot leave the new scope busy');
    await page.evaluate(() => (window as any).memory()); await page.waitForFunction(() => document.querySelector('textarea')?.value === '');
  } finally { await browser.close(); }
});
