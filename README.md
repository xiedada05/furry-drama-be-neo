# neo-server

兽剧聚合平台后端（Go/Gin 重构版）。**行为与 `../backend`（Express 5 + Mongoose 9）向下兼容**，前端零改动即可切换。

- Go 1.26，Gin，官方 `go.mongodb.org/mongo-driver`（v1，无 ODM）
- 独立 git 仓库，license AGPL-3.0-or-later
- module `github.com/xiedada05/furry-drama-be-neo`

## 文档导航（维护者必读）

| 文档 | 内容 |
|---|---|
| [docs/architecture.md](docs/architecture.md) | 分层架构 + 依赖注入链 + 请求生命周期（从零理解） |
| [docs/decisions.md](docs/decisions.md) | 架构决策记录 ADR（每个取舍的 Why + 替代方案） |
| [docs/contributing.md](docs/contributing.md) | 扩展标准模式 + 运行调试 + 代码规范（人工维护指南） |
| [docs/API.md](docs/API.md) | 手写 API 参考（全部端点 + 错误码 + cookie + 数据结构） |
| [/api/docs](http://localhost:5000/api/docs) | Swagger UI（运行时，覆盖全部端点，非生产） |
| [docs/behavior-baseline.md](docs/behavior-baseline.md) | Express 行为采样（差分 ground truth） |
| [AGENTS.md](AGENTS.md) | 接口契约 + 行为铁律（并行开发锚点） |
| [deploy/PRODUCTION_SWITCH.md](deploy/PRODUCTION_SWITCH.md) | 生产切换手册（Express→neo 配置映射/部署/验收/回滚） |

## 快速开始

```bash
go build -o bin/furry-drama-be-neo ./cmd/server
# 开发（env 覆盖）
JWT_SECRET=... MONGO_URI=mongodb://127.0.0.1:27017/furry_drama_tracker NODE_ENV=development \
  ./bin/furry-drama-be-neo --listen=tcp:0.0.0.0:5000
# 或生产（ini 配置）
./bin/furry-drama-be-neo --config=/etc/furry-drama-be-neo.ini
# Unix socket
./bin/furry-drama-be-neo --config=/etc/furry-drama-be-neo.ini --listen=unix:/run/furry-drama-be-neo.sock
```

## 配置

默认读取 `/etc/furry-drama-be-neo.ini`，`--config=` 覆盖，**同名环境变量优先**。section：`[server] [database] [jwt] [email] [vapid] [security]`。模板见 `deploy/furry-drama-be-neo.ini`。

启动致命校验：缺 `JWT_SECRET`（或 < 32 字符）/ `MONGO_URI` → 退出 1。

环境变量兼容旧 `.env`：`JWT_SECRET MONGO_URI ENCRYPTION_KEY ALTCHA_HMAC_KEY FRONTEND_URL SITE_URL ALLOWED_ORIGINS EMAIL_* VAPID_* DEMO_EMAILS DEV_API_TOKEN PORT NODE_ENV SKIP_RATE_LIMIT`。

## 测试与验证

```bash
go test ./...                          # 单元 + 集成测试（需 mongod 于 127.0.0.1:27017）
go vet ./...                           # 静态检查
node scripts/differential/run.js       # 差分测试（需 Express :5000 + neo :5001 同时运行）
```

差分测试（`scripts/differential/`）把同一组场景打到旧 Express 与 neo-server，比对状态码/JSON/cookie/关键头。当前覆盖全部业务域 45 个场景，**45 PASS / 0 FAIL / 2 SKIP**（2 个认证场景因两端限流计数不同步标 skip，行为由 neo 单测锁定）。行为基线见 `docs/behavior-baseline.md`。

## 架构

```
cmd/server/            入口（--config/--listen、TCP/Unix 监听、优雅关停）
internal/
  config/              ini+env 加载与校验
  server/              Gin 装配（中间件管线顺序）+ 路由注册表
  middleware/          CORS/CSRF/Protect 鉴权/ratelimit/apitracker/requestlogger/bodyparse/sanitize...
  router/              双版本挂载（/api/X 与 /api/v1/X + Deprecation/Sunset）
  handler/             薄 HTTP 层（auth + usersession/twofactor/users + 内容域 episodes/series/follows/favorites/folders/histories/ratings + 管理/审核/站点长尾/通知/工具）
  service/             领域逻辑（登录分支流、refresh 轮换+重用检测、设备登录码链、2FA、导出）
  repository/          mongo 仓储（Repos 聚合，ToObjectID 规范化）
  model/               struct 映射（bson 标签对齐 Mongoose schema）
  auth/                jwt/cookies/bcrypt(cost12)/totp/fieldcrypto(AES-256-CBC)/parseUserAgent
  security/            xss 过滤 Go 移植
  altcha/              Altcha v2 PoW
  ratelimit/           Store 接口 + 内存滑动窗口 + 命名限流器
  code/                验证码内存存储（CodeStore 接口）
  email/               go-mail 封装 + 模板 + 目标限流
  ipregion/            ipapi.co 外呼 + 24h LRU
  upload/              multer 等价（魔数校验）
  pagination/ errors/ logging/ audit/ migrate/ indexes/ cron/
docs/                  swagger 产物 + 行为基线
deploy/                systemd unit + ini 模板 + run.sh
scripts/differential/  差分测试
```

## 认证与安全模型

- **双 token**：access 15m（无状态 JWT）+ refresh 7d（轮换，含随机 jti）。`refreshToken` cookie path=`/api/auth`。
- **419** = access 过期（`auth.accessTokenExpired`），前端自动调 `/api/auth/refresh`。
- **refresh 重用检测**：原子取用并作废；30s 并发宽限内 → 409（不吊销），超期 → 401 且吊销全部会话。
- **CSRF**：非 GET 请求校验 `XSRF-TOKEN` cookie 与 `X-XSRF-TOKEN` header 双拷贝相等（三态 403）。
- **Altcha PoW 验证码**：SHA-256 cost 10000；`DEV_API_TOKEN` + `x-dev-token` 头可绕过（仅开发）。
- **账号锁定**：5 次失败锁 30 分钟（`loginAttempts`/`lockUntil`）。
- **字段加密**：AES-256-CBC，`enc:iv:ct`，key=`sha256(ENCRYPTION_KEY 或 JWT_SECRET)`，兼容旧数据。
- **限流**：全局 300/min + 各端点专用（login 5/15min、register 3/h 等）；per-endpoint 不作用于 `/api/v1/`。

## 部署

- `deploy/furry-drama-be-neo.service`：systemd unit（Unix socket / TCP）。
- `deploy/run.sh`：nohup 备选。
- 优雅关停：SIGTERM/SIGINT → 10s 内排空在途请求。

## 里程碑状态

- M0 骨架 ✓ / M1 基础原语 ✓ / M2 认证+用户域 ✓ / M3 差分闸门 ✓ / M4 部署+文档 ✓
- **内容域 ✓ / 剩余段落 ✓** —— Express 后端全部业务域已实现，差分 45 PASS / 0 FAIL / 2 SKIP

## 功能覆盖

Express 后端全部业务域已实现（Drop-In Replacement，前端零改动）：

- **认证+用户**：auth（session/device/password/email/account）、user-sessions、2fa、users
- **内容域**：episodes、series、follows、favorites、folders、saved-folders、histories、ratings
- **通知**：notifications（含 PushSubscription）
- **管理**：admin、audit-logs、stats、reports、review
- **内容管理**：categories、banners、auto-status、versions
- **站点长尾**：creator-profile、activity、creator、feedback、site-content
- **公告/壁纸/友链**：announcements、wallpapers、friend-links
- **工具**：rss、translate、backup

全部端点经 `router.MountDual` 同时获得 `/api` 与 `/api/v1` 双版本（Deprecation/Sunset 头），前端零改动切换。生产切换见 [deploy/PRODUCTION_SWITCH.md](deploy/PRODUCTION_SWITCH.md)。
