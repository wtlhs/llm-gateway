# 部署 Runbook · llm-gateway + llm-platform

> 把网关和可视化管理平台部署到生产服务器。
> 网关前置于 New API，全量捕获公司 LLM 对话流量；平台提供可视化看板+知识库+运维监控。
>
> **当前版本**(v0.3)：
> - 网关：MySQL 反查(users JOIN) + usage 注入 + 截断修复 + Anthropic 支持 + 知识资产分层存储 + caller_name
> - 平台：5 页面(总览/对话/知识库/运维/配置) + 17 API + 6 层安全防护

---

## 〇、部署架构

```
公网用户
   ↓ HTTPS
nginx (newapi.wtlhs.com)
   ├─ /v1/*           → llm-gateway 容器 (127.0.0.1:8080)  代理+沉淀
   └─ /platform/      → llm-platform 容器 (127.0.0.1:8088)  管理平台
       ├─ 网关 → New API 容器 (1Panel-new-api-8YHP:3000)
       ├─ caller 反查 → MySQL (1Panel-mysql-GYfH:3306, JOIN users+tokens)
       └─ 沉淀 → PostgreSQL (llm_gateway 库)
           平台 → 只读查询 PG
```

**两个独立容器**：
- `llm-gateway`：热路径，每条 LLM 请求必经，透明代理+实时沉淀
- `llm-platform`：管理平台，独立进程，只读查询，不影响热路径

所有容器在 `1panel-network` 内，用容器名互联。

---

## 一、前置条件检查

### 1.1 服务器环境
- 服务器：`82.156.138.160`（root）
- 已安装：Docker、nginx、1panel-network
- 容器网络：New API / MySQL / PostgreSQL 都在 `1panel-network`

### 1.2 制品清单

| 制品 | 路径 | 说明 |
|------|------|------|
| 网关镜像 | `dist/llm-gateway.tar` (~9MB) | `llm-gateway:local` |
| 平台镜像 | 本地构建或 `dist/llm-platform.tar` | `llm-platform:local` |
| 网关 compose | `docker-compose.deploy.yml` | 含 1panel-network |
| 平台 compose | `docker-compose.platform.yml` | 含安全配置 |
| 环境配置 | `.env.deploy` | 真实连接串（已 gitignore） |

### 1.3 数据库 Migration（3 个）

沉淀库 PG `llm_gateway` 需执行 3 个 migration：

```bash
# 连接 PG
psql "postgres://<user>:<password>@postgresql.wtlhs.com:5432/llm_gateway"

# 按顺序执行
\i internal/db/migrations/0001_init.up.sql          # llm_conversation 主表
\i internal/db/migrations/0002_system_prompts.up.sql # 分层存储 + system_prompts 表
\i internal/db/migrations/0003_caller_name.up.sql    # caller_name 列(真实用户名)
```

**验证**：
```sql
\dt llm_gateway
-- 应看到: llm_conversation, system_prompts 两张表
\d llm_conversation
-- 应含 caller_name 列
```

---

## 二、传输制品

```bash
# 传输网关
scp dist/llm-gateway.tar docker-compose.deploy.yml .env.deploy root@82.156.138.160:/opt/llm-gateway/

# 传输平台
scp docker-compose.platform.yml root@82.156.138.160:/opt/llm-platform/
```

---

## 三、网关部署

### 3.1 加载镜像 + 启动

```bash
ssh root@82.156.138.160
cd /opt/llm-gateway

docker load -i llm-gateway.tar
docker images llm-gateway:local  # 确认

# 启动(用部署 compose)
docker compose -f docker-compose.deploy.yml --env-file .env.deploy up -d

# 看启动日志
docker logs llm-gateway 2>&1 | head -20
```

**预期日志**：
```
INFO starting llm-gateway listen=:8080 mode=redact
INFO caller cache refreshed tokens=N    ← MySQL JOIN users 成功
```

### 3.2 验证容器网络

```bash
# 确认能解析 New API + MySQL 容器名
docker exec llm-gateway getent hosts 1Panel-new-api-8YHP
docker exec llm-gateway getent hosts 1Panel-mysql-GYfH
```

若解析失败：`docker network connect 1panel-network llm-gateway`

### 3.3 联调测试

```bash
# 容器内自测(经网关 → New API → LLM)
docker exec llm-gateway wget -qO- --timeout=30 \
  --header="Authorization: Bearer sk-<token>" \
  --header="Content-Type: application/json" \
  --post-data='{"model":"MiniMax-M3","messages":[{"role":"user","content":"ping"}]}' \
  http://127.0.0.1:8080/v1/chat/completions
```

---

## 四、平台部署

### 4.1 加载镜像 + 启动

```bash
cd /opt/llm-platform

# 如果有 tar: docker load -i llm-platform.tar
# 如果服务器有 Dockerfile.platform: docker build -f Dockerfile.platform -t llm-platform:local .

# 编辑环境变量(.env.platform 或直接在 compose 里设)
# 关键安全配置(生产必填):
#   PLATFORM_AUTH_TOKEN=<强随机token>
#   PLATFORM_ALLOW_IPS=127.0.0.1,10.0.0.0/8
#   PLATFORM_CORS_ORIGINS=https://newapi.wtlhs.com

docker compose -f docker-compose.platform.yml up -d

# 看启动日志(含安全配置摘要)
docker logs llm-platform 2>&1 | head -20
```

**预期日志**：
```
INFO starting platform server listen=:8088
INFO security: auth token enabled
INFO security: IP allowlist active ips=127.0.0.1,10.0.0.0/8
INFO security: rate limit per_min=120
INFO security: security headers enabled (CSP/X-Frame-Options/HSTS)
```

> ⚠️ 如果看到 `⚠ PLATFORM_AUTH_TOKEN 未设置`，生产必须设置，否则仅 localhost 可访问。

### 4.2 验证平台 API

```bash
# 健康(免鉴权)
curl http://127.0.0.1:8088/api/v1/health
# {"status":"ok"}

# 总览(需鉴权)
curl -H "Authorization: Bearer <token>" http://127.0.0.1:8088/api/v1/dashboard/overview

# 对话列表(验证 caller_name 字段)
curl -H "Authorization: Bearer <token>" \
  "http://127.0.0.1:8088/api/v1/conversations?size=2" | python3 -m json.tool
```

---

## 五、nginx 配置（切换流量）

### 5.1 备份

```bash
sudo cp /etc/nginx/conf.d/newapi.conf /etc/nginx/conf.d/newapi.conf.bak.$(date +%s)
```

### 5.2 修改 nginx

```nginx
upstream newapi_backend {
    server 127.0.0.1:8080;  # 经 llm-gateway
}

server {
    listen 443 ssl;
    server_name newapi.wtlhs.com;
    # ... ssl 配置不变 ...

    # LLM API: 经网关
    location /v1/ {
        proxy_pass http://newapi_backend;
        proxy_http_version 1.1;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_buffering off;           # SSE 必需
        proxy_read_timeout 300s;
        proxy_send_timeout 300s;
    }

    # 管理平台
    location /platform/ {
        proxy_pass http://127.0.0.1:8088/;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
    }

    # New API 管理后台(不经网关)
    location / {
        proxy_pass http://127.0.0.1:3000;  # New API 直连
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
    }
}
```

### 5.3 测试 + 重载

```bash
sudo nginx -t
sudo nginx -s reload
```

### 5.4 线上验证

```bash
# LLM API(经网关)
curl https://newapi.wtlhs.com/v1/chat/completions \
  -H "Authorization: Bearer sk-<token>" \
  -H "Content-Type: application/json" \
  -d '{"model":"MiniMax-M3","messages":[{"role":"user","content":"线上验证"}]}'

# 管理平台
# 浏览器打开 https://newapi.wtlhs.com/platform/
```

---

## 六、安全配置清单

平台含敏感对话数据（prompt/completion），生产部署必须配置：

| 配置项 | 环境变量 | 生产值 | 说明 |
|--------|---------|--------|------|
| 鉴权 Token | `PLATFORM_AUTH_TOKEN` | `<强随机字符串>` | 必填，留空仅 localhost 可访问 |
| IP 白名单 | `PLATFORM_ALLOW_IPS` | `127.0.0.1,10.0.0.0/8` | 限制来源 IP |
| CORS 来源 | `PLATFORM_CORS_ORIGINS` | `https://newapi.wtlhs.com` | 不用 * |
| 速率限制 | `PLATFORM_RATE_LIMIT_PER_MIN` | `120` | 每 IP 每分钟请求数 |

**安全防护（6 层，自动生效）**：
1. Token 鉴权（Bearer + `?token=` 回退）
2. IP 白名单（CIDR）
3. 速率限制（令牌桶，每 IP 独立）
4. 安全响应头（CSP / X-Frame-Options / HSTS / X-Content-Type-Options）
5. CORS 收紧
6. 启动安全摘要日志

---

## 七、回滚

### 流量回滚（秒级）

```bash
# 还原 nginx(流量回直连 New API)
sudo cp /etc/nginx/conf.d/newapi.conf.bak.* /etc/nginx/conf.d/newapi.conf
sudo nginx -s reload
```

### 停容器（可选）

```bash
docker compose -f docker-compose.deploy.yml down      # 网关
docker compose -f docker-compose.platform.yml down     # 平台
```

已沉淀的数据不受影响。

---

## 八、运维操作

### 8.1 升级网关

```bash
cd /opt/llm-gateway
docker load -i llm-gateway.tar  # 新镜像
docker compose -f docker-compose.deploy.yml up -d  # 重建容器
```

### 8.2 升级平台

```bash
cd /opt/llm-platform
docker build -f Dockerfile.platform -t llm-platform:local .  # 或 load tar
docker compose -f docker-compose.platform.yml up -d
```

### 8.3 执行新 Migration

```bash
psql "postgres://<user>:<password>@postgresql.wtlhs.com:5432/llm_gateway" \
  -f internal/db/migrations/000X_xxx.up.sql
```

### 8.4 回填 caller_name（老数据）

老数据的 `caller_name` 为 NULL（网关部署前写入）。可选回填：

```sql
-- 用 caller_user_id 关联更新(需从 MySQL 同步 user_id→username 映射)
-- 或直接在平台执行批量回填脚本
UPDATE llm_conversation SET caller_name = sub.username
FROM (
  SELECT DISTINCT caller_user_id, caller_name FROM llm_conversation
  WHERE caller_name IS NOT NULL
) sub
WHERE llm_conversation.caller_user_id = sub.caller_user_id
  AND llm_conversation.caller_name IS NULL;
```

### 8.5 常用查询

```sql
-- 今日对话量(北京时间, 见 docs/TIMEZONE.md)
SELECT (created_at AT TIME ZONE 'Asia/Shanghai')::date, count(*)
FROM llm_conversation GROUP BY 1 ORDER BY 1 DESC LIMIT 7;

-- caller_name 填充率
SELECT
  count(*) total,
  count(caller_name) with_name,
  round(100.0 * count(caller_name) / count(*), 1) pct
FROM llm_conversation;

-- 知识资产层
SELECT agent_name, use_count FROM system_prompts ORDER BY use_count DESC;

-- VACUUM(若表膨胀)
VACUUM FULL ANALYZE llm_conversation;
VACUUM ANALYZE system_prompts;
```

---

## 九、常见问题

**Q: 平台访问 401 Unauthorized？**
A: `PLATFORM_AUTH_TOKEN` 已设置但请求未带 token。在前端 Settings 页输入 token，或 header 加 `Authorization: Bearer <token>`。

**Q: 平台访问 403 Forbidden？**
A: IP 不在白名单。检查 `PLATFORM_ALLOW_IPS`，加入你的来源 IP/网段。

**Q: 平台访问 429 Too Many Requests？**
A: 触发速率限制。调高 `PLATFORM_RATE_LIMIT_PER_MIN` 或等 60s。

**Q: caller_name 全是空？**
A: 老数据（网关部署前）无 caller_name。重建网关部署后新请求会填充。可选回填（§8.4）。

**Q: caller cache refresh failed（MySQL 连不上）？**
A: `docker exec llm-gateway getent hosts 1Panel-mysql-GYfH`，不通则 `docker network connect 1panel-network llm-gateway`。

**Q: SSE 流式响应卡顿？**
A: nginx 必须 `proxy_buffering off;`（见 §5.2）。

**Q: prompt_text 里看不到 system？**
A: 分层存储生效，system 被抽到 `system_prompts` 表。看 `system_prompt_hash` 列是否非空，非空说明已分流。
