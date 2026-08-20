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

### 3.6 safeJSON 修复链收敛（08-17 ~ 08-19，三轮真实数据驱动）
raw 率每轮都基于**真实流量定位新非法模式**后收敛，最终归零：
| 轮次 | 非法模式 | 根因 | 修复 |
|---|---|---|---|
| 1 | `"maximum":*` / `None`/`True`/`False` | Claude Code 工具 schema Python 风格值 | `repairJSON` 状态机（08-17 `27e4586`） |
| 2 | `"maximum":****4321` | 网关 redact 脱敏把数字替换成星号串+数字尾，状态机只处理单 `*` | 吞星号串+数字尾（含 `-****4321`）→ null（08-18 随提取器修复同批 `c9e6408` 导出 `RepairJSONScan`） |
| 3 | `\a***@example.com` | redact 脱敏邮箱产出 `\a`（非法 JSON 转义，`\` 后只能跟 `" \ / b f n r t u`） | 延迟写入反斜杠：合法转义保留、非法丢弃（08-19 `47ab1ef`，已部署） |
**验证**：08-19 04:05Z 新网关后 1,179+ 条记录 **raw=0.000%**，长时间窗口稳定。
**后续（08-20）**：发现 176 条截断记录（Claude Code `max_tokens:64000` 上下文请求，~850KB 即触发截断）→
`LLM_AUDIT_MAX_BODY_BYTES` 1MB → **4MB**（`4194304`，.env + 重启生效），900KB 请求实测不再截断（truncated=f 完整落库）。

### 3.7 训练数据管道与合规（08-19 ~ 08-20）
- `cmd/build-training-data` + `internal/training`：训练语料构建（16,442 条，详见第五章）
- **PII 合规审计发现并修复**（详见 3.8 / 第五章）
- `scripts/sft/`：第 3 步 SFT 实验脚本（prepare/train/evaluate/README）

### 3.8 合规 PII 审计与修复（08-20）
**审计发现（训练集 15,364 条）**：
1. **网关 email 脱敏正则 `[\w.]+` 不含连字符** → 带连字符域名邮箱（`liulei@yuexin-logistics.com`/`yisong@wiser-bridge.com`）**漏脱敏**，训练集 3,010 条明文（assistant 498 + tool 2512）
2. 内网 IP 明文 12,351 条（tool 结果/代码输出）
3. 手机号 20 条（多为订单号误报）；"身份证"310 条命中经确认均为订单号/编码（未处理）

**修复（双防线，commit `9b13d5c`）**：
- 网关层：`redact.go` email 正则 `\b[\w.-]+@[\w.-]+\.\w+\b`（新数据生效）
- 训练层：`internal/training/pii.go` 输出前强制二次清洗（邮箱→`<EMAIL>`、手机→`<PHONE>`、内网 IP→`<LAN_IP>`，覆盖消息 + tool_calls arguments，存量/新数据统一）
- **复审计（16,442 样本）：email=0 / phone=0 / ip_lan=0 全部归零**

---

## 四、数据现状（08-18 快照）

### 4.1 沉淀库 `llm_conversation`（PG `llm_gateway`）
| 指标 | 值 |
|---|---|
| 总量 | ~33,700 条（request_id 唯一） |
| 日均 | ~1,900 条 |
| caller_name 填充 | 98.9%（部署后新数据） |
| usage 覆盖 | 100%（~68% 为估算，`usage_estimated=true`） |
| 截断 | ~10% 存量；**08-20 起阈值调至 4MB**（`LLM_AUDIT_MAX_BODY_BYTES=4194304`），仅 >4MB 超极端请求截断 |
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

## 五、训练大模型评估与执行进度（08-18 评估，08-20 更新）

**数据画像**：31,617 条有效对话、16,442 条训练样本（`build-training-data` 产出）、
工具调用场景 47.4%（训练集工具轮 85%）、领域 = 软件工程 coding agent 交互、中英混合。

**评级：B+（PII 清洗后，可用于领域后训练）**

**核心判断**：
1. 数据是 **agent 行为轨迹**，不是通用语料 → 适合 **领域 SFT（编码 agent 对齐）/ RLHF 偏好对**，不适合通用预训练/基座
2. **`knowledge_pairs` 不能直接当训练数据**：其过滤标准（去中间过程）是知识检索导向；训练需要**完整对话轨迹（含 tool_calls/thinking）**，数据源是清洗后的 `llm_conversation`
3. 规模：16,442 条起步 + 15 分钟增量（年化 ~50 万条）
4. **合规**：PII 已技术侧清零（email/phone/内网 IP = 0）；**人工授权确认待办**（员工授权/数据分级）

**执行进度**：
- ✅ **第 1 步 训练数据管道**（08-19）：`cmd/build-training-data` + `internal/training`，产出 16,442 条（PII 清洗版 `train.jsonl`，1.68GB）
- ✅ **第 2 步 合规技术侧**（08-20）：PII 审计 + 双防线修复 + 复审计归零；**人工决策待办见第六章**
- ✅ **第 3 步脚本**（08-20）：`scripts/sft/`（prepare/train/evaluate/README）已就绪
- ⏳ **第 3 步执行**：待 GPU 硬件（采购 DGX Spark GB10 已决策）→ 见第九章《训练执行手册》
- ⬜ **第 4 步 规模化**：第 3 步验收通过后全量训练 + 持续增量 + 可选 RLHF 偏好对

---

## 六、遗留问题与后续任务清单（给接手 agent）

### 已完成（本批）
- ✅ 训练数据管道（第 1 步，08-19）：16,442 条 `train.jsonl`（PII 清洗版）
- ✅ 合规技术侧（第 2 步技术部分，08-20）：PII 审计+双防线修复+复审计归零
- ✅ 第 3 步实验脚本（08-20）：`scripts/sft/`（prepare/train/evaluate/README）
- ✅ raw 率归零（08-19 起 1,179+ 条 0.000%，三轮修复链收敛）
- ✅ 知识提取定时化（cron */15，召回率 18.8%，knowledge_pairs 1,340+ 对）

### 待执行（按优先级）
- [ ] **合规人工决策（第 2 步剩余，训练前置硬门槛）**：
  1. 员工对话数据**授权确认**（8 个活跃用户，HR/法务流程）
  2. **公司数据分级核验**（训练用途是否合规范围；内部训练可接受，外部/开源禁止）
  3. 训练集含公司内部业务信息——禁止模型/数据外发
  4. 建议人工抽检 20-50 条样本
- [ ] **第 3 步执行**：DGX Spark 到货后按第九章《训练执行手册》跑通小规模 SFT（预计 5-15 小时/轮）
- [ ] **第 4 步规模化**：验收通过后全量 16,442 条 + 持续增量训练（15 分钟增量管道已在跑）
- [ ] **平台层展示 `usage_estimated`**：对话详情/统计标注"估算"（列已存在，平台未消费）
- [ ] **提取器水位表**：cron 每次固定重扫少量无价值记录（`max(conv_id)` 不推进），当前 1 秒无害，可加独立水位表
- [ ] **存量 raw 转结构化回填**（可选）：`cmd/repair-prompt-backfill`（dry-run 85% 可修复；UPDATE 慢建议分批，当前提取器已动态修复不依赖）
- [ ] **mimo-v2.5/kimi-k3 渠道配置**（New API 侧，503 model_not_found）
- [ ] **GLM 上游稳定性观察**（08-10/11 出现过断开高峰，已回落）
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

---

## 九、训练执行手册（DGX Spark GB10，2026-08-20 采购决策）

> 目标：在第 3 步用小规模 LoRA SFT 验证数据可用性与 agent 行为对齐效果。
> 前置条件：**合规人工决策完成**（第六章待办 1-3）方可执行训练。

### 9.1 硬件与预期（基于我们实际负载估算）
| 项 | DGX Spark (GB10) | 说明 |
|---|---|---|
| GPU | Blackwell，BF16 密集 ~40-60 TFLOPS | 峰值 1 PFLOP 是 FP4 稀疏宣传值，训练看 BF16 |
| 内存 | 128GB 统一内存（273GB/s） | 7B/14B LoRA 富余；可跑 32B QLoRA、70B 4bit 推理 |
| 训练吞吐 | ~500-1,500 tokens/s（带宽瓶颈） | 2,500 样本 × ~4K tokens × 3 epochs ≈ 3,000 万 tokens |
| **预计单轮实验** | **5-15 小时**（夜间跑，白天分析） | 全量 16,442 条约 2-5 天 |
| 合规 | 数据不出内网 | 满足"内部训练、禁止外发"约束（优于云 GPU） |

### 9.2 环境准备（一次性）
```bash
# 1. 系统: DGX OS(预装) 或 Ubuntu + NVIDIA 驱动 + CUDA
# 2. NGC 容器(推荐)或原生 Python:
#    docker run --gpus all -it --shm-size 32g nvcr.io/nvidia/pytorch:24.xx-py3
# 3. 训练框架:
pip install "llamafactory[torch]"      # 或 git clone https://github.com/hiyouga/LLaMA-Factory
pip install transformers peft accelerate
# 4. 模型(首次运行自动下载, 国内可配 HF_ENDPOINT=https://hf-mirror.com):
#    Qwen/Qwen2.5-Coder-7B-Instruct
```

### 9.3 数据迁移（从服务器）
```bash
# 在 DGX Spark 上(约 1.7GB, 内网/直连):
scp root@82.156.138.160:/opt/llm-platform-build/train-data/train.jsonl ./
# 代码: git clone https://github.com/wtlhs/llm-gateway 或拷贝 scripts/sft/
```

### 9.4 执行（三步）
```bash
# 1) 准备: 分层抽样(长度<=8K 优先 + 工具轮>=50%) + sharegpt 格式
python prepare_dataset.py --input train.jsonl --out-dir ./data \
    --train-n 2500 --eval-n 200 --max-len 8192 --tool-ratio 0.5

# 2) 训练(默认 Qwen2.5-Coder-7B-Instruct, LoRA r16; 环境变量可覆盖)
BASE_MODEL=Qwen/Qwen2.5-Coder-7B-Instruct MAX_LEN=8192 EPOCHS=3 bash train_lora.sh

# 3) 评测: 工具格式遵循率 + 回答非空率 + 生成抽样
python evaluate.py --model ./output/qwen-sft --data ./data/eval.json --sample-n 20
```

### 9.5 验收标准与决策树
| 指标 | 达标线 |
|---|---|
| 训练 loss 收敛（对比 `CUTOFF=0.9` 预留集） | 显著低于初始 |
| 工具调用格式遵循率（`[TOOL_CALL] name({json})`） | ≥90% |
| 回答非空率 | ≥95% |
| 生成抽样人工审查 | 中文连贯、无 PII 明文（应为 `<EMAIL>`/`<PHONE>` 占位符） |

```
达标 → 第 4 步: 全量 16,442 条(--train-n 15000) + 15 分钟增量定期复训
不达标 → 回数据管道调优: 抽样策略/max_len/清洗规则; 或换底座(Qwen3-8B/GLM-4-9B)
```

### 9.6 常见问题排查（参考本批历史问题）
| 现象 | 处理 |
|---|---|
| 训练显存不足 | 降低 `MAX_LEN`（数据 p50=10K tokens，prepare 已优先抽 ≤8K）或 `BATCH`/`GRAD_ACC` |
| 数据加载报格式错 | 确认 `train.json` 为 sharegpt `conversations` 格式（prepare 已生成）；LLaMA-Factory 需 `dataset_info.json` 登记 |
| 模型输出含 `<EMAIL>` 占位符 | 正常（PII 清洗预期行为）；若含明文邮箱 → 训练集泄漏，停止并查 pii 清洗 |
| 训练太慢 | 接受（GB10 带宽瓶颈）；或减小 `MAX_LEN`/样本量先验证收敛 |
| 长上下文截断丢尾部 | 属预期（`cutoff_len` 截断）；如需提升可调大 `MAX_LEN`（16K 时 60% 样本可全量入） |

---
