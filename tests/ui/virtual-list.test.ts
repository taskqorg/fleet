// Requirement: shared window anchor methods preserve visible key/offset and coordinate deletion/empty recovery.
// Risk: continuous prepared updates jump the reading position or retain an invalid old scroll.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { build } from 'esbuild';
import { chromium } from 'playwright';
test('mounted VirtualList anchor capture/restore, nearest deleted-key survivor and empty reset', { skip: !process.env.FLEET_CONTROL_BROWSER }, async () => {
  const result = await build({ stdin: { contents: `
    import {render} from 'preact';import {VirtualList} from './packages/ui/src/index.ts';
    const w=window;w.items=Array.from({length:200},(_,i)=>'row-'+i);w.handle={current:null};
    w.draw=()=>render(<VirtualList items={w.items} getKey={x=>x} rowHeight={32} follow="top" label="Fixture" handle={w.handle} render={x=><span>{x}</span>}/>,document.body);w.draw();
  `, resolveDir: process.cwd(), loader: 'tsx' }, bundle: true, write: false, format: 'iife', jsx: 'automatic', jsxImportSource: 'preact' });
  const browser = await chromium.launch({ headless: true }), page = await browser.newPage();
  try {
    await page.setContent('<body></body>'); await page.addStyleTag({ content: readFileSync('packages/ui/src/styles.css','utf8') + '.vl{height:300px}.vl-wrap{width:500px}' });
    await page.addScriptTag({ content: result.outputFiles[0].text }); await page.getByRole('grid').waitFor();
    await page.getByRole('grid').evaluate(el => { el.scrollTop=650;el.dispatchEvent(new Event('scroll')); });
    const before = await page.evaluate(() => (window as any).handle.current.getAnchor()); assert.ok(before.key);
    await page.evaluate(() => { const w=window as any;w.items=['inserted',...w.items];w.draw(); });
    await page.evaluate(anchor => (window as any).handle.current.restoreAnchor(anchor.key,anchor.offsetPx),before);
    assert.deepEqual(await page.evaluate(() => (window as any).handle.current.getAnchor()),before);
    const nearest = await page.evaluate(anchor => { const w=window as any;const i=w.items.indexOf(anchor.key),next=w.items[i+1];w.items=w.items.filter((x:string)=>x!==anchor.key);w.draw();return next; },before);
    await page.evaluate(key => (window as any).handle.current.restoreAnchor(key,0),nearest);
    const restored = await page.evaluate(() => (window as any).handle.current.getAnchor()); assert.equal(restored.key,nearest);assert.equal(restored.offsetPx,0);
    await page.evaluate(() => { const w=window as any;w.items=[];w.draw(); });
    await page.evaluate(() => (window as any).handle.current.restoreAnchor('',0));
    assert.equal(await page.getByRole('grid').evaluate(el=>el.scrollTop),0);
  } finally { await browser.close(); }
});
