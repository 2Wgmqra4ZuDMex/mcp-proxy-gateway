package aggregation

import (
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/myGithub/mcp-proxy-gateway/internal/domain"
)

// 账号被拒的类型：上游明确拒绝了这次请求（未执行），换同名工具的其他账号重试是安全的。
const (
	RejectionRateLimited    = "rate_limited"
	RejectionQuotaExhausted = "quota_exhausted"
	RejectionUnauthorized   = "unauthorized"
)

const (
	rateLimitCooldown = 60 * time.Second
	longCooldown      = 10 * time.Minute
	// 只检查较短的结果：正常的搜索/抓取结果可能很长且恰好提到 "rate limit" 等词。
	maxRejectionScanRunes = 4000
	maxAccountFailovers   = 5
)

// AccountRejection 描述一次被上游账号拒绝的调用。
type AccountRejection struct {
	Kind     string
	Status   int
	Message  string
	Cooldown time.Duration
}

var (
	quotaPattern = regexp.MustCompile(`(?i)quota|insufficient[_ ](credit|balance|fund)|out of credits|no (remaining )?credits|credits? (exhausted|exceeded|depleted)|usage limit|plan limit|monthly limit|limit exceeded for (your|this) (plan|account)|payment required|upgrade your plan|exceeded your (current )?(plan|quota|limit)`)
	ratePattern  = regexp.MustCompile(`(?i)rate[ _-]?limit|too many requests|excessive requests|throttl|request limit reached`)
	authPattern  = regexp.MustCompile(`(?i)invalid[ _-]?api[ _-]?key|api[ _-]?key[^.\n]{0,30}(invalid|missing|required|not valid|expired|revoked|incorrect)|(invalid|incorrect|missing|expired|revoked)[^.\n]{0,20}api[ _-]?key|unauthori[sz]ed[^.\n]{0,30}(api[ _-]?key|token|credential)|authentication (failed|required)|invalid (access )?token|invalid credentials`)
	// 传输层错误信息中的 HTTP 状态，例如 "status 429"、"HTTP 401"、"429 Too Many Requests"。
	errStatusPattern = regexp.MustCompile(`(?i)(?:status(?: code)?|http)[^0-9]{0,4}(4\d\d)\b|\b(4\d\d) (too many requests|unauthorized|payment required|forbidden)`)
)

// DetectAccountRejection 判断一次调用是否被上游账号拒绝（限流、额度用完、Key 无效）。
//
// 覆盖两种上报方式：MCP 错误结果（isError），以及把错误当作普通文本结果返回的上游
// （例如 tavily 返回 {"error":"Search failed","status":429,...} 且 isError=false）。
// 抓取目标网站返回的 401/403 不属于账号问题，因此认证类只认 API Key / 认证相关措辞。
func DetectAccountRejection(result domain.ToolResult, callErr error) (AccountRejection, bool) {
	if callErr != nil {
		msg := callErr.Error()
		if apiErr, ok := errors.AsType[*domain.APIError](callErr); ok && apiErr.Code == domain.CodeRateLimited {
			// 网关自身的额度限制在选择来源时处理，不是上游拒绝。
			return AccountRejection{}, false
		}
		status := 0
		if m := errStatusPattern.FindStringSubmatch(msg); m != nil {
			status = atoiStatus(m[1] + m[2])
		}
		return classifyRejection(status, msg, true)
	}

	text := resultText(result.Content)
	if text == "" || utf8.RuneCountInString(text) > maxRejectionScanRunes {
		return AccountRejection{}, false
	}
	status, looksLikeError := errorPayload(text)
	if !result.IsError && !looksLikeError {
		return AccountRejection{}, false
	}
	return classifyRejection(status, text, false)
}

// classifyRejection 按状态码与措辞分类。fromTransport 表示错误来自与上游 MCP 本身的连接，
// 此时裸 401/403 可信；工具结果里的 401/403 可能是抓取目标网站返回的，必须带 Key/认证措辞。
func classifyRejection(status int, text string, fromTransport bool) (AccountRejection, bool) {
	msg := strings.TrimSpace(text)
	if r := []rune(msg); len(r) > 300 {
		msg = string(r[:300]) + "…"
	}
	switch {
	case status == 402 || status == 432 || status == 433 || quotaPattern.MatchString(text):
		return AccountRejection{Kind: RejectionQuotaExhausted, Status: status, Message: msg, Cooldown: longCooldown}, true
	case status == 429 || ratePattern.MatchString(text):
		return AccountRejection{Kind: RejectionRateLimited, Status: status, Message: msg, Cooldown: rateLimitCooldown}, true
	case authPattern.MatchString(text) || (fromTransport && (status == 401 || status == 403)):
		return AccountRejection{Kind: RejectionUnauthorized, Status: status, Message: msg, Cooldown: longCooldown}, true
	}
	return AccountRejection{}, false
}

// resultText 拼接 MCP content 中的文本项。
func resultText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var items []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &items); err != nil {
		return ""
	}
	parts := make([]string, 0, len(items))
	for _, item := range items {
		if item.Type == "text" && item.Text != "" {
			parts = append(parts, item.Text)
		}
	}
	return strings.TrimSpace(strings.Join(parts, "\n"))
}

// errorPayload 判断非 isError 的文本结果是否其实是一个错误对象，并取出其中的 HTTP 状态。
func errorPayload(text string) (int, bool) {
	trimmed := strings.TrimSpace(text)
	if strings.HasPrefix(trimmed, "{") {
		var obj map[string]json.RawMessage
		if json.Unmarshal([]byte(trimmed), &obj) == nil {
			status := 0
			for _, key := range []string{"status", "statusCode", "status_code", "code"} {
				var n int
				if raw, ok := obj[key]; ok && json.Unmarshal(raw, &n) == nil && n >= 400 && n < 600 {
					status = n
					break
				}
			}
			_, hasError := obj["error"]
			return status, hasError || status != 0
		}
	}
	lower := strings.ToLower(trimmed)
	if strings.HasPrefix(lower, "error") || strings.HasPrefix(lower, "failed") {
		status := 0
		if m := errStatusPattern.FindStringSubmatch(trimmed); m != nil {
			status = atoiStatus(m[1] + m[2])
		}
		return status, true
	}
	return 0, false
}

func atoiStatus(s string) int {
	n := 0
	for _, ch := range s {
		if ch < '0' || ch > '9' {
			return 0
		}
		n = n*10 + int(ch-'0')
	}
	return n
}

type accountLimitState struct {
	Until   time.Time
	Kind    string
	Message string
}

// markAccountLimited 让被拒的账号在冷却期内优先被跳过。按上游（账号）记录，
// 因为限流与额度通常作用于整个账号的所有工具。
func (s *Service) markAccountLimited(upstreamID string, r AccountRejection) {
	s.accountLimitMu.Lock()
	defer s.accountLimitMu.Unlock()
	if s.accountLimits == nil {
		s.accountLimits = make(map[string]accountLimitState)
	}
	s.accountLimits[upstreamID] = accountLimitState{Until: s.currentTime().Add(r.Cooldown), Kind: r.Kind, Message: r.Message}
}

func (s *Service) accountLimited(upstreamID string) bool {
	s.accountLimitMu.Lock()
	defer s.accountLimitMu.Unlock()
	state, ok := s.accountLimits[upstreamID]
	if !ok {
		return false
	}
	if !state.Until.After(s.currentTime()) {
		delete(s.accountLimits, upstreamID)
		return false
	}
	return true
}

// AccountLimitView 为管理台展示的账号冷却状态。
type AccountLimitView struct {
	UpstreamID string    `json:"upstreamId"`
	Kind       string    `json:"kind"`
	Message    string    `json:"message"`
	Until      time.Time `json:"until"`
}

// AccountLimits 返回仍在冷却中的账号。
func (s *Service) AccountLimits() []AccountLimitView {
	s.accountLimitMu.Lock()
	defer s.accountLimitMu.Unlock()
	now := s.currentTime()
	out := make([]AccountLimitView, 0, len(s.accountLimits))
	for id, state := range s.accountLimits {
		if state.Until.After(now) {
			out = append(out, AccountLimitView{UpstreamID: id, Kind: state.Kind, Message: state.Message, Until: state.Until})
		}
	}
	return out
}

// candidatesForAttempt 去掉本次调用已尝试过的来源；还有未冷却的来源时，暂时跳过冷却中的账号。
func (s *Service) candidatesForAttempt(entry ReverseEntry, tried map[string]struct{}) (ReverseEntry, bool) {
	fresh := make([]ToolCandidate, 0, len(entry.Candidates))
	for _, c := range entry.Candidates {
		if _, done := tried[sourceFailureKey(c)]; !done {
			fresh = append(fresh, c)
		}
	}
	available := make([]ToolCandidate, 0, len(fresh))
	compatibleAvailable := false
	for _, c := range fresh {
		if !s.accountLimited(c.UpstreamID) {
			available = append(available, c)
			compatibleAvailable = compatibleAvailable || c.Compatible
		}
	}
	out := entry
	if compatibleAvailable {
		out.Candidates = available
	} else {
		out.Candidates = fresh
	}
	hasCompatible := false
	for _, c := range out.Candidates {
		hasCompatible = hasCompatible || c.Compatible
	}
	return out, hasCompatible
}
