package store

import (
	"context"
	"sync/atomic"
	"time"

	"gorm.io/gorm"

	"github.com/myGithub/mcp-proxy-gateway/internal/domain"
)

const (
	accountEventRetention  = 30 * 24 * time.Hour
	accountEventPruneEvery = 100
)

// accountEventModel 为上游账号异常记录（限流、额度不足、Key 无效）。不参与备份。
type accountEventModel struct {
	ID           int64     `gorm:"column:id;primaryKey;autoIncrement"`
	UpstreamID   string    `gorm:"column:upstream_id;type:varchar(64);not null;index:idx_account_event_upstream_time,priority:1"`
	UpstreamName string    `gorm:"column:upstream_name;type:varchar(200);not null;default:''"`
	Kind         string    `gorm:"column:kind;type:varchar(32);not null"`
	HTTPStatus   int       `gorm:"column:http_status;type:integer;not null;default:0"`
	Message      string    `gorm:"column:message;type:text;not null;default:''"`
	Tool         string    `gorm:"column:tool;type:varchar(200);not null;default:''"`
	Source       string    `gorm:"column:source;type:varchar(16);not null;default:'call'"`
	CooldownS    int       `gorm:"column:cooldown_s;type:integer;not null;default:0"`
	Level        int       `gorm:"column:level;type:integer;not null;default:0"`
	OccurredAt   time.Time `gorm:"column:occurred_at;type:timestamptz;not null;index:idx_account_event_upstream_time,priority:2;index:idx_account_event_time"`
}

func (accountEventModel) TableName() string { return "upstream_account_event" }

type AccountEventRepo struct {
	db      *gorm.DB
	inserts atomic.Int64
}

func NewAccountEventRepo(db *gorm.DB) *AccountEventRepo { return &AccountEventRepo{db: db} }

// Insert 写入一条记录，并定期清理超过保留期的记录。
func (r *AccountEventRepo) Insert(ctx context.Context, ev domain.AccountEvent) error {
	if ev.OccurredAt.IsZero() {
		ev.OccurredAt = time.Now().UTC()
	}
	m := accountEventModel{UpstreamID: ev.UpstreamID, UpstreamName: ev.UpstreamName, Kind: ev.Kind, HTTPStatus: ev.HTTPStatus,
		Message: ev.Message, Tool: ev.Tool, Source: ev.Source, OccurredAt: ev.OccurredAt.UTC(), CooldownS: ev.CooldownS, Level: ev.Level}
	if err := r.db.WithContext(ctx).Create(&m).Error; err != nil {
		return err
	}
	if r.inserts.Add(1)%accountEventPruneEvery == 1 {
		_ = r.Prune(ctx)
	}
	return nil
}

// ListSince 返回 since 之后的记录，按时间倒序，最多 limit 条。
func (r *AccountEventRepo) ListSince(ctx context.Context, since time.Time, limit int) ([]domain.AccountEvent, error) {
	var models []accountEventModel
	if err := r.db.WithContext(ctx).Where("occurred_at >= ?", since.UTC()).Order("occurred_at DESC, id DESC").Limit(limit).Find(&models).Error; err != nil {
		return nil, err
	}
	out := make([]domain.AccountEvent, 0, len(models))
	for _, m := range models {
		out = append(out, domain.AccountEvent{ID: m.ID, UpstreamID: m.UpstreamID, UpstreamName: m.UpstreamName, Kind: m.Kind,
			HTTPStatus: m.HTTPStatus, Message: m.Message, Tool: m.Tool, Source: m.Source, OccurredAt: m.OccurredAt,
			CooldownS: m.CooldownS, Level: m.Level})
	}
	return out, nil
}

// Clear 删除记录；upstreamID 为空时删除全部。
func (r *AccountEventRepo) Clear(ctx context.Context, upstreamID string) error {
	q := r.db.WithContext(ctx)
	if upstreamID != "" {
		return q.Where("upstream_id = ?", upstreamID).Delete(&accountEventModel{}).Error
	}
	return q.Where("1 = 1").Delete(&accountEventModel{}).Error
}

// Prune 删除超过保留期的记录。
func (r *AccountEventRepo) Prune(ctx context.Context) error {
	return r.db.WithContext(ctx).Where("occurred_at < ?", time.Now().UTC().Add(-accountEventRetention)).Delete(&accountEventModel{}).Error
}
