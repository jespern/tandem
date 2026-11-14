import {test} from 'node:test';
import assert from 'node:assert/strict';
import * as t from '../dist/index.js';
import {newAction,sealAction,openAction,messageID} from '../dist/browser/actions.js';
test('durable action boxes bind the hub, action ID and local wrapping key',()=>{
  const key=t.randomBytes(32),hub=t.randomBytes(32),body=t.seal(key,hub,t.directoryTag(key,hub),Buffer.from('staged'),3);
  const action=newAction('share',[{name:'roster',messages:[{channel:t.directoryTag(key,hub),body,deferred:true,guard:{type:'control'}}]},{name:'grant',messages:[]}],{secret:t.b64u(t.randomBytes(32))});
  const box=sealAction(action,key,hub),restored=openAction(action.id,box,key,hub);assert.deepEqual(restored,action);assert.equal(messageID(restored.phases[0].messages[0]),t.b64u(t.sha(body)));
  assert.throws(()=>openAction('another',box,key,hub));assert.throws(()=>openAction(action.id,box,t.randomBytes(32),hub));assert.throws(()=>openAction(action.id,box,key,t.randomBytes(32)));
  const bad=Uint8Array.from(box);bad[bad.length-1]^=1;assert.throws(()=>openAction(action.id,bad,key,hub));assert.throws(()=>openAction(action.id,box,key,hub,box.length-1));
});
