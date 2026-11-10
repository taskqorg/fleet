// Fake S3 for outside tests and app checks: objects in memory, multipart uploads, Range reads; every request must carry a
// SigV4 signature (header or query). The signer itself is checked against the AWS vector in go test.
import {randomUUID} from 'node:crypto';
import {createServer} from 'node:http';

export async function fakeS3() {
const objects = new Map<string, Buffer>(), uploads = new Map<string, Map<number, Buffer>>();
const s3 = createServer(async (req, res) => {
  const u = new URL(req.url!, 'http://x'), key = decodeURIComponent(u.pathname), q = u.searchParams;
  const chunks: Buffer[] = []; for await (const c of req) chunks.push(c as Buffer); const body = Buffer.concat(chunks);
  const signed = /^AWS4-HMAC-SHA256 Credential=\S+, SignedHeaders=\S*host\S*, Signature=[0-9a-f]{64}$/.test(String(req.headers.authorization)) || /^[0-9a-f]{64}$/.test(q.get('X-Amz-Signature') ?? '');
  if (!signed) { res.writeHead(403); return res.end('<Error><Code>SignatureDoesNotMatch</Code></Error>'); }
  if (req.method === 'POST' && q.has('uploads')) { const id = randomUUID(); uploads.set(id, new Map()); return res.end(`<InitiateMultipartUploadResult><UploadId>${id}</UploadId></InitiateMultipartUploadResult>`); }
  if (req.method === 'PUT' && q.has('partNumber')) { uploads.get(q.get('uploadId')!)!.set(Number(q.get('partNumber')), body); res.setHeader('ETag', '"e' + q.get('partNumber') + '"'); return res.end(); }
  if (req.method === 'POST' && q.has('uploadId')) { const parts = uploads.get(q.get('uploadId')!)!; objects.set(key, Buffer.concat([...parts.keys()].sort((a, b) => a - b).map(n => parts.get(n)!))); uploads.delete(q.get('uploadId')!); return res.end('<CompleteMultipartUploadResult/>'); }
  if (req.method === 'DELETE' && q.has('uploadId')) { uploads.delete(q.get('uploadId')!); res.writeHead(204); return res.end(); }
  if (req.method === 'PUT') { const src = req.headers['x-amz-copy-source']; objects.set(key, src ? objects.get('/' + decodeURIComponent(String(src)))! : body); return res.end(src ? '<CopyObjectResult/>' : ''); }
  if (req.method === 'DELETE') { objects.delete(key); res.writeHead(204); return res.end(); }
  const o = objects.get(key); if (!o) { res.writeHead(404); return res.end(req.method === 'HEAD' ? undefined : '<Error><Code>NoSuchKey</Code></Error>'); }
  const m = /^bytes=(\d+)-(\d+)$/.exec(String(req.headers.range ?? ''));
  if (m) { const part = o.subarray(Number(m[1]), Number(m[2]) + 1); res.writeHead(206, {'content-range': `bytes ${m[1]}-${m[2]}/${o.length}`, 'content-length': part.length}); return res.end(part); }
  res.writeHead(200, {'content-length': o.length}); res.end(req.method === 'HEAD' ? undefined : o);
});
const port = await new Promise<number>(r => s3.listen(0, '127.0.0.1', () => r((s3.address() as any).port)));
  return {port, objects, close: () => s3.close()};
}