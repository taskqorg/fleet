// createApp for tests. People: one owner in a people file; browser-Bearer injects carry that person, as the guard adds it (§1).
// Hosts: the old test config names (hostId, nativeHostIds, integrationHostId, publicationHostId, machineSecret) become host rows
// registered as the owner, each with its own secret hostSecret(machineSecret, id). An inject with 'Bearer <machineSecret>' is
// rewritten to the secret of the body's hostId when that host is registered, else to the default host's secret, so a foreign
// hostId is refused as it would be for a real host (§3). A test that checks the headers itself passes 'x-fleet-person' (null for
// none) or a real host secret.
import {mkdtempSync,writeFileSync} from 'node:fs';
import {createHash} from 'node:crypto';
import {createApp as real} from '../../packages/coordinator/app.ts';
export const OWNER='owner@fleet.test';
export const hostSecret=(machineSecret:string,id:string)=>machineSecret+':'+id+':test-host-secret-0123456789abcdef';
export const sha256=(s:string)=>createHash('sha256').update(s).digest('hex');
export function peopleFile(list:any[]=[{email:OWNER,name:'Owner',roles:['operator','architect'],owner:true}]){const f=mkdtempSync('/tmp/fleet-people-')+'/people.json';writeFileSync(f,JSON.stringify(list));return f}
type Legacy={machineSecret?:string,nativeHostIds?:string[],integrationHostId?:string,publicationHostId?:string};
export function testHosts(c:Legacy&{hostId:string}){return [
 ...[...new Set([c.hostId,...(c.nativeHostIds??[])])].map(id=>({id,roles:[] as string[]})),
 ...(c.integrationHostId?[{id:c.integrationHostId,roles:['inspector-for:'+c.hostId]}]:[]),
 ...(c.publicationHostId?[{id:c.publicationHostId,roles:['publication']}]:[])]}
export function createApp(config:Parameters<typeof real>[0]&Legacy){
 const {machineSecret,nativeHostIds,integrationHostId,publicationHostId,...rest}=config;
 const app=real({...rest,peopleFile:config.peopleFile??peopleFile()});const inject=app.inject.bind(app) as any;
 const hosts=machineSecret?testHosts(config):[];let registered:Promise<void>|null=null;
 const register=()=>registered??=(async()=>{for(const h of hosts){const r=await inject({method:'POST',url:'/api/ops/hosts_register',headers:{authorization:'Bearer '+config.browserSecret,'x-fleet-person':OWNER,...(config.browserOrigin?{origin:config.browserOrigin}:{})},payload:{id:h.id,roles:h.roles,secretSha256:sha256(hostSecret(machineSecret!,h.id))}});
  if(r.statusCode!==200&&r.statusCode!==409)throw Error('test host '+h.id+': '+r.body)}})();
 (app as any).registerTestHosts=register;
 (app as any).inject=async(o:any,...more:any[])=>{if(o&&typeof o==='object'){const h={...(o.headers??{})};const k=Object.keys(h).find(x=>x.toLowerCase()==='x-fleet-person');
  const auth=String(h.authorization??h.Authorization??'');
  if(k){if(h[k]===null)delete h[k]}else if(auth==='Bearer '+config.browserSecret)h['x-fleet-person']=OWNER;
  if(machineSecret&&auth==='Bearer '+machineSecret){await register();const body=typeof o.payload==='string'?JSON.parse(o.payload||'{}'):o.payload??{};
   const id=hosts.some(x=>x.id===body?.hostId)?body.hostId:config.hostId;delete h.Authorization;h.authorization='Bearer '+hostSecret(machineSecret,id)}
  else if(machineSecret&&hosts.length)await register();
  o={...o,headers:h}}return inject(o,...more)};
 return app;
}
