import {test} from 'node:test';
import assert from 'node:assert/strict';
import {PublicationHistory,type JournalPublication,type LeafState} from '../performance/publication-proof.ts';
const epoch='11111111-1111-1111-1111-111111111111';
const leaf=(execution:string,calls=1,errors=0,included=true):LeafState=>({execution,included,calls,errors,sourceSHA:'sha-'+execution,op:included?'I':'D'});
const row=(revision:number,calls:number,errors:number,leaves:LeafState[],extra:Partial<JournalPublication>={}):JournalPublication=>({project:'p',epoch,revision:String(revision),previous:revision===10?null:String(revision-1),anchor:revision===10,transaction:String(revision+100),calls,errors,leaves,...extra});
const fact=(execution:string,calls=1,errors=0)=>({project:'p',execution,calls,errors});
const version=(revision:number)=>epoch+':'+revision;

// Requirement: exact OLD painted versions survive delayed proof RPCs. Risk: CURRENT-version
// sampling inflates every burst leaf to the final paint or accepts a merely equal counter.
test('immutable committed history proves old paints and excludes withheld or future leaves',()=>{
 const h=new PublicationHistory();h.ingest(row(10,0,0,[]));h.ingest(row(11,1,1,[leaf('A',1,1)]));h.ingest(row(12,2,1,[leaf('B')]));
 assert.equal(h.check(fact('A',1,1),version(11),'1 · 1 failed','distinct').accepted,true);
 assert.equal(h.check(fact('B'),version(11),'1 · 1 failed','distinct').accepted,false);
 assert.equal(h.check(fact('withheld'),version(12),'2 · 1 failed','distinct').accepted,false);
 assert.equal(h.check(fact('B'),version(12),'2 · 1 failed','distinct').accepted,true);
 assert.equal(h.check(fact('A',1,1),version(12),'2 · 0 failed','distinct').reason,'publication-text-mismatch');
 assert.equal(h.check(fact('A',1,1),version(13),'3 · 1 failed','distinct').reason,'unknown-publication');
 assert.equal(h.check({...fact('A',1,1),project:'foreign'},version(11),'1 · 1 failed','distinct').accepted,false);
});

// Requirement: complete insert/update/delete histories and exact leaf values. Risk: a deleted
// or changed leaf remains acknowledged from an older proof or a newer aggregate alone.
test('updates and deletions retain exact historical leaf states and hot prefix semantics',()=>{
 const h=new PublicationHistory();h.ingest(row(10,0,0,[]));h.ingest(row(11,1,1,[leaf('A',1,1)]));h.ingest(row(12,2,1,[leaf('B')]));
 h.ingest(row(13,4,2,[leaf('A',0,0,false),{...leaf('A',3,2),op:'U'}]));
 assert.equal(h.check(fact('A',1,1),version(13),'4 · 2 failed','distinct').reason,'leaf-facts-mismatch');
 assert.equal(h.check(fact('A',3,2),version(13),'4 · 2 failed','distinct').accepted,true);
 assert.equal(h.check(fact('A',1,1),version(13),'4 · 2 failed','hot').accepted,true);
 h.ingest(row(14,1,0,[leaf('A',0,0,false)]));
 assert.equal(h.check(fact('A',3,2),version(14),'1 · 0 failed','distinct').reason,'leaf-not-included');
 assert.equal(h.check(fact('B'),version(14),'1 · 0 failed','distinct').accepted,true);
 assert.equal(h.check(fact('A',1,1),version(11),'1 · 1 failed','distinct').accepted,true);
});

// Requirement: missing/contradictory/rolled-back/unanchored publications never become evidence.
// Risk: gaps or resets invent inclusion. Real SQL rollback is tested separately.
test('journal gaps, bare counters, duplicate contradictions and epoch resets fail closed',()=>{
 const h=new PublicationHistory();h.ingest(row(10,0,0,[]));h.ingest(row(11,1,0,[leaf('A')]));h.ingest(row(11,1,0,[leaf('A')]));
 assert.equal(h.check(fact('A'),version(11),'1 · 0 failed','distinct').accepted,true);
 h.ingest(row(13,2,0,[leaf('B')],{previous:'11'}));assert.equal(h.check(fact('B'),version(13),'2 · 0 failed','distinct').reason,'publication-history-gap');
 const counter=new PublicationHistory();counter.ingest(row(10,0,0,[]));counter.ingest(row(11,1,0,[]));
 assert.equal(counter.check(fact('A'),version(11),'1 · 0 failed','distinct').reason,'publication-ledger-mismatch');
 const contradictory=new PublicationHistory();contradictory.ingest(row(10,0,0,[]));contradictory.ingest(row(10,1,0,[leaf('A')]));
 assert.equal(contradictory.check(fact('A'),version(10),'1 · 0 failed','distinct').reason,'contradictory-publication');
 const reset=new PublicationHistory();reset.ingest(row(1,1,0,[leaf('A')],{epoch:'22222222-2222-2222-2222-222222222222',previous:null,anchor:false}));
 assert.equal(reset.check(fact('A'),'22222222-2222-2222-2222-222222222222:1','1 · 0 failed','distinct').reason,'missing-anchor');
 assert.throws(()=>reset.ingest(row(-1,0,0,[])),/invalid journal identity/);
});

// Requirement: delayed proof/transport retries preserve the original actual observation bound.
// Risk: a successful late acknowledgement overwrites first receipt or accepts changed old text.
test('observation retry retains first receipt and immutable body without blocking new samples',async()=>{
 const {ObservationBook}=await import('../performance/publication-proof.ts');const book=new ObservationBook();
 const a={observation:'a',reader:'r',run:'run',project:'p',version:version(11),text:'1 · 1 failed',value:1,errors:1,visible:true,browserMonotonic:10,browserWall:100,domNodes:10,longTasks:[]};
 assert.equal(book.receive(a,1000).firstReceipt,1000);
 const b={...a,observation:'b',version:version(12),text:'2 · 1 failed',value:2,browserMonotonic:20};
 assert.equal(book.receive(b,1100).firstReceipt,1100,'a pending proof cannot prevent observing b');
 const retry=book.receive(a,9000);assert.equal(retry.firstReceipt,1000);assert.equal(retry.attempts,2);assert.equal(retry.consistent,true);
 assert.equal(book.receive({...a,text:'2 · 0 failed'},9100).consistent,false);
 assert.equal(book.receive(a,9200).firstReceipt,1000);
});

// Requirement: a short1000-burst diagnostic cannot replace the declared full envelope or dev1.
// Risk: shortened schedules or percentile results silently weaken qualification.
test('controlled burst is explicit and preserves independent source/original-window/max guards',async()=>{
 const {workload,sourceRate,stats,hasRenderFailure}=await import('../performance/interface.ts');
 const diagnostic={mode:'controlled-burst' as const,sourceDistribution:'distinct' as const,readers:1,rate:1,warmup:3,duration:0,burst:1000,recovery:10};
 const spec=workload(diagnostic);assert.equal(spec.mode,'controlled-burst');
 assert.throws(()=>workload({...diagnostic,mode:'connected'}));assert.throws(()=>workload(diagnostic,true));assert.throws(()=>workload({...diagnostic,sourceDistribution:'hot'}));assert.throws(()=>workload({...diagnostic,burst:999}));
 const windows=[{phase:'warmup' as const,plannedStart:0,plannedEnd:3000,actualStart:0,actualEnd:3000,startOvershootMs:0,endOvershootMs:0,offered:3,expected:3,offering:'paced' as const},{phase:'sustained' as const,plannedStart:3000,plannedEnd:3000,actualStart:3000,actualEnd:3000,startOvershootMs:0,endOvershootMs:0,offered:0,expected:0,offering:'paced' as const},{phase:'burst' as const,plannedStart:3000,plannedEnd:4000,actualStart:3000,actualEnd:4000,startOvershootMs:0,endOvershootMs:0,offered:1000,expected:1000,offering:'early' as const}];
 const facts=Array.from({length:1000},(_,i)=>({id:'f'+i,execution:'e'+i,project:'p',n:i+1,calls:1,errors:0,lower:3000,upper:3500,phase:'burst',due:3000,transaction:'tx'+i}));
 const valid=sourceRate(facts,spec,1,windows);assert.equal(valid.status,'qualified');assert.equal(valid.phases[0].notRun,true);
 assert.equal(sourceRate(facts.slice(1),spec,1,windows).status,'invalid-source-rate');
 assert.equal(sourceRate(facts.map(f=>({...f,transaction:'same'})),spec,1,windows).status,'invalid-source-rate');
 assert.equal(sourceRate(facts.map(f=>({...f,upper:3999})),spec,1,windows).status,'invalid-source-rate','outward clock bounds must fit original1000ms');
 assert.equal(stats([5000,5000.01]).violations,1);
 assert.equal(hasRenderFailure('controlled-burst',[5000.01],0,0),true);assert.equal(hasRenderFailure('connected',[5000.01],0,0),true);assert.equal(hasRenderFailure('controlled-burst',[5000],0,0),false);assert.equal(hasRenderFailure('controlled-burst',[1],0,1),true);assert.equal(hasRenderFailure('controlled-burst',[1],1,0),true);assert.equal(hasRenderFailure('controlled-burst',[],0,0),true);assert.equal(hasRenderFailure('exceed-capacity',[5000.01],0,0),false);
});

// Requirement: publication identity retains every bigint revision. Risk: numeric rounding acknowledges another revision.
test('publication revisions above safe integer retain exact ordering',()=>{
 const h=new PublicationHistory();h.ingest({...row(10,0,0,[]),revision:'9007199254740993'});
 h.ingest({...row(11,1,0,[leaf('A')]),revision:'9007199254740994',previous:'9007199254740993'});
 assert.equal(h.latest('p',epoch),'9007199254740994');
 assert.equal(h.check(fact('A'),epoch+':9007199254740994','1 · 0 failed','distinct').accepted,true);
 assert.equal(h.check(fact('A'),epoch+':9007199254740993','0 · 0 failed','distinct').accepted,false);
});

// Requirement: first proven receipt survives source/proof completion reordering.
// Risk: a newer paint wins while an older proven observation awaits source confirmation.
test('render bounds retain pending early observations and correct late old proofs',async()=>{
 const {RenderBounds}=await import('../performance/publication-proof.ts');const b=new RenderBounds<string>();
 b.offer('r/f',100,'old paint before source receipt');assert.equal(b.offer('r/f',200,'new paint'),false);
 assert.equal(b.confirm('r/f')?.receipt,100);assert.equal(b.entries.get('r/f')?.value,'old paint before source receipt');
 b.offer('r/g',300,'new proof finished first');b.confirm('r/g');assert.equal(b.offer('r/g',150,'old proof finished late'),true);
 assert.equal(b.entries.get('r/g')?.confirmed,true);assert.equal(b.confirm('r/g')?.receipt,150);
 assert.equal(b.entries.size,2,'correction must not duplicate samples');
});
