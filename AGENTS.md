# neo-server — AGENTS.md

Go/Gin 版兽剧聚合平台后端。**行为与 `../backend`（Express 5 + Mongoose 9）向下兼容**，前端零改动可切换。

- 独立 git 仓库，license AGPL-3.0-or-later（见 LICENSE.md）
- module `github.com/xiedada05/furry-drama-be-neo`，Go 1.26
- 官方 `go.mongodb.org/mongo-driver`（v1，无 ODM），手写 repository

## Commands

```bash
go build ./...                    # 编译全部
go build -o bin/server ./cmd/server
go vet ./...                      # 静态检查
go test ./...                     # 单元 + 集成测试（需 mongod 于 127.0.0.1:27017）
go run ./cmd/server --config=dev.ini --listen=tcp:0.0.0.0:5000
go run ./cmd/server --listen=unix:/run/furry-drama-be-neo.sock   # Unix socket
```

## 配置

ini 为主（默认 `/etc/furry-drama-be-neo.ini`），`--config=` 覆盖，**同名环境变量覆盖 ini**。
section：`[server] [database] [jwt] [email] [vapid] [security]`。
启动致命校验：缺 `JWT_SECRET`/`MONGO_URI` 或 secret < 32 字符 → 退出 1。

环境变量兼容旧 `.env`：`JWT_SECRET MONGO_URI ENCRYPTION_KEY ALTCHA_HMAC_KEY FRONTEND_URL SITE_URL ALLOWED_ORIGINS EMAIL_* VAPID_* DEMO_EMAILS DEV_API_TOKEN PORT NODE_ENV SKIP_RATE_LIMIT`。

## 目录结构

```
cmd/server/           入口（--config/--listen、TCP/Unix 监听、优雅关停）
internal/
  config/             ini+env 加载与校验
  server/             Gin 装配（中间件管线顺序见 app.go 注释）
  middleware/         CORS/CSRF/security/ratelimit/apitracker/requestlogger/bodyparse/...
  router/             双版本挂载（MountDual）+ 路由注册表
  handler/            薄 HTTP 层（auth/{session,device,password,email,account}、usersession、twofactor、users、csrf、health）
  service/            领域逻辑（登录分支流、refresh 轮换、设备码链、导出）
  repository/         mongo repo（Repos 聚合注入）
  model/              struct 映射（bson 标签对齐 Mongoose schema）
  auth/               jwt/cookies/bcrypt/totp/fieldcrypto/lockout
  security/           xss 过滤 Go 移植
  altcha/             Altcha v2 PoW
  ratelimit/          限流（Store interface，内存滑动窗口）
  code/               验证码内存存储（CodeStore interface）
  email/              SMTP 客户端 + 模板
  ipregion/           ipapi.co 外呼 + LRU
  upload/             multer 等价（魔数校验）
  pagination/ errors/ logging/ audit/ migrate/ indexes/ cron/
docs/                 swaggo 产物 + 行为基线
deploy/               systemd unit + ini 模板 + run.sh
scripts/differential/ 差分测试（Node）
```

## 接口契约（固定，勿改签名——并行 subagent 依赖）

- `config.Load(path string) (*config.Config, error)`；字段 `Cfg.IsDev`、`Cfg.Server.AllowOrigins`、`Cfg.Security.LoginMaxAttempts/LoginLockMinutes/CSRFMaxAgeHours`、`Cfg.JWT.Secret/EncryptionKey/AltchaHMACKey/DevAPIToken/DemoEmails/AccessTTL/RefreshTTL`
- `repository.Connect(ctx, uri, name string, pool int) (*mongo.Database, error)`
- `repository.NewRepos(db, loginMaxAttempts, loginLockMinutes int) *repository.Repos`
- `auth.NewSigner(secret string) *auth.Signer`；`(*Signer).Sign/Verify`
- `auth.HashToken(token) string`（sha256 hex）
- `auth.SetAuthCookies(c, access, refresh string, isProd bool)`、`auth.ClearAuthCookies(c, isProd bool)`、`auth.SetCSRFCookie(c, isProd bool, maxAgeHours int) string`、`auth.GetAccessToken(c)`、`auth.GetRefreshToken(c)`
- `errors.New/NewKey/Wrap(status, message[, key, cause])`、`errors.AbortWithAppError(c, err, isDev)`
- `pagination.Parse(c) (Query)`、`(*Query).Skip()/TotalPages()`
- `middleware.CORS(allowOrigins []string)`、`middleware.CSRF()`
- `router.MountDual(r *gin.Engine, mountPath string, register func(*gin.RouterGroup))`
- `server.NewApp(server.Deps{Config, DB, Repos, Signer}) *gin.Engine`
- `handler.CSRF(cfg)`、`handler.Health(db)`

## 行为一致性铁律（差分测试为准）

1. **双 token**：access 15m / refresh 7d，HS256；`refreshToken` cookie path=`/api/auth`；419=access 过期（`auth.accessTokenExpired`）
2. **refresh 轮换 + 重用**：原子 findOneAndUpdate 取用作废；未抢到且 logoutAt 在 30s 并发宽限内 → **409**（`auth.concurrentRefresh`，不清 cookie 不吊销）；否则吊销全部 → 401（`auth.refreshTokenReuse`）
3. **CSRF 三态** 403（对齐 src/index.js:267-283）
4. **登录分支顺序**：altcha→锁定→删除宽限→密码→邮箱(403 needVerification)→设备(403 needDeviceVerify)→2FA(200 need2FA)→成功；状态码不能乱
5. **AES-256-CBC**：key=`sha256(ENCRYPTION_KEY 或 JWT_SECRET)`（二选一非拼接），格式 `enc:iv:ct`，兼容旧数据
6. **限流挂载不对称**：per-endpoint 只作用于 `/api/<path>`（不含 `/api/v1/`）；global 作用于全部 `/api/*`
7. **`_id` 输出 hex 字符串**；`backgroundPrefs` 默认 `{image:'',enabled:false,opacity:30,blur:0}`
8. 差分测试（scripts/differential）是行为 ground truth；现 Express 进程于本机 :5000 常驻（oracle）

## 里程碑

- M0 种子（骨架/config/repos/model/认证原语/健康检查/CSRF/双版本）— 完成
- M1 基础原语（xss/中间件/altcha/email+ipregion+code/upload/repo 补全/migrate/indexes/cron）— 并行
- M2 认证+用户域（service + handler）
- M3 集成测试 + 差分闸门
- M4 部署产物 + swaggo 注解 + godoc + README
