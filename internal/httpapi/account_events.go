package httpapi

import (
	"context"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/myGithub/mcp-proxy-gateway/internal/audit"
	"github.com/myGithub/mcp-proxy-gateway/internal/domain"
)

// AccountCooldown 为当前正在冷却（被临时跳过）的账号。
type AccountCooldown struct {
	UpstreamID string    `json:"upstreamId"`
	Kind       string    `json:"kind"`
	Message    string    `json:"message"`
	Until      time.Time `json:"until"`
	// Level 为连续被拒次数：暂停时间随之逐级延长。
	Level int `json:"level"`
}

// AccountEventSummary 为单个账号近期的异常统计。
type AccountEventSummary struct {
	UpstreamID   string    `json:"upstreamId"`
	UpstreamName string    `json:"upstreamName"`
	Last24h      int       `json:"last24h"`
	Last7d       int       `json:"last7d"`
	LastKind     string    `json:"lastKind"`
	LastStatus   int       `json:"lastStatus,omitempty"`
	LastMessage  string    `json:"lastMessage"`
	LastTool     string    `json:"lastTool,omitempty"`
	LastAt       time.Time `json:"lastAt"`
}

// AccountEventStatus 为账号异常页面的数据。
type AccountEventStatus struct {
	Cooldowns []AccountCooldown     `json:"cooldowns"`
	Accounts  []AccountEventSummary `json:"accounts"`
	Events    []domain.AccountEvent `json:"events"`
}

// AccountEventService 为账号异常记录的窄接口。
type AccountEventService interface {
	Status(ctx context.Context) (AccountEventStatus, error)
	Clear(ctx context.Context, upstreamID string) error
}

func (r *Router) registerAccountEventRoutes(g *gin.RouterGroup) {
	a := g.Group("/account-events")
	a.GET("", r.getAccountEvents)
	a.DELETE("", r.clearAccountEvents)
}

func (r *Router) getAccountEvents(c *gin.Context) {
	if r.accountEvents == nil {
		respondServiceUnavailable(c, "账号异常记录服务未就绪")
		return
	}
	status, err := r.accountEvents.Status(c.Request.Context())
	if err != nil {
		respondError(c, err)
		return
	}
	respondOK(c, status)
}

func (r *Router) clearAccountEvents(c *gin.Context) {
	if r.accountEvents == nil {
		respondServiceUnavailable(c, "账号异常记录服务未就绪")
		return
	}
	upstreamID := c.Query("upstreamId")
	if err := r.accountEvents.Clear(c.Request.Context(), upstreamID); err != nil {
		respondError(c, err)
		return
	}
	target := "account-events"
	if upstreamID != "" {
		target += ":" + upstreamID
	}
	r.recordDelete(c, audit.ResourceUpstream, target)
	respondNoContent(c)
}
