// Dependency-free tests of the real client code. DOM behavior is also checked
// in Chrome against the documented real-relay integration fixture.
const {test} = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');
const path = require('node:path');
const source = fs.readFileSync(process.env.TINCAN_UI_SOURCE || path.join(__dirname,'static/app.js'),'utf8');
class Node {
  constructor(tag='div'){ this.tagName=tag.toUpperCase();this.children=[];this.dataset={};this.attrs={};this.value='';this.hidden=false;this.disabled=false;this.parentElement=null;this._text='';this.listeners={};this.scrollHeight=200;this.clientHeight=400;this.scrollTop=0;this.style={setProperty(){}};
    const classes=new Set();this.classList={add:(...c)=>c.forEach(x=>classes.add(x)),remove:(...c)=>c.forEach(x=>classes.delete(x)),contains:c=>classes.has(c),toggle:(c,on)=>{if(on===undefined)on=!classes.has(c);on?classes.add(c):classes.delete(c);return on;}};
  }
  set innerHTML(v){throw Error('HTML injection sink used');}
  set textContent(v){this._text=String(v??'');this.children=[];}
  get textContent(){return this._text+this.children.map(c=>typeof c==='string'?c:c.textContent).join('');}
  get lastElementChild(){return [...this.children].reverse().find(c=>typeof c!=='string');}
  append(...kids){for(const c of kids){if(c instanceof Node){c.remove();c.parentElement=this;}this.children.push(c);}}
  replaceChildren(...kids){for(const c of this.children)if(c instanceof Node)c.parentElement=null;this.children=[];this._text='';this.append(...kids);}
  insertBefore(node,before){node.remove();const i=this.children.indexOf(before);this.children.splice(i<0?this.children.length:i,0,node);node.parentElement=this;}
  replaceWith(node){if(this.parentElement){this.parentElement.insertBefore(node,this);this.remove();}}
  remove(){if(this.parentElement){const a=this.parentElement.children;a.splice(a.indexOf(this),1);this.parentElement=null;}}
  setAttribute(k,v){this.attrs[k]=String(v);if(k.startsWith('data-'))this.dataset[k.slice(5).replace(/-([a-z])/g,(_,c)=>c.toUpperCase())]=String(v);}
  getAttribute(k){return this.attrs[k]??null;}
  removeAttribute(k){delete this.attrs[k];}
  addEventListener(k,v){(this.listeners[k]??=[]).push(v);}
  async fire(k,e={}){for(const f of this.listeners[k]||[])await f({preventDefault(){},target:this,...e});}
  focus(){this.doc.activeElement=this;}
  select(){} setSelectionRange(){} scrollTo(){} scrollIntoView(){} getClientRects(){return [{}];}
  showModal(){this.open=true;} close(){this.open=false;for(const f of this.listeners.close||[])f({});}
  querySelector(){return null;} querySelectorAll(){return [];}
}
function harness(fetcher=async()=>response({})){
  const ids=new Map(),storage=new Map(); let context;const events={};
  const document={readyState:'loading',hidden:false,title:'',activeElement:null,
    createElement:tag=>{const n=new Node(tag);n.doc=document;return n;},createElementNS:(_,tag)=>document.createElement(tag),
    getElementById:id=>{if(!ids.has(id)){const n=document.createElement('div');n.id=id;ids.set(id,n);}return ids.get(id);},
    addEventListener:(k,f)=>events[k]=f,
    querySelector:s=>document.getElementById(s==='.chat'?'chat':'login-card'),
    querySelectorAll:s=>s.startsWith('meta[')?[document.getElementById('meta0'),document.getElementById('meta1')]:[],
  };
  document.documentElement=document.createElement('html');document.body=document.createElement('body');
  for(const id of ['target-hint','connection','jump'])document.getElementById(id).append(document.createElement('span'));
  const store={getItem:k=>storage.get(k)||null,setItem:(k,v)=>storage.set(k,v),removeItem:k=>storage.delete(k)};
  context={document,location:{href:'http://127.0.0.1:8799/'},navigator:{platform:'Mac',clipboard:{writeText:async()=>{}}},localStorage:store,sessionStorage:store,
    URL,TextEncoder,AbortController,crypto:require('node:crypto').webcrypto,CSS:{supports:()=>true,escape:s=>s},innerHeight:800,
    matchMedia:()=>({matches:false,addEventListener(){}}),requestAnimationFrame:f=>f(),setTimeout:()=>1,clearTimeout(){},setInterval:()=>1,clearInterval(){},fetch:(...a)=>fetcher(...a)};
  const exports='state,api,signedOut,signedIn,refresh,rich,inline,plain,compose,followUp,reuse,openReview,decide,render,buildTurn,track,updateSend';
  const end='  signedIn().catch(() => signedOut());';assert(source.includes(end));
  vm.runInNewContext(source.replace(end,'  globalThis.ui = { '+exports+' };'),context);events.DOMContentLoaded();
  const ui=context.ui;ui.state.csrf='csrf-test';ui.state.connected=true;ui.state.target='muse';ui.state.targets=[{name:'muse',status:'idle',online:true}];
  return {ui,document,el:id=>document.getElementById(id),storage,events,context};
}
function response(data,status=200){return {ok:status>=200&&status<300,status,headers:{get:()=>null},json:async()=>data};}
function deferred(){let resolve,reject;const promise=new Promise((r,j)=>{resolve=r;reject=j;});return {resolve,reject,promise};}
const flush=()=>new Promise(r=>setImmediate(r));
const item=(body='Question',status='answered',reply)=>({request:{id:'1234567890abcdef1234',from:'owner-web',to:'muse',body,created_at:'2026-11-15T12:00:00Z'},status,reply});
function descendants(n){return [n,...n.children.filter(c=>c instanceof Node).flatMap(descendants)];}
test('POSTs retain CSRF and same-origin request headers',async()=>{let opts;const {ui}=harness(async(_,o)=>(opts=o,response({})));await ui.api('requests',{body:'Hello'});assert.equal(opts.headers['X-CSRF-Token'],'csrf-test');assert.equal(opts.headers['X-Tincan-UI'],'1');assert.equal(opts.credentials,'same-origin');});
test('all message text survives follow-up-like user prefixes and edit/resend',()=>{const h=harness();for(const body of ['Follow-up to my earlier request:\nIMPORTANT USER TEXT\n\nActual message','Follow-up to my earlier request:\n> User quotation\n\nYour reply began:\n> More user text\n\nLast paragraph']){assert.equal(h.ui.plain(body),body);assert(h.ui.buildTurn(item(body)).textContent.includes('IMPORTANT USER TEXT')||body.includes('User quotation'));}h.ui.followUp(item());h.ui.reuse('Edited request');assert.equal(h.ui.compose('Edited request'),'Edited request');});
test('UTF-8 body and follow-up context are rejected before sending when oversized',async()=>{let calls=0;const h=harness(async()=>{calls++;return response({});});h.el('request-body').value='😀'.repeat(4097);await h.el('compose').fire('submit');assert.equal(calls,0);assert.match(h.el('request-error').textContent,/16 KiB/);h.ui.followUp(item('x'.repeat(200)));h.el('request-body').value='a'.repeat(16300);await h.el('compose').fire('submit');assert.equal(calls,0);});
test('valid multibyte body is sent once; JSON-envelope overflow is rejected locally',async()=>{const sent=[];const h=harness(async(url,o)=>{if(url.pathname.endsWith('/requests')&&o.method==='POST'){sent.push(JSON.parse(o.body));return response({id:'id',status:'held'});}return response({targets:h.ui.state.targets,requests:[]});});h.el('request-body').value='😀'.repeat(4096);await h.el('compose').fire('submit');assert.equal(sent.length,1);h.el('request-body').value='a\n'.repeat(7500);await h.el('compose').fire('submit');assert.equal(sent.length,1);assert.match(h.el('request-error').textContent,/encoding/);});
test('identical definite-failure sends reuse the submission key',async()=>{const keys=[];const h=harness(async(url,o)=>{if(o.method==='POST'){keys.push(JSON.parse(o.body).submission_key);return response({error:'try later'},503);}return response({targets:h.ui.state.targets,requests:[]});});h.el('request-body').value='same text';await h.el('compose').fire('submit');await h.el('compose').fire('submit');assert.equal(keys.length,2);assert.equal(keys[0],keys[1]);});
for(const status of [409,502])test('uncertain/'+status+' sends cannot be resent by a second submit',async()=>{let posts=0;const h=harness(async(_,o)=>{if(o.method==='POST'){posts++;return response({error:'uncertain',uncertain:true},status);}return response({targets:h.ui.state.targets,requests:[]});});h.el('request-body').value='do once';await h.el('compose').fire('submit');await h.el('compose').fire('submit');assert.equal(posts,1);assert.match(h.el('request-error').textContent,/may already/);});
test('late refresh cannot restore private data after sign-out',async()=>{const d=deferred();const h=harness(()=>d.promise);const pending=h.ui.refresh();h.ui.signedOut();d.resolve(response({targets:[{name:'private-agent'}],requests:[item('PRIVATE REQUEST')]}));await pending;assert.equal(h.ui.state.items.length,0);assert.equal(h.ui.state.targets.length,0);assert(!h.el('feed').textContent.includes('PRIVATE'));assert.equal(h.ui.state.csrf,'');});
test('late send cannot restore state or throw after sign-out',async()=>{const d=deferred();const h=harness(()=>d.promise);h.el('request-body').value='PRIVATE DRAFT';const pending=h.el('compose').fire('submit');h.ui.signedOut();d.resolve(response({error:'denied'},401));await pending;assert.equal(h.ui.state.submission,null);assert.equal(h.el('request-body').value,'');assert.equal(h.ui.state.busy,false);});
test('late review never opens a dialog after sign-out',async()=>{const d=deferred();const h=harness(()=>d.promise);const pending=h.ui.openReview('1234567890abcdef1234');h.ui.signedOut();d.resolve(response({request:{to:'muse',body:'PRIVATE REVIEW'},approve_token:'a',deny_token:'d'}));await pending;assert(!h.el('review').open);assert.equal(h.el('review-body').textContent,'');assert.equal(h.ui.state.review,null);});
test('sign-out clears private drafts, target, context, toast and request state',()=>{const h=harness();h.ui.followUp(item('private context'));h.el('toast').textContent='Private agent name';h.storage.set('tincan-draft','private draft');h.ui.state.busy=true;h.ui.signedOut();assert.equal(h.ui.state.target,null);assert.equal(h.el('context-quote').textContent,'');assert.equal(h.el('toast').textContent,'');assert.equal(h.storage.has('tincan-draft'),false);assert(h.el('send').disabled);});
test('review uses raw text and one-use tokens; repeated click does not resend decision',async()=>{const posts=[];const d=deferred();const raw='<script> & **literal**';const h=harness(async(url,o)=>{posts.push({url:String(url),body:JSON.parse(o.body)});return url.pathname.endsWith('/review')?response({request:{to:'muse',body:raw},approve_token:'fresh-a',deny_token:'fresh-d'}):d.promise;});await h.ui.openReview('1234567890abcdef1234');assert.equal(h.el('review-body').textContent,raw);const a=h.ui.decide('approve'),b=h.ui.decide('approve');await flush();const decisions=posts.filter(p=>p.url.endsWith('/decision'));d.resolve(response({}));await Promise.all([a,b]);assert.equal(decisions.length,1);assert.equal(decisions[0].body.review_token,'fresh-a');});
test('rich text keeps HTML and unsafe schemes inert; links are http(s) and isolated',()=>{const h=harness();const html='<img src=x onerror=alert(1)> javascript:alert(1) data:text/html,bad https://example.test/path **bold** `code`';const rich=h.ui.rich(html);const nodes=descendants(rich);assert(!nodes.some(n=>['IMG','SCRIPT'].includes(n.tagName)));const links=nodes.filter(n=>n.tagName==='A');assert.equal(links.length,1);for(const a of links){assert.match(a.attrs.href,/^https?:\/\//);assert.equal(a.attrs.rel,'noopener noreferrer');assert.equal(a.attrs.target,'_blank');}assert(rich.textContent.includes('<img src=x onerror=alert(1)>'));});
test('relay empty decline is a system line, not an agent bubble; terminal states remain visible',()=>{const h=harness();for(const status of ['declined','expired','failed']){const turn=h.ui.buildTurn(item('question',status,{from:'relay',body:'',status}));assert(descendants(turn).some(n=>n.className==='sysline'));assert(!descendants(turn).some(n=>(n.className||'').includes('msg-agent')));}const needs=h.ui.buildTurn(item('question','needs_input'));assert.match(needs.textContent,/new request/);});
test('show-more changes the keyed render signature and expands the actual message',()=>{const h=harness();h.ui.state.items=[item('x'.repeat(1500))];h.ui.render();const first=h.ui.state.nodes.get('1234567890abcdef1234').el;const more=descendants(first).find(n=>n.className==='more');more.listeners.click[0]();assert.notEqual(h.ui.state.nodes.get('1234567890abcdef1234').el,first);});
test('client never emits inline styles or HTML injection sinks',()=>{assert(!/\.innerHTML\s*=|insertAdjacentHTML\s*\(|setAttribute\(['"]style['"]|\.style\b/.test(source));});

test('follow-up context cannot leak to another target',()=>{const h=harness();h.ui.followUp(item('private context'));h.ui.state.target='another-agent';assert.equal(h.ui.compose('new text'),'new text');});
test('late successful send cannot repopulate toast or live text after sign-out',async()=>{const d=deferred();const h=harness(()=>d.promise);h.el('request-body').value='private send';const pending=h.el('compose').fire('submit');h.ui.signedOut();d.resolve(response({id:'test',status:'held'}));await pending;assert.equal(h.el('live').textContent,'');assert.equal(h.el('toast').textContent,'');});
test('late network failure after sign-out does not access a cleared submission',async()=>{const d=deferred();const h=harness(()=>d.promise);h.el('request-body').value='private send';const pending=h.el('compose').fire('submit');h.ui.signedOut();d.reject(new Error('Network disconnected'));await pending;assert.equal(h.ui.state.submission,null);});

test('typing the next message during a send preserves the new draft',async()=>{const d=deferred();const h=harness(async(_,o)=>o.method==='POST'?d.promise:response({targets:h.ui.state.targets,requests:[]}));h.el('request-body').value='first message';const pending=h.el('compose').fire('submit');h.el('request-body').value='next message';await h.el('request-body').fire('input');d.resolve(response({id:'id',status:'held'}));await pending;assert.equal(h.el('request-body').value,'next message');assert.deepEqual(JSON.parse(h.storage.get('tincan-conversation-drafts')),[['muse','next message']]);});

function openConversation(h, name) {
  const button = descendants(h.el('conversations')).find(n => n.dataset.conversation === name);
  assert.ok(button);
  return button.fire('click');
}

test('switching recipients keeps unsent drafts in their own chat', async () => {
  const h = harness();
  h.ui.state.targets.push({name:'dot-web', online:true});
  h.ui.render();
  const composer = h.el('request-body');
  composer.value = 'Muse draft only';
  await composer.fire('input');
  await openConversation(h, 'dot-web');
  assert.equal(composer.value, '');
  composer.value = 'Dot draft only';
  await composer.fire('input');
  await openConversation(h, 'muse');
  assert.equal(composer.value, 'Muse draft only');
  await openConversation(h, 'dot-web');
  assert.equal(composer.value, 'Dot draft only');
});

test('a pending send stays with its recipient when switching chats', async () => {
  const pending = deferred();
  const posted = [];
  const h = harness(async (_, options) => {
    if (options.method === 'POST') {
      posted.push(JSON.parse(options.body).target);
      return pending.promise;
    }
    return response({targets:h.ui.state.targets, requests:[]});
  });
  h.ui.state.targets.push({name:'dot-web', online:true});
  h.ui.render();
  const composer = h.el('request-body');
  composer.value = 'To Muse only';
  await composer.fire('input');
  const send = h.el('compose').fire('submit');
  await openConversation(h, 'dot-web');
  const preview = h.el('sending-slot');
  assert.ok(preview.hidden || !preview.textContent.includes('To Muse only'));
  composer.value = 'Unsent Dot draft';
  await composer.fire('input');
  pending.resolve(response({id:'id',status:'pending'}));
  await send;
  assert.deepEqual(posted, ['muse']);
  assert.equal(composer.value, 'Unsent Dot draft');
  await openConversation(h, 'muse');
  assert.equal(composer.value, '');
});

test('agent delivery shows recovery, separates targets, and clears private status on logout', async () => {
  const h = harness();
  h.ui.state.targets[0].delivery = {
    last_reply: '2026-01-03T12:30:00Z',
    last_problem: '2026-01-02T12:30:00Z', problem: 'editor',
  };
  h.ui.state.targets.push({name: 'dot-web', online: true, delivery: {}});
  h.ui.render();
  assert.match(h.el('delivery-problem').textContent, /Browser could not submit/);
  assert.match(h.el('delivery-action').textContent, /answered after/);
  h.el('delivery').open = true;
  await openConversation(h, 'dot-web');
  assert.equal(h.el('delivery').open, false);
  assert.equal(h.el('delivery-summary').textContent, 'No verified reply');
  assert.equal(h.el('delivery-problem').textContent, 'None recorded');
  h.ui.state.targets[1].browser_signed_out = true;
  h.ui.render();
  assert.match(h.el('delivery-summary').textContent, /sign-in needed/);
  h.ui.signedOut();
  assert.equal(h.el('delivery').hidden, true);
  assert.equal(h.el('delivery-summary').textContent, '');
});

test('an observed delivery issue gives a next step and a missing history stays unknown', () => {
  const h = harness();
  h.ui.state.targets[0].delivery = {last_problem:'2026-01-05T10:00:00Z', problem:'extension'};
  h.ui.render();
  assert.equal(h.el('delivery-summary').textContent, 'Extension disconnected');
  assert.match(h.el('delivery-action').textContent, /extension is enabled/);
  delete h.ui.state.targets[0].delivery;
  h.ui.render();
  assert.equal(h.el('delivery-summary').textContent, 'History unavailable');
});

test('group address and displayed recipients agree; direct drafts are never forwarded', async () => {
  const writes = [];
  const h = harness(async (url, opts) => {
    if (opts.method === 'POST') {
      writes.push({path:url.pathname, ...JSON.parse(opts.body)});
      return response({id:'post'});
    }
    return response({targets:h.ui.state.targets, requests:[], group_posts:[]});
  });
  h.ui.state.groupEnabled = true;
  h.ui.state.posts = [{id:'old-post',body:'Earlier message',created_at:new Date().toISOString()}];
  h.ui.state.targets.push({name:'dot-web', online:true});
  const ta = h.el('request-body');
  ta.value = 'private draft';
  await ta.fire('input');
  h.ui.render();
  await openConversation(h, 'group/muse-dot');
  assert.equal(ta.value, '');
  for (const [text, audience, body] of [
    ['First post', 'both', 'First post'],
    ['@Dot second post', 'dot-web', 'second post'],
    ['@Muse: third post', 'muse', 'third post'],
  ]) {
    ta.value = text;
    await ta.fire('input');
    assert.equal(h.el('group-audience').value, audience);
    await h.el('compose').fire('submit');
    assert.equal(writes.at(-1).audience, audience);
    assert.equal(writes.at(-1).body, body);
    assert.equal(writes.at(-1).path, '/api/group');
  }
  ta.value = '@Other ambiguous'; await h.el('compose').fire('submit');
  assert.equal(writes.length, 3);
  await openConversation(h, 'muse');
  assert.equal(ta.value, 'private draft');
  h.ui.signedOut();
  assert.equal(h.ui.state.posts.length, 0);
  assert.equal(h.el('group-route').hidden, true);
});

test('one group post shows both outcomes, appears once and stays outside the direct chat', async () => {
  const h = harness();
  h.ui.state.groupEnabled = true;
  h.ui.state.targets.push({name:'dot-web',online:true});
  const member = item('shared post','answered',{from:'muse',body:'group reply',status:'answered',created_at:new Date().toISOString()});
  member.request.group = 'owner-group-sample';
  h.ui.state.items = [member];
  h.ui.state.posts = [{id:member.request.group,audience:'both',body:'shared post',created_at:member.request.created_at,requests:[member]}];
  h.ui.render(); assert(!h.el('feed').textContent.includes('shared post'));
  await openConversation(h, 'group/muse-dot');
  assert.equal(h.el('feed').textContent.split('shared post').length, 2);
  assert.match(h.el('feed').textContent,/group reply/);
  assert.match(h.el('feed').textContent,/Dot · Delivery unconfirmed/);
});

test('shared thread uses mentions and offers the other agent on a reply', async () => {
  const writes=[];
  const h=harness(async (url,o)=>{
    if(o.method==='POST') { writes.push({path:url.pathname,...JSON.parse(o.body)}); return response({id:'post'}); }
    return response({targets:h.ui.state.targets,requests:[],shared_posts:h.ui.state.shared});
  });
  h.ui.state.groupEnabled=true;
  h.ui.state.audience='dot-web'; // An earlier group preference cannot narrow a new shared turn.
  h.ui.state.targets.push({name:'dot-web',online:true});
  const reply=item('Landing page idea','answered',{from:'muse',body:'Lead with a demo.',status:'answered',created_at:new Date().toISOString()});
  reply.request.group='owner-shared-sample';
  h.ui.state.shared=[{id:reply.request.group,audience:'muse',body:'Landing page idea',created_at:reply.request.created_at,requests:[reply]}];
  h.ui.state.items=[reply];
  h.ui.render(); await openConversation(h,'shared/muse-dot');
  assert.equal(h.el('group-route').hidden,true);
  assert.match(h.el('head-presence').textContent,/Both agents can see/);
  const ta=h.el('request-body'); ta.value='First question'; await h.el('compose').fire('submit');
  assert.equal(writes.at(-1).path,'/api/shared'); assert.equal(writes.at(-1).audience,'both');
  const ask=descendants(h.el('feed')).find(n=>n.tagName==='BUTTON' && n.textContent==='Ask Dot about this');
  assert.ok(ask); await ask.fire('click');
  assert.equal(ta.value,'@Dot ');ta.value+='What would you change?';await h.el('compose').fire('submit');
  assert.equal(writes.at(-1).audience,'dot-web');assert.match(writes.at(-1).body,/Muse replied:\n> Lead with a demo\./);
  await openConversation(h,'muse');assert(!h.el('feed').textContent.includes('Lead with a demo.'));
  h.ui.signedOut();assert.equal(h.ui.state.shared.length,0);
});
