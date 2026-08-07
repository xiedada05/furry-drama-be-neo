// 差分测试归一化：把两端响应中"合法变化的字段"统一成占位符，使行为差异可见。
// 规则以 Express 行为基线（docs/behavior-baseline.md）为准。
'use strict';

// 归一化一个 JSON 值（深拷贝后处理）。
function normalizeValue(value, depth = 0) {
  if (value === null || value === undefined) return value;
  if (typeof value === 'string') {
    return normalizeString(value);
  }
  if (typeof value === 'number') return value;
  if (Array.isArray(value)) return value.map((v) => normalizeValue(v, depth + 1));
  if (typeof value === 'object') {
    const out = {};
    for (const k of Object.keys(value).sort()) {
      out[k] = normalizeValue(value[k], depth + 1);
    }
    return out;
  }
  return value;
}

// 归一化单个字符串：识别 24hex 的 ObjectId、JWT、时间戳、UUID、日期等。
function normalizeString(s) {
  if (!s) return s;
  // 24 位 hex ObjectId（_id / userId / episodeId 值）
  if (/^[0-9a-f]{24}$/.test(s)) return '<id>';
  // JWT（三段 base64url）
  if (/^[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+$/.test(s) && s.length > 40) return '<jwt>';
  // ISO 时间戳
  if (/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d+)?Z?$/.test(s)) return '<ts>';
  // 32/64 hex token（csrfToken、salt、nonce、signature、tokenHash）
  if (/^[0-9a-f]{32}$/.test(s) || /^[0-9a-f]{64}$/.test(s)) return '<hex>';
  return s;
}

// 归一化 Set-Cookie 响应头数组：只保留 名称 + 非动态属性（Path/HttpOnly/SameSite），
// 丢弃值、Max-Age、Expires（有效期实现差异合法）。
function normalizeCookies(setCookieHeaders) {
  if (!setCookieHeaders || !setCookieHeaders.length) return [];
  return setCookieHeaders.map((sc) => {
    const parts = sc.split(';').map((p) => p.trim());
    const name = parts[0].split('=')[0];
    const attrs = [];
    for (const p of parts.slice(1)) {
      if (/^path=/i.test(p)) attrs.push(p.toLowerCase());
      if (/^httponly$/i.test(p)) attrs.push('httponly');
      if (/^samesite=/i.test(p)) attrs.push(p.toLowerCase());
    }
    return [name, ...attrs.sort()].join('; ');
  }).sort();
}

// 归一化响应头：丢弃 RateLimit-*（限流计数合法变化）、Date、Keep-Alive 等。
const DROP_HEADERS = new Set([
  'date', 'connection', 'keep-alive', 'transfer-encoding', 'content-length',
  'ratelimit-limit', 'ratelimit-remaining', 'ratelimit-reset', 'ratelimit-policy',
  'x-powered-by', 'etag', 'last-modified', 'vary',
]);

function normalizeHeaders(headers) {
  const out = {};
  for (const [k, v] of Object.entries(headers)) {
    const lk = k.toLowerCase();
    if (DROP_HEADERS.has(lk)) continue;
    if (lk === 'set-cookie') { out[lk] = normalizeCookies(v); continue; }
    out[k] = v;
  }
  return out;
}

module.exports = { normalizeValue, normalizeCookies, normalizeHeaders };
