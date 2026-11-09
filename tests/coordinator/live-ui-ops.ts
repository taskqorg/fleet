// Live T2 driver: drives the deployed web UI through an SSH tunnel and records every /api call it makes.
// Usage: FLEET_TUNNEL=http://127.0.0.1:19555 OUT=evidence/dir node --experimental-strip-types tests/coordinator/live-ui-ops.ts
// The browser secret is read over ssh into memory only and is injected by request routing (simulating the access proxy).
import {chromium} from 'playwright';import {execSync} from 'node:child_process';import {mkdirSync,writeFileSync} from 'node:fs';
const base=process.env.FLEET_TUNNEL!,out=process.env.OUT!,origin='https://fleet.ufobe.com';mkdirSync(out,{recursive:true});
const secret=execSync('ssh root@pds18.ufobe.com /root/fsecret.sh').toString();
const calls:any[]=[];const browser=await chromium.launch();
const ctx=async(viewport:any)=>{const c=await browser.newContext({viewport});await c.route(base+'/**',r=>{const h:any={...r.request().headers(),authorization:'Bearer '+secret};if(r.request().method()!=='GET')h.origin=origin;r.continue({headers:h})});const p=await c.newPage();p.on('request',q=>{const u=new URL(q.url());if(u.pathname.startsWith('/api/'))calls.push({method:q.method(),path:u.pathname.replace(/[0-9a-f-]{36}/g,':id')})});return p};
const p=await ctx({width:1280,height:900});
// Sessions page: start worker turn.
await p.goto(base+'/');await p.click('#fresh');await p.waitForTimeout(1500);
await p.fill('#prompt','Reply with the single word OK. Do not run tools.');await p.locator('form button').first().click();
await p.waitForFunction(()=>/completed|failed|stopped/.test(document.body.innerText),null,{timeout:300000}).catch(()=>{});
await p.screenshot({path:out+'/t2-sessions-desktop.png',fullPage:true});const sessionText=await p.locator('body').innerText();
await p.setViewportSize({width:390,height:844});await p.screenshot({path:out+'/t2-sessions-mobile.png',fullPage:true});
// Projects page: delivery on fleet-hello-world-alt. The select is hard-coded to test-site projects, so the option is injected (recorded defect).
const q=await ctx({width:1280,height:900});await q.goto(base+'/projects');await q.waitForTimeout(2000);
await q.evaluate(()=>{const s=document.getElementById('project') as HTMLSelectElement;s.append(new Option('fleet-hello-world-alt','fleet-hello-world-alt'));s.value='fleet-hello-world-alt';s.dispatchEvent(new Event('change'))});await q.waitForTimeout(3000);
await q.fill('#task',process.env.TASK!);await q.selectOption('#invocation','direct');await q.click('#create');await q.waitForTimeout(3000);
await q.getByRole('button',{name:'Сохранить ответ'}).first().click();await q.waitForTimeout(3000);
await q.getByRole('button',{name:'Запустить создание'}).first().click();await q.waitForTimeout(8000);
await q.screenshot({path:out+'/t2-projects-desktop.png',fullPage:true});await q.setViewportSize({width:390,height:844});await q.screenshot({path:out+'/t2-projects-mobile.png',fullPage:true});
const status=await q.locator('#status').innerText();
writeFileSync(out+'/t2-ui-calls.json',JSON.stringify({calls,status,sessionTail:sessionText.slice(-600)},null,1));await browser.close();
