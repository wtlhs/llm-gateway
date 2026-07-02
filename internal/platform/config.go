// Package platform 实现可视化管理平台的后端服务。
// 详见 docs/PLATFORM_DESIGN.md。
package platform

import (
	"fmt"
	"time"

	"github.com/kelseyhightower/envconfig"
)

// Config 平台后端运行期配置(PLATFORM_* 环境变量)。
// 与网关 config 独立, 平台是只读查询服务, 不需要网关的限流/熔断等参数。
type Config struct {
	ListenAddr string `envconfig:"PLATFORM_LISTEN_ADDR" default:":8088"`

	// 沉淀库(与网关共用同一个 PG)
	DBURL string `envconfig:"PLATFORM_DB_URL" required:"true"`

	// 连接池(平台查询用, 故意设小避免挤占网关写入, 见 PLATFORM_DESIGN §5.4)
	DBMaxOpenConns int `envconfig:"PLATFORM_DB_MAX_OPEN_CONNS" default:"5"`

	// 鉴权(复用网关的 AdminAuthToken, 或单独配置)
	AuthToken string `envconfig:"PLATFORM_AUTH_TOKEN" default:""`

	// CORS(前端独立部署时需要)
	CORSOrigins string `envconfig:"PLATFORM_CORS_ORIGINS" default:"*"`

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
	return &c, nil
}
