package aggregation

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/myGithub/mcp-proxy-gateway/internal/domain"
	"github.com/myGithub/mcp-proxy-gateway/internal/store"
)

func textResult(isError bool, text string) domain.ToolResult {
	raw, _ := json.Marshal([]map[string]string{{"type": "text", "text": text}})
	return domain.ToolResult{IsError: isError, Content: raw}
}

const tavily429 = `{"error":"Search failed","status":429,"detail":{"error":"Your request has been blocked due to excessive requests. Please reduce the rate of requests. Verify you are using production API keys."},"documentation":"https://docs.tavily.com/documentation/api-reference/endpoint/search"}`

func TestDetectAccountRejection(t *testing.T) {
	long := strings.Repeat("Search result about API rate limit best practices. ", 120)
	cases := []struct {
		name   string
		result domain.ToolResult
		err    error
		want   string
	}{
		{"tavily 把 429 当普通结果返回", textResult(false, tavily429), nil, RejectionRateLimited},
		{"isError 的额度不足", textResult(true, "Error: Insufficient credits to perform this request."), nil, RejectionQuotaExhausted},
		{"tavily 432 套餐上限", textResult(false, `{"error":"plan exceeded","status":432}`), nil, RejectionQuotaExhausted},
		{"isError 的 Key 无效", textResult(true, "Invalid API key provided"), nil, RejectionUnauthorized},
		{"isError 的 429 文本", textResult(true, "HTTP 429 Too Many Requests"), nil, RejectionRateLimited},
		{"传输层 401", nil2(), errors.New("sending request: HTTP 401 Unauthorized"), RejectionUnauthorized},
		{"传输层 429", nil2(), domain.NewError(domain.CodeUpstreamUnavailable, "calling tool: status 429"), RejectionRateLimited},
		{"抓取目标网站 403 不算", textResult(true, "Failed to scrape URL. Status code: 403. Error: Forbidden"), nil, ""},
		{"抓取目标网站 401 不算", textResult(true, "Failed to scrape URL. Status code: 401. Error: Unauthorized"), nil, ""},
		{"参数错误不算", textResult(true, "Error: invalid parameter 'query'"), nil, ""},
		{"正常结果提到 rate limit 不算", textResult(false, "Rate limiting protects APIs from too many requests."), nil, ""},
		{"超长结果不扫描", textResult(true, long), nil, ""},
		{"正常 JSON 结果不算", textResult(false, `{"results":[{"title":"quota"}]}`), nil, ""},
		{"网关自身额度限制不算", nil2(), domain.NewError(domain.CodeRateLimited, "rate limit exceeded"), ""},
		{"普通连接失败不算", nil2(), errors.New("dial tcp: connection refused"), ""},
	}
	for _, tc := range cases {
		got, ok := DetectAccountRejection(tc.result, tc.err)
		if tc.want == "" {
			if ok {
				t.Errorf("%s：不应判为账号被拒，got %+v", tc.name, got)
			}
			continue
		}
		if !ok || got.Kind != tc.want || got.Cooldown <= 0 {
			t.Errorf("%s：want %s, got %+v ok=%v", tc.name, tc.want, got, ok)
		}
	}
}

func nil2() domain.ToolResult { return domain.ToolResult{} }

type scriptedInvoker struct {
	mu    sync.Mutex
	byUp  map[string]domain.ToolResult
	errUp map[string]error
	calls []string
}

func (i *scriptedInvoker) CallUpstream(_ context.Context, upstreamID, originalName string, _ json.RawMessage) (domain.ToolResult, error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.calls = append(i.calls, upstreamID)
	if err := i.errUp[upstreamID]; err != nil {
		return domain.ToolResult{}, err
	}
	if r, ok := i.byUp[upstreamID]; ok {
		return r, nil
	}
	return textResult(false, "ok from "+upstreamID), nil
}

func (i *scriptedInvoker) called() []string {
	i.mu.Lock()
	defer i.mu.Unlock()
	return append([]string(nil), i.calls...)
}

type multiRecorder struct {
	mu   sync.Mutex
	recs []store.CallStatRecord
}

func (r *multiRecorder) RecordAsync(_ context.Context, rec store.CallStatRecord) {
	r.mu.Lock()
	r.recs = append(r.recs, rec)
	r.mu.Unlock()
}

func failoverService(invoker *scriptedInvoker, ups ...string) *Service {
	tools := map[string][]domain.ToolDef{}
	upstreams := make([]domain.Upstream, 0, len(ups))
	for i, id := range ups {
		tools[id] = []domain.ToolDef{{OriginalName: "search", Name: "search", InputSchema: []byte("{}")}}
		upstreams = append(upstreams, invEnabledUpstream(id, i))
	}
	cache := &invFakeCache{tools: tools}
	svc := invNewService(cache, &invFakeUpstreams{upstreams: upstreams},
		&invFakeAliases{byUpstream: map[string][]domain.AliasRule{}},
		&invFakeMCPFilters{byUpstream: map[string][]domain.FilterRule{}},
		&invFakeAPIKeyFilters{byAPIKey: map[string][]domain.FilterRule{}})
	svc.SetInvoker(invoker)
	svc.SetRoutingStrategy(domain.ToolRoutingRoundRobin)
	return svc
}

func TestInvokeFailsOverWhenAccountRejects(t *testing.T) {
	invoker := &scriptedInvoker{byUp: map[string]domain.ToolResult{"tav-1": textResult(false, tavily429)}}
	svc := failoverService(invoker, "tav-1", "tav-2", "tav-3")
	recorder := &multiRecorder{}
	svc.recorder = recorder
	now := time.Now()
	svc.now = func() time.Time { return now }

	res, err := svc.InvokeTool(context.Background(), "", "search", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := resultText(res.Content); got != "ok from tav-2" && got != "ok from tav-3" {
		t.Fatalf("被限流后应换到下一个账号并返回其结果，got %q（调用顺序 %v）", got, invoker.called())
	}
	if len(recorder.recs) != 2 || recorder.recs[0].Success || recorder.recs[0].UpstreamID != "tav-1" ||
		!strings.Contains(recorder.recs[0].ErrorMessage, "限流") || !recorder.recs[1].Success {
		t.Fatalf("每次尝试都应记录，被拒的记为失败：%+v", recorder.recs)
	}

	// 冷却期内轮询应跳过 tav-1。
	for range 4 {
		if _, err := svc.InvokeTool(context.Background(), "", "search", json.RawMessage(`{}`)); err != nil {
			t.Fatal(err)
		}
	}
	for _, up := range invoker.called()[2:] {
		if up == "tav-1" {
			t.Fatalf("冷却期内不应再调用被限流的账号：%v", invoker.called())
		}
	}
	if views := svc.AccountLimits(); len(views) != 1 || views[0].UpstreamID != "tav-1" || views[0].Kind != RejectionRateLimited {
		t.Fatalf("应能查询到冷却中的账号：%+v", views)
	}

	// 冷却结束后恢复参与轮询。
	now = now.Add(rateLimitCooldown + time.Second)
	delete(invoker.byUp, "tav-1")
	seen := false
	for range 3 {
		_, _ = svc.InvokeTool(context.Background(), "", "search", json.RawMessage(`{}`))
	}
	for _, up := range invoker.called()[6:] {
		seen = seen || up == "tav-1"
	}
	if !seen {
		t.Fatalf("冷却结束后应恢复调用：%v", invoker.called())
	}
}

func TestInvokeReturnsLastRejectionWhenAllAccountsReject(t *testing.T) {
	quota := textResult(true, "Error: Insufficient credits")
	invoker := &scriptedInvoker{byUp: map[string]domain.ToolResult{"a": quota, "b": quota}}
	svc := failoverService(invoker, "a", "b")
	res, err := svc.InvokeTool(context.Background(), "", "search", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError || len(invoker.called()) != 2 {
		t.Fatalf("所有账号都被拒时应各尝试一次并原样返回最后结果：%+v %v", res, invoker.called())
	}
	// 全部冷却时仍照常调用，不把请求堵死。
	if _, err := svc.InvokeTool(context.Background(), "", "search", json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	if len(invoker.called()) != 4 {
		t.Fatalf("全部冷却时应仍尝试调用：%v", invoker.called())
	}
}

func TestInvokeDoesNotFailOverOrdinaryErrors(t *testing.T) {
	invoker := &scriptedInvoker{
		byUp:  map[string]domain.ToolResult{"a": textResult(true, "Error: invalid parameter 'query'")},
		errUp: map[string]error{},
	}
	svc := failoverService(invoker, "a", "b")
	res, _ := svc.InvokeTool(context.Background(), "", "search", json.RawMessage(`{}`))
	if !res.IsError || len(invoker.called()) != 1 {
		t.Fatalf("普通错误结果应原样返回、不换账号重试：%v", invoker.called())
	}
	invoker2 := &scriptedInvoker{errUp: map[string]error{"a": errors.New("dial tcp: connection refused")}}
	svc2 := failoverService(invoker2, "a", "b")
	if _, err := svc2.InvokeTool(context.Background(), "", "search", json.RawMessage(`{}`)); err == nil || len(invoker2.called()) != 1 {
		t.Fatalf("连接失败（请求可能已发出）不应自动重试：%v %v", err, invoker2.called())
	}
}

type memEventRecorder struct {
	mu     sync.Mutex
	events []domain.AccountEvent
}

func (r *memEventRecorder) Insert(_ context.Context, ev domain.AccountEvent) error {
	r.mu.Lock()
	r.events = append(r.events, ev)
	r.mu.Unlock()
	return nil
}

func (r *memEventRecorder) snapshot() []domain.AccountEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]domain.AccountEvent(nil), r.events...)
}

func TestInvokeRecordsAccountEventOnRejection(t *testing.T) {
	invoker := &scriptedInvoker{byUp: map[string]domain.ToolResult{"tav-1": textResult(false, tavily429)}}
	svc := failoverService(invoker, "tav-1", "tav-2")
	rec := &memEventRecorder{}
	svc.SetAccountEventRecorder(rec)
	for range 2 {
		if _, err := svc.InvokeTool(context.Background(), "", "search", json.RawMessage(`{}`)); err != nil {
			t.Fatal(err)
		}
	}
	deadline := time.Now().Add(2 * time.Second)
	for len(rec.snapshot()) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	evs := rec.snapshot()
	if len(evs) != 1 || evs[0].UpstreamID != "tav-1" || evs[0].Kind != RejectionRateLimited || evs[0].HTTPStatus != 429 ||
		evs[0].Tool != "search" || evs[0].Source != "call" {
		t.Fatalf("账号被拒时应写入一条记录（之后冷却跳过不再记录）：%+v", evs)
	}
	svc.ClearAccountLimit("tav-1")
	if len(svc.AccountLimits()) != 0 {
		t.Fatal("清除后应结束冷却")
	}
}

func TestAllCoolingPrefersEarliestRecovery(t *testing.T) {
	invoker := &scriptedInvoker{byUp: map[string]domain.ToolResult{
		"bad-key": textResult(true, "Invalid API key provided"),
		"limited": textResult(false, tavily429),
	}}
	svc := failoverService(invoker, "bad-key", "limited")
	now := time.Now()
	svc.now = func() time.Time { return now }
	// 第一次调用：两个账号都被拒，分别进入 10 分钟与 1 分钟冷却。
	_, _ = svc.InvokeTool(context.Background(), "", "search", json.RawMessage(`{}`))
	if len(svc.AccountLimits()) != 2 {
		t.Fatalf("两个账号都应在冷却：%+v", svc.AccountLimits())
	}
	// 限流账号恢复正常后再调用：应先试最早恢复的 limited，而不是 Key 无效的 bad-key。
	delete(invoker.byUp, "limited")
	before := len(invoker.called())
	res, err := svc.InvokeTool(context.Background(), "", "search", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	calls := invoker.called()[before:]
	if len(calls) != 1 || calls[0] != "limited" || resultText(res.Content) != "ok from limited" {
		t.Fatalf("全部冷却时应先试最早恢复的账号：%v", calls)
	}
}
