package aggregation

import (
	"context"
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
	// Level 为连续被拒的次数（由 markAccountLimited 填写）。
	Level    int
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

// AccountEventRecorder 持久化账号异常记录（实现应快速返回或自行异步）。
type AccountEventRecorder interface {
	Insert(ctx context.Context, ev domain.AccountEvent) error
}

// SetAccountEventRecorder 注入账号异常记录器，仅在装配阶段调用。
func (s *Service) SetAccountEventRecorder(r AccountEventRecorder) *Service {
	s.accountEvents = r
	return s
}

// recordAccountEvent 在后台写入一条账号异常记录，失败只记日志，不影响调用。
func (s *Service) recordAccountEvent(ctx context.Context, c ToolCandidate, r AccountRejection) {
	if s.accountEvents == nil {
		return
	}
	ev := domain.AccountEvent{UpstreamID: c.UpstreamID, UpstreamName: c.UpstreamName, Kind: r.Kind, HTTPStatus: r.Status,
		Message: r.Message, Tool: c.OriginalName, Source: "call", OccurredAt: s.currentTime().UTC(),
		CooldownS: int(r.Cooldown / time.Second), Level: r.Level}
	recorder := s.accountEvents
	base := context.WithoutCancel(ctx)
	go func() {
		writeCtx, cancel := context.WithTimeout(base, 5*time.Second)
		defer cancel()
		if err := recorder.Insert(writeCtx, ev); err != nil {
			s.logger().Warn("写入账号异常记录失败", "upstreamID", ev.UpstreamID, "error", err)
		}
	}()
}

// 连续被拒时逐级延长的暂停时间：限流从 1 分钟起，额度不足 / Key 无效从 10 分钟起，最长 24 小时。
// 账号成功调用一次即回到第一级；暂停结束后超过 24 小时没有再被拒也回到第一级。
var (
	rateLimitBackoff = []time.Duration{time.Minute, 5 * time.Minute, 30 * time.Minute, 2 * time.Hour, 6 * time.Hour, 24 * time.Hour}
	longBackoff      = []time.Duration{10 * time.Minute, time.Hour, 6 * time.Hour, 24 * time.Hour}
	backoffResetIdle = 24 * time.Hour
	maxRetryAfter    = 24 * time.Hour
	retryAfterInText = regexp.MustCompile(`(?i)retry[-_ ]?after["'\s:=]*(\d{1,6})`)
)

func backoffSteps(kind string) []time.Duration {
	if kind == RejectionRateLimited {
		return rateLimitBackoff
	}
	return longBackoff
}

// retryAfterFromMessage 从报错文本中读取"多少秒后重试"（Retry-After / retry_after）。
func retryAfterFromMessage(msg string) time.Duration {
	m := retryAfterInText.FindStringSubmatch(msg)
	if m == nil {
		return 0
	}
	n := atoiStatus(m[1])
	if n <= 0 {
		return 0
	}
	return min(time.Duration(n)*time.Second, maxRetryAfter)
}

type accountLimitState struct {
	Until   time.Time
	Kind    string
	Message string
	// Level 为连续被拒的次数（从 1 开始），决定暂停时长。
	Level int
}

// markAccountLimited 让被拒的账号暂停参与轮询。按上游（账号）记录，因为限流与额度通常作用于
// 整个账号的所有工具。连续被拒时暂停时间逐级延长；上游给出 Retry-After 时按它暂停。
// 返回填好实际暂停时长与级别的 rejection。
func (s *Service) markAccountLimited(upstreamID string, r AccountRejection) AccountRejection {
	s.accountLimitMu.Lock()
	defer s.accountLimitMu.Unlock()
	if s.accountLimits == nil {
		s.accountLimits = make(map[string]accountLimitState)
	}
	now := s.currentTime()
	level := 1
	if prev, ok := s.accountLimits[upstreamID]; ok && now.Sub(prev.Until) < backoffResetIdle {
		level = prev.Level + 1
	}
	steps := backoffSteps(r.Kind)
	cooldown := steps[min(level, len(steps))-1]
	if retry := retryAfterFromMessage(r.Message); retry > 0 {
		cooldown = retry
	}
	r.Cooldown, r.Level = cooldown, level
	s.accountLimits[upstreamID] = accountLimitState{Until: now.Add(cooldown), Kind: r.Kind, Message: r.Message, Level: level}
	return r
}

// markAccountHealthy 在账号成功调用后清除暂停记录，下次被拒从第一级开始。
func (s *Service) markAccountHealthy(upstreamID string) {
	s.accountLimitMu.Lock()
	defer s.accountLimitMu.Unlock()
	delete(s.accountLimits, upstreamID)
}

func (s *Service) accountLimited(upstreamID string) bool {
	s.accountLimitMu.Lock()
	defer s.accountLimitMu.Unlock()
	state, ok := s.accountLimits[upstreamID]
	if !ok {
		return false
	}
	// 暂停结束后保留记录以便继续升级；长期没有再被拒时清理。
	if s.currentTime().Sub(state.Until) >= backoffResetIdle {
		delete(s.accountLimits, upstreamID)
		return false
	}
	return state.Until.After(s.currentTime())
}

// ClearAccountLimit 立即结束账号冷却；upstreamID 为空时结束全部。
func (s *Service) ClearAccountLimit(upstreamID string) {
	s.accountLimitMu.Lock()
	defer s.accountLimitMu.Unlock()
	if upstreamID == "" {
		s.accountLimits = nil
		return
	}
	delete(s.accountLimits, upstreamID)
}

// AccountLimitView 为管理台展示的账号冷却状态。
type AccountLimitView struct {
	UpstreamID string    `json:"upstreamId"`
	Kind       string    `json:"kind"`
	Message    string    `json:"message"`
	Until      time.Time `json:"until"`
	Level      int       `json:"level"`
}

// AccountLimits 返回仍在冷却中的账号。
func (s *Service) AccountLimits() []AccountLimitView {
	s.accountLimitMu.Lock()
	defer s.accountLimitMu.Unlock()
	now := s.currentTime()
	out := make([]AccountLimitView, 0, len(s.accountLimits))
	for id, state := range s.accountLimits {
		if state.Until.After(now) {
			out = append(out, AccountLimitView{UpstreamID: id, Kind: state.Kind, Message: state.Message, Until: state.Until, Level: state.Level})
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
		// 全部在冷却：先试最早恢复的账号（通常是只限流 1 分钟的），Key 无效等长冷却的账号排在后面。
		out.Candidates = s.earliestRecovering(fresh)
	}
	hasCompatible := false
	for _, c := range out.Candidates {
		hasCompatible = hasCompatible || c.Compatible
	}
	return out, hasCompatible
}

// earliestRecovering 只保留冷却结束时间最早的兼容来源（同一时刻的并列保留）；
// 没有兼容来源时原样返回。后续尝试会去掉已试过的来源，再轮到其余账号。
func (s *Service) earliestRecovering(candidates []ToolCandidate) []ToolCandidate {
	s.accountLimitMu.Lock()
	defer s.accountLimitMu.Unlock()
	var earliest time.Time
	for _, c := range candidates {
		if !c.Compatible {
			continue
		}
		until := s.accountLimits[c.UpstreamID].Until
		if earliest.IsZero() || until.Before(earliest) {
			earliest = until
		}
	}
	if earliest.IsZero() {
		return candidates
	}
	out := make([]ToolCandidate, 0, len(candidates))
	for _, c := range candidates {
		if c.Compatible && !s.accountLimits[c.UpstreamID].Until.After(earliest) {
			out = append(out, c)
		}
	}
	return out
}
