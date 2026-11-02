package httpapi

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/myGithub/mcp-proxy-gateway/internal/audit"
	"github.com/myGithub/mcp-proxy-gateway/internal/discovery"
	"github.com/myGithub/mcp-proxy-gateway/internal/domain"
	"github.com/myGithub/mcp-proxy-gateway/internal/mcpapi"
)

// SmartDiscoveryService 为智能发现（AI 接口配置、工具说明增强）的窄接口。
type SmartDiscoveryService interface {
	Config(ctx context.Context) discovery.ConfigView
	SaveConfig(ctx context.Context, in discovery.ConfigInput) (discovery.ConfigView, error)
	TestConnection(ctx context.Context, in discovery.ConfigInput) (discovery.TestResult, error)
	ListModels(ctx context.Context, in discovery.ConfigInput) ([]string, error)
	StartEnrich(force bool) error
	Status(ctx context.Context) discovery.Status
	EnrichedTools(ctx context.Context) ([]discovery.EnrichedTool, map[string]string, error)
}

// SmartSearchPreviewer 为管理台试搜：对比关键词检索与 AI 挑选。
type SmartSearchPreviewer interface {
	PreviewSearch(ctx context.Context, apiKeyID, query string) (mcpapi.SearchPage, *mcpapi.SearchPage, bool, error, error)
}

type smartDiscoveryEnrichRequest struct {
	Force bool `json:"force"`
}

type smartDiscoveryPreviewRequest struct {
	Query    string `json:"query"`
	APIKeyID string `json:"apiKeyId"`
}

type smartDiscoveryPreviewResponse struct {
	Keyword   mcpapi.SearchPage       `json:"keyword"`
	AI        *mcpapi.SearchPage      `json:"ai,omitempty"`
	AIEnabled bool                    `json:"aiEnabled"`
	AIError   string                  `json:"aiError,omitempty"`
	AILatency int64                   `json:"aiLatencyMs,omitempty"`
	AIInfo    *discovery.RerankResult `json:"aiInfo,omitempty"`
}

func (r *Router) registerSmartDiscoveryRoutes(g *gin.RouterGroup) {
	d := g.Group("/smart-discovery")
	d.GET("", r.getSmartDiscovery)
	d.PUT("", r.saveSmartDiscovery)
	d.POST("/test", r.testSmartDiscovery)
	d.POST("/models", r.listSmartDiscoveryModels)
	d.POST("/enrich", r.startSmartDiscoveryEnrich)
	d.GET("/tools", r.listSmartDiscoveryTools)
	d.POST("/preview", r.previewSmartDiscovery)
}

func (r *Router) requireSmartDiscovery(c *gin.Context) bool {
	if r.smartDiscovery == nil {
		respondServiceUnavailable(c, "智能发现服务未就绪")
		return false
	}
	return true
}

func (r *Router) getSmartDiscovery(c *gin.Context) {
	if !r.requireSmartDiscovery(c) {
		return
	}
	ctx := c.Request.Context()
	respondOK(c, gin.H{"config": r.smartDiscovery.Config(ctx), "status": r.smartDiscovery.Status(ctx)})
}

func (r *Router) saveSmartDiscovery(c *gin.Context) {
	if !r.requireSmartDiscovery(c) {
		return
	}
	var in discovery.ConfigInput
	if !bindJSON(c, &in) {
		return
	}
	view, err := r.smartDiscovery.SaveConfig(c.Request.Context(), in)
	if err != nil {
		respondError(c, err)
		return
	}
	r.recordUpdate(c, audit.ResourceSetting, "smart-discovery")
	respondOK(c, gin.H{"config": view, "status": r.smartDiscovery.Status(c.Request.Context())})
}

func (r *Router) testSmartDiscovery(c *gin.Context) {
	if !r.requireSmartDiscovery(c) {
		return
	}
	var in discovery.ConfigInput
	if !bindJSON(c, &in) {
		return
	}
	result, err := r.smartDiscovery.TestConnection(c.Request.Context(), in)
	if err != nil {
		respondError(c, err)
		return
	}
	respondOK(c, result)
}

func (r *Router) listSmartDiscoveryModels(c *gin.Context) {
	if !r.requireSmartDiscovery(c) {
		return
	}
	var in discovery.ConfigInput
	if !bindJSON(c, &in) {
		return
	}
	models, err := r.smartDiscovery.ListModels(c.Request.Context(), in)
	if err != nil {
		respondError(c, err)
		return
	}
	respondOK(c, gin.H{"models": models})
}

func (r *Router) startSmartDiscoveryEnrich(c *gin.Context) {
	if !r.requireSmartDiscovery(c) {
		return
	}
	var in smartDiscoveryEnrichRequest
	if !bindJSON(c, &in) {
		return
	}
	if err := r.smartDiscovery.StartEnrich(in.Force); err != nil {
		var derr *domain.APIError
		if errors.As(err, &derr) {
			respondError(c, err)
			return
		}
		respondError(c, domain.NewError(domain.CodeConflict, err.Error()))
		return
	}
	respondOK(c, r.smartDiscovery.Status(c.Request.Context()))
}

func (r *Router) listSmartDiscoveryTools(c *gin.Context) {
	if !r.requireSmartDiscovery(c) {
		return
	}
	tools, services, err := r.smartDiscovery.EnrichedTools(c.Request.Context())
	if err != nil {
		respondError(c, err)
		return
	}
	respondOK(c, gin.H{"tools": tools, "services": services})
}

func (r *Router) previewSmartDiscovery(c *gin.Context) {
	if r.smartPreview == nil {
		respondServiceUnavailable(c, "智能模式服务未就绪")
		return
	}
	var in smartDiscoveryPreviewRequest
	if !bindJSON(c, &in) {
		return
	}
	query := strings.TrimSpace(in.Query)
	if query == "" {
		respondError(c, domain.NewValidationError("请输入搜索内容", map[string]string{"query": "不能为空"}))
		return
	}
	started := time.Now()
	keyword, ai, aiEnabled, aiErr, err := r.smartPreview.PreviewSearch(c.Request.Context(), strings.TrimSpace(in.APIKeyID), query)
	if err != nil {
		respondError(c, err)
		return
	}
	resp := smartDiscoveryPreviewResponse{Keyword: keyword, AI: ai, AIEnabled: aiEnabled}
	if ai != nil {
		resp.AIInfo = ai.AI
	}
	if aiEnabled {
		resp.AILatency = time.Now().Sub(started).Milliseconds()
	}
	if aiErr != nil {
		resp.AIError = aiErr.Error()
	}
	respondOK(c, resp)
}
