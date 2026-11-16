// Passed directly to page.addInitScript: no closure imports, new EventSource, HTTP or application callback replacement.
export function installEventSourceCapture({projects,maxBytes=64*1024*1024}:{projects:string[];maxBytes?:number}) {
 const Native=window.EventSource,documentId=crypto.randomUUID(),encoder=new TextEncoder();
 const urlOf=Object.getOwnPropertyDescriptor(Native.prototype,'url')!.get!,credentialsOf=Object.getOwnPropertyDescriptor(Native.prototype,'withCredentials')!.get!,stateOf=Object.getOwnPropertyDescriptor(Native.prototype,'readyState')!.get!;
 const add=EventTarget.prototype.addEventListener,remove=EventTarget.prototype.removeEventListener;
 let next=1,ack=0,bytes=0,frozen=false,observed=0,lost=0,nativeErrors=0;
 const rows:{ordinal:number;line:string;bytes:number;notification:boolean}[]=[],errors:string[]=[],sources:{id:number;native:EventSource;opens:number;remove:()=>void}[]=[];
 let firstLost:object|null=null;
 const append=(record:Record<string,unknown>,notification=false)=>{
  if(frozen)return;
  const ordinal=next++;observed++;
  const line=JSON.stringify({ordinal,documentId,provenance:record.trusted===false?'untrusted-eventsource-dispatch':'native-eventsource-callback',...record})+'\n',size=encoder.encode(line).length;
  if(bytes+size>maxBytes||lost){lost++;if(!firstLost){firstLost={ordinal,kind:record.kind,sourceId:record.sourceId,connectionId:record.connectionId,lastEventId:record.lastEventId,browserMonotonic:record.browserMonotonic,bytes:size};errors.push('native EventSource capture buffer full');}return;}
  rows.push({ordinal,line,bytes:size,notification});bytes+=size;
 };
 const scope=new Set(projects);
 window.EventSource=new Proxy(Native,{construct(target,args,newTarget){
  const native=Reflect.construct(target,args,newTarget) as EventSource;
  const url=urlOf.call(native) as string,withCredentials=credentialsOf.call(native) as boolean;
  if(new URL(url).origin!==location.origin||new URL(url).pathname!=='/api/stream')return native;
  const source={id:sources.length+1,native,opens:0,remove:()=>{}};sources.push(source);
  const base=()=>({sourceId:source.id,openGeneration:source.opens,connectionId:documentId+':'+source.id+':'+source.opens,url,withCredentials,browserMonotonic:performance.now(),browserWall:Date.now()});
  const open=(event:Event)=>{if(!event.isTrusted){errors.push('untrusted EventSource open');append({kind:'untrusted-open',...base(),trusted:false});return;}source.opens++;append({kind:'open',...base(),trusted:true,readyState:stateOf.call(native)});};
  const error=(event:Event)=>{if(!event.isTrusted){errors.push('untrusted EventSource error');append({kind:'untrusted-error',...base(),trusted:false});return;}nativeErrors++;append({kind:'error',...base(),trusted:true,readyState:stateOf.call(native)});};
  const message=(event:MessageEvent)=>{
   const record={kind:'message',...base(),trusted:event.isTrusted,lastEventId:event.lastEventId,data:event.data};
   if(!event.isTrusted){errors.push('untrusted EventSource message');append({...record,kind:'untrusted-message'});return;}
   let change:{sequence:string;project:string;op:unknown}|undefined;
   try {
    const parsed=JSON.parse(event.data);
    if(parsed.kind==='resource_summary'&&scope.has(parsed.id)){
     if(!(typeof parsed.seq==='string'&&/^\d+$/.test(parsed.seq)||Number.isSafeInteger(parsed.seq)&&parsed.seq>=0))throw Error('invalid resource-summary sequence');
     change={sequence:String(parsed.seq),project:parsed.id,op:parsed.op};
    }
   }catch(e){errors.push('native message capture: '+String(e));}
   append({...record,...(change?{change}:{})},!!change);
  };
  add.call(native,'open',open);add.call(native,'error',error);add.call(native,'message',message as EventListener);
  source.remove=()=>{remove.call(native,'open',open);remove.call(native,'error',error);remove.call(native,'message',message as EventListener);};
  return native;
 }});
 (window as any).__interfaceSSE={documentId,
  drain(acknowledged:number,freeze=false){
   if(!Number.isSafeInteger(acknowledged)||acknowledged<ack||acknowledged>=next){errors.push('invalid capture acknowledgement');}
   else {ack=acknowledged;while(rows.length&&rows[0].ordinal<=ack){bytes-=rows[0].bytes;rows.shift();}}
   if(freeze&&!frozen){frozen=true;for(const source of sources)source.remove();}
   const batch=rows.slice(0,256);
   return {documentId,first:batch[0]?.ordinal??null,last:batch.at(-1)?.ordinal??ack,events:batch.map(r=>r.line).join(''),notifications:batch.filter(r=>r.notification).map(r=>r.line).join(''),count:batch.length,notificationCount:batch.filter(r=>r.notification).length,remaining:rows.length-batch.length,observed,nativeErrors,acknowledged:ack,bufferBytes:bytes,maxBytes,frozen,errors:[...errors],lost,firstLost,sources:sources.map(s=>({sourceId:s.id,openGeneration:s.opens,url:urlOf.call(s.native),withCredentials:credentialsOf.call(s.native),readyState:stateOf.call(s.native)}))};
  }
 };
}

// Node-side evidence collector. Acknowledgement never precedes a successful evidence write.
import {createWriteStream,renameSync} from 'node:fs';
import {finished} from 'node:stream/promises';
import {resolve} from 'node:path';
import type {Page} from 'playwright';
type Reader={id:string;page:Page};
export class EventSourceRecorder {
 private streams:ReturnType<typeof createWriteStream>[];
 private cursors=new Map<string,{documentId:string;last:number}>();
 private documents=new Map<string,any>();
 private errors:string[]=[];
 private eventCount=0;
 private notificationCount=0;
 private timer:ReturnType<typeof setInterval>|undefined;
 private pumping:Promise<void>|undefined;
 private ended=false;
 private complete=false;
 private out:string;
 constructor(out:string){
  this.out=out;
  this.streams=['eventsource-events','notifications-received'].map(name=>createWriteStream(resolve(out,name+'.jsonl.partial'),{flags:'wx'}));
  for(const stream of this.streams)stream.on('error',e=>this.errors.push(String(e)));
 }
 private async write(index:number,text:string){
  const stream=this.streams[index];if(stream.errored||stream.destroyed)throw stream.errored??Error('event evidence stream unavailable');
  if(text)await new Promise<void>((resolve,reject)=>stream.write(text,error=>error?reject(error):resolve()));
  if(stream.errored)throw stream.errored;
 }
 async capture(reader:Reader,freeze=false){
  let target:number|undefined;
  try {for(;;){
   const cursor=this.cursors.get(reader.id);
   const batch=await reader.page.evaluate(({cursor,freeze})=>{
    const state=(window as any).__interfaceSSE;if(!state)throw Error('native EventSource capture missing');
    return state.drain(cursor&&cursor.documentId===state.documentId?cursor.last:0,freeze);
   },{cursor,freeze});
   target??=batch.observed;
   const current=cursor&&cursor.documentId===batch.documentId?cursor:{documentId:batch.documentId,last:0};
   this.documents.set(reader.id+'/'+batch.documentId,{reader:reader.id,...batch,events:undefined,notifications:undefined});
   if(batch.count){
    if(batch.first!==current.last+1||batch.last-batch.first+1!==batch.count)throw Error('native EventSource ordinal gap');
    const identify=(text:string)=>text.replace(/^\{/gm,'{\"reader\":'+JSON.stringify(reader.id)+',');
    await this.write(0,identify(batch.events));await this.write(1,identify(batch.notifications));
    this.eventCount+=batch.count;this.notificationCount+=batch.notificationCount;
    current.last=batch.last;this.cursors.set(reader.id,current);
   }
   if(batch.errors.length||batch.lost){this.errors.push(reader.id+': '+JSON.stringify({errors:batch.errors,lost:batch.lost,firstLost:batch.firstLost}));if(!batch.count||!batch.remaining)return;}
   if(freeze){if(!batch.count)return;}else if(!batch.count||current.last>=target!)return;
  }}catch(e){this.errors.push(reader.id+': '+String(e));throw e;}
 }
 start(readers:Reader[]){
  this.timer=setInterval(()=>{if(this.pumping)return;this.pumping=(async()=>{for(const reader of readers)await this.capture(reader);})().catch(()=>{}).finally(()=>{this.pumping=undefined;});},5000);
 }
 async stop(){clearInterval(this.timer);await this.pumping;}
 snapshot(){return {format:2,complete:this.complete,provenance:'native-eventsource-callback',eventCount:this.eventCount,count:this.notificationCount,documents:[...this.documents.values()],errors:[...this.errors]};}
 async finish(complete:boolean){
  if(this.ended)return;this.ended=true;
  for(const stream of this.streams)stream.end();
  try{await Promise.all(this.streams.map(stream=>finished(stream,{cleanup:true})));}catch(e){this.errors.push(String(e));if(complete)throw e;}
  if(complete&&(!this.documents.size||[...this.documents.values()].some(d=>!d.frozen||d.acknowledged!==d.observed||!d.sources.length)))this.errors.push('native EventSource document evidence unfinished or missing');
  if(complete&&[...this.documents.values()].some(d=>d.nativeErrors))this.errors.push('native EventSource error events observed');
  if(complete&&this.errors.length)throw Error('native EventSource capture incomplete: '+this.errors.join('; '));
  if(complete){try{for(const name of ['eventsource-events','notifications-received'])renameSync(resolve(this.out,name+'.jsonl.partial'),resolve(this.out,name+'.jsonl'));this.complete=true;}catch(e){this.errors.push(String(e));throw e;}}
 }
}
