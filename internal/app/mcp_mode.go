package app

import "github.com/myGithub/mcp-proxy-gateway/internal/config"

// defaultMCPMode 返回 /mcp/sse、/mcp/http、/mcp/ws 使用的接入模式。未配置（旧配置文件没有该字段）
// 时按智能模式处理。
func defaultMCPMode(configured string) string {
	if configured == config.ModeFull {
		return config.ModeFull
	}
	return config.ModeSmart
}
