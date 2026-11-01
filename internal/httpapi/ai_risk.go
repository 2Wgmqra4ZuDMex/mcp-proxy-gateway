package httpapi

import (
	"context"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/myGithub/mcp-proxy-gateway/internal/audit"
	"github.com/myGithub/mcp-proxy-gateway/internal/domain"
	"github.com/myGithub/mcp-proxy-gateway/internal/risk"
	"github.com/myGithub/mcp-proxy-gateway/internal/store"
)

type manualOverrideRequest struct {
	Level  risk.Level `json:"level"`
	Tags   []string   `json:"tags"`
	Reason string     `json:"reason"`
	Force  bool       `json:"force"`
}

type bulkOverrideRequest struct {
	Items []struct {
		UpstreamID   string `json:"upstreamId"`
		OriginalName string `json:"originalName"`
	} `json:"items"`
	Level  risk.Level `json:"level"`
	Tags   []string   `json:"tags"`
	Reason string     `json:"reason"`
	Force  bool       `json:"force"`
}

func (r *Router) registerAIRiskRoutes(g *gin.RouterGroup) {
	group := g.Group("/ai-risk")
	group.GET("/tools", r.listRiskTools)
	group.POST("/tools/bulk-override", r.bulkRiskOverride)
	group.GET("/tools/:upstreamId/:originalName", r.getRiskTool)
	group.PUT("/tools/:upstreamId/:originalName/manual-override", r.setRiskOverride)
	group.DELETE("/tools/:upstreamId/:originalName/manual-override", r.clearRiskOverride)
	group.POST("/reconcile", r.reconcileRiskCatalog)
}

func (r *Router) requireAIRisk(c *gin.Context) bool {
	if r.toolRiskStore == nil {
		respondServiceUnavailable(c, "工具风险服务未就绪")
		return false
	}
	return true
}

func (r *Router) listRiskTools(c *gin.Context) {
	if !r.requireAIRisk(c) {
		return
	}
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	size, _ := strconv.Atoi(c.DefaultQuery("pageSize", "50"))
	var minConfidence *float64
	if raw := c.Query("minConfidence"); raw != "" {
		value, err := strconv.ParseFloat(raw, 64)
		if err != nil || value < 0 || value > 1 {
			respondError(c, domain.NewError(domain.CodeValidation, "minConfidence 必须在 0 到 1 之间"))
			return
		}
		minConfidence = &value
	}
	result, err := r.toolRiskStore.List(c.Request.Context(), store.RiskListQuery{
		UpstreamID: c.Query("upstreamId"), Status: risk.Status(c.Query("status")), Keyword: c.Query("keyword"),
		Level: risk.Level(c.Query("level")), ManualOnly: c.Query("manualOnly") == "true", MinConfidence: minConfidence, Page: page, PageSize: size,
	})
	if err != nil {
		respondError(c, err)
		return
	}
	r.attachRiskUpstreamNames(c.Request.Context(), result.Items)
	respondOK(c, result)
}

func (r *Router) attachRiskUpstreamNames(ctx context.Context, items []risk.Assessment) {
	if r.upstream == nil || len(items) == 0 {
		return
	}
	upstreams, err := r.upstream.List(ctx)
	if err != nil {
		return
	}
	names := make(map[string]string, len(upstreams))
	for _, upstream := range upstreams {
		names[upstream.ID] = upstream.Config.Name
	}
	for i := range items {
		items[i].UpstreamName = names[items[i].UpstreamID]
	}
}

func (r *Router) getRiskTool(c *gin.Context) {
	if !r.requireAIRisk(c) {
		return
	}
	item, err := r.toolRiskStore.Get(c.Request.Context(), c.Param("upstreamId"), c.Param("originalName"))
	if err != nil {
		respondError(c, err)
		return
	}
	items := []risk.Assessment{item}
	r.attachRiskUpstreamNames(c.Request.Context(), items)
	item = items[0]
	respondOK(c, item)
}
func (r *Router) bulkRiskOverride(c *gin.Context) {
	if !r.requireAIRisk(c) {
		return
	}
	var req bulkOverrideRequest
	if !bindJSON(c, &req) {
		return
	}
	if len(req.Items) == 0 || len(req.Items) > 500 {
		respondError(c, domain.NewError(domain.CodeValidation, "批量覆写需要包含 1 到 500 个工具"))
		return
	}
	targets := make([]store.RiskOverrideTarget, 0, len(req.Items))
	for _, target := range req.Items {
		targets = append(targets, store.RiskOverrideTarget{UpstreamID: target.UpstreamID, OriginalName: target.OriginalName})
	}
	updated, err := r.toolRiskStore.BulkSetManualOverride(c.Request.Context(), targets, req.Level, req.Tags, req.Reason, req.Force)
	if err != nil {
		respondError(c, err)
		return
	}
	r.invalidateToolSetCache()
	r.recordUpdate(c, audit.ResourceSetting, "ai-risk-bulk-override")
	respondOK(c, gin.H{"items": updated, "updated": len(updated)})
}
func (r *Router) setRiskOverride(c *gin.Context) {
	if !r.requireAIRisk(c) {
		return
	}
	var req manualOverrideRequest
	if !bindJSON(c, &req) {
		return
	}
	item, err := r.toolRiskStore.SetManualOverride(c.Request.Context(), c.Param("upstreamId"), c.Param("originalName"), req.Level, req.Tags, req.Reason, req.Force)
	if err != nil {
		respondError(c, err)
		return
	}
	r.invalidateToolSetCache()
	r.recordUpdate(c, audit.ResourceSetting, item.ID)
	respondOK(c, item)
}
func (r *Router) clearRiskOverride(c *gin.Context) {
	if !r.requireAIRisk(c) {
		return
	}
	item, err := r.toolRiskStore.ClearManualOverride(c.Request.Context(), c.Param("upstreamId"), c.Param("originalName"))
	if err != nil {
		respondError(c, err)
		return
	}
	r.invalidateToolSetCache()
	r.recordUpdate(c, audit.ResourceSetting, item.ID)
	respondOK(c, item)
}
func (r *Router) reconcileRiskCatalog(c *gin.Context) {
	if !r.requireAIRisk(c) {
		return
	}
	// 同步遍历所有上游逐个 Reconcile 属于低频管理操作，加 30s 超时避免长时间阻塞。
	ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
	defer cancel()
	upstreams, err := r.upstream.List(ctx)
	if err != nil {
		respondError(c, err)
		return
	}
	total := store.ReconcileResult{}
	for _, upstream := range upstreams {
		tools, _, _ := r.toolCache.Get(ctx, upstream.ID)
		result, reconcileErr := r.toolRiskStore.Reconcile(ctx, upstream.ID, tools)
		if reconcileErr != nil {
			respondError(c, reconcileErr)
			return
		}
		total.Added += result.Added
		total.Changed += result.Changed
		total.Removed += result.Removed
		total.Current += result.Current
	}
	r.invalidateToolSetCache()
	r.recordUpdate(c, audit.ResourceSetting, "ai-risk-reconcile")
	respondOK(c, total)
}
