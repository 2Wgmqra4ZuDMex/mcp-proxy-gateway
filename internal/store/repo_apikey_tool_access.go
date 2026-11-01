package store

import (
	"context"
	"sort"
	"strings"

	"gorm.io/gorm"

	"github.com/myGithub/mcp-proxy-gateway/internal/domain"
)

// MaxToolAccessNameLen 为工具白名单中单个工具名的最大长度，与表结构一致。
const MaxToolAccessNameLen = 200

// APIKeyToolAccess 是单个 API Key 的工具白名单快照。
type APIKeyToolAccess struct {
	Mode      string   `json:"mode"`
	ToolNames []string `json:"toolNames"`
}

// APIKeyToolAccessRepo 原子维护 API Key 的工具访问模式与已选工具名。
type APIKeyToolAccessRepo struct {
	db *gorm.DB
}

func NewAPIKeyToolAccessRepo(db *gorm.DB) *APIKeyToolAccessRepo {
	return &APIKeyToolAccessRepo{db: db}
}

func (r *APIKeyToolAccessRepo) Get(ctx context.Context, apiKeyID string) (APIKeyToolAccess, error) {
	uid, err := parseUUID(apiKeyID)
	if err != nil {
		return APIKeyToolAccess{}, err
	}
	var key apiKeyModel
	if err := r.db.WithContext(ctx).Select("id", "tool_access_mode").Where("id = ?", uid).First(&key).Error; err != nil {
		return APIKeyToolAccess{}, notFoundIfNoRows(err, "API Key 不存在")
	}
	var rows []apiKeyToolAccessModel
	if err := r.db.WithContext(ctx).Where("api_key_id = ?", uid).Find(&rows).Error; err != nil {
		return APIKeyToolAccess{}, err
	}
	names := make([]string, 0, len(rows))
	for _, row := range rows {
		names = append(names, row.ToolName)
	}
	sort.Strings(names)
	return APIKeyToolAccess{Mode: normalizeToolAccessMode(key.ToolAccessMode), ToolNames: names}, nil
}

func (r *APIKeyToolAccessRepo) Replace(ctx context.Context, apiKeyID, mode string, toolNames []string) error {
	uid, err := parseUUID(apiKeyID)
	if err != nil {
		return err
	}
	names := NormalizeToolAccessNames(toolNames)
	mode = normalizeToolAccessMode(mode)
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		res := tx.Model(&apiKeyModel{}).Where("id = ?", uid).Update("tool_access_mode", mode)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return domain.NewError(domain.CodeNotFound, "API Key 不存在")
		}
		if err := tx.Where("api_key_id = ?", uid).Delete(&apiKeyToolAccessModel{}).Error; err != nil {
			return err
		}
		if mode != "selected" || len(names) == 0 {
			return nil
		}
		rows := make([]apiKeyToolAccessModel, 0, len(names))
		for _, name := range names {
			rows = append(rows, apiKeyToolAccessModel{APIKeyID: uid, ToolName: name})
		}
		if err := tx.Create(&rows).Error; err != nil {
			return classifyWrite(err, "工具权限配置冲突", "包含不存在的 API Key")
		}
		return nil
	})
}

// NormalizeToolAccessNames 去除空白、空值与重复项，并按字典序返回。
func NormalizeToolAccessNames(toolNames []string) []string {
	seen := make(map[string]struct{}, len(toolNames))
	out := make([]string, 0, len(toolNames))
	for _, name := range toolNames {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func normalizeToolAccessMode(mode string) string {
	if mode == "selected" {
		return "selected"
	}
	return "all"
}
