// Package platform 实现可视化管理平台的后端服务。
// 详见 docs/PLATFORM_DESIGN.md。
package platform

import (
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/kelseyhightower/envconfig"
)

// Config 平台后端运行期配置(PLATFORM_* 环境变量)。
type Config struct {
	ListenAddr string `envconfig:"PLATFORM_LISTEN_ADDR" default:":8088"`

	// 沉淀库(与网关共用同一个 PG)
	DBURL string `envconfig:"PLATFORM_DB_URL" required:"true"`

	// 连接池(平台查询用, 故意设小避免挤占网关写入)
	DBMaxOpenConns int `envconfig:"PLATFORM_DB_MAX_OPEN_CONNS" default:"5"`

	// 鉴权(生产必须设置, 留空启动会 WARN)
	// 管理员账号(登录用户名, 默认 admin)
	AdminUser string `envconfig:"PLATFORM_ADMIN_USER" default:"admin"`
	// 管理员密码(明文配置; 启动时 bcrypt 哈希后存内存比对)
	AdminPassword string `envconfig:"PLATFORM_ADMIN_PASSWORD" default:""`
	// 会话有效期(登录后 cookie 的存活时长)
	SessionTTL time.Duration `envconfig:"PLATFORM_SESSION_TTL" default:"24h"`
	// 旧 Bearer token(兼容存量; 未配置密码登录时作为兜底鉴权)
	AuthToken string `envconfig:"PLATFORM_AUTH_TOKEN" default:""`

	// IP 白名单(逗号分隔 CIDR/IP, 留空=不限; 生产建议设内网段)
	// 例: "127.0.0.1,10.0.0.0/8,172.16.0.0/12"
	AllowIPs string `envconfig:"PLATFORM_ALLOW_IPS" default:""`

	// CORS(生产建议设具体域名, 不用 *)
	CORSOrigins string `envconfig:"PLATFORM_CORS_ORIGINS" default:"*"`

	// 速率限制(每分钟请求数, 防 API 被暴力爬取)
	RateLimitPerMin int `envconfig:"PLATFORM_RATE_LIMIT_PER_MIN" default:"120"`

	// 查询超时(防慢查询拖垮 PG)
	QueryTimeout time.Duration `envconfig:"PLATFORM_QUERY_TIMEOUT" default:"10s"`

	// 时区(所有时间聚合按此时区切分)
	Timezone string `envconfig:"PLATFORM_TIMEZONE" default:"Asia/Shanghai"`

	LogLevel string `envconfig:"PLATFORM_LOG_LEVEL" default:"info"`
}

// Load 从环境变量加载配置。
func Load() (*Config, error) {
	var c Config
	if err := envconfig.Process("PLATFORM", &c); err != nil {
		return nil, fmt.Errorf("envconfig: %w", err)
	}
	if c.DBMaxOpenConns <= 0 {
		c.DBMaxOpenConns = 5
	}
	if c.RateLimitPerMin <= 0 {
		c.RateLimitPerMin = 120
	}
	return &c, nil
}

// AllowIPSet 解析 IP 白名单为可校验的格式(net.IPNet 列表)。
// 空表示不限。
func (c *Config) AllowIPSet() []*net.IPNet {
	if c.AllowIPs == "" {
		return nil
	}
	var nets []*net.IPNet
	for _, item := range strings.Split(c.AllowIPs, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		// 不带 / 的当 /32 (IPv4) 或 /128 (IPv6)
		if !strings.Contains(item, "/") {
			if strings.Contains(item, ":") {
				item += "/128"
			} else {
				item += "/32"
			}
		}
		if _, n, err := net.ParseCIDR(item); err == nil {
			nets = append(nets, n)
		}
	}
	return nets
}
