# 改造方案：TokenReader 支持 MySQL 反查

> **背景**：llm-gateway 的 `TokenReader` 当前用 pgx（PostgreSQL 驱动）连接 New API 库反查 token。
> 但 New API 实际使用 **MySQL** 数据库，pgx 无法连接 MySQL，导致 caller 反查功能不可用（降级运行）。
> 本方案让 TokenReader 支持 MySQL，恢复 caller 反查能力。

---

## 一、改造范围（仅 1 个核心文件 + 1 处依赖）

| 文件 | 改动 | 性质 |
|------|------|------|
| `internal/db/tokenreader.go` | **核心**：pgx 池 → database/sql + go-sql-driver/mysql | 重写 |
| `go.mod` / `go.sum` | 新增 `github.com/go-sql-driver/mysql` 依赖 | 自动生成 |
| `.env` | `NEWAPI_DB_URL` 改为 MySQL DSN | 配置 |

**不需要改的文件**（已逐个核对源码）：
- `internal/audit/caller_cache.go` —— 只依赖 `TokenReader.LoadAll()` 返回的 `[]TokenRow` 接口，不关心底层驱动。已核对：`refreshOnce`（caller_cache.go:73）仅调 `reader.LoadAll(ctx)`，无 pgx 类型泄露。
- `cmd/gateway/main.go` —— `NewTokenReader(ctx, dbURL)` 签名不变。已核对 main.go:63 调用方式不变。
- `internal/db/store.go` —— 沉淀库（PG）逻辑独立，不受影响。
- `internal/db/pg_e2e_test.go` —— **只测 Store，不引用 TokenReader / NewTokenReader**（grep 已确认），无需改动。但 `go test ./...` 仍会编译整个 db 包，需确认改造后包级编译通过。
- 其他所有文件。

---

## 二、New API MySQL 库信息

```
连接（容器内视角）：root:<password>@tcp(1Panel-mysql-GYfH:3306)/new_api
库：new_api
表：tokens
```

> ⚠️ **`1Panel-mysql-GYfH` 是 New API 容器内部视角的主机名**，llm-gateway 容器默认无法解析。
> 这是本方案最容易踩的坑：容器能起、端口能通，但 caller 反查会因 DNS 解析失败而静默降级。
> **必须先解决 §三 的容器网络问题，再配 DSN。**

### tokens 表结构（反查所需的 5 个字段）

| 字段 | 类型 | 说明 | 网关对应 |
|------|------|------|---------|
| `key` | varchar(128) | sk-xxx | TokenRow.Key |
| `name` | varchar(191) | token 名称 | TokenRow.Name |
| `user_id` | bigint | 用户ID | TokenRow.UserID |
| `group` | varchar(191) | 分组 | TokenRow.Group |
| `status` | bigint | 1=启用 | WHERE 条件 |
| `deleted_at` | datetime(3) | 软删除 | WHERE 条件 |

> **注意**：MySQL 的 `group` 是保留字，SQL 里必须用反引号 `` `group` ``。
> 现有 pgx 查询用的是双引号 `"group"`（PG 风格，tokenreader.go:54），MySQL 不认，必须改成反引号。
> 同理 `key` 在部分 MySQL 版本是保留字/关键字，统一用反引号最稳。

---

## 三、前置条件（部署前必须完成）

这两项若不满足，容器能启动但 caller 反查会**静默失败降级**，排查成本高。务必先做。

### 3.1 容器网络：让 llm-gateway 能解析 MySQL 主机名

确认 llm-gateway 容器与 MySQL 容器的网络关系，三选一：

| 方案 | DSN 写法 | 适用场景 |
|------|---------|---------|
| A. 加入同一 docker network | `1Panel-mysql-GYfH:3306`（原样） | **推荐** |
| B. 走宿主机端口映射 | `127.0.0.1:<映射端口>` | MySQL 已 `-p 3306:3306` 暴露到宿主 |
| C. 用 MySQL 容器真实 IP | `<容器IP>:3306` | 仅临时验证，IP 会变不推荐 |

**推荐方案 A**，操作：
```bash
# 1. 看 new-api / mysql 在哪个网络
docker network ls
docker inspect <mysql容器名> | grep -A5 Networks
# 2. 把 llm-gateway 加入该网络
docker network connect <newapi_net> llm-gateway
# 3. 验证解析
docker exec llm-gateway getent hosts 1Panel-mysql-GYfH
```

### 3.2 MySQL 只读账号（强烈建议，非强制）

当前 DSN 用的是 New API 的 **root 账号**。按 DESIGN.md §9 最小权限原则，建议建一个只读账号供网关使用，避免网关 DSN 泄露后危及 New API 全库：

```sql
-- 在 MySQL 上执行（用 root 登录）
CREATE USER 'llm_gateway_ro'@'%' IDENTIFIED BY '<强密码>';
GRANT SELECT ON new_api.tokens TO 'llm_gateway_ro'@'%';
FLUSH PRIVILEGES;
```

之后 DSN 改为：
```
llm_gateway_ro:<强密码>@tcp(1Panel-mysql-GYfH:3306)/new_api?...
```

> 若希望最小改动先跑通，可暂用 root，但这是安全债，建议尽快切只读账号。

---

## 四、具体改动

### 改动 1：go.mod 加 MySQL 驱动（只用 go get，禁止手写 require）

```bash
go get github.com/go-sql-driver/mysql
go mod tidy
```

**只此一条 `go get` 命令**，它会自动修改 `go.mod` 的 `require` 块并补全 `go.sum`。

> ⚠️ **不要手动在 go.mod 里加 require 行**。手写会导致 `go.sum` 缺少对应条目，
> 编译时报 `missing go.sum entry`（之前 prometheus/procfs 踩过这个坑）。
> 改完 `go mod tidy` 收尾一遍即可。

### 改动 2：tokenreader.go 重写（完整替换文件）

```go
package db

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	_ "github.com/go-sql-driver/mysql" // MySQL 驱动注册
)

// TokenRow 对应 new-api 库 tokens 表的字段(只读所需列)。
type TokenRow struct {
	Key    string // sk-xxx(将被调用方立即 sha256, 不长期持有)
	Name   string
	UserID int32
	Group  string
}

// TokenReader 只读访问 new-api MySQL 库, 用于反查 caller 映射。
// 依据 DESIGN.md §4.4 / §5.4。
type TokenReader struct {
	db *sql.DB
}

// NewTokenReader 基于 new-api MySQL 库连接池构造(只读账号)。
// dbURL 格式: root:pass@tcp(host:3306)/new_api?charset=utf8mb4&parseTime=true
func NewTokenReader(ctx context.Context, dbURL string) (*TokenReader, error) {
	db, err := sql.Open("mysql", dbURL)
	if err != nil {
		return nil, fmt.Errorf("open mysql: %w", err)
	}
	// 只读场景保守连接数
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(2)
	// 防止 MySQL wait_timeout 主动断开空闲连接后, 下次刷新报 bad connection
	db.SetConnMaxLifetime(5 * time.Minute)

	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("ping mysql: %w", err)
	}
	return &TokenReader{db: db}, nil
}

// Close 释放连接池。
func (r *TokenReader) Close() {
	if r.db != nil {
		r.db.Close()
	}
}

// LoadAll 拉取所有启用中的 token 映射。
// MySQL 注意: key/group 均用反引号(MySQL 保留字/关键字)。
func (r *TokenReader) LoadAll(ctx context.Context) ([]TokenRow, error) {
	const sqlQuery = `
SELECT ` + "`key`" + `, COALESCE(` + "`name`" + `,''), user_id, COALESCE(` + "`group`" + `,'')
FROM tokens
WHERE deleted_at IS NULL AND status = 1`

	rows, err := r.db.QueryContext(ctx, sqlQuery)
	if err != nil {
		return nil, fmt.Errorf("token reader: %w", err)
	}
	defer rows.Close()

	var out []TokenRow
	for rows.Next() {
		var t TokenRow
		if err := rows.Scan(&t.Key, &t.Name, &t.UserID, &t.Group); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}
```

#### 改动要点说明

| 原代码（pgx） | 新代码（MySQL） |
|--------------|----------------|
| `import "github.com/jackc/pgx/v5/pgxpool"` | `import "database/sql"` + `_ "github.com/go-sql-driver/mysql"` |
| `pgxpool.ParseConfig(dbURL)` | `sql.Open("mysql", dbURL)` |
| `*pgxpool.Pool` | `*sql.DB` |
| `cfg.MaxConns = 4` | `db.SetMaxOpenConns(4)` + `db.SetMaxIdleConns(2)` |
| 无 | `db.SetConnMaxLifetime(5m)` ← **新增，防 wait_timeout 断连** |
| `pool.Ping(ctx)` | `db.PingContext(ctx)` |
| `pool.Query(ctx, sql)` | `db.QueryContext(ctx, sql)` |
| `pool.Close()` | `db.Close()` |
| SQL 里 `"group"`（双引号） | SQL 里 `` `group` ``（反引号，MySQL 保留字） |

> **为什么用 database/sql 而不是其他 MySQL 库？**
> `database/sql` 是 Go 标准接口，`go-sql-driver/mysql` 是最成熟的 MySQL 驱动。
> 这样改最小化依赖，且 `LoadAll()` 对外接口完全不变（返回 `[]TokenRow`），
> 上层 `caller_cache.go` 和 `main.go` **零改动**。

---

## 五、配置变更（.env）

### 改造前（占位，无法用）

```env
# 指向 PG 占位（反查失败降级）
NEWAPI_DB_URL=postgres://<user>:<password>@<pg-container>:5432/llm_gateway?sslmode=disable
```

### 改造后（真实 MySQL 连接）

```env
# MySQL DSN 格式: user:pass@tcp(host:port)/db?params
NEWAPI_DB_URL=root:<password>@tcp(1Panel-mysql-GYfH:3306)/new_api?charset=utf8mb4&parseTime=true&loc=Local
```

**DSN 各段说明**：
- `root:<password>` —— 用户名:密码（建议改用 §3.2 的只读账号）
- `tcp(1Panel-mysql-GYfH:3306)` —— MySQL 主机:端口。**前提：§3.1 网络已打通，否则换 127.0.0.1 或容器 IP**
- `new_api` —— 库名
- `charset=utf8mb4` —— 字符集
- `parseTime=true` —— 自动解析 datetime 为 time.Time
- `loc=Local` —— 时区

---

## 六、验证步骤（开发完成 + 重新部署后）

### 6.1 本地编译验证（必做）

```bash
# 1. 依赖拉取
go get github.com/go-sql-driver/mysql && go mod tidy

# 2. 全包编译（确认 tokenreader.go 改造后整个 db 包可编译）
go build ./...

# 3. 测试（pg_e2e_test.go 不涉及 TokenReader，无需改；但要能编译通过）
go test ./internal/db/... -run TokenReader -v
go vet ./...
```

### 6.2 服务器验证：网络连通性（部署后第一时间）

```bash
# 确认 llm-gateway 能解析 MySQL 主机名
docker exec llm-gateway getent hosts 1Panel-mysql-GYfH
```
若解析失败 → 回到 §3.1 处理网络。

### 6.3 服务器验证：caller cache 加载成功

部署后，看网关日志不再报 `tokens 表不存在`：

```bash
# 改造前（报错降级）
docker logs llm-gateway 2>&1 | grep "caller cache"
# ERROR caller cache refresh failed err="... relation \"tokens\" does not exist"

# 改造后（应成功）
docker logs llm-gateway 2>&1 | grep "caller cache"
# INFO caller cache refreshed tokens=1234
```

### 6.4 反查功能验证

发一个带 token 的请求，看审计记录里 `caller_tag` 不再为空：

```sql
SELECT caller_tag, caller_group, count(*)
FROM llm_conversation
WHERE created_at > now() - interval '5 minutes'
GROUP BY caller_tag, caller_group;
```

改造前 `caller_tag` 全为空；改造后应有 token name 填充。

---

## 七、回滚方案

如果改造出问题，改回 `.env` 占位配置即可秒级回滚（不影响代理+审计）：

```env
NEWAPI_DB_URL=postgres://<user>:<password>@<pg-container>:5432/llm_gateway?sslmode=disable
```

> **注**：回滚后 tokenreader.go 已是 MySQL 实现，连 PG 占位 URL 会 ping 失败。
> main.go:65 的逻辑会捕获该失败并降级为 noop caller cache——这是符合预期的行为，
> 核心功能（代理 + 审计）完全不受影响。

---

## 八、验收清单

**代码层**
- [ ] `go get github.com/go-sql-driver/mysql` 已执行，go.sum 有对应条目
- [ ] `tokenreader.go` 改用 database/sql + mysql 驱动
- [ ] SQL 的 `key` / `group` 字段改为反引号
- [ ] 新增 `SetConnMaxLifetime(5m)`
- [ ] 本地 `go build ./...` 编译通过
- [ ] 本地 `go test ./internal/db/...` 通过（pg_e2e_test.go 不需改）

**部署层**
- [ ] §3.1 容器网络已打通（`getent hosts 1Panel-mysql-GYfH` 在网关容器内可解析）
- [ ] （建议）§3.2 只读账号已创建，DSN 已切换

**运行层**
- [ ] 日志显示 `caller cache refreshed tokens=N`（N > 0）
- [ ] 审计记录 `caller_tag` 不再为空
