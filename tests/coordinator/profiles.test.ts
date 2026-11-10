import {test} from 'node:test';
import assert from 'node:assert/strict';
import {readFileSync,mkdtempSync,writeFileSync} from 'node:fs';
import {canonical,profileDigest,validNativeProfile,readTrustedProfile,listProfiles} from '../../packages/coordinator/profiles.ts';

test('canonical JSON and profile digest',()=>{
 assert.equal(canonical({z:2,a:{y:1,b:0}}),'{"a":{"b":0,"y":1},"z":2}');
 assert.equal(profileDigest(JSON.parse(readFileSync('config/agents/baseline.json','utf8'))),'9d23f7fde668430ab9d50cb6db6fdcb3d92523b51f21c820f9e9d270825e8fd2');
});

test('native profile validation',()=>{
 assert.ok(validNativeProfile({model:'gpt-5',effort:'high'}));
 assert.ok(!validNativeProfile({model:'-x',effort:'high'}));
 assert.ok(!validNativeProfile({harness:'claude',model:'opus',effort:'high'}));
 assert.ok(!validNativeProfile({model:'gpt-5',effort:'high',fleetTools:'yes'}));
});

test('trusted profiles accept optional skills and connections, reject bad ones',()=>{
 const dir=mkdtempSync('/tmp/fleet-profiles-');const base=JSON.parse(readFileSync('config/agents/baseline.json','utf8'));
 const write=(id:string,extra:any)=>writeFileSync(`${dir}/${id}.json`,JSON.stringify({...base,id,...extra}));
 write('good',{skills:['web-research'],connections:['tavily']});write('bad',{skills:['../x']});write('dup',{connections:['a','a']});
 assert.deepEqual(readTrustedProfile(dir,'good').skills,['web-research']);
 for(const id of ['bad','dup'])assert.throws(()=>readTrustedProfile(dir,id),/Invalid profile/);
 assert.deepEqual(listProfiles(dir).map(p=>p.id),['good']);
});
