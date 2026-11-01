package discovery

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/myGithub/mcp-proxy-gateway/internal/domain"
	"github.com/myGithub/mcp-proxy-gateway/internal/risk"
)

type memProviders struct {
	mu   sync.Mutex
	list []risk.Provider
}

func (m *memProviders) List(context.Context) ([]risk.Provider, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]risk.Provider(nil), m.list...), nil
}

func (m *memProviders) Create(_ context.Context, p risk.Provider) (risk.Provider, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p.ID = "p1"
	m.list = append(m.list, p)
	return p, nil
}

func (m *memProviders) Update(_ context.Context, p risk.Provider) (risk.Provider, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.list {
		if m.list[i].ID == p.ID {
			p.Active = m.list[i].Active
			m.list[i] = p
			return p, nil
		}
	}
	return risk.Provider{}, errors.New("not found")
}

func (m *memProviders) Activate(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.list {
		m.list[i].Active = m.list[i].ID == id
	}
	return nil
}

type memEnrichments struct {
	mu    sync.Mutex
	items map[string]Enrichment
}

func (m *memEnrichments) List(context.Context) ([]Enrichment, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []Enrichment{}
	for _, item := range m.items {
		out = append(out, item)
	}
	return out, nil
}

func (m *memEnrichments) Upsert(_ context.Context, items []Enrichment) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, item := range items {
		m.items[item.Hash] = item
	}
	return nil
}

// fakeLLM 模拟 OpenAI 兼容接口：按 system 提示词分辨用途，回复包在 ```json 代码块里。
type fakeLLM struct {
	hits     atomic.Int32
	rerank   []string
	authSeen atomic.Value
	failNext atomic.Bool
}

func (f *fakeLLM) handler(w http.ResponseWriter, r *http.Request) {
	f.hits.Add(1)
	f.authSeen.Store(r.Header.Get("Authorization"))
	if f.failNext.Swap(false) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"invalid api key"}}`))
		return
	}
	var req struct {
		Messages []struct {
			Content string `json:"content"`
		} `json:"messages"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	system, user := req.Messages[0].Content, req.Messages[1].Content
	var reply string
	switch {
	case strings.Contains(system, "连通性测试"):
		reply = "OK"
	case strings.Contains(system, "MCP 工具检索助手"):
		var in struct {
			Tools []struct{ ID, Name string } `json:"tools"`
		}
		_ = json.Unmarshal([]byte(user), &in)
		out := []map[string]any{}
		for _, tool := range in.Tools {
			out = append(out, map[string]any{"id": tool.ID, "summary": tool.Name + " 的中文简介",
				"keywords": []string{"网页抓取", "爬虫", "scrape", "网页抓取"}, "useWhen": "需要读取网页时", "related": []string{"not_a_sibling"}})
		}
		b, _ := json.Marshal(map[string]any{"tools": out})
		reply = "```json\n" + string(b) + "\n```"
	case strings.Contains(system, "MCP 服务写一句中文简介"):
		var in struct {
			Services []struct{ ID string } `json:"services"`
		}
		_ = json.Unmarshal([]byte(user), &in)
		out := []map[string]any{}
		for _, svc := range in.Services {
			out = append(out, map[string]any{"id": svc.ID, "summary": "网页搜索与抓取"})
		}
		b, _ := json.Marshal(map[string]any{"services": out})
		reply = string(b)
	case strings.Contains(system, "工具路由助手"):
		b, _ := json.Marshal(map[string]any{"tools": f.rerank, "note": "先搜索再抓取"})
		reply = "好的：" + string(b)
	}
	_ = json.NewEncoder(w).Encode(map[string]any{
		"choices": []map[string]any{{"message": map[string]string{"content": reply}}},
		"usage":   map[string]int{"prompt_tokens": 10, "completion_tokens": 5},
	})
}

func testTools() []domain.ToolDiscovery {
	return []domain.ToolDiscovery{
		{Tool: domain.ToolDef{Name: "tavily_search", OriginalName: "tavily_search", Description: "Search the web"}, UpstreamNames: []string{"tavily01", "tavily02"}},
		{Tool: domain.ToolDef{Name: "tavily_extract", OriginalName: "tavily_extract", Description: "Extract page content",
			InputSchema: json.RawMessage(`{"properties":{"urls":{},"format":{}},"required":["urls"]}`)}, UpstreamNames: []string{"tavily01"}},
		{Tool: domain.ToolDef{Name: "query-docs", OriginalName: "query-docs", Description: "Query library docs"}, UpstreamNames: []string{"context7-01"}},
	}
}

func newTestService(t *testing.T, llm *fakeLLM, resolver ProxyResolver) (*Service, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(llm.handler))
	t.Cleanup(srv.Close)
	svc := New(&memProviders{}, &memEnrichments{items: map[string]Enrichment{}}, resolver, nil)
	svc.SetToolSource(func(context.Context) ([]domain.ToolDiscovery, error) { return testTools(), nil })
	return svc, srv
}

func waitIdle(t *testing.T, svc *Service) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if !svc.snapshotStatus().Running {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("生成任务未在 5 秒内结束")
}

func TestSaveConfigCreatesProviderAndKeepsKeyWhenBlank(t *testing.T) {
	llm := &fakeLLM{}
	svc, srv := newTestService(t, llm, nil)
	ctx := context.Background()
	if _, err := svc.SaveConfig(ctx, ConfigInput{Model: "m"}); err == nil {
		t.Fatal("缺少接口地址时应报校验错误")
	}
	view, err := svc.SaveConfig(ctx, ConfigInput{BaseURL: srv.URL + "/v1/", Model: "m", APIKey: "sk-secret-1234"})
	if err != nil {
		t.Fatal(err)
	}
	if !view.Configured || !view.HasKey || view.KeyHint != "…1234" || view.BaseURL != srv.URL+"/v1" {
		t.Fatalf("配置视图不符：%+v", view)
	}
	if _, err := svc.SaveConfig(ctx, ConfigInput{BaseURL: srv.URL + "/v1", Model: "m2", RerankEnabled: true}); err != nil {
		t.Fatal(err)
	}
	p, _ := svc.currentProvider()
	if p.APIKey != "sk-secret-1234" || p.Model != "m2" || !p.RerankEnabled || !p.Active {
		t.Fatalf("Key 留空应保留，其余字段应更新：%+v", p)
	}
	res, err := svc.TestConnection(ctx, ConfigInput{BaseURL: srv.URL + "/v1", Model: "m2"})
	if err != nil || res.Reply != "OK" || llm.authSeen.Load() != "Bearer sk-secret-1234" {
		t.Fatalf("测试连接应使用已保存的 Key：%+v %v", res, err)
	}
}

func TestEnrichGeneratesOncePerFingerprintAndFeedsKeywords(t *testing.T) {
	llm := &fakeLLM{}
	svc, srv := newTestService(t, llm, nil)
	ctx := context.Background()
	invalidated := atomic.Int32{}
	svc.SetInvalidate(func() { invalidated.Add(1) })
	if _, err := svc.SaveConfig(ctx, ConfigInput{BaseURL: srv.URL, Model: "m", EnrichEnabled: false}); err != nil {
		t.Fatal(err)
	}
	if err := svc.StartEnrich(false); err != nil {
		t.Fatal(err)
	}
	waitIdle(t, svc)
	st := svc.Status(ctx)
	if st.Total != 3 || st.Enriched != 3 || st.Pending != 0 || st.Services != 2 || st.LastAdded != 3 {
		t.Fatalf("应为 3 个工具与 2 个服务生成说明：%+v", st)
	}
	if invalidated.Load() == 0 {
		t.Fatal("生成后应触发索引重建")
	}
	if got := svc.ToolKeywords("tavily_search", "Search the web"); got != "" {
		t.Fatalf("未开启工具说明增强时不应注入索引：%q", got)
	}
	if _, err := svc.SaveConfig(ctx, ConfigInput{BaseURL: srv.URL, Model: "m", EnrichEnabled: true}); err != nil {
		t.Fatal(err)
	}
	got := svc.ToolKeywords("tavily_search", "Search the web")
	if !strings.Contains(got, "网页抓取") || strings.Count(got, "网页抓取") != 1 || !strings.Contains(got, "需要读取网页时") {
		t.Fatalf("增强文本应含去重后的关键词与适用场景：%q", got)
	}
	tools, services, err := svc.EnrichedTools(ctx)
	if err != nil || len(tools) != 3 || services["tavily"] != "网页搜索与抓取" {
		t.Fatalf("说明列表不符：%+v %+v %v", tools, services, err)
	}
	for _, tool := range tools {
		if len(tool.Related) != 0 {
			t.Fatalf("related 只能取同服务工具：%+v", tool)
		}
	}
	hits := llm.hits.Load()
	if err := svc.StartEnrich(false); err != nil {
		t.Fatal(err)
	}
	waitIdle(t, svc)
	if llm.hits.Load() != hits {
		t.Fatal("指纹未变化时不应重复调用模型")
	}
	overview := svc.ServiceOverview(testTools())
	if !strings.Contains(overview, "- context7：网页搜索与抓取（query-docs）") || !strings.Contains(overview, "- tavily") {
		t.Fatalf("服务清单不符：%s", overview)
	}
}

func TestEnrichRecordsProviderErrors(t *testing.T) {
	llm := &fakeLLM{}
	svc, srv := newTestService(t, llm, nil)
	if _, err := svc.SaveConfig(context.Background(), ConfigInput{BaseURL: srv.URL, Model: "m"}); err != nil {
		t.Fatal(err)
	}
	llm.failNext.Store(true)
	if err := svc.StartEnrich(false); err != nil {
		t.Fatal(err)
	}
	waitIdle(t, svc)
	st := svc.snapshotStatus()
	if st.Failed != 3 || !strings.Contains(st.LastError, "invalid api key") {
		t.Fatalf("接口报错应计入失败并给出原因：%+v", st)
	}
}

func TestRerankFiltersUnknownNamesAndCaches(t *testing.T) {
	llm := &fakeLLM{rerank: []string{"tavily_search", "made_up_tool", "tavily_search", "tavily_extract"}}
	svc, srv := newTestService(t, llm, nil)
	ctx := context.Background()
	cands := []Candidate{{Name: "tavily_search"}, {Name: "tavily_extract"}, {Name: "query-docs"}}
	if _, err := svc.Rerank(ctx, "搜网页", cands, 5); err == nil {
		t.Fatal("未开启 AI 挑选时应报错")
	}
	if _, err := svc.SaveConfig(ctx, ConfigInput{BaseURL: srv.URL, Model: "m", RerankEnabled: true}); err != nil {
		t.Fatal(err)
	}
	res, err := svc.Rerank(ctx, "搜网页", cands, 5)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(res.Names, ",") != "tavily_search,tavily_extract" || res.Note != "先搜索再抓取" {
		t.Fatalf("应去掉候选外与重复的名称：%+v", res)
	}
	hits := llm.hits.Load()
	again, err := svc.Rerank(ctx, "搜网页", cands, 5)
	if err != nil || !again.Cached || llm.hits.Load() != hits {
		t.Fatalf("相同查询应命中缓存：%+v %v", again, err)
	}
	limited, _ := svc.Rerank(ctx, "搜网页 2", cands, 1)
	if len(limited.Names) != 1 {
		t.Fatalf("应遵守挑选上限：%+v", limited)
	}
}

func TestProxyFailsClosed(t *testing.T) {
	llm := &fakeLLM{}
	resolver := func(id string) (*url.URL, error) {
		if id == "good" {
			return url.Parse("http://127.0.0.1:1")
		}
		return nil, errors.New("出站代理已停用")
	}
	svc, srv := newTestService(t, llm, resolver)
	if _, err := svc.SaveConfig(context.Background(), ConfigInput{BaseURL: srv.URL, Model: "m", ProxyID: "bad"}); err == nil {
		t.Fatal("保存时应校验代理可用")
	}
	p := risk.Provider{BaseURL: srv.URL, Model: "m", APIStyle: risk.APIStyleChatCompletions, TimeoutS: 5, ProxyID: "bad"}
	if _, _, err := svc.client.complete(context.Background(), p, "s", "u", time.Second); err == nil || !strings.Contains(err.Error(), "出站代理不可用") {
		t.Fatalf("代理不可用时应直接失败：%v", err)
	}
	if llm.hits.Load() != 0 {
		t.Fatal("代理不可用时不得直连接口")
	}
}

func TestServiceNameAndJSONExtraction(t *testing.T) {
	for in, want := range map[string]string{"tavily01": "tavily", "context7-03": "context7", "Brave Search 搜索": "Brave Search 搜索", "007": "007"} {
		if got := ServiceName(in); got != want {
			t.Fatalf("ServiceName(%q)=%q, want %q", in, got, want)
		}
	}
	var out struct{ A int }
	if err := decodeJSONObject("说明\n```json\n{\"A\":3}\n```", &out); err != nil || out.A != 3 {
		t.Fatalf("应能从代码块中提取 JSON：%v %+v", err, out)
	}
	if err := decodeJSONObject("没有 JSON", &out); err == nil {
		t.Fatal("没有 JSON 时应报错")
	}
	if got := schemaParams(json.RawMessage(`{"properties":{"b":{},"a":{}},"required":["b"]}`)); strings.Join(got, ",") != "a,b*" {
		t.Fatalf("参数提取不符：%v", got)
	}
}
