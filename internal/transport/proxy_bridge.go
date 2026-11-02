package transport

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	xproxy "golang.org/x/net/proxy"
)

// stdio 子进程通过环境变量使用代理，但很多运行时不支持 SOCKS5：Node 内置 fetch 只认
// HTTP 代理，npm 也不认 socks5://；curl 等按 socks5:// 在本地解析域名，遇到被污染的
// DNS 会连错地址。因此对 SOCKS5 代理，网关在 127.0.0.1 上为每个代理开一个 HTTP 代理
// 中转，子进程统一使用 http://127.0.0.1:端口，中转再经 SOCKS5 出站并把域名交给代理解析。

var proxyBridges sync.Map // map[string]*proxyBridge，键为代理地址

type proxyBridge struct {
	once sync.Once
	addr string
	err  error
}

// childProxyURL 返回 stdio 子进程应使用的代理地址：SOCKS5 代理换成本地 HTTP 中转，
// 其余协议原样返回。中转启动失败时返回错误，使连接失败而不是退回直连。
func childProxyURL(proxy *url.URL) (string, error) {
	switch strings.ToLower(proxy.Scheme) {
	case "socks5", "socks5h":
	default:
		return proxy.String(), nil
	}
	key := proxy.String()
	v, _ := proxyBridges.LoadOrStore(key, &proxyBridge{})
	b := v.(*proxyBridge)
	b.once.Do(func() { b.addr, b.err = startProxyBridge(proxy) })
	if b.err != nil {
		proxyBridges.CompareAndDelete(key, b)
		return "", fmt.Errorf("启动本地代理中转失败：%w", b.err)
	}
	return "http://" + b.addr, nil
}

func startProxyBridge(proxy *url.URL) (string, error) {
	dialer, err := xproxy.FromURL(proxy, &net.Dialer{Timeout: 30 * time.Second})
	if err != nil {
		return "", err
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	forward := http.DefaultTransport.(*http.Transport).Clone()
	forward.Proxy = http.ProxyURL(proxy)
	srv := &http.Server{
		Handler:           &bridgeHandler{dialer: dialer, forward: forward},
		ReadHeaderTimeout: 30 * time.Second,
	}
	go func() { _ = srv.Serve(ln) }()
	return ln.Addr().String(), nil
}

type bridgeHandler struct {
	dialer  xproxy.Dialer
	forward http.RoundTripper
}

var hopHeaders = []string{
	"Connection", "Proxy-Connection", "Keep-Alive", "Proxy-Authenticate",
	"Proxy-Authorization", "Te", "Trailer", "Transfer-Encoding", "Upgrade",
}

func (h *bridgeHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodConnect {
		h.tunnel(w, r)
		return
	}
	if !r.URL.IsAbs() {
		http.Error(w, "仅接受代理请求", http.StatusBadRequest)
		return
	}
	out := r.Clone(r.Context())
	out.RequestURI = ""
	for _, k := range hopHeaders {
		out.Header.Del(k)
	}
	resp, err := h.forward.RoundTrip(out)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	for _, k := range hopHeaders {
		resp.Header.Del(k)
	}
	for k, vs := range resp.Header {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

func (h *bridgeHandler) tunnel(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	var upstream net.Conn
	var err error
	if cd, ok := h.dialer.(xproxy.ContextDialer); ok {
		upstream, err = cd.DialContext(ctx, "tcp", r.Host)
	} else {
		upstream, err = h.dialer.Dial("tcp", r.Host)
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		upstream.Close()
		http.Error(w, "不支持隧道", http.StatusInternalServerError)
		return
	}
	client, buf, err := hj.Hijack()
	if err != nil {
		upstream.Close()
		return
	}
	if _, err := client.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n")); err != nil {
		client.Close()
		upstream.Close()
		return
	}
	if n := buf.Reader.Buffered(); n > 0 {
		pending, _ := buf.Reader.Peek(n)
		if _, err := upstream.Write(pending); err != nil {
			client.Close()
			upstream.Close()
			return
		}
	}
	go func() {
		_, _ = io.Copy(upstream, client)
		upstream.Close()
	}()
	_, _ = io.Copy(client, upstream)
	client.Close()
}
