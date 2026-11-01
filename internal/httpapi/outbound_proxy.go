package httpapi

import (
	"context"

	"github.com/gin-gonic/gin"

	"github.com/myGithub/mcp-proxy-gateway/internal/audit"
	"github.com/myGithub/mcp-proxy-gateway/internal/outbound"
)

// OutboundProxyService 是出站代理管理依赖的窄接口；*outbound.Service 满足该接口。
type OutboundProxyService interface {
	List(ctx context.Context) ([]outbound.Proxy, error)
	Create(ctx context.Context, in outbound.Input) (outbound.Proxy, error)
	Update(ctx context.Context, id string, in outbound.Input) (outbound.Proxy, error)
	Delete(ctx context.Context, id string) error
	Test(ctx context.Context, id string) (outbound.TestResult, error)
	Assign(ctx context.Context, upstreamIDs []string, proxyID string) (outbound.AssignResult, error)
	Distribute(ctx context.Context, upstreamIDs, proxyIDs []string) (outbound.AssignResult, error)
}

type proxyAssignRequest struct {
	UpstreamIDs []string `json:"upstreamIds"`
	ProxyID     string   `json:"proxyId"`
}

type proxyDistributeRequest struct {
	UpstreamIDs []string `json:"upstreamIds"`
	ProxyIDs    []string `json:"proxyIds"`
}

// registerOutboundProxyRoutes 注册出站代理管理端点（/api/admin/outbound-proxies）。
// 静态子路由 assign / distribute 先于参数路由注册。
func (r *Router) registerOutboundProxyRoutes(g *gin.RouterGroup) {
	p := g.Group("/outbound-proxies")
	p.GET("", r.listOutboundProxies)
	p.POST("", r.createOutboundProxy)
	p.POST("/assign", r.assignOutboundProxy)
	p.POST("/distribute", r.distributeOutboundProxies)
	p.PUT("/:id", r.updateOutboundProxy)
	p.DELETE("/:id", r.deleteOutboundProxy)
	p.POST("/:id/test", r.testOutboundProxy)
}

func (r *Router) requireOutboundProxies(c *gin.Context) bool {
	if r.outboundProxies == nil {
		respondServiceUnavailable(c, "出站代理服务未就绪")
		return false
	}
	return true
}

func (r *Router) listOutboundProxies(c *gin.Context) {
	if !r.requireOutboundProxies(c) {
		return
	}
	items, err := r.outboundProxies.List(c.Request.Context())
	if err != nil {
		respondError(c, err)
		return
	}
	respondOK(c, gin.H{"proxies": items})
}

func (r *Router) createOutboundProxy(c *gin.Context) {
	if !r.requireOutboundProxies(c) {
		return
	}
	var in outbound.Input
	if !bindJSON(c, &in) {
		return
	}
	item, err := r.outboundProxies.Create(c.Request.Context(), in)
	if err != nil {
		respondError(c, err)
		return
	}
	r.recordCreate(c, audit.ResourceSetting, "outbound-proxy:"+item.Name)
	respondCreated(c, item)
}

func (r *Router) updateOutboundProxy(c *gin.Context) {
	if !r.requireOutboundProxies(c) {
		return
	}
	var in outbound.Input
	if !bindJSON(c, &in) {
		return
	}
	item, err := r.outboundProxies.Update(c.Request.Context(), c.Param("id"), in)
	if err != nil {
		respondError(c, err)
		return
	}
	r.invalidateToolSetCache()
	r.recordUpdate(c, audit.ResourceSetting, "outbound-proxy:"+item.Name)
	respondOK(c, item)
}

func (r *Router) deleteOutboundProxy(c *gin.Context) {
	if !r.requireOutboundProxies(c) {
		return
	}
	if err := r.outboundProxies.Delete(c.Request.Context(), c.Param("id")); err != nil {
		respondError(c, err)
		return
	}
	r.recordDelete(c, audit.ResourceSetting, "outbound-proxy:"+c.Param("id"))
	respondNoContent(c)
}

func (r *Router) testOutboundProxy(c *gin.Context) {
	if !r.requireOutboundProxies(c) {
		return
	}
	result, err := r.outboundProxies.Test(c.Request.Context(), c.Param("id"))
	if err != nil {
		respondError(c, err)
		return
	}
	respondOK(c, result)
}

func (r *Router) assignOutboundProxy(c *gin.Context) {
	if !r.requireOutboundProxies(c) {
		return
	}
	var req proxyAssignRequest
	if !bindJSON(c, &req) {
		return
	}
	result, err := r.outboundProxies.Assign(c.Request.Context(), req.UpstreamIDs, req.ProxyID)
	if err != nil {
		respondError(c, err)
		return
	}
	r.invalidateToolSetCache()
	r.recordUpdate(c, audit.ResourceUpstream, "outbound-proxy-assign")
	respondOK(c, result)
}

func (r *Router) distributeOutboundProxies(c *gin.Context) {
	if !r.requireOutboundProxies(c) {
		return
	}
	var req proxyDistributeRequest
	if !bindJSON(c, &req) {
		return
	}
	result, err := r.outboundProxies.Distribute(c.Request.Context(), req.UpstreamIDs, req.ProxyIDs)
	if err != nil {
		respondError(c, err)
		return
	}
	r.invalidateToolSetCache()
	r.recordUpdate(c, audit.ResourceUpstream, "outbound-proxy-distribute")
	respondOK(c, result)
}
