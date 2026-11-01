package store

import (
	"context"
	"encoding/json"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/myGithub/mcp-proxy-gateway/internal/discovery"
)

// toolEnrichmentModel 保存 AI 生成的工具/服务检索元数据，主键为指纹。
// 数据可随时重新生成，因此不参与备份。
type toolEnrichmentModel struct {
	Hash      string    `gorm:"column:hash;type:varchar(64);primaryKey"`
	Kind      string    `gorm:"column:kind;type:varchar(16);not null;default:'tool'"`
	Name      string    `gorm:"column:name;type:varchar(256);not null;default:''"`
	Summary   string    `gorm:"column:summary;type:text;not null;default:''"`
	Keywords  string    `gorm:"column:keywords;type:text;not null;default:'[]'"`
	UseWhen   string    `gorm:"column:use_when;type:text;not null;default:''"`
	Related   string    `gorm:"column:related;type:text;not null;default:'[]'"`
	Model     string    `gorm:"column:model;type:varchar(200);not null;default:''"`
	UpdatedAt time.Time `gorm:"column:updated_at;type:timestamptz;not null;default:now();autoUpdateTime:false"`
}

func (toolEnrichmentModel) TableName() string { return "tool_enrichment" }

type ToolEnrichmentRepo struct{ db *gorm.DB }

func NewToolEnrichmentRepo(db *gorm.DB) *ToolEnrichmentRepo { return &ToolEnrichmentRepo{db: db} }

func (r *ToolEnrichmentRepo) List(ctx context.Context) ([]discovery.Enrichment, error) {
	var models []toolEnrichmentModel
	if err := r.db.WithContext(ctx).Order("kind, name").Find(&models).Error; err != nil {
		return nil, err
	}
	out := make([]discovery.Enrichment, 0, len(models))
	for _, m := range models {
		out = append(out, enrichmentFromModel(m))
	}
	return out, nil
}

func (r *ToolEnrichmentRepo) Upsert(ctx context.Context, items []discovery.Enrichment) error {
	if len(items) == 0 {
		return nil
	}
	models := make([]toolEnrichmentModel, 0, len(items))
	for _, item := range items {
		models = append(models, enrichmentToModel(item))
	}
	return r.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "hash"}},
		DoUpdates: clause.AssignmentColumns([]string{"kind", "name", "summary", "keywords", "use_when", "related", "model", "updated_at"}),
	}).Create(&models).Error
}

func enrichmentToModel(e discovery.Enrichment) toolEnrichmentModel {
	keywords, _ := json.Marshal(nonNilStrings(e.Keywords))
	related, _ := json.Marshal(nonNilStrings(e.Related))
	updated := e.UpdatedAt
	if updated.IsZero() {
		updated = time.Now().UTC()
	}
	return toolEnrichmentModel{Hash: e.Hash, Kind: e.Kind, Name: e.Name, Summary: e.Summary,
		Keywords: string(keywords), UseWhen: e.UseWhen, Related: string(related), Model: e.Model, UpdatedAt: updated}
}

func enrichmentFromModel(m toolEnrichmentModel) discovery.Enrichment {
	var keywords, related []string
	_ = json.Unmarshal([]byte(m.Keywords), &keywords)
	_ = json.Unmarshal([]byte(m.Related), &related)
	return discovery.Enrichment{Hash: m.Hash, Kind: m.Kind, Name: m.Name, Summary: m.Summary,
		Keywords: keywords, UseWhen: m.UseWhen, Related: related, Model: m.Model, UpdatedAt: m.UpdatedAt}
}

func nonNilStrings(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}
