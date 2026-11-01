package outbound

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/myGithub/mcp-proxy-gateway/internal/domain"
	"github.com/myGithub/mcp-proxy-gateway/internal/store"
	"github.com/myGithub/mcp-proxy-gateway/internal/transport"
)

type memRepo struct {
	items []store.OutboundProxy
	seq   int
}

func (r *memRepo) List(context.Context) ([]store.OutboundProxy, error) {
	return slices.Clone(r.items), nil
}

func (r *memRepo) Get(_ context.Context, id string) (store.OutboundProxy, error) {
	for _, p := range r.items {
		if p.ID == id {
			return p, nil
		}
	}
	return store.OutboundProxy{}, domain.NewError(domain.CodeNotFound, "出站代理不存在")
}

func (r *memRepo) Create(_ context.Context, p store.OutboundProxy) (store.OutboundProxy, error) {
	if p.ID == "" {
		r.seq++
		p.ID = "p" + string(rune('0'+r.seq))
	}
	r.items = append(r.items, p)
	return p, nil
}

func (r *memRepo) Update(_ context.Context, p store.OutboundProxy) (store.OutboundProxy, error) {
	for i := range r.items {
		if r.items[i].ID == p.ID {
			r.items[i] = p
			return p, nil
		}
	}
	return store.OutboundProxy{}, domain.NewError(domain.CodeNotFound, "出站代理不存在")
}

func (r *memRepo) Delete(_ context.Context, id string) error {
	r.items = slices.DeleteFunc(r.items, func(p store.OutboundProxy) bool { return p.ID == id })
	return nil
}

type memUpstreams struct {
	ups        []domain.Upstream
	reconnects []string
}

func (m *memUpstreams) List(context.Context) ([]domain.Upstream, error) {
	return slices.Clone(m.ups), nil
}

func (m *memUpstreams) Update(_ context.Context, id string, cfg domain.UpstreamConfig) (domain.Upstream, error) {
	for i := range m.ups {
		if m.ups[i].ID == id {
			m.ups[i].Config = cfg
			return m.ups[i], nil
		}
	}
	return domain.Upstream{}, domain.NewError(domain.CodeNotFound, "上游不存在")
}

func (m *memUpstreams) Reconnect(_ context.Context, id string) error {
	m.reconnects = append(m.reconnects, id)
	return nil
}

func upstream(id, name string, params map[string]any) domain.Upstream {
	if params == nil {
		params = map[string]any{}
	}
	params["url"] = "https://mcp.example.com/mcp"
	return domain.Upstream{ID: id, Config: domain.UpstreamConfig{Name: name, Transport: domain.TransportStreamableHTTP, ConnParams: params}}
}

func newTestService(t *testing.T, proxies []store.OutboundProxy, ups []domain.Upstream) (*Service, *memRepo, *memUpstreams) {
	t.Helper()
	repo := &memRepo{items: proxies}
	mgr := &memUpstreams{ups: ups}
	s := New(repo, mgr)
	if err := s.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	return s, repo, mgr
}

func TestResolveFailsClosedForMissingOrDisabledProxy(t *testing.T) {
	s, _, _ := newTestService(t, []store.OutboundProxy{
		{ID: "on", Name: "A", URL: "socks5://10.0.0.1:1080", Enabled: true},
		{ID: "off", Name: "B", URL: "socks5://10.0.0.2:1080", Enabled: false},
	}, nil)
	if u, err := s.Resolve("on"); err != nil || u.Host != "10.0.0.1:1080" {
		t.Fatalf("启用的代理应可解析，got %v %v", u, err)
	}
	if _, err := s.Resolve("off"); err == nil {
		t.Fatal("停用的代理应解析失败，避免退回直连")
	}
	if _, err := s.Resolve("missing"); err == nil {
		t.Fatal("不存在的代理应解析失败")
	}
}

func TestDeleteRejectsProxyInUse(t *testing.T) {
	s, repo, _ := newTestService(t,
		[]store.OutboundProxy{{ID: "p1", Name: "A", URL: "socks5://10.0.0.1:1080", Enabled: true}},
		[]domain.Upstream{upstream("u1", "tavily01", map[string]any{transport.ParamProxyID: "p1"})},
	)
	err := s.Delete(context.Background(), "p1")
	if err == nil || !strings.Contains(err.Error(), "tavily01") {
		t.Fatalf("在用的代理应拒绝删除并列出上游，got %v", err)
	}
	if len(repo.items) != 1 {
		t.Fatal("拒绝删除时不应移除代理")
	}
}

func TestDistributeRoundRobin(t *testing.T) {
	s, _, mgr := newTestService(t,
		[]store.OutboundProxy{
			{ID: "pa", Name: "A", URL: "socks5://10.0.0.1:1080", Enabled: true},
			{ID: "pb", Name: "B", URL: "socks5://10.0.0.2:1080", Enabled: true},
		},
		[]domain.Upstream{
			upstream("u1", "t1", nil), upstream("u2", "t2", nil), upstream("u3", "t3", map[string]any{transport.ParamProxyURL: "http://old:8080"}),
		},
	)
	res, err := s.Distribute(context.Background(), []string{"u1", "u2", "u3"}, []string{"pa", "pb"})
	if err != nil || res.Updated != 3 {
		t.Fatalf("应更新 3 个上游，got %+v %v", res, err)
	}
	want := map[string]string{"u1": "pa", "u2": "pb", "u3": "pa"}
	for _, up := range mgr.ups {
		if got := transport.ProxyIDFromParams(up.Config.ConnParams); got != want[up.ID] {
			t.Errorf("%s 期望代理 %s，实际 %s", up.ID, want[up.ID], got)
		}
		if _, ok := up.Config.ConnParams[transport.ParamProxyURL]; ok {
			t.Errorf("%s 分配后不应保留内联代理地址", up.ID)
		}
		if up.Config.ConnParams["url"] == nil {
			t.Errorf("%s 分配不应丢失其他连接参数", up.ID)
		}
	}
}

func TestAssignDirectRemovesProxy(t *testing.T) {
	s, _, mgr := newTestService(t,
		[]store.OutboundProxy{{ID: "pa", Name: "A", URL: "socks5://10.0.0.1:1080", Enabled: true}},
		[]domain.Upstream{upstream("u1", "t1", map[string]any{transport.ParamProxyID: "pa"})},
	)
	if _, err := s.Assign(context.Background(), []string{"u1"}, ""); err != nil {
		t.Fatal(err)
	}
	if transport.ProxyIDFromParams(mgr.ups[0].Config.ConnParams) != "" {
		t.Fatal("设为直连后应移除代理引用")
	}
}

func TestAssignRejectsDisabledProxy(t *testing.T) {
	s, _, _ := newTestService(t,
		[]store.OutboundProxy{{ID: "off", Name: "B", URL: "socks5://10.0.0.2:1080", Enabled: false}},
		[]domain.Upstream{upstream("u1", "t1", nil)},
	)
	if _, err := s.Assign(context.Background(), []string{"u1"}, "off"); err == nil {
		t.Fatal("不能分配到已停用的代理")
	}
}

func TestUpdateURLReconnectsUsers(t *testing.T) {
	s, _, mgr := newTestService(t,
		[]store.OutboundProxy{{ID: "pa", Name: "A", URL: "socks5://10.0.0.1:1080", Enabled: true}},
		[]domain.Upstream{
			upstream("u1", "t1", map[string]any{transport.ParamProxyID: "pa"}),
			upstream("u2", "t2", nil),
		},
	)
	if _, err := s.Update(context.Background(), "pa", Input{Name: "A", URL: "socks5://10.0.0.9:1080"}); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(mgr.reconnects, []string{"u1"}) {
		t.Fatalf("地址变化应只重连引用该代理的上游，got %v", mgr.reconnects)
	}
	if u, _ := s.Resolve("pa"); u.Host != "10.0.0.9:1080" {
		t.Fatalf("缓存应更新为新地址，got %v", u)
	}
	mgr.reconnects = nil
	if _, err := s.Update(context.Background(), "pa", Input{Name: "A2", URL: "socks5://10.0.0.9:1080"}); err != nil {
		t.Fatal(err)
	}
	if len(mgr.reconnects) != 0 {
		t.Fatal("仅改名不应触发重连")
	}
}

func TestCreateValidatesInput(t *testing.T) {
	s, _, _ := newTestService(t, nil, nil)
	if _, err := s.Create(context.Background(), Input{Name: "", URL: "ftp://x:1"}); err == nil {
		t.Fatal("空名称与非法协议应校验失败")
	}
	p, err := s.Create(context.Background(), Input{Name: "家里", URL: "socks5://u:secret@10.0.0.1:1080"})
	if err != nil {
		t.Fatal(err)
	}
	if !p.Enabled || strings.Contains(p.DisplayURL, "secret") {
		t.Fatalf("新代理默认启用且展示地址应隐去密码，got %+v", p)
	}
}

func TestTestReportsExitIPThroughProxy(t *testing.T) {
	var sawAbsolute bool
	proxySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawAbsolute = r.URL.Host == "ip.check.invalid"
		_, _ = io.WriteString(w, `{"ip":"203.0.113.7"}`)
	}))
	defer proxySrv.Close()

	s, _, _ := newTestService(t, []store.OutboundProxy{{ID: "pa", Name: "A", URL: proxySrv.URL, Enabled: true}}, nil)
	s.ipCheckURL = "http://ip.check.invalid/?format=json"
	res, err := s.Test(context.Background(), "pa")
	if err != nil {
		t.Fatal(err)
	}
	if !res.OK || res.ExitIP != "203.0.113.7" || !sawAbsolute {
		t.Fatalf("应经代理拿到出口 IP，got %+v sawProxy=%v", res, sawAbsolute)
	}
}

func TestTestRedactsPasswordInError(t *testing.T) {
	s, _, _ := newTestService(t, []store.OutboundProxy{{ID: "pa", Name: "A", URL: "http://u:topsecret@127.0.0.1:1", Enabled: true}}, nil)
	s.ipCheckURL = "http://ip.check.invalid/"
	res, err := s.Test(context.Background(), "pa")
	if err != nil {
		t.Fatal(err)
	}
	if res.OK || strings.Contains(res.Message, "topsecret") {
		t.Fatalf("连接失败且错误信息不得包含密码，got %+v", res)
	}
}

func TestParseProxyURLRequiresPort(t *testing.T) {
	if _, err := transport.ParseProxyURL("socks5://10.0.0.1"); err == nil {
		t.Fatal("缺少端口应失败")
	}
	if u, err := transport.ParseProxyURL(" socks5h://10.0.0.1:1080 "); err != nil || u == nil {
		t.Fatalf("合法地址应通过，got %v %v", u, err)
	}
}
