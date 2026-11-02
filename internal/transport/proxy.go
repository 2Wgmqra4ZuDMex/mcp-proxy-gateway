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
// 管理台已改为通过 ParamProxyID 引用「出站代理」，此字段保留用于兼容与迁移。
const ParamProxyURL = "proxyUrl"

// ParamProxyID 为上游引用的出站代理标识，可选；由 SetProxyResolver 注入的解析器换算为代理地址。
const ParamProxyID = "proxyId"

// ProxyResolver 将出站代理标识解析为代理地址。代理不存在或已停用时必须返回错误，
// 使引用它的上游连接失败而不是退回直连。
type ProxyResolver func(id string) (*url.URL, error)

var (
	proxyResolverMu sync.RWMutex
	proxyResolver   ProxyResolver
)

// SetProxyResolver 注入出站代理解析器；nil 表示未启用出站代理管理。
func SetProxyResolver(resolver ProxyResolver) {
	proxyResolverMu.Lock()
	proxyResolver = resolver
	proxyResolverMu.Unlock()
}

func currentProxyResolver() ProxyResolver {
	proxyResolverMu.RLock()
	defer proxyResolverMu.RUnlock()
	return proxyResolver
}

// ProxyIDFromParams 返回连接参数中引用的出站代理标识；未引用时为空串。
func ProxyIDFromParams(params map[string]any) string {
	s, _ := params[ParamProxyID].(string)
	return strings.TrimSpace(s)
}

var allowedProxySchemes = []string{"http", "https", "socks5", "socks5h"}

// validateProxyParam 校验可选的代理配置：proxyUrl 需为合法代理地址；proxyId 需为字符串，
// 且在已注入解析器时必须指向存在并启用的出站代理。两者不可同时设置。
func validateProxyParam(params map[string]any, fields map[string]string) {
	if _, err := proxyURLFromParams(params); err != nil {
		fields[fieldKey(ParamProxyURL)] = err.Error()
	}
	raw, ok := params[ParamProxyID]
	if !ok || raw == nil {
		return
	}
	id, isStr := raw.(string)
	if !isStr {
		fields[fieldKey(ParamProxyID)] = fmt.Sprintf("连接参数 %q 必须为字符串", ParamProxyID)
		return
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return
	}
	if u, _ := proxyURLFromParams(params); u != nil {
		fields[fieldKey(ParamProxyID)] = "不能同时设置代理地址与出站代理"
		return
	}
	if resolver := currentProxyResolver(); resolver != nil {
		if _, err := resolver(id); err != nil {
			fields[fieldKey(ParamProxyID)] = err.Error()
		}
	}
}

// resolveProxy 返回上游实际使用的代理：优先出站代理引用，其次内联代理地址；均未设置时为 nil。
func resolveProxy(params map[string]any) (*url.URL, error) {
	if id := ProxyIDFromParams(params); id != "" {
		resolver := currentProxyResolver()
		if resolver == nil {
			return nil, fmt.Errorf("出站代理服务未就绪")
		}
		return resolver(id)
	}
	return proxyURLFromParams(params)
}

// proxyURLFromParams 解析连接参数中的内联代理地址；未配置时返回 nil。
func proxyURLFromParams(params map[string]any) (*url.URL, error) {
	raw, ok := params[ParamProxyURL]
	if !ok || raw == nil {
		return nil, nil
	}
	s, ok := raw.(string)
	if !ok {
		return nil, fmt.Errorf("连接参数 %q 必须为字符串", ParamProxyURL)
	}
	return ParseProxyURL(s)
}

// ParseProxyURL 校验并解析代理地址；空串返回 nil。
func ParseProxyURL(s string) (*url.URL, error) {
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
// SOCKS5 代理会换成本地 HTTP 中转（见 childProxyURL），并开启 Node 读取代理环境变量。
func proxyEnv(proxy *url.URL, userEnv map[string]string) (map[string]string, error) {
	if proxy == nil {
		return userEnv, nil
	}
	value, err := childProxyURL(proxy)
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(userEnv)+9)
	for k, v := range userEnv {
		out[k] = v
	}
	defaults := map[string]string{"NODE_USE_ENV_PROXY": "1", "NO_PROXY": "localhost,127.0.0.1,::1", "no_proxy": "localhost,127.0.0.1,::1"}
	for _, key := range []string{"HTTPS_PROXY", "HTTP_PROXY", "ALL_PROXY", "https_proxy", "http_proxy", "all_proxy"} {
		defaults[key] = value
	}
	for key, v := range defaults {
		if _, exists := out[key]; !exists {
			out[key] = v
		}
	}
	return out, nil
}
