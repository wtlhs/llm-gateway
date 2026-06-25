// Package config 加载并校验所有运行期配置(env 优先)。
// 设计依据见 DESIGN.md §6.1、§0(决策对齐)、启动期校验见 §6.1 注(S6)。
package config

import (
	"fmt"
	"strings"
	"time"

	"github.com/kelseyhightower/envconfig"
)

// Mode 枚举 LLM_AUDIT_MODE。
type Mode string

const (
	ModeFull   Mode = "full"   // 原文落库(仅受控环境)
	ModeRedact Mode = "redact" // 脱敏后落库
	ModeOff    Mode = "off"    // 不落库, 纯透传
)

// Config 全部运行期配置。字段名通过 envconfig tag 映射环境变量。
type Config struct {
	ListenAddr     string `envconfig:"LISTEN_ADDR" default:":8080"`
	NewAPIBaseURL  string `envconfig:"NEW_API_BASE_URL" default:"http://newapi.internal:3000"`
	NewAPITLS      bool   `envconfig:"NEW_API_TLS" default:"false"`

	ContextDBURL string `envconfig:"CONTEXT_DB_URL" required:"true"`
	NewAPIB_URL  string `envconfig:"NEWAPI_DB_URL"  required:"true"`

	// 审计
	AuditMode            Mode   `envconfig:"LLM_AUDIT_MODE" default:"redact"`
	AuditExcludeModels   string `envconfig:"LLM_AUDIT_EXCLUDE_MODELS" default:""`
	CaptureEndpointsCSV  string `envconfig:"LLM_AUDIT_CAPTURE_ENDPOINTS" default:"chat/completions,completions,responses,embeddings,moderations"`
	MaxBodyBytes         int64  `envconfig:"LLM_AUDIT_MAX_BODY_BYTES" default:"65536"`
	PreBodyMaxBytes      int64  `envconfig:"LLM_AUDIT_PRE_BODY_MAX_BYTES" default:"33554432"` // 32MB
	TTLDays              int    `envconfig:"LLM_AUDIT_TTL_DAYS" default:"90"`

	// 过载保护
	BreakerFailures int `envconfig:"BREAKER_FAILURES" default:"5"`
	RatePerCaller   int `envconfig:"RATE_PER_CALLER" default:"50"`
	RateBurst       int `envconfig:"RATE_BURST" default:"100"`
	RateAnon        int `envconfig:"RATE_ANON" default:"10"`

	// 性能
	CaptureChannelSize int           `envconfig:"CAPTURE_CHANNEL_SIZE" default:"4096"`
	DBMaxOpenConns     int           `envconfig:"DB_MAX_OPEN_CONNS" default:"25"`
	WorkerPoolSize     int           `envconfig:"WORKER_POOL_SIZE" default:"8"`
	ShutdownTimeout    time.Duration `envconfig:"SHUTDOWN_TIMEOUT_SEC" default:"30s"`
	DrainTimeout       time.Duration `envconfig:"DRAIN_TIMEOUT_SEC" default:"20s"`

	// caller 反查
	CallerCacheRefresh time.Duration `envconfig:"CALLER_CACHE_REFRESH_SEC" default:"60s"`

	// 安全
	MetricsAuthToken        string `envconfig:"METRICS_AUTH_TOKEN" default:""`
	AdminAuthToken          string `envconfig:"ADMIN_AUTH_TOKEN" default:""`
	SlogBlocklistCSV        string `envconfig:"SLOG_PROMPT_FIELDS_BLOCKLIST" default:"request.body,response.body,prompt_text,completion_text"`

	// 日志
	LogLevel string `envconfig:"LOG_LEVEL" default:"info"`
}

// Load 从环境变量加载配置。prefix 留空, 直接读裸 env 名。
func Load() (*Config, error) {
	var c Config
	if err := envconfig.Process("", &c); err != nil {
		return nil, fmt.Errorf("envconfig: %w", err)
	}
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return &c, nil
}

// CaptureEndpoints 返回解析后的端点白名单集合(用于 §5.1 isCaptureEndpoint)。
// 注意:仅含 A/B 类;C/D 类(images/audio/models/realtime)不在此集合,自动 bypass。
func (c *Config) CaptureEndpoints() map[string]struct{} {
	out := make(map[string]struct{})
	for _, e := range strings.Split(c.CaptureEndpointsCSV, ",") {
		if e = strings.TrimSpace(e); e != "" {
			out[e] = struct{}{}
		}
	}
	return out
}

// ExcludeModels 返回不捕获的模型集合。
func (c *Config) ExcludeModels() map[string]struct{} {
	out := make(map[string]struct{})
	for _, m := range strings.Split(c.AuditExcludeModels, ",") {
		if m = strings.TrimSpace(m); m != "" {
			out[m] = struct{}{}
		}
	}
	return out
}

// SlogBlocklist 返回 slog 字段黑名单集合。
func (c *Config) SlogBlocklist() map[string]struct{} {
	out := make(map[string]struct{})
	for _, f := range strings.Split(c.SlogBlocklistCSV, ",") {
		if f = strings.TrimSpace(f); f != "" {
			out[f] = struct{}{}
		}
	}
	return out
}
