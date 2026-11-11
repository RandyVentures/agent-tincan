'use strict';
const $ = id => document.getElementById(id);
const state = { csrf:'', items:[], targets:[], selected:null, busy:false, polling:false, submission:null, review:null, interval:null };
const root = new URL('./', location.href);
const labels = {held:'Needs approval',queued:'Waiting for agent',delivered:'Delivered',claimed:'In progress',answered:'Answered',failed:'Could not complete',declined:'Declined',cancelled:'Cancelled',expired:'Expired',needs_input:'Needs your input'};
function say(text){ $('live').textContent=text; }
function error(id,text){$(id).textContent=text;$(id).hidden=!text;}
function notice(text){$('notice').textContent=text;$('notice').hidden=!text;}
async function api(path,body){
  const ctl=new AbortController();const timer=setTimeout(()=>ctl.abort(),20000);
  try{
    const res=await fetch(new URL('api/'+path,root),{method:body===undefined?'GET':'POST',credentials:'same-origin',cache:'no-store',headers:{'Content-Type':'application/json','X-Tincan-UI':'1',...(state.csrf?{'X-CSRF-Token':state.csrf}:{})},body:body===undefined?undefined:JSON.stringify(body),signal:ctl.signal});
    const data=await res.json().catch(()=>({}));
    if(res.headers.get('X-Tincan-Test-Fixture')==='1')$('mode').textContent='Local integration test · simulated Muse';
    if(!res.ok){if(res.status===401&&path!=='login')signedOut();throw Object.assign(new Error(data.error||'Could not reach the workspace.'),{status:res.status,uncertain:data.uncertain});}
    return data;
  }catch(e){if(e.name==='AbortError')throw Object.assign(new Error('The connection timed out. Check request history before sending again.'),{uncertain:true});throw e;}finally{clearTimeout(timer);}
}
function signedOut(){state.csrf='';state.items=[];state.targets=[];state.selected=null;state.submission=null;state.review=null;clearInterval(state.interval);state.interval=null;$('history-list').replaceChildren();for(const id of ['request-text','reply-text','review-body','review-route','progress','live'])$(id).textContent='';$('request-body').value='';$('workspace').hidden=true;$('login-view').hidden=false;$('sign-out').hidden=true;if($('review').open)$('review').close();}
async function signedIn(){const sess=await api('session');state.csrf=sess.csrf;$('login-view').hidden=true;$('workspace').hidden=false;$('sign-out').hidden=false;await refresh();clearInterval(state.interval);state.interval=setInterval(()=>{if(!document.hidden)refresh();},4000);}
async function refresh(){
  if(state.polling||!state.csrf)return;state.polling=true;
  try{const [agents,requests]=await Promise.all([api('targets'),api('requests')]);state.targets=agents.targets;state.items=requests.requests;state.items.sort((a,b)=>new Date(b.request.created_at)-new Date(a.request.created_at));$('connection').textContent='Connected';notice('');render();}
  catch(e){if(state.csrf){$('connection').textContent='Connection interrupted';notice(e.message);$('send').disabled=true;}}
  finally{state.polling=false;}
}
function render(){
  const list=$('history-list');list.replaceChildren();$('history-empty').hidden=state.items.length>0;
  for(const item of state.items){const li=document.createElement('li');const button=document.createElement('button');button.type='button';button.className='history-item';button.setAttribute('aria-current',String(state.selected===item.request.id));const title=document.createElement('strong');title.textContent=item.request.body.slice(0,80);const status=document.createElement('span');status.textContent=item.request.to+' · '+(labels[item.status]||item.status);button.append(title,status);button.addEventListener('click',()=>{state.selected=item.request.id;error('request-error','');render();});li.append(button);list.append(li);}
  const current=$('target').value;$('target').replaceChildren();for(const target of state.targets){const option=document.createElement('option');option.value=target.name;option.textContent=target.name+' · '+(target.status||'offline');$('target').append(option);}if(state.targets.some(t=>t.name===current))$('target').value=current;
  const item=state.items.find(i=>i.request.id===state.selected);$('detail').hidden=!item;$('compose').hidden=!!item;$('thread-state').hidden=!item;$('thread-title').textContent=item?'Request to '+item.request.to:'What would you like done?';
  if(item){$('thread-state').textContent=labels[item.status]||item.status;$('request-text').textContent=item.request.body;$('request-time').textContent=new Date(item.request.created_at).toLocaleString();$('reply').hidden=!item.reply;if(item.reply){$('reply-name').textContent=item.reply.from;$('reply-text').textContent=item.reply.body;}$('held-actions').hidden=item.status!=='held';const progress=item.progress?.note||'';$('progress').hidden=!progress;$('progress').textContent=progress;}
  recipient();$('send').disabled=state.busy||state.targets.length===0;
}
function recipient(){const target=state.targets.find(t=>t.name===$('target').value);$('target-status').textContent=target?.status||'';$('target-hint').textContent=target&&!target.online?'This agent is offline. Your request can wait in the queue.':'Requests may wait for your approval before delivery.';}
$('login-form').addEventListener('submit',async e=>{e.preventDefault();$('sign-in').disabled=true;error('login-error','');try{const auth=await api('login',{password:$('password').value});$('password').value='';state.csrf=auth.csrf;await signedIn();}catch(e){error('login-error',e.message);}finally{$('sign-in').disabled=false;}});
$('sign-out').addEventListener('click',async()=>{try{await api('logout',{});state.items=[];state.targets=[];state.selected=null;state.submission=null;$('request-body').value='';signedOut();notice('');}catch(e){notice(e.message);}});
$('new-request').addEventListener('click',()=>{state.selected=null;error('request-error','');render();$('request-body').focus();});
$('refresh').addEventListener('click',refresh);$('target').addEventListener('change',recipient);
$('compose').addEventListener('submit',async e=>{
  e.preventDefault();if(state.busy)return;const body=$('request-body').value.trim(),target=$('target').value;if(!body||!target)return;
  const same=state.submission&&state.submission.body===body&&state.submission.target===target;
  if(same&&state.submission.uncertain){error('request-error','This request may already be in your history. Refresh and check it before creating another.');return;}
  if(!same)state.submission={key:crypto.randomUUID(),body,target,uncertain:false};
  state.busy=true;$('send').disabled=true;error('request-error','');
  try{const sent=await api('requests',{target,body,submission_key:state.submission.key});$('request-body').value='';state.submission=null;state.selected=sent.id;say('Request saved. '+(sent.status==='held'?'Waiting for your approval.':'Waiting for the agent.'));await refresh();}
  catch(e){if(e.uncertain||!e.status||e.status===409)state.submission.uncertain=true;error('request-error',e.message);await refresh();}
  finally{state.busy=false;$('send').disabled=state.targets.length===0;}
});
$('open-review').addEventListener('click',async()=>{const id=state.selected;error('request-error','');$('open-review').disabled=true;try{const review=await api('requests/'+encodeURIComponent(id)+'/review',{});state.review={...review,id};$('review-route').textContent='You → '+review.request.to;$('review-body').textContent=review.request.body;error('review-error','');$('approve').disabled=$('deny').disabled=false;$('review').showModal();}catch(e){error('request-error',e.message);await refresh();}finally{$('open-review').disabled=false;}});
$('close-review').addEventListener('click',()=>{$('review').close();state.review=null;});
async function decide(decision){if(!state.review)return;const review=state.review;$('approve').disabled=$('deny').disabled=true;error('review-error','');try{await api('requests/'+encodeURIComponent(review.id)+'/decision',{decision,review_token:review[decision+'_token']});$('review').close();state.review=null;say(decision==='approve'?'Request approved for delivery.':'Request denied.');await refresh();}catch(e){state.review=null;error('review-error',e.message+' Close this review and refresh before trying again.');await refresh();}finally{$('approve').disabled=$('deny').disabled=!state.review;}}
$('approve').addEventListener('click',()=>decide('approve'));$('deny').addEventListener('click',()=>decide('deny'));
$('review').addEventListener('close',()=>{state.review=null;});
document.addEventListener('visibilitychange',()=>{if(!document.hidden&&state.csrf)refresh();});
signedIn().catch(()=>signedOut());
