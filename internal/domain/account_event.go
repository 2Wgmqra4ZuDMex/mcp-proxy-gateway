package domain

import "time"

// 账号异常类型：上游明确拒绝了请求（未执行）。
const (
	AccountEventRateLimited    = "rate_limited"
	AccountEventQuotaExhausted = "quota_exhausted"
	AccountEventUnauthorized   = "unauthorized"
)

// AccountEvent 记录一次上游账号被拒（限流、额度不足、Key 无效或无权限）。
// Source 为 "call"（实际调用）或 "check"（MCP 检测）。
type AccountEvent struct {
	ID           int64     `json:"id"`
	UpstreamID   string    `json:"upstreamId"`
	UpstreamName string    `json:"upstreamName"`
	Kind         string    `json:"kind"`
	HTTPStatus   int       `json:"httpStatus,omitempty"`
	Message      string    `json:"message"`
	Tool         string    `json:"tool,omitempty"`
	Source       string    `json:"source"`
	OccurredAt   time.Time `json:"occurredAt"`
}
