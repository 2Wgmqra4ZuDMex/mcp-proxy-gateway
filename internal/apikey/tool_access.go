package apikey

import (
	"context"
	"fmt"

	"github.com/myGithub/mcp-proxy-gateway/internal/domain"
	"github.com/myGithub/mcp-proxy-gateway/internal/store"
)

// MaxToolAccessNames 为单个 API Key 工具白名单的条目上限。
const MaxToolAccessNames = 1000

// ToolAccessConfig 描述 API Key 可见的对外工具范围：all 不限制，selected 仅暴露 ToolNames。
//
// 白名单按聚合后的对外工具名匹配：同名工具的多个来源（如多账号）共享一个条目，
// 新增同名来源无需重新勾选。
type ToolAccessConfig struct {
	Mode      UpstreamAccessMode `json:"mode"`
	ToolNames []string           `json:"toolNames"`
}

type ToolAccessRepository interface {
	Get(ctx context.Context, apiKeyID string) (store.APIKeyToolAccess, error)
	Replace(ctx context.Context, apiKeyID, mode string, toolNames []string) error
}

// ToolAccessManager 管理 API Key 的工具白名单，并供聚合链路过滤可见工具集合。
type ToolAccessManager struct {
	repo ToolAccessRepository
}

func NewToolAccessManager(repo ToolAccessRepository) *ToolAccessManager {
	return &ToolAccessManager{repo: repo}
}

func (m *ToolAccessManager) Get(ctx context.Context, apiKeyID string) (ToolAccessConfig, error) {
	row, err := m.repo.Get(ctx, apiKeyID)
	if err != nil {
		return ToolAccessConfig{}, err
	}
	names := row.ToolNames
	if names == nil {
		names = []string{}
	}
	return ToolAccessConfig{Mode: normalizeAccessMode(row.Mode), ToolNames: names}, nil
}

func (m *ToolAccessManager) Set(ctx context.Context, apiKeyID string, cfg ToolAccessConfig) (ToolAccessConfig, error) {
	if !validAccessMode(cfg.Mode) {
		return ToolAccessConfig{}, domain.NewValidationError("工具权限配置校验失败", map[string]string{
			"mode": "仅支持 all 或 selected",
		})
	}
	names := store.NormalizeToolAccessNames(cfg.ToolNames)
	if cfg.Mode == UpstreamAccessAll {
		names = nil
	}
	if len(names) > MaxToolAccessNames {
		return ToolAccessConfig{}, domain.NewValidationError("工具权限配置校验失败", map[string]string{
			"toolNames": fmt.Sprintf("最多选择 %d 个工具", MaxToolAccessNames),
		})
	}
	for _, name := range names {
		if len(name) > store.MaxToolAccessNameLen {
			return ToolAccessConfig{}, domain.NewValidationError("工具权限配置校验失败", map[string]string{
				"toolNames": fmt.Sprintf("工具名长度不能超过 %d 个字符", store.MaxToolAccessNameLen),
			})
		}
	}
	if err := m.repo.Replace(ctx, apiKeyID, string(cfg.Mode), names); err != nil {
		return ToolAccessConfig{}, err
	}
	return m.Get(ctx, apiKeyID)
}

// AllowedToolNames 返回 API Key 的工具白名单；restricted 为 false 表示不限制。
// apiKeyID 为空（无 API Key 视角）时不限制。
func (m *ToolAccessManager) AllowedToolNames(ctx context.Context, apiKeyID string) (map[string]struct{}, bool, error) {
	if apiKeyID == "" {
		return nil, false, nil
	}
	cfg, err := m.Get(ctx, apiKeyID)
	if err != nil {
		return nil, false, err
	}
	if cfg.Mode == UpstreamAccessAll {
		return nil, false, nil
	}
	allowed := make(map[string]struct{}, len(cfg.ToolNames))
	for _, name := range cfg.ToolNames {
		allowed[name] = struct{}{}
	}
	return allowed, true, nil
}
