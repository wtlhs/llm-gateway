#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""第 3 步实验: 训练数据准备脚本
从 train.jsonl 抽样小规模 SFT 数据集(LLaMA-Factory sharegpt 格式)。

策略(第一性原理):
- 训练集 p50=10K tokens、42% 超 8K → 按 max_seq_len 分层抽样, 保证可训练
- 工具轮样本保留 >= tool_ratio(agent 行为特征是核心价值)
- tool_calls 序列化为文本标记 [TOOL_CALL] name(args_json), 便于格式评测

用法:
  python prepare_dataset.py --input train.jsonl --out-dir ./data \
      [--train-n 2500] [--eval-n 200] [--max-len 8192] [--tool-ratio 0.5]
"""
import argparse
import json
import random
import sys


def est_tokens(text: str) -> int:
    """粗估 tokens(中英混合 ~3 字符/token)。"""
    return max(1, len(text) // 3)


def serialize_messages(messages) -> list:
    """messages -> LLaMA-Factory sharegpt conversations([{from,value}]),
    tool_calls 序列化为 assistant 文本标记。"""
    out = []
    for m in messages:
        role = m.get("role", "user")
        content = m.get("content", "")
        tc = m.get("tool_calls") or []
        if tc:
            parts = [content] if content else []
            for call in tc:
                fn = call.get("function", {})
                parts.append(f"[TOOL_CALL] {fn.get('name','')}({fn.get('arguments','')})")
            content = "\n".join(p for p in parts if p)
        if role == "tool":
            out.append({"from": "tool", "value": content or "<tool_result>"})
        elif role == "system":
            out.append({"from": "system", "value": content})
        elif role == "assistant":
            out.append({"from": "assistant", "value": content})
        else:
            out.append({"from": "human", "value": content})
    return out


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--input", required=True, help="train.jsonl 路径")
    ap.add_argument("--out-dir", default="./data")
    ap.add_argument("--train-n", type=int, default=2500)
    ap.add_argument("--eval-n", type=int, default=200)
    ap.add_argument("--max-len", type=int, default=8192, help="目标最大 tokens")
    ap.add_argument("--tool-ratio", type=float, default=0.5, help="工具轮样本占比下限")
    ap.add_argument("--seed", type=int, default=42)
    args = ap.parse_args()

    random.seed(args.seed)
    all_rows = []
    with open(args.input, encoding="utf-8") as f:
        for line in f:
            d = json.loads(line)
            text = json.dumps(d, ensure_ascii=False)
            has_tool = any(m.get("role") in ("tool", "assistant") and (m.get("tool_calls") or m.get("role") == "tool")
                           for m in d["messages"])
            all_rows.append({"data": d, "tokens": est_tokens(text), "has_tool": has_tool})

    total = len(all_rows)
    print(f"输入样本: {total}, 工具轮占比: {100*sum(1 for r in all_rows if r['has_tool'])//total}%")

    # 分层抽样: 优先 <= max_len, 剩余不足再从 >max_len 补
    short = [r for r in all_rows if r["tokens"] <= args.max_len]
    long = [r for r in all_rows if r["tokens"] > args.max_len]
    need = args.train_n + args.eval_n
    if len(short) < need:
        print(f"警告: <=max_len({args.max_len}) 样本 {len(short)} < 需求 {need}, 将补充长样本")
        picked = short + random.sample(long, min(len(long), need - len(short)))
    else:
        # 工具轮分层: 保证 tool_ratio
        st = [r for r in short if r["has_tool"]]
        sn = [r for r in short if not r["has_tool"]]
        tool_need = int(need * args.tool_ratio)
        tool_pick = random.sample(st, min(len(st), tool_need))
        no_tool_need = need - len(tool_pick)
        no_tool_pick = random.sample(sn, min(len(sn), no_tool_need))
        picked = tool_pick + no_tool_pick
        random.shuffle(picked)

    random.shuffle(picked)
    train = picked[: args.train_n]
    eval_ = picked[args.train_n : args.train_n + args.eval_n]

    import os
    os.makedirs(args.out_dir, exist_ok=True)
    with open(os.path.join(args.out_dir, "train.json"), "w", encoding="utf-8") as f:
        for r in train:
            d = r["data"]
            f.write(json.dumps({
                "conversations": serialize_messages(d["messages"]),
            }, ensure_ascii=False) + "\n")
    with open(os.path.join(args.out_dir, "eval.json"), "w", encoding="utf-8") as f:
        for r in eval_:
            d = r["data"]
            f.write(json.dumps({
                "conversations": serialize_messages(d["messages"]),
            }, ensure_ascii=False) + "\n")

    def stats(name, rows):
        toks = [r["tokens"] for r in rows]
        tools = sum(1 for r in rows if r["has_tool"])
        print(f"{name}: n={len(rows)} tool={100*tools//max(1,len(rows))}% "
              f"tokens p50={sorted(toks)[len(toks)//2]} max={max(toks)}")

    stats("train", train)
    stats("eval ", eval_)
    print(f"输出: {os.path.join(args.out_dir, 'train.json')} / eval.json")


if __name__ == "__main__":
    main()
