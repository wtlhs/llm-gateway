# 企业上下文仓库 · 可视化管理平台设计方案

> **目标**:在网关已沉淀的对话流量基础上,构建可视化管理平台,将数据沉淀升级为企业知识库,最终反哺 agent 落地。
> **定位**:看(观测)→ 管(治理)→ 析(洞察)→ 反哺(闭环)。
> **形态**:独立 Web 应用(React + Ant Design 前端 / Go cmd/platform 后端)。

---

## 一、平台架构

### 1.1 部署架构

```
浏览器 (React SPA)
   ↓ HTTPS
nginx (复用现有, 加 /platform location)
   ↓
platform-server (Go 独立进程, 新增 cmd/platform)
   ├─ REST API /api/v1/*
   ├─ 鉴权中间件 (Bearer Token)
   └─ 独立 PG 连接池 (复用 db.Store 代码, 连接池隔离)
       ├─ llm_conversation (对话流水)
       ├─ system_prompts (知识资产-配置层)
       ├─ knowledge_items (知识资产-经验层, 新增)
       └─ pg_stat_user_tables (运维监控)
```

**核心决策:独立进程,不内嵌网关**。理由见 §五性能分析。

### 1.2 与现有系统的关系

```
现状(已完成):
  网关(cmd/gateway) → llm_conversation + system_prompts

平台演进(本次设计):
  cmd/platform(新增) → 复用 db.Store 代码, 独立连接池
  web/(新增)         → React SPA
  knowledge_items    → 知识库经验层(新增)

最终形态:
  ┌─ 网关层: cmd/gateway (热路径, 不动)
  ├─ 平台层: cmd/platform + web/ (管理 + 知识库)
  └─ 数据层: PG (llm_conversation + system_prompts + knowledge_items)
```

---

## 二、功能全景(四层能力)

### 2.1 第一层:看(观测)— 只读展示

| 模块 | 解决什么问题 | 数据来源 |
|------|------------|---------|
| **总览看板** | 系统健康度、今日用量、趋势 | llm_conversation 聚合 + /metrics |
| **对话检索** | 查看某次对话内容、排查失败 | llm_conversation 明细 |
| **运维监控** | 数据库容量、熔断状态、TTL | pg_stat + metrics |
| **趋势分析** | 用量增长、团队分布 | 时间序列聚合 |

### 2.2 第二层:管(治理)— 含写操作

| 模块 | 解决什么问题 | 关键能力 |
|------|------------|---------|
| **知识库浏览** | 公司有哪些 agent、配置是什么 | system_prompts + knowledge_items |
| **配置管理** | 改网关参数/白名单/排除模型 | config 读写 |
| **数据治理** | 脱敏/截断/TTL 清理状态 | redacted/truncated 统计 |
| **导出归档** | 批量导出对话做分析 | JSON/CSV 导出 |

### 2.3 第三层:析(洞察)— 知识提取

| 模块 | 解决什么问题 | 关键能力 |
|------|------------|---------|
| **agent 效能** | 哪个 system prompt 效果好 | 成功率/token 效率对比 |
| **prompt 模式** | 什么问法/配置效果好 | 聚类 + 关联分析 |
| **工具使用** | 哪些 MCP 工具高频/失败 | tool_calls 统计 |
| **成本分析** | 哪个团队烧 token 多 | token 维度聚合 |

### 2.4 第四层:反哺(闭环)— 终极目标

| 模块 | 解决什么问题 | 关键能力 |
|------|------------|---------|
| **知识检索** | 新 agent 查企业已有经验 | 向量检索 + 关键词 |
| **智能注入** | 自动把知识注入新请求 context | OnRequestRewrite hook |
| **推荐配置** | 新建 agent 推荐最佳 system | 相似度推荐 |
| **效果追踪** | 注入的知识是否提升效果 | A/B 对比 |

---

## 三、6 大功能模块详细设计

### 3.1 总览看板 `/`

- 今日对话量(大数字卡片 + 同比昨日)
- 7 天趋势线图(对话量 + token 消耗双 Y 轴)
- 实时指标:QPS / 平均延迟 / 错误率(调网关 /metrics)
- Top 5 模型 / Top 5 调用方(饼图/柱状)
- 知识资产:system prompt 总数 + 高频 agent Top 5

### 3.2 对话检索 `/conversations`

- 筛选栏:时间范围 / model / caller_tag / endpoint / 流式 / 状态码
- 列表:分页表格,每行显示时间/model/caller/延迟/token 数/状态/system_prompt_hash
- 详情抽屉:点击行展开完整 prompt + completion + tool_calls(JSON 美化展示)
- 导出:选中行导出 JSON/CSV

### 3.3 知识库 `/knowledge`

三个子页签:

**① 配置库(system_prompts,已有数据)**
- agent 列表:按 use_count 排序,显示 agent_name / 使用次数 / 首次出现 / 最近使用
- 配置详情:点击展开完整 system prompt 原文(代码高亮)
- 演进历史:同 agent_name 不同 hash 版本时间线
- 使用分析:哪些 caller 在用该 system / 使用趋势

**② 经验库(knowledge_items,本次新增)**
- 知识列表:按 quality_score 排序,显示 title/question 摘要/type/tags
- 审核工作台:draft → approve/reject/edit
- 详情:question + answer 完整内容 + 来源对话链接

**③ 检索(Phase 3)**
- 搜索框:输入问题/关键词,检索相关知识
- 向量检索 + 关键词混合排序
- 结果按 quality_score × 相似度排序

### 3.4 运维监控 `/ops`

- 网关健康:熔断状态 / channel 深度 / worker 状态(调 /metrics 解析)
- 数据库健康:表大小 / 死元组 / 索引大小 / VACUUM 历史
- TTL 清理:最近清理记录 / 下次预计
- 性能趋势:延迟 P50/P95/P99 折线 / 错误率

### 3.5 配置管理 `/settings`

- 网关参数:当前 AuditMode / MaxBodyBytes / 限流参数(一期只读展示)
- 白名单:C/D 端点列表(可编辑, 需重启网关生效)
- 排除模型:列表管理(增删)
- 平台用户:Token 管理(后续扩展)

### 3.6 安全审计 `/audit`

- 敏感操作日志:配置变更 / 导出操作 / 敏感对话查看
- 数据脱敏状态:redacted 比例 / 截断比例
- 权限审计:谁查看了哪些对话(后续)

---

## 四、知识库设计(核心)

### 4.1 知识来源金字塔

```
    ┌─────────────────────┐
    │  ④ 人工标注(专家筛选)     │  ← 质量最高, 量最少
    ├─────────────────────┤
    │  ③ 工具使用经验           │  ← 从 tool_calls 提取
    ├─────────────────────┤
    │  ② 对话问答对             │  ← 从 completion 提取
    ├─────────────────────┤
    │  ① system prompt        │  ← 已有(自动沉淀)
    └─────────────────────┘
```

| 来源 | 性质 | 状态 |
|------|------|------|
| ① system prompt | 配置型知识(agent 角色/能力/红线) | ✅ 已有(system_prompts 表) |
| ② 对话问答对 | 经验型知识(用户问→agent 答) | 需提取(knowledge_items) |
| ③ 工具使用 | 工具型知识(哪些 MCP/参数有效) | 需提取(knowledge_items) |
| ④ 人工标注 | 高质量精选 | 需运营(平台审核 UI) |

### 4.2 知识类型分类

| 类型 | type 值 | 例子 | 复用方式 |
|------|---------|------|---------|
| 配置型 | `config` | coding agent 的 system prompt | 新 agent 复用(已有 system_prompts) |
| 问答型 | `qa` | "Go 并发怎么处理→goroutine/channel" | 检索相似问题 |
| 模式型 | `pattern` | "'分步骤思考'prompt 模式效果好" | prompt 工程参考 |
| 治理型 | `governance` | "禁止修改 agent model 配置" | 合规约束注入 |
| 工具型 | `tool_usage` | "search 工具传 {q:'...'} 效果好" | 工具调用优化 |

### 4.3 knowledge_items 表 Schema

```sql
CREATE TABLE knowledge_items (
    id              BIGSERIAL PRIMARY KEY,

    -- 知识身份
    hash            VARCHAR(64) UNIQUE,        -- 内容指纹(去重)
    type            VARCHAR(32) NOT NULL,      -- qa/pattern/tool_usage/governance/best_practice

    -- 知识内容
    title           VARCHAR(256),              -- 标题
    question        TEXT,                      -- 问题/场景描述(qa 类型)
    answer          TEXT,                      -- 回答/经验总结
    summary         TEXT,                      -- 一句话摘要(检索展示用)

    -- 来源追溯
    source_type     VARCHAR(32) NOT NULL,      -- auto_extract/manual/imported
    source_conv_id  BIGINT,                    -- 来源对话(自动提取时)
    source_system_hash VARCHAR(64),            -- 关联的 system prompt

    -- 关联维度
    agent_name      VARCHAR(128),              -- 所属 agent
    model           VARCHAR(128),              -- 关联模型
    tags            TEXT[],                    -- 标签(多维度)
    caller_tags     TEXT[],                    -- 使用团队

    -- 质量评分
    quality_score   REAL DEFAULT 0,            -- 质量分(0-1, 自动+人工)
    use_count       INTEGER DEFAULT 0,         -- 被检索引用次数
    helpful_count   INTEGER DEFAULT 0,         -- "有用"投票
    status          VARCHAR(16) DEFAULT 'draft', -- draft/approved/archived/rejected

    -- 向量(Phase 3, pgvector)
    embedding       vector(1536),              -- 语义向量(question+summary 编码)

    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_knowledge_type     ON knowledge_items (type, status);
CREATE INDEX idx_knowledge_agent    ON knowledge_items (agent_name);
CREATE INDEX idx_knowledge_tags     ON knowledge_items USING GIN (tags);
CREATE INDEX idx_knowledge_quality  ON knowledge_items (quality_score DESC);
-- 向量索引(Phase 3, 需 pgvector 扩展)
-- CREATE INDEX idx_knowledge_embedding ON knowledge_items USING ivfflat (embedding vector_cosine_ops);
```

### 4.4 ETL 提取管道

```
llm_conversation(对话流水)
        │
        ▼
   ┌─ 自动提取管道(后台 job) ─────────────┐
   │                                       │
   │  ① Q&A 提取(非流式优先)               │  prompt 的 user → question
   │                                       │  completion 的 content → answer
   │                                       │  截取前 200 字 → summary
   │                                       │
   │  ② 质量打分                            │  http_status=200 + token 效率高 → +
   │                                       │  无 error_message → +
   │                                       │  completion 长度合理 → +
   │                                       │
   │  ③ 工具使用提取                        │  tool_calls JSONB → 工具名+入参
   │                                       │
   │  ④ 去重                               │  hash(question+answer) → 已有则跳过
   │                                       │
   │  ⑤ 入库(status=draft)                 │  等待人工审核
   └───────────────────────────────────────┘
        │
        ▼
   人工审核(平台 UI)
   │  approve → status=approved, 进入检索池
   │  reject  → status=rejected
   │  edit    → 调整 title/summary/tags/quality_score
```

**提取时机**:
- 实时(轻量):对话落库后 hook 触发(已预留 `hooks.OnConversationComplete`)
- 批量(主力):定时 job(每小时)批量处理,跑质量打分和去重

### 4.5 反哺机制(Phase 4)

```
新 agent 启动 / 新请求到达网关
        │
        ▼
   OnRequestRewrite hook(已预留 internal/hooks/hooks.go)
        │
        ├── ① 提取当前意图(user message)
        │
        ├── ② 检索知识库
        │     ├─ 向量检索: embedding 相似度 Top-K
        │     ├─ 关键词检索: tags/question 匹配
        │     └─ 混合排序: quality_score × 相似度
        │
        ├── ③ 拼装注入(仅 approved 知识)
        │     将检索到的知识注入 system prompt 或 user context
        │     "<企业知识参考> 以下是相关经验: ..."
        │
        └── ④ 旁路安全
              检索失败/超时 → 跳过注入, 不阻塞请求(DESIGN.md §8.4)
```

反哺分三层渐进:

| 层次 | 做什么 | 复杂度 |
|------|--------|--------|
| 被动参考(Phase 4.1) | 平台展示"相似问题怎么解决的",人工参考 | 低 |
| 主动注入(Phase 4.2) | 网关自动注入知识到 context | 中 |
| 智能推荐(Phase 4.3) | 新建 agent 推荐最佳配置 | 高 |

### 4.6 知识库与分层存储的关系

```
现状(已完成):
  llm_conversation ──ref──→ system_prompts(配置层)

知识库演进:
  llm_conversation ──extract──→ knowledge_items(经验层)
  system_prompts  ──ref──────→ knowledge_items(关联配置)

最终三层:
  ┌─ 配置层: system_prompts(agent 配置资产, 已有)
  ├─ 经验层: knowledge_items(Q&A/模式/工具经验, 新增)
  └─ 检索层: pgvector embeddings(语义检索, Phase 3)
        │
        ▼
  OnRequestRewrite → 反哺新 agent
```

---

## 五、性能影响分析:为什么独立进程

### 5.1 三方案对比

| 维度 | 内嵌网关 | 独立进程(推荐) | 全独立 |
|------|---------|--------------|--------|
| 共享进程 | ✅ 是 | ❌ 否 | ❌ 否 |
| 共享代码 | ✅ db.Store | ✅ db.Store | ❌ 各写各的 |
| 热路径风险 | ⚠️ 高 | ✅ 无 | ✅ 无 |
| 开发量 | 最小 | 小(多 ~100 行 main) | 大(重写查询) |
| 运维 | 1 容器 | 2 容器 | 2 容器 |

### 5.2 内嵌网关的风险(为什么不选)

网关是热路径——每条 LLM 请求必经。平台内嵌会导致三个资源争用:

**① 连接池争用**
```
DBMaxOpenConns = 25(网关配置)
  网关 8 worker 常规占用 ≤8 连接
  平台慢查询(JSONB 全表扫)可能占 1-10 连接
  剩余空闲连接随平台用户增长而减少 → 挤占写入
```

**② CPU 争用**
```
平台聚合查询(GROUP BY / JSONB 解析)是 CPU 密集
网关热路径(SSE 聚合/gzip 解压)也吃 CPU
同进程竞争 Go scheduler, 高负载时互相拖慢
```

**③ Goroutine 膨胀**(间接)
```
虽然 goroutine 本身轻(8KB), 但平台慢查询持有的 DB 连接是真实瓶颈
```

### 5.3 独立进程的隔离保证

```
网关进程(独立):              平台进程(独立):
├─ 25 连接池                 ├─ 自己的连接池(建议 5-10)
├─ 8 worker                  ├─ HTTP handler
└─ 热路径纯净                └─ 慢查询不影响网关
        ↓                           ↓
         └── 共享 PG(MVCC 隔离) ──┘
```

**PG 层面**:PostgreSQL MVCC 机制保证读写互不阻塞(读不阻塞写,写不阻塞读)。平台查询和网关写入走不同连接,天然隔离。

### 5.4 独立进程的防护措施

即使独立进程,平台慢查询仍可能拖垮 PG 实例(共享 CPU/IO)。必须做:

1. **查询全部加 LIMIT + 分页**(防全表扫)
2. **JSONB 内容搜索加超时**(`context.WithTimeout`)
3. **复杂聚合用物化视图或缓存**(避免每次重算)
4. **平台连接池设小**(MaxOpenConns=5,防雪崩)
5. **大查询走只读副本**(未来数据量大时)

---

## 六、REST API 设计

### 6.1 API 规范

```
鉴权: 所有 /api/v1/* 需 Bearer Token(Authorization: Bearer <token>)
响应: 统一 { code: 0, data: ..., msg: "ok" }
分页: ?page=1&size=20, 返回 { list: [], total: N }
时区: 所有时间参数用 ISO8601, 内部按 Asia/Shanghai 处理
```

### 6.2 端点清单

| 端点 | 方法 | 说明 |
|------|------|------|
| `/api/v1/dashboard/overview` | GET | 总览看板数据 |
| `/api/v1/dashboard/trend` | GET | 趋势数据(?days=7) |
| `/api/v1/conversations` | GET | 对话列表(分页+筛选) |
| `/api/v1/conversations/:id` | GET | 对话详情 |
| `/api/v1/conversations/export` | POST | 导出 |
| `/api/v1/knowledge/configs` | GET | system_prompts 列表 |
| `/api/v1/knowledge/configs/:hash` | GET | system prompt 详情 |
| `/api/v1/knowledge/configs/:hash/timeline` | GET | 演进历史 |
| `/api/v1/knowledge/items` | GET | knowledge_items 列表 |
| `/api/v1/knowledge/items` | POST | 新建(人工) |
| `/api/v1/knowledge/items/:id` | PUT | 编辑/审核 |
| `/api/v1/knowledge/search` | GET | 知识检索(Phase 3) |
| `/api/v1/ops/health` | GET | 网关+DB 健康 |
| `/api/v1/ops/db-stats` | GET | 数据库统计 |
| `/api/v1/settings` | GET | 配置(只读) |

---

## 七、技术选型

| 层 | 技术 | 理由 |
|----|------|------|
| 前端框架 | React 18 + TypeScript | 生态成熟, Ant Design Pro 开箱即用 |
| UI 组件 | Ant Design 5 | 表格/表单/图表齐全, 适合管理后台 |
| 图表 | ECharts (echarts-for-react) | 国内文档好, 支持 timeline/热力图 |
| 构建工具 | Vite | 比 CRA 快 10 倍 |
| 后端 | Go 1.25 + chi 路由 | 与网关同语言, 复用 db 层 |
| 数据访问 | pgx 原生 SQL(不用 ORM) | 与现有代码一致 |
| 鉴权 | Bearer Token 中间件 | 起步用 ADMIN_AUTH_TOKEN |

---

## 八、项目结构

```
NewApiLog/
├── cmd/
│   ├── gateway/              (现有) 网关进程
│   └── platform/             (新增) 平台后端
│       └── main.go
├── internal/
│   ├── db/                   (现有, 扩展只读查询方法)
│   │   └── store.go          +ListConversations +GetConversation +DashboardOverview...
│   ├── platform/             (新增)
│   │   ├── server.go         HTTP server + chi 路由 + 鉴权
│   │   ├── auth.go           Token 鉴权中间件
│   │   └── handler/          API handler
│   │       ├── dashboard.go
│   │       ├── conversation.go
│   │       ├── knowledge.go
│   │       └── ops.go
│   └── ...                   (现有网关代码不动)
├── web/                      (新增) 前端
│   ├── package.json
│   ├── src/
│   │   ├── pages/            6 模块页面
│   │   ├── components/
│   │   ├── api/              API 客户端
│   │   └── App.tsx
│   └── vite.config.ts
├── migrations/
│   ├── 0001_init.up.sql
│   ├── 0002_system_prompts.up.sql
│   └── 0003_knowledge_items.up.sql  (新增)
└── docker-compose.platform.yml
```

---

## 九、分期实施(4 期)

| 期 | 范围 | 工作量 | 产出 |
|----|------|--------|------|
| **一期 MVP** | 后端骨架 + 总览看板 + 对话检索 + 配置库浏览 | 1-2 周 | 能看数据, 80% 价值 |
| **二期** | knowledge_items + 自动提取管道 + 审核UI + 运维监控 | 1-2 周 | 知识库经验层 |
| **三期** | pgvector + 向量检索 + 智能分析(效能/成本) | 2 周 | 语义检索能力 |
| **四期** | OnRequestRewrite 反哺注入 + 效果追踪 | 2 周 | 知识闭环 |

### 9.1 一期 MVP 任务清单

**后端**:
- [ ] `cmd/platform/main.go`:进程入口(config + db + server)
- [ ] `internal/platform/server.go`:chi 路由 + Bearer Token 鉴权中间件
- [ ] `internal/platform/handler/dashboard.go`:overview + trend API
- [ ] `internal/platform/handler/conversation.go`:列表 + 详情 API
- [ ] `internal/platform/handler/knowledge.go`:配置库列表 + 详情 API
- [ ] `internal/db/store.go` 新增查询:ListConversations / GetConversation / DashboardOverview / DashboardTrend / ListAgents

**前端**:
- [ ] Vite + React + Ant Design Pro 脚手架
- [ ] API 客户端 + Bearer Token 拦截器
- [ ] 总览看板页(数字卡片 + ECharts 趋势图)
- [ ] 对话检索页(筛选 + 分页表格 + 详情抽屉)
- [ ] 配置库浏览页(agent 列表 + system prompt 详情)

---

## 十、数据库新增汇总

```sql
-- 0003_knowledge_items.up.sql (二期)

-- 操作审计日志(平台敏感操作)
CREATE TABLE IF NOT EXISTS platform_audit_log (
    id          BIGSERIAL PRIMARY KEY,
    actor       VARCHAR(128) NOT NULL,    -- 操作人
    action      VARCHAR(64)  NOT NULL,    -- export/config_change/view_sensitive
    target      VARCHAR(256),             -- 操作对象
    detail      JSONB,                    -- 操作详情
    ip          VARCHAR(64),
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- 知识库经验层
CREATE TABLE IF NOT EXISTS knowledge_items (
    -- 见 §4.3 完整定义
);
```

---

## 十一、关键决策记录

| 决策 | 选择 | 理由 |
|------|------|------|
| 独立进程 vs 内嵌 | **独立进程** | 隔离热路径, 复杂查询不挤占代理资源(§五) |
| ORM vs 原生 SQL | **原生 SQL** | 与现有代码一致, 性能可控 |
| SPA vs SSR | **SPA** | 内部工具不需 SEO, 交互体验好 |
| 一期鉴权 | **Bearer Token** | 复用 ADMIN_AUTH_TOKEN, 零额外基建 |
| 图表库 | **ECharts** | 国内文档好, Ant Design 兼容 |
| 知识库存储 | **knowledge_items 统一表** | 五类知识归一化, 通过 type 区分 |
| 知识审核工作流 | **draft→approve** | 自动提取保证量, 人工审核保证质 |
| 反哺方式 | **渐进三层** | 被动参考→主动注入→智能推荐 |
| 向量检索 | **pgvector** | 留在 PG 内, 不引入新基础设施 |
| 配置读写 | **一期只读** | 写需重启网关, 先展示不操作 |

---

## 十二、docs 体系总览

落地后,docs 目录形成完整闭环:

```
docs/
├── MYSQL_REFACTOR_PLAN.md   MySQL 反查(已实施)
├── TIMEZONE.md              时区规范(查询护栏)
├── KNOWLEDGE_LAYER.md       分层存储架构(已实施)
└── PLATFORM_DESIGN.md       本文档(平台 + 知识库设计)
DEPLOY.md                    生产部署 Runbook
DESIGN.md                    网关 Phase 1 总体设计
```

从"网关数据沉淀(Phase 1)"→"分层存储(Phase 1.5)"→"可视化管理平台(Phase 2)"→"知识反哺(Phase 3)",形成完整的企业上下文仓库演进路线。
