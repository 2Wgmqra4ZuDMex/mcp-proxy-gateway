package discovery

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/myGithub/mcp-proxy-gateway/internal/risk"
)

const maxResponseBytes = 2 << 20

// ProxyResolver 按出站代理 ID 解析代理地址；与上游共用同一套代理配置。
type ProxyResolver func(id string) (*url.URL, error)

// Usage 为一次模型调用的 token 用量；服务未返回时为 0。
type Usage struct {
	InputTokens  int64 `json:"inputTokens"`
	OutputTokens int64 `json:"outputTokens"`
}

type client struct {
	resolveProxy ProxyResolver
}

// RequestError 为模型接口返回的非 2xx 或网络错误，Message 已去除敏感信息。
type RequestError struct {
	StatusCode int
	Message    string
	TimedOut   bool
}

func (e *RequestError) Error() string {
	switch {
	case e.TimedOut:
		return "AI 接口请求超时"
	case e.StatusCode != 0 && e.Message != "":
		return fmt.Sprintf("AI 接口返回 HTTP %d：%s", e.StatusCode, e.Message)
	case e.StatusCode != 0:
		return fmt.Sprintf("AI 接口返回 HTTP %d", e.StatusCode)
	case e.Message != "":
		return "AI 接口请求失败：" + e.Message
	default:
		return "AI 接口请求失败"
	}
}

// complete 发送一次对话请求并返回模型文本。timeout<=0 时使用 Provider 的超时。
func (c *client) complete(ctx context.Context, p risk.Provider, system, user string, timeout time.Duration) (string, Usage, error) {
	endpoint, body, err := buildRequest(p, system, user)
	if err != nil {
		return "", Usage{}, err
	}
	httpClient, err := c.httpClient(p, timeout)
	if err != nil {
		return "", Usage{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return "", Usage{}, fmt.Errorf("创建 AI 请求失败：%w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if p.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+p.APIKey)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return "", Usage{}, classifyTransportError(ctx, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return "", Usage{}, classifyTransportError(ctx, err)
	}
	if len(raw) > maxResponseBytes {
		return "", Usage{}, errors.New("AI 接口响应超过 2 MiB 限制")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", Usage{}, &RequestError{StatusCode: resp.StatusCode, Message: errorMessage(raw)}
	}
	text, err := extractContent(p.APIStyle, raw)
	if err != nil {
		return "", Usage{}, err
	}
	return text, extractUsage(raw), nil
}

// listModels 读取 OpenAI 兼容接口的模型列表（GET {base}/models）。
func (c *client) listModels(ctx context.Context, p risk.Provider) ([]string, error) {
	base, err := url.Parse(strings.TrimSpace(p.BaseURL))
	if err != nil {
		return nil, fmt.Errorf("Base URL 无效")
	}
	base.Path = path.Join(base.Path, "models")
	httpClient, err := c.httpClient(p, 30*time.Second)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("创建请求失败：%w", err)
	}
	if p.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+p.APIKey)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, classifyTransportError(ctx, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return nil, classifyTransportError(ctx, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, &RequestError{StatusCode: resp.StatusCode, Message: errorMessage(raw)}
	}
	var body struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
		Models []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return nil, errors.New("模型列表格式无法识别")
	}
	seen := map[string]struct{}{}
	out := []string{}
	add := func(id string) {
		id = strings.TrimSpace(id)
		if id == "" {
			return
		}
		if _, dup := seen[id]; dup {
			return
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	for _, m := range body.Data {
		add(m.ID)
	}
	for _, m := range body.Models {
		if m.ID != "" {
			add(m.ID)
		} else {
			add(strings.TrimPrefix(m.Name, "models/"))
		}
	}
	sort.Strings(out)
	return out, nil
}

func (c *client) httpClient(p risk.Provider, timeout time.Duration) (*http.Client, error) {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	// 不读取进程环境变量代理：只走页面上选择的出站代理，未选择即直连。
	transport.Proxy = nil
	if strings.TrimSpace(p.ProxyID) != "" {
		if c.resolveProxy == nil {
			return nil, errors.New("出站代理服务未就绪")
		}
		proxy, err := c.resolveProxy(p.ProxyID)
		if err != nil {
			// 代理不可用时直接失败，不回退直连。
			return nil, fmt.Errorf("出站代理不可用：%w", err)
		}
		transport.Proxy = http.ProxyURL(proxy)
	}
	if timeout <= 0 {
		timeout = time.Duration(p.TimeoutS) * time.Second
	}
	base, _ := url.Parse(p.BaseURL)
	return &http.Client{
		Transport: transport,
		Timeout:   timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 3 || base == nil || !strings.EqualFold(req.URL.Hostname(), base.Hostname()) {
				return http.ErrUseLastResponse
			}
			return nil
		},
	}, nil
}

func buildRequest(p risk.Provider, system, user string) (string, []byte, error) {
	base, err := url.Parse(strings.TrimSpace(p.BaseURL))
	if err != nil {
		return "", nil, fmt.Errorf("Base URL 无效")
	}
	if p.APIStyle == risk.APIStyleResponses {
		base.Path = path.Join(base.Path, "responses")
		body, err := json.Marshal(map[string]any{"model": p.Model, "instructions": system, "input": user})
		return base.String(), body, err
	}
	base.Path = path.Join(base.Path, "chat/completions")
	body, err := json.Marshal(map[string]any{
		"model": p.Model, "temperature": 0, "stream": false,
		"messages": []map[string]string{{"role": "system", "content": system}, {"role": "user", "content": user}},
	})
	return base.String(), body, err
}

func extractContent(style risk.APIStyle, raw []byte) (string, error) {
	if style == risk.APIStyleResponses {
		var body struct {
			OutputText string `json:"output_text"`
			Output     []struct {
				Content []struct {
					Text string `json:"text"`
				} `json:"content"`
			} `json:"output"`
		}
		if err := json.Unmarshal(raw, &body); err != nil {
			return "", errors.New("解析 Responses 响应失败")
		}
		if body.OutputText != "" {
			return body.OutputText, nil
		}
		for _, out := range body.Output {
			for _, content := range out.Content {
				if content.Text != "" {
					return content.Text, nil
				}
			}
		}
		return "", errors.New("Responses 响应缺少文本结果")
	}
	var body struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return "", errors.New("解析 Chat Completions 响应失败")
	}
	if len(body.Choices) == 0 || strings.TrimSpace(body.Choices[0].Message.Content) == "" {
		return "", errors.New("Chat Completions 响应缺少结果")
	}
	return body.Choices[0].Message.Content, nil
}

func extractUsage(raw []byte) Usage {
	var body struct {
		Usage struct {
			InputTokens      int64 `json:"input_tokens"`
			OutputTokens     int64 `json:"output_tokens"`
			PromptTokens     int64 `json:"prompt_tokens"`
			CompletionTokens int64 `json:"completion_tokens"`
		} `json:"usage"`
	}
	if json.Unmarshal(raw, &body) != nil {
		return Usage{}
	}
	return Usage{
		InputTokens:  max(body.Usage.InputTokens, body.Usage.PromptTokens),
		OutputTokens: max(body.Usage.OutputTokens, body.Usage.CompletionTokens),
	}
}

// errorMessage 提取 OpenAI 风格错误信息，截断以免回显过长内容。
func errorMessage(raw []byte) string {
	var body struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
		Message string `json:"message"`
	}
	msg := ""
	if json.Unmarshal(raw, &body) == nil {
		msg = body.Error.Message
		if msg == "" {
			msg = body.Message
		}
	}
	msg = strings.TrimSpace(msg)
	if r := []rune(msg); len(r) > 200 {
		msg = string(r[:200]) + "…"
	}
	return msg
}

func classifyTransportError(ctx context.Context, err error) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		if errors.Is(ctxErr, context.DeadlineExceeded) {
			return &RequestError{TimedOut: true}
		}
		return ctxErr
	}
	var netErr net.Error
	if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &netErr) && netErr.Timeout()) {
		return &RequestError{TimedOut: true}
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		// url.Error 含完整请求地址，只保留底层原因。
		return &RequestError{Message: urlErr.Err.Error()}
	}
	return &RequestError{Message: err.Error()}
}

// decodeJSONObject 从模型回复中提取第一个 JSON 对象，兼容 ```json 代码块与前后说明文字。
func decodeJSONObject(text string, out any) error {
	start := strings.Index(text, "{")
	end := strings.LastIndex(text, "}")
	if start < 0 || end <= start {
		return errors.New("模型回复中没有 JSON 对象")
	}
	if err := json.Unmarshal([]byte(text[start:end+1]), out); err != nil {
		return fmt.Errorf("模型回复的 JSON 无法解析：%w", err)
	}
	return nil
}
