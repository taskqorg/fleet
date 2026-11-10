// Use-case registry drift check (docs/designs/fleet-lab.md, Stage 1 item 17). Each entry of config/lab/use-cases/*.json points
// at a section of a source document: {repo, path, anchor, commit, sha256}. This recomputes the section's hash from the repo's
// HEAD and reports entries whose source changed. It warns; it never blocks a launch.
//
//   npx tsx scripts/lab-drift.ts            print changed or missing entries; exit 1 when there is any
//   npx tsx scripts/lab-drift.ts --write    also write source_status into the registry files
//   npx tsx scripts/lab-drift.ts --pin ID…  accept the current source of these entries (commit, sha256, status current)
//
// Repos: "fleet" is this checkout; any other repo is <FLEET_LAB_REPOS>/<repo> (default: the directory above this checkout).
// An absent repo gives status "unavailable" and leaves the stored status alone (CI checks out Fleet only).
import {createHash} from 'node:crypto';
import {execFileSync} from 'node:child_process';
import {existsSync,readdirSync,readFileSync,writeFileSync} from 'node:fs';
import {dirname,join,resolve} from 'node:path';
import {fileURLToPath} from 'node:url';

export type Source={repo:string,path:string,anchor:string,commit:string,sha256:string};
export type Status='current'|'changed'|'missing'|'unavailable';

// GitHub's heading slug: lower case, punctuation dropped, spaces to hyphens.
export const slug=(heading:string)=>heading.trim().toLowerCase().replace(/[^\p{L}\p{N}\s_-]/gu,'').replace(/\s/g,'-');

// The anchored text: a section (an explicit <a id> line or a heading with that slug, up to the next heading of the same or a
// higher level) or one table row whose first cell starts with the anchor. Null when the anchor is not in the text.
export function section(text:string,anchor:string):string|null{
 const lines=text.split('\n'),level=(l:string)=>/^(#+) /.exec(l)?.[1].length??0;
 let start=lines.findIndex(l=>l.trim()===`<a id="${anchor}"></a>`);
 if(start>=0){while(start<lines.length&&!level(lines[start]))start++}
 else start=lines.findIndex(l=>level(l)>0&&slug(l.replace(/^#+ /,''))===anchor);
 if(start>=0&&start<lines.length){const n=level(lines[start]);let end=start+1;while(end<lines.length&&!(level(lines[end])&&level(lines[end])<=n))end++;return lines.slice(start,end).join('\n').trim()}
 const row=lines.find(l=>new RegExp(`^\\|\\s*${anchor.replace(/[.*+?^${}()|[\]\\]/g,'\\$&')}(\\s|\\|)`).test(l));
 return row?row.trim():null}

export const hash=(s:string)=>createHash('sha256').update(s).digest('hex');

export function repoDir(repo:string,root:string,repos=process.env.FLEET_LAB_REPOS??join(root,'..')){return repo==='fleet'?root:join(repos,repo)}

// The source at the repo's HEAD: its commit and the anchored section's hash, or why it cannot be read.
export function current(s:Source,root:string,repos?:string):{status:'unavailable'|'missing'}|{commit:string,sha256:string}{
 const dir=repoDir(s.repo,root,repos);if(!existsSync(join(dir,'.git')))return {status:'unavailable'};
 const git=(...a:string[])=>execFileSync('git',['-C',dir,...a],{encoding:'utf8',stdio:['ignore','pipe','ignore']});
 let text:string;try{text=git('show','HEAD:'+s.path)}catch{return {status:'missing'}}
 const part=section(text,s.anchor);return part===null?{status:'missing'}:{commit:git('rev-parse','HEAD').trim(),sha256:hash(part)}}

export function check(s:Source,root:string,repos?:string):Status{const c=current(s,root,repos);return 'status' in c?c.status:c.sha256===s.sha256?'current':'changed'}

export const registryDir=(root:string)=>join(root,'config/lab/use-cases');
export function loadRegistry(root:string){return readdirSync(registryDir(root)).filter(f=>f.endsWith('.json')).sort().map(f=>({file:join(registryDir(root),f),entries:JSON.parse(readFileSync(join(registryDir(root),f),'utf8')) as any[]}))}
const save=(file:string,entries:any[])=>writeFileSync(file,JSON.stringify(entries,null,1)+'\n');

export function run(argv:string[],root:string,log=console.log,repos?:string){
 const write=argv.includes('--write'),pinAt=argv.indexOf('--pin'),pins=pinAt>=0?new Set(argv.slice(pinAt+1).filter(a=>!a.startsWith('--'))):null;
 let flagged=0;
 for(const {file,entries} of loadRegistry(root)){let dirty=false;
  for(const e of entries){
   if(pins){if(!pins.has(e.id))continue;const c=current(e.source,root,repos);if('status' in c){log(`${e.id}: cannot pin, source ${c.status}`);flagged++;continue}
    Object.assign(e.source,c);e.source_status='current';dirty=true;log(`${e.id}: pinned ${c.commit.slice(0,12)}`);continue}
   const s=check(e.source,root,repos);
   if(s==='changed'||s==='missing'){flagged++;log(`${e.id}: ${s} (${e.source.repo}/${e.source.path}#${e.source.anchor})`)}
   if(write&&s!=='unavailable'&&e.source_status!==s){e.source_status=s;dirty=true}}
  if(dirty)save(file,entries)}
 return flagged}

if(process.argv[1]&&resolve(process.argv[1])===fileURLToPath(import.meta.url)){
 const root=resolve(dirname(fileURLToPath(import.meta.url)),'..'),n=run(process.argv.slice(2),root);
 if(n){console.log(`lab-drift: ${n} entr${n===1?'y':'ies'} need review (warning only)`);process.exitCode=1}}
