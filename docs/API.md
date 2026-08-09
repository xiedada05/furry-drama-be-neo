# API 参考（手写可读版）

本文件是**第一段（认证+用户域）全部端点的纯文本参考**，与 `/api/docs`（swagger，机器可查）互补：这里可直接 grep、复制、离线读。所有端点同时存在于 `/api/X` 与 `/api/v1/X`（v1 带 `Deprecation: true` 与 `Sunset` 头），下表只列 `/api` 前缀。

## 认证与前置

- **CSRF**：所有非 GET 请求需带 `X-XSRF-TOKEN` header，且与 `XSRF-TOKEN` cookie 值相等（先 `GET /api/csrf-token` 获取两者）。
- **鉴权**：登录后 access token 在 httpOnly cookie `accessToken` 中自动携带；也可用 `Authorization: Bearer <token>`。
- **dev-token 绕过**（仅开发）：header `x-dev-token` 等于 `DEV_API_TOKEN` 时绕过 altcha/邮箱/设备验证。

## Cookie

| 名称 | httpOnly | Path | 有效期 | SameSite | 说明 |
|---|---|---|---|---|---|
| `accessToken` | 是 | `/` | 15min | lax | access JWT |
| `refreshToken` | 是 | `/api/auth` | 7d | lax | refresh JWT（轮换） |
| `token` | 是 | `/` | 30d | lax | 兼容旧客户端 |
| `XSRF-TOKEN` | 否 | `/` | 24h | strict | CSRF 双拷贝 |

## 认证端点 `/api/auth`

| 方法 | 路径 | 鉴权 | 限流 | 请求要点 | 响应要点 |
|---|---|---|---|---|---|
| GET | `/auth/captcha` | 公开 | captcha 60/min | — | 200 `{parameters:{algorithm,cost,expiresAt,keyLength,keyPrefix,nonce,salt},signature}`（altcha 挑战） |
| GET | `/auth/check-accountId?accountId=` | 公开 | 20/min | query accountId | 200 `{available:bool}` |
| POST | `/auth/register` | 公开 | 3/h | `{accountId,username,email,password,deviceInfo?,altcha}`；accountId 3-20 `[a-zA-Z0-9_]`，密码≥8含字母数字 | 200 `{message,email,needVerification:true}`；400 各类校验文案 |
| POST | `/auth/login` | 公开 | 5/15min | `{email,password,deviceInfo?,altcha}` | 200 用户对象 或 `{need2FA,email,twoFactorChallenge}`；403 `{needVerification}` / `{needDeviceVerify,deviceInfo}` |
| POST | `/auth/logout` | 登录 | — | — | 200 `{message:"退出成功"}` |
| POST | `/auth/refresh` | refresh cookie | 5/15min | 需 `refreshToken` cookie | 200 用户对象（轮换新 cookie）；401/409 |
| GET | `/auth/me` | 登录 | — | — | 200 `{_id,accountId,username,email,isEmailVerified,role,avatar,emailNotificationPrefs,backgroundPrefs,personalWallpapers}` |
| GET | `/auth/sse-ticket` | 登录 | — | — | 200 `{ticket}`（30s JWT，SSE 用） |
| POST | `/auth/verify-device` | 公开 | 5/15min | `{token}`（device-verify JWT） | 200 `{verified:true,loginCode}`；400 各类 |
| POST | `/auth/confirm-device-login` | 公开 | 5/15min | `{loginCode}`（6 位） | 200 用户对象 或 `{need2FA,email,twoFactorChallenge}`；400 |
| POST | `/auth/login-2fa` | 公开 | 5/15min | `{email,twoFactorToken,twoFactorChallenge,deviceInfo?}` | 200 用户对象；400/403/423 |
| PUT | `/auth/change-password` | 登录 | 3/h | `{currentPassword,newPassword}` | 200 `{message:"密码修改成功"}`（吊销全部会话+清cookie） |
| POST | `/auth/forgot-password` | 公开 | 3/h | `{email,altcha}` | 200 统一文案（不泄露账号存在性） |
| POST | `/auth/reset-password` | 公开 | 3/h | `{token,newPassword}` | 200 `{message:"密码重置成功"}`；token 一次性（UsedToken） |
| PUT | `/auth/change-email` | 超管 | 3/h | `{newEmail,password}` | 200 `{message,email,isEmailVerified:false,forceEmailChange:false}` |
| PUT | `/auth/email-notification-prefs` | 登录 | — | 7 布尔键（episodeUpdate/newDeviceLogin/feedbackReply/friendLinkStatus/friendLinkApply/announcement/reviewResult） | 200 `{message,emailNotificationPrefs}` |
| POST | `/auth/verify-email` | 公开 | 5/h | `{code,email?}`（6 位验证码） | 200 `{message:"邮箱验证成功"}`；码一次性、5 次尝试上限 |
| POST | `/auth/resend-verification` | 登录 | 5/h | — | 200 `{message}`（未配置 SMTP 时提示） |
| POST | `/auth/resend-verification-by-email` | 公开 | 5/h | `{email,altcha}` | 200 统一文案 |
| POST | `/auth/request-email-change` | 登录 | 3/h | `{password,newEmail,altcha}` | 200 `{message:"验证邮件已发送到新邮箱"}`；锁定感知（423） |
| POST | `/auth/verify-email-change` | 公开 | — | `{token}`（email-change JWT） | 200 `{message:"邮箱修改成功，请重新登录",email}`；一次性 |
| POST | `/auth/request-deletion` | 登录 | — | — | 200 `{message,deletionRequestedAt,deleteAt}`（7 天宽限） |
| POST | `/auth/cancel-deletion` | 登录 | — | — | 200 `{message:"注销申请已取消"}` |
| GET | `/auth/deletion-status` | 登录 | — | — | 200 `{requested:false}` 或 `{requested:true,deletionRequestedAt,deleteAt}` |

## 用户会话 `/api/user-sessions`

| 方法 | 路径 | 鉴权 | 请求要点 | 响应要点 |
|---|---|---|---|---|
| POST | `/user-sessions/create` | 登录 | `{screenWidth,screenHeight,language}` | 200 `{sessionId}`（按 access token hash upsert） |
| GET | `/user-sessions/my` | 登录 | — | 200 会话数组（含 `isCurrent`） |
| PUT | `/user-sessions/:id/name` | 登录 | `{deviceName}`（≤50，本人） | 200 `{message}` |
| DELETE | `/user-sessions/:id` | 登录 | 不能下线当前设备 | 200 `{message}` |
| DELETE | `/user-sessions/my/all` | 登录 | — | 200 `{message:"已下线其他所有设备"}` |
| POST | `/user-sessions/heartbeat` | 登录 | — | 200 `{ok:true}` |
| GET | `/user-sessions/all` | 超管 | limit 200 | 200 会话数组（含 username/userRole） |
| DELETE | `/user-sessions/admin/:id` | 超管 | — | 200 |
| DELETE | `/user-sessions/admin/user/:userId/all` | 超管 | — | 200 |

## 2FA `/api/2fa`（全部走 twoFactor 限流 5/15min）

| 方法 | 路径 | 鉴权 | 请求要点 | 响应要点 |
|---|---|---|---|---|
| POST | `/2fa/enable` | 登录 | — | 200 `{secret,backupCodes,otpauthUrl}`（secret 为 base64，AES 加密存储） |
| POST | `/2fa/verify-enable` | 登录 | `{token}`（TOTP） | 200 `{message:"2FA enabled successfully"}` |
| POST | `/2fa/disable` | 登录 | `{token}`（TOTP 或备份码） | 200 `{message:"2FA disabled successfully"}` |
| POST | `/2fa/verify` | 登录 | `{token}` | 200 `{verified:true}` |

## 用户 `/api/users`

| 方法 | 路径 | 鉴权 | 请求要点 | 响应要点 |
|---|---|---|---|---|
| POST | `/users/avatar` | 登录 | multipart `avatar`（≤2MB，图片） | 200 `{url:"/uploads/..."}` |
| POST | `/users/background-upload` | 登录 | multipart `image`（≤5MB） | 200 `{url}` |
| PUT | `/users/background-prefs` | 登录 | `{enabled?,opacity(0-100),blur(0-20),image?}` | 200 `{backgroundPrefs}` |
| PUT | `/users/profile` | 登录 | `{username}`（≤20） | 200 用户对象 |
| GET | `/users/export-my-data?format=json\|csv` | 登录 | 3/h 限流 | 下载文件（json 或 csv 含 BOM） |

## 错误码（messageKey → 前端 i18n）

| messageKey | HTTP | 触发 | 前端行为 |
|---|---|---|---|
| `auth.noToken` | 401 | 无 access token | 跳登录 |
| `auth.invalidToken` | 401 | token 非法 / refresh 等 purpose 误用 | 跳登录 |
| `auth.userNotFound` | 401 | 用户已删除 | 跳登录 |
| `auth.forbidden` | 403 | 角色不符（非 admin/superadmin 访问管理端点） | 提示无权限 |
| `auth.accessTokenExpired` | **419** | access 过期 | **自动调 /api/auth/refresh** |
| `auth.noRefreshToken` | 401 | refresh cookie 缺失 | 跳登录 |
| `auth.refreshTokenExpired` | 401 | refresh 过期 | 跳登录 |
| `auth.concurrentRefresh` | 409 | 并发刷新宽限（30s 内旧 token） | 静默重试 |
| `auth.refreshTokenReuse` | 401 | 重用检测（吊销全部会话） | 跳登录 |

其他错误：限流 429 `{message}`；登录分支 403（`needVerification`/`needDeviceVerify`，**不带 messageKey**，前端按字段分支）；普通校验错误 400 `{message}`（中文文案）；服务端错误 500 `{message:"服务器内部错误"}`（生产隐藏详情）。

## 数据结构速查（核心 collection）

**users**：`accountId`(唯一) `username` `email`(唯一) `password`(bcrypt,不输出) `avatar` `deviceInfo{...}` `lastLoginAt/Ip/Region` `deletionRequestedAt` `isEmailVerified` `role[user|creator|admin|superadmin]` `passwordChangedAt` `twoFactorEnabled` `twoFactorSecret/BackupCodes`(AES加密) `emailNotificationPrefs{7布尔}` `backgroundPrefs{image,enabled,opacity,blur}` `personalWallpapers[]` `loginAttempts/lockUntil`(锁定) `createdAt`

**usersessions**：`userId` `tokenHash`(旧access) `refreshTokenHash`(轮换) `deviceInfo` `ip` `isActive` `loginAt` `lastActiveAt` `logoutAt`

**usedtokens**（一次性防重放）：`tokenHash`(唯一) `purpose[reset-password|email-change|device-verify]` `expiresAt`(TTL)

**auditlogs**：`userId/userName` `action` `target` `details` `ip` `userAgent` `createdAt`

**notifications**：`userId` `episodeId?` `type` `message` `isRead` `createdAt`(TTL 90d)

（完整字段见 `internal/model/` 的 struct 与 Mongoose schema `../backend/models/`）
