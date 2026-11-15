import {test} from 'node:test';
import assert from 'node:assert/strict';
import {execFileSync} from 'node:child_process';
import {join} from 'node:path';
import {homedir} from 'node:os';
import {startCoordinator} from '../harness.ts';
import {sourceTransaction} from '../performance/interface.ts';
import {PublicationHistory,installPublicationJournalSQL,publicationAnchorsSQL,publicationJournalSQL,publicationNotificationsSQL} from '../performance/publication-proof.ts';

// Requirement: the measurement journal binds actual committed I/U/D to exact old publications.
// Risk: delayed proof acknowledges a withheld leaf, invents a rollback, or guesses through a gap/reset.
test('committed publication journal preserves withheld leaf, old paint, rollback, updates and deletion',{timeout:60000},async()=>{
 const c=await startCoordinator({name:'publication-journal',env:{FLEET_RESOURCE_PROCESSOR:'on',FLEET_RESOURCE_READS:'on',FLEET_LOST_AFTER_MS:'864000000'}});
 const project='journal-owned',marker='!qualification-progress';
 const until=async(check:()=>boolean)=>{const end=Date.now()+15000;while(!check()){assert.ok(Date.now()<end,'journal fixture checkpoint timeout');await new Promise(r=>setTimeout(r,20));}};
 try{
  c.sql(`INSERT INTO definition(kind,id,path,head,project_id) VALUES('project','${project}','fixture',0,'${project}'); INSERT INTO execution(id,kind,state,project_id) VALUES('journal-baseline','agent_run','running','${project}')`);
  await until(()=>c.sql(`SELECT ready FROM resource_project_summary WHERE project_id='${project}'`).trim()==='t');
  await until(()=>c.sql(`SELECT count(*) FROM resource_work_scope WHERE project_id='${project}'`).trim()==='0');
  c.sql(installPublicationJournalSQL([project],marker));
  const anchors=JSON.parse(c.sql('SELECT '+publicationAnchorsSQL));assert.equal(anchors.length,1);
  const db=c.sql('SELECT current_database()').trim(),pg=process.env.FLEET_TRIAL_PGDATA||join(homedir(),'.cache/fleet-trial/pg');
  const appSQL=(input:string)=>execFileSync('psql',['-X','-qAt','-v','ON_ERROR_STOP=1','-h',pg,'-p',process.env.FLEET_TRIAL_PGPORT||'54329','-U','fleet_app','-d',db],{input,encoding:'utf8',stdio:['pipe','pipe','pipe']}).trim();
  assert.equal(appSQL(`SET fleet.project_ids='${project}';SELECT count(*) FROM interface_publication`),'0','journal rows/counts remain maintenance-only');
  assert.throws(()=>appSQL("SET fleet.project_ids='*';UPDATE interface_publication SET calls=999"),/permission denied/,'runtime role cannot rewrite immutable evidence');
  const h=new PublicationHistory();for(const a of anchors)h.ingest(a);const epoch=anchors[0].epoch;
  const ingest=()=>{const rows=JSON.parse(c.sql('SELECT '+publicationJournalSQL(project,epoch,h.latest(project,epoch))));for(const row of rows)h.ingest(row);return rows;};
  const current=()=>JSON.parse(c.sql(`SELECT json_build_object('epoch',s.epoch,'revision',s.revision::text,'calls',coalesce(t.calls,0),'errors',coalesce(t.errors,0)) FROM resource_project_summary s LEFT JOIN resource_tool t ON t.project_id=s.project_id AND t.name='${marker}' AND t.connection='' WHERE s.project_id='${project}'`));
  c.sql(`CREATE TABLE journal_hold(id text PRIMARY KEY); INSERT INTO journal_hold VALUES('journal-held');
   CREATE FUNCTION journal_hold_leaf() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF EXISTS(SELECT 1 FROM journal_hold WHERE id=NEW.execution_id) THEN RAISE EXCEPTION 'fixture journal held leaf'; END IF; RETURN NEW; END $$;
   CREATE TRIGGER journal_hold_leaf BEFORE INSERT OR UPDATE ON resource_contribution FOR EACH ROW EXECUTE FUNCTION journal_hold_leaf()`);
  const held={id:'held',execution:'journal-held',project,n:1,calls:1,errors:1,lower:0,upper:1,phase:'burst',due:0};
  const other={...held,id:'other',execution:'journal-other',n:2,errors:0};
  c.sql(sourceTransaction(held,'distinct'));c.sql(sourceTransaction(other,'distinct'));
  // Synthetic completed history must not become phantom live/stuck work or invented runtime.
  const history=JSON.parse(c.sql(`SELECT json_agg(json_build_object('state',e.state,'start',extract(epoch FROM e.started_at),'end',extract(epoch FROM e.ended_at),'wall',t.wall_ms,'cpu',t.cpu_ms,'api',t.api_ms,'active',t.active_ms,'tokens',t.tokens_input,'toolBasis',t.tool_calls_basis)) FROM execution e JOIN execution_totals t ON t.execution_id=e.id WHERE e.id IN ('journal-held','journal-other')`));
  assert.equal(history.length,2);for(const row of history){assert.equal(row.state,'succeeded');assert.equal(row.start,1704067200);assert.equal(row.end,1704067201);for(const field of ['wall','cpu','api','active','tokens'])assert.equal(row[field],null,'synthetic endpoints never invent measured '+field);assert.equal(row.toolBasis,'measured');}

  await until(()=>current().calls===1);const early=current();assert.equal(early.errors,0);
  assert.equal(c.sql("SELECT count(*) FROM resource_work WHERE execution_id='journal-held'").trim(),'1');
  c.sql("DELETE FROM journal_hold WHERE id='journal-held'");await until(()=>current().calls===2);const both=current();assert.equal(both.errors,1);
  const committedRows=JSON.parse(c.sql('SELECT '+publicationJournalSQL(project,epoch,anchors[0].revision)));
  const links=JSON.parse(c.sql('SELECT '+publicationNotificationsSQL(project,committedRows.map((row:any)=>row.transaction))));
  assert.equal(links.length,committedRows.length);for(const row of committedRows)assert.ok(links.some((link:any)=>link.project===project&&link.transaction===row.transaction&&/^\d+$/.test(link.sequence)),'notification sequence must bind to a genuine committed publication XID');
  ingest(); // Proof deliberately runs AFTER the database has advanced past the old observed paint.
  assert.equal(h.check(other,epoch+':'+early.revision,'1 · 0 failed','distinct').accepted,true);
  assert.equal(h.check(held,epoch+':'+early.revision,'1 · 0 failed','distinct').accepted,false);
  assert.equal(h.check(held,epoch+':'+both.revision,'2 · 1 failed','distinct').accepted,true);
  assert.equal(h.check(other,epoch+':'+early.revision,'1 · 1 failed','distinct').accepted,false);
  const before=c.sql(`SELECT count(*)::text||':'||(SELECT count(*) FROM interface_publication_delta) FROM interface_publication`).trim();
  c.sql(`BEGIN; UPDATE resource_contribution SET source_sha=source_sha WHERE execution_id='journal-other';UPDATE resource_project_summary SET revision=revision+1 WHERE project_id='${project}';ROLLBACK`);
  assert.equal(c.sql(`SELECT count(*)::text||':'||(SELECT count(*) FROM interface_publication_delta) FROM interface_publication`).trim(),before,'rollback must retain neither deltas nor publication');
  c.sql(`UPDATE execution_totals SET tool_calls='{"byTool":{"${marker}":{"calls":2,"errors":1}}}' WHERE execution_id='journal-other'`);
  await until(()=>current().calls===3);const updated=current();ingest();
  assert.equal(h.check(other,epoch+':'+updated.revision,'3 · 2 failed','distinct').reason,'leaf-facts-mismatch');
  assert.equal(h.check({...other,calls:2,errors:1},epoch+':'+updated.revision,'3 · 2 failed','distinct').accepted,true);
  c.sql("BEGIN;SELECT 1 FROM execution WHERE id='journal-held' FOR UPDATE;DELETE FROM execution_totals WHERE execution_id='journal-held';DELETE FROM execution WHERE id='journal-held';COMMIT");await until(()=>current().calls===2);const removed=current();ingest();
  assert.equal(h.check(held,epoch+':'+removed.revision,'2 · 1 failed','distinct').reason,'leaf-not-included');
  assert.equal(h.check(held,epoch+':'+both.revision,'2 · 1 failed','distinct').accepted,true,'old committed publication remains exact after deletion');
  c.sql("UPDATE execution_totals SET tool_calls=tool_calls WHERE execution_id='journal-other'");await until(()=>c.sql(`SELECT count(*) FROM resource_work_scope WHERE project_id='${project}'`).trim()==='0');assert.equal(current().revision,removed.revision,'no-op replay cannot invent a publication');
  const gap=new PublicationHistory();for(const a of anchors)gap.ingest(a);
  const rows=JSON.parse(c.sql('SELECT '+publicationJournalSQL(project,epoch,anchors[0].revision)));assert.ok(rows.length>=4);
  for(const row of rows.filter((_:unknown,i:number)=>i!==1))gap.ingest(row);
  assert.equal(gap.check(other,epoch+':'+removed.revision,'2 · 1 failed','distinct').reason,'publication-history-gap');
  c.sql(`UPDATE resource_project_summary SET epoch=gen_random_uuid(),revision=revision+1 WHERE project_id='${project}'`);
  const reset=current();const resetRows=JSON.parse(c.sql('SELECT '+publicationJournalSQL(project,reset.epoch,null)));for(const row of resetRows)h.ingest(row);
  assert.equal(h.check({...other,calls:2,errors:1},reset.epoch+':'+reset.revision,'2 · 1 failed','distinct').reason,'missing-anchor');
 }finally{await c.stop();}
});
