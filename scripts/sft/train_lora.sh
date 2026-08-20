#!/usr/bin/env bash
# 第 3 步实验: LoRA SFT 训练入口(LLaMA-Factory)
#
# 硬件要求: 建议 1x GPU >= 16GB(A100/L40S/4090 等); 无 GPU 机器请用云 GPU
# 底座默认: Qwen/Qwen2.5-Coder-7B-Instruct(编码底座, LoRA 友好, 中文强)
#
# 步骤:
#   1. 准备数据: python prepare_dataset.py --input <train.jsonl> --out-dir ./data
#   2. 安装 LLaMA-Factory: pip install "llamafactory[torch]" 或 clone 仓库
#   3. 运行: bash train_lora.sh
#   4. 评测: python evaluate.py --model ./output/qwen-sft/merged
set -euo pipefail

# ===== 可配置 =====
BASE_MODEL="${BASE_MODEL:-Qwen/Qwen2.5-Coder-7B-Instruct}"   # 或 Qwen/Qwen3-8B
DATA_DIR="${DATA_DIR:-./data}"
OUTPUT_DIR="${OUTPUT_DIR:-./output/qwen-sft}"
MAX_LEN="${MAX_LEN:-8192}"
EPOCHS="${EPOCHS:-3}"
LR="${LR:-2e-4}"
BATCH="${BATCH:-4}"          # per device
GRAD_ACC="${GRAD_ACC:-8}"     # 等效 batch = BATCH*GRAD_ACC*GPU数
LORA_R="${LORA_R:-16}"
LORA_ALPHA="${LORA_ALPHA:-32}"
CUTOFF="${CUTOFF:-0.9}"       # 数据比例(预留 10% 不训练可对比)

# ===== 训练(LLaMA-Factory CLI) =====
# 若未安装, 先: pip install "llamafactory[torch]" / git clone https://github.com/hiyouga/LLaMA-Factory.git
llamafactory-cli train \
  --model_name_or_path "${BASE_MODEL}" \
  --stage sft \
  --finetuning_type lora \
  --dataset_dir "${DATA_DIR}" \
  --dataset train \
  --val_size "${CUTOFF}" \
  --template qwen \
  --cutoff_len "${MAX_LEN}" \
  --max_samples 100000 \
  --preprocessing_num_workers 8 \
  --output_dir "${OUTPUT_DIR}" \
  --per_device_train_batch_size "${BATCH}" \
  --gradient_accumulation_steps "${GRAD_ACC}" \
  --lr_scheduler_type cosine \
  --learning_rate "${LR}" \
  --num_train_epochs "${EPOCHS}" \
  --logging_steps 10 \
  --save_steps 200 \
  --save_total_limit 2 \
  --eval_steps 200 \
  --eval_strategy steps \
  --load_best_model_at_end True \
  --bf16 True \
  --lora_rank "${LORA_R}" \
  --lora_alpha "${LORA_ALPHA}" \
  --lora_dropout 0.05 \
  --lora_target all \
  --report_to none

echo "===== LoRA 训练完成: ${OUTPUT_DIR} ====="
echo "下一步: python evaluate.py --model ${OUTPUT_DIR} --data ./data/eval.json"
