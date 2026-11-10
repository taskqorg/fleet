// Planted defects for the trial slice (docs/designs/fleet-stack.md#trial-slice-l1): each one is applied to the current
// TypeScript coordinator alone, the outside tests run through run.ts, the file is restored. A defect is caught when the
// test it names turns red. Prints one line per defect and exits 1 if any defect is missed or the file cannot be patched.
//   node tests/trial/defects.ts            all defects
//   node tests/trial/defects.ts P3 P7      some of them
import {execFileSync} from 'node:child_process';
import {readFileSync, writeFileSync} from 'node:fs';
import {join} from 'node:path';

const root = new URL('../../', import.meta.url).pathname;
type Defect = {id: string, what: string, file: string, find: string, replace: string, expect: string, also?: [string, string]};
export const DEFECTS: Defect[] = [
  {id: 'P1', what: 'seam: the event payload field data is stored as item (the ev.item / ev.data rename)', file: 'recovery.ts',
    find: 'value={...ev,id,executionId', replace: 'value={...ev,data:undefined,item:ev.data,id,executionId', expect: 'SL7'},
  {id: 'P2', what: 'seam, outbox rule: an unknown host secret is answered 403 instead of 401 (the executor drops the row)', file: 'app.ts',
    find: "if(!h)fail(401,'Unauthorized')", replace: "if(!h)fail(403,'Unauthorized')", expect: 'SL2'},
  {id: 'P3', what: 'seam, outbox rule: an unknown host secret is answered 200 with an empty poll (the executor marks the row sent)', file: 'app.ts',
    find: "if(!h)fail(401,'Unauthorized')", replace: "if(!h)return reply.code(200).send({commands:[]})", expect: 'SL2'},
  {id: 'P4', what: 'transaction: host events are written one by one while the batch is still being checked (partial batch)', file: 'recovery.ts',
    find: 'else prepared.push({value});', replace: 'else {await put(kindOf(value),value);prepared.push({value})}', expect: 'SL7'},
  {id: 'P5', what: 'transaction: write routes run without their per-request transaction, and the message route without its own', file: 'app.ts',
    find: ':function(this:any,req:any,reply:any){return store.atomic(()=>h.call(this,req,reply))}});', replace: ':h});', expect: 'SL10',
    also: ["app.post('/api/sessions/:id/messages',async(req:any)=>(await store.tx(async ()=>{", "app.post('/api/sessions/:id/messages',async(req:any)=>(await (async (f:any)=>f())(async ()=>{"]},
  {id: 'P6', what: 'transaction: write transactions take no scope lock (no queue, no advisory lock)', file: 'store.ts',
    find: 'if(mode===\'write\')await lockIn(t,lock??GLOBAL);', replace: '', expect: 'SL10'},
  {id: 'P7', what: 'ordering: execution events are returned in insertion order, not seq order', file: 'app.ts',
    find: "x.type!=='execution.ended').sort((a,b)=>a.seq-b.seq)", replace: "x.type!=='execution.ended')", expect: 'SL7'},
  {id: 'P8', what: 'ordering: a stream resumed from a cursor replays the change at the cursor', file: 'store.ts',
    find: "' AND (xid, seq) > ($1::xid8", replace: "' AND (xid, seq) >= ($1::xid8", expect: 'SL8'},
  {id: 'P9', what: 'authorization header: any scheme is accepted in front of the secret', file: 'app.ts',
    find: ".replace(/^Bearer /,'')", replace: ".replace(/^\\w+ /,'')", expect: 'SL1'},
  {id: 'P10', what: 'authorization: a host request may carry a person', file: 'app.ts',
    find: "if(named!==undefined)fail(403,'Host requests carry no person');", replace: '', expect: 'SL2'},
  {id: 'P11', what: 'authorization: a body hostId of another host is silently replaced', file: 'app.ts',
    find: "if(req.body.hostId!==undefined&&req.body.hostId!==h.id)fail(403,'Host rejected');", replace: '', expect: 'SL2'},
  {id: 'P12', what: 'authorization: a poll returns pending commands of every host', file: 'app.ts',
    find: "x.hostId===req.body.hostId&&x.status==='pending'", replace: "x.status==='pending'", expect: 'SL5',
    also: ["all('command',{hostId:req.body.hostId,status:'pending'})", "all('command',{status:'pending'})"]},
  {id: 'P13', what: 'row-level security: a person without grants works in the operator scope', file: 'app.ts',
    find: 'if(!p||p.owner)return null;', replace: 'return null;', expect: 'SL9'},
  {id: 'P14', what: 'receipts: a status regression is accepted', file: 'app.ts',
    find: 'if(ranks[b.status]<ranks[c.status]||', replace: 'if(false||', expect: 'SL6'},
  {id: 'P15', what: 'SSE: change frames carry no id line', file: 'stream.ts',
    find: "o.write(`${id!==undefined?`id: ${id}\\n`:''}", replace: "o.write(`${''}", expect: 'SL8'},
  {id: 'P16', what: 'SSE: the stream is served as text/plain', file: 'stream.ts',
    find: "'Content-Type':'text/event-stream; charset=utf-8'", replace: "'Content-Type':'text/plain; charset=utf-8'", expect: 'SL8'},
  {id: 'P17', what: 'LISTEN: the stream listens on the wrong channel, so no NOTIFY wakes it', file: 'stream.ts',
    find: "store.listen('fleet_change',wake)", replace: "store.listen('fleet_changes',wake)", expect: 'SL8'},
];

const only = process.argv.slice(2);
// A defect counts as caught only against a green baseline.
try { execFileSync(process.execPath, [join(root, 'tests/trial/run.ts')], {cwd: root, stdio: 'ignore'}); }
catch { console.log('baseline is red: node tests/trial/run.ts must pass before defects are planted'); process.exit(1); }
let missed = 0;
for (const d of DEFECTS.filter(d => !only.length || only.includes(d.id))) {
  const path = join(root, 'packages/coordinator', d.file), original = readFileSync(path, 'utf8');
  const edits = [[d.find, d.replace], ...(d.also ? [d.also] : [])];
  if (edits.some(([find]) => original.split(find).length !== 2)) { console.log(`${d.id} NOT-APPLIED: a text is not found exactly once in ${d.file}`); missed++; continue; }
  writeFileSync(path, edits.reduce((text, [find, replace]) => text.replace(find, () => replace), original));
  let out = '';
  try { out = execFileSync(process.execPath, [join(root, 'tests/trial/run.ts')], {cwd: root, encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe']}); }
  catch (e: any) { out = String(e.stdout ?? '') + String(e.stderr ?? ''); }
  finally { writeFileSync(path, original); }
  const red = [...new Set([...out.matchAll(/^not ok \d+ - (SL\d+)/gm)].map(m => m[1]))];
  const caught = red.includes(d.expect);
  if (!caught) missed++;
  console.log(`${d.id} ${caught ? 'CAUGHT' : 'MISSED'} expect=${d.expect} red=${red.join(',') || (/trial-run/.test(out) ? 'none' : 'server-did-not-start')} ${d.what}`);
}
process.exit(missed ? 1 : 0);
