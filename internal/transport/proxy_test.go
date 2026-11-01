package transport

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"

	"github.com/myGithub/mcp-proxy-gateway/internal/domain"
)

func TestValidateConnParamsProxyURL(t *testing.T) {
	cases := []struct {
		proxy any
		ok    bool
	}{
		{nil, true},
		{"", true},
		{"socks5://127.0.0.1:1080", true},
		{"socks5h://user:pass@proxy.local:1080", true},
		{"http://proxy.local:8080", true},
		{"https://proxy.local:8443", true},
		{"ftp://proxy.local:21", false},
		{"socks5://proxy.local", false},
		{"socks5://:1080", false},
		{123, false},
	}
	for _, tc := range cases {
		params := map[string]any{ParamURL: "https://mcp.example.com/mcp"}
		if tc.proxy != nil {
			params[ParamProxyURL] = tc.proxy
		}
		err := ValidateConnParams(domain.UpstreamConfig{Transport: domain.TransportStreamableHTTP, ConnParams: params})
		if (err == nil) != tc.ok {
			t.Errorf("proxy=%v 期望 ok=%v，实际 err=%v", tc.proxy, tc.ok, err)
		}
	}
}

func TestAuthHTTPClientRoutesThroughProxy(t *testing.T) {
	var hits atomic.Int32
	var gotAuth atomic.Value
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 正向代理收到的是绝对 URI。
		if r.URL.Host == "upstream.invalid" {
			hits.Add(1)
			gotAuth.Store(r.Header.Get("Authorization"))
		}
		_, _ = io.WriteString(w, "via-proxy")
	}))
	defer proxy.Close()
	proxyURL, _ := url.Parse(proxy.URL)

	client := newAuthHTTPClient("secret", nil, proxyURL)
	resp, err := client.Get("http://upstream.invalid/mcp")
	if err != nil {
		t.Fatalf("经代理请求失败：%v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if string(body) != "via-proxy" || hits.Load() != 1 {
		t.Fatalf("请求未经过代理：body=%q hits=%d", body, hits.Load())
	}
	if gotAuth.Load() != "Bearer secret" {
		t.Fatalf("经代理时仍应携带鉴权头，实际 %v", gotAuth.Load())
	}
}

func TestAuthHTTPClientNilWithoutCredentialHeadersOrProxy(t *testing.T) {
	if newAuthHTTPClient("", nil, nil) != nil {
		t.Fatal("无凭证、请求头与代理时应返回 nil 以使用默认客户端")
	}
	proxyURL, _ := url.Parse("socks5://127.0.0.1:1080")
	if newAuthHTTPClient("", nil, proxyURL) == nil {
		t.Fatal("仅配置代理时也应返回自定义客户端")
	}
}

func TestBaseTransportForReusesPerProxy(t *testing.T) {
	a, _ := url.Parse("socks5://127.0.0.1:1080")
	b, _ := url.Parse("socks5://127.0.0.1:1081")
	if baseTransportFor(nil) != http.DefaultTransport {
		t.Fatal("未配置代理应使用默认 Transport")
	}
	if baseTransportFor(a) != baseTransportFor(a) {
		t.Fatal("同一代理应复用 Transport")
	}
	if baseTransportFor(a) == baseTransportFor(b) {
		t.Fatal("不同代理不应共用 Transport")
	}
}

func TestProxyEnvRespectsUserOverrides(t *testing.T) {
	proxyURL, _ := url.Parse("socks5://127.0.0.1:1080")
	env := proxyEnv(proxyURL, map[string]string{"HTTPS_PROXY": "http://custom:3128", "FOO": "bar"})
	if env["HTTPS_PROXY"] != "http://custom:3128" {
		t.Fatalf("用户显式设置的变量应优先，实际 %q", env["HTTPS_PROXY"])
	}
	if env["HTTP_PROXY"] != "socks5://127.0.0.1:1080" || env["ALL_PROXY"] != "socks5://127.0.0.1:1080" {
		t.Fatalf("应注入代理环境变量，实际 %v", env)
	}
	if env["FOO"] != "bar" {
		t.Fatal("应保留用户其余环境变量")
	}
	if got := proxyEnv(nil, map[string]string{"A": "1"}); len(got) != 1 {
		t.Fatalf("未配置代理时不应注入，实际 %v", got)
	}
}

func TestRedactProxyURL(t *testing.T) {
	if got := RedactProxyURL("socks5://user:secret@proxy.local:1080"); got != "socks5://user:xxxxx@proxy.local:1080" {
		t.Fatalf("应隐去密码，实际 %q", got)
	}
	if got := RedactProxyURL("socks5://proxy.local:1080"); got != "socks5://proxy.local:1080" {
		t.Fatalf("无密码时应原样返回，实际 %q", got)
	}
}
