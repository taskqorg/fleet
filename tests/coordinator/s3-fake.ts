// In-process S3 store for tests: path-style requests, the subset Fleet uses (multipart upload, part copy, copy, HEAD, GET with
// Range, DELETE, ListObjectsV2). Signatures are not checked; every request is recorded so tests can check signed headers and
// Cloudflare Access headers. requireAccess makes it refuse requests without the Access headers, as the tunnel does.
import {createServer,type IncomingMessage,type ServerResponse} from 'node:http';
import {createHash,randomUUID} from 'node:crypto';
import {signature} from '../../packages/coordinator/s3.ts';
export type FakeStore={url:string,objects:Map<string,Buffer>,requests:{method:string,url:string,headers:Record<string,any>}[],close:()=>Promise<void>,uploads:Map<string,{key:string,parts:Map<number,Buffer>}>,down:boolean};
const xml=(res:ServerResponse,status:number,body:string)=>{res.writeHead(status,{'content-type':'application/xml'});res.end('<?xml version="1.0" encoding="UTF-8"?>'+body)};
const etag=(b:Buffer)=>'"'+createHash('md5').update(b).digest('hex')+'"';
const body=(req:IncomingMessage)=>new Promise<Buffer>(r=>{const c:Buffer[]=[];req.on('data',d=>c.push(d));req.on('end',()=>r(Buffer.concat(c)))});
// dropHeadLength: answer HEAD without Content-Length, as the Cloudflare tunnel does for compressed text types.
// rewriteEncoding: change Accept-Encoding on the way in, as the Cloudflare tunnel does. verify: check the SigV4 signature
// over the headers as they arrive (a signed header a proxy rewrote fails with 403, as Garage answered after abcf10c).
export async function fakeStore(opts:{requireAccess?:{id:string,secret:string},dropHeadLength?:boolean,rewriteEncoding?:boolean,verify?:{credentials:{accessKeyId:string,secretAccessKey:string},region:string}}={}):Promise<FakeStore>{
 const objects=new Map<string,Buffer>(),uploads=new Map<string,{key:string,parts:Map<number,Buffer>}>(),requests:FakeStore['requests']=[];
 const state:any={down:false};
 const server=createServer(async(req,res)=>{
  const u=new URL(req.url!,'http://x'),[bucket,...rest]=u.pathname.slice(1).split('/'),key=decodeURIComponent(rest.join('/'));
  requests.push({method:req.method!,url:req.url!,headers:{...req.headers}});const data=await body(req);
  if(state.down){res.writeHead(503);res.end();return}
  if(opts.rewriteEncoding)req.headers['accept-encoding']='gzip, br';
  if(opts.verify){const auth=String(req.headers.authorization??''),names=/SignedHeaders=([^,]+)/.exec(auth)?.[1].split(';')??[],given=/Signature=([0-9a-f]+)/.exec(auth)?.[1];
   const path=req.url!.split('?')[0],query=Object.fromEntries(new URL(req.url!,'http://x').searchParams);
   const {signature:want}=signature({method:req.method!,path,query,headers:Object.fromEntries(names.map(n=>[n,String(req.headers[n]??'')])),payload:String(req.headers['x-amz-content-sha256']),credentials:opts.verify.credentials,region:opts.verify.region,date:String(req.headers['x-amz-date'])});
   if(want!==given){res.writeHead(403,{'content-type':'application/xml'});res.end('<Error><Code>SignatureDoesNotMatch</Code><Message>Invalid signature</Message></Error>');return}}
  if(opts.requireAccess&&(req.headers['cf-access-client-id']!==opts.requireAccess.id||req.headers['cf-access-client-secret']!==opts.requireAccess.secret)){res.writeHead(302,{location:'https://access.example/login'});res.end();return}
  const q=u.searchParams;
  if(req.method==='GET'&&!key&&q.get('list-type')==='2'){const prefix=q.get('prefix')??'';const keys=[...objects.keys()].filter(k=>k.startsWith(prefix)).sort();
   return xml(res,200,`<ListBucketResult><Name>${bucket}</Name><Prefix>${prefix}</Prefix><KeyCount>${keys.length}</KeyCount><IsTruncated>false</IsTruncated>${keys.map(k=>`<Contents><Key>${k}</Key><Size>${objects.get(k)!.length}</Size></Contents>`).join('')}</ListBucketResult>`)}
  if(req.method==='POST'&&q.has('uploads')){const id=randomUUID();uploads.set(id,{key,parts:new Map()});return xml(res,200,`<InitiateMultipartUploadResult><Bucket>${bucket}</Bucket><Key>${key}</Key><UploadId>${id}</UploadId></InitiateMultipartUploadResult>`)}
  if(req.method==='PUT'&&q.has('uploadId')){const up=uploads.get(q.get('uploadId')!);if(!up){res.writeHead(404);res.end();return}
   let part=data;const src=req.headers['x-amz-copy-source'];
   if(src){const s=objects.get(decodeURIComponent(String(src)).replace(/^\/?[^/]+\//,''));if(!s){res.writeHead(404);res.end();return}const range=/bytes=(\d+)-(\d+)/.exec(String(req.headers['x-amz-copy-source-range']??''));part=range?s.subarray(Number(range[1]),Number(range[2])+1):s;up.parts.set(Number(q.get('partNumber')),part);return xml(res,200,`<CopyPartResult><ETag>${etag(part)}</ETag></CopyPartResult>`)}
   up.parts.set(Number(q.get('partNumber')),part);res.writeHead(200,{etag:etag(part)});res.end();return}
  if(req.method==='POST'&&q.has('uploadId')){const up=uploads.get(q.get('uploadId')!);if(!up){res.writeHead(404);res.end();return}
   const numbers=[...data.toString().matchAll(/<PartNumber>(\d+)<\/PartNumber>/g)].map(m=>Number(m[1]));const parts=numbers.map(n=>up.parts.get(n));
   if(parts.some(p=>!p))return xml(res,400,'<Error><Code>InvalidPart</Code></Error>');const all=Buffer.concat(parts as Buffer[]);objects.set(up.key,all);uploads.delete(q.get('uploadId')!);
   return xml(res,200,`<CompleteMultipartUploadResult><Bucket>${bucket}</Bucket><Key>${up.key}</Key><ETag>${etag(all)}</ETag></CompleteMultipartUploadResult>`)}
  if(req.method==='DELETE'&&q.has('uploadId')){uploads.delete(q.get('uploadId')!);res.writeHead(204);res.end();return}
  if(req.method==='PUT'&&req.headers['x-amz-copy-source']){const s=objects.get(decodeURIComponent(String(req.headers['x-amz-copy-source'])).replace(/^\/?[^/]+\//,''));if(!s){res.writeHead(404);res.end();return}objects.set(key,Buffer.from(s));return xml(res,200,`<CopyObjectResult><ETag>${etag(s)}</ETag></CopyObjectResult>`)}
  if(req.method==='PUT'){objects.set(key,data);res.writeHead(200,{etag:etag(data)});res.end();return}
  const o=objects.get(key);
  if(req.method==='HEAD'){if(!o){res.writeHead(404);res.end();return}res.writeHead(200,opts.dropHeadLength?{etag:etag(o)}:{'content-length':o.length,etag:etag(o)});res.end();return}
  if(req.method==='GET'){if(!o){return xml(res,404,'<Error><Code>NoSuchKey</Code></Error>')}const range=/bytes=(\d+)-(\d*)/.exec(String(req.headers.range??''));
   const b=range?o.subarray(Number(range[1]),range[2]?Number(range[2])+1:o.length):o;res.writeHead(range?206:200,{'content-length':b.length,...(range?{'content-range':`bytes ${range[1]}-${Number(range[1])+b.length-1}/${o.length}`}:{}),'content-type':String(q.get('response-content-type')??'application/octet-stream')});res.end(b);return}
  if(req.method==='DELETE'){objects.delete(key);res.writeHead(204);res.end();return}
  res.writeHead(400);res.end()});
 // Tests that never close it must not keep the process alive.
 server.on('connection',sock=>sock.unref());
 await new Promise<void>(r=>server.listen(0,'127.0.0.1',r));server.unref();
 const fake:any={url:`http://127.0.0.1:${(server.address() as any).port}`,objects,requests,uploads,close:()=>new Promise<void>(r=>server.close(()=>r()))};
 Object.defineProperty(fake,'down',{get:()=>state.down,set:(v:boolean)=>{state.down=v}});
 return fake;
}
