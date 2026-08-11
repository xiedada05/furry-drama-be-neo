# 扩展与维护指南 (Contributing)

本文面向**未来人工维护者**：如何本地跑起来、如何加新功能、有哪些规范必须遵守、常见问题怎么排查。**先读 README.md + architecture.md + decisions.md**。

## 环境与运行

```bash
# 前置：Go ≥1.26、mongod 于 127.0.0.1:27017
go build -o bin/furry-drama-be-neo ./cmd/server

# 开发（.env 自动加载，无需 --config）
./bin/furry-drama-be-neo

# 或开发（env 覆盖，无需 ini）
JWT_SECRET=<≥32字符> MONGO_URI=mongodb://127.0.0.1:27017/furry_drama_tracker \
NODE_ENV=development DEV_API_TOKEN=test-dev-token SKIP_RATE_LIMIT=1 \
./bin/furry-drama-be-neo --listen=tcp:0.0.0.0:5000

# 生产（ini 配置，路径按 OS 区分）
./bin/furry-drama-be-neo --config=/etc/furry-drama-tracker/backend.ini
# Unix socket（systemd 推荐）
./bin/furry-drama-be-neo --config=/etc/furry-drama-tracker/backend.ini --listen=unix:/run/furry-drama-be-neo.sock
```

配置优先级：**真实环境变量 > `.env` 文件 > ini 配置文件 > 默认值**。开发时在当前目录放一个 `.env`（格式同 dotenv）即可零配置启动。配置模板 `deploy/furry-drama-be-neo.ini`；systemd unit `deploy/furry-drama-be-neo.service`；nohup `deploy/run.sh`。

## 测试

```bash
go test ./...          # 单元 + 集成测试（需 mongod；集成测试用 neo_integration_test 库）
go vet ./...           # 静态检查
go build ./...         # 编译检查

# 差分测试（行为回归闸门）：需要旧 Express 于 :5000 + neo 于 :5001 同时运行
node scripts/differential/run.js
```

**差分测试是行为一致性的 ground truth**。改了任何影响 HTTP 响应的代码后必须跑，保持 26/26 全绿（第一段范围）。集成测试覆盖关键流程（登录/2FA/refresh 重用）。

## 扩展标准模式

### 加一个 HTTP 端点（四步）

以"加 `GET /api/auth/ping`"为例：

1. **handler**（`internal/handler/`）加方法，只做解析+调 service+组装响应：
   ```go
   // Ping GET /api/auth/ping：返回当前时间。
   func (h *Auth) Ping(c *gin.Context) {
       c.JSON(200, gin.H{"now": time.Now().Unix()})
   }
   ```
2. **service**（如需业务逻辑）在 `service.AuthService` 加方法；纯透传可跳过。
3. **挂路由**（`internal/server/register_routes.go`）在对应 `MountDual` 块内：
   ```go
   g.GET("/ping", amw.Protect(), h.Ping)   // 需要登录就加 Protect
   // 需要限流：rl(ratelimit.XXXSpec)（见 ratelimit 包 Spec 定义）
   // 需要角色：amw.Protect("admin", "superadmin")
   ```
   `MountDual` 自动获得 `/api/auth/ping` 与 `/api/v1/auth/ping`（v1 带弃用头）。
4. **swagger 注解**（`@Summary/@Tags/@Router`，参考 handler/auth.go 现有注解）→ `~/go/bin/swag init -g cmd/server/main.go --output docs` 重新生成 → 验证 `/api/docs`。

**规则**：handler 不写业务逻辑（查询/决策都在 service）；错误用 `errors.New/NewKey` 抛，handler 用 `errors.AbortWithAppError(c, err, isDev)` 渲染。

### 加一个 collection（三步）

1. `internal/model/` 加 struct（bson 标签对齐 Mongoose schema）。
2. `internal/repository/` 加 repo 文件 + 方法；在 `Repos` 聚合（`repos.go`）注册：`NewXxxRepo(db.Collection("xxx"))`。
3. `repository.NewRepos` 把新 repo 挂到 `Repos` 结构体（service/handler 通过 `Deps.Repos` 访问）。

### 加一个定时任务

`internal/cron/` 有会话清理/通知清理示例。新任务 = goroutine + time.Ticker + context 取消；在启动处注册。

### 加配置项

`internal/config/config.go`：在对应 section struct 加字段 → `applyINI` 读 ini → `applyEnv` 读 env。`Config` 是唯一配置入口，跨包经 `Deps.Config` 传递。

## 代码规范（必须遵守）

1. **godoc 注释**：每个导出类型/函数写注释（审计/回归手搓要用）。已覆盖 275 个导出符号。
2. **错误处理**：领域错误用 `errors.New(status, message)` / `errors.NewKey(status, message, key)`（key 是前端 i18n 键，不能随便改）；HTTP 层用 `AbortWithAppError`。
3. **行为铁律**（`AGENTS.md`）：双 token、419 语义、refresh 重用、CSRF 三态、登录分支顺序、AES 兼容、限流挂载不对称——改之前先读，改之后跑差分。
4. **依赖方向**：只允许向下（middleware → handler → service → repository → model），禁止反向。
5. **ID 查询**：用 `repository.ToObjectID`（hex → ObjectID），不要直接传字符串查 `_id`。
6. **不引入新的"自造轮子"**：能用成熟库用库（见 decisions.md ADR-008）；自实现需在 decisions.md 记录理由。

## 调试排查

| 现象 | 排查方向 |
|---|---|
| 前端所有写操作 403 | CSRF：cookie `XSRF-TOKEN` 与 header `X-XSRF-TOKEN` 必须相等；检查 `/api/csrf-token` 是否被限流/失败 |
| 登录后很快被登出 | access 过期（419）→ 前端自动 refresh；若 refresh 失败查会话吊销逻辑（重用检测）；确认 refresh token 带 jti |
| refresh 返回 409 | 并发刷新宽限（30s 内旧 token 再用）——正常行为，非错误 |
| 429 过早触发 | 限流窗口/计数；dev 环境设 `SKIP_RATE_LIMIT=1` 跳过 auth 限流 |
| 邮箱验证码收不到 | 检查 SMTP 配置（ini [email] 或 env EMAIL_*）；目标邮箱限流 10 封/h；dev 用 `DEV_API_TOKEN` + `x-dev-token` 头绕过 |
| 2FA 验证失败 | secret 是 base64（Express 旧数据兼容），`internal/auth/totp.go` 内部转 base32 用 pquerna；确认 ENCRYPTION_KEY 或 JWT_SECRET 与存储时一致 |
| 差分测试 FAIL | 用 `docs/behavior-baseline.md` + Express 实际响应对比；先归一化（`scripts/differential/normalize.js`）区分"真实差异"与"合法变化（随机值/时间戳）" |

## 发布流程

1. `go build ./... && go vet ./... && go test ./...`
2. 跑差分（需 Express oracle 在 :5000）
3. 更新 `CHANGELOG.md`（新增/修复/重构条目）
4. `git push origin main`
