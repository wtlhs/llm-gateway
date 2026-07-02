# 部署 Runbook · llm-gateway

> 把网关部署到生产服务器, 前置于 New API, 全量捕获公司 LLM 对话流量。
> 形态: Docker 容器 + nginx 反代, 全程可回滚。
>
> **当前版本**:含 MySQL 反查 + usage 注入 + 截断修复 + Anthropic 支持 + 知识资产分层存储。

---

## 〇、部署架构

```
公网用户
   ↓ HTTPS
nginx (newapi.wtlhs.com)
   ↓ proxy_pass
llm-gateway 容器 (127.0.0.1:8080)
   ├─ 透明代理 → New API 容器 (1Panel-new-api-8YHP:3000)
   ├─ caller 反查 → MySQL 容器 (1Panel-mysql-GYfH:3306, 只读)
   └─ 对话沉淀 → PostgreSQL (postgresql.wtlhs.com:5432, llm_gateway 库)
```

**核心设计**:网关对用户完全透明(用户无感),只做"边转发边沉淀"。
所有容器在同一个 `1panel-network` 内,用容器名互联。

---

## 一、前置条件检查

### 1.1 服务器环境
- 服务器:`82.156.138.160`(root)
- 已安装:Docker、nginx(已反代到 New API)
- 容器网络:`1panel-network`(New API / MySQL / PostgreSQL 都在此网络)

### 1.2 部署制品清单

本地 `D:\GitHubProjects\NewApiLog\` 下应存在:

| 制品 | 路径 | 说明 |
|------|------|------|
| 镜像 tar | `dist/llm-gateway.tar` (~9.1MB) | Docker 镜像 `llm-gateway:local` |
| compose 文件 | `docker-compose.deploy.yml` | 生产容器编排 |
| 环境配置 | `.env.deploy` | 含真实连接串(**不入库, 已 gitignore**) |

确认制品齐全:
```bash
ls -lh dist/llm-gateway.tar docker-compose.deploy.yml .env.deploy
```

### 1.3 数据库准备

**沉淀库(PG)**:`llm_gateway` 库已建,需执行 migration(2 个):

```sql
-- 连接: postgresql://<user>:<password>@postgresql.wtlhs.com:5432/llm_gateway
-- 在该库执行以下两个 migration(若尚未执行)

-- 0001: llm_conversation 表(主表)
\i internal/db/migrations/0001_init.up.sql

-- 0002: 知识资产分层存储(新增 system_prompts 表 + 引用列)
\i internal/db/migrations/0002_system_prompts.up.sql
```

**验证表已建**:
```sql
\dt llm_gateway
-- 应看到: llm_conversation, system_prompts 两张表
```

---

## 二、传输制品到服务器

在本地(本机):
```bash
# 传 3 个文件到服务器
scp dist/llm-gateway.tar docker-compose.deploy.yml .env.deploy root@82.156.138.160:/opt/llm-gateway/
```

> 若服务器上还没有 `/opt/llm-gateway/` 目录,先 `ssh root@82.156.138.160 'mkdir -p /opt/llm-gateway'`

---

## 三、服务器上部署

SSH 到服务器:
```bash
ssh root@82.156.138.160
cd /opt/llm-gateway
```

### 3.1 加载镜像
```bash
docker load -i llm-gateway.tar
# 确认镜像存在
docker images llm-gateway:local
# 应显示: llm-gateway:local  <image-id>  ... 9.1MB
```

### 3.2 确认配置文件
```bash
# 检查 .env.deploy 关键项(确认连接串正确)
grep -E "NEW_API_BASE_URL|CONTEXT_DB_URL|NEWAPI_DB_URL" .env.deploy
```

**关键配置项核对**(`.env.deploy`):
```env
# 上游 New API(容器名:端口, 同在 1panel-network)
NEW_API_BASE_URL=http://1Panel-new-api-8YHP:3000

# 沉淀库(PG, 用容器名或外网域名)
CONTEXT_DB_URL=postgres://<user>:<password>@1Panel-postgresql-AWYE:5432/llm_gateway?sslmode=disable

# 反查库(MySQL, 与 New API 容器的 SQL_DSN 一致)
NEWAPI_DB_URL=<user>:<password>@tcp(1Panel-mysql-GYfH:3306)/new_api?charset=utf8mb4&parseTime=true&loc=Local
```

> ⚠️ `1Panel-mysql-GYfH` / `1Panel-new-api-8YHP` 是 New API 容器内视角的主机名,
> llm-gateway 容器必须加入 `1panel-network` 才能解析(见 3.3)。

### 3.3 启动容器
```bash
# 用部署专用 compose 启动(已配置 1panel-network 加入)
docker compose -f docker-compose.deploy.yml up -d

# 查看启动日志
docker logs llm-gateway 2>&1 | head -20
```

**预期日志**(健康启动):
```
INFO starting llm-gateway listen=:8080 upstream=http://1Panel-new-api-8YHP:3000 mode=redact
INFO caller cache refreshed tokens=N    ← MySQL 反查成功(N>0)
INFO context db connected               ← PG 沉淀库连接成功
```

**异常排查**:
- `caller cache refresh failed` → MySQL 连不上,检查容器网络(3.3 的 network)或 DSN
- `context db connect failed` → PG 连不上,检查 CONTEXT_DB_URL
- 上述失败网关仍能启动(MySQL 失败降级为 noop caller cache;PG 失败则退出)

### 3.4 验证容器内网络联通
```bash
# 确认能解析 New API 容器名
docker exec llm-gateway getent hosts 1Panel-new-api-8YHP
# 确认能解析 MySQL 容器名
docker exec llm-gateway getent hosts 1Panel-mysql-GYfH
```

若解析失败,手动加入网络:
```bash
docker network connect 1panel-network llm-gateway
```

---

## 四、联调验证(切流量前)

### 4.1 容器内自测
```bash
# 网关健康检查(无需鉴权)
docker exec llm-gateway wget -qO- http://127.0.0.1:8080/ctx/stats || echo "需 ADMIN_AUTH_TOKEN, 见下"

# 带鉴权的完整请求测试(经网关 → New API → 真实 LLM)
docker exec llm-gateway wget -qO- --timeout=30 \
  --header="Authorization: Bearer sk-你的token" \
  --header="Content-Type: application/json" \
  --post-data='{"model":"MiniMax-M3","messages":[{"role":"user","content":"ping"}]}' \
  http://127.0.0.1:8080/v1/chat/completions
# 应返回正常的 LLM 响应
```

### 4.2 沉淀验证
```bash
# 查 PG 落库(用 psql 或 Go 探针)
psql "postgres://<user>:<password>@postgresql.wtlhs.com:5432/llm_gateway" \
  -c "SELECT id, model, caller_tag, prompt_tokens, system_prompt_hash, created_at
      FROM llm_conversation ORDER BY id DESC LIMIT 3;"
```

**预期**:新记录的 `prompt_tokens` 非零(usage 注入生效)、`system_prompt_hash` 可能有值(分层存储生效)、`caller_tag` 有 token 名(反查生效)。

### 4.3 知识资产层验证
```sql
-- 查 system_prompts 表是否有去重的 system prompt
SELECT hash, agent_name, use_count, content_size, last_seen
FROM system_prompts ORDER BY use_count DESC LIMIT 5;
```

---

## 五、切换流量(关键步骤!影响全公司)

### 5.1 备份 nginx 配置(回滚用)
```bash
sudo cp /etc/nginx/conf.d/newapi.conf /etc/nginx/conf.d/newapi.conf.bak.$(date +%s)
```

### 5.2 修改 nginx upstream
编辑 `/etc/nginx/conf.d/newapi.conf`,把 upstream 从原 New API 指向网关:

```nginx
upstream newapi_backend {
    # 原(直连 New API):
    # server 127.0.0.1:3000;
    # 改(经 llm-gateway):
    server 127.0.0.1:8080;
}

server {
    listen 443 ssl;
    server_name newapi.wtlhs.com;
    # ... 原有 ssl 配置不变 ...

    location / {
        proxy_pass http://newapi_backend;
        proxy_http_version 1.1;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;

        # SSE 必需: 关闭缓冲, 长连接(流式响应必须!)
        proxy_buffering off;
        proxy_read_timeout 300s;
        proxy_send_timeout 300s;
        # WebSocket(realtime 用)
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection "upgrade";
    }
}
```

### 5.3 测试 + 重载
```bash
sudo nginx -t          # 语法检查
sudo nginx -s reload   # 零停机重载
```

### 5.4 线上验证
```bash
# 公网请求(经 DNS → nginx → 网关 → New API → LLM)
curl https://newapi.wtlhs.com/v1/chat/completions \
  -H "Authorization: Bearer sk-你的token" \
  -H "Content-Type: application/json" \
  -d '{"model":"MiniMax-M3","messages":[{"role":"user","content":"线上验证"}]}'

# 实时看网关日志
docker logs -f llm-gateway

# 看 Prometheus 指标
curl -H "Authorization: Bearer $ADMIN_AUTH_TOKEN" http://localhost:8080/metrics | grep gateway_request
```

---

## 六、回滚(出问题时)

流量回滚是**秒级**的(改 nginx 指回原 New API),已沉淀的数据不受影响:

```bash
# 1. 还原 nginx 配置(秒级回滚流量)
sudo cp /etc/nginx/conf.d/newapi.conf.bak.* /etc/nginx/conf.d/newapi.conf
sudo nginx -s reload
# 流量立即回到直连 New API

# 2. 停网关容器(可选, 也可保留观察日志)
docker compose -f docker-compose.deploy.yml down
```

---

## 七、常见问题

**Q: `caller cache refresh failed`(MySQL 反查失败)?**
A: 网关会降级运行(caller_tag 留空),不影响代理+沉淀。排查:
```bash
docker exec llm-gateway getent hosts 1Panel-mysql-GYfH  # 网络是否通
```
若不通,`docker network connect 1panel-network llm-gateway`。

**Q: SSE 流式响应被缓冲/卡顿?**
A: nginx 必须 `proxy_buffering off;`(见 5.2)。否则流式响应会被 nginx 缓冲,客户端体验差。

**Q: 新记录 prompt_tokens 还是 0?**
A: 确认流量确实经网关(看 nginx upstream 是 8080),且网关日志无 `system prompt upsert failed`。usage 注入只对流式 chat 请求生效。

**Q: system_prompts 表为空?**
A: 知识资产分层存储只对**含 system prompt 的请求**生效。若都是简单问答(无 system),表自然为空。发一个带 system 的 coding agent 请求再查。

**Q: 历史数据(部署前的记录)system_prompt_hash 为 NULL?**
A: 正常。老数据 prompt_text 仍含完整 system(向前兼容)。新数据才走分层。可选:用回填脚本处理老数据(见 KNOWLEDGE_LAYER.md §5.2)。

**Q: 容器健康检查失败?**
A: `/ctx/stats` 需鉴权(ADMIN_AUTH_TOKEN)。若 METRICS_AUTH_TOKEN/ADMIN_AUTH_TOKEN 为空,端点免鉴权;生产建议设置 token。

---

## 八、运维操作

### 8.1 升级镜像
```bash
cd /opt/llm-gateway
# 传新 tar, 加载, 重启
docker load -i llm-gateway.tar
docker compose -f docker-compose.deploy.yml up -d  # 自动重建容器
```

### 8.2 执行新 migration
```bash
# 若新版有新 migration(如 0003), 手动执行
psql "postgres://<user>:<password>@postgresql.wtlhs.com:5432/llm_gateway" \
  -f internal/db/migrations/0003_xxx.up.sql
```

### 8.3 查看沉淀统计
```sql
-- 今日对话量(北京时间, 见 docs/TIMEZONE.md)
SELECT (created_at AT TIME ZONE 'Asia/Shanghai')::date AS day, count(*)
FROM llm_conversation
GROUP BY 1 ORDER BY 1 DESC LIMIT 7;

-- 最活跃 agent(知识资产层)
SELECT agent_name, use_count, last_seen
FROM system_prompts ORDER BY use_count DESC LIMIT 10;

-- token 计量填充率(应接近 100%)
SELECT
  count(*) total,
  count(*) FILTER (WHERE prompt_tokens > 0) with_usage,
  round(100.0 * count(*) FILTER (WHERE prompt_tokens > 0) / count(*), 1) AS pct
FROM llm_conversation;
```

### 8.4 VACUUM(偶发, 防膨胀)
```sql
-- 若表异常膨胀(查 pg_total_relation_size 远超数据量), 执行
VACUUM FULL ANALYZE llm_conversation;
VACUUM ANALYZE system_prompts;
```
