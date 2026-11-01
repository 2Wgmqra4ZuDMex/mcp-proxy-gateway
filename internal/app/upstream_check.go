package app

import (
	"context"
	"encoding/json"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/myGithub/mcp-proxy-gateway/internal/domain"
)

const checkPreviewRunes = 300

// Check 用已保存上游的配置建立临时会话：检测连接与工具列表；probe 非空时再真实调用一次，
// 以确认凭证有效。临时会话独立于运行中的连接与轮询，结果精确对应这一个上游（账号）。
// 检测调用不计入调用统计与上游额度计数。
func (t upstreamTester) Check(ctx context.Context, id string, cfg domain.UpstreamConfig, probe *domain.UpstreamCheckProbe) (domain.UpstreamCheckResult, error) {
	result := domain.UpstreamCheckResult{UpstreamID: id, Name: cfg.Name}
	if t.factory == nil {
		return result, domain.NewError(domain.CodeInternal, "上游测试服务未就绪")
	}
	testCtx, cancel := upstreamTestContext(ctx)
	defer cancel()

	start := time.Now()
	sess, err := t.factory.NewSession(cfg)
	if err != nil {
		result.Connect = t.failedResult("config", start, err)
		return result, nil
	}
	defer func() { _ = sess.Close() }()
	if err := sess.Connect(testCtx); err != nil {
		result.Connect = t.failedResult("connect", start, err)
		return result, nil
	}
	tools, err := sess.ListTools(testCtx)
	if err != nil {
		result.Connect = t.failedResult("list_tools", start, err)
		return result, nil
	}
	result.Connect = domain.UpstreamTestResult{
		OK: true, Stage: "ok", DurationMS: time.Since(start).Milliseconds(), Count: len(tools), Tools: []domain.ToolDef{},
	}

	if probe == nil || strings.TrimSpace(probe.Tool) == "" {
		return result, nil
	}
	call := &domain.UpstreamCallCheck{Tool: probe.Tool}
	result.Call = call
	found := false
	for _, tool := range tools {
		if tool.OriginalName == probe.Tool || tool.Name == probe.Tool {
			found = true
			break
		}
	}
	if !found {
		call.Message = "该上游没有这个工具"
		return result, nil
	}
	args := probe.Args
	if len(args) == 0 {
		args = json.RawMessage(`{}`)
	}
	callStart := time.Now()
	res, err := sess.CallTool(testCtx, probe.Tool, args)
	call.LatencyMS = time.Since(callStart).Milliseconds()
	if err != nil {
		call.Message = upstreamTestErrorMessage(err)
		return result, nil
	}
	call.IsError = res.IsError
	call.OK = !res.IsError
	call.Preview = contentPreview(res.Content)
	if res.IsError {
		call.Message = call.Preview
	}
	return result, nil
}

// contentPreview 提取 MCP content 中的文本并截断，用于展示调用结果摘要。
func contentPreview(raw json.RawMessage) string {
	var items []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	text := ""
	if json.Unmarshal(raw, &items) == nil {
		parts := make([]string, 0, len(items))
		for _, item := range items {
			if item.Text != "" {
				parts = append(parts, item.Text)
			} else if item.Type != "" {
				parts = append(parts, "["+item.Type+"]")
			}
		}
		text = strings.Join(parts, "\n")
	} else {
		text = string(raw)
	}
	text = strings.TrimSpace(text)
	if utf8.RuneCountInString(text) > checkPreviewRunes {
		text = string([]rune(text)[:checkPreviewRunes]) + "…"
	}
	return text
}
