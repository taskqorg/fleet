// Test helpers for the artifact store: register a fake S3 store for hosts through the real route, and upload a file the way an
// executor does (reserve, part URLs, PUT, commit, loopback re-hash, verified).
import {createHash} from 'node:crypto';
import {fakeStore,type FakeStore} from './s3-fake.js';
const send=async(app:any,secret:string,url:string,payload:any)=>{const r=await app.inject({method:'POST',url,headers:{authorization:'Bearer '+secret},payload});if(r.statusCode>=300)throw Object.assign(Error(url+' '+r.statusCode+' '+r.body),{statusCode:r.statusCode,body:r.body});return r.json()};
export async function attachStores(app:any,secret:string,hostIds:string[],fake?:FakeStore):Promise<FakeStore>{const f=fake??await fakeStore();
 for(const h of hostIds)await send(app,secret,'/api/hosts/stores',{hostId:h,store:{id:h+'-store',bucket:'fleet',zone:h,endpoint:f.url,localEndpoint:f.url,accessKeyId:'test-key',secretAccessKey:'test-secret',capacityBytes:1e12,usedBytes:0}});
 return f}
export async function upload(app:any,secret:string,v:{hostId:string,executionId:string,path:string,content:Buffer|string,mediaType?:string,required?:boolean}){
 const data=Buffer.isBuffer(v.content)?v.content:Buffer.from(v.content),sha256=createHash('sha256').update(data).digest('hex');
 const r=await send(app,secret,'/api/hosts/artifacts/reserve',{hostId:v.hostId,executionId:v.executionId,name:v.path,mediaType:v.mediaType??'application/octet-stream',size:data.length,sha256,required:v.required});
 if(r.state==='stored')return r.artifactId as string;
 const numbers=Array.from({length:Math.max(1,Math.ceil(data.length/r.partSize))},(_,i)=>i+1);
 const urls=(await send(app,secret,'/api/hosts/artifacts/parts',{hostId:v.hostId,artifactId:r.artifactId,partNumbers:numbers})).parts;
 const parts:any[]=[];for(const p of urls){const put=await fetch(p.url,{method:'PUT',body:new Uint8Array(data.subarray((p.partNumber-1)*r.partSize,p.partNumber*r.partSize))});if(!put.ok)throw Error('part '+put.status);parts.push({partNumber:p.partNumber,etag:put.headers.get('etag')})}
 const c=await send(app,secret,'/api/hosts/artifacts/commit',{hostId:v.hostId,artifactId:r.artifactId,parts});
 if(c.verify){const got=Buffer.from(await (await fetch(c.verify.url)).arrayBuffer());await send(app,secret,'/api/hosts/artifacts/verified',{hostId:v.hostId,artifactId:r.artifactId,storeId:c.verify.storeId,sha256:createHash('sha256').update(got).digest('hex')})}
 return r.artifactId as string}
