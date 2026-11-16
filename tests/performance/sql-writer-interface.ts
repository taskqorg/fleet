// Fixture-only FIFO psql transport. Every run keeps its own script, transaction and tagged receipt.
import {spawn} from 'node:child_process';
import {randomUUID} from 'node:crypto';
import {createInterface} from 'node:readline';
import {literal} from './seed-interface.ts';

export class Writer {
 readonly child: ReturnType<typeof spawn>;
 readonly stopped: Promise<void>;
 private pending = new Map<string,{resolve:(v:any)=>void;reject:(e:Error)=>void}>();
 private writes: Promise<void> = Promise.resolve();
 private failure: Error | null = null;
 private closing = false;
 constructor(db:string,log:NodeJS.WritableStream) {
  this.child=spawn('psql',['-X','-qAt','-v','ON_ERROR_STOP=1','-h',process.env.FLEET_TRIAL_PGDATA!,'-p',process.env.FLEET_TRIAL_PGPORT??'54329','-U','fleet','-d',db],{stdio:['pipe','pipe','pipe']});
  this.child.stderr!.pipe(log,{end:false});
  const lines=createInterface({input:this.child.stdout!});
  lines.on('line',line=>{
   if(this.failure||!line.startsWith('{'))return;
   try {
    const value=JSON.parse(line),entry=this.pending.get(value.tag);
    if(!entry||typeof value.afterCommit!=='string'||!Number.isSafeInteger(value.backend)||value.backend<=0)throw Error('invalid SQL receipt');
    this.pending.delete(value.tag);entry.resolve(value);
   }catch(e){this.cancel(e instanceof Error?e:Error(String(e)));}
  });
  this.child.on('error',e=>{this.fail(e);});
  this.child.stdin!.on('error',e=>{this.cancel(e);});
  this.stopped=new Promise(resolve=>this.child.once('close',code=>{
   lines.close();
   if(this.pending.size)this.fail(Error('writer closed with unresolved receipts (exit '+code+')'));
   this.closing=true;resolve();
  }));
  this.enqueue("SET fleet.project_ids='*';\n");
 }
 private fail(error:Error) {
  this.failure??=error;
  for(const entry of this.pending.values())entry.reject(this.failure);
  this.pending.clear();
 }
 private enqueue(text:string) {
  this.writes=this.writes.then(async()=>{
   if(this.failure)throw this.failure;
   if(this.child.stdin!.write(text))return;
   await new Promise<void>((resolve,reject)=>{
    const stream=this.child.stdin!;
    const done=()=>{cleanup();resolve();},bad=(e:Error)=>{cleanup();reject(e);},closed=()=>bad(Error('writer stdin closed before drain'));
    const cleanup=()=>{stream.off('drain',done);stream.off('error',bad);stream.off('close',closed);};
    stream.once('drain',done);stream.once('error',bad);stream.once('close',closed);
    if(stream.destroyed||this.failure)bad(this.failure??Error('writer stdin unavailable'));
   });
  }).catch(e=>{this.cancel(e instanceof Error?e:Error(String(e)));});
 }
 run(sql:string,payload='NULL'):Promise<any> {
  if(this.failure||this.closing)return Promise.reject(this.failure??Error('writer is closing'));
  const tag=randomUUID();
  const receipt=new Promise((resolve,reject)=>this.pending.set(tag,{resolve,reject}));
  // Submission includes this FIFO and stdin backpressure; never wait for an earlier receipt to queue the next script.
  this.enqueue(sql+`\nSELECT json_build_object('tag',${literal(tag)},'afterCommit',clock_timestamp(),'backend',pg_backend_pid(),'payload',${payload});\n`);
  return receipt;
 }
 cancel(error=Error('writer cancelled')) {
  if(!this.failure){this.fail(error);this.child.stdin!.destroy();this.child.kill('SIGTERM');}
  return this.stopped;
 }
 async close() {
  this.closing=true;await this.writes;
  if(!this.child.stdin!.destroyed)this.child.stdin!.end();
  await this.stopped;
 }
}
