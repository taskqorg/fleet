// AWS Signature Version 4 vectors from the S3 documentation ("Examples: Signature calculations" and
// "Authenticating requests: using query parameters"), plus the client's retry and Access-header behaviour on the fake store.
import {test} from 'node:test';import assert from 'node:assert/strict';
import {signature,presignQuery,s3Client,S3Error} from '../../packages/coordinator/s3.ts';
import {fakeStore} from './s3-fake.ts';
const credentials={accessKeyId:'AKIAIOSFODNN7EXAMPLE',secretAccessKey:'wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY'},date='20130524T000000Z',empty='e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855';

test('SigV4 header vectors: GET object with Range, PUT object, GET lifecycle, GET bucket list',()=>{
 const host='examplebucket.s3.amazonaws.com',sig=(method:string,path:string,query:Record<string,string>,headers:Record<string,string>,payload:string)=>signature({method,path,query,headers:{host,...headers,'x-amz-content-sha256':payload,'x-amz-date':date},payload,credentials,region:'us-east-1',date}).signature;
 assert.equal(sig('GET','/test.txt',{},{range:'bytes=0-9'},empty),'f0e8bdb87c964420e857bd35b5d6ed310bd44f0170aba48dd91039c6036bdb41');
 assert.equal(sig('PUT','/test%24file.text',{},{date:'Fri, 24 May 2013 00:00:00 GMT','x-amz-storage-class':'REDUCED_REDUNDANCY'},'44ce7dd67c959e0d3524ffac1771dfbba87d2b6b4b4e99e42034a8b803f8b072'),'98ad721746da40c64f1a55b78f14c238d841ea1380cd77a1b5971af0ece108bd');
 assert.equal(sig('GET','/',{lifecycle:''},{},empty),'fea454ca298b7da1c68078a5d1bdbfbbe0d65c699e0f91ac7a200a0136783543');
 assert.equal(sig('GET','/',{'max-keys':'2',prefix:'J'},{},empty),'34b48302e7b5fa45bde8084f4b7868a86f0a534bc59db6670ed5711ef69dc6f7');
});

test('SigV4 presign vector: GET object for 86400 s',()=>{
 const q=presignQuery({method:'GET',host:'examplebucket.s3.amazonaws.com',path:'/test.txt',query:{},credentials,region:'us-east-1',date,expires:86400});
 assert.equal(new URLSearchParams(q).get("X-Amz-Signature"),"aeeed9bbccd4d02ee5c0109b86d86835f995330da4c265957d157751f604d404");
 assert.match(q,/X-Amz-Credential=AKIAIOSFODNN7EXAMPLE%2F20130524%2Fus-east-1%2Fs3%2Faws4_request/);
});

test('client: path-style requests, Access headers outside the signature, multipart and part copy, typed errors, retry on 503',async()=>{
 const access={id:'cid',secret:'csecret'},fake=await fakeStore({requireAccess:access});
 const c=s3Client({endpoint:fake.url,credentials,extraHeaders:()=>({'cf-access-client-id':access.id,'cf-access-client-secret':access.secret})});
 await c.put('b','a/one.txt',Buffer.from('hello'),'text/plain');
 const put=fake.requests.at(-1)!;assert.equal(put.url,'/b/a/one.txt');assert.equal(put.headers['cf-access-client-id'],'cid');
 assert.doesNotMatch(String(put.headers.authorization),/cf-access/);assert.doesNotMatch(Object.keys(put.headers).join(' '),/checksum/);
 assert.equal((await c.head('b','a/one.txt'))?.contentLength,5);
 const chunks:Buffer[]=[];for await(const d of await c.get('b','a/one.txt'))chunks.push(d);assert.equal(Buffer.concat(chunks).toString(),'hello');
 await c.copy('b','a/two.txt','b/a/one.txt');assert.equal(fake.objects.get('a/two.txt')?.toString(),'hello');
 const id=await c.createMultipartUpload('b','a/big.bin','application/octet-stream');
 const parts=[{partNumber:1,etag:await c.uploadPartCopy('b','a/big.bin',id,1,'b/a/one.txt','bytes=0-1')},{partNumber:2,etag:await c.uploadPartCopy('b','a/big.bin',id,2,'b/a/one.txt','bytes=2-4')}];
 await c.completeMultipartUpload('b','a/big.bin',id,parts);assert.equal(fake.objects.get('a/big.bin')?.toString(),'hello');
 const aborted=await c.createMultipartUpload('b','a/x');await c.abortMultipartUpload('b','a/x',aborted);assert.equal(fake.uploads.has(aborted),false);
 await c.delete('b','a/two.txt');
 assert.equal(await c.head('b','a/two.txt'),null);
 await assert.rejects(c.get('b','a/two.txt'),(e:any)=>e instanceof S3Error&&e.code==='NoSuchKey'&&e.status===404);
 const url=new URL(c.presign('PUT','b','a/big.bin',900,{partNumber:'3',uploadId:id}));assert.equal(url.pathname,'/b/a/big.bin');assert.equal(url.searchParams.get('partNumber'),'3');assert.equal(url.searchParams.get('X-Amz-SignedHeaders'),'host');
 const before=fake.requests.length;fake.down=true;
 await assert.rejects(c.head('b','a/one.txt'),(e:any)=>e.status===503);assert.equal(fake.requests.length-before,3,'three attempts');
 fake.down=false;await fake.close();
});

test('canonical query sorts by encoded name',()=>{
 // Raw order puts a_ (0x5f) before aé; encoded, a%C3%A9 sorts first.
 const q=presignQuery({method:'GET',host:'h',path:'/b/k',query:{'a_':'2','aé':'1'},credentials,region:'garage',date,expires:60});
 assert.ok(q.indexOf('a%C3%A9=1')<q.indexOf('a_=2'));
});

import {createServer} from 'node:http';
const serve=async(handler:(req:any,res:any,n:number)=>void)=>{let n=0;const srv=createServer((req,res)=>handler(req,res,n++));await new Promise<void>(r=>srv.listen(0,'::1',r));return {srv,url:`http://[::1]:${(srv.address() as any).port}`,count:()=>n}};

test('IPv6 endpoint, XML entities, no retry on 501, clock offset after RequestTimeTooSkewed, idle timeout retried',async()=>{
 // IPv6 literal endpoint, and every XML entity form decoded.
 let s=await serve((req,res)=>{res.writeHead(200,{'content-type':'application/xml'});res.end('<InitiateMultipartUploadResult><UploadId>a&amp;b&lt;c&gt;&quot;&apos;&#65;&#x42;</UploadId></InitiateMultipartUploadResult>')});
 assert.equal(await s3Client({endpoint:s.url,credentials}).createMultipartUpload('b','k'),'a&b<c>"\'AB');s.srv.close();
 // An out-of-range reference in an error body is replaced, not thrown.
 s=await serve((req,res)=>{res.writeHead(400,{'content-type':'application/xml'});res.end('<Error><Code>Bad</Code><Message>x&#x110000;y</Message></Error>')});
 await assert.rejects(s3Client({endpoint:s.url,credentials}).delete('b','k'),(e:any)=>e.code==='Bad'&&e.message==='x\ufffdy');s.srv.close();
 // 501 is not transient: one attempt.
 s=await serve((req,res)=>{res.writeHead(501);res.end()});
 await assert.rejects(s3Client({endpoint:s.url,credentials}).delete('b','k'),(e:any)=>e.status===501);assert.equal(s.count(),1);s.srv.close();
 // Skew: the retry is signed with the server's clock.
 const server=Date.now()+3600000,dates:string[]=[];
 s=await serve((req,res,n)=>{dates.push(String(req.headers['x-amz-date']));if(n===0){res.writeHead(403,{'content-type':'application/xml',date:new Date(server).toUTCString()});res.end('<Error><Code>RequestTimeTooSkewed</Code></Error>')}else{res.writeHead(204);res.end()}});
 const skewed=s3Client({endpoint:s.url,credentials});await skewed.delete('b','k');
 const hour=(d:string)=>Date.UTC(+d.slice(0,4),+d.slice(4,6)-1,+d.slice(6,8),+d.slice(9,11),+d.slice(11,13));
 assert.ok(Math.abs(hour(dates[1])-server)<120000,'second attempt uses the server clock');
 assert.match(new URL(skewed.presign('GET','b','k',60)).searchParams.get('X-Amz-Date')!,new RegExp('^'+dates[1].slice(0,11)));s.srv.close();
 // A silent socket is aborted after the idle timeout and retried.
 s=await serve((req,res,n)=>{if(n===0)return;res.writeHead(204);res.end()});
 await s3Client({endpoint:s.url,credentials,idleTimeoutMs:200}).delete('b','k');assert.equal(s.count(),2);s.srv.closeAllConnections();s.srv.close();
});

test('head: a proxy that drops Content-Length on HEAD still gives the size (one-byte ranged GET)',async()=>{
 const fake=await fakeStore({dropHeadLength:true}),c=s3Client({endpoint:fake.url,credentials});
 try{await c.put('b','page.html',Buffer.from('<h1>hello</h1>'),'text/html');assert.equal((await c.head('b','page.html'))?.contentLength,14);
  assert.equal(fake.requests.find(r=>r.method==='HEAD')!.headers['accept-encoding'],'identity');assert.equal(await c.head('b','missing'),null)}
 finally{await fake.close()}
});
