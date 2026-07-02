# 时区规范

> **目的**:沉淀库采用 UTC 存储(符合 timestamptz 最佳实践),但企业用户在北京时间(CST/UTC+8)工作。
> 本文档记录时区设计决策,并给出 Phase 2 时间分析的正确写法,避免"按天统计错 8 小时"的常见陷阱。

---

## 一、当前设计(已验证正确)

### 1.1 存储层

| 项 | 值 | 说明 |
|----|-----|------|
| 列类型 | `TIMESTAMPTZ` | timestamp with time zone,存 UTC 绝对时刻 |
| `created_at` 来源 | `DEFAULT now()` | 由 PG 服务器填充,**代码不写入** |
| PG `timezone` | `UTC` | PG 服务器时区配置 |
| PG `now()` 行为 | 返回 UTC 时刻 | 无论 timezone 怎么设,timestamptz 存的都是 UTC |

**已验证**:插入一条记录后,`created_at::text` = `2026-07-02 12:16:55+00`(UTC),绝对时刻正确。

### 1.2 读取层(pgx)

pgx 读取 `timestamptz` 时,自动转换成 Go 进程的 Local 时区显示。

```
PG 存储:        2026-07-02 12:16:55+00  (UTC)
pgx 读回(Go):  2026-07-02 20:16:55 CST  (北京时间, Local)
```

这是标准行为,**不是 bug**——绝对时刻一致,只是显示时区不同。

### 1.3 TTL 清理(已验证正确)

`internal/cleanup/ttl.go`:
```go
cutoff := time.Now().AddDate(0, 0, -ttlDays)  // 带 Local 时区
store.DeleteOlderThan(ctx, cutoff)             // 传给 PG 比较
```

PG 的 `created_at < $1` 比较的是**绝对时刻**(epoch 秒),cutoff 带什么时区都能正确比较。**无 bug**。

---

## 二、潜在风险:Phase 2 按天统计

⚠️ **这是将来最容易踩的坑**:DB 存 UTC,但企业用户在北京时间工作。

### 2.1 错误写法(会归错日期)

```sql
-- ❌ 用 UTC 切天: 北京时间 0-8 点的记录被归到前一天
SELECT created_at::date AS day, count(*)
FROM llm_conversation
GROUP BY 1;
```

**后果**:
- 北京时间 `2026-07-02 08:00` 对应 UTC `2026-07-02 00:00` —— 归 7/2 ✓
- 北京时间 `2026-07-02 07:00` 对应 UTC `2026-07-01 23:00` —— 归 7/1 ✗(错!)
- **每天有 8 小时(北京 00:00-08:00)的记录归错日期**
- "今天的对话量"会偏低,早高峰流量被算到昨天

### 2.2 正确写法(按北京时间切天)

```sql
-- ✅ 先转北京时区再切天
SELECT (created_at AT TIME ZONE 'Asia/Shanghai')::date AS day, count(*)
FROM llm_conversation
GROUP BY 1
ORDER BY 1;
```

### 2.3 常见分析场景的写法对照

| 场景 | ❌ 错误(UTC) | ✅ 正确(北京时间) |
|------|--------------|-------------------|
| 按天统计 | `created_at::date` | `(created_at AT TIME ZONE 'Asia/Shanghai')::date` |
| 按小时统计 | `EXTRACT(HOUR FROM created_at)` | `EXTRACT(HOUR FROM created_at AT TIME ZONE 'Asia/Shanghai')` |
| 今日数据 | `created_at >= current_date` | `created_at >= current_date AT TIME ZONE 'Asia/Shanghai'` |
| 近 N 天 | `created_at > now() - interval '7 days'` | `created_at > now() - interval '7 days'`(✅ 这个没问题,绝对时刻比较) |
| 按周/月 | `date_trunc('week', created_at)` | `date_trunc('week', created_at AT TIME ZONE 'Asia/Shanghai')` |

> **记忆口诀**:凡是要"切天/切小时/切周月"(取整到某个时间粒度)的,必须先 `AT TIME ZONE 'Asia/Shanghai'` 转北京时区;
> 凡是"区间比较"(>、<、between)的,直接用 timestamptz 比较绝对时刻即可,无需转时区。

---

## 三、时区函数速查

```sql
-- 当前 UTC 时刻
SELECT now();
-- 当前北京时间(datetime, 无时区)
SELECT now() AT TIME ZONE 'Asia/Shanghai';
-- 当前北京日期
SELECT (now() AT TIME ZONE 'Asia/Shanghai')::date;
-- 北京今日 0 点(作为查询边界)
SELECT date_trunc('day', now() AT TIME ZONE 'Asia/Shanghai');
```

---

## 四、为什么不在代码里加冗余时区字段

考虑过加 `created_at_local` 冗余字段,但**否决**,理由:

1. **timestamptz 已是最佳实践**:存 UTC,读时按需转换,单一真相源
2. **冗余字段会不一致**:一旦 Go 代码时区或服务器迁移,冗余字段与 UTC 失同步
3. **PG 的 `AT TIME ZONE` 运行时计算零成本**:索引 + 表达式可优化,无需预存
4. **真正风险只在分析 SQL 忘了转时区**:文档护栏(本文档)即可解决

---

## 五、验证方法

部署后或写新分析 SQL 时,用这条快速验证时区是否正确:

```sql
-- 应返回北京时间日期(如 2026-07-02), 非 UTC 日期
SELECT
    created_at::date AS utc_date,                           -- ❌ UTC 切天
    (created_at AT TIME ZONE 'Asia/Shanghai')::date AS cn_date,  -- ✅ 北京切天
    count(*)
FROM llm_conversation
WHERE created_at > now() - interval '1 day'
GROUP BY 1, 2;
```

如果 `utc_date` 和 `cn_date` 不同(差 1 天),说明跨天边界记录存在,按天统计必须用 `cn_date` 那列。
