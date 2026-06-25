# 企业级上下文仓库 · Phase 1:对话流量沉淀网关(Go 实施方案)

> 本文件是经过多轮对齐后确定的**终版架构与实施计划**,作为开发依据。
> 生成日期:2026-06-25
> 位置:`D:\GitHubProjects\NewApiLog\DESIGN.md`

---

## 0. 决策对齐(全部已拍板)

| 决策点 | 结论 | 架构影响 |
|---|---|---|
| 数据策略 | 先沉淀后分析 | Phase1 只 capture+落库 |
| 集成方式 | 网关层、用户无感 | 前置透明代理 |
| 数据来源 | New API 实时流量 | 不拉日志,实时捕获 |
| 技术栈 | Go(最高性能+最佳实践) | httputil.ReverseProxy + 单 goroutine 尽力捕获 + body 原字节还原 |
| 高可用 | 单实例起步+预留多实例 | TTL 用 advisory lock;去重靠 request_id UNIQUE |
| 过载保护 | 熔断+按 caller 限流 | Transport 包 gobreaker + 令牌桶(burst/rate 分离) |
| caller 标识 | 反查 New API token 表 | 只读连 New API 库,启动拉映射+定时刷新 |
| 可观测性 | Prometheus metrics + slog | /metrics + 结构化日志(prompt/completion 进黑名单) |
| **端点白名单** | **见 §0.1** | 决定哪些端点走捕获、schema 是否适用 |
| **WebSocket** | **Phase 2** | Phase 1 `/v1/realtime` 仅透传不捕获,移出验收 |
| **留存期** | **默认 90 天,需法务/安全评审确认** | TTL 配置化,合规先行 |

### 0.1 端点白名单

New API 实际暴露约 12 个端点,并非都适合统一 schema 捕获。按形态分级:

| 等级 | 端点 | Phase 1 处理 | 理由 |
|---|---|---|---|
| **A 捕获** | `POST /v1/chat/completions`、`POST /v1/completions`、`POST /v1/responses` | ✅ 全量捕获 prompt+completion | 结构化文本,JsonB schema 完全适用 |
| **B 捕获(特化)** | `POST /v1/embeddings` | ✅ 只捕获 input,completion_tokens=0 | 只有 prompt 无 completion |
| **B 捕获(特化)** | `POST /v1/moderations` | ✅ 捕获 input+results | 文本输入,适合审计 |
| **C 仅计费不捕获** | `POST /v1/images/*`、`POST /v1/audio/*` | ⚠️ multipart/二进制,记元数据+大小,不存 body | JsonB 不适合二进制 |
| **D 不捕获** | `GET /v1/models`、`POST /v1/rerank`(可选) | ❌ 透传不落库 | 非业务对话 / 列表查询 |
| **D 不捕获** | `WS /v1/realtime` | ❌ Phase 1 仅透传 | 见下,移至 Phase 2 |

> **配置化**:`LLM_AUDIT_CAPTURE_ENDPOINTS=chat/completions,completions,responses,embeddings,moderations` 控制白名单,运维可调。等级 C/D 默认不在白名单,纯透传。
> **stream 字段判定**:`is_stream` 从 **请求 body 的 `stream` 字段**读取(配合 §5.2 的 SSE 分支),不从响应头推断。

### 0.2 留存与合规(需法务/安全评审)

- 90 天为**默认建议值**,正式上线前需法务/安全评审按公司数据分级政策确认。
- 不同业务/模型可设不同 TTL(通过 `LLM_AUDIT_TTL_DAYS_<MODEL>` 覆盖,v1.1 实现)。
- 审计系统的价值在于"出事时不丢数据",因此捕获完整性优先于压缩存储。

---

## 1. 背景与目标

New API(QuantumNous/new-api,基于 songquanpeng/one-api 二次开发,当前 v0.13.2)作为公司级 LLM 通用底座,全公司 agent 共用。目标:将其所有 LLM 交互沉淀为**企业级上下文仓库**,后续反哺 agent。

**已核实事实(New API v0.13.2 源码)**:

| 事实 | 证据 |
|---|---|
| `logs` 表只存元数据,无 prompt/completion 字段 | `model/log.go:34-56`(25 字段全元数据) |
| `RecordConsumeLog` 只写元数据,不写原文 | `model/log.go:318-381` |
| `RelayInfo` 不持有请求/响应 body | `relay/common/relay_info.go:88-128` |
| 鉴权头 `Authorization: Bearer sk-xxx` | `middleware/auth.go:45` |
| 请求体支持 gzip/br 压缩 | `middleware/gzip.go:42-67` |
| 请求体上限 32MB | `constant.MaxRequestBodyMB` |
| 入口路由 `POST /v1/chat/completions` 等 | `router/relay-router.go:96` |
| WebSocket 入口 `/v1/realtime` | `router/relay-router.go:78` |
| 响应头回传 `X-Oneapi-Request-Id` | `middleware/request-id.go:16`、`common/constants.go:196` |
| **New API 无条件自生成 request_id,忽略客户端传入** | `middleware/request-id.go:12`(`common.NewRequestId()`,不看请求头) |
| token 表字段:Key/Name/UserId/Group | `model/token.go:14-32`、`GetTokenByKey`(token.go:255) |

---

## 2. 架构总览

```
  所有 Agent / 客户端 (Bearer token 透传, 零改造)
        │
        ▼
┌─────────────────────────────────────────────────────────────┐
│  LLM Gateway (Go, 单二进制, 本项目)                           │
│  ┌ Transport 层(最高性能)──────────────────────────────┐  │
│  │ httputil.ReverseProxy · HTTP/2 · 请求body解压+原字节还原 │
│  │ ├ gobreaker 熔断(New API 慢/挂时保护)                │  │
│  │ └ 令牌桶限流(按 caller, burst/rate 分离, 限流不计入熔断) │
│  └─────────────────────────────────────────────────────┘  │
│  ┌ 透传层 ────────────────────────────────────────────┐   │
│  │ HTTP 非流式: 整体捕获响应                            │   │
│  │ SSE 流式: 单 goroutine 尽力捕获(零延迟转发)         │   │
│  │ WS /v1/realtime: Phase1 仅 passthrough(Phase2 捕获) │   │
│  └─────────────────────────────────────────────────────┘   │
│  ┌ 沉淀层(异步, 背压)─────────────────────────────────┐  │
│  │ capture → buffered channel → worker pool             │  │
│  │   ├ 反查 token→{name,user_id,group} 填 caller_tag    │  │
│  │   ├ redact 脱敏 → truncate 截断                      │  │
│  │   └ sqlc → PG(context_repo 库) ON CONFLICT 幂等      │  │
│  └─────────────────────────────────────────────────────┘  │
│  ┌ 横切 ──────────────────────────────────────────────┐   │
│  │ Prometheus /metrics · slog · TTL(advisory lock)     │   │
│  └─────────────────────────────────────────────────────┘   │
│  ┌ Phase2 hook: OnConversationComplete(向量化/抽取)      │  │
│  └ Phase3 hook: OnRequestRewrite(上下文注入)            │  │
└─────────────────────────────────────────────────────────────┘
        │  (内网, 透传 Authorization)
        ▼
   New API (公司级通用底座, 零改动)
        ▼
     上游 LLM
```

**最高性能与最佳实践的体现**:

1. **热路径零阻塞**:请求方向只"读 body + 算 hash",record 在响应阶段推 channel,落库/脱敏/反查全异步。响应延迟≈纯反代。
2. **流式零延迟**:单 goroutine 转发+尽力捕获(捕获永不返 error),客户端体感同直连。
3. **分级背压保护**(I4):full channel 满时降级为 metadata-only(必保元数据),双 channel 都满才彻底丢 + 计数告警,**绝不阻塞请求**。
4. **熔断+限流**(M2):New API **建连失败**时熔断防雪崩;流中断走独立指标不熔断;单 agent 暴涨时限流防挤占。
5. **单二进制 + 标准库为主**:运维友好,依赖克制。

---

## 3. 技术选型(全部 Go 最佳实践)

| 维度 | 选型 | 理由 |
|---|---|---|
| 反向代理 | 标准库 `net/http/httputil.ReverseProxy` | Traefik/Caddy 同款;原生 HTTP/2、连接复用 |
| SSE 透传 | 单 goroutine + boundedWriter | 转发与捕获同循环,捕获 error 不反噬转发 |
| WebSocket | `gorilla/websocket` + 双向 dial | `/v1/realtime` 覆盖 |
| 请求解压 | `compress/gzip` + `github.com/andybalholm/brotli` | 与 New API `gzip.go` 一致 |
| 熔断 | `github.com/sony/gobreaker` | Go 熔断事实标准 |
| 限流 | `golang.org/x/time/rate` 令牌桶 | 标准库系,按 token 维度 |
| 存储 | PostgreSQL | JsonB + 成熟运维;Phase2 接 pgvector |
| 查询层 | **`sqlc`**(写 SQL 生成 Go) | 零反射、类型安全;优于 GORM |
| DB driver | `github.com/jackc/pgx/v5` + `database/sql` | 高并发标配 |
| 迁移 | `github.com/pressly/goose/v3` | 纯 Go,二进制友好 |
| 可观测 | `github.com/prometheus/client_golang` + `log/slog` | 公司级标配 |
| 配置 | `github.com/kelseyhightower/envconfig` | env 优先,轻量 |

---

## 4. 数据库设计

### 4.1 双库连接

- **`context_repo` 库**(读写):存沉淀数据。
- **`new-api` 库**(只读):反查 token 映射。

### 4.2 `context_repo.llm_conversation`

```sql
-- migrations/0001_init.up.sql
CREATE TABLE llm_conversation (
    id                  BIGSERIAL PRIMARY KEY,
    -- id 语义(M1 修正): 见 §5.1 "request_id 关联设计"
    --   request_id           = 网关自生成的 gateway_id, 进请求时注入 X-Ctx-Gateway-Id 头, 全程追踪 + 幂等键
    --   upstream_request_id  = 从 New API 响应头 X-Oneapi-Request-Id 读到的 id, 即 New API logs 表里的 id, 跨库关联键
    request_id          VARCHAR(64)  NOT NULL UNIQUE,   -- gateway_id(网关自生成), 幂等键
    upstream_request_id VARCHAR(128),                   -- New API 的 X-Oneapi-Request-Id, 关联 New API logs
    -- caller 标识(反查 token 表填充; 失败时用 token_key_hash 兜底回填)
    caller_tag          VARCHAR(128),                   -- token.Name, e.g. "scm-prod-agent"
    caller_user_id      INTEGER,                        -- token.UserId; 用于行级权限过滤
    caller_group        VARCHAR(64),                    -- token.Group
    token_key_hash      VARCHAR(64),                    -- SHA256(token_key), cache miss 时兜底, 便于离线回填
    -- 调用上下文
    model               VARCHAR(128) NOT NULL,
    endpoint            VARCHAR(64)  NOT NULL,          -- chat/completions|completions|responses|embeddings|moderations
    is_stream           BOOLEAN      NOT NULL DEFAULT FALSE,  -- 从请求 body 的 stream 字段判定
    -- 内容(审计核心)
    prompt_text         JSONB        NOT NULL,          -- messages / input
    completion_text     JSONB        NOT NULL DEFAULT '{}'::jsonb,  -- 聚合后 choices / output
    tool_calls          JSONB,                          -- function calling 调用链, 独立列便于查询
    request_body_hash   VARCHAR(64),                    -- SHA256(规范化 prompt), 相同 prompt 去重统计(可省 30%+ 存储)
    -- 计量与状态(冗余, 非权威; 权威仍是 New API logs)
    http_status         INTEGER      NOT NULL,
    prompt_tokens       INTEGER,
    completion_tokens   INTEGER,
    error_message       TEXT,
    client_ip           VARCHAR(64),
    redacted            BOOLEAN      NOT NULL DEFAULT FALSE,
    truncated           BOOLEAN      NOT NULL DEFAULT FALSE,
    -- 性能观测
    upstream_latency_ms INTEGER,
    total_latency_ms    INTEGER,
    -- 演进兼容
    version             SMALLINT     NOT NULL DEFAULT 1, -- schema 版本, Phase2 pgvector/新字段向前兼容
    created_at          TIMESTAMPTZ  NOT NULL DEFAULT now()
);
CREATE INDEX idx_llm_conv_created   ON llm_conversation (created_at);
CREATE INDEX idx_llm_conv_model_ct  ON llm_conversation (model, created_at);
CREATE INDEX idx_llm_conv_caller_ct ON llm_conversation (caller_tag, created_at);
CREATE INDEX idx_llm_conv_hash      ON llm_conversation (request_body_hash);
```

**设计要点**:
- `request_id` UNIQUE:网关重试天然幂等(避免重复落库),也是多实例安全基础。
- **不存 quota**:权威在 New API,避免双写不一致;需要时按 `request_id` 关联查询。
- `JsonB`:结构化对话天然;Phase2 向量化直接从 JSON 抽文本。
- `caller_tag/caller_user_id/caller_group`:反查 token 表填充;**`caller_user_id` 还用于行级权限过滤**(见 §9),防跨 agent 越权。
- `token_key_hash`:cache miss 时兜底记录,便于离线回填 caller(见 §5.4)。
- `tool_calls`:function calling 时代必备,独立列比埋在 completion_text 里更易查询。
- `request_body_hash`:相同 prompt 去重统计,可显著降低存储(尤其系统 prompt 重复率高的场景)。
- `version`:schema 版本号,为 Phase2 向量化等新字段做向前兼容。
- `upstream_latency_ms/total_latency_ms`:网关是观测全公司 LLM 延迟的唯一位置。
- 索引克制:`created_at` / `model+created_at` / `caller_tag+created_at` / `request_body_hash` / `request_id`(UNIQUE)。
- **protocol 列已移除**:Phase 1 不处理 WS(见 §0),endpoint 列已足够区分。
- `is_stream` 判定:从**请求 body 的 `stream` 字段**读取,不从响应头推断(避免响应到达后才分支)。

### 4.3 sqlc 查询(幂等去重 + 多实例安全)

```sql
-- queries/conversation.sql
-- name: InsertConversation :one
INSERT INTO llm_conversation (request_id, upstream_request_id, caller_tag, caller_user_id,
  caller_group, token_key_hash, model, endpoint, is_stream, prompt_text, completion_text,
  tool_calls, request_body_hash, http_status, prompt_tokens, completion_tokens,
  error_message, client_ip, redacted, truncated, upstream_latency_ms, total_latency_ms, version)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23)
ON CONFLICT (request_id) DO NOTHING
RETURNING *;

-- name: DeleteOlderThan :execrows
DELETE FROM llm_conversation WHERE created_at < $1;

-- name: BackfillCallerByTokenHash :execrows
UPDATE llm_conversation SET caller_tag=$2, caller_user_id=$3, caller_group=$4
WHERE token_key_hash=$1 AND caller_tag IS NULL;

-- TTL 多实例安全: advisory lock 防重复扫表
-- name: TryAcquireTtlLock :one
SELECT pg_try_advisory_lock($1) AS ok;
-- name: ReleaseTtlLock :exec
SELECT pg_advisory_unlock($1);
```

### 4.4 token 映射(只读查 new-api 库)

```sql
-- queries/token_map.sql (连 new-api 库执行)
-- name: LoadTokenMap :many
SELECT key, name, user_id, "group" FROM tokens WHERE deleted_at IS NULL AND status = 1;
```

---

## 5. 网关核心实现(最佳实践骨架)

### 5.1 Transport 层(限流→捕获还原→熔断转发)+ request_id 关联设计

`internal/gateway/transport.go`。

#### 5.1.1 request_id 关联设计(M1 修正,核心)

**问题**:New API 的 `RequestId()` 中间件**无条件自生成** request_id,忽略客户端传入(见 §1 证据)。因此网关不能假设"自己生成的 id 能对上 New API 的 log"。

**解决方案**:两个 id 分工,通过**注入请求头 + 读响应头**完成关联:

| id | 来源 | 用途 | 何时获得 |
|---|---|---|---|
| `gateway_id`(→`request_id` 列,UNIQUE 幂等键) | **网关自生成**,请求进入时注入请求头 `X-Ctx-Gateway-Id` | 网关内全程追踪、幂等去重、与客户端联调 | 请求阶段(立即有) |
| `X-Oneapi-Request-Id`(→`upstream_request_id` 列) | **New API 响应头**回传(`request-id.go:16`) | 关联 New API `logs` 表,跨库审计 | 响应阶段(拿到 response header 后) |

**record 生命周期**(时序修正):
1. **请求阶段**(`preHandler`/Transport 入口):生成 `gateway_id` → 注入请求头 → 建立 record 骨架(含 gateway_id、prompt、元数据),**暂不推 channel**。
2. **转发**:`base.RoundTrip` → 拿到 `*http.Response`。
3. **响应阶段**:从 `resp.Header.Get("X-Oneapi-Request-Id")` 读 upstream id → 填入 record → 捕获 completion(非流式整体 / 流式聚合)→ **此时才推 channel 落库**。
4. `X-Ctx-Gateway-Id` 请求头透传给 New API(New API 不识别也无害,但 New API 管理员可在其日志/请求列表里看到该头,便于反向联调)。

> 这样 channel 推送只延迟到响应结束,而请求处理耗时通常远小于落库耗时,对背压影响可忽略。

#### 5.1.2 数据流(K3,gzip/br 完整处理)

```
r.Body(原始压缩字节)
   │
   ├─► io.ReadAll → rawBuf                       [硬上限: preBodyMaxBytes, 防解压炸弹]
   │      │
   │      ├─► decodeMaybe(rawBuf, Content-Encoding)  [gzip→compress/gzip, br→andybalholm/brotli]
   │      │      ├─ 成功 → decodedBuf              [硬上限: postBodyMaxBytes, truncated=true 截断]
   │      │      │        └─► rec.Prompt = decodedBuf   [存进 record 骨架, 待响应阶段一起推 channel]
   │      │      └─ 失败 → metrics.DecodeFailed.Inc() + slog(无 prompt 原文)   [C6: 不静默]
   │      │
   │      └─► 还原 r.Body = NopCloser(bytes.NewReader(rawBuf))   [原始压缩字节透传给 New API]
   │             r.ContentLength = len(rawBuf)
   │             r.GetBody = ...                                  [ReverseProxy 重试用]
   │             [Content-Encoding 头保留: New API 自行解压]
   │
   └─► base.RoundTrip(r)  → New API
```

**要点**:
- **白名单只含 A/B 类端点**(C3 修正):`chat/completions, completions, responses, embeddings, moderations`。**等级 C(images/audio,multipart 二进制)和 D(models/rerank/realtime)直接 bypass 捕获逻辑**——multipart 既不是 JSON 也无法用 `decodeMaybe` 处理,捕获它只会出错。C 类的"记元数据+大小"延后到 v1.1 单独实现。
- **透传的是原始压缩字节**(`rawBuf`),New API 期待 `Content-Encoding: gzip`,网关原样转,零兼容风险。
- **捕获的是解压后的 `decodedBuf`**,落库人类可读的 prompt。
- 两处硬上限:`preBodyMaxBytes`(解压前,防解压炸弹,默认 32MB)、`postBodyMaxBytes`(解压后,对齐 `LLM_AUDIT_MAX_BODY_BYTES=64KB`)。
- `GetBody` 必须重设:`httputil.ReverseProxy` 在重试路径会重新读 body。

#### 5.1.3 代码骨架

```go
type captureTransport struct {
    base       http.RoundTripper
    breaker    *gobreaker.CircuitBreaker   // 只统计"建连+首字节"失败(见 §5.6 M2)
    limiters   *TokenLimiterPool           // burst/rate 分离 + 匿名桶
    capturer   *audit.Capturer             // 推 channel
    authCache  *tokenKeyCache              // 只缓存 hash, 不缓存原文 key(M3)
}

func (t *captureTransport) RoundTrip(r *http.Request) (*http.Response, error) {
    // 0. 注入 gateway_id(M1)
    gwID := r.Header.Get("X-Ctx-Gateway-Id")        // preHandler 已生成并注入
    rec := audit.NewRecord(gwID, r)                  // 骨架: gateway_id + 元数据, prompt 待填

    // 1. 限流(按 caller; 在熔断之外, 拒绝不计入熔断统计)
    if !t.limiters.Allow(t.authCache.tokenHashOf(r)) {
        return nil, errRateLimited                   // 429 由上层 handler 包装
    }

    // 2. 请求 body 捕获 + 还原(仅 A/B 类白名单端点; C3)
    if isCaptureEndpoint(r.URL.Path) && r.Body != nil && r.ContentLength != 0 {
        raw, err := readBounded(r.Body, preBodyMaxBytes)
        r.Body.Close()
        if err != nil { return nil, err }
        decoded, trunc, derr := decodeMaybe(raw, r.Header.Get("Content-Encoding"), postBodyMaxBytes)
        if derr != nil {
            metrics.DecodeFailed.Inc()               // C6: 解压失败不静默
        } else if decoded != nil {
            rec.SetPrompt(decoded, trunc)            // 暂存进 record, 不推 channel
        }
        restoreBody(r, raw)                          // 原字节还原
    }

    // 3. 熔断转发(M2: 只覆盖建连+首字节阶段)
    resp, err := t.breaker.Execute(func() (*http.Response, error) {
        return t.base.RoundTrip(r)
    })
    if err != nil { return nil, err }                // 熔断 open / 网络错误, 不落库或落骨架

    // 4. 响应阶段: 读 New API 的 upstream id + 把 record 交给响应处理(M1)
    rec.UpstreamRequestID = resp.Header.Get("X-Oneapi-Request-Id")
    resp.Body = t.capturer.WrapResponseBody(resp.Body, rec, isStream(r))  // 非流式: 读完后推 channel; 流式: 装入 §5.2 循环
    return resp, nil
}

func restoreBody(r *http.Request, raw []byte) {
    r.Body = io.NopCloser(bytes.NewReader(raw))
    r.ContentLength = int64(len(raw))
    r.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(raw)), nil }
}
```

> **注意**:`WrapResponseBody` 是关键——它把 record 对象"挂"到响应流上,使得**非流式**在 `io.ReadAll(resp.Body)` 完成时、**流式**在 SSE 循环(§5.2)结束时,都能拿到完整 completion 并推 channel。这是 M1 时序修正的落地方式。

### 5.2 SSE 单 goroutine 尽力捕获(K1 修正)

`internal/gateway/sse_loop.go`(原草案名 `sse_tee.go`,因抛弃 TeeReader 已改名)。**抛弃 `io.TeeReader + io.Pipe`**——该模式有个隐患:`TeeReader` 在 writer 返回 error 时会把 error 传回 reader,导致**捕获侧一旦触发截断,客户端流也跟着断**。改用**单 goroutine 尽力捕获**,转发与捕获在同一个循环里,捕获永不返回 error,彻底解耦。

```go
// is_stream=true 的流式响应: 边透传给客户端, 边旁路累积进 rec, 单 goroutine 零耦合
// rec 进入时已含 gateway_id + prompt + upstream_request_id(§5.1 响应阶段已填); 本循环结束后推 channel(M1)
func sseCaptureLoop(upstream io.ReadCloser, w http.ResponseWriter, rec *audit.Record, push func(*audit.Record)) {
    flusher, _ := w.(http.Flusher)
    defer upstream.Close()

    buf := make([]byte, 32*1024)
    for {
        n, rerr := upstream.Read(buf)
        if n > 0 {
            // (a) 优先转发给客户端; 客户端断开则停止
            if _, werr := w.Write(buf[:n]); werr != nil {
                metrics.StreamClientGone.Inc()       // M2: 客户端断连统计(不进熔断, 单独指标)
                break
            }
            if flusher != nil { flusher.Flush() }            // 立即 flush, 零延迟
            // (b) 尽力累积进 rec; 永不返回 error, 不影响 (a)
            rec.AppendCapture(buf[:n], postBodyMaxBytes)     // 内部有界, 满后静默丢弃后续 chunk
        }
        if rerr != nil {
            if rerr != io.EOF { metrics.StreamInterrupted.Inc() }  // M2: 流中途断开统计
            break
        }
    }
    rec.Finalize()                                            // 识别 [DONE] / finish_reason / usage, 组装 completion
    push(rec)                                                 // 推 channel 落库(M1: 响应阶段才推)
}
```

**`Record.AppendCapture` 内部的 SSE 解析职责**(`internal/audit/sse_aggregator.go`):
- 按行扫描 `data: ` 前缀,累积到 `SSEBuf`(有界)。
- 识别哨兵 `data: [DONE]` → 标记流结束。
- 累积每个 chunk 的 `delta.content` / `delta.reasoning_content` → 拼成完整 `completion_text`。
- 捕获最后 chunk 的 `usage.prompt_tokens / completion_tokens`。
- 捕获 `choices[0].finish_reason`。
- 提取 `choices[0].delta.tool_calls` → 拼成 `tool_calls` 数组。
- 超过 `postBodyMaxBytes` 后停止累积(避免 OOM),但仍标记 `truncated=true`,**不停止转发**。

**`boundedWriter` 防 OOM**(对照 `LLM_AUDIT_MAX_BODY_BYTES` 实施硬截断):
```go
type boundedWriter struct{ buf *bytes.Buffer; max int; truncated bool }
func (b *boundedWriter) Write(p []byte) (int, error) {
    if b.buf.Len()+len(p) > b.max { b.truncated = true; return len(p), nil } // 静默丢弃, 不返 error
    return b.buf.Write(p)
}
```

### 5.3 异步落库 + 分级背压降级(I4 修正 + M1 时序 + C4 hook 接入点)

`internal/audit/pipeline.go`。

**时序(M1)**:record 在**响应阶段**才推 channel(此时 prompt+completion+upstream_id 都已齐),不再在请求方向推送。

**分级背压(I4)**:审计系统的价值在"出事时不丢数据",而故障时刻恰恰是 channel 最可能满的时刻。用**双 channel**保证元数据必保、完整内容尽量保:

```go
var (
    fullCh  = make(chan *Record, CAPTURE_CHANNEL_SIZE)    // 完整记录(prompt+completion), 可丢
    metaCh  = make(chan *RecordMeta, CAPTURE_CHANNEL_SIZE*4) // 仅元数据(无 prompt/completion), 必保
)

// push(响应阶段调用): 优先推 full, 满则降级推 meta, 都满才彻底丢
func push(rec *Record) {
    select {
    case fullCh <- rec:
        metrics.CaptureOutcome.WithLabelValues("full").Inc()
        return
    default:
    }
    select {
    case metaCh <- rec.StripContent():                    // 降级: 保留 gateway_id/upstream_id/endpoint/model/status/tokens/时间, 丢弃原文
        metrics.CaptureOutcome.WithLabelValues("metadata-only").Inc()
        rateLimitedWarn("full channel full, degraded to metadata-only")
    default:
        metrics.CaptureOutcome.WithLabelValues("dropped").Inc()  // 最坏: 全丢, 但元数据 channel 也满意味着严重过载
        rateLimitedWarn("both channels full, record dropped")
    }
}

// worker pool 消费两个 channel(背压吸收, 不拖垮热路径)
for rec := range fullCh {
    persist(rec)                                          // 含完整内容
}
for meta := range metaCh {
    persist(meta)                                         // completion_text=NULL, 但调用链可追溯
}

func persist(rec persistable) {
    enrichCaller(rec)                                     // 反查 token 映射(见 §5.4)
    redact.Apply(rec)                                     // 脱敏
    truncate(rec)
    start := time.Now()
    _, err := queries.InsertConversation(ctx, rec)        // ON CONFLICT(request_id) DO NOTHING 幂等
    metrics.DBInsertDuration.Observe(time.Since(start).Seconds())
    if err != nil {
        metrics.DBInsertErrors.Inc()
        rateLimitedWarn("insert failed", "err", err)
    }
    // C4: Phase2 接入点 — 在此处调用 hooks.OnConversationComplete(rec)
    // Phase1 不调用(空实现), 保持热路径纯净
    hooks.OnConversationComplete(rec)
}

// 日志速率限制(防热路径 slog 风暴): 简单令牌桶, 1s 最多 N 条
func rateLimitedWarn(msg string, args ...any) {
    if logLimiter.Allow() { slog.Warn(msg, args...) }
}
```

**告警规则**(Prometheus):
- `rate(gateway_capture_outcome_total{outcome="dropped"}[5m]) > 0` → page(彻底丢, 严重)
- `rate(gateway_capture_outcome_total{outcome="metadata-only"}[5m]) > 0` → warn(降级, 需关注)

### 5.4 caller 反查(I3 修正)

`internal/audit/caller_cache.go`。**修正**:启动期 cache 空、60s 窗口内新 token 无标识、cache 加载失败静默退化——都是真问题。补:指标观测 + token_key_hash 兜底 + 失败告警 + 离线回填。

```go
// 启动从 new-api 库只读拉全量 key→{name,user_id,group}
// 拉到后立即对 key 求 sha256, 内存里只存 hash→CallerInfo(M3: 绝不缓存原文 sk-xxx)
// 每 CALLER_CACHE_REFRESH_SEC(默认60s) 全量刷新(sync.RWMutex 保护)
type CallerCache struct {
    m        sync.RWMutex
    data     map[string]CallerInfo        // key = sha256(token_key), 非原文
    ready    atomic.Bool                   // 启动后首次加载完成才 true
}

type CallerInfo struct {
    Tag    string   // token.Name
    UserID int      // token.UserId
    Group  string   // token.Group
}

func (c *CallerCache) LookupByHash(hash string) (CallerInfo, bool) {
    c.m.RLock(); defer c.m.RUnlock()
    info, ok := c.data[hash]
    return info, ok
}
```

**enrichCaller 的健壮性策略**(M3 修正:不接触原文 key):
```go
func enrichCaller(rec *Record) {
    // rec.TokenKeyHash 在请求阶段(§5.1)就已由 authCache 计算并填入, enrichCaller 只做反查
    // authCache 只缓存 "解析出的 hash", 绝不在内存常驻原文 sk-xxx(M3)
    if info, ok := callerCache.LookupByHash(rec.TokenKeyHash); ok {
        metrics.CallerLookup.WithLabelValues("hit").Inc()
        rec.CallerTag, rec.CallerUserID, rec.CallerGroup = info.Tag, info.UserID, info.Group
    } else {
        metrics.CallerLookup.WithLabelValues("miss").Inc()
        // caller_tag 留 NULL, 但 token_key_hash 已存 → 后续可回填
    }
}
```

**M3 安全约束**:`tokenKeyCache`(§5.1 `authCache`)只在**单次请求处理期间**短暂持有从 `Authorization` 头解析出的原文 key(用于算 hash 后立即丢弃),**绝不缓存原文进 map**。缓存的 key 是 `sha256(rawKey)`,值是解析结果。这样即使 core dump,内存里也没有全公司明文 token。

**回填机制**:cache 刷新拉到新 token 时,执行 `BackfillCallerByTokenHash`(见 §4.3)把历史 `token_key_hash` 匹配但 `caller_tag IS NULL` 的记录补上。这样:
- 启动期/cache miss 的请求不会永久丢失 caller 信息。
- cache 连续刷新失败时:`CallerLookup{result="error"}` 计数飙升 → 告警,**不静默退化**。

**新增指标**:
```
caller_lookup_total{result="hit|miss|error"}
caller_cache_last_refresh_success_timestamp  (Prometheus 探测连续失败用)
```

### 5.5 TTL 多实例安全

`internal/cleanup/ttl.go`:

```go
func Run(ctx context.Context) {
    t := time.NewTicker(1 * time.Hour)
    for {
        select {
        case <-ctx.Done(): return
        case <-t.C:
        }
        if ok, _ := queries.TryAcquireTtlLock(ctx, ttlLockID); !ok {
            continue                       // 其他实例已在清理, 跳过
        }
        queries.DeleteOlderThan(ctx, time.Now().AddDate(0, 0, -ttlDays))
        queries.ReleaseTtlLock(ctx, ttlLockID)
    }
}
```

### 5.6 熔断器配置(只统计"建连+首字节"失败,M2)

`internal/gateway/breaker.go`。

**M2 关键取舍**:熔断器包裹的是 `base.RoundTrip`,它在**拿到 response header**(建连+首字节成功)时就返回。但 SSE 流的实际消费(§5.2 的循环)发生在 `RoundTrip` 返回**之后**——所以**流中途断开的失败不会被熔断器统计**。这是 httputil.ReverseProxy + body capture 模式的固有时序,无法让熔断同时覆盖"建连失败"和"流中断"。

**设计决策**:
- 熔断**只负责保护"建连 + 首字节"阶段**:New API 完全不可达 / TLS 失败 / 连续 5xx 建连错误 → 熔断,防止雪崩。
- **流中断失败走独立指标**:`gateway_stream_interrupted_total`、`gateway_stream_client_gone_total`(见 §5.2),不进熔断。理由:流中断往往是上游模型本身或客户端主动断开,熔断无意义。
- 限流拒绝、解压失败、捕获失败都**不进**熔断统计。

```go
cb := gobreaker.NewCircuitBreaker(gobreaker.Settings{
    Name:        "new-api",
    MaxRequests: 5,                        // 半开状态试探数
    Interval:    60 * time.Second,
    Timeout:     30 * time.Second,         // 开→半开等待
    ReadyToTrip: func(c gobreaker.Counts) bool {
        return c.ConsecutiveFailures > 5   // 连续5次建连失败→熔断
    },
    OnStateChange: func(name string, from, to gobreaker.State) {
        slog.Warn("breaker state change", "name", name, "from", from, "to", to)
        metrics.BreakerState.Set(stateToGauge(to))   // 0=closed 1=half 2=open
    },
    IsSuccessful: func(err error) bool {
        // 限流错误不算 New API 失败
        return !errors.Is(err, errRateLimited)
    },
})
```

### 5.7 Prometheus metrics(补全 + M2/C5/C6)

`internal/metrics/metrics.go`。

**C5 标签基数约束**:Prometheus 忌讳高基数 label。`caller_tag`(token.Name,可能几百上千个)**不进 label**,改用基数可控的 `caller_user_id`(用户数通常远小于 token 数)。若仍需按 token 维度看,走日志/查询 API,不走 metrics label。

```
# 流量与延迟
gateway_request_total{endpoint,model,caller_user_id,status}   # C5: 用 user_id 不用 caller_tag
gateway_request_duration_seconds(histogram, P50/P95/P99)      # 含注入延迟(Phase3)
gateway_upstream_duration_seconds(histogram)
gateway_upstream_first_byte_seconds(histogram)                # SSE 首 token 延迟

# 捕获与落库(I4 分级 + C6)
gateway_capture_outcome_total{outcome="full|metadata-only|dropped"}  # 替代原 capture_dropped
gateway_capture_channel_depth{channel="full|meta"} (gauge)
gateway_db_insert_duration_seconds(histogram)
gateway_db_insert_errors_total
gateway_decode_failed_total                                   # C6: 请求体解压失败

# 流式健康(M2, 独立于熔断)
gateway_stream_interrupted_total                              # 上游流中途断开
gateway_stream_client_gone_total                              # 客户端主动断开

# caller 反查
caller_lookup_total{result="hit|miss|error"}
caller_cache_last_refresh_success_timestamp

# 过载保护
gateway_breaker_state(gauge, 0=closed 1=half 2=open)
gateway_ratelimit_rejected_total{bucket="known|anon"}

# 安全(见 §9)
gateway_redact_applied_total{rule}
gateway_auth_rejected_total{endpoint="metrics|stats"}
```

### 5.8 优雅停机(含时间预算)

`cmd/gateway/main.go`。**修正**:明确 shutdown timeout,避免 PG 慢时排空卡死。

```go
go srv.ListenAndServe()
<-ctx.Done()                                              // SIGTERM/SIGINT

// 阶段1: 停止接收新请求
shutdownCtx, cancel := context.WithTimeout(context.Background(), SHUTDOWN_TIMEOUT) // 默认 30s
defer cancel()
srv.Shutdown(shutdownCtx)                                 // 排空在途连接

// 阶段2: 排空 capture channel(残留 record 不丢)
close(captureCh)
drained := make(chan struct{})
go func() { wg.Wait(); close(drained) }()
select {
case <-drained:                                           // 正常排空
case <-time.After(DRAIN_TIMEOUT):                         // 默认 20s, PG 太慢则强制退出
    slog.Error("drain timeout, forcing exit with residual records")
}
```

---

## 6. 配置 / 部署

### 6.1 配置(env 优先)

```env
# .env.example
LISTEN_ADDR=:8080
NEW_API_BASE_URL=http://newapi.internal:3000

# 双库
CONTEXT_DB_URL=postgres://ctx:***@pg:5432/context_repo?sslmode=disable
NEWAPI_DB_URL=postgres://ro:***@pg:5432/new-api?sslmode=disable   # 只读

# 审计
LLM_AUDIT_MODE=redact                         # full | redact | off
LLM_AUDIT_EXCLUDE_MODELS=
LLM_AUDIT_CAPTURE_ENDPOINTS=chat/completions,completions,responses,embeddings,moderations
LLM_AUDIT_MAX_BODY_BYTES=65536                # 解压后单条上限
LLM_AUDIT_PRE_BODY_MAX_BYTES=33554432         # 解压前上限(对齐 New API 32MB, 防解压炸弹)
LLM_AUDIT_TTL_DAYS=90                         # 需法务/安全评审确认

# 过载保护
BREAKER_FAILURES=5
RATE_PER_CALLER=50                            # 稳态 req/s per Bearer token
RATE_BURST=100                                # 突发桶大小
RATE_ANON=10                                  # 无 Authorization 的匿名桶

# 性能
CAPTURE_CHANNEL_SIZE=4096
DB_MAX_OPEN_CONNS=25                          # 必须 ≥ WORKER_POOL_SIZE
WORKER_POOL_SIZE=8
SHUTDOWN_TIMEOUT_SEC=30
DRAIN_TIMEOUT_SEC=20

# caller 反查
CALLER_CACHE_REFRESH_SEC=60

# 安全(见 §9; C2: 同端口路由+鉴权)
METRICS_AUTH_TOKEN=                            # /metrics 的 bearer, 空则仅内网 IP 可访问管理端点
ADMIN_AUTH_TOKEN=                              # /ctx/* 的 bearer
SLOG_PROMPT_FIELDS_BLOCKLIST=request.body,response.body,prompt_text,completion_text
```

> **启动期配置校验**(S6):`envconfig` 读完后 `Validate()`:LLM_AUDIT_MODE ∈ {full,redact,off};`DB_MAX_OPEN_CONNS ≥ WORKER_POOL_SIZE`;`LLM_AUDIT_PRE_BODY_MAX_BYTES > LLM_AUDIT_MAX_BODY_BYTES`;非法直接拒绝启动。

### 6.2 流量切换(用户无感)

```
原: agent → newapi.company.com → New API
改: agent → newapi.company.com → [LLM Gateway] → New API(内网)
```

- DNS/反代把 `newapi.company.com` 指网关,upstream 指 New API 内网。agent base_url 不变 → **真·零改造**。
- 回滚:DNS 切回直连。

### 6.3 部署

- 单二进制:`make build` → `llm-gateway`(distroless 镜像 <20MB)。
- systemd unit 或 k8s Deployment。预留多实例时无代码改动(advisory lock + request_id 去重已就位)。

---

## 7. 项目结构

在 `D:\GitHubProjects\NewApiLog` 下创建:

```
NewApiLog/
├── go.mod / go.sum / Makefile / Dockerfile
├── cmd/gateway/main.go                  # 启动: config 校验→server→graceful
├── internal/
│   ├── config/
│   │   ├── config.go                    # envconfig
│   │   └── validate.go                 # 启动期范围校验(S6)
│   ├── gateway/
│   │   ├── transport.go                 # captureTransport.Forward: 限流→捕获还原→熔断(M1 核心)
│   │   ├── proxy.go                     # 自定义 handler: 分流式/非流式响应捕获
│   │   ├── resp_wrap.go                 # sseCaptureLoop: 单 goroutine 尽力捕获(K1, M2 流中断统计)
│   │   ├── body_snapshot.go             # body 读取+解压+还原(K2/K3, C6 decode失败 metric)
│   │   ├── breaker.go                   # gobreaker(M2: 只覆盖建连+首字节)
│   │   ├── ratelimit.go                 # 令牌桶池(已知token桶/匿名桶, burst/rate 分离)
│   │   ├── auth_cache.go                # 解析 Authorization→token hash, 不缓存原文(M3)
│   │   ├── hash.go                      # sha256Hex + gateway_id 生成
│   │   ├── ws_proxy.go                  # [Phase2] /v1/realtime WS, Phase1 仅 passthrough stub
│   │   └── handler.go                   # 路由分发(mux: /metrics, /ctx/*, /v1/realtime, /v1/*)
│   ├── audit/
│   │   ├── record.go                    # Record 结构 + SetPrompt/Finalize/StripContent(I4 降级)
│   │   ├── pipeline.go                  # 双 channel(full/meta) worker pool + 分级背压(I4)
│   │   ├── caller_cache.go              # hash→CallerInfo, 反查+指标+回填(I3, M3)
│   │   ├── redact.go                    # 脱敏(规则见 §9)
│   │   ├── sse_aggregator.go            # chunk→完整文本([DONE]/usage/tool_calls)
│   │   ├── bounded.go                   # boundedWriter 防 OOM
│   │   └── hash.go                      # sha256Hex 共用
│   ├── db/
│   │   ├── store.go                     # 沉淀库读写(手写薄封装, 镜像 queries/*.sql 语义)
│   │   ├── tokenreader.go               # new-api 库只读, 反查 token 映射
│   │   ├── migrations/*.sql             # goose
│   │   ├── queries/*.sql                # sqlc 输入(Phase1 被 store.go 镜像, sqlc 可选)
│   │   └── gen/                         # [可选] sqlc 生成, `make sqlc`
│   ├── hooks/
│   │   └── hooks.go                     # OnConversationComplete / OnRequestRewrite (Phase1 no-op)
│   ├── security/
│   │   ├── auth_middleware.go           # /metrics /ctx/* bearer + 内网限制(C2/I6)
│   │   └── slog_filter.go              # 字段黑名单防日志泄密(I6)
│   ├── metrics/metrics.go               # prometheus 全部指标(§5.7)
│   ├── query/stats_handler.go           # GET /ctx/stats
│   └── cleanup/ttl.go                   # advisory lock TTL
├── test/                                # [Phase2] 集成测试(testcontainers/pg), Phase1 用 co-located *_test.go
├── .env.example / README.md
└── DESIGN.md                            # 本文件
```

> **与初版差异说明**:
> - `hooks/on_complete.go` + `on_rewrite.go` → 合并为 `hooks.go`(内容一致, 减少文件数)
> - `audit/store.go` → 移至 `db/store.go`(避免 audit↔db 循环依赖, store 直接操作 pgxpool)
> - `gateway/sse_loop.go` → 实现于 `resp_wrap.go`(函数名 `sseCaptureLoop` 不变)
> - `test/unit/` → co-located `*_test.go`(Go 惯例, 非独立目录)
> - `db/gen/` → `make sqlc` 生成, Phase1 未生成(sqlc 工具链可选)

---

## 8. 验收清单

### 8.1 Phase 1 功能验收

1. `make build && make migrate-up && ./llm-gateway` 起服务。
2. **HTTP 非流式**:curl `POST /v1/chat/completions` → 响应正常 → PG 有 1 条完整记录,`caller_tag` 来自反查 token。
3. **SSE 流式**:`stream:true` → 逐字输出无延迟 → 流结束 PG 有 1 条聚合记录(非多条),含 `tool_calls`/`usage`。
4. **gzip/br 请求体**:`curl --compressed` → 网关解压捕获正确,**原始压缩字节透传给 New API**,New API 正常处理。
5. **错误透传**:非法 model → 客户端拿原样错误 → PG 记 `error_message`、`completion_text={}`。
6. **端点白名单**:`POST /v1/images/generations`(等级 C)→ 透传成功但 PG 无 body 记录;`GET /v1/models`(等级 D)→ 纯透传不落库。
7. **request_id 关联(M1)**:PG 中 `upstream_request_id` 列 = New API 响应头 `X-Oneapi-Request-Id` = New API 后台 logs 表的 `request_id`,三者一致;PG `request_id` 列 = 网关自生成的 `X-Ctx-Gateway-Id`(可在 New API 请求列表的请求头里反向查到)。
8. **脱敏**:含手机号/邮箱/身份证 → PG 已脱敏、`redacted=true`。
9. **熔断**:模拟 New API 连续失败 → 熔断开启 → 客户端快速失败,不再打 New API;限流拒绝**不触发**熔断。
10. **限流**:单 token 超 `RATE_PER_CALLER`/`RATE_BURST` → 429;无 Authorization 走匿名桶。
11. **caller 反查健壮性**:新建 token 60s 内请求 → `caller_tag` 暂空但 `token_key_hash` 已存 → cache 刷新后回填成功。
12. **TTL**:记录改 91 天前 → 清理后删除。
13. **多实例 TTL**:两实例同时跑 → advisory lock 保证只有一个执行清理。
14. **优雅停机**:SIGTERM → 30s 内排空 record 后退出,无丢失;超时则记 error 强制退出。
15. **白名单 bypass(C3)**:`POST /v1/images/generations`(C 类)、`GET /v1/models`(D 类)→ 纯透传,捕获逻辑不触发(不进 `isCaptureEndpoint` 分支),无解压/落库动作。
16. **分级降级(I4)**:填满 full channel 后,新 record 降级为 metadata-only 入 meta channel(无 prompt/completion 但有元数据);PG 中可见 `completion_text IS NULL` 的降级记录。
17. **流中断不熔断(M2)**:SSE 流中途断开 → `gateway_stream_interrupted_total` +1,但熔断器状态**不变**(仍 closed);只有连续建连失败才熔断。
18. **解压失败可见(C6)**:发一个声明 `Content-Encoding: gzip` 但 body 损坏的请求 → `gateway_decode_failed_total` +1,坏 body 原样透传给 New API(New API 报错),不静默。

### 8.2 安全验收(I6)

19. **管理端点鉴权**:`GET /metrics`、`/ctx/stats` 无 bearer → 401;正确 bearer → 200。
20. **slog 黑名单**:触发任何错误日志 → 日志中**不含** prompt/completion/request body 原文。
21. **行级权限**:`GET /ctx/stats` 按 caller 隔离,agent A 看不到 agent B 的统计。

### 8.3 性能验收(仅 Phase 1)

22. **延迟基准(Phase 1)**:wrk 压测,**纯代理模式**(不含 Phase 3 注入),网关 vs 直连 P99 延迟增量 < 1ms,SSE 首 token 延迟增量 < 1ms。
    > ⚠️ 此项**仅适用 Phase 1**。Phase 3 引入向量检索+注入后,延迟预算另行定义(见 §8.4)。

### 8.4 Phase 3 延迟预算(届时验收,I5)

Phase 3 `OnRequestRewrite` 是**同步热路径**操作,必然增加首 token 延迟,无法做到 < 1ms。届时需满足:
- **缓存命中路径** < 5ms(意图→上下文 LRU 缓存)。
- **缓存未命中路径**(pgvector HNSW 检索 + 拼装)< 50ms。
- 提供**旁路开关**:超时/失败时跳过注入,降级为 Phase 1 行为,绝不阻塞请求。
- 单列验收项,与 Phase 1 的 < 1ms 不冲突。

---

## 9. 安全 / 合规设计(I6)

> 章节层级修正(C1):原 §8.5 夹在验收清单与路线图之间,层级混乱。安全/合规是**设计章节**而非验收项,提升为独立 §9。相关验收项保留在 §8.2。

审计系统存储全公司 LLM 对话原文(含 PII),本身是高敏感系统,安全设计必须先行。

| 维度 | 风险 | 措施 |
|---|---|---|
| **管理端点鉴权** | `/metrics`、`/ctx/stats` 无认证,内网任意人可拉取 | **同端口路由 + bearer 鉴权**(C2):网关只 bind 一个 `LISTEN_ADDR`,`/metrics`、`/ctx/*` 强制校验 `METRICS_AUTH_TOKEN`/`ADMIN_AUTH_TOKEN`;业务 `/v1/*` 透传不校验(交给 New API)。token 空则拒绝非内网 IP 访问管理端点。生产建议前置 mTLS |
| **脱敏规则** | "脱敏"未定义=不知道脱什么 | 明确规则集(见下表),`LLM_AUDIT_MODE=redact` 时强制应用,记录 `gateway_redact_applied_total{rule}` |
| **slog 字段黑名单** | 错误日志打印 prompt/completion 造成二次泄露 | `slog_filter.go` 拦截,`SLOG_PROMPT_FIELDS_BLOCKLIST` 列黑名单,违规字段替换为 `<redacted>` |
| **留存期合规** | 90 天无法律依据 | §0.2 已标注:上线前法务/安全评审按数据分级政策确认;按模型/业务可差异化 TTL |
| **PII 落盘** | 普通盘泄露风险 | PG 列加密(`pgcrypto`)对 `prompt_text`/`completion_text`;或表空间加密;Phase1 先依赖磁盘加密 + 严格访问控制,列加密列入 v1.1 |
| **跨 agent 越权** | agent A 能查 agent B 的对话 | `caller_user_id` 做行级过滤;`/ctx/*` 查询强制带 caller 作用域 |
| **token_key 不落盘明文** | sk-xxx 泄露 = 全公司 key 泄露 | 只存 `token_key_hash`(SHA256),**绝不**存原始 key |

**脱敏规则明细**(`internal/audit/redact.go`):

| 规则 | 正则 | 替换 |
|---|---|---|
| 手机号 | `\b1[3-9]\d{9}\b` | 前3后4,如 `138****1234` |
| 身份证 | `\b\d{17}[\dXx]\b` | 前6后4,中间 `*` |
| 邮箱 | `\b[\w.]+@[\w.]+\b` | 首字符+`***@域名` |
| 银行卡 | `\b\d{16,19}\b` | 后4位 |
| API key | `\bsk-[A-Za-z0-9]{20,}\b` | `sk-***` |
| 自定义 | 配置文件 `redact.rules` 扩展 | — |

> **脱敏只在落库前应用**,透传给 New API 的是原始内容(否则模型行为会变)。`LLM_AUDIT_MODE=full` 时跳过脱敏(仅限受控环境)。

---

## 10. 路线图

| 阶段 | 范围 |
|---|---|
| **Phase 1**(本计划) | 透明网关 + HTTP/SSE 沉淀 + 熔断/限流 + 反查 caller + 脱敏/TTL + metrics + 安全/合规;**WS 仅 passthrough 不捕获** |
| Phase 2 | (a) **WebSocket `/v1/realtime` 捕获**:Hijacker+双向 dial,边收边发定期 flush,events JSONB 子结构(不复用 prompt/completion 二分);(b) `OnConversationComplete`:文本抽取→pgvector 向量化→高频问题聚类→实体识别 |
| Phase 3 | `OnRequestRewrite`:按意图召回上下文→注入 system prompt(网关层,用户无感);**延迟预算见 §8.4,旁路开关必备** |

---

## 11. 待确认的实现细节(不阻塞开工)

1. New API 内网地址(`NEW_API_BASE_URL`)、PG 连接串。
2. New API 是否 HTTPS(影响 Transport TLS 配置)。
3. `caller_group` 是否值得存(若 token 多不设 group,可去掉该列)。
4. 留存期 90 天是否获法务/安全评审通过(§0.2)。
5. 列加密(`pgcrypto`)是 Phase 1 还是 v1.1(取决于现有 PG 是否已磁盘加密)。

> 以上细节可在实现中微调,不影响主架构。
