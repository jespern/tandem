import {test} from 'node:test';
import assert from 'node:assert/strict';
import {JMAPBlobFolder} from '../dist/browser/blob-jmap.js';
import {fastmailConfig, NetworkFailure} from '../dist/browser/jmap.js';
import {chunkMessage} from '../dist/blob-folder.js';
const core='urn:ietf:params:jmap:core',mail='urn:ietf:params:jmap:mail';
const envelope=Uint8Array.from({length:64},(_,i)=>i),channel='C'.repeat(32);
// Fastmail's session puts downloads on another host than the API (www.fastmailusercontent.com).
async function mocked(download,work){
  const original=globalThis.fetch,calls=[],cfg=fastmailConfig('example-token');
  globalThis.fetch=async(url,init={})=>{
    calls.push({url,authorization:init.headers?.Authorization});
    if(url===cfg.sessionUrl){const response=Response.json({capabilities:{[core]:{},[mail]:{}},primaryAccounts:{[mail]:'account'},accounts:{account:{accountCapabilities:{[mail]:{}}}},
      apiUrl:'https://api.fastmail.com/jmap/api/',uploadUrl:'https://api.fastmail.com/jmap/upload/{accountId}/',downloadUrl:download});Object.defineProperty(response,'url',{value:url});return response;}
    if(url.startsWith('https://api.fastmail.com/jmap/api/')){
      const [method]=JSON.parse(init.body).methodCalls[0];
      const result=method==='Mailbox/query'?{ids:['folder']}:method==='Mailbox/get'?{list:[{id:'folder',name:'Tandem-Blobs',parentId:null}]}:{list:[{id:'m1',blobId:'b1',mailboxIds:{folder:true},size:400}],notFound:[]};
      return Response.json({methodResponses:[[method,result,'0']]});
    }
    return new Response(chunkMessage(channel,envelope));
  };
  const folder=await JMAPBlobFolder.open(cfg);
  try{return await work(folder,calls);}finally{globalThis.fetch=original;await folder.close();}
}
test('Fastmail blob folder downloads from Fastmail\'s content host with the token',async()=>{
  for(const host of ['www.fastmailusercontent.com','beta.fastmailusercontent.com'])
    await mocked(`https://${host}/jmap/download/{accountId}/{blobId}/{name}?type={type}`,async(folder,calls)=>{
      const got=await folder.fetch(['m1'],new AbortController().signal);
      assert.equal(got.length,1);assert.deepEqual([...got[0].envelope],[...envelope]);
      const fetched=calls.at(-1);assert.ok(fetched.url.startsWith(`https://${host}/jmap/download/account/b1/`));assert.equal(fetched.authorization,'Bearer example-token');
    });
});
test('Fastmail blob folder refuses lookalike download hosts as untrusted, before sending credentials',async()=>{
  for(const host of ['fastmailusercontent.com.evil.test','evil-fastmailusercontent.com','www.fastmailusercontent.com:8443'])
    await mocked(`https://${host}/jmap/download/{accountId}/{blobId}/{name}?type={type}`,async(folder,calls)=>{
      const before=calls.length;
      await assert.rejects(folder.fetch(['m1'],new AbortController().signal),error=>!(error instanceof NetworkFailure)&&error.message==='untrusted JMAP URL');
      assert.ok(calls.slice(before).every(call=>call.url.startsWith('https://api.fastmail.com/')),'no request reaches the lookalike');
    });
});
