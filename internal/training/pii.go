package training

import (
	"regexp"
)

// PII 清洗层(2026-08-20 合规审计): 网关 redact 存在漏洞(带连字符域名邮箱漏脱敏),
// 且 system/tool 内容可能含明文 PII。训练语料输出前强制二次清洗,
// 目标: 训练集无明文手机号/邮箱/内网 IP。

var (
	// 邮箱(含连字符域名): liulei@yuexin-logistics.com
	piiEmailRe = regexp.MustCompile(`[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}`)
	// 手机号: 1[3-9] 开头 11 位(\b 边界, 长数字串中间不匹配, 防误伤订单号)
	piiPhoneRe = regexp.MustCompile(`\b1[3-9]\d{9}\b`)
	// 内网 IP: 10.x.x.x / 172.16-31.x.x / 192.168.x.x
	piiLANIPRe = regexp.MustCompile(`\b(?:192\.168\.|10\.|172\.(?:1[6-9]|2\d|3[01])\.)(?:\d{1,3}\.){1,2}\d{1,3}\b`)
)

// CleanPII 将训练文本中的 PII 替换为占位符。
// 注意: 身份证 18 位数字不处理——审计显示命中多为订单号/编码(误伤 > 收益)。
func CleanPII(s string) string {
	s = piiEmailRe.ReplaceAllString(s, "<EMAIL>")
	s = piiPhoneRe.ReplaceAllString(s, "<PHONE>")
	s = piiLANIPRe.ReplaceAllString(s, "<LAN_IP>")
	return s
}

// HasPII 判断文本是否含明文 PII。
func HasPII(s string) bool {
	return piiEmailRe.MatchString(s) || piiPhoneRe.MatchString(s) || piiLANIPRe.MatchString(s)
}
