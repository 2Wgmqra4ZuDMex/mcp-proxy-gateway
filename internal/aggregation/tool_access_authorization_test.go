package aggregation

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/myGithub/mcp-proxy-gateway/internal/domain"
)

type fixedToolAllowlist struct {
	allowed    map[string]struct{}
	restricted bool
}

func (a fixedToolAllowlist) AllowedToolNames(_ context.Context, apiKeyID string) (map[string]struct{}, bool, error) {
	if apiKeyID == "" {
		return nil, false, nil
	}
	return a.allowed, a.restricted, nil
}

func newToolAccessTestService(cache *invFakeCache, upstreams ...domain.Upstream) *Service {
	return invNewService(cache,
		&invFakeUpstreams{upstreams: upstreams},
		&invFakeAliases{byUpstream: map[string][]domain.AliasRule{}},
		&invFakeMCPFilters{byUpstream: map[string][]domain.FilterRule{}},
		&invFakeAPIKeyFilters{byAPIKey: map[string][]domain.FilterRule{}},
	)
}

func TestToolAllowlistKeepsOnlySelectedTools(t *testing.T) {
	cache := &invFakeCache{tools: map[string][]domain.ToolDef{
		"up-a": {{OriginalName: "search", Name: "search"}, {OriginalName: "crawl", Name: "crawl"}},
		"up-b": {{OriginalName: "search", Name: "search"}},
	}}
	svc := newToolAccessTestService(cache, invEnabledUpstream("up-a", 0), invEnabledUpstream("up-b", 1))
	svc.SetToolAccessAuthorizer(fixedToolAllowlist{allowed: map[string]struct{}{"search": {}}, restricted: true})

	tools, reverse, err := svc.buildToolSetWithReverseMap(context.Background(), "key-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != 1 || tools[0].Name != "search" {
		t.Fatalf("应只暴露白名单工具，实际：%+v", tools)
	}
	if _, ok := reverse["crawl"]; ok {
		t.Fatal("反向映射不应包含未勾选的工具")
	}
	// 同名工具的多个来源仍全部保留，供轮询与额度切换使用。
	if got := len(reverse["search"].Candidates); got != 2 {
		t.Fatalf("同名工具应保留全部来源，实际 %d", got)
	}
}

func TestToolAllowlistUnrestrictedKeepsAllTools(t *testing.T) {
	cache := &invFakeCache{tools: map[string][]domain.ToolDef{
		"up-a": {{OriginalName: "search", Name: "search"}, {OriginalName: "crawl", Name: "crawl"}},
	}}
	svc := newToolAccessTestService(cache, invEnabledUpstream("up-a", 0))
	svc.SetToolAccessAuthorizer(fixedToolAllowlist{restricted: false})

	tools, _, err := svc.buildToolSetWithReverseMap(context.Background(), "key-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != 2 {
		t.Fatalf("未启用白名单时应暴露全部工具，实际 %d", len(tools))
	}
}

func TestInvokeToolRejectsToolOutsideAllowlist(t *testing.T) {
	cache := &invFakeCache{tools: map[string][]domain.ToolDef{
		"up-a": {{OriginalName: "search", Name: "search"}, {OriginalName: "crawl", Name: "crawl"}},
	}}
	svc := newToolAccessTestService(cache, invEnabledUpstream("up-a", 0))
	invoker := &invRecordingInvoker{}
	svc.SetInvoker(invoker)
	svc.SetToolAccessAuthorizer(fixedToolAllowlist{allowed: map[string]struct{}{"search": {}}, restricted: true})

	if _, err := svc.InvokeTool(context.Background(), "key-1", "crawl", json.RawMessage(`{}`)); err == nil {
		t.Fatal("白名单外的工具调用应被拒绝")
	}
	if invoker.called {
		t.Fatal("白名单拒绝后不得转发调用")
	}
}
