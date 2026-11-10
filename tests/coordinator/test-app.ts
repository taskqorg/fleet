// createApp for tests: one owner in a people file, and browser-Bearer injects carry that person, as the guard adds it (§1).
// A test that checks the person header itself passes 'x-fleet-person' (a value, or null for none).
import {mkdtempSync,writeFileSync} from 'node:fs';
import {createApp as real} from '../../packages/coordinator/app.ts';
export const OWNER='owner@fleet.test';
export function peopleFile(list:any[]=[{email:OWNER,name:'Owner',roles:['operator','architect'],owner:true}]){const f=mkdtempSync('/tmp/fleet-people-')+'/people.json';writeFileSync(f,JSON.stringify(list));return f}
export function createApp(config:Parameters<typeof real>[0]){
 const app=real({peopleFile:peopleFile(),...config});const inject=app.inject.bind(app);
 (app as any).inject=(o:any,...rest:any[])=>{if(o&&typeof o==='object'){const h={...(o.headers??{})};const k=Object.keys(h).find(x=>x.toLowerCase()==='x-fleet-person');
  if(k){if(h[k]===null)delete h[k]}else if(String(h.authorization??h.Authorization??'')==='Bearer '+config.browserSecret)h['x-fleet-person']=OWNER;o={...o,headers:h}}return (inject as any)(o,...rest)};
 return app;
}
