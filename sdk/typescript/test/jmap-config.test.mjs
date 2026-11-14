import {test} from 'node:test';
import assert from 'node:assert/strict';
import {JMAPStore,fastmailConfig} from '../dist/browser/jmap.js';
const core='urn:ietf:params:jmap:core',mail='urn:ietf:params:jmap:mail';
async function mocked(origin,config){
  const original=globalThis.fetch,calls=[];
  globalThis.fetch=async(url,init)=>{
    calls.push({url,authorization:init.headers.Authorization});
    if(url===config.sessionUrl){const response=Response.json({capabilities:{[core]:{maxObjectsInGet:128},[mail]:{}},primaryAccounts:{[mail]:'account'},accounts:{account:{accountCapabilities:{[mail]:{}}}},apiUrl:origin+'/api',uploadUrl:origin+'/upload/{accountId}',eventSourceUrl:origin+'/events?types={types}&close={closeafter}&ping={ping}'});Object.defineProperty(response,'url',{value:url});return response;}
    const [method,args]=JSON.parse(init.body).methodCalls[0];
    const result=method==='Mailbox/query'?{ids:['folder']}:{list:[{id:'folder',name:config.folderName??'Tandem',parentId:null}]};
    return Response.json({methodResponses:[[method,result,'0']]});
  };
  try{const store=await JMAPStore.open(config);await store.close();return calls;}
  catch(error){error.calls=calls;throw error;}
  finally{globalThis.fetch=original;}
}
test('Fastmail preset trusts regional API hosts and supports a dedicated mailbox',async()=>{
  for(const origin of ['https://api.fastmail.com','https://ams.api.fastmail.com','https://nyc.api.fastmail.com']){
    const calls=await mocked(origin,fastmailConfig('example-token',{folderName:'Tandem-probe-example'}));
    assert.equal(calls.length,3);assert.ok(calls.every(call=>call.authorization==='Bearer example-token'));assert.ok(calls[1].url.startsWith(origin+'/'));
  }
});
test('Fastmail preset refuses lookalikes, other ports and insecure endpoints before sending credentials',async()=>{
  for(const origin of ['https://api.fastmail.com.evil.test','https://evil-fastmail.com','https://fastmail.com','https://ams.api.fastmail.com:8443','http://ams.api.fastmail.com']){
    await assert.rejects(mocked(origin,fastmailConfig('example-token')),error=>error.message==='untrusted JMAP URL'&&error.calls.length===1);
  }
  assert.throws(()=>fastmailConfig('token\nheader'));
  await assert.rejects(JMAPStore.open({...fastmailConfig('example-token'),sessionUrl:'https://evil.test/session'}),/invalid Fastmail/);
});
test('generic JMAP keeps exact-origin trust unless additional origins are explicit',async()=>{
  const cfg={sessionUrl:'https://mail.example.test/session',authorization:'Bearer example-token'};
  await assert.rejects(mocked('https://api.example.test',cfg),error=>error.message==='untrusted JMAP URL'&&error.calls.length===1);
  assert.equal((await mocked('https://api.example.test',{...cfg,trustedOrigins:['https://api.example.test']})).length,3);
});
