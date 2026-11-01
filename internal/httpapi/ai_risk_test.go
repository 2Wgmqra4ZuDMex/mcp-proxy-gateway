package httpapi

import (
	"context"
	"net/http"
	"testing"

	"github.com/myGithub/mcp-proxy-gateway/internal/domain"
	"github.com/myGithub/mcp-proxy-gateway/internal/risk"
	"github.com/myGithub/mcp-proxy-gateway/internal/store"
)

type fakeToolRiskStore struct{}

func (fakeToolRiskStore) Get(context.Context, string, string) (risk.Assessment, error) {
	return risk.Assessment{}, nil
}

func (fakeToolRiskStore) List(context.Context, store.RiskListQuery) (store.RiskListResult, error) {
	return store.RiskListResult{}, nil
}

func (fakeToolRiskStore) Reconcile(context.Context, string, []domain.ToolDef) (store.ReconcileResult, error) {
	return store.ReconcileResult{}, nil
}

func (fakeToolRiskStore) SetManualOverride(context.Context, string, string, risk.Level, []string, string, bool) (risk.Assessment, error) {
	return risk.Assessment{}, nil
}

func (fakeToolRiskStore) BulkSetManualOverride(context.Context, []store.RiskOverrideTarget, risk.Level, []string, string, bool) ([]risk.Assessment, error) {
	return nil, nil
}

func (fakeToolRiskStore) ClearManualOverride(context.Context, string, string) (risk.Assessment, error) {
	return risk.Assessment{}, nil
}

// TestRiskToolListAvailableWithoutAIProvider 验证移除 AI 评级后，工具风险目录仍可查询。
func TestRiskToolListAvailableWithoutAIProvider(t *testing.T) {
	engine := newTestEngine(Deps{ToolRiskStore: fakeToolRiskStore{}})

	response := doJSON(engine, http.MethodGet, "/api/admin/ai-risk/tools", "")
	if response.Code != http.StatusOK {
		t.Fatalf("工具风险目录应可查询，status=%d body=%s", response.Code, response.Body.String())
	}
}

// TestAIProviderRoutesRemoved 验证 AI Provider 与评级任务端点已不再注册。
func TestAIProviderRoutesRemoved(t *testing.T) {
	engine := newTestEngine(Deps{ToolRiskStore: fakeToolRiskStore{}})

	for _, path := range []string{"/api/admin/ai-risk/providers", "/api/admin/ai-risk/jobs"} {
		response := doJSON(engine, http.MethodGet, path, "")
		if response.Code != http.StatusNotFound {
			t.Fatalf("%s 应返回 404，实际 %d", path, response.Code)
		}
	}
}
