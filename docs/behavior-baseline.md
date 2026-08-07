# 行为基线（Express 实际采样）

来源：本机启动的旧 Express 实例（:5000，`backend/` 工作区，DEV_API_TOKEN=test-dev-token，NODE_ENV=development）。
日期：2026-08-08。差分测试以本文件 + Express 进程为 ground truth。

## Swagger（残缺，不能单独当契约）
- `GET /api/docs.json` 仅 2625 字节，paths 只有 `/api/series`、`/api/series/{id}`；components 含 securitySchemes/schemas
- 原因：swagger-jsdoc 只扫 `./routes/*.js`（不扫 `routes/auth/`），全仓库仅 11 个 `@swagger` 注解

## 全局
- `GET /api/health` → 200 `{"status":"ok","timestamp":"<ISO UTC>","db":"connected"}`
- `GET /api/csrf-token` → 200 `{"csrfToken":"<64hex>"}`；Set-Cookie `XSRF-TOKEN=<64hex>; Max-Age=86400; Path=/; SameSite=Strict`（**无 HttpOnly**）

## Captcha (altcha v2)
- `GET /api/auth/captcha` → `{"parameters":{"algorithm":"SHA-256","cost":10000,"expiresAt":<秒>,"keyLength":32,"keyPrefix":"00","nonce":"<32hex>","salt":"<32hex>"},"signature":"<64hex>"}`
- `GET /api/auth/check-accountId?accountId=` → `{"available":true}`

## 认证
- **register**（普通邮箱）→ 200 `{"message":"注册成功，验证码已发送至您的邮箱","email":"...","needVerification":true}`（无 Set-Cookie）
- **register**（DEMO_EMAILS=demo@furry09.com 且不存在）→ 400 `{"message":"该邮箱不可注册"}`
- **login 成功** → 200 用户对象 + 双 cookie：
  ```
  Set-Cookie: accessToken=<jwt>; Max-Age=900; Path=/; HttpOnly; SameSite=Lax
  Set-Cookie: refreshToken=<jwt>; Max-Age=604800; Path=/api/auth; HttpOnly; SameSite=Lax
  用户对象: {"_id":"<hex>","accountId":..,"username":..,"email":..,"isEmailVerified":false,"role":"user","forceEmailChange":false,
            "backgroundPrefs":{"image":"","enabled":false,"opacity":30,"blur":0},"personalWallpapers":[]}
  ```
- **login**（无 altcha）→ 400 `{"message":"验证码错误或已过期"}`
- **me**（带 cookie）→ 200 `{"_id":..,"accountId":..,"username":..,"email":..,"isEmailVerified":..,"role":..,"avatar":"","emailNotificationPrefs":{"episodeUpdate":true,"newDeviceLogin":true,"feedbackReply":true,"friendLinkStatus":true,"friendLinkApply":true,"announcement":true,"reviewResult":true},"backgroundPrefs":{..},"personalWallpapers":[]}`
- **me**（无 token）→ 401 `{"message":"Not authorized, no token","messageKey":"auth.noToken"}`
- **refresh**（轮换）→ 200 新 access/refresh cookie + 用户对象
- **refresh**（被限流）→ 429 `{"message":"操作过于频繁，请15分钟后再试"}`（twoFactorLimiter 5/15min 覆盖 refresh）
- **logout** → 200 `{"message":"退出成功"}`

## JWT claims（HS256）
- access：`{"id":"<hex>","purpose":"access","iat":<sec>,"exp":<sec>}`（15m）
- refresh：`{"id":"<hex>","purpose":"refresh","jti":"<32hex>","iat":<sec>,"exp":<sec>}`（7d）

## 代码确认的关键行为（未逐一 curl，来自源码）
- login 分支顺序：altcha→用户存在→isLocked→删除宽限→密码→未验证邮箱(403 needVerification)→自动 verified(skipVerification)→新设备(403 needDeviceVerify)→2FA(need2FA 200)→成功
- 403 邮箱：`{"message":"请先验证邮箱后再登录，验证码已发送至您的邮箱","needVerification":true,"email":..}`
- 403 设备：`{"message":"检测到新设备登录，验证码已发送至您的邮箱","needDeviceVerify":true,"email":..,"deviceInfo":{browser,browserVersion,os,osVersion,deviceType,ip}}`
- 2FA：200 `{"need2FA":true,"email":..,"twoFactorChallenge":"<jwt 5m purpose=2fa-challenge>"}`
- refresh 重用检测：原子 findOneAndUpdate；未抢到且 logoutAt 在 30s 并发宽限内 → **409** `{"message":"Concurrent refresh","messageKey":"auth.concurrentRefresh"}`（不吊销不清 cookie）；否则吊销全部 → 401 `{"message":"Refresh token reuse detected","messageKey":"auth.refreshTokenReuse"}`
- 419：`{"message":"Access token expired","messageKey":"auth.accessTokenExpired"}`
- requireEmailChanged：403 `{"message":"请先修改管理员邮箱后再进行操作","forceEmailChange":true}`
- CSRF 三态：`CSRF token mismatch` / `CSRF protection: missing X-XSRF-TOKEN header` / `CSRF protection: missing XSRF-TOKEN cookie, please refresh the page`
- register 校验文案：邮箱格式不正确 / 账号ID长度需在3-20个字符之间 / 账号ID只能包含字母、数字和下划线 / 昵称长度需在1-20个字符之间 / 该邮箱已被注册 / 该账号ID已被占用 / 该信息已被使用
- device/password/email/account/2FA/userSessions/users 端点文案与流程见 AGENTS.md 行为基线与 Express 源码（`backend/routes/`）
