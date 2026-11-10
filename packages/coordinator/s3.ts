// The S3 subset Fleet uses against Garage, signed with AWS Signature Version 4 (docs/designs/fleet-toolchain.md D3).
// It keeps what the @aws-sdk/client-s3 client was configured with: region 'garage', path-style URLs, no request or response
// checksums (WHEN_REQUIRED, and nothing we call requires one), Cloudflare Access headers added after signing so they are
// never part of the signature, and the SDK's standard retry: 3 attempts, jittered exponential backoff, on 500/502/503/504,
// 429, throttling codes and network errors, with the clock offset taken from the server's Date header after a skew error.
// There is no total request cap (a 5 GB CopyObject can take long before its response): node:http, not fetch, whose undici
// default stops at 300 s. Instead a socket idle timeout (no bytes either way for idleTimeoutMs, default 60 s, operator
// setting FLEET_S3_IDLE_TIMEOUT_MS) aborts a half-open tunnel and retries as a network error.
import {createHash,createHmac} from 'node:crypto';
import {request as httpRequest,type IncomingMessage} from 'node:http';
import {request as httpsRequest} from 'node:https';
import type {Readable} from 'node:stream';

export type Credentials={accessKeyId:string,secretAccessKey:string};
export type Part={partNumber:number,etag:string};
// status is the HTTP status, code the S3 error Code from the XML body (or the status when there is no body).
export class S3Error extends Error{
 readonly status:number;readonly code:string;
 constructor(status:number,code:string,message:string){super(message);this.name='S3Error';this.status=status;this.code=code}
}

const hash=(b:string|Buffer)=>createHash('sha256').update(b).digest('hex');
const hmac=(k:string|Buffer,s:string)=>createHmac('sha256',k).update(s).digest();
// RFC 3986 encoding as SigV4 requires; '/' stays literal in an object key path.
const enc=(s:string,slash=false)=>{const e=encodeURIComponent(s).replace(/[!'()*]/g,c=>'%'+c.charCodeAt(0).toString(16).toUpperCase());return slash?e.replace(/%2F/g,'/'):e};
// SigV4 sorts by the encoded name.
const query=(q:Record<string,string>)=>Object.entries(q).map(([k,v])=>[enc(k),enc(v)]).sort(([a],[b])=>a<b?-1:a>b?1:0).map(([k,v])=>k+'='+v).join('&');
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

const amzDate=(ms:number)=>new Date(ms).toISOString().replace(/[-:]|\.\d{3}/g,'');
const ENTITIES:Record<string,string>={quot:'"',apos:"'",lt:'<',gt:'>',amp:'&'};
// A numeric reference outside Unicode becomes U+FFFD: a store's reply must never throw here.
const point=(n:number)=>Number.isInteger(n)&&n>=0&&n<=0x10ffff?String.fromCodePoint(n):'\ufffd';
const unxml=(s:string)=>s.replace(/&(#x[0-9a-fA-F]+|#\d+|quot|apos|lt|gt|amp);/g,(_,e:string)=>e[0]==='#'?point(e[1]==='x'?parseInt(e.slice(2),16):Number(e.slice(1))):ENTITIES[e]);
const tag=(xml:string,name:string)=>{const v=new RegExp(`<${name}>([^<]*)</${name}>`).exec(xml)?.[1];return v===undefined?undefined:unxml(v)};
const xmlEsc=(s:string)=>s.replace(/&/g,'&amp;').replace(/</g,'&lt;').replace(/>/g,'&gt;').replace(/"/g,'&quot;');
const RETRY_STATUS=new Set([429,500,502,503,504]),RETRY_CODES=new Set(['SlowDown','Throttling','ThrottlingException','RequestTimeout','RequestTimeTooSkewed']);
const NET_ERRORS=new Set(['ECONNRESET','ECONNREFUSED','EPIPE','ETIMEDOUT','EAI_AGAIN','ECONNABORTED','EHOSTUNREACH','ENETUNREACH']);
const ATTEMPTS=3,BASE_MS=100,MAX_MS=20000;
const backoff=(attempt:number)=>new Promise(r=>setTimeout(r,Math.random()*Math.min(MAX_MS,BASE_MS*2**attempt)));
const readAll=(r:IncomingMessage)=>new Promise<string>((ok,no)=>{const c:Buffer[]=[];r.on('data',d=>c.push(d));r.on('end',()=>ok(Buffer.concat(c).toString()));r.on('error',no)});

export function s3Client(o:{endpoint:string,credentials:Credentials,region?:string,extraHeaders?:()=>Record<string,string>|null,idleTimeoutMs?:number}){
 const base=new URL(o.endpoint),region=o.region??'garage',https=base.protocol==='https:',hostname=base.hostname.replace(/^\[|\]$/g,'');
 const idle=o.idleTimeoutMs??(Number(process.env.FLEET_S3_IDLE_TIMEOUT_MS)||60000);
 // Server time minus local time, learnt from the Date header after RequestTimeTooSkewed, as the SDK does.
 let offset=0;const now=()=>Date.now()+offset;
 const host=base.port?`${base.hostname}:${base.port}`:base.hostname,prefix=base.pathname.replace(/\/$/,'');
 const pathOf=(bucket:string,key?:string)=>prefix+'/'+enc(bucket)+(key===undefined?'':'/'+enc(key,true));

 // One attempt; resolves with the response, or rejects with S3Error for a non-2xx status.
 function once(method:string,path:string,q:Record<string,string>,headers:Record<string,string>,body?:Buffer){
  const date=amzDate(now()),payload=body?hash(body):EMPTY;
  // Accept-Encoding stays outside the signature: the store tunnel rewrites it, and a signed copy would no longer match
  // ("Invalid signature" 403 on every HEAD and GET after abcf10c). Like the Access headers, it is added after signing.
  const {'accept-encoding':encoding,...signed}=headers;
  const h:Record<string,string>={...signed,host,'x-amz-date':date,'x-amz-content-sha256':payload};
  if(body)h['content-length']=String(body.length);
  h.authorization=signature({method,path,query:q,headers:h,payload,credentials:o.credentials,region,date}).authorization;
  Object.assign(h,o.extraHeaders?.()??{});if(encoding)h['accept-encoding']=encoding;
  const qs=query(q);
  return new Promise<IncomingMessage>((ok,no)=>{
   const req=(https?httpsRequest:httpRequest)({method,hostname,port:base.port||undefined,path:path+(qs?'?'+qs:''),headers:h},async res=>{try{
    const status=res.statusCode??0;if(status>=200&&status<300)return ok(res);
    const text=method==='HEAD'?(res.resume(),''):await readAll(res).catch(()=>'');
    const code=tag(text,'Code')??String(status);
    if(code==='RequestTimeTooSkewed'&&res.headers.date){const server=Date.parse(res.headers.date);if(!Number.isNaN(server))offset=server-Date.now()}
    no(new S3Error(status,code,tag(text,'Message')??`${method} ${path}: ${status} ${code}`))}catch(e){no(e)}});
   // Idle, not total: the timer restarts on every byte, so a long upload or download keeps going.
   req.setTimeout(idle,()=>req.destroy(Object.assign(new Error(`${method} ${path}: no data for ${idle} ms`),{code:'ETIMEDOUT'})));
   req.on('error',no);req.end(body)})
 }
 async function send(method:string,path:string,q:Record<string,string>={},headers:Record<string,string>={},body?:Buffer){
  for(let attempt=0;;attempt++){
   try{return await once(method,path,q,headers,body)}
   catch(e:any){const retry=e instanceof S3Error?RETRY_STATUS.has(e.status)||RETRY_CODES.has(e.code):NET_ERRORS.has(e?.code);
    if(!retry||attempt+1>=ATTEMPTS)throw e;await backoff(attempt)}}
 }
 const text=async(r:Promise<IncomingMessage>)=>readAll(await r);
 // A 200 response can still carry an error for Copy and CompleteMultipartUpload.
 const checked=(xml:string)=>{const code=tag(xml,'Code');if(/<Error>/.test(xml)&&code)throw new S3Error(200,code,tag(xml,'Message')??code);return xml};

 return {
  put:async(bucket:string,key:string,body:Buffer,contentType?:string)=>{(await send('PUT',pathOf(bucket,key),{},contentType?{'content-type':contentType}:{},body)).resume()},
  // null when the object does not exist.
  // A proxy in front of the store (the Cloudflare tunnel) may compress text types and then drop Content-Length from HEAD
  // (text/html artifacts failed with "size NaN"). Ask for identity encoding, and when the length is still missing read it
  // from the Content-Range of a one-byte GET.
  head:async(bucket:string,key:string)=>{try{const r=await send('HEAD',pathOf(bucket,key),{},{'accept-encoding':'identity'});r.resume();let n=Number(r.headers['content-length']);
   if(!Number.isSafeInteger(n)){const g=await send('GET',pathOf(bucket,key),{},{'accept-encoding':'identity',range:'bytes=0-0'});g.resume();n=Number(/\/(\d+)$/.exec(String(g.headers['content-range']??''))?.[1]??(g.statusCode===200?g.headers['content-length']:NaN))}
   return {contentLength:n,etag:r.headers.etag}}catch(e){if(e instanceof S3Error&&e.status===404)return null;if(e instanceof S3Error&&e.status===416)return {contentLength:0,etag:undefined};throw e}},
  // range: inclusive byte offsets [start, end] (an HTTP Range of one part), for paged reads of large objects.
  get:async(bucket:string,key:string,range?:[number,number]):Promise<Readable>=>send('GET',pathOf(bucket,key),{},{'accept-encoding':'identity',...(range?{range:`bytes=${range[0]}-${range[1]}`}:{})}),
  delete:async(bucket:string,key:string)=>{(await send('DELETE',pathOf(bucket,key))).resume()},
  copy:async(bucket:string,key:string,source:string)=>{checked(await text(send('PUT',pathOf(bucket,key),{},{'x-amz-copy-source':enc(source,true)})))},
  createMultipartUpload:async(bucket:string,key:string,contentType?:string)=>{const id=tag(checked(await text(send('POST',pathOf(bucket,key),{uploads:''},contentType?{'content-type':contentType}:{}))),'UploadId');if(!id)throw new S3Error(200,'InvalidResponse','CreateMultipartUpload returned no UploadId');return id},
  uploadPartCopy:async(bucket:string,key:string,uploadId:string,partNumber:number,source:string,range:string)=>{const etag=tag(checked(await text(send('PUT',pathOf(bucket,key),{partNumber:String(partNumber),uploadId},{'x-amz-copy-source':enc(source,true),'x-amz-copy-source-range':range}))),'ETag');if(!etag)throw new S3Error(200,'InvalidResponse','UploadPartCopy returned no ETag');return etag},
  completeMultipartUpload:async(bucket:string,key:string,uploadId:string,parts:Part[])=>{const xml=`<CompleteMultipartUpload>${parts.map(p=>`<Part><PartNumber>${p.partNumber}</PartNumber><ETag>${xmlEsc(p.etag)}</ETag></Part>`).join('')}</CompleteMultipartUpload>`;
   checked(await text(send('POST',pathOf(bucket,key),{uploadId},{'content-type':'application/xml'},Buffer.from(xml))))},
  abortMultipartUpload:async(bucket:string,key:string,uploadId:string)=>{(await send('DELETE',pathOf(bucket,key),{uploadId})).resume()},
  // Presigned GET (optionally with response-content-type) or UploadPart PUT, for executors; Access headers are theirs to add.
  presign:(method:'GET'|'PUT',bucket:string,key:string,expires:number,q:Record<string,string>={})=>{const path=pathOf(bucket,key);
   return `${base.protocol}//${host}${path}?${presignQuery({method,host,path,query:q,credentials:o.credentials,region,date:amzDate(now()),expires})}`},
 };
}
export type S3=ReturnType<typeof s3Client>;
