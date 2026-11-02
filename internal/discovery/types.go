// Package discovery 实现智能模式的 AI 辅助发现：
//   - 工具说明增强：用 OpenAI 兼容模型为每个工具生成中文简介、中英文关键词与适用场景，
//     并入关键词索引，提升中文与换说法查询的命中率；结果按工具指纹持久化，只生成一次。
//   - AI 挑选工具：search_tools 时让模型从候选中挑选最合适的工具并说明调用顺序；
//     失败或超时由调用方回退到关键词检索。
//   - 服务清单：为智能模式连接生成「有哪些服务、各自做什么」的概览。
//
// 模型接口复用 ai_provider 表中启用的 Provider。
package discovery

import "time"

const (
	KindTool    = "tool"
	KindService = "service"
)

// Enrichment 为 AI 为一个工具（或一个服务）生成的检索元数据。
//
// Hash 为工具指纹（原始名称 + 描述）或服务指纹（服务名 + 工具名集合），
// 描述变化即指纹变化，自动视为待生成。
type Enrichment struct {
	Hash      string    `json:"hash"`
	Kind      string    `json:"kind"`
	Name      string    `json:"name"`
	Summary   string    `json:"summary"`
	Keywords  []string  `json:"keywords"`
	UseWhen   string    `json:"useWhen,omitempty"`
	Related   []string  `json:"related,omitempty"`
	Model     string    `json:"model,omitempty"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// Candidate 为 AI 挑选工具时的一个候选（调用方按可见集合构造）。
type Candidate struct {
	Name         string
	OriginalName string
	Description  string
	Service      string
}

// RerankResult 为 AI 挑选结果：Names 按建议调用顺序排列，均来自候选集合。
type RerankResult struct {
	Names     []string `json:"names"`
	Note      string   `json:"note,omitempty"`
	LatencyMS int64    `json:"latencyMs"`
	Cached    bool     `json:"cached,omitempty"`
	// Model 为实际给出结果的模型；FallbackUsed 表示主模型失败后由备用模型给出；
	// Failures 为之前失败的模型及原因。
	Model        string   `json:"model,omitempty"`
	FallbackUsed bool     `json:"fallbackUsed,omitempty"`
	Failures     []string `json:"failures,omitempty"`
}
