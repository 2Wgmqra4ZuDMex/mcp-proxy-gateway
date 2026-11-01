package transport

import (
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
)

// ParamProxyURL 为上游出站代理地址，可选；支持 http://、https://、socks5://、socks5h://。
// 留空表示沿用进程环境变量（HTTP_PROXY / HTTPS_PROXY / NO_PROXY）。
const ParamProxyURL = "proxyUrl"

var allowedProxySchemes = []string{"http", "https", "socks5", "socks5h"}

// validateProxyParam 校验可选的代理地址；空字符串视为未配置。
func validateProxyParam(params map[string]any, fields map[string]string) {
	if _, err := proxyURLFromParams(params); err != nil {
		fields[fieldKey(ParamProxyURL)] = err.Error()
	}
}

// proxyURLFromParams 解析连接参数中的代理地址；未配置时返回 nil。
func proxyURLFromParams(params map[string]any) (*url.URL, error) {
	raw, ok := params[ParamProxyURL]
	if !ok || raw == nil {
		return nil, nil
	}
	s, ok := raw.(string)
	if !ok {
		return nil, fmt.Errorf("连接参数 %q 必须为字符串", ParamProxyURL)
	}
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	u, err := url.Parse(s)
	if err != nil {
		return nil, fmt.Errorf("代理地址不是合法 URL")
	}
	if !slices.Contains(allowedProxySchemes, strings.ToLower(u.Scheme)) {
		return nil, fmt.Errorf("代理地址协议必须为 %s", strings.Join(schemeSuffixes(allowedProxySchemes), "、"))
	}
	if u.Hostname() == "" {
		return nil, fmt.Errorf("代理地址缺少主机名")
	}
	if u.Port() == "" {
		return nil, fmt.Errorf("代理地址缺少端口")
	}
	return u, nil
}

// RedactProxyURL 隐去代理地址中的密码，用于日志、诊断与界面回显。
func RedactProxyURL(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.User == nil {
		return raw
	}
	if _, has := u.User.Password(); has {
		u.User = url.UserPassword(u.User.Username(), "xxxxx")
	}
	return u.String()
}

// proxyTransports 按代理地址复用 http.Transport，使同一代理下的多个上游共享连接池。
var proxyTransports sync.Map // map[string]*http.Transport

// baseTransportFor 返回出站使用的 RoundTripper：未配置代理时为 http.DefaultTransport
// （遵循环境变量代理），配置后为固定走该代理的 Transport。
func baseTransportFor(proxy *url.URL) http.RoundTripper {
	if proxy == nil {
		return http.DefaultTransport
	}
	key := proxy.String()
	if cached, ok := proxyTransports.Load(key); ok {
		return cached.(*http.Transport)
	}
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.Proxy = http.ProxyURL(proxy)
	actual, _ := proxyTransports.LoadOrStore(key, t)
	return actual.(*http.Transport)
}

// proxyEnv 为 stdio 子进程生成代理环境变量；用户在 env 中显式设置的同名变量优先。
func proxyEnv(proxy *url.URL, userEnv map[string]string) map[string]string {
	if proxy == nil {
		return userEnv
	}
	out := make(map[string]string, len(userEnv)+6)
	for k, v := range userEnv {
		out[k] = v
	}
	value := proxy.String()
	for _, key := range []string{"HTTPS_PROXY", "HTTP_PROXY", "ALL_PROXY", "https_proxy", "http_proxy", "all_proxy"} {
		if _, exists := out[key]; !exists {
			out[key] = value
		}
	}
	return out
}
