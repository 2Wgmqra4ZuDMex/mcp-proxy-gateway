package discovery

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/myGithub/mcp-proxy-gateway/internal/domain"
	"github.com/myGithub/mcp-proxy-gateway/internal/risk"
)

const (
	defaultProviderName  = "智能发现"
	defaultBatchSize     = 8
	defaultConcurrency   = 2
	defaultTimeoutS      = 60
	maxRerankTimeout     = 20 * time.Second
	rerankCacheTTL       = 10 * time.Minute
	rerankCacheMax       = 256
	defaultRerankLimit   = 5
	maxRerankLimit       = 10
	autoEnrichDebounce   = 20 * time.Second
	maxDescriptionRunes  = 1200
	maxSiblings          = 40
	maxParams            = 20
	maxOverviewRunes     = 2000
	maxOverviewToolNames = 3
)

// ProviderStore 为 ai_provider 表的窄接口。
type ProviderStore interface {
	List(ctx context.Context) ([]risk.Provider, error)
	Create(ctx context.Context, p risk.Provider) (risk.Provider, error)
	Update(ctx context.Context, p risk.Provider) (risk.Provider, error)
	Activate(ctx context.Context, id string) error
}

// EnrichmentStore 为 tool_enrichment 表的窄接口。
type EnrichmentStore interface {
	List(ctx context.Context) ([]Enrichment, error)
	Upsert(ctx context.Context, items []Enrichment) error
}

// ToolSource 返回全局视角下全部已启用上游的工具（用于生成说明）。
type ToolSource func(ctx context.Context) ([]domain.ToolDiscovery, error)

// ConfigView 为管理台展示的配置，不含明文 Key。
type ConfigView struct {
	Configured    bool   `json:"configured"`
	BaseURL       string `json:"baseUrl"`
	APIStyle      string `json:"apiStyle"`
	Model         string `json:"model"`
	HasKey        bool   `json:"hasKey"`
	KeyHint       string `json:"keyHint,omitempty"`
	ProxyID       string `json:"proxyId"`
	TimeoutS      int    `json:"timeoutS"`
	EnrichEnabled bool   `json:"enrichEnabled"`
	RerankEnabled bool   `json:"rerankEnabled"`
}

// ConfigInput 为保存配置的输入；APIKey 为空表示保留已保存的 Key。
type ConfigInput struct {
	BaseURL       string `json:"baseUrl"`
	APIStyle      string `json:"apiStyle"`
	Model         string `json:"model"`
	APIKey        string `json:"apiKey"`
	ProxyID       string `json:"proxyId"`
	TimeoutS      int    `json:"timeoutS"`
	EnrichEnabled bool   `json:"enrichEnabled"`
	RerankEnabled bool   `json:"rerankEnabled"`
}

// TestResult 为测试连接的结果。
type TestResult struct {
	LatencyMS    int64  `json:"latencyMs"`
	Reply        string `json:"reply"`
	Model        string `json:"model"`
	InputTokens  int64  `json:"inputTokens"`
	OutputTokens int64  `json:"outputTokens"`
}

// Status 为工具说明增强的进度与覆盖情况。
type Status struct {
	Running        bool       `json:"running"`
	Total          int        `json:"total"`
	Enriched       int        `json:"enriched"`
	Pending        int        `json:"pending"`
	Services       int        `json:"services"`
	Progress       int        `json:"progress"`
	ProgressTotal  int        `json:"progressTotal"`
	Failed         int        `json:"failed"`
	LastError      string     `json:"lastError,omitempty"`
	LastRunAt      *time.Time `json:"lastRunAt,omitempty"`
	LastDurationMS int64      `json:"lastDurationMs,omitempty"`
	LastAdded      int        `json:"lastAdded,omitempty"`
	InputTokens    int64      `json:"inputTokens,omitempty"`
	OutputTokens   int64      `json:"outputTokens,omitempty"`
}

type rerankEntry struct {
	result  RerankResult
	expires time.Time
}

// Service 为智能发现的 AI 辅助服务。所有方法并发安全。
type Service struct {
	providers   ProviderStore
	enrichments EnrichmentStore
	client      *client
	logger      *slog.Logger
	now         func() time.Time

	mu         sync.RWMutex
	provider   *risk.Provider
	byHash     map[string]Enrichment
	byService  map[string]Enrichment
	toolSource ToolSource
	invalidate func()
	baseCtx    context.Context

	runMu       sync.Mutex
	running     bool
	rerunQueued bool
	status      Status
	debounce    *time.Timer

	cacheMu     sync.Mutex
	rerankCache map[string]rerankEntry
}

func New(providers ProviderStore, enrichments EnrichmentStore, resolveProxy ProxyResolver, logger *slog.Logger) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{
		providers: providers, enrichments: enrichments,
		client: &client{resolveProxy: resolveProxy}, logger: logger, now: time.Now,
		byHash: map[string]Enrichment{}, byService: map[string]Enrichment{},
		baseCtx: context.Background(), rerankCache: map[string]rerankEntry{},
	}
}

// SetToolSource 注入全局工具来源。
func (s *Service) SetToolSource(source ToolSource) {
	s.mu.Lock()
	s.toolSource = source
	s.mu.Unlock()
}

// SetInvalidate 注入检索索引失效回调（工具说明更新或开关变化后重建索引）。
func (s *Service) SetInvalidate(fn func()) {
	s.mu.Lock()
	s.invalidate = fn
	s.mu.Unlock()
}

// Start 记录后台任务使用的上下文；应用停止时取消该上下文即可终止生成任务。
func (s *Service) Start(ctx context.Context) {
	s.mu.Lock()
	s.baseCtx = ctx
	s.mu.Unlock()
}

// Load 读取 Provider 与已生成的说明到内存。
func (s *Service) Load(ctx context.Context) error {
	if err := s.reloadProvider(ctx); err != nil {
		return err
	}
	items, err := s.enrichments.List(ctx)
	if err != nil {
		return err
	}
	s.mu.Lock()
	for _, item := range items {
		s.remember(item)
	}
	s.mu.Unlock()
	return nil
}

func (s *Service) remember(item Enrichment) {
	if item.Kind == KindService {
		s.byService[item.Name] = item
		return
	}
	s.byHash[item.Hash] = item
}

func (s *Service) reloadProvider(ctx context.Context) error {
	list, err := s.providers.List(ctx)
	if err != nil {
		return err
	}
	var chosen *risk.Provider
	for i := range list {
		if list[i].Active && list[i].Enabled {
			chosen = &list[i]
			break
		}
	}
	if chosen == nil && len(list) > 0 {
		chosen = &list[0]
	}
	s.mu.Lock()
	s.provider = chosen
	s.mu.Unlock()
	return nil
}

func (s *Service) currentProvider() (risk.Provider, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.provider == nil {
		return risk.Provider{}, false
	}
	return *s.provider, true
}

// ── 配置 ────────────────────────────────────────────────────────────────

func (s *Service) Config(context.Context) ConfigView {
	p, ok := s.currentProvider()
	if !ok {
		return ConfigView{APIStyle: string(risk.APIStyleChatCompletions), TimeoutS: defaultTimeoutS}
	}
	view := ConfigView{
		Configured: true, BaseURL: p.BaseURL, APIStyle: string(p.APIStyle), Model: p.Model,
		HasKey: p.APIKey != "", ProxyID: p.ProxyID, TimeoutS: p.TimeoutS,
		EnrichEnabled: p.EnrichEnabled, RerankEnabled: p.RerankEnabled,
	}
	if r := []rune(p.APIKey); len(r) > 8 {
		view.KeyHint = "…" + string(r[len(r)-4:])
	}
	return view
}

func (s *Service) mergeInput(in ConfigInput) (risk.Provider, error) {
	p, ok := s.currentProvider()
	if !ok {
		p = risk.Provider{Name: defaultProviderName, Enabled: true, BatchSize: defaultBatchSize, MaxConcurrency: defaultConcurrency}
	}
	p.BaseURL = strings.TrimRight(strings.TrimSpace(in.BaseURL), "/")
	p.Model = strings.TrimSpace(in.Model)
	p.APIStyle = risk.APIStyle(strings.TrimSpace(in.APIStyle))
	if p.APIStyle == "" {
		p.APIStyle = risk.APIStyleChatCompletions
	}
	if key := strings.TrimSpace(in.APIKey); key != "" {
		p.APIKey = key
	}
	p.ProxyID = strings.TrimSpace(in.ProxyID)
	p.TimeoutS = in.TimeoutS
	if p.TimeoutS == 0 {
		p.TimeoutS = defaultTimeoutS
	}
	if p.BatchSize < 1 || p.BatchSize > 50 {
		p.BatchSize = defaultBatchSize
	}
	if p.MaxConcurrency < 1 || p.MaxConcurrency > 3 {
		p.MaxConcurrency = defaultConcurrency
	}
	if strings.TrimSpace(p.Name) == "" {
		p.Name = defaultProviderName
	}
	p.EnrichEnabled, p.RerankEnabled = in.EnrichEnabled, in.RerankEnabled
	fields := map[string]string{}
	if p.BaseURL == "" {
		fields["baseUrl"] = "请填写接口地址"
	}
	if p.Model == "" {
		fields["model"] = "请填写模型名称"
	}
	if len(fields) > 0 {
		return p, domain.NewValidationError("配置不完整", fields)
	}
	if err := risk.ValidateProvider(p); err != nil {
		return p, domain.NewValidationError(err.Error(), nil)
	}
	if p.ProxyID != "" && s.client.resolveProxy != nil {
		if _, err := s.client.resolveProxy(p.ProxyID); err != nil {
			return p, domain.NewValidationError("出站代理不可用："+err.Error(), map[string]string{"proxyId": err.Error()})
		}
	}
	return p, nil
}

// SaveConfig 保存接口配置与开关。没有 Provider 时新建并设为启用。
func (s *Service) SaveConfig(ctx context.Context, in ConfigInput) (ConfigView, error) {
	before, hadBefore := s.currentProvider()
	p, err := s.mergeInput(in)
	if err != nil {
		return ConfigView{}, err
	}
	p.Enabled = true
	var saved risk.Provider
	if p.ID == "" {
		saved, err = s.providers.Create(ctx, p)
	} else {
		saved, err = s.providers.Update(ctx, p)
	}
	if err != nil {
		return ConfigView{}, err
	}
	if !saved.Active {
		if err := s.providers.Activate(ctx, saved.ID); err != nil {
			return ConfigView{}, err
		}
		saved.Active = true
	}
	s.mu.Lock()
	s.provider = &saved
	invalidate := s.invalidate
	s.mu.Unlock()
	s.clearRerankCache()
	if invalidate != nil && (!hadBefore || before.EnrichEnabled != saved.EnrichEnabled) {
		invalidate()
	}
	if saved.EnrichEnabled && (!hadBefore || !before.EnrichEnabled) {
		s.ToolsChanged()
	}
	return s.Config(ctx), nil
}

// TestConnection 用输入的配置（Key 留空则用已保存的）发一次最小请求。
func (s *Service) TestConnection(ctx context.Context, in ConfigInput) (TestResult, error) {
	p, err := s.mergeInput(in)
	if err != nil {
		return TestResult{}, err
	}
	started := s.now()
	reply, usage, err := s.client.complete(ctx, p, "你是接口连通性测试。", "只回复两个字母：OK", 30*time.Second)
	if err != nil {
		return TestResult{}, domain.NewError(domain.CodeUpstreamUnavailable, err.Error())
	}
	return TestResult{
		LatencyMS: s.now().Sub(started).Milliseconds(), Reply: truncateRunes(strings.TrimSpace(reply), 100),
		Model: p.Model, InputTokens: usage.InputTokens, OutputTokens: usage.OutputTokens,
	}, nil
}

// ── 检索集成 ──────────────────────────────────────────────────────────────

// ToolKeywords 返回某工具的增强检索文本（中文简介 + 关键词 + 适用场景）；
// 未开启工具说明增强或尚未生成时返回空串。
func (s *Service) ToolKeywords(originalName, description string) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.provider == nil || !s.provider.EnrichEnabled {
		return ""
	}
	item, ok := s.byHash[ToolHash(originalName, description)]
	if !ok {
		return ""
	}
	parts := append([]string{item.Summary}, item.Keywords...)
	if item.UseWhen != "" {
		parts = append(parts, item.UseWhen)
	}
	return strings.Join(parts, " ")
}

// RerankEnabled 报告 AI 挑选工具是否可用。
func (s *Service) RerankEnabled() bool {
	p, ok := s.currentProvider()
	return ok && p.Enabled && p.RerankEnabled
}

// ServiceOverview 生成服务清单：每个服务一行，含 AI 生成的服务简介（如有）与代表性工具名。
func (s *Service) ServiceOverview(discoveries []domain.ToolDiscovery) string {
	type group struct {
		label string
		tools []string
	}
	groups := map[string]*group{}
	order := []string{}
	for _, d := range discoveries {
		label := serviceLabel(d)
		key := strings.ToLower(label)
		g, ok := groups[key]
		if !ok {
			g = &group{label: label}
			groups[key] = g
			order = append(order, key)
		}
		g.tools = append(g.tools, d.Tool.Name)
	}
	if len(order) == 0 {
		return ""
	}
	sort.Slice(order, func(i, j int) bool { return groups[order[i]].label < groups[order[j]].label })

	s.mu.RLock()
	defer s.mu.RUnlock()
	var b strings.Builder
	b.WriteString("本网关可用的服务如下。需要相关能力时，先用 search_tools（可直接用中文描述需求）或 list_tools（upstream 填服务名）找到工具，再用 get_tool 查看参数、call_tool 调用。\n")
	for i, key := range order {
		g := groups[key]
		names := g.tools
		suffix := ""
		if len(names) > maxOverviewToolNames {
			suffix = fmt.Sprintf(" 等 %d 个", len(names))
			names = names[:maxOverviewToolNames]
		}
		line := "- " + g.label
		if item, ok := s.byService[key]; ok && item.Summary != "" {
			line += "：" + item.Summary
		}
		line += "（" + strings.Join(names, "、") + suffix + "）\n"
		if utf8.RuneCountInString(b.String())+utf8.RuneCountInString(line) > maxOverviewRunes {
			fmt.Fprintf(&b, "- …另有 %d 个服务，可用 list_tools 查看\n", len(order)-i)
			break
		}
		b.WriteString(line)
	}
	return strings.TrimRight(b.String(), "\n")
}

// ── AI 挑选工具 ───────────────────────────────────────────────────────────

const rerankSystemPrompt = `你是 MCP 工具路由助手。根据用户需求，从候选工具中挑选最合适的工具，最多 %d 个，按建议调用顺序排列：需要前置步骤的（例如先查 ID 再查询）把前置工具放前面。
只能从候选中选，名称必须与候选 name 完全一致；没有合适的就返回空数组，不要硬凑。
note 用中文一句话说明怎么用或调用顺序，不超过 60 字。
只输出 JSON，不要其他文字：{"tools":["工具名"],"note":"…"}`

// Rerank 让模型从候选中挑选工具。调用方应在出错时回退到关键词检索。
func (s *Service) Rerank(ctx context.Context, query string, candidates []Candidate, limit int) (RerankResult, error) {
	p, ok := s.currentProvider()
	if !ok || !p.RerankEnabled {
		return RerankResult{}, errors.New("AI 挑选工具未启用")
	}
	if len(candidates) == 0 {
		return RerankResult{Names: []string{}}, nil
	}
	if limit <= 0 {
		limit = defaultRerankLimit
	}
	limit = min(limit, maxRerankLimit)
	names := make([]string, 0, len(candidates))
	for _, c := range candidates {
		names = append(names, c.Name)
	}
	cacheKey := hashOf(p.Model, p.BaseURL, strings.TrimSpace(query), fmt.Sprint(limit), strings.Join(names, "\x00"))
	if cached, ok := s.cachedRerank(cacheKey); ok {
		cached.Cached = true
		return cached, nil
	}

	type line struct {
		Name    string   `json:"name"`
		Service string   `json:"service,omitempty"`
		Summary string   `json:"summary"`
		UseWhen string   `json:"useWhen,omitempty"`
		Related []string `json:"related,omitempty"`
	}
	lines := make([]line, 0, len(candidates))
	s.mu.RLock()
	for _, c := range candidates {
		l := line{Name: c.Name, Service: c.Service, Summary: truncateRunes(collapseSpace(c.Description), 160)}
		if item, ok := s.byHash[ToolHash(c.OriginalName, c.Description)]; ok {
			l.Summary, l.UseWhen, l.Related = item.Summary, item.UseWhen, item.Related
		}
		lines = append(lines, l)
	}
	s.mu.RUnlock()
	user, _ := json.Marshal(map[string]any{"query": query, "candidates": lines})

	timeout := min(time.Duration(p.TimeoutS)*time.Second, maxRerankTimeout)
	started := s.now()
	text, _, err := s.client.complete(ctx, p, fmt.Sprintf(rerankSystemPrompt, limit), string(user), timeout)
	if err != nil {
		return RerankResult{}, err
	}
	var parsed struct {
		Tools []string `json:"tools"`
		Note  string   `json:"note"`
	}
	if err := decodeJSONObject(text, &parsed); err != nil {
		return RerankResult{}, err
	}
	allowed := make(map[string]struct{}, len(names))
	for _, n := range names {
		allowed[n] = struct{}{}
	}
	result := RerankResult{Names: []string{}, Note: truncateRunes(strings.TrimSpace(parsed.Note), 120), LatencyMS: s.now().Sub(started).Milliseconds()}
	seen := map[string]struct{}{}
	for _, n := range parsed.Tools {
		n = strings.TrimSpace(n)
		if _, ok := allowed[n]; !ok {
			continue
		}
		if _, dup := seen[n]; dup {
			continue
		}
		seen[n] = struct{}{}
		result.Names = append(result.Names, n)
		if len(result.Names) >= limit {
			break
		}
	}
	s.storeRerank(cacheKey, result)
	return result, nil
}

func (s *Service) cachedRerank(key string) (RerankResult, bool) {
	s.cacheMu.Lock()
	defer s.cacheMu.Unlock()
	entry, ok := s.rerankCache[key]
	if !ok || s.now().After(entry.expires) {
		delete(s.rerankCache, key)
		return RerankResult{}, false
	}
	return entry.result, true
}

func (s *Service) storeRerank(key string, result RerankResult) {
	s.cacheMu.Lock()
	defer s.cacheMu.Unlock()
	if len(s.rerankCache) >= rerankCacheMax {
		now := s.now()
		for k, v := range s.rerankCache {
			if now.After(v.expires) || len(s.rerankCache) >= rerankCacheMax {
				delete(s.rerankCache, k)
			}
		}
	}
	s.rerankCache[key] = rerankEntry{result: result, expires: s.now().Add(rerankCacheTTL)}
}

func (s *Service) clearRerankCache() {
	s.cacheMu.Lock()
	s.rerankCache = map[string]rerankEntry{}
	s.cacheMu.Unlock()
}

// ── 指纹与服务名 ──────────────────────────────────────────────────────────

// ToolHash 为工具指纹：原始名称 + 描述。多账号提供的同一工具共享一份说明。
func ToolHash(originalName, description string) string {
	return hashOf("tool", originalName, description)
}

func hashOf(parts ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(sum[:16])
}

var accountSuffix = regexp.MustCompile(`[\s_-]*\d+$`)

// ServiceName 去掉上游名称末尾的账号序号：tavily01 → tavily，context7-03 → context7。
// 与管理台账号分组规则一致。
func ServiceName(upstreamName string) string {
	trimmed := strings.TrimSpace(upstreamName)
	stripped := accountSuffix.ReplaceAllString(trimmed, "")
	if stripped == "" {
		return trimmed
	}
	return stripped
}

func serviceLabel(d domain.ToolDiscovery) string {
	if len(d.UpstreamNames) == 0 {
		return "其他"
	}
	names := append([]string(nil), d.UpstreamNames...)
	sort.Strings(names)
	return ServiceName(names[0])
}

func truncateRunes(value string, limit int) string {
	r := []rune(value)
	if len(r) <= limit {
		return value
	}
	return string(r[:limit]) + "…"
}

func collapseSpace(value string) string {
	return strings.Join(strings.Fields(value), " ")
}
