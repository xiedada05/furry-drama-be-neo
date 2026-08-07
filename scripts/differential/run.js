// 差分测试驱动器：对同一组场景请求，分别打到旧 Express 与 neo-server，比对
// 状态码 + 归一化 JSON + Set-Cookie 属性 + Content-Disposition。
//
// 用法：
//   OLD_BASE=http://127.0.0.1:5000 NEO_BASE=http://127.0.0.1:5001 node scripts/differential/run.js
//
// 场景文件：scripts/differential/scenarios.json（见该文件顶部的格式说明）。
'use strict';

const fs = require('fs');
const path = require('path');
const { normalizeValue, normalizeCookies, normalizeHeaders } = require('./normalize');

const OLD_BASE = process.env.OLD_BASE || 'http://127.0.0.1:5000';
const NEO_BASE = process.env.NEO_BASE || 'http://127.0.0.1:5001';
const DEV_TOKEN = process.env.DEV_TOKEN || 'test-dev-token';
const SCENARIOS_FILE = path.join(__dirname, 'scenarios.json');

let passCount = 0;
let failCount = 0;
const failures = [];

// ---------- cookie jar ----------
class CookieJar {
  constructor() { this.cookies = new Map(); } // name -> {value, path}
  apply(headers) {
    const list = [...this.cookies.entries()].map(([n, c]) => `${n}=${c.value}`);
    if (list.length) headers['Cookie'] = list.join('; ');
  }
  absorb(setCookieHeaders) {
    for (const sc of setCookieHeaders || []) {
      const [nameVal, ...attrs] = sc.split(';').map((p) => p.trim());
      const eq = nameVal.indexOf('=');
      if (eq < 0) continue;
      const name = nameVal.slice(0, eq);
      const value = nameVal.slice(eq + 1);
      // 过期/删除：Max-Age=0 或值为空表示删除
      const maxAge0 = attrs.some((a) => /^max-age=0$/i.test(a));
      if (maxAge0 || value === '') { this.cookies.delete(name); continue; }
      let p = '/';
      for (const a of attrs) { if (/^path=/i.test(a)) p = a.slice(5); }
      this.cookies.set(name, { value, path: p });
    }
  }
  csrfToken() {
    const c = this.cookies.get('XSRF-TOKEN');
    return c ? c.value : '';
  }
}

// ---------- request ----------
async function send(base, step, jar, isFirst) {
  const method = (step.method || 'GET').toUpperCase();
  const headers = { ...(step.headers || {}) };
  headers['User-Agent'] = step.ua || 'Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 Chrome/126 Safari/537.36';
  if (step.devToken !== false) headers['x-dev-token'] = DEV_TOKEN;
  // CSRF：非 GET 且 jar 有 token 则带 X-XSRF-TOKEN
  if (method !== 'GET' && jar.csrfToken()) headers['X-XSRF-TOKEN'] = jar.csrfToken();
  jar.apply(headers);

  const opts = { method, headers, redirect: 'manual' };
  if (step.body !== undefined) {
    if (step.form) {
      const fd = new URLSearchParams();
      for (const [k, v] of Object.entries(step.body)) fd.append(k, v);
      headers['Content-Type'] = 'application/x-www-form-urlencoded';
      opts.body = fd.toString();
    } else {
      headers['Content-Type'] = 'application/json';
      opts.body = JSON.stringify(step.body);
    }
  }
  // 首次请求前：同一 jar 打一次 csrf-token（若无）
  if (isFirst && method !== 'GET' && !jar.csrfToken()) {
    await send(base, { method: 'GET', path: '/api/csrf-token' }, jar, false);
  }
  const res = await fetch(base + step.path, opts);
  const rawBody = await res.text();
  let body;
  try { body = JSON.parse(rawBody); } catch { body = rawBody; }
  const setCookie = res.headers.getSetCookie ? res.headers.getSetCookie() : [];
  // 每次响应后更新 cookie jar（含 isFirst 的 csrf-token 预取），保证后续步骤携带 cookie。
  jar.absorb(setCookie);
  return {
    status: res.status,
    body,
    rawBody,
    headers: res.headers,
    setCookie,
  };
}

// ---------- compare ----------
function compareStep(name, a, b) {
  const issues = [];
  if (a.status !== b.status) {
    issues.push(`status: Express=${a.status} neo=${b.status}`);
  } else if (a.status >= 200 && a.status < 300 || a.status === 400 || a.status === 401 || a.status === 403 || a.status === 409 || a.status === 419 || a.status === 422 || a.status === 429) {
    // 状态码一致时，对"应为 JSON"的状态严格比对归一化 JSON（捕获文案/字段差异）；
    // 404/5xx 的 HTML/纯文本 404 页差异视为合法，忽略。
    if (typeof a.body === 'object' && a.body !== null && typeof b.body === 'object' && b.body !== null) {
      const na = JSON.stringify(normalizeValue(a.body));
      const nb = JSON.stringify(normalizeValue(b.body));
      if (na !== nb) issues.push(`body:\n  Express: ${na}\n  neo    : ${nb}`);
    } else if (a.rawBody !== b.rawBody) {
      issues.push(`body-text: Express=${JSON.stringify(a.rawBody).slice(0, 200)} neo=${JSON.stringify(b.rawBody).slice(0, 200)}`);
    }
  }
  // cookie 属性对比
  const ca = JSON.stringify(normalizeCookies(a.setCookie));
  const cb = JSON.stringify(normalizeCookies(b.setCookie));
  if (ca !== cb) issues.push(`set-cookie(keys):\n  Express: ${ca}\n  neo    : ${cb}`);
  // 关键头对比（Content-Disposition / Content-Type / Deprecation / Sunset）
  // 404 的 content-type 差异（Express text/html vs gin text/plain）为合法实现差异。
  const isBoth404 = a.status === 404 && b.status === 404;
  for (const h of ['content-disposition', 'content-type', 'deprecation', 'sunset']) {
    if (isBoth404 && h === 'content-type') continue;
    const va = a.headers.get(h) || '';
    const vb = b.headers.get(h) || '';
    if (h === 'content-type' && (va.includes('json') && vb.includes('json'))) continue;
    if (va !== vb) issues.push(`header ${h}: Express=${JSON.stringify(va)} neo=${JSON.stringify(vb)}`);
  }
  return issues;
}

// ---------- main ----------
async function run() {
  const data = JSON.parse(fs.readFileSync(SCENARIOS_FILE, 'utf8'));
  const scenarios = data.scenarios;
  for (const sc of scenarios) {
    const jarA = new CookieJar();
    const jarB = new CookieJar();
    let ok = true;
    const detail = [];
    for (let i = 0; i < sc.steps.length; i++) {
      const step = sc.steps[i];
      let a, b;
      try { a = await send(OLD_BASE, step, jarA, i === 0); } catch (e) { a = { status: -1, body: null, rawBody: 'ERR ' + e.message, setCookie: [], headers: new Headers() }; }
      try { b = await send(NEO_BASE, step, jarB, i === 0); } catch (e) { b = { status: -1, body: null, rawBody: 'ERR ' + e.message, setCookie: [], headers: new Headers() }; }
      jarA.absorb(a.setCookie);
      jarB.absorb(b.setCookie);
      const issues = compareStep(sc.name + ' step' + i, a, b);
      if (issues.length) { ok = false; detail.push(`  step ${i} ${step.method} ${step.path}:\n    ` + issues.join('\n    ')); }
    }
    if (ok) { passCount++; console.log(`✔ PASS ${sc.name}`); }
    else { failCount++; console.log(`✘ FAIL ${sc.name}`); failures.push(sc.name); console.log(detail.join('\n')); }
  }
  console.log(`\n========== 结果: ${passCount} PASS / ${failCount} FAIL ==========`);
  if (failCount) { console.log('失败场景: ' + failures.join(', ')); process.exitCode = 1; }
}

run().catch((e) => { console.error(e); process.exitCode = 1; });
