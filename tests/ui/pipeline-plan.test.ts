// Requirement: activation remains separate from actual execution observations.
// Risk: an inactive branch appears completed or receives fabricated zero resources.
import {test} from 'node:test';
import assert from 'node:assert/strict';
import {mkdirSync} from 'node:fs';
import {build} from 'esbuild';
import {chromium} from 'playwright';

test('pipeline activation is its own finite state family', async () => {
  const bundle=await build({stdin:{contents:"export {states} from './packages/ui/src/registry.ts'",resolveDir:process.cwd(),loader:'ts'},bundle:true,write:false,platform:'node',format:'esm'});
  const {states}=await import('data:text/javascript;base64,'+Buffer.from(bundle.outputFiles[0].text).toString('base64'));
  assert.deepEqual(Object.keys(states.activation), ['pending','selected','not_selected','blocked']);
  assert.equal('not_selected' in states.execution, false);
});

test('mounted plan distinguishes inactive, actual, reused and unconfirmed rows', {skip: !process.env.FLEET_CONTROL_BROWSER}, async () => {
  const result = await build({stdin:{contents:`
    import {render} from 'preact';import {StepList} from './packages/ui/src/index.ts';
    const u={value:null,basis:'unavailable'};
    const base={kind:'program',executor:'local',reads:'packet',writes:'result'};
    render(<StepList label="Conditional plan" items={[
      {...base,id:'pending',activation:'pending'},
      {...base,id:'inactive',activation:'not_selected',activationReason:'condition_false'},
      {...base,id:'blocked',activation:'blocked',activationReason:'source_missing'},
      {...base,id:'actual',activation:'selected',run:{state:'succeeded',time:{value:2,basis:'measured'},cost:u}},
      {...base,id:'prepared',activation:'selected',run:{state:'preparing',time:u,cost:u}},
      {...base,id:'reused',activation:'selected',run:{state:'succeeded',note:'reused · original resources',time:u,cost:u}},
      {...base,id:'unknown',activation:'selected',run:{state:'failed',note:'unconfirmed',time:u,cost:u}},
    ]}/>,document.body);
  `,resolveDir:process.cwd(),loader:'tsx'},bundle:true,write:false,format:'iife',jsx:'automatic',jsxImportSource:'preact'});
  const {uiCss}=await import(new URL('../../packages/ui/scripts/tokens.mjs',import.meta.url).href) as {uiCss:()=>string};
  const browser=await chromium.launch({headless:true}),page=await browser.newPage();
  try {
    await page.setViewportSize({width:1280,height:900});await page.setContent('<body></body>');
    await page.addStyleTag({content:uiCss()});
    await page.addScriptTag({content:result.outputFiles[0].text});
    const rows=page.getByRole('list',{name:'Conditional plan'}).getByRole('listitem');
    assert.equal(await rows.count(),7);
    for(const [index,text] of [[0,'Pending'],[1,'Not selected'],[2,'Blocked']] as const){
      assert.match(await rows.nth(index).innerText(),new RegExp(text));
      assert.equal(await rows.nth(index).locator('.svc').count(),0,'inactive work has no resource measurements');
    }
    assert.match(await rows.nth(5).innerText(),/reused · original resources/);
    assert.match(await rows.nth(6).innerText(),/unconfirmed/);
    const shots=process.env.FLEET_BROWSER_SHOTS;
    if(shots){
      mkdirSync(shots,{recursive:true});await page.screenshot({path:`${shots}/pipeline-plan.png`,fullPage:true});
      await page.setViewportSize({width:620,height:900});
      await page.evaluate(()=>document.documentElement.dataset.theme='dark');
      await page.screenshot({path:`${shots}/pipeline-plan-dark-620.png`,fullPage:true});
    }
  } finally {await browser.close();}
});
