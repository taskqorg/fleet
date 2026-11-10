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
 assert.equal((await c.head('b','a/one.txt')).contentLength,5);
 const chunks:Buffer[]=[];for await(const d of await c.get('b','a/one.txt'))chunks.push(d);assert.equal(Buffer.concat(chunks).toString(),'hello');
 await c.copy('b','a/two.txt','b/a/one.txt');assert.equal(fake.objects.get('a/two.txt')?.toString(),'hello');
 const id=await c.createMultipartUpload('b','a/big.bin','application/octet-stream');
 const parts=[{PartNumber:1,ETag:await c.uploadPartCopy('b','a/big.bin',id,1,'b/a/one.txt','bytes=0-1')},{PartNumber:2,ETag:await c.uploadPartCopy('b','a/big.bin',id,2,'b/a/one.txt','bytes=2-4')}];
 await c.completeMultipartUpload('b','a/big.bin',id,parts);assert.equal(fake.objects.get('a/big.bin')?.toString(),'hello');
 const aborted=await c.createMultipartUpload('b','a/x');await c.abortMultipartUpload('b','a/x',aborted);assert.equal(fake.uploads.has(aborted),false);
 await c.delete('b','a/two.txt');
 await assert.rejects(c.head('b','a/two.txt'),(e:any)=>e instanceof S3Error&&e.$metadata.httpStatusCode===404&&e.name==='NotFound');
 await assert.rejects(c.get('b','a/two.txt'),(e:any)=>e.name==='NoSuchKey'&&e.$metadata.httpStatusCode===404);
 const url=new URL(c.presign('PUT','b','a/big.bin',900,{partNumber:'3',uploadId:id}));assert.equal(url.pathname,'/b/a/big.bin');assert.equal(url.searchParams.get('partNumber'),'3');assert.equal(url.searchParams.get('X-Amz-SignedHeaders'),'host');
 const before=fake.requests.length;fake.down=true;
 await assert.rejects(c.head('b','a/one.txt'),(e:any)=>e.$metadata.httpStatusCode===503);assert.equal(fake.requests.length-before,3,'three attempts');
 fake.down=false;await fake.close();
});
