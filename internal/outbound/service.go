// Package outbound 管理出站代理：代理的增删改查、上游分配、连通性与出口 IP 检测。
//
// 上游通过 connParams.proxyId 引用代理；传输层经 transport.SetProxyResolver 注入的
// Resolve 在建连时换算为代理地址。代理不存在或已停用时解析失败，引用它的上游连接
// 随之失败而不会退回直连，以免意外暴露网关本机出口。
package outbound

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/myGithub/mcp-proxy-gateway/internal/domain"
	"github.com/myGithub/mcp-proxy-gateway/internal/store"
	"github.com/myGithub/mcp-proxy-gateway/internal/transport"
)

const (
	maxNameLen = 100
	maxNoteLen = 500
	// DefaultIPCheckURL 为检测出口 IP 使用的公共服务，返回 {"ip": "..."}。
	DefaultIPCheckURL = "https://api.ipify.org?format=json"
	testTimeout       = 15 * time.Second
)

// Repository 是出站代理持久化窄接口；*store.OutboundProxyRepo 满足该接口。
type Repository interface {
	List(ctx context.Context) ([]store.OutboundProxy, error)
	Get(ctx context.Context, id string) (store.OutboundProxy, error)
	Create(ctx context.Context, p store.OutboundProxy) (store.OutboundProxy, error)
	Update(ctx context.Context, p store.OutboundProxy) (store.OutboundProxy, error)
	Delete(ctx context.Context, id string) error
}

// UpstreamManager 是上游读写与重连窄接口；*manager.Manager 满足该接口。
type UpstreamManager interface {
	List(ctx context.Context) ([]domain.Upstream, error)
	Update(ctx context.Context, id string, cfg domain.UpstreamConfig) (domain.Upstream, error)
	Reconnect(ctx context.Context, id string) error
}

// Proxy 是对外返回的代理视图；URL 为明文（管理端编辑使用），DisplayURL 隐去密码。
type Proxy struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	URL        string    `json:"url"`
	DisplayURL string    `json:"displayUrl"`
	Note       string    `json:"note"`
	Enabled    bool      `json:"enabled"`
	CreatedAt  time.Time `json:"createdAt"`
	// UpstreamIDs 为当前引用该代理的上游标识。
	UpstreamIDs []string `json:"upstreamIds"`
}

// Input 为创建/更新代理的请求载体。
type Input struct {
	Name    string `json:"name"`
	URL     string `json:"url"`
	Note    string `json:"note"`
	Enabled *bool  `json:"enabled"`
}

// TestResult 为代理检测结果。
type TestResult struct {
	OK        bool   `json:"ok"`
	LatencyMS int64  `json:"latencyMs"`
	ExitIP    string `json:"exitIp,omitempty"`
	Message   string `json:"message,omitempty"`
}

// AssignResult 汇总批量分配结果；单个上游失败不影响其余上游。
type AssignResult struct {
	Updated int               `json:"updated"`
	Failed  map[string]string `json:"failed,omitempty"`
}

// Service 是出站代理应用服务。
type Service struct {
	repo       Repository
	upstreams  UpstreamManager
	ipCheckURL string
	httpClient func(proxy *url.URL) *http.Client

	mu    sync.RWMutex
	cache map[string]store.OutboundProxy
}

// New 构造出站代理服务；调用方需在使用前调用 Load 预热缓存。
func New(repo Repository, upstreams UpstreamManager) *Service {
	return &Service{
		repo:       repo,
		upstreams:  upstreams,
		ipCheckURL: DefaultIPCheckURL,
		httpClient: func(proxy *url.URL) *http.Client {
			t := http.DefaultTransport.(*http.Transport).Clone()
			t.Proxy = http.ProxyURL(proxy)
			return &http.Client{Transport: t, Timeout: testTimeout}
		},
		cache: map[string]store.OutboundProxy{},
	}
}

// SetUpstreamManager 延迟注入上游管理器（装配时 manager 晚于本服务构造）。
func (s *Service) SetUpstreamManager(m UpstreamManager) { s.upstreams = m }

// Load 从仓储重新加载代理缓存。
func (s *Service) Load(ctx context.Context) error {
	list, err := s.repo.List(ctx)
	if err != nil {
		return err
	}
	next := make(map[string]store.OutboundProxy, len(list))
	for _, p := range list {
		next[p.ID] = p
	}
	s.mu.Lock()
	s.cache = next
	s.mu.Unlock()
	return nil
}

// Resolve 实现 transport.ProxyResolver：代理不存在或已停用时返回错误。
func (s *Service) Resolve(id string) (*url.URL, error) {
	s.mu.RLock()
	p, ok := s.cache[id]
	s.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("出站代理不存在")
	}
	if !p.Enabled {
		return nil, fmt.Errorf("出站代理「%s」已停用", p.Name)
	}
	u, err := transport.ParseProxyURL(p.URL)
	if err != nil {
		return nil, fmt.Errorf("出站代理「%s」地址无效：%v", p.Name, err)
	}
	return u, nil
}

// List 返回全部代理及其引用上游。
func (s *Service) List(ctx context.Context) ([]Proxy, error) {
	list, err := s.repo.List(ctx)
	if err != nil {
		return nil, err
	}
	usage, err := s.usage(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Proxy, 0, len(list))
	for _, p := range list {
		out = append(out, toView(p, usage[p.ID]))
	}
	return out, nil
}

func (s *Service) Create(ctx context.Context, in Input) (Proxy, error) {
	p, err := validateInput(in, true)
	if err != nil {
		return Proxy{}, err
	}
	created, err := s.repo.Create(ctx, p)
	if err != nil {
		return Proxy{}, err
	}
	if err := s.Load(ctx); err != nil {
		return Proxy{}, err
	}
	return toView(created, nil), nil
}

// Update 更新代理；地址或启停变化时重连所有引用它的上游，使新配置立即生效。
func (s *Service) Update(ctx context.Context, id string, in Input) (Proxy, error) {
	before, err := s.repo.Get(ctx, id)
	if err != nil {
		return Proxy{}, err
	}
	p, err := validateInput(in, before.Enabled)
	if err != nil {
		return Proxy{}, err
	}
	p.ID = before.ID
	updated, err := s.repo.Update(ctx, p)
	if err != nil {
		return Proxy{}, err
	}
	if err := s.Load(ctx); err != nil {
		return Proxy{}, err
	}
	usage, err := s.usage(ctx)
	if err != nil {
		return Proxy{}, err
	}
	if before.URL != updated.URL || before.Enabled != updated.Enabled {
		for _, upstreamID := range usage[updated.ID] {
			_ = s.upstreams.Reconnect(ctx, upstreamID)
		}
	}
	return toView(updated, usage[updated.ID]), nil
}

// Delete 删除代理；仍有上游引用时拒绝并列出这些上游。
func (s *Service) Delete(ctx context.Context, id string) error {
	ups, err := s.upstreams.List(ctx)
	if err != nil {
		return err
	}
	var names []string
	for _, up := range ups {
		if transport.ProxyIDFromParams(up.Config.ConnParams) == id {
			names = append(names, up.Config.Name)
		}
	}
	if len(names) > 0 {
		return domain.NewError(domain.CodeConflict, fmt.Sprintf("仍有 %d 个上游在使用该代理：%s，请先改为其他代理或直连", len(names), strings.Join(names, "、")))
	}
	if err := s.repo.Delete(ctx, id); err != nil {
		return err
	}
	return s.Load(ctx)
}

// Assign 将一组上游设为同一代理；proxyID 为空表示直连。
func (s *Service) Assign(ctx context.Context, upstreamIDs []string, proxyID string) (AssignResult, error) {
	proxyID = strings.TrimSpace(proxyID)
	if proxyID != "" {
		if _, err := s.Resolve(proxyID); err != nil {
			return AssignResult{}, domain.NewValidationError("分配失败", map[string]string{"proxyId": err.Error()})
		}
	}
	plan := make(map[string]string, len(upstreamIDs))
	for _, id := range upstreamIDs {
		plan[id] = proxyID
	}
	return s.apply(ctx, plan)
}

// Distribute 将上游按顺序轮流分配到给定代理：第 i 个上游使用第 i mod n 个代理。
func (s *Service) Distribute(ctx context.Context, upstreamIDs, proxyIDs []string) (AssignResult, error) {
	if len(proxyIDs) == 0 {
		return AssignResult{}, domain.NewValidationError("分配失败", map[string]string{"proxyIds": "请至少选择一个代理"})
	}
	for _, id := range proxyIDs {
		if _, err := s.Resolve(id); err != nil {
			return AssignResult{}, domain.NewValidationError("分配失败", map[string]string{"proxyIds": err.Error()})
		}
	}
	plan := make(map[string]string, len(upstreamIDs))
	for i, id := range upstreamIDs {
		plan[id] = proxyIDs[i%len(proxyIDs)]
	}
	return s.apply(ctx, plan)
}

func (s *Service) apply(ctx context.Context, plan map[string]string) (AssignResult, error) {
	ups, err := s.upstreams.List(ctx)
	if err != nil {
		return AssignResult{}, err
	}
	byID := make(map[string]domain.Upstream, len(ups))
	for _, up := range ups {
		byID[up.ID] = up
	}
	result := AssignResult{Failed: map[string]string{}}
	for upstreamID, proxyID := range plan {
		up, ok := byID[upstreamID]
		if !ok {
			result.Failed[upstreamID] = "上游不存在"
			continue
		}
		cfg := up.Config
		params := make(map[string]any, len(cfg.ConnParams)+1)
		maps.Copy(params, cfg.ConnParams)
		delete(params, transport.ParamProxyURL)
		if proxyID == "" {
			delete(params, transport.ParamProxyID)
		} else {
			params[transport.ParamProxyID] = proxyID
		}
		cfg.ConnParams = params
		if _, err := s.upstreams.Update(ctx, upstreamID, cfg); err != nil {
			result.Failed[up.Config.Name] = err.Error()
			continue
		}
		result.Updated++
	}
	if len(result.Failed) == 0 {
		result.Failed = nil
	}
	return result, nil
}

// Test 经代理访问出口 IP 查询服务，返回连通性、延迟与出口 IP。
func (s *Service) Test(ctx context.Context, id string) (TestResult, error) {
	p, err := s.repo.Get(ctx, id)
	if err != nil {
		return TestResult{}, err
	}
	u, err := transport.ParseProxyURL(p.URL)
	if err != nil || u == nil {
		return TestResult{OK: false, Message: "代理地址无效"}, nil
	}
	ctx, cancel := context.WithTimeout(ctx, testTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.ipCheckURL, nil)
	if err != nil {
		return TestResult{}, err
	}
	start := time.Now()
	resp, err := s.httpClient(u).Do(req)
	latency := time.Since(start).Milliseconds()
	if err != nil {
		return TestResult{OK: false, LatencyMS: latency, Message: redactError(err.Error(), p.URL)}, nil
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode != http.StatusOK {
		return TestResult{OK: false, LatencyMS: latency, Message: fmt.Sprintf("出口 IP 查询返回 HTTP %d", resp.StatusCode)}, nil
	}
	var payload struct {
		IP string `json:"ip"`
	}
	if json.Unmarshal(body, &payload) != nil || payload.IP == "" {
		payload.IP = strings.TrimSpace(string(body))
	}
	return TestResult{OK: true, LatencyMS: latency, ExitIP: payload.IP}, nil
}

// MigrateInlineProxies 把上游里内联的 proxyUrl 迁移为出站代理引用：相同地址复用同一代理。
// 返回迁移的上游数量。应在恢复上游连接之前调用。
func MigrateInlineProxies(ctx context.Context, repo Repository, upstreams *store.UpstreamRepo) (int, error) {
	rows, err := upstreams.List(ctx)
	if err != nil {
		return 0, err
	}
	existing, err := repo.List(ctx)
	if err != nil {
		return 0, err
	}
	byURL := make(map[string]string, len(existing))
	names := make(map[string]struct{}, len(existing))
	for _, p := range existing {
		byURL[strings.TrimSpace(p.URL)] = p.ID
		names[p.Name] = struct{}{}
	}
	migrated := 0
	for _, row := range rows {
		raw, _ := row.Config.ConnParams[transport.ParamProxyURL].(string)
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		if u, err := transport.ParseProxyURL(raw); err != nil || u == nil {
			continue
		}
		id, ok := byURL[raw]
		if !ok {
			created, err := repo.Create(ctx, store.OutboundProxy{Name: uniqueName(names, hostLabel(raw)), URL: raw, Enabled: true, Note: "由上游「" + row.Config.Name + "」的代理设置自动迁移"})
			if err != nil {
				return migrated, err
			}
			id = created.ID
			byURL[raw] = id
			names[created.Name] = struct{}{}
		}
		cfg := row.Config
		params := make(map[string]any, len(cfg.ConnParams))
		maps.Copy(params, cfg.ConnParams)
		delete(params, transport.ParamProxyURL)
		params[transport.ParamProxyID] = id
		cfg.ConnParams = params
		if _, err := upstreams.Update(ctx, row.ID, cfg); err != nil {
			return migrated, err
		}
		migrated++
	}
	return migrated, nil
}

func (s *Service) usage(ctx context.Context) (map[string][]string, error) {
	ups, err := s.upstreams.List(ctx)
	if err != nil {
		return nil, err
	}
	out := map[string][]string{}
	for _, up := range ups {
		if id := transport.ProxyIDFromParams(up.Config.ConnParams); id != "" {
			out[id] = append(out[id], up.ID)
		}
	}
	return out, nil
}

func validateInput(in Input, defaultEnabled bool) (store.OutboundProxy, error) {
	fields := map[string]string{}
	name := strings.TrimSpace(in.Name)
	if n := utf8.RuneCountInString(name); n < 1 || n > maxNameLen {
		fields["name"] = fmt.Sprintf("名称长度需在 1 至 %d 个字符之间", maxNameLen)
	}
	rawURL := strings.TrimSpace(in.URL)
	if rawURL == "" {
		fields["url"] = "请填写代理地址"
	} else if _, err := transport.ParseProxyURL(rawURL); err != nil {
		fields["url"] = err.Error()
	}
	note := strings.TrimSpace(in.Note)
	if utf8.RuneCountInString(note) > maxNoteLen {
		fields["note"] = fmt.Sprintf("备注不能超过 %d 个字符", maxNoteLen)
	}
	if len(fields) > 0 {
		return store.OutboundProxy{}, domain.NewValidationError("出站代理校验失败", fields)
	}
	enabled := defaultEnabled
	if in.Enabled != nil {
		enabled = *in.Enabled
	}
	return store.OutboundProxy{Name: name, URL: rawURL, Note: note, Enabled: enabled}, nil
}

func toView(p store.OutboundProxy, upstreamIDs []string) Proxy {
	ids := slices.Clone(upstreamIDs)
	if ids == nil {
		ids = []string{}
	}
	return Proxy{
		ID: p.ID, Name: p.Name, URL: p.URL, DisplayURL: transport.RedactProxyURL(p.URL),
		Note: p.Note, Enabled: p.Enabled, CreatedAt: p.CreatedAt, UpstreamIDs: ids,
	}
}

func hostLabel(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "代理"
	}
	return u.Scheme + "-" + u.Host
}

func uniqueName(taken map[string]struct{}, base string) string {
	if _, ok := taken[base]; !ok {
		return base
	}
	for i := 2; ; i++ {
		name := fmt.Sprintf("%s-%d", base, i)
		if _, ok := taken[name]; !ok {
			return name
		}
	}
}

// redactError 去掉错误信息里可能出现的代理密码。
func redactError(msg, proxyURL string) string {
	if u, err := url.Parse(proxyURL); err == nil && u.User != nil {
		if pw, ok := u.User.Password(); ok && pw != "" {
			msg = strings.ReplaceAll(msg, pw, "xxxxx")
		}
	}
	return msg
}
