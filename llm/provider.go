// Package llm 提供 LLM API 提供者的抽象接口和多种实现。
// 支持 OpenAI 兼容、Ollama、Azure、自定义端点四种模式。
package llm

import (
	"context"
	"net/http"
	"net/url"
	"time"
)

// EndpointMode 端点模式枚举
const (
	EndpointChatCompletions = "chat_completions" // /v1/chat/completions (默认)
	EndpointResponses       = "responses"        // /v1/responses (OpenAI 新格式)
)

// ChatRequest 聊天请求参数
type ChatRequest struct {
	Model     string  `json:"model"`               // 模型名称
	Message   string  `json:"message"`             // 用户消息内容
	MaxTokens int     `json:"max_tokens"`           // 最大输出 token 数
	Temperature float64 `json:"temperature"`        // 温度参数 (0-2)
}

// ChatResponse 聊天响应结果
type ChatResponse struct {
	Success        bool   `json:"success"`                   // 是否成功
	Content        string `json:"content"`                   // 回复内容
	FinishReason   string `json:"finish_reason"`             // 结束原因: stop / length
	LatencyMs      float64 `json:"latency_ms"`               // 延迟（毫秒）
	PromptTokens   int    `json:"prompt_tokens"`             // 输入 token 数
	CompletionToks int    `json:"completion_tokens"`         // 输出 token 数
	TotalTokens    int    `json:"total_tokens"`              // 总 token 数
	Error          string `json:"error,omitempty"`           // 错误信息
	StatusCode     int    `json:"status_code,omitempty"`     // HTTP 状态码
}

// ConnectionResult 连接测试结果
type ConnectionResult struct {
	Success bool     `json:"success"`          // 是否成功
	Message string   `json:"message"`          // 结果描述
	Models  []string `json:"models,omitempty"` // 可用的模型列表
}

// Provider 定义了 LLM API 提供者的统一接口
type Provider interface {
	// TestConnection 测试与 API 的连接是否正常
	TestConnection(ctx context.Context) *ConnectionResult

	// Chat 发送聊天消息并获取回复
	Chat(ctx context.Context, req *ChatRequest) *ChatResponse

	// ListModels 获取可用的模型列表
	ListModels(ctx context.Context) ([]string, error)
}

// Config 是 Provider 的配置参数（与 storage.Config 对应）
type Config struct {
	APIType      string  // API 类型: openai / ollama / azure / custom
	BaseURL      string  // API 基础地址
	APIKey       string  // API Key
	Model        string  // 模型名称
	CustomPath   string  // 自定义路径（custom 类型使用）
	EndpointMode string  // 端点模式
	HTTPReferer  string  // HTTP-Referer 头
	XTitle       string  // X-Title 头
	Temperature  float64 // 温度参数
	MaxTokens    int     // 最大输出 token 数
	Timeout      int     // HTTP 请求超时（秒），0 表示使用默认值 60s
	ProxyURL     string  // HTTP 代理地址，如 http://127.0.0.1:7890
}

// NewProvider 根据配置创建对应的 Provider 实例
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
		// openai / azure 使用 OpenAIProvider
		return &OpenAIProvider{config: cfg}
	}
}

// defaultTimeout 默认 HTTP 请求超时
const defaultTimeout = 60 * time.Second

// userAgent 默认 User-Agent
const userAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0 Safari/537.36"

// GetTimeout 返回配置的有效超时时间，0 表示使用默认值
func (c *Config) GetTimeout() time.Duration {
	if c == nil || c.Timeout <= 0 {
		return defaultTimeout
	}
	return time.Duration(c.Timeout) * time.Second
}

// GetHTTPClient 返回配置的 HTTP 客户端（支持代理和超时）
func (c *Config) GetHTTPClient() *http.Client {
	if c == nil {
		return &http.Client{Timeout: defaultTimeout}
	}

	transport := &http.Transport{
		// 从环境变量读取代理作为兜底
		Proxy: http.ProxyFromEnvironment,
	}

	// 如果配置了代理，覆盖环境变量
	if c.ProxyURL != "" {
		if proxyURL, err := url.Parse(c.ProxyURL); err == nil {
			transport.Proxy = http.ProxyURL(proxyURL)
		}
	}

	return &http.Client{
		Timeout:   c.GetTimeout(),
		Transport: transport,
	}
}