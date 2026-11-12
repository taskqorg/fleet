// One project definition and the same operations used by the PM.
import {useEffect,useRef,useState} from 'preact/hooks';
import {useLocation} from 'wouter-preact';
import {ListDetail,DetailSection,SettingField,CodeEditor,VirtualList,SearchField,Button,Empty,Icon,KvRow,ErrorBanner,matchSearch} from '../../../packages/ui/src/index.ts';
import {op,writeOnce,useAlive} from '../api.ts';
import {useDoc} from '../store.ts';

type Bundle={settings:Record<string,any>,contextFiles:{path:string,text:string}[]};
const fresh=():Bundle=>({settings:{id:'',name:'',contextFiles:['rules.md'],roles:{pm:'pm'},repos:[],environments:[]},contextFiles:[{path:'rules.md',text:''}]});
export function Projects({id}:{id?:string}){
 const [,go]=useLocation();const [creating,setCreating]=useState(false),[query,setQuery]=useState('');
 const list=useDoc<any>('projects',()=>op('project_list',{}),c=>c.kind==='definition');
 const rows=(list.data?.projects??[]).filter((p:any)=>matchSearch(query,`${p.name} ${p.id}`,{project:p.id}));
 const pane=<><header class="page-h"><h1>Projects</h1><span class="grow"/><Button label="Create project" icon="plus" onClick={()=>setCreating(true)}/></header>
 <SearchField label="Find project" value={query} onInput={setQuery} onSearch={()=>{}}/>
 {list.error?<div role="alert">{list.error}</div>:!list.data?<Empty text="Loading…"/>:<VirtualList items={rows} getKey={(p:any)=>p.id} rowHeight={52} follow="top" label="Projects" render={(p:any)=><><Icon name="folder-open"/><a class="title row-link" href={'/projects/'+p.id} onClick={e=>{e.preventDefault();setCreating(false);go('/projects/'+p.id);}}>{p.name}<span class="id">{p.id}</span></a></>}/>}
 <span>{rows.length} of {list.data?.projects.length??'—'}</span></>;
 return <ListDetail list={pane} detail={creating||id?<ProjectEditor key={creating?'create':id} id={creating?undefined:id} onSaved={p=>{setCreating(false);go('/projects/'+p);}}/>:<Empty text="Choose a project or create one."/>}/>;
}
function ProjectEditor({id,onSaved}:{id?:string,onSaved:(id:string)=>void}){
 const [bundle,setBundle]=useState<Bundle>(fresh),[revision,setRevision]=useState(0),[error,setError]=useState(''),[busy,setBusy]=useState(false),[ready,setReady]=useState<any>(null),[access,setAccess]=useState<any>(null),[repoSha,setRepoSha]=useState<string|null>(null),[sourceState,setSourceState]=useState('');
 const requestId=useRef(crypto.randomUUID()),alive=useAlive();const [loaded,setLoaded]=useState(!id);
 const load=async()=>{if(!id)return;try{const p:any=await op('project_context',{projectId:id});setBundle(p.content);setRevision(p.revision);setAccess(p.initialAccess);setRepoSha(p.repoSha??null);setSourceState(p.sourceState??'');requestId.current=crypto.randomUUID();setReady(null);setError('');setLoaded(true);}catch(e:any){setError(e.message);}};
 useEffect(()=>{void load();},[id]);
 const setting=(key:string,value:any)=>setBundle(b=>({...b,settings:{...b.settings,[key]:value}}));
 const field=(label:string,value:string,save:(s:string)=>void)=><SettingField help="Edit this project field; Save project applies the complete version." label={label} value={value} status={{kind:'pending'}} now={Date.now()} onSave={save}/>;
 const save=async()=>{setBusy(true);try{
  const unknown=()=>setError('unknown, retrying');const p:any=id?await writeOnce('project_update',{projectId:id,requestId:requestId.current,baseRevision:revision,content:bundle,repoSha},unknown,{signal:alive}):await writeOnce('project_create',{id:bundle.settings.id,requestId:requestId.current,content:bundle},unknown,{signal:alive});
  const check:any=await op('project_context',{projectId:p.projectId});if(check.revision!==p.revision||check.contentSha!==p.contentSha)throw Error('Saved version changed; reload before editing.');
  setBundle(check.content);setRevision(check.revision);setAccess(check.initialAccess);setRepoSha(check.repoSha??null);setSourceState(check.sourceState??'');requestId.current=crypto.randomUUID();setError('');onSaved(p.projectId);
 }catch(e:any){setError(e.message);}finally{setBusy(false);}};
 const bindings=(kind:'repos'|'environments',title:string,keys:string[])=>{
  const items:any[]=bundle.settings[kind]??[];
  const change=(i:number,k:string,v:string)=>setting(kind,items.map((item,n)=>n===i?{...item,[k]:v}:item));
  return <DetailSection title={title}>{items.map((item,i)=><div key={i}>{keys.map(k=>field(`${title} ${i+1} ${{id:"ID",remote:"URL",defaultBranch:"Default branch",connection:"Git connection",hostId:"Host",checkoutPath:"Checkout path",repo:"Repository"}[k]??k}`,item[k]??'',v=>change(i,k,v)))}<Button label={`Remove ${title} ${i+1}`} icon="x" onClick={()=>setting(kind,items.filter((_,n)=>n!==i))}/></div>)}<Button label={`Add ${title}`} icon="plus" onClick={()=>setting(kind,[...items,kind==='repos'?{id:'repo-'+(items.length+1),remote:'',forge:'github',defaultBranch:'main'}:{hostId:'',checkoutPath:''}])}/></DetailSection>;
 };
 if(!loaded)return error?<ErrorBanner text={error} onRetry={()=>void load()}/>:<Empty text="Loading project…"/>;
 return <article class="detail" data-ui="detail"><header class="detail-h"><h2>{id?bundle.settings.name:'Create project'}</h2><span class="grow"/><Button label="Save project" icon="save" busy={busy} onClick={save}/>{id&&<Button label="Reload project" icon="rotate-cw" onClick={()=>void load()}/>}</header>
 {error&&<div role="alert">{error}</div>}{sourceState==='repository-changed'&&<p>Source configuration changed. Saving keeps this edited project version.</p>}<DetailSection title="Context">{!id&&field('Project ID',bundle.settings.id,v=>setting('id',v))}{field('Name',bundle.settings.name,v=>setting('name',v))}
 {bundle.contextFiles.map((f,i)=><div key={f.path}><CodeEditor label={`Project context ${f.path}`} value={f.text} onChange={text=>setBundle(b=>({...b,contextFiles:b.contextFiles.map((old,n)=>i===n?{...old,text}:old)}))}/></div>)}
 <KvRow icon="folder-open" label="Saved version" value={revision?String(revision):'Not saved'}/></DetailSection>
 {bindings('repos','Repository',['id','remote','defaultBranch','connection'])}{bindings('environments','Environment',['hostId','checkoutPath','repo'])}
 <DetailSection title="Agent roles">{Object.entries(bundle.settings.roles??{}).map(([role,profile])=>field(role,String(profile),v=>setting('roles',{...bundle.settings.roles,[role]:v})))}</DetailSection>
 {id&&<DetailSection title="Readiness"><Button label="Check readiness" icon="circle-check" onClick={()=>op('project_readiness',{projectId:id}).then(setReady,e=>setError(e.message))}/>{ready&&<>{(ready.hosts??[]).length===0?<Empty text="No environment is configured."/>:(ready.hosts??[]).map((h:any)=>h.executionId?<ReadinessResult key={h.executionId} hostId={h.hostId} executionId={h.executionId}/>:<KvRow icon="panels-top-left" label={h.hostId??'Environment'} value="Unavailable" meta={h.problem??'Readiness check unavailable'}/>)}</>}
 <Button label="Open PM conversation" icon="messages-square" onClick={()=>location.assign('/conversations?new='+encodeURIComponent(id))}/></DetailSection>}
 {access&&<DetailSection title="Initial access"><KvRow icon="folder-open" label="Initial member" value={access.member??'None'}/><KvRow icon="folder-open" label="State" value={!access.member?'None':access.revoked?'Revoked':'Active'}/>{access.member&&!access.revoked&&id&&<Button label="Revoke initial access" icon="shield" onClick={()=>op('project_initial_access_revoke',{projectId:id,baseAccessVersion:access.version}).then(()=>load(),e=>setError(e.message))}/>}<p>Separate explicit grants and full administrator access remain effective.</p></DetailSection>}
 </article>;
}

function ReadinessResult({hostId,executionId}:{hostId:string,executionId:string}){
 const result=useDoc<any>('project-readiness:'+executionId,()=>op('execution_status',{executionId}),c=>c.kind==='event'||(c.kind==='execution'&&c.id===executionId));
 const latest=result.data?.readiness?.at(-1);
 return <><KvRow icon="panels-top-left" label={hostId} value={result.error?'Observation unavailable':latest?(latest.ok?'Ready':'Not ready'):result.data?.state??'Check requested'} meta={result.error??(latest?undefined:'Awaiting readiness result')}/>{(latest?.checks??[]).map((check:any)=><KvRow icon="circle-check" label={check.check} value={check.status} meta={[check.target,check.reason].filter(Boolean).join(' · ')}/>)}</>;
}
