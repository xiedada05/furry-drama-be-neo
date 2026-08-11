# 生产切换手册：Express → neo-server

将生产服务器上的 Express 后端替换为 neo-server（Go/Gin）。目标：**行为向下兼容，旧数据可读，前端零改动**。

> 切换原则：neo-server 复用同一 MongoDB（旧数据原地可读）+ 同一 `JWT_SECRET`/`ENCRYPTION_KEY`（旧 token、AES 字段解密兼容）。**不要建新库、不要改 secret**。

## 0. 前置条件

- 生产服务器有 Go 1.26 或可在本机交叉编译二进制
- MongoDB 已运行，`furry_drama_tracker` 库有数据（旧用户/剧集/追番等）
- 当前 Express 的 `.env` 在手上（用于映射配置）

## 1. 配置映射（Express `.env` → neo `ini`）

复制模板并逐项映射到 `/etc/furry-drama-be-neo.ini`：

| Express `.env` | neo `[section].key` | 必填 | 说明 |
|---|---|---|---|
| `JWT_SECRET` | `[jwt] secret` | **必须一致** | 旧 access/refresh token 验证 + AES key 派生（无 ENCRYPTION_KEY 时） |
| `ENCRYPTION_KEY` | `[jwt] encryption_key` | 建议一致 | AES-256-CBC 解密旧加密字段（邮箱/2FA secret/设备 token） |
| `ALTCHA_HMAC_KEY` | `[jwt] altcha_hmac_key` | 建议一致 | 无则由 secret 派生，行为一致 |
| `MONGO_URI` | `[database] uri` | **指向现有库** | 直接用 Express 正在连的库名 |
| `FRONTEND_URL` | `[server] frontend_url` | 是 | 邮件链接 + CORS |
| `SITE_URL` | `[server] site_url` | 是 | 邮件/上传地址 |
| `ALLOWED_ORIGINS` | `[server] allow_origins` | 是 | 逗号分隔，追加在前端白名单后 |
| `EMAIL_HOST/PORT/USER/PASS/FROM_NAME` | `[email] *` | 是 | SMTP；SiteContent(email) 配置存在时优先 |
| `VAPID_*` | `[vapid] *` | 建议 | web push |
| `SUPERADMIN_EMAIL/ACCOUNT_ID` | 无（写在库里） | — | 迁移脚本已把 adminAccess→role |
| — | `[server] node_env` | 是 | **production** |
| — | `[jwt] dev_api_token` | **留空** | ⚠ 生产必须空，否则 `x-dev-token` 头可绕过 altcha/邮箱/设备验证 |
| `DEMO_EMAILS` | `[jwt] demo_emails` | **留空** | 仅开发生效 |
| `SKIP_RATE_LIMIT` | — | 忽略 | 仅开发生效 |

上传目录：`[server] uploads_dir` 指向原 Express 的 `uploads/`（neo 复用同一目录，旧图片 URL 不变）。

## 2. 数据兼容说明

- **密码**：bcrypt `$2a$`/`$2b$` 均兼容（cost 12），旧用户密码直接可验
- **email/加密字段**：若旧数据是 `enc:iv:ct`，需 `encryption_key`（或 secret）与 Express 一致才能解密；若旧库 email 明文（如现有测试数据）则直接读
- **会话**：旧 refresh token（7d 有效期内）用同 `JWT_SECRET` 验证，切换后无需重新登录；access token 过期后前端自动走 refresh 轮换
- **通知/追番/收藏等**：集合名与 Mongoose 默认小写复数对齐（users/sessions/episodes/series/...），原地可读
- **迁移标记**：neo 启动时自动跑迁移（accountId 回填、adminAccess→role、CreatorProfile adminId→creatorId），幂等，存 settings 集合

## 3. 部署步骤

```bash
# 1) 拉代码并构建
cd /opt/furry-drama-tracker/neo-server
git pull origin main
export GOPROXY=https://goproxy.cn,direct GOFLAGS=-mod=readonly
go build -o bin/furry-drama-be-neo ./cmd/server

# 2) 写配置
cp deploy/furry-drama-be-neo.ini /etc/furry-drama-be-neo.ini
#   按第 1 节映射填写；文件权限 600
chmod 600 /etc/furry-drama-be-neo.ini

# 3) 上传目录
mkdir -p /var/www/furry-drama-tracker/uploads
chown -R www-data:www-data /var/www/furry-drama-tracker/uploads

# 4) 安装 systemd unit
cp deploy/furry-drama-be-neo.service /etc/systemd/system/
#   确认 User/WorkingDirectory/uploads 路径与配置一致
systemctl daemon-reload
systemctl enable --now furry-drama-be-neo

# 5) 健康检查
curl http://127.0.0.1:5000/api/health   # {"status":"ok","db":"connected",...}
journalctl -u furry-drama-be-neo -f     # 观察启动日志（迁移/索引）
```

### Caddy 反代（前端 + API 同域名）

```
example.com {
    reverse_proxy /api/* /uploads/*  unix:///run/furry-drama-be-neo.sock
    reverse_proxy /*  localhost:3000
}
```

- neo 监听 Unix socket（`--listen=unix:/run/furry-drama-be-neo.sock`），systemd `RuntimeDirectory` 已建好
- 也可改 TCP：`--listen=tcp:0.0.0.0:5000`，Caddy 反代 `127.0.0.1:5000`

## 4. 验收清单（切换后逐项过）

| # | 项 | 命令/操作 | 期望 |
|---|---|---|---|
| 1 | 健康 | `GET /api/health` | 200 `{"status":"ok","db":"connected"}` |
| 2 | 旧用户登录 | 用存量账号 + 密码登录 | 200，拿到 token |
| 3 | 前端浏览 | 打开生产域名 | 剧集/详情/追番数据正常渲染 |
| 4 | 新用户注册 | 注册 + 收验证码邮件 | 邮箱收到验证码，能验证 |
| 5 | 上传 | 上传头像/封面 | 文件落 uploads，URL 可访问 |
| 6 | 管理后台 | 超管登录操作 | 用户/内容管理可用 |
| 7 | 双版本 | `GET /api/v1/health` | Deprecation/Sunset 头存在 |
| 8 | 登录态保持 | 隔 15 分钟后刷新 | 前端自动 refresh，不登出 |

## 5. 回滚

neo 与 Express 共用同一 MongoDB + 同一 `JWT_SECRET`，切换是可逆的：

```bash
systemctl stop furry-drama-be-neo
# 启动回 Express（原进程/PM2/systemd），指向同一库
```

- 旧 refresh token 在 neo 期间未被轮换的仍有效；被轮换的（用户刷新过）会在 Express 侧被重用检测拦 → 用户重新登录一次即可
- 数据无破坏（neo 只增改，不删），回滚无数据丢失风险

## 6. 风险与兜底

- **secret 不一致** = 最严重：旧 token 全失效 + AES 字段解不开 → 用户全部重新登录 + 邮箱显示乱码。部署前务必核对 `[jwt] secret` == Express `JWT_SECRET`
- **dev_api_token 非空** = 安全洞：任何人可带 `x-dev-token` 头绕过验证。生产必须留空
- **邮件未配** = 验证码/通知发不出：先配 `[email]`，再用 `/api/site-content` 的 POST /test-email 自测
- **上传目录权限**：www-data 需对 uploads 有写权限，否则上传 500

## 附：与 Express 的已知行为差异（不影响切换）

- 差分锁定行为一致（45 场景 PASS），2 个认证场景（重复注册/改密）因差分环境限流计数不同步标 skip，行为由 neo 单测锁定
- `repository/histories.go` 已迁移为 `histories_extra.go`（内部重构，行为不变）
