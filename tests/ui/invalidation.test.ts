// Requirement: authoritative target/scope invalidations preserve current-person, legacy and dependency refreshes.
// Risk: a project audit reloads preferences, actor replaces target, or an unknown change silently hides new facts.
import {test} from 'node:test';
import assert from 'node:assert/strict';
import {preferencesTouched,projectDefinitionTouched} from '../../app/src/invalidation.ts';
import type {Change} from '../../contracts/generated/ops.ts';
const change=(kind:Change['kind'],id:string,settingTarget?:string|null):Change=>({seq:1,kind,id,settingTarget,op:'U',taskId:null,sessionId:null,hostId:null});
test('personal preference reads follow the current target, independently of writer, with safe legacy fallback',()=>{
 assert.equal(preferencesTouched([change('setting-change','audit','definition:project/a')],'member@fixture'),false);
 assert.equal(preferencesTouched([change('setting-change','audit','person:other@fixture')],'member@fixture'),false);
 assert.equal(preferencesTouched([change('setting-change','audit','person:member@fixture')],'member@fixture'),true);
 for(const target of [undefined,null,'','person:'])assert.equal(preferencesTouched([change('setting-change','legacy',target)],'member@fixture'),true);
 assert.equal(preferencesTouched([change('setting-change','audit','person:member@fixture')],undefined),true);
 assert.equal(preferencesTouched([change('definition','project/a')],'member@fixture'),false);
 assert.equal(preferencesTouched([change('setting-change','other','task:t'),change('setting-change','legacy')],'member@fixture'),true);
});
test('project detail skips only a known other project and keeps own, dependency and unknown definitions',()=>{
 assert.equal(projectDefinitionTouched([change('definition','project/b')],'a'),false);
 for(const id of ['project/a','agent/pm','pipeline/start','legacy-id','project/'])assert.equal(projectDefinitionTouched([change('definition',id)],'a'),true);
 assert.equal(projectDefinitionTouched([change('setting-change','audit','project:a')],'a'),false);
 assert.equal(projectDefinitionTouched([change('definition','project/a')],undefined),false);
 assert.equal(projectDefinitionTouched([change('definition','project/b'),change('definition','project/a')],'a'),true);
});
