'use strict';
/* Tincan owner workspace.
 * Security notes kept from the first version:
 *  - Every request/reply is rendered with DOM text nodes (never innerHTML).
 *  - A send reuses its submission key for identical text; an uncertain send
 *    is never repeated automatically.
 *  - Approve/deny always go through a freshly fetched, one-use review token. */

// Apply the saved appearance before first paint (this file loads in <head>).
const THEME_KEY = 'tincan-theme';
function readTheme() { try { const v = localStorage.getItem(THEME_KEY); return v === 'light' || v === 'dark' ? v : 'system'; } catch { return 'system'; } }
(function () { const t = readTheme(); if (t !== 'system') document.documentElement.dataset.theme = t; })();

if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', main); else main();

function main() {
  const $ = id => document.getElementById(id);
  const NS = 'http://www.w3.org/2000/svg';
  const root = new URL('./', location.href);
  const POLL_MS = 4000;
  const MAX_BODY = 16384;
  const encoder = new TextEncoder();
  const byteLength = text => encoder.encode(text).length;
  const state = {
    epoch: 0, controllers: new Set(), reviewPending: false, csrf: '', items: [], targets: [], target: null, busy: false, polling: false, connected: true,
    submission: null, review: null, interval: null, followUp: null, loaded: false,
    nodes: new Map(), days: new Map(), expanded: new Set(), lastStatus: new Map(), fixture: false,
  };

  const STATUS = {
    held: { label: 'Needs approval', tone: 'amber' },
    queued: { label: 'Queued', tone: 'blue' },
    delivered: { label: 'Delivered', tone: 'blue' },
    claimed: { label: 'In progress', tone: 'blue' },
    needs_input: { label: 'Needs your input', tone: 'amber' },
    answered: { label: 'Answered', tone: 'green' },
    failed: { label: 'Could not complete', tone: 'red' },
    declined: { label: 'Declined', tone: 'red' },
    cancelled: { label: 'Cancelled', tone: 'gray' },
    expired: { label: 'Expired', tone: 'gray' },
  };
  const ACTIVE = new Set(['held', 'queued', 'delivered', 'claimed', 'needs_input']);
  const PRESENCE = {
    idle: 'Online · listening',
    busy: 'Working on a request',
    stuck: 'Stuck — its session needs a look',
    limited: 'Usage limited for now',
    offline: 'Offline · requests wait in the queue',
  };
  const STARTERS = [
    ['Status check', 'Give me a quick status update on '],
    ['Draft', 'Draft a short reply to '],
    ['Research', 'Research and compare '],
    ['Summarize', 'Summarize the key points of '],
  ];

  /* ───────── DOM helpers ───────── */
  function h(tag, props, ...kids) {
    const el = document.createElement(tag);
    for (const [k, v] of Object.entries(props || {})) {
      if (v == null || v === false) continue;
      if (k === 'class') el.className = v;
      else if (k === 'text') el.textContent = v;
      else if (k.startsWith('on')) el.addEventListener(k.slice(2), v);
      else el.setAttribute(k, v === true ? '' : v);
    }
    for (const kid of kids.flat()) if (kid != null && kid !== false) el.append(kid);
    return el;
  }
  function icon(name) {
    const svg = document.createElementNS(NS, 'svg');
    svg.setAttribute('aria-hidden', 'true');
    const use = document.createElementNS(NS, 'use');
    use.setAttribute('href', '#i-' + name);
    svg.append(use);
    return svg;
  }
  function hue(name) { let x = 0; for (const c of name) x = (x * 31 + c.codePointAt(0)) % 360; return (x + 12) % 360; }
  function avatar(name, cls = '', presence = '') {
    const el = h('span', { class: 'avatar ' + cls, 'aria-hidden': 'true', text: (name || '?').slice(0, 1) });
    el.classList.add('hue-' + Math.floor(hue(name || '?') / 45));
    if (presence) el.dataset.presence = presence;
    return el;
  }
  const say = text => { $('live').textContent = text; };
  function error(id, text) { $(id).textContent = text || ''; $(id).hidden = !text; }
  function notice(text) { $('notice').textContent = text || ''; $('notice').hidden = !text; }
  let toastTimer;
  function toast(text, ic = 'check') {
    const t = $('toast'); t.replaceChildren(icon(ic), h('span', { text })); t.hidden = false;
    clearTimeout(toastTimer); toastTimer = setTimeout(() => { t.hidden = true; }, 2600);
  }
  const isMobile = () => matchMedia('(max-width: 820px)').matches;

  /* ───────── Time ───────── */
  const time = d => d.toLocaleTimeString([], { hour: 'numeric', minute: '2-digit' });
  function dayKey(d) { return d.getFullYear() + '-' + d.getMonth() + '-' + d.getDate(); }
  function dayLabel(d) {
    const today = new Date(); const y = new Date(); y.setDate(today.getDate() - 1);
    if (dayKey(d) === dayKey(today)) return 'Today';
    if (dayKey(d) === dayKey(y)) return 'Yesterday';
    const opts = { weekday: 'long', month: 'short', day: 'numeric' };
    if (d.getFullYear() !== today.getFullYear()) opts.year = 'numeric';
    return d.toLocaleDateString([], opts);
  }
  function short(d) {
    const s = (Date.now() - d) / 1000;
    if (s < 60) return 'now';
    if (s < 3600) return Math.floor(s / 60) + 'm';
    if (dayKey(d) === dayKey(new Date())) return time(d);
    if (s < 6 * 86400) return d.toLocaleDateString([], { weekday: 'short' });
    return d.toLocaleDateString([], { month: 'short', day: 'numeric' });
  }
  function elapsed(d) {
    const s = Math.max(0, Math.floor((Date.now() - d) / 1000));
    if (s < 60) return s + 's';
    if (s < 3600) return Math.floor(s / 60) + 'm ' + String(s % 60).padStart(2, '0') + 's';
    if (s < 86400) return Math.floor(s / 3600) + 'h ' + Math.floor((s % 3600) / 60) + 'm';
    return Math.floor(s / 86400) + 'd';
  }
  function refreshTimes() {
    for (const el of document.querySelectorAll('[data-elapsed]')) el.textContent = elapsed(new Date(el.dataset.elapsed));
    for (const el of document.querySelectorAll('[data-short]')) el.textContent = short(new Date(el.dataset.short));
  }

  /* ───────── Safe rich text (DOM nodes only) ───────── */
  const UL = /^\s*[-*•]\s+/, OL = /^\s*\d+[.)]\s+/, QUOTE = /^\s*>/, FENCE = /^\s*```/, HEAD = /^#{1,4}\s/, HR = /^\s*(-{3,}|\*{3,}|_{3,})\s*$/;
  const INLINE = /(`[^`\n]+`)|(\*\*[^*\n]+\*\*)|(https?:\/\/[^\s<>"'`]*[^\s<>"'`.,;:!?)\]])/g;
  function inline(el, text) {
    let last = 0;
    for (const m of text.matchAll(INLINE)) {
      if (m.index > last) el.append(text.slice(last, m.index));
      if (m[1]) el.append(h('code', { text: m[1].slice(1, -1) }));
      else if (m[2]) el.append(h('strong', { text: m[2].slice(2, -2) }));
      else el.append(h('a', { href: m[3], target: '_blank', rel: 'noopener noreferrer', text: m[3] }));
      last = m.index + m[0].length;
    }
    if (last < text.length) el.append(text.slice(last));
    return el;
  }
  function rich(text) {
    const out = h('div', { class: 'rich' });
    const lines = String(text).replace(/\r\n?/g, '\n').split('\n');
    let i = 0;
    while (i < lines.length) {
      const line = lines[i];
      if (!line.trim()) { i++; continue; }
      if (FENCE.test(line)) {
        const buf = []; i++;
        while (i < lines.length && !FENCE.test(lines[i])) buf.push(lines[i++]);
        i++; out.append(h('pre', {}, h('code', { text: buf.join('\n') }))); continue;
      }
      if (QUOTE.test(line)) {
        const buf = [];
        while (i < lines.length && QUOTE.test(lines[i])) buf.push(lines[i++].replace(/^\s*> ?/, ''));
        out.append(inline(h('blockquote'), buf.join('\n'))); continue;
      }
      if (HEAD.test(line)) { out.append(inline(h('h3'), line.replace(/^#{1,4}\s+/, ''))); i++; continue; }
      if (HR.test(line)) { out.append(h('hr')); i++; continue; }
      if (UL.test(line) || OL.test(line)) {
        const ordered = OL.test(line), re = ordered ? OL : UL;
        const list = h(ordered ? 'ol' : 'ul', ordered ? { start: parseInt(line, 10) > 1 ? parseInt(line, 10) : null } : {});
        while (i < lines.length && re.test(lines[i])) {
          let content = lines[i++].replace(re, '');
          while (i < lines.length && /^\s{2,}\S/.test(lines[i]) && !UL.test(lines[i]) && !OL.test(lines[i])) content += '\n' + lines[i++].trim();
          list.append(inline(h('li'), content));
        }
        out.append(list); continue;
      }
      const buf = [];
      while (i < lines.length && lines[i].trim() && !FENCE.test(lines[i]) && !QUOTE.test(lines[i]) && !UL.test(lines[i]) && !OL.test(lines[i]) && !HEAD.test(lines[i])) buf.push(lines[i++]);
      if (!buf.length) buf.push(lines[i++]);
      out.append(inline(h('p'), buf.join('\n')));
    }
    return out;
  }

  /* ───────── Appearance ───────── */
  const THEMES = [['system', 'Auto', 'auto'], ['light', 'Light', 'sun'], ['dark', 'Dark', 'moon']];
  function applyTheme(pref, animate) {
    if (animate) { document.body.classList.add('theme-anim'); setTimeout(() => document.body.classList.remove('theme-anim'), 400); }
    if (pref === 'system') delete document.documentElement.dataset.theme; else document.documentElement.dataset.theme = pref;
    try { if (pref === 'system') localStorage.removeItem(THEME_KEY); else localStorage.setItem(THEME_KEY, pref); } catch { /* storage may be blocked */ }
    for (const btn of document.querySelectorAll('[data-theme-switch] button')) {
      const selected = btn.dataset.value === pref;
      btn.setAttribute('aria-checked', String(selected)); btn.tabIndex = selected ? 0 : -1;
    }
    const meta = [...document.querySelectorAll('meta[name="theme-color"]')];
    const forced = pref === 'dark' ? '#0f1013' : pref === 'light' ? '#f4f2ee' : null;
    meta[0].content = forced || '#f4f2ee'; meta[1].content = forced || '#0f1013';
    const cur = THEMES.find(t => t[0] === pref);
    $('theme-cycle').replaceChildren(icon(cur[2]));
    $('theme-cycle').setAttribute('aria-label', 'Appearance: ' + cur[1] + ' (change)');
    $('theme-cycle').title = 'Appearance: ' + cur[1];
  }
  for (const wrap of document.querySelectorAll('[data-theme-switch]')) {
    wrap.addEventListener('keydown', e => {
      const buttons = [...wrap.querySelectorAll('button')];
      const index = buttons.indexOf(document.activeElement);
      const direction = { ArrowRight: 1, ArrowDown: 1, ArrowLeft: -1, ArrowUp: -1 }[e.key];
      if (index < 0 || (!direction && e.key !== 'Home' && e.key !== 'End')) return;
      e.preventDefault();
      const next = e.key === 'Home' ? 0 : e.key === 'End' ? buttons.length - 1 : (index + direction + buttons.length) % buttons.length;
      applyTheme(buttons[next].dataset.value, true); buttons[next].focus();
    });
    for (const [value, label, ic] of THEMES) {
      wrap.append(h('button', { type: 'button', role: 'radio', 'data-value': value, 'aria-checked': 'false', 'aria-label': label + ' appearance', onclick: () => applyTheme(value, true) }, icon(ic), h('span', { text: label })));
    }
  }
  $('theme-cycle').addEventListener('click', () => {
    const order = ['system', 'light', 'dark'];
    const next = order[(order.indexOf(readTheme()) + 1) % 3];
    applyTheme(next, true);
    toast('Appearance: ' + THEMES.find(t => t[0] === next)[1], THEMES.find(t => t[0] === next)[2]);
  });
  applyTheme(readTheme(), false);
  $('mod-key').textContent = /Mac|iPhone|iPad/.test(navigator.platform || navigator.userAgent) ? '⌘' : 'Ctrl';

  /* ───────── API ───────── */
  async function api(path, body) {
    const epoch = state.epoch;
    const ctl = new AbortController(); state.controllers.add(ctl); const timer = setTimeout(() => ctl.abort(), 20000);
    try {
      const res = await fetch(new URL('api/' + path, root), {
        method: body === undefined ? 'GET' : 'POST', credentials: 'same-origin', cache: 'no-store',
        headers: { 'Content-Type': 'application/json', 'X-Tincan-UI': '1', ...(state.csrf ? { 'X-CSRF-Token': state.csrf } : {}) },
        body: body === undefined ? undefined : JSON.stringify(body), signal: ctl.signal,
      });
      const data = await res.json().catch(() => ({}));
      if (epoch !== state.epoch) throw Object.assign(new Error('Session changed.'), { cancelled: true });
      if (res.headers.get('X-Tincan-Test-Fixture') === '1' && !state.fixture) {
        state.fixture = true;
        for (const b of document.querySelectorAll('[data-mode-badge]')) { b.textContent = 'Local test · simulated Muse'; b.hidden = false; }
      }
      if (!res.ok) {
        if (res.status === 401 && path !== 'login') signedOut();
        throw Object.assign(new Error(data.error || 'Could not reach the workspace.'), { status: res.status, uncertain: data.uncertain });
      }
      return data;
    } catch (e) {
      if (epoch !== state.epoch) throw Object.assign(new Error('Session changed.'), { cancelled: true });
      if (e.name === 'AbortError') throw Object.assign(new Error('The connection timed out. Check your conversation before sending again.'), { uncertain: true });
      throw e;
    } finally { clearTimeout(timer); state.controllers.delete(ctl); }
  }

  /* ───────── Session ───────── */
  function signedOut() {
    state.epoch++;
    for (const ctl of state.controllers) ctl.abort();
    state.controllers.clear();
    state.railSig = ''; state.busy = false; state.polling = false; state.connected = false; state.target = null; state.reviewPending = false;
    state.csrf = ''; state.items = []; state.targets = []; state.submission = null; state.review = null; state.followUp = null; state.loaded = false;
    state.nodes.clear(); state.days.clear(); state.expanded.clear(); state.lastStatus.clear();
    clearInterval(state.interval); state.interval = null;
    $('feed').replaceChildren(); $('sending-slot').replaceChildren(); $('conversations').replaceChildren(); $('approvals').replaceChildren();
    for (const id of ['review-body', 'review-route', 'review-error', 'live', 'head-name', 'head-presence', 'context-quote', 'approvals-count', 'count', 'login-error']) $(id).textContent = '';
    $('request-body').value = ''; clearDraft(); setContext(null); error('request-error', ''); notice('');
    $('workspace').hidden = true; $('login-view').hidden = false; closeRail(false);
    clearTimeout(toastTimer); $('toast').replaceChildren(); $('toast').hidden = true;
    $('password').value = ''; $('password').type = 'password';
    $('peek').setAttribute('aria-pressed', 'false'); $('peek').setAttribute('aria-label', 'Show passphrase');
    $('sign-in').disabled = false; $('send').disabled = true;
    $('approve').disabled = $('deny').disabled = true;
    $('loadbar').classList.remove('on'); $('refresh').classList.remove('spinning');
    if ($('review').open) $('review').close();
    document.title = 'Tincan';
    requestAnimationFrame(() => $('password').focus());
  }
  async function signedIn() {
    const epoch = state.epoch;
    const sess = await api('session');
    if (epoch !== state.epoch) return;
    state.csrf = sess.csrf;
    $('login-view').hidden = true; $('workspace').hidden = false;
    restoreDraft();
    await refresh();
    if (epoch !== state.epoch || !state.csrf) return;
    clearInterval(state.interval);
    state.interval = setInterval(() => { if (!document.hidden) refresh(); }, POLL_MS);
    if (!isMobile()) $('request-body').focus();
  }

  async function refresh(manual) {
    if (state.polling || !state.csrf) return;
    state.polling = true;
    const epoch = state.epoch;
    const loadTimer = setTimeout(() => { if (epoch === state.epoch) $('loadbar').classList.add('on'); }, manual ? 0 : 600);
    if (manual) $('refresh').classList.add('spinning');
    try {
      const [agents, requests] = await Promise.all([api('targets'), api('requests')]);
      if (epoch !== state.epoch) return;
      state.targets = agents.targets || [];
      state.items = (requests.requests || []).slice().sort((a, b) => new Date(a.request.created_at) - new Date(b.request.created_at));
      state.connected = true; setConn(true); notice('');
      render();
    } catch (e) {
      if (epoch === state.epoch && state.csrf) { state.connected = false; setConn(false); notice(e.message); updateSend(); }
    } finally {
      clearTimeout(loadTimer);
      if (epoch === state.epoch) {
        $('loadbar').classList.remove('on');
        setTimeout(() => { if (epoch === state.epoch) $('refresh').classList.remove('spinning'); }, 300);
        state.polling = false;
      }
    }
  }
  function setConn(ok) {
    const c = $('connection'); c.className = 'conn ' + (ok ? 'ok' : 'bad');
    c.lastElementChild.textContent = ok ? 'Live' : 'Reconnecting…';
    c.title = ok ? 'Updates every few seconds' : 'Trying to reach the relay';
  }

  /* ───────── Rendering ───────── */
  function targetNames() {
    const names = state.targets.map(t => t.name);
    for (const it of state.items) if (!names.includes(it.request.to)) names.push(it.request.to);
    return names;
  }
  function targetInfo(name) { return state.targets.find(t => t.name === name); }
  function presenceKey(t) { if (!t) return 'offline'; const s = t.status || (t.online ? 'idle' : 'offline'); return t.online || s !== 'idle' ? s : 'offline'; }
  const held = () => state.items.filter(i => i.status === 'held');
  // The relay records its own outcomes (a decline, an expiry) as a reply
  // "from relay", often with an empty body. Only an agent's text is a reply.
  const agentReply = it => it.reply && it.reply.from !== 'relay' && it.reply.body.trim() ? it.reply : null;
  const relayReply = it => it.reply && it.reply.from === 'relay' && it.reply.body.trim() ? it.reply.body.trim() : '';

  function render() {
    const names = targetNames();
    if (!names.includes(state.target)) state.target = names[0] || null;
    renderRail(names);
    renderHead();
    renderFeed();
    updateSend();
    const n = held().length;
    document.title = (n ? '(' + n + ') ' : '') + 'Tincan' + (state.target ? ' · ' + cap(state.target) : '');
    $('menu-dot').hidden = !n;
    refreshTimes();
    state.loaded = true;
  }
  const cap = s => s ? s.charAt(0).toUpperCase() + s.slice(1) : s;

  function renderRail(names) {
    const sig = JSON.stringify([names, state.target, state.targets, state.items]);
    if (state.railSig === sig) return;
    state.railSig = sig;
    const focused = document.activeElement;
    const focusTarget = focused?.dataset.conversation; const focusReview = focused?.dataset.review;
    const list = $('conversations'); list.replaceChildren();
    for (const name of names) {
      const items = state.items.filter(i => i.request.to === name);
      const last = items[items.length - 1];
      const pending = items.filter(i => i.status === 'held').length;
      const rep = last && agentReply(last);
      const preview = !last ? PRESENCE[presenceKey(targetInfo(name))] : rep ? rep.body
        : (ACTIVE.has(last.status) || last.status === 'answered' ? 'You: ' : (STATUS[last.status] || { label: last.status }).label + ' · ') + plain(last.request.body);
      const when = last ? new Date((last.reply && last.reply.created_at) || last.request.created_at) : null;
      list.append(h('li', {}, h('button', {
        type: 'button', class: 'convo', 'data-conversation': name, 'aria-current': String(name === state.target),
        onclick: () => { if (state.target !== name) { state.target = name; setContext(null); state.nodes.clear(); state.days.clear(); $('feed').replaceChildren(); state.loaded = false; render(); } closeRail(); $('request-body').focus({ preventScroll: true }); },
      },
      avatar(name, '', presenceKey(targetInfo(name))),
      h('span', { class: 'convo-main' },
        h('span', { class: 'convo-name' }, h('span', { text: cap(name) }), when ? h('time', { 'data-short': when.toISOString(), text: short(when) }) : null),
        h('span', { class: 'convo-preview', text: preview.replace(/\s+/g, ' ').slice(0, 90) })),
      pending ? h('span', { class: 'count', text: String(pending), 'aria-label': pending + ' waiting for approval' }) : null)));
    }
    const pend = held();
    $('approvals-section').hidden = !pend.length;
    $('approvals-count').textContent = pend.length || '';
    const ap = $('approvals'); ap.replaceChildren();
    for (const it of pend.slice().reverse()) {
      ap.append(h('li', {}, h('button', { type: 'button', class: 'approval', 'data-review': it.request.id, onclick: () => { closeRail(); focusTurn(it); openReview(it.request.id); } },
        h('strong', { text: plain(it.request.body).replace(/\s+/g, ' ').slice(0, 80) }),
        h('span', { text: 'To ' + cap(it.request.to) + ' · Review now →' }))));
    }
    const key = focusTarget ? '[data-conversation="' + CSS.escape(focusTarget) + '"]' : focusReview ? '[data-review="' + CSS.escape(focusReview) + '"]' : null;
    if (key) ($('rail').querySelector(key) || $('close-rail')).focus({ preventScroll: true });
  }

  function renderHead() {
    const name = state.target;
    const t = targetInfo(name);
    const key = presenceKey(t);
    $('head-avatar').replaceWith(Object.assign(avatar(name || '?', '', name ? key : ''), { id: 'head-avatar' }));
    $('head-name').textContent = name ? cap(name) : 'No agents yet';
    const p = $('head-presence'); p.replaceChildren();
    if (!name) p.textContent = 'Connect an agent to the relay to start a conversation.';
    else if (t && t.status === 'stuck' && t.status_reason) p.append(h('b', { text: 'Stuck' }), ' · ' + t.status_reason);
    else p.textContent = PRESENCE[key] || key;
  }

  function signature(it) {
    const t = targetInfo(it.request.to);
    return JSON.stringify([it.status, it.request.body, it.reply && [it.reply.body, it.reply.from, it.reply.status, it.reply.created_at],
      it.progress && [it.progress.note, it.progress.at], it.relay_note && it.relay_note.note,
      (it.exchanges || []).map(e => [e.question, e.answer]), it.status === 'queued' ? presenceKey(t) : '', state.expanded.has(it.request.id + ':q'), state.expanded.has(it.request.id + ':a')]);
  }

  function renderFeed() {
    const tl = $('timeline'), feed = $('feed');
    const nearBottom = tl.scrollHeight - tl.scrollTop - tl.clientHeight < 140;
    const items = state.items.filter(i => i.request.to === state.target);
    const wanted = []; let lastDay = ''; let changed = false; let gotReply = false;
    if (items.length >= 20) wanted.push(state.days.get('cap') || (state.days.set('cap', h('li', { class: 'day' }, h('span', { text: 'Showing your 20 most recent requests' }))), state.days.get('cap')));
    for (const it of items) {
      const d = new Date(it.request.created_at), k = dayKey(d);
      if (k !== lastDay) {
        if (!state.days.has(k)) state.days.set(k, h('li', { class: 'day' }, h('span', { text: dayLabel(d) })));
        wanted.push(state.days.get(k)); lastDay = k;
      }
      const id = it.request.id, sig = signature(it);
      let rec = state.nodes.get(id);
      if (!rec || rec.sig !== sig) {
        const el = buildTurn(it);
        if (!rec && state.loaded) el.classList.add('enter');
        if (rec) { rec.el.replaceWith(el); if (!rec.hadReply && agentReply(it)) gotReply = true; }
        rec = { sig, el, hadReply: !!agentReply(it) }; state.nodes.set(id, rec); changed = true;
      }
      const prev = state.lastStatus.get(id);
      if (state.loaded && prev && prev !== it.status) say(cap(it.request.to) + ': ' + (STATUS[it.status] || { label: it.status }).label);
      state.lastStatus.set(id, it.status);
      wanted.push(rec.el);
    }
    const keep = new Set(wanted);
    for (const child of [...feed.children]) if (!keep.has(child)) { child.remove(); changed = true; }
    wanted.forEach((node, i) => { if (feed.children[i] !== node) { feed.insertBefore(node, feed.children[i] || null); changed = true; } });

    const empty = !items.length;
    $('empty').hidden = !empty;
    if (empty) renderEmpty();
    if (!state.loaded || (changed && nearBottom)) scrollBottom(!state.loaded);
    else if (changed) showJump(gotReply ? 'New reply' : 'New activity');
  }

  function renderEmpty() {
    const name = state.target;
    $('empty-title').textContent = name ? 'Start a conversation with ' + cap(name) : 'No agents connected';
    $('empty-text').textContent = name
      ? 'Everything you send lands here as one continuous thread — approvals, progress and replies in line, oldest at the top.'
      : 'Once an agent joins the relay, it shows up here and you can message it.';
    const s = $('starters'); s.replaceChildren();
    if (!name) return;
    for (const [label, text] of STARTERS) s.append(h('button', { type: 'button', class: 'starter', onclick: () => { const ta = $('request-body'); ta.value = text; autosize(); ta.focus(); ta.setSelectionRange(text.length, text.length); saveDraft(); } }, icon('spark'), label));
  }

  function clampable(bubble, id, text) {
    if (text.length < 1400 && text.split('\n').length < 22) return [bubble];
    const open = state.expanded.has(id);
    if (!open) bubble.classList.add('clamped');
    return [bubble, h('button', { type: 'button', class: 'more', text: open ? 'Show less' : 'Show more', onclick: () => {
      if (state.expanded.has(id)) state.expanded.delete(id); else state.expanded.add(id);
      render();
    } })];
  }

  function chip(status) { const s = STATUS[status] || { label: status, tone: 'gray' }; return h('span', { class: 'chip t-' + s.tone, text: s.label }); }

  function track(it) {
    const name = cap(it.request.to), t = targetInfo(it.request.to), online = presenceKey(t) !== 'offline';
    const conf = {
      held: [1, 'warn', 'Waiting for your approval', name + ' can’t see it until you approve.'],
      queued: [2, '', 'Queued', online ? name + ' picks it up on its next check.' : name + ' is offline — it’ll wait in the queue.'],
      delivered: [3, '', 'Delivered', name + ' has it.'],
      claimed: [3, '', 'Working on it', name + ' is on it.'],
      needs_input: [3, 'warn', name + ' has a question', 'A follow-up sends a new request; it does not resolve this request’s question.'],
    }[it.status];
    if (!conf) return null;
    const [now, tone, title, sub] = conf;
    const note = it.progress && it.progress.note;
    const steps = h('div', { class: 'track-steps', 'aria-hidden': 'true' });
    for (let i = 0; i < 5; i++) steps.append(h('i', { class: i < now ? 'done' : i === now ? 'now' : '' }));
    const labels = ['Sent', 'Approval', 'Pickup', 'Working', 'Answered'];
    return h('div', { class: 'track ' + tone, role: 'group', 'aria-label': 'Progress: step ' + (now + 1) + ' of 5, ' + labels[now] + ': ' + title },
      steps,
      h('div', { class: 'track-text' },
        h('span', {}, h('b', { text: title }), ' · ' + (note || sub)),
        h('time', { 'data-elapsed': it.request.created_at, title: 'Time since sent' })));
  }

  function buildTurn(it) {
    const id = it.request.id, req = it.request, to = cap(req.to);
    const sent = new Date(req.created_at);
    const li = h('li', { class: 'turn', 'data-id': id });

    // Your message
    const youBubble = h('div', { class: 'bubble bubble-you' }, rich(req.body));
    li.append(h('div', { class: 'msg msg-you' }, h('div', { class: 'msg-body' },
      ...clampable(youBubble, id + ':q', req.body),
      h('div', { class: 'meta' },
        ACTIVE.has(it.status) ? null : h('span', { class: 'tools' },
          h('button', { type: 'button', class: 'tool', onclick: () => reuse(plain(req.body)) }, icon('redo'), 'Edit & resend')),
        h('time', { datetime: req.created_at, title: sent.toLocaleString(), text: time(sent) }),
        chip(it.status)))));

    // Progress
    const tr = track(it);
    if (tr) li.append(tr);

    // Approval gate
    if (it.status === 'held') {
      const blocked = (req.attachments && req.attachments.length) || req.then || req.follow_of;
      const btn = h('button', { type: 'button', class: 'btn btn-primary', 'data-review': id, onclick: () => openReview(id) }, icon('shield'), 'Review & approve');
      li.append(h('div', { class: 'gate' }, icon('shield'),
        h('strong', { text: 'Approve before ' + to + ' sees it' }),
        h('p', { text: 'Nothing is delivered until you review the exact text.' }),
        blocked ? h('p', { class: 'gate-note', text: 'This request has attachments or follow-on steps — review it with the relay’s admin tools.' }) : btn));
    }

    if (it.relay_note && it.relay_note.note) li.append(h('p', { class: 'sysline', text: it.relay_note.note }));

    // Clarifications
    for (const ex of it.exchanges || []) {
      li.append(agentMsg(req.to, ex.at, h('div', { class: 'bubble bubble-question' }, h('div', { class: 'q-label' }, icon('reply'), 'Question'), rich(ex.question)),
        ex.answer ? null : [h('button', { type: 'button', class: 'tool', onclick: () => followUp(it, ex.question) }, icon('reply'), 'Discuss in a new request')]));
      if (ex.answer) li.append(h('div', { class: 'msg msg-you' }, h('div', { class: 'msg-body' }, h('div', { class: 'bubble bubble-you' }, rich(ex.answer)))));
    }

    // Typing
    if (it.status === 'claimed' && !agentReply(it)) {
      li.append(h('div', { class: 'msg msg-agent' }, avatar(req.to, 'avatar-sm'), h('div', { class: 'msg-body' },
        h('div', { class: 'bubble bubble-agent typing', 'aria-label': to + ' is working' }, h('i'), h('i'), h('i')))));
    }

    // Reply
    const rep = agentReply(it);
    if (rep) {
      const failed = rep.status === 'failed' || it.status === 'failed';
      const bubble = h('div', { class: 'bubble bubble-agent' + (failed ? ' is-failed' : '') }, rich(rep.body));
      const copy = h('button', { type: 'button', class: 'tool', onclick: async () => {
        try { await navigator.clipboard.writeText(rep.body); copy.replaceChildren(icon('check'), 'Copied'); copy.classList.add('done'); setTimeout(() => { copy.replaceChildren(icon('copy'), 'Copy'); copy.classList.remove('done'); }, 1600); }
        catch { toast('Copy isn’t available here', 'x'); }
      } }, icon('copy'), 'Copy');
      li.append(agentMsg(rep.from || req.to, rep.created_at, clampable(bubble, id + ':a', rep.body),
        [h('button', { type: 'button', class: 'tool', onclick: () => followUp(it) }, icon('reply'), 'Follow up'), copy], failed ? chip('failed') : null));
    } else if (!ACTIVE.has(it.status)) {
      const msg = { declined: 'You declined this request — it was never delivered.', expired: 'This request expired before ' + to + ' replied.', cancelled: 'This request was cancelled.', failed: to + ' couldn’t complete this request.' }[it.status];
      const fromRelay = relayReply(it);
      if (msg || fromRelay) li.append(h('p', { class: 'sysline', text: fromRelay || msg }));
    }
    return li;
  }

  function agentMsg(name, at, bubbles, tools, extra) {
    const d = at ? new Date(at) : null;
    return h('div', { class: 'msg msg-agent' }, avatar(name, 'avatar-sm'), h('div', { class: 'msg-body' },
      h('div', { class: 'meta' }, h('span', { class: 'who-label', text: name }), d ? h('time', { datetime: at, title: d.toLocaleString(), text: time(d) }) : null, extra || null),
      ...[].concat(bubbles),
      tools ? h('div', { class: 'meta' }, h('span', { class: 'tools' }, ...tools)) : null));
  }

  /* ───────── Scrolling ───────── */
  function scrollBottom(instant) {
    const tl = $('timeline');
    requestAnimationFrame(() => { tl.scrollTo({ top: tl.scrollHeight, behavior: instant ? 'instant' : 'smooth' }); });
    $('jump').hidden = true;
  }
  function showJump(text) { $('jump').lastElementChild.textContent = text; $('jump').hidden = false; }
  $('jump').addEventListener('click', () => scrollBottom(false));
  $('timeline').addEventListener('scroll', () => {
    const tl = $('timeline');
    if (tl.scrollHeight - tl.scrollTop - tl.clientHeight < 60) $('jump').hidden = true;
  }, { passive: true });
  function focusTurn(it) {
    if (state.target !== it.request.to) { state.target = it.request.to; state.nodes.clear(); state.days.clear(); $('feed').replaceChildren(); state.loaded = false; render(); }
    const el = $('feed').querySelector('[data-id="' + CSS.escape(it.request.id) + '"]');
    if (!el) return;
    el.scrollIntoView({ block: 'center', behavior: 'smooth' });
    el.classList.remove('flash'); void el.offsetWidth; el.classList.add('flash');
  }

  /* ───────── Composer ───────── */
  const ta = $('request-body');
  const fieldSizing = typeof CSS !== 'undefined' && CSS.supports && CSS.supports('field-sizing', 'content');
  function autosize() {
    if (!fieldSizing) { ta.rows = 1; ta.rows = Math.min(10, Math.max(1, Math.ceil((ta.scrollHeight - 20) / 24))); }
    const n = byteLength(compose(ta.value.trim())); const c = $('count');
    c.hidden = n < 12000; c.textContent = n.toLocaleString() + ' / ' + MAX_BODY.toLocaleString() + ' bytes'; c.classList.toggle('warn', n > 15000);
    updateSend();
  }
  function updateSend() {
    const btn = $('send');
    btn.disabled = state.busy || !state.connected || !state.target || !targetInfo(state.target) || !ta.value.trim();
    btn.classList.toggle('busy', state.busy);
    const t = targetInfo(state.target), hint = $('target-hint');
    const offline = state.target && presenceKey(t) === 'offline';
    hint.classList.toggle('offline', !!offline);
    hint.lastElementChild.textContent = !state.target ? 'No agent available to message yet.'
      : !t ? cap(state.target) + ' isn’t connected to the relay right now.'
      : offline ? cap(state.target) + ' is offline. Your request can wait in the queue.'
      : 'Requests may wait for your approval before delivery.';
    ta.placeholder = state.target ? 'Message ' + cap(state.target) + '…' : 'Message…';
  }
  const DRAFT = 'tincan-draft';
  function saveDraft() { try { sessionStorage.setItem(DRAFT, ta.value); } catch { /* ignore */ } }
  function clearDraft() { try { sessionStorage.removeItem(DRAFT); } catch { /* ignore */ } }
  function restoreDraft() { try { const d = sessionStorage.getItem(DRAFT); if (d && !ta.value) ta.value = d; } catch { /* ignore */ } autosize(); }
  ta.addEventListener('input', () => { error('request-error', ''); autosize(); saveDraft(); });
  ta.addEventListener('keydown', e => {
    if (e.key === 'Enter' && (e.metaKey || e.ctrlKey)) { e.preventDefault(); $('compose').requestSubmit(); }
  });

  // Follow-ups carry a quoted excerpt of the earlier exchange. The review
  // sheet and timeline preserve that exact text; prefixes are not hidden metadata.
  const plain = body => body;
  function excerpt(text, n) { const t = text.replace(/\s+/g, ' ').trim(); return t.length > n ? t.slice(0, n - 1).trimEnd() + '…' : t; }
  function followUp(it, question) {
    setContext({ id: it.request.id, target: it.request.to, request: excerpt(it.request.body, 220), reply: question ? excerpt(question, 280) : it.reply ? excerpt(it.reply.body, 280) : '', question: !!question });
    ta.focus();
  }
  function setContext(ctx) {
    state.followUp = ctx;
    $('context').hidden = !ctx;
    $('context-quote').textContent = ctx ? ctx.reply || ctx.request : '';
    autosize();
  }
  $('clear-context').addEventListener('click', () => { setContext(null); ta.focus(); });
  function reuse(text) {
    setContext(null); ta.value = text; autosize(); saveDraft(); ta.focus(); ta.setSelectionRange(text.length, text.length);
    toast('Copied into the composer — edit, then send', 'redo');
  }
  function compose(text) {
    const c = state.followUp;
    if (!c || c.target !== state.target) return text;
    const quote = s => s.split('\n').map(l => '> ' + l).join('\n');
    let out = 'Follow-up to my earlier request:\n' + quote(c.request);
    if (c.reply) out += '\n\n' + (c.question ? 'You asked:' : 'Your reply began:') + '\n' + quote(c.reply);
    return out + '\n\n' + text;
  }

  $('compose').addEventListener('submit', async e => {
    e.preventDefault();
    if (state.busy || !state.csrf || !state.connected) return;
    const epoch = state.epoch;
    const draft = ta.value, context = state.followUp;
    const text = draft.trim(), target = state.target;
    if (!text || !target) return;
    const body = compose(text);
    if (byteLength(body) > MAX_BODY) { error('request-error', 'Message including follow-up context exceeds 16 KiB. Shorten it before sending.'); return; }
    // JSON escaping can exceed the existing 20 KiB request envelope even when the body fits.
    if (byteLength(JSON.stringify({ target, body, submission_key: '0'.repeat(36) })) > 20 * 1024) { error('request-error', 'Message encoding is too large. Shorten it before sending.'); return; }
    const same = state.submission && state.submission.body === body && state.submission.target === target;
    if (same && state.submission.uncertain) { error('request-error', 'This request may already be in your conversation. Refresh and check it before creating another.'); return; }
    if (!same) state.submission = { key: crypto.randomUUID(), body, target, uncertain: false };
    const submission = state.submission;
    state.busy = true; updateSend(); error('request-error', '');
    $('sending-slot').replaceChildren(h('div', { class: 'sending' }, h('div', { class: 'bubble bubble-you' }, rich(body)), h('div', { class: 'meta' }, h('span', { class: 'chip', text: 'Sending…' }))));
    scrollBottom(false);
    try {
      const sent = await api('requests', { target, body, submission_key: submission.key });
      if (epoch !== state.epoch) return;
      state.submission = null;
      if (ta.value === draft && state.followUp === context && state.target === target) {
        ta.value = ''; clearDraft(); setContext(null);
      }
      autosize();
      const msg = sent.status === 'held' ? 'Sent — waiting for your approval' : 'Sent to ' + cap(target);
      say(msg + '.'); toast(msg, sent.status === 'held' ? 'shield' : 'check');
      $('sending-slot').replaceChildren();
      await refresh();
      scrollBottom(false);
    } catch (err) {
      if (epoch !== state.epoch) return;
      $('sending-slot').replaceChildren();
      if (err.uncertain || !err.status || err.status === 409) submission.uncertain = true;
      error('request-error', err.message);
      await refresh();
    } finally {
      if (epoch === state.epoch) { state.busy = false; updateSend(); }
    }
  });

  /* ───────── Review ───────── */
  async function openReview(id) {
    if (state.reviewPending || $('review').open || !state.csrf) return;
    state.reviewPending = true;
    const epoch = state.epoch;
    error('request-error', '');
    const btns = document.querySelectorAll('[data-review="' + CSS.escape(id) + '"]');
    btns.forEach(b => { b.disabled = true; });
    try {
      const review = await api('requests/' + encodeURIComponent(id) + '/review', {});
      if (epoch !== state.epoch) return;
      state.review = { ...review, id };
      $('review-route').replaceChildren('You', ' → ', avatar(review.request.to), h('strong', { text: cap(review.request.to) }));
      $('review-body').textContent = review.request.body;
      error('review-error', '');
      $('approve').disabled = $('deny').disabled = false;
      $('review').showModal();
      $('review-body').scrollTop = 0;
    } catch (e) {
      if (epoch === state.epoch) { error('request-error', e.message); await refresh(); }
    } finally { if (epoch === state.epoch) { state.reviewPending = false; btns.forEach(b => { b.disabled = false; }); } }
  }
  $('close-review').addEventListener('click', () => { $('review').close(); state.review = null; });
  $('review').addEventListener('click', e => { if (e.target === $('review')) { $('review').close(); } });
  $('review').addEventListener('close', () => { state.review = null; });
  async function decide(decision) {
    if (!state.review) return;
    const review = state.review; const epoch = state.epoch;
    state.review = null;
    $('approve').disabled = $('deny').disabled = true; error('review-error', '');
    try {
      await api('requests/' + encodeURIComponent(review.id) + '/decision', { decision, review_token: review[decision + '_token'] });
      if (epoch !== state.epoch) return;
      $('review').close(); state.review = null;
      const msg = decision === 'approve' ? 'Approved — delivering to ' + cap(review.request.to) : 'Request denied';
      say(msg + '.'); toast(msg, decision === 'approve' ? 'check' : 'x');
      await refresh();
    } catch (e) {
      if (epoch !== state.epoch) return;
      state.review = null;
      error('review-error', e.message + ' Close this review and refresh before trying again.');
      await refresh();
    } finally { if (epoch === state.epoch) $('approve').disabled = $('deny').disabled = !state.review; }
  }
  $('approve').addEventListener('click', () => decide('approve'));
  $('deny').addEventListener('click', () => decide('deny'));

  /* ───────── Rail (mobile drawer) ───────── */
  function openRail() {
    if (!isMobile()) return;
    $('workspace').classList.add('rail-open'); $('scrim').hidden = false;
    $('rail').setAttribute('role', 'dialog'); $('rail').setAttribute('aria-modal', 'true');
    document.querySelector('.chat').setAttribute('inert', '');
    document.querySelector('.chat').setAttribute('aria-hidden', 'true');
    $('open-rail').setAttribute('aria-expanded', 'true'); $('close-rail').focus();
  }
  function closeRail(restoreFocus = true) {
    const wasOpen = $('workspace').classList.contains('rail-open');
    $('workspace').classList.remove('rail-open'); $('scrim').hidden = true;
    $('rail').removeAttribute('role'); $('rail').removeAttribute('aria-modal');
    document.querySelector('.chat').removeAttribute('inert');
    document.querySelector('.chat').removeAttribute('aria-hidden');
    $('open-rail').setAttribute('aria-expanded', 'false');
    if (wasOpen && restoreFocus && !$('workspace').hidden) $('open-rail').focus();
  }
  matchMedia('(max-width: 820px)').addEventListener('change', () => { if (!isMobile()) closeRail(false); });
  $('open-rail').addEventListener('click', openRail);
  $('close-rail').addEventListener('click', closeRail);
  $('scrim').addEventListener('click', closeRail);

  /* ───────── Global controls ───────── */
  $('refresh').addEventListener('click', () => refresh(true));
  $('sign-out').addEventListener('click', async () => {
    const epoch = state.epoch;
    try { await api('logout', {}); if (epoch !== state.epoch) return; signedOut(); toast('Signed out', 'out'); } catch (e) { if (epoch === state.epoch) notice(e.message); }
  });
  document.addEventListener('keydown', e => {
    if ($('workspace').classList.contains('rail-open')) {
      if (e.key === 'Escape') { e.preventDefault(); closeRail(); return; }
      if (e.key === 'Tab') {
        const focusable = [...$('rail').querySelectorAll('button:not(:disabled), a[href], [tabindex="0"]')].filter(el => el.getClientRects().length);
        const first = focusable[0], last = focusable[focusable.length - 1];
        if (first && e.shiftKey && document.activeElement === first) { e.preventDefault(); last.focus(); }
        else if (last && !e.shiftKey && document.activeElement === last) { e.preventDefault(); first.focus(); }
      }
    }
    const typing = /^(INPUT|TEXTAREA|SELECT)$/.test(document.activeElement && document.activeElement.tagName);
    if (e.key === '/' && !typing && !$('workspace').hidden && !$('review').open) { e.preventDefault(); ta.focus(); }
  });
  document.addEventListener('visibilitychange', () => { if (!document.hidden && state.csrf) refresh(); });
  setInterval(() => { if (state.csrf && !document.hidden) refreshTimes(); }, 1000);

  /* ───────── Sign in ───────── */
  $('peek').addEventListener('click', () => {
    const pw = $('password'), show = pw.type === 'password';
    pw.type = show ? 'text' : 'password';
    $('peek').setAttribute('aria-pressed', String(show));
    $('peek').setAttribute('aria-label', show ? 'Hide passphrase' : 'Show passphrase');
    pw.focus();
  });
  $('login-form').addEventListener('submit', async e => {
    e.preventDefault();
    const pw = $('password'); const epoch = state.epoch;
    if (!pw.value) { error('login-error', 'Enter your owner passphrase.'); pw.focus(); return; }
    $('sign-in').disabled = true; error('login-error', '');
    try {
      const auth = await api('login', { password: pw.value });
      if (epoch !== state.epoch) return;
      pw.value = ''; pw.type = 'password'; state.csrf = auth.csrf;
      await signedIn();
    } catch (err) {
      if (epoch !== state.epoch) return;
      error('login-error', err.message);
      const card = document.querySelector('.login-card'); card.classList.remove('shake'); void card.offsetWidth; card.classList.add('shake');
      pw.select();
    } finally { if (epoch === state.epoch) $('sign-in').disabled = false; }
  });

  signedIn().catch(() => signedOut());
}
