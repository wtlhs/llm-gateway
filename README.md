# llm-gateway · 企业级上下文仓库 Phase 1

架在 [New API](https://github.com/QuantumNous/new-api)(公司级 LLM 通用底座)前的**透明代理网关**,
把全公司所有 agent 的对话流量(prompt + completion 原文)实时沉淀为**企业级上下文仓库**,
为 Phase 2(向量化/分析)、Phase 3(上下文注入)打基础。

> 完整架构与决策依据见 [`DESIGN.md`](./DESIGN.md)。

## 特性

- **零改造接入**:agent 配置的 base_url 不变,改 DNS 即可让全流量经过网关
- **透明代理**:透传 Authorization、原始压缩字节,New API 零改动、升级无忧
- **HTTP + SSE 流式 + WebSocket**:`/v1/realtime` Phase1 仅 passthrough,其余全量捕获
- **高性能**:热路径零阻塞,响应延迟≈纯反代;SSE 单 goroutine 零延迟透传
- **过载保护**:熔断(只覆盖建连失败)+ 按 caller 令牌桶限流(burst/rate 分离)
- **分级背压**:full channel 满时降级 metadata-only,元数据必保
- **脱敏**:落库前正则脱敏(手机/邮箱/身份证/银行卡/API key)
- **可观测**:Prometheus `/metrics` + slog(字段黑名单防泄密)
- **多实例安全**:TTL 用 advisory lock,去重靠 request_id UNIQUE
- **安全合规**:管理端点 bearer 鉴权,token 只存 SHA256 不落盘

## 快速开始

### 1. 环境要求

- Go 1.25+
- PostgreSQL 14+
- 可选:`sqlc`、`goose`(见工具说明)

### 2. 配置

```bash
cp .env.example .env
# 编辑 .env: 重点改 CONTEXT_DB_URL / NEWAPI_DB_URL / NEW_API_BASE_URL
```

### 3. 建库 + 迁移

```sql
-- 在 PG 里建沉淀库
CREATE DATABASE context_repo;
```

```bash
make migrate-up   # 应用 internal/db/migrations 到 context_repo
```

### 4. 运行

```bash
make run   # = go build + 启动
```

### 5. 流量切换(用户无感)

```
原: agent → newapi.company.com → New API
改: agent → newapi.company.com → [llm-gateway] → New API(内网)
```

把 `newapi.company.com` 的 DNS/反代指向网关,网关 `NEW_API_BASE_URL` 指向 New API 内网地址。
agent 配置不变 → 真零改造。回滚:DNS 切回直连。

## 验证

```bash
# 非流式:应返回正常响应 + PG 出现一条记录
curl -X POST localhost:8080/v1/chat/completions \
  -H "Authorization: Bearer sk-xxx" \
  -H "Content-Type: application/json" \
  -d '{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}'

# 流式:应逐字输出,结束后 PG 出现一条聚合记录
curl -X POST localhost:8080/v1/chat/completions \
  -H "Authorization: Bearer sk-xxx" -H "Content-Type: application/json" \
  -d '{"model":"gpt-4o","stream":true,"messages":[{"role":"user","content":"hi"}]}'

# 指标
curl localhost:8080/metrics -H "Authorization: Bearer $ADMIN_AUTH_TOKEN"
```

完整验收清单见 [`DESIGN.md` §8](./DESIGN.md)。

## 数据库

沉淀库 `context_repo` 单表 `llm_conversation`,见 [`migrations/0001_init.up.sql`](./internal/db/migrations/0001_init.up.sql)。

| id 列语义 | 说明 |
|---|---|
| `request_id` | 网关自生成 `X-Ctx-Gateway-Id`,幂等键 |
| `upstream_request_id` | New API 响应头 `X-Oneapi-Request-Id`,关联 New API `logs` 表 |

> **关键**:New API 的 `RequestId()` 中间件会无条件自生成 id(忽略客户端传入),
> 所以两个 id 必须分工,详见 DESIGN.md §5.1.1。

## 项目结构

```
cmd/gateway/         启动入口
internal/
  config/            配置加载+校验(S6)
  gateway/           透明代理核心(transport/proxy/sse_loop/breaker/ratelimit)
  audit/             捕获→降级→反查→脱敏→落库管道
  db/                PG 持久化(migrations + queries + 手写 store)
  metrics/           Prometheus 指标
  security/          管理端点鉴权 + slog 黑名单
  cleanup/           TTL 清理(advisory lock)
  query/             /ctx/stats 查询 API
  hooks/             Phase 2/3 扩展点(no-op)
```

## 工具说明

| 工具 | 用途 | 命令 |
|---|---|---|
| `goose` | 数据库迁移 | `go install github.com/pressly/goose/v3/cmd/goose@latest` |
| `sqlc` | SQL→Go 代码生成(可选优化) | `make sqlc` |

> Phase 1 用手写薄封装 `internal/db/store.go`(镜像 `queries/*.sql` 语义)直跑,
> 避免强制依赖 sqlc。后续 `make sqlc` 生成 `gen/` 后可平滑替换。

## 路线图

| 阶段 | 范围 |
|---|---|
| **Phase 1**(当前) | 透明网关 + HTTP/SSE 沉淀 + 熔断/限流 + 反查 caller + 脱敏/TTL + metrics + 安全 |
| Phase 2 | WebSocket 捕获;`OnConversationComplete`:pgvector 向量化 / 高频问题聚类 / 实体识别 |
| Phase 3 | `OnRequestRewrite`:按意图召回上下文注入(旁路开关必备) |

## 开发

```bash
make test    # 单元测试(race)
make vet     # 静态检查
make fmt     # 格式化
make tidy    # 整理依赖
make build   # 编译单二进制
```

## 验证工具(cmd/)

除单元/集成测试外, 提供真实环境的验证工具(详见各目录 REPORT.md):

| 工具 | 用途 | 运行 |
|---|---|---|
| `cmd/bench` | 压测网关纯开销(P99 增量, mock 上游基线) | `go run ./cmd/bench -concurrency=30 -duration=8s` |
| `cmd/latcmp` | 真实 LLM 延迟对比(直连 vs 网关, 用户体感) | `NEWAPI_LIVE_TOKEN=sk-... go run ./cmd/latcmp/` |
| `cmd/cachechk` | **Prompt 缓存影响验证**(网关是否破坏缓存命中) | `NEWAPI_LIVE_TOKEN=sk-... go run ./cmd/cachechk/` |

**关键验证结论**:
- 网关纯开销 P99 增量 ~1ms, 占真实 LLM 延迟 0.1%, 用户无感([latcmp/REPORT.md](cmd/latcmp/REPORT.md))
- **Prompt 缓存命中率不受影响**(经网关 98% 命中, 与直连一致), 因原字节透传([cachechk/REPORT.md](cmd/cachechk/REPORT.md))

## 合规提示

- 默认留存 90 天,**上线前需法务/安全评审**按公司数据分级政策确认(DESIGN.md §0.2)
- `LLM_AUDIT_MODE=redact` 为默认(脱敏);`full` 仅限受控环境
- token key 仅以 SHA256 落盘,绝不存明文(DESIGN.md §9 / M3)
