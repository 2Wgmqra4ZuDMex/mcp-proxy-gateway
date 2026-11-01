package transport

import (
	"errors"
	"net/url"
	"testing"

	"github.com/myGithub/mcp-proxy-gateway/internal/domain"
)

func withResolver(t *testing.T, r ProxyResolver) {
	t.Helper()
	SetProxyResolver(r)
	t.Cleanup(func() { SetProxyResolver(nil) })
}

func TestParseConnParamsResolvesProxyID(t *testing.T) {
	withResolver(t, func(id string) (*url.URL, error) {
		if id == "p1" {
			return url.Parse("socks5://10.0.0.1:1080")
		}
		return nil, errors.New("出站代理不存在")
	})
	cfg := domain.UpstreamConfig{Transport: domain.TransportStreamableHTTP, ConnParams: map[string]any{
		ParamURL: "https://mcp.example.com/mcp", ParamProxyID: "p1",
	}}
	p, err := parseConnParams(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if p.proxy == nil || p.proxy.Host != "10.0.0.1:1080" {
		t.Fatalf("应解析为出站代理地址，got %v", p.proxy)
	}
}

func TestParseConnParamsFailsClosedWhenProxyMissing(t *testing.T) {
	withResolver(t, func(string) (*url.URL, error) { return nil, errors.New("出站代理已停用") })
	cfg := domain.UpstreamConfig{Transport: domain.TransportStreamableHTTP, ConnParams: map[string]any{
		ParamURL: "https://mcp.example.com/mcp", ParamProxyID: "gone",
	}}
	if _, err := parseConnParams(cfg); err == nil {
		t.Fatal("代理不可用时应拒绝建立会话，而不是退回直连")
	}
}

func TestParseConnParamsFailsClosedWithoutResolver(t *testing.T) {
	withResolver(t, nil)
	_, err := resolveProxy(map[string]any{ParamProxyID: "p1"})
	if err == nil {
		t.Fatal("未注入解析器时引用代理应失败")
	}
}

func TestValidateRejectsProxyIDAndURLTogether(t *testing.T) {
	withResolver(t, func(string) (*url.URL, error) { return url.Parse("socks5://10.0.0.1:1080") })
	err := ValidateConnParams(domain.UpstreamConfig{Transport: domain.TransportStreamableHTTP, ConnParams: map[string]any{
		ParamURL: "https://mcp.example.com/mcp", ParamProxyID: "p1", ParamProxyURL: "socks5://10.0.0.2:1080",
	}})
	if err == nil {
		t.Fatal("同时设置代理地址与出站代理应校验失败")
	}
}
