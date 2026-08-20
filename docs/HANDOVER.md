# LLM 网关与知识库项目交接文档

> **用途**：本文档是 `D:\GitHubProjects\NewApiLog`（llm-gateway 项目）的完整交接说明。
> 阅读对象：接手继续开发的其他 AI agent 或工程师。
> 生成时间：2026-08-18（基于 08-04 ~ 08-18 全部工作与决策记录）。
> 代码仓库：`github.com/wtlhs/llm-gateway`（分支 `master`，工作区 `D:\GitHubProjects\NewApiLog`）。

---

## 一、项目定位与架构

架在 **New API**（`calciumion/new-api:v1.0.0-rc.23`，公司级 LLM 通用底座）前的**透明代理网关**，
零改造接入（agent 的 `base_url` 不变，改 DNS 即可让全流量经过网关），把公司所有 agent 的
对话流量（prompt + completion 原文）实时沉淀为**企业级上下文仓库**，并支撑可视化平台与知识库。

```
agent(Claude Code/ZCode/opencode 等)
   ↓ https
nginx (newapi.wtlhs.com)
   ├─ /v1/*       → llm-gateway 容器(127.0.0.1:8080)   代理+脱敏+沉淀
   └─ /           → New API 容器(1Panel-new-api-8YHP:3000, 公网 3300) 管理后台

llm-gateway → 沉淀 PostgreSQL(llm_gateway 库) + caller 反查 MySQL(new_api 库)
platform.wtlhs.com → llm-platform 容器(127.0.0.1:8089) 管理平台(只读查询)
cron */15 分钟 → knowledge-extract(--from-last 增量) → knowledge_pairs 问答对
```

**双组件**：
| 组件 | 容器 | 端口 | 职责 |
|---|---|---|---|
| 网关 | `llm-gateway` | 127.0.0.1:8080 | 热路径，每条 LLM 请求必经，透明代理 + 脱敏 + 实时沉淀 |
| 平台 | `llm-platform` | 127.0.0.1:8089（容器内 8088） | 管理平台：总览/对话/知识库/运维/配置 5 页面 + 17 API |

所有容器在 `1panel-network` 内用容器名互联。

---

## 二、部署环境（服务器 82.156.138.160，SSH 别名 `scm`）

### 2.1 连接方式
- SSH 别名 `scm` 已配置在 `~/.ssh/config`（HostName 82.156.138.160, User root, IdentityFile ~/.ssh/id_rsa）
- 命令：`ssh scm "<cmd>"`

### 2.2 容器与端口
| 容器 | 镜像 | 端口映射 | 说明 |
|---|---|---|---|
| `llm-gateway` | `llm-gateway:local` | 127.0.0.1:8080→8080 | 网关（最新镜像 `953544d`，含 safeJSON 脱敏宽容化） |
| `llm-platform` | `llm-platform:local` | 127.0.0.1:8089→8088 | 平台（最新镜像 `53899d55`，含 tzdata） |
| `1Panel-new-api-8YHP` | `calciumion/new-api:v1.0.0-rc.23` | 0.0.0.0:3300→3000 | New API 管理后台 |
| `1Panel-postgresql-AWYE` | `postgres:18.3-alpine` | 127.0.0.1:15432→5432 | 沉淀库 PG |
| `1Panel-mysql-GYfH` | `mysql:5.7.44` | 0.0.0.0:3306→3306 | New API 业务库 MySQL |

### 2.3 关键路径（服务器）
- `/opt/llm-gateway/`：网关部署目录（`docker-compose.yml` + `.env`，.env 含 `CONTEXT_DB_URL`/`NEWAPI_DB_URL`/`METRICS_AUTH_TOKEN`/`ADMIN_AUTH_TOKEN` 等）
- `/opt/llm-platform/`：平台部署目录（`.env`，含 `PLATFORM_ADMIN_USER/PASSWORD`、`PLATFORM_AUTH_TOKEN`、`PLATFORM_CORS_ORIGINS`）
- `/opt/llm-gateway-build/`：网关构建中转（源码副本、Dockerfile.quick、llm-gateway-linux 二进制）
- `/opt/llm-platform-build/`：平台构建中转（平台/提取器二进制、`run-knowledge-extract.sh`）
- `/var/log/knowledge-extract.log`：知识提取定时任务日志

> **安全约定**：所有密码/Token 只存服务器 `.env`，**不在任何文档/代码中明文写密码**。
> 需要时 `ssh scm "cat /opt/llm-gateway/.env"` 或 `/opt/llm-platform/.env` 查看。

### 2.4 域名与 nginx
- `newapi.wtlhs.com`：`/v1/*` → 网关 8080，`/` → New API 3300（证书 certbot 已签）
- `platform.wtlhs.com`：全部 → 127.0.0.1:8089（证书已签）
- nginx 配置在 `/etc/nginx/conf.d/*.conf`，改后 `nginx -t && nginx -s reload`

### 2.5 定时任务（crontab，root）
```
*/15 * * * * /opt/llm-platform-build/run-knowledge-extract.sh
```
知识提取增量任务（wrapper 读 .env → 127.0.0.1:15432 → `knowledge-extract --from-last`）。

---

## 三、已上线功能与决策记录（按时间线）

### 3.1 网关核心（Phase 1，已上线）
透明代理 + HTTP/SSE 沉淀 + 熔断/限流 + caller 反查（MySQL JOIN users）+ 脱敏（redact）+ TTL 90 天 + Prometheus metrics + 管理端点 bearer 鉴权。性能：网关 P99 增量 ~1ms，prompt 缓存命中率不受影响（98%）。

### 3.2 client_gone 根因分析与修复（08-04 ~ 08-17）
**现象**：New API 后台大量 `stream ended: reason=client_gone end_error="context canceled"`。
**根因链**（证据闭环）：
1. Claude Code 大上下文请求（61k-93k tokens ≈ 200-350KB）经网关
2. 网关 `LLM_AUDIT_MAX_BODY_BYTES` 截断请求体（原 64KB）→ 截断 JSON 解析失败
3. `extractPromptMeta` 的 `json.Unmarshal` 整体失败 → **`is_stream` 误判 false**
4. 网关误走非流式路径 `io.ReadAll` 缓冲整个 SSE → **客户端收不到流式增量 → 超时断开**
5. 网关 `r.Context()` 取消 → 关闭与 New API 连接 → New API 记 client_gone

**修复**：`extractPromptMeta` 增加容错——`json.Unmarshal` 失败时用正则从 JSON 头部探测
`model`/`stream`（`modelFieldRe`/`streamFieldRe`）。另服务器 `LLM_AUDIT_MAX_BODY_BYTES` 已调大到 1MB。

### 3.3 数据质量修复（08-17，本批核心）
| 问题 | 根因 | 修复 |
|---|---|---|
| prompt_text 92% 是 `{"raw":...}` 包装 | Claude Code 工具 schema 含 Python 风格非法值（`"maximum":*`、`None`/`True`/`False`）**以及网关 redact 脱敏把数字替换成 `****4321`** | `safeJSON` 宽容化：新增 `repairJSON` 状态机（字符串外：`*`→null、`****4321`→null、`-****4321`→null、None→null、True/False→小写、尾逗号删除）；对合法 JSON no-op |
| prompt_tokens 大量为 0（glm-5.2 仅 33.8%） | GLM 上游 `message_start.usage` 恒为 `{"input_tokens":0}`（实测抓包） | 三级兜底：真实值 → `cache_creation+cache_read` 求和 → 本地估算（剥离 JSON 结构字符后 ~2 字符/token）+ `usage_estimated` 标记列（migration 0006） |
| system_prompts agent_name 33% 为 unknown | 旧正则要求 `You are a/an/the` 冠词，无冠词写法（`You are opencode,`/`cc_entrypoint=claude-vscode;You are Claude,`）全落空 | 已知 agent 名单 `reAgentKnown`（22 个，词边界，长短语优先）+ migration 0007 存量回填 |
| 存量数据无法提取（知识库召回率 3.7%） | raw 包装内容非法 → `unwrapPrompt` 解包后仍无法解析 | `knowledge/extract.go` 宽容化：解包后解析失败 → `audit.RepairJSONScan` 修复重试；`unwrapPrompt` 返回已解包内容供修复。**提取时动态修复，不回填表**（避免 1MB JSONB 逐条 UPDATE 膨胀，实测 10 分钟未完成） |

### 3.4 平台部署（08-17）
`llm-platform` 首次完整部署（替换 08-13 旧容器）：
- 前端 `web/`（React+Vite+Antd）embed 进 Go 二进制（`internal/platform/web_dist`）
- 新增 `_ "time/tzdata"` 嵌入时区数据（静态二进制否则 `PLATFORM_TIMEZONE` 失效）
- 管理员登录：`admin` / 密码见服务器 `/opt/llm-platform/.env`
- 安全：Bearer token 兼容 + 限流 120/min + 安全头 + CORS

### 3.5 知识提取定时化（08-17 ~ 08-18）
- `knowledge-extract` 新增 `--from-last`（增量起点 = `knowledge_pairs.max(conv_id)`，秒级）
- cron 每 15 分钟（替换了 08-12 配置但从未成功执行的旧每日任务 `kb-extract.sh`——旧日志为空即根因）
- 召回率结果：提取率 3.7% → 18.8%；knowledge_pairs 272 → **1,135 对**

---

## 四、数据现状（08-18 快照）

### 4.1 沉淀库 `llm_conversation`（PG `llm_gateway`）
| 指标 | 值 |
|---|---|
| 总量 | ~33,700 条（request_id 唯一） |
| 日均 | ~1,900 条 |
| caller_name 填充 | 98.9%（部署后新数据） |
| usage 覆盖 | 100%（~68% 为估算，`usage_estimated=true`） |
| 截断 | ~10%（>1MB 超长请求） |
| 脱敏 | redact 模式（敏感值→`****`） |
| 错误 | 200 占 ~94%，502/503 等 6% |

### 4.2 知识库 `knowledge_pairs`
- **1,135 对**问答对（今日 +164），15 分钟增量持续更新
- 字段：conv_id/question/answer/code_blocks/file_paths/keywords/model/caller_name/endpoint/created_at

### 4.3 已知观察项
- GLM 上游 08-10/11 出现过 28.5% 断开高峰（message_start 后无内容），08-12 后回落 1-2%——关注 GLM 渠道稳定性
- `mimo-v2.5`/`mimo-v2.5-pro`/`kimi-k3` 共 186 条全部 503 `model_not_found`（New API 渠道未配置，非网关问题）
- 网关日志早期 `insert failed: invalid input syntax for type json` 已随 utf8.Valid 校验修复（部署后 0 次）

---

## 五、训练大模型评估（咨询结论，08-18）

**数据画像**：31,617 条有效对话、14,369 条完整对话对（prompt 未截断+有回答，其中 >200 字符回答 6,446）、
工具调用场景 47.4%（14,973 条）、领域 = 软件工程 coding agent 交互、中英混合。

**评级：B-（可用于特定训练方向，需治理）**

**核心判断**：
1. 数据是 **agent 行为轨迹**，不是通用语料 → 适合 **领域 SFT（编码 agent 对齐）/ RLHF 偏好对**，不适合通用预训练/基座
2. **`knowledge_pairs` 不能直接当训练数据**：其过滤标准（去中间过程）是知识检索导向；训练需要**完整对话轨迹（含 tool_calls/thinking）**，数据源应是清洗后的 `llm_conversation`
3. 规模：14K 对起步 + 15 分钟增量（年化 ~50 万条），先跑通管道再谈规模
4. **合规前置**：训练前必须确认员工对话数据授权与公司数据分级（DEPLOY.md 上线评审项）

**后续路线（待执行）**：
- **第 1 步 训练数据管道**：`cmd/repair-prompt-backfill` 全量转结构化 → 构建 `training_dataset` 提取器（保留多轮轨迹+tool_calls，清洗规则与知识库不同：去 `****` 残留、去 system-reminder、去截断/错误记录、按 request 聚合多轮）
- **第 2 步 合规确认**：员工授权/匿名化复核/人工抽检
- **第 3 步 小规模验证**：2-3K 对做 domain SFT（Qwen/GLM 开源底座 LoRA），评测编码通过率/工具调用格式
- **第 4 步 规模化**：管道稳定后全量，评估 RLHF 偏好对

---

## 六、遗留问题与后续任务清单（给接手 agent）

### 优先级高
- [ ] **训练数据管道**（见五，第 1 步）：全量 `repair-prompt-backfill` 转结构化 + `training_dataset` 提取器设计
- [ ] **合规确认**：员工数据授权/匿名化抽检，找法务/安全评审
- [ ] **新网关观察**：08-18 部署的脱敏宽容化网关（`953544d`）在白天工作时段 raw 率应归零，需抽查验证（`SELECT count(*) FROM llm_conversation WHERE created_at > now()-interval '24h' AND prompt_text::text LIKE '{"raw":%'`）

### 训练数据管道（已完成，2026-08-19）
- `cmd/build-training-data` + `internal/training`：从 llm_conversation 组装 OpenAI messages 训练样本
- 全量产出 **15,364 条样本**（`/opt/llm-platform-build/train-data/train.jsonl`，1.56GB）
- 清洗：脱敏残留→`<REDACTED>`、system-reminder 剥离、按 request_body_hash 去重；system 经 hash JOIN system_prompts（99% 覆盖）；工具轮占 85%
- 复跑：`/opt/llm-platform-build/btd-linux --db-url <URL> --out <file> [--days N]`（增量用 `--days`，全量 6.5 分钟）
- **下一步**：第 2 步合规确认 → 第 3 步小规模 SFT 验证（2-3K 样本 LoRA）

### 合规确认（第 2 步，2026-08-20 进行中）
**PII 审计发现（训练集 15,364 条）**：
1. **网关 email 脱敏正则漏洞**：`[\w.]+` 不含连字符 → 带连字符域名邮箱（`liulei@yuexin-logistics.com` / `yisong@wiser-bridge.com`）**全部漏脱敏** → 训练集 3,010 条明文邮箱（assistant 498 + tool 2512）
2. 内网 IP 明文 12,351 条（tool 结果/代码输出）；手机号 20 条（多为订单号误报）
3. 身份证 18 位数字 310 条命中——**审计确认多为订单号/编码，非身份证**（未处理，误伤 > 收益）

**已修复（commit `9b13d5c`）**：
- `internal/audit/redact.go`：email 正则加连字符 `[\w.-]+`（新数据落库生效）
- `internal/training/pii.go`：训练集输出前**强制二次清洗**（邮箱→`<EMAIL>`、手机→`<PHONE>`、内网 IP→`<LAN_IP>`），覆盖所有消息 + tool_calls arguments（存量/新数据统一，不依赖网关修复）
- **复审计（16,442 样本）：email=0 / phone=0 / ip_lan=0 全部归零**

**仍需人工/法务决策（未处理）**：
- [ ] 员工对话数据用于训练的**授权确认**（8 个活跃用户，需 HR/法务审批流程）
- [ ] 公司数据分级政策核验（LLM_AUDIT_TTL_DAYS=90、训练用途是否在合规范围）
- [ ] 训练集含**公司内部业务信息**（项目名/内部系统名/业务数据）——内部训练可接受，外部训练/开源**禁止**
- [ ] 复跑训练集后建议人工抽检 20-50 条样本确认无异常

### 优先级中
- [ ] **平台层展示 `usage_estimated`**：对话详情/统计标注"估算"（列已存在，平台未消费）
- [ ] **提取器水位表**：cron 每次固定重扫 57 条无价值记录（`max(conv_id)` 不推进），当前 1 秒无害，可加独立水位表彻底消除
- [ ] **存量 raw 转结构化回填**（可选）：`cmd/repair-prompt-backfill`（dry-run 85% 可修复；注意全量 UPDATE 慢，建议分批/低峰）

### 优先级低 / 观察
- [ ] mimo-v2.5/kimi-k3 渠道配置（New API 侧，503 model_not_found）
- [ ] GLM 上游稳定性观察（08-10/11 断开高峰）
- [ ] 服务器 `/opt/llm-gateway-build/`、`/opt/llm-platform-build/` 构建中间文件清理

---

## 七、关键命令速查

```bash
# SSH
ssh scm "<cmd>"

# 沉淀库查询（容器内 psql，URL 从 .env）
DBURL=$(grep -E "^CONTEXT_DB_URL=" /opt/llm-gateway/.env | cut -d= -f2-)
docker exec 1Panel-postgresql-AWYE psql "$DBURL" -c "SELECT ..."

# 网关指标
TOKEN=$(grep -E "^METRICS_AUTH_TOKEN=" /opt/llm-gateway/.env | cut -d= -f2-)
curl -H "Authorization: Bearer $TOKEN" http://127.0.0.1:8080/metrics

# 手动跑知识提取（增量）
/opt/llm-platform-build/knowledge-extract-linux --db-url "$(grep CONTEXT_DB_URL= /opt/llm-gateway/.env | cut -d= -f2- | sed -E 's|1Panel-postgresql-AWYE:5432|127.0.0.1:15432|')" --from-last

# 网关重启（新镜像构建：本地交叉编译 → scp → /opt/llm-gateway-build/ 下 docker build -f Dockerfile.quick）
# 平台登录：https://platform.wtlhs.com（凭据在 /opt/llm-platform/.env）
```

### 本地开发（Windows, D:\GitHubProjects\NewApiLog）
```powershell
# 交叉编译 linux 二进制（部署用，注意设置环境变量）
$env:CGO_ENABLED="0"; $env:GOOS="linux"; $env:GOARCH="amd64"
go build -trimpath -ldflags "-s -w" -o llm-gateway-linux ./cmd/gateway

go test ./...      # 全量测试
make migrate-up    # goose 迁移（本地）
```

---

## 八、代码结构与关键文件

```
cmd/gateway/            网关入口
cmd/platform/           平台入口
cmd/knowledge-extract/  知识提取 CLI（--from-last 增量）
cmd/repair-prompt-backfill/  存量 raw 转结构化工具（可选）
internal/audit/         捕获记录核心（record.go: safeJSON/repairJSON/估算兜底; system_split.go: agent 名单; anthropic_aggregator.go: usage）
internal/gateway/       透明代理（proxy.go: ctx 透传; resp_wrap.go: 流中断诊断; usage_inject.go; body_snapshot.go）
internal/knowledge/     知识提取（extract.go: 宽容化解析; store.go: StreamSources 起始 id）
internal/db/            store.go + migrations/（0001~0007）
internal/platform/      平台后端（embed.go: go:embed web_dist）
web/                    平台前端（React+Vite, /api 相对路径同源）
docs/                   DESIGN.md / PLATFORM_DESIGN.md / KNOWLEDGE_LAYER.md / DEPLOY.md / TIMEZONE.md
```

**给接手 agent 的阅读建议**：先读本文档，再按需读 `DESIGN.md`（架构决策）、`DEPLOY.md`（部署 Runbook）、
`docs/KNOWLEDGE_LAYER.md`（知识资产分层）。改代码后跑 `go test ./...`；部署按第七节流程。
