// Requirement: one invalidation rule (S9): a read is reread when the server names its operation in a change's touches and
// the change names no other entity of its request; a setting-change of another known person touches nothing here.
// Risk: a view misses a remote update, or every change rereads every mounted read.
import {test} from 'node:test';
import assert from 'node:assert/strict';
import {touchedBy,setIdentity} from '../../app/src/store.ts';
import type {Change} from '../../contracts/generated/ops.ts';
const change=(kind:Change['kind'],id:string,touches:string[],more:Partial<Change>={}):Change=>({seq:1,kind,id,op:'U',taskId:null,sessionId:null,hostId:null,touches,...more});
test('touchedBy follows the server touches and the request scope',()=>{
 setIdentity(()=>'member@fixture');
 assert.equal(touchedBy(change('task','t1',['task_page']),'task_get',{taskId:'t1'}),false,'an operation the change does not touch');
 assert.equal(touchedBy(change('task','t1',['task_get'],{taskId:'t1'}),'task_get',{taskId:'t1'}),true);
 assert.equal(touchedBy(change('question','q',['task_get'],{taskId:'t2'}),'task_get',{taskId:'t1'}),false,'another task');
 assert.equal(touchedBy(change('definition','project/b',['project_detail']),'project_detail',{projectId:'a'}),false,'a known other project');
 for(const id of ['project/a','agent/pm','project/'])assert.equal(touchedBy(change('definition',id,['project_detail']),'project_detail',{projectId:'a'}),true,id);
 for(const t of ['person:other@fixture','definition:project/a'])assert.equal(touchedBy(change('setting-change','x',['person_settings_get'],{settingTarget:t}),'person_settings_get',{}),false,t);
 for(const t of ['person:member@fixture',null,undefined,'person:'])assert.equal(touchedBy(change('setting-change','x',['person_settings_get'],{settingTarget:t}),'person_settings_get',{}),true,String(t));
 assert.equal(touchedBy(change('task','t9',['entity_card'],{taskId:'t9'}),'entity_card',{kind:'task',id:'t1'}),false,'a card of another task');
 assert.equal(touchedBy(change('session','s1',['list_membership'],{sessionId:'s1'}),'list_membership',{ids:['s2']}),false,'rows by id: none named');
 assert.equal(touchedBy(change('session','s2',['list_membership'],{sessionId:'s2'}),'list_membership',{ids:['s2']}),true);
});
