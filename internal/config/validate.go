package config

import "fmt"

// Validate 启动期范围校验(S6)。非法直接拒绝启动, 避免带病运行。
// 校验项依据 DESIGN.md §6.1 注释。
func (c *Config) Validate() error {
	switch c.AuditMode {
	case ModeFull, ModeRedact, ModeOff:
	default:
		return fmt.Errorf("LLM_AUDIT_MODE 非法值 %q, 必须是 full|redact|off", c.AuditMode)
	}

	if c.DBMaxOpenConns < c.WorkerPoolSize {
		return fmt.Errorf("DB_MAX_OPEN_CONNS(%d) 必须 >= WORKER_POOL_SIZE(%d)",
			c.DBMaxOpenConns, c.WorkerPoolSize)
	}
	if c.PreBodyMaxBytes <= c.MaxBodyBytes {
		return fmt.Errorf("LLM_AUDIT_PRE_BODY_MAX_BYTES(%d) 必须 > LLM_AUDIT_MAX_BODY_BYTES(%d)",
			c.PreBodyMaxBytes, c.MaxBodyBytes)
	}
	if c.CaptureChannelSize <= 0 {
		return fmt.Errorf("CAPTURE_CHANNEL_SIZE 必须 > 0, 当前 %d", c.CaptureChannelSize)
	}
	if c.WorkerPoolSize <= 0 {
		return fmt.Errorf("WORKER_POOL_SIZE 必须 > 0, 当前 %d", c.WorkerPoolSize)
	}
	if c.RatePerCaller <= 0 || c.RateBurst <= 0 || c.RateAnon <= 0 {
		return fmt.Errorf("限流参数必须 > 0: per_caller=%d burst=%d anon=%d",
			c.RatePerCaller, c.RateBurst, c.RateAnon)
	}
	if c.BreakerFailures <= 0 {
		return fmt.Errorf("BREAKER_FAILURES 必须 > 0, 当前 %d", c.BreakerFailures)
	}
	if c.TTLDays <= 0 {
		return fmt.Errorf("LLM_AUDIT_TTL_DAYS 必须 > 0, 当前 %d", c.TTLDays)
	}
	if c.ContextDBURL == "" {
		return fmt.Errorf("CONTEXT_DB_URL 不能为空")
	}
	if c.NewAPIB_URL == "" {
		return fmt.Errorf("NEWAPI_DB_URL 不能为空")
	}
	if c.NewAPIBaseURL == "" {
		return fmt.Errorf("NEW_API_BASE_URL 不能为空")
	}
	return nil
}
