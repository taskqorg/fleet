// The S3 subset Fleet uses against Garage, signed with AWS Signature Version 4 (docs/designs/fleet-toolchain.md D3).
// It keeps what the @aws-sdk/client-s3 client was configured with: region 'garage', path-style URLs, no request or response
// checksums (WHEN_REQUIRED, and nothing we call requires one), Cloudflare Access headers added after signing so they are
// never part of the signature, and the SDK's standard retry: 3 attempts, jittered exponential backoff, on 5xx, 429,
// throttling codes and network errors. Like the SDK's Node handler it sets no request timeout: it uses node:http, not fetch,
// because fetch (undici) stops at 300 s without response headers and a large CopyObject can take longer.
import {createHash,createHmac} from 'node:crypto';
import {request as httpRequest,type IncomingMessage} from 'node:http';
import {request as httpsRequest} from 'node:https';
import type {Readable} from 'node:stream';

export type Credentials={accessKeyId:string,secretAccessKey:string};
export type Part={PartNumber:number,ETag:string};
// Same shape callers read from SDK errors: name is the S3 error Code (NotFound for a HEAD 404) and $metadata.httpStatusCode.
export class S3Error extends Error{
 readonly $metadata:{httpStatusCode:number};readonly Code:string;
 constructor(status:number,code:string,message:string){super(message);this.name=code;this.Code=code;this.$metadata={httpStatusCode:status}}
}

const hash=(b:string|Buffer)=>createHash('sha256').update(b).digest('hex');
const hmac=(k:string|Buffer,s:string)=>createHmac('sha256',k).update(s).digest();
// RFC 3986 encoding as SigV4 requires; '/' stays literal in an object key path.
const enc=(s:string,slash=false)=>{const e=encodeURIComponent(s).replace(/[!'()*]/g,c=>'%'+c.charCodeAt(0).toString(16).toUpperCase());return slash?e.replace(/%2F/g,'/'):e};
const query=(q:Record<string,string>)=>Object.keys(q).sort().map(k=>enc(k)+'='+enc(q[k])).join('&');
const EMPTY=hash('');

// Signs one request. Exported for the AWS SigV4 test vectors. headers must include host; payload is the body's SHA-256 hex
// or 'UNSIGNED-PAYLOAD'. Returns the Authorization header value.
export function signature(o:{method:string,path:string,query:Record<string,string>,headers:Record<string,string>,payload:string,credentials:Credentials,region:string,date:string}){
 const names=Object.keys(o.headers).map(h=>h.toLowerCase()).sort(),lower=Object.fromEntries(Object.entries(o.headers).map(([k,v])=>[k.toLowerCase(),v]));
 const signed=names.join(';'),canonical=[o.method,o.path,query(o.query),names.map(n=>n+':'+String(lower[n]).trim().replace(/\s+/g,' ')+'\n').join(''),signed,o.payload].join('\n');
 const day=o.date.slice(0,8),scope=`${day}/${o.region}/s3/aws4_request`;
 const key=hmac(hmac(hmac(hmac('AWS4'+o.credentials.secretAccessKey,day),o.region),'s3'),'aws4_request');
 const sig=createHmac('sha256',key).update(['AWS4-HMAC-SHA256',o.date,scope,hash(canonical)].join('\n')).digest('hex');
 return {authorization:`AWS4-HMAC-SHA256 Credential=${o.credentials.accessKeyId}/${scope}, SignedHeaders=${signed}, Signature=${sig}`,signedHeaders:signed,signature:sig,scope};
}

// Query-string signing (presigned URL), signing only host, payload UNSIGNED-PAYLOAD, as the SDK presigner does for S3.
export function presignQuery(o:{method:string,host:string,path:string,query:Record<string,string>,credentials:Credentials,region:string,date:string,expires:number}){
 const day=o.date.slice(0,8);
 const q={...o.query,'X-Amz-Algorithm':'AWS4-HMAC-SHA256','X-Amz-Credential':`${o.credentials.accessKeyId}/${day}/${o.region}/s3/aws4_request`,'X-Amz-Date':o.date,'X-Amz-Expires':String(o.expires),'X-Amz-SignedHeaders':'host'};
 const {signature:sig}=signature({method:o.method,path:o.path,query:q,headers:{host:o.host},payload:'UNSIGNED-PAYLOAD',credentials:o.credentials,region:o.region,date:o.date});
 return query({...q,'X-Amz-Signature':sig});
}

const amzDate=()=>new Date().toISOString().replace(/[-:]|\.\d{3}/g,'');
const tag=(xml:string,name:string)=>new RegExp(`<${name}>([^<]*)</${name}>`).exec(xml)?.[1]?.replace(/&quot;/g,'"').replace(/&amp;/g,'&');
const xmlEsc=(s:string)=>s.replace(/&/g,'&amp;').replace(/</g,'&lt;').replace(/>/g,'&gt;').replace(/"/g,'&quot;');
const RETRY_CODES=new Set(['SlowDown','Throttling','ThrottlingException','RequestTimeout','RequestTimeTooSkewed','InternalError','ServiceUnavailable']);
const NET_ERRORS=new Set(['ECONNRESET','ECONNREFUSED','EPIPE','ETIMEDOUT','EAI_AGAIN','ENOTFOUND','ECONNABORTED','EHOSTUNREACH','ENETUNREACH']);
const ATTEMPTS=3,BASE_MS=100,MAX_MS=20000;
const backoff=(attempt:number)=>new Promise(r=>setTimeout(r,Math.random()*Math.min(MAX_MS,BASE_MS*2**attempt)));
const readAll=(r:IncomingMessage)=>new Promise<string>((ok,no)=>{const c:Buffer[]=[];r.on('data',d=>c.push(d));r.on('end',()=>ok(Buffer.concat(c).toString()));r.on('error',no)});

export function s3Client(o:{endpoint:string,credentials:Credentials,region?:string,extraHeaders?:()=>Record<string,string>|null}){
 const base=new URL(o.endpoint),region=o.region??'garage',https=base.protocol==='https:';
 const host=base.port?`${base.hostname}:${base.port}`:base.hostname,prefix=base.pathname.replace(/\/$/,'');
 const pathOf=(bucket:string,key?:string)=>prefix+'/'+enc(bucket)+(key===undefined?'':'/'+enc(key,true));

 // One attempt; resolves with the response, or rejects with S3Error for a non-2xx status.
 function once(method:string,path:string,q:Record<string,string>,headers:Record<string,string>,body?:Buffer){
  const date=amzDate(),payload=body?hash(body):EMPTY;
  const h:Record<string,string>={...headers,host,'x-amz-date':date,'x-amz-content-sha256':payload};
  if(body)h['content-length']=String(body.length);
  h.authorization=signature({method,path,query:q,headers:h,payload,credentials:o.credentials,region,date}).authorization;
  Object.assign(h,o.extraHeaders?.()??{});
  const qs=query(q);
  return new Promise<IncomingMessage>((ok,no)=>{
   const req=(https?httpsRequest:httpRequest)({method,hostname:base.hostname,port:base.port||undefined,path:path+(qs?'?'+qs:''),headers:h},async res=>{
    const status=res.statusCode??0;if(status>=200&&status<300)return ok(res);
    const text=method==='HEAD'?(res.resume(),''):await readAll(res).catch(()=>'');
    const code=tag(text,'Code')??(status===404?'NotFound':status===412?'PreconditionFailed':status===503?'ServiceUnavailable':String(status));
    no(new S3Error(status,code,tag(text,'Message')??`${method} ${path}: ${status} ${code}`))});
   req.on('error',no);req.end(body)})
 }
 async function send(method:string,path:string,q:Record<string,string>={},headers:Record<string,string>={},body?:Buffer){
  for(let attempt=0;;attempt++){
   try{return await once(method,path,q,headers,body)}
   catch(e:any){const retry=e instanceof S3Error?e.$metadata.httpStatusCode>=500||e.$metadata.httpStatusCode===429||RETRY_CODES.has(e.Code):NET_ERRORS.has(e?.code);
    if(!retry||attempt+1>=ATTEMPTS)throw e;await backoff(attempt)}}
 }
 const text=async(r:Promise<IncomingMessage>)=>readAll(await r);
 // A 200 response can still carry an error for Copy and CompleteMultipartUpload.
 const checked=(xml:string)=>{const code=tag(xml,'Code');if(/<Error>/.test(xml)&&code)throw new S3Error(200,code,tag(xml,'Message')??code);return xml};

 return {
  put:async(bucket:string,key:string,body:Buffer,contentType?:string)=>{(await send('PUT',pathOf(bucket,key),{},contentType?{'content-type':contentType}:{},body)).resume()},
  head:async(bucket:string,key:string)=>{const r=await send('HEAD',pathOf(bucket,key));r.resume();return {contentLength:Number(r.headers['content-length']),etag:r.headers.etag}},
  get:async(bucket:string,key:string):Promise<Readable>=>send('GET',pathOf(bucket,key)),
  delete:async(bucket:string,key:string)=>{(await send('DELETE',pathOf(bucket,key))).resume()},
  copy:async(bucket:string,key:string,source:string)=>{checked(await text(send('PUT',pathOf(bucket,key),{},{'x-amz-copy-source':enc(source,true)})))},
  createMultipartUpload:async(bucket:string,key:string,contentType?:string)=>{const id=tag(checked(await text(send('POST',pathOf(bucket,key),{uploads:''},contentType?{'content-type':contentType}:{}))),'UploadId');if(!id)throw new S3Error(200,'InvalidResponse','CreateMultipartUpload returned no UploadId');return id},
  uploadPartCopy:async(bucket:string,key:string,uploadId:string,partNumber:number,source:string,range:string)=>{const etag=tag(checked(await text(send('PUT',pathOf(bucket,key),{partNumber:String(partNumber),uploadId},{'x-amz-copy-source':enc(source,true),'x-amz-copy-source-range':range}))),'ETag');if(!etag)throw new S3Error(200,'InvalidResponse','UploadPartCopy returned no ETag');return etag},
  completeMultipartUpload:async(bucket:string,key:string,uploadId:string,parts:Part[])=>{const xml=`<CompleteMultipartUpload>${parts.map(p=>`<Part><PartNumber>${p.PartNumber}</PartNumber><ETag>${xmlEsc(p.ETag)}</ETag></Part>`).join('')}</CompleteMultipartUpload>`;
   checked(await text(send('POST',pathOf(bucket,key),{uploadId},{'content-type':'application/xml'},Buffer.from(xml))))},
  abortMultipartUpload:async(bucket:string,key:string,uploadId:string)=>{(await send('DELETE',pathOf(bucket,key),{uploadId})).resume()},
  // Presigned GET (optionally with response-content-type) or UploadPart PUT, for executors; Access headers are theirs to add.
  presign:(method:'GET'|'PUT',bucket:string,key:string,expires:number,q:Record<string,string>={})=>{const path=pathOf(bucket,key);
   return `${base.protocol}//${host}${path}?${presignQuery({method,host,path,query:q,credentials:o.credentials,region,date:amzDate(),expires})}`},
 };
}
export type S3=ReturnType<typeof s3Client>;
