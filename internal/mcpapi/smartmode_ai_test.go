package mcpapi

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/myGithub/mcp-proxy-gateway/internal/discovery"
	"github.com/myGithub/mcp-proxy-gateway/internal/domain"
)

type fakeAssistant struct {
	enabled    bool
	names      []string
	err        error
	candidates []discovery.Candidate
	limit      int
}

func (f *fakeAssistant) RerankEnabled() bool { return f.enabled }

func (f *fakeAssistant) Rerank(_ context.Context, _ string, candidates []discovery.Candidate, limit int) (discovery.RerankResult, error) {
	f.candidates, f.limit = candidates, limit
	if f.err != nil {
		return discovery.RerankResult{}, f.err
	}
	return discovery.RerankResult{Names: f.names, Note: "先 resolve 再 query"}, nil
}

func (f *fakeAssistant) ServiceOverview(discoveries []domain.ToolDiscovery) string {
	return "服务清单：" + discoveries[0].UpstreamNames[0]
}

func aiTestAggregation() *smDetailedAggregation {
	resolve := smTool("resolve-library-id", "Resolve a library name")
	query := smTool("query-docs", "Query library documentation")
	web := smTool("web_search", "Search the web")
	return &smDetailedAggregation{details: []domain.ToolDetail{
		{Tool: resolve, Sources: []domain.ToolSourceView{{UpstreamName: "context7-01"}}},
		{Tool: query, Sources: []domain.ToolSourceView{{UpstreamName: "context7-01"}}},
		{Tool: web, Sources: []domain.ToolSourceView{{UpstreamName: "exa01"}}},
	}}
}

func TestSmartSearchUsesAIPickInSuggestedOrder(t *testing.T) {
	assistant := &fakeAssistant{enabled: true, names: []string{"resolve-library-id", "query-docs"}}
	h := NewSmartModeHandler(aiTestAggregation(), 50)
	h.assistant = assistant
	page, err := h.SearchTools(context.Background(), "k", "react 最新用法", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if page.RankedBy != "ai" || len(page.Tools) != 2 || page.Tools[0].Name != "resolve-library-id" || page.Hint != "先 resolve 再 query" {
		t.Fatalf("应返回 AI 挑选结果：%+v", page)
	}
	if len(assistant.candidates) != 3 || assistant.limit != defaultAIPickLimit || assistant.candidates[0].Service != "context7" {
		t.Fatalf("工具较少时应全部作为候选并带服务名：%+v limit=%d", assistant.candidates, assistant.limit)
	}
}

func TestSmartSearchFallsBackToKeywordWhenAIFailsOrDisabled(t *testing.T) {
	h := NewSmartModeHandler(aiTestAggregation(), 50)
	h.assistant = &fakeAssistant{enabled: true, err: errors.New("timeout")}
	page, err := h.SearchTools(context.Background(), "k", "library documentation", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if page.RankedBy != "" || len(page.Tools) == 0 || page.Tools[0].Name != "query-docs" {
		t.Fatalf("AI 失败应回退关键词结果：%+v", page)
	}
	h.assistant = &fakeAssistant{enabled: true, names: []string{}}
	page, _ = h.SearchTools(context.Background(), "k", "library documentation", "", 0)
	if page.RankedBy != "" || page.Hint != searchHintAIEmpty {
		t.Fatalf("AI 无结果时应回退关键词并说明：%+v", page)
	}
	h.assistant = &fakeAssistant{enabled: false, names: []string{"web_search"}}
	page, _ = h.SearchTools(context.Background(), "k", "library documentation", "", 0)
	if page.RankedBy != "" {
		t.Fatalf("未开启时不应调用 AI：%+v", page)
	}
}

func TestSmartGatewayToolsIncludeOverview(t *testing.T) {
	h := NewSmartModeHandler(aiTestAggregation(), 50)
	if got := h.Overview(context.Background(), "k"); got != "" {
		t.Fatalf("未注入 AI 辅助时不应生成服务清单：%q", got)
	}
	h.assistant = &fakeAssistant{}
	overview := h.Overview(context.Background(), "k")
	if !strings.HasPrefix(overview, "服务清单：") {
		t.Fatalf("服务清单不符：%q", overview)
	}
	for _, tool := range h.GatewayToolsWithOverview(overview) {
		if tool.Name == GatewayToolSearchTools && !strings.Contains(tool.Description, overview) {
			t.Fatalf("search_tools 描述应附上服务清单：%s", tool.Description)
		}
	}
}
