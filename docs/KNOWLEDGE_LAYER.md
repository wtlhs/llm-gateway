# 知识资产分层存储架构

> **目标**:将企业上下文仓库从"对话流水账"升级为"可反哺 agent 的知识库"。
> 本文档是 Phase 2 的实施蓝图,记录分层存储的 schema 设计、ETL 流程、迁移策略。
> 实施时机:数据量过万条后(当前 66 条,分层收益不明显)。

---

## 一、为什么需要分层

### 1.1 现状问题(基于 66 条真实数据诊断)

当前所有对话数据平铺在单表 `llm_conversation.prompt_text`,system prompt 和 user message 混存。

| 问题 | 数据证据 | 影响 |
|------|---------|------|
| **存储浪费** | system prompt 占 prompt 总存储 41.2%,平均 15.3KB/条,是 user(1.2KB)的 12.7 倍 | 1 万条约 150MB 纯 system 冗余 |
| **高度重复** | 同一 agent 的 system prompt 每次请求全量重传 | 100 次请求 = 100 份相同 system,知识零增量 |
| **截断根因** | coding agent 请求体 50-200KB,90% 是 system prompt + 工具定义撑大 | 导致 76% 数据截断损坏(已修但仍占空间) |
| **知识不可检索** | system prompt 混在 JSONB 里,无独立索引/指纹 | 无法"查找公司有哪些 agent 配置""哪个 system prompt 效果好" |

### 1.2 核心洞察

诊断数据显示 system prompt 的本质:
- 96% 含 `agent`、84% 含 `MCP`/`tools`、53% 含 `禁止`/`权限`
- **这不是"对话内容",是"agent 配置资产"**:角色定义、工具清单、治理红线
- 内容由开发者维护,跨请求复用,版本化演进

企业知识库的目标是**反哺 agent 落地**——而 agent 落地最核心的资产恰恰是 system prompt。把它从对话流水里抽出来单独管理,是知识库的基础。

---

## 二、架构设计

### 2.1 三层分离

```
┌─────────────────────────────────────────────────────┐
│ 对话流水层 llm_conversation (现有, 改造)              │
│   审计/合规/排查。去掉 system, 引用知识层              │
│   prompt_text: user + assistant 消息                  │
│   system_prompt_hash: FK → system_prompts.hash        │
└──────────────────┬──────────────────────────────────┘
                   │ join
┌──────────────────▼──────────────────────────────────┐
│ 知识资产层 system_prompts (新增)                      │
│   agent 配置库。去重存储, 版本追踪                    │
│   hash (内容指纹, PK)                                 │
│   content (完整 system, 去重存 1 份)                  │
│   agent_name / tokens / use_count / first_seen       │
└─────────────────────────────────────────────────────┘
                   │
┌──────────────────▼──────────────────────────────────┐
│ 检索服务层 (Phase 3, 本文档不展开)                    │
│   向量检索 + 关键词 → 注入新 agent context            │
└─────────────────────────────────────────────────────┘
```

### 2.2 设计原则

1. **流水层只存增量**:去掉重复的 system,存 user/assistant + 引用
2. **资产层去重存 1 份**:同一 system prompt(按内容指纹)全局只存一次
3. **可还原**:join 流水层 + 资产层 = 完整原始请求,不丢信息
4. **版本追踪**:agent 改配置产生新 hash,自然记录演进历史
5. **向后兼容**:迁移期老数据保留,新数据走新结构

---

## 三、Schema 设计

### 3.1 知识资产层 `system_prompts`(新增)

```sql
-- +goose Up
-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS system_prompts (
    hash            VARCHAR(64)  PRIMARY KEY,              -- sha256(content), 内容指纹 + 主键
    agent_name      VARCHAR(128),                           -- 识别出的 agent 名(启发式提取)
    content         TEXT         NOT NULL,                  -- 完整 system prompt 原文
    content_size    INTEGER      NOT NULL,                  -- 字节数(便于统计)
    tokens          INTEGER,                                -- 该 system 的 token 数(首次观测值)

    -- 版本与使用追踪
    first_seen      TIMESTAMPTZ  NOT NULL DEFAULT now(),    -- 首次出现时间
    last_seen       TIMESTAMPTZ  NOT NULL DEFAULT now(),    -- 最近出现时间(每次刷新)
    use_count       BIGINT       NOT NULL DEFAULT 0,        -- 累计使用次数
    caller_tags     TEXT[],                                -- 使用过该 system 的 caller 数组

    -- 演进链(同 agent 的不同版本)
    prev_hash       VARCHAR(64),                            -- 上一版本 hash(若能识别同 agent 演进)

    created_at      TIMESTAMPTZ  NOT NULL DEFAULT now()
);

-- agent_name 索引:按 agent 查配置
CREATE INDEX IF NOT EXISTS idx_sysprompt_agent ON system_prompts (agent_name);
-- 最近使用索引:找活跃 agent
CREATE INDEX IF NOT EXISTS idx_sysprompt_lastseen ON system_prompts (last_seen DESC);
-- 使用次数:找高频 agent(知识库价值排序)
CREATE INDEX IF NOT EXISTS idx_sysprompt_usecount ON system_prompts (use_count DESC);
-- +goose StatementEnd
```

**字段设计要点**:

| 字段 | 用途 |
|------|------|
| `hash` | 内容指纹,天然去重。同一 system prompt 全局唯一 |
| `agent_name` | 启发式提取(见 §3.3)。让"公司有哪些 agent"可查询 |
| `use_count` | 每次该 system 出现在请求里就 +1。识别高频/核心 agent |
| `caller_tags` | 哪些团队/用户在用这个 agent。跨团队复用分析 |
| `prev_hash` | 演进链。同一 agent 改配置后,新旧版本可追溯 |

### 3.2 流水层 `llm_conversation`(改造)

新增引用列,不破坏现有结构:

```sql
-- +goose Up
ALTER TABLE llm_conversation ADD COLUMN IF NOT EXISTS system_prompt_hash VARCHAR(64);
ALTER TABLE llm_conversation ADD COLUMN IF NOT EXISTS system_prompt_size  INTEGER;

-- 外键引用(可选: 若担心孤儿记录可加, 但会降低写入性能; 推荐逻辑外键)
-- ALTER TABLE llm_conversation
--   ADD CONSTRAINT fk_conv_sysprompt FOREIGN KEY (system_prompt_hash)
--   REFERENCES system_prompts(hash) ON DELETE SET NULL;

CREATE INDEX IF NOT EXISTS idx_llm_conv_sysprompthash ON llm_conversation (system_prompt_hash);
```

**改造后 prompt_text 的含义**:
- **新数据**:`prompt_text` 只含 user/assistant/tool 消息(去掉 system),`system_prompt_hash` 指向资产层
- **老数据**:`prompt_text` 仍含完整请求(含 system),`system_prompt_hash` 为 NULL
- **查询兼容**:无论新老,`prompt_text` 始终是合法可解析的请求体

### 3.3 agent_name 启发式提取

system prompt 内容没有标准结构,但可从内容识别 agent 身份。优先级:

```
1. 内容含 <Role>...<Name>X</Name>  → X
2. 内容含 "你是 X" / "You are X"    → X
3. 内容含 @X(Lane/agent 声明)      → X
4. caller_tag(coding-agent-xxx)    → xxx
5. 无法识别                          → "unknown"
```

这是尽力而为,不追求 100% 准确。Phase 2 后期可用 LLM 做更精准的分类。

---

## 四、ETL 流程(采集时实时分流)

### 4.1 写入路径(网关层改造)

在现有 `record.go` 的 `SetPrompt` 之后,新增 system 分流:

```
请求到达
  ↓
snapshotBody(现有) → snap.decoded
  ↓
injectIncludeUsage(现有)
  ↓
★ 新增:splitSystemPrompt(snap.decoded)
  ├─ 提取 system content(OpenAI: messages[role=system]; Anthropic: 顶层 system)
  ├─ 计算 system_hash = sha256(system_content)
  ├─ 从 snap.decoded 移除 system 消息 → 精简后的 prompt_text
  └─ 返回 (system_content, system_hash, slimmed_prompt)
  ↓
rec.SetPrompt(slimmed_prompt)         ← 流水层存精简版
rec.SystemPromptHash = system_hash    ← 引用资产层
  ↓
★ 新增:pipeline.upsertSystemPrompt(system_hash, system_content, agent_name)
  ↑ 异步, 走 meta channel(必须保留, 不降级)
```

### 4.2 upsertSystemPrompt 逻辑

```sql
INSERT INTO system_prompts (hash, agent_name, content, content_size, first_seen, last_seen, use_count)
VALUES ($1, $2, $3, $4, now(), now(), 1)
ON CONFLICT (hash) DO UPDATE
SET last_seen = now(),
    use_count = system_prompts.use_count + 1,
    caller_tags = array_append(
        array_remove(system_prompts.caller_tags, $5), $5
    )  -- 去重追加当前 caller
WHERE NOT $5 = ANY(system_prompts.caller_tags);  -- 避免重复 caller 重复计数
```

**幂等**:`ON CONFLICT (hash) DO UPDATE` 保证并发安全。
**use_count 语义**:每条新对话 +1(不是每次请求去重 caller 后 +1,这样高频 agent 自然浮现)。

### 4.3 agent_name 提取时机

在 `splitSystemPrompt` 里同步提取(不阻塞热路径,因为只是字符串扫描):

```go
func extractAgentName(systemContent string) string {
    // 优先级匹配, 见 §3.3
    // 用 strings.Contains + 正则, 失败返回 "unknown"
}
```

---

## 五、迁移策略(老数据处理)

### 5.1 不回填,向前兼容

- **老数据**(66 条及之后到迁移前的数据):`prompt_text` 含完整 system,`system_prompt_hash = NULL`
- **新数据**(迁移后):`system_prompt_hash` 非空,`prompt_text` 精简
- **查询**:应用层兼容两种格式(`system_prompt_hash IS NOT NULL` → join 资产层;否则从 prompt_text 提取)

### 5.2 可选:批处理回填

迁移后,对老数据做一次性回填(离线脚本,不影响在线):

```sql
-- 对每条老记录, 提取 system, 算 hash, 写入资产层, 回填引用
-- 用 Go 脚本: 读取 → splitSystemPrompt → upsert + UPDATE
-- 见 cmd/backfill/main.go(实施时编写)
```

回填是可选的优化,不回填也不影响新数据质量。

---

## 六、收益预估

### 6.1 存储节省

| 场景 | 现状(平铺) | 分层后 | 节省 |
|------|------------|--------|------|
| 1 万条对话,100 个 agent 配置 | system 占 ~150MB | 资产层 100×15KB=1.5MB + 流水层引用 | **~98% system 空间** |
| 单条请求大小 | 50-200KB(coding agent) | 5-20KB(去 system 后) | 75-90% |

### 6.2 能力解锁

| 能力 | 现状 | 分层后 |
|------|------|--------|
| 查公司有哪些 agent | ❌ 无法 | `SELECT DISTINCT agent_name FROM system_prompts` |
| 找最活跃 agent | ❌ 无法 | `ORDER BY use_count DESC` |
| agent 配置版本历史 | ❌ 无法 | `WHERE agent_name=X ORDER BY first_seen`(prev_hash 链) |
| 跨团队复用分析 | ❌ 无法 | `caller_tags` 数组查交集 |
| 检索最佳 system prompt | ❌ 无法 | Phase 3 向量检索资产层 |

### 6.3 性能影响

| 项 | 影响 | 缓解 |
|----|------|------|
| 写入多一次 upsert | +1 次 DB 操作(system_prompts) | ON CONFLICT 幂等,走 meta channel |
| 热路径多一次 split | +几微秒(字符串扫描) | 忽略不计 |
| 查询需 join 还原 | 读放大 | 90% 查询不需要 system(看 user 问题即可) |

---

## 七、实施清单

### 7.1 前置条件
- [ ] 数据量过万条(当前 66 条,收益不明显)
- [ ] 截断 bug + Anthropic bug 已修复并部署(✅ 已完成)
- [ ] system prompt 在 prompt_text 里完整保存(✅ MaxBodyBytes=1MB)

### 7.2 开发任务
- [ ] 新增 migration: `0002_system_prompts.up.sql`
- [ ] `internal/db/store.go`: 新增 `UpsertSystemPrompt` + `splitSystemPrompt` 逻辑
- [ ] `internal/audit/record.go`: `Record` 增加 `SystemPromptHash` 字段,`SetPrompt` 后分流
- [ ] `internal/audit/pipeline.go`: persist 阶段 upsert system prompt(走 meta channel)
- [ ] `cmd/backfill/main.go`: 老数据回填脚本(可选)
- [ ] 单测 + E2E: system 分流 + 资产层去重 + 还原 join

### 7.3 验收
- [ ] 新对话的 `system_prompt_hash` 非空
- [ ] `system_prompts` 表的 use_count 随对话增长
- [ ] 同一 agent 的多次请求,system 只存 1 份
- [ ] join 流水层 + 资产层 能还原完整原始请求
- [ ] 流水层 prompt_text 大小下降 75%+

---

## 八、决策记录

### 8.1 为什么不存到外部向量库(Pinecone/Milvus)
Phase 2 阶段数据量不大,PG 单表 + 索引足够。Phase 3 做语义检索时,再加 pgvector 扩展(仍在 PG 内),避免引入新基础设施。

### 8.2 为什么用 hash 而非 agent_name 做主键
同一 agent_name 可能有多个版本(配置演进),hash 是内容指纹保证唯一性。agent_name 作为可查询的普通字段。

### 8.3 为什么 use_count 每条对话 +1 而非去重 caller
高频 agent 应该浮现——一个团队每天用 1000 次的 agent 比另一个用 10 次的更有知识价值。use_count 反映真实使用强度。

### 8.4 为什么不强制外键
逻辑外键(应用层保证 hash 存在)比物理外键灵活——允许资产层 GC 删除冷门 system 而不阻塞流水层写入。
