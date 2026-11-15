// Measurement-only committed history in a disposable qualification database.
import {isDeepStrictEqual} from 'node:util';
import {literal} from './seed-interface.ts';
export type LeafState={execution:string;included:boolean;calls:number;errors:number;sourceSHA:string|null;op:string};
export type JournalPublication={project:string;epoch:string;revision:string;previous:string|null;anchor:boolean;transaction:string;calls:number;errors:number;leaves:LeafState[]};
export function journalVersion(value:string){const m=/^([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}):(\d+)$/.exec(value);return m?{epoch:m[1],revision:m[2]}:null;}
type EpochHistory={rows:Map<string,JournalPublication>;leaves:Map<string,{revision:bigint;state:LeafState}[]>;latest:bigint;calls:number;errors:number;invalid:string|null};
export class PublicationHistory{
 epochs=new Map<string,EpochHistory>();
 ingest(row:JournalPublication){
  if(!journalVersion(row.epoch+':'+row.revision)||row.previous!==null&&!/^\d+$/.test(row.previous))throw Error('invalid journal identity');
  const key=row.project+'/'+row.epoch;let h=this.epochs.get(key);const revision=BigInt(row.revision);
  if(!h){h={rows:new Map(),leaves:new Map(),latest:-1n,calls:0,errors:0,invalid:null};this.epochs.set(key,h);if(!row.anchor||row.previous!==null)h.invalid??='missing-anchor';}
  const old=h.rows.get(row.revision);
  if(old){if(!isDeepStrictEqual(old,row))h.invalid??='contradictory-publication';return;}
  if(!Number.isSafeInteger(row.calls)||!Number.isSafeInteger(row.errors)||row.calls<0||row.errors<0)h.invalid??='invalid-publication-counts';
  if(h.latest>=0n&&(row.anchor||row.previous!==h.latest.toString()||revision!==h.latest+1n))h.invalid??='publication-history-gap';
  for(const leaf of row.leaves){
   if(!Number.isSafeInteger(leaf.calls)||!Number.isSafeInteger(leaf.errors)||leaf.calls<0||leaf.errors<0)h.invalid??='invalid-leaf-counts';
   const history=h.leaves.get(leaf.execution)??[];const prior=history.at(-1)?.state;if(prior?.included){h.calls-=prior.calls;h.errors-=prior.errors;}if(leaf.included){h.calls+=leaf.calls;h.errors+=leaf.errors;}history.push({revision,state:leaf});h.leaves.set(leaf.execution,history);
  }
  if(h.calls!==row.calls||h.errors!==row.errors)h.invalid??='publication-ledger-mismatch';
  h.rows.set(row.revision,row);h.latest=revision;
 }
 latest(project:string,epoch:string){return this.epochs.get(project+'/'+epoch)?.latest.toString()??null;}
 lookup(project:string,version:string,text:string){
  const v=journalVersion(version);if(!v)return {accepted:false,reason:'invalid-version'} as const;
  const h=this.epochs.get(project+'/'+v.epoch);if(!h)return {accepted:false,reason:'unknown-epoch'} as const;
  if(h.invalid)return {accepted:false,reason:h.invalid} as const;
  const publication=h.rows.get(v.revision);if(!publication)return {accepted:false,reason:'unknown-publication'} as const;
  if(text!==`${publication.calls} · ${publication.errors} failed`)return {accepted:false,reason:'publication-text-mismatch'} as const;
  return {accepted:true,reason:'committed-publication',publication} as const;
 }
 check(fact:{project:string;execution:string;calls:number;errors:number},version:string,text:string,distribution:'hot'|'distinct'){
  const found=this.lookup(fact.project,version,text);if(!found.accepted)return found;
  const publication=found.publication,v=journalVersion(version)!,h=this.epochs.get(fact.project+'/'+v.epoch)!;
  const history=h.leaves.get(fact.execution)??[];let lo=0,hi=history.length;const revision=BigInt(v.revision);
  while(lo<hi){const mid=(lo+hi)>>>1;if(history[mid].revision<=revision)lo=mid+1;else hi=mid;}
  const leaf=history[lo-1]?.state;if(!leaf?.included)return {accepted:false,reason:'leaf-not-included'} as const;
  if(distribution==='distinct'?(leaf.calls!==fact.calls||leaf.errors!==fact.errors):(leaf.calls<fact.calls||leaf.errors<fact.errors))return {accepted:false,reason:'leaf-facts-mismatch'} as const;
  return {accepted:true,reason:'committed-publication',publication,leaf} as const;
 }
}

// Install only after the baseline is ready and before measured writers. Project locks
// precede DDL locks; the anchor and triggers become visible together on commit.
export function installPublicationJournalSQL(projects:string[],marker:string){return `BEGIN;
DO $$ DECLARE p text; BEGIN FOR p IN SELECT project_id FROM resource_build ORDER BY project_id COLLATE "C" LOOP
 PERFORM pg_advisory_xact_lock(hashtextextended('project:'||p,0)); END LOOP; END $$;
CREATE TABLE interface_publication_delta(
 ordinal bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,txid xid8 NOT NULL DEFAULT pg_current_xact_id(),project text NOT NULL,leaf jsonb NOT NULL);
CREATE INDEX interface_publication_delta_tx ON interface_publication_delta(txid,project,ordinal);
CREATE TABLE interface_publication(
 project text NOT NULL,epoch text NOT NULL,revision bigint NOT NULL,previous bigint NULL,anchor boolean NOT NULL DEFAULT false,
 publication_xid xid8 NOT NULL DEFAULT pg_current_xact_id(),calls bigint NOT NULL,errors bigint NOT NULL,leaves jsonb NOT NULL,
 PRIMARY KEY(project,epoch,revision));
ALTER TABLE interface_publication_delta ENABLE ROW LEVEL SECURITY;
ALTER TABLE interface_publication_delta FORCE ROW LEVEL SECURITY;
CREATE POLICY maintenance ON interface_publication_delta USING(current_setting('fleet.project_ids',true)='*');
ALTER TABLE interface_publication ENABLE ROW LEVEL SECURITY;
ALTER TABLE interface_publication FORCE ROW LEVEL SECURITY;
CREATE POLICY maintenance ON interface_publication USING(current_setting('fleet.project_ids',true)='*');
REVOKE UPDATE,DELETE ON interface_publication_delta,interface_publication FROM fleet_app;
CREATE FUNCTION interface_capture_leaf() RETURNS trigger LANGUAGE plpgsql SET search_path=public AS $$ DECLARE v jsonb; BEGIN
 IF TG_OP<>'INSERT' AND OLD.project_id IS NOT NULL THEN
  INSERT INTO interface_publication_delta(project,leaf) VALUES(OLD.project_id,jsonb_build_object('execution',OLD.execution_id,'included',false,'calls',0,'errors',0,'sourceSHA',OLD.source_sha,'op','D'));
 END IF;
 IF TG_OP<>'DELETE' AND NEW.project_id IS NOT NULL THEN
  SELECT item INTO v FROM jsonb_array_elements(NEW.facts->'tools') item WHERE item->>'name'=TG_ARGV[0] AND item->>'connection'='';
  INSERT INTO interface_publication_delta(project,leaf) VALUES(NEW.project_id,jsonb_build_object('execution',NEW.execution_id,'included',NEW.included,'calls',coalesce((v->>'calls')::bigint,0),'errors',coalesce((v->>'errors')::bigint,0),'sourceSHA',NEW.source_sha,'op',left(TG_OP,1)));
 END IF;
 RETURN NULL; END $$;
CREATE TRIGGER interface_capture_leaf AFTER INSERT OR UPDATE OR DELETE ON resource_contribution FOR EACH ROW EXECUTE FUNCTION interface_capture_leaf(${literal(marker)});
CREATE FUNCTION interface_capture_publication() RETURNS trigger LANGUAGE plpgsql SET search_path=public AS $$ BEGIN
 INSERT INTO interface_publication(project,epoch,revision,previous,calls,errors,leaves)
 SELECT NEW.project_id,NEW.epoch::text,NEW.revision,CASE WHEN NEW.epoch=OLD.epoch THEN OLD.revision END,
  coalesce(t.calls,0),coalesce(t.errors,0),
  (SELECT coalesce(jsonb_agg(leaf ORDER BY ordinal),'[]'::jsonb) FROM interface_publication_delta WHERE txid=pg_current_xact_id() AND project=NEW.project_id)
 FROM (VALUES(1)) v(n) LEFT JOIN resource_tool t ON t.project_id=NEW.project_id AND t.name=TG_ARGV[0] AND t.connection='';
 RETURN NULL; END $$;
CREATE TRIGGER interface_capture_publication AFTER UPDATE OF revision,epoch ON resource_project_summary
 FOR EACH ROW WHEN(OLD.revision IS DISTINCT FROM NEW.revision OR OLD.epoch IS DISTINCT FROM NEW.epoch) EXECUTE FUNCTION interface_capture_publication(${literal(marker)});
INSERT INTO interface_publication(project,epoch,revision,previous,anchor,calls,errors,leaves)
 SELECT s.project_id,s.epoch::text,s.revision,NULL,true,coalesce(t.calls,0),coalesce(t.errors,0),
  (SELECT coalesce(jsonb_agg(jsonb_build_object('execution',c.execution_id,'included',c.included,'calls',(v->>'calls')::bigint,'errors',(v->>'errors')::bigint,'sourceSHA',c.source_sha,'op','I')),'[]'::jsonb)
   FROM resource_contribution c CROSS JOIN LATERAL jsonb_array_elements(c.facts->'tools') v
   WHERE c.project_id=s.project_id AND v->>'name'=${literal(marker)} AND v->>'connection'='')
 FROM resource_project_summary s LEFT JOIN resource_tool t ON t.project_id=s.project_id AND t.name=${literal(marker)} AND t.connection=''
 WHERE s.project_id=ANY(ARRAY[${projects.map(literal).join(',')}]::text[]) AND s.ready;
COMMIT;`;}
export function publicationJournalSQL(project:string,epoch:string,after:string|null,limit=1000){
 if(!journalVersion(epoch+':0')||after!==null&&!/^\d+$/.test(after)||!Number.isInteger(limit)||limit<1||limit>1000)throw Error('invalid journal cursor');
 return `(SELECT coalesce(jsonb_agg(to_jsonb(p)-'revision'-'previous'-'publication_xid'||jsonb_build_object('revision',revision::text,'previous',previous::text,'transaction',publication_xid::text) ORDER BY revision),'[]'::jsonb) FROM (SELECT * FROM interface_publication WHERE project=${literal(project)} AND epoch=${literal(epoch)} ${after===null?'':`AND revision>${after}::bigint`} ORDER BY revision LIMIT ${limit}) p)`;
}

export type RenderObservation={observation:string;reader:string;run:string;project:string;version:string;text:string;value:number;errors:number;visible:boolean;browserMonotonic:number;browserWall:number;domNodes:number;longTasks:unknown[]};
export class ObservationBook{
 entries=new Map<string,{body:RenderObservation;firstReceipt:number;attempts:number}>();
 receive(body:RenderObservation,receipt:number){
  if(typeof body.observation!=='string'||!body.observation||body.observation.length>200||!Number.isFinite(receipt))throw Error('invalid observation identity');
  const key=body.reader+'/'+body.observation,old=this.entries.get(key);
  if(old){old.attempts++;return {...old,consistent:isDeepStrictEqual(old.body,body)};}
  const entry={body:structuredClone(body),firstReceipt:receipt,attempts:1};this.entries.set(key,entry);return {...entry,consistent:true};
 }
}
export const publicationAnchorsSQL=`(SELECT coalesce(jsonb_agg(to_jsonb(p)-'revision'-'previous'-'publication_xid'||jsonb_build_object('revision',revision::text,'previous',previous::text,'transaction',publication_xid::text)),'[]'::jsonb) FROM interface_publication p WHERE anchor)`;

// Proof and source receipts may complete in either order. Keep the earliest proven
// observation even before confirmation; later completion cannot overwrite its bound.
export class RenderBounds<T>{
 entries=new Map<string,{receipt:number;value:T;confirmed:boolean}>();
 offer(key:string,receipt:number,value:T){const old=this.entries.get(key);if(old&&old.receipt<=receipt)return false;this.entries.set(key,{receipt,value,confirmed:old?.confirmed??false});return true;}
 confirm(key:string){const entry=this.entries.get(key);if(!entry)return null;entry.confirmed=true;return entry;}
}

// Bounded post-envelope export binds observed SSE sequence IDs to the actual
// committed publication XID; no sequence or higher version supplies inclusion.
export function publicationNotificationsSQL(project:string,transactions:string[]){
 if(transactions.length>1000||transactions.some(x=>!/^\d+$/.test(x)))throw Error('invalid notification export page');
 return `(SELECT coalesce(jsonb_agg(jsonb_build_object('sequence',seq::text,'transaction',xid::text,'project',id) ORDER BY xid,seq),'[]'::jsonb) FROM change WHERE kind='resource_summary' AND id=${literal(project)} AND xid=ANY(ARRAY[${[...new Set(transactions)].map(literal).join(',')}]::xid8[]))`;
}
