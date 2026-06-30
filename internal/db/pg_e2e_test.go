package db

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"testing"
	"time"
)

// 这个测试验证真实 PG 落库链路: Store.Insert 的 SQL 真的能写入并读回。
// 仅当环境变量 PG_E2E_URL 设置时才跑(避免在 CI/无库环境失败)。
// 本地运行: PG_E2E_URL="postgresql://..." go test ./internal/db/ -run TestPG -v
//
// 安全: 连接串不进仓库; 表名 llm_conversation 已建好(见 migrations)。
func TestPG_InsertAndRead(t *testing.T) {
	url := os.Getenv("PG_E2E_URL")
	if url == "" {
		t.Skip("PG_E2E_URL 未设置, 跳过真实 PG 验证")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	store, err := NewStore(ctx, url, 5)
	if err != nil {
		t.Fatalf("connect failed: %v", err)
	}
	defer store.Close()

	// 插入一条完整记录
	rid := "e2e-test-" + time.Now().Format("150405.000000")
	c := &Conversation{
		RequestID:         rid,
		UpstreamRequestID: "newapi-req-e2e",
		CallerTag:         "scm-test-agent",
		CallerUserID:      42,
		CallerGroup:       "default",
		TokenKeyHash:      sha256Str("sk-test-e2e-key"),
		Model:             "gpt-4o",
		Endpoint:          "chat/completions",
		IsStream:          false,
		PromptText:        json.RawMessage(`{"messages":[{"role":"user","content":"hi"}]}`),
		CompletionText:    json.RawMessage(`{"choices":[{"message":{"content":"hello"}}]}`),
		HTTPStatus:        200,
		PromptTokens:      5,
		CompletionTokens:  2,
		ClientIP:          "10.0.0.1",
		Redacted:          true,
	}
	if err := store.Insert(ctx, c); err != nil {
		t.Fatalf("Insert failed: %v", err)
	}

	// 读回验证
	var (
		gotModel, gotUpstream, gotTag string
		gotStatus                     int
		gotPrompt                     []byte
	)
	err = store.pool.QueryRow(ctx,
		`SELECT model, upstream_request_id, caller_tag, http_status, prompt_text
		 FROM llm_conversation WHERE request_id=$1`, rid,
	).Scan(&gotModel, &gotUpstream, &gotTag, &gotStatus, &gotPrompt)
	if err != nil {
		t.Fatalf("read back failed: %v", err)
	}
	if gotModel != "gpt-4o" {
		t.Errorf("model=%q", gotModel)
	}
	if gotUpstream != "newapi-req-e2e" {
		t.Errorf("upstream_request_id=%q", gotUpstream)
	}
	if gotStatus != 200 {
		t.Errorf("http_status=%d", gotStatus)
	}
	if !json.Valid(gotPrompt) {
		t.Errorf("prompt_text not valid json: %s", gotPrompt)
	}

	// 幂等: 同 request_id 再插应返回 ErrDuplicate
	err = store.Insert(ctx, c)
	if err != ErrDuplicate {
		t.Errorf("second insert: err=%v, want ErrDuplicate", err)
	}

	// 清理
	store.pool.Exec(ctx, "DELETE FROM llm_conversation WHERE request_id=$1", rid)
}

// TestPG_AdvisoryLock 验证 TTL advisory lock 多实例互斥。
func TestPG_AdvisoryLock(t *testing.T) {
	url := os.Getenv("PG_E2E_URL")
	if url == "" {
		t.Skip("PG_E2E_URL 未设置")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	s1, _ := NewStore(ctx, url, 2)
	defer s1.Close()
	s2, _ := NewStore(ctx, url, 2)
	defer s2.Close()

	const lockID = int64(9876543210)
	// s1 先拿锁
	ok1, err := s1.TryAcquireTTLLock(ctx, lockID)
	if err != nil || !ok1 {
		t.Fatalf("s1 acquire: ok=%v err=%v", ok1, err)
	}
	// s2 应拿不到(互斥)
	ok2, _ := s2.TryAcquireTTLLock(ctx, lockID)
	if ok2 {
		t.Fatal("s2 should NOT acquire lock while s1 holds it")
	}
	// s1 释放后 s2 能拿
	s1.ReleaseTTLLock(ctx, lockID)
	ok3, _ := s2.TryAcquireTTLLock(ctx, lockID)
	if !ok3 {
		t.Fatal("s2 should acquire after s1 released")
	}
	s2.ReleaseTTLLock(ctx, lockID)
}

// sha256Str 测试辅助(不依赖 audit 包, 避免循环 import)。
func sha256Str(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}
