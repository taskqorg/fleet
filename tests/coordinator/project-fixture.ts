import {mkdtempSync,mkdirSync} from 'node:fs';import {attachStores} from './artifact-helpers.ts';import {createApp} from './test-app.ts';import {fakeGateway,seedConnections} from './fake-gateway.ts';
// A project app whose api steps call the real connection client against a fake gateway (connection tavily).
export async function projectFixture(){const dir=mkdtempSync('/tmp/fleet-project-');mkdirSync(dir+'/connections/tavily',{recursive:true});
 const reply=(b:any)=>({results:[{url:'https://example.com/hello',title:'Hello reference',content:'page text'}],usage:{credits:1},observedMax:b.max_results});
 const gw=await fakeGateway(dir+'/gateway',async b=>b.query==='slow-native'?(await new Promise(r=>setTimeout(r,300)),[200,reply(b)]):b.query==='quota'?[429,{error:'quota unavailable'}]:[200,reply(b)]);
 const config={database:dir+'/db',browserSecret:'browser',machineSecret:'machine',hostId:'host',gatewayDir:dir+'/gateway',gatewayUrl:gw.url,connectionTickMs:0,storeEndpointHosts:['127.0.0.1']};const app=createApp(config);await app.ready();await seedConnections(app,dir);const fake=await attachStores(app,'machine',['host']);
 return {dir,config,app,fake,calls:()=>gw.seen.length,close:async()=>{await gw.close();await fake.close()}}}
