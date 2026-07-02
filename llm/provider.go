// Package llm 提供 LLM API 提供者的抽象接口和多种实现。
// 支持 OpenAI 兼容、Ollama、Azure、自定义端点四种模式。
//
// 设计原则：
// 1. 所有 Provider 共用请求体构建函数（BuildChatBody），避免每个 Provider 手写 body
// 2. 统一错误处理通过 errors 包分类，避免各 Provider 自行实现错误映射
// 3. stream=false 由共享函数统一处理，防止部分 API 默认返回 SSE 流式导致超时
package llm

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/qwdingyu/llm-tester/errors"
)

// ─── 常量 ────────────────────────────────────────────

// EndpointMode 端点模式枚举
const (
	// EndpointChatCompletions 标准 OpenAI 聊天补全接口（默认）
	// 路径: /v1/chat/completions，绝大多数 API 使用此格式
	EndpointChatCompletions = "chat_completions"
	// EndpointResponses OpenAI 新格式（2024+）
	// 路径: /v1/responses，仅部分较新 API 支持
	EndpointResponses = "responses"
)

// ─── 请求/响应数据结构 ──────────────────────────────

// Message 聊天消息（用于多轮对话）
type Message struct {
	Role    string `json:"role"`    // user / assistant / system
	Content string `json:"content"` // 消息内容
}

// ChatRequest 聊天请求参数
//
// 两种消息模式:
// 1. Message != "" → 单条用户消息（兼容现有调用）
// 2. Messages != nil → 多条消息构成的对话历史（覆盖 Message）
//
// ResponseFormat 用于 JSON 模式测试: "json_object" / ""(默认text)
type ChatRequest struct {
	Model          string    `json:"model"`                     // 模型名称
	Message        string    `json:"message,omitempty"`         // 单条用户消息
	Messages       []Message `json:"messages,omitempty"`        // 多轮消息历史
	MaxTokens      int       `json:"max_tokens"`                // 最大输出 token 数
	Temperature    float64   `json:"temperature"`               // 温度参数
	ResponseFormat string    `json:"response_format,omitempty"` // 响应格式: json_object
}

// ChatResponse 聊天响应结果
// LatencyMs 不包含网络传输的精确时间，仅为客户端侧的总耗时
type ChatResponse struct {
	Success        bool    `json:"success"`                   // 是否成功
	Content        string  `json:"content"`                   // 回复内容
	FinishReason   string  `json:"finish_reason"`             // 结束原因: stop(正常) / length(截断)
	LatencyMs      float64 `json:"latency_ms"`               // 延迟（毫秒）
	PromptTokens   int     `json:"prompt_tokens"`             // 输入 token 数
	CompletionToks int     `json:"completion_tokens"`         // 输出 token 数
	TotalTokens    int     `json:"total_tokens"`              // 总 token 数
	Error          string  `json:"error,omitempty"`           // 错误信息
	StatusCode     int     `json:"status_code,omitempty"`     // HTTP 状态码
}

// ConnectionResult 连接测试结果
// Success=false 时仅通过 Message 字段返回错误描述，不单独定义 Error 字段
// 这是与 ChatResponse 的一个设计差异：连接测试的错误是最终状态，无需后续处理
type ConnectionResult struct {
	Success bool     `json:"success"`          // 是否成功
	Message string   `json:"message"`          // 结果描述（成功/失败原因）
	Models  []string `json:"models,omitempty"` // 可用的模型列表（仅成功时非空）
}

// ─── Provider 接口 ──────────────────────────────────

// Provider 定义了 LLM API 提供者的统一接口
// 所有 Provider 实现必须支持此接口，以便上层代码（Gin handler）统一调用
// 目前有 3 个实现: OpenAIProvider / OllamaProvider / CustomProvider
type Provider interface {
	// TestConnection 测试与 API 的连接是否正常
	// 返回 ConnectionResult，成功时包含可用模型列表
	TestConnection(ctx context.Context) *ConnectionResult

	// Chat 发送聊天消息并获取回复
	Chat(ctx context.Context, req *ChatRequest) *ChatResponse

	// ListModels 获取可用的模型列表
	// 与 TestConnection 底层逻辑相同，但只返回模型名，不关心连接详情
	ListModels(ctx context.Context) ([]string, error)
}

// ─── 配置 ────────────────────────────────────────────

// Config 是 Provider 的配置参数
// 与 storage.Config 字段一一对应，但独立定义以避免循环依赖
// （storage 引入 llm 会形成循环: main → storage → llm → storage）
type Config struct {
	APIType      string  // API 类型: openai / ollama / azure / custom
	BaseURL      string  // API 基础地址，不应包含 /v1（代码自动添加）
	APIKey       string  // API Key
	Model        string  // 模型名称，如 gpt-4o-mini
	CustomPath   string  // 自定义路径（custom 类型使用）
	EndpointMode string  // 端点模式: chat_completions(默认) / responses
	HTTPReferer  string  // HTTP-Referer 头，部分 API（如 DeepSeek）需要
	XTitle       string  // X-Title 头，部分 API 用于标识应用
	Temperature  float64 // 温度参数 0-2，默认 0.7
	MaxTokens    int     // 最大输出 token 数
	Timeout      int     // HTTP 请求超时（秒），0 表示使用默认值 60s
	ProxyURL     string  // HTTP 代理地址，如 http://127.0.0.1:7890
}

// NewProvider 根据配置创建对应的 Provider 实例
// 策略:
//   - ollama → OllamaProvider（/api/chat 协议）
//   - custom → CustomProvider（自定义端点）
//   - openai / azure / 其他 → OpenAIProvider（标准 OpenAI 兼容协议）
//
// 注意: 配置为空时返回 nil，调用方需自行处理 nil 检查
func NewProvider(cfg *Config) Provider {
	if cfg == nil {
		return nil
	}
	switch cfg.APIType {
	case "ollama":
		return &OllamaProvider{config: cfg}
	case "custom":
		return &CustomProvider{config: cfg}
	default:
		// openai / azure / 未识别类型全部走 OpenAIProvider
		// 因为绝大多数第三方 API 都兼容 OpenAI 的 /v1/chat/completions 格式
		return &OpenAIProvider{config: cfg}
	}
}

// ─── 默认值与工具函数 ──────────────────────────────

// defaultTimeout 默认 HTTP 请求超时
// 60 秒是 LLM API 调用的合理上限：
// - 短提示（<100 tokens）通常在 2-5 秒内
// - 长生成（>4K tokens）可能需要 30-60 秒
// 若需要更长的超时，用户应在配置中显式设置
const defaultTimeout = 60 * time.Second

// userAgent 默认 User-Agent
// 部分反爬系统会根据 UA 做差异化处理，使用浏览器 UA 可提高兼容性
const userAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0 Safari/537.36"

// GetTimeout 返回配置的有效超时时间
// 0 或负数表示未配置，返回默认值 60s
// 此设计允许前端传递 0 来表示"使用默认值"，简化前端逻辑
func (c *Config) GetTimeout() time.Duration {
	if c == nil || c.Timeout <= 0 {
		return defaultTimeout
	}
	return time.Duration(c.Timeout) * time.Second
}

// GetHTTPClient 返回配置的 HTTP 客户端（支持代理和超时）
//
// 代理解析优先级:
// 1. 配置中的 ProxyURL（最高优先级）
// 2. 环境变量 HTTP_PROXY / HTTPS_PROXY（兜底）
// 3. 直连（无代理）
//
// 注意: 每次调用都创建新的 http.Client（含新的 Transport），
// 但 Transport 的连接池会被 GC 回收。在 LLM Tester 的低并发场景下，
// 每次创建的开销（< 1μs）远小于 API 调用耗时（秒级），可以接受。
func (c *Config) GetHTTPClient() *http.Client {
	if c == nil {
		return &http.Client{Timeout: defaultTimeout}
	}

	transport := &http.Transport{
		// http.ProxyFromEnvironment 自动读取 HTTP_PROXY / HTTPS_PROXY / NO_PROXY
		// 这是 Go 标准库提供的代理解析函数，与 curl 的行为一致
		Proxy: http.ProxyFromEnvironment,
	}

	// 配置中的代理覆盖环境变量
	// 这样设计是为了让用户可以针对单个 API 配置不同的代理
	//（例如: OpenAI 直连，DeepSeek 走代理）
	if c.ProxyURL != "" {
		if proxyURL, err := url.Parse(c.ProxyURL); err == nil {
			transport.Proxy = http.ProxyURL(proxyURL)
		}
		// 忽略代理 URL 解析错误，此时使用环境变量代理作为兜底
		// 用户填写的代理地址格式错误时不应让整个请求失败
	}

	return &http.Client{
		Timeout:   c.GetTimeout(),
		Transport: transport,
	}
}

// ─── 共享请求体构建 ────────────────────────────────

// BuildChatBody 构造标准聊天请求体（所有 Provider 共用）
//
// 消息构建规则:
// - req.Messages 不为空时，直接使用 Messages 作为消息数组
// - req.Messages 为空时，以 [{"role":"user","content":req.Message}] 作为单条消息
// - ResponseFormat 不为空时添加 "response_format": {"type": "json_object"}
// - stream=false 防止 API 默认返回 SSE 流式响应，参见 docs/04_采坑记录.md → 坑005
func BuildChatBody(model string, req *ChatRequest) map[string]interface{} {
	body := map[string]interface{}{
		"model":       model,
		"messages":    buildMessages(req),
		"temperature": req.Temperature,
		"max_tokens":  req.MaxTokens,
		"stream":      false,
	}
	if req.ResponseFormat == "json_object" {
		body["response_format"] = map[string]string{"type": "json_object"}
	}
	return body
}

// buildMessages 构建 messages 数组，支持单条和多轮消息
func buildMessages(req *ChatRequest) interface{} {
	if len(req.Messages) > 0 {
		msgs := make([]map[string]string, len(req.Messages))
		for i, m := range req.Messages {
			msgs[i] = map[string]string{"role": m.Role, "content": m.Content}
		}
		return msgs
	}
	return []map[string]string{
		{"role": "user", "content": req.Message},
	}
}

// BuildTestBody 构造连接测试请求体
// 使用最小化请求（1 token + ping），减少不必要的 API 消耗
// 当模型名为空时使用 gpt-4o-mini 作为默认值，因为这是 OpenAI 最快速的模型之一
func BuildTestBody(model string) map[string]interface{} {
	if model == "" {
		model = "gpt-4o-mini"
	}
	return map[string]interface{}{
		"model": model,
		"messages": []map[string]string{
			{"role": "user", "content": "ping"},
		},
		"max_tokens": 1,
		"stream":     false,
	}
}

// SetCommonHeaders 设置通用请求头（所有 Provider 共用）
//
// 设置以下头信息:
// - User-Agent: 使用浏览器 UA 避免反爬
// - Accept: 仅接受 JSON 响应
// - HTTP-Referer / Referer: 部分 API（如 DeepSeek）需要验证来源
// - X-Title: 部分 API 用于标识调用方应用名称
//
// 注意：Content-Type 和 Authorization 不在本函数中设置，
// 因为不同 Provider 有不同的认证方式（Bearer token vs API-Key）
func SetCommonHeaders(cfg *Config, req *http.Request) {
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "application/json")

	// 以下两个头只有部分 API 需要，默认不发送以减少不必要的请求头
	if cfg.HTTPReferer != "" {
		req.Header.Set("HTTP-Referer", cfg.HTTPReferer)
		req.Header.Set("Referer", cfg.HTTPReferer)
	}
	if cfg.XTitle != "" {
		req.Header.Set("X-Title", cfg.XTitle)
	}
}

// ─── 统一响应读取 ──────────────────────────────────

// ReadBodyOK 读取 HTTP 响应体并统一处理错误分类
//
// 返回 (body, "") 表示成功，body 为完整的响应体字节
// 返回 (nil, errorMsg) 表示失败，errorMsg 为人类可读的错误描述
//
// 错误分类委托给 errors.NewAPIError，按以下顺序判断:
// 1. 网络层错误（DNS/连接/超时）
// 2. HTTP 状态码（401/403/429/5xx 等）
// 3. 未知错误
//
// 使用此函数替代手动 io.ReadAll 的好处:
// - 统一的 defer resp.Body.Close()，不再遗漏
// - 统一的错误分类和中文提示
// - 减少每个 Provider 实现中的重复代码
func ReadBodyOK(resp *http.Response, err error) ([]byte, string) {
	// 处理网络层错误（resp 为 nil 的情况）
	if err != nil {
		apiErr := errors.NewAPIError(0, nil, err)
		return nil, apiErr.Message + ": " + apiErr.Suggestion
	}
	defer resp.Body.Close()

	// 读取完整响应体
	body, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		return nil, fmt.Sprintf("读取响应失败: %v", readErr)
	}

	// 处理 HTTP 错误状态码
	if resp.StatusCode != http.StatusOK {
		apiErr := errors.NewAPIError(resp.StatusCode, body, nil)
		return nil, fmt.Sprintf("%s - %s", apiErr.Message, apiErr.Suggestion)
	}

	return body, ""
}

// trimURL 去除 URL 末尾的 /
// 用于拼接 URL 时避免重复斜杠
// 例: trimURL("http://localhost:11434/") + "/api/chat" → "http://localhost:11434/api/chat"
func trimURL(baseURL string) string {
	return strings.TrimRight(baseURL, "/")
}
