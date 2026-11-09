// Realistic coordinator volume: 150 sessions, 200 executions, 200 commands, 3000 events (shapes as the live store holds them).
export function volume(hostId='mac'){const docs:[string,any][]=[];const sessions:string[]=[];
 for(let i=0;i<150;i++){const id='session-'+i;sessions.push(id);docs.push(['session',{id,createdAt:i}]);docs.push(['message',{id:'message-'+i,sessionId:id,executionId:'execution-'+i,requestId:'r'+i,text:'request '+i}])}
 for(let i=0;i<200;i++){const id='execution-'+i,sessionId=i>=100&&i<140?sessions[10]:sessions[i%150],done=i<190;
  docs.push(['execution',{id,executionId:id,sessionId,hostId,state:done?'completed':'queued',createdAt:i,model:'gpt-6.1-sol',effort:'low',...(done?{startedAt:i,finishedAt:i+1}:{})}]);
  docs.push(['command',{id:'command-'+i,executionId:id,sessionId,hostId,kind:'start',status:done?'completed':'pending',payload:{harness:'codex',text:'request '+i,model:'gpt-6.1-sol',effort:'low',instructions:''}}]);
  for(let e=0;e<15;e++)docs.push(['event',{id:`event:${id}:${e}`,executionId:id,sessionId,seq:e,type:e===0?'thread.started':e===14?'turn.completed':'item.completed',timestamp:e,data:{text:'x'.repeat(200)}}])}
 // 120 artifacts of ~100 KB (about 13 MB, as live); parents in sessions 0-19 delegate to children. Child 199 waits on inputs every
 // poll; its parent session (session-10) holds 40 runs without artifacts, so the old input lookup scans artifacts once per run.
 for(let i=0;i<120;i++){const executionId='execution-'+(i<60?40+i:80+i);docs.push(['artifact',{id:`artifact:${executionId}:notes-${i}.txt`,sha256:'0'.repeat(64),executionId,sessionId:docs.find(([k,d])=>k==='execution'&&d.id===executionId)![1].sessionId,path:`notes-${i}.txt`,mediaType:'text/plain',contentBase64:Buffer.alloc(75000,'a').toString('base64'),size:75000}])}
 for(let i=0;i<20;i++)docs.push(['delegation',{id:'delegation-'+i,parentExecutionId:'execution-'+i,childExecutionId:'execution-'+(i+20),parentSessionId:sessions[i],childSessionId:sessions[(i+20)%150],state:'result-retained',request:{profileId:'baseline',input:'review'},result:{executionId:'execution-'+(i+20),state:'completed',events:[],artifacts:[]}}])
 docs.push(['delegation',{id:'delegation-waiting',parentExecutionId:'execution-139',childExecutionId:'execution-199',parentSessionId:sessions[10],childSessionId:sessions[49],state:'requested',request:{profileId:'website-reviewer',input:'review'}}]);
 const child=docs.find(([k,d])=>k==='execution'&&d.id==='execution-199')![1];child.parentExecutionId='execution-139';
 for(let i=0;i<30;i++)docs.push(['queued-message',{id:'queued-'+i,sessionId:sessions[190%150+(i%5)],requestId:'q'+i,text:'later',state:'queued',createdAt:1000+i}]);
 return {docs,sessions};
}
