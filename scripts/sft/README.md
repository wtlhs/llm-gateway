# 第 3 步实验：小规模 LoRA SFT（agent 行为对齐）

> 目标：用 2,500 条公司真实 agent 对话样本验证「数据可用性 + 格式遵循 + 工具调用能力」，
> 非完整产品训练。通过后扩大规模（16,442 条全量 + 持续增量）。

## 硬件与环境

- **GPU**：建议 1× 16GB+（A100 / L40S / 4090 等）。当前服务器与本地开发机**均无 GPU**，
  需在云 GPU 或专用机器执行（数据已就绪，仅需拷贝训练集）
- Python 3.10+，CUDA 环境

## 数据来源

- 训练集：`/opt/llm-platform-build/train-data/train.jsonl`（服务器，16,442 条，1.68GB，
  已 PII 清洗：email/phone/内网 IP 归零，commit `9b13d5c`）
- 训练前**合规前提**：员工授权 + 数据分级确认（见 HANDOVER「合规确认」章节）——训练仅限内部

## 快速开始

```bash
# 1. 获取数据(在 GPU 机器)
scp root@82.156.138.160:/opt/llm-platform-build/train-data/train.jsonl ./

# 2. 准备实验数据(分层抽样: 长度<=8K tokens 优先, 工具轮>=50%)
python prepare_dataset.py --input train.jsonl --out-dir ./data \
    --train-n 2500 --eval-n 200 --max-len 8192 --tool-ratio 0.5

# 3. 安装 LLaMA-Factory(任选)
#    pip install "llamafactory[torch]"
#    或 git clone https://github.com/hiyouga/LLaMA-Factory.git && cd LLaMA-Factory && pip install -e .

# 4. 训练(默认 Qwen2.5-Coder-7B-Instruct, LoRA r16)
bash train_lora.sh          # 环境变量可覆盖: BASE_MODEL/EPOCHS/LR/MAX_LEN 等

# 5. 评测(工具调用格式遵循率 + 回答质量抽样)
python evaluate.py --model ./output/qwen-sft --data ./data/eval.json --sample-n 20
```

## 参数说明（train_lora.sh 可覆盖）

| 参数 | 默认 | 说明 |
|---|---|---|
| `BASE_MODEL` | `Qwen/Qwen2.5-Coder-7B-Instruct` | 编码底座；可选 `Qwen/Qwen3-8B` |
| `MAX_LEN` | 8192 | 训练截断长度（数据集 p50=10K，42% 超 8K——prepare 已优先抽样短样本） |
| `EPOCHS` | 3 | 小数据量 2-4 轮即可 |
| `LR` | 2e-4 | LoRA 标准学习率 |
| `LORA_R/ALPHA` | 16/32 | |
| `CUTOFF` | 0.9 | 预留 10% 不训练用于训练内对比 |

## 评测解读

| 指标 | 达标线 | 说明 |
|---|---|---|
| 训练 loss 收敛 | 显著低于初始（对比 CUTOFF 预留集） | 数据可学习 |
| 工具调用格式遵循率 | ≥90% | `[TOOL_CALL] name({json})` 标记完整 |
| 回答非空率 | ≥95% | 无空回复 |
| 生成抽样 | 人工审查 5-20 条 | 中文回答连贯、无 PII 泄漏（应输出 `<EMAIL>` 等占位符而非明文） |

## 通过标准（下一步决策）

1. loss 收敛 + 工具格式遵循 ≥90% → **扩大规模**：全量 16,442 条（`--train-n 15000`），
   或持续增量训练
2. 任一项不达标 → 回到数据管道调优（抽样策略 / max_len / 清洗规则）
3. 完整编码能力评测（HumanEval 等）不在本步，属后续

## 已知限制

- 样本为单轮请求-响应对（含多轮历史），跨会话长程一致性无法验证
- 工具调用以文本标记呈现（`[TOOL_CALL]`），非原生 function-calling 训练
- 内网 IP/业务数据已清洗，但**公司业务语义保留**——严禁模型/数据外发
