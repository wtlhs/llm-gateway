# 部署 Runbook · llm-gateway

> 把网关部署到服务器, 前置于 New API。
> 形态: Docker 容器 + nginx 反代。
> 全程可回滚。

## 前置条件

- 服务器: Linux + Docker + nginx(已反代到 New API)
- 已有制品: `llm-gateway.tar`(镜像)、`docker-compose.yml`、`.env`
- PG: 已建 `llm_gateway` 库 + 迁移(见 README)

---

## 步骤 1: 传输制品到服务器

在本地(本机):
```bash
# 假设服务器 SSH: user@your-server
scp llm-gateway.tar docker-compose.yml .env user@your-server:/opt/llm-gateway/
```

## 步骤 2: 服务器上加载镜像 + 启动

SSH 到服务器:
```bash
ssh user@your-server
cd /opt/llm-gateway

# 加载镜像(无需镜像仓库)
docker load -i llm-gateway.tar
# 确认: docker images llm-gateway:local

# 启动容器
docker compose up -d

# 验证启动 + 健康
docker logs llm-gateway | head
docker exec llm-gateway wget -qO- http://127.0.0.1:8080/ctx/stats
# 应返回 {"total":"...",...}
```

此时网关在 `127.0.0.1:8080` 监听, **尚未接公网流量**(nginx 还指向原 New API)。

## 步骤 3: 联调(切流量前)

在服务器本机验证网关能正常代理 + 落库:
```bash
docker exec llm-gateway wget -qO- --timeout=30 \
  --header="Authorization: Bearer sk-你的token" \
  --header="Content-Type: application/json" \
  --post-data='{"model":"你的模型","messages":[{"role":"user","content":"ping"}]}' \
  http://127.0.0.1:8080/v1/chat/completions

# 查 PG 落库
psql "$CONTEXT_DB_URL" -c "SELECT count(*) FROM llm_conversation;"
```

## 步骤 4: 切流量(关键! 影响全公司, 先备份 nginx 配置)

```bash
# 备份当前 nginx 配置(回滚用)
sudo cp /etc/nginx/conf.d/newapi.conf /etc/nginx/conf.d/newapi.conf.bak.$(date +%s)
```

修改 nginx, 把 newapi 的 upstream 从原 New API 指向网关:

```nginx
# /etc/nginx/conf.d/newapi.conf (修改后)
upstream newapi_backend {
    # 原: server 127.0.0.1:3000;       # New API 直接
    # 改: server 127.0.0.1:8080;       # 经 llm-gateway
    server 127.0.0.1:8080;
    # 若 New API 不在本机, 填 New API 内网地址到网关的 NEW_API_BASE_URL, 这里指网关
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

        # SSE 必需: 关闭缓冲, 长连接
        proxy_buffering off;
        proxy_read_timeout 300s;
        proxy_send_timeout 300s;
        # WebSocket(Phase2 realtime 用)
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection "upgrade";
    }
}
```

```bash
# 测试配置语法
sudo nginx -t
# 重载(零停机)
sudo nginx -s reload
```

## 步骤 5: 验证线上

```bash
# 公网请求(经 DNS → nginx → 网关 → New API)
curl https://newapi.wtlhs.com/v1/chat/completions \
  -H "Authorization: Bearer sk-你的token" \
  -H "Content-Type: application/json" \
  -d '{"model":"你的模型","messages":[{"role":"user","content":"线上验证"}]}'

# 看网关日志 + 指标
docker logs -f llm-gateway
curl -H "Authorization: Bearer $ADMIN_AUTH_TOKEN" http://localhost:8080/metrics | grep gateway_request_total
```

---

## 回滚(出问题时)

流量回滚是秒级的(改 nginx 指回原 New API):

```bash
# 还原 nginx 配置
sudo cp /etc/nginx/conf.d/newapi.conf.bak.* /etc/nginx/conf.d/newapi.conf
sudo nginx -s reload
# 流量立即回到直连 New API, 已沉淀的数据不受影响

# 停网关容器(可选, 也可保留观察)
docker compose down
```

---

## 常见问题

**Q: `NEWAPI_DB_URL` 连不上(caller 反查)?**
A: 网关会降级运行(caller_tag 留空), 不影响代理+沉淀。查 New API 的真实库名填对。
   生产建议修好, 否则 caller 分析(哪个 agent 发的)不可用。

**Q: SSE 流式响应被缓冲/卡顿?**
A: nginx 必须 `proxy_buffering off;`(见上方配置)。

**Q: 容器健康检查失败?**
A: `/ctx/stats` 需鉴权(ADMIN_AUTH_TOKEN)。生产可加专门的 `/healthz` 免鉴权端点。
