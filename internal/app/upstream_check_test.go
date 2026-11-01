package app

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/myGithub/mcp-proxy-gateway/internal/domain"
)

func checkTools() []domain.ToolDef {
	return []domain.ToolDef{{Name: "search", OriginalName: "search"}, {Name: "crawl", OriginalName: "crawl"}}
}

func TestCheckConnectOnly(t *testing.T) {
	sess := &fakeToolSession{tools: checkTools()}
	res, err := upstreamTester{factory: fakeFactory{sess: sess}}.Check(context.Background(), "u1", domain.UpstreamConfig{Name: "tavily01"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Connect.OK || res.Connect.Count != 2 || res.Call != nil || res.Name != "tavily01" {
		t.Fatalf("仅连接检测结果不符：%+v", res)
	}
	if !sess.closed {
		t.Fatal("检测结束应关闭临时会话")
	}
}

func TestCheckProbeSuccess(t *testing.T) {
	sess := &fakeToolSession{tools: checkTools(), callResult: domain.ToolResult{Content: json.RawMessage(`[{"type":"text","text":"result ok"}]`)}}
	res, _ := upstreamTester{factory: fakeFactory{sess: sess}}.Check(context.Background(), "u1", domain.UpstreamConfig{}, &domain.UpstreamCheckProbe{Tool: "search", Args: json.RawMessage(`{"query":"x"}`)})
	if res.Call == nil || !res.Call.OK || res.Call.Preview != "result ok" {
		t.Fatalf("调用成功时应 OK 并带摘要：%+v", res.Call)
	}
}

func TestCheckProbeUpstreamErrorMeansInvalid(t *testing.T) {
	sess := &fakeToolSession{tools: checkTools(), callResult: domain.ToolResult{IsError: true, Content: json.RawMessage(`[{"type":"text","text":"401 Unauthorized: invalid api key"}]`)}}
	res, _ := upstreamTester{factory: fakeFactory{sess: sess}}.Check(context.Background(), "u1", domain.UpstreamConfig{}, &domain.UpstreamCheckProbe{Tool: "search"})
	if res.Call == nil || res.Call.OK || !strings.Contains(res.Call.Message, "401") {
		t.Fatalf("上游返回错误结果时应判定失败并给出原因：%+v", res.Call)
	}
}

func TestCheckProbeMissingTool(t *testing.T) {
	sess := &fakeToolSession{tools: checkTools()}
	res, _ := upstreamTester{factory: fakeFactory{sess: sess}}.Check(context.Background(), "u1", domain.UpstreamConfig{}, &domain.UpstreamCheckProbe{Tool: "nope"})
	if res.Call == nil || res.Call.OK || res.Call.Message == "" {
		t.Fatalf("工具不存在时应提示：%+v", res.Call)
	}
}

func TestCheckConnectFailureSkipsProbe(t *testing.T) {
	sess := &fakeToolSession{connectErr: errors.New("dial failed")}
	res, _ := upstreamTester{factory: fakeFactory{sess: sess}}.Check(context.Background(), "u1", domain.UpstreamConfig{}, &domain.UpstreamCheckProbe{Tool: "search"})
	if res.Connect.OK || res.Connect.Stage != "connect" || res.Call != nil {
		t.Fatalf("连接失败时不应继续调用：%+v", res)
	}
}

func TestContentPreviewTruncates(t *testing.T) {
	long := strings.Repeat("字", checkPreviewRunes+50)
	raw, _ := json.Marshal([]map[string]string{{"type": "text", "text": long}})
	got := contentPreview(raw)
	if len([]rune(got)) != checkPreviewRunes+1 || !strings.HasSuffix(got, "…") {
		t.Fatalf("摘要应截断到 %d 字并加省略号，got %d", checkPreviewRunes, len([]rune(got)))
	}
}
