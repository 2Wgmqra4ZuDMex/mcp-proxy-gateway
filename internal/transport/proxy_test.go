package transport

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

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
	proxyURL, _ := url.Parse("http://127.0.0.1:3128")
	env, err := proxyEnv(proxyURL, map[string]string{"HTTPS_PROXY": "http://custom:3128", "FOO": "bar"})
	if err != nil {
		t.Fatal(err)
	}
	if env["HTTPS_PROXY"] != "http://custom:3128" {
		t.Fatalf("用户显式设置的变量应优先，实际 %q", env["HTTPS_PROXY"])
	}
	if env["HTTP_PROXY"] != "http://127.0.0.1:3128" || env["ALL_PROXY"] != "http://127.0.0.1:3128" {
		t.Fatalf("HTTP 代理应原样注入，实际 %v", env)
	}
	if env["NODE_USE_ENV_PROXY"] != "1" {
		t.Fatalf("应开启 Node 读取代理环境变量，实际 %v", env)
	}
	if env["FOO"] != "bar" {
		t.Fatal("应保留用户其余环境变量")
	}
	if got, _ := proxyEnv(nil, map[string]string{"A": "1"}); len(got) != 1 {
		t.Fatalf("未配置代理时不应注入，实际 %v", got)
	}
}

// fakeSOCKS5 是只支持无认证 CONNECT 的最小 SOCKS5 服务端：记录客户端请求的目标主机，
// 并把连接转发到 target。
func fakeSOCKS5(t *testing.T, target string) (addr string, requested chan string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	requested = make(chan string, 8)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				buf := make([]byte, 262)
				if _, err := io.ReadFull(c, buf[:2]); err != nil {
					return
				}
				if _, err := io.ReadFull(c, buf[:buf[1]]); err != nil {
					return
				}
				c.Write([]byte{5, 0})
				if _, err := io.ReadFull(c, buf[:4]); err != nil {
					return
				}
				var host string
				switch buf[3] {
				case 3:
					io.ReadFull(c, buf[:1])
					n := int(buf[0])
					io.ReadFull(c, buf[:n])
					host = string(buf[:n])
				case 1:
					io.ReadFull(c, buf[:4])
					host = net.IP(buf[:4]).String()
				default:
					return
				}
				io.ReadFull(c, buf[:2])
				requested <- host
				up, err := net.Dial("tcp", target)
				if err != nil {
					c.Write([]byte{5, 1, 0, 1, 0, 0, 0, 0, 0, 0})
					return
				}
				defer up.Close()
				c.Write([]byte{5, 0, 0, 1, 0, 0, 0, 0, 0, 0})
				go io.Copy(up, c)
				io.Copy(c, up)
			}(c)
		}
	}()
	return ln.Addr().String(), requested
}

// TestProxyEnvBridgesSOCKS5 验证 SOCKS5 代理被换成本地 HTTP 中转：子进程经中转发起的
// CONNECT 与普通 HTTP 请求都经 SOCKS5 出站，且把域名交给代理解析。
func TestProxyEnvBridgesSOCKS5(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "backend-ok")
	}))
	t.Cleanup(backend.Close)
	socksAddr, requested := fakeSOCKS5(t, backend.Listener.Addr().String())
	proxyURL, _ := url.Parse("socks5://" + socksAddr)

	env, err := proxyEnv(proxyURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	bridge := env["HTTPS_PROXY"]
	if !strings.HasPrefix(bridge, "http://127.0.0.1:") || env["HTTP_PROXY"] != bridge || env["ALL_PROXY"] != bridge {
		t.Fatalf("SOCKS5 代理应换成本地 HTTP 中转，实际 %v", env)
	}
	if again, _ := proxyEnv(proxyURL, nil); again["HTTPS_PROXY"] != bridge {
		t.Fatal("同一代理应复用同一个中转")
	}

	bridgeURL, _ := url.Parse(bridge)
	_, port, _ := net.SplitHostPort(backend.Listener.Addr().String())
	client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(bridgeURL)}, Timeout: 5 * time.Second}

	// 普通 HTTP：绝对地址请求经中转转发。
	resp, err := client.Get("http://api.example.test:" + port + "/")
	if err != nil {
		t.Fatalf("经中转的 HTTP 请求失败：%v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(body) != "backend-ok" {
		t.Fatalf("HTTP 响应不符：%q", body)
	}
	if host := <-requested; host != "api.example.test" {
		t.Fatalf("应把域名交给 SOCKS5 代理解析，实际请求 %q", host)
	}

	// CONNECT 隧道：手工发起，验证隧道内数据双向转发。
	conn, err := net.Dial("tcp", bridgeURL.Host)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	fmt.Fprintf(conn, "CONNECT tunnel.example.test:%s HTTP/1.1\r\nHost: tunnel.example.test:%s\r\n\r\n", port, port)
	br := bufio.NewReader(conn)
	status, _ := br.ReadString('\n')
	if !strings.Contains(status, " 200 ") {
		t.Fatalf("CONNECT 应成功，实际 %q", status)
	}
	br.ReadString('\n')
	if host := <-requested; host != "tunnel.example.test" {
		t.Fatalf("CONNECT 应把域名交给 SOCKS5 代理解析，实际请求 %q", host)
	}
	fmt.Fprintf(conn, "GET / HTTP/1.1\r\nHost: tunnel.example.test\r\nConnection: close\r\n\r\n")
	rest, _ := io.ReadAll(br)
	if !strings.Contains(string(rest), "backend-ok") {
		t.Fatalf("隧道内响应不符：%q", rest)
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
