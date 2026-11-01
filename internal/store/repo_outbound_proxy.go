package store

import (
	"context"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/myGithub/mcp-proxy-gateway/internal/domain"
)

// outboundProxyModel 为出站代理（outbound_proxy 表）。上游通过 connParams.proxyId 引用。
type outboundProxyModel struct {
	ID        string    `gorm:"column:id;type:uuid;primaryKey"`
	Name      string    `gorm:"column:name;type:varchar(100);not null;uniqueIndex:idx_outbound_proxy_name"`
	URL       string    `gorm:"column:url;type:text;not null"`
	Note      string    `gorm:"column:note;type:varchar(500);not null;default:''"`
	Enabled   bool      `gorm:"column:enabled;type:boolean;not null;default:true"`
	CreatedAt time.Time `gorm:"column:created_at;type:timestamptz;not null;default:now()"`
}

func (outboundProxyModel) TableName() string { return "outbound_proxy" }

// OutboundProxy 是出站代理的仓储视图；URL 含明文凭证，仅供管理端与连接拨号使用。
type OutboundProxy struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	URL       string    `json:"url"`
	Note      string    `json:"note"`
	Enabled   bool      `json:"enabled"`
	CreatedAt time.Time `json:"createdAt"`
}

// OutboundProxyRepo 提供出站代理的类型安全增删查改。
type OutboundProxyRepo struct {
	db *gorm.DB
}

func NewOutboundProxyRepo(db *gorm.DB) *OutboundProxyRepo {
	return &OutboundProxyRepo{db: db}
}

func (r *OutboundProxyRepo) List(ctx context.Context) ([]OutboundProxy, error) {
	var models []outboundProxyModel
	if err := r.db.WithContext(ctx).Order("created_at ASC").Find(&models).Error; err != nil {
		return nil, err
	}
	out := make([]OutboundProxy, 0, len(models))
	for _, m := range models {
		out = append(out, modelToOutboundProxy(m))
	}
	return out, nil
}

func (r *OutboundProxyRepo) Get(ctx context.Context, id string) (OutboundProxy, error) {
	uid, err := parseUUID(strings.TrimSpace(id))
	if err != nil || uid == "" {
		return OutboundProxy{}, domain.NewError(domain.CodeNotFound, "出站代理不存在")
	}
	var m outboundProxyModel
	if err := r.db.WithContext(ctx).Where("id = ?", uid).First(&m).Error; err != nil {
		return OutboundProxy{}, notFoundIfNoRows(err, "出站代理不存在")
	}
	return modelToOutboundProxy(m), nil
}

// Create 新建代理；p.ID 为合法 UUID 时沿用（备份导入保持引用关系），否则生成新标识。
func (r *OutboundProxyRepo) Create(ctx context.Context, p OutboundProxy) (OutboundProxy, error) {
	id := newUUID()
	if raw := strings.TrimSpace(p.ID); raw != "" {
		if parsed, err := parseUUID(raw); err == nil {
			id = parsed
		}
	}
	if err := r.db.WithContext(ctx).Model(&outboundProxyModel{}).Create(map[string]any{
		"id":      id,
		"name":    p.Name,
		"url":     p.URL,
		"note":    p.Note,
		"enabled": p.Enabled,
	}).Error; err != nil {
		return OutboundProxy{}, classifyWrite(err, "代理名称已存在", "出站代理创建失败")
	}
	return r.Get(ctx, id)
}

func (r *OutboundProxyRepo) Update(ctx context.Context, p OutboundProxy) (OutboundProxy, error) {
	uid, err := parseUUID(strings.TrimSpace(p.ID))
	if err != nil || uid == "" {
		return OutboundProxy{}, domain.NewError(domain.CodeNotFound, "出站代理不存在")
	}
	res := r.db.WithContext(ctx).Model(&outboundProxyModel{}).Where("id = ?", uid).Updates(map[string]any{
		"name":    p.Name,
		"url":     p.URL,
		"note":    p.Note,
		"enabled": p.Enabled,
	})
	if res.Error != nil {
		return OutboundProxy{}, classifyWrite(res.Error, "代理名称已存在", "出站代理不存在")
	}
	if res.RowsAffected == 0 {
		return OutboundProxy{}, domain.NewError(domain.CodeNotFound, "出站代理不存在")
	}
	return r.Get(ctx, uid)
}

func (r *OutboundProxyRepo) Delete(ctx context.Context, id string) error {
	uid, err := parseUUID(strings.TrimSpace(id))
	if err != nil || uid == "" {
		return domain.NewError(domain.CodeNotFound, "出站代理不存在")
	}
	res := r.db.WithContext(ctx).Where("id = ?", uid).Delete(&outboundProxyModel{})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return domain.NewError(domain.CodeNotFound, "出站代理不存在")
	}
	return nil
}

func modelToOutboundProxy(m outboundProxyModel) OutboundProxy {
	return OutboundProxy{ID: m.ID, Name: m.Name, URL: m.URL, Note: m.Note, Enabled: m.Enabled, CreatedAt: m.CreatedAt}
}
