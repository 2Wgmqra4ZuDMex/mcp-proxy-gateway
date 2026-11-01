package discovery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/myGithub/mcp-proxy-gateway/internal/domain"
	"github.com/myGithub/mcp-proxy-gateway/internal/risk"
)

const toolSystemPrompt = `你是 MCP 工具检索助手。为每个工具生成检索元数据，帮助其他 AI 用中文或英文的自然说法找到它。
要求：
- summary：中文一句话说明工具做什么，不超过 40 字。
- keywords：10～20 个检索关键词，中英文都要有，包含同义词、常见中文说法和用户可能的提问用词（例如"网页抓取""爬虫""scrape"），不要只重复工具名。
- useWhen：中文，什么情况下应该用它，不超过 60 字。
- related：同一服务中通常需要先调用或配合使用的工具名，只能从该工具的 siblings 中选，没有就返回空数组。
只输出 JSON，不要其他文字：{"tools":[{"id":"…","summary":"…","keywords":["…"],"useWhen":"…","related":["…"]}]}，id 与输入一致。`

const serviceSystemPrompt = `为每个 MCP 服务写一句中文简介，不超过 30 字，说明它擅长做什么，便于 AI 判断何时使用。
只输出 JSON，不要其他文字：{"services":[{"id":"…","summary":"…"}]}，id 与输入一致。`

type pendingTool struct {
	hash        string
	name        string
	original    string
	description string
	service     string
	params      []string
}

// ToolsChanged 在工具集变化后调用：开启了工具说明增强时，防抖后为新增工具补生成说明。
func (s *Service) ToolsChanged() {
	p, ok := s.currentProvider()
	if !ok || !p.EnrichEnabled {
		return
	}
	s.runMu.Lock()
	defer s.runMu.Unlock()
	if s.debounce != nil {
		s.debounce.Stop()
	}
	s.debounce = time.AfterFunc(autoEnrichDebounce, func() {
		if err := s.StartEnrich(false); err != nil && !errors.Is(err, errAlreadyRunning) {
			s.logger.Warn("自动生成工具说明未启动", "error", err)
		}
	})
}

var errAlreadyRunning = errors.New("工具说明正在生成中")

// StartEnrich 在后台生成工具说明。force 为 true 时全部重新生成。
func (s *Service) StartEnrich(force bool) error {
	p, ok := s.currentProvider()
	if !ok {
		return domain.NewValidationError("请先配置 AI 接口", nil)
	}
	s.mu.RLock()
	source, ctx := s.toolSource, s.baseCtx
	s.mu.RUnlock()
	if source == nil {
		return domain.NewError(domain.CodeInternal, "工具来源未就绪")
	}
	s.runMu.Lock()
	if s.running {
		if !force {
			s.rerunQueued = true
		}
		s.runMu.Unlock()
		return errAlreadyRunning
	}
	s.running = true
	s.status.Running, s.status.Progress, s.status.ProgressTotal, s.status.Failed = true, 0, 0, 0
	s.status.LastError, s.status.LastAdded, s.status.InputTokens, s.status.OutputTokens = "", 0, 0, 0
	s.runMu.Unlock()

	go func() {
		defer func() {
			if r := recover(); r != nil {
				s.logger.Error("生成工具说明时发生异常", "panic", r)
				s.setLastError(fmt.Sprint(r))
			}
			s.runMu.Lock()
			s.running, s.status.Running = false, false
			again := s.rerunQueued
			s.rerunQueued = false
			s.runMu.Unlock()
			if again {
				_ = s.StartEnrich(false)
			}
		}()
		s.runEnrich(ctx, p, source, force)
	}()
	return nil
}

func (s *Service) runEnrich(ctx context.Context, p risk.Provider, source ToolSource, force bool) {
	started := s.now()
	discoveries, err := source(ctx)
	if err != nil {
		s.setLastError("读取工具列表失败：" + err.Error())
		return
	}
	pending, siblings := s.pendingTools(discoveries, force)
	s.runMu.Lock()
	s.status.ProgressTotal = len(pending)
	s.runMu.Unlock()

	batchSize := p.BatchSize
	if batchSize < 1 || batchSize > 20 {
		batchSize = defaultBatchSize
	}
	workers := min(max(p.MaxConcurrency, 1), 3)
	batches := make(chan []pendingTool)
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for batch := range batches {
				if ctx.Err() != nil {
					continue
				}
				s.enrichBatch(ctx, p, batch, siblings)
			}
		}()
	}
	for start := 0; start < len(pending); start += batchSize {
		batches <- pending[start:min(start+batchSize, len(pending))]
	}
	close(batches)
	wg.Wait()

	if ctx.Err() == nil {
		s.enrichServices(ctx, p, discoveries, force)
	}
	s.mu.RLock()
	invalidate := s.invalidate
	s.mu.RUnlock()
	if invalidate != nil {
		invalidate()
	}
	finished := s.now()
	s.runMu.Lock()
	s.status.LastRunAt = &finished
	s.status.LastDurationMS = finished.Sub(started).Milliseconds()
	s.runMu.Unlock()
	s.logger.Info("工具说明生成完成", "pending", len(pending), "added", s.snapshotStatus().LastAdded, "failed", s.snapshotStatus().Failed)
}

// pendingTools 按指纹去重，返回待生成的工具与每个服务的工具名（供 related 选择）。
func (s *Service) pendingTools(discoveries []domain.ToolDiscovery, force bool) ([]pendingTool, map[string][]string) {
	siblings := map[string][]string{}
	seen := map[string]struct{}{}
	pending := []pendingTool{}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, d := range discoveries {
		service := serviceLabel(d)
		siblings[service] = append(siblings[service], d.Tool.Name)
		hash := ToolHash(d.Tool.OriginalName, d.Tool.Description)
		if _, dup := seen[hash]; dup {
			continue
		}
		seen[hash] = struct{}{}
		if _, done := s.byHash[hash]; done && !force {
			continue
		}
		pending = append(pending, pendingTool{
			hash: hash, name: d.Tool.Name, original: d.Tool.OriginalName, description: d.Tool.Description,
			service: service, params: schemaParams(d.Tool.InputSchema),
		})
	}
	return pending, siblings
}

func (s *Service) enrichBatch(ctx context.Context, p risk.Provider, batch []pendingTool, siblings map[string][]string) {
	type input struct {
		ID          string   `json:"id"`
		Name        string   `json:"name"`
		Service     string   `json:"service"`
		Description string   `json:"description"`
		Params      []string `json:"params,omitempty"`
		Siblings    []string `json:"siblings,omitempty"`
	}
	inputs := make([]input, 0, len(batch))
	byID := make(map[string]pendingTool, len(batch))
	for i, tool := range batch {
		id := fmt.Sprintf("t%d", i+1)
		byID[id] = tool
		sib := []string{}
		for _, name := range siblings[tool.service] {
			if name != tool.name && len(sib) < maxSiblings {
				sib = append(sib, name)
			}
		}
		inputs = append(inputs, input{ID: id, Name: tool.name, Service: tool.service,
			Description: truncateRunes(collapseSpace(tool.description), maxDescriptionRunes), Params: tool.params, Siblings: sib})
	}
	user, _ := json.Marshal(map[string]any{"tools": inputs})
	text, usage, err := s.client.complete(ctx, p, toolSystemPrompt, string(user), 0)
	s.addUsage(usage)
	if err != nil {
		s.batchFailed(len(batch), err)
		return
	}
	var parsed struct {
		Tools []struct {
			ID       string   `json:"id"`
			Summary  string   `json:"summary"`
			Keywords []string `json:"keywords"`
			UseWhen  string   `json:"useWhen"`
			Related  []string `json:"related"`
		} `json:"tools"`
	}
	if err := decodeJSONObject(text, &parsed); err != nil {
		s.batchFailed(len(batch), err)
		return
	}
	now := s.now().UTC()
	items := make([]Enrichment, 0, len(parsed.Tools))
	for _, out := range parsed.Tools {
		tool, ok := byID[strings.TrimSpace(out.ID)]
		if !ok || strings.TrimSpace(out.Summary) == "" {
			continue
		}
		delete(byID, out.ID)
		allowed := map[string]struct{}{}
		for _, name := range siblings[tool.service] {
			allowed[name] = struct{}{}
		}
		related := []string{}
		for _, name := range out.Related {
			if _, ok := allowed[name]; ok && name != tool.name && len(related) < 5 {
				related = append(related, name)
			}
		}
		items = append(items, Enrichment{
			Hash: tool.hash, Kind: KindTool, Name: tool.name,
			Summary: truncateRunes(strings.TrimSpace(out.Summary), 80), Keywords: cleanKeywords(out.Keywords),
			UseWhen: truncateRunes(strings.TrimSpace(out.UseWhen), 120), Related: related, Model: p.Model, UpdatedAt: now,
		})
	}
	if err := s.save(ctx, items); err != nil {
		s.batchFailed(len(batch), err)
		return
	}
	missing := len(byID)
	s.runMu.Lock()
	s.status.Progress += len(batch)
	s.status.LastAdded += len(items)
	s.status.Failed += missing
	if missing > 0 && s.status.LastError == "" {
		s.status.LastError = fmt.Sprintf("模型漏掉了 %d 个工具，下次生成时会补上", missing)
	}
	s.runMu.Unlock()
}

func (s *Service) enrichServices(ctx context.Context, p risk.Provider, discoveries []domain.ToolDiscovery, force bool) {
	type svcTool struct {
		Name    string `json:"name"`
		Summary string `json:"summary,omitempty"`
	}
	type svcInput struct {
		ID    string    `json:"id"`
		Name  string    `json:"name"`
		Tools []svcTool `json:"tools"`
	}
	groups := map[string][]domain.ToolDiscovery{}
	labels := map[string]string{}
	for _, d := range discoveries {
		label := serviceLabel(d)
		key := strings.ToLower(label)
		labels[key] = label
		groups[key] = append(groups[key], d)
	}
	keys := make([]string, 0, len(groups))
	for key := range groups {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	s.mu.RLock()
	inputs := []svcInput{}
	hashes := map[string]string{}
	for _, key := range keys {
		names := make([]string, 0, len(groups[key]))
		for _, d := range groups[key] {
			names = append(names, d.Tool.Name)
		}
		sort.Strings(names)
		hash := "svc:" + hashOf(append([]string{key}, names...)...)
		if existing, ok := s.byService[key]; ok && existing.Hash == hash && !force {
			continue
		}
		id := fmt.Sprintf("s%d", len(inputs)+1)
		hashes[id] = hash
		tools := []svcTool{}
		for _, d := range groups[key] {
			if len(tools) >= 30 {
				break
			}
			t := svcTool{Name: d.Tool.Name}
			if item, ok := s.byHash[ToolHash(d.Tool.OriginalName, d.Tool.Description)]; ok {
				t.Summary = item.Summary
			} else {
				t.Summary = truncateRunes(collapseSpace(d.Tool.Description), 100)
			}
			tools = append(tools, t)
		}
		inputs = append(inputs, svcInput{ID: id, Name: labels[key], Tools: tools})
	}
	s.mu.RUnlock()
	if len(inputs) == 0 {
		return
	}
	for start := 0; start < len(inputs); start += 20 {
		chunk := inputs[start:min(start+20, len(inputs))]
		user, _ := json.Marshal(map[string]any{"services": chunk})
		text, usage, err := s.client.complete(ctx, p, serviceSystemPrompt, string(user), 0)
		s.addUsage(usage)
		if err != nil {
			s.setLastError("生成服务简介失败：" + err.Error())
			continue
		}
		var parsed struct {
			Services []struct {
				ID      string `json:"id"`
				Summary string `json:"summary"`
			} `json:"services"`
		}
		if err := decodeJSONObject(text, &parsed); err != nil {
			s.setLastError("生成服务简介失败：" + err.Error())
			continue
		}
		byID := map[string]svcInput{}
		for _, in := range chunk {
			byID[in.ID] = in
		}
		items := []Enrichment{}
		for _, out := range parsed.Services {
			in, ok := byID[strings.TrimSpace(out.ID)]
			if !ok || strings.TrimSpace(out.Summary) == "" {
				continue
			}
			items = append(items, Enrichment{Hash: hashes[in.ID], Kind: KindService, Name: strings.ToLower(in.Name),
				Summary: truncateRunes(strings.TrimSpace(out.Summary), 60), Keywords: []string{}, Model: p.Model, UpdatedAt: s.now().UTC()})
		}
		if err := s.save(ctx, items); err != nil {
			s.setLastError("保存服务简介失败：" + err.Error())
		}
	}
}

func (s *Service) save(ctx context.Context, items []Enrichment) error {
	if len(items) == 0 {
		return nil
	}
	if err := s.enrichments.Upsert(ctx, items); err != nil {
		return err
	}
	s.mu.Lock()
	for _, item := range items {
		if item.Kind == KindService {
			// 服务指纹按服务名唯一：新指纹覆盖旧记录。
			s.byService[item.Name] = item
			continue
		}
		s.byHash[item.Hash] = item
	}
	s.mu.Unlock()
	return nil
}

func (s *Service) batchFailed(count int, err error) {
	s.logger.Warn("生成工具说明失败", "count", count, "error", err)
	s.runMu.Lock()
	s.status.Progress += count
	s.status.Failed += count
	s.status.LastError = err.Error()
	s.runMu.Unlock()
}

func (s *Service) setLastError(msg string) {
	s.runMu.Lock()
	s.status.LastError = msg
	s.runMu.Unlock()
}

func (s *Service) addUsage(u Usage) {
	s.runMu.Lock()
	s.status.InputTokens += u.InputTokens
	s.status.OutputTokens += u.OutputTokens
	s.runMu.Unlock()
}

func (s *Service) snapshotStatus() Status {
	s.runMu.Lock()
	defer s.runMu.Unlock()
	return s.status
}

// Status 返回生成进度与当前工具集的覆盖情况。
func (s *Service) Status(ctx context.Context) Status {
	st := s.snapshotStatus()
	s.mu.RLock()
	source := s.toolSource
	s.mu.RUnlock()
	if source == nil {
		return st
	}
	discoveries, err := source(ctx)
	if err != nil {
		return st
	}
	seen := map[string]struct{}{}
	services := map[string]struct{}{}
	s.mu.RLock()
	for _, d := range discoveries {
		services[strings.ToLower(serviceLabel(d))] = struct{}{}
		hash := ToolHash(d.Tool.OriginalName, d.Tool.Description)
		if _, dup := seen[hash]; dup {
			continue
		}
		seen[hash] = struct{}{}
		st.Total++
		if _, ok := s.byHash[hash]; ok {
			st.Enriched++
		}
	}
	for key := range services {
		if _, ok := s.byService[key]; ok {
			st.Services++
		}
	}
	s.mu.RUnlock()
	st.Pending = st.Total - st.Enriched
	return st
}

// EnrichedTool 为管理台展示的一条工具说明。
type EnrichedTool struct {
	Name     string   `json:"name"`
	Service  string   `json:"service"`
	Summary  string   `json:"summary"`
	Keywords []string `json:"keywords"`
	UseWhen  string   `json:"useWhen,omitempty"`
	Related  []string `json:"related,omitempty"`
	Enriched bool     `json:"enriched"`
}

// EnrichedTools 返回当前工具集中每个工具的说明（未生成的 Enriched=false）以及服务简介。
func (s *Service) EnrichedTools(ctx context.Context) ([]EnrichedTool, map[string]string, error) {
	s.mu.RLock()
	source := s.toolSource
	s.mu.RUnlock()
	if source == nil {
		return []EnrichedTool{}, map[string]string{}, nil
	}
	discoveries, err := source(ctx)
	if err != nil {
		return nil, nil, err
	}
	out := []EnrichedTool{}
	services := map[string]string{}
	seen := map[string]struct{}{}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, d := range discoveries {
		label := serviceLabel(d)
		if item, ok := s.byService[strings.ToLower(label)]; ok {
			services[label] = item.Summary
		}
		hash := ToolHash(d.Tool.OriginalName, d.Tool.Description)
		if _, dup := seen[hash]; dup {
			continue
		}
		seen[hash] = struct{}{}
		row := EnrichedTool{Name: d.Tool.Name, Service: label, Keywords: []string{}}
		if item, ok := s.byHash[hash]; ok {
			row.Enriched, row.Summary, row.Keywords, row.UseWhen, row.Related = true, item.Summary, item.Keywords, item.UseWhen, item.Related
		}
		out = append(out, row)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Service != out[j].Service {
			return out[i].Service < out[j].Service
		}
		return out[i].Name < out[j].Name
	})
	return out, services, nil
}

func cleanKeywords(values []string) []string {
	out := []string{}
	seen := map[string]struct{}{}
	for _, v := range values {
		v = strings.TrimSpace(v)
		key := strings.ToLower(v)
		if v == "" || len([]rune(v)) > 40 {
			continue
		}
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, v)
		if len(out) >= 24 {
			break
		}
	}
	return out
}

// schemaParams 提取参数名（必填的加 * 号），帮助模型理解工具用途。
func schemaParams(schema json.RawMessage) []string {
	var parsed struct {
		Properties map[string]json.RawMessage `json:"properties"`
		Required   []string                   `json:"required"`
	}
	if len(schema) == 0 || json.Unmarshal(schema, &parsed) != nil {
		return nil
	}
	required := map[string]struct{}{}
	for _, name := range parsed.Required {
		required[name] = struct{}{}
	}
	names := make([]string, 0, len(parsed.Properties))
	for name := range parsed.Properties {
		names = append(names, name)
	}
	sort.Strings(names)
	out := []string{}
	for _, name := range names {
		if len(out) >= maxParams {
			break
		}
		if _, ok := required[name]; ok {
			name += "*"
		}
		out = append(out, name)
	}
	return out
}
