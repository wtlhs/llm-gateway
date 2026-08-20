#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""第 3 步实验: SFT 结果评测脚本

维度:
1. 工具调用格式遵循率([TOOL_CALL] name(args_json) 标记完整性)
2. 回答非空率 / 平均长度
3. 训练前后对比(需 BASE_MODEL 与 SFT 模型都可用时)
4. 生成抽样输出(人工审查)

用法:
  python evaluate.py --model <SFT模型路径> --data ./data/eval.json \
      [--base-model <原模型>] [--max-new 256] [--sample-n 20]
"""
import argparse
import json
import re

TOOL_CALL_RE = re.compile(r"\[TOOL_CALL\]\s*([A-Za-z_][A-Za-z0-9_]*)\((\{.*\}|.*?)\)")


def load_data(path):
    with open(path, encoding="utf-8") as f:
        return [json.loads(line) for line in f]


def build_prompt(conv):
    """sharegpt conversations -> 提示文本(最后一条 human 前的内容)。"""
    lines = []
    for m in conv:
        frm, val = m["from"], m["value"]
        if frm == "system":
            lines.append(f"System: {val}")
        elif frm == "human":
            lines.append(f"User: {val}")
        elif frm == "assistant":
            lines.append(f"Assistant: {val}")
        elif frm == "tool":
            lines.append(f"Tool result: {val[:200]}")
    return "\n".join(lines)


def check_tool_format(text):
    """检查 assistant 输出是否含格式良好的 [TOOL_CALL]。返回 (count, ok)。"""
    found = TOOL_CALL_RE.findall(text)
    if not found:
        return 0, False
    # 要求 name 非空且 arguments 能解析为 JSON 或以 { 开头
    ok = all(name and args.strip().startswith("{") for name, args in found)
    return len(found), ok


def evaluate_model(model, data, max_new=256, sample_n=20):
    """用 transformers 加载模型生成评测(需 GPU/足够内存)。"""
    from transformers import AutoModelForCausalLM, AutoTokenizer
    import torch

    tok = AutoTokenizer.from_pretrained(model, trust_remote_code=True)
    m = AutoModelForCausalLM.from_pretrained(model, trust_remote_code=True, torch_dtype=torch.bfloat16, device_map="auto")
    if tok.pad_token is None:
        tok.pad_token = tok.eos_token

    stats = {"n": 0, "tool_format_ok": 0, "tool_calls": 0, "nonempty": 0, "chars": 0}
    samples = []
    for i, row in enumerate(data):
        if i >= sample_n:
            break
        prompt = build_prompt(row["conversations"])
        inputs = tok(prompt, return_tensors="pt", truncation=True, max_length=2048).to(m.device)
        out = m.generate(**inputs, max_new_tokens=max_new, do_sample=True, temperature=0.7, top_p=0.9)
        gen = tok.decode(out[0][inputs.input_ids.shape[1]:], skip_special_tokens=True)
        stats["n"] += 1
        if gen.strip():
            stats["nonempty"] += 1
            stats["chars"] += len(gen)
        ncall, ok = check_tool_format(gen)
        stats["tool_calls"] += ncall
        if ok:
            stats["tool_format_ok"] += 1
        if i < 5:
            samples.append({"i": i, "gen": gen[:400]})
    return stats, samples


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--model", required=True)
    ap.add_argument("--data", required=True)
    ap.add_argument("--max-new", type=int, default=256)
    ap.add_argument("--sample-n", type=int, default=20)
    args = ap.parse_args()

    data = load_data(args.data)
    print(f"加载 eval 数据: {len(data)} 条")

    stats, samples = evaluate_model(args.model, data, args.max_new, args.sample_n)
    n = max(1, stats["n"])
    print("\n===== 评测结果 =====")
    print(f"生成样本数: {stats['n']}")
    print(f"回答非空率: {100 * stats['nonempty'] // n}%")
    print(f"平均回答长度: {stats['chars'] // n} 字符")
    print(f"工具调用标记总数: {stats['tool_calls']}")
    print(f"工具调用格式遵循率: {100 * stats['tool_format_ok'] // n}%")
    print("\n===== 生成抽样(前 5 条) =====")
    for s in samples:
        print(f"--- sample {s['i']} ---")
        print(s["gen"])
        print()


if __name__ == "__main__":
    main()
