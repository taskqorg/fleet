// Requirement: shared Composer preserves newer authored input on late send and resets by adapter key.
// Risk: an old completion clears current text or shared controls access unscoped browser storage.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { build } from 'esbuild';
import { chromium } from 'playwright';
test('mounted Composer: original-text clear, newer input, adapter reset and memory-only default', { skip: !process.env.FLEET_CONTROL_BROWSER }, async () => {
  const result = await build({ stdin: { contents: `
    import {render} from 'preact'; import {Composer} from './packages/ui/src/index.ts';
    const w=window; w.record='restored'; w.key='one'; w.refuse=false;
    const adapter=()=>({key:w.key,read:()=>w.record,write:text=>{w.record=text},clearIfUnchanged:text=>{if(w.refuse)throw Error('stale');if(w.record===text)w.record=''}});
    w.draw=()=>render(<Composer draftPersistence={adapter()} onSend={()=>new Promise((resolve,reject)=>{w.sent=resolve;w.failed=reject})}/>,document.body);
    w.memory=()=>render(<Composer onSend={()=>{}}/>,document.body);w.draw();
  `, resolveDir: process.cwd(), loader: 'tsx' }, bundle: true, write: false, format: 'iife', jsx: 'automatic', jsxImportSource: 'preact' });
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
